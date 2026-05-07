# Stage 2 / Plan 6: Plugin Detection and Warning

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Detect Composer plugin packages (`composer-plugin`, `composer-installer`) in the resolved set and emit one clear, actionable warning per plugin to stderr. Plugins are still installed into `vendor/` — they just do not run. This matches the spec's "detected and ignored with a warning" policy. Add a manifest-level suppression escape hatch (`extra.composer-go.suppress-plugin-warnings`) and accept (but no-op) the `--allow-plugins` CLI flag so users can paste Composer commands without rejection.

**Why this is necessary.** The spec is explicit: composer-go does not implement Composer's plugin system, but it cannot silently install plugin packages either — many real-world projects rely on plugins for behavior that will not happen here. The most common case is `composer/installers`, which rewrites install paths for WordPress / Drupal / TYPO3 / etc. layouts. With composer-go, those packages land in `vendor/<vendor>/<name>/` regardless. A loud warning at the right moment is the difference between "user sees a confusing failure later" and "user understands and decides".

**Architecture:**

- New package `internal/plugins` owns the detection rules and the rendered warning text. Pure logic, easy to test, no I/O.
- The orchestrator pipeline calls `plugins.Inspect(lockFile, manifest)` after `resolveOrCache` returns and before `fetchAll`. The resulting warnings are written to `os.Stderr` (or an injected `io.Writer` for tests).
- Plugin packages remain in `lockFile.Packages` / `lockFile.PackagesDev`. They flow through fetch, materialize, autoload exactly like any other library package.
- The `--allow-plugins` flag is parsed by Cobra so commands like `composer-go install --allow-plugins='*'` do not error, but the flag is otherwise unused. Stage 5+ may revisit if real plugin execution ever ships.

**Tech Stack:** Go 1.22+, standard library only. No new dependencies.

**Depends on:**
- Stage 1 Plan 1 — `internal/manifest` exposes `Manifest.Extra map[string]any`.
- Stage 1 Plan 2 — `internal/registry.PackageVersion.Type` is populated from Packagist.
- Stage 1 Plan 6 — `internal/orchestrator/pipeline.go` is the natural injection point; `lock.Package` carries `Type` through resolve.

If `lock.Package` does not yet carry a `Type` field, this plan adds it (Task 1).

---

## File structure

| Path | Responsibility |
|------|---------------|
| `internal/lock/types.go` | Add `Type string` to `lock.Package` if absent; ensure resolver populates it |
| `internal/plugins/plugins.go` | `Inspect(...) []Warning`, `Render(io.Writer, []Warning)`, plugin-type table |
| `internal/plugins/plugins_test.go` | Detection table tests + suppression + composer/installers special case |
| `internal/orchestrator/pipeline.go` | Call `plugins.Inspect` + `plugins.Render` between resolve and fetch |
| `internal/orchestrator/orchestrator.go` | Add `WarnWriter io.Writer` to `Options` (defaults to `os.Stderr`) |
| `internal/cli/install.go`, `internal/cli/update.go` | Accept `--allow-plugins` (no-op) |

---

## Task 1: Carry package Type through the lockfile

**Files:**
- Modify: `internal/lock/types.go` (or wherever `Package` is defined)
- Modify: `internal/resolver` — wherever `ToLockPackages` builds `lock.Package` from the resolver result, copy `Type` across.
- Modify: `internal/lock/lock_test.go` (or equivalent) to pin the new field.

If `lock.Package.Type` already exists and is populated by `ToLockPackages`, mark this task done and proceed to Task 2.

- [ ] **Step 1: Inspect existing types**

Run: `grep -n "Type" internal/lock/*.go internal/resolver/*.go`

If `lock.Package` already has a `Type string` field with a JSON tag and the resolver populates it from `registry.PackageVersion.Type`, skip to Task 2 — record this in the commit message of Task 2.

- [ ] **Step 2: Write failing test**

Append to `internal/lock/lock_test.go`:

```go
func TestPackageTypeRoundTrips(t *testing.T) {
	in := &File{
		SchemaVersion: SchemaVersion,
		Generator:     Generator{Name: "composer-go", Version: "0.1.0"},
		Packages: []Package{
			{Name: "composer/installers", Version: "2.3.0", Type: "composer-installer"},
			{Name: "psr/log", Version: "3.0.0", Type: "library"},
		},
	}
	data, err := in.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if out.Packages[0].Type != "composer-installer" || out.Packages[1].Type != "library" {
		t.Errorf("Type round-trip failed: %+v", out.Packages)
	}
}
```

