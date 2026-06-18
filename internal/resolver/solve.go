package resolver

import (
	"context"
	"errors"
	"sort"

	"github.com/torstendittmann/gomposer/internal/constraint"
	"github.com/torstendittmann/gomposer/internal/lock"
	"github.com/torstendittmann/gomposer/internal/manifest"
	"github.com/torstendittmann/gomposer/internal/platform"
	"github.com/torstendittmann/gomposer/internal/registry"
)

// Input is everything Solve needs.
type Input struct {
	Manifest *manifest.Manifest
	Lock     *lock.File // optional; used for "stay close to lock" preference (stage 3)
	Source   registry.SourceLookup
	// IncludeDev includes require-dev when true. Default false matches
	// `composer install --no-dev` semantics.
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

	// StrictPlatform makes platform-req mismatches fatal at the resolver
	// stage: incompatible versions are dropped from the candidate list.
	// Default false matches the design-spec rule "warnings by default,
	// hard errors only under --no-dev". The orchestrator wires this from
	// opts.NoDev. The orchestrator still reports warnings post-resolution
	// either way; what differs is whether the bad versions are filtered
	// before the solver sees them.
	StrictPlatform bool

	// PlatformFingerprint is the string form of Platform, retained for
	// the resolution-result cache key.
	PlatformFingerprint string
	// MinimumStability defaults to "stable" if empty.
	MinimumStability string
}

// Solve runs the PubGrub loop and returns the resolution Result. On failure
// it returns either a *ConflictError (genuine package conflict) or another
// error (transient I/O, malformed input, etc.).
func Solve(ctx context.Context, in Input) (*Result, error) {
	if in.Manifest == nil {
		return nil, errors.New("resolver: Manifest is required")
	}
	if in.Source == nil {
		return nil, errors.New("resolver: Source is required")
	}
	minStab := in.MinimumStability
	if minStab == "" {
		minStab = in.Manifest.MinimumStability
	}
	vl := newVersionLister(in.Source, minStab)
	vl.platform = in.Platform
	vl.ignorePlatformReqs = in.IgnorePlatformReqs
	vl.strictPlatform = in.StrictPlatform

	// Stability flags: explicit "dev-<branch>" requires admit that branch
	// regardless of minStab (Composer-compatible behaviour).
	registerExplicitDev := func(reqs map[string]string) {
		for pkg, raw := range reqs {
			c, err := constraint.Parse(raw)
			if err != nil {
				continue
			}
			if branch := c.ExplicitDevBranch(); branch != "" {
				vl.AllowDevBranch(pkg, branch)
			}
		}
	}
	registerExplicitDev(in.Manifest.Require)
	if in.IncludeDev {
		registerExplicitDev(in.Manifest.RequireDev)
	}

	// 1. Seed root incompatibilities.
	ics := buildRootIncompatibilities(in.Manifest, in.IncludeDev)
	ps := NewPartialSolution()

	// Pre-derive each direct require from CauseRoot. We model the root as a
	// virtual "$root" decision which is implicitly at level 0.
	for _, ic := range ics {
		// Each root incompatibility has shape {root, dep-not-satisfying-c};
		// we propagate by deriving the inverse of the dep term.
		if len(ic.Terms) == 2 {
			ps.Derive(ic.Terms[1].Inverse(), ic)
		}
	}

	// 2. PubGrub main loop.
	const safetyLimit = 100000
	seedPkg := ""
	for step := 0; step < safetyLimit; step++ {
		// Propagate.
		if seedPkg == "" {
			seedPkg = pickAnyMentionedPackage(ics)
		}
		if seedPkg != "" {
			if conflict := propagate(ps, ics, seedPkg); conflict != nil {
				newIC, target := conflictResolution(ps, conflict)
				if newIC.IsFailure() {
					return nil, &ConflictError{Root: newIC}
				}
				ics = append(ics, newIC)
				ps.Backtrack(target)
				// Re-derive from newIC: it should be almost-satisfied after backtrack.
				seedPkg = ""
				if len(newIC.Terms) > 0 {
					seedPkg = newIC.Terms[0].Package
				}
				continue
			}
		}

		// Decide.
		st, decidedPkg, err := decide(ctx, ps, &ics, vl)
		if err != nil {
			return nil, err
		}
		switch st {
		case decideAllDone:
			return buildResult(ps, in.Manifest, in.IncludeDev, vl, ctx), nil
		case decideMade, decideNoVersions:
			seedPkg = decidedPkg
		}
	}
	return nil, errors.New("resolver: solver exceeded safety limit")
}

