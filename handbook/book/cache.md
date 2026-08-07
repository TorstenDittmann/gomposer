# Cache paths

gomposer keeps everything project-external under one on-disk root:

| OS | Location |
|---|---|
| macOS | `~/Library/Caches/gomposer/` |
| Linux / other | `$XDG_CACHE_HOME/gomposer/` (falls back to `~/.cache/gomposer/`) |

Sub-directories:

| Path | Purpose |
|---|---|
| `store/` | Content-addressed zip and expanded-package store. Keyed by SHA-256 of the archive bytes; shared across every project. Files are only ever added, never mutated. |
| `packagist/http/` | HTTP response cache for Packagist v2 (`/p2/*.json`) — ETag-aware. |
| `packagist/parsed/` | Parsed-response cache: decoded `PackageMetadata` gob-encoded on disk. |
| `vcs/` | Cloned repositories and parsed metadata for `repositories: [{type: "vcs"}]` entries. |
| `resolution/` | Cached resolver results, keyed by manifest bytes + lock bytes + platform fingerprint. |

Deleting any of them is safe; the next install will refill what it needs. Prefer the built-in commands over deleting directories manually:

```sh
gomposer cache                         # path, per-layer sizes, and total
gomposer cache dir                     # raw path for scripts and shell tools
gomposer cache clear                   # clear all layers
gomposer cache clear metadata          # clear Packagist HTTP + parsed metadata
gomposer cache clear store resolution  # clear multiple selected layers
```

The user-facing layers map to disk as follows:

| Layer | Directory | Contents |
|---|---|---|
| `store` | `store/` | Downloaded package archives and immutable expanded trees used by warm installs. |
| `metadata` | `packagist/` | Packagist HTTP responses and parsed metadata. |
| `resolution` | `resolution/` | Resolver results. |
| `vcs` | `vcs/` | VCS clones and metadata. |

The most common reason to clear a specific layer is to force a re-fetch after an upstream metadata anomaly, such as a broken Packagist entry that has since been fixed. Clearing is immediate and non-interactive; an unknown layer name fails before anything is removed.

## Per-project state

Inside a project, gomposer writes:

| Path | Purpose |
|---|---|
| `gomposer.lock` | The lockfile. See [gomposer.lock](./gomposer-lockfile.md) for schema notes. |
| `vendor/` | Standard Composer layout. |
| `vendor/<vendor>/<name>/.composer-go-sha` | Per-package marker that lets the extract phase short-circuit when the target already matches the locked SHA. Safe to delete; the extract will re-run and rewrite it. |

## Concurrency

Runs against a single project are not expected to overlap. Running two concurrent `gomposer install` calls against the same directory is not supported; they may fight over `gomposer.lock` and `vendor/`.

Runs against **different** projects can share the same cache root safely — the store is content-addressed, and the parsed and resolution caches are keyed by input hashes.