- [ ] **Step 3: Verify failure**

Run: `go test ./internal/lock/...`

Expected: build error on `Package.Type` (or test fail if the field exists but is unexported / has wrong JSON tag).

- [ ] **Step 4: Add the field**

In the file declaring `lock.Package`, add the field after `Version`:

```go
type Package struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// Type is the composer "type" value ("library", "composer-plugin",
	// "composer-installer", etc.). The orchestrator uses it to detect plugins
	// and emit a warning; it is otherwise informational.
	Type string `json:"type,omitempty"`
	// ... existing fields ...
}
```

- [ ] **Step 5: Populate Type in the resolver-to-lock conversion**

Locate `resolver.ToLockPackages` (or wherever `registry.PackageVersion` is converted to `lock.Package`). Add the assignment:

```go
out := lock.Package{
	Name:    v.Name,
	Version: v.Version,
	Type:    v.Type, // forwarded for plugin detection in the orchestrator
	// ... existing assignments ...
}
```

- [ ] **Step 6: Run tests**

Run: `go test ./internal/lock/... ./internal/resolver/...`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/lock internal/resolver
git commit -m "feat(lock): carry package type through resolver -> lockfile"
```

---

## Task 2: `internal/plugins` — detection + warning rendering

**Files:**
- Create: `internal/plugins/plugins.go`
- Create: `internal/plugins/plugins_test.go`

Pure-logic package. Given a resolved lockfile and the project manifest, return a list of `Warning` values. Each warning carries package name, version, plugin type, and a one-line "what this means" string. The package also exposes a `Render` helper that writes a stable, grep-friendly format to an `io.Writer`.

The recognized plugin types are:

| Type | Notes |
|------|-------|
| `composer-plugin` | Generic Composer plugin. Hooks into install/update events. |
| `composer-installer` | Custom installer (e.g., `composer/installers`). Rewrites install paths. |

Both are warned about; the `composer-installer` message is special-cased to call out custom install paths explicitly. `composer/installers` itself receives an extra-explicit message because it is the most common case and the failure mode (packages landing in `vendor/` instead of `wp-content/plugins/...`) is very visible to end users.

- [ ] **Step 1: Write failing tests**

Create `internal/plugins/plugins_test.go`:

```go
package plugins

import (
	"bytes"
	"strings"
	"testing"

	"github.com/torstendittmann/composer-go/internal/lock"
	"github.com/torstendittmann/composer-go/internal/manifest"
)

func TestInspectDetectsComposerPlugin(t *testing.T) {
	f := &lock.File{Packages: []lock.Package{
		{Name: "phpstan/extension-installer", Version: "1.4.0", Type: "composer-plugin"},
		{Name: "psr/log", Version: "3.0.0", Type: "library"},
	}}
	got := Inspect(f, &manifest.Manifest{})
	if len(got) != 1 {
		t.Fatalf("len(warnings) = %d, want 1: %+v", len(got), got)
	}
	if got[0].Name != "phpstan/extension-installer" {
		t.Errorf("Name = %q", got[0].Name)
	}
	if got[0].Type != "composer-plugin" {
		t.Errorf("Type = %q", got[0].Type)
	}
	if !strings.Contains(got[0].Message, "install/update events") {
		t.Errorf("Message did not explain plugin behavior: %q", got[0].Message)
	}
}

func TestInspectDetectsComposerInstaller(t *testing.T) {
	f := &lock.File{Packages: []lock.Package{
		{Name: "composer/installers", Version: "2.3.0", Type: "composer-installer"},
	}}
	got := Inspect(f, &manifest.Manifest{})
	if len(got) != 1 {
		t.Fatalf("len(warnings) = %d, want 1", len(got))
	}
	if !strings.Contains(got[0].Message, "custom install paths") {
		t.Errorf("composer-installer message must mention custom install paths: %q", got[0].Message)
	}
	if !strings.Contains(got[0].Message, "vendor/") {
		t.Errorf("composer-installer message must mention vendor/: %q", got[0].Message)
	}
}

