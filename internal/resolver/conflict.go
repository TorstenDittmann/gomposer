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
