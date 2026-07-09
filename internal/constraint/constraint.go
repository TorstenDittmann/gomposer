package constraint

import (
	"fmt"
	"strconv"
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
	// For branch-alias versions (Dev stability but with numeric Major set),
	// compare numerically against the term's version so that constraints like
	// "^1.0" (expanded to ">=1.0 <2.0") match "1.x-dev" (Major=1).
	cmp := v.Compare(t.v)
	if v.Stability == Dev && v.Major != 0 {
		cmp = compareNumeric(v, t.v)
	}
	switch t.op {
	case OpEq:
		return cmp == 0
	case OpNe:
		return cmp != 0
	case OpLt:
		return cmp < 0
	case OpLe:
		return cmp <= 0
	case OpGt:
		return cmp > 0
	case OpGe:
		return cmp >= 0
	}
	return false
}

// compareNumeric compares only the numeric Major.Minor.Patch portion,
// ignoring stability. Used for branch-alias dev version constraint matching.
func compareNumeric(a, b Version) int {
	if c := cmpInt(a.Major, b.Major); c != 0 {
		return c
	}
	if c := cmpInt(a.Minor, b.Minor); c != 0 {
		return c
	}
	return cmpInt(a.Patch, b.Patch)
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

// Parse parses a Composer-style constraint string.
//
// Accepted syntax:
//   - Exact versions:        "1.2.3", "v1.2.3", "1.0.0-RC1"
//   - Comparison operators:  ">=1.0", ">1", "<=2.0", "<2.0", "=1.0.0", "!=1.0.0"
//     Operator and version may be space-separated: ">= 1.0".
//   - Caret and tilde:       "^1.2", "~1.2.3"
//   - Wildcards:             "1.*", "1.x", "1.2.*"
//   - Hyphen ranges:         "1.0 - 2.0" (partial RHS bumps the next segment;
//     full RHS is inclusive)
//   - AND combinators:       whitespace OR "," between terms within a clause.
//   - OR combinators:        "|" or "||" between alternatives.
//   - Dev branches:          "dev-main", "dev-feature/foo", "dev-main#sha".
//   - Branch aliases:        "1.x-dev", "1.0.x-dev".
//   - Inline aliases:        "dev-foo as 1.0.0" (RHS recorded but ignored).
//   - Stability suffix:      "1.0.0@dev", "^2.0@beta" (parsed and recorded
//     on Version.StabilityFlag; resolver semantics are deferred to a later
//     stability-policy plan).
//
// The returned Constraint retains the original input on Constraint.Original
// for diagnostics.
func Parse(s string) (Constraint, error) {
	c := Constraint{Original: s}
	// Composer accepts both "|" and "||" as the OR separator, often without
	// surrounding whitespace ("^7.2|^8.0"). Normalize "||" to "|" first, then
	// split on "|".
	normalized := strings.ReplaceAll(s, "||", "|")
	// Inline branch-alias form: "<branch> as <version>" lets the user pin a
	// dev branch but treat it as the aliased version for transitive
	// constraint matching. Stage-2 honors the LHS (the actual branch to
	// resolve); the "as <version>" suffix is recorded but does not yet
	// participate in transitive matching.
	groups := strings.Split(normalized, "|")
	for _, g := range groups {
		g = stripInlineAlias(g)
		// Composer accepts ',' as a synonym for whitespace inside an AND clause.
		// Normalizing to space here keeps parseAndClause's strings.Fields-based
		// tokenization correct for both forms.
		g = strings.ReplaceAll(g, ",", " ")
		g = glueOperatorSpaces(g)
		clause, err := parseAndClause(g)
		if err != nil {
			return c, err
		}
		c.clauses = append(c.clauses, clause)
	}
	return c, nil
}

// stripInlineAlias removes the " as <alias>" tail from a constraint clause,
// returning just the actual constraint to resolve. It tolerates surrounding
// whitespace and is case-sensitive on the literal " as " token.
func stripInlineAlias(s string) string {
	trimmed := strings.TrimSpace(s)
	idx := strings.Index(trimmed, " as ")
	if idx < 0 {
		return trimmed
	}
	return strings.TrimSpace(trimmed[:idx])
}

// glueOperatorSpaces collapses one or more spaces between a leading comparison
// operator and the following version token, so that ">= 1.0" parses
// the same as ">=1.0". It only fuses when the lookahead starts with
// a digit, 'v', or 'V' to avoid mangling otherwise-valid clauses like
// ">=1.0 <2.0" (where "<" properly leads a fresh term).
func glueOperatorSpaces(s string) string {
	ops := []string{">=", "<=", "!=", ">", "<", "=", "^", "~"}
	out := strings.Builder{}
	out.Grow(len(s))
	i := 0
	for i < len(s) {
		matched := ""
		for _, op := range ops {
			if strings.HasPrefix(s[i:], op) {
				matched = op
				break
			}
		}
		if matched == "" {
			out.WriteByte(s[i])
			i++
			continue
		}
		out.WriteString(matched)
		j := i + len(matched)
		// Only glue when the next non-space byte is a version-leading char.
		k := j
		for k < len(s) && s[k] == ' ' {
			k++
		}
		if k > j && k < len(s) && (isDigit(s[k]) || s[k] == 'v' || s[k] == 'V') {
			i = k
			continue
		}
		i = j
	}
	return out.String()
}

func parseAndClause(s string) ([]term, error) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return nil, fmt.Errorf("constraint: empty clause in %q", s)
	}
	// Collapse hyphen-range triples ("X - Y") into a single synthetic term
	// before per-field parsing. The hyphen MUST be a standalone field —
	// "1.0-beta" is a pre-release, not a range.
	out := make([]term, 0, len(fields))
	i := 0
	for i < len(fields) {
		if i+2 < len(fields) && fields[i+1] == "-" {
			ts, err := hyphenRangeTerms(fields[i], fields[i+2])
			if err != nil {
				return nil, err
			}
			out = append(out, ts...)
			i += 3
			continue
		}
		t, err := parseTerm(fields[i])
		if err != nil {
			return nil, err
		}
		out = append(out, t...)
		i++
	}
	return out, nil
}

