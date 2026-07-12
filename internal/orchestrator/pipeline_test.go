package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/torstendittmann/gomposer/internal/constraint"
	"github.com/torstendittmann/gomposer/internal/lock"
	"github.com/torstendittmann/gomposer/internal/manifest"
	"github.com/torstendittmann/gomposer/internal/platform"
	"github.com/torstendittmann/gomposer/internal/registry"
	"github.com/torstendittmann/gomposer/internal/resolver/testlookup"
)

// writeFile creates path (and any missing parent directories) with the
// given contents. Used by workspace-pipeline tests to seed a monorepo
// directory tree under t.TempDir().
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("writeFile: mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writeFile: write %s: %v", path, err)
	}
}

func mustVer(t *testing.T, s string) constraint.Version {
	t.Helper()
	v, err := constraint.ParseVersion(s)
	if err != nil {
		t.Fatalf("ParseVersion(%q): %v", s, err)
	}
	return v
}

func TestEvaluatePlatformWarningsDefaultMode(t *testing.T) {
	pf := &platform.Platform{PHPVersion: mustVer(t, "8.2.0")}
	pkgs := []lock.Package{
		{Name: "acme/x", Version: "1.0.0", Require: map[string]string{"php": "^7.4"}},
	}
	var stderr bytes.Buffer
	warnings, err := evaluatePlatformWarnings(pkgs, pf, nil, false /*noDev*/, false /*quiet*/, &stderr)
	if err != nil {
		t.Fatalf("evaluatePlatformWarnings: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %+v", warnings)
	}
	if !strings.Contains(warnings[0], "acme/x") || !strings.Contains(warnings[0], "php") {
		t.Errorf("warning text: %q", warnings[0])
	}
	if !strings.Contains(stderr.String(), "acme/x") {
		t.Errorf("stderr did not contain warning: %q", stderr.String())
	}
}

func TestEvaluatePlatformWarningsNoDevFails(t *testing.T) {
	pf := &platform.Platform{PHPVersion: mustVer(t, "8.2.0")}
	pkgs := []lock.Package{
		{Name: "acme/x", Version: "1.0.0", Require: map[string]string{"php": "^7.4"}},
	}
	var stderr bytes.Buffer
	_, err := evaluatePlatformWarnings(pkgs, pf, nil, true /*noDev*/, false, &stderr)
	if err == nil {
		t.Error("expected error in --no-dev mode")
	}
}

func TestEvaluatePlatformWarningsIgnoreFlag(t *testing.T) {
	pf := &platform.Platform{PHPVersion: mustVer(t, "8.2.0")}
	pkgs := []lock.Package{
		{Name: "acme/x", Version: "1.0.0", Require: map[string]string{"php": "^7.4"}},
	}
	ignore := map[string]bool{"php": true}
	var stderr bytes.Buffer
	warnings, err := evaluatePlatformWarnings(pkgs, pf, ignore, true /*noDev*/, false, &stderr)
	if err != nil {
		t.Fatalf("ignored req should not fail: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings should be empty: %+v", warnings)
	}
}

func TestEvaluatePlatformWarningsQuiet(t *testing.T) {
	pf := &platform.Platform{PHPVersion: mustVer(t, "8.2.0")}
	pkgs := []lock.Package{
		{Name: "acme/x", Version: "1.0.0", Require: map[string]string{"php": "^7.4"}},
	}
	var stderr bytes.Buffer
	warnings, _ := evaluatePlatformWarnings(pkgs, pf, nil, false, true /*quiet*/, &stderr)
	if stderr.Len() != 0 {
		t.Errorf("--quiet should suppress stderr; got %q", stderr.String())
	}
	if len(warnings) != 1 {
		t.Errorf("warnings should still be recorded for the lockfile: %+v", warnings)
	}
}

