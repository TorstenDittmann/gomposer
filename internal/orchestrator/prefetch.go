package orchestrator

import (
	"context"
	"runtime"

	"golang.org/x/sync/errgroup"

	"github.com/torstendittmann/composer-go/internal/lock"
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

// startPrefetch begins downloading every package in lf using f. Errors are
// intentionally swallowed: the resolver pass is the authoritative gate, and
// any genuine missing-package or network failure will surface there with
// the right error message.
//
// includeDev mirrors the orchestrator's `!opts.NoDev` flag so we don't waste
// bandwidth on require-dev packages that won't be installed.
//
// limit caps in-flight downloads. Pass <=0 for runtime.NumCPU().
func startPrefetch(ctx context.Context, lf *lock.File, f Fetcher, includeDev bool, limit int) *Prefetcher {
	if lf == nil || f == nil {
		return &Prefetcher{}
	}
	if limit <= 0 {
		limit = runtime.NumCPU()
	}

	pkgs := make([]lock.Package, 0, len(lf.Packages)+len(lf.PackagesDev))
	pkgs = append(pkgs, lf.Packages...)
	if includeDev {
		pkgs = append(pkgs, lf.PackagesDev...)
	}
	if len(pkgs) == 0 {
		return &Prefetcher{}
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(limit)
	for i := range pkgs {
		p := pkgs[i] // capture
		g.Go(func() error {
			// Swallow errors: prefetch is opportunistic. The resolver +
			// fetchAll is what surfaces problems. Returning an error here
			// would cancel the errgroup and abort sibling downloads we'd
			// happily have completed.
			_, _ = f.Fetch(gctx, p)
			return nil
		})
	}
	return &Prefetcher{wait: func() { _ = g.Wait() }}
}
