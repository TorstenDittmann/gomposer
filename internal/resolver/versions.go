package resolver

import (
	"context"
	"sort"

	"github.com/torstendittmann/composer-go/internal/constraint"
	"github.com/torstendittmann/composer-go/internal/platform"
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
	allowDev map[string]map[string]bool // pkg -> branch set

	platform           *platform.Platform
	ignorePlatformReqs map[string]bool
}

func newVersionLister(src registry.SourceLookup, minStability string) *versionLister {
	return &versionLister{
		src:      src,
		minStab:  parseStabilityName(minStability),
		cache:    map[string][]listedVersion{},
		notFound: map[string]bool{},
		allowDev: map[string]map[string]bool{},
	}
}

// AllowDevBranch records that pkg's dev-<branch> version is admissible
// regardless of minimum stability. Call once per explicit-dev require before
// the first versions() call for that package.
func (vl *versionLister) AllowDevBranch(pkg, branch string) {
	m, ok := vl.allowDev[pkg]
	if !ok {
		m = map[string]bool{}
		vl.allowDev[pkg] = m
	}
	m[branch] = true
}

func (vl *versionLister) devAdmitted(pkg, branch string) bool {
	return vl.allowDev[pkg][branch]
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
	raw := make([]listedVersion, 0, len(md.Versions))
	for _, rv := range md.Versions {
		parsed, err := constraint.ParseVersion(rv.Version)
		if err != nil {
			// Skip unparseable; do not block the entire package on one bad row.
			continue
		}
		if parsed.Stability < vl.minStab {
			if !(parsed.Stability == constraint.Dev && vl.devAdmitted(pkg, parsed.Branch)) {
				continue
			}
		}
		raw = append(raw, listedVersion{Raw: rv.Version, Parsed: parsed, Record: rv})
	}
	sort.Slice(raw, func(i, j int) bool { return raw[i].Parsed.Compare(raw[j].Parsed) > 0 })
	// Platform filtering: drop versions whose platform reqs are not satisfied.
	// If ALL versions are incompatible, keep them all so that the resolver can
	// still pick the best one — the orchestrator will emit warnings afterward.
	filtered := make([]listedVersion, 0, len(raw))
	for _, v := range raw {
		if vl.versionInstallable(v.Record) {
			filtered = append(filtered, v)
		}
	}
	out := raw
	if len(filtered) > 0 {
		out = filtered
	}
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