func TestEvaluatePlatformWarningsLibStarOnce(t *testing.T) {
	pf := &platform.Platform{PHPVersion: mustVer(t, "8.2.0")}
	pkgs := []lock.Package{
		{Name: "a/x", Require: map[string]string{"lib-curl": ">=7.0"}},
		{Name: "a/y", Require: map[string]string{"lib-icu": ">=70"}},
	}
	var stderr bytes.Buffer
	warnings, err := evaluatePlatformWarnings(pkgs, pf, nil, false, false, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	libCount := 0
	for _, w := range warnings {
		if strings.Contains(w, "lib-*") {
			libCount++
		}
	}
	if libCount != 1 {
		t.Errorf("expected exactly one coalesced lib-* warning; got %d in %+v", libCount, warnings)
	}
}

func TestVerbosePrintsTimingBlock(t *testing.T) {
	// Capture stderr.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()

	dir := t.TempDir()
	manifestBytes := []byte(`{"name":"vendor/root","require":{"a/a":"^1.0"}}`)
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), manifestBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	opts := Options{
		ProjectDir:   dir,
		Verbose:      true,
		Fetcher:      &fakeFetcher{},
		Materializer: &fakeMaterializer{},
		Autoloader:   &fakeAutoloader{},
		Source: &fakeSource{pkgs: map[string]*registry.PackageMetadata{
			"a/a": {Name: "a/a", Versions: []registry.PackageVersion{{
				Name: "a/a", Version: "1.0.0", VersionNorm: "1.0.0.0",
				Dist: registry.Dist{Type: "zip", URL: "x", Sha: "deadbeef"},
			}}},
		}},
		NoScripts: true,
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install: %v", err)
	}

	w.Close()
	out, _ := io.ReadAll(r)
	got := string(out)

	for _, want := range []string{
		"gomposer: timing",
		"read manifest",
		"resolve",
		"fetch",
		"materialize",
		"autoload",
		"write lock",
		"-------- total",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("verbose output missing %q in:\n%s", want, got)
		}
	}
}

func TestQuietSuppressesTimingBlock(t *testing.T) {
	r, w, _ := os.Pipe()
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()

	dir := t.TempDir()
	manifestBytes := []byte(`{"name":"vendor/root","require":{"a/a":"^1.0"}}`)
	os.WriteFile(filepath.Join(dir, "composer.json"), manifestBytes, 0o644)

	opts := Options{
		ProjectDir:   dir,
		Verbose:      true,
		Quiet:        true,
		Fetcher:      &fakeFetcher{},
		Materializer: &fakeMaterializer{},
		Autoloader:   &fakeAutoloader{},
		Source: &fakeSource{pkgs: map[string]*registry.PackageMetadata{
			"a/a": {Name: "a/a", Versions: []registry.PackageVersion{{
				Name: "a/a", Version: "1.0.0", VersionNorm: "1.0.0.0",
				Dist: registry.Dist{Type: "zip", URL: "x", Sha: "deadbeef"},
			}}},
		}},
		NoScripts: true,
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install: %v", err)
	}
	w.Close()
	out, _ := io.ReadAll(r)
	if strings.Contains(string(out), "gomposer: timing") {
		t.Errorf("quiet+verbose should suppress timing, got:\n%s", out)
	}
}

// --- Task 3: metadata prefetch wall-time integration test ---

// TestMetadataPrefetchReducesResolveWallTime asserts that when the resolver
// has to look up N packages against a source whose Lookup takes non-
// negligible time, the total pipeline duration is smaller with metadata
// prefetch enabled than without it. We do not assert an exact ratio (to
// avoid flakiness) — just that prefetch is materially faster.
func TestMetadataPrefetchReducesResolveWallTime(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-sensitive; skipping under -short")
	}
	platform.SetTestPlatform(t, "8.2.0")

	// Build a slow-fake registry: each *cold* Lookup sleeps 40ms, then caches
	// the result. This mirrors the real Packagist client, whose on-disk/HTTP
	// cache is exactly what maybeStartMetadataPrefetch relies on warming: the
	// prefetch pool's concurrent Lookups populate the cache while the
	// resolver's own serial Lookups race to consume it. Without this caching
	// behavior the fake wouldn't exercise the thing prefetch actually speeds
	// up (a source with no memory of its own can never be "pre-warmed").
	slow := &sleepySourceLookup{delay: 40 * time.Millisecond, versions: fakeMultiPkgVersions()}

	// runFullPipeline also caches resolution results on disk, keyed by a hash
	// of (manifest bytes, lock bytes, platform fingerprint). The serial and
	// parallel runs below use byte-identical manifests, so each gets its own
	// $XDG_CACHE_HOME (scoped to this test via t.TempDir()) to keep them from
	// serving each other's resolution out of that cache — which would bypass
	// Source.Lookup entirely and produce a "speedup" unrelated to prefetch.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	base := Options{
		ProjectDir:   writeManifestObj(t, fakeMultiPkgManifest()),
		Source:       slow,
		NoNetwork:    false,
		Fetcher:      &fakeFetcher{},
		Materializer: &fakeMaterializer{},
		Autoloader:   &fakeAutoloader{},
		NoScripts:    true,
		// disable BOTH prefetches for the baseline run
		NoPrefetch:         true,
		NoMetadataPrefetch: true,
	}
	tSerial := timeInstall(t, base)

	// Fresh source + project dir + cache root for the parallel run so
	// neither the fake registry's cache nor the on-disk resolution cache
	// carries over from the baseline run.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	slow2 := &sleepySourceLookup{delay: 40 * time.Millisecond, versions: fakeMultiPkgVersions()}
	base.Source = slow2
	base.ProjectDir = writeManifestObj(t, fakeMultiPkgManifest())
	base.NoMetadataPrefetch = false
	tParallel := timeInstall(t, base)

	if tParallel >= tSerial {
		t.Errorf("metadata prefetch did not speed up install: serial=%v parallel=%v", tSerial, tParallel)
	}
	if tParallel*10 >= tSerial*7 { // require > 30% speedup
		t.Logf("marginal speedup: serial=%v parallel=%v", tSerial, tParallel)
	}
}

