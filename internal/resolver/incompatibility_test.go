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