// buildRootIncompatibilities translates the manifest's direct requires into
// the seed set of incompatibilities.
//
// Encoding: for each direct require "name : constraint", we add
//
//	{ positive: $root@*, negative: name@constraint }   cause: CauseRoot
//
// The positive root term is permanently true (we never decide $root), so
// propagation will always derive `name@constraint` (positive) and seed the
// rest of the algorithm.
func buildRootIncompatibilities(m *manifest.Manifest, includeDev bool) []*Incompatibility {
	rootC, _ := constraint.Parse("*")
	rootTerm := Term{Package: "$root", Constraint: rootC, Positive: true}

	var ics []*Incompatibility
	add := func(name, raw string) {
		if isPlatformPackage(name) {
			return
		}
		c, err := constraint.Parse(raw)
		if err != nil {
			c = constraint.Constraint{}
		}
		ics = append(ics, NewIncompatibility(
			[]Term{rootTerm, {Package: name, Constraint: c, Positive: false}},
			CauseRoot{},
		))
	}
	// Sorting for determinism.
	for _, name := range sortedKeys(m.Require) {
		add(name, m.Require[name])
	}
	if includeDev {
		for _, name := range sortedKeys(m.RequireDev) {
			add(name, m.RequireDev[name])
		}
	}
	return ics
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// pickAnyMentionedPackage returns any non-$root package referenced by an
// incompatibility, used to seed the propagation worklist.
func pickAnyMentionedPackage(ics []*Incompatibility) string {
	for _, ic := range ics {
		for _, t := range ic.Terms {
			if t.Package != "$root" {
				return t.Package
			}
		}
	}
	return ""
}

// buildResult walks the partial solution's decisions and looks each up in
// the registry to attach the registry record (Source/Dist/Require) to the
// result. It also splits decisions into prod vs dev based on which require
// map originally pulled them in (transitive deps inherit prod-ness).
func buildResult(ps *PartialSolution, m *manifest.Manifest, includeDev bool, vl *versionLister, ctx context.Context) *Result {
	devRoots := map[string]bool{}
	if includeDev {
		for k := range m.RequireDev {
			devRoots[k] = true
		}
	}
	// Compute the set of dev-only packages: a transitive dep is dev-only if
	// every chain leading to it goes through a require-dev root.
	devOnly := computeDevOnlySet(ps, m, includeDev)

	res := &Result{}
	for _, a := range ps.Assignments {
		if !a.IsDecision || a.Package == "$root" {
			continue
		}
		// Look up the registry record for the chosen version.
		vs, err := vl.versions(ctx, a.Package)
		if err != nil {
			continue
		}
		var rec registry.PackageVersion
		for _, v := range vs {
			if v.Parsed.Equal(a.Version) {
				rec = v.Record
				break
			}
		}
		rp := ResolvedPackage{Name: a.Package, Version: a.Version, Record: rec}
		if devOnly[a.Package] {
			res.PackagesDev = append(res.PackagesDev, rp)
		} else {
			res.Packages = append(res.Packages, rp)
		}
	}
	// Sort by name for deterministic output.
	sort.SliceStable(res.Packages, func(i, j int) bool { return res.Packages[i].Name < res.Packages[j].Name })
	sort.SliceStable(res.PackagesDev, func(i, j int) bool { return res.PackagesDev[i].Name < res.PackagesDev[j].Name })
	return res
}

// computeDevOnlySet returns the names of packages that are reachable only
// via require-dev. Implementation: BFS from the prod roots; whatever isn't
// reached is dev-only (when IncludeDev is true).
func computeDevOnlySet(ps *PartialSolution, m *manifest.Manifest, includeDev bool) map[string]bool {
	if !includeDev {
		return map[string]bool{}
	}
	prodReached := map[string]bool{}
	work := []string{}
	for k := range m.Require {
		work = append(work, k)
		prodReached[k] = true
	}
	for len(work) > 0 {
		cur := work[0]
		work = work[1:]
		v, ok := ps.DecisionOf(cur)
		if !ok {
			continue
		}
		// Find this package's record-derived requires by looking at the
		// derivations that mention it.
		for _, a := range ps.Assignments {
			if a.IsDecision || a.Cause == nil {
				continue
			}
			cd, ok := a.Cause.Cause.(CauseDependency)
			if !ok {
				continue
			}
			if cd.Depender != cur {
				continue
			}
			if !prodReached[cd.Dependee] {
				prodReached[cd.Dependee] = true
				work = append(work, cd.Dependee)
			}
		}
		_ = v
	}
	devOnly := map[string]bool{}
	for _, a := range ps.Assignments {
		if !a.IsDecision || a.Package == "$root" {
			continue
		}
		if !prodReached[a.Package] {
			devOnly[a.Package] = true
		}
	}
	return devOnly
}