// TestMetadataPrefetchCancelsOnCacheHit asserts that when resolveOrCache
// short-circuits (existing-lock or resolution-cache hit), the metadata
// prefetch pool is cancelled and does not add wall time. Approach: run the
// pipeline once to populate gomposer.lock, then run it again against a
// Source whose Lookup blocks until its context is cancelled — if the second
// run's deferred mprefetch.Wait() actually waited on that pool (the bug this
// commit fixes), the install would take as long as the bounding context
// allows; if the pool is cancelled promptly, it returns near-instantly.
func TestMetadataPrefetchCancelsOnCacheHit(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-sensitive; skipping under -short")
	}
	platform.SetTestPlatform(t, "8.2.0")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	dir := writeManifestObj(t, fakeMultiPkgManifest())
	base := Options{
		ProjectDir:   dir,
		Source:       &sleepySourceLookup{delay: 5 * time.Millisecond, versions: fakeMultiPkgVersions()},
		Fetcher:      &fakeFetcher{},
		Materializer: &fakeMaterializer{},
		Autoloader:   &fakeAutoloader{},
		NoScripts:    true,
	}

	// First run: cold resolve, writes gomposer.lock.
	if err := Install(context.Background(), base); err != nil {
		t.Fatalf("first Install: %v", err)
	}

	// Second run: gomposer.lock now exists and matches the manifest, so
	// resolveOrCache takes the existing-lock short-circuit and never calls
	// resolveFunc. maybeStartMetadataPrefetch, however, always starts before
	// resolveOrCache runs — a Source whose Lookup blocks until cancelled
	// proves the prefetch pool is actually cancelled rather than merely
	// racing to finish on its own: an uncancelled pool would hang until the
	// bounding context below expires.
	base.Source = &blockingSourceLookup{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	if err := Install(ctx, base); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	elapsed := time.Since(start)

	const budget = 200 * time.Millisecond
	if elapsed > budget {
		t.Errorf("cache-hit install took %v, want under %v (metadata prefetch should have been cancelled, not awaited)", elapsed, budget)
	}
}

// sleepySourceLookup simulates a registry client with an internal metadata
// cache: the first Lookup for a given name sleeps `delay` (simulated network
// RTT), subsequent Lookups for the same name return instantly from cache.
// This is the property maybeStartMetadataPrefetch depends on to be useful at
// all — warming a name only helps if the warmed result is visible to the
// resolver's later Lookup call for that same name.
//
// Concurrent same-name Lookups are also coalesced via singleflight, mirroring
// packagist.Client.Lookup's in-flight dedup (see
// internal/registry/packagist/packagist.go). Without this, sleepySourceLookup
// only models the "completed lookup" half of the real client's behavior —
// it can't demonstrate the win from two callers racing to look up the same
// still-in-flight name (see TestInFlightDedupCoalescesConcurrentSameNameLookups).
//
// uncoalesced tracks how many times lookupUncoalesced actually ran per name —
// i.e. how many cold, non-deduped round-trips the source paid for. This is
// what proves dedup is doing anything: in a chain-shaped install the pool and
// the resolver race to Lookup the same name at nearly the same instant, so
// dedup cannot buy wall-clock time (both callers still wait out the same
// in-flight request), but it does collapse what would otherwise be two
// uncoalesced round-trips into one.
type sleepySourceLookup struct {
	delay    time.Duration
	versions map[string]*registry.PackageMetadata

	mu    sync.Mutex
	cache map[string]*registry.PackageMetadata

	sf singleflight.Group // models packagist.Client's in-flight dedup

	countMu     sync.Mutex
	uncoalesced map[string]int // count of lookupUncoalesced calls per name
}

