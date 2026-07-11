# Workspaces (Scope 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship pnpm/bun-inspired workspace support: a `"workspaces"` glob array in the root `composer.json`, a `workspace:*`/`workspace:<constraint>` protocol for cross-workspace deps, and a single install at the root that produces a shared `vendor/` with symlinks tying everything together.

**Architecture:** Discovery lives in `internal/manifest`; constraint parser recognizes `workspace:` prefix; a new `internal/orchestrator/workspace_aggregate.go` builds the virtual super-manifest fed to the existing resolver; a new `internal/orchestrator/workspace_symlink.go` runs post-materialize to lay down the symlink structure; CLI gains a walk-up-to-workspace-root step. No changes to the resolver or fetcher.

**Tech Stack:** Go 1.25, existing stdlib usage (`path/filepath` for globs and symlinks), no new dependencies.

## Global Constraints

- **Discovery field:** top-level `"workspaces"` in the root `composer.json`, `[]string` of glob patterns.
- **Protocol:** `workspace:*` (any version), `workspace:<constraint>` (any Composer-style constraint that `constraint.Parse` accepts on the tail). Constraint parser tags the returned `Constraint` with `IsWorkspace bool = true`.
- **Aggregate resolve:** super-manifest is the union of every workspace's requires (root + workspaces), minus every `workspace:`-tagged entry. Resolver sees external deps only; synthetic workspace entries are added to the resolver result post-solve.
- **Vendor layout:**
  - Repo-root `vendor/` is a real directory.
  - Each workspace's `vendor/` is a relative symlink to the repo-root `vendor/` — replacing a real dir if present.
  - Each cross-workspace dep at `vendor/<vendor>/<name>` is a relative symlink to the workspace source dir.
- **Autoloader:** every workspace appears in the autoload generator's package set as `Entry{Name: "acme/shared", Version: "1.0.0", InstallPath: "vendor/acme/shared", Autoload: <workspace-manifest.autoload>}`. Same shape as an external package.
- **Lockfile:** synthetic entries `{"type": "workspace", "source": {"type": "path", "url": "packages/shared"}, "dist": {}}` land in `gomposer.lock`. `manifestContentHash` extends to include every workspace's manifest bytes concatenated in workspace-sort-order (workspace name ascending).
- **CLI walk-up:** `install` and `update` walk up from CWD until they find a `composer.json` whose parsed `Workspaces` is non-empty. Stop at filesystem root or on entering a directory containing `.git` when its own `composer.json` doesn't declare workspaces (a plain single-project repo shouldn't be sucked into some ancestor's workspace).
- **Error surface:**
  - Duplicate workspace name → `"workspaces: duplicate name %q at %s and %s"`.
  - Unknown target in `workspace:` require → `"workspaces: workspace:%s require %q not found in workspace set"`.
  - `workspace:<constraint>` fails against target's declared version → `"workspaces: %s requires %s (workspace:%s) but workspace has version %q"`.
  - `workspace:<constraint>` targets a workspace without a version → `"workspaces: %s requires %s (workspace:%s) but workspace has no version field"`.
- **Backward compat:** projects with no `"workspaces"` field or an empty array install exactly as today. The walk-up terminates without a match and CLI falls back to CWD-only behavior.

## File structure

| Path | Responsibility |
|------|---------------|
| `internal/manifest/manifest.go` | Add `Workspaces []string \`json:"workspaces,omitempty"\`` to `Manifest`. |
| `internal/manifest/workspaces.go` | New. `DiscoverWorkspaces(rootDir string, root *Manifest) ([]Workspace, error)`. |
| `internal/manifest/workspaces_test.go` | Discovery, dedup, missing-dir warning, empty-array short-circuit. |
| `internal/constraint/constraint.go` | `Parse` handles `workspace:` prefix; `Constraint.IsWorkspace bool`. |
| `internal/constraint/constraint_test.go` | New cases exercising the protocol. |
| `internal/orchestrator/workspace_aggregate.go` | New. Build super-manifest; validate `workspace:` requires; produce synthetic resolver entries. |
| `internal/orchestrator/workspace_aggregate_test.go` | Union semantics; error cases. |
| `internal/orchestrator/workspace_symlink.go` | New. Post-materialize: workspace vendors → root vendor symlinks; cross-workspace deps → workspace source symlinks. |
| `internal/orchestrator/workspace_symlink_test.go` | Layout assertions on a temp-dir fixture. |
| `internal/orchestrator/pipeline.go` | Wire discovery + aggregate + symlink pass. Feed synthetic entries into `resolver.Result`. Extend `manifestContentHash` to cover workspace manifests. |
| `internal/orchestrator/pipeline_test.go` | Add an integration test using the `workspaces-simple` fixture. |
| `internal/orchestrator/testdata/workspaces-simple/` | New. Root + `packages/shared/` + `apps/api/`. |
| `internal/cli/root.go` | Walk-up-to-workspace-root when `--project` isn't set. |
| `internal/cli/root_test.go` (or sibling) | Walk-up finds the workspace root; ancestor with `.git` boundary respected. |
| `README.md` | Add a short "Workspaces" section pointing at the spec. |

---

## Task 1: `manifest.Workspaces` field + `DiscoverWorkspaces`

**Files:**
- Modify: `internal/manifest/manifest.go`
- Create: `internal/manifest/workspaces.go`
- Create: `internal/manifest/workspaces_test.go`
- Create: `internal/manifest/testdata/workspaces-simple/composer.json`
- Create: `internal/manifest/testdata/workspaces-simple/packages/shared/composer.json`
- Create: `internal/manifest/testdata/workspaces-simple/apps/api/composer.json`
- Create: `internal/manifest/testdata/workspaces-simple/apps/api/src/App.php` (empty file — just so the dir isn't empty; walker doesn't care about contents)

**Interfaces:**
- Consumes: `filepath.Glob`, existing `manifest.Load`.
- Produces:
  - `Manifest.Workspaces []string` — parsed workspace glob patterns.
  - `type Workspace struct { Name string; Dir string; Manifest *Manifest; Version string }` — one per discovered workspace.
  - `func DiscoverWorkspaces(rootDir string, root *Manifest, warnf func(format string, args ...any)) ([]Workspace, error)` — glob-expand, load each manifest, dedup check. `warnf` is the caller's stderr writer (mockable in tests).

- [ ] **Step 1: Write the fixture composer.json files**

`internal/manifest/testdata/workspaces-simple/composer.json`:

```json
{
    "name": "acme/monorepo",
    "workspaces": ["packages/*", "apps/*"]
}
```

