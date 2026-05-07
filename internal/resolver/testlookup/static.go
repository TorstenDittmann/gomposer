// Package testlookup provides a deterministic in-memory implementation of
// registry.SourceLookup for use in resolver unit tests. It is intentionally
// kept under internal/resolver/ so non-test code cannot accidentally depend
// on it.
package testlookup

import (
	"context"
	"fmt"
	"sort"

	"github.com/torstendittmann/composer-go/internal/registry"
)

// Static answers Lookup calls from an in-memory map. Versions for each
// package are returned in descending semver-like order — the resolver
// expects "newest first" semantics.
type Static struct {
	Packages map[string][]registry.PackageVersion
}

// New returns a Static seeded with the given packages.
func New(pkgs map[string][]registry.PackageVersion) *Static {
	s := &Static{Packages: map[string][]registry.PackageVersion{}}
	for k, v := range pkgs {
		cp := make([]registry.PackageVersion, len(v))
		copy(cp, v)
		// Stable order by version string descending.
		sort.SliceStable(cp, func(i, j int) bool { return cp[i].Version > cp[j].Version })
		s.Packages[k] = cp
	}
	return s
}

// Lookup implements registry.SourceLookup.
func (s *Static) Lookup(_ context.Context, name string) (*registry.PackageMetadata, error) {
	v, ok := s.Packages[name]
	if !ok {
		return nil, fmt.Errorf("%s: %w", name, registry.ErrPackageNotFound)
	}
	return &registry.PackageMetadata{Name: name, Versions: v}, nil
}

// Helpers for fluent test fixtures.

// Pkg constructs a PackageVersion with the given require map.
func Pkg(name, version string, requires map[string]string) registry.PackageVersion {
	return registry.PackageVersion{
		Name:        name,
		Version:     version,
		VersionNorm: version,
		Type:        "library",
		Require:     requires,
		Source:      registry.Source{Type: "git", URL: "git://" + name, Ref: "ref-" + version},
		Dist:        registry.Dist{Type: "zip", URL: "https://example.invalid/" + name + "-" + version + ".zip"},
	}
}