func (s *sleepySourceLookup) Lookup(ctx context.Context, name string) (*registry.PackageMetadata, error) {
	result, err, _ := s.sf.Do(name, func() (any, error) {
		return s.lookupUncoalesced(ctx, name)
	})
	if err != nil {
		return nil, err
	}
	return result.(*registry.PackageMetadata), nil
}

// lookupUncoalesced is the raw Lookup body without singleflight wrapping.
// Callers should use Lookup, which coalesces concurrent same-name requests
// through s.sf.
func (s *sleepySourceLookup) lookupUncoalesced(ctx context.Context, name string) (*registry.PackageMetadata, error) {
	s.countMu.Lock()
	if s.uncoalesced == nil {
		s.uncoalesced = map[string]int{}
	}
	s.uncoalesced[name]++
	s.countMu.Unlock()

	s.mu.Lock()
	if v, ok := s.cache[name]; ok {
		s.mu.Unlock()
		return v, nil
	}
	s.mu.Unlock()

	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	v, ok := s.versions[name]
	if !ok {
		return nil, registry.ErrPackageNotFound
	}
	s.mu.Lock()
	if s.cache == nil {
		s.cache = map[string]*registry.PackageMetadata{}
	}
	s.cache[name] = v
	s.mu.Unlock()
	return v, nil
}

// uncoalescedCallsFor returns how many times lookupUncoalesced actually ran
// for name (as opposed to joining an in-flight call via singleflight).
func (s *sleepySourceLookup) uncoalescedCallsFor(name string) int {
	s.countMu.Lock()
	defer s.countMu.Unlock()
	return s.uncoalesced[name]
}

// totalUncoalescedCalls sums uncoalescedCallsFor across every name the
// source has ever seen.
func (s *sleepySourceLookup) totalUncoalescedCalls() int {
	s.countMu.Lock()
	defer s.countMu.Unlock()
	total := 0
	for _, n := range s.uncoalesced {
		total += n
	}
	return total
}

// fakeMultiPkgManifest returns a root manifest requiring 5 independent leaf
// packages — enough to expose the serial-vs-parallel resolve gap without
// making the test slow.
func fakeMultiPkgManifest() *manifest.Manifest {
	return &manifest.Manifest{
		Name: "acme/app",
		Require: map[string]string{
			"a/a": "^1.0",
			"b/b": "^1.0",
			"c/c": "^1.0",
			"d/d": "^1.0",
			"e/e": "^1.0",
		},
	}
}

// fakeMultiPkgVersions builds a single 1.0.0 release for each of the 5
// packages named in fakeMultiPkgManifest, each with a fake dist entry (the
// fetch/materialize phases use fakeFetcher/fakeMaterializer, so the dist
// details are never actually read).
func fakeMultiPkgVersions() map[string]*registry.PackageMetadata {
	out := map[string]*registry.PackageMetadata{}
	for _, name := range []string{"a/a", "b/b", "c/c", "d/d", "e/e"} {
		out[name] = &registry.PackageMetadata{
			Name: name,
			Versions: []registry.PackageVersion{{
				Name: name, Version: "1.0.0", VersionNorm: "1.0.0.0",
				Dist: registry.Dist{Type: "zip", URL: "http://fixture/" + name + ".zip", Sha: "deadbeef"},
			}},
		}
	}
	return out
}

// writeManifestObj serializes m to composer.json in a fresh temp project dir
// and returns the dir.
func writeManifestObj(t *testing.T, m *manifest.Manifest) string {
	t.Helper()
	dir := t.TempDir()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("writeManifestObj: encode: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), data, 0o644); err != nil {
		t.Fatalf("writeManifestObj: write: %v", err)
	}
	return dir
}

