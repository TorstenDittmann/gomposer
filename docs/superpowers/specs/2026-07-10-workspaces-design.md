# Workspaces (monorepo) design

## Motivation

PHP monorepos today either duplicate `vendor/` across every app or reach for niche tooling (Composer path repositories, `symplify/monorepo-builder`, custom scripts) to make cross-package linking work. The JS ecosystem's pnpm and bun converged on a clean shape — a single install at the repo root that aggregates every workspace's deps into a shared, deduplicated store, with in-repo packages linked to each other via a `workspace:*` protocol. gomposer's speed goals mean we're paying the cost of a fast installer; a workspaces model turns that speed into a genuine developer-experience win at repo scale.

## Scope

**Scope 1 (this spec):** discovery, aggregation, `workspace:*` / `workspace:<constraint>` protocol, single-install-at-root semantics, symlinked cross-workspace edges, single lockfile.

**Scope 2 (follow-up spec):** `--filter=<pkg>` for subset installs; `gomposer run <script> [--filter]` for topologically-ordered cross-workspace script execution.

## Non-goals

- **`workspace:./relative/path` protocol variant.** Pnpm and bun both support pinning by path (not name); dropped from the POC.
- **Per-workspace install semantics** (`cd packages/shared && gomposer install` scoped to just that workspace). Bun does full-workspace installs from anywhere; pnpm scopes to the current package. We follow bun — see CLI section.
- **Publishing workflow.** `gomposer publish`-style tooling is out of scope; workspaces are a local-dev feature.
- **Nested workspaces** (workspaces within workspaces). Root has one `workspaces` field; workspaces do not declare their own.
- **Change detection / affected-graph.** Scope 2 might grow this; not now.

## Discovery

A **workspace root** is a directory whose `composer.json` contains a top-level `"workspaces"` array of glob patterns:

```json
{
    "name": "acme/monorepo",
    "workspaces": ["packages/*", "apps/*"]
}
```

- Glob expansion uses `filepath.Glob`. Each matched entry must be a directory containing a `composer.json` — non-directories and dirs without a manifest are silently skipped (they may be README-only sibling dirs).
- Glob patterns that match zero dirs emit **one warning line to stderr** and installation proceeds.
- `"workspaces"` may also be an empty array, which is equivalent to omitting the field — the project is a single-manifest install exactly as today.

Every workspace's `composer.json` is loaded; the collection is:

```
workspaces = {
    <name>: {
        Dir:      "packages/shared",   // repo-relative
        Manifest: *manifest.Manifest,
        Version:  "1.0.0",             // from manifest.Version, may be ""
    },
    ...
}
```

Two workspaces sharing the same `name` → hard error at load time: `workspaces: duplicate name "acme/thing" at <dir-A> and <dir-B>`.

## `workspace:` protocol

Recognized in any workspace's `require` / `require-dev` map (root's requires may use it too):

| Syntax | Semantics |
|---|---|
| `workspace:*` | Match the local workspace at any version. Always resolves. |
| `workspace:^1.0`, `workspace:~1.2`, `workspace:1.2.3` | Match the local workspace only if its declared `version` satisfies the constraint. If the workspace has no `version` field, fail with a clear error. |
| `workspace:>=1.0 <2.0`, other combinators | Same rules as above — parse the constraint, evaluate against the target workspace's `version`. |

The protocol is a **constraint parser addition**. `constraint.Parse` gains a case: input starting with `workspace:` strips the prefix, parses the tail as a normal constraint, and tags the returned `Constraint` with an `IsWorkspace bool` flag.

At aggregate time (below), every `workspace:` require is validated: if the required name isn't in the workspace set → hard error (`workspaces: workspace:… require "acme/x" not found in workspace set`). If the constraint tail doesn't satisfy the target workspace's `version` → hard error.

## Aggregate resolve

1. Build a virtual **super-manifest**:
   - `Require` = union of root's `Require` and every workspace's `Require`, minus every `workspace:*`/`workspace:<constraint>` entry (those are handled locally, not fetched from a registry).
   - `RequireDev` = union of same, subject to `--no-dev`.
   - Duplicate keys (same package required by two workspaces): if constraints are compatible via intersection, take the intersection. If they conflict, let the resolver report the conflict — the PubGrub derivation already blames the specific packages.
