package orchestrator

import "sync"

// fetchTicker dedups IncFetch by package name so a package fetched
// speculatively by prefetch and re-verified warm by fetchAll ticks
// exactly once. Safe for concurrent use from prefetch worker
// goroutines and fetchAll worker goroutines simultaneously.
type fetchTicker struct {
	mu   sync.Mutex
	seen map[string]struct{}
	prog Progress
}

// newFetchTicker wraps prog (nil-safe via progressOrNoop) in a ticker.
// The caller that opens the fetching phase should do so via
// ticker.prog so Begin/Inc/End all route through the same instance.
func newFetchTicker(prog Progress) *fetchTicker {
	return &fetchTicker{seen: make(map[string]struct{}), prog: progressOrNoop(prog)}
}

// tick reports name as fetched exactly once; repeat calls for the same
// name are ignored. The rendered label matches fetchAll's existing
// format: "name version".
func (ft *fetchTicker) tick(name, version string) {
	ft.mu.Lock()
	if _, dup := ft.seen[name]; dup {
		ft.mu.Unlock()
		return
	}
	ft.seen[name] = struct{}{}
	ft.mu.Unlock()
	ft.prog.IncFetch(name + " " + version)
}
