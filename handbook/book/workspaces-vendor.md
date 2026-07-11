# Vendor and Symlinks

Every workspace's external dependencies are installed into a **single shared `vendor/`** at the repo root, with symlinks tying each workspace to it and to its cross-workspace peers.

## The layout after install

```
acme-monorepo/
├── composer.json
├── vendor/                    # real dir; aggregate of every workspace's external deps
│   ├── autoload.php
│   ├── composer/…
│   ├── psr/log/               # external dep, fetched normally
│   ├── monolog/monolog/       # external dep, fetched normally
│   └── acme/shared            # symlink → ../../packages/shared
├── packages/shared/
│   ├── composer.json
│   ├── src/
│   └── vendor                 # symlink → ../../vendor
└── apps/api/
    ├── composer.json
    ├── src/
    └── vendor                 # symlink → ../../vendor
```

Every symlink emitted by gomposer is **relative**, computed via `filepath.Rel`.

## Two symlinks per workspace

For each workspace X at `<X-dir>`:

1. **`<X-dir>/vendor` → repo-root `vendor/`.** Bootstrap files like `require __DIR__ . '/../vendor/autoload.php'` continue to work; the symlink resolves through to the shared install.
2. **`vendor/<vendor>/<name>` → `<X-dir>`.** So other workspaces can autoload X's classes via the standard autoload path.

## Autoload aggregation

The autoload generator treats every workspace as a first-class package entry with:

- `Name` = workspace name (`acme/shared`).
- `Version` = workspace's declared `version` (may be empty for `workspace:*`-only cases).
- `InstallPath` = `vendor/<vendor>/<name>` — the symlink path.
- `Autoload` = whatever the workspace's own `composer.json` declares.

The emitted PSR-4 / classmap / files entries reference `vendor/<vendor>/<name>/…`, which reads through the symlink at runtime with zero overhead. So `use Acme\Shared\Thing` works from any workspace exactly as if `acme/shared` were a Packagist-published package.

## Symlinks are idempotent

Running `gomposer install` twice produces the same result. `linkWorkspaces` (the internal pass that lays down the symlinks) unlinks whatever exists at each target before writing the new symlink, so re-runs are safe.

## Migrating from a real `vendor/`

If a workspace previously had a **real** `vendor/` populated by `composer install` — potentially with megabytes of extracted dependencies — the symlink pass **destroys it** during the switch and replaces it with the symlink to the root vendor.

The design spec calls this out as an acceptable mutation for the POC: the contents are recreatable and the direction of migration is what you asked for by declaring workspaces. If a safety guard on this matters for your workflow (e.g. refuse without a `--force` flag), please [open an issue](https://github.com/TorstenDittmann/gomposer/issues).

## `.gitignore`

Per-workspace `vendor/` symlinks and the repo-root `vendor/` should be gitignored the same way `vendor/` always is in Composer projects:

```gitignore
/vendor/
/packages/*/vendor
/apps/*/vendor
```

Or just `vendor` at the top of a root `.gitignore` (matches recursively).
