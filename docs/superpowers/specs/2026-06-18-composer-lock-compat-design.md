# composer.lock write-compat design

## Motivation

gomposer today emits a bespoke `gomposer.lock` file that Composer cannot read. Users who already have a `composer.lock` end up with two lockfiles in git and one CI system won't understand the other. Cross-tool interop is the point of Composer compatibility — the lockfile is the last remaining seam.

This spec covers **one-way write compatibility**: gomposer writes `composer.lock` in the exact schema Composer expects, and drops `gomposer.lock`. Round-trip fidelity (preserving arbitrary fields a future Composer version may add) is out of scope for this pass; if the user runs `composer install` after gomposer, Composer rewrites the file in its full glory, and next time gomposer reads it we handle it.

## Non-goals

- Full round-trip preservation of Composer's optional per-package metadata (`authors`, `license`, `description`, `keywords`, `homepage`, `funding`, `support`). We omit these entirely; Composer accepts locks without them.
- Composer's `--dry-run` output parity.
- Reading Composer's `platform-overrides` and using them to override the platform probe. We serialize the field when present, but don't act on it in this pass.
- Reading `plugin-api-version` and enforcing it. Serialized as `2.6.0` (matching the Composer 2.x major line), not verified.
- A migration tool that converts `gomposer.lock` → `composer.lock`. Users regenerate via one `gomposer update`.

## Scope

Three chunks, executed together:

1. **Schema swap** in `internal/lock/lock.go` — replace our shape with Composer's on-disk shape.
2. **Content-hash** in `internal/manifest/contenthash.go` — port Composer's `Locker::getContentHash` byte-for-byte.
3. **Read/write plumbing** across the orchestrator, resolver adapter, packagist registry, bench tool, and CLI copy.

## Chunk 1 — schema

New `lock.File`:

```go
type File struct {
    Readme            []string          `json:"_readme,omitempty"`
    ContentHash       string            `json:"content-hash"`
    Packages          []Package         `json:"packages"`
    PackagesDev       []Package         `json:"packages-dev"`
    Aliases           []Alias           `json:"aliases"`
    MinimumStability  string            `json:"minimum-stability"`
    StabilityFlags    map[string]int    `json:"stability-flags"`
    PreferStable      bool              `json:"prefer-stable"`
    PreferLowest      bool              `json:"prefer-lowest"`
    Platform          map[string]string `json:"platform"`
    PlatformDev       map[string]string `json:"platform-dev"`
    PlatformOverrides map[string]string `json:"platform-overrides,omitempty"`
    PluginAPIVersion  string            `json:"plugin-api-version,omitempty"`
}
```

New `lock.Package`:

```go
type Package struct {
    Name            string            `json:"name"`
    Version         string            `json:"version"`
    Type            string            `json:"type,omitempty"`
    Source          Source            `json:"source,omitempty"`
    Dist            Dist               `json:"dist,omitempty"`
    Require         map[string]string `json:"require,omitempty"`
    Autoload        map[string]any    `json:"autoload,omitempty"`
    NotificationURL string            `json:"notification-url,omitempty"`
    Time            string            `json:"time,omitempty"`
}

type Source struct {
    Type      string `json:"type"`
    URL       string `json:"url"`
    Reference string `json:"reference"` // renamed from Ref
}

type Dist struct {
    Type      string `json:"type"`
    URL       string `json:"url"`
    Reference string `json:"reference,omitempty"`
    Shasum    string `json:"shasum"` // renamed from Sha256
}
```

`Alias` shape (`{package, version, alias}`) already matches Composer's — unchanged.

Fields we drop from the old struct:

- `SchemaVersion`, `Generator{Name, Version}` — Composer doesn't emit these.
- `ManifestContentHash` — replaced by `ContentHash` at the top level.
- `PlatformFingerprint` — moves to the resolution cache key (already computed there, this field was redundant).
- `Stability` nested struct — flattened into `MinimumStability` + `PreferStable`.
- `Warnings []string` — Composer has no such field. Warnings become stderr-only output during resolution.

### Deterministic serialization

`Encode` continues to write:

