# gomposer

A Composer-compatible PHP dependency installer, written in Go, that aims for a 2× cold-install and 5× warm-install speedup over upstream Composer.

## Status

Alpha. Stages 1–3 (core install path, real-world coverage, speed and polish) are complete and covered by an in-tree test suite. Stage 4 (signed artifacts, Homebrew tap) is in progress; prebuilt binaries for macOS and Linux are already published to GitHub Releases.

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

The script downloads the latest release from GitHub, verifies its SHA-256 against the release's `checksums.txt`, and installs the binary to `/usr/local/bin/gomposer` (falling back to `~/.local/bin` if the default isn't writable without sudo). Override with `GOMPOSER_INSTALL_DIR=/path/to/dir` or pin a version with `GOMPOSER_VERSION=v0.1.0`.

**From source (requires Go 1.25+):**

```sh
go install github.com/torstendittmann/gomposer/cmd/gomposer@latest
```

**Manual download:** each release on the [Releases page](https://github.com/TorstenDittmann/gomposer/releases) has a `.tar.gz` per platform plus a checksums file. Verify with `sha256sum -c`, extract, and drop the `gomposer` binary anywhere on your PATH.

Homebrew and a Windows build are not planned for now.

## Usage

Inside a project that has a `composer.json`:

```sh
gomposer install          # install from composer.json, using composer.lock if present
gomposer update           # re-resolve everything and rewrite composer.lock + vendor/
```

Common flags:

| Flag | What it does |
|---|---|
| `--no-dev` | Skip `require-dev`; enforce platform requirements strictly. |
| `-q`, `--quiet` | Suppress non-error output. |
| `-v`, `--verbose` | Print a per-phase timing breakdown. |
| `--ignore-platform` | Skip every platform check (`php`, `ext-*`, `lib-*`). |
| `--ignore-platform-req=<name>` | Skip a specific platform requirement (repeatable). |
| `--no-scripts` | Do not execute user-defined scripts. |
| `--no-prefetch` | Disable lock-driven speculative prefetch (benchmarking hook). |
| `--project <dir>` | Point at a project directory other than the current one. |

Run `gomposer install --help` for the full list.

## Composer compatibility

gomposer reads `composer.json` and produces the same `vendor/` layout Composer does, including `vendor/autoload.php`, `vendor/composer/autoload_*.php`, and `vendor/composer/installed.php`. Autoloader coverage includes PSR-4, PSR-0, classmap (token-stream scanner, not regex), files, and `exclude-from-classmap`.

What it does **not** do:

- Reads and writes the standard `composer.lock`. gomposer emits a valid Composer-shape lockfile (`content-hash`, `stability-flags`, `platform`, `packages` with `source`/`dist`/`autoload`/`time`/`notification-url`); Composer can consume it directly. Optional per-package metadata Composer emits (authors, license, description, keywords) is not populated on our writes; Composer will fill it back in on its next run if you use both tools alternately.
- It does not run Composer plugins. `--allow-plugins` is accepted for compatibility and is a no-op.
- Stage 4 items (signed releases, Homebrew, `curl | sh`, migration guide) are pending.

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

- **cold** — `vendor/` and `composer.lock` are removed before each run.
- **warm** — lockfile and on-disk caches preserved; only `vendor/` is removed.
- **lock-unchanged** — nothing is removed; the timed run starts fully populated.

## Contributing

Design notes live under [`docs/superpowers/specs/`](docs/superpowers/specs/) and per-stage implementation plans under [`docs/superpowers/plans/`](docs/superpowers/plans/). Pull requests welcome; please run `go test ./...` before opening one and add a test with any behavior change.

## License

MIT — see [LICENSE](LICENSE).
