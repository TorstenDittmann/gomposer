package resolver

import (
	"context"
	"sort"

	"github.com/torstendittmann/composer-go/internal/constraint"
	"github.com/torstendittmann/composer-go/internal/registry"
)

// listedVersion pairs the parsed version with the raw form Packagist
// published, plus the underlying registry record. The resolver uses Parsed
// for ordering and constraint matching, Raw when writing the lockfile.
type listedVersion struct {
	Raw    string
	Parsed constraint.Version
	Record registry.PackageVersion
}

// versionLister wraps a SourceLookup with parse+sort+filter+cache. One
// instance is created per Solve() call.
type versionLister struct {
	src      registry.SourceLookup
	minStab  constraint.Stability
	cache    map[string][]listedVersion
	notFound map[string]bool
}

func newVersionLister(src registry.SourceLookup, minStability string) *versionLister {
	return &versionLister{
		src:      src,
		minStab:  parseStabilityName(minStability),
		cache:    map[string][]listedVersion{},
		notFound: map[string]bool{},
	}
}

func parseStabilityName(s string) constraint.Stability {
	switch s {
	case "dev":
		return constraint.Dev
	case "alpha":
		return constraint.Alpha
	case "beta":
		return constraint.Beta
	case "RC", "rc":
		return constraint.RC
	case "", "stable":
		return constraint.Stable
	}
	return constraint.Stable
}

// versions returns candidate versions for a package, newest-first, filtered
// by minimum stability. ErrPackageNotFound is converted into a tagged miss
// (caller decides whether to surface as Cause: CauseUnknownPackage).
func (vl *versionLister) versions(ctx context.Context, pkg string) ([]listedVersion, error) {
	if v, ok := vl.cache[pkg]; ok {
		return v, nil
	}
	if vl.notFound[pkg] {
		return nil, errPackageNotFound{pkg: pkg}
	}
	md, err := vl.src.Lookup(ctx, pkg)
	if err != nil {
		if isNotFoundErr(err) {
			vl.notFound[pkg] = true
			return nil, errPackageNotFound{pkg: pkg}
		}
		return nil, err
	}
	out := make([]listedVersion, 0, len(md.Versions))
	for _, raw := range md.Versions {
		parsed, err := constraint.ParseVersion(raw.Version)
		if err != nil {
			// Skip unparseable; do not block the entire package on one bad row.
			continue
		}
		if parsed.Stability < vl.minStab {
			continue
		}
		out = append(out, listedVersion{Raw: raw.Version, Parsed: parsed, Record: raw})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Parsed.Compare(out[j].Parsed) > 0 })
	vl.cache[pkg] = out
	return out, nil
}

// isKnown reports whether a package is known to the source.
func (vl *versionLister) isKnown(ctx context.Context, pkg string) (bool, error) {
	if _, err := vl.versions(ctx, pkg); err != nil {
		if _, miss := err.(errPackageNotFound); miss {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// errPackageNotFound is the resolver-internal flavour; users see
// CauseUnknownPackage instead.
type errPackageNotFound struct{ pkg string }

func (e errPackageNotFound) Error() string { return "resolver: unknown package " + e.pkg }

func isNotFoundErr(err error) bool {
	for e := err; e != nil; {
		if _, ok := e.(interface{ Error() string }); ok {
			if e.Error() != "" && containsErrPackageNotFound(e) {
				return true
			}
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := e.(unwrapper)
		if !ok {
			break
		}
		e = u.Unwrap()
	}
	return false
}

func containsErrPackageNotFound(err error) bool {
	// Use errors.Is via a small import-free helper to keep this file's deps tight:
	// the registry package documents ErrPackageNotFound and uses %w wrapping.
	return err == registry.ErrPackageNotFound || err.Error() == registry.ErrPackageNotFound.Error() || strContains(err.Error(), "package not found")
}

func strContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
