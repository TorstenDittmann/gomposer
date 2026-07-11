# gomposer

A Composer-compatible PHP dependency installer, written in Go, that aims for a 2× cold-install and 5× warm-install speedup over upstream Composer — plus first-class **pnpm/bun-style workspaces** for PHP monorepos.

## Status

Alpha. Stages 1–3 (core install path, real-world coverage, speed and polish) are complete and covered by an in-tree test suite. Stage 4 (signed artifacts, Homebrew tap) is in progress; prebuilt binaries for macOS and Linux are already published to [GitHub Releases](https://github.com/TorstenDittmann/gomposer/releases).

Not recommended for production use yet. Please try it on non-critical projects and file issues at [github.com/TorstenDittmann/gomposer](https://github.com/TorstenDittmann/gomposer).

## Why

- **Parallel fetch + extract.** Downloads and zip extractions run on a worker pool sized to `NumCPU`. A per-package marker skips the extract when the target already matches the locked SHA.
- **Content-addressed store.** Downloaded zips live under `~/Library/Caches/gomposer/store/` (macOS) or `$XDG_CACHE_HOME/gomposer/store/`, keyed by SHA-256; multiple projects share the same bytes on disk.
- **Speculative prefetch (two flavors).** Artifact zips start downloading as soon as the previous lock is read. Registry metadata (`/p2/<name>.json`) warms in parallel while the solver runs.
- **PubGrub resolver.** Version conflicts are reported as human-readable derivations, not stack traces.
- **Composer-compatible input.** Standard `composer.json`, `repositories`, `minimum-stability`, `stability-flags`, per-require `@RC` / `@beta` / `@dev` suffixes, scripts, platform requirements.
- **Workspaces.** A pnpm/bun-inspired `workspaces` array + `workspace:` protocol turns a repo full of `composer.json` files into a single shared install.

## Where to start

- New to gomposer? Start with [Installation](./installation.md) and [Quick Start](./quick-start.md).
- Looking up a flag? Jump to the [CLI Reference](./cli-reference.md).
- Migrating a Composer project? Check [Reading composer.json](./reading-composer-json.md) and [gomposer.lock](./gomposer-lockfile.md).
- Building a monorepo? [Workspaces Overview](./workspaces.md) is the entry point.
- Contributing? [Contributing](./contributing.md) covers repo layout, tests, and the design docs.
