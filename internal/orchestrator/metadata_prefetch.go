// Metadata prefetch — warms Packagist metadata (/p2/<name>.json) in the
// background so the resolver's synchronous Lookup calls hit warm cache.
// Symmetric with prefetch.go (artifact zips).
package orchestrator

import (
	"context"
	"sync"
	"time"
)

// MetadataPrefetcher is the runtime handle returned by
// maybeStartMetadataPrefetch. Callers Wait() unconditionally at the end
// of the pipeline; the noop variant makes that safe.
type MetadataPrefetcher struct {
	wg    sync.WaitGroup
	stats prefetchStats
}

// prefetchStats records outcome for the verbose timing block. Populated
// atomically by worker goroutines in Task 2.
type prefetchStats struct {
	mu       sync.Mutex
	warmed   int
	errors   int
	duration time.Duration
}

// Wait blocks until every worker goroutine returns. Safe to call on a
// noop instance (constructed via newNoopMetadataPrefetcher).
func (p *MetadataPrefetcher) Wait() {
	p.wg.Wait()
}

// newNoopMetadataPrefetcher returns a zero-value MetadataPrefetcher whose
// Wait returns immediately. Used when metadata prefetch is disabled or
// there is nothing to warm.
func newNoopMetadataPrefetcher() *MetadataPrefetcher {
	return &MetadataPrefetcher{}
}

// unused imports guard for later tasks; remove when Task 2 uses them.
var _ = context.Background