// timeInstall runs Install with opts and returns the wall-clock duration.
func timeInstall(t *testing.T, opts Options) time.Duration {
	t.Helper()
	start := time.Now()
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install: %v", err)
	}
	return time.Since(start)
}

// --- Discovery-driven metadata prefetch: OnVersionDecided wire-through ---

// nameCountingSourceLookup wraps a fixed metadata map and records how many
// times each package name was looked up. Used to prove resolveFunc's
// OnVersionDecided callback actually reaches the metadata prefetch pool: a
// transitive dependency absent from the root manifest can only be looked up
// twice (once by the pool, once by the resolver itself) if something fed its
// name to mprefetch.Add() mid-resolve.
//
// Distinct from the package-level countingSourceLookup (metadata_prefetch_test.go),
// which only counts total calls and returns empty metadata; this variant
// needs per-name counts and real Versions/Require data to drive a full
// resolve.
type nameCountingSourceLookup struct {
	versions map[string]*registry.PackageMetadata

	mu     sync.Mutex
	counts map[string]int
}

func (s *nameCountingSourceLookup) Lookup(ctx context.Context, name string) (*registry.PackageMetadata, error) {
	s.mu.Lock()
	if s.counts == nil {
		s.counts = map[string]int{}
	}
	s.counts[name]++
	s.mu.Unlock()

	v, ok := s.versions[name]
	if !ok {
		return nil, registry.ErrPackageNotFound
	}
	return v, nil
}

func (s *nameCountingSourceLookup) count(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[name]
}

// TestResolveCallbackFeedsMprefetchAdd asserts the wiring added in Task 4:
// resolveFunc's resolver.Input.OnVersionDecided closure calls
// ps.mprefetch.Add with the just-decided package's transitive requires.
//
// Setup: the root manifest requires only "a/a"; "a/a" in turn requires
// "b/b", which never appears anywhere in the root manifest or an existing
// lockfile. collectMetadataPrefetchNames (the initial warm set) therefore
// never enqueues "b/b" — the *only* way the prefetch pool ever calls
// Lookup("b/b") is via the discovery-driven Add() path exercised by the
// OnVersionDecided callback once the resolver commits "a/a".
//
// This is a call-count assertion, not a timing one: runFullPipeline's
// deferred mprefetch.Wait() drains the pool (including anything enqueued via
// Add) before Install returns, so by the time Install returns, "b/b" has
// been looked up once by the resolver's own synchronous Lookup and once more
// by the pool — 2 calls total — if and only if the callback fired and
// reached Add(). Without the wiring, only the resolver's own call happens
// (1 call).
func TestResolveCallbackFeedsMprefetchAdd(t *testing.T) {
	platform.SetTestPlatform(t, "8.2.0")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	src := &nameCountingSourceLookup{versions: map[string]*registry.PackageMetadata{
		"a/a": {
			Name: "a/a",
			Versions: []registry.PackageVersion{{
				Name: "a/a", Version: "1.0.0", VersionNorm: "1.0.0.0",
				Require: map[string]string{"b/b": "^1.0"},
				Dist:    registry.Dist{Type: "zip", URL: "http://fixture/a.zip", Sha: "deadbeef"},
			}},
		},
		"b/b": {
			Name: "b/b",
			Versions: []registry.PackageVersion{{
				Name: "b/b", Version: "1.0.0", VersionNorm: "1.0.0.0",
				Dist: registry.Dist{Type: "zip", URL: "http://fixture/b.zip", Sha: "deadbeef"},
			}},
		},
	}}

	opts := Options{
		ProjectDir: writeManifestObj(t, &manifest.Manifest{
			Name:    "acme/app",
			Require: map[string]string{"a/a": "^1.0"},
		}),
		Source:       src,
		Fetcher:      &fakeFetcher{},
		Materializer: &fakeMaterializer{},
		Autoloader:   &fakeAutoloader{},
		NoScripts:    true,
	}

	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if got := src.count("b/b"); got != 2 {
		t.Errorf("Lookup(%q) called %d times, want 2 (1 from resolver, 1 from OnVersionDecided-driven mprefetch.Add)", "b/b", got)
	}
}

// --- Task 4: workspaces pipeline wire-in ---

