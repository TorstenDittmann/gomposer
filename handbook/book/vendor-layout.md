# Vendor Layout

gomposer writes the same `vendor/` layout Composer 2 does, so any project bootstrap that starts with `require __DIR__ . '/../vendor/autoload.php'` works unchanged.

## What lands in `vendor/`

```
vendor/
├── autoload.php                    # entry point
├── composer/
│   ├── autoload_classmap.php       # class → file map
│   ├── autoload_files.php          # files that run on autoload
│   ├── autoload_namespaces.php     # PSR-0 map
│   ├── autoload_psr4.php           # PSR-4 map
│   ├── autoload_real.php           # ComposerAutoloaderInit<hash>
│   ├── autoload_static.php         # PHP-5.6+ optimized static map
│   ├── ClassLoader.php             # vendored from Composer under MIT
│   ├── InstalledVersions.php       # runtime package metadata API
│   └── installed.php               # metadata array
└── <vendor>/<name>/                # one dir per resolved package
```

Generated files are covered by snapshots and runtime integration tests in `internal/autoload/`.

## Autoloader coverage

- **PSR-4** — full support, including multiple prefixes and multiple paths per prefix.
- **PSR-0** — full support (Composer treats this as legacy but still generates it).
- **Classmap** — PHP token-stream scanner (not regex). Correctly handles `class Foo` inside string literals, heredocs, and other edge cases regex scanners get wrong.
- **Files** — `require`-once files that fire on autoload load.
- **`exclude-from-classmap`** — Composer's glob dialect (`**/Tests/`, `**/*Test.php`) is honored.

## `InstalledVersions.php`

Composer's runtime `InstalledVersions` API exposes installed package names,
versions, references, types, and paths. gomposer generates this metadata for the
root project and every selected production, development, and workspace package.
Development filtering, multiple registered vendor trees, and the standard
lookup, range, raw-data, and reload methods are supported. Version ranges use
the installed version and aliases; `provide` and `replace` ranges are not yet
retained in `gomposer.lock`.

## The `ClassLoader.php` file

Vendored verbatim from Composer under its MIT license (see `internal/autoload/embedded/LICENSE`). This is the runtime autoloader users' code loads at bootstrap. gomposer would not benefit from re-implementing it and Composer's version is battle-tested.

## Warm-vendor fast path

After a successful extract, gomposer drops a `.composer-go-sha` file at the target package root containing the archive's SHA-256. On the next install, if the marker matches the locked SHA, the extract is skipped entirely (a single stat replaces a zip walk). This turns a fully-installed repeat install into a near-instant operation.

The marker is safe to delete manually; the next install will re-extract and rewrite it.

## Plugins

gomposer detects `type: "composer-plugin"` and `type: "composer-installer"` packages during resolve and emits **one warning per plugin** to stderr. The package's *code* is installed to `vendor/` normally — the warning is about plugin *execution*, which gomposer never does.

To silence the warning for a package you've reviewed:

```json
{
    "extra": {
        "gomposer": {
            "suppress-plugin-warnings": ["some/composer-plugin"]
        }
    }
}
```

`--allow-plugins` is accepted for Composer-CLI compatibility and is a **no-op** — gomposer never runs plugin code regardless of what you pass.
