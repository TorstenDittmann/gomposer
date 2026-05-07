# Stage 1 / Plan 6: Orchestrator + End-to-End Integration

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire Plans 1–5 into a single working pipeline. End-to-end: read `composer.json` → check resolution-result cache → resolve via Packagist → fetch zips concurrently → materialize `vendor/` from the content-addressed store → generate the autoloader → write `composer-go.lock`. Implements both `install` (use existing lock if present) and `update` (force re-resolve). After this plan, `composer-go install` on a real Packagist project produces a working `vendor/` and a warm-cache repeat install completes in <100ms.

**Architecture:**

- `internal/orchestrator/orchestrator.go` is the only place that knows the full pipeline. Every other package exposes a narrow API; the orchestrator stitches them together.
- Concurrency: `golang.org/x/sync/errgroup` with a bounded worker pool (default `runtime.NumCPU()`). Download/extract phases parallelize across packages; resolve and lock-write are serial.
- Cancellation: a single `context.Context` flows through every phase. CLI cancels on SIGINT.
- Resolution-result cache (cache layer 3) is checked at the very top of `Install`. The key is `(manifestContentHash, lockHash, platformFingerprint)`. A hit skips Plan 3 (resolver) entirely — we go straight from "we already know the locked set" to fetch + materialize + autoload.
- `Update` follows the same pipeline but never reads the existing lock and never consults the resolution-result cache. It re-resolves, re-locks, and re-materializes.
- `internal/platform/platform.go` is a stub for stage 1. It returns the literal string `"php-unknown"`. Real PHP detection is **deferred to Stage 2** (spec section "Stage 2 — Real-world coverage"). The fingerprint still flows through every cache key so when stage 2 swaps the implementation, every old cache entry is naturally invalidated.

**Tech Stack:** Go 1.22+, `golang.org/x/sync/errgroup` (already pulled in by Plan 4 if not earlier), standard library only otherwise.

**Depends on:**
- Plan 1 — `internal/manifest`, `internal/constraint`, `internal/lock`, CLI scaffold.
- Plan 2 — `internal/registry`, `internal/registry/packagist`, `internal/cache/{httpcache,parsedcache}`.
- Plan 3 — `internal/resolver` exposing `Resolve(ctx, manifest, source) (*resolver.Result, error)` where `Result` has `Packages []lock.Package` and `PackagesDev []lock.Package`.
- Plan 4 — `internal/fetcher` exposing `Fetch(ctx, dist) (storeKey string, error)` and `internal/store` exposing `Materialize(ctx, key, dest) error`.
- Plan 5 — `internal/autoload` exposing `Generate(ctx, projectDir string, packages []lock.Package, manifest *manifest.Manifest) error` which writes `vendor/autoload.php` plus the `composer/` files.

If any of those packages export different names, adjust the orchestrator wiring; the import surface here is the only file that needs changing.

---

## File structure

| Path | Responsibility |
|------|---------------|
| `internal/platform/platform.go` | Stage-1 stub: `Fingerprint() (string, error)` returns `"php-unknown"` |
| `internal/platform/platform_test.go` | Pin the stub return value so accidental changes break a test |
| `internal/orchestrator/orchestrator.go` | `Install(ctx, Options) error`, `Update(ctx, Options) error`, `Options` struct |
| `internal/orchestrator/pipeline.go` | Internal phase functions (resolve, fetch, materialize, autoload, write-lock) |
| `internal/orchestrator/cachekey.go` | Resolution-result cache key derivation + read/write |
| `internal/orchestrator/orchestrator_test.go` | Unit tests with fake source / fake fetcher |
| `internal/orchestrator/live_test.go` | End-to-end live test gated on `COMPOSER_GO_LIVE_NETWORK=1` |
| `internal/cli/install.go` | **Replace** the Plan 1 stub with `orchestrator.Install` |
| `internal/cli/update.go` | **Replace** the Plan 1 stub with `orchestrator.Update` |

---

## Task 1: Platform fingerprint stub

**Files:**
- Create: `internal/platform/platform.go`
- Create: `internal/platform/platform_test.go`

The stub exists so every cache key in this plan can include the platform fingerprint from day one. When stage 2 replaces the implementation with a real `php -r` probe, every existing resolution-result cache entry naturally invalidates.

- [ ] **Step 1: Write the failing test**

Create `internal/platform/platform_test.go`:

```go
package platform

import "testing"

func TestStubFingerprint(t *testing.T) {
	got, err := Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	// Pinned value: changing this string is a deliberate cache-bust.
	// Stage 2 will swap to a real `php -r` probe; until then this stays "php-unknown".
	if got != "php-unknown" {
		t.Errorf("Fingerprint = %q, want php-unknown", got)
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/platform/...`

Expected: build error on `Fingerprint`.

- [ ] **Step 3: Implement the stub**

Create `internal/platform/platform.go`:

```go
// Package platform exposes a stable fingerprint of the PHP environment that
// will execute the user's project. The fingerprint flows into every cache
// key so that a different PHP runtime invalidates resolution-result and
// classmap caches automatically.
//
// Stage 1 implementation is a stub: it returns "php-unknown". This keeps the
// shape of every downstream cache stable while the real probe is built in
// Stage 2 (see docs/superpowers/specs/2026-05-07-composer-go-design.md,
// section "Stage 2 — Real-world coverage", "Platform req detection").
//
// When Stage 2 lands and replaces this implementation with a real probe,
// every cache entry produced by Stage 1 will naturally miss because their
// key contains "php-unknown" while the new entries will contain something
// like "php-8.2.14;ext-mbstring;ext-json;...". That is the desired behavior.
package platform

// Fingerprint returns a stable string identifying the runtime PHP. Stage 1
// always returns "php-unknown".
func Fingerprint() (string, error) {
	return "php-unknown", nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/platform/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/platform
git commit -m "feat(platform): stage-1 stub fingerprint (\"php-unknown\")"
```

---

## Task 2: Orchestrator skeleton + Options

**Files:**
- Create: `internal/orchestrator/orchestrator.go`
- Create: `internal/orchestrator/orchestrator_test.go`

We start with the public surface and a no-op pipeline. Subsequent tasks fill in each phase. This task makes the build green with the new package importable.

- [ ] **Step 1: Write the failing test**

Create `internal/orchestrator/orchestrator_test.go`:

```go
package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallRequiresManifest(t *testing.T) {
	dir := t.TempDir()
	err := Install(context.Background(), Options{ProjectDir: dir})
	if err == nil {
		t.Fatal("Install with no composer.json should error")
	}
}

func TestInstallReadsManifest(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"),
		[]byte(`{"name":"vendor/pkg"}`), 0o644); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}
	// With NoNetwork=true and an empty require list, Install must succeed
	// without contacting Packagist.
	err := Install(context.Background(), Options{ProjectDir: dir, NoNetwork: true})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/orchestrator/...`

Expected: build error on `Install`, `Options`.

- [ ] **Step 3: Implement skeleton**

Create `internal/orchestrator/orchestrator.go`:

```go
// Package orchestrator drives the full install/update pipeline. It is the
// only package in composer-go that knows the order of phases:
//
//	read manifest -> [maybe read lock] -> [maybe consult resolution cache] ->
//	resolve -> fetch -> materialize vendor/ -> generate autoloader ->
//	write lock.
//
// Every other package exposes a narrow API. The orchestrator owns the
// errgroup, the worker pool, and the cancellation context.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/torstendittmann/composer-go/internal/manifest"
	"github.com/torstendittmann/composer-go/internal/registry"
)

// Options configures a single Install or Update run.
type Options struct {
	// ProjectDir is the directory containing composer.json. Required.
	ProjectDir string
	// NoDev mirrors --no-dev: skip require-dev and enforce platform
	// requirements strictly.
	NoDev bool
	// Verbose enables phase-timing logs.
	Verbose bool
	// Workers caps the parallel-fetch worker count. Zero -> runtime.NumCPU().
	Workers int
	// NoNetwork is a test hook: if set, the orchestrator must complete
	// without making network calls. Used by unit tests with empty manifests
	// and by future "offline mode" flags.
	NoNetwork bool
	// Source overrides the default Packagist source. Tests inject a fake
	// here. Production callers leave it nil.
	Source registry.SourceLookup
}

// Install runs the install pipeline: use the existing lockfile if present and
// up to date, otherwise resolve fresh.
func Install(ctx context.Context, opts Options) error {
	m, err := loadManifest(opts.ProjectDir)
	if err != nil {
		return err
	}
	return run(ctx, opts, m, false /* forceResolve */)
}

// Update runs the update pipeline: re-resolve every package regardless of
// the lockfile, then materialize.
func Update(ctx context.Context, opts Options) error {
	m, err := loadManifest(opts.ProjectDir)
	if err != nil {
		return err
	}
	return run(ctx, opts, m, true /* forceResolve */)
}

func loadManifest(projectDir string) (*manifest.Manifest, error) {
	if projectDir == "" {
		return nil, errors.New("orchestrator: ProjectDir is required")
	}
	path := filepath.Join(projectDir, "composer.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: read manifest: %w", err)
	}
	m, err := manifest.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: parse manifest: %w", err)
	}
	return m, nil
}

func workerCount(opt int) int {
	if opt > 0 {
		return opt
	}
	if n := runtime.NumCPU(); n > 0 {
		return n
	}
	return 4
}

// run is filled in by subsequent tasks. Stage 1: empty manifest path.
func run(ctx context.Context, opts Options, m *manifest.Manifest, forceResolve bool) error {
	if len(m.Require) == 0 && len(m.RequireDev) == 0 {
		// Nothing to do; the empty-pipeline test exercises this branch.
		return nil
	}
	return errors.New("orchestrator: pipeline not yet wired (later tasks)")
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/orchestrator/...`

Expected: PASS for both tests. The non-empty pipeline is wired in later tasks.

- [ ] **Step 5: Commit**

```bash
git add internal/orchestrator
git commit -m "feat(orchestrator): public Install/Update surface and Options"
```

---

## Task 3: Resolution-result cache key

**Files:**
- Create: `internal/orchestrator/cachekey.go`
- Modify: `internal/orchestrator/orchestrator_test.go`

The cache key combines `sha256(manifest bytes)`, `sha256(lock bytes if present, else empty)`, and the platform fingerprint. We compute the key here once per run; the same value is later used to key the resolution-result cache layer.

- [ ] **Step 1: Append failing tests**

Append to `internal/orchestrator/orchestrator_test.go`:

```go
func TestCacheKeyChangesWithManifest(t *testing.T) {
	a := computeCacheKey([]byte(`{"name":"a"}`), nil, "php-unknown")
	b := computeCacheKey([]byte(`{"name":"b"}`), nil, "php-unknown")
	if a == b {
		t.Errorf("expected different keys for different manifests, got %q", a)
	}
}

func TestCacheKeyStableForSameInputs(t *testing.T) {
	a := computeCacheKey([]byte(`{"name":"a"}`), []byte("lock"), "php-unknown")
	b := computeCacheKey([]byte(`{"name":"a"}`), []byte("lock"), "php-unknown")
	if a != b {
		t.Errorf("expected stable key, got %q vs %q", a, b)
	}
}

func TestCacheKeyChangesWithPlatform(t *testing.T) {
	a := computeCacheKey([]byte(`m`), nil, "php-unknown")
	b := computeCacheKey([]byte(`m`), nil, "php-8.2.0;ext-json")
	if a == b {
		t.Errorf("expected different keys for different platforms")
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/orchestrator/...`

Expected: build error on `computeCacheKey`.

- [ ] **Step 3: Implement key derivation + cache I/O**

Create `internal/orchestrator/cachekey.go`:

```go
package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/torstendittmann/composer-go/internal/cache"
	"github.com/torstendittmann/composer-go/internal/lock"
)

// computeCacheKey is the resolution-result cache key. It MUST be:
//   - deterministic for the same inputs (so warm runs hit)
//   - sensitive to manifest, lock content, and platform (so stale entries
//     cannot be served on changed inputs)
//
// We hash a length-prefixed encoding so that, e.g., manifest=[ab]/lock=[]
// cannot collide with manifest=[a]/lock=[b].
func computeCacheKey(manifestBytes, lockBytes []byte, platform string) string {
	h := sha256.New()
	writeLengthed(h, manifestBytes)
	writeLengthed(h, lockBytes)
	writeLengthed(h, []byte(platform))
	return hex.EncodeToString(h.Sum(nil))
}

func writeLengthed(h interface{ Write(p []byte) (int, error) }, b []byte) {
	var lenBuf [8]byte
	n := uint64(len(b))
	for i := 0; i < 8; i++ {
		lenBuf[i] = byte(n >> (8 * i))
	}
	_, _ = h.Write(lenBuf[:])
	_, _ = h.Write(b)
}

// resolutionCacheDir returns the directory where resolution-result entries
// live. Each entry is a serialized lock.File keyed by computeCacheKey.
func resolutionCacheDir() (string, error) {
	root, err := cache.Root()
	if err != nil {
		return "", err
	}
	d := filepath.Join(root, "resolution")
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", err
	}
	return d, nil
}

// loadResolution returns (file, true, nil) on cache hit, or (nil, false, nil)
// on miss. Decode failures are treated as miss (and the corrupt file evicted)
// because cache integrity is enforced by the spec's "evict + refetch, never
// silently serve corrupt data" rule.
func loadResolution(key string) (*lock.File, bool, error) {
	dir, err := resolutionCacheDir()
	if err != nil {
		return nil, false, err
	}
	path := filepath.Join(dir, key+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	f, err := lock.Decode(data)
	if err != nil {
		_ = os.Remove(path)
		return nil, false, nil
	}
	return f, true, nil
}

// storeResolution writes a resolved lock.File to the cache. Cache write
// failures are non-fatal at the call site; we still return them so callers
// can log them with --verbose.
func storeResolution(key string, f *lock.File) error {
	dir, err := resolutionCacheDir()
	if err != nil {
		return err
	}
	data, err := f.Encode()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, key+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("orchestrator: write resolution cache: %w", err)
	}
	return os.Rename(tmp, path)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/orchestrator/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/orchestrator
git commit -m "feat(orchestrator): resolution-result cache key + read/write"
```

---

## Task 4: Wire resolver — pipeline phase 1

**Files:**
- Create: `internal/orchestrator/pipeline.go`
- Modify: `internal/orchestrator/orchestrator.go`
- Modify: `internal/orchestrator/orchestrator_test.go`

This task wires Plans 1, 2, 3 together: read manifest, check resolution-result cache, otherwise call the resolver. We do NOT yet fetch or materialize. The pipeline returns the resolved `lock.File` for inspection.

- [ ] **Step 1: Append failing test (fake resolver path)**

Append to `internal/orchestrator/orchestrator_test.go`:

```go
import "github.com/torstendittmann/composer-go/internal/lock"

// fakeSource implements registry.SourceLookup for tests. Returns canned
// metadata; resolver is exercised through this in the resolveFunc indirection
// in pipeline.go.
type fakeSource struct {
	pkgs map[string]*registry.PackageMetadata
}

func (f *fakeSource) Lookup(_ context.Context, name string) (*registry.PackageMetadata, error) {
	if v, ok := f.pkgs[name]; ok {
		return v, nil
	}
	return nil, registry.ErrPackageNotFound
}

func TestResolveProducesLockFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"),
		[]byte(`{"name":"vendor/pkg","require":{"acme/leaf":"1.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	src := &fakeSource{pkgs: map[string]*registry.PackageMetadata{
		"acme/leaf": {Name: "acme/leaf", Versions: []registry.PackageVersion{{
			Name: "acme/leaf", Version: "1.0.0", VersionNorm: "1.0.0.0",
			Dist: registry.Dist{Type: "zip", URL: "http://fixture/leaf-1.0.0.zip", Sha: "deadbeef"},
		}}},
	}}

	// Inject a fetcher/materializer/autoloader stub via Options once those
	// are wired (Tasks 5–7). For now we directly call the helper that
	// produces the lock file and assert against it.
	got, err := resolveOnly(context.Background(), Options{ProjectDir: dir, Source: src})
	if err != nil {
		t.Fatalf("resolveOnly: %v", err)
	}
	if len(got.Packages) != 1 || got.Packages[0].Name != "acme/leaf" {
		t.Errorf("Packages = %+v", got.Packages)
	}
	if got.PlatformFingerprint != "php-unknown" {
		t.Errorf("PlatformFingerprint = %q", got.PlatformFingerprint)
	}
	if got.SchemaVersion != lock.SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, lock.SchemaVersion)
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/orchestrator/...`

Expected: build error on `resolveOnly`.

- [ ] **Step 3: Implement pipeline phase 1**

Create `internal/orchestrator/pipeline.go`:

```go
package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/torstendittmann/composer-go/internal/lock"
	"github.com/torstendittmann/composer-go/internal/manifest"
	"github.com/torstendittmann/composer-go/internal/platform"
	"github.com/torstendittmann/composer-go/internal/registry"
	"github.com/torstendittmann/composer-go/internal/resolver"
)

// pipelineState carries values across phases. Built once at the top of run().
type pipelineState struct {
	opts          Options
	manifest      *manifest.Manifest
	manifestBytes []byte
	lockBytes     []byte // existing lock contents, if any (nil means none)
	platform      string
	cacheKey      string
}

func newPipelineState(opts Options, m *manifest.Manifest) (*pipelineState, error) {
	manifestBytes, err := os.ReadFile(filepath.Join(opts.ProjectDir, "composer.json"))
	if err != nil {
		return nil, fmt.Errorf("orchestrator: read manifest bytes: %w", err)
	}
	lockBytes, _ := os.ReadFile(filepath.Join(opts.ProjectDir, "composer-go.lock"))
	pf, err := platform.Fingerprint()
	if err != nil {
		return nil, fmt.Errorf("orchestrator: platform fingerprint: %w", err)
	}
	return &pipelineState{
		opts:          opts,
		manifest:      m,
		manifestBytes: manifestBytes,
		lockBytes:     lockBytes,
		platform:      pf,
		cacheKey:      computeCacheKey(manifestBytes, lockBytes, pf),
	}, nil
}

// resolveFunc is the resolver entry point, indirected for tests.
var resolveFunc = func(ctx context.Context, m *manifest.Manifest, src registry.SourceLookup, includeDev bool) (*resolver.Result, error) {
	return resolver.Resolve(ctx, m, src, resolver.Options{IncludeDev: includeDev})
}

// resolveOrCache returns a fully populated lock.File. It either:
//   - returns the cached resolution if (manifest, lock, platform) matches,
//   - or runs the resolver and caches the result.
//
// forceResolve=true skips the cache (Update path).
func resolveOrCache(ctx context.Context, ps *pipelineState, forceResolve bool) (*lock.File, error) {
	if !forceResolve {
		if cached, ok, err := loadResolution(ps.cacheKey); err == nil && ok {
			return cached, nil
		}
	}

	src := ps.opts.Source
	if src == nil {
		return nil, fmt.Errorf("orchestrator: no registry source configured")
	}

	res, err := resolveFunc(ctx, ps.manifest, src, !ps.opts.NoDev)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: resolve: %w", err)
	}

	f := buildLockFile(ps, res)
	// Best-effort cache write. Resolution proceeds even if the cache write fails.
	_ = storeResolution(ps.cacheKey, f)
	return f, nil
}

