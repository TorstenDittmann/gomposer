package orchestrator

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/torstendittmann/gomposer/internal/manifest"
	"github.com/torstendittmann/gomposer/internal/registry"
)

func TestNoopMetadataPrefetcherWaitReturnsImmediately(t *testing.T) {
	p := newNoopMetadataPrefetcher()
	// Wait must return immediately for the noop; a deadlock or panic here is
	// a bug. We rely on the test harness's default timeout to catch a hang.
	p.Wait()
}

// fakeSourceLookup records every Lookup call. Safe for concurrent use.
type fakeSourceLookup struct {
	mu    sync.Mutex
	calls map[string]int
}

func newFakeSourceLookup() *fakeSourceLookup {
	return &fakeSourceLookup{calls: map[string]int{}}
}

func (f *fakeSourceLookup) Lookup(_ context.Context, name string) (*registry.PackageMetadata, error) {
	f.mu.Lock()
	f.calls[name]++
	f.mu.Unlock()
	return &registry.PackageMetadata{Name: name}, nil
}

func (f *fakeSourceLookup) totalCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for _, n := range f.calls {
		total += n
	}
	return total
}

func TestCollectMetadataPrefetchNamesUnionsAndDedupes(t *testing.T) {
	ps := &pipelineState{
		manifest: &manifest.Manifest{
			Require:    map[string]string{"a/a": "^1", "b/b": "^1", "php": ">=8.1"},
			RequireDev: map[string]string{"d/d": "^1", "b/b": "^1"}, // b/b overlaps
		},
	}
	// no lock; expect just the manifest names minus php.
	got := collectMetadataPrefetchNames(ps, true /* includeDev */)
	sort.Strings(got)
	want := []string{"a/a", "b/b", "d/d"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestCollectMetadataPrefetchNamesRespectsNoDev(t *testing.T) {
	ps := &pipelineState{
		manifest: &manifest.Manifest{
			Require:    map[string]string{"a/a": "^1"},
			RequireDev: map[string]string{"d/d": "^1"},
		},
	}
	got := collectMetadataPrefetchNames(ps, false /* includeDev */)
	if len(got) != 1 || got[0] != "a/a" {
		t.Errorf("got %v, want just a/a", got)
	}
}

func TestCollectMetadataPrefetchNamesFiltersPlatformReqs(t *testing.T) {
	ps := &pipelineState{
		manifest: &manifest.Manifest{
			Require: map[string]string{
				"a/a":      "^1",
				"php":      ">=8.1",
				"ext-json": "*",
				"lib-curl": "*",
			},
		},
	}
	got := collectMetadataPrefetchNames(ps, true)
	if len(got) != 1 || got[0] != "a/a" {
		t.Errorf("got %v, want just a/a", got)
	}
}

func TestMaybeStartMetadataPrefetchWarmsUniqueNames(t *testing.T) {
	src := newFakeSourceLookup()
	ps := &pipelineState{
		manifest: &manifest.Manifest{
			Require: map[string]string{"a/a": "^1", "b/b": "^1"},
		},
	}
	opts := Options{Source: src}
	p := maybeStartMetadataPrefetch(context.Background(), ps, opts)
	p.Wait()
	if got := src.totalCalls(); got != 2 {
		t.Errorf("Lookup called %d times, want 2", got)
	}
}

func TestMaybeStartMetadataPrefetchNoopWhenDisabled(t *testing.T) {
	src := newFakeSourceLookup()
	ps := &pipelineState{
		manifest: &manifest.Manifest{
			Require: map[string]string{"a/a": "^1"},
		},
	}
	opts := Options{Source: src, NoMetadataPrefetch: true}
	p := maybeStartMetadataPrefetch(context.Background(), ps, opts)
	p.Wait()
	if got := src.totalCalls(); got != 0 {
		t.Errorf("Lookup called %d times, want 0", got)
	}
}

func TestMaybeStartMetadataPrefetchNoopOnNoNetwork(t *testing.T) {
	src := newFakeSourceLookup()
	ps := &pipelineState{
		manifest: &manifest.Manifest{
			Require: map[string]string{"a/a": "^1"},
		},
	}
	opts := Options{Source: src, NoNetwork: true}
	p := maybeStartMetadataPrefetch(context.Background(), ps, opts)
	p.Wait()
	if got := src.totalCalls(); got != 0 {
		t.Errorf("Lookup called %d times under NoNetwork, want 0", got)
	}
}

func TestMaybeStartMetadataPrefetchConcurrentCallsToSameName(t *testing.T) {
	// Simulate a real-world dedup guarantee: even if the manifest and lock
	// mention the same package, Lookup is called once per unique name.
	// (The pool itself is not required to be fully-parallel; the assertion
	// is on call *count*, not concurrency.)
	var counter atomic.Int32
	src := &countingSourceLookup{onLookup: func() { counter.Add(1) }}
	ps := &pipelineState{
		manifest: &manifest.Manifest{
			Require: map[string]string{"a/a": "^1", "b/b": "^1"},
			// b/b appears in both — count must still be 2.
			RequireDev: map[string]string{"b/b": "^1", "c/c": "^1"},
		},
	}
	opts := Options{Source: src}
	p := maybeStartMetadataPrefetch(context.Background(), ps, opts)
	p.Wait()
	if got := counter.Load(); got != 3 {
		t.Errorf("Lookup called %d times, want 3", got)
	}
}

type countingSourceLookup struct {
	onLookup func()
}

func (c *countingSourceLookup) Lookup(_ context.Context, name string) (*registry.PackageMetadata, error) {
	c.onLookup()
	return &registry.PackageMetadata{Name: name}, nil
}

// TestMetadataPrefetcherCancelStopsPool asserts that Cancel() aborts
// in-flight Lookup calls promptly and that none of them are counted as
// warmed — only a genuinely successful Lookup should increment
// stats.warmed.
func TestMetadataPrefetcherCancelStopsPool(t *testing.T) {
	started := make(chan struct{}, 3)
	src := &blockingSourceLookup{
		onStart: func() {
			select {
			case started <- struct{}{}:
			default:
			}
		},
	}
	ps := &pipelineState{
		manifest: &manifest.Manifest{
			Require: map[string]string{"a/a": "^1", "b/b": "^1", "c/c": "^1"},
		},
	}
	opts := Options{Source: src}
	p := maybeStartMetadataPrefetch(context.Background(), ps, opts)
	<-started // wait for at least one worker to enter Lookup
	p.Cancel()
	p.Wait()

	// Even if some workers were mid-Lookup, cancel should have short-
	// circuited them. Assert no calls counted as warmed.
	warmed, _ := p.Stats()
	if warmed != 0 {
		t.Errorf("cancelled prefetch reported %d warmed, want 0", warmed)
	}
}

// TestMetadataPrefetcherCancelIsSafeOnNoop asserts that Cancel() on a noop
// instance (prefetch disabled, or nothing to warm) is a harmless no-op.
func TestMetadataPrefetcherCancelIsSafeOnNoop(t *testing.T) {
	p := newNoopMetadataPrefetcher()
	p.Cancel() // must not panic
	p.Wait()
}

func TestMaybeStartMetadataPrefetchSeedsSeenWithInitialWarmSet(t *testing.T) {
	// After maybeStart returns, every name in the initial warm set must
	// be present in seen so a future Add doesn't re-enqueue them.
	src := newFakeSourceLookup()
	ps := &pipelineState{
		manifest: &manifest.Manifest{
			Require: map[string]string{"a/a": "^1", "b/b": "^1"},
		},
	}
	opts := Options{Source: src}
	p := maybeStartMetadataPrefetch(context.Background(), ps, opts)
	p.Wait()

	p.seenMu.Lock()
	defer p.seenMu.Unlock()
	if _, ok := p.seen["a/a"]; !ok {
		t.Errorf("seen missing a/a: %v", p.seen)
	}
	if _, ok := p.seen["b/b"]; !ok {
		t.Errorf("seen missing b/b: %v", p.seen)
	}
}

// blockingSourceLookup blocks every Lookup call until its context is
// cancelled, then reports ctx.Err(). Used to assert that Cancel() actually
// unblocks in-flight prefetch workers rather than merely being ignored.
type blockingSourceLookup struct {
	onStart func()
}

func (b *blockingSourceLookup) Lookup(ctx context.Context, _ string) (*registry.PackageMetadata, error) {
	if b.onStart != nil {
		b.onStart()
	}
	<-ctx.Done()
	return nil, ctx.Err()
}
