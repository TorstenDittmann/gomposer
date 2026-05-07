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
