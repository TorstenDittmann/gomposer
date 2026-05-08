package orchestrator

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/torstendittmann/composer-go/internal/lock"
)

// recordingFetcher records every Fetch call. Optionally sleeps `delay` so
// tests can observe race ordering against the resolver.
type recordingFetcher struct {
	mu    sync.Mutex
	calls []string
	delay time.Duration
	// failNext, when non-nil and called, reports an error from Fetch. We
	// still record the call before invoking it so tests can assert that
	// errors from prefetch don't leak.
	fail func(name string) error
	// concurrent tracks how many goroutines are currently inside Fetch.
	concurrent    atomic.Int32
	maxConcurrent atomic.Int32
}

func (r *recordingFetcher) Fetch(ctx context.Context, pkg lock.Package) (string, error) {
	now := r.concurrent.Add(1)
	defer r.concurrent.Add(-1)
	for {
		peak := r.maxConcurrent.Load()
		if now <= peak || r.maxConcurrent.CompareAndSwap(peak, now) {
			break
		}
	}
	r.mu.Lock()
	r.calls = append(r.calls, pkg.Name)
	r.mu.Unlock()
	if r.delay > 0 {
		select {
		case <-time.After(r.delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if r.fail != nil {
		if err := r.fail(pkg.Name); err != nil {
			return "", err
		}
	}
	return "sha-" + pkg.Name, nil
}

func (r *recordingFetcher) Calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	copy(out, r.calls)
	return out
}

func TestPrefetcherFiresOneCallPerPackage(t *testing.T) {
	f := &recordingFetcher{}
	lf := &lock.File{
		Packages: []lock.Package{
			{Name: "v/a"}, {Name: "v/b"}, {Name: "v/c"},
		},
		PackagesDev: []lock.Package{
			{Name: "v/d"},
		},
	}
	pf := startPrefetch(context.Background(), lf, f, true /* includeDev */, 4)
	pf.Wait()

	got := f.Calls()
	if len(got) != 4 {
		t.Fatalf("calls = %d, want 4 (3 prod + 1 dev)", len(got))
	}
	want := map[string]bool{"v/a": true, "v/b": true, "v/c": true, "v/d": true}
	for _, name := range got {
		if !want[name] {
			t.Errorf("unexpected fetch %q", name)
		}
	}
}

func TestPrefetcherSkipsDevWhenAsked(t *testing.T) {
	f := &recordingFetcher{}
	lf := &lock.File{
		Packages:    []lock.Package{{Name: "v/a"}},
		PackagesDev: []lock.Package{{Name: "v/d"}},
	}
	pf := startPrefetch(context.Background(), lf, f, false /* includeDev */, 4)
	pf.Wait()
	if len(f.Calls()) != 1 {
		t.Errorf("calls = %v, want only [v/a]", f.Calls())
	}
}

func TestPrefetcherSwallowsErrors(t *testing.T) {
	f := &recordingFetcher{
		fail: func(name string) error {
			if name == "v/b" {
				return context.DeadlineExceeded // any error
			}
			return nil
		},
	}
	lf := &lock.File{
		Packages: []lock.Package{{Name: "v/a"}, {Name: "v/b"}, {Name: "v/c"}},
	}
	// Wait must NOT return an error — prefetch is best-effort.
	pf := startPrefetch(context.Background(), lf, f, false, 4)
	pf.Wait()
	if len(f.Calls()) != 3 {
		t.Errorf("expected all 3 packages attempted, got %v", f.Calls())
	}
}

func TestPrefetcherCapsConcurrency(t *testing.T) {
	f := &recordingFetcher{delay: 25 * time.Millisecond}
	pkgs := make([]lock.Package, 16)
	for i := range pkgs {
		pkgs[i] = lock.Package{Name: "v/p" + string(rune('a'+i))}
	}
	lf := &lock.File{Packages: pkgs}
	pf := startPrefetch(context.Background(), lf, f, false, 3)
	pf.Wait()
	if peak := f.maxConcurrent.Load(); peak > 3 {
		t.Errorf("maxConcurrent = %d, want <= 3", peak)
	}
}

func TestPrefetcherRespectsContextCancel(t *testing.T) {
	f := &recordingFetcher{delay: 200 * time.Millisecond}
	pkgs := make([]lock.Package, 8)
	for i := range pkgs {
		pkgs[i] = lock.Package{Name: "v/slow" + string(rune('a'+i))}
	}
	lf := &lock.File{Packages: pkgs}
	ctx, cancel := context.WithCancel(context.Background())
	pf := startPrefetch(ctx, lf, f, false, 4)
	cancel()
	done := make(chan struct{})
	go func() {
		pf.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after context cancel")
	}
}

// TestPrefetchRacesResolver: every locked package's Fetch fires before the
// (simulated slow) resolver returns. Directional assertion, not a tight
// deadline — bump the sleep if flaky in CI.
func TestPrefetchRacesResolver(t *testing.T) {
	f := &recordingFetcher{delay: 5 * time.Millisecond}
	lf := &lock.File{Packages: []lock.Package{
		{Name: "v/a"}, {Name: "v/b"}, {Name: "v/c"}, {Name: "v/d"},
	}}
	pf := startPrefetch(context.Background(), lf, f, false, 4)

	// Simulate slow resolver. With 4 workers @ 5ms each, all 4 Fetches
	// comfortably complete inside this window.
	time.Sleep(100 * time.Millisecond)

	got := f.Calls()
	want := map[string]bool{"v/a": true, "v/b": true, "v/c": true, "v/d": true}
	seen := 0
	for _, n := range got {
		if want[n] {
			seen++
		}
	}
	if seen < len(want) {
		t.Fatalf("prefetch did not see all 4 packages by 'resolver finish'; calls = %v", got)
	}
	pf.Wait()
}
