# Stage 2 / Plan 3: Real PHP Detection + Platform Requirement Enforcement

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the stage-1 `platform.Fingerprint()` stub with a real one-shot probe of the runtime PHP, surface a structured `Platform` value through the orchestrator into the resolver, and enforce platform requirements (`php`, `ext-*`) during resolution. Default mode warns on unsatisfied platform reqs; `--no-dev` upgrades them to hard errors. A new repeatable `--ignore-platform-req=<name>` flag suppresses checks for the named req. `lib-*` requirements are detected and surfaced as a single info-level "ignored, not implemented" message — they never participate in resolution.

**Architecture:**

- `internal/platform/platform.go` evolves from "return `php-unknown`" into a real probe. The probe shells out to `php -r` once per process and emits a structured `Platform` value `{ PHPVersion, Extensions }`. The plain-string `Fingerprint()` survives as a thin wrapper (`Platform.Fingerprint()`) so every existing cache key continues to work.
- `internal/resolver` gains `Input.Platform *platform.Platform` and a small filter pass: when listing candidate versions for a package, requires keyed by `php` / `ext-*` are evaluated against the runtime; unsatisfiable candidates are dropped before they enter PubGrub. `lib-*` requires are captured and surfaced as warnings.
- The orchestrator probes platform once, passes it to the resolver, and emits warnings to stderr (one line per package per failed req). With `--no-dev`, unsatisfied platform reqs become a fatal error.
- The resolution-result cache key continues to use the *string* fingerprint. Warnings are stored alongside cached results (under a new `warnings` field on `lock.File`) so a cache hit can still re-emit the same diagnostic output.

**Tech stack:** Go 1.22+, standard library `os/exec`. No new third-party deps.

**Depends on:**
- Stage 1 Plan 1 — `internal/manifest`, `internal/constraint`, `internal/lock`, CLI scaffold.
- Stage 1 Plan 3 — `internal/resolver` exposing `resolver.Solve(ctx, Input) (*Result, error)`.
- Stage 1 Plan 6 — `internal/orchestrator` with `pipelineState.platform`, `resolveOrCache`, `defaultDeps`, the `Options` struct.

If any of those packages have moved on, adjust the wiring; the plan touches only the platform package, the resolver's input/version-filter, the orchestrator's platform-probe call site, the lock file's optional `warnings` slice, and the CLI flag wiring.

---

## File structure

| Path | Responsibility |
|------|----------------|
| `internal/platform/platform.go` | Real `Probe()` returning `*Platform`; legacy `Fingerprint()` delegates. |
| `internal/platform/probe.go` | Internal helpers: build the `php -r` script, parse its JSON output. |
| `internal/platform/platform_test.go` | Replace stub pin; cover Probe, caching, missing-php error. |
| `internal/platform/probe_test.go` | Parser unit tests (canned PHP JSON outputs). |
| `internal/platform/check.go` | `Check(req map[string]string, p *Platform, ignored map[string]bool) []Violation` |
| `internal/platform/check_test.go` | Constraint-vs-runtime evaluation tests, `lib-*` ignore behaviour. |
| `internal/resolver/solve.go` | Pass `Input.Platform` into the version lister. |
| `internal/resolver/versions.go` | Filter candidate versions by their platform requires. |
| `internal/resolver/versions_test.go` | New tests covering platform-filtered listings. |
| `internal/resolver/adapter.go` | (Unchanged shape; documented invariant.) |
| `internal/lock/lock.go` | Add optional `Warnings []string` field on `lock.File`. |
| `internal/orchestrator/orchestrator.go` | New `Options` fields: `IgnorePlatformReqs []string`, `Quiet bool`. |
| `internal/orchestrator/pipeline.go` | Probe platform once, thread into resolver, emit/replay warnings. |
| `internal/orchestrator/pipeline_test.go` | Test: warnings emit on default mode; `--no-dev` errors; ignore flag suppresses. |
| `internal/cli/root.go` | New persistent flags: `--ignore-platform-req`, `--quiet`. |
| `internal/cli/install.go` / `update.go` | Wire the new flags into `orchestrator.Options`. |

---

## Task 1: Structured `Platform` type and parser

**Files:**
- Modify: `internal/platform/platform.go`
- Create: `internal/platform/probe.go`
- Create: `internal/platform/probe_test.go`

We replace the bare-string fingerprint with a structured value. The Probe machinery is split off so we can unit-test JSON parsing without `exec`-ing PHP.

- [ ] **Step 1: Write failing parser tests**

Create `internal/platform/probe_test.go`:

```go
package platform

import "testing"

func TestParseProbeOutputBasic(t *testing.T) {
	in := []byte(`{"php":"8.2.14","ext":{"json":"","mbstring":"","openssl":"3.1.4"}}`)
	p, err := parseProbeOutput(in)
	if err != nil {
		t.Fatalf("parseProbeOutput: %v", err)
	}
	if p.PHPVersion.Original != "8.2.14" {
		t.Errorf("PHPVersion.Original = %q", p.PHPVersion.Original)
	}
	if _, ok := p.Extensions["json"]; !ok {
		t.Errorf("ext-json missing")
	}
	if v := p.Extensions["openssl"]; v.Original != "3.1.4" {
		t.Errorf("openssl version = %q", v.Original)
	}
}

func TestParseProbeOutputEmptyExtVersion(t *testing.T) {
	in := []byte(`{"php":"8.3.0","ext":{"core":""}}`)
	p, err := parseProbeOutput(in)
	if err != nil {
		t.Fatalf("parseProbeOutput: %v", err)
	}
	v, ok := p.Extensions["core"]
	if !ok {
		t.Fatalf("ext-core missing")
	}
	// Empty version means "any version present"; we represent that with
	// the zero Version. Callers must treat this as a wildcard.
	if v.Original != "" {
		t.Errorf("expected empty version, got %q", v.Original)
	}
}

func TestParseProbeOutputMalformed(t *testing.T) {
	if _, err := parseProbeOutput([]byte("not json")); err == nil {
		t.Error("expected parse error")
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/platform/...`

Expected: build error on `parseProbeOutput`, `Platform`, etc.

- [ ] **Step 3: Implement the structured types**

Replace `internal/platform/platform.go`:

```go
// Package platform exposes a structured snapshot of the PHP runtime that
// will execute the user's project.
//
// The snapshot is captured by Probe(), which shells out to `php -r` once
// per process and parses the resulting JSON. Subsequent calls return the
// cached result — PHP versions don't change while a single gomposer
// run is in progress.
//
// Cross-process: NOT cached on disk. PHP versions can change with OS
// upgrades, brew installs, or Docker image swaps; probing is cheap enough
// (~30ms cold) that re-probing per run is the correct trade-off.
//
// The string Fingerprint(), inherited from stage 1, still flows into every
// cache key. Stage 2's fingerprint shape is "php-<version>;ext-foo;ext-bar"
// — different from stage 1's "php-unknown" — so all stage-1 cache entries
// naturally invalidate on the upgrade.
package platform

import (
	"sort"
	"strings"
	"sync"

	"github.com/torstendittmann/gomposer/internal/constraint"
)

// Platform is a structured snapshot of the runtime PHP.
type Platform struct {
	// PHPVersion is the parsed PHP_VERSION (e.g. 8.2.14).
	PHPVersion constraint.Version
	// Extensions maps extension name (without the "ext-" prefix) to its
	// reported version. Many extensions report an empty string; in that
	// case the Version is the zero value and callers should treat it as
	// "any version present" — a constraint of `*` is satisfied, but a
	// concrete version constraint like `^7.4` is not.
	Extensions map[string]constraint.Version
}

// Fingerprint returns the canonical string fingerprint for this Platform,
// suitable as part of a cache key. Format:
//
//	php-<version>;ext-name1;ext-name2[@<version>];...
//
// Extension names are sorted to keep the string deterministic. Versions
// are appended only when known (non-empty), since adding "@<empty>" would
// be noise.
func (p *Platform) Fingerprint() string {
	var sb strings.Builder
	sb.WriteString("php-")
	if p == nil || p.PHPVersion.Original == "" {
		sb.WriteString("unknown")
		return sb.String()
	}
	sb.WriteString(p.PHPVersion.Original)
	names := make([]string, 0, len(p.Extensions))
	for n := range p.Extensions {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		sb.WriteString(";ext-")
		sb.WriteString(n)
		if v := p.Extensions[n]; v.Original != "" {
			sb.WriteString("@")
			sb.WriteString(v.Original)
		}
	}
	return sb.String()
}

// HasExtension reports whether `ext-<name>` is loaded. Names should NOT
// include the `ext-` prefix.
func (p *Platform) HasExtension(name string) bool {
	if p == nil || p.Extensions == nil {
		return false
	}
	_, ok := p.Extensions[name]
	return ok
}

// ExtensionVersion returns the loaded version of an extension and whether
// it is present. The returned version may be the zero Version (for
// extensions that don't report a version string).
func (p *Platform) ExtensionVersion(name string) (constraint.Version, bool) {
	if p == nil {
		return constraint.Version{}, false
	}
	v, ok := p.Extensions[name]
	return v, ok
}

// --- process-level cache ---

var (
	probeOnce   sync.Once
	probeResult *Platform
	probeErr    error
)

// Probe returns the runtime Platform. The first call shells out to PHP;
// subsequent calls return the cached result.
func Probe() (*Platform, error) {
	probeOnce.Do(func() {
		probeResult, probeErr = runProbe()
	})
	return probeResult, probeErr
}

// resetProbeCacheForTests is exposed for testing. It is a no-op outside
// tests; production callers MUST NOT call this.
func resetProbeCacheForTests() {
	probeOnce = sync.Once{}
	probeResult = nil
	probeErr = nil
}

// Fingerprint preserves the stage-1 entry point. Production callers prefer
// Probe(); this exists so existing cache-key code keeps compiling.
func Fingerprint() (string, error) {
	p, err := Probe()
	if err != nil {
		return "", err
	}
	return p.Fingerprint(), nil
}
```

- [ ] **Step 4: Implement parser + probe stub**

Create `internal/platform/probe.go`:

```go
package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"

	"github.com/torstendittmann/gomposer/internal/constraint"
)

// probeScript is a single-line PHP program that prints a JSON document of
// the form:
//
//	{"php": "<PHP_VERSION>", "ext": {"name": "<version-or-empty>", ...}}
//
// We use phpversion(<ext>) which returns either the version string or false.
// false is JSON-encoded as `false`, which we coerce to "" in the parser.
const probeScript = `` +
	`$out=["php"=>PHP_VERSION,"ext"=>[]];` +
	`foreach(get_loaded_extensions() as $e){` +
	`$v=phpversion($e); if($v===false){$v="";} ` +
	`$out["ext"][$e]=$v;` +
	`}` +
	`echo json_encode($out);`

// runProbe shells out to `php -r` and parses the result. Used by Probe().
var runProbe = func() (*Platform, error) {
	cmd := exec.Command("php", "-r", probeScript)
	out, err := cmd.Output()
	if err != nil {
		var execErr *exec.Error
		if errors.As(err, &execErr) {
			return nil, fmt.Errorf("platform: php executable not found: %w\n"+
				"hint: install PHP (e.g. `brew install php`, `apt install php-cli`) "+
				"or pass --ignore-platform to skip platform requirement checks", err)
		}
		return nil, fmt.Errorf("platform: php probe failed: %w", err)
	}
	return parseProbeOutput(out)
}

type probeJSON struct {
	PHP string                 `json:"php"`
	Ext map[string]interface{} `json:"ext"`
}

// parseProbeOutput decodes the JSON shape emitted by probeScript. Extensions
// without a version (phpversion()===false) appear as `false` or `""`; both
// collapse to the zero Version.
func parseProbeOutput(raw []byte) (*Platform, error) {
	var pj probeJSON
	if err := json.Unmarshal(raw, &pj); err != nil {
		return nil, fmt.Errorf("platform: parse probe output: %w", err)
	}
	if pj.PHP == "" {
		return nil, errors.New("platform: probe output missing php version")
	}
	pv, err := constraint.ParseVersion(pj.PHP)
	if err != nil {
		return nil, fmt.Errorf("platform: parse php version %q: %w", pj.PHP, err)
	}
	exts := make(map[string]constraint.Version, len(pj.Ext))
	for name, raw := range pj.Ext {
		var ver constraint.Version
		if s, ok := raw.(string); ok && s != "" {
			if parsed, err := constraint.ParseVersion(s); err == nil {
				ver = parsed
			}
		}
		exts[name] = ver
	}
	return &Platform{PHPVersion: pv, Extensions: exts}, nil
}
```

NOTE: if `constraint.ParseVersion` is named differently in your tree, adjust the import. The existing `internal/constraint/version.go` exposes a parser; check the symbol name and update accordingly.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/platform/...`

Expected: parser tests PASS. Probe tests still pending (next task).

- [ ] **Step 6: Commit**

```bash
git add internal/platform
git commit -m "feat(platform): structured Platform type and probe-output parser"
```

---

## Task 2: Probe execution + missing-PHP error path

**Files:**
- Modify: `internal/platform/platform_test.go`
- Modify: `internal/platform/probe.go`

We exercise the real `Probe()` against a stubbed `runProbe`, then verify the missing-PHP error message contains actionable hints.

- [ ] **Step 1: Replace the stage-1 stub test**

Replace `internal/platform/platform_test.go` (the stub-pin test no longer applies):

```go
package platform

import (
	"errors"
	"strings"
	"testing"

	"github.com/torstendittmann/gomposer/internal/constraint"
)

func TestProbeReturnsCachedResult(t *testing.T) {
	resetProbeCacheForTests()
	t.Cleanup(resetProbeCacheForTests)

	calls := 0
	originalRunProbe := runProbe
	t.Cleanup(func() { runProbe = originalRunProbe })
	runProbe = func() (*Platform, error) {
		calls++
		v, _ := constraint.ParseVersion("8.2.14")
		return &Platform{
			PHPVersion: v,
			Extensions: map[string]constraint.Version{"json": {}, "mbstring": {}},
		}, nil
	}

	p1, err := Probe()
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	p2, err := Probe()
	if err != nil {
		t.Fatalf("Probe (cached): %v", err)
	}
	if p1 != p2 {
		t.Errorf("Probe should return cached pointer; got %p vs %p", p1, p2)
	}
	if calls != 1 {
		t.Errorf("runProbe called %d times, want 1", calls)
	}
}

func TestProbeMissingPHPErrorHints(t *testing.T) {
	resetProbeCacheForTests()
	t.Cleanup(resetProbeCacheForTests)

	originalRunProbe := runProbe
	t.Cleanup(func() { runProbe = originalRunProbe })
	runProbe = func() (*Platform, error) {
		return nil, errors.New("platform: php executable not found: hint: brew install php apt install php-cli")
	}

	_, err := Probe()
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "brew install php") {
		t.Errorf("missing brew hint in %q", msg)
	}
	if !strings.Contains(msg, "apt install php-cli") {
		t.Errorf("missing apt hint in %q", msg)
	}
}

