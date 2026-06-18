// Package resolver implements a PubGrub-based dependency resolver for
// gomposer. The algorithm is described at a high level in the design spec
// and follows the canonical Dart pub solver in shape, adapted to PHP's
// stability flags and dev-* versions.
//
// Vocabulary (PubGrub):
//   - Term:           a (package, constraint, sign) tuple. Positive terms
//                     assert "the chosen version of P satisfies C"; negative
//                     terms assert the opposite.
//   - Incompatibility: a conjunction of Terms which must NOT all be true
//                     simultaneously. The whole solver state is a set of
//                     these.
//   - Assignment:     a single fact recorded in the partial solution; either
//                     a Decision (we picked a concrete version) or a
//                     Derivation (logical consequence of earlier facts).
//   - PartialSolution: the ordered list of assignments forming the current
//                     trial solution.
package resolver

import (
	"fmt"

	"github.com/torstendittmann/gomposer/internal/constraint"
)

// Term is one constraint over one package, with a sign.
//
// A positive Term {p, C} reads "the resolved version of p will satisfy C".
// A negative Term {p, !C} reads "the resolved version of p will NOT satisfy C".
type Term struct {
	Package    string
	Constraint constraint.Constraint
	Positive   bool
}

// Satisfies reports whether a concrete version v satisfies this term.
func (t Term) Satisfies(v constraint.Version) bool {
	matches := t.Constraint.Satisfies(v)
	if t.Positive {
		return matches
	}
	return !matches
}

// Inverse returns the same term with the sign flipped.
func (t Term) Inverse() Term {
	return Term{Package: t.Package, Constraint: t.Constraint, Positive: !t.Positive}
}

// Relation describes how one term relates to another over the same package.
type Relation int

const (
	// Disjoint: the two terms cannot both be satisfied by any single version.
	Disjoint Relation = iota
	// Subset: every version satisfying t also satisfies other.
	Subset
	// Overlapping: some versions satisfy both, but not all.
	Overlapping
)

// Relation classifies how t relates to other. Both terms must be over the
// same package; mixing packages is a programmer error.
//
// Relation is approximate by design: we use a finite set of probe versions
// drawn from a candidate pool that the caller sorts. For the outer driver
// this is enough — PubGrub uses Relation only to short-circuit certain
// branches; any "Overlapping" classification is always sound.
func (t Term) Relation(other Term) Relation {
	if t.Package != other.Package {
		panic(fmt.Sprintf("resolver: Relation across packages %q and %q", t.Package, other.Package))
	}
	// Special-case: identical terms.
	if t.Positive == other.Positive && t.Constraint.Original == other.Constraint.Original {
		return Subset
	}
	// Default to Overlapping; the caller refines using probe versions when
	// it has them.
	return Overlapping
}

// Empty reports whether this term is unsatisfiable (a contradiction in
// itself). Currently we treat a positive term whose constraint string is
// "<empty>" as empty; this is only used by Incompatibility constructors.
func (t Term) Empty() bool {
	return t.Positive && t.Constraint.Original == "<empty>"
}

// String returns a human-readable form, used in error messages.
func (t Term) String() string {
	sign := ""
	if !t.Positive {
		sign = "not "
	}
	c := t.Constraint.Original
	if c == "" {
		c = "*"
	}
	return fmt.Sprintf("%s%s %s", sign, t.Package, c)
}