func TestInspectInspectsDevPackages(t *testing.T) {
	f := &lock.File{PackagesDev: []lock.Package{
		{Name: "phpstan/extension-installer", Version: "1.4.0", Type: "composer-plugin"},
	}}
	got := Inspect(f, &manifest.Manifest{})
	if len(got) != 1 {
		t.Fatalf("len(warnings) = %d, want 1", len(got))
	}
}

func TestInspectIgnoresLibraries(t *testing.T) {
	f := &lock.File{Packages: []lock.Package{
		{Name: "psr/log", Version: "3.0.0", Type: "library"},
		{Name: "monolog/monolog", Version: "3.5.0", Type: ""},
	}}
	if got := Inspect(f, &manifest.Manifest{}); len(got) != 0 {
		t.Errorf("expected no warnings, got %+v", got)
	}
}

func TestInspectSuppressedByManifestExtra(t *testing.T) {
	f := &lock.File{Packages: []lock.Package{
		{Name: "phpstan/extension-installer", Version: "1.4.0", Type: "composer-plugin"},
	}}
	m := &manifest.Manifest{Extra: map[string]any{
		"composer-go": map[string]any{
			"suppress-plugin-warnings": true,
		},
	}}
	if got := Inspect(f, m); len(got) != 0 {
		t.Errorf("suppression flag should silence warnings, got %+v", got)
	}
}

func TestInspectSuppressionIgnoresOtherTruthyValues(t *testing.T) {
	// Only the literal boolean true suppresses. "true" the string does not —
	// composer.json has a real bool type and we don't want fuzzy matching.
	f := &lock.File{Packages: []lock.Package{
		{Name: "phpstan/extension-installer", Type: "composer-plugin"},
	}}
	m := &manifest.Manifest{Extra: map[string]any{
		"composer-go": map[string]any{"suppress-plugin-warnings": "true"},
	}}
	if got := Inspect(f, m); len(got) != 1 {
		t.Errorf("string \"true\" should NOT suppress; got %d warnings", len(got))
	}
}

func TestInspectSpecialCasesComposerInstallers(t *testing.T) {
	f := &lock.File{Packages: []lock.Package{
		{Name: "composer/installers", Version: "2.3.0", Type: "composer-installer"},
	}}
	got := Inspect(f, &manifest.Manifest{})
	if len(got) != 1 {
		t.Fatalf("len = %d", len(got))
	}
	// composer/installers is the canonical case. The message should be
	// concrete about WordPress/Drupal-style layouts breaking.
	msg := got[0].Message
	if !strings.Contains(msg, "composer/installers") {
		t.Errorf("special-case message missing package name: %q", msg)
	}
}

func TestRenderProducesOneLinePerWarning(t *testing.T) {
	ws := []Warning{
		{Name: "a/x", Version: "1.0.0", Type: "composer-plugin", Message: "msg-a"},
		{Name: "b/y", Version: "2.0.0", Type: "composer-installer", Message: "msg-b"},
	}
	var buf bytes.Buffer
	Render(&buf, ws)
	out := buf.String()
	if !strings.Contains(out, "a/x@1.0.0") || !strings.Contains(out, "b/y@2.0.0") {
		t.Errorf("Render output missing entries: %q", out)
	}
	if !strings.Contains(out, "msg-a") || !strings.Contains(out, "msg-b") {
		t.Errorf("Render output missing messages: %q", out)
	}
	if !strings.Contains(out, "warning") {
		t.Errorf("Render output should be visibly tagged as a warning: %q", out)
	}
}