`internal/manifest/testdata/workspaces-simple/packages/shared/composer.json`:

```json
{
    "name": "acme/shared",
    "version": "1.0.0",
    "autoload": { "psr-4": { "Acme\\Shared\\": "src/" } }
}
```

`internal/manifest/testdata/workspaces-simple/apps/api/composer.json`:

```json
{
    "name": "acme/api",
    "require": { "acme/shared": "workspace:^1.0" }
}
```

`internal/manifest/testdata/workspaces-simple/apps/api/src/App.php`: empty file.

- [ ] **Step 2: Write the failing tests**

Create `internal/manifest/workspaces_test.go`:

```go
package manifest

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestManifestParsesWorkspacesField(t *testing.T) {
	body := []byte(`{"name":"acme/monorepo","workspaces":["packages/*","apps/*"]}`)
	m, err := Decode(body)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	want := []string{"packages/*", "apps/*"}
	if len(m.Workspaces) != len(want) {
		t.Fatalf("Workspaces = %v, want %v", m.Workspaces, want)
	}
	for i := range want {
		if m.Workspaces[i] != want[i] {
			t.Errorf("Workspaces[%d] = %q, want %q", i, m.Workspaces[i], want[i])
		}
	}
}

func TestDiscoverWorkspacesFindsAll(t *testing.T) {
	rootDir := filepath.Join("testdata", "workspaces-simple")
	root, err := Load(rootDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, err := DiscoverWorkspaces(rootDir, root, nil)
	if err != nil {
		t.Fatalf("DiscoverWorkspaces: %v", err)
	}
	sort.Slice(got, func(i, j int) bool { return got[i].Name < got[j].Name })
	if len(got) != 2 {
		t.Fatalf("got %d workspaces, want 2", len(got))
	}
	if got[0].Name != "acme/api" || got[1].Name != "acme/shared" {
		t.Errorf("names = %v", []string{got[0].Name, got[1].Name})
	}
	if got[1].Version != "1.0.0" {
		t.Errorf("shared.Version = %q, want 1.0.0", got[1].Version)
	}
	if !strings.HasSuffix(filepath.ToSlash(got[1].Dir), "packages/shared") {
		t.Errorf("shared.Dir = %q, want …/packages/shared", got[1].Dir)
	}
}

func TestDiscoverWorkspacesEmptyGlobWarns(t *testing.T) {
	// Root manifest with a glob that matches zero dirs. Warning to warnf; no
	// workspaces returned; no error.
	root := &Manifest{Workspaces: []string{"nowhere/*"}}
	var warnings []string
	got, err := DiscoverWorkspaces(t.TempDir(), root, func(format string, args ...any) {
		warnings = append(warnings, format)
	})
	if err != nil {
		t.Fatalf("DiscoverWorkspaces: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d workspaces, want 0", len(got))
	}
	if len(warnings) != 1 {
		t.Errorf("warnings = %v, want 1 warning", warnings)
	}
}

func TestDiscoverWorkspacesDuplicateNameHardFails(t *testing.T) {
	dir := t.TempDir()
	mkComposer := func(rel, name string) {
		abs := filepath.Join(dir, rel)
		if err := writeFile(t, abs, `{"name":"`+name+`"}`); err != nil {
			t.Fatal(err)
		}
	}
	mkComposer("packages/a/composer.json", "acme/thing")
	mkComposer("packages/b/composer.json", "acme/thing")
	root := &Manifest{Workspaces: []string{"packages/*"}}
	_, err := DiscoverWorkspaces(dir, root, nil)
	if err == nil {
		t.Fatal("expected error on duplicate name")
	}
	if !strings.Contains(err.Error(), "duplicate name") {
		t.Errorf("err = %v", err)
	}
}

func TestDiscoverWorkspacesEmptyArrayShortCircuits(t *testing.T) {
	root := &Manifest{Workspaces: []string{}}
	got, err := DiscoverWorkspaces(t.TempDir(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

// writeFile creates parent dirs and writes body.
func writeFile(t *testing.T, path, body string) error {
	// implementation deferred to Step 3 (or inline it here)
	t.Helper()
	if err := mkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	return writeAll(path, []byte(body))
}

// mkdirAll and writeAll are thin wrappers around os.MkdirAll and os.WriteFile;
// declared here to keep the test's happy path readable. Implementation in Step 3.
func mkdirAll(path string) error { return osMkdirAll(path) }
func writeAll(path string, body []byte) error { return osWriteFile(path, body) }
```

- [ ] **Step 3: Add `Workspaces` field + `DiscoverWorkspaces`**

Edit `internal/manifest/manifest.go`: add to the existing `Manifest` struct:

```go
// Workspaces is the list of glob patterns declaring workspace directories
// under this repo root. When non-empty, this manifest is a workspace root
// (bun-flavored monorepo semantics — see docs/superpowers/specs/
// 2026-07-10-workspaces-design.md). Empty or absent means single-project.
Workspaces []string `json:"workspaces,omitempty"`
```

Create `internal/manifest/workspaces.go`:

```go
package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Workspace is one member of the workspace set discovered by
// DiscoverWorkspaces.
type Workspace struct {
	// Name is the workspace's composer.json "name", e.g. "acme/shared".
	Name string
	// Dir is the workspace's directory, absolute or as-passed to DiscoverWorkspaces.
	Dir string
	// Manifest is the parsed composer.json.
	Manifest *Manifest
	// Version is a convenience copy of Manifest.Version (may be empty).
	Version string
}

// DiscoverWorkspaces glob-expands root.Workspaces relative to rootDir,
// loads each matched directory's composer.json, and returns the collection.
// Dedup by name is enforced with a hard error. Glob patterns matching zero
// directories emit a warning via warnf (nil is treated as no-op). Empty or
// nil root.Workspaces returns (nil, nil).
func DiscoverWorkspaces(rootDir string, root *Manifest, warnf func(format string, args ...any)) ([]Workspace, error) {
	if root == nil || len(root.Workspaces) == 0 {
		return nil, nil
	}
	if warnf == nil {
		warnf = func(string, ...any) {}
	}
	seen := map[string]string{} // name -> first dir (for duplicate error msg)
	out := []Workspace{}
	for _, pattern := range root.Workspaces {
		abs := filepath.Join(rootDir, pattern)
		matches, err := filepath.Glob(abs)
		if err != nil {
			return nil, fmt.Errorf("workspaces: glob %q: %w", pattern, err)
		}
		if len(matches) == 0 {
			warnf("workspaces: pattern %q matched no directories", pattern)
			continue
		}
		sort.Strings(matches)
		for _, m := range matches {
			info, err := os.Stat(m)
			if err != nil || !info.IsDir() {
				continue // non-directory match; skip silently
			}
			composer := filepath.Join(m, "composer.json")
			if _, err := os.Stat(composer); os.IsNotExist(err) {
				continue // no composer.json here — treat as sibling README dir
			}
			ws, err := Load(m)
			if err != nil {
				return nil, fmt.Errorf("workspaces: load %s: %w", m, err)
			}
			name := ws.Name
			if name == "" {
				return nil, fmt.Errorf("workspaces: %s: composer.json has no name", m)
			}
			if prev, dup := seen[name]; dup {
				return nil, fmt.Errorf("workspaces: duplicate name %q at %s and %s", name, prev, m)
			}
			seen[name] = m
			out = append(out, Workspace{
				Name:     name,
				Dir:      m,
				Manifest: ws,
				Version:  ws.Version,
			})
		}
	}
	return out, nil
}
```

