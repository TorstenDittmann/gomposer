# gomposer

A Composer-compatible PHP dependency installer, written in Go, that aims for a 2× cold-install and 5× warm-install speedup over upstream Composer.

## Status

Alpha. The core install path, real-world package coverage, and performance work are complete and covered by an in-tree test suite. Prebuilt binaries for macOS and Linux are published to GitHub Releases with SHA-256 checksums; release artifacts are not cryptographically signed.

Not recommended for production use yet. Please try it on non-critical projects and file issues.

## Why

- **Parallel fetch + extract.** Downloads and zip extractions run on a worker pool sized to `NumCPU`. On repeat runs a per-package marker skips the extract entirely when the target already matches the locked SHA.
- **Content-addressed store.** Downloaded zips live under `~/Library/Caches/gomposer/store/` (macOS) or `$XDG_CACHE_HOME/gomposer/store/`, keyed by SHA-256; multiple projects share the same bytes on disk.
- **Speculative prefetch.** As soon as the previous lock is read, artifact zips start downloading in the background while the resolver runs.
- **PubGrub resolver.** Version conflicts are reported as human-readable derivations ("because X requires Y ^1.0 and Z requires Y ^2.0, no solution remains"), not stack traces.
- **Composer-compatible input.** Reads standard `composer.json`, including `require`, `require-dev`, `repositories` (`composer`, `vcs`, `git`), `minimum-stability`, `stability-flags`, `scripts`, and platform requirements.

## Install

**Prebuilt binary (macOS + Linux, amd64 + arm64):**

```sh
curl -fsSL https://raw.githubusercontent.com/TorstenDittmann/gomposer/main/install.sh | sh
```

The script downloads the latest release from GitHub, verifies its SHA-256 against the release's `gomposer_<version>_checksums.txt`, and installs the binary to `/usr/local/bin/gomposer` (falling back to `~/.local/bin` if the default isn't writable without sudo). Override with `GOMPOSER_INSTALL_DIR=/path/to/dir` or pin a version with `GOMPOSER_VERSION=v0.1.0`.

**From source (requires Go 1.25+):**

```sh
go install github.com/torstendittmann/gomposer/cmd/gomposer@latest
```