2. Run the existing resolver against the super-manifest. The result is a resolved graph of external packages.
3. Extend the result with workspace entries: for each workspace, add a synthetic resolved package `{Name: workspace.Name, Version: workspace.Version, Type: "workspace", InstallPath: workspace.Dir}`. This lets the lockfile record them so warm re-installs can validate.
4. Metadata prefetch (Stage 3 Plan 4's follow-up landed on `main`): the workspace names are excluded from the prefetch warm set — no HTTP for local packages.

## Install & vendor layout

**Single real `vendor/` at repo root.** Materialization for external packages is unchanged: content-addressed store → symlink/copy into `vendor/<vendor>/<name>`.

**Cross-workspace packages** appear in `vendor/<vendor>/<name>` as **symlinks** to the workspace source directory. So `vendor/acme/shared` is a relative symlink pointing at `../../packages/shared`. If a workspace's `composer.json` declares autoload paths (`psr-4`, `classmap`, `files`), the root autoloader picks them up as if the workspace were an installed package.

**Per-workspace `vendor/` becomes a symlink** to the repo-root `vendor/`. So bootstrap code in any workspace can `require __DIR__ . '/../vendor/autoload.php'` and resolve to the shared install.

- Repo-root `vendor/`: real directory.
- `packages/shared/vendor/` → symlink → `../../vendor/`.
- `apps/api/vendor/` → symlink → `../../vendor/`.

If a workspace's `vendor/` exists as a real directory (from a pre-workspaces install), gomposer **replaces it with a symlink**. This is a real filesystem mutation and worth mentioning in the CLI copy. Backup or refuse is not planned for the POC — the deletion is the same class of action as `gomposer install` doing when a package's dist changes.

**Autoloader aggregation.** The autoload generator already builds a single map from a package set. Workspaces feed into that set as ordinary entries with `InstallPath = "vendor/<vendor>/<name>"` — the symlink path, not the source path. This keeps every emitted autoload entry uniform in shape (whether it's a fetched external package or a workspace symlink) and matches how Composer's own path-repository install serializes. `vendor/acme/shared/src` reads through the symlink at runtime with zero overhead. No new autoload code path.

## Lockfile

Single `gomposer.lock` at repo root. The existing schema (Stage 1's `gomposer.lock`, not the abandoned Composer-compat shape) already carries per-package `Type` and `Source` fields; workspaces get:

```json
{
    "name": "acme/shared",
    "version": "1.0.0",
    "type": "workspace",
    "source": { "type": "path", "url": "packages/shared", "ref": "" },
    "dist": { "type": "", "url": "", "sha256": "" },
    "require": { ... }
}
```

A warm re-install verifies each `type: "workspace"` entry by checking the directory still exists and its `composer.json` still parses; if either fails, force a re-resolve. The `manifestContentHash` at the top of the lockfile hashes the root manifest bytes AND every workspace's manifest bytes concatenated in workspace-sort-order (deterministic), so any workspace's manifest edit invalidates the cache.

## CLI

`gomposer install` gets a **workspace-root walk-up**:

1. If `<cwd>/composer.json` has a `"workspaces"` field, that's the root — install from there.
2. Else, walk up from CWD until we find a `composer.json` with `"workspaces"` (stop at filesystem root or a `.git` directory boundary).
3. If nothing found, behave exactly as today: install from CWD.

`gomposer update` follows the same walk-up rule.

**No new CLI flags in Scope 1.** `--filter=<pkg>` and `gomposer run` are Scope 2.

Verbose output (`-v`):
- Timing block already prints `read manifest`, `resolve`, `fetch`, `materialize`, etc. Add one line: `workspaces  <n> discovered  <ms>` — populated only when the workspace root has a non-empty `workspaces` field.
- Under `read manifest`, count workspace manifests as separate reads.

Error handling — every failure mode surfaces a clear message and non-zero exit:
- Glob no-matches → warning to stderr, install continues.
- Duplicate workspace names → hard error.
- Unknown workspace name in `workspace:*` → hard error (`workspaces: workspace:* references unknown workspace "acme/x"`).
- Version mismatch on `workspace:<constraint>` → hard error naming the requirer, the target, and the versions involved.
- Workspace declared version but `workspace:^1.0` requirer's constraint fails → hard error at pre-resolve.
- Workspace dir declared in `workspaces` but no `composer.json` inside → silent skip (matches the "sibling README dir" case).

## File structure

| Path | Responsibility |
|------|---------------|
| `internal/manifest/manifest.go` | Add `Workspaces []string` to the parsed `Manifest`. |
| `internal/manifest/workspaces.go` | New. `DiscoverWorkspaces(rootDir string, root *Manifest) ([]Workspace, error)` — glob expansion, load each workspace manifest, dedup check. |
| `internal/manifest/workspaces_test.go` | New. Fixture-driven tests for glob expansion, dedup, missing dirs. |
| `internal/constraint/constraint.go` | Recognize `workspace:` prefix in `Parse`; carry an `IsWorkspace bool` on the returned `Constraint`. |
| `internal/constraint/constraint_test.go` | New cases for `workspace:*`, `workspace:^1.0`, etc. |
| `internal/orchestrator/workspace_aggregate.go` | New. Build the super-manifest from root + workspaces; validate every `workspace:` require; produce the aggregate `Manifest` fed to the resolver, plus a `map[string]Workspace` for later linking. |
| `internal/orchestrator/workspace_aggregate_test.go` | New. Validate aggregate build + error messages. |
| `internal/orchestrator/pipeline.go` | Walk-up to workspace root; call `DiscoverWorkspaces`; feed aggregate manifest to resolve; after materialize, run the symlink pass. |
| `internal/orchestrator/workspace_symlink.go` | New. Per-workspace vendor symlink + per-workspace-package cross-link. Guarded by "workspace mode active". |
| `internal/orchestrator/workspace_symlink_test.go` | New. Symlink layout assertions on a temp-dir fixture project. |
| `internal/cli/root.go` | Walk-up-to-workspace-root logic when `--project` isn't set. |
| `docs/superpowers/plans/2026-07-10-workspaces.md` | Implementation plan (next step). |

## Test plan (spec-level)

Beyond the unit-test coverage in the file table above, the acceptance integration is one fixture project under `internal/orchestrator/testdata/workspaces-simple/`:

```
workspaces-simple/
├── composer.json                # { "name": "acme/monorepo", "workspaces": ["packages/*", "apps/*"] }
├── packages/
│   └── shared/
│       ├── composer.json        # { "name": "acme/shared", "version": "1.0.0", "autoload": { "psr-4": { "Acme\\Shared\\": "src/" } } }
│       └── src/Thing.php
└── apps/
    └── api/
        ├── composer.json        # { "name": "acme/api", "require": { "acme/shared": "workspace:^1.0" } }
        └── src/App.php
```

Assertions after `gomposer install`:
- `workspaces-simple/vendor/` is a real dir with `autoload.php`, `composer/`, etc.
- `workspaces-simple/vendor/acme/shared` is a relative symlink → `../../packages/shared`.
- `workspaces-simple/packages/shared/vendor` and `workspaces-simple/apps/api/vendor` are symlinks → `../../vendor`.
- `require 'vendor/autoload.php'` from either workspace resolves `Acme\Shared\Thing`.
- `workspaces-simple/gomposer.lock` contains `acme/shared` and `acme/api` as `type: "workspace"` entries.
- Re-running `gomposer install` is a warm-lock no-op (same lockfile, all symlinks intact).
- Deleting `packages/shared/vendor` (a symlink) and re-running restores it.

## Related follow-ups (not this spec)

- **Filter (`--filter=<pkg>`).** Restrict install to a workspace and its transitive deps.
- **`gomposer run <script> [--filter]`.** Topologically-ordered execution across workspaces.
- **`workspace:./relative/path`.** Pin by path, not name.
- **Publish workflow.** Automated `composer publish` from a workspace with version bumping.
- **Change detection.** Given a git ref, compute the set of workspaces whose deps or code changed and pass to `--filter` automatically.
