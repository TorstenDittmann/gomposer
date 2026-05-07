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