func TestFingerprintShape(t *testing.T) {
	v, _ := constraint.ParseVersion("8.2.14")
	p := &Platform{
		PHPVersion: v,
		Extensions: map[string]constraint.Version{
			"json":     {},
			"mbstring": {},
			"openssl":  mustVer(t, "3.1.4"),
		},
	}
	got := p.Fingerprint()
	want := "php-8.2.14;ext-json;ext-mbstring;ext-openssl@3.1.4"
	if got != want {
		t.Errorf("Fingerprint = %q, want %q", got, want)
	}
}

func TestNilPlatformFingerprint(t *testing.T) {
	var p *Platform
	if got := p.Fingerprint(); got != "php-unknown" {
		t.Errorf("Fingerprint(nil) = %q, want php-unknown", got)
	}
}

func mustVer(t *testing.T, s string) constraint.Version {
	t.Helper()
	v, err := constraint.ParseVersion(s)
	if err != nil {
		t.Fatalf("ParseVersion(%q): %v", s, err)
	}
	return v
}
```

- [ ] **Step 2: Verify**

Run: `go test ./internal/platform/...`

Expected: all PASS. The `runProbe` indirection in Task 1 already supports test injection.

- [ ] **Step 3: Smoke test against real PHP (optional, manual)**

If `php` is installed:

```bash
go test -run TestRealProbeSmoke ./internal/platform/... -tags realprobe
```

Add a tagged smoke test if you want a CI-skipped real probe (out of scope for this plan; defer to stage 3 benchmark harness).

- [ ] **Step 4: Commit**

```bash
git add internal/platform
git commit -m "feat(platform): cached Probe(), missing-php error with install hints"
```

---

## Task 3: Constraint check function with `lib-*` handling

**Files:**
- Create: `internal/platform/check.go`
- Create: `internal/platform/check_test.go`

`Check` evaluates a package's `require` map against the runtime Platform and returns a list of `Violation`s. It is the only place that knows the rules: `php` matches `Platform.PHPVersion`, `ext-*` matches loaded extensions (with the empty-version-as-wildcard rule), `lib-*` is collected separately as "ignored".

- [ ] **Step 1: Write failing tests**

Create `internal/platform/check_test.go`:

```go
package platform

import (
	"testing"

	"github.com/torstendittmann/gomposer/internal/constraint"
)

func newTestPlatform(t *testing.T) *Platform {
	t.Helper()
	v, _ := constraint.ParseVersion("8.2.14")
	return &Platform{
		PHPVersion: v,
		Extensions: map[string]constraint.Version{
			"json":     {}, // loaded, no version
			"mbstring": {},
			"openssl":  mustVer(t, "3.1.4"),
		},
	}
}

func TestCheckPHPSatisfied(t *testing.T) {
	p := newTestPlatform(t)
	v := Check(map[string]string{"php": "^8.0"}, p, nil)
	if len(v) != 0 {
		t.Errorf("expected no violations, got %+v", v)
	}
}

func TestCheckPHPUnsatisfied(t *testing.T) {
	p := newTestPlatform(t)
	v := Check(map[string]string{"php": "^7.4"}, p, nil)
	if len(v) != 1 {
		t.Fatalf("violations = %+v", v)
	}
	if v[0].Req != "php" || v[0].Kind != ViolationVersion {
		t.Errorf("got %+v", v[0])
	}
}

func TestCheckExtensionMissing(t *testing.T) {
	p := newTestPlatform(t)
	v := Check(map[string]string{"ext-curl": "*"}, p, nil)
	if len(v) != 1 || v[0].Req != "ext-curl" || v[0].Kind != ViolationMissing {
		t.Errorf("got %+v", v)
	}
}

func TestCheckExtensionPresentWildcardOK(t *testing.T) {
	p := newTestPlatform(t)
	v := Check(map[string]string{"ext-json": "*"}, p, nil)
	if len(v) != 0 {
		t.Errorf("got %+v", v)
	}
}

func TestCheckExtensionPresentSpecificVersionUnknown(t *testing.T) {
	p := newTestPlatform(t)
	// ext-json reports empty version; a specific version constraint cannot
	// be evaluated -> treated as unsatisfied.
	v := Check(map[string]string{"ext-json": "^7.0"}, p, nil)
	if len(v) != 1 || v[0].Kind != ViolationVersion {
		t.Errorf("got %+v", v)
	}
}

func TestCheckExtensionVersionSatisfied(t *testing.T) {
	p := newTestPlatform(t)
	v := Check(map[string]string{"ext-openssl": "^3.0"}, p, nil)
	if len(v) != 0 {
		t.Errorf("got %+v", v)
	}
}

func TestCheckIgnoreSet(t *testing.T) {
	p := newTestPlatform(t)
	v := Check(map[string]string{"php": "^7.4"}, p, map[string]bool{"php": true})
	if len(v) != 0 {
		t.Errorf("ignored req should not produce violation, got %+v", v)
	}
}

func TestCheckLibStarIgnoredWithFlag(t *testing.T) {
	p := newTestPlatform(t)
	v := Check(map[string]string{"lib-curl": ">=7.0"}, p, nil)
	if len(v) != 1 || v[0].Kind != ViolationLibIgnored || v[0].Req != "lib-curl" {
		t.Errorf("got %+v", v)
	}
}

