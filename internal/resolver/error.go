package resolver

import (
	"fmt"

	"github.com/torstendittmann/gomposer/internal/constraint"
	"github.com/torstendittmann/gomposer/internal/registry"
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
	return RenderConflict(e.Root)
}

// collectLeafCauses walks the conflict tree and returns deduplicated
// human-readable descriptions of the underlying root causes (no-versions,
// unknown-package, dependency clashes, root requires), which is what users
// actually need to see — not the PubGrub derivation chain.
func collectLeafCauses(ic *Incompatibility) []string {
	seen := map[string]bool{}
	var out []string
	add := func(msg string) {
		if !seen[msg] {
			seen[msg] = true
			out = append(out, msg)
		}
	}
	var walk func(*Incompatibility)
	walk = func(ic *Incompatibility) {
		if ic == nil {
			return
		}
		switch c := ic.Cause.(type) {
		case CauseConflict:
			walk(c.Conflict)
			walk(c.Other)
		case CauseNoVersions:
			add(fmt.Sprintf("no published versions of %q satisfy the requested constraint", c.Package))
		case CauseUnknownPackage:
			add(fmt.Sprintf("package %q is not available from any configured registry", c.Package))
		case CauseDependency:
			add(fmt.Sprintf("dependency clash: %s requires %s but the chosen version cannot be reconciled", c.Depender, c.Dependee))
		case CauseRoot:
			// CauseRoot nodes are structural connector nodes in the conflict
			// tree. The derivation section already explains what the manifest
			// requires in context; emitting redundant bullets here adds noise
			// rather than clarity. Skip.
		}
	}
	walk(ic)
	return out
}

// termConstraintLabel renders a Term's constraint for human-readable error
// messages. Falls back to "*" when the constraint string is empty.
func termConstraintLabel(t Term) string {
	s := t.Constraint.Original
	if s == "" {
		s = "*"
	}
	if !t.Positive {
		return "(not " + s + ")"
	}
	return s
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
