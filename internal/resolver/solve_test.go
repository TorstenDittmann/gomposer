package resolver

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/torstendittmann/gomposer/internal/manifest"
	"github.com/torstendittmann/gomposer/internal/registry"
	"github.com/torstendittmann/gomposer/internal/resolver/testlookup"
)

func TestSolveSimpleNoDeps(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {testlookup.Pkg("a/a", "1.0.0", nil)},
	})
	m := &manifest.Manifest{
		Name:    "user/app",
		Require: map[string]string{"a/a": "^1.0"},
	}
	res, err := Solve(context.Background(), Input{Manifest: m, Source: src})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if len(res.Packages) != 1 {
		t.Fatalf("Packages = %d, want 1", len(res.Packages))
	}
	if res.Packages[0].Name != "a/a" || res.Packages[0].Version.Major != 1 {
		t.Errorf("got %+v", res.Packages[0])
	}
}

func TestSolveTransitiveDeps(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {testlookup.Pkg("a/a", "1.0.0", map[string]string{"b/b": "^1.0"})},
		"b/b": {testlookup.Pkg("b/b", "1.2.3", nil)},
	})
	m := &manifest.Manifest{
		Name:    "user/app",
		Require: map[string]string{"a/a": "^1.0"},
	}
	res, err := Solve(context.Background(), Input{Manifest: m, Source: src})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if len(res.Packages) != 2 {
		t.Fatalf("Packages = %d, want 2 (a/a and b/b), got %v", len(res.Packages), res.Packages)
	}
	names := map[string]bool{}
	for _, p := range res.Packages {
		names[p.Name] = true
	}
	if !names["a/a"] || !names["b/b"] {
		t.Errorf("expected both a/a and b/b, got %v", names)
	}
}

func TestSolvePicksHighestSatisfying(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {
			testlookup.Pkg("a/a", "1.0.0", nil),
			testlookup.Pkg("a/a", "1.5.0", nil),
			testlookup.Pkg("a/a", "2.0.0", nil),
		},
	})
	m := &manifest.Manifest{
		Name:    "user/app",
		Require: map[string]string{"a/a": "^1.0"},
	}
	res, err := Solve(context.Background(), Input{Manifest: m, Source: src})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if len(res.Packages) != 1 || res.Packages[0].Version.Minor != 5 {
		t.Errorf("expected a/a=1.5.0, got %+v", res.Packages)
	}
}

func TestSolveConflictReturnsConflictError(t *testing.T) {
	// a/a 1.0.0 -> requires b/b ^1.0
	// c/c 1.0.0 -> requires b/b ^2.0
	// no version of b/b satisfies both; and we require both a/a and c/c.
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {testlookup.Pkg("a/a", "1.0.0", map[string]string{"b/b": "^1.0"})},
		"c/c": {testlookup.Pkg("c/c", "1.0.0", map[string]string{"b/b": "^2.0"})},
		"b/b": {
			testlookup.Pkg("b/b", "1.0.0", nil),
			testlookup.Pkg("b/b", "2.0.0", nil),
		},
	})
	m := &manifest.Manifest{
		Name: "user/app",
		Require: map[string]string{
			"a/a": "^1.0",
			"c/c": "^1.0",
		},
	}
	_, err := Solve(context.Background(), Input{Manifest: m, Source: src})
	if err == nil {
		t.Fatal("expected ConflictError, got nil")
	}
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("err type = %T, want *ConflictError; err=%v", err, err)
	}
}

func TestSolveSkipsPlatformRequires(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {testlookup.Pkg("a/a", "1.0.0", map[string]string{"php": ">=8.1", "ext-mbstring": "*"})},
	})
	m := &manifest.Manifest{
		Name:    "user/app",
		Require: map[string]string{"a/a": "^1.0"},
	}
	res, err := Solve(context.Background(), Input{Manifest: m, Source: src})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if len(res.Packages) != 1 {
		t.Errorf("Packages = %d, want 1 (php and ext-* must not be resolved)", len(res.Packages))
	}
}