// hyphenRangeTerms expands a Composer hyphen range "lhs - rhs" per
// https://getcomposer.org/doc/articles/versions.md#range:
//   - LHS partial: missing segments filled with 0, used as inclusive lower bound.
//   - RHS fully qualified (three numeric segments): inclusive upper bound (OpLe).
//   - RHS partial: bump the next segment up, exclusive upper bound (OpLt).
//     "1.0 - 2"   => >=1.0.0 <3.0.0
//     "1.0 - 2.0" => >=1.0.0 <2.1.0
func hyphenRangeTerms(lhs, rhs string) ([]term, error) {
	lo, err := ParseVersion(lhs)
	if err != nil {
		return nil, fmt.Errorf("constraint: invalid hyphen-range lower %q: %w", lhs, err)
	}
	rhsParts := strings.Split(strings.TrimPrefix(rhs, "v"), ".")
	if len(rhsParts) >= 3 {
		hi, err := ParseVersion(rhs)
		if err != nil {
			return nil, fmt.Errorf("constraint: invalid hyphen-range upper %q: %w", rhs, err)
		}
		return []term{{OpGe, lo}, {OpLe, hi}}, nil
	}
	// Partial right side: bump the last present segment.
	nums := make([]int, len(rhsParts))
	for i, p := range rhsParts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("constraint: invalid hyphen-range upper %q: %w", rhs, err)
		}
		nums[i] = n
	}
	nums[len(nums)-1]++
	hi := versionFromInts(nums)
	return []term{{OpGe, lo}, {OpLt, hi}}, nil
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
	// Wildcard form: "1.*", "1.2.*", "1.x", "1.2.x" — expand to a half-open
	// range. Per Composer: "1.2.*" => ">=1.2.0 <1.3.0"; "1.*" => ">=1.0.0 <2.0.0".
	if isWildcardTerm(f) {
		return wildcardTerms(f)
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
//
// Both bounds carry Dev stability so pre-release versions of the boundary
// tags compare correctly: `2.0.0-RC1` satisfies `>=2.0.0` (since RC > Dev
// at the boundary), and `3.0.0-RC1` fails `<3.0.0` (since RC > Dev at the
// upper). This mirrors Composer's internal `VersionParser::normalize`,
// which treats `^X.Y.Z` as `>=X.Y.Z.0-dev, <NEXT.0.0.0-dev`. Without dev
// bounds, per-require stability flags like `@RC` would either miss valid
// candidates (lower) or over-admit next-major pre-releases (upper).
func caretTerms(s string) ([]term, error) {
	v, err := ParseVersion(s)
	if err != nil {
		return nil, err
	}
	// The lower bound reads as ">=X.Y.Z at dev-stability boundary" — any
	// stability of X.Y.Z or higher satisfies it.
	lower := v
	lower.Stability = Dev
	upper := nextCaretUpper(v)
	return []term{{OpGe, lower}, {OpLt, upper}}, nil
}

func nextCaretUpper(v Version) Version {
	if v.Major > 0 {
		return Version{Major: v.Major + 1, Stability: Dev}
	}
	return Version{Major: 0, Minor: v.Minor + 1, Stability: Dev}
}

// IsExplicitDev reports whether the constraint is a single literal
// "dev-<branch>" (optionally with a "#<ref>" pin). When true, the resolver
// may admit the matching dev version even if minimum-stability is stricter.
func (c Constraint) IsExplicitDev() bool {
	s := strings.TrimSpace(c.Original)
	if !strings.HasPrefix(s, "dev-") {
		return false
	}
	// Reject anything that introduces a second alternative or a combinator.
	for _, ch := range s {
		switch ch {
		case '|', ',', ' ':
			return false
		}
	}
	// Allow "dev-foo" or "dev-foo#sha". Branch must be non-empty.
	body := strings.TrimPrefix(s, "dev-")
	if body == "" {
		return false
	}
	if i := strings.IndexByte(body, '#'); i >= 0 {
		body = body[:i]
	}
	return body != ""
}

// StabilityFlag returns the "@<stability>" suffix parsed off the constraint,
// e.g. `^2.0@RC` → "RC". Returns "" when no flag was present. When multiple
// terms carry a flag (rare — typically only the first version literal does),
// the first non-empty one wins. This is what the resolver consults to
// override the global minimum-stability for a single require.
func (c Constraint) StabilityFlag() string {
	for _, cl := range c.clauses {
		for _, t := range cl {
			if t.v.StabilityFlag != "" {
				return t.v.StabilityFlag
			}
		}
	}
	return ""
}

// ExplicitDevBranch returns the branch name when IsExplicitDev is true,
// stripped of any "#sha" pin. Returns "" otherwise.
func (c Constraint) ExplicitDevBranch() string {
	if !c.IsExplicitDev() {
		return ""
	}
	body := strings.TrimPrefix(strings.TrimSpace(c.Original), "dev-")
	if i := strings.IndexByte(body, '#'); i >= 0 {
		body = body[:i]
	}
	return body
}

// tildeTerms expands "~X.Y.Z" to ">=X.Y.Z, <X.(Y+1).0" and
// "~X.Y" to ">=X.Y.0, <(X+1).0.0". Bounds use Dev stability for the same
// reason as caretTerms — see that function's doc.
func tildeTerms(s string) ([]term, error) {
	v, err := ParseVersion(s)
	if err != nil {
		return nil, err
	}
	lower := v
	lower.Stability = Dev
	upper := nextTildeUpper(s, v)
	return []term{{OpGe, lower}, {OpLt, upper}}, nil
}

func nextTildeUpper(s string, v Version) Version {
	base := s
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		base = s[:i]
	}
	dots := strings.Count(base, ".")
	if dots >= 2 {
		return Version{Major: v.Major, Minor: v.Minor + 1, Stability: Dev}
	}
	return Version{Major: v.Major + 1, Stability: Dev}
}

