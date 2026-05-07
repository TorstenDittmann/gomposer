# Stage 2 / Plan 2: VCS (git) repositories + dev-* version handling

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal.** Make `"type": "vcs"` repositories declared in `composer.json` work end-to-end for git URLs. A user-defined VCS entry must contribute its tagged versions and its tracked branches (as `dev-<branch>` versions) to the resolver, with branch aliases (`extra.branch-alias`) honoured. When stage 1 only saw Packagist, stage 2 plan 2 introduces a multi-source aggregator so Packagist and any number of VCS repos can coexist behind one `registry.SourceLookup`. Auth is delegated to the user's existing git config — `auth.json` parsing is plan 4's job.

**Architecture.**

- `manifest.Manifest` gains a `Repositories []Repository` slice. Each repository carries `Type`, `URL`, plus optional `NoAPI`/`Only`/`Exclude` fields we ignore for now. `"type": "composer"` and `"type": "path"`/`"package"` produce a clear unsupported error.
- `internal/registry/vcs` is a new sub-package implementing `registry.SourceLookup` for one git URL.
  - On first `Lookup`, the client clones the repo as a bare mirror under `<cacheRoot>/vcs/<sha256(url)>.git` (idempotent — `git fetch --prune` if it already exists).
  - It uses `git ls-remote` to enumerate refs, picks tags + tracked branches, and for each ref runs `git show <ref>:composer.json` to read the per-version manifest. Each ref becomes a `registry.PackageVersion`. Tags map to their parsed version; branches map to `dev-<branch>`. The published name comes from the ref's own `composer.json#name`.
  - A configurable per-repo TTL (default 60s) gates `git fetch` so back-to-back lookups in one process do not refetch.
  - Per (url, ref) parsed manifests go through `parsedcache` so warm runs skip the `git show` round-trip entirely; the cache key is the bytes of `url\x00ref` so two different repos with the same ref names never collide.
