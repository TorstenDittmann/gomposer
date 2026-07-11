# Verbose Output

Add `-v` (or `--verbose`) to any `install` or `update` to get a per-phase timing block after the run completes.

```
gomposer: timing
  read manifest       52 ms
  resolve           6664 ms (20 packages)
    metadata-prefetch   804 ms (4 warmed)
  fetch             1915 ms (20/20 cold, 897 KB)
  materialize         50 ms
  autoload             1 ms
  write lock           0 ms
  scripts              0 ms
  -------- total    8723 ms
```

## Phases

| Phase | What it measures |
|---|---|
| `read manifest` | Parsing the root `composer.json` (plus every workspace manifest if the root declares any). |
| `resolve` | PubGrub-based dependency resolution. On a warm re-run this drops to `0 ms` because the resolution cache short-circuits. |
| `metadata-prefetch` | Background pool that warmed `/p2/<name>.json` for known-needed packages. Printed only when the pool actually did work (not on cache-hit runs where the resolver short-circuited). |
| `fetch` | Artifact download. `X/Y cold` means X of Y archives were pulled over the network; the rest hit the content-addressed store. |
| `materialize` | Extracting archives into `vendor/`. A per-package `.composer-go-sha` marker lets this drop to near-zero on warm runs when the target already matches the locked SHA. |
| `autoload` | Generating `vendor/autoload.php` and friends. |
| `write lock` | Serializing and writing `gomposer.lock` (atomic rename). |
| `scripts` | Sum of every user-defined lifecycle script that fired. |

## Reading the phases

- **`resolve = 0 ms` on a warm re-run** is the resolution cache doing its job. Nothing to worry about.
- **Big `resolve` on a cold run** is dominated by metadata HTTP round-trips. The metadata prefetch line under it shows what the background pool covered — the resolver still made its own synchronous lookups; the pool just tried to make them hit warm cache.
- **Big `fetch` (`X/Y cold` with X > 0)** is dominated by download bandwidth. `--no-prefetch` disables the artifact prefetch pool for comparison.
- **Big `materialize` on a repeat install** shouldn't happen; the `.composer-go-sha` marker per package should make it near-zero. If it doesn't, please [open an issue](https://github.com/TorstenDittmann/gomposer/issues) with the timing block and the fixture that reproduces it.

## Turning things off for measurement

To isolate the wall-clock contribution of each background optimization:

```sh
gomposer install -v --no-prefetch --no-metadata-prefetch
```

`--no-prefetch` disables the artifact prefetch pool. `--no-metadata-prefetch` disables the registry-metadata prefetch pool. Both are on by default; both are safe to leave on in production.
