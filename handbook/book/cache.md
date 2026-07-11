# Cache paths

gomposer keeps everything project-external under one on-disk root:

| OS | Location |
|---|---|
| macOS | `~/Library/Caches/gomposer/` |
| Linux / other | `$XDG_CACHE_HOME/gomposer/` (falls back to `~/.cache/gomposer/`) |

Sub-directories:

| Path | Purpose |
|---|---|
| `store/` | Content-addressed zip store. Keyed by SHA-256 of the archive bytes; shared across every project. Files are only ever added, never mutated. |
| `packagist/http/` | HTTP response cache for Packagist v2 (`/p2/*.json`) — ETag-aware. |
| `packagist/parsed-v2/` | Parsed-response cache: the decoded `PackageMetadata` gob-encoded on disk. Bumped from `parsed/` to `parsed-v2/` when the metadata shape changed. |
| `vcs/` | Cloned VCS repositories for `repositories: [{type: "vcs"}]` entries. |
| `resolution-v2/` | Cached resolver results, keyed by manifest bytes + lock bytes + platform fingerprint. Bumped from `resolution/` after a schema change. |

Deleting any of them is safe; the next install will refill what it needs. The most common reason to clear a specific subdir is to force a re-fetch after an upstream metadata anomaly (e.g., a broken Packagist entry that's since been fixed).

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