- `internal/registry/multisource` aggregates an ordered list of `registry.SourceLookup`s. `Lookup(name)` returns the first hit; `ErrPackageNotFound` is propagated only when every child misses. The Packagist client and one VCS client per `repositories` entry are wrapped in this aggregator at the orchestrator layer (orchestrator wiring is touched lightly here; the full integration lives in plan 6's autoloader+integration plan).
- `dev-*` semantics. The existing `constraint.Version` already parses `dev-<branch>`. The resolver's `versions.go` already filters by `minStab`, which means setting `minimum-stability: dev` already lets dev branches in globally. This plan adds **explicit-require stability flags** — when a constraint string is exactly `dev-<branch>`, the resolver permits that single dev version even if `minStab` is `stable`, matching Composer's behaviour. We add `constraint.Constraint.RequiresExplicitDev()` and have the version lister exempt versions named in explicit-dev requires.
- Branch aliases. When a ref's `composer.json` declares `extra.branch-alias`, we synthesize a second `PackageVersion` per alias: same dist/source, but with `Version` set to the aliased version (e.g. `1.x-dev` for `dev-main as 1.x-dev`). The aliased version parses cleanly and is sortable with semver constraints so `^1.0` matches it. The original `dev-main` entry is preserved for `require: dev-main` consumers.

**Tech Stack.** Go stdlib `os/exec` for git, `encoding/json`, `crypto/sha256`. Existing `parsedcache`. No new external deps.

**Depends on.**
- Stage 1 plan 1 (constraint), plan 2 (registry types + caches), plan 3 (resolver).
- Plan 6 (orchestrator) is **not** a hard dep here: the wiring this plan touches is the `manifest.Manifest` shape and a tiny constructor exposed for the orchestrator to consume.

---

## File structure

| Path | Responsibility |
|------|---------------|
| `internal/manifest/manifest.go` | Add `Repository` and `Repositories` field. |
| `internal/manifest/manifest_test.go` | Coverage for repositories parsing, including unsupported types. |
| `internal/manifest/repository.go` | New helpers: `Validate`, `IsGit`. |
| `internal/manifest/repository_test.go` | Validation cases. |
| `internal/constraint/constraint.go` | Add `IsExplicitDev` predicate. |
| `internal/constraint/constraint_test.go` | Test new predicate. |
| `internal/resolver/versions.go` | Honour explicit-dev requires; carry stability flags from manifest. |
| `internal/resolver/versions_test.go` | Test that `dev-main` is admitted under `minimum-stability: stable`. |
| `internal/registry/multisource/multisource.go` | Ordered fan-out aggregator. |
| `internal/registry/multisource/multisource_test.go` | Round-trip tests. |
| `internal/registry/vcs/git.go` | Thin shell-out wrapper around `git`. |
| `internal/registry/vcs/git_test.go` | Tests using a real local bare repo created in `t.TempDir()`. |
| `internal/registry/vcs/vcs.go` | `Client` implementing `SourceLookup`. |
| `internal/registry/vcs/vcs_test.go` | End-to-end tests against a local file:// repo. |
| `internal/registry/vcs/alias.go` | `branch-alias` expansion. |
| `internal/registry/vcs/alias_test.go` | Alias expansion cases. |

---

## Task 1: Manifest — parse `repositories`

**Files:**
- Modify: `/Users/torstendittmann/Documents/skunk/composer-go/internal/manifest/manifest.go`
- Create: `/Users/torstendittmann/Documents/skunk/composer-go/internal/manifest/repository.go`
- Create: `/Users/torstendittmann/Documents/skunk/composer-go/internal/manifest/repository_test.go`

`composer.json`'s `repositories` may be an array of objects or (legacy) a map keyed by name. Stage 2 only needs the array form; we error out clearly on the map form so users know to upgrade. We also reject `"type": "composer"`, `"path"`, `"package"`, `"artifact"` for stage 2 with a stable error code message.

- [ ] **Step 1: Failing test for the new field**

Append to `/Users/torstendittmann/Documents/skunk/composer-go/internal/manifest/manifest_test.go` (create the file if it doesn't exist):

```go
package manifest

import "testing"

func TestParseRepositoriesArray(t *testing.T) {
	in := []byte(`{
	  "name": "demo/app",
	  "repositories": [
	    {"type": "vcs", "url": "https://github.com/example/lib.git"},
	    {"type": "vcs", "url": "git@github.com:example/other.git"}
	  ],
	  "require": {"example/lib": "^1.0"}
	}`)
	m, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.Repositories) != 2 {
		t.Fatalf("Repositories len=%d, want 2", len(m.Repositories))
	}
	if m.Repositories[0].Type != "vcs" || m.Repositories[0].URL != "https://github.com/example/lib.git" {
		t.Errorf("Repositories[0] = %+v", m.Repositories[0])
	}
}

func TestParseRepositoriesMissing(t *testing.T) {
	m, err := Parse([]byte(`{"name": "demo/app"}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.Repositories != nil {
		t.Errorf("Repositories = %+v, want nil", m.Repositories)
	}
}

func TestParseRepositoriesLegacyMapErrors(t *testing.T) {
	in := []byte(`{"repositories": {"foo": {"type": "vcs", "url": "x"}}}`)
	_, err := Parse(in)
	if err == nil {
		t.Fatal("expected error for legacy map form")
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/manifest/...`

Expected: build error on `m.Repositories`.

- [ ] **Step 3: Implement `Repository` and re-shape Parse**

Replace `/Users/torstendittmann/Documents/skunk/composer-go/internal/manifest/manifest.go`:

```go
// Package manifest parses composer.json files into a structured form.
// Parsing is pure: no network, no filesystem side effects.
package manifest

import (
	"encoding/json"
	"fmt"
)

// Manifest is the parsed view of a composer.json file. Fields not yet
// supported by composer-go are omitted; unknown fields in the input are
// ignored silently for forward-compatibility with future Composer features.
type Manifest struct {
	Name             string            `json:"name"`
	Type             string            `json:"type"`
	Require          map[string]string `json:"require,omitempty"`
	RequireDev       map[string]string `json:"require-dev,omitempty"`
	Autoload         Autoload          `json:"autoload,omitempty"`
	AutoloadDev      Autoload          `json:"autoload-dev,omitempty"`
	MinimumStability string            `json:"minimum-stability,omitempty"`
	PreferStable     bool              `json:"prefer-stable,omitempty"`
	// Repositories holds user-defined repository entries, in declaration
	// order. Only the array form is accepted; the legacy map form is a
	// hard error so users see an explicit upgrade path.
	Repositories []Repository `json:"-"`
}

// Repository is one entry from composer.json `repositories`. Fields beyond
// Type/URL are kept on the wire as a raw map so future stages can read them
// (Auth, Excludes, Only, etc.) without revising this struct.
type Repository struct {
	Type string         `json:"type"`
	URL  string         `json:"url"`
	Raw  map[string]any `json:"-"`
}

// rawManifest mirrors Manifest but with Repositories as json.RawMessage so we
// can disambiguate array vs map at decode time.
type rawManifest struct {
	Manifest
	Repositories json.RawMessage `json:"repositories,omitempty"`
}

// Parse decodes a composer.json byte slice. The error message includes the
// offset on JSON syntax errors so callers can surface useful diagnostics.
func Parse(data []byte) (*Manifest, error) {
	var raw rawManifest
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	m := raw.Manifest
	if len(raw.Repositories) > 0 {
		repos, err := parseRepositories(raw.Repositories)
		if err != nil {
			return nil, err
		}
		m.Repositories = repos
	}
	return &m, nil
}

func parseRepositories(data []byte) ([]Repository, error) {
	// trim leading whitespace
	for i := 0; i < len(data); i++ {
		switch data[i] {
		case ' ', '\t', '\n', '\r':
			continue
		case '[':
			return parseRepositoriesArray(data)
		case '{':
			return nil, fmt.Errorf("manifest: legacy map form of `repositories` is not supported; use the array form (composer-go CG203)")
		case 'f': // false / disable-defaults convention
			return nil, nil
		}
		break
	}
	return nil, fmt.Errorf("manifest: invalid `repositories` value")
}

func parseRepositoriesArray(data []byte) ([]Repository, error) {
	var entries []map[string]any
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("manifest: repositories: %w", err)
	}
	out := make([]Repository, 0, len(entries))
	for i, e := range entries {
		typ, _ := e["type"].(string)
		url, _ := e["url"].(string)
		out = append(out, Repository{Type: typ, URL: url, Raw: e})
		_ = i
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/manifest/...`

Expected: PASS.

- [ ] **Step 5: Add validation helpers**

Create `/Users/torstendittmann/Documents/skunk/composer-go/internal/manifest/repository.go`:

```go
package manifest

import "fmt"

// IsGit reports whether r is a VCS repository that this stage's VCS source
// can handle. Only "vcs" and "git" types map to git.
func (r Repository) IsGit() bool {
	switch r.Type {
	case "vcs", "git":
		return true
	}
	return false
}

// Validate returns nil for repository entries the resolver can use, or a
// human-readable error for unsupported types. The orchestrator calls this
// once per repository at startup.
func (r Repository) Validate() error {
	if r.URL == "" {
		return fmt.Errorf("manifest: repository missing `url`")
	}
	switch r.Type {
	case "vcs", "git":
		return nil
	case "composer":
		return fmt.Errorf("manifest: `composer` repositories are not supported in stage 2 (CG204): %s", r.URL)
	case "path":
		return fmt.Errorf("manifest: `path` repositories are not supported (CG205): %s", r.URL)
	case "package":
		return fmt.Errorf("manifest: inline `package` repositories are not supported (CG206)")
	case "artifact":
		return fmt.Errorf("manifest: `artifact` repositories are not supported (CG207)")
	case "":
		return fmt.Errorf("manifest: repository missing `type`: %s", r.URL)
	default:
		return fmt.Errorf("manifest: unknown repository type %q (CG208)", r.Type)
	}
}
```

Create `/Users/torstendittmann/Documents/skunk/composer-go/internal/manifest/repository_test.go`:

```go
package manifest

import (
	"strings"
	"testing"
)

func TestRepositoryValidate(t *testing.T) {
	cases := []struct {
		name string
		in   Repository
		want string // empty = OK
	}{
		{"git ok", Repository{Type: "vcs", URL: "https://example/x.git"}, ""},
		{"git alias", Repository{Type: "git", URL: "https://example/x.git"}, ""},
		{"composer rejected", Repository{Type: "composer", URL: "https://x"}, "CG204"},
		{"path rejected", Repository{Type: "path", URL: "../lib"}, "CG205"},
		{"missing url", Repository{Type: "vcs"}, "missing `url`"},
		{"missing type", Repository{URL: "https://x"}, "missing `type`"},
		{"unknown", Repository{Type: "fish", URL: "x"}, "CG208"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.in.Validate()
			if c.want == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Validate err = %v, want substring %q", err, c.want)
			}
		})
	}
}
```

- [ ] **Step 6: Run tests**

Run: `go test ./internal/manifest/...`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/manifest
git commit -m "feat(manifest): parse repositories array; reject legacy + unsupported types"
```

---

## Task 2: Constraint — explicit `dev-<branch>` predicate

**Files:**
- Modify: `/Users/torstendittmann/Documents/skunk/composer-go/internal/constraint/constraint.go`
- Modify: `/Users/torstendittmann/Documents/skunk/composer-go/internal/constraint/constraint_test.go`

Composer treats a constraint that is literally `dev-<branch>` (or `dev-<branch>#<ref>`) as a stability override for that single requirement: the dev version is admitted regardless of `minimum-stability`. We need a way for the resolver to detect that case.

- [ ] **Step 1: Failing test**

Append to `/Users/torstendittmann/Documents/skunk/composer-go/internal/constraint/constraint_test.go`:

```go
func TestIsExplicitDev(t *testing.T) {
	cases := map[string]bool{
		"dev-main":         true,
		"dev-master":       true,
		"dev-feature/foo":  true,
		"dev-main#abc1234": true,
		"^1.0":             false,
		"~2.3":             false,
		">=1.0,<2.0":       false,
		"":                 false,
		"dev-main || ^1.0": false, // mixed: not a pure explicit-dev require
	}
	for in, want := range cases {
		c, err := Parse(in)
		if err != nil && in != "" {
			// "" is genuinely invalid; skip
			t.Logf("parse %q: %v", in, err)
			continue
		}
		if got := c.IsExplicitDev(); got != want {
			t.Errorf("IsExplicitDev(%q) = %v, want %v", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/constraint/...`

Expected: build error on `IsExplicitDev`.

- [ ] **Step 3: Implement**

Append to `/Users/torstendittmann/Documents/skunk/composer-go/internal/constraint/constraint.go`:

```go
// IsExplicitDev reports whether the constraint is a single literal
// "dev-<branch>" (optionally with a "#<ref>" pin). When true, the resolver
// may admit the matching dev version even if minimum-stability is stricter.
func (c Constraint) IsExplicitDev() bool {
	s := strings.TrimSpace(c.Original)
	if !strings.HasPrefix(s, "dev-") {
		return false
	}
	// Reject anything that introduces a second alternative or a combinator.
	for _, ch := range s {
		switch ch {
		case '|', ',', ' ':
			return false
		}
	}
	// Allow "dev-foo" or "dev-foo#sha". Branch must be non-empty.
	body := strings.TrimPrefix(s, "dev-")
	if body == "" {
		return false
	}
	if i := strings.IndexByte(body, '#'); i >= 0 {
		body = body[:i]
	}
	return body != ""
}

// ExplicitDevBranch returns the branch name when IsExplicitDev is true,
// stripped of any "#sha" pin. Returns "" otherwise.
func (c Constraint) ExplicitDevBranch() string {
	if !c.IsExplicitDev() {
		return ""
	}
	body := strings.TrimPrefix(strings.TrimSpace(c.Original), "dev-")
	if i := strings.IndexByte(body, '#'); i >= 0 {
		body = body[:i]
	}
	return body
}
```

Confirm the `strings` import already exists; if not, add it.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/constraint/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/constraint
git commit -m "feat(constraint): IsExplicitDev predicate for stability-override requires"
```

---

## Task 3: Resolver — admit explicit `dev-*` requires under stricter minStab

**Files:**
- Modify: `/Users/torstendittmann/Documents/skunk/composer-go/internal/resolver/versions.go`
- Modify: `/Users/torstendittmann/Documents/skunk/composer-go/internal/resolver/versions_test.go`

Today `versions.go` filters by `minStab` only. We add a per-package allowlist of explicit-dev branches contributed by the manifest's `require`/`require-dev`. When a branch is on the allowlist, the dev version passes the stability gate even when `minStab > Dev`.

- [ ] **Step 1: Failing test**

Append to `/Users/torstendittmann/Documents/skunk/composer-go/internal/resolver/versions_test.go` (or create if absent):

```go
package resolver

import (
	"context"
	"testing"

	"github.com/torstendittmann/composer-go/internal/registry"
)

type fakeLookup struct {
	md map[string]*registry.PackageMetadata
}

func (f fakeLookup) Lookup(_ context.Context, name string) (*registry.PackageMetadata, error) {
	if v, ok := f.md[name]; ok {
		return v, nil
	}
	return nil, registry.ErrPackageNotFound
}

func TestVersionListerAdmitsExplicitDev(t *testing.T) {
	src := fakeLookup{md: map[string]*registry.PackageMetadata{
		"acme/lib": {
			Name: "acme/lib",
			Versions: []registry.PackageVersion{
				{Name: "acme/lib", Version: "1.0.0", VersionNorm: "1.0.0.0"},
				{Name: "acme/lib", Version: "dev-main", VersionNorm: "dev-main"},
			},
		},
	}}
	vl := newVersionLister(src, "stable")
	vl.AllowDevBranch("acme/lib", "main")
	got, err := vl.versions(context.Background(), "acme/lib")
	if err != nil {
		t.Fatal(err)
	}
	var foundDev bool
	for _, v := range got {
		if v.Raw == "dev-main" {
			foundDev = true
		}
	}
	if !foundDev {
		t.Fatalf("expected dev-main to be admitted; got %+v", got)
	}
}

func TestVersionListerStillFiltersUnlistedDev(t *testing.T) {
	src := fakeLookup{md: map[string]*registry.PackageMetadata{
		"acme/lib": {
			Name: "acme/lib",
			Versions: []registry.PackageVersion{
				{Name: "acme/lib", Version: "1.0.0", VersionNorm: "1.0.0.0"},
				{Name: "acme/lib", Version: "dev-feature", VersionNorm: "dev-feature"},
			},
		},
	}}
	vl := newVersionLister(src, "stable")
	got, _ := vl.versions(context.Background(), "acme/lib")
	for _, v := range got {
		if v.Raw == "dev-feature" {
			t.Fatalf("dev-feature should be filtered out without an allow entry")
		}
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/resolver/... -run TestVersionLister`

Expected: build error on `vl.AllowDevBranch`.

- [ ] **Step 3: Implement allowlist**

Edit `/Users/torstendittmann/Documents/skunk/composer-go/internal/resolver/versions.go`. Replace the struct and the `versions` method to consult the new map:

```go
type versionLister struct {
	src       registry.SourceLookup
	minStab   constraint.Stability
	cache     map[string][]listedVersion
	notFound  map[string]bool
	allowDev  map[string]map[string]bool // pkg -> branch set
}

func newVersionLister(src registry.SourceLookup, minStability string) *versionLister {
	return &versionLister{
		src:      src,
		minStab:  parseStabilityName(minStability),
		cache:    map[string][]listedVersion{},
		notFound: map[string]bool{},
		allowDev: map[string]map[string]bool{},
	}
}

// AllowDevBranch records that pkg's dev-<branch> version is admissible
// regardless of minimum stability. Call once per explicit-dev require before
// the first versions() call for that package.
func (vl *versionLister) AllowDevBranch(pkg, branch string) {
	m, ok := vl.allowDev[pkg]
	if !ok {
		m = map[string]bool{}
		vl.allowDev[pkg] = m
	}
	m[branch] = true
}

func (vl *versionLister) devAdmitted(pkg, branch string) bool {
	return vl.allowDev[pkg][branch]
}
```

Then update the filter loop in `versions()`:

```go
	for _, raw := range md.Versions {
		parsed, err := constraint.ParseVersion(raw.Version)
		if err != nil {
			continue
		}
		if parsed.Stability < vl.minStab {
			if !(parsed.Stability == constraint.Dev && vl.devAdmitted(pkg, parsed.Branch)) {
				continue
			}
		}
		out = append(out, listedVersion{Raw: raw.Version, Parsed: parsed, Record: raw})
	}
```

- [ ] **Step 4: Wire into Solve**

In `/Users/torstendittmann/Documents/skunk/composer-go/internal/resolver/solve.go`, after `vl := newVersionLister(...)` add a pass over the manifest's direct requires to register explicit-dev branches:

```go
	// Stability flags: explicit "dev-<branch>" requires admit that branch
	// regardless of minStab (Composer-compatible behaviour).
	registerExplicitDev := func(reqs map[string]string) {
		for pkg, raw := range reqs {
			c, err := constraint.Parse(raw)
			if err != nil {
				continue
			}
			if branch := c.ExplicitDevBranch(); branch != "" {
				vl.AllowDevBranch(pkg, branch)
			}
		}
	}
	registerExplicitDev(in.Manifest.Require)
	if in.IncludeDev {
		registerExplicitDev(in.Manifest.RequireDev)
	}
```

(Add `"github.com/torstendittmann/composer-go/internal/constraint"` to the imports of `solve.go` if missing.)

- [ ] **Step 5: Run tests**

Run: `go test ./internal/resolver/...`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/resolver
git commit -m "feat(resolver): explicit dev-<branch> requires bypass minimum-stability"
```

---

## Task 4: Multi-source aggregator

**Files:**
- Create: `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/multisource/multisource.go`
- Create: `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/multisource/multisource_test.go`

The aggregator queries each child in order. The first non-error, non-`ErrPackageNotFound` result wins. Genuine errors (network, timeout) propagate immediately — we do not fall through to a later source on a failed lookup, which matches Composer's "first repository wins" semantics and avoids accidentally serving stale data when a primary source is briefly down.

- [ ] **Step 1: Failing test**

Create the test file:

```go
package multisource

import (
	"context"
	"errors"
	"testing"

	"github.com/torstendittmann/composer-go/internal/registry"
)

type stubSource struct {
	name string
	md   *registry.PackageMetadata
	err  error
}

func (s stubSource) Lookup(_ context.Context, name string) (*registry.PackageMetadata, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.md == nil || s.md.Name != name {
		return nil, registry.ErrPackageNotFound
	}
	return s.md, nil
}

func TestAggregatorReturnsFirstHit(t *testing.T) {
	a := stubSource{md: &registry.PackageMetadata{Name: "acme/x", Versions: []registry.PackageVersion{{Name: "acme/x", Version: "1.0.0"}}}}
	b := stubSource{md: &registry.PackageMetadata{Name: "acme/x", Versions: []registry.PackageVersion{{Name: "acme/x", Version: "2.0.0"}}}}
	agg := New(a, b)
	got, err := agg.Lookup(context.Background(), "acme/x")
	if err != nil {
		t.Fatal(err)
	}
	if got.Versions[0].Version != "1.0.0" {
		t.Errorf("first hit should win; got %q", got.Versions[0].Version)
	}
}

func TestAggregatorFallsThroughNotFound(t *testing.T) {
	a := stubSource{}
	b := stubSource{md: &registry.PackageMetadata{Name: "acme/y"}}
	agg := New(a, b)
	got, err := agg.Lookup(context.Background(), "acme/y")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "acme/y" {
		t.Errorf("expected fallthrough to b; got %+v", got)
	}
}

func TestAggregatorAllMissReturnsNotFound(t *testing.T) {
	agg := New(stubSource{}, stubSource{})
	_, err := agg.Lookup(context.Background(), "acme/none")
	if !errors.Is(err, registry.ErrPackageNotFound) {
		t.Fatalf("err = %v, want ErrPackageNotFound", err)
	}
}

func TestAggregatorPropagatesHardError(t *testing.T) {
	hard := errors.New("network down")
	a := stubSource{err: hard}
	b := stubSource{md: &registry.PackageMetadata{Name: "acme/z"}}
	agg := New(a, b)
	_, err := agg.Lookup(context.Background(), "acme/z")
	if !errors.Is(err, hard) {
		t.Fatalf("expected hard error to propagate, got %v", err)
	}
}

func TestAggregatorEmpty(t *testing.T) {
	agg := New()
	_, err := agg.Lookup(context.Background(), "anything")
	if !errors.Is(err, registry.ErrPackageNotFound) {
		t.Fatalf("err = %v, want ErrPackageNotFound", err)
	}
}
```

- [ ] **Step 2: Implement**

Create `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/multisource/multisource.go`:

```go
// Package multisource aggregates multiple registry.SourceLookup
// implementations behind a single SourceLookup. Lookups are tried in
// declaration order; the first hit wins. ErrPackageNotFound from one source
// causes fallthrough; any other error stops the search and is returned.
package multisource

import (
	"context"
	"errors"
	"fmt"

	"github.com/torstendittmann/composer-go/internal/registry"
)

// Aggregator implements registry.SourceLookup over an ordered list of
// children. It is safe for concurrent use as long as every child is.
type Aggregator struct {
	children []registry.SourceLookup
}

// New returns an Aggregator over the given children.
func New(children ...registry.SourceLookup) *Aggregator {
	return &Aggregator{children: children}
}

// Lookup implements registry.SourceLookup.
func (a *Aggregator) Lookup(ctx context.Context, name string) (*registry.PackageMetadata, error) {
	if len(a.children) == 0 {
		return nil, fmt.Errorf("%s: %w", name, registry.ErrPackageNotFound)
	}
	for _, c := range a.children {
		md, err := c.Lookup(ctx, name)
		if err == nil {
			return md, nil
		}
		if errors.Is(err, registry.ErrPackageNotFound) {
			continue
		}
		return nil, err
	}
	return nil, fmt.Errorf("%s: %w", name, registry.ErrPackageNotFound)
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/registry/multisource/...`

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/registry/multisource
git commit -m "feat(registry/multisource): ordered fan-out aggregator"
```

---

## Task 5: Git command wrapper

**Files:**
- Create: `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/vcs/git.go`
- Create: `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/vcs/git_test.go`

A small typed wrapper around `git` so the rest of the code calls `git.LsRemote(ctx, dir)` rather than building `exec.Cmd`s by hand. Tests exercise it against a real bare repo created in `t.TempDir()`.

- [ ] **Step 1: Failing test**

Create `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/vcs/git_test.go`:

```go
package vcs

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeBareRepo creates a bare git repo with one commit on `main` containing
// the given composer.json bytes. Returns the file:// URL of the bare repo.
func makeBareRepo(t *testing.T, manifest string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	work := filepath.Join(root, "work")
	bare := filepath.Join(root, "bare.git")
	mustRun := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t",
			"GIT_COMMITTER_EMAIL=t@x", "HOME="+root)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	mustRun(root, "init", "-q", "-b", "main", work)
	if err := writeFile(filepath.Join(work, "composer.json"), manifest); err != nil {
		t.Fatal(err)
	}
	mustRun(work, "add", ".")
	mustRun(work, "commit", "-q", "-m", "init")
	mustRun(root, "clone", "-q", "--bare", work, bare)
	return "file://" + bare
}

func writeFile(path, body string) error {
	return writeFileBytes(path, []byte(body))
}

func TestLsRemoteAndShow(t *testing.T) {
	url := makeBareRepo(t, `{"name":"acme/widget","require":{"php":">=8.0"}}`)
	root := t.TempDir()
	bare := filepath.Join(root, "mirror.git")
	g := Git{}
	if err := g.CloneMirror(context.Background(), url, bare); err != nil {
		t.Fatalf("CloneMirror: %v", err)
	}
	refs, err := g.LsRemote(context.Background(), bare)
	if err != nil {
		t.Fatalf("LsRemote: %v", err)
	}
	var sawMain bool
	for _, r := range refs {
		if strings.HasSuffix(r.Name, "refs/heads/main") || r.Name == "main" {
			sawMain = true
		}
	}
	if !sawMain {
		t.Errorf("expected refs/heads/main in %+v", refs)
	}
	body, err := g.Show(context.Background(), bare, "main", "composer.json")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if !strings.Contains(string(body), `"acme/widget"`) {
		t.Errorf("Show body = %q", body)
	}
}
```

- [ ] **Step 2: Implement**

Create `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/vcs/git.go`:

```go
// Package vcs implements registry.SourceLookup backed by `git`. We shell
// out rather than embedding go-git: it keeps the binary small, reuses the
// user's existing SSH and credential-helper setup, and avoids reimplementing
// git's wire protocol.
package vcs

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Git is a stateless wrapper around the `git` binary.
type Git struct {
	// Binary is the git executable to invoke; defaults to "git".
	Binary string
}

func (g Git) bin() string {
	if g.Binary != "" {
		return g.Binary
	}
	return "git"
}

// Ref is one row from `git ls-remote`.
type Ref struct {
	SHA  string // 40-hex commit SHA
	Name string // full ref name, e.g. "refs/heads/main" or "refs/tags/v1.2.3"
}

// CloneMirror creates a bare mirror clone at dst. dst must not already exist.
// On success the directory contains a usable bare repository.
func (g Git) CloneMirror(ctx context.Context, url, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("vcs: clone target already exists: %s", dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, g.bin(), "clone", "--mirror", "--quiet", url, dst)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("vcs: clone %s: %w\n%s", url, err, out)
	}
	return nil
}

// Fetch refreshes a bare mirror; idempotent.
func (g Git) Fetch(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, g.bin(), "fetch", "--quiet", "--prune", "--tags", "origin")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("vcs: fetch %s: %w\n%s", dir, err, out)
	}
	return nil
}

