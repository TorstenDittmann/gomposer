# CLI Reference

Dependency installation and inspection use `install`, `update`, `require`,
`remove`, `show`, `why`, `outdated`, `audit`, `dump-autoload`, and `run`.
Project bootstrap and checks use `init` and `validate`. Cache inspection and
maintenance live under the `cache` command group.

## `gomposer init`

Create a basic `composer.json` from flags (non-interactive; Composer-compatible
option names):

```sh
gomposer init --name=acme/demo --description="Demo" --license=MIT -a src/
gomposer init --name=acme/app --require=psr/log:^3.0 --require-dev=phpunit/phpunit:^10
```

| Flag | Effect |
|---|---|
| `--name` | Package name (`vendor/package`). When omitted, defaults to `$USER/<dirname>`. |
| `--description` | Package description. |
| `--author` | Author as `Name <email@example.com>`. When omitted, uses `git config user.name` / `user.email` when available. |
| `--type` | Package type (`library`, `project`, …). |
| `--homepage` | Package homepage URL. |
| `--require` | Production requirement (`name:constraint`). Repeatable. Also accepts `name=constraint` and `name constraint`. |
| `--require-dev` | Development requirement. Repeatable. |
| `-s`, `--stability` | `minimum-stability` (`stable`, `RC`, `beta`, `alpha`, `dev`). |
| `-l`, `--license` | Package license. |
| `-a`, `--autoload` | Add a PSR-4 mapping for the package namespace to this relative directory (creates the directory). |
| `--project <dir>` | Write `composer.json` under `<dir>` instead of the current directory. |

Fails if `composer.json` already exists. Does not generate `vendor/`; run
`gomposer install` afterward.

## `gomposer validate`

Validate `composer.json` (and `gomposer.lock` when present):

```sh
gomposer validate
gomposer validate path/to/composer.json
gomposer validate --no-check-publish --strict
```

Checks JSON syntax, package names, version constraints, repositories,
`minimum-stability`, and workspace discovery. Publishability requires `name`
and `description`. Warnings cover missing license, unbound (`*`) or exact
version constraints, and a present `version` field. When `gomposer.lock`
exists, also checks the stored `manifestContentHash` and that locked package
versions still satisfy direct constraints.

| Flag | Effect |
|---|---|
| `--no-check-all` | Skip unbound/exact constraint warnings. |
| `--no-check-lock` | Skip `gomposer.lock` freshness checks. |
| `--no-check-publish` | Do not treat missing publish fields as errors. |
| `--no-check-version` | Do not warn when `version` is present. |
| `--strict` | Non-zero exit on warnings as well as errors. |
| `--project <dir>` | Validate `<dir>/composer.json` (ignored when a file argument is given). |

Exit codes match Composer: `0` ok, `1` warnings with `--strict`, `2` errors,
`3` missing/unreadable file.

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

## `gomposer dump-autoload`

Regenerate `vendor/autoload.php` and the Composer helper files from the current
`gomposer.lock` without resolving or reinstalling packages:

```sh
gomposer dump-autoload
gomposer dump-autoload --no-dev
gomposer dump-autoload --no-scripts
```

Root `autoload` and `autoload-dev` are re-read from `composer.json`; package
autoload maps come from the lockfile. `--no-dev` omits `autoload-dev` and locked
development packages. `--no-scripts` skips `pre-autoload-dump` and
`post-autoload-dump`. In a workspace, dump from a member writes the shared root
`vendor/` (the same walk-up as `install`). `dumpautoload` is accepted as an
alias.

Use this after adding a class under an existing classmap path, or after editing
root autoload config, when a full `gomposer install` is unnecessary.

## `gomposer run`

Run a named script from `composer.json` (custom names such as `test`, and
lifecycle events such as `post-install-cmd`):

```sh
gomposer run test
gomposer run-script test
gomposer run test -- --filter=FooTest
gomposer run --list
gomposer run --timeout 0 test
```

Unknown script names are errors. Extra arguments after `--` are POSIX-quoted
and appended to shell script bodies. PHP-callables still receive no
`Composer\Script\Event` (the existing Stage 2 limit). `--timeout` is seconds;
`0` disables the deadline; the default is `300`, matching Composer.
`--list` / `-l` prints defined script names, sorted.

In a workspace, `run` uses the **selected** manifest: a member directory runs
that member's scripts with the member as the working directory (its `vendor/`
symlink already points at the shared root). The workspace root runs the root
manifest. `--filter` and topological execution across workspaces remain
[Scope 2](./workspaces.md#scope).

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
| `--project <dir>` | `install`, `update`, `dump-autoload`, `run`, `init`, `validate` | Operate on the composer.json at `<dir>` instead of the current working directory. For `install` / `update` / `dump-autoload` in workspace mode this is combined with the walk-up to find the workspace root (see [Workspaces](./workspaces.md#installing)). For `run`, the nearest `composer.json` walking up is used so a member keeps its own scripts. For `validate`, ignored when a file argument is given. |
| `--no-prefetch` | `install`, `update` | Disable the lock-driven artifact prefetch (a benchmarking hook). |
| `--no-metadata-prefetch` | `install`, `update` | Disable the resolver-metadata prefetch (a benchmarking hook). |
| `--allow-plugins <name…>` | `install`, `update` | Accepted for Composer-CLI compatibility. **No-op** — gomposer never runs plugin code. The bare form `--allow-plugins` (no value) is accepted too. |

## Exit codes

- `0` — success.
- `1` — generic failure, or `validate --strict` with warnings only.
- `2` — `validate` found errors (including publishability errors unless `--no-check-publish`).
- `3` — `validate` could not read `composer.json`.
- `130` — cancelled (`SIGINT` / `SIGTERM`).

Details are printed to stderr (or to the command's configured writers).
`validate` prints its report to stdout unless `--quiet`.

gomposer is strictly non-interactive; no prompts, no confirmations.
`gomposer init` takes the same stance: pass flags instead of answering
Composer-style questions.

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