Add the `writeFile` / `mkdirAll` / `writeAll` helpers used in Step 2's test to `internal/manifest/workspaces_test.go` inline (delete the placeholder shims; use `os.MkdirAll` + `os.WriteFile` directly).

- [ ] **Step 4: Verify RED then GREEN**

```bash
go test ./internal/manifest/... -run 'TestManifestParsesWorkspacesField|TestDiscoverWorkspaces' -v
```

Expected sequence: compile errors on missing symbols → PASS after Step 3.

- [ ] **Step 5: Commit**

```bash
git add internal/manifest/manifest.go internal/manifest/workspaces.go internal/manifest/workspaces_test.go internal/manifest/testdata/workspaces-simple/
git commit -m "feat(manifest): parse workspaces field + DiscoverWorkspaces

Adds Manifest.Workspaces []string and the DiscoverWorkspaces helper that
glob-expands the field relative to the repo root, loads each matched
directory's composer.json, and dedups by name. Empty glob patterns
warn to a caller-supplied writer (stderr in production); an empty or
absent Workspaces field returns nil — the single-project code path is
unchanged."
```

---

## Task 2: Constraint parser recognizes `workspace:` prefix

**Files:**
- Modify: `internal/constraint/constraint.go`
- Modify: `internal/constraint/constraint_test.go`

**Interfaces:**
- Consumes: existing `Parse`, `ExplicitDevBranch`, `StabilityFlag`.
- Produces: `Constraint.IsWorkspace bool` — `true` when the input started with `workspace:`. The parsed constraint carries the constraint of the tail (`*` → matches anything; `^1.0` → normal caret expansion).

- [ ] **Step 1: Write the failing tests**

Append to `internal/constraint/constraint_test.go`:

```go
func TestConstraintParseWorkspaceStar(t *testing.T) {
	c, err := Parse("workspace:*")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !c.IsWorkspace {
		t.Errorf("IsWorkspace = false, want true")
	}
	// workspace:* matches every version (like plain "*").
	v, _ := ParseVersion("1.2.3")
	if !c.Satisfies(v) {
		t.Errorf("workspace:* rejected 1.2.3")
	}
	v, _ = ParseVersion("999.0.0")
	if !c.Satisfies(v) {
		t.Errorf("workspace:* rejected 999.0.0")
	}
}

func TestConstraintParseWorkspaceCaret(t *testing.T) {
	c, err := Parse("workspace:^1.0")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !c.IsWorkspace {
		t.Errorf("IsWorkspace = false, want true")
	}
	// ^1.0 semantics: matches 1.x, rejects 2.x.
	v, _ := ParseVersion("1.5.0")
	if !c.Satisfies(v) {
		t.Errorf("workspace:^1.0 rejected 1.5.0")
	}
	v, _ = ParseVersion("2.0.0")
	if c.Satisfies(v) {
		t.Errorf("workspace:^1.0 admitted 2.0.0")
	}
}

func TestConstraintParseWorkspaceExactVersion(t *testing.T) {
	c, err := Parse("workspace:1.2.3")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !c.IsWorkspace {
		t.Errorf("IsWorkspace = false, want true")
	}
	v, _ := ParseVersion("1.2.3")
	if !c.Satisfies(v) {
		t.Errorf("workspace:1.2.3 rejected 1.2.3")
	}
	v, _ = ParseVersion("1.2.4")
	if c.Satisfies(v) {
		t.Errorf("workspace:1.2.3 admitted 1.2.4")
	}
}

// Sanity: normal constraints without the prefix don't get the flag.
func TestConstraintNoWorkspaceFlagOnRegular(t *testing.T) {
	c, err := Parse("^1.0")
	if err != nil {
		t.Fatal(err)
	}
	if c.IsWorkspace {
		t.Errorf("plain ^1.0 has IsWorkspace = true")
	}
}
```

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/constraint/ -run TestConstraintParseWorkspace -v
```

Expected: compile error on `IsWorkspace`.

- [ ] **Step 3: Add prefix handling to `Parse`**

In `internal/constraint/constraint.go`:

```go
type Constraint struct {
	// existing fields ...

	// IsWorkspace is true when the input constraint started with "workspace:".
	// The rest of the struct describes the constraint on the tail — the
	// aggregate builder in orchestrator/ consults IsWorkspace to route the
	// require through the local workspace set instead of the registry.
	IsWorkspace bool
}
```

In `Parse`:

```go
func Parse(s string) (Constraint, error) {
	var workspace bool
	if strings.HasPrefix(s, "workspace:") {
		workspace = true
		s = strings.TrimPrefix(s, "workspace:")
		if s == "" {
			return Constraint{}, fmt.Errorf("constraint: empty workspace:<tail>")
		}
	}
	// ... existing parse of `s` unchanged ...
	c.IsWorkspace = workspace
	return c, nil
}
```

Place the flag-set immediately before the `return c, nil` at the bottom of `Parse` — the tail's own parsing (caret, tilde, wildcards, hyphen ranges, etc.) is untouched.

- [ ] **Step 4: Verify GREEN**

```bash
go test ./internal/constraint/... -v
```

Expected: PASS. Existing tests should be untouched — the new field defaults to `false` on any constraint that doesn't hit the prefix branch.

- [ ] **Step 5: Commit**

```bash
git add internal/constraint/constraint.go internal/constraint/constraint_test.go
git commit -m "feat(constraint): accept workspace: prefix