// LsRemote enumerates refs in a local bare repo. Symbolic HEAD lines are
// skipped — callers that care about HEAD use HeadBranch instead.
func (g Git) LsRemote(ctx context.Context, dir string) ([]Ref, error) {
	cmd := exec.CommandContext(ctx, g.bin(), "ls-remote", "--heads", "--tags", ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("vcs: ls-remote %s: %w\n%s", dir, err, out)
	}
	var refs []Ref
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			continue
		}
		// Drop "^{}" peeled-tag rows; we treat the tag itself as the ref.
		if strings.HasSuffix(parts[1], "^{}") {
			continue
		}
		refs = append(refs, Ref{SHA: parts[0], Name: parts[1]})
	}
	return refs, nil
}

// HeadBranch returns the default branch name (e.g. "main"). On older gits
// without `symbolic-ref --short HEAD`, falls back to parsing `git remote
// show origin`.
func (g Git) HeadBranch(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, g.bin(), "symbolic-ref", "--short", "HEAD")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	// Fallback: parse remote show.
	cmd = exec.CommandContext(ctx, g.bin(), "remote", "show", "origin")
	cmd.Dir = dir
	out2, err2 := cmd.CombinedOutput()
	if err2 != nil {
		return "", fmt.Errorf("vcs: HEAD: %w\n%s", err, out)
	}
	for _, line := range strings.Split(string(out2), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "HEAD branch:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "HEAD branch:")), nil
		}
	}
	return "", fmt.Errorf("vcs: could not determine HEAD branch in %s", dir)
}

