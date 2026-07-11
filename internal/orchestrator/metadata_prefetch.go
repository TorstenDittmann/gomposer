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
	"github.com/torstendittmann/gomposer/internal/manifest"
	"github.com/torstendittmann/gomposer/internal/platform"
	"github.com/torstendittmann/gomposer/internal/registry"
)

// MetadataPrefetcher is the runtime handle returned by
// maybeStartMetadataPrefetch. Callers Wait() unconditionally at the end
// of the pipeline; the noop variant makes that safe.
//
// Intended sequence:
//   - Wait() alone — let the pool run to completion, then drain.
//   - Cancel() then Wait() — abort in-flight lookups (they observe
//     ctx.Err() and count as errors, not warmed), then drain quickly.
//   - Wait() alone on a noop — returns immediately.
//
// Cancel is idempotent and safe on a noop instance.
type MetadataPrefetcher struct {
	// wg tracks every dispatched Lookup — both the initial warm set and
	// anything later added via Add() — via one Add(1)/Done() pair per
	// name. This is deliberately NOT errgroup's own Wait(): g.Wait()
	// belongs to a single internal sync.WaitGroup, and calling g.Go()
	// (from Add(), on an arbitrary caller goroutine) concurrently with a
	// g.Wait() call already in flight (from the one-shot initial-batch
	// dispatch below) is undefined per sync.WaitGroup's contract — it
	// doesn't require caller misuse to trigger, since the initial batch's
	// Wait can complete on its own schedule, independent of when Add() is
	// called. Tracking completion on our own wg instead, with one
	// Add(1)/Done() per dispatched name, sidesteps that: g is kept only
	// for its concurrency-limiting semaphore (SetLimit), never Wait()ed.
	wg        sync.WaitGroup
	stats     prefetchStats
	statsOnce sync.Once
	cancel    context.CancelFunc
	start     time.Time

	// Hoisted from the maybeStart goroutine so Add() can reuse them.
	// Zero-valued on the noop instance; Add short-circuits on g == nil.
	g       *errgroup.Group
	ctx     context.Context
	source  registry.SourceLookup
	wsNames map[string]struct{}
	seenMu  sync.Mutex
	seen    map[string]struct{}
}

// prefetchStats records the outcome the verbose timing block surfaces.
// Failed lookups are intentionally not counted — the resolver's on-demand
// Lookup is authoritative and will retry; a "pool error count" would be
// misleading (a cancelled prefetch inflates it without any real problem).
type prefetchStats struct {
	mu       sync.Mutex
	warmed   int
	duration time.Duration
}

// Wait blocks until every dispatched Lookup returns — both the initial
// warm set and anything added via Add(). Safe to call on a noop instance
// (constructed via newNoopMetadataPrefetcher). Callers that want to abort
// work in flight should Cancel() before Wait().
//
// Callers must not call Add() concurrently with (or after) Wait(): Wait
// only drains work dispatched-before it returns, and — like any
// sync.WaitGroup-backed pool — racing a new Add against the final Wait
// is unsupported. In the pipeline's actual usage this holds naturally:
// Add() is driven synchronously by the resolver while it runs, and Wait()
// is called once, after resolution has returned.
func (p *MetadataPrefetcher) Wait() {
	p.wg.Wait()
	if p.g == nil {
		return
	}
	if p.cancel != nil {
		p.cancel() // idempotent; releases cancelCtx's resources now that everything has drained
	}
	p.statsOnce.Do(func() {
		p.stats.mu.Lock()
		p.stats.duration = time.Since(p.start)
		p.stats.mu.Unlock()
	})
}

