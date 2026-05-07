package constraint

import (
	"fmt"
	"strings"
)

// Op is a single comparison operator applied to a Version.
type Op int

const (
	OpEq Op = iota
	OpNe
	OpLt
	OpLe
	OpGt
	OpGe
)

// term is one (op, version) clause.
type term struct {
	op Op
	v  Version
}

func (t term) satisfies(v Version) bool {
	c := v.Compare(t.v)
	switch t.op {
	case OpEq:
		return c == 0
	case OpNe:
		return c != 0
	case OpLt:
		return c < 0
	case OpLe:
		return c <= 0
	case OpGt:
		return c > 0
	case OpGe:
		return c >= 0
	}
	return false
}

// Constraint is a conjunction of disjunctions: ANDs of ORs.
//
//	"^1.0 || ^2.0"          -> [[^1.0], [^2.0]]
//	">=1.0 <2.0"            -> [[>=1.0, <2.0]]
//	">=1.0 <2.0 || ^3.0"    -> [[>=1.0, <2.0], [^3.0]]
type Constraint struct {
	// Outer slice: alternatives (OR). Inner slice: combined terms (AND).
	clauses [][]term
	// Original is the input string, retained for diagnostics.
	Original string
}

// Satisfies reports whether v satisfies the constraint.
func (c Constraint) Satisfies(v Version) bool {
	for _, clause := range c.clauses {
		if andSatisfies(clause, v) {
			return true
		}
	}
	return false
}

func andSatisfies(clause []term, v Version) bool {
	for _, t := range clause {
		if !t.satisfies(v) {
			return false
		}
	}
	return true
}

// Parse parses a constraint string.
func Parse(s string) (Constraint, error) {
	c := Constraint{Original: s}
	groups := strings.Split(s, "||")
	for _, g := range groups {
		clause, err := parseAndClause(g)
		if err != nil {
			return c, err
		}
		c.clauses = append(c.clauses, clause)
	}
	return c, nil
}

func parseAndClause(s string) ([]term, error) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return nil, fmt.Errorf("constraint: empty clause in %q", s)
	}
	out := make([]term, 0, len(fields))
	for _, f := range fields {
		t, err := parseTerm(f)
		if err != nil {
			return nil, err
		}
		out = append(out, t...)
	}
	return out, nil
}

func parseTerm(f string) ([]term, error) {
	if len(f) == 0 {
		return nil, fmt.Errorf("constraint: empty term")
	}
	// "*" is the universal constraint — every version satisfies it. We model
	// this as an empty AND-clause (vacuously true).
	if f == "*" {
		return []term{}, nil
	}
	switch {
	case strings.HasPrefix(f, "^"):
		return caretTerms(f[1:])
	case strings.HasPrefix(f, "~"):
		return tildeTerms(f[1:])
	case strings.HasPrefix(f, ">="):
		return singleOp(OpGe, f[2:])
	case strings.HasPrefix(f, "<="):
		return singleOp(OpLe, f[2:])
	case strings.HasPrefix(f, "!="):
		return singleOp(OpNe, f[2:])
	case strings.HasPrefix(f, ">"):
		return singleOp(OpGt, f[1:])
	case strings.HasPrefix(f, "<"):
		return singleOp(OpLt, f[1:])
	case strings.HasPrefix(f, "="):
		return singleOp(OpEq, f[1:])
	}
	return singleOp(OpEq, f)
}

func singleOp(op Op, s string) ([]term, error) {
	v, err := ParseVersion(s)
	if err != nil {
		return nil, err
	}
	return []term{{op, v}}, nil
}

// caretTerms expands "^X.Y.Z" to ">=X.Y.Z" AND "<NEXT.0.0" where NEXT = X+1
// for X>0; for X==0, the upper bound becomes "<0.(Y+1).0".
func caretTerms(s string) ([]term, error) {
	v, err := ParseVersion(s)
	if err != nil {
		return nil, err
	}
	upper := nextCaretUpper(v)
	return []term{{OpGe, v}, {OpLt, upper}}, nil
}

func nextCaretUpper(v Version) Version {
	if v.Major > 0 {
		return Version{Major: v.Major + 1, Stability: Stable}
	}
	return Version{Major: 0, Minor: v.Minor + 1, Stability: Stable}
}

// tildeTerms expands "~X.Y.Z" to ">=X.Y.Z, <X.(Y+1).0" and
// "~X.Y" to ">=X.Y.0, <(X+1).0.0".
func tildeTerms(s string) ([]term, error) {
	v, err := ParseVersion(s)
	if err != nil {
		return nil, err
	}
	upper := nextTildeUpper(s, v)
	return []term{{OpGe, v}, {OpLt, upper}}, nil
}

func nextTildeUpper(s string, v Version) Version {
	base := s
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		base = s[:i]
	}
	dots := strings.Count(base, ".")
	if dots >= 2 {
		return Version{Major: v.Major, Minor: v.Minor + 1, Stability: Stable}
	}
	return Version{Major: v.Major + 1, Stability: Stable}
}
