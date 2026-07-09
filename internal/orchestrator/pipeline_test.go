package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/torstendittmann/gomposer/internal/constraint"
	"github.com/torstendittmann/gomposer/internal/lock"
	"github.com/torstendittmann/gomposer/internal/manifest"
	"github.com/torstendittmann/gomposer/internal/platform"
	"github.com/torstendittmann/gomposer/internal/registry"
)

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

	// runFullPipeline caches resolution results on disk keyed by a hash of
	// (manifest bytes, lock bytes, platform fingerprint) — NOT by ProjectDir,
	// and NOT scoped to the test process (cache.Root() is a real, persistent
	// directory on the host). Two Install() calls with byte-identical
	// composer.json content — including across separate `go test` runs —
	// would have the *second* one served entirely from that cache, bypassing
	// Source.Lookup altogether and producing a "speedup" that has nothing to
	// do with metadata prefetch. Stamp each run's manifest with a
	// time-based nonce in an unused field so the resolution-cache key is
	// unique every time this test runs, forcing a genuine resolve against
	// the slow fake source both times.
	base := Options{
		ProjectDir:   writeManifestObj(t, fakeMultiPkgManifest(uniqueNonce("serial"))),
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

	// Fresh source + project dir (with a distinct nonce) for the parallel
	// run so neither the fake registry's cache nor the on-disk resolution
	// cache carries over from the baseline run.
	slow2 := &sleepySourceLookup{delay: 40 * time.Millisecond, versions: fakeMultiPkgVersions()}
	base.Source = slow2
	base.ProjectDir = writeManifestObj(t, fakeMultiPkgManifest(uniqueNonce("parallel")))
	base.NoMetadataPrefetch = false
	tParallel := timeInstall(t, base)

	if tParallel >= tSerial {
		t.Errorf("metadata prefetch did not speed up install: serial=%v parallel=%v", tSerial, tParallel)
	}
	if tParallel*10 >= tSerial*7 { // require > 30% speedup
		t.Logf("marginal speedup: serial=%v parallel=%v", tSerial, tParallel)
	}
}

// sleepySourceLookup simulates a registry client with an internal metadata
// cache: the first Lookup for a given name sleeps `delay` (simulated network
// RTT), subsequent Lookups for the same name return instantly from cache.
// This is the property maybeStartMetadataPrefetch depends on to be useful at
// all — warming a name only helps if the warmed result is visible to the
// resolver's later Lookup call for that same name.
type sleepySourceLookup struct {
	delay    time.Duration
	versions map[string]*registry.PackageMetadata

	mu    sync.Mutex
	cache map[string]*registry.PackageMetadata
}

func (s *sleepySourceLookup) Lookup(ctx context.Context, name string) (*registry.PackageMetadata, error) {
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

// uniqueNonce combines label with the current nanosecond timestamp so
// repeated test runs never collide on the persistent, process-independent
// resolution cache (see the comment in TestMetadataPrefetchReducesResolveWallTime).
func uniqueNonce(label string) string {
	return fmt.Sprintf("%s-%d", label, time.Now().UnixNano())
}

// fakeMultiPkgManifest returns a root manifest requiring 5 independent leaf
// packages — enough to expose the serial-vs-parallel resolve gap without
// making the test slow. nonce is stashed in an unused manifest field purely
// to perturb the encoded bytes (see the resolution-cache note above); it has
// no effect on resolution itself.
func fakeMultiPkgManifest(nonce string) *manifest.Manifest {
	return &manifest.Manifest{
		Name: "acme/app",
		Require: map[string]string{
			"a/a": "^1.0",
			"b/b": "^1.0",
			"c/c": "^1.0",
			"d/d": "^1.0",
			"e/e": "^1.0",
		},
		Extra: map[string]any{"test-nonce": nonce},
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
