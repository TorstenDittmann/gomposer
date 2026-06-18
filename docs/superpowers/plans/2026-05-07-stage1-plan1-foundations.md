# Stage 1 / Plan 1: Foundations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up the Go module, CLI scaffold, `composer.json` parser, PHP-style version + constraint logic, and lockfile read/write. No network, no resolver, no install side effects yet — but every type future plans depend on is defined and unit-tested.

**Architecture:** Standard Go layout (`cmd/gomposer` + `internal/...`). Cobra for CLI. Parsers are pure (input → struct, no I/O). Constraint logic is custom because PHP semver has quirks no off-the-shelf library handles correctly (stability flags, `dev-*`, branch aliases, `as` aliasing).

**Tech Stack:** Go 1.22+, `github.com/spf13/cobra` (CLI), `github.com/charmbracelet/log` (logging), standard library `encoding/json`, `testing`.

---

## File structure

| Path | Responsibility |
|------|---------------|
| `go.mod`, `go.sum` | Module definition |
| `.gitignore` | Standard Go ignores + project-local cache dir |
| `cmd/gomposer/main.go` | Thin entrypoint: build context, hand off to `cli.Execute` |
| `internal/cli/root.go` | Root cobra command + global flags (`--verbose`, `--no-dev`) |
| `internal/cli/install.go` | `install` subcommand (stub: prints "not implemented") |
| `internal/cli/update.go` | `update` subcommand (stub: prints "not implemented") |
| `internal/manifest/manifest.go` | `Manifest` struct + `Parse([]byte)` |
| `internal/manifest/manifest_test.go` | Manifest parsing tests |
| `internal/manifest/autoload.go` | `Autoload` sub-struct (psr-4, psr-0, files, classmap) |
| `internal/constraint/version.go` | PHP-style `Version` type + `Parse`, `Compare`, `Equal` |
| `internal/constraint/version_test.go` | Version unit tests |
| `internal/constraint/constraint.go` | `Constraint` type + `Parse`, `Satisfies(Version)` |
| `internal/constraint/constraint_test.go` | Constraint unit tests |
| `internal/lock/lock.go` | `LockFile`, `LockedPackage` structs + JSON marshal/unmarshal |
| `internal/lock/lock_test.go` | Lockfile round-trip tests |

---

## Task 1: Module init + gitignore

**Files:**
- Create: `go.mod`
- Create: `.gitignore`

- [ ] **Step 1: Initialize Go module**

Run: `cd /Users/torstendittmann/Documents/skunk/gomposer && go mod init github.com/torstendittmann/gomposer`

Expected: creates `go.mod` with `module github.com/torstendittmann/gomposer` and a `go` directive.

- [ ] **Step 2: Write .gitignore**

Create `.gitignore`:

```gitignore
# Binaries
/gomposer
/dist/

# Go
*.test
*.out
/coverage.out

# Project caches
/.gomposer/
/vendor/

# Editor
.idea/
.vscode/
*.swp
.DS_Store
```

- [ ] **Step 3: Commit**

```bash
git add go.mod .gitignore
git commit -m "chore: initialize Go module and gitignore"
```

---

## Task 2: CLI scaffold (root + stub install/update)

**Files:**
- Create: `cmd/gomposer/main.go`
- Create: `internal/cli/root.go`
- Create: `internal/cli/install.go`
- Create: `internal/cli/update.go`
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add cobra dependency**

Run: `go get github.com/spf13/cobra@latest`

Expected: cobra added to `go.mod`, `go.sum` populated.

- [ ] **Step 2: Write `cmd/gomposer/main.go`**

```go
package main

import (
	"os"

	"github.com/torstendittmann/gomposer/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
```

- [ ] **Step 3: Write `internal/cli/root.go`**

```go
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	flagVerbose bool
	flagNoDev   bool
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "gomposer",
		Short:         "A fast Go-based PHP package manager",
		Long:          "gomposer installs PHP packages described in composer.json. It is a compatible consumer of composer.json but writes its own gomposer.lock.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().BoolVarP(&flagVerbose, "verbose", "v", false, "verbose output with timing breakdown")
	root.PersistentFlags().BoolVar(&flagNoDev, "no-dev", false, "skip require-dev dependencies; enforce platform requirements strictly")

	root.AddCommand(newInstallCmd())
	root.AddCommand(newUpdateCmd())
	return root
}

// Execute runs the CLI and returns an error on failure. Errors are printed
// to stderr by Execute itself, so callers should not double-print.
func Execute() error {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "gomposer: %v\n", err)
		return err
	}
	return nil
}
```

- [ ] **Step 4: Write `internal/cli/install.go` (stub)**

```go
package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

func newInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Resolve dependencies from the lockfile and materialize vendor/",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("install: not implemented yet")
		},
	}
}
```

- [ ] **Step 5: Write `internal/cli/update.go` (stub)**

```go
package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

func newUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Re-resolve dependencies and rewrite the lockfile",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("update: not implemented yet")
		},
	}
}
```