// Cancel signals every in-flight Lookup to abort. Idempotent; safe to
// call on a noop instance and safe to call after the pool has already
// completed. Callers typically pair Cancel() with a subsequent Wait() so
// the goroutine can drain before the pipeline returns.
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
	// Workspace names are local — never fetched from a registry, whether
	// they appear in the aggregate manifest's requires (they don't; workspace:
	// entries are stripped) or in a prior run's lockfile (they do, as
	// synthetic type=workspace entries).
	wsNames := map[string]struct{}{}
	for _, w := range ps.workspaces {
		wsNames[w.Name] = struct{}{}
	}
	seen := map[string]struct{}{}
	add := func(name string) {
		if platform.IsPlatformReq(name) {
			return
		}
		if _, ok := wsNames[name]; ok {
			return
		}
		seen[name] = struct{}{}
	}
	agg := ps.aggregateManifest
	if agg == nil {
		agg = ps.manifest
	}
	for name := range agg.Require {
		add(name)
	}
	if includeDev {
		for name := range agg.RequireDev {
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
	g.SetLimit(workerCount(opts.Workers))

	// Seed the seen set with the initial warm set so discovery-driven
	// Add() calls don't re-enqueue what A/B already fired.
	seen := make(map[string]struct{}, len(names))
	for _, n := range names {
		seen[n] = struct{}{}
	}

	// Workspace name set for Add's filter. collectMetadataPrefetchNames
	// already excluded workspace names from the initial set, but
	// discovery-driven callers pass arbitrary require maps.
	wsNames := workspaceNameSet(ps.workspaces)

	p := &MetadataPrefetcher{
		cancel:  cancel,
		start:   time.Now(),
		g:       g,
		ctx:     gctx,
		source:  opts.Source,
		wsNames: wsNames,
		seen:    seen,
	}
	// Reserve the initial batch's count on p.wg synchronously, before p is
	// returned to the caller. This matters: a caller may call Wait()
	// immediately after this constructor returns, and if the count were
	// instead registered lazily from inside the goroutine below, that
	// Wait() could race the same way Add() used to race g.Wait() — an
	// Add(1) landing concurrently with a Wait() while the counter is
	// momentarily zero. Pre-reserving here means the counter is already
	// len(names) (> 0, since the len(names) == 0 case returned above) by
	// the time anyone outside this function can observe p.
	p.wg.Add(len(names))
	// Dispatching the actual g.Go() calls happens on its own goroutine so
	// this constructor still returns immediately even when len(names)
	// exceeds the worker limit — g.Go() blocks the calling goroutine once
	// its semaphore is full, and the resolver must not stall on that.
	go func() {
		for _, name := range names {
			p.dispatch(name)
		}
	}()
	return p
}

// dispatch fires a single Lookup for name through the shared errgroup
// (bounding concurrency via its semaphore). The caller must have already
// registered the name on p.wg via p.wg.Add(1) (or, for a batch, via a
// single p.wg.Add(len(batch)) upfront) so Wait() drains it — regardless
// of whether it came from the initial warm set or from a later Add()
// call.
func (p *MetadataPrefetcher) dispatch(name string) {
	p.g.Go(func() error {
		defer p.wg.Done()
		if _, err := p.source.Lookup(p.ctx, name); err != nil {
			return nil // errors do not propagate — resolver's on-demand Lookup is authoritative
		}
		p.stats.mu.Lock()
		p.stats.warmed++
		p.stats.mu.Unlock()
		return nil
	})
}

// Add enqueues additional names into the running metadata prefetch pool.
// Safe to call from multiple goroutines. Names that fail the workspace /
// platform filter or were already enqueued (either as part of the
// initial warm set or by a prior Add) are silently dropped. Returns
// without waiting for the lookups to complete — but may briefly block
// the caller when the pool's concurrency limit is saturated (the
// errgroup's SetLimit backpressure). Callers should still Wait on the
// prefetcher at the end of the pipeline to drain the pool.
//
// Safe to call on a noop instance (constructed via
// newNoopMetadataPrefetcher): it's a no-op.
func (p *MetadataPrefetcher) Add(names []string) {
	if p == nil || p.g == nil {
		return
	}
	for _, name := range names {
		if _, ok := p.wsNames[name]; ok {
			continue
		}
		if platform.IsPlatformReq(name) {
			continue
		}
		p.seenMu.Lock()
		if _, dup := p.seen[name]; dup {
			p.seenMu.Unlock()
			continue
		}
		p.seen[name] = struct{}{}
		p.seenMu.Unlock()

		p.wg.Add(1)
		p.dispatch(name)
	}
}

// workspaceNameSet returns a lookup set of workspace names, or an empty
// non-nil map when the project has no workspaces. Used by Add() to
// filter out workspace names surfaced through OnVersionDecided.
func workspaceNameSet(ws []manifest.Workspace) map[string]struct{} {
	out := make(map[string]struct{}, len(ws))
	for _, w := range ws {
		out[w.Name] = struct{}{}
	}
	return out
}