// TestMetadataPrefetchWarmTransitivesOnFreshInstall asserts Scope C:
// on a fresh install with no lock, transitive requires get prefetched
// as the solver commits versions, so total wall time is lower than
// with metadata prefetch disabled entirely.
//
// The fixture is a fan-out, not a chain: acme/root requires all of
// acme/{a,b,c,d,e} directly, and none of those five have further requires.
// A serial chain (a -> b -> c -> d -> e) cannot demonstrate discovery-driven
// prefetch's *wall-time* benefit here even with in-flight dedup wired up
// (sleepySourceLookup.Lookup now joins an already in-flight call via
// singleflight — see its doc comment): the pool's Lookup(b) and the
// resolver's own Lookup(b) both fire the instant a is decided, so whichever
// arrives second still just joins the first's still-in-flight 80ms wait —
// both pay the full 80ms wall-clock, identical to no prefetch at all, and
// that repeats at every link (dedup does cut round-trips here, see
// TestInFlightDedupCoalescesConcurrentSameNameLookups, just not wall time).
// A fan-out sidesteps that: the resolver's serial walk only races the pool
// once, on the first sibling (acme/a); by the time it reaches acme/b,
// acme/c, acme/d, acme/e, the pool's already-dispatched (and by then mostly
// finished) parallel Lookups for those names have warmed the source's
// cache.
func TestMetadataPrefetchWarmTransitivesOnFreshInstall(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-sensitive; skipping under -short")
	}
	platform.SetTestPlatform(t, "8.2.0")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	chain := map[string]*registry.PackageMetadata{
		"acme/root": {
			Name: "acme/root",
			Versions: []registry.PackageVersion{{
				Name:        "acme/root",
				Version:     "1.0.0",
				VersionNorm: "1.0.0.0",
				Require: map[string]string{
					"acme/a": "^1.0",
					"acme/b": "^1.0",
					"acme/c": "^1.0",
					"acme/d": "^1.0",
					"acme/e": "^1.0",
				},
				Dist: registry.Dist{Type: "zip", URL: "http://fixture/acme-root.zip", Sha: "deadbeef"},
			}},
		},
	}
	for _, leaf := range []string{"a", "b", "c", "d", "e"} {
		name := "acme/" + leaf
		chain[name] = &registry.PackageMetadata{
			Name: name,
			Versions: []registry.PackageVersion{{
				Name:        name,
				Version:     "1.0.0",
				VersionNorm: "1.0.0.0",
				Dist:        registry.Dist{Type: "zip", URL: "http://fixture/acme-" + leaf + ".zip", Sha: "deadbeef"},
			}},
		}
	}

	baseOpts := func() Options {
		return Options{
			ProjectDir: writeManifestObj(t, &manifest.Manifest{
				Name:    "acme/app",
				Require: map[string]string{"acme/root": "^1.0"},
			}),
			Source:       &sleepySourceLookup{delay: 80 * time.Millisecond, versions: chain},
			Fetcher:      &fakeFetcher{},
			Materializer: &fakeMaterializer{},
			Autoloader:   &fakeAutoloader{},
			NoScripts:    true,
			NoPrefetch:   true, // artifact prefetch off — isolate metadata prefetch
		}
	}

	// Baseline: no metadata prefetch. Resolver walks root -> a -> b -> c ->
	// d -> e serially: 6 cold Lookups x 80ms = 480ms.
	base := baseOpts()
	base.NoMetadataPrefetch = true
	tBaseline := timeInstall(t, base)

	// Fresh cache for the prefetch run.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	// With discovery-driven prefetch (default on): the initial warm set is
	// {acme/root} (the root manifest's direct require), so deciding root
	// still costs a full 80ms — nothing can prefetch it earlier. Once root
	// is decided, OnVersionDecided feeds {a,b,c,d,e} to the pool in one
	// batch, dispatching all five in parallel; the resolver's own serial
	// walk through them then mostly rides the pool's concurrent (or
	// already-finished) Lookups instead of paying for its own. Expected:
	// ~80ms (root) + ~80ms (acme/a, races the pool) + near-zero for the
	// remaining four ~= 160ms, comfortably under 70% of the 480ms baseline.
	on := baseOpts()
	on.NoMetadataPrefetch = false
	tPrefetch := timeInstall(t, on)

	if want := tBaseline * 7 / 10; tPrefetch >= want {
		t.Errorf("discovery-driven prefetch speedup below expected margin: baseline=%v prefetch=%v, want prefetch < %v (70%% of baseline)", tBaseline, tPrefetch, want)
	}
}