// Show returns the bytes of `path` at `ref` in the bare repo at dir. A
// missing path returns an empty slice and a nil error so callers can handle
// "ref has no composer.json" without inspecting error strings.
func (g Git) Show(ctx context.Context, dir, ref, path string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, g.bin(), "show", ref+":"+path)
	cmd.Dir = dir
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		txt := errBuf.String()
		if strings.Contains(txt, "exists on disk, but not in") ||
			strings.Contains(txt, "does not exist") ||
			strings.Contains(txt, "fatal: path") {
			return nil, nil
		}
		return nil, fmt.Errorf("vcs: show %s:%s: %w\n%s", ref, path, err, txt)
	}
	return out.Bytes(), nil
}
```

Add the small helper used by the test (place in the `_test.go` file, not the production file):

```go
// in git_test.go
func writeFileBytes(path string, b []byte) error {
	return os.WriteFile(path, b, 0o644)
}
```

…and add the `os` import to `git_test.go`.

- [ ] **Step 3: Run tests**

Run: `go test ./internal/registry/vcs/...`

Expected: PASS (skips on systems without git in PATH).

- [ ] **Step 4: Commit**

```bash
git add internal/registry/vcs
git commit -m "feat(vcs): typed wrapper around git ls-remote/show/clone"
```

---

## Task 6: VCS client — package-name discovery

**Files:**
- Create: `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/vcs/vcs.go`
- Create: `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/vcs/vcs_test.go`

The first feature of the VCS client is *self-naming*: given a clone URL, resolve which package this repo defines by reading `composer.json#name` on the default branch. The orchestrator uses that to know whether a `Lookup("acme/widget")` should consult this client at all.

- [ ] **Step 1: Failing test**

Create `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/vcs/vcs_test.go`:

```go
package vcs

import (
	"context"
	"path/filepath"
	"testing"
)

func TestClientReportsPackageName(t *testing.T) {
	url := makeBareRepo(t, `{"name":"acme/widget","require":{"php":">=8.0"}}`)
	cacheRoot := t.TempDir()
	c, err := New(Config{URL: url, CacheRoot: filepath.Join(cacheRoot, "vcs")})
	if err != nil {
		t.Fatal(err)
	}
	name, err := c.PackageName(context.Background())
	if err != nil {
		t.Fatalf("PackageName: %v", err)
	}
	if name != "acme/widget" {
		t.Errorf("name = %q", name)
	}
}
```

- [ ] **Step 2: Implement Config + Client + PackageName**

Create `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/vcs/vcs.go`:

```go
package vcs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/torstendittmann/composer-go/internal/cache/parsedcache"
	"github.com/torstendittmann/composer-go/internal/registry"
)

// Config configures a single VCS-backed lookup. One Config corresponds to
// one repositories[] entry in composer.json.
type Config struct {
	URL       string        // git clone URL (https or ssh)
	CacheRoot string        // root directory for the bare mirror; required
	FetchTTL  time.Duration // minimum interval between `git fetch` calls; 0 -> default 60s
	Git       Git           // git wrapper (allow overriding the binary in tests)
}

// Client is a registry.SourceLookup over one git URL.
type Client struct {
	cfg       Config
	mirrorDir string
	parsed    *parsedcache.Cache[refManifest]

	mu        sync.Mutex
	lastFetch time.Time
	cachedPkg string
	headBr    string
	versions  *registry.PackageMetadata // memoised lookup result
}

// refManifest is the parsedcache value: the decoded composer.json + the
// bytes hash so the resolver does not re-`git show` on a warm run.
type refManifest struct {
	Name        string
	Type        string
	Require     map[string]string
	RequireDev  map[string]string
	Autoload    registry.Autoload
	AutoloadDev registry.Autoload
	Suggest     map[string]string
	BranchAlias map[string]string // dev-foo -> 1.x-dev
}

// New creates a Client. CacheRoot is created lazily.
func New(cfg Config) (*Client, error) {
	if cfg.URL == "" {
		return nil, errors.New("vcs: URL is required")
	}
	if cfg.CacheRoot == "" {
		return nil, errors.New("vcs: CacheRoot is required")
	}
	if cfg.FetchTTL == 0 {
		cfg.FetchTTL = 60 * time.Second
	}
	mirror := filepath.Join(cfg.CacheRoot, "mirrors", urlKey(cfg.URL)+".git")
	parsedDir := filepath.Join(cfg.CacheRoot, "parsed")
	pc, err := parsedcache.New[refManifest](parsedDir)
	if err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, mirrorDir: mirror, parsed: pc}, nil
}

func urlKey(url string) string {
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:])
}

// ensureMirror clones if missing, otherwise refreshes if outside the TTL.
func (c *Client) ensureMirror(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := os.Stat(c.mirrorDir); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := c.cfg.Git.CloneMirror(ctx, c.cfg.URL, c.mirrorDir); err != nil {
			return err
		}
		c.lastFetch = time.Now()
		return nil
	}
	if time.Since(c.lastFetch) < c.cfg.FetchTTL {
		return nil
	}
	if err := c.cfg.Git.Fetch(ctx, c.mirrorDir); err != nil {
		return err
	}
	c.lastFetch = time.Now()
	return nil
}

// PackageName returns the `name` field from composer.json on the default
// branch. The orchestrator uses this to register name -> client mappings.
func (c *Client) PackageName(ctx context.Context) (string, error) {
	if c.cachedPkg != "" {
		return c.cachedPkg, nil
	}
	if err := c.ensureMirror(ctx); err != nil {
		return "", err
	}
	head, err := c.cfg.Git.HeadBranch(ctx, c.mirrorDir)
	if err != nil {
		return "", err
	}
	body, err := c.cfg.Git.Show(ctx, c.mirrorDir, head, "composer.json")
	if err != nil {
		return "", err
	}
	if len(body) == 0 {
		return "", fmt.Errorf("vcs: %s default branch has no composer.json", c.cfg.URL)
	}
	var manifest struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return "", fmt.Errorf("vcs: %s: invalid composer.json on %s: %w", c.cfg.URL, head, err)
	}
	if manifest.Name == "" {
		return "", fmt.Errorf("vcs: %s composer.json has no `name`", c.cfg.URL)
	}
	c.cachedPkg = manifest.Name
	c.headBr = head
	return c.cachedPkg, nil
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/registry/vcs/...`

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/registry/vcs
git commit -m "feat(vcs): Client.PackageName resolves repo URL to composer.json name"
```

---

## Task 7: VCS client — enumerate refs as PackageVersions

**Files:**
- Modify: `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/vcs/vcs.go`
- Modify: `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/vcs/vcs_test.go`

For each tag and each tracked branch we synthesize a `PackageVersion`:
- Tags become their tag name (with leading `v` tolerated by `constraint.ParseVersion`).
- Branches become `dev-<branch>`.
- Source = git@URL@SHA. Dist is empty (the fetcher will use git to checkout, not download a zip; that path is plan 5/6 work — for now we just leave Dist zero so the lockfile carries the source).

- [ ] **Step 1: Failing test for tags**

Append to `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/vcs/vcs_test.go`:

```go
import (
	"os/exec"
	"strings"

	"github.com/torstendittmann/composer-go/internal/registry"
)

// makeBareRepoMulti creates a bare repo with the given files committed on
// each ref. Map key is ref ("refs/heads/main", "refs/tags/v1.0.0"); value is
// the composer.json bytes for that ref.
func makeBareRepoMulti(t *testing.T, refs map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	work := filepath.Join(root, "work")
	bare := filepath.Join(root, "bare.git")
	mustRun := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t",
			"GIT_COMMITTER_EMAIL=t@x", "HOME="+root)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	mustRun(root, "init", "-q", "-b", "main", work)
	for ref, body := range refs {
		switch {
		case strings.HasPrefix(ref, "refs/heads/"):
			branch := strings.TrimPrefix(ref, "refs/heads/")
			if branch != "main" {
				mustRun(work, "checkout", "-q", "-b", branch)
			}
			_ = writeFileBytes(filepath.Join(work, "composer.json"), []byte(body))
			mustRun(work, "add", ".")
			mustRun(work, "commit", "-q", "-m", "ref "+ref)
			mustRun(work, "checkout", "-q", "main")
		case strings.HasPrefix(ref, "refs/tags/"):
			tag := strings.TrimPrefix(ref, "refs/tags/")
			_ = writeFileBytes(filepath.Join(work, "composer.json"), []byte(body))
			mustRun(work, "add", ".")
			mustRun(work, "commit", "-q", "-m", "tag "+tag)
			mustRun(work, "tag", tag)
		}
	}
	mustRun(root, "clone", "-q", "--bare", work, bare)
	return "file://" + bare
}

func TestClientLookupEnumeratesTagsAndBranches(t *testing.T) {
	url := makeBareRepoMulti(t, map[string]string{
		"refs/heads/main":  `{"name":"acme/widget","require":{"php":">=8.0"}}`,
		"refs/tags/v1.0.0": `{"name":"acme/widget","require":{"php":">=8.0"}}`,
		"refs/tags/v1.1.0": `{"name":"acme/widget","require":{"php":">=8.0"}}`,
	})
	c, err := New(Config{URL: url, CacheRoot: filepath.Join(t.TempDir(), "vcs")})
	if err != nil {
		t.Fatal(err)
	}
	md, err := c.Lookup(context.Background(), "acme/widget")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	got := map[string]bool{}
	for _, v := range md.Versions {
		got[v.Version] = true
	}
	for _, want := range []string{"1.0.0", "1.1.0", "dev-main"} {
		if !got[want] {
			t.Errorf("missing version %q in %+v", want, got)
		}
	}
}

