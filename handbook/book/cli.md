# CLI reference

Two commands: `install` and `update`. Both share the same set of global flags plus a couple of per-command flags.

## `gomposer install`

Install dependencies into `vendor/` from `composer.json`, using `gomposer.lock` when it's present and up to date.

```sh
gomposer install [flags]
```

## `gomposer update`

Re-resolve every dependency, rewrite `gomposer.lock`, then install.

```sh
gomposer update [flags]
```

## Global flags (both commands)

| Flag | Effect |
|---|---|
| `-v`, `--verbose` | Print a per-phase timing breakdown after the install completes. |
| `-q`, `--quiet` | Suppress non-error output. |
| `--no-dev` | Skip `require-dev`. Also enforces platform requirements strictly (a mismatch fails the install rather than warns). |
| `--no-scripts` | Skip every user-defined script entry — useful for CI or when debugging a resolver problem. |
| `--ignore-platform` | Skip every platform requirement check (`php`, `ext-*`, `lib-*`). |
| `--ignore-platform-req=<name>` | Skip a specific platform requirement. Repeatable: `--ignore-platform-req=php --ignore-platform-req=ext-curl`. |

## Per-command flags

| Flag | On | Effect |
|---|---|---|
| `--project <dir>` | `install`, `update` | Operate on the composer.json at `<dir>` instead of the current working directory. In workspace mode (see [Workspaces](./workspaces.md)), this is combined with the walk-up to find the workspace root. |
| `--no-prefetch` | `install`, `update` | Disable the lock-driven artifact prefetch (a benchmarking hook). |
| `--no-metadata-prefetch` | `install`, `update` | Disable the resolver-metadata prefetch (a benchmarking hook). |
| `--allow-plugins <name…>` | `install`, `update` | Accepted for Composer-CLI compatibility. **No-op** — gomposer never runs plugin code. The bare form `--allow-plugins` (no value) is accepted too. |

## Verbose output

`-v` prints a per-phase timing block after the install completes:

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

Interpretation:

- `read manifest` — parsing the root `composer.json` (plus every workspace manifest if you're in a workspace root).
- `resolve` — PubGrub-based dependency resolution. On a warm re-run, this drops to `0 ms` because the resolution cache short-circuits.
- `metadata-prefetch` — the background metadata warm-up. Printed only when the pool actually did work (not on cache-hit runs).
- `fetch` — artifact download. `X/Y cold` = X archives were pulled over the network, Y-X hit the content-addressed store.
- `materialize` — extracting archives into `vendor/`. A per-package marker file lets this drop to near-zero on warm runs when the target already matches the locked SHA.
- `autoload` — generating `vendor/autoload.php` and friends.
- `write lock` — writing `gomposer.lock`.
- `scripts` — sum of every user-defined lifecycle script that fired.
