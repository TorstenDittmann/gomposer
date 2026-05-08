package orchestrator

import (
	"bytes"
	"strings"
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

func TestTimingsRender(t *testing.T) {
	tt := NewTimings()
	// Synthesize phases without calling Begin/End so durations are
	// deterministic.
	tt.phases = []Phase{
		{Name: "read manifest", Elapsed: 1 * time.Millisecond},
		{Name: "resolve", Elapsed: 50 * time.Millisecond},
		{Name: "fetch", Elapsed: 200 * time.Millisecond},
		{Name: "materialize", Elapsed: 30 * time.Millisecond},
		{Name: "autoload", Elapsed: 10 * time.Millisecond},
		{Name: "scripts", Elapsed: 5 * time.Millisecond},
		{Name: "write lock", Elapsed: 2 * time.Millisecond},
	}
	tt.counters = Counters{
		PackagesResolved: 12,
		PackagesFetched:  12,
		CacheHits:        4,
		BytesDownloaded:  512 * 1024,
	}

	var buf bytes.Buffer
	tt.Render(&buf)
	got := buf.String()

	want := []string{
		"composer-go: timing",
		"read manifest        1 ms",
		"resolve             50 ms (12 packages)",
		"fetch              200 ms (8/12 cold, 512 KB)",
		"materialize         30 ms",
		"autoload            10 ms",
		"scripts              5 ms",
		"write lock           2 ms",
		"-------- total     298 ms",
	}
	for _, line := range want {
		if !strings.Contains(got, line) {
			t.Errorf("missing line %q in:\n%s", line, got)
		}
	}
}