Parse now recognizes 'workspace:<tail>' and sets Constraint.IsWorkspace.
The tail parses through the normal path (any Composer-style constraint —
star, caret, tilde, wildcards, hyphen ranges), so callers can validate
against a workspace's declared version using the same Satisfies logic
they already use for external packages."
```

---

## Task 3: `workspace_aggregate.go` — super-manifest builder + validation

**Files:**
- Create: `internal/orchestrator/workspace_aggregate.go`
- Create: `internal/orchestrator/workspace_aggregate_test.go`

**Interfaces:**
- Consumes: `manifest.Manifest`, `manifest.Workspace`, `constraint.Parse`.
- Produces:
  - `func BuildAggregateManifest(root *manifest.Manifest, workspaces []manifest.Workspace, includeDev bool) (*manifest.Manifest, error)` — returns the virtual super-manifest fed to the resolver. Cross-workspace requires are validated and stripped from the result.
  - `func WorkspaceEntries(workspaces []manifest.Workspace) []resolver.ResolvedPackage` — synthetic resolved entries to graft onto the resolver's `Result`.

- [ ] **Step 1: Failing tests**

Create `internal/orchestrator/workspace_aggregate_test.go`:

```go
package orchestrator

import (
	"strings"
	"testing"

	"github.com/torstendittmann/gomposer/internal/manifest"
)

func mkWorkspace(name, version string, require map[string]string) manifest.Workspace {
	return manifest.Workspace{
		Name:    name,
		Version: version,
		Dir:     "packages/" + strings.TrimPrefix(name, "acme/"),
		Manifest: &manifest.Manifest{
			Name:    name,
			Version: version,
			Require: require,
		},
	}
}

func TestBuildAggregateManifestUnionsExternalRequires(t *testing.T) {
	root := &manifest.Manifest{
		Name:    "acme/monorepo",
		Require: map[string]string{"psr/log": "^3.0"},
	}
	ws := []manifest.Workspace{
		mkWorkspace("acme/shared", "1.0.0", nil),
		mkWorkspace("acme/api", "", map[string]string{
			"symfony/console": "^6.0",
			"acme/shared":     "workspace:^1.0",
		}),
	}
	agg, err := BuildAggregateManifest(root, ws, true /* includeDev */)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, ok := agg.Require["acme/shared"]; ok {
		t.Errorf("workspace: require should be stripped from aggregate")
	}
	if agg.Require["psr/log"] != "^3.0" {
		t.Errorf("Require[psr/log] = %q", agg.Require["psr/log"])
	}
	if agg.Require["symfony/console"] != "^6.0" {
		t.Errorf("Require[symfony/console] = %q", agg.Require["symfony/console"])
	}
}

func TestBuildAggregateManifestRejectsUnknownWorkspaceTarget(t *testing.T) {
	root := &manifest.Manifest{Name: "acme/monorepo"}
	ws := []manifest.Workspace{
		mkWorkspace("acme/api", "", map[string]string{"acme/nope": "workspace:*"}),
	}
	_, err := BuildAggregateManifest(root, ws, true)
	if err == nil {
		t.Fatal("expected error on unknown workspace target")
	}
	if !strings.Contains(err.Error(), "not found in workspace set") {
		t.Errorf("err = %v", err)
	}
}

func TestBuildAggregateManifestRejectsVersionMismatch(t *testing.T) {
	root := &manifest.Manifest{Name: "acme/monorepo"}
	ws := []manifest.Workspace{
		mkWorkspace("acme/shared", "1.0.0", nil),
		mkWorkspace("acme/api", "", map[string]string{"acme/shared": "workspace:^2.0"}),
	}
	_, err := BuildAggregateManifest(root, ws, true)
	if err == nil {
		t.Fatal("expected version mismatch error")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Errorf("err = %v", err)
	}
}

func TestBuildAggregateManifestRejectsWorkspaceWithoutVersion(t *testing.T) {
	root := &manifest.Manifest{Name: "acme/monorepo"}
	ws := []manifest.Workspace{
		mkWorkspace("acme/shared", "", nil), // no version
		mkWorkspace("acme/api", "", map[string]string{"acme/shared": "workspace:^1.0"}),
	}
	_, err := BuildAggregateManifest(root, ws, true)
	if err == nil {
		t.Fatal("expected error on workspace without version")
	}
	if !strings.Contains(err.Error(), "no version") {
		t.Errorf("err = %v", err)
	}
}

func TestBuildAggregateManifestAllowsWorkspaceStarWithNoVersion(t *testing.T) {
	// workspace:* never checks version, so a version-less workspace is OK.
	root := &manifest.Manifest{Name: "acme/monorepo"}
	ws := []manifest.Workspace{
		mkWorkspace("acme/shared", "", nil),
		mkWorkspace("acme/api", "", map[string]string{"acme/shared": "workspace:*"}),
	}
	if _, err := BuildAggregateManifest(root, ws, true); err != nil {
		t.Errorf("workspace:* with no version should be OK: %v", err)
	}
}

func TestBuildAggregateManifestExcludesDevWhenAsked(t *testing.T) {
	root := &manifest.Manifest{Name: "acme/monorepo"}
	ws := []manifest.Workspace{
		{
			Name: "acme/api", Dir: "apps/api",
			Manifest: &manifest.Manifest{
				Name:       "acme/api",
				Require:    map[string]string{"psr/log": "^3.0"},
				RequireDev: map[string]string{"phpunit/phpunit": "^10.0"},
			},
		},
	}
	agg, err := BuildAggregateManifest(root, ws, false /* includeDev */)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := agg.Require["phpunit/phpunit"]; ok {
		t.Errorf("dev require leaked to aggregate under includeDev=false")
	}
	if agg.Require["psr/log"] != "^3.0" {
		t.Errorf("Require[psr/log] = %q", agg.Require["psr/log"])
	}
}
```

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/orchestrator/ -run TestBuildAggregateManifest -v
```

Expected: compile error on `BuildAggregateManifest`.

- [ ] **Step 3: Implement `workspace_aggregate.go`**

Create `internal/orchestrator/workspace_aggregate.go`:

```go
// Workspaces — aggregate the root manifest and every workspace manifest
// into the virtual super-manifest fed to the resolver. Cross-workspace
// requires (the workspace:* / workspace:<constraint> protocol) are
// validated and stripped: workspaces are already known locally, no need
// to route them through the registry.
package orchestrator

import (
	"fmt"

	"github.com/torstendittmann/gomposer/internal/constraint"
	"github.com/torstendittmann/gomposer/internal/manifest"
)

// BuildAggregateManifest returns the manifest the resolver actually sees.
// It's the union of root's requires and every workspace's requires, minus
// every workspace:-prefixed entry. Duplicate external requires with
// compatible constraints are collapsed to the first-seen; conflicts are
// left to the resolver (its PubGrub derivation names the specific
// packages, which is more useful than an aggregate-time error).
func BuildAggregateManifest(root *manifest.Manifest, workspaces []manifest.Workspace, includeDev bool) (*manifest.Manifest, error) {
	if root == nil {
		return nil, fmt.Errorf("workspaces: nil root manifest")
	}
	byName := map[string]manifest.Workspace{}
	for _, w := range workspaces {
		byName[w.Name] = w
	}

	agg := &manifest.Manifest{
		Name:             root.Name,
		Version:          root.Version,
		Require:          map[string]string{},
		RequireDev:       map[string]string{},
		MinimumStability: root.MinimumStability,
		PreferStable:     root.PreferStable,
		Repositories:     root.Repositories,
	}

	// mergeRequires walks a source require map and adds non-workspace entries
	// to dst. Workspace entries are validated against the target workspace's
	// version.
	mergeRequires := func(dst, src map[string]string, ownerName string) error {
		for name, raw := range src {
			c, err := constraint.Parse(raw)
			if err != nil {
				return fmt.Errorf("workspaces: %s: parse %s: %w", ownerName, name, err)
			}
			if c.IsWorkspace {
				target, ok := byName[name]
				if !ok {
					return fmt.Errorf("workspaces: workspace:%s require %q not found in workspace set", ownerName, name)
				}
				// workspace:* has no tail constraint to check.
				tailIsStar := raw == "workspace:*"
				if !tailIsStar {
					if target.Version == "" {
						return fmt.Errorf("workspaces: %s requires %s (%s) but workspace has no version field", ownerName, name, raw)
					}
					v, err := constraint.ParseVersion(target.Version)
					if err != nil {
						return fmt.Errorf("workspaces: %s: parse target version %q: %w", target.Name, target.Version, err)
					}
					if !c.Satisfies(v) {
						return fmt.Errorf("workspaces: %s requires %s (%s) but workspace has version %q", ownerName, name, raw, target.Version)
					}
				}
				continue // don't leak to aggregate
			}
			if _, dup := dst[name]; !dup {
				dst[name] = raw
			}
		}
		return nil
	}

	if err := mergeRequires(agg.Require, root.Require, root.Name); err != nil {
		return nil, err
	}
	if includeDev {
		if err := mergeRequires(agg.RequireDev, root.RequireDev, root.Name); err != nil {
			return nil, err
		}
	}
	for _, w := range workspaces {
		if err := mergeRequires(agg.Require, w.Manifest.Require, w.Name); err != nil {
			return nil, err
		}
		if includeDev {
			if err := mergeRequires(agg.RequireDev, w.Manifest.RequireDev, w.Name); err != nil {
				return nil, err
			}
		}
	}
	return agg, nil
}
```

- [ ] **Step 4: Verify GREEN**

```bash
go test ./internal/orchestrator/ -run TestBuildAggregateManifest -v
```

Expected: PASS on all six subtests.

- [ ] **Step 5: Commit**

```bash
git add internal/orchestrator/workspace_aggregate.go internal/orchestrator/workspace_aggregate_test.go
git commit -m "feat(orchestrator): workspace aggregate + protocol validation

BuildAggregateManifest unions the root manifest and every workspace
manifest into the virtual super-manifest fed to the resolver.
Workspace: requires are validated against the target workspace's
declared version and stripped from the aggregate (workspaces are
already known locally). Errors surface at the aggregate step so the
resolver never sees a confusing 'package not found' for a local name."
```

---

## Task 4: Pipeline wire-in (discovery + aggregate + synthetic entries + content-hash extension)

**Files:**
- Modify: `internal/orchestrator/pipeline.go`
- Modify: `internal/orchestrator/pipeline_test.go`

