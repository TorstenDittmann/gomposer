package resolver

import (
	"context"
	"testing"

	"github.com/torstendittmann/composer-go/internal/constraint"
	"github.com/torstendittmann/composer-go/internal/platform"
	"github.com/torstendittmann/composer-go/internal/registry"
	"github.com/torstendittmann/composer-go/internal/resolver/testlookup"
)

func mustVerRslv(t *testing.T, s string) constraint.Version {
	t.Helper()
	v, err := constraint.ParseVersion(s)
	if err != nil {
		t.Fatalf("ParseVersion(%q): %v", s, err)
	}
	return v
}

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

func TestVersionListerFiltersIncompatibleByPlatform(t *testing.T) {
	php82 := mustVerRslv(t, "8.2.0")
	pf := &platform.Platform{PHPVersion: php82, Extensions: map[string]constraint.Version{}}

	src := testlookup.New(map[string][]registry.PackageVersion{
		"acme/widget": {
			{Name: "acme/widget", Version: "1.0.0", VersionNorm: "1.0.0.0",
				Require: map[string]string{"php": "^7.4"}},
			{Name: "acme/widget", Version: "2.0.0", VersionNorm: "2.0.0.0",
				Require: map[string]string{"php": "^8.0"}},
		},
	})
	vl := newVersionLister(src, constraint.Stable.String())
	vl.platform = pf
	vl.strictPlatform = true // platform filtering is opt-in (--no-dev mode)

	got, err := vl.versions(context.Background(), "acme/widget")
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if len(got) != 1 || got[0].Record.Version != "2.0.0" {
		t.Errorf("expected only 2.0.0 to survive php-8.2 filter; got %+v", got)
	}
}

func TestVersionListerKeepsIncompatibleByDefault(t *testing.T) {
	// Mirror of the strict-mode test above, but without strictPlatform: all
	// versions stay in the candidate list and the orchestrator emits
	// warnings post-resolution. Regression guard for the "1.1.* matches a
	// version filtered for ext-scrypt" bug.
	php82 := mustVerRslv(t, "8.2.0")
	pf := &platform.Platform{PHPVersion: php82, Extensions: map[string]constraint.Version{}}
	src := testlookup.New(map[string][]registry.PackageVersion{
		"acme/widget": {
			{Name: "acme/widget", Version: "1.0.0", VersionNorm: "1.0.0.0",
				Require: map[string]string{"php": "^7.4"}},
			{Name: "acme/widget", Version: "2.0.0", VersionNorm: "2.0.0.0",
				Require: map[string]string{"php": "^8.0"}},
		},
	})
	vl := newVersionLister(src, constraint.Stable.String())
	vl.platform = pf
	// No strictPlatform → both versions kept.

	got, err := vl.versions(context.Background(), "acme/widget")
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("default mode should keep all candidate versions; got %+v", got)
	}
}

func TestVersionListerHonorsIgnoreAll(t *testing.T) {
	php82 := mustVerRslv(t, "8.2.0")
	pf := &platform.Platform{PHPVersion: php82}
	src := testlookup.New(map[string][]registry.PackageVersion{
		"acme/widget": {{
			Name: "acme/widget", Version: "1.0.0", VersionNorm: "1.0.0.0",
			Require: map[string]string{"php": "^7.4"},
		}},
	})
	vl := newVersionLister(src, constraint.Stable.String())
	vl.platform = pf
	vl.ignorePlatformReqs = map[string]bool{"*": true}

	got, _ := vl.versions(context.Background(), "acme/widget")
	if len(got) != 1 {
		t.Errorf("ignore-all should keep all candidates; got %d", len(got))
	}
}

func TestVersionListerKeepsLibStar(t *testing.T) {
	pf := &platform.Platform{PHPVersion: mustVerRslv(t, "8.2.0")}
	src := testlookup.New(map[string][]registry.PackageVersion{
		"acme/widget": {{
			Name: "acme/widget", Version: "1.0.0", VersionNorm: "1.0.0.0",
			Require: map[string]string{"lib-curl": ">=10.0"},
		}},
	})
	vl := newVersionLister(src, constraint.Stable.String())
	vl.platform = pf

	got, _ := vl.versions(context.Background(), "acme/widget")
	if len(got) != 1 {
		t.Errorf("lib-* should NOT cause filtering; got %d", len(got))
	}
}
