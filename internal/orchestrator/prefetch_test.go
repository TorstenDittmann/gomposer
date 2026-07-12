package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/torstendittmann/gomposer/internal/lock"
	"github.com/torstendittmann/gomposer/internal/manifest"
	"github.com/torstendittmann/gomposer/internal/registry"
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
	pf := startPrefetch(context.Background(), prefetchPackages(lf, true), f, 4, nil)
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
	pf := startPrefetch(context.Background(), prefetchPackages(lf, false), f, 4, nil)
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
	pf := startPrefetch(context.Background(), prefetchPackages(lf, false), f, 4, nil)
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
	pf := startPrefetch(context.Background(), prefetchPackages(lf, false), f, 3, nil)
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
	pf := startPrefetch(ctx, prefetchPackages(lf, false), f, 4, nil)
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

// --- Fixture helpers for integration tests ---

// writeFixtureManifest writes a minimal composer.json to dir.
func writeFixtureManifest(tb testing.TB, dir string) {
	tb.Helper()
	data := []byte(`{"name":"vendor/root","require":{"vendor/a":"*"}}`)
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), data, 0o644); err != nil {
		tb.Fatalf("writeFixtureManifest: %v", err)
	}
}

// writeFixtureLock writes a minimal gomposer.lock with the given package
// names as production packages. Each package gets a stub Dist URL and sha256.
func writeFixtureLock(tb testing.TB, dir string, names []string) {
	tb.Helper()
	pkgs := make([]lock.Package, len(names))
	for i, n := range names {
		pkgs[i] = lock.Package{
			Name:    n,
			Version: "1.0.0",
			Dist:    lock.Dist{Type: "zip", URL: "http://stub/" + n, Sha256: "stub-sha-" + n},
		}
	}
	f := &lock.File{
		SchemaVersion: lock.SchemaVersion,
		Packages:      pkgs,
	}
	data, err := f.Encode()
	if err != nil {
		tb.Fatalf("writeFixtureLock: encode: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gomposer.lock"), data, 0o644); err != nil {
		tb.Fatalf("writeFixtureLock: write: %v", err)
	}
}

// recordingMaterializer is a Materializer that creates the destination dir and
// records each (key, dest) call.
type recordingMaterializer struct {
	mu    sync.Mutex
	wrote map[string]string // dest -> key
}

func (m *recordingMaterializer) Materialize(_ context.Context, key, dest string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.wrote == nil {
		m.wrote = make(map[string]string)
	}
	m.wrote[dest] = key
	return os.MkdirAll(dest, 0o755)
}

// recordingAutoloader is an Autoloader that writes a stub autoload.php.
type recordingAutoloader struct{}

func (a *recordingAutoloader) Generate(_ context.Context, projectDir string, _ []lock.Package, _ *manifest.Manifest) error {
	vendorDir := filepath.Join(projectDir, "vendor")
	if err := os.MkdirAll(vendorDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(vendorDir, "autoload.php"), []byte("<?php\n"), 0o644)
}

// stubSource is a SourceLookup that returns a single PackageVersion for any
// requested package name, using the names provided at construction time.
type stubSource struct {
	names []string
}

func newStubSource(names ...string) *stubSource { return &stubSource{names: names} }

func (s *stubSource) Lookup(_ context.Context, name string) (*registry.PackageMetadata, error) {
	return &registry.PackageMetadata{
		Name: name,
		Versions: []registry.PackageVersion{{
			Name:        name,
			Version:     "1.0.0",
			VersionNorm: "1.0.0.0",
			Dist:        registry.Dist{Type: "zip", URL: "http://stub/" + name, Sha: "stub-sha-" + name},
		}},
	}, nil
}

// warmAwareFetcher reports unique-name fetches as `cold` and repeat
// fetches for the same name as `warm`. Mirrors the real fetcher's
// content-addressed dedup behaviour.
type warmAwareFetcher struct {
	mu   sync.Mutex
	cold map[string]int
	warm map[string]int
}

func newWarmAwareFetcher() *warmAwareFetcher {
	return &warmAwareFetcher{cold: map[string]int{}, warm: map[string]int{}}
}

func (w *warmAwareFetcher) Fetch(_ context.Context, pkg lock.Package) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, seen := w.cold[pkg.Name]; seen {
		w.warm[pkg.Name]++
	} else {
		w.cold[pkg.Name]++
	}
	return "sha-" + pkg.Name, nil
}

// runInstallWith is a small helper that drives Install with a fake fetcher
// and the standard test-injected materializer/autoloader, returning the
// fetcher for assertions.
func runInstallWith(t *testing.T, dir string, noPrefetch bool, names []string) *warmAwareFetcher {
	t.Helper()
	writeFixtureManifest(t, dir)
	writeFixtureLock(t, dir, names)
	f := newWarmAwareFetcher()
	opts := Options{
		ProjectDir:   dir,
		Workers:      4,
		NoPrefetch:   noPrefetch,
		Fetcher:      f,
		Materializer: &recordingMaterializer{},
		Autoloader:   &recordingAutoloader{},
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install: %v", err)
	}
	return f
}

