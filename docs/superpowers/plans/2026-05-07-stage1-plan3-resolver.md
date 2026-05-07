# Stage 1 / Plan 3: PubGrub Resolver + Resolution-Result Cache Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement a PubGrub-based dependency resolver that, given a parsed `manifest.Manifest`, an optional existing `lock.File`, a `registry.SourceLookup`, and a platform fingerprint string, returns a deterministic list of `lock.Package` entries (with `Source`/`Dist` left for the orchestrator to fill). Wrap the resolver in a content-addressed resolution-result cache (cache layer 3) so unchanged inputs bypass the solver entirely.

**Architecture:** PubGrub decomposed into independently-testable pieces, then a top-level `Solver` that wires them together. The solver is single-threaded by design — concurrency lives in the metadata fetcher one layer below us, accessed via a small `versionLister` helper that wraps `SourceLookup` with caching of intermediate parses. The resolver returns its own `Result` type (decoupled from `lock.File`); a tiny adapter layer converts `Result` to `[]lock.Package`. The resolution cache wraps the whole thing with a single key `(manifestContentHash, lockContentHash, platformFingerprint)`.

**Tech Stack:** Go 1.22+, standard library only (`encoding/json`, `encoding/gob`, `crypto/sha256`, `sort`). No new external deps.

**Depends on:** Plan 1 (Foundations) — `manifest.Manifest`, `constraint.Version`, `constraint.Constraint`, `lock.File`, `lock.Package`. Plan 2 (Metadata) — `registry.SourceLookup`, `registry.PackageMetadata`, `registry.PackageVersion`.

---

## File structure

| Path | Responsibility |
|------|---------------|
| `internal/resolver/term.go` | `Term` (positive/negative constraint over a package); intersection, relation |
| `internal/resolver/term_test.go` | Term unit tests |
| `internal/resolver/incompatibility.go` | `Incompatibility` (conjunction of Terms forbidden to all hold) + cause chain |
| `internal/resolver/incompatibility_test.go` | Incompatibility construction + relation tests |
| `internal/resolver/assignment.go` | `Assignment` (decision or derivation) + `PartialSolution` |
| `internal/resolver/assignment_test.go` | PartialSolution decision/derivation/backtrack tests |
| `internal/resolver/versions.go` | `versionLister`: wraps `SourceLookup`, sorts versions, applies stability filters |
| `internal/resolver/versions_test.go` | versionLister tests against an in-memory fake `SourceLookup` |
| `internal/resolver/propagate.go` | Unit propagation loop (`propagate`) + `almostSatisfies` check |
| `internal/resolver/conflict.go` | Conflict resolution (root-cause incompatibility computation) |
| `internal/resolver/decide.go` | Decision-making (pick next package + version) |
| `internal/resolver/solve.go` | Top-level `Solve()` driver and `Result` type |
| `internal/resolver/solve_test.go` | End-to-end resolver tests with canned `SourceLookup` |
| `internal/resolver/error.go` | `ConflictError` with derivation chain, `ErrNoVersions`, etc. |
| `internal/resolver/adapter.go` | `ToLockPackages(Result) []lock.Package` |
| `internal/resolver/adapter_test.go` | Adapter test |
| `internal/resolver/cache.go` | Resolution-result cache (cache layer 3) — `CachedSolver` wrapper |
| `internal/resolver/cache_test.go` | Cache hit/miss tests |
| `internal/resolver/testlookup/static.go` | In-memory `registry.SourceLookup` for tests (test helper, not exported beyond `_test`) |

---

## Task 1: Term type — positive and negative constraints

The PubGrub `Term` is a wrapper around a package + a constraint, with a `positive` flag. A positive Term `(P, C)` says "P satisfies C"; a negative Term `(P, !C)` says "P does NOT satisfy C". We never store the package version directly — only the constraint. The empty constraint and the universal constraint are distinguished states because they drive PubGrub's relation machinery.

**Files:**
- Create: `internal/resolver/term.go`
- Create: `internal/resolver/term_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/resolver/term_test.go`:

```go
package resolver

import (
	"testing"

	"github.com/torstendittmann/composer-go/internal/constraint"
)

func mustC(t *testing.T, s string) constraint.Constraint {
	t.Helper()
	c, err := constraint.Parse(s)
	if err != nil {
		t.Fatalf("constraint.Parse(%q): %v", s, err)
	}
	return c
}

func mustV(t *testing.T, s string) constraint.Version {
	t.Helper()
	v, err := constraint.ParseVersion(s)
	if err != nil {
		t.Fatalf("constraint.ParseVersion(%q): %v", s, err)
	}
	return v
}

func TestTermSatisfies(t *testing.T) {
	pos := Term{Package: "p", Constraint: mustC(t, "^1.0"), Positive: true}
	if !pos.Satisfies(mustV(t, "1.2.3")) {
		t.Errorf("positive ^1.0 should satisfy 1.2.3")
	}
	if pos.Satisfies(mustV(t, "2.0.0")) {
		t.Errorf("positive ^1.0 should not satisfy 2.0.0")
	}

	neg := Term{Package: "p", Constraint: mustC(t, "^1.0"), Positive: false}
	if neg.Satisfies(mustV(t, "1.2.3")) {
		t.Errorf("negative !^1.0 should not satisfy 1.2.3")
	}
	if !neg.Satisfies(mustV(t, "2.0.0")) {
		t.Errorf("negative !^1.0 should satisfy 2.0.0")
	}
}

func TestTermInverse(t *testing.T) {
	a := Term{Package: "p", Constraint: mustC(t, "^1.0"), Positive: true}
	b := a.Inverse()
	if b.Positive {
		t.Errorf("inverse of positive should be negative")
	}
	if b.Package != "p" {
		t.Errorf("inverse changed package")
	}
}

func TestTermSamePackagePanicsOnMismatch(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Errorf("expected panic on mixing packages")
		}
	}()
	a := Term{Package: "p", Constraint: mustC(t, "^1.0"), Positive: true}
	b := Term{Package: "q", Constraint: mustC(t, "^1.0"), Positive: true}
	_ = a.Relation(b)
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/resolver/...`

Expected: build error referencing `Term`, `Satisfies`, `Inverse`, `Relation`.

- [ ] **Step 3: Implement Term**

Create `internal/resolver/term.go`:

```go
// Package resolver implements a PubGrub-based dependency resolver for
// composer-go. The algorithm is described at a high level in the design spec
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

	"github.com/torstendittmann/composer-go/internal/constraint"
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
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/resolver/...`

Expected: PASS for `TestTermSatisfies`, `TestTermInverse`, `TestTermSamePackagePanicsOnMismatch`.

- [ ] **Step 5: Commit**

```bash
git add internal/resolver
git commit -m "feat(resolver): Term type with positive/negative constraints"
```

---

## Task 2: Incompatibility type — conjunction of Terms with cause chain

An `Incompatibility` is a set of `Term`s the conjunction of which must be false. The "root" incompatibility derives from the manifest's direct requires; "derived" incompatibilities arise from conflict resolution and carry pointers to the two parents that produced them. We need this cause chain so the error renderer can show users why resolution failed.

**Files:**
- Create: `internal/resolver/incompatibility.go`
- Create: `internal/resolver/incompatibility_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/resolver/incompatibility_test.go`:

```go
package resolver

import "testing"

func TestIncompatibilityRootHasNoCause(t *testing.T) {
	ic := NewIncompatibility(
		[]Term{{Package: "root", Constraint: mustC(t, "1.0.0"), Positive: true}},
		CauseRoot{},
	)
	if ic.Cause == nil {
		t.Errorf("Cause should not be nil — should be CauseRoot{}")
	}
	if _, ok := ic.Cause.(CauseRoot); !ok {
		t.Errorf("Cause = %T, want CauseRoot", ic.Cause)
	}
}

func TestIncompatibilityDependencyHasParents(t *testing.T) {
	ic := NewIncompatibility(
		[]Term{
			{Package: "a", Constraint: mustC(t, "^1.0"), Positive: true},
			{Package: "b", Constraint: mustC(t, "^1.0"), Positive: false},
		},
		CauseDependency{Depender: "a", Dependee: "b"},
	)
	c, ok := ic.Cause.(CauseDependency)
	if !ok {
		t.Fatalf("Cause type = %T", ic.Cause)
	}
	if c.Depender != "a" || c.Dependee != "b" {
		t.Errorf("CauseDependency = %+v", c)
	}
}

func TestIncompatibilityString(t *testing.T) {
	ic := NewIncompatibility(
		[]Term{
			{Package: "a", Constraint: mustC(t, "^1.0"), Positive: true},
			{Package: "b", Constraint: mustC(t, "^1.0"), Positive: false},
		},
		CauseDependency{Depender: "a", Dependee: "b"},
	)
	got := ic.String()
	if got == "" {
		t.Errorf("String() returned empty")
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/resolver/...`

Expected: build error.

- [ ] **Step 3: Implement Incompatibility**

Create `internal/resolver/incompatibility.go`:

```go
package resolver

import (
	"strings"
)

// Incompatibility is a set of Terms whose conjunction must NOT hold for any
// final solution. It also carries the reason it was created, which lets the
// error renderer reconstruct a derivation chain.
type Incompatibility struct {
	Terms []Term
	Cause Cause
}

// Cause explains why an Incompatibility exists. Concrete causes:
//
//   - CauseRoot:           the user's manifest directly requires this
//   - CauseDependency:     package P-at-version X depends on Q at constraint C
//   - CauseNoVersions:     no published versions of P satisfy C
//   - CauseUnknownPackage: P is not known to the source
//   - CauseConflict:       derived during conflict resolution from two parents
type Cause interface {
	causeMarker()
}

type CauseRoot struct{}

func (CauseRoot) causeMarker() {}

type CauseDependency struct {
	Depender string
	Dependee string
}

func (CauseDependency) causeMarker() {}

type CauseNoVersions struct {
	Package string
}

func (CauseNoVersions) causeMarker() {}

type CauseUnknownPackage struct {
	Package string
}

func (CauseUnknownPackage) causeMarker() {}

type CauseConflict struct {
	Conflict *Incompatibility
	Other    *Incompatibility
}

func (CauseConflict) causeMarker() {}

// NewIncompatibility constructs an incompatibility, deduplicating terms over
// the same package by intersecting their constraints conceptually (we don't
// actually intersect the constraint strings — we keep the first occurrence
// and rely on PubGrub's own machinery to refine).
func NewIncompatibility(terms []Term, cause Cause) *Incompatibility {
	out := make([]Term, 0, len(terms))
	for _, t := range terms {
		out = append(out, t)
	}
	return &Incompatibility{Terms: out, Cause: cause}
}

// String returns a human-readable form of the incompatibility used in error
// messages and verbose tracing.
func (ic *Incompatibility) String() string {
	parts := make([]string, 0, len(ic.Terms))
	for _, t := range ic.Terms {
		parts = append(parts, t.String())
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// IsFailure reports whether this incompatibility is the empty-set
// incompatibility (denoted "false" in PubGrub literature). When the solver
// derives this, no solution exists.
func (ic *Incompatibility) IsFailure() bool {
	return len(ic.Terms) == 0
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/resolver/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/resolver
git commit -m "feat(resolver): Incompatibility type with typed cause chain"
```

---

## Task 3: PartialSolution — assignments, decision levels, backtracking

The `PartialSolution` is a stack of `Assignment`s. Each assignment is either a `Decision` (the solver picked a concrete version of a package) or a `Derivation` (a Term that follows from existing decisions plus an Incompatibility). Each carries a `DecisionLevel` integer; backtracking truncates to the prefix whose level is `<=` a target.

**Files:**
- Create: `internal/resolver/assignment.go`
- Create: `internal/resolver/assignment_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/resolver/assignment_test.go`:

```go
package resolver

import "testing"

func TestPartialSolutionDecide(t *testing.T) {
	ps := NewPartialSolution()
	v, _ := mustV(t, "1.2.3"), 0
	ps.Decide("p", v)
	if ps.DecisionLevel() != 1 {
		t.Errorf("DecisionLevel after one decision = %d, want 1", ps.DecisionLevel())
	}
	got, ok := ps.DecisionOf("p")
	if !ok {
		t.Fatal("DecisionOf(p) ok=false after decide")
	}
	if got.Major != 1 || got.Minor != 2 {
		t.Errorf("DecisionOf(p) = %+v", got)
	}
}

func TestPartialSolutionDerive(t *testing.T) {
	ps := NewPartialSolution()
	ic := NewIncompatibility(
		[]Term{{Package: "p", Constraint: mustC(t, "^1.0"), Positive: false}},
		CauseRoot{},
	)
	ps.Derive(Term{Package: "p", Constraint: mustC(t, "^1.0"), Positive: true}, ic)
	if ps.DecisionLevel() != 0 {
		t.Errorf("derivations at level 0 expected, got %d", ps.DecisionLevel())
	}
	if len(ps.Assignments) != 1 {
		t.Fatalf("Assignments = %d, want 1", len(ps.Assignments))
	}
	if ps.Assignments[0].IsDecision {
		t.Errorf("first assignment should be derivation")
	}
}

func TestPartialSolutionBacktrack(t *testing.T) {
	ps := NewPartialSolution()
	ps.Decide("a", mustV(t, "1.0.0"))
	ps.Decide("b", mustV(t, "1.0.0"))
	ps.Decide("c", mustV(t, "1.0.0"))
	if ps.DecisionLevel() != 3 {
		t.Fatalf("level = %d", ps.DecisionLevel())
	}
	ps.Backtrack(1)
	if ps.DecisionLevel() != 1 {
		t.Errorf("after Backtrack(1), level = %d, want 1", ps.DecisionLevel())
	}
	if _, ok := ps.DecisionOf("a"); !ok {
		t.Errorf("a decision should still be present")
	}
	if _, ok := ps.DecisionOf("b"); ok {
		t.Errorf("b decision should be gone")
	}
}

func TestPartialSolutionRelationOfTerm(t *testing.T) {
	ps := NewPartialSolution()
	ps.Decide("p", mustV(t, "1.2.3"))
	rel := ps.RelationOf(Term{Package: "p", Constraint: mustC(t, "^1.0"), Positive: true})
	if rel != Subset {
		t.Errorf("RelationOf positive matching term = %v, want Subset", rel)
	}
	rel2 := ps.RelationOf(Term{Package: "p", Constraint: mustC(t, "^2.0"), Positive: true})
	if rel2 != Disjoint {
		t.Errorf("RelationOf positive non-matching term = %v, want Disjoint", rel2)
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/resolver/...`

Expected: build error.

- [ ] **Step 3: Implement PartialSolution**

Create `internal/resolver/assignment.go`:

```go
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
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/resolver/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/resolver
git commit -m "feat(resolver): PartialSolution with assignments and backtracking"
```

---

## Task 4: Static test SourceLookup

Before any resolver logic exercises real version data, we need a controllable in-memory `registry.SourceLookup` for tests. This isolates resolver bugs from registry-client bugs and gives every later resolver test a known package universe.

**Files:**
- Create: `internal/resolver/testlookup/static.go`

- [ ] **Step 1: Write the static lookup**

Create `internal/resolver/testlookup/static.go`:

```go
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
```