func TestSolveDevRequiresIncluded(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {testlookup.Pkg("a/a", "1.0.0", nil)},
		"d/d": {testlookup.Pkg("d/d", "1.0.0", nil)},
	})
	m := &manifest.Manifest{
		Name:       "user/app",
		Require:    map[string]string{"a/a": "^1.0"},
		RequireDev: map[string]string{"d/d": "^1.0"},
	}
	res, err := Solve(context.Background(), Input{Manifest: m, Source: src, IncludeDev: true})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if len(res.Packages) != 1 || len(res.PackagesDev) != 1 {
		t.Errorf("Packages=%d, PackagesDev=%d", len(res.Packages), len(res.PackagesDev))
	}
}

func TestSolveDevRequiresExcluded(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {testlookup.Pkg("a/a", "1.0.0", nil)},
		"d/d": {testlookup.Pkg("d/d", "1.0.0", nil)},
	})
	m := &manifest.Manifest{
		Name:       "user/app",
		Require:    map[string]string{"a/a": "^1.0"},
		RequireDev: map[string]string{"d/d": "^1.0"},
	}
	res, err := Solve(context.Background(), Input{Manifest: m, Source: src, IncludeDev: false})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if len(res.Packages) != 1 || len(res.PackagesDev) != 0 {
		t.Errorf("Packages=%d, PackagesDev=%d (dev should be excluded)", len(res.Packages), len(res.PackagesDev))
	}
}

// TestSolveAdmitsPerRequireStabilityFlag mirrors a real Appwrite manifest
// entry: "utopia-php/http": "^2.0@RC". The package only ships RC-stability
// tags; the manifest's minimum-stability is stable. Composer honors the
// per-require @RC as an override for that one package. Our resolver must
// too — the RC version has to be selected without loosening minStab
// globally.
func TestSolveAdmitsPerRequireStabilityFlag(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"utopia-php/http": {
			testlookup.Pkg("utopia-php/http", "2.0.0-RC1", nil),
		},
	})
	m := &manifest.Manifest{
		Name:             "user/app",
		Require:          map[string]string{"utopia-php/http": "^2.0@RC"},
		MinimumStability: "stable",
	}
	res, err := Solve(context.Background(), Input{Manifest: m, Source: src})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if len(res.Packages) != 1 {
		t.Fatalf("Packages = %d, want 1", len(res.Packages))
	}
	if res.Packages[0].Record.Version != "2.0.0-RC1" {
		t.Errorf("picked %q, want 2.0.0-RC1", res.Packages[0].Record.Version)
	}
}

// TestSolvePerRequireStabilityDoesNotLeakToOtherPackages guards the scoping
// rule: @RC on one require must NOT admit RC candidates for every other
// package. A stable package with an RC candidate available should still get
// the stable one.
func TestSolvePerRequireStabilityDoesNotLeakToOtherPackages(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"utopia-php/http": {
			testlookup.Pkg("utopia-php/http", "2.0.0-RC1", nil),
		},
		"acme/lib": {
			testlookup.Pkg("acme/lib", "2.0.0-RC1", nil),
			testlookup.Pkg("acme/lib", "1.9.0", nil),
		},
	})
	m := &manifest.Manifest{
		Name: "user/app",
		Require: map[string]string{
			"utopia-php/http": "^2.0@RC",
			"acme/lib":        "^1.0",
		},
		MinimumStability: "stable",
	}
	res, err := Solve(context.Background(), Input{Manifest: m, Source: src})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	var httpVer, libVer string
	for _, p := range res.Packages {
		switch p.Name {
		case "utopia-php/http":
			httpVer = p.Record.Version
		case "acme/lib":
			libVer = p.Record.Version
		}
	}
	if httpVer != "2.0.0-RC1" {
		t.Errorf("utopia-php/http = %q, want 2.0.0-RC1", httpVer)
	}
	if libVer != "1.9.0" {
		t.Errorf("acme/lib = %q, want 1.9.0 (RC must not leak to unflagged require)", libVer)
	}
}

