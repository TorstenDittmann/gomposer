package main

import (
	"testing"
	"time"
)

func TestScenarioStringRoundtrips(t *testing.T) {
	for _, s := range []Scenario{ScenarioCold, ScenarioWarm, ScenarioLockUnchanged} {
		got, err := ParseScenario(string(s))
		if err != nil {
			t.Fatalf("ParseScenario(%q): %v", s, err)
		}
		if got != s {
			t.Errorf("ParseScenario(%q) = %q", s, got)
		}
	}
}

func TestParseScenarioRejectsUnknown(t *testing.T) {
	if _, err := ParseScenario("frozen"); err == nil {
		t.Error("expected error on unknown scenario")
	}
}

func TestMedianOddCount(t *testing.T) {
	got := median([]time.Duration{3 * time.Second, 1 * time.Second, 2 * time.Second})
	if got != 2*time.Second {
		t.Errorf("median = %v, want 2s", got)
	}
}

func TestMedianEvenCount(t *testing.T) {
	got := median([]time.Duration{4 * time.Second, 1 * time.Second, 2 * time.Second, 3 * time.Second})
	// median of even-count sample = mean of the two middle values: (2+3)/2 = 2.5s
	if got != 2500*time.Millisecond {
		t.Errorf("median = %v, want 2.5s", got)
	}
}

func TestMedianEmptyReturnsZero(t *testing.T) {
	if got := median(nil); got != 0 {
		t.Errorf("median(nil) = %v, want 0", got)
	}
}
