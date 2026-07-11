# Installing

One command at the root installs the whole workspace tree.

```sh
gomposer install
```

That's it. From any workspace subdirectory too — see walk-up below.

## Aggregate resolve

Under the hood, gomposer:

1. Loads the root `composer.json` and discovers every workspace's `composer.json`.
2. Builds a virtual **super-manifest** that unions every workspace's `require` (and `require-dev`, unless `--no-dev`) into one set. Cross-workspace `workspace:` requires are validated and stripped from this set — workspaces are already known locally, no need to route them through the registry.
3. Hands the super-manifest to the PubGrub resolver as a single problem.
4. Extends the resolved graph with a synthetic entry per workspace (`type: "workspace"`) so the lockfile and autoloader see them.
5. Fetches, materializes, and lays down the symlink structure (see [Vendor and Symlinks](./workspaces-vendor.md)).

Everything downstream of the resolver — fetch, extract, autoload — works exactly as in a single-project install. Workspaces are excluded from fetch and materialize (they have no dist to download).

## The install entry point (walk-up)

`gomposer install` and `gomposer update` walk up from the current working directory until they find a `composer.json` whose parsed `workspaces` field is non-empty:

1. **Match** → run from that directory. Same behavior whether you run from `./`, `./packages/shared/`, or `./apps/api/src/`.
2. **Filesystem root reached** → no walk-up match; run against the current directory in single-project mode. (Same as pre-workspaces gomposer.)
3. **Ancestor with `.git/` but no `workspaces`** → **stop**. This prevents an unrelated ancestor's workspace root from swallowing a nested single-project repo. Walk-up is scoped to the current git project.

## Single lockfile

`gomposer.lock` at the repo root records every resolved external package plus a `"type": "workspace"` entry per workspace. See [gomposer.lock](./gomposer-lockfile.md) for the schema.

Every workspace manifest's bytes contribute to the `manifestContentHash` (concatenated in workspace-name order), so any workspace-manifest edit invalidates the resolution cache. Adding or removing a workspace via a root manifest edit invalidates via the root bytes.

## Warm re-install

The second `gomposer install` on an unchanged tree hits the resolution cache and short-circuits the entire solve. The metadata prefetch is cancelled on cache-hit to avoid a spurious "warm N packages" wait. Warm re-installs are typically sub-100ms.

## Warnings

Platform / plugin warnings stream to stderr and re-run every install (they aren't persisted anywhere). If a workspace's `composer.json` declares an unsatisfied `ext-*` requirement, the warning fires on every install regardless of cache state.

## `--no-dev`

`--no-dev` propagates through the aggregate: every workspace's `require-dev` is excluded, and platform requirements are enforced strictly (mismatch → error, not warning). Same behavior as single-project mode.

## Backward compatibility

A project with no `workspaces` field goes through **the exact same code path as pre-workspaces gomposer**. `newPipelineState` short-circuits to `ps.workspaces = nil` and sets `ps.aggregateManifest` to point at the root manifest (identical pointer). There is no feature flag, no behavior branch, no measurable overhead.