// TestInFlightDedupCoalescesConcurrentSameNameLookups exercises the end-to-
// end dedup wire-in. On a chain install with discovery-driven metadata
// prefetch on, the pool and the resolver race to Lookup the same package
// names: as soon as a parent in the chain is decided, OnVersionDecided feeds
// its child to the pool, and the resolver's own Lookup for that same child
// fires within microseconds of the pool's. Neither caller has a head start,
// so dedup buys no wall-clock time here (see the doc comment on
// sleepySourceLookup) — but it does mean the source only pays for one
// uncoalesced round-trip per name instead of two.
//
// Chain: root -> a -> b -> c -> d -> e. Six unique names. Without dedup
// engaging, each name would see up to 2 uncoalesced calls (one from the pool,
// one from the resolver) for up to 12 total. With dedup, each unique name's
// uncoalesced body should run exactly once, for 6 total.
func TestInFlightDedupCoalescesConcurrentSameNameLookups(t *testing.T) {
	platform.SetTestPlatform(t, "8.2.0")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	names := []string{"root", "a", "b", "c", "d", "e"}
	chain := map[string]*registry.PackageMetadata{}
	for i, leaf := range names {
		name := "acme/" + leaf
		require := map[string]string{}
		if i+1 < len(names) {
			require["acme/"+names[i+1]] = "^1.0"
		}
		chain[name] = &registry.PackageMetadata{
			Name: name,
			Versions: []registry.PackageVersion{{
				Name:        name,
				Version:     "1.0.0",
				VersionNorm: "1.0.0.0",
				Require:     require,
				Dist:        registry.Dist{Type: "zip", URL: "http://fixture/acme-" + leaf + ".zip", Sha: "deadbeef"},
			}},
		}
	}
	src := &sleepySourceLookup{delay: 20 * time.Millisecond, versions: chain}

	opts := Options{
		ProjectDir: writeManifestObj(t, &manifest.Manifest{
			Name:    "acme/app",
			Require: map[string]string{"acme/root": "^1.0"},
		}),
		Source:             src,
		Fetcher:            &fakeFetcher{},
		Materializer:       &fakeMaterializer{},
		Autoloader:         &fakeAutoloader{},
		NoScripts:          true,
		NoPrefetch:         true, // artifact prefetch off — isolate metadata prefetch
		NoMetadataPrefetch: false,
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install: %v", err)
	}

	total := src.totalUncoalescedCalls()
	const unique = 6 // root, a, b, c, d, e
	// With dedup engaging on every same-name race, we expect each name to
	// run its uncoalesced body exactly once. Allow one extra as a safety
	// margin (e.g. if a follower arrives just as the leader completes and
	// misses the singleflight window by a hair).
	if total > unique+1 {
		t.Errorf("total uncoalesced Lookups = %d, want <= %d (chain has %d unique names; dedup should collapse concurrent same-name calls to 1 each)",
			total, unique+1, unique)
	}
	if total < unique {
		t.Errorf("total uncoalesced Lookups = %d, want >= %d (fewer than unique names means a name was never looked up)", total, unique)
	}
}

