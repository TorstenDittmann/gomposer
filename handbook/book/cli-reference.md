# CLI Reference

Dependency installation and inspection use `install`, `update`, `require`,
`remove`, `show`, `why`, `outdated`, and `audit`. Cache inspection and
maintenance live under the `cache` command group.

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

## `gomposer cache`

Print the cache root, disk usage for each cache layer, and the total:

```sh
gomposer cache
```

The cache group also supports:

```sh
gomposer cache dir                       # print only the cache root
gomposer cache clear                     # clear every layer
gomposer cache clear store metadata      # clear selected layers
```

Valid layer names are `store`, `metadata`, `resolution`, and `vcs`. Clearing is non-interactive because every layer is rebuildable. An unknown layer fails before anything is removed, and repeated layer names are cleared only once.

`--quiet` suppresses the informational output from `cache` and `cache clear`. `cache dir` still prints the path under `--quiet` so it remains suitable for command substitution.

See [Cache Paths](./cache.md) for the layer layout.

## `gomposer why`

Explain why a package or platform requirement is present in the selected
dependency graph:

```sh
gomposer why psr/log
gomposer why --recursive psr/log
gomposer why --tree psr/log
gomposer why --format=json php
```

The default output lists immediate dependents. `--recursive` includes every
transitive reverse edge, while `--tree` renders complete reverse dependency
paths and marks cycles. `--no-dev` removes development packages and
`require-dev` edges. In a workspace member, explanations are limited to the
graph reachable from that member while still using the root lockfile.

## `gomposer outdated`

Compare locked packages with configured repository metadata:

```sh
gomposer outdated
gomposer outdated --direct
gomposer outdated --format=json
gomposer outdated --strict
```

The output distinguishes the newest `wanted` version allowed by known incoming
constraints from the unrestricted `latest` version. Local workspace packages
are skipped. `--strict` returns status 1 when updates are reported.

## `gomposer audit`

Check packages in `gomposer.lock` against Packagist security advisories:

```sh
gomposer audit
gomposer audit --no-dev
gomposer audit --format=json
```

Audits include development packages by default and always use the shared root
lock in a workspace. A clean audit exits 0; matching advisories or operational
failures exit 1. Advisory results are fetched fresh and are not stored in the
metadata cache.

## Install and update flags

Available on both dependency commands.

| Flag | Effect |
|---|---|
| `-v`, `--verbose` | Print a per-phase timing breakdown after the install completes. See [Verbose Output](./verbose-output.md). |
| `-q`, `--quiet` | Suppress non-error output. |
| `--color=auto\|always\|never` | Select automatic, forced, or disabled color. Automatic mode honors `NO_COLOR` and `TERM=dumb`. Animation remains TTY-only. |
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

Install and update adapt to their output destination. A terminal gets a live
checklist; CI and redirected stderr get one stable line per completed phase.
See [Terminal Output](./terminal-output.md) for examples and color behavior.

## Environment

Not covered by a flag but worth knowing:

| Variable | Effect |
|---|---|
| `XDG_CACHE_HOME` | Override the on-disk cache root. See [Cache Paths](./cache.md). |
| `HOME` | Used to construct the default cache root when `XDG_CACHE_HOME` isn't set. |
| `COMPOSER_HOME` / `COMPOSER_AUTH` | Consumed by the auth layer for Packagist bearer / basic credentials, matching Composer's own semantics. |
