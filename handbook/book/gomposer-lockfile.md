# gomposer.lock

gomposer keeps its own lockfile, `gomposer.lock`, at the project root. It's an intentional design choice — this page explains what's in it and how it interacts with a `composer.lock` that may already exist alongside it.

## Independent from `composer.lock`

gomposer does **not** read `composer.lock`, and Composer does not read `gomposer.lock`. If both files exist they are independent — you can safely run Composer alongside gomposer on the same project.

The one-way write-compat direction (make gomposer emit `composer.lock`) was prototyped and abandoned in June 2026. See [Contributing](./contributing.md#how-this-project-makes-decisions) for the design docs that captured why.

## Schema

A `gomposer.lock` is JSON with 2-space indent, sorted keys, deterministic across runs. Top-level shape:

```json
{
    "schemaVersion": 1,
    "generator": { "name": "gomposer", "version": "0.1.0" },
    "manifestContentHash": "sha256:…",
    "platformFingerprint": "php-8.2.x;ext-mbstring;ext-json;…",
    "stability": {
        "minimumStability": "stable",
        "preferStable": true
    },
    "packages": [ … ],
    "packagesDev": [ … ],
    "aliases": [ … ]
}
```

## `manifestContentHash`

A SHA-256 over the raw `composer.json` bytes, plus every workspace manifest's bytes concatenated in workspace-name order (when the root declares `workspaces`). Any manifest edit anywhere in the tree invalidates the resolution cache and forces a fresh solve.

This is `gomposer.lock`'s own concept, not Composer's `content-hash`. If you're used to Composer's algorithm (MD5 of a specific field subset with PHP-style JSON encoding), the two are unrelated.

## `platformFingerprint`

A stable string encoding the runtime PHP version plus loaded extensions. Changing PHP versions or loading a new extension invalidates the resolution cache.

## `packages` and `packagesDev`

Every resolved package appears once. Per-entry shape:

```json
{
    "name": "monolog/monolog",
    "version": "3.5.0",
    "type": "library",
    "source": { "type": "git", "url": "https://…", "ref": "abc" },
    "dist":   { "type": "zip", "url": "https://…", "sha256": "…" },
    "require": { "php": ">=8.1", "psr/log": "^1|^2|^3" },
    "autoload": { "psr-4": { "Monolog\\": "src/Monolog" } },
    "suggest": { "graylog2/gelf-php": "…" }
}
```

- `dist.sha256` is the archive SHA-256 gomposer verified during download. It's also the key used to look the archive up in the content-addressed store.
- `require` records the transitive constraints (used by the resolver, not by the fetcher).
- `autoload` and `suggest` are copied verbatim from the registry.

## Workspaces in the lock

When the project declares `workspaces`, each workspace also appears as a `type: "workspace"` entry:

```json
{
    "name": "acme/shared",
    "version": "1.0.0",
    "type": "workspace",
    "source": { "type": "path", "url": "packages/shared", "ref": "" },
    "dist":   { "type": "", "url": "", "sha256": "" },
    "autoload": { "psr-4": { "Acme\\Shared\\": "src/" } }
}
```

`type: "workspace"` entries are excluded from fetch and materialize; they exist in the lock so warm re-installs can validate the workspace still exists and pick up its autoload contribution. See [Workspaces](./workspaces.md).

## Warm-install semantics

On `gomposer install`:

1. Read `gomposer.lock` if it exists.
2. If the `manifestContentHash` matches the current manifest bytes, use the lock's resolved graph directly — no resolver work.
3. Otherwise re-resolve and rewrite the lock. `gomposer update` skips step 2 unconditionally.

## Committing to git

Commit `gomposer.lock`. It's the mechanism that gives your team reproducible installs. `composer.lock` also belongs in git if the project is going to be installed by Composer too.
