# Workspaces

gomposer supports **pnpm/bun-style workspaces** for PHP monorepos. The core idea: declare a set of directories as workspaces in the root `composer.json`, use a `workspace:*` protocol to link them to each other, and run one `gomposer install` at the root that produces a shared `vendor/` for the whole tree.

## Declaring workspaces

Add a top-level `workspaces` array to the root `composer.json`:

```json
{
    "name": "acme/monorepo",
    "workspaces": ["packages/*", "apps/*"]
}
```

Each entry is a glob pattern (evaluated by `filepath.Glob`). Every matched directory containing its own `composer.json` becomes a workspace. Empty matches emit a warning to stderr; missing `composer.json` inside a matched directory is silently skipped (accommodates sibling `README/` dirs, etc.).

Duplicate workspace names across matches are a hard error at install time.

## The `workspace:` protocol

Two forms are accepted in any workspace's `require` or `require-dev` block:

| Syntax | Semantics |
|---|---|
| `workspace:*` | Match the local workspace at any version. Never checks version. |
| `workspace:<constraint>` | Match the local workspace only if its declared `version` satisfies the constraint. |

`<constraint>` is any Composer-style constraint: `^1.0`, `~1.2`, `>=1.0 <2.0`, `1.2.3`.

Example — a workspace `acme/api` that depends on the local `acme/shared`:

```json
{
    "name": "acme/api",
    "require": {
        "acme/shared": "workspace:^1.0",
        "psr/log": "^3.0"
    }
}
```

Validation happens **before** the resolver runs:

- Target name not found in the workspace set → hard error.
- Target has no `version` field but the constraint isn't `workspace:*` → hard error.
- Target version doesn't satisfy the constraint → hard error naming requirer, target, and the version mismatch.

## Vendor layout

`gomposer install` at the root (or from any workspace subdirectory — see walk-up below) produces:

```
acme-monorepo/
├── composer.json              # { "workspaces": ["packages/*", "apps/*"] }
├── vendor/                    # real directory — everyone's external deps
│   └── acme/shared            # symlink → ../../packages/shared
├── packages/shared/
│   ├── composer.json          # { "name": "acme/shared", "version": "1.0.0", … }
│   ├── src/
│   └── vendor                 # symlink → ../../vendor
└── apps/api/
    ├── composer.json          # requires acme/shared: workspace:^1.0
    ├── src/
    └── vendor                 # symlink → ../../vendor
```

Every symlink is **relative**. Bootstrap code in any workspace continues to work unchanged:

```php
require __DIR__ . '/../vendor/autoload.php';
```

resolves through the symlink to the shared install. Autoload maps aggregate every workspace's PSR-4/classmap/files declarations, so `use Acme\Shared\Thing` works from any workspace.

## Aggregate install semantics

- **One resolve.** gomposer collects every workspace's external requires (root + workspaces, minus the `workspace:` entries), unions them into a virtual super-manifest, and hands that to the resolver as a single problem. Duplicate external requires with different constraints across workspaces are intersected via Composer's AND syntax — compatible constraints solve together, incompatible ones surface as a real PubGrub derivation naming both owners.
- **One lockfile.** `gomposer.lock` at the repo root records every resolved external package **plus** a `"type": "workspace"` entry per workspace (with `"source": {"type": "path", "url": "packages/shared"}`) — enough for a warm re-install to validate the workspaces without re-scanning.
- **One `vendor/`.** External packages are materialized into `vendor/<vendor>/<name>` at the repo root; workspaces are symlinks to their source directories.
- **Warnings are per-run, not persisted.** Platform / plugin warnings stream to stderr and re-run every install (the previous "cache warnings in the lock" approach was removed).

## Install entry point (walk-up)

`gomposer install` walks up from the current working directory until it finds a `composer.json` whose parsed `workspaces` field is non-empty:

- Match → run from that directory. Same behavior whether you run from `./`, `./packages/shared/`, or deeper.
- Filesystem root reached → no walk-up match, run against the current directory (single-project mode).
- Ancestor with `.git/` but no `workspaces` → **stop**. Prevents an unrelated ancestor's workspace root from swallowing a nested single-project repo.

`gomposer update` follows the same rule.

## Existing `vendor/` handling

The symlink pass replaces whatever exists at `packages/*/vendor/` and `apps/*/vendor/` with a relative symlink to the root `vendor/`. If a workspace previously had a real `vendor/` (from a pre-workspaces `composer install`), it's **destroyed** during the switch. This is called out in the design spec as an acceptable mutation; the contents are recreatable via `gomposer install`. If you want a safety guard here, please [open an issue](https://github.com/TorstenDittmann/gomposer/issues).

## Backward compatibility

Projects without a `workspaces` field install exactly as before. `gomposer install` in a plain single-project repo takes the same code path as pre-workspaces gomposer.

## Not in scope (Scope 1 vs Scope 2)

Scope 1 shipped: discovery, `workspace:` protocol, aggregate resolve, symlink layout, single lock, CLI walk-up.

Scope 2 follow-up (not yet built):

- `--filter=<pkg>` for subset installs.
- `gomposer run <script> [--filter]` for topologically-ordered script execution.
- `workspace:./relative/path` variant.

If any of those matter for your workflow, please open an issue.