- [ ] **Step 2: Verify it builds**

Run: `go build ./internal/resolver/testlookup/...`

Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add internal/resolver/testlookup
git commit -m "test(resolver): in-memory SourceLookup for unit tests"
```

---

## Task 5: versionLister — sorted, filtered version stream

The resolver asks "for package P, what versions are still candidates ordered newest-first?". `versionLister` answers this by wrapping a `SourceLookup`, parsing each version once, applying minimum-stability filtering, and caching per-package results within a single solve.

**Files:**
- Create: `internal/resolver/versions.go`
- Create: `internal/resolver/versions_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/resolver/versions_test.go`:

```go
package resolver

import (
	"context"
	"testing"

	"github.com/torstendittmann/composer-go/internal/constraint"
	"github.com/torstendittmann/composer-go/internal/resolver/testlookup"
)

func TestVersionListerSortedDesc(t *testing.T) {
	l := testlookup.New(map[string][]any{}) // dummy
	_ = l
	src := testlookup.New(map[string][]any{}.(map[string][]any))
	_ = src
	// Use the proper helper:
	srcReal := testlookup.New(map[string][]any{}.(map[string][]any))
	_ = srcReal
}
```

(That stub is wrong on purpose to demonstrate "failing test"; replace with the real one in step 3.)

Replace it with this real failing test:

```go
package resolver

import (
	"context"
	"testing"

	"github.com/torstendittmann/composer-go/internal/registry"
	"github.com/torstendittmann/composer-go/internal/resolver/testlookup"
)

func TestVersionListerSortedDesc(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {
			testlookup.Pkg("a/a", "1.0.0", nil),
			testlookup.Pkg("a/a", "1.2.0", nil),
			testlookup.Pkg("a/a", "1.1.0", nil),
		},
	})
	vl := newVersionLister(src, "stable")
	got, err := vl.versions(context.Background(), "a/a")
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].Raw != "1.2.0" || got[1].Raw != "1.1.0" || got[2].Raw != "1.0.0" {
		t.Errorf("order: %v", []string{got[0].Raw, got[1].Raw, got[2].Raw})
	}
}

func TestVersionListerFiltersByMinStability(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {
			testlookup.Pkg("a/a", "2.0.0-alpha", nil),
			testlookup.Pkg("a/a", "1.9.0", nil),
		},
	})
	vl := newVersionLister(src, "stable")
	got, _ := vl.versions(context.Background(), "a/a")
	if len(got) != 1 || got[0].Raw != "1.9.0" {
		t.Errorf("expected only 1.9.0, got %+v", got)
	}

	vl2 := newVersionLister(src, "alpha")
	got2, _ := vl2.versions(context.Background(), "a/a")
	if len(got2) != 2 {
		t.Errorf("expected 2 with min=alpha, got %d", len(got2))
	}
}

