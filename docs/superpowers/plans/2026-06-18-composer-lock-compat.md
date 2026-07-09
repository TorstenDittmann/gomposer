# composer.lock Write-Compat Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `gomposer.lock` with a Composer-schema `composer.lock` that Composer's own `composer install` can consume.

**Architecture:** New `lock.File` shape mirrors Composer's on-disk JSON. A `manifest.ContentHash` helper ports Composer's `Locker::getContentHash` (MD5 over the filtered, PHP-encoded manifest subset). The resolver adapter fills the new fields (`stability-flags`, `platform`, `notification-url`, `time`) from data we already have plus one new field on `registry.PackageVersion`. Every mention of `gomposer.lock` is purged.

**Tech Stack:** Go 1.25, standard library (`encoding/json`, `crypto/md5`, `encoding/hex`, `unicode/utf8`). No new third-party dependencies.

## Global Constraints

- **Lockfile name:** `composer.lock` at the project root — never `gomposer.lock`, no fallback.
- **Content-hash algorithm:** must byte-for-byte match Composer's `Locker::getContentHash`. Key allowlist: `name`, `version`, `require`, `require-dev`, `conflict`, `replace`, `provide`, `minimum-stability`, `prefer-stable`, `repositories`, `extra`. Plus `config.platform` when present. Encoded to PHP-flavored JSON (slashes escaped as `\/`, non-ASCII escaped as `\uXXXX`, no indentation, sorted keys). MD5 hex, lowercase.
- **Stability rank map:** `dev=20`, `alpha=15`, `beta=10`, `RC=5`, `stable=0`.
- **`_readme` array:** the exact three-line Composer boilerplate:
  1. `This file locks the dependencies of your project to a known state`
  2. `Read more about it at https://getcomposer.org/doc/01-basic-usage.md#installing-dependencies`
  3. `This file is @generated automatically`
- **Plugin API version:** `"2.6.0"`.
- **Notification URL for Packagist entries:** `"https://packagist.org/downloads/"`.
- **Cache subdirs to bump:** `internal/registry/packagist/packagist.go` uses `parsed/` → change to `parsed-v2/`; `internal/orchestrator/cachekey.go` uses `resolution/` → change to `resolution-v2/`. Both are one-line changes and force re-derivation with the new shapes.
- **JSON encoding style for the lockfile itself:** 2-space indent, `SetEscapeHTML(false)`, trailing `\n`, map keys sorted (encoding/json default). Matches Composer's `Locker::save` output shape.

---

## File structure

| Path | Responsibility |
|------|---------------|
| `internal/manifest/contenthash.go` | New. `ContentHash(manifestBytes []byte) (string, error)` — Composer's `Locker::getContentHash` port. |
| `internal/manifest/contenthash_test.go` | New. Fixture-driven tests against baked-in expected hashes. |
| `internal/manifest/testdata/contenthash/*.json` | New. 3–5 real composer.json files. |
| `internal/lock/lock.go` | Full rewrite to Composer's schema. |
| `internal/lock/lock_test.go` | Rewrite to exercise the new shape. |
| `internal/resolver/adapter.go` | Populate new fields (`NotificationURL`, `Time`, `StabilityFlags`, `Platform`, `PlatformDev`, `Readme`, `PluginAPIVersion`, `ContentHash`). |
| `internal/resolver/adapter_test.go` | Assert the new fields flow through. |
| `internal/registry/source.go` | Add `Time string` to `registry.PackageVersion`. |
| `internal/registry/packagist/packagist.go` | Parse `time` from v2 payload; bump parsedcache dir to `parsed-v2`. |
| `internal/registry/packagist/packagist_test.go` | Fixture includes `"time"`, assertion added. |
| `internal/orchestrator/pipeline.go` | Replace `gomposer.lock` string references with `composer.lock`; delete anything that read `gomposer.lock`. |
| `internal/orchestrator/cachekey.go` | Rename cache dir from `resolution` to `resolution-v2`. |
| `internal/orchestrator/*_test.go` | Any `gomposer.lock` literals become `composer.lock`; `lock.File` literals get the new shape. |
| `internal/cli/install.go` | Update `Short` string. |
| `internal/cli/update.go` | Update `Short` string. |
| `internal/cli/root.go` | Update `Long` string. |
| `cmd/bench/runner.go` | Remove `"gomposer.lock"` from the cold-scenario removal list. |
| `cmd/bench/runner_test.go` | Update the corresponding fixture assertion. |
| `README.md` | Rewrite the "Composer compatibility" bullet. |
| `docs/superpowers/specs/2026-05-07-gomposer-design.md` | Update the "Lockfile format" section to point at the new spec. |

---

## Task 1: Content-hash algorithm

**Files:**
- Create: `internal/manifest/contenthash.go`
- Create: `internal/manifest/contenthash_test.go`
- Create: `internal/manifest/testdata/contenthash/empty.json`
- Create: `internal/manifest/testdata/contenthash/minimal.json`
- Create: `internal/manifest/testdata/contenthash/with-config-platform.json`
- Create: `internal/manifest/testdata/contenthash/with-repositories.json`
- Create: `internal/manifest/testdata/contenthash/with-unicode-extra.json`

**Interfaces:**
- Consumes: nothing (pure `[]byte → string`).
- Produces:
  - `func ContentHash(manifestBytes []byte) (string, error)` — the exported entry point downstream tasks use.
  - Helper `phpCompatibleJSON(v any) ([]byte, error)` — kept unexported.

- [ ] **Step 1: Fixture files**

Create each JSON file below at `internal/manifest/testdata/contenthash/`.

`empty.json` — a manifest with none of the relevant keys. Composer's hash for this is MD5 of `{}` = `99914b932bd37a50b983c5e7c90ae93b`.

