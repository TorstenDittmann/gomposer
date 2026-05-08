# composer-go

## Benchmarks

`cmd/bench` measures composer-go vs composer over a small fixture corpus and
prints a markdown table. It is run manually; CI does not invoke composer.

```sh
go build -o composer-go ./cmd/composer-go
go run ./cmd/bench \
  --corpus cmd/bench/testdata/corpus \
  --composer-go ./composer-go \
  --composer "$(which composer)" \
  --runs 5
```

The harness reports the median of N runs per `(fixture, scenario, tool)`.
Scenarios:

- **cold:** `vendor/`, `composer.lock`, and `composer-go.lock` removed before
  every run.
- **warm:** lockfile and on-disk caches preserved; only `vendor/` is removed.
- **lock-unchanged:** nothing is removed; the timed run starts fully populated.
