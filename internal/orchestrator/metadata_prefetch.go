// Metadata prefetch — warms Packagist metadata (/p2/<name>.json) in the
// background so the resolver's synchronous Lookup calls hit warm cache.
// Symmetric with prefetch.go (artifact zips).
package orchestrator

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/torstendittmann/gomposer/internal/lock"
	"github.com/torstendittmann/gomposer/internal/platform"
)

// MetadataPrefetcher is the runtime handle returned by
// maybeStartMetadataPrefetch. Callers Wait() unconditionally at the end
// of the pipeline; the noop variant makes that safe.
type MetadataPrefetcher struct {
	wg     sync.WaitGroup
	stats  prefetchStats
	cancel context.CancelFunc
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

// Cancel signals every in-flight Lookup to abort. Safe to call even
// when prefetch is already complete or was constructed via
// newNoopMetadataPrefetcher (then it's a no-op).
func (p *MetadataPrefetcher) Cancel() {
	if p == nil || p.cancel == nil {
		return
	}
	p.cancel()
}

// Stats returns the number of packages successfully warmed and the pool's
// total wall-clock duration. Call only after Wait() has returned; on a noop
// instance (or one that never dispatched any work) it reports (0, 0).
func (p *MetadataPrefetcher) Stats() (warmed int, dur time.Duration) {
	if p == nil {
		return 0, 0
	}
	p.stats.mu.Lock()
	defer p.stats.mu.Unlock()
	return p.stats.warmed, p.stats.duration
}

// newNoopMetadataPrefetcher returns a zero-value MetadataPrefetcher whose
// Wait returns immediately. Used when metadata prefetch is disabled or
// there is nothing to warm.
func newNoopMetadataPrefetcher() *MetadataPrefetcher {
	return &MetadataPrefetcher{}
}

// collectMetadataPrefetchNames unions the manifest requires with the
// existing lock's package list, filters out platform reqs, and returns a
// deduplicated slice.
func collectMetadataPrefetchNames(ps *pipelineState, includeDev bool) []string {
	if ps == nil || ps.manifest == nil {
		return nil
	}
	seen := map[string]struct{}{}
	add := func(name string) {
		if platform.IsPlatformReq(name) {
			return
		}
		seen[name] = struct{}{}
	}
	for name := range ps.manifest.Require {
		add(name)
	}
	if includeDev {
		for name := range ps.manifest.RequireDev {
			add(name)
		}
	}
	if len(ps.lockBytes) > 0 {
		if lf, err := lock.Decode(ps.lockBytes); err == nil {
			for _, p := range lf.Packages {
				add(p.Name)
			}
			if includeDev {
				for _, p := range lf.PackagesDev {
					add(p.Name)
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	return out
}

// maybeStartMetadataPrefetch begins warming metadata caches for the
// upcoming resolve. Returns a noop *MetadataPrefetcher when prefetch is
// disabled or there is nothing to warm. Callers Wait() unconditionally.
func maybeStartMetadataPrefetch(ctx context.Context, ps *pipelineState, opts Options) *MetadataPrefetcher {
	if opts.NoMetadataPrefetch || opts.NoNetwork || opts.Source == nil {
		return newNoopMetadataPrefetcher()
	}
	names := collectMetadataPrefetchNames(ps, !opts.NoDev)
	if len(names) == 0 {
		return newNoopMetadataPrefetcher()
	}
	// A separately created cancellable context sits in front of
	// errgroup.WithContext so Cancel() can abort every in-flight Lookup
	// without waiting for a g.Go closure to return an error. Both are
	// constructed here, before the goroutine spawns, so the constructor can
	// hold a reference to cancel.
	cancelCtx, cancel := context.WithCancel(ctx)
	g, gctx := errgroup.WithContext(cancelCtx)
	p := &MetadataPrefetcher{cancel: cancel}
	p.wg.Add(1)
	start := time.Now()
	go func() {
		defer p.wg.Done()
		// Release cancelCtx's resources once the pool is done, whether it
		// finished naturally or was aborted via Cancel(). cancel is
		// idempotent, so this never races with an external Cancel() call.
		defer cancel()
		g.SetLimit(workerCount(opts.Workers))
		for _, name := range names {
			name := name
			g.Go(func() error {
				if _, err := opts.Source.Lookup(gctx, name); err != nil {
					p.stats.mu.Lock()
					p.stats.errors++
					p.stats.mu.Unlock()
					return nil // errors do not propagate
				}
				p.stats.mu.Lock()
				p.stats.warmed++
				p.stats.mu.Unlock()
				return nil
			})
		}
		_ = g.Wait()
		p.stats.mu.Lock()
		p.stats.duration = time.Since(start)
		p.stats.mu.Unlock()
	}()
	return p
}
