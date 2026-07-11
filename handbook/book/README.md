# gomposer

A Composer-compatible PHP dependency installer, written in Go, that aims for a 2× cold-install and 5× warm-install speedup over upstream Composer — plus first-class **pnpm/bun-style workspaces** for PHP monorepos.

## What's here

- **[Install](./install.md)** — one-liner curl script, `go install`, or manual download.
- **[CLI reference](./cli.md)** — every flag on `install` and `update`.
- **[Composer compatibility](./composer-compatibility.md)** — what gomposer reads and produces, what it does not do.
- **[Workspaces](./workspaces.md)** — monorepo support via a top-level `workspaces` array and the `workspace:` protocol.
- **[Cache paths](./cache.md)** — where gomposer keeps things on disk and what's safe to delete.
- **[Benchmarks](./benchmarks.md)** — how to measure gomposer against upstream Composer.
- **[Contributing](./contributing.md)** — repo layout, tests, and where to find the design docs.

## Status

Alpha. Stages 1–3 (core install path, real-world coverage, speed and polish) are complete and covered by an in-tree test suite. Stage 4 (signed artifacts, Homebrew tap) is in progress; prebuilt binaries for macOS and Linux are already published to [GitHub Releases](https://github.com/TorstenDittmann/gomposer/releases).

Not recommended for production use yet. Please try it on non-critical projects and file issues at [github.com/TorstenDittmann/gomposer](https://github.com/TorstenDittmann/gomposer).

## Why

- **Parallel fetch + extract.** Downloads and zip extractions run on a worker pool sized to `NumCPU`. On repeat runs a per-package marker skips the extract entirely when the target already matches the locked SHA.
- **Content-addressed store.** Downloaded zips live under `~/Library/Caches/gomposer/store/` (macOS) or `$XDG_CACHE_HOME/gomposer/store/`, keyed by SHA-256; multiple projects share the same bytes on disk.
- **Speculative artifact prefetch.** As soon as the previous lock is read, artifact zips start downloading in the background while the resolver runs.
- **Speculative metadata prefetch.** Registry metadata (`/p2/<name>.json`) for every known require and lock entry warms in parallel while the solver runs.
- **PubGrub resolver.** Version conflicts are reported as human-readable derivations ("because X requires Y ^1.0 and Z requires Y ^2.0, no solution remains"), not stack traces.
- **Composer-compatible input.** Reads standard `composer.json`, including `require`, `require-dev`, `repositories` (`composer`, `vcs`, `git`), `minimum-stability`, `stability-flags`, `scripts`, platform requirements, and per-require stability flags (`^2.0@RC`, etc.).
- **Workspaces.** A pnpm/bun-inspired `workspaces` array + `workspace:` protocol turns a repo full of `composer.json` files into a single shared install.
