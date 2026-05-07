package resolver

import (
	"github.com/torstendittmann/composer-go/internal/constraint"
)

// Assignment is one step in the partial solution. Either a concrete decision
// or a derivation. Decisions advance the decision level; derivations don't.
type Assignment struct {
	IsDecision    bool
	Package       string
	Version       constraint.Version // valid when IsDecision
	Term          Term               // valid when !IsDecision
	Cause         *Incompatibility   // valid when !IsDecision (the incompatibility that forced this derivation)
	DecisionLevel int
}

// PartialSolution tracks the chain of assignments made so far.
type PartialSolution struct {
	Assignments []Assignment
	level       int
	// decisions maps package name -> latest decided version, for O(1) lookup.
	decisions map[string]constraint.Version
}

// NewPartialSolution returns an empty partial solution.
func NewPartialSolution() *PartialSolution {
	return &PartialSolution{decisions: map[string]constraint.Version{}}
}

// DecisionLevel returns the current decision level (count of Decide calls
// since the last Backtrack).
func (ps *PartialSolution) DecisionLevel() int { return ps.level }

// Decide records a concrete version pick for a package. This advances the
// decision level by one.
func (ps *PartialSolution) Decide(pkg string, v constraint.Version) {
	ps.level++
	ps.Assignments = append(ps.Assignments, Assignment{
		IsDecision:    true,
		Package:       pkg,
		Version:       v,
		DecisionLevel: ps.level,
	})
	ps.decisions[pkg] = v
}

// Derive records a Term that has been logically forced by `cause` together
// with the existing partial solution. Derivations sit at the current
// decision level and do not advance it.
func (ps *PartialSolution) Derive(t Term, cause *Incompatibility) {
	ps.Assignments = append(ps.Assignments, Assignment{
		IsDecision:    false,
		Package:       t.Package,
		Term:          t,
		Cause:         cause,
		DecisionLevel: ps.level,
	})
}

// DecisionOf returns the version decided for a package, if any.
func (ps *PartialSolution) DecisionOf(pkg string) (constraint.Version, bool) {
	v, ok := ps.decisions[pkg]
	return v, ok
}

// Backtrack truncates the partial solution to the longest prefix whose
// assignments all have DecisionLevel <= target.
func (ps *PartialSolution) Backtrack(target int) {
	keep := 0
	for i, a := range ps.Assignments {
		if a.DecisionLevel <= target {
			keep = i + 1
		} else {
			break
		}
	}
	ps.Assignments = ps.Assignments[:keep]
	ps.level = target
	// Rebuild decisions map.
	ps.decisions = map[string]constraint.Version{}
	for _, a := range ps.Assignments {
		if a.IsDecision {
			ps.decisions[a.Package] = a.Version
		}
	}
}

// PositiveDerivations returns all positive derivations and decisions for a
// package: the conjunction of constraints currently believed about pkg.
func (ps *PartialSolution) PositiveDerivations(pkg string) []Term {
	var out []Term
	for _, a := range ps.Assignments {
		if a.Package != pkg {
			continue
		}
		if a.IsDecision {
			// Decisions can be modeled as a positive term "= version".
			c, _ := constraint.Parse("=" + a.Version.Original)
			out = append(out, Term{Package: pkg, Constraint: c, Positive: true})
		} else if a.Term.Positive {
			out = append(out, a.Term)
		}
	}
	return out
}

// RelationOf classifies how a term relates to the current partial solution
// for the same package. Returns:
//
//   - Subset:      every version that could still be chosen for the package
//                  satisfies the term
//   - Disjoint:    no version that could still be chosen satisfies the term
//   - Overlapping: in between
//
// The implementation uses the most recent decision (if any) as a sound oracle:
// when a decision is present, the package is fully determined and the answer
// is exact. Otherwise we conservatively return Overlapping.
func (ps *PartialSolution) RelationOf(t Term) Relation {
	if v, ok := ps.decisions[t.Package]; ok {
		if t.Satisfies(v) {
			return Subset
		}
		return Disjoint
	}
	return Overlapping
}

// Satisfies reports whether the partial solution as a whole satisfies the
// term. For the decided case this is exact; for the undecided case we use
// PositiveDerivations as an approximation: the term is satisfied if every
// recorded positive derivation's constraint is implied by t.Constraint AND
// t.Positive.
//
// PubGrub only requires this to return true on cases that are clearly
// implied; over-conservative "false" answers just delay propagation a step.
func (ps *PartialSolution) Satisfies(t Term) bool {
	if v, ok := ps.decisions[t.Package]; ok {
		return t.Satisfies(v)
	}
	return false
}
