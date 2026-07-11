package resolver

import (
	"context"
	"sort"

	"github.com/torstendittmann/gomposer/internal/constraint"
)

// decideResult is one of three outcomes from one call to decide().
type decideResult int

const (
	// decideMade: a new decision was added.
	decideMade decideResult = iota
	// decideAllDone: every positively-derived package already has a decision.
	decideAllDone
	// decideNoVersions: a package has no viable versions; an incompatibility
	// was added and the caller should re-run propagation.
	decideNoVersions
)

// decide picks a package and version to commit to. On success it appends a
// Decision and zero-or-more dependency incompatibilities to the caller's
// `*ics`. Returns the package name decided (for propagation seeding).
func decide(
	ctx context.Context,
	ps *PartialSolution,
	ics *[]*Incompatibility,
	vl *versionLister,
) (decideResult, string, error) {
	// 1. Collect undecided packages that have positive derivations.
	undecided := undecidedPackages(ps)
	if len(undecided) == 0 {
		return decideAllDone, "", nil
	}
	sort.Strings(undecided)

	// 2. For each, gather candidate versions intersected with active constraints.
	type cand struct {
		pkg      string
		versions []listedVersion
	}
	var cands []cand
	for _, pkg := range undecided {
		all, err := vl.versions(ctx, pkg)
		if err != nil {
			if _, miss := err.(errPackageNotFound); miss {
				// Unknown package — synthesize an incompatibility {positive: pkg active-constraint}
				// caused by CauseUnknownPackage so propagation will backtrack to whichever decision
				// pulled it in.
				ic := makeUnknownPackageIncompat(ps, pkg)
				*ics = append(*ics, ic)
				return decideNoVersions, pkg, nil
			}
			return decideAllDone, "", err
		}
		filtered := filterByActiveConstraints(ps, pkg, all)
		cands = append(cands, cand{pkg: pkg, versions: filtered})
	}

	// 3. Prefer the package with fewest candidates; tie-break by name (already sorted).
	sort.SliceStable(cands, func(i, j int) bool {
		if len(cands[i].versions) != len(cands[j].versions) {
			return len(cands[i].versions) < len(cands[j].versions)
		}
		return cands[i].pkg < cands[j].pkg
	})
	chosen := cands[0]

	// 4. No viable versions: register a "no versions" incompatibility.
	if len(chosen.versions) == 0 {
		ic := makeNoVersionsIncompat(ps, chosen.pkg)
		*ics = append(*ics, ic)
		return decideNoVersions, chosen.pkg, nil
	}

	// 5. Pick the highest version. The lister returns descending order so [0].
	pick := chosen.versions[0]

	// 6. Add dependency incompatibilities from this version's requires.
	for depName, depCons := range pick.Record.Require {
		if isPlatformPackage(depName) {
			// Stage 1 deferred: platform constraint enforcement (Plan 6).
			// We still record it as a known dependency so failures surface
			// in stage 2 once platform.go is wired in.
			continue
		}
		c, err := constraint.Parse(depCons)
		if err != nil {
			// Unparseable constraint string — model as "no versions" of this
			// dep so the user gets a clear error.
			c = constraint.Constraint{}
		}
		dep := *NewIncompatibility(
			[]Term{
				{Package: chosen.pkg, Constraint: exactConstraint(pick.Parsed), Positive: true},
				{Package: depName, Constraint: c, Positive: false},
			},
			CauseDependency{Depender: chosen.pkg, Dependee: depName},
		)
		*ics = append(*ics, &dep)
	}

	// 7. Commit the decision.
	ps.Decide(chosen.pkg, pick.Parsed)
	if vl.onDecided != nil {
		vl.onDecided(pick.Record.Name, pick.Record.Require)
	}
	return decideMade, chosen.pkg, nil
}

// undecidedPackages returns packages with at least one positive derivation
// and no current decision.
func undecidedPackages(ps *PartialSolution) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range ps.Assignments {
		if a.IsDecision {
			continue
		}
		if !a.Term.Positive {
			continue
		}
		if _, decided := ps.DecisionOf(a.Package); decided {
			continue
		}
		if seen[a.Package] {
			continue
		}
		seen[a.Package] = true
		out = append(out, a.Package)
	}
	return out
}

// filterByActiveConstraints keeps only versions of pkg that satisfy the
// conjunction of ALL derivations (positive and negative) currently in the
// partial solution. Positive derivations require the version to satisfy the
// constraint; negative derivations require the version to NOT satisfy the
// constraint.
func filterByActiveConstraints(ps *PartialSolution, pkg string, vs []listedVersion) []listedVersion {
	var allTerms []Term
	for _, a := range ps.Assignments {
		if a.IsDecision || a.Package != pkg {
			continue
		}
		allTerms = append(allTerms, a.Term)
	}
	out := make([]listedVersion, 0, len(vs))
nextV:
	for _, v := range vs {
		for _, t := range allTerms {
			// t.Satisfies checks the term (positive: must satisfy; negative: must not satisfy)
			if !t.Satisfies(v.Parsed) {
				continue nextV
			}
		}
		out = append(out, v)
	}
	return out
}

// makeNoVersionsIncompat produces an incompatibility stating that the
// positive-derivation conjunction for pkg cannot be satisfied. We model it
// as one positive term per active derivation; the conjunction "all of these
// hold for some chosen version of pkg" is what's impossible.
func makeNoVersionsIncompat(ps *PartialSolution, pkg string) *Incompatibility {
	terms := ps.PositiveDerivations(pkg)
	if len(terms) == 0 {
		// Defensive: if we somehow tried to decide a package with no
		// positive derivations, fall back to a universal positive term.
		all, _ := constraint.Parse(">=0.0.0")
		terms = []Term{{Package: pkg, Constraint: all, Positive: true}}
	}
	return NewIncompatibility(terms, CauseNoVersions{Package: pkg})
}

// makeUnknownPackageIncompat is like makeNoVersionsIncompat but tags the
// cause as CauseUnknownPackage for nicer error messages.
func makeUnknownPackageIncompat(ps *PartialSolution, pkg string) *Incompatibility {
	terms := ps.PositiveDerivations(pkg)
	if len(terms) == 0 {
		all, _ := constraint.Parse(">=0.0.0")
		terms = []Term{{Package: pkg, Constraint: all, Positive: true}}
	}
	return NewIncompatibility(terms, CauseUnknownPackage{Package: pkg})
}

// exactConstraint returns a Constraint that matches exactly v.
func exactConstraint(v constraint.Version) constraint.Constraint {
	c, _ := constraint.Parse("=" + v.Original)
	return c
}

// isPlatformPackage reports whether a name refers to a platform requirement
// (php, hhvm, ext-*) rather than a real package. Stage 1 just skips them.
func isPlatformPackage(name string) bool {
	if name == "php" || name == "php-64bit" || name == "php-ipv6" || name == "hhvm" {
		return true
	}
	if len(name) >= 4 && name[:4] == "ext-" {
		return true
	}
	if len(name) >= 4 && name[:4] == "lib-" {
		return true
	}
	return false
}