**Interfaces:**
- Consumes: Tasks 1, 2, 3.
- Produces:
  - `pipelineState` grows: `workspaces []manifest.Workspace` (nil for single-project runs); `aggregateManifest *manifest.Manifest` (replaces `manifest` as the resolver's input when workspaces are active).
  - `resolver.Result` gets synthetic `ResolvedPackage` entries appended for each workspace, so the autoloader and lockfile see them.
  - `computeCacheKey` gets a new input: concatenated workspace manifest bytes.
  - Metadata prefetch's warm set drops workspace names — they're never fetched.

- [ ] **Step 1: Failing integration test**

Add to `internal/orchestrator/pipeline_test.go` (adapt to the file's existing helpers):

```go
func TestWorkspacesFullPipelineHappyPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "composer.json"), `{
        "name": "acme/monorepo",
        "workspaces": ["packages/*", "apps/*"]
    }`)
	writeFile(t, filepath.Join(dir, "packages", "shared", "composer.json"), `{
        "name": "acme/shared",
        "version": "1.0.0",
        "autoload": { "psr-4": { "Acme\\Shared\\": "src/" } }
    }`)
	writeFile(t, filepath.Join(dir, "packages", "shared", "src", "Thing.php"), "<?php\nnamespace Acme\\Shared; class Thing {}")
	writeFile(t, filepath.Join(dir, "apps", "api", "composer.json"), `{
        "name": "acme/api",
        "require": { "acme/shared": "workspace:^1.0" }
    }`)

	opts := Options{
		ProjectDir: dir,
		Source:     newSilentFakeSource(),
		Fetcher:    &fakeFetcher{},
		Materializer: &fakeMaterializer{},
		Autoloader: &fakeAutoloader{},
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install: %v", err)
	}
	// Assertions about symlink layout land in Task 5. This test's job is:
	// (1) install completes without error;
	// (2) the resulting lockfile records both workspace entries as type=workspace.
	lockBytes, err := os.ReadFile(filepath.Join(dir, "gomposer.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(lockBytes, []byte(`"type": "workspace"`)) {
		t.Errorf(`gomposer.lock has no "type": "workspace" entries:\n%s`, lockBytes)
	}
	if !bytes.Contains(lockBytes, []byte(`"acme/shared"`)) || !bytes.Contains(lockBytes, []byte(`"acme/api"`)) {
		t.Errorf("gomposer.lock missing workspace names:\n%s", lockBytes)
	}
}
```

`newSilentFakeSource`, `writeFile`, `fakeFetcher`, `fakeMaterializer`, `fakeAutoloader` are existing test helpers (or need small adaptation). Grep `pipeline_test.go` and `orchestrator_test.go` for their real names before writing new ones.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/orchestrator/ -run TestWorkspacesFullPipelineHappyPath -v
```

Expected: FAIL — either no workspace discovery happens or lockfile lacks `"type": "workspace"` entries.

- [ ] **Step 3: Wire discovery into `pipelineState`**

In `internal/orchestrator/pipeline.go` (adapt to the current `newPipelineState`):

1. Load `root, manifestBytes, err := manifest.Load(...)` as today.
2. `workspaces, err := manifest.DiscoverWorkspaces(projectDir, root, stderrWarnf)`.
3. If `len(workspaces) > 0`, set `ps.workspaces = workspaces` and `ps.aggregateManifest, err = BuildAggregateManifest(root, workspaces, !opts.NoDev)`.
4. Otherwise, `ps.aggregateManifest = root` (single-project code path unchanged).

Adjust the resolver call to use `ps.aggregateManifest` instead of `ps.manifest`.

- [ ] **Step 4: Extend `manifestContentHash`**

`computeCacheKey` currently hashes `ps.manifestBytes`. Extend:

```go
key := computeCacheKey(ps.manifestBytes, ps.lockBytes, ps.platformStr)
```

Becomes:

```go
allManifests := ps.manifestBytes
if len(ps.workspaces) > 0 {
    // Deterministic: sort by name, then concatenate the raw manifest bytes.
    sorted := append([]manifest.Workspace(nil), ps.workspaces...)
    sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
    for _, w := range sorted {
        b, _ := os.ReadFile(filepath.Join(w.Dir, "composer.json"))
        allManifests = append(allManifests, b...)
    }
}
key := computeCacheKey(allManifests, ps.lockBytes, ps.platformStr)
```

The `os.ReadFile` per workspace here is one syscall per workspace on the resolution-cache-key path. That's acceptable for a Scope-1 first-cut. If we ever see it in a profile, `Workspace` can grow a `RawBytes []byte` field; not now.

- [ ] **Step 5: Graft synthetic workspace entries into the resolver result**

After `resolveOrCache` returns and before building the lock file, if `ps.workspaces` is non-empty:

```go
for _, w := range ps.workspaces {
    result.Packages = append(result.Packages, resolver.ResolvedPackage{
        Name: w.Name,
        Version: constraint.Version{Original: w.Version},
        Record: registry.PackageVersion{
            Name:    w.Name,
            Version: w.Version,
            Type:    "workspace",
            Source:  registry.Source{Type: "path", URL: w.Dir},
            Autoload: workspaceAutoload(w.Manifest.Autoload),
        },
    })
}
```

`workspaceAutoload` converts `manifest.Autoload` to `registry.Autoload` shape (should be a thin re-shape — read the existing types before writing).

The lock adapter (already producing `lock.Package` entries from `resolver.ResolvedPackage`) inherits the `Type: "workspace"` field for free.

- [ ] **Step 6: Skip workspace names in metadata prefetch**

In `internal/orchestrator/metadata_prefetch.go`'s `collectMetadataPrefetchNames`: after unioning names, filter out anything in `ps.workspaces`. Small map lookup.

- [ ] **Step 7: Run tests**

```bash
go test ./internal/orchestrator/ -run TestWorkspacesFullPipelineHappyPath -v
go test ./...
```

Expected: PASS. If existing pipeline tests break because they assume `ps.manifest == ps.aggregateManifest` or similar, adjust — the single-project path should still be untouched (`aggregateManifest = root` when workspaces are empty).

- [ ] **Step 8: Commit**

```bash
git add internal/orchestrator/pipeline.go internal/orchestrator/pipeline_test.go internal/orchestrator/metadata_prefetch.go internal/orchestrator/workspace_aggregate.go
git commit -m "feat(orchestrator): wire workspace discovery into pipeline

- Discovery runs during newPipelineState and populates ps.workspaces.
- BuildAggregateManifest produces the manifest the resolver sees (union
  of every workspace's external requires; workspace: entries stripped).
- Synthetic 'type: workspace' resolver entries grafted onto the result
  so the autoloader and lockfile treat each workspace as a first-class
  package.
- computeCacheKey extended to include workspace manifest bytes so any
  workspace-manifest edit invalidates the resolution cache.
- Metadata prefetch's warm set excludes workspace names (they're local)."
```

---

## Task 5: Symlink pass (post-materialize)

**Files:**
- Create: `internal/orchestrator/workspace_symlink.go`
- Create: `internal/orchestrator/workspace_symlink_test.go`
- Modify: `internal/orchestrator/pipeline.go` (call the pass after materialize)

**Interfaces:**
- Consumes: `manifest.Workspace`, `os.Symlink`, `os.RemoveAll`.
- Produces:
  - `func linkWorkspaces(rootDir string, workspaces []manifest.Workspace) error` — the post-materialize pass.
  - Small helper: `relativeSymlink(target, linkPath string) error` — computes the relative path from `linkPath`'s parent to `target` and atomically writes the symlink (unlink existing, then symlink).

- [ ] **Step 1: Failing tests**

Create `internal/orchestrator/workspace_symlink_test.go`:

```go
package orchestrator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/torstendittmann/gomposer/internal/manifest"
)

func TestLinkWorkspacesCreatesVendorSymlinks(t *testing.T) {
	dir := t.TempDir()
	// Simulate a project with root vendor + one workspace with an existing
	// real vendor (which should get replaced).
	mustMkdirAll(t, filepath.Join(dir, "vendor"))
	mustMkdirAll(t, filepath.Join(dir, "packages", "shared", "vendor"))
	mustMkdirAll(t, filepath.Join(dir, "packages", "shared", "src"))
	mustMkdirAll(t, filepath.Join(dir, "vendor", "acme"))

	ws := []manifest.Workspace{{
		Name:    "acme/shared",
		Dir:     filepath.Join(dir, "packages", "shared"),
		Manifest: &manifest.Manifest{Name: "acme/shared", Version: "1.0.0"},
		Version: "1.0.0",
	}}

	if err := linkWorkspaces(dir, ws); err != nil {
		t.Fatalf("linkWorkspaces: %v", err)
	}

	// packages/shared/vendor should now be a symlink → repo-root vendor.
	wsVendor := filepath.Join(dir, "packages", "shared", "vendor")
	target, err := os.Readlink(wsVendor)
	if err != nil {
		t.Fatalf("packages/shared/vendor is not a symlink: %v", err)
	}
	if target != filepath.Join("..", "..", "vendor") {
		t.Errorf("workspace vendor target = %q, want ../../vendor", target)
	}

	// vendor/acme/shared should be a symlink → workspace source dir.
	crossLink := filepath.Join(dir, "vendor", "acme", "shared")
	target, err = os.Readlink(crossLink)
	if err != nil {
		t.Fatalf("vendor/acme/shared is not a symlink: %v", err)
	}
	if target != filepath.Join("..", "..", "packages", "shared") {
		t.Errorf("cross-workspace link target = %q, want ../../packages/shared", target)
	}
}

func TestLinkWorkspacesIdempotent(t *testing.T) {
	dir := t.TempDir()
	mustMkdirAll(t, filepath.Join(dir, "vendor"))
	mustMkdirAll(t, filepath.Join(dir, "packages", "shared"))
	ws := []manifest.Workspace{{
		Name:     "acme/shared",
		Dir:      filepath.Join(dir, "packages", "shared"),
		Manifest: &manifest.Manifest{Name: "acme/shared"},
	}}
	if err := linkWorkspaces(dir, ws); err != nil {
		t.Fatal(err)
	}
	if err := linkWorkspaces(dir, ws); err != nil {
		t.Errorf("second run failed: %v", err)
	}
}
```

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/orchestrator/ -run TestLinkWorkspaces -v
```

Expected: FAIL / compile error.

- [ ] **Step 3: Implement `workspace_symlink.go`**

```go
// Workspaces post-materialize symlink pass. See
// docs/superpowers/specs/2026-07-10-workspaces-design.md, "Install &
// vendor layout". All symlinks emitted are relative.
package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/torstendittmann/gomposer/internal/manifest"
)

func linkWorkspaces(rootDir string, workspaces []manifest.Workspace) error {
	rootVendor := filepath.Join(rootDir, "vendor")
	for _, w := range workspaces {
		// 1. Workspace's own vendor/ → root vendor/.
		wsVendor := filepath.Join(w.Dir, "vendor")
		relRoot, err := filepath.Rel(w.Dir, rootVendor)
		if err != nil {
			return fmt.Errorf("workspaces: rel %s -> %s: %w", w.Dir, rootVendor, err)
		}
		if err := replaceSymlink(wsVendor, relRoot); err != nil {
			return fmt.Errorf("workspaces: symlink %s: %w", wsVendor, err)
		}

		// 2. vendor/<vendor>/<name> → workspace source dir.
		crossLink := filepath.Join(rootVendor, filepath.FromSlash(w.Name))
		if err := os.MkdirAll(filepath.Dir(crossLink), 0o755); err != nil {
			return fmt.Errorf("workspaces: mkdir %s: %w", filepath.Dir(crossLink), err)
		}
		relTarget, err := filepath.Rel(filepath.Dir(crossLink), w.Dir)
		if err != nil {
			return fmt.Errorf("workspaces: rel %s -> %s: %w", filepath.Dir(crossLink), w.Dir, err)
		}
		if err := replaceSymlink(crossLink, relTarget); err != nil {
			return fmt.Errorf("workspaces: symlink %s: %w", crossLink, err)
		}
	}
	return nil
}

// replaceSymlink writes linkPath → target atomically. If linkPath exists
// (as a file, dir, or existing symlink), it's removed first.
func replaceSymlink(linkPath, target string) error {
	if err := os.RemoveAll(linkPath); err != nil {
		return err
	}
	return os.Symlink(target, linkPath)
}
```

Add `mustMkdirAll(t, path)` helper to the test file (thin `t.Helper()` wrapper around `os.MkdirAll`).

- [ ] **Step 4: Wire into `runFullPipeline`**

In `internal/orchestrator/pipeline.go`, after `materializeAll` completes and before the autoload generation:

```go
if len(ps.workspaces) > 0 {
    if err := linkWorkspaces(opts.ProjectDir, ps.workspaces); err != nil {
        return err
    }
}
```

Autoload generation already runs against the resolved package set (which now includes the synthetic workspace entries with `InstallPath: "vendor/<vendor>/<name>"`), so the emitted `autoload_*.php` files reference the symlink path — which resolves through to the workspace source.

- [ ] **Step 5: Verify GREEN**

```bash
go test ./internal/orchestrator/ -v -run 'TestLinkWorkspaces|TestWorkspacesFullPipelineHappyPath'
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/orchestrator/workspace_symlink.go internal/orchestrator/workspace_symlink_test.go internal/orchestrator/pipeline.go
git commit -m "feat(orchestrator): workspace symlink pass

After materialize, linkWorkspaces lays down:
- packages/<ws>/vendor -> ../../vendor  (workspace bootstraps still find
  the shared install via require __DIR__.'/../vendor/autoload.php')
- vendor/<vendor>/<name> -> ../../<workspace-dir>  (autoload map already
  references vendor/<vendor>/<name>; symlink lets it resolve to source)

Symlinks are relative and idempotent. A workspace's existing real
vendor/ is replaced — the spec calls this out as an acceptable mutation."
```

---

## Task 6: CLI walk-up-to-workspace-root

**Files:**
- Modify: `internal/cli/root.go`
- Modify: `internal/cli/install.go` (or wherever `--project` gets resolved to an absolute path)
- Create: `internal/cli/workspace_walkup_test.go`

**Interfaces:**
- Produces: `func findWorkspaceRoot(cwd string) (string, bool)` — walks up until a `composer.json` with a non-empty `Workspaces` field is found, or a `.git` boundary without such a manifest is hit. Returns `(dir, true)` on success, `("", false)` on no match.

- [ ] **Step 1: Failing test**

Create `internal/cli/workspace_walkup_test.go`:

```go
package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindWorkspaceRootFindsAncestor(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "composer.json"), `{"name":"acme/monorepo","workspaces":["packages/*"]}`)
	writeFile(t, filepath.Join(dir, "packages", "shared", "composer.json"), `{"name":"acme/shared"}`)
	writeFile(t, filepath.Join(dir, "packages", "shared", "src", "Thing.php"), "<?php")

	got, ok := findWorkspaceRoot(filepath.Join(dir, "packages", "shared", "src"))
	if !ok {
		t.Fatalf("no workspace root found from packages/shared/src")
	}
	if abs, _ := filepath.EvalSymlinks(got); abs != resolveOrPanic(t, dir) {
		t.Errorf("got %q, want %q", got, dir)
	}
}

func TestFindWorkspaceRootStopsAtGitBoundary(t *testing.T) {
	dir := t.TempDir()
	// Inner dir has a .git; outer has a workspaces-declaring composer.json.
	// Walk from inner should NOT find the outer root because .git is a
	// project boundary.
	writeFile(t, filepath.Join(dir, "composer.json"), `{"name":"acme/monorepo","workspaces":["packages/*"]}`)
	mustMkdirAll(t, filepath.Join(dir, "unrelated", ".git"))
	writeFile(t, filepath.Join(dir, "unrelated", "composer.json"), `{"name":"other/thing"}`)

	got, ok := findWorkspaceRoot(filepath.Join(dir, "unrelated"))
	if ok {
		t.Errorf("expected no match, got %q", got)
	}
}

func TestFindWorkspaceRootReturnsFalseWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "composer.json"), `{"name":"acme/plain"}`)
	if _, ok := findWorkspaceRoot(dir); ok {
		t.Errorf("plain project matched as workspace root")
	}
}

// resolveOrPanic exists because macOS t.TempDir() may live under /private/var
// while EvalSymlinks resolves to /var.
func resolveOrPanic(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/cli/ -run TestFindWorkspaceRoot -v
```

Expected: compile error on `findWorkspaceRoot`.

- [ ] **Step 3: Implement**

Add to `internal/cli/root.go`:

```go
// findWorkspaceRoot walks up from cwd looking for a composer.json whose
// parsed Workspaces field is non-empty. Stops at the filesystem root or
// on entering an ancestor whose own composer.json declares no
// workspaces AND contains a .git directory (project boundary). Returns
// (dir, true) on success, ("", false) otherwise.
func findWorkspaceRoot(cwd string) (string, bool) {
	cur := cwd
	for {
		manifestPath := filepath.Join(cur, "composer.json")
		if body, err := os.ReadFile(manifestPath); err == nil {
			var m manifest.Manifest
			if json.Unmarshal(body, &m) == nil && len(m.Workspaces) > 0 {
				return cur, true
			}
			// Own composer.json without workspaces + .git → project
			// boundary; don't cross it.
			if _, err := os.Stat(filepath.Join(cur, ".git")); err == nil {
				return "", false
			}
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false // filesystem root
		}
		cur = parent
	}
}
```

- [ ] **Step 4: Consume in `install` and `update`**

Where the resolved project directory is computed today (the `--project` handler), wire in:

```go
projectDir := flagProject
if projectDir == "" {
    projectDir, _ = os.Getwd()
}
if root, ok := findWorkspaceRoot(projectDir); ok {
    projectDir = root
}
```

- [ ] **Step 5: Verify GREEN**

```bash
go test ./internal/cli/... -v
```

Expected: PASS. Full suite too.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/root.go internal/cli/install.go internal/cli/update.go internal/cli/workspace_walkup_test.go
git commit -m "feat(cli): walk up to workspace root on install/update

gomposer install and update walk up from CWD until they find a
composer.json declaring 'workspaces'. Stop at the filesystem root or
on hitting a .git boundary whose own composer.json doesn't declare
workspaces (plain single-project repos are not sucked into an
unrelated ancestor's workspace)."
```

---

## Task 7: E2E fixture + README section

**Files:**
- Create: `internal/orchestrator/testdata/workspaces-simple/` (root + `packages/shared/` + `apps/api/`)
- Modify: `README.md` — new "Workspaces" section pointing at the spec.
- Modify: `internal/orchestrator/pipeline_test.go` — replace the inline `writeFile` construction in Task 4's integration test with a helper that loads from `testdata/workspaces-simple/`.

**Interfaces:** none new; documentation + fixture hardening.

- [ ] **Step 1: Move the fixture from Task 4's inline `writeFile` into `testdata/workspaces-simple/`**

Copy the three composer.json files + the `Thing.php` and `App.php` stubs into a real testdata tree so downstream tests can load them. Update the integration test to `copyDir("testdata/workspaces-simple", t.TempDir())` (existing helper — grep for `copyDir` in the orchestrator tests to reuse the real name; if none, use a small `filepath.WalkDir` inline).

- [ ] **Step 2: README section**

Insert after the "Usage" table in `README.md`:

```markdown
## Workspaces (monorepo)

gomposer supports pnpm/bun-style workspaces via a top-level `"workspaces"` array in the root `composer.json`:

\`\`\`json
{
    "name": "acme/monorepo",
    "workspaces": ["packages/*", "apps/*"]
}
\`\`\`

Every matched directory containing a `composer.json` becomes a workspace. Cross-workspace deps use the `workspace:*` protocol:

\`\`\`json
{
    "name": "acme/api",
    "require": { "acme/shared": "workspace:^1.0" }
}
\`\`\`

`gomposer install` at the repo root (or from any workspace subdirectory — it walks up) installs everything into a shared `vendor/` at the repo root. Each workspace gets a `vendor/` symlink to it, and cross-workspace packages become symlinks to their source dirs. See `docs/superpowers/specs/2026-07-10-workspaces-design.md` for details.

Not in scope yet (Scope 2 follow-up): `--filter=<pkg>` for subset installs; `gomposer run <script>` for topologically-ordered script execution across workspaces.
```

- [ ] **Step 3: Full-suite check**

```bash
go test ./...
```

Expected: green.

- [ ] **Step 4: Manual e2e**

```sh
go build -o /tmp/gomposer-ws ./cmd/gomposer
cp -R internal/orchestrator/testdata/workspaces-simple /tmp/ws-check
cd /tmp/ws-check
/tmp/gomposer-ws install
ls -la vendor/acme/shared          # should be a symlink → ../../packages/shared
ls -la packages/shared/vendor      # should be a symlink → ../../vendor
ls -la apps/api/vendor             # same
head vendor/composer/autoload_psr4.php  # should show Acme\Shared\ mapping
```

Expected: all symlinks present, autoload map contains both workspaces.

- [ ] **Step 5: Commit**

```bash
git add internal/orchestrator/testdata/workspaces-simple/ README.md internal/orchestrator/pipeline_test.go
git commit -m "docs(readme) + test: workspaces fixture and manual notes

Fixture project used by the integration test now lives under
internal/orchestrator/testdata/workspaces-simple/ so future tests can
load it. README gains a Workspaces section documenting the workspaces
field, the workspace: protocol, and the walk-up install semantics."
```

---

## Workspaces (Scope 1): acceptance check

After all tasks:

- `go test ./...` is green.
- `gomposer install` in the `workspaces-simple` fixture (or a real monorepo) produces:
  - Real `vendor/` at repo root with `autoload.php` and the aggregated autoload map.
  - `packages/*/vendor` and `apps/*/vendor` as symlinks to the root vendor.
  - `vendor/<vendor>/<name>` for every workspace as a symlink to the workspace source dir.
- `gomposer.lock` contains one entry per external package plus one `"type": "workspace"` entry per workspace.
- `gomposer install` from any workspace subdirectory finds the root and installs the whole thing.
- Backward compat: a project without a `"workspaces"` field installs identically to today.
- Error cases produce the messages listed under Global Constraints (duplicate name, unknown workspace target, version mismatch, no version field on constrained target).

If any of these fails, fix forward before declaring the plan done.