func TestVersionListerCachesWithinSolve(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {testlookup.Pkg("a/a", "1.0.0", nil)},
	})
	vl := newVersionLister(src, "stable")
	a, _ := vl.versions(context.Background(), "a/a")
	b, _ := vl.versions(context.Background(), "a/a")
	if &a[0] != &b[0] && a[0].Raw != b[0].Raw {
		t.Errorf("results should be stable across calls")
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/resolver/...`

Expected: build error on `newVersionLister`, `versions`, `Raw`.

- [ ] **Step 3: Implement versionLister**

Create `internal/resolver/versions.go`:

```go
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
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/resolver/...`

Expected: PASS for the three lister tests.

- [ ] **Step 5: Commit**

```bash
git add internal/resolver
git commit -m "feat(resolver): versionLister with stability filtering and caching"
```

---

## Task 6: Result type and resolver error types

We define the resolver's output type and the typed errors it can return. Keeping `Result` distinct from `lock.File` lets the orchestrator add `Source`/`Dist`/checksum verification before lockfile write.

**Files:**
- Create: `internal/resolver/error.go`

- [ ] **Step 1: Write the file**

Create `internal/resolver/error.go`:

```go
package resolver

import (
	"fmt"
	"strings"

	"github.com/torstendittmann/composer-go/internal/constraint"
	"github.com/torstendittmann/composer-go/internal/registry"
)

// Result is the resolver's output: one entry per chosen package.
//
// The Source/Dist fields on the underlying lock.Package are intentionally NOT
// populated here — the orchestrator is responsible for filling them after
// download succeeds. The resolver only knows what the registry advertised,
// which the orchestrator may need to override (e.g., dist mirrors, checksum
// re-verification).
type Result struct {
	// Packages is the set of resolved production packages.
	Packages []ResolvedPackage
	// PackagesDev is the set of resolved dev-only packages.
	PackagesDev []ResolvedPackage
}

// ResolvedPackage is one entry in the result.
type ResolvedPackage struct {
	Name    string
	Version constraint.Version
	// Record is the registry data that fed this resolution. The orchestrator
	// uses Record.Dist.URL etc. as a starting point for fetcher work.
	Record registry.PackageVersion
}

// ConflictError is returned by Solve when no solution exists. The Root
// incompatibility is the empty-set incompatibility derived by the conflict
// resolver; walking its Cause chain produces the human-readable derivation.
type ConflictError struct {
	Root *Incompatibility
}

func (e *ConflictError) Error() string {
	if e.Root == nil {
		return "resolver: no solution exists"
	}
	return "resolver: conflict — " + renderDerivation(e.Root)
}

// renderDerivation walks the cause chain and produces an indented chain of
// "because A and because B" lines. The output is intentionally simple in
// stage 1; stage 3 polishes the rendering.
func renderDerivation(ic *Incompatibility) string {
	var b strings.Builder
	render(&b, ic, 0)
	return b.String()
}

func render(b *strings.Builder, ic *Incompatibility, depth int) {
	for i := 0; i < depth; i++ {
		b.WriteString("  ")
	}
	fmt.Fprintf(b, "%s\n", ic.String())
	if cc, ok := ic.Cause.(CauseConflict); ok {
		render(b, cc.Conflict, depth+1)
		render(b, cc.Other, depth+1)
	}
}

// ErrNoVersionsForPackage is a sentinel returned when a package has no
// versions matching the requested constraint at the requested stability.
type ErrNoVersionsForPackage struct {
	Package    string
	Constraint string
}

func (e *ErrNoVersionsForPackage) Error() string {
	return fmt.Sprintf("resolver: no versions of %s satisfy %s", e.Package, e.Constraint)
}
```

- [ ] **Step 2: Verify it builds**

Run: `go build ./internal/resolver/...`

Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add internal/resolver
git commit -m "feat(resolver): Result, ConflictError, and ErrNoVersionsForPackage types"
```

---

## Task 7: Unit propagation

Unit propagation is the heart of PubGrub's "fast" path. Loop:

1. Pick a known incompatibility `ic` whose terms have not all been refuted by the partial solution.
2. Classify `ic` against the partial solution:
   - **Satisfied** (every term holds): trigger conflict resolution.
   - **Almost-satisfied** (all but one term holds; the last is "unresolved"): derive the inverse of the remaining term.
   - **Otherwise**: nothing to do.

We implement this single-step (`propagatePackage`) and let the driver loop call it.

**Files:**
- Create: `internal/resolver/propagate.go`

- [ ] **Step 1: Write the file with the algorithm**

Create `internal/resolver/propagate.go`:

```go
package resolver

// propagationResult captures what unit propagation did in one step.
type propagationResult int

const (
	// propResultNoChange: nothing about this package required action.
	propResultNoChange propagationResult = iota
	// propResultDerived: a new derivation was added to the partial solution.
	propResultDerived
	// propResultConflict: an incompatibility is fully satisfied — caller
	// must run conflict resolution starting from `conflictIC`.
	propResultConflict
)

// classifyIncompatibility classifies an incompatibility against the partial
// solution and, if "almost satisfied", returns the index of the unresolved
// term.
//
// Definitions:
//   - Satisfied:        ps.Satisfies(t) for every term t in ic.
//   - Almost satisfied: exactly one term t has ps.RelationOf(t) == Overlapping
//                       and all other terms have ps.RelationOf(t) == Subset.
//   - Otherwise:        unresolved (some term is Disjoint, etc.).
func classifyIncompatibility(ps *PartialSolution, ic *Incompatibility) (status propagationResult, unresolvedIdx int) {
	allSubset := true
	overlapCount := 0
	overlapIdx := -1
	for i, t := range ic.Terms {
		rel := ps.RelationOf(t)
		switch rel {
		case Disjoint:
			// At least one term is impossible; the incompatibility cannot
			// fire from here. No-op.
			return propResultNoChange, -1
		case Overlapping:
			allSubset = false
			overlapCount++
			overlapIdx = i
		case Subset:
			// continues
		}
	}
	if allSubset {
		return propResultConflict, -1
	}
	if overlapCount == 1 {
		return propResultDerived, overlapIdx
	}
	return propResultNoChange, -1
}

// propagateOnce runs unit propagation against all known incompatibilities
// that mention `pkg`. It iterates through `ics` in order; on the first
// productive step it stops and returns. Returning propResultDerived means
// the caller should re-run propagation (because the new derivation may have
// unlocked further propagation). Returning propResultConflict means the
// caller should hand off to conflict resolution.
func propagateOnce(ps *PartialSolution, ics []*Incompatibility, pkg string) (status propagationResult, conflictIC *Incompatibility) {
	for _, ic := range ics {
		if !mentionsPackage(ic, pkg) {
			continue
		}
		st, idx := classifyIncompatibility(ps, ic)
		switch st {
		case propResultConflict:
			return propResultConflict, ic
		case propResultDerived:
			t := ic.Terms[idx]
			ps.Derive(t.Inverse(), ic)
			return propResultDerived, nil
		}
	}
	return propResultNoChange, nil
}

func mentionsPackage(ic *Incompatibility, pkg string) bool {
	for _, t := range ic.Terms {
		if t.Package == pkg {
			return true
		}
	}
	return false
}

// propagate runs propagateOnce in a fixed-point loop over a worklist of
// "changed" packages. It returns either nil (steady state with no conflict)
// or the conflict incompatibility to feed conflict resolution.
func propagate(ps *PartialSolution, ics []*Incompatibility, seed string) *Incompatibility {
	work := []string{seed}
	seen := map[string]bool{seed: true}
	for len(work) > 0 {
		pkg := work[0]
		work = work[1:]
		for {
			st, conflictIC := propagateOnce(ps, ics, pkg)
			if st == propResultConflict {
				return conflictIC
			}
			if st == propResultNoChange {
				break
			}
			// Derivation: queue every package mentioned in the incompat that
			// just fired, so we keep propagating outward.
			last := ps.Assignments[len(ps.Assignments)-1]
			if last.Cause != nil {
				for _, t := range last.Cause.Terms {
					if !seen[t.Package] {
						seen[t.Package] = true
						work = append(work, t.Package)
					}
				}
			}
		}
	}
	return nil
}
```

- [ ] **Step 2: Verify it builds**

Run: `go build ./internal/resolver/...`

Expected: clean build (no tests yet for propagate; covered by the end-to-end Solve test).

- [ ] **Step 3: Commit**

```bash
git add internal/resolver
git commit -m "feat(resolver): unit propagation loop"
```

---

## Task 8: Conflict resolution — root cause incompatibility

When propagation reports a fully-satisfied incompatibility, conflict resolution walks the cause chain to compute a *new* incompatibility that explains the conflict in terms of decisions only (or in terms of the root). The simplified algorithm we use here:

1. Start with the satisfied incompatibility `ic`.
2. Find the most-recent assignment that satisfies any term in `ic`. Call it `a`.
3. If `a` is a Decision (decision level > 0 and `IsDecision`), we're done — backtrack to `a.DecisionLevel - 1` and add `ic` to the global incompatibility set; the next propagation will derive the inverse of `a`'s term.
4. If `a` is a Derivation, replace `ic` with the resolution of `ic` and `a.Cause` (drop the term satisfied by `a`, union the rest), record this as a new `Incompatibility` with `CauseConflict{ic, a.Cause}`, and repeat.

If we exhaust the partial solution without finding a Decision, the conflict is rooted at the manifest itself — return the (empty-terms) incompatibility for the driver to wrap as `ConflictError`.

**Files:**
- Create: `internal/resolver/conflict.go`

- [ ] **Step 1: Write the file**

Create `internal/resolver/conflict.go`:

```go
package resolver

// conflictResolution computes the root-cause incompatibility from a
// satisfied incompatibility plus the partial solution. It returns the
// incompatibility to add to the global set and the decision level to which
// the solver should backtrack.
//
// On total failure (the conflict bottoms out at the root manifest), the
// returned incompatibility has empty Terms and the level is 0; the driver
// converts that to ConflictError.
func conflictResolution(ps *PartialSolution, ic *Incompatibility) (*Incompatibility, int) {
	current := ic
	for {
		if current.IsFailure() {
			return current, 0
		}
		// Walk the assignment list backwards looking for the most recent
		// assignment that satisfies any term in `current`.
		idx := -1
		for i := len(ps.Assignments) - 1; i >= 0; i-- {
			a := ps.Assignments[i]
			if assignmentSatisfiesAnyTerm(a, current) {
				idx = i
				break
			}
		}
		if idx < 0 {
			// No satisfier found — conflict is rooted at the manifest.
			return current, 0
		}
		a := ps.Assignments[idx]
		if a.IsDecision {
			// Backtrack to the level just below this decision.
			target := a.DecisionLevel - 1
			if target < 0 {
				target = 0
			}
			return current, target
		}
		// Resolve: combine `current` with the cause of this derivation.
		next := resolveIncompatibilities(current, a.Cause, a.Term)
		current = next
	}
}

// assignmentSatisfiesAnyTerm reports whether assignment a satisfies at least
// one term in ic.
func assignmentSatisfiesAnyTerm(a Assignment, ic *Incompatibility) bool {
	for _, t := range ic.Terms {
		if t.Package != a.Package {
			continue
		}
		if a.IsDecision {
			if t.Satisfies(a.Version) {
				return true
			}
			continue
		}
		// Derivation: check if a.Term implies t. We use a coarse "same
		// package, opposite-or-overlapping sign" heuristic which is sound:
		// false positives just slow propagation.
		if a.Term.Package == t.Package {
			return true
		}
	}
	return false
}

// resolveIncompatibilities builds a new incompatibility from `current` and
// the cause of a derivation about `aTerm`. The constructed incompatibility
// drops the term in `current` that the derivation satisfied (matched by
// package), unions in the *other* terms of the cause, and records both
// parents for the derivation chain.
func resolveIncompatibilities(current, cause *Incompatibility, aTerm Term) *Incompatibility {
	merged := make([]Term, 0, len(current.Terms)+len(cause.Terms))
	for _, t := range current.Terms {
		if t.Package == aTerm.Package {
			continue
		}
		merged = appendDedup(merged, t)
	}
	for _, t := range cause.Terms {
		if t.Package == aTerm.Package {
			continue
		}
		merged = appendDedup(merged, t)
	}
	return &Incompatibility{
		Terms: merged,
		Cause: CauseConflict{Conflict: current, Other: cause},
	}
}

// appendDedup adds t to terms unless an equivalent term is already present
// (same package, same sign, same constraint string). This is a coarse
// dedup; it does not merge constraints.
func appendDedup(terms []Term, t Term) []Term {
	for _, existing := range terms {
		if existing.Package == t.Package &&
			existing.Positive == t.Positive &&
			existing.Constraint.Original == t.Constraint.Original {
			return terms
		}
	}
	return append(terms, t)
}
```

- [ ] **Step 2: Verify it builds**

Run: `go build ./internal/resolver/...`

Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add internal/resolver
git commit -m "feat(resolver): conflict resolution producing root-cause incompatibilities"
```

---

## Task 9: Decision-making

When propagation reaches steady state with no conflict and not all packages are decided, we pick a package to decide on. Heuristic for stage 1:

1. From the partial solution, collect packages that have a positive derivation but no decision.
2. For each, query `versionLister` for candidate versions intersected with the conjunction of positive derivations' constraints.
3. Prefer the package with the *fewest* viable versions (Pub's "preference for simpler" heuristic), breaking ties by name for determinism.
4. Pick the highest version from that candidate's list (newest-first ordering already done by the lister).
5. For each `require` in the chosen version, generate a new `Incompatibility` (`{positive: chosenPkg=version, negative: dep@constraint}` with `CauseDependency`) and add to the set.
6. Record the `Decide`.

If a package has zero viable versions, generate `{positive: pkg matches active constraint}` with `CauseNoVersions`, add it as an incompatibility, and let propagation produce the derivation that leads to backtracking.

**Files:**
- Create: `internal/resolver/decide.go`

- [ ] **Step 1: Write the file**

Create `internal/resolver/decide.go`:

```go
package resolver

import (
	"context"
	"sort"

	"github.com/torstendittmann/composer-go/internal/constraint"
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
// conjunction of positive derivations currently in the partial solution.
func filterByActiveConstraints(ps *PartialSolution, pkg string, vs []listedVersion) []listedVersion {
	terms := ps.PositiveDerivations(pkg)
	out := make([]listedVersion, 0, len(vs))
nextV:
	for _, v := range vs {
		for _, t := range terms {
			if !t.Constraint.Satisfies(v.Parsed) {
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
```

- [ ] **Step 2: Verify it builds**

Run: `go build ./internal/resolver/...`

Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add internal/resolver
git commit -m "feat(resolver): version-decision step with PubGrub heuristic"
```

---

## Task 10: Top-level Solve driver

The driver wires propagation, conflict resolution, and decision-making into the canonical PubGrub loop:

1. Seed: build root incompatibilities from manifest's `require` (and `require-dev` if `IncludeDev`).
2. Loop:
   a. Propagate from the most recently changed package.
   b. If conflict, run conflict resolution; if it yields a failure (empty terms), return `ConflictError`. Otherwise add the new incompatibility to the global set and backtrack.
   c. Else, decide. If `decideAllDone`, exit success.
3. Build `Result` from the partial solution's decisions.

**Files:**
- Create: `internal/resolver/solve.go`
- Create: `internal/resolver/solve_test.go`

- [ ] **Step 1: Write the failing tests first**

Create `internal/resolver/solve_test.go`:

```go
package resolver

import (
	"context"
	"errors"
	"testing"

	"github.com/torstendittmann/composer-go/internal/manifest"
	"github.com/torstendittmann/composer-go/internal/registry"
	"github.com/torstendittmann/composer-go/internal/resolver/testlookup"
)

func TestSolveSimpleNoDeps(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {testlookup.Pkg("a/a", "1.0.0", nil)},
	})
	m := &manifest.Manifest{
		Name:    "user/app",
		Require: map[string]string{"a/a": "^1.0"},
	}
	res, err := Solve(context.Background(), Input{Manifest: m, Source: src})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if len(res.Packages) != 1 {
		t.Fatalf("Packages = %d, want 1", len(res.Packages))
	}
	if res.Packages[0].Name != "a/a" || res.Packages[0].Version.Major != 1 {
		t.Errorf("got %+v", res.Packages[0])
	}
}

func TestSolveTransitiveDeps(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {testlookup.Pkg("a/a", "1.0.0", map[string]string{"b/b": "^1.0"})},
		"b/b": {testlookup.Pkg("b/b", "1.2.3", nil)},
	})
	m := &manifest.Manifest{
		Name:    "user/app",
		Require: map[string]string{"a/a": "^1.0"},
	}
	res, err := Solve(context.Background(), Input{Manifest: m, Source: src})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if len(res.Packages) != 2 {
		t.Fatalf("Packages = %d, want 2 (a/a and b/b), got %v", len(res.Packages), res.Packages)
	}
	names := map[string]bool{}
	for _, p := range res.Packages {
		names[p.Name] = true
	}
	if !names["a/a"] || !names["b/b"] {
		t.Errorf("expected both a/a and b/b, got %v", names)
	}
}

func TestSolvePicksHighestSatisfying(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {
			testlookup.Pkg("a/a", "1.0.0", nil),
			testlookup.Pkg("a/a", "1.5.0", nil),
			testlookup.Pkg("a/a", "2.0.0", nil),
		},
	})
	m := &manifest.Manifest{
		Name:    "user/app",
		Require: map[string]string{"a/a": "^1.0"},
	}
	res, err := Solve(context.Background(), Input{Manifest: m, Source: src})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if len(res.Packages) != 1 || res.Packages[0].Version.Minor != 5 {
		t.Errorf("expected a/a=1.5.0, got %+v", res.Packages)
	}
}

func TestSolveConflictReturnsConflictError(t *testing.T) {
	// a/a 1.0.0 -> requires b/b ^1.0
	// c/c 1.0.0 -> requires b/b ^2.0
	// no version of b/b satisfies both; and we require both a/a and c/c.
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {testlookup.Pkg("a/a", "1.0.0", map[string]string{"b/b": "^1.0"})},
		"c/c": {testlookup.Pkg("c/c", "1.0.0", map[string]string{"b/b": "^2.0"})},
		"b/b": {
			testlookup.Pkg("b/b", "1.0.0", nil),
			testlookup.Pkg("b/b", "2.0.0", nil),
		},
	})
	m := &manifest.Manifest{
		Name: "user/app",
		Require: map[string]string{
			"a/a": "^1.0",
			"c/c": "^1.0",
		},
	}
	_, err := Solve(context.Background(), Input{Manifest: m, Source: src})
	if err == nil {
		t.Fatal("expected ConflictError, got nil")
	}
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("err type = %T, want *ConflictError; err=%v", err, err)
	}
}

func TestSolveSkipsPlatformRequires(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {testlookup.Pkg("a/a", "1.0.0", map[string]string{"php": ">=8.1", "ext-mbstring": "*"})},
	})
	m := &manifest.Manifest{
		Name:    "user/app",
		Require: map[string]string{"a/a": "^1.0"},
	}
	res, err := Solve(context.Background(), Input{Manifest: m, Source: src})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if len(res.Packages) != 1 {
		t.Errorf("Packages = %d, want 1 (php and ext-* must not be resolved)", len(res.Packages))
	}
}

func TestSolveDevRequiresIncluded(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {testlookup.Pkg("a/a", "1.0.0", nil)},
		"d/d": {testlookup.Pkg("d/d", "1.0.0", nil)},
	})
	m := &manifest.Manifest{
		Name:       "user/app",
		Require:    map[string]string{"a/a": "^1.0"},
		RequireDev: map[string]string{"d/d": "^1.0"},
	}
	res, err := Solve(context.Background(), Input{Manifest: m, Source: src, IncludeDev: true})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if len(res.Packages) != 1 || len(res.PackagesDev) != 1 {
		t.Errorf("Packages=%d, PackagesDev=%d", len(res.Packages), len(res.PackagesDev))
	}
}

func TestSolveDevRequiresExcluded(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {testlookup.Pkg("a/a", "1.0.0", nil)},
		"d/d": {testlookup.Pkg("d/d", "1.0.0", nil)},
	})
	m := &manifest.Manifest{
		Name:       "user/app",
		Require:    map[string]string{"a/a": "^1.0"},
		RequireDev: map[string]string{"d/d": "^1.0"},
	}
	res, err := Solve(context.Background(), Input{Manifest: m, Source: src, IncludeDev: false})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if len(res.Packages) != 1 || len(res.PackagesDev) != 0 {
		t.Errorf("Packages=%d, PackagesDev=%d (dev should be excluded)", len(res.Packages), len(res.PackagesDev))
	}
}

func TestSolveDeterministic(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {
			testlookup.Pkg("a/a", "1.0.0", nil),
			testlookup.Pkg("a/a", "1.1.0", nil),
			testlookup.Pkg("a/a", "1.2.0", nil),
		},
		"b/b": {testlookup.Pkg("b/b", "1.0.0", nil)},
	})
	m := &manifest.Manifest{
		Name: "user/app",
		Require: map[string]string{
			"a/a": "^1.0",
			"b/b": "^1.0",
		},
	}
	r1, _ := Solve(context.Background(), Input{Manifest: m, Source: src})
	r2, _ := Solve(context.Background(), Input{Manifest: m, Source: src})
	if len(r1.Packages) != len(r2.Packages) {
		t.Fatalf("non-deterministic length")
	}
	for i := range r1.Packages {
		if r1.Packages[i].Name != r2.Packages[i].Name ||
			r1.Packages[i].Version.Original != r2.Packages[i].Version.Original {
			t.Errorf("non-deterministic at %d: %+v vs %+v", i, r1.Packages[i], r2.Packages[i])
		}
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/resolver/...`

Expected: build error on `Solve`, `Input`.

- [ ] **Step 3: Implement Solve**

Create `internal/resolver/solve.go`:

```go
package resolver

import (
	"context"
	"errors"
	"sort"

	"github.com/torstendittmann/composer-go/internal/constraint"
	"github.com/torstendittmann/composer-go/internal/lock"
	"github.com/torstendittmann/composer-go/internal/manifest"
	"github.com/torstendittmann/composer-go/internal/registry"
)

// Input is everything Solve needs.
type Input struct {
	Manifest *manifest.Manifest
	Lock     *lock.File // optional; used for "stay close to lock" preference (stage 3)
	Source   registry.SourceLookup
	// IncludeDev includes require-dev when true. Default false matches
	// `composer install --no-dev` semantics.
	IncludeDev bool
	// PlatformFingerprint is captured at resolution time and stored in the
	// lockfile. The resolver itself doesn't enforce platform constraints in
	// stage 1 (Plan 6 wires that in); the field is here so the cache key
	// includes it.
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
//   { positive: $root@*, negative: name@constraint }   cause: CauseRoot
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
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/resolver/... -v`

Expected: all `TestSolve*` tests PASS. Investigate any failures: most likely culprits are (a) the propagation worklist not seeding correctly after the initial root derivation, (b) `RelationOf` over-reporting `Overlapping` and stalling propagation, (c) `assignmentSatisfiesAnyTerm` matching too aggressively.

If a test fails:

1. Add a `t.Logf("partial solution: %+v", ps.Assignments)` near the failure.
2. Re-run with `-v` and trace which incompatibility was/was not propagated.
3. Fix and re-run.

- [ ] **Step 5: Commit**

```bash
git add internal/resolver
git commit -m "feat(resolver): top-level Solve driver wiring propagate/decide/conflict"
```

---

## Task 11: Adapter — `Result` to `[]lock.Package`

The orchestrator wants `lock.Package` slices to write into `lock.File`. The resolver's `Result` is intentionally distinct, but a thin adapter eliminates boilerplate at every call site.

**Files:**
- Create: `internal/resolver/adapter.go`
- Create: `internal/resolver/adapter_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/resolver/adapter_test.go`:

```go
package resolver

import (
	"testing"

	"github.com/torstendittmann/composer-go/internal/registry"
)

func TestToLockPackages(t *testing.T) {
	v, _ := mustV(t, "1.2.3"), 0
	r := &Result{
		Packages: []ResolvedPackage{{
			Name:    "a/a",
			Version: v,
			Record: registry.PackageVersion{
				Name:    "a/a",
				Version: "1.2.3",
				Source:  registry.Source{Type: "git", URL: "git://a", Ref: "abc"},
				Dist:    registry.Dist{Type: "zip", URL: "https://a.zip", Sha: "deadbeef"},
				Require: map[string]string{"php": ">=8.1"},
			},
		}},
		PackagesDev: []ResolvedPackage{{
			Name:    "d/d",
			Version: v,
			Record: registry.PackageVersion{
				Name:    "d/d",
				Version: "1.2.3",
				Source:  registry.Source{Type: "git", URL: "git://d", Ref: "def"},
				Dist:    registry.Dist{Type: "zip", URL: "https://d.zip", Sha: "cafebabe"},
			},
		}},
	}
	prod, dev := ToLockPackages(r)
	if len(prod) != 1 || prod[0].Name != "a/a" {
		t.Errorf("prod = %+v", prod)
	}
	if prod[0].Source.Ref != "abc" {
		t.Errorf("prod[0].Source.Ref = %q, want abc", prod[0].Source.Ref)
	}
	if prod[0].Dist.Sha256 != "deadbeef" {
		t.Errorf("prod[0].Dist.Sha256 = %q", prod[0].Dist.Sha256)
	}
	if prod[0].Require["php"] != ">=8.1" {
		t.Errorf("require not preserved: %v", prod[0].Require)
	}
	if len(dev) != 1 || dev[0].Name != "d/d" {
		t.Errorf("dev = %+v", dev)
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/resolver/...`

Expected: build error on `ToLockPackages`.

- [ ] **Step 3: Implement adapter**

Create `internal/resolver/adapter.go`:

```go
package resolver

import (
	"github.com/torstendittmann/composer-go/internal/lock"
)

// ToLockPackages converts a resolver Result into the slices the orchestrator
// writes into lock.File. The output preserves Source/Dist from the registry
// record AS-IS — the orchestrator may overwrite them after fetching (e.g.,
// to reflect verified sha256 rather than the registry-advertised one).
//
// Suggest is intentionally NOT copied here in stage 1; orchestrator can add
// it later if needed for `composer-go suggest` (post-MVP).
func ToLockPackages(r *Result) (prod, dev []lock.Package) {
	if r == nil {
		return nil, nil
	}
	prod = make([]lock.Package, 0, len(r.Packages))
	for _, p := range r.Packages {
		prod = append(prod, toLockPackage(p))
	}
	dev = make([]lock.Package, 0, len(r.PackagesDev))
	for _, p := range r.PackagesDev {
		dev = append(dev, toLockPackage(p))
	}
	return prod, dev
}

func toLockPackage(p ResolvedPackage) lock.Package {
	versionStr := p.Record.Version
	if versionStr == "" {
		versionStr = p.Version.Original
	}
	return lock.Package{
		Name:    p.Name,
		Version: versionStr,
		Source: lock.Source{
			Type: p.Record.Source.Type,
			URL:  p.Record.Source.URL,
			Ref:  p.Record.Source.Ref,
		},
		Dist: lock.Dist{
			Type:   p.Record.Dist.Type,
			URL:    p.Record.Dist.URL,
			Sha256: p.Record.Dist.Sha,
		},
		Require: p.Record.Require,
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/resolver/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/resolver
git commit -m "feat(resolver): adapter to convert Result into lock.Package slices"
```

---

## Task 12: Resolution-result cache (cache layer 3)

The cache wraps `Solve`. Key: sha256 of the concatenation of `manifestContentHash || lockContentHash || platformFingerprint || include-dev flag || min-stability`. Value: gob-encoded `Result`.

**Files:**
- Create: `internal/resolver/cache.go`
- Create: `internal/resolver/cache_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/resolver/cache_test.go`:

```go
package resolver

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/torstendittmann/composer-go/internal/manifest"
	"github.com/torstendittmann/composer-go/internal/registry"
	"github.com/torstendittmann/composer-go/internal/resolver/testlookup"
)

type countingLookup struct {
	inner registry.SourceLookup
	calls *int32
}

func (c countingLookup) Lookup(ctx context.Context, name string) (*registry.PackageMetadata, error) {
	atomic.AddInt32(c.calls, 1)
	return c.inner.Lookup(ctx, name)
}

func TestCachedSolverHitSkipsResolver(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {testlookup.Pkg("a/a", "1.0.0", nil)},
	})
	var calls int32
	counted := countingLookup{inner: src, calls: &calls}

	cs, err := NewCachedSolver(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	m := &manifest.Manifest{Name: "u/a", Require: map[string]string{"a/a": "^1.0"}}
	in := Input{
		Manifest:            m,
		Source:              counted,
		PlatformFingerprint: "php-8.2",
	}
	in1 := in
	in1.Source = counted
	r1, err := cs.Solve(context.Background(), in1, "manifest-hash-1", "lock-hash-1")
	if err != nil {
		t.Fatalf("Solve(1): %v", err)
	}
	calls1 := atomic.LoadInt32(&calls)
	if calls1 == 0 {
		t.Fatalf("expected lookups on cold cache, got 0")
	}

	atomic.StoreInt32(&calls, 0)
	in2 := in
	in2.Source = counted
	r2, err := cs.Solve(context.Background(), in2, "manifest-hash-1", "lock-hash-1")
	if err != nil {
		t.Fatalf("Solve(2): %v", err)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Errorf("warm cache should not call SourceLookup; got %d calls", calls)
	}
	if len(r1.Packages) != len(r2.Packages) {
		t.Errorf("warm result differs from cold")
	}
}

func TestCachedSolverDifferentInputsMiss(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {testlookup.Pkg("a/a", "1.0.0", nil)},
	})
	cs, _ := NewCachedSolver(t.TempDir())
	m := &manifest.Manifest{Name: "u/a", Require: map[string]string{"a/a": "^1.0"}}

	_, err := cs.Solve(context.Background(), Input{Manifest: m, Source: src, PlatformFingerprint: "php-8.2"}, "h1", "l1")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		mhash, lhash, fp string
	}{
		{"h-different", "l1", "php-8.2"},
		{"h1", "l-different", "php-8.2"},
		{"h1", "l1", "php-different"},
	}
	for _, tc := range cases {
		// We can't easily count from inside CachedSolver, but a different key
		// must produce a successful Solve (no panic, valid result).
		r, err := cs.Solve(context.Background(), Input{Manifest: m, Source: src, PlatformFingerprint: tc.fp}, tc.mhash, tc.lhash)
		if err != nil {
			t.Errorf("Solve(%v): %v", tc, err)
		}
		if r == nil {
			t.Errorf("nil result for %v", tc)
		}
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/resolver/...`

Expected: build error on `NewCachedSolver`, `cs.Solve`.

- [ ] **Step 3: Implement the cache**

Create `internal/resolver/cache.go`:

```go
package resolver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// CachedSolver wraps Solve with a disk-backed result cache. The cache is
// keyed by (manifestContentHash, lockContentHash, platformFingerprint,
// IncludeDev, MinimumStability). On a hit the wrapped registry SourceLookup
// is not called at all.
type CachedSolver struct {
	dir string
}

// NewCachedSolver creates a cache rooted at dir. Sub-dirs are created lazily.
func NewCachedSolver(dir string) (*CachedSolver, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &CachedSolver{dir: dir}, nil
}

// Solve returns a cached result if one exists for the given key, otherwise
// runs Solve and stores the result.
//
// `manifestHash` and `lockHash` are computed by the caller (hex-encoded
// sha256 strings, with an empty string permitted when no lockfile exists).
// The platform fingerprint comes from `in.PlatformFingerprint`.
func (cs *CachedSolver) Solve(ctx context.Context, in Input, manifestHash, lockHash string) (*Result, error) {
	key := cs.key(in, manifestHash, lockHash)
	if r, ok := cs.load(key); ok {
		return r, nil
	}
	r, err := Solve(ctx, in)
	if err != nil {
		return nil, err
	}
	if err := cs.store(key, r); err != nil {
		// Cache failures are non-fatal — the result is still correct.
		_ = err
	}
	return r, nil
}

func (cs *CachedSolver) key(in Input, manifestHash, lockHash string) string {
	h := sha256.New()
	h.Write([]byte("v1\n"))
	h.Write([]byte("manifest:" + manifestHash + "\n"))
	h.Write([]byte("lock:" + lockHash + "\n"))
	h.Write([]byte("platform:" + in.PlatformFingerprint + "\n"))
	h.Write([]byte("dev:" + strconv.FormatBool(in.IncludeDev) + "\n"))
	h.Write([]byte("stab:" + in.MinimumStability + "\n"))
	return hex.EncodeToString(h.Sum(nil))
}

func (cs *CachedSolver) path(key string) string {
	return filepath.Join(cs.dir, key[:2], key+".gob")
}

func (cs *CachedSolver) load(key string) (*Result, bool) {
	f, err := os.Open(cs.path(key))
	if err != nil {
		return nil, false
	}
	defer f.Close()
	var r Result
	if err := gob.NewDecoder(f).Decode(&r); err != nil && !errors.Is(err, io.EOF) {
		// Corrupt entry: evict, treat as miss.
		_ = os.Remove(cs.path(key))
		return nil, false
	}
	return &r, true
}

func (cs *CachedSolver) store(key string, r *Result) error {
	if err := os.MkdirAll(filepath.Dir(cs.path(key)), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(r); err != nil {
		return fmt.Errorf("resolver/cache: encode: %w", err)
	}
	tmp := cs.path(key) + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, cs.path(key))
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/resolver/...`

Expected: PASS for cache tests and all earlier tests.

- [ ] **Step 5: Commit**

```bash
git add internal/resolver
git commit -m "feat(resolver): resolution-result cache (cache layer 3)"
```

---

## Task 13: Property test — random satisfiable graphs

A small property test to catch determinism regressions and obvious solver bugs. We generate a random DAG of 5–15 packages, pick a random root manifest, and assert:

1. Solve returns no error.
2. Every chosen version satisfies every direct constraint in the manifest.
3. Every transitive require in a chosen version is satisfied by another chosen version.
4. Two runs produce identical results.

**Files:**
- Create: `internal/resolver/property_test.go`

- [ ] **Step 1: Write the test**

Create `internal/resolver/property_test.go`:

```go
package resolver

import (
	"context"
	"fmt"
	"math/rand"
	"testing"

	"github.com/torstendittmann/composer-go/internal/constraint"
	"github.com/torstendittmann/composer-go/internal/manifest"
	"github.com/torstendittmann/composer-go/internal/registry"
	"github.com/torstendittmann/composer-go/internal/resolver/testlookup"
)

func TestPropertyRandomSatisfiableGraphs(t *testing.T) {
	for seed := int64(1); seed <= 25; seed++ {
		seed := seed
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			r := rand.New(rand.NewSource(seed))
			pkgs := genGraph(r)
			src := testlookup.New(pkgs)

			rootReqs := map[string]string{}
			// Take 1-3 random packages as direct requires.
			names := keysOf(pkgs)
			r.Shuffle(len(names), func(i, j int) { names[i], names[j] = names[j], names[i] })
			n := 1 + r.Intn(3)
			if n > len(names) {
				n = len(names)
			}
			for _, name := range names[:n] {
				// Use "*" for liberal satisfiability.
				rootReqs[name] = "*"
			}
			m := &manifest.Manifest{Name: "u/a", Require: rootReqs}

			res, err := Solve(context.Background(), Input{Manifest: m, Source: src})
			if err != nil {
				t.Fatalf("Solve: %v (manifest=%v)", err, rootReqs)
			}

			// Verify every chosen version's transitive requires are satisfied
			// by another chosen version.
			chosen := map[string]constraint.Version{}
			for _, p := range res.Packages {
				chosen[p.Name] = p.Version
			}
			for _, p := range res.Packages {
				for depName, depRaw := range p.Record.Require {
					if isPlatformPackage(depName) {
						continue
					}
					depV, ok := chosen[depName]
					if !ok {
						t.Errorf("%s requires %s but %s not chosen", p.Name, depName, depName)
						continue
					}
					c, err := constraint.Parse(depRaw)
					if err != nil {
						continue
					}
					if !c.Satisfies(depV) {
						t.Errorf("%s -> %s: chosen %s does not satisfy %s",
							p.Name, depName, depV.Original, depRaw)
					}
				}
			}

			// Determinism.
			res2, err := Solve(context.Background(), Input{Manifest: m, Source: src})
			if err != nil {
				t.Fatalf("Solve(2): %v", err)
			}
			if len(res.Packages) != len(res2.Packages) {
				t.Fatalf("non-deterministic length")
			}
			for i := range res.Packages {
				if res.Packages[i].Name != res2.Packages[i].Name ||
					!res.Packages[i].Version.Equal(res2.Packages[i].Version) {
					t.Errorf("non-deterministic at %d", i)
				}
			}
		})
	}
}

// genGraph builds a small package universe with random deps. Versions are
// always picked so the universe is satisfiable: each dep's constraint is
// "*", which is always satisfied as long as the depended-on package has at
// least one version.
func genGraph(r *rand.Rand) map[string][]registry.PackageVersion {
	count := 5 + r.Intn(11)
	names := make([]string, count)
	for i := range names {
		names[i] = fmt.Sprintf("p%02d/x", i)
	}
	out := map[string][]registry.PackageVersion{}
	for i, n := range names {
		nVers := 1 + r.Intn(3)
		var vs []registry.PackageVersion
		for v := 0; v < nVers; v++ {
			req := map[string]string{}
			// Each version may depend on 0..2 strictly-later-indexed packages.
			if i+1 < len(names) {
				maxDeps := 2
				if remaining := len(names) - i - 1; remaining < maxDeps {
					maxDeps = remaining
				}
				if maxDeps > 0 {
					for d := 0; d < r.Intn(maxDeps+1); d++ {
						pick := i + 1 + r.Intn(len(names)-i-1)
						req[names[pick]] = "*"
					}
				}
			}
			vs = append(vs, testlookup.Pkg(n, fmt.Sprintf("1.%d.0", v), req))
		}
		out[n] = vs
	}
	return out
}

func keysOf(m map[string][]registry.PackageVersion) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
```

- [ ] **Step 2: Run the property test**

Run: `go test ./internal/resolver/... -run TestPropertyRandom -v`

Expected: 25 sub-tests PASS.

If any seed fails:

1. The test name shows the seed; fix the corresponding bug or adjust generator constraints.
2. Common failure mode: `chosen %s does not satisfy *` would indicate a `Satisfies` bug.
3. Common failure mode: missing transitive package indicates `decide` or `propagate` not seeding correctly.

- [ ] **Step 3: Commit**

```bash
git add internal/resolver
git commit -m "test(resolver): property test for random satisfiable graphs"
```

---

## Task 14: Vet, race detector, full test sweep

**Files:**
- (none — verification only)

- [ ] **Step 1: Run go vet**

Run: `go vet ./...`

Expected: no output (clean).

- [ ] **Step 2: Run with race detector**

Run: `go test -race ./internal/resolver/...`

Expected: PASS with no races.

- [ ] **Step 3: Run full test sweep**

Run: `go test ./...`

Expected: PASS for everything in `manifest`, `constraint`, `lock`, `cache`, `registry`, `resolver`.

- [ ] **Step 4: If anything failed, fix and re-commit**

Each fix is a separate commit; do not amend earlier commits.

- [ ] **Step 5: No-op commit not required**

If everything passes, this task produces no commit.

---

## Plan 3 acceptance check

- `go test ./...` is green.
- `go test -race ./internal/resolver/...` is green.
- `go vet ./...` is clean.
- The PubGrub solver returns a deterministic `Result` for a known-good graph (`monolog/monolog ^3.0` style trees).
- The solver returns a `*ConflictError` (verifiable via `errors.As`) when given a contradictory manifest (e.g., `a/a` requires `b/b ^1`, `c/c` requires `b/b ^2`, with both `a/a` and `c/c` in `require`).
- Repeating a Solve through `CachedSolver.Solve` with identical `(manifestHash, lockHash, platformFingerprint, IncludeDev, MinimumStability)` triggers ZERO calls into the wrapped `registry.SourceLookup` (verified by the counting-lookup test).
- The exported types `Term`, `Incompatibility`, `Cause`, `PartialSolution`, `Result`, `ResolvedPackage`, `ConflictError`, `Input`, `Solve`, `CachedSolver`, `NewCachedSolver`, `ToLockPackages` are stable enough for plans 4–6 to depend on without changes.
- The adapter `ToLockPackages(*Result) ([]lock.Package, []lock.Package)` produces lockfile-shaped data with `Source`/`Dist` populated from the registry record (not yet checksum-verified — that's the orchestrator's job in a later plan).

If any of these checks fails, fix forward in a follow-up commit before declaring Plan 3 done.

---

## Explicit deferrals (deliberately NOT in Plan 3)

These are flagged so future readers do not assume they were forgotten:

- **Stability flag handling beyond `minimum-stability`.** Per-package stability flags (`"foo/bar": "1.0@beta"`) are out of scope. The resolver respects only the manifest-level `minimum-stability`. Per-package flags become a follow-up after the install path lands.
- **Branch aliases (`dev-main as 1.x-dev`).** The constraint parser in Plan 1 records the raw alias text but the resolver does not yet collapse aliased dev branches into numeric ranges. Implemented in a stage-2 follow-up alongside VCS metadata.
- **`provide` / `replace` / `conflict` keywords.** None are honored. A package that *provides* another (e.g., a `psr/log-implementation`) will not be matched against requires of the provided name. A package that *conflicts* with another will be installed alongside it. These three keywords are tracked for stage-2 work.
- **Platform constraint enforcement.** `php`, `php-64bit`, `hhvm`, `ext-*`, `lib-*` requires are SILENTLY SKIPPED in stage 1. Plan 6 (platform detection) wires them in as a separate concern; until then they are documented in the lockfile but not validated against the runtime PHP.
- **"Stay close to lock" preference.** When `Input.Lock` is non-nil, stage 1 ignores it. Stage 3 uses it as a tiebreaker so `composer-go install` honors lockfile pins; for now `install` and `update` produce the same answer for the same manifest.
- **Suggestion propagation into the result.** `Suggest` from registry records is dropped; the orchestrator can re-read it from the manifest when rendering post-install suggestions.
- **PubGrub Relation refinement using probe versions.** `Term.Relation` always returns `Overlapping` when neither term is decided, which is sound but slower than necessary. Stage 3 introduces probe-version refinement for fewer wasted propagation steps.
