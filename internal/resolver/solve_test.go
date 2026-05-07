package resolver

import (
	"context"
	"errors"
	"testing"

	"github.com/torstendittmann/composer-go/internal/manifest"
	"github.com/torstendittmann/composer-go/internal/registry"
	"github.com/torstendittmann/composer-go/internal/resolver/testlookup"
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