func TestSolveConflictMessageReadsAsDerivation(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/x": {testlookup.Pkg("a/x", "1.0.0", map[string]string{"b/y": "^1.0"})},
		"b/y": {
			testlookup.Pkg("b/y", "1.0.0", nil),
			testlookup.Pkg("b/y", "2.0.0", nil),
		},
	})
	m := &manifest.Manifest{
		Require: map[string]string{
			"a/x": "^1.0",
			"b/y": "^2.0",
		},
	}
	_, err := Solve(context.Background(), Input{Manifest: m, Source: src})
	if err == nil {
		t.Fatalf("expected ConflictError, got nil")
	}
	ce := new(ConflictError)
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ConflictError, got %T: %v", err, err)
	}
	msg := ce.Error()
	for _, want := range []string{
		"resolver: conflict",
		"derivation:",
		"a/x",
		"b/y",
		"version solving failed",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("rendered message missing %q.\n--- full message ---\n%s", want, msg)
		}
	}
	// No "TODO(conflict)" placeholders should leak.
	if strings.Contains(msg, "TODO(conflict)") {
		t.Errorf("placeholder leaked into rendered output:\n%s", msg)
	}
}

func TestSolveDeterministic(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {
			testlookup.Pkg("a/a", "1.0.0", nil),
			testlookup.Pkg("a/a", "1.1.0", nil),
			testlookup.Pkg("a/a", "1.2.0", nil),
		},
		"b/b": {testlookup.Pkg("b/b", "1.0.0", nil)},
	})
	m := &manifest.Manifest{
		Name: "user/app",
		Require: map[string]string{
			"a/a": "^1.0",
			"b/b": "^1.0",
		},
	}
	r1, _ := Solve(context.Background(), Input{Manifest: m, Source: src})
	r2, _ := Solve(context.Background(), Input{Manifest: m, Source: src})
	if len(r1.Packages) != len(r2.Packages) {
		t.Fatalf("non-deterministic length")
	}
	for i := range r1.Packages {
		if r1.Packages[i].Name != r2.Packages[i].Name ||
			r1.Packages[i].Version.Original != r2.Packages[i].Version.Original {
			t.Errorf("non-deterministic at %d: %+v vs %+v", i, r1.Packages[i], r2.Packages[i])
		}
	}
}

func TestSolveFiresOnVersionDecidedForEveryCommit(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {testlookup.Pkg("a/a", "1.0.0", map[string]string{"b/b": "^1.0"})},
		"b/b": {testlookup.Pkg("b/b", "1.0.0", nil)},
	})
	m := &manifest.Manifest{
		Name:    "user/app",
		Require: map[string]string{"a/a": "^1.0"},
	}

	var mu sync.Mutex
	seen := map[string]map[string]string{}
	res, err := Solve(context.Background(), Input{
		Manifest: m,
		Source:   src,
		OnVersionDecided: func(name string, requires map[string]string) {
			mu.Lock()
			defer mu.Unlock()
			// Copy the map — the resolver may reuse the underlying map.
			cp := make(map[string]string, len(requires))
			for k, v := range requires {
				cp[k] = v
			}
			seen[name] = cp
		},
	})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if len(res.Packages) != 2 {
		t.Fatalf("Packages = %d, want 2", len(res.Packages))
	}

	mu.Lock()
	defer mu.Unlock()
	if _, ok := seen["a/a"]; !ok {
		t.Errorf("callback never fired for a/a; seen = %v", seen)
	}
	if _, ok := seen["b/b"]; !ok {
		t.Errorf("callback never fired for b/b; seen = %v", seen)
	}
	if len(seen["a/a"]) != 1 || seen["a/a"]["b/b"] != "^1.0" {
		t.Errorf("callback for a/a saw requires = %v, want {b/b: ^1.0}", seen["a/a"])
	}
}

func TestSolveTolerateNilOnVersionDecided(t *testing.T) {
	// Sanity: nil callback is the existing behavior; must not panic.
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {testlookup.Pkg("a/a", "1.0.0", nil)},
	})
	m := &manifest.Manifest{Name: "user/app", Require: map[string]string{"a/a": "^1.0"}}
	if _, err := Solve(context.Background(), Input{Manifest: m, Source: src}); err != nil {
		t.Fatalf("Solve with nil callback: %v", err)
	}
}
