# Contributing

## Repo layout

- `cmd/gomposer/` — the CLI entry point (13 lines of `main`, everything else is under `internal/`).
- `cmd/bench/` — the benchmark harness (see [Benchmarks](./benchmarks.md)).
- `internal/manifest/` — `composer.json` parser and the workspace discovery helper.
- `internal/constraint/` — version and constraint parsing, incl. the `workspace:` prefix.
- `internal/lock/` — `gomposer.lock` read/write.
- `internal/registry/` — Packagist v2 client + VCS registry + multisource aggregator.
- `internal/resolver/` — PubGrub-derived solver + human-readable conflict rendering.
- `internal/fetcher/` — downloads, content-addressed store, materialization (extract).
- `internal/autoload/` — `vendor/autoload.php` and friends, byte-compared against Composer's output.
- `internal/orchestrator/` — the pipeline that stitches the above together (resolve → prefetch → fetch → materialize → autoload → scripts).
- `internal/cli/` — Cobra command wiring.
- `internal/platform/`, `internal/scripts/`, `internal/plugins/`, `internal/cache/` — supporting subsystems.
- `internal/auth/` — Packagist auth config parsing.

## Building and testing

```sh
go build -o gomposer ./cmd/gomposer
go test ./...
go test -race ./...
```

The suite runs cleanly with `-race` and covers every subsystem, plus integration tests in `internal/orchestrator/` that exercise the full pipeline against in-process fakes.

Golden-output tests live under `internal/autoload/testdata/expected/` (autoload snapshots) and `internal/resolver/testdata/expected/` (conflict prose). To regenerate autoload fixtures after a legitimate change, `WRITE_EXPECTED=1 go test ./internal/autoload/ -run TestWriteExpected`.

## CI

`.github/workflows/ci.yml` runs `go mod tidy` check, `go vet`, `go build`, and `go test -race ./...` on `ubuntu-latest` and `macos-latest`. `.github/workflows/release.yml` triggers on `v*` tag push and runs GoReleaser to publish binaries.

## How this project makes decisions

The full design history lives under `docs/superpowers/` in the repo:

- `docs/superpowers/specs/2026-05-07-gomposer-design.md` — original overall design.
- `docs/superpowers/specs/2026-06-18-metadata-prefetch-design.md` — metadata prefetch design.
- `docs/superpowers/specs/2026-07-10-workspaces-design.md` — workspaces design.
- `docs/superpowers/plans/*.md` — per-stage implementation plans, checkbox-tracked task by task. Reading these top-to-bottom is the fastest way to understand what the codebase does and why.

Every feature currently in the tree corresponds to one of these plans; you'll typically find a spec that answered "what are we building", a plan that answered "how do we get there in bite-sized commits", and a linked PR.

## Pull requests

- Please run `go test ./...` before opening a PR.
- Add tests with any behavior change. TDD-flavored PRs (RED-then-GREEN in the same branch) are welcome.
- Issues and PRs live at [github.com/TorstenDittmann/gomposer](https://github.com/TorstenDittmann/gomposer).

## License

MIT — see [`LICENSE`](https://github.com/TorstenDittmann/gomposer/blob/main/LICENSE) in the repository root.
