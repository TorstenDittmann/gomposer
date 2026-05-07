package resolver

import (
	"context"
	"testing"

	"github.com/torstendittmann/composer-go/internal/registry"
	"github.com/torstendittmann/composer-go/internal/resolver/testlookup"
)

func TestVersionListerSortedDesc(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {
			testlookup.Pkg("a/a", "1.0.0", nil),
			testlookup.Pkg("a/a", "1.2.0", nil),
			testlookup.Pkg("a/a", "1.1.0", nil),
		},
	})
	vl := newVersionLister(src, "stable")
	got, err := vl.versions(context.Background(), "a/a")
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].Raw != "1.2.0" || got[1].Raw != "1.1.0" || got[2].Raw != "1.0.0" {
		t.Errorf("order: %v", []string{got[0].Raw, got[1].Raw, got[2].Raw})
	}
}

func TestVersionListerFiltersByMinStability(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {
			testlookup.Pkg("a/a", "2.0.0-alpha", nil),
			testlookup.Pkg("a/a", "1.9.0", nil),
		},
	})
	vl := newVersionLister(src, "stable")
	got, _ := vl.versions(context.Background(), "a/a")
	if len(got) != 1 || got[0].Raw != "1.9.0" {
		t.Errorf("expected only 1.9.0, got %+v", got)
	}

	vl2 := newVersionLister(src, "alpha")
	got2, _ := vl2.versions(context.Background(), "a/a")
	if len(got2) != 2 {
		t.Errorf("expected 2 with min=alpha, got %d", len(got2))
	}
}

func TestVersionListerCachesWithinSolve(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {testlookup.Pkg("a/a", "1.0.0", nil)},
	})
	vl := newVersionLister(src, "stable")
	a, _ := vl.versions(context.Background(), "a/a")
	b, _ := vl.versions(context.Background(), "a/a")
	if &a[0] != &b[0] && a[0].Raw != b[0].Raw {
		t.Errorf("results should be stable across calls")
	}
}
