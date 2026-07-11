# CLI Reference

Two commands: `install` and `update`. They share the same global flags plus a couple of per-command flags.

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

## Global flags

Available on both commands.

| Flag | Effect |
|---|---|
| `-v`, `--verbose` | Print a per-phase timing breakdown after the install completes. See [Verbose Output](./verbose-output.md). |
| `-q`, `--quiet` | Suppress non-error output. |
| `--no-dev` | Skip `require-dev`. Also enforces platform requirements strictly (a mismatch fails the install rather than warns). |
| `--no-scripts` | Skip every user-defined script entry — useful for CI or when debugging a resolver problem. |
| `--ignore-platform` | Skip every platform requirement check (`php`, `ext-*`, `lib-*`). |
| `--ignore-platform-req=<name>` | Skip a specific platform requirement. Repeatable: `--ignore-platform-req=php --ignore-platform-req=ext-curl`. |

## Per-command flags

| Flag | On | Effect |
|---|---|---|
| `--project <dir>` | `install`, `update` | Operate on the composer.json at `<dir>` instead of the current working directory. In workspace mode this is combined with the walk-up to find the workspace root (see [Workspaces](./workspaces.md#installing)). |
| `--no-prefetch` | `install`, `update` | Disable the lock-driven artifact prefetch (a benchmarking hook). |
| `--no-metadata-prefetch` | `install`, `update` | Disable the resolver-metadata prefetch (a benchmarking hook). |
| `--allow-plugins <name…>` | `install`, `update` | Accepted for Composer-CLI compatibility. **No-op** — gomposer never runs plugin code. The bare form `--allow-plugins` (no value) is accepted too. |

## Exit codes

- `0` — success.
- `1` — anything else. Details are printed to stderr with a `gomposer: <phase>:` prefix.

gomposer is strictly non-interactive; no prompts, no confirmations.

## Environment

Not covered by a flag but worth knowing:

| Variable | Effect |
|---|---|
| `XDG_CACHE_HOME` | Override the on-disk cache root. See [Cache Paths](./cache.md). |
| `HOME` | Used to construct the default cache root when `XDG_CACHE_HOME` isn't set. |
| `COMPOSER_HOME` / `COMPOSER_AUTH` | Consumed by the auth layer for Packagist bearer / basic credentials, matching Composer's own semantics. |