- [ ] **Step 6: Verify it builds and `--help` works**

Run: `go build ./cmd/gomposer && ./gomposer --help`

Expected output includes `Available Commands:` with `install` and `update` listed.

- [ ] **Step 7: Verify the stub error**

Run: `./gomposer install`

Expected: process exits non-zero with stderr containing `gomposer: install: not implemented yet`.

- [ ] **Step 8: Commit**

```bash
git add go.mod go.sum cmd/gomposer internal/cli
git commit -m "feat(cli): cobra scaffold with install and update stubs"
```

---

## Task 3: Manifest struct + JSON parsing — minimal

**Files:**
- Create: `internal/manifest/manifest.go`
- Create: `internal/manifest/manifest_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/manifest/manifest_test.go`:

```go
package manifest

import (
	"testing"
)

func TestParseMinimal(t *testing.T) {
	input := []byte(`{
		"name": "vendor/pkg",
		"type": "library"
	}`)
	m, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Name != "vendor/pkg" {
		t.Errorf("Name = %q, want vendor/pkg", m.Name)
	}
	if m.Type != "library" {
		t.Errorf("Type = %q, want library", m.Type)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/manifest/...`

Expected: build error referencing undefined `Parse` and `Manifest`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/manifest/manifest.go`:

```go
// Package manifest parses composer.json files into a structured form.
// Parsing is pure: no network, no filesystem side effects.
package manifest

import (
	"encoding/json"
	"fmt"
)

// Manifest is the parsed view of a composer.json file. Fields not yet
// supported by gomposer are omitted; unknown fields in the input are
// ignored silently for forward-compatibility with future Composer features.
type Manifest struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Parse decodes a composer.json byte slice. The error message includes the
// offset on JSON syntax errors so callers can surface useful diagnostics.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	return &m, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/manifest/...`

Expected: `ok  	github.com/torstendittmann/gomposer/internal/manifest`.

- [ ] **Step 5: Commit**

```bash
git add internal/manifest
git commit -m "feat(manifest): parse name and type from composer.json"
```

---

## Task 4: Manifest — require / require-dev

**Files:**
- Modify: `internal/manifest/manifest.go`
- Modify: `internal/manifest/manifest_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/manifest/manifest_test.go`:

```go
func TestParseRequires(t *testing.T) {
	input := []byte(`{
		"name": "vendor/pkg",
		"require": {
			"php": ">=8.1",
			"monolog/monolog": "^3.0"
		},
		"require-dev": {
			"phpunit/phpunit": "^10.0"
		}
	}`)
	m, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := m.Require["monolog/monolog"]; got != "^3.0" {
		t.Errorf("Require[monolog/monolog] = %q, want ^3.0", got)
	}
	if got := m.Require["php"]; got != ">=8.1" {
		t.Errorf("Require[php] = %q, want >=8.1", got)
	}
	if got := m.RequireDev["phpunit/phpunit"]; got != "^10.0" {
		t.Errorf("RequireDev[phpunit/phpunit] = %q, want ^10.0", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/manifest/...`

Expected: build error or `Require[...]` returning empty string.

- [ ] **Step 3: Extend Manifest**

Edit `internal/manifest/manifest.go`. Replace the `Manifest` struct with:

```go
type Manifest struct {
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Require    map[string]string `json:"require,omitempty"`
	RequireDev map[string]string `json:"require-dev,omitempty"`
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/manifest/...`

Expected: PASS for both `TestParseMinimal` and `TestParseRequires`.

- [ ] **Step 5: Commit**

```bash
git add internal/manifest
git commit -m "feat(manifest): parse require and require-dev maps"
```

---

## Task 5: Manifest — autoload sub-struct

**Files:**
- Create: `internal/manifest/autoload.go`
- Modify: `internal/manifest/manifest.go`
- Modify: `internal/manifest/manifest_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/manifest/manifest_test.go`:

```go
func TestParseAutoload(t *testing.T) {
	input := []byte(`{
		"name": "vendor/pkg",
		"autoload": {
			"psr-4": { "App\\": "src/" },
			"files": ["src/helpers.php"],
			"classmap": ["legacy/"]
		},
		"autoload-dev": {
			"psr-4": { "App\\Tests\\": "tests/" }
		}
	}`)
	m, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := m.Autoload.PSR4["App\\"]; got != "src/" {
		t.Errorf("PSR4[App\\] = %q, want src/", got)
	}
	if len(m.Autoload.Files) != 1 || m.Autoload.Files[0] != "src/helpers.php" {
		t.Errorf("Files = %v, want [src/helpers.php]", m.Autoload.Files)
	}
	if len(m.Autoload.Classmap) != 1 || m.Autoload.Classmap[0] != "legacy/" {
		t.Errorf("Classmap = %v, want [legacy/]", m.Autoload.Classmap)
	}
	if got := m.AutoloadDev.PSR4["App\\Tests\\"]; got != "tests/" {
		t.Errorf("AutoloadDev.PSR4[App\\Tests\\] = %q, want tests/", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/manifest/...`

