package fetcher

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/torstendittmann/composer-go/internal/registry"
)

// FetchAll downloads every pv in parallel with at most `limit` requests in
// flight at once. A `limit` of 0 or negative means runtime.NumCPU().
//
// On the first error, FetchAll cancels the underlying context and returns
// that error; in-flight downloads abort via the cancelled context. Already-
// completed downloads remain in the store — they're idempotent by sha.
func (f *Fetcher) FetchAll(ctx context.Context, pvs []registry.PackageVersion, limit int) error {
	if limit <= 0 {
		limit = runtime.NumCPU()
	}
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(limit)
	for _, pv := range pvs {
		pv := pv
		g.Go(func() error { return f.Fetch(ctx, pv) })
	}
	return g.Wait()
}

// MaterializeAll expands every pv into vendorRoot/<vendor>/<name>/ in
// parallel with at most `limit` extractions running concurrently. Each pv
// must already be present in the store (call FetchAll first).
func (f *Fetcher) MaterializeAll(ctx context.Context, pvs []registry.PackageVersion, vendorRoot string, limit int) error {
	if limit <= 0 {
		limit = runtime.NumCPU()
	}
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(limit)
	for _, pv := range pvs {
		pv := pv
		g.Go(func() error {
			target := vendorTargetFor(vendorRoot, pv.Name)
			return f.Materialize(ctx, pv, target)
		})
	}
	return g.Wait()
}

// vendorTargetFor maps "vendor/pkg" to "<vendorRoot>/vendor/pkg". Composer
// package names are always "<vendor>/<name>" with a single slash.
func vendorTargetFor(vendorRoot, name string) string {
	parts := strings.SplitN(name, "/", 2)
	if len(parts) == 2 {
		return filepath.Join(vendorRoot, parts[0], parts[1])
	}
	return filepath.Join(vendorRoot, name)
}