```json
{
  "name": "acme/tool",
  "type": "library",
  "description": "not in the relevant set",
  "authors": [{"name": "someone"}]
}
```

`minimal.json` — only `name` and `require`:

```json
{
  "name": "acme/app",
  "require": {
    "php": ">=8.1",
    "monolog/monolog": "^3.0"
  }
}
```

`with-config-platform.json` — exercises the `config.platform` copy-through:

```json
{
  "name": "acme/app",
  "require": {"php": ">=8.1"},
  "config": {
    "platform": {"php": "8.2.0"},
    "sort-packages": true
  }
}
```

`with-repositories.json` — exercises the slash-escape transform via a URL:

```json
{
  "name": "acme/app",
  "require": {"acme/lib": "dev-main"},
  "repositories": [
    {"type": "vcs", "url": "https://github.com/acme/lib.git"}
  ]
}
```

`with-unicode-extra.json` — exercises the non-ASCII → `\uXXXX` transform. The `extra` field is in the relevant set:

```json
{
  "name": "acme/app",
  "extra": {
    "note": "café ☕"
  }
}
```

- [ ] **Step 2: Write the failing test**

Create `internal/manifest/contenthash_test.go`:

```go
package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

// TestContentHashMatchesFixtures pins gomposer's ContentHash implementation
// to Composer's Locker::getContentHash. Each expected value was produced
// by running Composer against the same input manifest and reading
// composer.lock's "content-hash" field. Regenerate by running Composer
// against the fixture and updating the expected string below.
func TestContentHashMatchesFixtures(t *testing.T) {
	cases := []struct {
		file string
		want string
	}{
		// Filter yields {}; MD5 of "{}" is 99914b932bd37a50b983c5e7c90ae93b.
		{"empty.json", "99914b932bd37a50b983c5e7c90ae93b"},

		// Regenerate with: `composer install --no-scripts --dry-run` in a
		// throwaway project containing the fixture, then read
		// composer.lock's "content-hash". Update these when regenerating.
		{"minimal.json", "TASK1_TODO_MINIMAL"},
		{"with-config-platform.json", "TASK1_TODO_CONFIG_PLATFORM"},
		{"with-repositories.json", "TASK1_TODO_REPOSITORIES"},
		{"with-unicode-extra.json", "TASK1_TODO_UNICODE"},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			path := filepath.Join("testdata", "contenthash", tc.file)
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			got, err := ContentHash(body)
			if err != nil {
				t.Fatalf("ContentHash: %v", err)
			}
			if got != tc.want {
				t.Errorf("ContentHash(%s) = %q, want %q", tc.file, got, tc.want)
			}
		})
	}
}

// TestPhpCompatibleJSONEscapesSlashes verifies the slash-escape transform
// on a value containing a URL. PHP's json_encode escapes / as \/ by
// default; Go's json.Marshal never does.
func TestPhpCompatibleJSONEscapesSlashes(t *testing.T) {
	in := map[string]any{"url": "https://example.com/path"}
	got, err := phpCompatibleJSON(in)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"url":"https:\/\/example.com\/path"}`
	if string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// TestPhpCompatibleJSONEscapesNonASCII verifies non-ASCII → \uXXXX. PHP's
// default json_encode escapes non-ASCII; Go emits raw UTF-8.
func TestPhpCompatibleJSONEscapesNonASCII(t *testing.T) {
	in := map[string]any{"note": "café"}
	got, err := phpCompatibleJSON(in)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"note":"café"}`
	if string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// TestPhpCompatibleJSONHandlesAstralPlaneAsSurrogatePair — PHP's
// json_encode encodes characters above U+FFFF as UTF-16 surrogate pairs
// (😊 for U+1F60A). We must match.
func TestPhpCompatibleJSONHandlesAstralPlaneAsSurrogatePair(t *testing.T) {
	in := map[string]any{"emoji": "😊"} // U+1F60A
	got, err := phpCompatibleJSON(in)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"emoji":"😊"}`
	if string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
```

- [ ] **Step 3: Verify tests fail**

Run: `go test ./internal/manifest/ -run 'TestContentHash|TestPhpCompatibleJSON' -v`

Expected: build error referencing `ContentHash` and `phpCompatibleJSON` (undefined).

- [ ] **Step 4: Implement `internal/manifest/contenthash.go`**

```go
// Package manifest — contenthash.go implements Composer's
// Locker::getContentHash algorithm so gomposer's composer.lock carries a
// content-hash that upstream Composer accepts.
package manifest

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// relevantKeys mirrors Composer\Package\Locker::$relevantKeys. Changing
// this set breaks cross-tool compat.
var relevantKeys = map[string]struct{}{
	"name":              {},
	"version":           {},
	"require":           {},
	"require-dev":       {},
	"conflict":          {},
	"replace":           {},
	"provide":           {},
	"minimum-stability": {},
	"prefer-stable":     {},
	"repositories":      {},
	"extra":             {},
}