**Manual download:** each release on the [Releases page](https://github.com/TorstenDittmann/gomposer/releases) has a `.tar.gz` per platform plus a checksums file. Verify with `sha256sum -c`, extract, and drop the `gomposer` binary anywhere on your PATH.

Homebrew and a Windows build are not planned for now.

## Usage

Inside a project that has a `composer.json`:

```sh
gomposer install          # install from composer.json, using gomposer.lock if present
gomposer update           # re-resolve everything and rewrite gomposer.lock + vendor/
gomposer require psr/log:^3.0             # add a production dependency and install
gomposer require --dev phpunit/phpunit    # add a development dependency and install
gomposer remove psr/log                    # remove a production dependency and update
gomposer remove --dev phpunit/phpunit      # remove a development dependency and update
gomposer show                              # list packages from gomposer.lock
gomposer show monolog/monolog              # inspect one locked package
gomposer show --direct --tree              # render the direct dependency trees
gomposer why psr/log                       # show immediate dependents
gomposer why --recursive --tree psr/log    # show every reverse dependency path
gomposer cache            # print the cache path and per-layer disk usage
gomposer cache dir        # print only the cache path
gomposer cache clear      # clear every cache layer
gomposer cache clear store metadata  # clear selected layers
```

Common flags:

| Flag | What it does |
|---|---|
| `--no-dev` | Skip `require-dev`; enforce platform requirements strictly. |
| `-q`, `--quiet` | Suppress non-error output. |
| `-v`, `--verbose` | Print a per-phase timing breakdown. |
| `--color=auto\|always\|never` | Control color in human-readable terminal output. |
| `--ignore-platform` | Skip every platform check (`php`, `ext-*`, `lib-*`). |
| `--ignore-platform-req=<name>` | Skip a specific platform requirement (repeatable). |
| `--no-scripts` | Do not execute user-defined scripts. |
| `--no-prefetch` | Disable lock-driven speculative prefetch (benchmarking hook). |
| `--no-metadata-prefetch` | Disable registry-metadata prefetch (benchmarking hook). |
| `--project <dir>` | Point at a project directory other than the current one. |

Run `gomposer install --help` for the full list.

`gomposer require` accepts one or more `package[:constraint]` arguments. When
the constraint is omitted, `*` is written and the resolver selects the newest
compatible version for the lockfile. If resolution or installation fails,
gomposer restores both `composer.json` and `gomposer.lock`. From inside a
workspace, the selected workspace manifest is updated while the complete
workspace graph is resolved at the repository root.

`gomposer remove` accepts one or more direct dependency names. Use `--dev` to
remove entries from `require-dev`; omitting it targets `require`. The remaining
graph is re-resolved, packages no longer present in the new lockfile are pruned
from `vendor/`, and the manifest and lockfile are restored if the update fails.
Workspace removal follows the same selected-manifest/root-resolution behavior
as `require`.

`gomposer show` inspects the manifest and lockfile without network access. It
lists production, development, direct, and workspace package status; accepts a
package name for source, dist, and requirement details; and supports `--direct`,
`--tree`, and `--format=json`. When run for a workspace, direct dependencies
come from that workspace while resolved package data comes from the root lock.

`gomposer why` explains which root, workspace, or locked packages require a
package. Use `--recursive` to include transitive dependents, `--tree` to render
every reverse dependency path, and `--format=json` for structured output. It
also accepts platform requirement names such as `php` and `ext-curl`.

Interactive terminals show a live install checklist with package progress and
phase timings. Redirected stderr and CI receive one stable line per completed
phase instead; `NO_COLOR` and `TERM=dumb` are honored in automatic color mode.
Warnings temporarily clear and redraw the active row, so they remain readable
without damaging progress output.

## Workspaces (monorepo)

gomposer supports pnpm/bun-style workspaces via a top-level `"workspaces"` array in the root `composer.json`:

```json
{
    "name": "acme/monorepo",
    "workspaces": ["packages/*", "apps/*"]
}
```

Every matched directory containing a `composer.json` becomes a workspace. Cross-workspace deps use the `workspace:` protocol:

```json
{
    "name": "acme/api",
    "require": { "acme/shared": "workspace:^1.0" }
}
```

`workspace:*` matches the local workspace at any version; `workspace:<constraint>` requires the local workspace's declared `version` to satisfy the constraint (mismatch is a hard error at install time).

`gomposer install` at the repo root — or from any workspace subdirectory, it walks up — resolves every workspace's external deps together and installs into a shared `vendor/` at the repo root. Each workspace gets a `vendor/` symlink to it, and cross-workspace packages become symlinks to their source dirs:

```
acme-monorepo/
├── composer.json          # { "workspaces": ["packages/*", "apps/*"] }
├── vendor/                # real dir; aggregate of every workspace's external deps
│   └── acme/shared        # symlink → ../../packages/shared
├── packages/shared/
│   ├── composer.json
│   ├── src/
│   └── vendor             # symlink → ../../vendor
└── apps/api/
    ├── composer.json      # requires acme/shared: workspace:^1.0
    ├── src/
    └── vendor             # symlink → ../../vendor
```

Any workspace's own `require __DIR__ . '/../vendor/autoload.php'` bootstrap continues to work — the symlink resolves to the shared install.

Not yet in scope (Scope 2 follow-up): `--filter=<pkg>` for subset installs; `gomposer run <script>` for topologically-ordered script execution across workspaces.

See `docs/superpowers/specs/2026-07-10-workspaces-design.md` for the full design.

## Composer compatibility

gomposer reads `composer.json` and produces the same `vendor/` layout Composer does, including `vendor/autoload.php`, `vendor/composer/autoload_*.php`, and `vendor/composer/installed.php`. Autoloader coverage includes PSR-4, PSR-0, classmap (token-stream scanner, not regex), files, and `exclude-from-classmap`.

What it does **not** do:

- It does not read `composer.lock` — gomposer keeps its own `gomposer.lock` with a different schema. If both exist they are independent; you can run Composer alongside gomposer safely.
- It does not run Composer plugins. `--allow-plugins` is accepted for compatibility and is a no-op.

## Cache paths

| OS | Location |
|---|---|
| macOS | `~/Library/Caches/gomposer/` |
| Linux / other | `$XDG_CACHE_HOME/gomposer/` (falls back to `~/.cache/gomposer/`) |

Sub-directories:

- `store/` — content-addressed zip store, shared across projects.
- `packagist/` — HTTP + parsed-response cache for `/p2/*.json`.
- `vcs/` — cloned VCS repositories for `repositories: [{type: vcs}]` entries.
- `resolution/` — cached resolver results keyed by manifest+lock+platform.

Deleting any of them is safe; the next install will refill what it needs.

Use `gomposer cache` to inspect disk usage, `gomposer cache dir` to print the raw cache path, and `gomposer cache clear [layer...]` to clear all or selected layers. Valid layer names are `store`, `metadata`, `resolution`, and `vcs`.

## Benchmarks

`cmd/bench` measures gomposer vs Composer over a small fixture corpus and prints a markdown table. Run manually — CI does not invoke Composer.

```sh
go build -o gomposer ./cmd/gomposer
go run ./cmd/bench \
  --corpus cmd/bench/testdata/corpus \
  --gomposer ./gomposer \
  --composer "$(which composer)" \
  --runs 5
```

The harness reports the median of `--runs` runs per `(fixture, scenario, tool)`. Scenarios:

- **cold** — `vendor/`, `composer.lock`, and `gomposer.lock` are all removed before each run.
- **warm** — lockfile and on-disk caches preserved; only `vendor/` is removed.
- **lock-unchanged** — nothing is removed; the timed run starts fully populated.

## Contributing

Design notes live under [`docs/superpowers/specs/`](docs/superpowers/specs/) and per-stage implementation plans under [`docs/superpowers/plans/`](docs/superpowers/plans/). Pull requests welcome; please run `go test ./...` before opening one and add a test with any behavior change.

## License

MIT — see [LICENSE](LICENSE).
