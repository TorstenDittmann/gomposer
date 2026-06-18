package orchestrator

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// Phase is a single named span in the pipeline.
type Phase struct {
	Name    string
	Elapsed time.Duration
}

// Counters holds the small set of per-run numeric metrics rendered alongside
// the phase timings. Fields are owned by the orchestrator; callers update
// them under the Timings mutex via the dedicated Add* methods.
type Counters struct {
	PackagesResolved int
	PackagesFetched  int
	CacheHits        int   // package fetches served from the content store
	BytesDownloaded  int64 // bytes pulled over the network this run
}

// Timings records phase durations and run-wide counters for verbose output.
// It is safe for concurrent use; the fetcher callback fires from worker
// goroutines.
type Timings struct {
	mu           sync.Mutex
	starts       map[string]time.Time
	phases       []Phase
	counters     Counters
	scriptsTotal time.Duration
}

// NewTimings returns an empty Timings.
func NewTimings() *Timings {
	return &Timings{starts: make(map[string]time.Time)}
}

// Begin records the start time for the named phase. Repeated calls with the
// same name overwrite the previous start; phases are not nested.
func (t *Timings) Begin(name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.starts[name] = time.Now()
	t.mu.Unlock()
}

// End closes the named phase and appends an entry to the phase list. End
// without a matching Begin is a no-op so optional phases (scripts, write
// lock on a no-op run) do not require defensive code at every call site.
func (t *Timings) End(name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	start, ok := t.starts[name]
	if !ok {
		return
	}
	delete(t.starts, name)
	t.phases = append(t.phases, Phase{Name: name, Elapsed: time.Since(start)})
}

// Phases returns the recorded phases in the order they ended.
func (t *Timings) Phases() []Phase {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Phase, len(t.phases))
	copy(out, t.phases)
	return out
}

// Total is the sum of all phase elapsed times. We sum recorded phases rather
// than measuring wall time so concurrent overlap (e.g. prefetch) shows up
// honestly in the breakdown without inflating the total.
func (t *Timings) Total() time.Duration {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var sum time.Duration
	for _, p := range t.phases {
		sum += p.Elapsed
	}
	return sum
}

// Counters returns a copy of the current counters.
func (t *Timings) Counters() Counters {
	if t == nil {
		return Counters{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.counters
}

// SetPackagesResolved records the resolver result count.
func (t *Timings) SetPackagesResolved(n int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.counters.PackagesResolved = n
	t.mu.Unlock()
}

// AddFetch is the fetcher callback. fromCache=true means a warm store hit
// (no network); fromCache=false means we downloaded `bytes` bytes.
func (t *Timings) AddFetch(_ string, bytes int, fromCache bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.counters.PackagesFetched++
	if fromCache {
		t.counters.CacheHits++
	} else {
		t.counters.BytesDownloaded += int64(bytes)
	}
	t.mu.Unlock()
}

// AddScriptsTime accumulates time spent in lifecycle scripts. Multiple calls
// across the pipeline collapse into a single "scripts" phase entry; the
// final entry is materialized by FlushScripts at the end of the run.
func (t *Timings) AddScriptsTime(d time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.scriptsTotal += d
}

// FlushScripts appends the accumulated scripts time as a single phase entry.
// Called at the end of runFullPipeline so "scripts" lands in the breakdown
// in a stable position regardless of which lifecycle events fired.
func (t *Timings) FlushScripts() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.scriptsTotal > 0 {
		t.phases = append(t.phases, Phase{Name: "scripts", Elapsed: t.scriptsTotal})
		t.scriptsTotal = 0
	}
}

// Render writes a fixed-width breakdown to w. Output goes to stderr at the
// end of a verbose run.
func (t *Timings) Render(w io.Writer) {
	if t == nil {
		return
	}
	t.mu.Lock()
	phases := make([]Phase, len(t.phases))
	copy(phases, t.phases)
	c := t.counters
	t.mu.Unlock()

	fmt.Fprintln(w, "gomposer: timing")
	for _, p := range phases {
		annot := annotate(p.Name, c)
		if annot == "" {
			fmt.Fprintf(w, "  %-16s %5d ms\n", p.Name, p.Elapsed.Milliseconds())
		} else {
			fmt.Fprintf(w, "  %-16s %5d ms %s\n", p.Name, p.Elapsed.Milliseconds(), annot)
		}
	}
	var total time.Duration
	for _, p := range phases {
		total += p.Elapsed
	}
	fmt.Fprintf(w, "  -------- total   %5d ms\n", total.Milliseconds())
}

// annotate returns the parenthesized counter suffix for a phase, or "" if
// the phase has no counters attached.
func annotate(name string, c Counters) string {
	switch name {
	case "resolve":
		if c.PackagesResolved == 0 {
			return ""
		}
		return fmt.Sprintf("(%d packages)", c.PackagesResolved)
	case "fetch":
		if c.PackagesFetched == 0 {
			return ""
		}
		cold := c.PackagesFetched - c.CacheHits
		return fmt.Sprintf("(%d/%d cold, %d KB)", cold, c.PackagesFetched, c.BytesDownloaded/1024)
	}
	return ""
}