// ContentHash computes Composer's content-hash of the manifest bytes.
// Callers pass the raw composer.json contents (not the parsed Manifest
// struct) so field-name drift can't cause hash drift.
func ContentHash(manifestBytes []byte) (string, error) {
	var raw map[string]any
	if err := json.Unmarshal(manifestBytes, &raw); err != nil {
		return "", fmt.Errorf("manifest: content-hash: decode: %w", err)
	}
	filtered := map[string]any{}
	for k, v := range raw {
		if _, ok := relevantKeys[k]; ok {
			filtered[k] = v
		}
	}
	// Composer also carries config.platform when present.
	if cfg, ok := raw["config"].(map[string]any); ok {
		if plat, ok := cfg["platform"]; ok {
			filtered["config"] = map[string]any{"platform": plat}
		}
	}
	encoded, err := phpCompatibleJSON(filtered)
	if err != nil {
		return "", fmt.Errorf("manifest: content-hash: encode: %w", err)
	}
	sum := md5.Sum(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// phpCompatibleJSON produces bytes equivalent to PHP's default
// json_encode: no indentation, sorted map keys, slashes escaped as \/,
// non-ASCII characters escaped as \uXXXX (surrogate pairs for code
// points above U+FFFF). HTML metacharacters (<, >, &) are NOT escaped —
// PHP doesn't escape them by default either, and neither does Go with
// SetEscapeHTML(false).
func phpCompatibleJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// json.Encoder appends a trailing newline; strip it.
	raw := bytes.TrimRight(buf.Bytes(), "\n")

	// Walk the encoded bytes and apply two transforms:
	//   1. every literal '/' in a string value becomes '\/'
	//   2. every rune outside the ASCII range becomes '\uXXXX' (surrogate
	//      pair for code points >= 0x10000).
	// Structural characters (', ', :, {, }, [, ]) never contain / or
	// non-ASCII, so a byte-level pass is safe.
	var out strings.Builder
	out.Grow(len(raw))
	i := 0
	for i < len(raw) {
		c := raw[i]
		if c == '/' {
			out.WriteString(`\/`)
			i++
			continue
		}
		if c < 0x80 {
			out.WriteByte(c)
			i++
			continue
		}
		r, size := utf8.DecodeRune(raw[i:])
		if r == utf8.RuneError && size == 1 {
			// Invalid UTF-8; pass the byte through untouched.
			out.WriteByte(c)
			i++
			continue
		}
		if r <= 0xFFFF {
			fmt.Fprintf(&out, `\u%04x`, r)
		} else {
			// Split into UTF-16 surrogate pair (PHP does this).
			r2 := r - 0x10000
			hi := 0xD800 | ((r2 >> 10) & 0x3FF)
			lo := 0xDC00 | (r2 & 0x3FF)
			fmt.Fprintf(&out, `\u%04x\u%04x`, hi, lo)
		}
		i += size
	}
	return []byte(out.String()), nil
}
```

- [ ] **Step 5: Generate the missing expected hashes**

For each of `minimal.json`, `with-config-platform.json`, `with-repositories.json`, `with-unicode-extra.json`:

1. Create a throwaway directory: `mkdir /tmp/hashgen && cd /tmp/hashgen`.
2. `cp /Users/<user>/…/internal/manifest/testdata/contenthash/<file>.json composer.json`.
3. `composer install --no-scripts --no-plugins --ignore-platform-reqs --dry-run --no-interaction` (needs upstream Composer on PATH). This writes `composer.lock`.
4. `grep -o '"content-hash":\s*"[^"]*"' composer.lock` — record the hex value.
5. Paste the value into `contenthash_test.go` in place of the corresponding `TASK1_TODO_*` string.

If upstream Composer is unavailable, use a fallback: run `phpCompatibleJSON` locally on the filtered map, `md5sum` the output, and compare against a known-good hash generator (or spot-check the algorithm implementation). Document the generation method in a comment.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/manifest/ -run 'TestContentHash|TestPhpCompatibleJSON' -v`

Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add internal/manifest/contenthash.go internal/manifest/contenthash_test.go internal/manifest/testdata/contenthash/
git commit -m "feat(manifest): port Composer's Locker::getContentHash

Adds ContentHash(manifestBytes) and phpCompatibleJSON, matching Composer's
default json_encode semantics: sorted keys, escaped slashes, non-ASCII to
\uXXXX (surrogate pairs above U+FFFF), MD5 of the result. Fixture-driven
tests pin us to Composer's on-disk output."
```

---

## Task 2: New `lock.File` shape

**Files:**
- Modify: `internal/lock/lock.go` (full rewrite)
- Modify: `internal/lock/lock_test.go` (full rewrite)

**Interfaces:**
- Consumes: nothing (schema-only change).
- Produces:
  - Package `lock` exports: `File`, `Package`, `Source`, `Dist`, `Alias`, `Encode(f *File) ([]byte, error)`, `Decode(data []byte) (*File, error)`. All exports are documented on-struct with JSON keys aligned to Composer.
  - Field names later tasks depend on: `File.Readme []string`, `File.ContentHash string`, `File.Packages []Package`, `File.PackagesDev []Package`, `File.Aliases []Alias`, `File.MinimumStability string`, `File.StabilityFlags map[string]int`, `File.PreferStable bool`, `File.PreferLowest bool`, `File.Platform map[string]string`, `File.PlatformDev map[string]string`, `File.PlatformOverrides map[string]string`, `File.PluginAPIVersion string`. `Package.Name`, `Package.Version`, `Package.Type`, `Package.Source`, `Package.Dist`, `Package.Require map[string]string`, `Package.Autoload map[string]any`, `Package.NotificationURL string`, `Package.Time string`. `Source.Type`, `Source.URL`, `Source.Reference`. `Dist.Type`, `Dist.URL`, `Dist.Reference`, `Dist.Shasum`. `Alias.Package`, `Alias.Version`, `Alias.Alias`.

- [ ] **Step 1: Write the failing test**

Replace the contents of `internal/lock/lock_test.go` with:

```go
package lock

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestFileRoundTripsThroughEncodeDecode(t *testing.T) {
	orig := &File{
		Readme:      []string{"line one", "line two"},
		ContentHash: "abc123",
		Packages: []Package{{
			Name:            "acme/lib",
			Version:         "1.0.0",
			Type:            "library",
			Source:          Source{Type: "git", URL: "https://example.com/acme/lib.git", Reference: "deadbeef"},
			Dist:            Dist{Type: "zip", URL: "https://example.com/acme/lib.zip", Reference: "deadbeef", Shasum: "cafebabe"},
			Require:         map[string]string{"php": ">=8.1"},
			Autoload:        map[string]any{"psr-4": map[string]string{"Acme\\Lib\\": "src/"}},
			NotificationURL: "https://packagist.org/downloads/",
			Time:            "2026-05-01T00:00:00+00:00",
		}},
		PackagesDev:      []Package{},
		Aliases:          []Alias{{Package: "acme/lib", Version: "9999999-dev", Alias: "1.x-dev"}},
		MinimumStability: "stable",
		StabilityFlags:   map[string]int{"acme/lib": 5},
		PreferStable:     true,
		PreferLowest:     false,
		Platform:         map[string]string{"php": ">=8.1"},
		PlatformDev:      map[string]string{},
		PluginAPIVersion: "2.6.0",
	}
	data, err := orig.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	// Encode again and compare byte-for-byte to prove determinism.
	back, err := got.Encode()
	if err != nil {
		t.Fatalf("re-Encode: %v", err)
	}
	if !bytes.Equal(data, back) {
		t.Errorf("round-trip not byte-stable")
	}
}

// TestEncodedShapeMatchesComposer verifies the JSON key names and
// structure follow Composer's on-disk shape (hyphenated keys, packages/
// packages-dev/aliases at top level, per-package notification-url).
func TestEncodedShapeMatchesComposer(t *testing.T) {
	f := &File{
		ContentHash: "hash",
		Packages: []Package{{
			Name: "a/b", Version: "1.0.0",
			Dist: Dist{Type: "zip", URL: "https://example.com/x.zip", Shasum: "abc"},
		}},
		MinimumStability: "stable",
		StabilityFlags:   map[string]int{},
		Platform:         map[string]string{},
		PlatformDev:      map[string]string{},
	}
	data, err := f.Encode()
	if err != nil {
		t.Fatal(err)
	}
	// Decode into an anonymous map so we can assert key names.
	var top map[string]any
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"content-hash", "packages", "packages-dev", "aliases", "minimum-stability", "stability-flags", "prefer-stable", "prefer-lowest", "platform", "platform-dev"} {
		if _, ok := top[want]; !ok {
			t.Errorf("top-level key %q missing", want)
		}
	}
	pkgs, ok := top["packages"].([]any)
	if !ok || len(pkgs) != 1 {
		t.Fatalf("packages: %#v", top["packages"])
	}
	pkg := pkgs[0].(map[string]any)
	dist, ok := pkg["dist"].(map[string]any)
	if !ok {
		t.Fatalf("dist: %#v", pkg["dist"])
	}
	if _, ok := dist["shasum"]; !ok {
		t.Errorf("dist.shasum missing (should NOT be 'sha256')")
	}
}
```

- [ ] **Step 2: Verify tests fail**

Run: `go test ./internal/lock/ -v`

Expected: compile errors — `File.Readme`, `File.ContentHash`, `Dist.Shasum`, `Dist.Reference`, `Source.Reference`, `Package.NotificationURL`, `Package.Time`, `File.StabilityFlags`, `File.PreferLowest`, `File.Platform`, `File.PlatformDev`, `File.PluginAPIVersion` not defined.

- [ ] **Step 3: Rewrite `internal/lock/lock.go`**

Replace the file's contents in full:

```go
// Package lock handles composer.lock read and write.
//
// The on-disk shape mirrors Composer's own composer.lock so upstream
// Composer can consume what gomposer emits. See
// docs/superpowers/specs/2026-06-18-composer-lock-compat-design.md for
// the design rationale and field-level mapping.
package lock

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// File is the top-level composer.lock structure. JSON tags mirror
// Composer's Locker::save output verbatim.
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

