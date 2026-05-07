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

// parseTerm returns a slice because some operators (^, ~) expand to two
// terms (a lower bound AND an upper bound).
func parseTerm(f string) ([]term, error) {
	if len(f) == 0 {
		return nil, fmt.Errorf("constraint: empty term")
	}
	switch {
	case strings.HasPrefix(f, ">="):
		v, err := ParseVersion(f[2:])
		if err != nil {
			return nil, err
		}
		return []term{{OpGe, v}}, nil
	case strings.HasPrefix(f, "<="):
		v, err := ParseVersion(f[2:])
		if err != nil {
			return nil, err
		}
		return []term{{OpLe, v}}, nil
	case strings.HasPrefix(f, "!="):
		v, err := ParseVersion(f[2:])
		if err != nil {
			return nil, err
		}
		return []term{{OpNe, v}}, nil
	case strings.HasPrefix(f, ">"):
		v, err := ParseVersion(f[1:])
		if err != nil {
			return nil, err
		}
		return []term{{OpGt, v}}, nil
	case strings.HasPrefix(f, "<"):
		v, err := ParseVersion(f[1:])
		if err != nil {
			return nil, err
		}
		return []term{{OpLt, v}}, nil
	case strings.HasPrefix(f, "="):
		v, err := ParseVersion(f[1:])
		if err != nil {
			return nil, err
		}
		return []term{{OpEq, v}}, nil
	}
	v, err := ParseVersion(f)
	if err != nil {
		return nil, err
	}
	return []term{{OpEq, v}}, nil
}