// TestPipelinePrefetchWarmsFetchAll: install with a real lockfile, assert
// every package fetched exactly once cold AND that fetchAll observed at
// least one warm hit (i.e. prefetch ran first).
func TestPipelinePrefetchWarmsFetchAll(t *testing.T) {
	f := runInstallWith(t, t.TempDir(), false, []string{"vendor/a", "vendor/b", "vendor/c"})
	for _, name := range []string{"vendor/a", "vendor/b", "vendor/c"} {
		if f.cold[name] != 1 {
			t.Errorf("%s: cold = %d, want 1", name, f.cold[name])
		}
	}
	total := 0
	for _, n := range f.warm {
		total += n
	}
	if total == 0 {
		t.Errorf("expected fetchAll to observe at least one warm hit (prefetch may not be wired)")
	}
}

// TestPipelinePrefetchSkippedOnUpdate: forceResolve=true must skip prefetch.
// We use a stub Source so Solve returns the same packages as the lock; the
// assertion is "zero warm hits" rather than the resolver's correctness.
func TestPipelinePrefetchSkippedOnUpdate(t *testing.T) {
	dir := t.TempDir()
	writeFixtureManifest(t, dir)
	writeFixtureLock(t, dir, []string{"vendor/a"})
	f := newWarmAwareFetcher()
	opts := Options{
		ProjectDir:   dir,
		Workers:      4,
		Fetcher:      f,
		Materializer: &recordingMaterializer{},
		Autoloader:   &recordingAutoloader{},
		Source:       newStubSource("vendor/a"), // existing helper
	}
	if err := Update(context.Background(), opts); err != nil {
		t.Fatalf("Update: %v", err)
	}
	for name, n := range f.warm {
		if n > 0 {
			t.Errorf("update should not prefetch; got %d warm hits for %s", n, name)
		}
	}
}

// TestPipelinePrefetchSkippedWithFlag: --no-prefetch suppresses prefetch.
func TestPipelinePrefetchSkippedWithFlag(t *testing.T) {
	f := runInstallWith(t, t.TempDir(), true, []string{"vendor/a", "vendor/b"})
	for name, n := range f.warm {
		if n > 0 {
			t.Errorf("--no-prefetch should disable prefetch; got %d warm hits for %s", n, name)
		}
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
	pf := startPrefetch(context.Background(), prefetchPackages(lf, false), f, 4, nil)

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

// TestPrefetcherFiresOnFetchedPerSuccess: onFetched fires once per
// successful speculative fetch, never for failures (fetchAll retries
// those authoritatively and reports them through the shared ticker).
func TestPrefetcherFiresOnFetchedPerSuccess(t *testing.T) {
	f := &recordingFetcher{
		fail: func(name string) error {
			if name == "v/b" {
				return context.DeadlineExceeded // any error
			}
			return nil
		},
	}
	lf := &lock.File{Packages: []lock.Package{
		{Name: "v/a", Version: "1.0.0"},
		{Name: "v/b", Version: "2.0.0"},
		{Name: "v/c", Version: "3.0.0"},
	}}
	var mu sync.Mutex
	var ticks []string
	pf := startPrefetch(context.Background(), prefetchPackages(lf, false), f, 4,
		func(name, version string) {
			mu.Lock()
			ticks = append(ticks, name+" "+version)
			mu.Unlock()
		})
	pf.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(ticks) != 2 {
		t.Fatalf("onFetched fired %d times, want 2 (v/b failed); ticks=%v", len(ticks), ticks)
	}
	want := map[string]bool{"v/a 1.0.0": true, "v/c 3.0.0": true}
	for _, tk := range ticks {
		if !want[tk] {
			t.Errorf("unexpected tick %q", tk)
		}
	}
}

// TestPrefetchPackagesFiltersWorkspaceAndDev: the announced-total list
// excludes synthetic workspace entries (no dist to download) and
// respects the includeDev flag — it must be exactly the set fetchAll
// receives on the trusted-lockfile path.
func TestPrefetchPackagesFiltersWorkspaceAndDev(t *testing.T) {
	lf := &lock.File{
		Packages: []lock.Package{
			{Name: "v/a"},
			{Name: "acme/ws", Type: "workspace"},
		},
		PackagesDev: []lock.Package{{Name: "v/dev"}},
	}
	got := prefetchPackages(lf, false)
	if len(got) != 1 || got[0].Name != "v/a" {
		t.Errorf("prefetchPackages(includeDev=false) = %v, want [v/a]", got)
	}
	gotDev := prefetchPackages(lf, true)
	if len(gotDev) != 2 {
		t.Errorf("prefetchPackages(includeDev=true) returned %d packages, want 2 (v/a + v/dev)", len(gotDev))
	}
}