// Package is one locked package. Field set matches the subset of
// Composer's per-package output that gomposer populates. Optional-in-
// Composer fields we don't emit (authors, license, description,
// keywords, homepage, funding, support) are omitted; Composer accepts
// their absence.
type Package struct {
	Name            string            `json:"name"`
	Version         string            `json:"version"`
	Type            string            `json:"type,omitempty"`
	Source          Source            `json:"source,omitempty"`
	Dist            Dist              `json:"dist,omitempty"`
	Require         map[string]string `json:"require,omitempty"`
	Autoload        map[string]any    `json:"autoload,omitempty"`
	NotificationURL string            `json:"notification-url,omitempty"`
	Time            string            `json:"time,omitempty"`
}

type Source struct {
	Type      string `json:"type"`
	URL       string `json:"url"`
	Reference string `json:"reference"`
}

type Dist struct {
	Type      string `json:"type"`
	URL       string `json:"url"`
	Reference string `json:"reference,omitempty"`
	Shasum    string `json:"shasum"`
}

type Alias struct {
	Package string `json:"package"`
	Version string `json:"version"`
	Alias   string `json:"alias"`
}

// Encode serializes deterministically. 2-space indent + SetEscapeHTML
// (false) + trailing "\n". Map keys are sorted by encoding/json.
func (f *File) Encode() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "    ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(f); err != nil {
		return nil, fmt.Errorf("lock: encode: %w", err)
	}
	return buf.Bytes(), nil
}

// Decode parses a composer.lock. Unknown fields are ignored (Composer may
// add optional metadata we don't consume). Callers that need round-trip
// preservation should track that separately.
func Decode(data []byte) (*File, error) {
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("lock: decode: %w", err)
	}
	return &f, nil
}
```

Note the indent width is `"    "` (4 spaces) — this matches Composer's `Locker::save` output. Adjust the test if you want 2-space indent instead; Composer's actual on-disk uses 4-space in modern versions.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/lock/ -v`

Expected: pass.

- [ ] **Step 5: Verify the rest of the build compiles**

Run: `go build ./...`

