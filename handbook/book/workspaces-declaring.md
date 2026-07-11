# Declaring Workspaces

Workspaces are declared by a top-level `workspaces` array in the root `composer.json`:

```json
{
    "name": "acme/monorepo",
    "workspaces": ["packages/*", "apps/*"]
}
```

Each entry is a glob pattern evaluated by Go's `filepath.Glob` — the same syntax `find` and shell globs use, without recursive `**`.

## What gets discovered

For each pattern, gomposer looks at every matched directory. A directory becomes a workspace when it contains its own `composer.json`.

- **Empty glob match** (pattern matches zero directories) → **warning** to stderr; install proceeds.
- **Non-directory match** (e.g. glob matched a file) → silently skipped.
- **Directory without `composer.json`** (a README-only sibling, an assets dir, etc.) → silently skipped.
- **`composer.json` without a `name` field** → **hard error** at install time.

## Duplicate names

Two workspaces claiming the same name (`{"name": "acme/thing"}` in both `packages/a/composer.json` and `packages/b/composer.json`) is a **hard error** at install time. The error names both directories so you can pick which one to rename.

## Version

A workspace's `version` field is only required when another workspace requires it via a constrained `workspace:<constraint>` (e.g. `workspace:^1.0`). Workspaces required only via `workspace:*` don't need a version. See [The workspace: Protocol](./workspaces-protocol.md).

## Ordering

The order of workspaces in the discovery output is stable and independent of filesystem quirks — matches within each glob pattern are sorted, and patterns are processed in the order the root `workspaces` array lists them. This matters for:

- The `manifestContentHash` in `gomposer.lock` (workspace manifests are concatenated in name order — see [gomposer.lock](./gomposer-lockfile.md)).
- Error messages that name multiple workspaces.

## Nesting

Workspaces do not declare their own `workspaces`. Only the root manifest can. A `workspaces` field on a workspace's own `composer.json` is ignored.

## Empty or absent `workspaces`

`"workspaces": []` is equivalent to omitting the field entirely. The project runs in single-project mode; no walk-up, no aggregate manifest, no symlink pass. See [Workspaces Overview](./workspaces.md#backward-compatibility).

## Example trees

### Flat: everything under `packages/`

```
composer.json                     # { "workspaces": ["packages/*"] }
packages/
├── core/composer.json
├── shared/composer.json
└── utils/composer.json
```

### Split by role

```
composer.json                     # { "workspaces": ["packages/*", "apps/*"] }
packages/
├── shared/composer.json
└── ui/composer.json
apps/
├── admin/composer.json
├── api/composer.json
└── worker/composer.json
```

### Selective (README dirs and assets ignored)

```
composer.json                     # { "workspaces": ["packages/*"] }
packages/
├── shared/composer.json           # workspace
├── docs/                          # no composer.json → silently skipped
├── README.md                      # not a dir → silently skipped
└── ui/composer.json               # workspace
```