Expected: build failure on `m.Autoload.PSR4`.

- [ ] **Step 3: Add Autoload type**

Create `internal/manifest/autoload.go`:

```go
package manifest

// Autoload mirrors the autoload / autoload-dev sections of composer.json.
// PSR0 is parsed but gomposer intentionally does not generate PSR-0
// loaders; consumers should warn when PSR0 is non-empty.
type Autoload struct {
	PSR4     map[string]string `json:"psr-4,omitempty"`
	PSR0     map[string]string `json:"psr-0,omitempty"`
	Files    []string          `json:"files,omitempty"`
	Classmap []string          `json:"classmap,omitempty"`
}
```

- [ ] **Step 4: Wire into Manifest**

Replace the `Manifest` struct in `internal/manifest/manifest.go`:

```go
type Manifest struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Require     map[string]string `json:"require,omitempty"`
	RequireDev  map[string]string `json:"require-dev,omitempty"`
	Autoload    Autoload          `json:"autoload,omitempty"`
	AutoloadDev Autoload          `json:"autoload-dev,omitempty"`
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/manifest/...`

Expected: all tests PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/manifest
git commit -m "feat(manifest): parse autoload and autoload-dev"
```

---

## Task 6: Manifest — stability fields

**Files:**
- Modify: `internal/manifest/manifest.go`
- Modify: `internal/manifest/manifest_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/manifest/manifest_test.go`:

```go
func TestParseStability(t *testing.T) {
	input := []byte(`{
		"name": "vendor/pkg",
		"minimum-stability": "beta",
		"prefer-stable": true
	}`)
	m, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.MinimumStability != "beta" {
		t.Errorf("MinimumStability = %q, want beta", m.MinimumStability)
	}
	if !m.PreferStable {
		t.Errorf("PreferStable = false, want true")
	}
}