// TestWorkspacesFullPipelineHappyPath drives Install end-to-end on a tiny
// monorepo (root + two workspaces, one depending on the other via the
// workspace: protocol) and asserts the resulting lockfile records both
// workspaces as first-class type=workspace entries. Assertions about the
// vendor/ symlink layout land in Task 5 — this test's job is only to prove
// discovery + aggregation + lockfile grafting work end-to-end without error.
func TestWorkspacesFullPipelineHappyPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "composer.json"), `{
        "name": "acme/monorepo",
        "workspaces": ["packages/*", "apps/*"]
    }`)
	writeFile(t, filepath.Join(dir, "packages", "shared", "composer.json"), `{
        "name": "acme/shared",
        "version": "1.0.0",
        "autoload": { "psr-4": { "Acme\\Shared\\": "src/" } }
    }`)
	writeFile(t, filepath.Join(dir, "packages", "shared", "src", "Thing.php"), "<?php\nnamespace Acme\\Shared; class Thing {}")
	writeFile(t, filepath.Join(dir, "apps", "api", "composer.json"), `{
        "name": "acme/api",
        "require": { "acme/shared": "workspace:^1.0" }
    }`)

	opts := Options{
		ProjectDir:   dir,
		Source:       &fakeSource{pkgs: map[string]*registry.PackageMetadata{}},
		Fetcher:      &fakeFetcher{},
		Materializer: &fakeMaterializer{},
		Autoloader:   &fakeAutoloader{},
		NoScripts:    true,
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install: %v", err)
	}

	lockBytes, err := os.ReadFile(filepath.Join(dir, "gomposer.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(lockBytes, []byte(`"type": "workspace"`)) {
		t.Errorf(`gomposer.lock has no "type": "workspace" entries:\n%s`, lockBytes)
	}
	if !bytes.Contains(lockBytes, []byte(`"acme/shared"`)) || !bytes.Contains(lockBytes, []byte(`"acme/api"`)) {
		t.Errorf("gomposer.lock missing workspace names:\n%s", lockBytes)
	}
}

// --- Resolve-phase progress wire-in ---

type recordingProgress struct {
	mu       sync.Mutex
	events   []string
	resolves []string // names passed to IncResolve
}

func (r *recordingProgress) record(evt string) {
	r.mu.Lock()
	r.events = append(r.events, evt)
	r.mu.Unlock()
}

func (r *recordingProgress) BeginFetch(int)    { r.record("BeginFetch") }
func (r *recordingProgress) IncFetch(string)   {}
func (r *recordingProgress) EndFetch()         { r.record("EndFetch") }
func (r *recordingProgress) BeginExtract(int)  { r.record("BeginExtract") }
func (r *recordingProgress) IncExtract(string) {}
func (r *recordingProgress) EndExtract()       { r.record("EndExtract") }
func (r *recordingProgress) BeginResolve(int)  { r.record("BeginResolve") }
func (r *recordingProgress) IncResolve(name string) {
	r.mu.Lock()
	r.resolves = append(r.resolves, name)
	r.mu.Unlock()
}
func (r *recordingProgress) EndResolve() { r.record("EndResolve") }
func (r *recordingProgress) Done(int)    {}

func (r *recordingProgress) resolveCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.resolves)
}

func (r *recordingProgress) sawEvent(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if e == name {
			return true
		}
	}
	return false
}

// TestPipelineFiresResolveProgressOnFreshInstall asserts the resolve
// triad fires during a fresh install and NOT during a resolution-cache
// hit repeat install. The recording Progress fake records events + per-
// name IncResolve calls.
func TestPipelineFiresResolveProgressOnFreshInstall(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	src := testlookup.New(map[string][]registry.PackageVersion{
		"acme/a": {testlookup.Pkg("acme/a", "1.0.0", nil)},
	})
	rp := &recordingProgress{}

	opts := Options{
		ProjectDir: writeManifestObj(t, &manifest.Manifest{
			Name:    "acme/app",
			Require: map[string]string{"acme/a": "^1.0"},
		}),
		Source:       src,
		Progress:     rp,
		Fetcher:      &fakeFetcher{},
		Materializer: &fakeMaterializer{},
		Autoloader:   &fakeAutoloader{},
		NoScripts:    true,
		NoPrefetch:   true,
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !rp.sawEvent("BeginResolve") {
		t.Errorf("fresh install did not fire BeginResolve; events=%v", rp.events)
	}
	if rp.resolveCount() == 0 {
		t.Errorf("fresh install fired 0 IncResolve calls (expected at least 1)")
	}
	if !rp.sawEvent("EndResolve") {
		t.Errorf("fresh install did not fire EndResolve")
	}

	// Repeat install: the first Install wrote gomposer.lock into ProjectDir,
	// so resolveOrCache short-circuits on the existing-lockfile path and the
	// resolver never runs. Same silence contract as a resolution-cache hit.
	rp2 := &recordingProgress{}
	opts.Progress = rp2
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install #2: %v", err)
	}
	if rp2.sawEvent("BeginResolve") {
		t.Errorf("cache-hit install fired BeginResolve; expected silent resolve")
	}
	if rp2.resolveCount() != 0 {
		t.Errorf("cache-hit install fired %d IncResolve calls (expected 0)", rp2.resolveCount())
	}
}
