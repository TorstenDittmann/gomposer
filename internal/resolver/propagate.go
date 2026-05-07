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
