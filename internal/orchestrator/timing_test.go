package orchestrator

import (
	"testing"
	"time"
)

func TestTimingsBeginEndOrder(t *testing.T) {
	tt := NewTimings()
	tt.Begin("resolve")
	time.Sleep(2 * time.Millisecond)
	tt.End("resolve")

	tt.Begin("fetch")
	time.Sleep(1 * time.Millisecond)
	tt.End("fetch")

	phases := tt.Phases()
	if len(phases) != 2 {
		t.Fatalf("Phases len = %d, want 2", len(phases))
	}
	if phases[0].Name != "resolve" || phases[1].Name != "fetch" {
		t.Errorf("phase order = %v, want [resolve fetch]", phases)
	}
	if phases[0].Elapsed <= 0 {
		t.Errorf("resolve elapsed = %v, want >0", phases[0].Elapsed)
	}
}

func TestTimingsEndWithoutBeginIsNoop(t *testing.T) {
	tt := NewTimings()
	// Calling End without a matching Begin must not panic and must not
	// add a phase entry. Robustness matters because pipeline branches may
	// skip a phase entirely.
	tt.End("never-started")
	if len(tt.Phases()) != 0 {
		t.Errorf("phases = %d, want 0", len(tt.Phases()))
	}
}

func TestTimingsTotal(t *testing.T) {
	tt := NewTimings()
	tt.Begin("a")
	time.Sleep(2 * time.Millisecond)
	tt.End("a")
	tt.Begin("b")
	time.Sleep(2 * time.Millisecond)
	tt.End("b")

	total := tt.Total()
	if total < 4*time.Millisecond {
		t.Errorf("Total = %v, want >= 4ms", total)
	}
}