func TestParseStabilityDefaults(t *testing.T) {
	input := []byte(`{ "name": "vendor/pkg" }`)
	m, _ := Parse(input)
	if m.MinimumStability != "" {
		t.Errorf("MinimumStability = %q, want \"\" (caller picks default)", m.MinimumStability)
	}
	if m.PreferStable {
		t.Errorf("PreferStable = true, want false")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/manifest/...`

Expected: build error on `m.MinimumStability`.

- [ ] **Step 3: Extend Manifest**

Replace the `Manifest` struct:

```go
type Manifest struct {
	Name             string            `json:"name"`
	Type             string            `json:"type"`
	Require          map[string]string `json:"require,omitempty"`
	RequireDev       map[string]string `json:"require-dev,omitempty"`
	Autoload         Autoload          `json:"autoload,omitempty"`
	AutoloadDev      Autoload          `json:"autoload-dev,omitempty"`
	MinimumStability string            `json:"minimum-stability,omitempty"`
	PreferStable     bool              `json:"prefer-stable,omitempty"`
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/manifest/...`

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/manifest
git commit -m "feat(manifest): parse minimum-stability and prefer-stable"
```

---

## Task 7: Version type — parse + compare

**Files:**
- Create: `internal/constraint/version.go`
- Create: `internal/constraint/version_test.go`

PHP semver versions look like `1.2.3`, `1.2.3-beta.1`, `1.2.3-RC1`, `dev-main`, `v1.2.3`. We normalize away the leading `v` and compare structurally.

- [ ] **Step 1: Write the failing test**

Create `internal/constraint/version_test.go`:

```go
package constraint

import "testing"

func TestParseVersionStable(t *testing.T) {
	v, err := ParseVersion("1.2.3")
	if err != nil {
		t.Fatalf("ParseVersion: %v", err)
	}
	if v.Major != 1 || v.Minor != 2 || v.Patch != 3 {
		t.Errorf("got %d.%d.%d, want 1.2.3", v.Major, v.Minor, v.Patch)
	}
	if v.Stability != Stable {
		t.Errorf("Stability = %v, want Stable", v.Stability)
	}
}

func TestParseVersionWithLeadingV(t *testing.T) {
	v, err := ParseVersion("v1.2.3")
	if err != nil {
		t.Fatalf("ParseVersion: %v", err)
	}
	if v.Major != 1 {
		t.Errorf("Major = %d, want 1", v.Major)
	}
}

func TestParseVersionPreRelease(t *testing.T) {
	cases := []struct {
		input string
		stab  Stability
	}{
		{"1.2.3-alpha", Alpha},
		{"1.2.3-alpha.1", Alpha},
		{"1.2.3-beta1", Beta},
		{"1.2.3-RC1", RC},
		{"1.2.3-rc.2", RC},
	}
	for _, tc := range cases {
		v, err := ParseVersion(tc.input)
		if err != nil {
			t.Errorf("%s: %v", tc.input, err)
			continue
		}
		if v.Stability != tc.stab {
			t.Errorf("%s: Stability = %v, want %v", tc.input, v.Stability, tc.stab)
		}
	}
}

func TestParseVersionDev(t *testing.T) {
	v, err := ParseVersion("dev-main")
	if err != nil {
		t.Fatalf("ParseVersion: %v", err)
	}
	if v.Stability != Dev {
		t.Errorf("Stability = %v, want Dev", v.Stability)
	}
	if v.Branch != "main" {
		t.Errorf("Branch = %q, want main", v.Branch)
	}
}

func TestVersionCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.4", -1},
		{"1.2.3", "1.2.3", 0},
		{"2.0.0", "1.99.99", 1},
		{"1.2.3-alpha", "1.2.3-beta", -1},
		{"1.2.3-RC1", "1.2.3", -1},
		{"1.2.3-RC1", "1.2.3-RC2", -1},
	}
	for _, tc := range cases {
		va, _ := ParseVersion(tc.a)
		vb, _ := ParseVersion(tc.b)
		got := va.Compare(vb)
		if got != tc.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/constraint/...`

Expected: build error referencing `ParseVersion`, `Stability`, etc.

- [ ] **Step 3: Implement Version**

Create `internal/constraint/version.go`:

```go
// Package constraint implements PHP-style version and constraint parsing.
// PHP's semver dialect adds stability flags (stable/RC/beta/alpha/dev),
// dev-* branch versions, and a leading-v tolerance that off-the-shelf
// semver libraries do not handle correctly.
package constraint

import (
	"fmt"
	"strconv"
	"strings"
)

// Stability ranks pre-release labels. Higher value = more stable.
type Stability int

const (
	Dev Stability = iota
	Alpha
	Beta
	RC
	Stable
)

func (s Stability) String() string {
	switch s {
	case Dev:
		return "dev"
	case Alpha:
		return "alpha"
	case Beta:
		return "beta"
	case RC:
		return "RC"
	case Stable:
		return "stable"
	}
	return "unknown"
}

// Version is a parsed PHP-style version. For dev branches Major/Minor/Patch
// are zero and Branch is set; for normal versions Branch is empty.
type Version struct {
	Major     int
	Minor     int
	Patch     int
	Stability Stability
	// PreNum is the numeric suffix of a pre-release (e.g. 2 in "1.0.0-RC2").
	// Zero when absent.
	PreNum int
	// Branch is set only for dev-* versions.
	Branch string
	// Original is the input string, retained for round-tripping.
	Original string
}

// ParseVersion parses a PHP-style version string.
func ParseVersion(s string) (Version, error) {
	v := Version{Original: s, Stability: Stable}

	// dev-<branch>
	if strings.HasPrefix(s, "dev-") {
		v.Stability = Dev
		v.Branch = strings.TrimPrefix(s, "dev-")
		if v.Branch == "" {
			return v, fmt.Errorf("constraint: empty branch in %q", s)
		}
		return v, nil
	}

	// Strip leading v.
	body := strings.TrimPrefix(s, "v")

	// Split on first '-' or '+' to separate base from pre-release/build.
	base, pre := body, ""
	if i := strings.IndexAny(body, "-+"); i >= 0 {
		base = body[:i]
		pre = body[i+1:]
	}

	parts := strings.Split(base, ".")
	if len(parts) < 1 || len(parts) > 4 {
		return v, fmt.Errorf("constraint: invalid version %q", s)
	}
	nums := []int{0, 0, 0, 0}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return v, fmt.Errorf("constraint: invalid version %q: %w", s, err)
		}
		nums[i] = n
	}
	v.Major, v.Minor, v.Patch = nums[0], nums[1], nums[2]

	if pre != "" {
		stab, num := classifyPre(pre)
		v.Stability = stab
		v.PreNum = num
	}
	return v, nil
}

// classifyPre maps a pre-release suffix to a stability and trailing number.
// Recognized prefixes: alpha / a, beta / b, rc, patch / p (treated as
// stable). Anything else is treated as dev.
func classifyPre(pre string) (Stability, int) {
	low := strings.ToLower(pre)
	// Strip leading separators within the pre tag.
	low = strings.TrimLeft(low, "-.")
	switch {
	case strings.HasPrefix(low, "rc"):
		return RC, leadingNum(low[2:])
	case strings.HasPrefix(low, "beta"):
		return Beta, leadingNum(low[4:])
	case strings.HasPrefix(low, "b") && (len(low) == 1 || isDigit(low[1])):
		return Beta, leadingNum(low[1:])
	case strings.HasPrefix(low, "alpha"):
		return Alpha, leadingNum(low[5:])
	case strings.HasPrefix(low, "a") && (len(low) == 1 || isDigit(low[1])):
		return Alpha, leadingNum(low[1:])
	case strings.HasPrefix(low, "patch") || strings.HasPrefix(low, "pl") ||
		strings.HasPrefix(low, "p"):
		return Stable, 0
	default:
		return Dev, 0
	}
}

func leadingNum(s string) int {
	s = strings.TrimLeft(s, ".-")
	end := 0
	for end < len(s) && isDigit(s[end]) {
		end++
	}
	if end == 0 {
		return 0
	}
	n, _ := strconv.Atoi(s[:end])
	return n
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// Compare returns -1 if v < other, 0 if equal, +1 if v > other.
// Dev versions sort below all numeric versions; among themselves they
// compare alphabetically by branch (deterministic, not semantically
// meaningful).
func (v Version) Compare(other Version) int {
	// Dev vs dev: alphabetical by branch.
	if v.Stability == Dev && other.Stability == Dev {
		return strings.Compare(v.Branch, other.Branch)
	}
	if v.Stability == Dev {
		return -1
	}
	if other.Stability == Dev {
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

// Equal reports semantic equality. Two versions are equal iff Compare returns 0.
func (v Version) Equal(other Version) bool { return v.Compare(other) == 0 }

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/constraint/...`

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/constraint
git commit -m "feat(constraint): PHP-style version parser and comparator"
```

---

## Task 8: Constraint type — exact / range operators

**Files:**
- Create: `internal/constraint/constraint.go`
- Create: `internal/constraint/constraint_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/constraint/constraint_test.go`:

```go
package constraint

import "testing"

func TestParseExact(t *testing.T) {
	c, err := Parse("1.2.3")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	v, _ := ParseVersion("1.2.3")
	if !c.Satisfies(v) {
		t.Errorf("1.2.3 should satisfy =1.2.3")
	}
	v2, _ := ParseVersion("1.2.4")
	if c.Satisfies(v2) {
		t.Errorf("1.2.4 should not satisfy =1.2.3")
	}
}

func TestParseRangeOps(t *testing.T) {
	cases := []struct {
		constraint, version string
		want                bool
	}{
		{">=1.0", "1.0.0", true},
		{">=1.0", "0.9.0", false},
		{">1.0", "1.0.0", false},
		{">1.0", "1.0.1", true},
		{"<2.0", "1.9.9", true},
		{"<2.0", "2.0.0", false},
		{"<=2.0", "2.0.0", true},
		{"!=1.0.0", "1.0.0", false},
		{"!=1.0.0", "1.0.1", true},
	}
	for _, tc := range cases {
		c, err := Parse(tc.constraint)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.constraint, err)
			continue
		}
		v, _ := ParseVersion(tc.version)
		if got := c.Satisfies(v); got != tc.want {
			t.Errorf("%s satisfies %s = %v, want %v", tc.version, tc.constraint, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/constraint/...`

Expected: build error on `Parse`, `Satisfies`.

- [ ] **Step 3: Implement Constraint with simple ops**

Create `internal/constraint/constraint.go`:

```go
package constraint

import (
	"fmt"
	"strings"
)

// Op is a single comparison operator applied to a Version.
type Op int

const (
	OpEq Op = iota
	OpNe
	OpLt
	OpLe
	OpGt
	OpGe
)

// term is one (op, version) clause.
type term struct {
	op Op
	v  Version
}

func (t term) satisfies(v Version) bool {
	c := v.Compare(t.v)
	switch t.op {
	case OpEq:
		return c == 0
	case OpNe:
		return c != 0
	case OpLt:
		return c < 0
	case OpLe:
		return c <= 0
	case OpGt:
		return c > 0
	case OpGe:
		return c >= 0
	}
	return false
}

// Constraint is a conjunction of disjunctions: ANDs of ORs.
//
//	"^1.0 || ^2.0"          -> [[^1.0], [^2.0]]
//	">=1.0 <2.0"            -> [[>=1.0, <2.0]]
//	">=1.0 <2.0 || ^3.0"    -> [[>=1.0, <2.0], [^3.0]]
type Constraint struct {
	// Outer slice: alternatives (OR). Inner slice: combined terms (AND).
	clauses [][]term
	// Original is the input string, retained for diagnostics.
	Original string
}

// Satisfies reports whether v satisfies the constraint.
func (c Constraint) Satisfies(v Version) bool {
	for _, clause := range c.clauses {
		if andSatisfies(clause, v) {
			return true
		}
	}
	return false
}

func andSatisfies(clause []term, v Version) bool {
	for _, t := range clause {
		if !t.satisfies(v) {
			return false
		}
	}
	return true
}

// Parse parses a constraint string. Supported syntax in this task:
//   - exact:        "1.2.3"
//   - operators:    ">=1.0", ">1.0", "<=2.0", "<2.0", "=1.0.0", "!=1.0.0"
//   - whitespace AND between terms
//   - "||" OR between groups
//
// ^ and ~ are added in the next task.
func Parse(s string) (Constraint, error) {
	c := Constraint{Original: s}
	groups := strings.Split(s, "||")
	for _, g := range groups {
		clause, err := parseAndClause(g)
		if err != nil {
			return c, err
		}
		c.clauses = append(c.clauses, clause)
	}
	return c, nil
}

func parseAndClause(s string) ([]term, error) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return nil, fmt.Errorf("constraint: empty clause in %q", s)
	}
	out := make([]term, 0, len(fields))
	for _, f := range fields {
		t, err := parseTerm(f)
		if err != nil {
			return nil, err
		}
		out = append(out, t...)
	}
	return out, nil
}

// parseTerm returns a slice because some operators (^, ~) expand to two
// terms (a lower bound AND an upper bound).
func parseTerm(f string) ([]term, error) {
	if len(f) == 0 {
		return nil, fmt.Errorf("constraint: empty term")
	}
	switch {
	case strings.HasPrefix(f, ">="):
		v, err := ParseVersion(f[2:])
		if err != nil {
			return nil, err
		}
		return []term{{OpGe, v}}, nil
	case strings.HasPrefix(f, "<="):
		v, err := ParseVersion(f[2:])
		if err != nil {
			return nil, err
		}
		return []term{{OpLe, v}}, nil
	case strings.HasPrefix(f, "!="):
		v, err := ParseVersion(f[2:])
		if err != nil {
			return nil, err
		}
		return []term{{OpNe, v}}, nil
	case strings.HasPrefix(f, ">"):
		v, err := ParseVersion(f[1:])
		if err != nil {
			return nil, err
		}
		return []term{{OpGt, v}}, nil
	case strings.HasPrefix(f, "<"):
		v, err := ParseVersion(f[1:])
		if err != nil {
			return nil, err
		}
		return []term{{OpLt, v}}, nil
	case strings.HasPrefix(f, "="):
		v, err := ParseVersion(f[1:])
		if err != nil {
			return nil, err
		}
		return []term{{OpEq, v}}, nil
	}
	// Bare version = exact.
	v, err := ParseVersion(f)
	if err != nil {
		return nil, err
	}
	return []term{{OpEq, v}}, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/constraint/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/constraint
git commit -m "feat(constraint): exact and range operators with AND/OR composition"
```

---

## Task 9: Constraint — caret and tilde

**Files:**
- Modify: `internal/constraint/constraint.go`
- Modify: `internal/constraint/constraint_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/constraint/constraint_test.go`:

```go
func TestParseCaret(t *testing.T) {
	// ^1.2.3 means >=1.2.3, <2.0.0
	// ^0.3.0 means >=0.3.0, <0.4.0   (PHP-Composer semantics for 0.x)
	cases := []struct {
		constraint, version string
		want                bool
	}{
		{"^1.2.3", "1.2.3", true},
		{"^1.2.3", "1.9.9", true},
		{"^1.2.3", "1.2.2", false},
		{"^1.2.3", "2.0.0", false},
		{"^0.3.0", "0.3.5", true},
		{"^0.3.0", "0.4.0", false},
	}
	for _, tc := range cases {
		c, err := Parse(tc.constraint)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.constraint, err)
			continue
		}
		v, _ := ParseVersion(tc.version)
		if got := c.Satisfies(v); got != tc.want {
			t.Errorf("%s in %s = %v, want %v", tc.version, tc.constraint, got, tc.want)
		}
	}
}

func TestParseTilde(t *testing.T) {
	// ~1.2.3 means >=1.2.3, <1.3.0
	// ~1.2   means >=1.2.0, <2.0.0   (one fewer dot = looser)
	cases := []struct {
		constraint, version string
		want                bool
	}{
		{"~1.2.3", "1.2.3", true},
		{"~1.2.3", "1.2.9", true},
		{"~1.2.3", "1.3.0", false},
		{"~1.2", "1.5.0", true},
		{"~1.2", "2.0.0", false},
	}
	for _, tc := range cases {
		c, err := Parse(tc.constraint)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.constraint, err)
			continue
		}
		v, _ := ParseVersion(tc.version)
		if got := c.Satisfies(v); got != tc.want {
			t.Errorf("%s in %s = %v, want %v", tc.version, tc.constraint, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/constraint/...`

Expected: failures because `^` and `~` aren't recognized yet.

- [ ] **Step 3: Extend `parseTerm`**

In `internal/constraint/constraint.go`, replace `parseTerm` with:

```go
func parseTerm(f string) ([]term, error) {
	if len(f) == 0 {
		return nil, fmt.Errorf("constraint: empty term")
	}
	switch {
	case strings.HasPrefix(f, "^"):
		return caretTerms(f[1:])
	case strings.HasPrefix(f, "~"):
		return tildeTerms(f[1:])
	case strings.HasPrefix(f, ">="):
		return singleOp(OpGe, f[2:])
	case strings.HasPrefix(f, "<="):
		return singleOp(OpLe, f[2:])
	case strings.HasPrefix(f, "!="):
		return singleOp(OpNe, f[2:])
	case strings.HasPrefix(f, ">"):
		return singleOp(OpGt, f[1:])
	case strings.HasPrefix(f, "<"):
		return singleOp(OpLt, f[1:])
	case strings.HasPrefix(f, "="):
		return singleOp(OpEq, f[1:])
	}
	return singleOp(OpEq, f)
}

func singleOp(op Op, s string) ([]term, error) {
	v, err := ParseVersion(s)
	if err != nil {
		return nil, err
	}
	return []term{{op, v}}, nil
}

// caretTerms expands "^X.Y.Z" to ">=X.Y.Z" AND "<NEXT.0.0" where NEXT = X+1
// for X>0; for X==0, the upper bound becomes "<0.(Y+1).0".
func caretTerms(s string) ([]term, error) {
	v, err := ParseVersion(s)
	if err != nil {
		return nil, err
	}
	upper := nextCaretUpper(v)
	return []term{{OpGe, v}, {OpLt, upper}}, nil
}

func nextCaretUpper(v Version) Version {
	if v.Major > 0 {
		return Version{Major: v.Major + 1, Stability: Stable}
	}
	return Version{Major: 0, Minor: v.Minor + 1, Stability: Stable}
}

// tildeTerms expands "~X.Y.Z" to ">=X.Y.Z, <X.(Y+1).0" and
// "~X.Y" to ">=X.Y.0, <(X+1).0.0".
func tildeTerms(s string) ([]term, error) {
	v, err := ParseVersion(s)
	if err != nil {
		return nil, err
	}
	upper := nextTildeUpper(s, v)
	return []term{{OpGe, v}, {OpLt, upper}}, nil
}

func nextTildeUpper(s string, v Version) Version {
	// Count dots in the segment before '-' to decide width.
	base := s
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		base = s[:i]
	}
	dots := strings.Count(base, ".")
	if dots >= 2 {
		// ~X.Y.Z  ->  <X.(Y+1).0
		return Version{Major: v.Major, Minor: v.Minor + 1, Stability: Stable}
	}
	// ~X.Y    ->  <(X+1).0.0
	return Version{Major: v.Major + 1, Stability: Stable}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/constraint/...`

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/constraint
git commit -m "feat(constraint): caret and tilde operators"
```

---

## Task 10: Lockfile schema + read/write

**Files:**
- Create: `internal/lock/lock.go`
- Create: `internal/lock/lock_test.go`

The lockfile schema mirrors the spec section "Lockfile format". The struct shapes here are referenced by every later plan; do not rename fields without updating the spec.

- [ ] **Step 1: Write the failing test**

Create `internal/lock/lock_test.go`:

```go
package lock

import (
	"bytes"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	in := &File{
		SchemaVersion:       1,
		Generator:           Generator{Name: "gomposer", Version: "0.0.0-test"},
		ManifestContentHash: "sha256:abc",
		PlatformFingerprint: "php-8.2.0;ext-mbstring",
		Stability:           Stability{MinimumStability: "stable", PreferStable: true},
		Packages: []Package{{
			Name:    "monolog/monolog",
			Version: "3.5.0",
			Source:  Source{Type: "git", URL: "https://github.com/Seldaek/monolog.git", Ref: "abc123"},
			Dist:    Dist{Type: "zip", URL: "https://api.github.com/repos/Seldaek/monolog/zipball/abc123", Sha256: "sha256:deadbeef"},
			Require: map[string]string{"php": ">=8.1"},
		}},
	}

	data, err := in.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	out, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if out.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", out.SchemaVersion)
	}
	if len(out.Packages) != 1 || out.Packages[0].Name != "monolog/monolog" {
		t.Errorf("Packages mismatch after round trip: %+v", out.Packages)
	}
	if out.Packages[0].Dist.Sha256 != "sha256:deadbeef" {
		t.Errorf("Dist.Sha256 lost in round trip")
	}

	// Encoding must be deterministic for diff-friendliness.
	again, _ := in.Encode()
	if !bytes.Equal(data, again) {
		t.Errorf("Encode is not deterministic")
	}
}

func TestDecodeRejectsUnknownSchema(t *testing.T) {
	data := []byte(`{"schemaVersion": 99}`)
	_, err := Decode(data)
	if err == nil {
		t.Errorf("Decode should reject schemaVersion=99")
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/lock/...`

Expected: build error on `File`, `Encode`, `Decode`.

- [ ] **Step 3: Implement lock**

Create `internal/lock/lock.go`:

```go
// Package lock handles gomposer.lock read and write.
//
// The on-disk format is documented in
// docs/superpowers/specs/2026-05-07-gomposer-design.md (section "Lockfile
// format"). Field renames here MUST be reflected in the spec.
package lock

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// SchemaVersion is the on-disk format version this build understands.
// Decode rejects files with a different SchemaVersion to force a clean
// rebuild rather than guessing at compatibility.
const SchemaVersion = 1

type File struct {
	SchemaVersion       int        `json:"schemaVersion"`
	Generator           Generator  `json:"generator"`
	ManifestContentHash string     `json:"manifestContentHash"`
	PlatformFingerprint string     `json:"platformFingerprint"`
	Stability           Stability  `json:"stability"`
	Packages            []Package  `json:"packages"`
	PackagesDev         []Package  `json:"packagesDev,omitempty"`
	Aliases             []Alias    `json:"aliases,omitempty"`
}

type Generator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Stability struct {
	MinimumStability string `json:"minimumStability"`
	PreferStable     bool   `json:"preferStable"`
}

type Package struct {
	Name     string            `json:"name"`
	Version  string            `json:"version"`
	Source   Source            `json:"source"`
	Dist     Dist              `json:"dist"`
	Require  map[string]string `json:"require,omitempty"`
	Autoload map[string]any    `json:"autoload,omitempty"`
	Suggest  map[string]string `json:"suggest,omitempty"`
}

type Source struct {
	Type string `json:"type"`
	URL  string `json:"url"`
	Ref  string `json:"ref"`
}

type Dist struct {
	Type   string `json:"type"`
	URL    string `json:"url"`
	Sha256 string `json:"sha256"`
}

type Alias struct {
	Package string `json:"package"`
	Version string `json:"version"`
	Alias   string `json:"alias"`
}

// Encode serializes the lockfile deterministically: 2-space indent, sorted
// map keys (Go's encoding/json sorts maps by default), trailing newline.
func (f *File) Encode() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(f); err != nil {
		return nil, fmt.Errorf("lock: encode: %w", err)
	}
	return buf.Bytes(), nil
}

// Decode parses a lockfile and rejects unknown schema versions.
func Decode(data []byte) (*File, error) {
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("lock: decode: %w", err)
	}
	if f.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("lock: unsupported schemaVersion %d (this build supports %d) — delete gomposer.lock to rebuild", f.SchemaVersion, SchemaVersion)
	}
	return &f, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/lock/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/lock
git commit -m "feat(lock): gomposer.lock schema + deterministic round-trip"
```

---

## Task 11: Whole-package smoke test

**Files:**
- Modify: `internal/cli/install.go` — wire a manifest read so the CLI does *something* end-to-end.
- Create: `internal/cli/install_test.go`

This task proves the layers compose. Install reads `composer.json`, parses it, and prints a one-line summary. Real install lives in later plans.

- [ ] **Step 1: Write failing test**

Create `internal/cli/install_test.go`:

```go
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallReadsManifest(t *testing.T) {
	dir := t.TempDir()
	manifest := []byte(`{"name": "vendor/pkg", "require": {"monolog/monolog": "^3.0"}}`)
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), manifest, 0o644); err != nil {
		t.Fatalf("write composer.json: %v", err)
	}

	var stdout bytes.Buffer
	root := newRootCmd()
	root.SetOut(&stdout)
	root.SetErr(&stdout)
	root.SetArgs([]string{"install", "--project", dir})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := stdout.String()
	if !bytes.Contains([]byte(got), []byte("vendor/pkg")) {
		t.Errorf("expected manifest summary in output, got %q", got)
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/cli/...`

Expected: failure — install currently returns "not implemented yet".

- [ ] **Step 3: Wire install to read the manifest**

Replace `internal/cli/install.go` with:

```go
package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/torstendittmann/gomposer/internal/manifest"
)

func newInstallCmd() *cobra.Command {
	var projectDir string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Resolve dependencies from the lockfile and materialize vendor/",
		RunE: func(cmd *cobra.Command, args []string) error {
			if projectDir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				projectDir = wd
			}
			path := filepath.Join(projectDir, "composer.json")
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("install: %w", err)
			}
			m, err := manifest.Parse(data)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "manifest %s with %d direct requires\n", m.Name, len(m.Require))
			return nil
		},
	}
	cmd.Flags().StringVar(&projectDir, "project", "", "project directory containing composer.json (defaults to cwd)")
	return cmd
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./...`

Expected: all PASS in `manifest`, `constraint`, `lock`, `cli`.

- [ ] **Step 5: Manual smoke**

Run:
```bash
go build ./cmd/gomposer
mkdir -p /tmp/cg-smoke && cd /tmp/cg-smoke
echo '{"name":"vendor/pkg","require":{"monolog/monolog":"^3.0"}}' > composer.json
$OLDPWD/gomposer install
```

Expected: `manifest vendor/pkg with 1 direct requires`.

- [ ] **Step 6: Commit**

```bash
cd $OLDPWD
git add internal/cli
git commit -m "feat(cli): install reads and parses composer.json"
```

---

## Foundations: acceptance check

After all tasks:

- `go test ./...` is green.
- `go build ./cmd/gomposer` produces a binary.
- `gomposer install` in a directory with a valid `composer.json` prints the manifest summary.
- `gomposer install` in a directory with no `composer.json` exits non-zero with a clear error.
- The types `manifest.Manifest`, `constraint.Version`, `constraint.Constraint`, `lock.File`, `lock.Package`, `lock.Source`, `lock.Dist` are stable enough for plans 2–6 to depend on.

If any of these fails, fix forward in a follow-up commit before declaring Plan 1 done.
