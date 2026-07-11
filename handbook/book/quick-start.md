# Quick Start

A five-minute tour. Assumes you've followed [Installation](./installation.md) and `gomposer` is on your `PATH`.

## A fresh project

```sh
mkdir hello-gomposer && cd hello-gomposer
cat > composer.json <<'JSON'
{
    "name": "acme/hello",
    "require": {
        "psr/log": "^3.0",
        "monolog/monolog": "^3.0"
    }
}
JSON

gomposer install
```

You get a `vendor/` directory with Composer's standard layout, an autoloader at `vendor/autoload.php`, and a `gomposer.lock` recording the exact resolved versions.

## An existing Composer project

Point gomposer at any `composer.json` a Composer 2 project would install cleanly with:

```sh
cd path/to/your/project
gomposer install
```

gomposer reads `composer.json` and writes `vendor/` + `gomposer.lock`. It does not touch `composer.lock` — the two lockfiles are independent, so you can run Composer alongside gomposer safely.

## What just happened

Under `-v` you'll see the pipeline in detail (see [Verbose Output](./verbose-output.md)). Without `-v`, the install just prints platform-requirement warnings (if any) followed by a single "installed N packages in Xms" line.

The high-level flow:

1. Parse `composer.json`.
2. Discover workspaces (skipped if there's no `workspaces` field — see [Workspaces](./workspaces.md)).
3. Resolve dependencies via PubGrub. Metadata prefetch runs in parallel.
4. Fetch every needed zip into the content-addressed store. Artifact prefetch primes the pool.
5. Materialize (extract) into `vendor/`. Per-package marker skips extract when target already matches the locked SHA.
6. Generate autoloader files (`autoload.php`, `autoload_*.php`, `installed.php`).
7. Write `gomposer.lock`.
8. Fire the `post-install-cmd` script if defined.

## Updating

`gomposer install` uses the existing `gomposer.lock` when it's present and matches the manifest. To re-resolve everything from the current registry state:

```sh
gomposer update
```

`update` ignores the current lock, runs a fresh solve, and writes a new lockfile. `install` from that point returns the same graph.

## Next steps

- The [CLI Reference](./cli-reference.md) lists every flag on `install` and `update`.
- [Reading composer.json](./reading-composer-json.md) covers exactly which fields gomposer honors.
- If you're setting up a monorepo, jump to the [Workspaces Overview](./workspaces.md).