func TestClientLookupWrongPackageName(t *testing.T) {
	url := makeBareRepoMulti(t, map[string]string{
		"refs/heads/main": `{"name":"acme/widget"}`,
	})
	c, _ := New(Config{URL: url, CacheRoot: filepath.Join(t.TempDir(), "vcs")})
	_, err := c.Lookup(context.Background(), "other/lib")
	if err == nil {
		t.Fatal("expected ErrPackageNotFound for wrong name")
	}
	if !errors.Is(err, registry.ErrPackageNotFound) {
		t.Errorf("err = %v, want ErrPackageNotFound", err)
	}
}
```

Add `"errors"` to the test imports if missing.

- [ ] **Step 2: Implement Lookup**

Append to `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/vcs/vcs.go`:

```go
// Lookup implements registry.SourceLookup. The first call enumerates refs
// and parses each ref's composer.json; subsequent calls in the same process
// return the memoised result.
func (c *Client) Lookup(ctx context.Context, name string) (*registry.PackageMetadata, error) {
	pkgName, err := c.PackageName(ctx)
	if err != nil {
		return nil, err
	}
	if pkgName != name {
		return nil, fmt.Errorf("%s: %w", name, registry.ErrPackageNotFound)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.versions != nil {
		return c.versions, nil
	}
	refs, err := c.cfg.Git.LsRemote(ctx, c.mirrorDir)
	if err != nil {
		return nil, err
	}
	out := &registry.PackageMetadata{Name: name, Versions: make([]registry.PackageVersion, 0, len(refs))}
	for _, r := range refs {
		ver, ok := refToVersion(r.Name)
		if !ok {
			continue
		}
		rm, err := c.refManifest(ctx, r.Name, r.SHA)
		if err != nil {
			// A ref without a composer.json or with malformed JSON is
			// silently skipped — this matches Composer's tolerance.
			continue
		}
		if rm.Name != "" && rm.Name != name {
			// The branch/tag claims to be a different package; skip.
			continue
		}
		base := registry.PackageVersion{
			Name:        name,
			Version:     ver,
			VersionNorm: ver,
			Type:        rm.Type,
			Source:      registry.Source{Type: "git", URL: c.cfg.URL, Ref: r.SHA},
			Dist:        registry.Dist{},
			Require:     rm.Require,
			RequireDev:  rm.RequireDev,
			Autoload:    rm.Autoload,
			AutoloadDev: rm.AutoloadDev,
			Suggest:     rm.Suggest,
		}
		out.Versions = append(out.Versions, base)
		// Branch aliases — synthesize aliased rows for each "dev-<branch> as
		// X" pair. See alias.go for the expansion rules.
		for _, alias := range expandAliases(ver, rm.BranchAlias) {
			aliased := base
			aliased.Version = alias
			aliased.VersionNorm = alias
			out.Versions = append(out.Versions, aliased)
		}
	}
	c.versions = out
	return out, nil
}

// refManifest reads composer.json for one ref, with a parsedcache layer
// keyed by url+sha so warm runs skip the `git show` round-trip.
func (c *Client) refManifest(ctx context.Context, refName, sha string) (refManifest, error) {
	cacheKey := []byte(c.cfg.URL + "\x00" + sha)
	if v, ok, _ := c.parsed.Load(cacheKey); ok {
		return v, nil
	}
	body, err := c.cfg.Git.Show(ctx, c.mirrorDir, refName, "composer.json")
	if err != nil {
		return refManifest{}, err
	}
	if len(body) == 0 {
		return refManifest{}, fmt.Errorf("vcs: %s@%s: no composer.json", c.cfg.URL, refName)
	}
	rm, err := decodeRefManifest(body)
	if err != nil {
		return refManifest{}, err
	}
	_ = c.parsed.Store(cacheKey, rm)
	return rm, nil
}

func decodeRefManifest(body []byte) (refManifest, error) {
	var raw struct {
		Name        string            `json:"name"`
		Type        string            `json:"type"`
		Require     map[string]string `json:"require"`
		RequireDev  map[string]string `json:"require-dev"`
		Autoload    registry.Autoload `json:"autoload"`
		AutoloadDev registry.Autoload `json:"autoload-dev"`
		Suggest     map[string]string `json:"suggest"`
		Extra       struct {
			BranchAlias map[string]string `json:"branch-alias"`
		} `json:"extra"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return refManifest{}, err
	}
	return refManifest{
		Name:        raw.Name,
		Type:        raw.Type,
		Require:     raw.Require,
		RequireDev:  raw.RequireDev,
		Autoload:    raw.Autoload,
		AutoloadDev: raw.AutoloadDev,
		Suggest:     raw.Suggest,
		BranchAlias: raw.Extra.BranchAlias,
	}, nil
}

// refToVersion maps a full ref name to a published version string.
//   refs/tags/v1.2.3      -> "1.2.3"   (leading v stripped; ParseVersion is tolerant)
//   refs/tags/1.2.3       -> "1.2.3"
//   refs/heads/main       -> "dev-main"
// Returns ok=false for refs we should skip (HEAD, notes, pull requests, etc).
func refToVersion(ref string) (string, bool) {
	const tagPrefix = "refs/tags/"
	const headPrefix = "refs/heads/"
	switch {
	case len(ref) > len(tagPrefix) && ref[:len(tagPrefix)] == tagPrefix:
		t := ref[len(tagPrefix):]
		if len(t) > 0 && t[0] == 'v' {
			t = t[1:]
		}
		return t, true
	case len(ref) > len(headPrefix) && ref[:len(headPrefix)] == headPrefix:
		return "dev-" + ref[len(headPrefix):], true
	}
	return "", false
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/registry/vcs/...`

Expected: PASS (TestClientLookupEnumeratesTagsAndBranches and friends).

- [ ] **Step 4: Commit**

```bash
git add internal/registry/vcs
git commit -m "feat(vcs): Lookup enumerates refs as PackageVersions with parsedcache"
```

---

## Task 8: Branch aliases (`extra.branch-alias`)

**Files:**
- Create: `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/vcs/alias.go`
- Create: `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/vcs/alias_test.go`

`extra.branch-alias` is a map from a dev-version to an aliased dev-version. The most common case is `"dev-main": "1.x-dev"`, which means: callers requiring `^1.0` (or `~1.0`, etc.) should be able to find `dev-main`. The aliased form (`1.x-dev`) parses as a numeric dev version and matches numeric range constraints — so we synthesize an extra `PackageVersion` row carrying that aliased version string. The original `dev-main` row stays so `require: dev-main` still works.

- [ ] **Step 1: Failing test**

Create `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/vcs/alias_test.go`:

```go
package vcs

import (
	"reflect"
	"sort"
	"testing"
)

func TestExpandAliases(t *testing.T) {
	cases := []struct {
		name    string
		ver     string
		aliases map[string]string
		want    []string
	}{
		{
			name:    "dev-main aliased to 1.x-dev",
			ver:     "dev-main",
			aliases: map[string]string{"dev-main": "1.x-dev"},
			want:    []string{"1.x-dev"},
		},
		{
			name:    "alias key without dev- prefix is tolerated",
			ver:     "dev-main",
			aliases: map[string]string{"main": "2.x-dev"},
			want:    []string{"2.x-dev"},
		},
		{
			name:    "no match",
			ver:     "dev-feature",
			aliases: map[string]string{"dev-main": "1.x-dev"},
			want:    nil,
		},
		{
			name:    "tagged version ignored",
			ver:     "1.0.0",
			aliases: map[string]string{"dev-main": "1.x-dev"},
			want:    nil,
		},
		{
			name:    "empty map",
			ver:     "dev-main",
			aliases: nil,
			want:    nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := expandAliases(c.ver, c.aliases)
			sort.Strings(got)
			sort.Strings(c.want)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: Implement**

Create `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/vcs/alias.go`:

```go
package vcs

import "strings"

// expandAliases returns the alias version strings that apply to ver, given
// the package's `extra.branch-alias` map. Only dev-* versions are aliased.
//
// Composer accepts branch-alias keys both with and without the "dev-"
// prefix. We honour both spellings.
func expandAliases(ver string, aliases map[string]string) []string {
	if len(aliases) == 0 || !strings.HasPrefix(ver, "dev-") {
		return nil
	}
	branch := strings.TrimPrefix(ver, "dev-")
	var out []string
	for k, v := range aliases {
		key := strings.TrimPrefix(k, "dev-")
		if key == branch && v != "" {
			out = append(out, v)
		}
	}
	return out
}
```

- [ ] **Step 3: Add an integration test**

Append to `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/vcs/vcs_test.go`:

```go
func TestClientExposesBranchAliasVersion(t *testing.T) {
	url := makeBareRepoMulti(t, map[string]string{
		"refs/heads/main": `{
			"name":"acme/widget",
			"require":{"php":">=8.0"},
			"extra":{"branch-alias":{"dev-main":"1.x-dev"}}
		}`,
	})
	c, _ := New(Config{URL: url, CacheRoot: filepath.Join(t.TempDir(), "vcs")})
	md, err := c.Lookup(context.Background(), "acme/widget")
	if err != nil {
		t.Fatal(err)
	}
	var sawDevMain, sawAlias bool
	for _, v := range md.Versions {
		if v.Version == "dev-main" {
			sawDevMain = true
		}
		if v.Version == "1.x-dev" {
			sawAlias = true
		}
	}
	if !sawDevMain || !sawAlias {
		t.Errorf("expected both dev-main and 1.x-dev rows, got dev-main=%v alias=%v", sawDevMain, sawAlias)
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/registry/vcs/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/registry/vcs
git commit -m "feat(vcs): expand extra.branch-alias into synthesized PackageVersions"
```

---

## Task 9: Constraint matches `1.x-dev` aliased versions

**Files:**
- Modify: `/Users/torstendittmann/Documents/skunk/composer-go/internal/constraint/constraint_test.go`
- Possibly modify: `/Users/torstendittmann/Documents/skunk/composer-go/internal/constraint/version.go`

A `1.x-dev` version must parse: major=1, minor=∞-ish, stability=Dev. The current parser does not recognise the `x-dev` suffix. Add support so `^1.0` matches it.

- [ ] **Step 1: Failing test**

Append to `/Users/torstendittmann/Documents/skunk/composer-go/internal/constraint/constraint_test.go`:

```go
func TestParseAliasVersion1xDev(t *testing.T) {
	v, err := ParseVersion("1.x-dev")
	if err != nil {
		t.Fatalf("ParseVersion: %v", err)
	}
	if v.Major != 1 {
		t.Errorf("Major = %d", v.Major)
	}
	if v.Stability != Dev {
		t.Errorf("Stability = %v", v.Stability)
	}
}

func TestCaretMatches1xDev(t *testing.T) {
	c, err := Parse("^1.0")
	if err != nil {
		t.Fatal(err)
	}
	v, _ := ParseVersion("1.x-dev")
	if !c.Satisfies(v) {
		t.Errorf("^1.0 should satisfy 1.x-dev")
	}
	v2, _ := ParseVersion("2.x-dev")
	if c.Satisfies(v2) {
		t.Errorf("^1.0 should NOT satisfy 2.x-dev")
	}
}
```

- [ ] **Step 2: Implement parser support for `<major>.x-dev` and `<major>.<minor>.x-dev`**

Modify `/Users/torstendittmann/Documents/skunk/composer-go/internal/constraint/version.go`. At the top of `ParseVersion`, after the leading-`v` strip, handle the alias form:

```go
	// Alias form: "1.x-dev", "1.2.x-dev", "1.x", "1.2.x" (Composer treats
	// both with and without -dev as dev-stability aliases — we accept both).
	if i := strings.Index(body, "x"); i >= 0 {
		// Tolerate optional trailing "-dev" / ".x-dev".
		head := strings.TrimRight(body[:i], ".")
		// Must look like an integer-and-dots prefix.
		ok := head != ""
		for _, ch := range head {
			if !(ch == '.' || (ch >= '0' && ch <= '9')) {
				ok = false
				break
			}
		}
		if ok {
			parts := strings.Split(head, ".")
			nums := []int{0, 0, 0}
			for j, p := range parts {
				if j >= 3 {
					break
				}
				n, err := strconv.Atoi(p)
				if err != nil {
					return v, fmt.Errorf("constraint: invalid alias version %q", s)
				}
				nums[j] = n
			}
			v.Major, v.Minor, v.Patch = nums[0], nums[1], nums[2]
			v.Stability = Dev
			v.Branch = s // keep the original form for diagnostics
			return v, nil
		}
	}
```

(The new branch must come after the `dev-<branch>` check so `dev-1.x` is not misclassified.)

Also tweak `Compare`: dev-aliased versions (where Branch != "" but Major != 0) should be ordered above pure dev branches but still below stable. The simplest fix is to keep them at Stability=Dev but only fall back to alphabetical-by-branch when both are pure dev (Major == 0). Update:

```go
func (v Version) Compare(other Version) int {
	// Two "pure" dev versions (no major/minor/patch numbers): alphabetical.
	if v.Stability == Dev && other.Stability == Dev && v.Major == 0 && other.Major == 0 {
		return strings.Compare(v.Branch, other.Branch)
	}
	if v.Stability == Dev && v.Major == 0 {
		return -1
	}
	if other.Stability == Dev && other.Major == 0 {
		return 1
	}
	if c := cmpInt(v.Major, other.Major); c != 0 {
		return c
	}
	if c := cmpInt(v.Minor, other.Minor); c != 0 {
		return c
	}
	if c := cmpInt(v.Patch, other.Patch); c != 0 {
		return c
	}
	if c := cmpInt(int(v.Stability), int(other.Stability)); c != 0 {
		return c
	}
	return cmpInt(v.PreNum, other.PreNum)
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/constraint/...`

Expected: PASS, including pre-existing tests.

- [ ] **Step 4: Commit**

```bash
git add internal/constraint
git commit -m "feat(constraint): parse and order <N>.x-dev branch-alias versions"
```

---

## Task 10: VCS lookup short-circuits when name is not its own

**Files:**
- Modify: `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/vcs/vcs_test.go`

Already partially exercised by `TestClientLookupWrongPackageName`. Add a stronger test confirming we do not even hit the network when we know the configured URL serves a different package.

- [ ] **Step 1: Test**

Append:

```go
func TestClientLookupCachesNegativeName(t *testing.T) {
	url := makeBareRepoMulti(t, map[string]string{
		"refs/heads/main": `{"name":"acme/widget"}`,
	})
	c, _ := New(Config{URL: url, CacheRoot: filepath.Join(t.TempDir(), "vcs")})
	if _, err := c.PackageName(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Move the bare repo aside; subsequent Lookup for the wrong name must
	// still return ErrPackageNotFound (no fresh git invocations needed).
	if err := os.RemoveAll(c.mirrorDir); err != nil {
		t.Fatal(err)
	}
	_, err := c.Lookup(context.Background(), "other/lib")
	if !errors.Is(err, registry.ErrPackageNotFound) {
		t.Fatalf("err = %v, want ErrPackageNotFound", err)
	}
}
```

Add `"os"` to test imports if missing.

- [ ] **Step 2: Run tests**

Run: `go test ./internal/registry/vcs/...`

Expected: PASS (already, given the cached `cachedPkg` short-circuit in PackageName + the early return in Lookup).

- [ ] **Step 3: Commit**

```bash
git add internal/registry/vcs
git commit -m "test(vcs): negative-name lookup avoids re-hitting git"
```

---

## Task 11: Builder — `vcs.NewFromManifest`

**Files:**
- Modify: `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/vcs/vcs.go`
- Modify: `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/vcs/vcs_test.go`

A small helper turns a slice of `manifest.Repository` into a slice of `*Client`. The orchestrator calls this and passes the result to `multisource.New(packagistClient, vcsClients...)`.

- [ ] **Step 1: Test**

Append to `vcs_test.go`:

```go
import (
	"github.com/torstendittmann/composer-go/internal/manifest"
)

func TestNewFromManifest(t *testing.T) {
	url := makeBareRepoMulti(t, map[string]string{
		"refs/heads/main": `{"name":"acme/widget"}`,
	})
	root := t.TempDir()
	clients, err := NewFromManifest([]manifest.Repository{
		{Type: "vcs", URL: url},
	}, Options{CacheRoot: filepath.Join(root, "vcs")})
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 1 {
		t.Fatalf("clients len = %d", len(clients))
	}
	name, err := clients[0].PackageName(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if name != "acme/widget" {
		t.Errorf("name = %q", name)
	}
}

func TestNewFromManifestSkipsUnsupported(t *testing.T) {
	root := t.TempDir()
	clients, err := NewFromManifest([]manifest.Repository{
		{Type: "composer", URL: "https://x"}, // unsupported, must error
	}, Options{CacheRoot: filepath.Join(root, "vcs")})
	if err == nil {
		t.Fatalf("expected error for composer-type, got %d clients", len(clients))
	}
}
```

- [ ] **Step 2: Implement**

Append to `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/vcs/vcs.go`:

```go
import (
	// ... existing imports ...
	"github.com/torstendittmann/composer-go/internal/manifest"
)
```

(Move imports as needed; in Go they live in one block.)

```go
// Options is shared configuration for a batch of VCS clients.
type Options struct {
	CacheRoot string
	FetchTTL  time.Duration
	Git       Git
}

// NewFromManifest builds one Client per supported repository entry. It
// returns an error for unsupported types so misconfigurations surface early.
func NewFromManifest(repos []manifest.Repository, opts Options) ([]*Client, error) {
	var out []*Client
	for _, r := range repos {
		if err := r.Validate(); err != nil {
			return nil, err
		}
		if !r.IsGit() {
			continue
		}
		c, err := New(Config{
			URL:       r.URL,
			CacheRoot: opts.CacheRoot,
			FetchTTL:  opts.FetchTTL,
			Git:       opts.Git,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/registry/vcs/...`

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/registry/vcs
git commit -m "feat(vcs): NewFromManifest helper for orchestrator wiring"
```

---

## Task 12: Multisource adapts a slice of `*vcs.Client`

**Files:**
- Modify: `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/multisource/multisource_test.go`

A typed-slice helper avoids the orchestrator having to write a manual conversion to `[]registry.SourceLookup`.

- [ ] **Step 1: Test**

Append:

```go
import (
	"github.com/torstendittmann/composer-go/internal/registry"
)

type stubByName map[string]*registry.PackageMetadata

func (s stubByName) Lookup(_ context.Context, name string) (*registry.PackageMetadata, error) {
	if v, ok := s[name]; ok {
		return v, nil
	}
	return nil, registry.ErrPackageNotFound
}

func TestNewWithLookups(t *testing.T) {
	a := stubByName{"acme/x": {Name: "acme/x"}}
	b := stubByName{"acme/y": {Name: "acme/y"}}
	agg := NewWithLookups([]registry.SourceLookup{a, b})
	if _, err := agg.Lookup(context.Background(), "acme/y"); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Implement**

Append to `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/multisource/multisource.go`:

```go
// NewWithLookups is identical to New but takes a slice. Useful when the
// caller already has a typed slice (e.g. []*vcs.Client converted via the
// SourceLookup interface).
func NewWithLookups(children []registry.SourceLookup) *Aggregator {
	return &Aggregator{children: children}
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/registry/multisource/...`

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/registry/multisource
git commit -m "feat(multisource): NewWithLookups slice constructor"
```

---

## Task 13: End-to-end resolver test against a VCS source

**Files:**
- Create: `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/vcs/integration_test.go`

Wire `vcs.Client` + `multisource.Aggregator` into the resolver and confirm a real resolution works for a constraint that targets a tagged version, a `dev-main` constraint, and a `^1.0` constraint that hits a `1.x-dev` alias.

- [ ] **Step 1: Test**

Create the file:

```go
package vcs

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/torstendittmann/composer-go/internal/manifest"
	"github.com/torstendittmann/composer-go/internal/registry/multisource"
	"github.com/torstendittmann/composer-go/internal/resolver"
)

func TestResolverFindsTaggedVersion(t *testing.T) {
	url := makeBareRepoMulti(t, map[string]string{
		"refs/heads/main":  `{"name":"acme/widget"}`,
		"refs/tags/v1.0.0": `{"name":"acme/widget"}`,
		"refs/tags/v1.1.0": `{"name":"acme/widget"}`,
	})
	c, _ := New(Config{URL: url, CacheRoot: filepath.Join(t.TempDir(), "vcs")})
	src := multisource.New(c)
	m := &manifest.Manifest{
		Name:    "demo/app",
		Require: map[string]string{"acme/widget": "^1.0"},
	}
	res, err := resolver.Solve(context.Background(), resolver.Input{
		Manifest:   m,
		Source:     src,
		IncludeDev: false,
	})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if len(res.Packages) != 1 {
		t.Fatalf("Packages = %d", len(res.Packages))
	}
	if res.Packages[0].Record.Version != "1.1.0" {
		t.Errorf("picked %q, want 1.1.0", res.Packages[0].Record.Version)
	}
}

func TestResolverFindsExplicitDevMain(t *testing.T) {
	url := makeBareRepoMulti(t, map[string]string{
		"refs/heads/main":  `{"name":"acme/widget"}`,
		"refs/tags/v1.0.0": `{"name":"acme/widget"}`,
	})
	c, _ := New(Config{URL: url, CacheRoot: filepath.Join(t.TempDir(), "vcs")})
	src := multisource.New(c)
	m := &manifest.Manifest{
		Name:    "demo/app",
		Require: map[string]string{"acme/widget": "dev-main"},
		// Note: no minimum-stability change. Stage 2 plan 2 admits explicit dev-* requires.
	}
	res, err := resolver.Solve(context.Background(), resolver.Input{
		Manifest: m, Source: src, IncludeDev: false,
	})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if res.Packages[0].Record.Version != "dev-main" {
		t.Errorf("picked %q, want dev-main", res.Packages[0].Record.Version)
	}
}

func TestResolverFindsBranchAliasUnderCaret(t *testing.T) {
	url := makeBareRepoMulti(t, map[string]string{
		"refs/heads/main": `{
			"name":"acme/widget",
			"extra":{"branch-alias":{"dev-main":"1.x-dev"}}
		}`,
	})
	c, _ := New(Config{URL: url, CacheRoot: filepath.Join(t.TempDir(), "vcs")})
	src := multisource.New(c)
	m := &manifest.Manifest{
		Name:             "demo/app",
		Require:          map[string]string{"acme/widget": "^1.0"},
		MinimumStability: "dev", // alias matching still requires dev-stability admittance
	}
	res, err := resolver.Solve(context.Background(), resolver.Input{
		Manifest: m, Source: src, IncludeDev: false,
	})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	picked := res.Packages[0].Record.Version
	if picked != "1.x-dev" {
		t.Errorf("picked %q, want 1.x-dev", picked)
	}
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./internal/registry/vcs/... -run TestResolver`

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/registry/vcs
git commit -m "test(vcs): resolver picks tagged, dev-main, and aliased versions"
```

---

## Task 14: Live network smoke test (skipped by default)

**Files:**
- Modify: `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/vcs/vcs_test.go`

A confidence test against a stable public repo, gated on an env var so it does not run on CI by default.

- [ ] **Step 1: Append the test**

```go
func TestLiveLookupPublicRepo(t *testing.T) {
	if os.Getenv("COMPOSER_GO_LIVE_NETWORK") != "1" {
		t.Skip("set COMPOSER_GO_LIVE_NETWORK=1 to run")
	}
	c, err := New(Config{
		URL:       "https://github.com/Seldaek/monolog.git",
		CacheRoot: filepath.Join(t.TempDir(), "vcs"),
	})
	if err != nil {
		t.Fatal(err)
	}
	name, err := c.PackageName(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if name != "monolog/monolog" {
		t.Errorf("name = %q", name)
	}
	md, err := c.Lookup(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	if len(md.Versions) < 5 {
		t.Errorf("want >=5 versions, got %d", len(md.Versions))
	}
}
```

- [ ] **Step 2: Run gated**

Run: `COMPOSER_GO_LIVE_NETWORK=1 go test ./internal/registry/vcs/... -run TestLive -v`

Expected: PASS, lots of versions.

- [ ] **Step 3: Run ungated**

Run: `go test ./internal/registry/vcs/... -v`

Expected: TestLive SKIPPED, others PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/registry/vcs
git commit -m "test(vcs): live-network smoke test gated on env var"
```

---

## Task 15: Documentation comments on the public surface

**Files:**
- Modify: `/Users/torstendittmann/Documents/skunk/composer-go/internal/registry/vcs/vcs.go`
- Modify: `/Users/torstendittmann/Documents/skunk/composer-go/internal/manifest/manifest.go`

A short package-level doc comment for `vcs` and updated comment on `Manifest.Repositories` clarifying invariants for downstream readers.

- [ ] **Step 1: Edit `vcs.go` package doc**

Replace the package-doc comment at the top of `vcs.go` with the following text (keep the package declaration unchanged):

```go
// Package vcs implements registry.SourceLookup against a single git URL.
//
// One Client wraps one repository. Calls to Lookup enumerate the repo's
// tags and tracked branches, parse each ref's composer.json, and return one
// PackageVersion per ref:
//
//   - tags become normalized versions (leading "v" tolerated by the
//     constraint parser), e.g. "refs/tags/v1.2.3" -> "1.2.3";
//   - branches become "dev-<branch>" rows;
//   - extra.branch-alias entries (e.g. {"dev-main":"1.x-dev"}) produce
//     additional synthesized rows so that range constraints like "^1.0"
//     can match a development branch.
//
// The package shells out to git rather than embedding go-git: it keeps the
// binary small, reuses the user's existing SSH and credential helper
// configuration, and avoids reimplementing git's wire protocol. Auth is out
// of scope for this package; plan 4 layers auth.json handling on top.
//
// Caching:
//   - the bare mirror lives at <CacheRoot>/mirrors/<sha256(url)>.git;
//   - per-(url, sha) refManifest values are persisted via parsedcache so
//     warm runs skip `git show` entirely;
//   - `git fetch` is rate-limited by Config.FetchTTL (default 60s) so
//     back-to-back lookups in one process do not refetch.
```

- [ ] **Step 2: Tighten `Manifest.Repositories` comment**

Edit `/Users/torstendittmann/Documents/skunk/composer-go/internal/manifest/manifest.go` and replace the comment on `Repositories` with:

```go
	// Repositories holds user-defined repository entries, in declaration
	// order. Only the JSON array form is accepted; the legacy map form is a
	// hard error (CG203). Validation of individual entries (supported types,
	// required fields) is performed by Repository.Validate, called by the
	// orchestrator at startup so misconfigurations surface before any I/O.
```

- [ ] **Step 3: Run go vet to catch typos**

Run: `go vet ./...`

Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add internal/registry/vcs internal/manifest
git commit -m "docs(vcs,manifest): package-level docs for the new VCS surface"
```

---

## Plan 2 acceptance check

Run all of these:

- `go test ./...` is green offline (with `git` available in PATH; the VCS tests skip otherwise).
- `go vet ./...` is clean.
- `COMPOSER_GO_LIVE_NETWORK=1 go test ./internal/registry/vcs/... -run TestLive` clones a real public repo and finds at least five published versions.
- A composer.json with a single VCS repo and `require: { acme/widget: "^1.0" }` resolves to the highest matching tag.
- The same project with `require: { acme/widget: "dev-main" }` resolves to the branch even if `minimum-stability` is unset.
- The same project with `require: { acme/widget: "^1.0" }` and `minimum-stability: dev` and a `branch-alias` of `{"dev-main":"1.x-dev"}` resolves to the aliased branch.
- The aggregator round-trips: a manifest that lists both Packagist and a VCS source resolves a Packagist-only package and a VCS-only package in the same `Solve` call (covered indirectly by the unit tests; full integration ships with plan 6).

Out of scope and explicitly deferred to later plans:

- `auth.json` parsing — plan 4.
- Fetching package contents (zip vs git checkout) — plan 5/6.
- `path` and `package` repository types — post-MVP per spec.
- GitHub API metadata short-circuit (`api.github.com/repos/.../contents/composer.json`) — possible stage 3 optimisation, not part of this plan.