- 2-space indent
- `SetEscapeHTML(false)`
- No trailing newline (matches Composer's `Locker::save` — Composer writes `PHP_EOL` at end, which is `\n` on Linux/macOS; we already match).
- Map keys sorted alphabetically (Go's `encoding/json` default).

## Chunk 2 — content-hash

New file `internal/manifest/contenthash.go`, one exported function:

```go
// ContentHash mirrors Composer's Locker::getContentHash: take the raw
// composer.json bytes, decode to a map, keep only the "relevant" subset,
// re-encode with sorted keys and unescaped slashes, and MD5 the result.
// Composer's install path uses this to detect stale locks.
func ContentHash(manifestBytes []byte) (string, error)
```

Algorithm (see Composer's `src/Composer/Package/Locker.php::getContentHash`):

1. `json.Unmarshal(manifestBytes, &raw)` into `map[string]any`.
2. Filter to the exact key allowlist Composer uses:
   `name`, `version`, `require`, `require-dev`, `conflict`, `replace`, `provide`, `minimum-stability`, `prefer-stable`, `repositories`, `extra`.
3. Composer also carries `config.platform` into the hash when present. Handle it: if `raw["config"]` is an object and contains a `platform` key, add `{"config": {"platform": <that value>}}` into the filtered map.
4. Re-encode with sorted keys (Go's default for maps), then post-process to match PHP's default `json_encode` byte-for-byte:
   - **Slash escaping.** PHP escapes every `/` in string values as `\/`; Go never does. After `json.Marshal`, run a byte-level substitution `/ → \/`. This is safe because Go's encoder does not emit `\/` anywhere (so the transform can't double-escape).
   - **Non-ASCII escaping.** PHP's default `json_encode` escapes every rune outside the ASCII range as `\uXXXX`; Go emits raw UTF-8. Walk the encoded bytes, and for every multi-byte UTF-8 sequence replace it with the corresponding `\uXXXX` (or surrogate pair for code points ≥ U+10000). Necessary because `extra` is user-controlled and can contain any Unicode.
   - **HTML escaping stays disabled** (`SetEscapeHTML(false)`) — Composer doesn't escape `<`, `>`, `&` in the content-hash input either.
5. `md5.Sum(bytes)` → hex-encode → lowercase (Go's `encoding/hex` already returns lowercase).

The transforms in step 4 live in a small helper `phpCompatibleJSON([]byte) []byte` next to `ContentHash`; both are unit-tested independently against known-hash fixtures.

### Verification

Fixture-driven test:

- Under `internal/manifest/testdata/contenthash/`, drop 3–5 real `composer.json` files (Laravel skeleton, Symfony skeleton, Monolog, our fixture-project, one small hand-crafted case). For each, run Composer once locally to produce the reference `content-hash` and pin it as the expected string in the test table.
- The test isn't run against live Composer in CI — the expected hashes are baked strings. Regeneration requires re-running Composer manually and updating the constants, akin to `WRITE_EXPECTED=1` in the autoload snapshot tests.

Edge cases the tests must cover:

- Manifest with none of the relevant keys (`{"name": "x/y", "type": "library"}`) — filter yields `{}`, MD5 of `{}` = `99914b932bd37a50b983c5e7c90ae93b`.
- Empty `require: {}` vs missing `require` — Composer treats these as equivalent; both encode to `"require":{}`. Confirm.
- Unicode in `description` — irrelevant (description isn't in the allowlist).
- Trailing `\n` in manifest bytes — irrelevant (we decode first).

## Chunk 3 — plumbing

### Orchestrator (`internal/orchestrator/pipeline.go`)

- Read `<projectDir>/composer.lock` on the read path. If absent, resolve from scratch.
- Write to `<projectDir>/composer.lock` via temp file + atomic rename. Retain the `.tmp` sibling name and cleanup on failure.
- **`gomposer.lock` purge:** every mention of `gomposer.lock` is removed from the codebase — CLI help strings, error messages, doc comments, README, in-tree specs, tests, bench harness. No fallback read. No warning if the file exists. Users are expected to `rm gomposer.lock` after upgrading; we do not touch their working copy.
- **Resolution cache key** already includes lock bytes (`internal/orchestrator/cachekey.go`); a schema-changed lock naturally yields a new key so no cache eviction step is needed. To be safe, rename the cache subdir from `resolution` to `resolution-v2` so leftover pre-migration entries can't collide.

### Resolver adapter (`internal/resolver/adapter.go`)

Feeds resolver results into the new `lock.File` shape.

- `NotificationURL`: hard-code `https://packagist.org/downloads/` for entries whose `Record.Source.Type == "git"` and whose canonical registry is Packagist. Empty otherwise. (Precise-source detection: registry adapter tags each `registry.PackageVersion` with the source type it came from — see below.)
- `Time`: read from `registry.PackageVersion.Time` (new field; see registry section).
- `StabilityFlags`: for each entry in `manifest.Require` and `manifest.RequireDev` whose parsed constraint has a non-empty `StabilityFlag`, record `{name → rank}`. Ranks: `dev=20`, `alpha=15`, `beta=10`, `RC=5`, `stable=0` (Composer's `BasePackage::STABILITY_*` constants).
- `Platform`/`PlatformDev`: filter `manifest.Require` / `manifest.RequireDev` through `platform.IsPlatformReq`; keep the raw constraint string as the value.
- `Readme`: emit the exact Composer three-line boilerplate:
  ```
  This file locks the dependencies of your project to a known state
  Read more about it at https://getcomposer.org/doc/01-basic-usage.md#installing-dependencies
  This file is @generated automatically
  ```
- `PluginAPIVersion`: `"2.6.0"` (Composer 2.6 baseline; picked as a well-known non-controversial value).
- `PreferLowest`: `false` for now — we don't yet accept `--prefer-lowest`.
- `PlatformOverrides`: omitted (empty map serialized as absent because of `omitempty`).
- `ContentHash`: passed in from the orchestrator, which computes it via `manifest.ContentHash(manifestBytes)`.

### Packagist registry (`internal/registry/packagist/packagist.go`)

Add `Time` to `v2Version`, `registry.PackageVersion`, and the decode path:

```go
type v2Version struct {
    // ...existing fields...
    Time string `json:"time"`
}
```

This is a pure additive change; the parsedcache entry shape changes so we bump the parsedcache path from `parsed` to `parsed-v2` to force refetch. (Trivial: change one string constant in `packagist.go`.)

### Bench tool (`cmd/bench/runner.go`)

Change the cold-scenario file removal list from `["vendor", "composer.lock", "gomposer.lock"]` to `["vendor", "composer.lock"]`. `gomposer.lock` no longer exists so removing it is a no-op, but keeping the reference would be confusing.

### CLI copy edits

- `internal/cli/install.go` `Short`: "Install dependencies into vendor/ from composer.json (using composer.lock if present)".
- `internal/cli/update.go` `Short`: "Re-resolve all dependencies and rewrite composer.lock + vendor/".
- `internal/cli/root.go` `Long`: "gomposer installs PHP packages described in composer.json. It reads and writes the standard composer.lock."
- README `## Composer compatibility`: replace the "does not read `composer.lock`" bullet with "reads and writes `composer.lock` (one-way write compat: gomposer emits a valid file; Composer can consume it. Composer may add optional fields on its next run — that's expected)".
- Design spec `docs/superpowers/specs/2026-05-07-gomposer-design.md`: update the "Lockfile format" section to reference this new spec.

### Tests

Roughly the following test files touch lock shape or fields:

- `internal/lock/lock_test.go` — full rewrite.
- `internal/orchestrator/pipeline_test.go`, `orchestrator_test.go`, `prefetch_test.go`, `scripts_test.go`, `progress_test.go`, `bench_prefetch_test.go`, `timing_test.go` — small mechanical changes to any `lock.File` literals and `gomposer.lock` string references.
- `internal/resolver/adapter_test.go` — adapt to the new shape and validate the new fields (notification-url, time, stability-flags, platform).
- `internal/registry/packagist/packagist_test.go` — add `time` to the fixture JSON, assert it flows through.
- `internal/manifest/contenthash_test.go` — new file, described above.
- `cmd/bench/runner_test.go` — remove `gomposer.lock` from the fixture assertions.

## Verification (post-implementation)

- `go test ./...` green on ubuntu-latest and macos-latest via the existing CI.
- Manual e2e: `gomposer install` on our fixture-project produces a `composer.lock` whose `content-hash` matches the value upstream Composer computes for the same `composer.json`. (Do this once during implementation to sanity-check the algorithm.)
- Cross-tool e2e: on a real project (Laravel skeleton), `gomposer install` → delete `vendor/` → `composer install` → Composer accepts the lock without regenerating. Should complete without content-hash errors.

## Related follow-ups (not in this plan)

- **Full round-trip fidelity**: preserve unknown fields so Composer's optional metadata (`license`, `authors`, `keywords`) doesn't churn after a `gomposer install`. Requires storing a `json.RawMessage`-shaped shadow of the on-disk lock.
- **`platform-overrides`**: honor Composer's field to override the runtime probe.
- **`--prefer-lowest`** flag support end-to-end.
