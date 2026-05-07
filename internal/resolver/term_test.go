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
