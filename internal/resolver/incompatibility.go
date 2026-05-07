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