func TestRenderEmptyIsNoOp(t *testing.T) {
	var buf bytes.Buffer
	Render(&buf, nil)
	if buf.Len() != 0 {
		t.Errorf("Render(nil) wrote %q, want empty", buf.String())
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/plugins/...`

Expected: build errors on `Inspect`, `Render`, `Warning`.

- [ ] **Step 3: Implement the package**

Create `internal/plugins/plugins.go`:

```go
// Package plugins detects Composer plugin packages in a resolved lockfile and
// produces human-readable warnings.
//
// composer-go intentionally does not run plugins (see
// docs/superpowers/specs/2026-05-07-composer-go-design.md, section
// "Non-goals" and "Plugin detection"). Plugin packages still install into
// vendor/ — they just never execute. This package draws the user's attention
// to that asymmetry so install-time surprises do not become run-time
// mysteries.
//
// Recognized plugin types:
//
//   - "composer-plugin"     — generic Composer plugin, hooks into events
//   - "composer-installer"  — custom installer (e.g., composer/installers)
//
// Both are warned about. The composer-installer warning specifically calls
// out that custom install paths will not take effect; packages will land in
// vendor/<vendor>/<name>/ regardless.
//
// Suppression: a project that knowingly accepts the limitation can set
//
//	"extra": { "composer-go": { "suppress-plugin-warnings": true } }
//
// in composer.json. Only the literal boolean true suppresses; any other
// value (including the string "true") is treated as not-set.
package plugins

import (
	"fmt"
	"io"

	"github.com/torstendittmann/composer-go/internal/lock"
	"github.com/torstendittmann/composer-go/internal/manifest"
)

// Warning describes a single plugin package the user should know about.
type Warning struct {
	Name    string
	Version string
	Type    string
	Message string
}

// pluginTypes is the set of package "type" values we treat as plugins.
// Keeping it as a small map (rather than an if-chain) makes future additions
// trivial and the test surface obvious.
var pluginTypes = map[string]bool{
	"composer-plugin":    true,
	"composer-installer": true,
}

// Inspect walks the resolved package set and returns one Warning per plugin
// package. Returns nil if the user has set extra.composer-go.suppress-plugin-warnings=true,
// or if no plugins are present.
//
// Both prod and dev packages are inspected: a dev-only plugin (e.g.,
// phpstan/extension-installer) misbehaves the same way as a prod plugin.
func Inspect(f *lock.File, m *manifest.Manifest) []Warning {
	if f == nil {
		return nil
	}
	if isSuppressed(m) {
		return nil
	}
	var out []Warning
	for _, p := range f.Packages {
		if w, ok := warningFor(p); ok {
			out = append(out, w)
		}
	}
	for _, p := range f.PackagesDev {
		if w, ok := warningFor(p); ok {
			out = append(out, w)
		}
	}
	return out
}

func warningFor(p lock.Package) (Warning, bool) {
	if !pluginTypes[p.Type] {
		return Warning{}, false
	}
	return Warning{
		Name:    p.Name,
		Version: p.Version,
		Type:    p.Type,
		Message: messageFor(p),
	}, true
}

func messageFor(p lock.Package) string {
	// Special-case the canonical composer/installers package: it is by far
	// the most common installer plugin and the failure mode (custom paths
	// silently ignored) hits WordPress, Drupal, TYPO3, and Magento projects
	// constantly. Be concrete about what won't work.
	if p.Name == "composer/installers" {
		return "composer/installers normally rewrites install paths " +
			"(e.g., wp-content/plugins/<name>, modules/contrib/<name>); " +
			"composer-go does not run installer plugins, so packages " +
			"that depend on it will land in vendor/<vendor>/<name>/ regardless."
	}
	switch p.Type {
	case "composer-installer":
		return "this is a custom installer plugin; composer-go does not run it, " +
			"so any custom install paths it would normally configure will not " +
			"take effect — packages will land in vendor/<vendor>/<name>/."
	default: // "composer-plugin"
		return "this plugin would normally hook into install/update events; " +
			"composer-go does not run plugins, so any package-installer " +
			"behavior you depend on (custom install paths, patching, " +
			"autoload tweaks, etc.) will not happen."
	}
}

// isSuppressed returns true iff the manifest sets
// extra.composer-go.suppress-plugin-warnings to the literal boolean true.
//
// We deliberately do NOT accept the string "true" or other truthy values:
// composer.json is JSON, the boolean type is unambiguous, and fuzzy matching
// invites accidental suppression.
func isSuppressed(m *manifest.Manifest) bool {
	if m == nil || m.Extra == nil {
		return false
	}
	cg, ok := m.Extra["composer-go"].(map[string]any)
	if !ok {
		return false
	}
	v, ok := cg["suppress-plugin-warnings"].(bool)
	return ok && v
}

// Render writes warnings to w in a stable, grep-friendly one-line-per-warning
// format. No-op when warnings is empty so callers can call Render
// unconditionally.
func Render(w io.Writer, warnings []Warning) {
	for _, x := range warnings {
		fmt.Fprintf(w, "warning: composer-go does not run plugins: %s@%s (type=%s) — %s\n",
			x.Name, x.Version, x.Type, x.Message)
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/plugins/...`

Expected: PASS for all eight tests.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins
git commit -m "feat(plugins): detect composer-plugin/installer types and render warnings"
```

---

## Task 3: Hook into the orchestrator pipeline

**Files:**
- Modify: `internal/orchestrator/orchestrator.go` — add `WarnWriter io.Writer` to `Options`.
- Modify: `internal/orchestrator/pipeline.go` — call `plugins.Inspect` + `plugins.Render` between resolve and fetch.
- Modify: `internal/orchestrator/orchestrator_test.go` — assert warnings reach the writer.

The warning fires once per orchestrator run, after `resolveOrCache` returns. Plugin packages are NOT removed from the lockfile — they still flow through fetch + materialize like any other package, so `vendor/` ends up with the plugin code on disk (just unrun). This matches the spec's "detect and ignore with warning" wording and keeps composer.json compatibility intact.

- [ ] **Step 1: Append failing test**

Append to `internal/orchestrator/orchestrator_test.go`:

```go
func TestInstallEmitsPluginWarning(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"),
		[]byte(`{"name":"vendor/pkg","require":{"acme/plugin":"1.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{pkgs: map[string]*registry.PackageMetadata{
		"acme/plugin": {Name: "acme/plugin", Versions: []registry.PackageVersion{{
			Name: "acme/plugin", Version: "1.0.0", VersionNorm: "1.0.0.0",
			Type: "composer-plugin",
			Dist: registry.Dist{Type: "zip", URL: "u", Sha: "s"},
		}}},
	}}
	var stderr bytes.Buffer
	opts := Options{
		ProjectDir:   dir,
		Source:       src,
		Fetcher:      &fakeFetcher{},
		Materializer: &fakeMaterializer{},
		Autoloader:   &fakeAutoloader{},
		WarnWriter:   &stderr,
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !strings.Contains(stderr.String(), "acme/plugin") ||
		!strings.Contains(stderr.String(), "composer-plugin") {
		t.Errorf("expected plugin warning, got: %q", stderr.String())
	}
	// Plugin must STILL flow through fetch + materialize.
	if mz, ok := opts.Materializer.(*fakeMaterializer); ok {
		want := filepath.Join(dir, "vendor", "acme", "plugin")
		if _, ok := mz.wrote[want]; !ok {
			t.Errorf("plugin package not materialized; wrote=%+v", mz.wrote)
		}
	}
}

func TestInstallSuppressedByManifestExtra(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{
"name":"vendor/pkg",
"require":{"acme/plugin":"1.0.0"},
"extra":{"composer-go":{"suppress-plugin-warnings":true}}
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{pkgs: map[string]*registry.PackageMetadata{
		"acme/plugin": {Name: "acme/plugin", Versions: []registry.PackageVersion{{
			Name: "acme/plugin", Version: "1.0.0", VersionNorm: "1.0.0.0",
			Type: "composer-plugin",
			Dist: registry.Dist{Type: "zip", URL: "u", Sha: "s"},
		}}},
	}}
	var stderr bytes.Buffer
	opts := Options{
		ProjectDir: dir, Source: src,
		Fetcher: &fakeFetcher{}, Materializer: &fakeMaterializer{}, Autoloader: &fakeAutoloader{},
		WarnWriter: &stderr,
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if stderr.Len() != 0 {
		t.Errorf("expected no warning under suppression, got: %q", stderr.String())
	}
}
```

Add `"bytes"` and `"strings"` to the imports if missing.

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/orchestrator/...`

Expected: build error on `Options.WarnWriter`, or test fails because no warning is emitted.

- [ ] **Step 3: Add `WarnWriter` to `Options`**

In `internal/orchestrator/orchestrator.go`, extend the struct (preserving alignment with the existing fields):

```go
type Options struct {
	// ... existing fields ...

	// WarnWriter receives stage-2 plugin warnings. Defaults to os.Stderr
	// when nil. Tests inject a buffer to assert on the rendered text.
	WarnWriter io.Writer
}
```

Add `"io"` to the imports.

- [ ] **Step 4: Wire `plugins.Inspect` into the pipeline**

In `internal/orchestrator/pipeline.go`, add the import:

```go
import "github.com/torstendittmann/composer-go/internal/plugins"
```

Inside `runFullPipeline`, immediately after the `resolveOrCache` call returns successfully and before `fetchAll`, insert:

```go
	// Stage-2 plugin policy: detect composer-plugin / composer-installer
	// packages and emit one warning per plugin to stderr. The packages
	// themselves still flow through fetch + materialize — they are installed
	// into vendor/ but never executed. See
	// docs/superpowers/plans/2026-05-08-stage2-plan6-plugin-warning.md.
	if warnings := plugins.Inspect(lockFile, m); len(warnings) > 0 {
		w := opts.WarnWriter
		if w == nil {
			w = os.Stderr
		}
		plugins.Render(w, warnings)
	}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/orchestrator/... ./internal/plugins/...`

Expected: PASS, including the two new tests.

- [ ] **Step 6: Commit**

```bash
git add internal/orchestrator
git commit -m "feat(orchestrator): emit plugin warnings between resolve and fetch"
```

---

## Task 4: CLI accepts `--allow-plugins` (no-op)

**Files:**
- Modify: `internal/cli/install.go`
- Modify: `internal/cli/update.go`
- Modify: `internal/cli/install_test.go` or a new test file

`--allow-plugins` is a Composer 2 flag that gates plugin execution. composer-go does not run plugins at all, so the flag is meaningless here — but rejecting it would break copy-pasted Composer commands and CI scripts. We accept it (with any value, including the wildcard `*` and comma-separated lists) and ignore it. The help text explicitly notes this so users are not misled.

- [ ] **Step 1: Append failing CLI test**

Create or append to `internal/cli/plugins_flag_test.go`:

```go
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestAllowPluginsFlagAcceptedAsNoOp verifies the flag parses without error.
// We give the command a manifest with no requires so the orchestrator
// short-circuits — we are testing the flag plumbing, not the install path.
func TestAllowPluginsFlagAcceptedAsNoOp(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"),
		[]byte(`{"name":"vendor/pkg"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"install", "--project", dir, "--allow-plugins"},
		{"install", "--project", dir, "--allow-plugins=*"},
		{"install", "--project", dir, "--allow-plugins=composer/installers,phpstan/extension-installer"},
		{"update", "--project", dir, "--allow-plugins=*"},
	} {
		var out bytes.Buffer
		root := newRootCmd()
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Errorf("args=%v: unexpected error: %v\noutput: %s", args, err, out.String())
		}
	}
}

func TestAllowPluginsHelpMentionsNoOp(t *testing.T) {
	var out bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"install", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("help: %v", err)
	}
	body := out.String()
	if !contains(body, "--allow-plugins") {
		t.Errorf("help missing --allow-plugins: %s", body)
	}
	if !contains(body, "no-op") && !contains(body, "ignored") {
		t.Errorf("help text must clarify --allow-plugins is a no-op: %s", body)
	}
}

func contains(haystack, needle string) bool {
	return bytes.Contains([]byte(haystack), []byte(needle))
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/cli/...`

Expected: errors on the unknown `--allow-plugins` flag.

- [ ] **Step 3: Add the flag to install.go**

In `internal/cli/install.go`, inside `newInstallCmd()`, add a string-slice variable and flag declaration. The flag is parsed but never read — its presence alone keeps Cobra from rejecting the input.

```go
	var (
		projectDir   string
		allowPlugins []string // accepted for Composer-CLI compatibility; no-op (composer-go does not run plugins)
	)
	cmd := &cobra.Command{
		// ... existing fields ...
	}
	cmd.Flags().StringVar(&projectDir, "project", "", "project directory containing composer.json (defaults to cwd)")
	cmd.Flags().StringSliceVar(&allowPlugins, "allow-plugins", nil,
		"accepted for Composer compatibility; no-op (composer-go does not run plugins, so this flag has no effect)")
	// Allow bare `--allow-plugins` with no value (Composer accepts that form).
	cmd.Flags().Lookup("allow-plugins").NoOptDefVal = "*"
	_ = allowPlugins // explicitly unused
```

- [ ] **Step 4: Mirror in update.go**

Apply the same change to `internal/cli/update.go`.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/cli/...`

Expected: PASS.

- [ ] **Step 6: Manual smoke**

```bash
go build -o composer-go ./cmd/composer-go
./composer-go install --help | grep -A1 allow-plugins
./composer-go install --allow-plugins='*' --project $(mktemp -d 2>/dev/null) || true
```

Expected: help mentions "no-op"; the `--allow-plugins='*'` invocation does not produce an "unknown flag" error (it may error later for the missing manifest, which is fine).

- [ ] **Step 7: Commit**

```bash
git add internal/cli
git commit -m "feat(cli): accept --allow-plugins as a no-op for Composer compatibility"
```

---

## Task 5: Live verification + documentation note

**Files:**
- Create: `internal/orchestrator/plugin_live_test.go` (gated, optional)
- No documentation file changes — the warning text itself is the user-facing doc.

A small live test that installs a real plugin package (`phpstan/extension-installer`) and confirms a warning hits stderr. Gated on `COMPOSER_GO_LIVE_NETWORK=1` like the other live tests.

- [ ] **Step 1: Write the live test**

Create `internal/orchestrator/plugin_live_test.go`:

```go
package orchestrator

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLiveInstallEmitsPluginWarning installs phpstan/extension-installer (a
// real composer-plugin) and asserts that a warning lands on the WarnWriter.
// Gated on COMPOSER_GO_LIVE_NETWORK=1.
func TestLiveInstallEmitsPluginWarning(t *testing.T) {
	if os.Getenv("COMPOSER_GO_LIVE_NETWORK") != "1" {
		t.Skip("set COMPOSER_GO_LIVE_NETWORK=1 to run against real Packagist")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{
  "name": "composer-go-test/plugin-warn",
  "require": { "phpstan/extension-installer": "^1.4" }
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if err := Install(context.Background(), Options{ProjectDir: dir, WarnWriter: &stderr}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !strings.Contains(stderr.String(), "phpstan/extension-installer") ||
		!strings.Contains(stderr.String(), "composer-plugin") {
		t.Errorf("expected plugin warning, got: %q", stderr.String())
	}
	// Plugin must be installed despite the warning.
	if _, err := os.Stat(filepath.Join(dir, "vendor", "phpstan", "extension-installer")); err != nil {
		t.Errorf("plugin not materialized: %v", err)
	}
}
```

- [ ] **Step 2: Run the live test**

Run: `COMPOSER_GO_LIVE_NETWORK=1 go test ./internal/orchestrator/... -run TestLiveInstallEmitsPluginWarning -v`

Expected: PASS. Warning text appears in the test log; `vendor/phpstan/extension-installer/` exists.

- [ ] **Step 3: Full sweep**

Run: `go test ./...`

Expected: green offline.

Run: `COMPOSER_GO_LIVE_NETWORK=1 go test ./...`

Expected: green with live tests.

- [ ] **Step 4: Commit**

```bash
git add internal/orchestrator/plugin_live_test.go
git commit -m "test(orchestrator): live plugin-warning verification against Packagist"
```

---

## Stage 2 Plan 6 acceptance check

- [ ] `lock.Package.Type` round-trips through encode/decode and is populated by the resolver.
- [ ] `internal/plugins.Inspect` returns one warning per `composer-plugin` and `composer-installer` package across both `Packages` and `PackagesDev`.
- [ ] The `composer/installers` warning text specifically calls out custom install paths and the fact that packages will land in `vendor/<vendor>/<name>/` regardless.
- [ ] Setting `extra.composer-go.suppress-plugin-warnings: true` in `composer.json` silences all plugin warnings; any other value (including the string `"true"`) does not.
- [ ] The orchestrator emits warnings to `Options.WarnWriter` (or `os.Stderr` when nil) between resolve and fetch.
- [ ] Plugin packages are still fetched, materialized into `vendor/`, and recorded in `composer-go.lock` — the warning is the only behavioral change.
- [ ] `--allow-plugins` (with any value, with no value via `NoOptDefVal`, or with a comma-separated list) is accepted by `install` and `update` and has no runtime effect; the help text says so.
- [ ] `go test ./...` is green offline.
- [ ] `COMPOSER_GO_LIVE_NETWORK=1 go test ./...` is green and includes `TestLiveInstallEmitsPluginWarning`.

If any item fails, fix forward in a follow-up commit before merging. Real plugin execution is explicitly out of scope for stage 2 and beyond — it would require a PHP runtime bridge that contradicts the "single static binary, no PHP for composer-go itself" goal in the design spec.