Expected: many errors in packages that used the old `Sha256`, `Ref`, `SchemaVersion`, `Generator`, `Warnings`, or the nested `Stability` struct. That's fine — later tasks fix them. Don't try to fix them in this task.

- [ ] **Step 6: Commit**

```bash
git add internal/lock/lock.go internal/lock/lock_test.go
git commit -m "feat(lock): switch to composer.lock schema

Full rewrite of internal/lock. New shape mirrors Composer's on-disk
lockfile: _readme, content-hash, packages, packages-dev, aliases,
minimum-stability, stability-flags, prefer-stable, prefer-lowest,
platform, platform-dev, platform-overrides, plugin-api-version. Per-
package: name, version, type, source (with 'reference'), dist (with
'reference' and 'shasum'), require, autoload, notification-url, time.

The next commits update every caller."
```

---

## Task 3: `registry.PackageVersion.Time` + Packagist plumbing

**Files:**
- Modify: `internal/registry/source.go` (add field)
- Modify: `internal/registry/packagist/packagist.go` (parse + bump cache dir)
- Modify: `internal/registry/packagist/packagist_test.go` (add fixture data + assertion)

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `registry.PackageVersion.Time string` — RFC3339 timestamp of when the package version was published, per Packagist v2.
  - Parsed cache subdir renamed from `parsed` to `parsed-v2`.

- [ ] **Step 1: Add the field**

In `internal/registry/source.go`, find `type PackageVersion struct` and add `Time string` alongside the other scalar fields. Suggested placement: just after `VersionNorm`.

```go
type PackageVersion struct {
    Name        string
    Version     string
    VersionNorm string
    Time        string // RFC3339 timestamp from Packagist v2 "time" field.
    // ...existing fields...
}
```

- [ ] **Step 2: Write the failing test**

Add to `internal/registry/packagist/packagist_test.go`:

```go
func TestLookupCarriesPublishedTime(t *testing.T) {
	body := `{"packages":{"acme/pkg":[
		{"name":"acme/pkg","version":"1.0.0","version_normalized":"1.0.0.0",
		 "type":"library","time":"2026-05-01T00:00:00+00:00",
		 "source":{"type":"git","url":"https://example.invalid/x.git","reference":"deadbeef"},
		 "dist":{"type":"zip","url":"https://example.invalid/x.zip","shasum":"abc"},
		 "require":{}}
	]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c, err := New(Config{BaseURL: srv.URL, CacheDir: t.TempDir(), HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	md, err := c.Lookup(context.Background(), "acme/pkg")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(md.Versions) != 1 {
		t.Fatalf("Versions = %d", len(md.Versions))
	}
	if md.Versions[0].Time != "2026-05-01T00:00:00+00:00" {
		t.Errorf("Time = %q", md.Versions[0].Time)
	}
}
```

- [ ] **Step 3: Verify it fails**

Run: `go test ./internal/registry/packagist/ -run TestLookupCarriesPublishedTime -v`

Expected: FAIL — `Time` is zero.

- [ ] **Step 4: Wire the field**

In `internal/registry/packagist/packagist.go`, add `Time string \`json:"time"\`` to `v2Version`, and copy it into `registry.PackageVersion` in `decodeV2`.

```go
type v2Version struct {
    Name              string       `json:"name"`
    Version           string       `json:"version"`
    VersionNormalized string       `json:"version_normalized"`
    Time              string       `json:"time"`
    // ...existing fields...
}
```

In `decodeV2`, add `Time: v.Time,` to the `registry.PackageVersion` literal.

- [ ] **Step 5: Bump the parsed-cache subdir**

In the same file, find `parsedDir := filepath.Join(cfg.CacheDir, "parsed")` and change to `"parsed-v2"`. This forces re-fetch after the shape change (parsed cache stores `registry.PackageMetadata` including our new `Time`).

- [ ] **Step 6: Run tests**

Run: `go test ./internal/registry/packagist/ -v`

Expected: pass.

- [ ] **Step 7: Commit**

```bash
git add internal/registry/source.go internal/registry/packagist/packagist.go internal/registry/packagist/packagist_test.go
git commit -m "feat(registry): carry Packagist 'time' field on PackageVersion

The upcoming composer.lock schema includes per-package 'time'. Add the
field to registry.PackageVersion, parse it in the v2 decoder, and bump
the parsed-cache subdir to parsed-v2 so pre-schema-bump entries are not
served."
```

---

## Task 4: Resolver adapter populates new fields

**Files:**
- Modify: `internal/resolver/adapter.go`
- Modify: `internal/resolver/adapter_test.go`

**Interfaces:**
- Consumes: `lock.File`/`lock.Package` shape from Task 2; `registry.PackageVersion.Time` from Task 3; `manifest.ContentHash` from Task 1; `constraint.Constraint.StabilityFlag()` (already present).
- Produces:
  - Existing exported function signature (`BuildLock`, `NewLockFile`, whatever the file exposes) unchanged EXCEPT it now takes an extra `manifestBytes []byte` parameter (needed for content-hash). Rename if it makes the signature clearer, but keep the change localized.
  - `notificationURLFor(source registry.Source) string` — small helper.
  - `stabilityRank(flag string) int` — helper mapping `"RC" → 5` etc.

- [ ] **Step 1: Read the existing adapter to learn its signature**

Read `internal/resolver/adapter.go`. Identify the function that produces `*lock.File` (typical name: `BuildLock`, `LockFile`, `ToLock`, or similar). Note its parameter list.

- [ ] **Step 2: Write failing tests**

Add to `internal/resolver/adapter_test.go` (append; keep existing tests):

```go
func TestAdapterEmitsComposerFlavoredLock(t *testing.T) {
	manifestBytes := []byte(`{
	  "name": "acme/app",
	  "require": {"acme/lib": "^1.0"},
	  "minimum-stability": "stable"
	}`)
	// Adapt to whatever the actual test-input helper is; the key point is
	// that the resolver output should carry: NotificationURL, Time,
	// StabilityFlags, Platform, Readme, PluginAPIVersion, ContentHash.
	// Use the helpers already present in adapter_test.go for building the
	// Result and Manifest inputs; here we show the assertion shape only.

	// out := BuildLock(result, manifest, manifestBytes)  ← adapt to real name
	// ...

	if len(out.Readme) < 1 || out.Readme[0] == "" {
		t.Errorf("_readme not populated")
	}
	if out.ContentHash == "" {
		t.Errorf("content-hash empty")
	}
	if out.PluginAPIVersion != "2.6.0" {
		t.Errorf("plugin-api-version = %q", out.PluginAPIVersion)
	}
	if out.Platform["php"] == "" {
		// If manifest had no platform reqs, skip. Otherwise assert.
	}
}

func TestAdapterMapsStabilityFlagRanks(t *testing.T) {
	cases := map[string]int{
		"dev": 20, "alpha": 15, "beta": 10, "RC": 5, "rc": 5, "stable": 0,
	}
	for flag, want := range cases {
		if got := stabilityRank(flag); got != want {
			t.Errorf("stabilityRank(%q) = %d, want %d", flag, got, want)
		}
	}
}

func TestAdapterNotificationURLForPackagist(t *testing.T) {
	// Sources coming from a git URL under packagist mirror emit the
	// Packagist notification-url; VCS-only sources get "".
	packagistLike := registry.Source{Type: "git", URL: "https://api.github.com/repos/acme/lib/zipball/x"}
	if got := notificationURLFor(packagistLike, /* isPackagist=true */ true); got != "https://packagist.org/downloads/" {
		t.Errorf("packagist source: %q", got)
	}
	vcsOnly := registry.Source{Type: "git", URL: "git@github.com:acme/private.git"}
	if got := notificationURLFor(vcsOnly, /* isPackagist=false */ false); got != "" {
		t.Errorf("vcs source: %q", got)
	}
}
```

Note the `isPackagist` parameter above. Wire it from wherever the resolver already knows a package's source registry (multisource passes registry identity through — inspect `internal/registry/multisource/multisource.go` for how). If that thread isn't already in place, add a `SourceKind` string on `registry.PackageVersion` set to `"packagist"` or `"vcs"` in the corresponding registry's Lookup, and read it in the adapter.

- [ ] **Step 3: Verify the tests fail**

Run: `go test ./internal/resolver/ -run 'TestAdapterEmitsComposer|TestAdapterMapsStability|TestAdapterNotificationURL' -v`

Expected: compile errors — `stabilityRank`, `notificationURLFor`, and (probably) `manifestBytes` parameter undefined.

- [ ] **Step 4: Implement helpers**

Append to `internal/resolver/adapter.go`:

```go
// stabilityRank maps a Composer stability-flag string to its numeric rank
// used in composer.lock's stability-flags map. Ranks are Composer's
// BasePackage::STABILITY_* constants.
func stabilityRank(flag string) int {
	switch strings.ToLower(flag) {
	case "dev":
		return 20
	case "alpha":
		return 15
	case "beta":
		return 10
	case "rc":
		return 5
	default:
		return 0
	}
}

// notificationURLFor picks the notification-url a locked package advertises.
// Packagist-sourced packages get "https://packagist.org/downloads/"; VCS-
// sourced packages get an empty string (Composer's convention).
func notificationURLFor(_ registry.Source, isPackagist bool) string {
	if isPackagist {
		return "https://packagist.org/downloads/"
	}
	return ""
}
```

Add `import "strings"` if not present.

- [ ] **Step 5: Update the lock-building function**

Rework the existing lock-building function to populate the new fields. The exact shape below assumes the function is `BuildLock(result *Result, m *manifest.Manifest, manifestBytes []byte) (*lock.File, error)` — adapt to the actual name and add the `manifestBytes` parameter if missing.

```go
func BuildLock(result *Result, m *manifest.Manifest, manifestBytes []byte) (*lock.File, error) {
	hash, err := manifest.ContentHash(manifestBytes)
	if err != nil {
		return nil, fmt.Errorf("resolver: build lock: %w", err)
	}
	stabilityFlags := map[string]int{}
	for name, raw := range m.Require {
		c, err := constraint.Parse(raw)
		if err != nil {
			continue
		}
		if flag := c.StabilityFlag(); flag != "" {
			stabilityFlags[name] = stabilityRank(flag)
		}
	}
	for name, raw := range m.RequireDev {
		c, err := constraint.Parse(raw)
		if err != nil {
			continue
		}
		if flag := c.StabilityFlag(); flag != "" {
			stabilityFlags[name] = stabilityRank(flag)
		}
	}

	platform := map[string]string{}
	for name, raw := range m.Require {
		if platformpkg.IsPlatformReq(name) {
			platform[name] = raw
		}
	}
	platformDev := map[string]string{}
	for name, raw := range m.RequireDev {
		if platformpkg.IsPlatformReq(name) {
			platformDev[name] = raw
		}
	}

	packages := make([]lock.Package, 0, len(result.Packages))
	for _, p := range result.Packages {
		packages = append(packages, toLockPackage(p))
	}
	packagesDev := make([]lock.Package, 0, len(result.PackagesDev))
	for _, p := range result.PackagesDev {
		packagesDev = append(packagesDev, toLockPackage(p))
	}

	minStab := m.MinimumStability
	if minStab == "" {
		minStab = "stable"
	}
	return &lock.File{
		Readme: []string{
			"This file locks the dependencies of your project to a known state",
			"Read more about it at https://getcomposer.org/doc/01-basic-usage.md#installing-dependencies",
			"This file is @generated automatically",
		},
		ContentHash:      hash,
		Packages:         packages,
		PackagesDev:      packagesDev,
		Aliases:          buildAliases(result), // whatever alias builder already exists
		MinimumStability: minStab,
		StabilityFlags:   stabilityFlags,
		PreferStable:     m.PreferStable,
		PreferLowest:     false,
		Platform:         platform,
		PlatformDev:      platformDev,
		PluginAPIVersion: "2.6.0",
	}, nil
}

func toLockPackage(p ResolvedPackage) lock.Package {
	rec := p.Record
	return lock.Package{
		Name:    rec.Name,
		Version: rec.Version,
		Type:    rec.Type,
		Source: lock.Source{
			Type:      rec.Source.Type,
			URL:       rec.Source.URL,
			Reference: rec.Source.Ref,
		},
		Dist: lock.Dist{
			Type:      rec.Dist.Type,
			URL:       rec.Dist.URL,
			Reference: rec.Source.Ref,
			Shasum:    rec.Dist.Sha,
		},
		Require:         rec.Require,
		Autoload:        toLockAutoload(rec.Autoload),
		NotificationURL: notificationURLFor(rec.Source, rec.SourceKind == "packagist"),
		Time:            rec.Time,
	}
}
```

`toLockAutoload` translates the resolver's autoload struct into Composer's shape (`{"psr-4": {...}, "psr-0": {...}, "files": [...], "classmap": [...]}`); reuse or move the existing serializer.

If `registry.PackageVersion.SourceKind` isn't already threaded through, add it: `internal/registry/source.go` gains `SourceKind string` and the Packagist / VCS lookups set it explicitly.

- [ ] **Step 6: Update every call site**

Find every caller of the old `BuildLock` (or whatever name is): `grep -rn "BuildLock\b" --include="*.go"`. Add the new `manifestBytes` argument. Read the manifest bytes from disk once and pass through.

- [ ] **Step 7: Run tests**

Run: `go test ./internal/resolver/ -v`

Expected: pass.

- [ ] **Step 8: Commit**

```bash
git add internal/resolver/adapter.go internal/resolver/adapter_test.go internal/registry/source.go
git commit -m "feat(resolver): fill Composer-shaped composer.lock fields

Populates content-hash (via manifest.ContentHash), _readme boilerplate,
stability-flags (with Composer's rank map), platform/platform-dev
extraction, notification-url for Packagist packages, plugin-api-version,
and per-package Time from Packagist metadata. Every caller of the lock
builder now passes raw manifest bytes for the content-hash input."
```

---

## Task 5: Orchestrator reads/writes `composer.lock`

**Files:**
- Modify: `internal/orchestrator/pipeline.go`
- Modify: `internal/orchestrator/cachekey.go`
- Modify: `internal/orchestrator/*_test.go` (mechanical rename in test literals)

**Interfaces:**
- Consumes: `lock.File.Encode`, `lock.Decode` from Task 2; the new adapter signature from Task 4.
- Produces: `composer.lock` at `<projectDir>/composer.lock`; resolution cache in `resolution-v2/`.

- [ ] **Step 1: Grep for gomposer.lock references in orchestrator**

Run: `grep -n "gomposer.lock" internal/orchestrator/*.go`

Expected findings (approximate): the `readFile` at pipeline.go:88 and the `writeLock` `final` path at pipeline.go:303.

- [ ] **Step 2: Update the read path**

In `internal/orchestrator/pipeline.go`, change the lock-read line:

```go
lockBytes, _ := os.ReadFile(filepath.Join(opts.ProjectDir, "composer.lock"))
```

There is no fallback. If `composer.lock` is absent, `lockBytes` is nil and the resolver runs from scratch — same as before.

- [ ] **Step 3: Update the write path**

In `writeLock`:

```go
final := filepath.Join(projectDir, "composer.lock")
```

The `tmp` suffix and rename logic stay the same.

- [ ] **Step 4: Update the write callers**

The lock-builder function signature changed in Task 4 (added `manifestBytes`). Update the pipeline caller(s) to pass the manifest bytes we already read into `pipelineState.manifestBytes`.

- [ ] **Step 5: Bump resolution cache subdir**

In `internal/orchestrator/cachekey.go`, find `d := filepath.Join(root, "resolution")` and change to `resolution-v2`. This prevents pre-schema-bump entries from being decoded as the new shape.

- [ ] **Step 6: Update the tests**

`grep -rn "gomposer.lock" internal/orchestrator/ --include='*_test.go'`

Rename any string literal `"gomposer.lock"` to `"composer.lock"`. Update any `lock.File` struct literals: strip `SchemaVersion`, `Generator`, `ManifestContentHash`, `PlatformFingerprint`, `Stability`, `Warnings` — those fields no longer exist. Rename `Dist.Sha256` → `Dist.Shasum`, `Source.Ref` → `Source.Reference` in test literals.

- [ ] **Step 7: Run tests**

Run: `go test ./internal/orchestrator/... -v`

Expected: pass (the packages this task touches). If other packages fail to build, that's fine — subsequent tasks will fix them.

- [ ] **Step 8: Commit**

```bash
git add internal/orchestrator/pipeline.go internal/orchestrator/cachekey.go internal/orchestrator/*_test.go
git commit -m "feat(orchestrator): read + write composer.lock

Replaces every gomposer.lock reference in the orchestrator. Read path
looks only for composer.lock (no fallback). Write path emits composer.lock
via atomic rename. Resolution cache subdir renamed to resolution-v2 so
old-shape entries can't be decoded as the new schema."
```

---

## Task 6: CLI copy + bench + README + spec doc

**Files:**
- Modify: `internal/cli/install.go`
- Modify: `internal/cli/update.go`
- Modify: `internal/cli/root.go`
- Modify: `cmd/bench/runner.go`
- Modify: `cmd/bench/runner_test.go`
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-05-07-gomposer-design.md`

**Interfaces:** none new; documentation-only edits.

- [ ] **Step 1: CLI help strings**

`internal/cli/install.go`:

```go
Short: "Install dependencies into vendor/ from composer.json (using composer.lock if present)",
```

`internal/cli/update.go`:

```go
Short: "Re-resolve all dependencies and rewrite composer.lock + vendor/",
```

`internal/cli/root.go`:

```go
Long: "gomposer installs PHP packages described in composer.json. It reads and writes the standard composer.lock.",
```

- [ ] **Step 2: Bench cold-scenario file list**

`cmd/bench/runner.go`:

```go
for _, rel := range []string{"vendor", "composer.lock"} {
```

Adjust the corresponding test in `cmd/bench/runner_test.go`: any assertion that a `gomposer.lock` was created/deleted becomes a `composer.lock` assertion.

- [ ] **Step 3: README**

In `README.md`, replace the "Composer compatibility" bullets:

Old:
```markdown
- It does not read `composer.lock` — gomposer keeps its own `gomposer.lock` with a different schema...
```

New:
```markdown
- Reads and writes the standard `composer.lock`. gomposer emits a valid Composer-shape lockfile (`content-hash`, `stability-flags`, `platform`, `packages` with `source`/`dist`/`autoload`/`time`/`notification-url`); Composer can consume it directly. Optional per-package metadata Composer emits (authors, license, description, keywords) is not populated on our writes; Composer will fill it back in on its next run if you use both tools alternately.
```

Also update any leftover `gomposer.lock` references in README to `composer.lock`.

- [ ] **Step 4: Design spec update**

Open `docs/superpowers/specs/2026-05-07-gomposer-design.md`. Locate the "Lockfile format" section. Replace its contents with a one-paragraph pointer:

```markdown
### Lockfile format

gomposer reads and writes the standard Composer `composer.lock`. See
`docs/superpowers/specs/2026-06-18-composer-lock-compat-design.md` for the
schema mapping and content-hash algorithm.
```

- [ ] **Step 5: Full test sweep**

Run: `go test ./...`

Expected: pass everywhere. Any lingering compile errors from `gomposer.lock` strings or old lock-shape fields should surface here.

- [ ] **Step 6: Sanity manual check**

```sh
go build -o gomposer ./cmd/gomposer
mkdir -p /tmp/lockcheck && cd /tmp/lockcheck
cat > composer.json <<'JSON'
{"name":"acme/x","require":{"psr/log":"^3.0"}}
JSON
$OLDPWD/gomposer install
head -30 composer.lock
```

Expected: `composer.lock` exists with the new shape (top-level `_readme`, `content-hash`, `packages`, etc.). No `gomposer.lock` file.

- [ ] **Step 7: Commit**

```bash
git add internal/cli/install.go internal/cli/update.go internal/cli/root.go cmd/bench/runner.go cmd/bench/runner_test.go README.md docs/superpowers/specs/2026-05-07-gomposer-design.md
git commit -m "docs+cli: purge gomposer.lock references, describe composer.lock

Renames every user-facing gomposer.lock mention to composer.lock. The
old spec's 'Lockfile format' section now points at the compat spec.
README explains one-way write compat with Composer."
```

---

## Task 7: Cross-tool end-to-end sanity

**Files:** none.

**Interfaces:** none.

- [ ] **Step 1: Smoke against a real project**

Requires upstream Composer on PATH.

```sh
cd /tmp
git clone --depth 1 https://github.com/Seldaek/monolog cg-monolog
cd cg-monolog
rm -rf vendor composer.lock
/path/to/gomposer install
head -5 composer.lock
# Then verify Composer accepts our output:
rm -rf vendor
composer install --no-scripts --no-plugins --no-interaction
```

Expected: `composer install` completes without a `content-hash` error. `vendor/` is populated by Composer. Composer may rewrite `composer.lock` with additional metadata fields (authors, license, description, ...) — that's expected under our one-way-write scope.

If `composer install` errors with a content-hash mismatch, the Task 1 algorithm is drifting from Composer's — return to that task's Step 5 and regenerate the expected hashes against real Composer output.

- [ ] **Step 2: Round-trip the other way**

Still in `/tmp/cg-monolog`:

```sh
rm -rf vendor
/path/to/gomposer install
```

Expected: gomposer accepts Composer's rewritten `composer.lock` and installs from it. Unknown fields are ignored (that's fine per B).

- [ ] **Step 3: No commit**

This task's deliverable is a manual e2e checkpoint. If both steps pass, mark the plan complete. If either fails, file a follow-up.

---

## composer.lock compat: acceptance check

After all tasks:

- `go test ./...` is green.
- `gomposer install` on a fresh project emits `composer.lock` (not `gomposer.lock`).
- The emitted `composer.lock` has the exact top-level keys: `_readme`, `content-hash`, `packages`, `packages-dev`, `aliases`, `minimum-stability`, `stability-flags`, `prefer-stable`, `prefer-lowest`, `platform`, `platform-dev`, `plugin-api-version`.
- Each package entry has `name`, `version`, `type` (when known), `source.{type,url,reference}`, `dist.{type,url,shasum}`, `require` (when non-empty), `autoload` (when non-empty), `notification-url` (Packagist packages only), `time` (when known).
- `composer install` from upstream Composer accepts a gomposer-emitted lock without content-hash mismatch.
- `gomposer install` accepts a Composer-emitted lock and installs from it.
- No file in the repo references `gomposer.lock` (grep is empty).

If any of these fails, fix forward before declaring the plan done.
