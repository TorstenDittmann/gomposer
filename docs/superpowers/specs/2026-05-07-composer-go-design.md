# composer-go: a fast Go-based PHP package manager

- **Date:** 2026-05-07
- **Status:** Approved (brainstorm)
- **Owner:** Torsten Dittmann

## Summary

`composer-go` is a Go reimplementation of the install/update path of Composer (PHP's package manager), focused on raw speed. It reads existing `composer.json` files unchanged, resolves dependencies against Packagist and arbitrary git VCS repositories, generates Composer-compatible autoloaders, and runs install scripts. It writes its own lockfile (`composer-go.lock`) rather than `composer.lock`, and it intentionally does not implement Composer's plugin system.

## Goals

- Install/update is meaningfully faster than Composer 2 on both warm and cold caches.
- Compatible consumer of existing `composer.json` (no manifest migration required).
- Real-world Laravel and Symfony skeleton projects install end-to-end and boot.
- Single static binary; no PHP required to install `composer-go` itself (PHP is required at install-time of the user's project for platform detection and PHP-callable scripts).

## Non-goals

- The Composer plugin system. Plugins listed in `require`/`require-dev` are detected and ignored with a warning.
- Writing `composer.lock`. The two tools coexist on `composer.json` but maintain separate lockfiles.
- Commands beyond `install` and `update` for the MVP. `require` / `remove` / `show` / `why` / `outdated` / `audit` / `dump-autoload` are all post-MVP.
- PSR-0 autoloader (warn-and-skip).
- `lib-*` platform constraints (rare and expensive to compute).
- Windows for the MVP. macOS and Linux first; Windows after stage 4 if there is demand.

## Architecture

```
┌────────────────────────────────────────────────────────────┐
│ CLI:  install | update                                     │
└────────────────────┬───────────────────────────────────────┘
                     │
              ┌──────▼───────┐
              │ Orchestrator  │  drives the install pipeline,
              │               │  owns concurrency + cancellation
              └──┬─┬──┬──┬──┬─┘
   ┌─────────────┘ │  │  │  └────────────┐
   │               │  │  │               │
┌──▼────┐  ┌───────▼─┐│  ▼          ┌────▼─────┐
│Manifest│  │Resolver ││ Fetcher    │Autoloader│
│ + Lock │  │(PubGrub)││ (HTTP+VCS) │Generator │
└──┬─────┘  └─┬───────┘│ └────┬─────┘└────┬────┘
   │          │        │      │            │
   │          │   ┌────▼──────▼──┐         │
   │          │   │ Package Store │         │
   │          │   │ (content-     │         │
   │          │   │  addressed,   │         │
   │          │   │  reflinked)   │         │
   │          │   └───────┬───────┘         │
   │          │           │                 │
   │       ┌──▼───────────▼─────────────────▼──┐
   │       │ Cache layer                        │
   │       │ (1) metadata HTTP cache w/ ETags  │
   │       │ (2) content-addressed package zips│
   │       │ (3) resolution-result cache       │
   │       │ (4) parsed-manifest cache         │
   │       └────────────────────────────────────┘
   │
   └──→ Scripts runner (shell + `php -r`)
```

- **Concurrency model:** `errgroup` with a bounded worker pool per phase (metadata fetch, package fetch, extract, autoloader scan). Cancellation is cooperative via `context.Context`.
- **State location:** caches at `$XDG_CACHE_HOME/composer-go/` (or `~/Library/Caches/composer-go/` on macOS). Credentials read from both `~/.composer/auth.json` and `~/.config/composer-go/auth.json`; the latter wins on conflict.
- **Filesystem strategy:** package store defaults to `<project>/.composer-go/store` so reflinks/clonefile can hardlink into `vendor/` on the same filesystem. Falls back to copy if the store is on a different filesystem from the project.

## Caching and optimistic operations

Four caching layers and two optimistic operations are baked in. Layers 1, 2, and 4 land in stage 1; layer 3 is also in stage 1 (designed in from day one to avoid retrofit). Optimistic op 2 (pipelined extract) ships with stage 1; optimistic op 1 (lock-driven prefetch) ships with stage 3.

1. **Metadata HTTP cache** — disk-backed cache of Packagist v2 JSON keyed by URL, with ETag and Last-Modified for conditional GETs. Re-runs against unchanged metadata are zero-network.
2. **Content-addressed package store** — package zips stored under `store/<sha256>/`. Hardlink/reflink (APFS clonefile, Linux FICLONE) into `vendor/<vendor>/<name>/`. Reinstalling a known version is essentially free.
3. **Resolution-result cache** — keyed by `(manifest content hash, lock content hash, platform fingerprint)`. Cache hit means resolution is skipped entirely; we only need to materialize `vendor/`.
4. **Parsed-manifest cache** — decoded `composer.json` and lockfiles serialized to a compact binary format keyed by file content hash. Skips JSON parsing on warm runs.

**Optimistic op 1 — speculative prefetch.** When a lockfile exists, start downloading the top-N packages by size in parallel with the resolver. Discard if the resolver disagrees.

**Optimistic op 2 — pipelined extract.** Begin streaming zip extraction into the package store as bytes arrive over HTTP, rather than waiting for the full download.

## Stages

### Stage 1 — Core install path (Packagist + PSR-4)

**Goal.** A modern Packagist-only library with PSR-4 autoload installs and runs. Cache architecture is in from day one.

**Components.**

- CLI scaffold: `install`, `update`, `--no-dev`, `--verbose`.
- `composer.json` parser (subset: `name`, `type`, `require`, `require-dev`, `autoload`, `autoload-dev`, `minimum-stability`, `prefer-stable`). The default Packagist repository is the only metadata source in stage 1; user-defined `repositories` entries are deferred to stage 2 (VCS) and beyond. Non-default `composer`-type repository entries error out with a clear message.
- Version-constraint parser: `^`, `~`, ranges, `|`, `dev-*`, branch aliases (`dev-main as 1.x-dev`), stability flags.
- Packagist v2 metadata client with disk-backed HTTP cache (ETag + Last-Modified). **Cache layer 1.**
- Parsed-manifest cache. **Cache layer 4.**
- PubGrub-based resolver.
- Own lockfile reader/writer.
- Resolution-result cache. **Cache layer 3.**
- Content-addressed package store with reflink/clonefile/hardlink/copy fallback chain. **Cache layer 2.**
- Concurrent zip download with pipelined extract. **Optimistic op 2.**
- PSR-4 autoloader output.

**Acceptance.** `composer-go install` on a small real Packagist project (e.g., something depending on `monolog/monolog`) succeeds and the generated autoloader resolves classes. Repeat install on a warm cache completes in under 100ms.

### Stage 2 — Real-world coverage (Laravel/Symfony work)

**Goal.** Unmodified `laravel/laravel` and `symfony/skeleton` install and boot.

**Components.**

- `files` autoloader output. **Status:** implemented (Stage 2 / Plan 1).
- `classmap` autoloader: token-stream PHP scanner (not regex) over declared paths, emit static map. **Status:** implemented (Stage 2 / Plan 1).
- Platform req detection: one-shot `php -r` at startup, capture version + loaded extensions.
- Platform constraint enforcement in resolver — warnings by default; hard error when `--no-dev` is set.
- VCS (git) repository support: clone, enumerate tags/branches, build per-version metadata from each ref's `composer.json`, cache aggressively.
- Auth: parse `~/.composer/auth.json` and `~/.config/composer-go/auth.json` (latter wins on conflict). Support `http-basic`, `bearer`, `github-oauth`, `gitlab-token`. SSH delegated to system `git`.
- Script runner: execute string commands via `sh -c`; execute `Class::method` references via a `php -r` shim.
- Plugin detection — packages with `"type": "composer-plugin"` (and references in `extra.composer-plugin-*`) are detected and ignored with a per-package warning.

**Acceptance.** Stock Laravel and Symfony skeletons install end-to-end. `php artisan` and `bin/console` boot without errors.

### Stage 3 — Speed and polish

**Goal.** Quantified, reproducible speed wins vs Composer 2.

**Components.**

- Lock-driven speculative prefetch. **Optimistic op 1.**
- Benchmark harness comparing cold-cache, warm-cache, and lock-unchanged installs against `composer install` on a corpus of real projects.
- Concurrency tuning per phase, driven by benchmark data.
- Resolver-conflict error rendering: PubGrub derivation chain rendered package-by-package.
- `--verbose` timing breakdown per phase.
- Optional terminal progress UI (single-line or simple multi-line; no fullscreen TUI).

**Acceptance.** Published benchmark numbers on Laravel skeleton, Symfony skeleton, a Drupal install, and at least one larger real project. Targets: warm-cache and lock-unchanged installs ≥5x faster than Composer; cold installs ≥2x faster.

### Stage 4 — Distribution

**Goal.** End-users can install easily.

**Components.**

- Cross-compile matrix: macOS (arm64, x86_64), Linux (arm64, x86_64).
- GoReleaser config + signed releases.
- Homebrew tap.
- `curl | sh` install script with checksum verification.
- Migration doc for users coming from Composer.
- Public README + a small docs site.

**Acceptance.** `brew install` and `curl | sh` both work; binaries are reproducible and signed.

## Cross-cutting design

### Lockfile format

JSON for diff-friendliness, with a sidecar binary cache for fast loads on warm runs.

```
{
  "schemaVersion": 1,
  "generator": { "name": "composer-go", "version": "..." },
  "manifestContentHash": "sha256:...",
  "platformFingerprint": "php-8.2.x;ext-mbstring;ext-json;...",
  "stability": { "minimumStability": "stable", "preferStable": true },
  "packages":    [ { name, version, source: { type, url, ref },
                     dist:   { type, url, sha256 },
                     require, autoload, suggest } ],
  "packagesDev": [ ...same shape... ],
  "aliases":     [ ... ]
}
```

The `platformFingerprint` is captured at resolution time. If the user's PHP changes under us, the fingerprint mismatches and we force a re-resolve.

### Resolver: PubGrub

PubGrub over a custom SAT solver. Reasons:

- Well-documented algorithm (Dart `pub`, `uv`, others).
- Produces human-readable derivation chains for conflicts. Composer's wall-of-text resolver errors are a known pain point.
- PHP's constraint quirks (stability flags, `dev-*`, branch aliases) layer on top of PubGrub without surgery.

### Error handling

- Every user-visible error carries a stable code (`CG001`, …) and a one-line "what to do next."
- Network errors: bounded exponential backoff; surface the underlying cause once retries are exhausted.
- Resolver conflicts: render the PubGrub derivation chain, package by package.
- Cache integrity: every cached artifact has a checksum (sha256 for zips, content hash for metadata). Mismatch → evict + refetch, never silently serve corrupt data.

### Testing strategy

- **Unit.** Parsers (manifest, constraint, lockfile), constraint matching, autoloader generators, classmap scanner.
- **Integration.** Recorded HTTP fixtures (record once against real Packagist, replay in tests) for resolver + fetcher; in-process fake git server for VCS tests.
- **Snapshot.** Autoloader output compared byte-for-byte against expected files for several real fixtures.
- **End-to-end (slow lane).** Install a small set of real projects (Laravel skeleton, Symfony skeleton, monolog) on CI; assert the autoloader resolves and a smoke command runs.
- **Resolver property tests.** Generate random package graphs; assert "any version chosen satisfies all requires," "if no solution exists we report a derivation," and "results are deterministic for a fixed input."

### Project layout

```
composer-go/
├── cmd/composer-go/         # CLI entrypoint, thin
├── internal/
│   ├── cli/                 # cobra commands
│   ├── manifest/            # composer.json parsing
│   ├── lock/                # composer-go.lock read/write
│   ├── constraint/          # PHP version constraint logic
│   ├── resolver/            # PubGrub
│   ├── registry/            # packagist + vcs metadata sources
│   ├── fetcher/             # http + git download
│   ├── store/               # content-addressed package store
│   ├── autoload/            # PSR-4 + files + classmap generation
│   ├── scripts/             # shell + php-callable runner
│   ├── auth/                # auth.json handling
│   ├── platform/            # php version + extension detection
│   └── cache/               # the four cache layers, shared
└── docs/superpowers/specs/  # this spec
```

### External dependencies (intent)

- `spf13/cobra` for CLI.
- `charmbracelet/log` for structured leveled logging.
- Custom constraint + lockfile code (PHP's semver quirks make off-the-shelf libraries unsafe).
- Shell out to `git` rather than embed `go-git`. Simpler, smaller binary, reuses the user's existing git config and SSH auth.
- Standard library `encoding/json`, `archive/zip`, `net/http`. If profiling in stage 3 shows JSON parsing as a hot spot, consider `goccy/go-json` or `bytedance/sonic`.

## Explicit deferrals

These are mentioned so future readers do not assume they were forgotten.

- Writing `composer.lock` for cross-tool compat. Out of scope; users pick one tool per project.
- Plugin support. Detected and ignored.
- PSR-0. Detected and warned.
- `lib-*` platform constraints. Detected and warned.
- `path` and `package` repository types. Post-MVP.
- `require`, `remove`, `show`, `why`, `outdated`, `audit`, `dump-autoload`, and all other Composer subcommands beyond `install`/`update`. Post-MVP.
- Windows. Post-stage-4.
- Interactive auth prompts on 401/403. Stage-2 polish item; deferred.
