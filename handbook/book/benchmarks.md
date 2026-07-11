# Benchmarks

gomposer ships a benchmark harness at `cmd/bench` that measures gomposer against upstream Composer over a small fixture corpus. It's run manually; CI does not invoke Composer.

## Run it

```sh
go build -o gomposer ./cmd/gomposer
go run ./cmd/bench \
  --corpus cmd/bench/testdata/corpus \
  --gomposer ./gomposer \
  --composer "$(which composer)" \
  --runs 5
```

The harness reports the median of `--runs` runs per `(fixture, scenario, tool)` and prints a markdown table you can paste into an issue.

## Scenarios

Each fixture is measured under three scenarios:

- **cold** — `vendor/` and `composer.lock` are removed before every run. Registry HTTP cache and content-addressed store are also cleared. Everything is fetched from scratch.
- **warm** — lockfile and on-disk caches are preserved; only `vendor/` is removed. This is the common CI cache-hit shape.
- **lock-unchanged** — nothing is removed; the timed run starts fully populated. Measures the "did nothing changed?" fast path.

## Corpus

The default corpus lives under `cmd/bench/testdata/corpus/`:

- `tiny-psrlog` — a single-file dep.
- `monolog` — small realistic library.
- `laravel-skeleton` — Laravel's default `composer.json`.
- `symfony-skeleton` — Symfony's default `composer.json`.

Point `--corpus` at any directory of `<name>/composer.json` fixtures to expand the run.

## Targets

- Warm-cache and lock-unchanged installs ≥ 5× faster than upstream Composer.
- Cold installs ≥ 2× faster.

These are the Stage 3 acceptance targets. Real numbers depend on your network — HTTP dominates cold installs, and cold-install speedup is largely bounded by parallel-fetch bandwidth.

## Isolating individual phases

Use `-v` on a hand-run install to see where the time is going:

```sh
./gomposer install -v
```

Prints a per-phase timing block (see [Verbose Output](./verbose-output.md)). Compare `resolve` (dominated by metadata HTTP round-trips on cold cache) against `fetch` and `materialize` to know where to focus.

To measure the isolated contribution of each background optimization, disable them:

```sh
./gomposer install -v --no-prefetch --no-metadata-prefetch
```

`--no-prefetch` disables lock-driven artifact prefetch; `--no-metadata-prefetch` disables registry-metadata prefetch. Both are on by default.