func TestIsPlatformReq(t *testing.T) {
	cases := map[string]bool{
		"php": true, "ext-json": true, "lib-curl": true,
		"php-64bit": true, "vendor/pkg": false, "ext-": true,
	}
	for k, want := range cases {
		if got := IsPlatformReq(k); got != want {
			t.Errorf("IsPlatformReq(%q) = %v, want %v", k, got, want)
		}
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/platform/...`

Expected: build error on `Check`, `Violation`, `IsPlatformReq`.

- [ ] **Step 3: Implement**

Create `internal/platform/check.go`:

```go
package platform

import (
	"strings"

	"github.com/torstendittmann/gomposer/internal/constraint"
)

// ViolationKind classifies why a platform req was not satisfied.
type ViolationKind int

const (
	// ViolationVersion: the req is for php or an installed extension whose
	// reported version doesn't satisfy the constraint.
	ViolationVersion ViolationKind = iota
	// ViolationMissing: the req is for an extension that is not loaded.
	ViolationMissing
	// ViolationLibIgnored: the req is `lib-*`, which gomposer does not
	// implement. Not a real failure; surfaced once per run as info.
	ViolationLibIgnored
	// ViolationUnparseable: the constraint string itself failed to parse.
	ViolationUnparseable
)

// Violation describes a single failed platform req check.
type Violation struct {
	Req        string         // "php", "ext-mbstring", "lib-curl", ...
	Constraint string         // raw constraint from the manifest
	Kind       ViolationKind
	// Have describes what the runtime actually has, suitable for messages.
	// Examples: "php 8.2.14", "ext-openssl 3.1.4", "ext-json (no version)",
	// "ext-curl not loaded".
	Have string
	// PlatformVersion is the runtime version when applicable; zero otherwise.
	PlatformVersion constraint.Version
}

// IsPlatformReq reports whether a require key denotes a platform req
// (php / ext-* / lib-*) rather than a regular package name. The classifier
// matches Composer's: any key starting with `php`, `ext-`, or `lib-`.
func IsPlatformReq(name string) bool {
	if name == "php" || strings.HasPrefix(name, "php-") {
		return true
	}
	if strings.HasPrefix(name, "ext-") {
		return true
	}
	if strings.HasPrefix(name, "lib-") {
		return true
	}
	return false
}

// Check evaluates `requires` against `p` and returns the unsatisfied reqs.
// `ignored` is consulted by exact key match: any req present in the map is
// treated as if it had no constraint. A nil/empty map is fine.
//
// Non-platform reqs in `requires` are ignored by Check; the caller is
// responsible for filtering.
func Check(requires map[string]string, p *Platform, ignored map[string]bool) []Violation {
	if len(requires) == 0 {
		return nil
	}
	out := make([]Violation, 0)
	for name, raw := range requires {
		if !IsPlatformReq(name) {
			continue
		}
		if ignored != nil && ignored[name] {
			continue
		}
		switch {
		case name == "php" || strings.HasPrefix(name, "php-"):
			out = append(out, checkPHP(name, raw, p)...)
		case strings.HasPrefix(name, "ext-"):
			out = append(out, checkExt(name, raw, p)...)
		case strings.HasPrefix(name, "lib-"):
			out = append(out, Violation{Req: name, Constraint: raw, Kind: ViolationLibIgnored})
		}
	}
	return out
}

func checkPHP(name, raw string, p *Platform) []Violation {
	c, err := constraint.Parse(raw)
	if err != nil {
		return []Violation{{Req: name, Constraint: raw, Kind: ViolationUnparseable}}
	}
	if p == nil || p.PHPVersion.Original == "" {
		return []Violation{{Req: name, Constraint: raw, Kind: ViolationMissing, Have: "php (unknown)"}}
	}
	// `php-64bit` and similar sub-keys are out of MVP scope; treat as a php
	// version check ignoring the suffix. (Composer treats php-64bit as a
	// platform-arch req; we evaluate against runtime only.)
	if !c.Satisfies(p.PHPVersion) {
		return []Violation{{
			Req: name, Constraint: raw, Kind: ViolationVersion,
			Have:            "php " + p.PHPVersion.Original,
			PlatformVersion: p.PHPVersion,
		}}
	}
	return nil
}

func checkExt(name, raw string, p *Platform) []Violation {
	extName := strings.TrimPrefix(name, "ext-")
	c, err := constraint.Parse(raw)
	if err != nil {
		return []Violation{{Req: name, Constraint: raw, Kind: ViolationUnparseable}}
	}
	have, ok := p.ExtensionVersion(extName)
	if !ok {
		return []Violation{{
			Req: name, Constraint: raw, Kind: ViolationMissing,
			Have: name + " not loaded",
		}}
	}
	if have.Original == "" {
		// Wildcard ("*") is satisfied by mere presence; anything more
		// specific cannot be evaluated and is treated as unsatisfied.
		if isWildcardConstraint(raw) {
			return nil
		}
		return []Violation{{
			Req: name, Constraint: raw, Kind: ViolationVersion,
			Have: name + " (no version)",
		}}
	}
	if !c.Satisfies(have) {
		return []Violation{{
			Req: name, Constraint: raw, Kind: ViolationVersion,
			Have:            name + " " + have.Original,
			PlatformVersion: have,
		}}
	}
	return nil
}

// isWildcardConstraint returns true for the few raw constraint strings
// that always match. We include the empty string defensively; callers
// pass raw strings from JSON.
func isWildcardConstraint(raw string) bool {
	s := strings.TrimSpace(raw)
	return s == "" || s == "*" || s == ">=0" || s == ">=0.0" || s == ">=0.0.0"
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/platform/...`

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/platform
git commit -m "feat(platform): Check() for php/ext-*/lib-* requirements"
```

---

## Task 4: Resolver consumes `Platform` and filters candidates

**Files:**
- Modify: `internal/resolver/solve.go`
- Modify: `internal/resolver/versions.go`
- Modify: `internal/resolver/versions_test.go`

We give the resolver a Platform-aware view of the registry: when listing candidate versions, drop any version whose `require` map contains an unsatisfiable platform req. Deciding to keep "warn-don't-fail" semantics happens in the orchestrator after resolution; the resolver itself simply prefers a version that is installable on the current platform.

Two filter modes:
1. **Strict**: drop candidates with any platform-req violation (default).
2. **Permissive (--ignore-platform / --ignore-platform-req=*)**: keep all candidates regardless of platform.

The orchestrator decides which mode based on flags. In all cases we also collect the *effective* violations on the chosen versions so the orchestrator can warn or error.

- [ ] **Step 1: Add `Platform` to resolver Input and a hook**

Modify `internal/resolver/solve.go` (the `Input` struct and `Solve`):

```go
import (
	// ... existing imports ...
	"github.com/torstendittmann/gomposer/internal/platform"
)

// Input is everything Solve needs.
type Input struct {
	Manifest *manifest.Manifest
	Lock     *lock.File
	Source   registry.SourceLookup
	IncludeDev bool

	// Platform, if non-nil, is used to filter candidate versions: any
	// version whose require map contains an unsatisfiable php/ext-* req
	// (and the req is not in IgnorePlatformReqs) is removed before
	// PubGrub considers it.
	//
	// Stage 2 wires this in. Stage 1 left it nil (no filtering).
	Platform *platform.Platform

	// IgnorePlatformReqs is the set of platform-req names whose checks
	// should be skipped entirely. Maps to the CLI's --ignore-platform-req
	// flag (repeatable). A special key of "*" means "skip all" (mirrors
	// Composer's --ignore-platform-reqs).
	IgnorePlatformReqs map[string]bool

	// PlatformFingerprint is the string form of Platform, retained for
	// the resolution-result cache key.
	PlatformFingerprint string
	MinimumStability    string
}
```

In `Solve`, plumb the platform values through into the version lister:

```go
vl := newVersionLister(in.Source, minStab)
vl.platform = in.Platform
vl.ignorePlatformReqs = in.IgnorePlatformReqs
```

(Adjust the existing `newVersionLister` constructor or set fields directly. If the type lives in `versions.go` and is unexported, just set fields after construction.)

- [ ] **Step 2: Add the filter to versions.go**

In `internal/resolver/versions.go`, augment the version lister with platform-aware filtering. The exact symbol names depend on what Plan 3 produced; the addition looks like:

```go
type versionLister struct {
	source     registry.SourceLookup
	minStab    constraint.Stability

	platform           *platform.Platform
	ignorePlatformReqs map[string]bool

	// ... existing fields (cache, etc.)
}

// versionInstallable returns true when every platform req in the version's
// require map is satisfied (or ignored) on the current platform. lib-* reqs
// are always treated as installable: we never gate resolution on them.
func (vl *versionLister) versionInstallable(rec registry.PackageVersion) bool {
	if vl.platform == nil {
		return true
	}
	if vl.ignorePlatformReqs != nil && vl.ignorePlatformReqs["*"] {
		return true
	}
	violations := platform.Check(rec.Require, vl.platform, vl.ignorePlatformReqs)
	for _, v := range violations {
		if v.Kind == platform.ViolationLibIgnored {
			continue
		}
		return false
	}
	return true
}
```

Then, in the existing `versions()` (or whatever method returns the candidate list), apply the filter:

```go
filtered := raw[:0]
for _, v := range raw {
	if vl.versionInstallable(v.Record) {
		filtered = append(filtered, v)
	}
}
return filtered, nil
```

- [ ] **Step 3: Write a failing test**

Append to `internal/resolver/versions_test.go`:

```go
func TestVersionListerFiltersIncompatibleByPlatform(t *testing.T) {
	php82, _ := constraint.ParseVersion("8.2.0")
	pf := &platform.Platform{PHPVersion: php82, Extensions: map[string]constraint.Version{}}

	src := newFakeSource(map[string][]registry.PackageVersion{
		"acme/widget": {
			{Name: "acme/widget", Version: "1.0.0", VersionNorm: "1.0.0.0",
				Require: map[string]string{"php": "^7.4"}},
			{Name: "acme/widget", Version: "2.0.0", VersionNorm: "2.0.0.0",
				Require: map[string]string{"php": "^8.0"}},
		},
	})
	vl := newVersionLister(src, constraint.Stable)
	vl.platform = pf

	got, err := vl.versions(context.Background(), "acme/widget")
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if len(got) != 1 || got[0].Record.Version != "2.0.0" {
		t.Errorf("expected only 2.0.0 to survive php-8.2 filter; got %+v", got)
	}
}

func TestVersionListerHonorsIgnoreAll(t *testing.T) {
	php82, _ := constraint.ParseVersion("8.2.0")
	pf := &platform.Platform{PHPVersion: php82}
	src := newFakeSource(map[string][]registry.PackageVersion{
		"acme/widget": {{
			Name: "acme/widget", Version: "1.0.0", VersionNorm: "1.0.0.0",
			Require: map[string]string{"php": "^7.4"},
		}},
	})
	vl := newVersionLister(src, constraint.Stable)
	vl.platform = pf
	vl.ignorePlatformReqs = map[string]bool{"*": true}

	got, _ := vl.versions(context.Background(), "acme/widget")
	if len(got) != 1 {
		t.Errorf("ignore-all should keep all candidates; got %d", len(got))
	}
}
```

(`newFakeSource` is whatever stage-1 plan 3 introduced; reuse it or add a small helper.)

- [ ] **Step 4: Run tests**

Run: `go test ./internal/resolver/...`

Expected: PASS, including the new tests.

- [ ] **Step 5: Confirm `lib-*` does NOT filter**

Add one more focused test:

```go
func TestVersionListerKeepsLibStar(t *testing.T) {
	pf := &platform.Platform{PHPVersion: mustVer(t, "8.2.0")}
	src := newFakeSource(map[string][]registry.PackageVersion{
		"acme/widget": {{
			Name: "acme/widget", Version: "1.0.0", VersionNorm: "1.0.0.0",
			Require: map[string]string{"lib-curl": ">=10.0"},
		}},
	})
	vl := newVersionLister(src, constraint.Stable)
	vl.platform = pf

	got, _ := vl.versions(context.Background(), "acme/widget")
	if len(got) != 1 {
		t.Errorf("lib-* should NOT cause filtering; got %d", len(got))
	}
}
```

Run: `go test ./internal/resolver/...`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/resolver
git commit -m "feat(resolver): platform-aware candidate filtering"
```

---

## Task 5: Lockfile carries warnings across cache hits

**Files:**
- Modify: `internal/lock/lock.go`
- Modify: `internal/lock/lock_test.go`

When the resolution-result cache hits, we want to re-emit the same platform warnings without re-running the resolver. Persist them on the lock file under a new optional `warnings` field.

- [ ] **Step 1: Append failing test**

Append to `internal/lock/lock_test.go`:

```go
func TestLockFileRoundTripsWarnings(t *testing.T) {
	in := &lock.File{
		SchemaVersion: lock.SchemaVersion,
		Generator:     lock.Generator{Name: "gomposer", Version: "test"},
		Warnings:      []string{"acme/x: php ^7.4 not satisfied (have php 8.2.14)"},
	}
	enc, err := in.Encode()
	if err != nil {
		t.Fatal(err)
	}
	out, err := lock.Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Warnings) != 1 || out.Warnings[0] != in.Warnings[0] {
		t.Errorf("warnings round-trip = %+v", out.Warnings)
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/lock/...`

Expected: build error on `Warnings`.

- [ ] **Step 3: Add the field**

In `internal/lock/lock.go`, add to the `File` struct:

```go
// Warnings, if non-empty, are human-readable strings the orchestrator
// should print to stderr after a cache hit. They mirror what would have
// been printed during a fresh resolution and exist so cache-hit runs
// produce identical UX.
//
// We store them in the lockfile (NOT only in the resolution-result
// cache) because the JSON lockfile is the canonical source of truth a
// user can inspect, and a future `gomposer check` should be able to
// re-print them without re-resolving.
Warnings []string `json:"warnings,omitempty"`
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/lock/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/lock
git commit -m "feat(lock): optional Warnings slice for cache-replay parity"
```

---

## Task 6: Orchestrator probes platform once and threads it through

**Files:**
- Modify: `internal/orchestrator/orchestrator.go`
- Modify: `internal/orchestrator/pipeline.go`

Wire `Probe()` into `pipelineState`, pass the structured Platform into the resolver, and stop using the legacy `Fingerprint()` directly (call `Platform.Fingerprint()` instead, which is identical when Probe succeeds and degrades to `php-unknown` when Platform is nil).

- [ ] **Step 1: Extend `Options`**

In `internal/orchestrator/orchestrator.go`, add fields to `Options`:

```go
type Options struct {
	// ... existing fields ...

	// IgnorePlatformReqs is the parsed form of --ignore-platform-req
	// (repeatable). A value of "*" means "ignore all platform reqs"
	// (--ignore-platform).
	IgnorePlatformReqs []string

	// Quiet suppresses non-error output (warnings, info messages).
	Quiet bool
}
```

- [ ] **Step 2: Probe in `newPipelineState`**

Modify `internal/orchestrator/pipeline.go`. Replace the existing `newPipelineState`:

```go
type pipelineState struct {
	opts          Options
	manifest      *manifest.Manifest
	manifestBytes []byte
	lockBytes     []byte
	platform      *platform.Platform  // structured, may be nil only on probe error w/ ignore-all
	platformStr   string              // fingerprint string (cache key input)
	cacheKey      string
	ignoreSet     map[string]bool
}

func newPipelineState(opts Options, m *manifest.Manifest) (*pipelineState, error) {
	manifestBytes, err := os.ReadFile(filepath.Join(opts.ProjectDir, "composer.json"))
	if err != nil {
		return nil, fmt.Errorf("orchestrator: read manifest bytes: %w", err)
	}
	lockBytes, _ := os.ReadFile(filepath.Join(opts.ProjectDir, "gomposer.lock"))

	ignore := buildIgnoreSet(opts.IgnorePlatformReqs)

	var pf *platform.Platform
	if !ignore["*"] {
		pf, err = platform.Probe()
		if err != nil {
			return nil, fmt.Errorf("orchestrator: %w", err)
		}
	}
	return &pipelineState{
		opts:          opts,
		manifest:      m,
		manifestBytes: manifestBytes,
		lockBytes:     lockBytes,
		platform:      pf,
		platformStr:   pf.Fingerprint(),
		cacheKey:      computeCacheKey(manifestBytes, lockBytes, pf.Fingerprint()),
		ignoreSet:     ignore,
	}, nil
}

func buildIgnoreSet(list []string) map[string]bool {
	out := make(map[string]bool, len(list))
	for _, n := range list {
		out[n] = true
	}
	return out
}
```

- [ ] **Step 3: Pass Platform into the resolver**

Replace `resolveFunc` and `resolveOrCache`'s resolver call with one that takes the new fields:

```go
var resolveFunc = func(ctx context.Context, ps *pipelineState, src registry.SourceLookup, includeDev bool) (*resolver.Result, error) {
	return resolver.Solve(ctx, resolver.Input{
		Manifest:            ps.manifest,
		Source:              src,
		IncludeDev:          includeDev,
		Platform:            ps.platform,
		IgnorePlatformReqs:  ps.ignoreSet,
		PlatformFingerprint: ps.platformStr,
	})
}
```

Update the callsite in `resolveOrCache`:

```go
res, err := resolveFunc(ctx, ps, src, !ps.opts.NoDev)
```

- [ ] **Step 4: Build**

Run: `go build ./...`

Expected: clean. Existing orchestrator tests still pass because the test seam (`Source`) and the empty-manifest fast path are untouched.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/orchestrator/...`

Expected: PASS. If the existing tests inject a `Platform`-less Input via `resolveFunc` indirection, update them to use the new signature.

- [ ] **Step 6: Commit**

```bash
git add internal/orchestrator
git commit -m "feat(orchestrator): probe platform and pass it into the resolver"
```

---

## Task 7: Compute, emit, and persist platform warnings

**Files:**
- Modify: `internal/orchestrator/pipeline.go`
- Create: `internal/orchestrator/pipeline_test.go`

After the resolver returns its `Result`, walk every chosen package's `require` map, collect violations, and:

- Default mode: print each violation to stderr (one line per package per req) and stash the strings in `lockFile.Warnings`.
- `--no-dev`: any violation is a fatal error (return after printing all of them).
- `--quiet`: print nothing to stderr; warnings still land in the lockfile.

`lib-*` violations are coalesced into a single info-level message: `"gomposer: ignoring lib-* platform requirements (not implemented)"`.

- [ ] **Step 1: Write failing tests**

Create `internal/orchestrator/pipeline_test.go`:

```go
package orchestrator

import (
	"bytes"
	"strings"
	"testing"

	"github.com/torstendittmann/gomposer/internal/constraint"
	"github.com/torstendittmann/gomposer/internal/lock"
	"github.com/torstendittmann/gomposer/internal/platform"
)

func mustVer(t *testing.T, s string) constraint.Version {
	t.Helper()
	v, err := constraint.ParseVersion(s)
	if err != nil {
		t.Fatalf("ParseVersion(%q): %v", s, err)
	}
	return v
}

func TestEvaluatePlatformWarningsDefaultMode(t *testing.T) {
	pf := &platform.Platform{PHPVersion: mustVer(t, "8.2.0")}
	pkgs := []lock.Package{
		{Name: "acme/x", Version: "1.0.0", Require: map[string]string{"php": "^7.4"}},
	}
	var stderr bytes.Buffer
	warnings, err := evaluatePlatformWarnings(pkgs, pf, nil, false /*noDev*/, false /*quiet*/, &stderr)
	if err != nil {
		t.Fatalf("evaluatePlatformWarnings: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %+v", warnings)
	}
	if !strings.Contains(warnings[0], "acme/x") || !strings.Contains(warnings[0], "php") {
		t.Errorf("warning text: %q", warnings[0])
	}
	if !strings.Contains(stderr.String(), "acme/x") {
		t.Errorf("stderr did not contain warning: %q", stderr.String())
	}
}

func TestEvaluatePlatformWarningsNoDevFails(t *testing.T) {
	pf := &platform.Platform{PHPVersion: mustVer(t, "8.2.0")}
	pkgs := []lock.Package{
		{Name: "acme/x", Version: "1.0.0", Require: map[string]string{"php": "^7.4"}},
	}
	var stderr bytes.Buffer
	_, err := evaluatePlatformWarnings(pkgs, pf, nil, true /*noDev*/, false, &stderr)
	if err == nil {
		t.Error("expected error in --no-dev mode")
	}
}

func TestEvaluatePlatformWarningsIgnoreFlag(t *testing.T) {
	pf := &platform.Platform{PHPVersion: mustVer(t, "8.2.0")}
	pkgs := []lock.Package{
		{Name: "acme/x", Version: "1.0.0", Require: map[string]string{"php": "^7.4"}},
	}
	ignore := map[string]bool{"php": true}
	var stderr bytes.Buffer
	warnings, err := evaluatePlatformWarnings(pkgs, pf, ignore, true /*noDev*/, false, &stderr)
	if err != nil {
		t.Fatalf("ignored req should not fail: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings should be empty: %+v", warnings)
	}
}

func TestEvaluatePlatformWarningsQuiet(t *testing.T) {
	pf := &platform.Platform{PHPVersion: mustVer(t, "8.2.0")}
	pkgs := []lock.Package{
		{Name: "acme/x", Version: "1.0.0", Require: map[string]string{"php": "^7.4"}},
	}
	var stderr bytes.Buffer
	warnings, _ := evaluatePlatformWarnings(pkgs, pf, nil, false, true /*quiet*/, &stderr)
	if stderr.Len() != 0 {
		t.Errorf("--quiet should suppress stderr; got %q", stderr.String())
	}
	if len(warnings) != 1 {
		t.Errorf("warnings should still be recorded for the lockfile: %+v", warnings)
	}
}

func TestEvaluatePlatformWarningsLibStarOnce(t *testing.T) {
	pf := &platform.Platform{PHPVersion: mustVer(t, "8.2.0")}
	pkgs := []lock.Package{
		{Name: "a/x", Require: map[string]string{"lib-curl": ">=7.0"}},
		{Name: "a/y", Require: map[string]string{"lib-icu": ">=70"}},
	}
	var stderr bytes.Buffer
	warnings, err := evaluatePlatformWarnings(pkgs, pf, nil, false, false, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	libCount := 0
	for _, w := range warnings {
		if strings.Contains(w, "lib-*") {
			libCount++
		}
	}
	if libCount != 1 {
		t.Errorf("expected exactly one coalesced lib-* warning; got %d in %+v", libCount, warnings)
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/orchestrator/...`

Expected: build error on `evaluatePlatformWarnings`.

- [ ] **Step 3: Implement**

Append to `internal/orchestrator/pipeline.go`:

```go
import "io"

// evaluatePlatformWarnings walks every package's require map, runs
// platform.Check, and produces:
//   - a slice of formatted warning strings (for the lockfile + future replay),
//   - prints each to `stderr` unless `quiet` is set,
//   - errors if `noDev` is true and any non-lib-* violation occurred.
//
// lib-* violations are coalesced into a single info-level message printed
// at most once per call.
func evaluatePlatformWarnings(
	pkgs []lock.Package,
	pf *platform.Platform,
	ignored map[string]bool,
	noDev bool,
	quiet bool,
	stderr io.Writer,
) ([]string, error) {
	if pf == nil {
		// Platform was skipped (e.g. --ignore-platform); nothing to check.
		return nil, nil
	}
	var (
		warnings  []string
		hardFails []string
		sawLib    bool
	)
	for _, p := range pkgs {
		violations := platform.Check(p.Require, pf, ignored)
		for _, v := range violations {
			if v.Kind == platform.ViolationLibIgnored {
				sawLib = true
				continue
			}
			line := formatViolation(p.Name, v)
			warnings = append(warnings, line)
			hardFails = append(hardFails, line)
			if !quiet {
				fmt.Fprintln(stderr, "gomposer: "+line)
			}
		}
	}
	if sawLib {
		const libLine = "ignoring lib-* platform requirements (not implemented)"
		warnings = append(warnings, libLine)
		if !quiet {
			fmt.Fprintln(stderr, "gomposer: "+libLine)
		}
	}
	if noDev && len(hardFails) > 0 {
		return warnings, fmt.Errorf("orchestrator: platform requirements unsatisfied (--no-dev): %d violation(s)", len(hardFails))
	}
	return warnings, nil
}

func formatViolation(pkg string, v platform.Violation) string {
	switch v.Kind {
	case platform.ViolationMissing:
		return fmt.Sprintf("%s requires %s %q but %s", pkg, v.Req, v.Constraint, v.Have)
	case platform.ViolationVersion:
		return fmt.Sprintf("%s requires %s %q (have %s)", pkg, v.Req, v.Constraint, v.Have)
	case platform.ViolationUnparseable:
		return fmt.Sprintf("%s requires %s %q (unparseable constraint)", pkg, v.Req, v.Constraint)
	default:
		return fmt.Sprintf("%s requires %s %q", pkg, v.Req, v.Constraint)
	}
}
```

- [ ] **Step 4: Wire into the pipeline**

In `runFullPipeline`, after `resolveOrCache` returns and before `fetchAll`, insert:

```go
all := append([]lock.Package(nil), lockFile.Packages...)
if !opts.NoDev {
	all = append(all, lockFile.PackagesDev...)
}

// Platform warnings: emit, persist on the lockfile so cache-hit runs can
// re-emit them, and (in --no-dev) escalate to a hard error.
warnings, err := evaluatePlatformWarnings(all, ps.platform, ps.ignoreSet, opts.NoDev, opts.Quiet, os.Stderr)
if err != nil {
	return err
}
if len(warnings) > 0 {
	lockFile.Warnings = warnings
} else if !opts.NoDev {
	// Replay-on-cache-hit: if we're using a cached/existing lock and it
	// already has warnings, re-emit them now.
	if !opts.Quiet {
		for _, w := range lockFile.Warnings {
			fmt.Fprintln(os.Stderr, "gomposer: "+w)
		}
	}
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/orchestrator/...`

Expected: PASS, including the new `pipeline_test.go` cases.

- [ ] **Step 6: Commit**

```bash
git add internal/orchestrator
git commit -m "feat(orchestrator): emit/persist platform warnings; --no-dev hard error"
```

---

## Task 8: CLI flags `--ignore-platform-req`, `--ignore-platform`, `--quiet`

**Files:**
- Modify: `internal/cli/root.go`
- Modify: `internal/cli/install.go`
- Modify: `internal/cli/update.go`
- Modify: `internal/cli/install_test.go`

Add the new flags as persistent (so both `install` and `update` see them) and pass their parsed values into `orchestrator.Options`.

- [ ] **Step 1: Wire flags in `root.go`**

Modify `internal/cli/root.go`:

```go
var (
	flagVerbose             bool
	flagNoDev               bool
	flagQuiet               bool
	flagIgnorePlatform      bool
	flagIgnorePlatformReqs  []string
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "gomposer",
		Short:         "A fast Go-based PHP package manager",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().BoolVarP(&flagVerbose, "verbose", "v", false, "verbose output with timing breakdown")
	root.PersistentFlags().BoolVar(&flagNoDev, "no-dev", false, "skip require-dev; treat platform req mismatches as fatal")
	root.PersistentFlags().BoolVarP(&flagQuiet, "quiet", "q", false, "suppress non-error output")
	root.PersistentFlags().BoolVar(&flagIgnorePlatform, "ignore-platform", false, "skip ALL platform requirement checks (php / ext-*)")
	root.PersistentFlags().StringSliceVar(&flagIgnorePlatformReqs, "ignore-platform-req", nil,
		"skip a specific platform requirement (repeatable, e.g. --ignore-platform-req=php --ignore-platform-req=ext-curl)")

	root.AddCommand(newInstallCmd())
	root.AddCommand(newUpdateCmd())
	return root
}
```

- [ ] **Step 2: Translate flags in `install.go` and `update.go`**

In both files, replace the construction of `orchestrator.Options` with:

```go
ignored := append([]string(nil), flagIgnorePlatformReqs...)
if flagIgnorePlatform {
	ignored = append(ignored, "*")
}
return orchestrator.Install(ctx, orchestrator.Options{
	ProjectDir:         projectDir,
	NoDev:              flagNoDev,
	Verbose:            flagVerbose,
	Quiet:              flagQuiet,
	IgnorePlatformReqs: ignored,
})
```

(The same edit in `update.go`, with `orchestrator.Update`.)

- [ ] **Step 3: Add a flag-wiring test**

Append to `internal/cli/install_test.go`:

```go
func TestInstallAcceptsIgnorePlatformReqRepeated(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"install",
		"--project", t.TempDir(),
		"--ignore-platform-req=php",
		"--ignore-platform-req=ext-curl",
	})
	// Will fail at orchestrator level (no manifest), but we only assert
	// flag parsing works.
	_ = root.Execute()
	if len(flagIgnorePlatformReqs) != 2 {
		t.Errorf("flagIgnorePlatformReqs = %+v", flagIgnorePlatformReqs)
	}
}
```

- [ ] **Step 4: Build and smoke**

```bash
go build ./cmd/gomposer
./gomposer install --help | grep ignore-platform
```

Expected: help text shows both flags.

- [ ] **Step 5: Run tests**

Run: `go test ./...`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/cli
git commit -m "feat(cli): --ignore-platform, --ignore-platform-req, --quiet flags"
```

---

## Task 9: End-to-end integration test for platform enforcement

**Files:**
- Modify: `internal/orchestrator/orchestrator_test.go`

A full-pipeline test using fake fetcher/materializer/autoloader that:
1. Resolves a manifest where the chosen package has `php: ^7.4`.
2. With Platform=PHP 8.2 and default mode: install succeeds, lock has 1 warning.
3. Same setup with `--no-dev`: install fails.
4. Same setup with `IgnorePlatformReqs=["php"]`: install succeeds, lock has 0 warnings.

- [ ] **Step 1: Append failing test**

Append to `internal/orchestrator/orchestrator_test.go`:

```go
func TestInstallEmitsPlatformWarningsAndPersistsOnLock(t *testing.T) {
	resetPlatformProbeForTest(t, "8.2.14")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"),
		[]byte(`{"name":"vendor/pkg","require":{"acme/leaf":"1.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{pkgs: map[string]*registry.PackageMetadata{
		"acme/leaf": {Name: "acme/leaf", Versions: []registry.PackageVersion{{
			Name: "acme/leaf", Version: "1.0.0", VersionNorm: "1.0.0.0",
			Dist:    registry.Dist{Type: "zip", URL: "u", Sha: "s"},
			Require: map[string]string{"php": "^7.4"},
		}}},
	}}

	opts := Options{
		ProjectDir:   dir,
		Source:       src,
		Fetcher:      &fakeFetcher{},
		Materializer: &fakeMaterializer{},
		Autoloader:   &fakeAutoloader{},
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "gomposer.lock"))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	f, err := lock.Decode(data)
	if err != nil {
		t.Fatalf("decode lock: %v", err)
	}
	if len(f.Warnings) == 0 {
		t.Errorf("expected platform warning persisted on lock; got %+v", f.Warnings)
	}
}

func TestInstallNoDevFailsOnPlatformMismatch(t *testing.T) {
	resetPlatformProbeForTest(t, "8.2.14")

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "composer.json"),
		[]byte(`{"name":"x/y","require":{"acme/leaf":"1.0.0"}}`), 0o644)
	src := &fakeSource{pkgs: map[string]*registry.PackageMetadata{
		"acme/leaf": {Name: "acme/leaf", Versions: []registry.PackageVersion{{
			Name: "acme/leaf", Version: "1.0.0", VersionNorm: "1.0.0.0",
			Dist:    registry.Dist{Type: "zip", URL: "u", Sha: "s"},
			Require: map[string]string{"php": "^7.4"},
		}}},
	}}
	opts := Options{
		ProjectDir:   dir,
		NoDev:        true,
		Source:       src,
		Fetcher:      &fakeFetcher{},
		Materializer: &fakeMaterializer{},
		Autoloader:   &fakeAutoloader{},
	}
	if err := Install(context.Background(), opts); err == nil {
		t.Error("--no-dev should fail when platform req unsatisfied")
	}
}

func TestInstallIgnorePlatformReqSuppresses(t *testing.T) {
	resetPlatformProbeForTest(t, "8.2.14")

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "composer.json"),
		[]byte(`{"name":"x/y","require":{"acme/leaf":"1.0.0"}}`), 0o644)
	src := &fakeSource{pkgs: map[string]*registry.PackageMetadata{
		"acme/leaf": {Name: "acme/leaf", Versions: []registry.PackageVersion{{
			Name: "acme/leaf", Version: "1.0.0", VersionNorm: "1.0.0.0",
			Dist:    registry.Dist{Type: "zip", URL: "u", Sha: "s"},
			Require: map[string]string{"php": "^7.4"},
		}}},
	}}
	opts := Options{
		ProjectDir:         dir,
		Source:             src,
		Fetcher:            &fakeFetcher{},
		Materializer:       &fakeMaterializer{},
		Autoloader:         &fakeAutoloader{},
		IgnorePlatformReqs: []string{"php"},
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "gomposer.lock"))
	f, _ := lock.Decode(data)
	for _, w := range f.Warnings {
		if strings.Contains(w, "acme/leaf") {
			t.Errorf("warning should be suppressed: %q", w)
		}
	}
}
```

Add a small helper in the same file so tests can install a fake Platform without touching real PHP:

```go
import (
	platformpkg "github.com/torstendittmann/gomposer/internal/platform"
)

// resetPlatformProbeForTest installs a fake PHP version (with a generic
// extension set) into the platform package's process cache. Idempotent.
func resetPlatformProbeForTest(t *testing.T, phpVersion string) {
	t.Helper()
	platformpkg.SetTestPlatform(t, phpVersion)
}
```

- [ ] **Step 2: Add `SetTestPlatform` helper**

Append to `internal/platform/platform.go`:

```go
// SetTestPlatform installs a fake Platform for the lifetime of a test.
// It pre-populates the process-level Probe cache with a Platform whose
// PHP version is `phpVersion` and whose extensions are a small standard
// set (`json`, `mbstring`). The previous cache is restored on test
// cleanup.
//
// This is intentionally not a generic public API; it exists so tests that
// span multiple packages (orchestrator, resolver) can avoid shelling out
// to a real `php` binary.
func SetTestPlatform(t interface{ Cleanup(func()) }, phpVersion string) {
	v, err := constraint.ParseVersion(phpVersion)
	if err != nil {
		panic("SetTestPlatform: " + err.Error())
	}
	saved := struct {
		once   sync.Once
		result *Platform
		err    error
	}{probeOnce, probeResult, probeErr}
	probeOnce = sync.Once{}
	probeResult = &Platform{
		PHPVersion: v,
		Extensions: map[string]constraint.Version{"json": {}, "mbstring": {}},
	}
	probeErr = nil
	probeOnce.Do(func() {})
	t.Cleanup(func() {
		probeOnce = saved.once
		probeResult = saved.result
		probeErr = saved.err
	})
}
```

- [ ] **Step 3: Verify failure**

Run: `go test ./internal/orchestrator/...`

Expected: build errors resolve once `SetTestPlatform` lands.

- [ ] **Step 4: Run tests**

Run: `go test ./...`

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/platform internal/orchestrator
git commit -m "test(orchestrator): integration tests for platform-req enforcement modes"
```

---

## Task 10: Documentation + final acceptance sweep

**Files:**
- None (verification-only)

Run the full test suite, build the binary, and confirm the documented behaviour against a real project on a machine that has PHP installed.

- [ ] **Step 1: Build**

```bash
go build -o gomposer ./cmd/gomposer
```

- [ ] **Step 2: Probe smoke**

```bash
php -r 'echo PHP_VERSION;'
./gomposer install --project /tmp/no-such-dir 2>&1 | head -n5
```

Expected: clear error mentioning the missing manifest. The probe itself was attempted.

- [ ] **Step 3: Mismatch smoke (manual)**

Pick a project whose top-level require is e.g. `php: ^7.4` and run on a PHP 8 machine:

```bash
./gomposer install --project ./fixtures/php74-only 2>&1 | grep "gomposer:"
```

Expected: warning lines naming the offending package and the failed req.

```bash
./gomposer install --project ./fixtures/php74-only --no-dev
```

Expected: non-zero exit; same warnings emitted before the error line.

```bash
./gomposer install --project ./fixtures/php74-only --no-dev --ignore-platform-req=php
```

Expected: exit 0.

- [ ] **Step 4: lib-* smoke**

If you can craft a fixture whose require map contains `lib-curl: '*'`:

```bash
./gomposer install --project ./fixtures/lib-curl-fixture
```

Expected: exactly one stderr line containing `ignoring lib-* platform requirements`.

- [ ] **Step 5: Final test sweep**

```bash
go test ./...
```

Expected: green.

- [ ] **Step 6: Commit (if any docs/text changed)**

```bash
git status
# Only commit if something is dirty.
```

---

## Stage 2 / Plan 3 acceptance check

This plan is done when:

- [ ] `internal/platform/Probe()` returns a structured `*Platform` populated from `php -r`; the result is cached for the lifetime of the process.
- [ ] Missing `php` produces a single error mentioning both `brew install php` and `apt install php-cli` and the `--ignore-platform` escape hatch.
- [ ] `Platform.Fingerprint()` is the canonical string used by every existing cache key, replacing the stage-1 `"php-unknown"`.
- [ ] The resolver receives the structured `Platform` via `Input.Platform` and drops candidate versions whose platform reqs are unsatisfiable. `lib-*` reqs never cause filtering.
- [ ] Default install warns to stderr (one line per package per failed req) and persists those warnings on `lock.File.Warnings` so cache-hit runs replay the same output.
- [ ] `--no-dev` upgrades any unsatisfied platform req to a fatal error.
- [ ] `--ignore-platform-req=<name>` (repeatable) and `--ignore-platform` skip the configured requirements; both produce zero warnings on the lockfile when applied.
- [ ] `--quiet` suppresses stderr output without affecting the persisted warnings.
- [ ] `lib-*` violations are coalesced into exactly one info-level message per run.
- [ ] `go test ./...` is green; `internal/platform`, `internal/resolver`, and `internal/orchestrator` all carry the new tests defined here.

If any item fails, fix forward in a follow-up commit on the same branch before declaring this plan done. Stage 2 plans 4+ (VCS support, classmap autoloader, scripts) build on the resolver's `Platform` plumbing introduced here; do not retrofit any of that work into this plan.
