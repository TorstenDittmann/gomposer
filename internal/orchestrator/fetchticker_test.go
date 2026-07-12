package orchestrator

import (
	"sync"
	"testing"
)

// countingFetchProgress records IncFetch labels; every other Progress
// method is an inherited noop.
type countingFetchProgress struct {
	noopProgress
	mu     sync.Mutex
	labels []string
}

func (c *countingFetchProgress) IncFetch(label string) {
	c.mu.Lock()
	c.labels = append(c.labels, label)
	c.mu.Unlock()
}

func TestFetchTickerDedupsByName(t *testing.T) {
	prog := &countingFetchProgress{}
	ft := newFetchTicker(prog)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ft.tick("vendor/a", "1.0.0")
			ft.tick("vendor/b", "2.0.0")
		}()
	}
	wg.Wait()

	prog.mu.Lock()
	defer prog.mu.Unlock()
	if len(prog.labels) != 2 {
		t.Fatalf("IncFetch fired %d times, want 2 (deduped); labels=%v", len(prog.labels), prog.labels)
	}
	want := map[string]bool{"vendor/a 1.0.0": true, "vendor/b 2.0.0": true}
	for _, l := range prog.labels {
		if !want[l] {
			t.Errorf("unexpected label %q", l)
		}
	}
}

func TestFetchTickerNilProgressIsSafe(t *testing.T) {
	ft := newFetchTicker(nil)
	ft.tick("vendor/a", "1.0.0") // must not panic
}
