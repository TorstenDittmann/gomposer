# Workspaces

gomposer supports **pnpm/bun-style workspaces** for PHP monorepos: declare a set of directories as workspaces in the root `composer.json`, use a `workspace:*` protocol to link them to each other, and run one `gomposer install` at the root that produces a shared `vendor/` for the whole tree.

If you've used pnpm workspaces, yarn workspaces, or bun workspaces in the JS ecosystem, this will feel familiar. If you've been reaching for Composer path repositories or `symplify/monorepo-builder` to get the same effect in PHP, workspaces are the alternative.

## The shape

```
acme-monorepo/
├── composer.json              # { "workspaces": ["packages/*", "apps/*"] }
├── packages/
│   └── shared/composer.json   # { "name": "acme/shared", "version": "1.0.0" }
└── apps/
    └── api/composer.json      # { "require": { "acme/shared": "workspace:^1.0" } }
```

One `gomposer install` at the repo root produces:

```
acme-monorepo/
├── vendor/                    # real dir; aggregate of every workspace's external deps
│   └── acme/shared            # symlink → ../../packages/shared
├── packages/shared/vendor     # symlink → ../../vendor
└── apps/api/vendor            # symlink → ../../vendor
```

## Four things to read

Deep dives follow in this section:

- [Declaring Workspaces](./workspaces-declaring.md) — the `workspaces` field, glob syntax, and discovery rules.
- [The workspace: Protocol](./workspaces-protocol.md) — the `workspace:*` and `workspace:<constraint>` require syntax and how it's validated.
- [Vendor and Symlinks](./workspaces-vendor.md) — the shared `vendor/`, autoload aggregation, and the symlink layout.
- [Installing](./workspaces-install.md) — walk-up entry point, aggregate resolve semantics, warm re-install, and backward compat.

## Scope

**Scope 1** (shipped): discovery, `workspace:` protocol, aggregate resolve, symlink layout, single lockfile, CLI walk-up.

**Scope 2** (follow-up, not yet built):

- `--filter=<pkg>` for subset installs.
- `gomposer run <script> [--filter]` for topologically-ordered script execution.
- `workspace:./relative/path` variant (pin a workspace by path, not name).

If any of those matter for your workflow, please [open an issue](https://github.com/TorstenDittmann/gomposer/issues).

## Backward compatibility

Projects without a `workspaces` field install exactly as before. `gomposer install` in a plain single-project repo takes the same code path as pre-workspaces gomposer — no feature flag, no branch, no behavior drift.