// isWildcardTerm reports whether f looks like a Composer wildcard constraint
// such as "1.*", "1.2.*", "1.x", or "1.2.x". The branch-alias form "1.x-dev"
// is NOT a wildcard — it's parsed as a regular dev version.
func isWildcardTerm(f string) bool {
	if strings.Contains(f, "-") {
		return false
	}
	parts := strings.Split(f, ".")
	for _, p := range parts {
		if p == "*" || p == "x" || p == "X" {
			return true
		}
	}
	return false
}

// wildcardTerms expands a wildcard like "1.2.*" or "1.x" into a half-open
// range >= base, < bumped-prefix.
func wildcardTerms(f string) ([]term, error) {
	parts := strings.Split(f, ".")
	// Find the first wildcard segment; everything before it is the prefix.
	wildAt := -1
	for i, p := range parts {
		if p == "*" || p == "x" || p == "X" {
			wildAt = i
			break
		}
	}
	if wildAt < 0 {
		return nil, fmt.Errorf("constraint: no wildcard in %q", f)
	}
	if wildAt == 0 {
		return []term{}, nil // "*"-only, vacuously true
	}
	prefix := make([]int, wildAt)
	for i, p := range parts[:wildAt] {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("constraint: invalid wildcard %q: %w", f, err)
		}
		prefix[i] = n
	}
	lower := versionFromInts(prefix)
	bumped := append([]int(nil), prefix...)
	bumped[len(bumped)-1]++
	upper := versionFromInts(bumped)
	return []term{{OpGe, lower}, {OpLt, upper}}, nil
}

func versionFromInts(parts []int) Version {
	v := Version{Stability: Stable}
	if len(parts) > 0 {
		v.Major = parts[0]
	}
	if len(parts) > 1 {
		v.Minor = parts[1]
	}
	if len(parts) > 2 {
		v.Patch = parts[2]
	}
	return v
}
