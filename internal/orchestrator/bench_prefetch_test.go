package orchestrator

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/torstendittmann/composer-go/internal/lock"
)

// BenchmarkPrefetchVsNoPrefetch reports the wall-clock contribution of
// optimistic op 1 in isolation. Run with:
//
//	go test -bench=. -benchtime=10x ./internal/orchestrator/...
//
// Setup: 30-package lock.File, each backed by a content-addressed in-memory
// "fetcher" that sleeps perRequestDelay on a cold hit and returns instantly on
// a warm (already-seen sha256) hit. A simulated "resolver delay" of resolverDelay
// stands in for real Packagist RTT.
//
// With prefetch ON:
//   - startPrefetch dispatches all 30 downloads in background goroutines.
//   - The simulated resolver work runs concurrently (time.Sleep).
//   - prefetch.Wait() drains; by then most/all downloads have already completed.
//   - The fetchAll equivalent sees warm hits → near-zero latency.
//
// With prefetch OFF:
//   - The simulated resolver work runs serially first.
//   - fetchAll then does all 30 cold downloads.
//
// Expected speedup ≥ 1.5 on a workstation (actual value depends on parallelism
// and timing; this is a regression indicator, not a CI gate).
func BenchmarkPrefetchVsNoPrefetch(b *testing.B) {
	const numPackages = 30
	const perRequestDelay = 20 * time.Millisecond
	// resolverDelay simulates Packagist metadata RTT. With 8 workers @ 20ms,
	// ceil(30/8)*20ms ≈ 80ms. Setting resolver to 80ms means downloads should
	// complete during the resolver window with prefetch ON.
	const resolverDelay = 80 * time.Millisecond

	// Build 30 lock packages, each with a unique sha so they are distinct
	// cache entries.
	pkgs := make([]lock.Package, numPackages)
	for i := range pkgs {
		pkgs[i] = lock.Package{
			Name: fmt.Sprintf("vendor/p%02d", i),
			Dist: lock.Dist{
				Type:   "zip",
				URL:    fmt.Sprintf("http://bench/p%02d.zip", i),
				Sha256: fmt.Sprintf("sha256bench%02d", i),
			},
		}
	}
	lf := &lock.File{SchemaVersion: lock.SchemaVersion, Packages: pkgs}

	// runScenario directly exercises the prefetch/fetchAll interaction without
	// going through the full pipeline (which would require a live lockfile on
	// disk AND a resolver path that re-resolves — two mutually exclusive
	// conditions given the current pipeline design).
	//
	// This is a direct unit-level harness for the load-bearing claim of
	// optimistic op 1: "downloads overlap the resolver pass".
	runScenario := func(noPrefetch bool) time.Duration {
		f := &benchFetchImpl{delay: perRequestDelay}
		t0 := time.Now()

		var pf *Prefetcher
		if !noPrefetch {
			pf = startPrefetch(context.Background(), lf, f, true, 8)
		}

		// Simulate resolver: blocks for resolverDelay (Packagist RTT).
		time.Sleep(resolverDelay)

		if pf != nil {
			pf.Wait()
		}

		// fetchAll: with prefetch, all shas are warm; without, all are cold.
		if _, err := fetchAll(context.Background(), lf.Packages, f, 8); err != nil {
			b.Fatalf("fetchAll: %v", err)
		}

		return time.Since(t0)
	}

	var withPrefetch, withoutPrefetch time.Duration
	for i := 0; i < b.N; i++ {
		withPrefetch += runScenario(false)
		withoutPrefetch += runScenario(true)
	}
	b.ReportMetric(float64(withPrefetch.Milliseconds())/float64(b.N), "ms/op-with-prefetch")
	b.ReportMetric(float64(withoutPrefetch.Milliseconds())/float64(b.N), "ms/op-without-prefetch")
	if withPrefetch > 0 {
		b.ReportMetric(float64(withoutPrefetch)/float64(withPrefetch), "speedup")
	}
}

// benchFetchImpl is a Fetcher that simulates a content-addressed store:
// first Fetch for a given sha sleeps delay (cold download), subsequent calls
// for the same sha are instant (warm hit). Thread-safe.
type benchFetchImpl struct {
	mu    sync.Mutex
	warm  map[string]bool
	delay time.Duration
}

func (f *benchFetchImpl) Fetch(ctx context.Context, pkg lock.Package) (string, error) {
	sha := pkg.Dist.Sha256
	if sha == "" {
		sha = pkg.Name
	}
	f.mu.Lock()
	if f.warm == nil {
		f.warm = make(map[string]bool)
	}
	hot := f.warm[sha]
	f.mu.Unlock()
	if hot {
		return sha, nil // warm hit: instant
	}
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return "", ctx.Err()
	}
	f.mu.Lock()
	f.warm[sha] = true
	f.mu.Unlock()
	return sha, nil
}

func makeBenchZip(tb testing.TB) []byte {
	tb.Helper()
	return makeBenchZipNamed(tb, "vendor/p")
}

func makeBenchZipNamed(tb testing.TB, name string) []byte {
	tb.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create(name + "/composer.json")
	_, _ = w.Write([]byte(`{"name":"` + name + `"}`))
	_ = zw.Close()
	return buf.Bytes()
}

func sha256OfBench(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// writeFixtureLockWithDist writes a composer-go.lock where every package
// has a real Dist.URL and Dist.Sha256 pointing at the provided url and sha.
// This lets the production fetcher (used in the benchmark) actually attempt
// the download.
func writeFixtureLockWithDist(tb testing.TB, dir string, names []string, url, sha string) {
	tb.Helper()
	urls := make([]string, len(names))
	shas := make([]string, len(names))
	for i := range names {
		urls[i] = url
		shas[i] = sha
	}
	writeFixtureLockWithDistPerPkg(tb, dir, names, urls, shas)
}

// writeFixtureLockWithDistPerPkg writes a composer-go.lock where each package
// has an individually specified Dist.URL and Dist.Sha256.
func writeFixtureLockWithDistPerPkg(tb testing.TB, dir string, names, urls, shas []string) {
	tb.Helper()
	pkgs := make([]lock.Package, len(names))
	for i, n := range names {
		pkgs[i] = lock.Package{
			Name:    n,
			Version: "1.0.0",
			Dist:    lock.Dist{Type: "zip", URL: urls[i], Sha256: shas[i]},
		}
	}
	f := &lock.File{
		SchemaVersion: lock.SchemaVersion,
		Packages:      pkgs,
	}
	data, err := f.Encode()
	if err != nil {
		tb.Fatalf("writeFixtureLockWithDistPerPkg: encode: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "composer-go.lock"), data, 0o644); err != nil {
		tb.Fatalf("writeFixtureLockWithDistPerPkg: write: %v", err)
	}
}