func buildLockFile(ps *pipelineState, res *resolver.Result) *lock.File {
	manifestHash := sha256.Sum256(ps.manifestBytes)
	return &lock.File{
		SchemaVersion:       lock.SchemaVersion,
		Generator:           lock.Generator{Name: "composer-go", Version: "0.1.0"},
		ManifestContentHash: "sha256:" + hex.EncodeToString(manifestHash[:]),
		PlatformFingerprint: ps.platform,
		Stability: lock.Stability{
			MinimumStability: ps.manifest.MinimumStability,
			PreferStable:     ps.manifest.PreferStable,
		},
		Packages:    res.Packages,
		PackagesDev: res.PackagesDev,
	}
}

// resolveOnly is a test seam: run only the manifest + resolve phases.
func resolveOnly(ctx context.Context, opts Options) (*lock.File, error) {
	m, err := loadManifest(opts.ProjectDir)
	if err != nil {
		return nil, err
	}
	ps, err := newPipelineState(opts, m)
	if err != nil {
		return nil, err
	}
	return resolveOrCache(ctx, ps, true /* forceResolve, ignore cache in tests */)
}
```

- [ ] **Step 4: Update `run` to call `resolveOrCache`**

In `internal/orchestrator/orchestrator.go`, replace the `run` function with:

```go
func run(ctx context.Context, opts Options, m *manifest.Manifest, forceResolve bool) error {
	if len(m.Require) == 0 && len(m.RequireDev) == 0 {
		return nil
	}
	if opts.NoNetwork {
		return errors.New("orchestrator: NoNetwork is set but manifest has requires")
	}
	ps, err := newPipelineState(opts, m)
	if err != nil {
		return err
	}
	if _, err := resolveOrCache(ctx, ps, forceResolve); err != nil {
		return err
	}
	// Phases below are filled in by subsequent tasks.
	return errors.New("orchestrator: fetch/materialize/autoload not yet wired")
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/orchestrator/...`

Expected: `TestResolveProducesLockFile` PASSES; the empty-manifest test still PASSES; the `TestInstallReadsManifest` (NoNetwork) test still PASSES.

- [ ] **Step 6: Commit**

```bash
git add internal/orchestrator
git commit -m "feat(orchestrator): wire resolver phase + resolution-result cache"
```

---

## Task 5: Wire fetcher — concurrent download phase

**Files:**
- Modify: `internal/orchestrator/pipeline.go`
- Modify: `internal/orchestrator/orchestrator_test.go`
- Modify: `go.mod`, `go.sum` (add `golang.org/x/sync`)

The resolver gives us a list of `lock.Package` entries with `Dist.URL` + `Dist.Sha256`. We download them in parallel through `internal/fetcher` (Plan 4), bounded by the worker count from `Options.Workers`. Each successful fetch returns a content-store key.

- [ ] **Step 1: Add the dep**

Run: `go get golang.org/x/sync@latest`

Expected: `golang.org/x/sync` added to `go.mod`.

- [ ] **Step 2: Append failing test (fake fetcher)**

Append to `internal/orchestrator/orchestrator_test.go`:

```go
import "sync"

type fakeFetcher struct {
	mu       sync.Mutex
	calls    []string
	returnFn func(name string) (string, error)
}

func (f *fakeFetcher) Fetch(_ context.Context, pkg lock.Package) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, pkg.Name)
	f.mu.Unlock()
	if f.returnFn != nil {
		return f.returnFn(pkg.Name)
	}
	return "store-key-" + pkg.Name, nil
}

