// Lock-driven speculative prefetch — "optimistic op 1" from the design
// spec. When a lockfile exists and we're on the install (not update) path,
// every package in the lock is dispatched to the production Fetcher in
// parallel with the resolver pass. On the common case the resolver agrees
// with the lock and fetchAll observes a fully warm store. On the rare case
// the resolver picks different versions, the speculative downloads are
// wasted bandwidth but their bytes still seed the content-addressed store
// for future runs.
//
// Errors from prefetch are intentionally swallowed: the resolver pass plus
// the authoritative fetchAll is what surfaces real failures with the right
// error message and the right stack frame. A failed prefetch never
// propagates to the user.
//
// Safety: the Fetcher is content-addressed by sha256, the on-disk store
// uses tmp-then-rename for atomicity, and concurrent Fetch calls for the
// same sha race only over the final os.Rename — both branches resolve to
// the same byte-for-byte file. See internal/store/store.go and
// internal/fetcher/fetcher.go for the underlying invariants.
package orchestrator

import (
	"context"
	"runtime"

	"golang.org/x/sync/errgroup"

	"github.com/torstendittmann/gomposer/internal/lock"
)

// Prefetcher is the runtime handle returned by startPrefetch. Callers Wait
// for the speculative downloads to finish at the latest opportunity (just
// before fetchAll). Wait never returns an error — prefetch is best-effort,
// and the resolver pass is what surfaces real failures.
type Prefetcher struct {
	wait func()
}

// Wait blocks until every speculative download has finished or the parent
// context has been cancelled. It is safe to call Wait more than once; the
// underlying errgroup.Wait is idempotent after first return.
func (p *Prefetcher) Wait() {
	if p == nil || p.wait == nil {
		return
	}
	p.wait()
}

// maybeStartPrefetch decides whether to kick off the speculative prefetch
// based on opts and the parsed pipeline state. It returns a non-nil
// *Prefetcher in every branch — callers can Wait() unconditionally without
// nil-checking. When prefetch is skipped, the returned Prefetcher's Wait
// is a no-op and the returned ticker is nil.
//
// When prefetch starts, maybeStartPrefetch opens the fetching progress
// phase (BeginFetch with the exact package count) and returns the shared
// fetchTicker that fetchAll must route its increments through. The phase
// MUST open before startPrefetch dispatches workers: a worker can
// complete a fetch (and tick) immediately, and a tick landing before
// BeginFetch would be zeroed by the renderer's beginPhase. The announced
// total is exact because prefetch only runs when the lockfile is trusted
// verbatim — fetchAll later receives exactly these packages.
//
// Skip conditions:
//   - forceResolve (update path): we have no reason to trust the lock.
//   - opts.NoNetwork: test-only flag; honour the no-network contract.
//   - opts.NoPrefetch: explicit user opt-out.
//   - len(ps.lockBytes) == 0: no lockfile to be confident in.
//   - lock.Decode fails: corrupt lock; fall back to resolver.
//   - opts.Fetcher == nil: defensive (defaultDeps wiring failure).
func maybeStartPrefetch(ctx context.Context, ps *pipelineState, opts Options, forceResolve bool) (*Prefetcher, *fetchTicker) {
	if forceResolve || opts.NoNetwork || opts.NoPrefetch {
		return &Prefetcher{}, nil
	}
	if len(ps.lockBytes) == 0 || opts.Fetcher == nil {
		return &Prefetcher{}, nil
	}
	lf, err := lock.Decode(ps.lockBytes)
	if err != nil {
		return &Prefetcher{}, nil
	}
	pkgs := prefetchPackages(lf, !opts.NoDev)
	if len(pkgs) == 0 {
		return &Prefetcher{}, nil
	}
	ticker := newFetchTicker(opts.Progress)
	ticker.prog.BeginFetch(len(pkgs))
	return startPrefetch(ctx, pkgs, opts.Fetcher, workerCount(opts.Workers), ticker.tick), ticker
}

// prefetchPackages builds the speculative download list from a decoded
// lock: production packages plus (optionally) dev packages, minus
// synthetic workspace entries, which have no dist to download. On the
// trusted-lockfile path this is exactly the set fetchAll later
// receives, so a progress total announced from this list is never
// revised.
func prefetchPackages(lf *lock.File, includeDev bool) []lock.Package {
	pkgs := make([]lock.Package, 0, len(lf.Packages)+len(lf.PackagesDev))
	pkgs = append(pkgs, lf.Packages...)
	if includeDev {
		pkgs = append(pkgs, lf.PackagesDev...)
	}
	return nonWorkspacePackages(pkgs)
}

// startPrefetch begins downloading every package in pkgs using f.
// Errors are intentionally swallowed: the resolver pass is the
// authoritative gate, and any genuine missing-package or network
// failure will surface there with the right error message.
//
// onFetched, when non-nil, fires after each successful speculative
// download (or warm-store hit) with the package's name and version.
// Failed fetches do not fire it — fetchAll retries those
// authoritatively and reports them through the same shared ticker.
// Called from prefetch worker goroutines; implementations must be
// concurrency-safe and cheap.
//
// limit caps in-flight downloads. Pass <=0 for runtime.NumCPU().
func startPrefetch(ctx context.Context, pkgs []lock.Package, f Fetcher, limit int, onFetched func(name, version string)) *Prefetcher {
	if len(pkgs) == 0 || f == nil {
		return &Prefetcher{}
	}
	if limit <= 0 {
		limit = runtime.NumCPU()
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(limit)

	// Dispatch goroutines from a background goroutine so startPrefetch returns
	// immediately. Without this, errgroup.SetLimit causes g.Go to block the
	// caller when the concurrency cap is reached — defeating the purpose of
	// starting downloads "in the background while the resolver runs".
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range pkgs {
			p := pkgs[i] // capture
			g.Go(func() error {
				// Swallow errors: prefetch is opportunistic. The resolver +
				// fetchAll is what surfaces problems. Returning an error here
				// would cancel the errgroup and abort sibling downloads we'd
				// happily have completed.
				_, err := f.Fetch(gctx, p)
				if err == nil && onFetched != nil {
					onFetched(p.Name, p.Version)
				}
				return nil
			})
		}
		_ = g.Wait()
	}()

	return &Prefetcher{wait: func() {
		<-done // wait for all g.Go calls + all goroutines to finish
	}}
}