func TestFetchPhaseDownloadsAllPackages(t *testing.T) {
	pkgs := []lock.Package{
		{Name: "a/x", Version: "1.0.0", Dist: lock.Dist{Type: "zip", URL: "u1", Sha256: "s1"}},
		{Name: "b/y", Version: "2.0.0", Dist: lock.Dist{Type: "zip", URL: "u2", Sha256: "s2"}},
		{Name: "c/z", Version: "3.0.0", Dist: lock.Dist{Type: "zip", URL: "u3", Sha256: "s3"}},
	}
	ff := &fakeFetcher{}
	keys, err := fetchAll(context.Background(), pkgs, ff, 2)
	if err != nil {
		t.Fatalf("fetchAll: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("len(keys) = %d, want 3", len(keys))
	}
	for _, p := range pkgs {
		if keys[p.Name] != "store-key-"+p.Name {
			t.Errorf("keys[%s] = %q", p.Name, keys[p.Name])
		}
	}
}

func TestFetchPhaseSurfacesError(t *testing.T) {
	pkgs := []lock.Package{{Name: "bad/pkg", Dist: lock.Dist{URL: "u"}}}
	ff := &fakeFetcher{returnFn: func(string) (string, error) { return "", errors.New("network down") }}
	if _, err := fetchAll(context.Background(), pkgs, ff, 2); err == nil {
		t.Error("expected error when fetcher fails")
	}
}
```

Add `"errors"` to the imports if it isn't there yet.

- [ ] **Step 3: Verify failure**

Run: `go test ./internal/orchestrator/...`

Expected: build error on `fetchAll`, `Fetcher` interface.

- [ ] **Step 4: Implement the fetch phase**

Append to `internal/orchestrator/pipeline.go`:

```go
import (
	"sync"

	"golang.org/x/sync/errgroup"
)

// Fetcher downloads a single locked package and returns a content-store key
// (sha256 of the zip). Implemented by internal/fetcher.Client (Plan 4).
type Fetcher interface {
	Fetch(ctx context.Context, pkg lock.Package) (string, error)
}

// fetchAll downloads every package in pkgs concurrently with at most
// `workers` goroutines in flight. Returns map[name]storeKey.
//
// Cancellation: the first error cancels the group; in-flight goroutines see
// ctx.Err() and bail. The returned error is the first failure.
func fetchAll(ctx context.Context, pkgs []lock.Package, f Fetcher, workers int) (map[string]string, error) {
	if workers < 1 {
		workers = 1
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)

	var mu sync.Mutex
	keys := make(map[string]string, len(pkgs))

	for i := range pkgs {
		p := pkgs[i] // copy for closure
		g.Go(func() error {
			key, err := f.Fetch(gctx, p)
			if err != nil {
				return fmt.Errorf("orchestrator: fetch %s: %w", p.Name, err)
			}
			mu.Lock()
			keys[p.Name] = key
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return keys, nil
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/orchestrator/...`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/orchestrator
git commit -m "feat(orchestrator): bounded-parallel fetch phase via errgroup"
```

---

## Task 6: Wire materializer — write into vendor/

**Files:**
- Modify: `internal/orchestrator/pipeline.go`
- Modify: `internal/orchestrator/orchestrator_test.go`

For each locked package we ask the store (Plan 4) to materialize content from `<storeKey>` into `<projectDir>/vendor/<vendor>/<name>/`. The store uses reflink/clonefile/hardlink/copy as appropriate. Materialization is also bounded-parallel, but per-package independent so we use the same worker pool size.

- [ ] **Step 1: Append failing test (fake materializer)**

Append to `internal/orchestrator/orchestrator_test.go`:

```go
type fakeMaterializer struct {
	mu    sync.Mutex
	wrote map[string]string // dest -> storeKey
}

func (m *fakeMaterializer) Materialize(_ context.Context, key, dest string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.wrote == nil {
		m.wrote = make(map[string]string)
	}
	m.wrote[dest] = key
	return os.MkdirAll(dest, 0o755)
}

func TestMaterializePhasePopulatesVendor(t *testing.T) {
	dir := t.TempDir()
	pkgs := []lock.Package{
		{Name: "vendor/a", Version: "1.0.0"},
		{Name: "vendor/b", Version: "1.0.0"},
	}
	keys := map[string]string{
		"vendor/a": "key-a",
		"vendor/b": "key-b",
	}
	mz := &fakeMaterializer{}
	if err := materializeAll(context.Background(), dir, pkgs, keys, mz, 2); err != nil {
		t.Fatalf("materializeAll: %v", err)
	}
	if len(mz.wrote) != 2 {
		t.Fatalf("wrote %d, want 2: %+v", len(mz.wrote), mz.wrote)
	}
	wantA := filepath.Join(dir, "vendor", "vendor", "a")
	if got := mz.wrote[wantA]; got != "key-a" {
		t.Errorf("wrote[%s] = %q, want key-a", wantA, got)
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/orchestrator/...`

Expected: build error on `materializeAll`, `Materializer` interface.

- [ ] **Step 3: Implement materialize phase**

Append to `internal/orchestrator/pipeline.go`:

```go
// Materializer populates a destination directory from a content-store key.
// Implemented by internal/store.Store (Plan 4) using reflink/clonefile/hardlink/copy.
type Materializer interface {
	Materialize(ctx context.Context, key, dest string) error
}

func vendorPath(projectDir, packageName string) string {
	// "vendor/foo" -> projectDir/vendor/vendor/foo
	// "psr/log"    -> projectDir/vendor/psr/log
	return filepath.Join(projectDir, "vendor", filepath.FromSlash(packageName))
}

func materializeAll(ctx context.Context, projectDir string, pkgs []lock.Package, keys map[string]string, m Materializer, workers int) error {
	if workers < 1 {
		workers = 1
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)
	for i := range pkgs {
		p := pkgs[i]
		key, ok := keys[p.Name]
		if !ok {
			return fmt.Errorf("orchestrator: missing store key for %s", p.Name)
		}
		dest := vendorPath(projectDir, p.Name)
		g.Go(func() error {
			if err := m.Materialize(gctx, key, dest); err != nil {
				return fmt.Errorf("orchestrator: materialize %s: %w", p.Name, err)
			}
			return nil
		})
	}
	return g.Wait()
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/orchestrator/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/orchestrator
git commit -m "feat(orchestrator): bounded-parallel materialize into vendor/"
```

---

## Task 7: Wire autoloader generation

**Files:**
- Modify: `internal/orchestrator/pipeline.go`
- Modify: `internal/orchestrator/orchestrator_test.go`

After every package is materialized, generate `vendor/autoload.php` and helpers via `internal/autoload` (Plan 5). The autoloader sees both the resolved packages and the project's own `autoload` / `autoload-dev` entries from `composer.json`.

- [ ] **Step 1: Append failing test (fake generator)**

Append to `internal/orchestrator/orchestrator_test.go`:

```go
type fakeAutoloader struct {
	called      int
	gotPackages int
}

func (a *fakeAutoloader) Generate(_ context.Context, projectDir string, pkgs []lock.Package, m *manifest.Manifest) error {
	a.called++
	a.gotPackages = len(pkgs)
	return os.WriteFile(filepath.Join(projectDir, "vendor", "autoload.php"), []byte("<?php // stub\n"), 0o644)
}

func TestAutoloadPhaseInvokesGenerator(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "vendor"), 0o755); err != nil {
		t.Fatal(err)
	}
	gen := &fakeAutoloader{}
	pkgs := []lock.Package{{Name: "psr/log", Version: "3.0.0"}}
	m := &manifest.Manifest{Name: "vendor/pkg"}
	if err := generateAutoloader(context.Background(), dir, pkgs, m, gen); err != nil {
		t.Fatalf("generateAutoloader: %v", err)
	}
	if gen.called != 1 {
		t.Errorf("called %d times, want 1", gen.called)
	}
	if gen.gotPackages != 1 {
		t.Errorf("packages received = %d, want 1", gen.gotPackages)
	}
	if _, err := os.Stat(filepath.Join(dir, "vendor", "autoload.php")); err != nil {
		t.Errorf("vendor/autoload.php should exist: %v", err)
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/orchestrator/...`

Expected: build error on `generateAutoloader`, `Autoloader` interface.

- [ ] **Step 3: Implement autoload phase**

Append to `internal/orchestrator/pipeline.go`:

```go
// Autoloader generates vendor/autoload.php and the composer/ helper files.
// Implemented by internal/autoload (Plan 5).
type Autoloader interface {
	Generate(ctx context.Context, projectDir string, packages []lock.Package, m *manifest.Manifest) error
}

func generateAutoloader(ctx context.Context, projectDir string, pkgs []lock.Package, m *manifest.Manifest, a Autoloader) error {
	if err := a.Generate(ctx, projectDir, pkgs, m); err != nil {
		return fmt.Errorf("orchestrator: autoload: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/orchestrator/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/orchestrator
git commit -m "feat(orchestrator): wire autoloader generation phase"
```

---

## Task 8: Wire lockfile write

**Files:**
- Modify: `internal/orchestrator/pipeline.go`
- Modify: `internal/orchestrator/orchestrator_test.go`

After autoloader generation, write `composer-go.lock` to the project directory. We write atomically via temp-file + rename to avoid partially-written lockfiles on a crash.

- [ ] **Step 1: Append failing test**

Append to `internal/orchestrator/orchestrator_test.go`:

```go
func TestWriteLockProducesValidJSON(t *testing.T) {
	dir := t.TempDir()
	f := &lock.File{
		SchemaVersion: lock.SchemaVersion,
		Generator:     lock.Generator{Name: "composer-go", Version: "0.1.0"},
		Packages:      []lock.Package{{Name: "psr/log", Version: "3.0.0"}},
	}
	if err := writeLock(dir, f); err != nil {
		t.Fatalf("writeLock: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "composer-go.lock"))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	out, err := lock.Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Packages) != 1 || out.Packages[0].Name != "psr/log" {
		t.Errorf("decoded lock: %+v", out)
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/orchestrator/...`

Expected: build error on `writeLock`.

- [ ] **Step 3: Implement writeLock**

Append to `internal/orchestrator/pipeline.go`:

```go
// writeLock serializes f and writes it atomically to composer-go.lock.
func writeLock(projectDir string, f *lock.File) error {
	data, err := f.Encode()
	if err != nil {
		return fmt.Errorf("orchestrator: encode lock: %w", err)
	}
	final := filepath.Join(projectDir, "composer-go.lock")
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("orchestrator: write lock: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("orchestrator: rename lock: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/orchestrator/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/orchestrator
git commit -m "feat(orchestrator): atomic composer-go.lock write"
```

---

## Task 9: Compose all phases — full pipeline

**Files:**
- Modify: `internal/orchestrator/orchestrator.go`
- Modify: `internal/orchestrator/pipeline.go`
- Modify: `internal/orchestrator/orchestrator_test.go`

Tie every phase into `run`. Add a `defaultDeps` builder that constructs the production `Fetcher`, `Materializer`, and `Autoloader` from their respective packages, and let tests inject fakes via `Options`.

- [ ] **Step 1: Add fake-injection fields to Options + a helper test**

In `internal/orchestrator/orchestrator.go`, extend `Options`:

```go
type Options struct {
	ProjectDir string
	NoDev      bool
	Verbose    bool
	Workers    int
	NoNetwork  bool
	Source     registry.SourceLookup

	// Test-only injection points. Production callers leave these nil and
	// the orchestrator constructs the real implementations.
	Fetcher      Fetcher
	Materializer Materializer
	Autoloader   Autoloader
}
```

Append a full-pipeline test to `internal/orchestrator/orchestrator_test.go`:

```go
func TestInstallFullPipelineWithFakes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"),
		[]byte(`{"name":"vendor/pkg","require":{"acme/leaf":"1.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{pkgs: map[string]*registry.PackageMetadata{
		"acme/leaf": {Name: "acme/leaf", Versions: []registry.PackageVersion{{
			Name: "acme/leaf", Version: "1.0.0", VersionNorm: "1.0.0.0",
			Dist: registry.Dist{Type: "zip", URL: "u", Sha: "s"},
		}}},
	}}
	opts := Options{
		ProjectDir:   dir,
		Source:       src,
		Fetcher:      &fakeFetcher{},
		Materializer: &fakeMaterializer{},
		Autoloader:   &fakeAutoloader{},
		Workers:      2,
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "composer-go.lock")); err != nil {
		t.Errorf("composer-go.lock not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "vendor", "autoload.php")); err != nil {
		t.Errorf("vendor/autoload.php not written: %v", err)
	}
}

func TestInstallUsesResolutionCacheOnSecondRun(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"),
		[]byte(`{"name":"vendor/pkg","require":{"acme/leaf":"1.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{pkgs: map[string]*registry.PackageMetadata{
		"acme/leaf": {Name: "acme/leaf", Versions: []registry.PackageVersion{{
			Name: "acme/leaf", Version: "1.0.0", VersionNorm: "1.0.0.0",
			Dist: registry.Dist{Type: "zip", URL: "u", Sha: "s"},
		}}},
	}}

	hits := 0
	originalResolve := resolveFunc
	t.Cleanup(func() { resolveFunc = originalResolve })
	resolveFunc = func(ctx context.Context, m *manifest.Manifest, _ registry.SourceLookup, includeDev bool) (*resolver.Result, error) {
		hits++
		return originalResolve(ctx, m, src, includeDev)
	}

	opts := Options{
		ProjectDir:   dir,
		Source:       src,
		Fetcher:      &fakeFetcher{},
		Materializer: &fakeMaterializer{},
		Autoloader:   &fakeAutoloader{},
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Errorf("resolver invoked %d times across two Install calls; want 1 (second should hit cache)", hits)
	}
}
```

- [ ] **Step 2: Implement default deps + composed pipeline**

Append to `internal/orchestrator/pipeline.go`:

```go
import (
	"github.com/torstendittmann/composer-go/internal/autoload"
	"github.com/torstendittmann/composer-go/internal/cache"
	"github.com/torstendittmann/composer-go/internal/fetcher"
	"github.com/torstendittmann/composer-go/internal/registry/packagist"
	"github.com/torstendittmann/composer-go/internal/store"
)

// defaultDeps wires up the production Fetcher, Materializer, Autoloader, and
// (if absent) Source. Tests typically pre-populate Options so this returns
// quickly with what's already there.
func defaultDeps(opts *Options) error {
	cacheRoot, err := cache.Root()
	if err != nil {
		return err
	}
	if opts.Source == nil {
		c, err := packagist.New(packagist.Config{
			CacheDir: filepath.Join(cacheRoot, "packagist"),
		})
		if err != nil {
			return err
		}
		opts.Source = c
	}
	if opts.Fetcher == nil {
		opts.Fetcher = fetcher.New(fetcher.Config{
			CacheDir: filepath.Join(cacheRoot, "dist"),
			StoreDir: filepath.Join(opts.ProjectDir, ".composer-go", "store"),
		})
	}
	if opts.Materializer == nil {
		opts.Materializer = store.New(filepath.Join(opts.ProjectDir, ".composer-go", "store"))
	}
	if opts.Autoloader == nil {
		opts.Autoloader = autoload.New()
	}
	return nil
}

func runFullPipeline(ctx context.Context, opts Options, m *manifest.Manifest, forceResolve bool) error {
	if err := defaultDeps(&opts); err != nil {
		return err
	}
	ps, err := newPipelineState(opts, m)
	if err != nil {
		return err
	}
	lockFile, err := resolveOrCache(ctx, ps, forceResolve)
	if err != nil {
		return err
	}

	all := append([]lock.Package(nil), lockFile.Packages...)
	if !opts.NoDev {
		all = append(all, lockFile.PackagesDev...)
	}

	keys, err := fetchAll(ctx, all, opts.Fetcher, workerCount(opts.Workers))
	if err != nil {
		return err
	}
	if err := materializeAll(ctx, opts.ProjectDir, all, keys, opts.Materializer, workerCount(opts.Workers)); err != nil {
		return err
	}
	if err := generateAutoloader(ctx, opts.ProjectDir, all, m, opts.Autoloader); err != nil {
		return err
	}
	if err := writeLock(opts.ProjectDir, lockFile); err != nil {
		return err
	}
	return nil
}
```

- [ ] **Step 3: Replace `run` in `orchestrator.go`**

```go
func run(ctx context.Context, opts Options, m *manifest.Manifest, forceResolve bool) error {
	if len(m.Require) == 0 && len(m.RequireDev) == 0 {
		return nil
	}
	if opts.NoNetwork {
		return errors.New("orchestrator: NoNetwork is set but manifest has requires")
	}
	return runFullPipeline(ctx, opts, m, forceResolve)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/orchestrator/...`

Expected: all PASS, including `TestInstallFullPipelineWithFakes` and `TestInstallUsesResolutionCacheOnSecondRun`.

- [ ] **Step 5: Commit**

```bash
git add internal/orchestrator
git commit -m "feat(orchestrator): compose resolve+fetch+materialize+autoload+lock pipeline"
```

---

## Task 10: CLI rewiring — install + update

**Files:**
- Modify: `internal/cli/install.go`
- Modify: `internal/cli/update.go`
- Modify: `internal/cli/install_test.go` (delete or rewrite the Plan 1 stub-summary test)

Replace the Plan 1 stubs with real calls into `orchestrator.Install` / `orchestrator.Update`. Honor `--verbose`, `--no-dev`, and a new `--project` flag (carried over from Plan 1's smoke-test wiring).

- [ ] **Step 1: Rewrite `install.go`**

Replace `internal/cli/install.go`:

```go
package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/torstendittmann/composer-go/internal/orchestrator"
)

func newInstallCmd() *cobra.Command {
	var projectDir string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install dependencies into vendor/ from composer.json (using composer-go.lock if present)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if projectDir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				projectDir = wd
			}
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			return orchestrator.Install(ctx, orchestrator.Options{
				ProjectDir: projectDir,
				NoDev:      flagNoDev,
				Verbose:    flagVerbose,
			})
		},
	}
	cmd.Flags().StringVar(&projectDir, "project", "", "project directory containing composer.json (defaults to cwd)")
	return cmd
}
```

- [ ] **Step 2: Rewrite `update.go`**

Replace `internal/cli/update.go`:

```go
package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/torstendittmann/composer-go/internal/orchestrator"
)

func newUpdateCmd() *cobra.Command {
	var projectDir string
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Re-resolve all dependencies and rewrite composer-go.lock + vendor/",
		RunE: func(cmd *cobra.Command, args []string) error {
			if projectDir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				projectDir = wd
			}
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			return orchestrator.Update(ctx, orchestrator.Options{
				ProjectDir: projectDir,
				NoDev:      flagNoDev,
				Verbose:    flagVerbose,
			})
		},
	}
	cmd.Flags().StringVar(&projectDir, "project", "", "project directory containing composer.json (defaults to cwd)")
	return cmd
}
```

- [ ] **Step 3: Update or delete the Plan 1 install test**

The Plan 1 test asserts on a "manifest summary" stdout line. That output no longer exists. Either delete `internal/cli/install_test.go` or rewrite it to assert "Install with no composer.json fails":

```go
package cli

import (
	"bytes"
	"testing"
)

func TestInstallFailsWithoutManifest(t *testing.T) {
	var stdout bytes.Buffer
	root := newRootCmd()
	root.SetOut(&stdout)
	root.SetErr(&stdout)
	root.SetArgs([]string{"install", "--project", t.TempDir()})
	if err := root.Execute(); err == nil {
		t.Error("install with no composer.json should fail")
	}
}
```

- [ ] **Step 4: Build + smoke**

```bash
go build ./cmd/composer-go
./composer-go install --help
```

Expected: help text mentions `--project`, `--no-dev`, `--verbose`.

- [ ] **Step 5: Run tests**

Run: `go test ./...`

Expected: all PASS (offline; network tests still skipped).

- [ ] **Step 6: Commit**

```bash
git add internal/cli
git commit -m "feat(cli): wire install and update to the orchestrator"
```

---

## Task 11: End-to-end live test

**Files:**
- Create: `internal/orchestrator/live_test.go`

The acceptance test for stage 1. We use `psr/log` because:
- It is on Packagist (no VCS support yet — that's Stage 2).
- It is a leaf package (no transitive deps with platform reqs, so the stub fingerprint doesn't break resolution).
- It is small: zip is ~10 KB, makes the warm-cache <100ms target achievable.
- It is widely used and unlikely to disappear.

Gated on `COMPOSER_GO_LIVE_NETWORK=1` so CI doesn't hit Packagist on every PR.

- [ ] **Step 1: Write the live test**

Create `internal/orchestrator/live_test.go`:

```go
package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLiveInstallPsrLog is the stage-1 acceptance test: install a real
// Packagist project end-to-end, then re-install on a warm cache in <100ms.
//
// Gated on COMPOSER_GO_LIVE_NETWORK=1.
func TestLiveInstallPsrLog(t *testing.T) {
	if os.Getenv("COMPOSER_GO_LIVE_NETWORK") != "1" {
		t.Skip("set COMPOSER_GO_LIVE_NETWORK=1 to run this test against real Packagist")
	}

	// Isolate caches: we want a clean cold path on the first run.
	cacheHome := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheHome)

	projectDir := t.TempDir()
	manifestPath := filepath.Join(projectDir, "composer.json")
	manifest := []byte(`{
  "name": "composer-go-test/live",
  "type": "library",
  "require": { "psr/log": "^3.0" }
}`)
	if err := os.WriteFile(manifestPath, manifest, 0o644); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}

	ctx := context.Background()

	// --- Cold install ---
	if err := Install(ctx, Options{ProjectDir: projectDir, Verbose: true}); err != nil {
		t.Fatalf("cold Install: %v", err)
	}

	if _, err := os.Stat(filepath.Join(projectDir, "vendor", "psr", "log")); err != nil {
		t.Errorf("vendor/psr/log not materialized: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "vendor", "autoload.php")); err != nil {
		t.Errorf("vendor/autoload.php not generated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "composer-go.lock")); err != nil {
		t.Errorf("composer-go.lock not written: %v", err)
	}

	// At least one PHP source file should be present in the materialized package.
	entries, err := os.ReadDir(filepath.Join(projectDir, "vendor", "psr", "log"))
	if err != nil || len(entries) == 0 {
		t.Errorf("vendor/psr/log appears empty: %v", err)
	}

	// --- Warm install ---
	// Wipe vendor/ to force re-materialization but keep all caches and the
	// existing lockfile. This simulates "user nuked vendor and re-ran install".
	if err := os.RemoveAll(filepath.Join(projectDir, "vendor")); err != nil {
		t.Fatalf("rm vendor: %v", err)
	}

	start := time.Now()
	if err := Install(ctx, Options{ProjectDir: projectDir}); err != nil {
		t.Fatalf("warm Install: %v", err)
	}
	elapsed := time.Since(start)
	t.Logf("warm install elapsed: %v", elapsed)

	if elapsed > 100*time.Millisecond {
		t.Errorf("warm install took %v, want <100ms (stage-1 acceptance criterion)", elapsed)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "vendor", "psr", "log")); err != nil {
		t.Errorf("vendor/psr/log not re-materialized on warm run: %v", err)
	}
}

// TestLiveUpdateRewritesLock exercises the update path against real Packagist.
// Gated identically.
func TestLiveUpdateRewritesLock(t *testing.T) {
	if os.Getenv("COMPOSER_GO_LIVE_NETWORK") != "1" {
		t.Skip("set COMPOSER_GO_LIVE_NETWORK=1")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"),
		[]byte(`{"name":"x/y","require":{"psr/log":"^3.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Update(context.Background(), Options{ProjectDir: dir}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "composer-go.lock")); err != nil {
		t.Errorf("composer-go.lock not written by update: %v", err)
	}
}
```

- [ ] **Step 2: Run the live test**

Run: `COMPOSER_GO_LIVE_NETWORK=1 go test ./internal/orchestrator/... -run TestLive -v`

Expected:
- Cold install completes in 1–5 seconds (network-bound).
- `vendor/psr/log/`, `vendor/autoload.php`, `composer-go.lock` exist.
- Warm install completes in <100ms.

If the warm install exceeds 100ms, the bottleneck is one of:
- Resolution-result cache miss → check `loadResolution` returns `(file, true, nil)`.
- Materializer falling back to copy when reflink should work → check `internal/store` is reflinking on APFS/Linux.
- JSON parsing of metadata not hitting the parsed-manifest cache → check `parsedcache` directory has entries after the cold run.

Investigate via `--verbose` timing output (added in Stage 3, but ad-hoc `t.Log` works for now).

- [ ] **Step 3: Commit**

```bash
git add internal/orchestrator/live_test.go
git commit -m "test(orchestrator): live psr/log install + warm-cache <100ms"
```

---

## Task 12: CLI smoke + final acceptance

**Files:**
- None (verification-only)

Run the binary against a real project to confirm the user-facing UX matches the stage-1 acceptance criteria from the design spec.

- [ ] **Step 1: Build**

```bash
go build -o composer-go ./cmd/composer-go
```

Expected: binary in repo root, ~5–15 MB.

- [ ] **Step 2: Cold install with the binary**

```bash
SMOKE=$(mktemp -d)
cat > $SMOKE/composer.json <<'EOF'
{
  "name": "composer-go-smoke/test",
  "type": "library",
  "require": { "psr/log": "^3.0" }
}
EOF
./composer-go install --project $SMOKE
ls $SMOKE/vendor/psr/log
ls $SMOKE/vendor/autoload.php
ls $SMOKE/composer-go.lock
```

Expected: `psr/log/` directory with PHP files, `autoload.php` exists, `composer-go.lock` exists. Exit code 0.

- [ ] **Step 3: Warm install timing with the binary**

```bash
rm -rf $SMOKE/vendor
time ./composer-go install --project $SMOKE
```

Expected: real time <100ms.

- [ ] **Step 4: Update path**

```bash
./composer-go update --project $SMOKE
```

Expected: exits 0; `composer-go.lock` is rewritten (mtime advances).

- [ ] **Step 5: Cancellation works**

```bash
rm -rf ~/.cache/composer-go ~/Library/Caches/composer-go $SMOKE/vendor
( ./composer-go install --project $SMOKE & echo $! > /tmp/cg.pid ; wait )
# In another terminal during the run: kill -INT $(cat /tmp/cg.pid)
```

Expected: process exits non-zero with a context-cancelled-style message (manual; only run if you want to confirm graceful cancellation).

- [ ] **Step 6: Final test sweep**

```bash
go test ./...
COMPOSER_GO_LIVE_NETWORK=1 go test ./...
```

Expected: both green. Live run includes `TestLiveInstallPsrLog`, `TestLiveUpdateRewritesLock`, and the Plan 2 live Packagist test.

---

## Stage 1 acceptance check

This is the spec's stage-1 acceptance bar (design spec section "Stage 1 — Core install path"):

- [ ] `composer-go install` on a small real Packagist project (`psr/log`) succeeds.
- [ ] The generated `vendor/autoload.php` exists in `<project>/vendor/`.
- [ ] `composer-go.lock` exists with at least one entry.
- [ ] Repeat install on a warm cache completes in <100ms (measured by `TestLiveInstallPsrLog` and the Task 12 step-3 manual run).
- [ ] `composer-go update` rewrites the lockfile and exits 0.
- [ ] All four cache layers (HTTP, content-addressed package store, resolution-result, parsed-manifest) are exercised on the cold path and short-circuit the hot path.
- [ ] Cancellation via SIGINT propagates through the orchestrator and stops in-flight downloads.
- [ ] `go test ./...` is green offline; live tests are green with `COMPOSER_GO_LIVE_NETWORK=1`.
- [ ] Stub `platform.Fingerprint()` returns `"php-unknown"` and is referenced in every cache key (manifest hash, lock hash, fingerprint) — verifiable by inspecting `computeCacheKey` callers and grepping for `Fingerprint()`.

If any item fails, fix forward in a follow-up commit before declaring stage 1 done. Leave Stage 2 work (real PHP detection, VCS, classmap, files autoloader, scripts) for the next plan series; do not retrofit any of it into this plan.
