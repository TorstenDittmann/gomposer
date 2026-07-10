# Metadata Prefetch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Warm the registry metadata cache (Packagist `/p2/<name>.json` responses) in the background so the resolver's per-package `SourceLookup.Lookup` calls hit warm cache instead of triggering serial HTTP round-trips.

**Architecture:** Mirror the shape of the existing Stage 3 artifact prefetch at `internal/orchestrator/prefetch.go`. New file `metadata_prefetch.go` defines a `MetadataPrefetcher` with a bounded worker pool that calls `opts.Source.Lookup` (which the packagist adapter populates into `httpcache` + `parsedcache` transparently). Wired into `runFullPipeline` next to `maybeStartPrefetch`.

**Tech Stack:** Go 1.25, standard library, `golang.org/x/sync/errgroup` (already in use).

## Global Constraints

- **Warm set:** union of
  1. Every key in `manifest.Require`.
  2. If `!opts.NoDev`, every key in `manifest.RequireDev`.
  3. If `ps.lockBytes` decodes cleanly, every `Name` in `Packages` and (if `!opts.NoDev`) `PackagesDev`.
  4. **Excluded:** any name matching `platform.IsPlatformReq` (`php`, `php-*` without `/`, `ext-*`, `lib-*`).
  5. **Deduplicated:** a `map[string]struct{}` collects names; each is fetched at most once.
- **Concurrency:** bounded pool via `errgroup.WithContext`, limit = `workerCount(opts.Workers)` (matches artifact prefetch).
- **Error policy:** prefetch errors are captured but NOT propagated as pipeline failures. The resolver's own on-demand `Lookup` is authoritative.
- **Disable rules:** prefetch is skipped when
  - `opts.NoMetadataPrefetch` is true,
  - `opts.NoNetwork` is true,
  - `opts.Source` is nil (test wiring failure),
  - the warm set is empty.
- **CLI flag:** `--no-metadata-prefetch`. Persistent flag on root command. Help text: `"disable resolver-metadata prefetch (benchmarking hook)"`.
- **Verbose line:** when `opts.Verbose`, append one line to the timing block: `metadata-prefetch: <n> warmed in <duration>`.
- **Non-blocking:** the prefetch runs concurrently with resolve/fetch/materialize and is awaited via `Wait()` before the pipeline returns.

---

## File structure

| Path | Responsibility |
|------|---------------|
| `internal/orchestrator/metadata_prefetch.go` | New. `MetadataPrefetcher`, `maybeStartMetadataPrefetch`, `Wait`. |
| `internal/orchestrator/metadata_prefetch_test.go` | New. Unit tests for pool, dedup, filtering, off-switch. |
| `internal/orchestrator/orchestrator.go` | Add `NoMetadataPrefetch bool` to `Options`. |
| `internal/orchestrator/pipeline.go` | Kick off `maybeStartMetadataPrefetch` in `runFullPipeline`; add its stats to the verbose timing block; `Wait` before returning. |
| `internal/orchestrator/pipeline_test.go` | Integration test: wall-time reduction with a slow-fake source. |
| `internal/cli/root.go` | New persistent flag `--no-metadata-prefetch` + wiring to `Options.NoMetadataPrefetch`. |
| `internal/cli/plugins_flag_test.go` (or a sibling file) | CLI test asserting the flag reaches Options. |

---

## Task 1: `MetadataPrefetcher` type + `Wait` + noop path

**Files:**
- Create: `internal/orchestrator/metadata_prefetch.go`
- Create: `internal/orchestrator/metadata_prefetch_test.go`

**Interfaces:**
- Consumes: `context.Context`, `registry.SourceLookup`.
- Produces:
  - `type MetadataPrefetcher struct { ... }` — unexported fields; only `Wait` is exported.
  - `func (p *MetadataPrefetcher) Wait()` — idempotent, safe to call on a noop instance.
  - Unexported constructor `newNoopMetadataPrefetcher() *MetadataPrefetcher` — returns an instance whose `Wait` is a no-op. Used by later tasks for the "prefetch disabled" branch.

- [ ] **Step 1: Write the failing test**

Create `internal/orchestrator/metadata_prefetch_test.go`:

```go
package orchestrator

import "testing"

func TestNoopMetadataPrefetcherWaitReturnsImmediately(t *testing.T) {
	p := newNoopMetadataPrefetcher()
	// Wait must return immediately for the noop; a deadlock or panic here is
	// a bug. We rely on the test harness's default timeout to catch a hang.
	p.Wait()
}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/orchestrator/ -run TestNoopMetadataPrefetcher -v`

Expected: build error referencing `newNoopMetadataPrefetcher`, `MetadataPrefetcher`.

- [ ] **Step 3: Write the skeleton**

Create `internal/orchestrator/metadata_prefetch.go`:

```go
// Metadata prefetch — warms Packagist metadata (/p2/<name>.json) in the
// background so the resolver's synchronous Lookup calls hit warm cache.
// Symmetric with prefetch.go (artifact zips).
package orchestrator

import (
	"context"
	"sync"
	"time"
)

// MetadataPrefetcher is the runtime handle returned by
// maybeStartMetadataPrefetch. Callers Wait() unconditionally at the end
// of the pipeline; the noop variant makes that safe.
type MetadataPrefetcher struct {
	wg    sync.WaitGroup
	stats prefetchStats
}

// prefetchStats records outcome for the verbose timing block. Populated
// atomically by worker goroutines in Task 2.
type prefetchStats struct {
	mu       sync.Mutex
	warmed   int
	errors   int
	duration time.Duration
}

// Wait blocks until every worker goroutine returns. Safe to call on a
// noop instance (constructed via newNoopMetadataPrefetcher).
func (p *MetadataPrefetcher) Wait() {
	p.wg.Wait()
}

// newNoopMetadataPrefetcher returns a zero-value MetadataPrefetcher whose
// Wait returns immediately. Used when metadata prefetch is disabled or
// there is nothing to warm.
func newNoopMetadataPrefetcher() *MetadataPrefetcher {
	return &MetadataPrefetcher{}
}

// unused imports guard for later tasks; remove when Task 2 uses them.
var _ = context.Background
```

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/orchestrator/ -run TestNoopMetadataPrefetcher -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/orchestrator/metadata_prefetch.go internal/orchestrator/metadata_prefetch_test.go
git commit -m "feat(orchestrator): MetadataPrefetcher skeleton + noop path

Introduces the runtime handle for the upcoming metadata prefetch. Only
the noop path is wired for now — Wait() returns immediately, which lets
subsequent tasks add the real bounded-pool goroutines without touching
call sites."
```

---

## Task 2: Warm-set assembly + bounded-pool workers

**Files:**
- Modify: `internal/orchestrator/metadata_prefetch.go`
- Modify: `internal/orchestrator/metadata_prefetch_test.go`

**Interfaces:**
- Consumes: `orchestrator.Options`, `pipelineState`, `registry.SourceLookup`, `platform.IsPlatformReq`, `lock.Decode`, `workerCount`.
- Produces:
  - `func maybeStartMetadataPrefetch(ctx context.Context, ps *pipelineState, opts Options) *MetadataPrefetcher` — real constructor. Returns a noop instance when prefetch should be skipped.
  - Unexported helper `collectMetadataPrefetchNames(ps *pipelineState, noDev bool) []string` — assembles the warm set. Extracted for direct unit testing.

- [ ] **Step 1: Write the failing tests**

Append to `internal/orchestrator/metadata_prefetch_test.go`:

```go
import (
	// existing imports
	"context"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/torstendittmann/gomposer/internal/manifest"
	"github.com/torstendittmann/gomposer/internal/registry"
)

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
				"a/a":       "^1",
				"php":       ">=8.1",
				"ext-json":  "*",
				"lib-curl":  "*",
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
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/orchestrator/ -run 'TestCollectMetadataPrefetchNames|TestMaybeStartMetadataPrefetch' -v`

Expected: compile errors referencing `collectMetadataPrefetchNames`, `maybeStartMetadataPrefetch`, `NoMetadataPrefetch`.

- [ ] **Step 3: Add `NoMetadataPrefetch` field to `Options`**

In `internal/orchestrator/orchestrator.go`, near the existing `NoPrefetch bool` field:

```go
// NoMetadataPrefetch disables the resolver-metadata prefetch. Default
// (false) means prefetch is on. Symmetric to NoPrefetch (which controls
// the artifact prefetch). Mostly useful for benchmarks measuring the
// isolated wall-clock contribution.
//
// Metadata prefetch is also implicitly disabled when:
//   - NoNetwork is true,
//   - opts.Source is nil,
//   - the warm set is empty.
NoMetadataPrefetch bool
```

- [ ] **Step 4: Implement `collectMetadataPrefetchNames`**

In `internal/orchestrator/metadata_prefetch.go`, add:

```go
import (
    "github.com/torstendittmann/gomposer/internal/lock"
    "github.com/torstendittmann/gomposer/internal/platform"
    "golang.org/x/sync/errgroup"
)

// collectMetadataPrefetchNames unions the manifest requires with the
// existing lock's package list, filters out platform reqs, and returns a
// deduplicated slice.
func collectMetadataPrefetchNames(ps *pipelineState, includeDev bool) []string {
    if ps == nil || ps.manifest == nil {
        return nil
    }
    seen := map[string]struct{}{}
    add := func(name string) {
        if platform.IsPlatformReq(name) {
            return
        }
        seen[name] = struct{}{}
    }
    for name := range ps.manifest.Require {
        add(name)
    }
    if includeDev {
        for name := range ps.manifest.RequireDev {
            add(name)
        }
    }
    if len(ps.lockBytes) > 0 {
        if lf, err := lock.Decode(ps.lockBytes); err == nil {
            for _, p := range lf.Packages {
                add(p.Name)
            }
            if includeDev {
                for _, p := range lf.PackagesDev {
                    add(p.Name)
                }
            }
        }
    }
    out := make([]string, 0, len(seen))
    for name := range seen {
        out = append(out, name)
    }
    return out
}
```

- [ ] **Step 5: Implement `maybeStartMetadataPrefetch`**

Add to the same file:

```go
// maybeStartMetadataPrefetch begins warming metadata caches for the
// upcoming resolve. Returns a noop *MetadataPrefetcher when prefetch is
// disabled or there is nothing to warm. Callers Wait() unconditionally.
func maybeStartMetadataPrefetch(ctx context.Context, ps *pipelineState, opts Options) *MetadataPrefetcher {
    if opts.NoMetadataPrefetch || opts.NoNetwork || opts.Source == nil {
        return newNoopMetadataPrefetcher()
    }
    names := collectMetadataPrefetchNames(ps, !opts.NoDev)
    if len(names) == 0 {
        return newNoopMetadataPrefetcher()
    }
    p := &MetadataPrefetcher{}
    p.wg.Add(1)
    start := time.Now()
    go func() {
        defer p.wg.Done()
        g, gctx := errgroup.WithContext(ctx)
        g.SetLimit(workerCount(opts.Workers))
        for _, name := range names {
            name := name
            g.Go(func() error {
                if _, err := opts.Source.Lookup(gctx, name); err != nil {
                    p.stats.mu.Lock()
                    p.stats.errors++
                    p.stats.mu.Unlock()
                    return nil // errors do not propagate
                }
                p.stats.mu.Lock()
                p.stats.warmed++
                p.stats.mu.Unlock()
                return nil
            })
        }
        _ = g.Wait()
        p.stats.mu.Lock()
        p.stats.duration = time.Since(start)
        p.stats.mu.Unlock()
    }()
    return p
}
```

Remove the `var _ = context.Background` guard added in Task 1.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/orchestrator/ -v -run 'TestNoop|TestCollect|TestMaybeStart'`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/orchestrator/metadata_prefetch.go internal/orchestrator/metadata_prefetch_test.go internal/orchestrator/orchestrator.go
git commit -m "feat(orchestrator): metadata prefetch pool

maybeStartMetadataPrefetch warms Packagist metadata (/p2/<name>.json)
in the background. Warm set = manifest.Require ∪ (RequireDev when
--no-dev is not set) ∪ existing-lock package names, minus platform
reqs, deduplicated. Concurrency = workerCount(opts.Workers) via
errgroup. Errors are counted but never propagated as pipeline failures
— the resolver's own on-demand Lookup remains authoritative."
```

---

## Task 3: Wire into `runFullPipeline` and verbose timing

**Files:**
- Modify: `internal/orchestrator/pipeline.go`
- Modify: `internal/orchestrator/pipeline_test.go`

**Interfaces:**
- Consumes: `maybeStartMetadataPrefetch`, `MetadataPrefetcher.Wait`.
- Produces: no new exports.

- [ ] **Step 1: Locate the wire-in point**

In `internal/orchestrator/pipeline.go`, find the block near where `maybeStartPrefetch` is called inside `runFullPipeline`. That's the correct sibling location.

- [ ] **Step 2: Write the integration test**

Add to `internal/orchestrator/pipeline_test.go` (adapt input helpers to the file's existing conventions):

```go
// TestMetadataPrefetchReducesResolveWallTime asserts that when the resolver
// has to look up N packages against a source whose Lookup takes non-
// negligible time, the total pipeline duration is smaller with metadata
// prefetch enabled than without it. We do not assert an exact ratio (to
// avoid flakiness) — just that prefetch is materially faster.
func TestMetadataPrefetchReducesResolveWallTime(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-sensitive; skipping under -short")
	}
	// Build a slow-fake registry: each Lookup sleeps 40ms. This is not the
	// resolver's actual behavior (it does a lot more than a single Lookup)
	// but it's enough to expose the parallel vs. serial gap for a plausible
	// number of packages.
	slow := &sleepySourceLookup{delay: 40 * time.Millisecond, versions: fakeMultiPkgVersions()}

	base := Options{
		ProjectDir: writeManifest(t, fakeMultiPkgManifest()),
		Source:     slow,
		NoNetwork:  false,
		// disable BOTH prefetches for the baseline run
		NoPrefetch:         true,
		NoMetadataPrefetch: true,
	}
	tSerial := timeInstall(t, base)

	base.NoMetadataPrefetch = false
	tParallel := timeInstall(t, base)

	if tParallel >= tSerial {
		t.Errorf("metadata prefetch did not speed up install: serial=%v parallel=%v", tSerial, tParallel)
	}
	if tParallel*10 >= tSerial*7 { // require > 30% speedup
		t.Logf("marginal speedup: serial=%v parallel=%v", tSerial, tParallel)
	}
}
```

The helpers `sleepySourceLookup`, `fakeMultiPkgVersions`, `fakeMultiPkgManifest`, `writeManifest`, and `timeInstall` are new. Add them alongside the test or move them to a small `_helpers_test.go` if the file is already crowded. If a similar wall-time comparison test already exists in the orchestrator package, mirror its shape exactly and reuse its helpers.

- [ ] **Step 3: Verify RED**

Run: `go test ./internal/orchestrator/ -run TestMetadataPrefetchReducesResolveWallTime -v`

Expected: FAIL — either compile errors (helper types missing) or the assertion fails because `maybeStartMetadataPrefetch` isn't wired.

- [ ] **Step 4: Wire the prefetch into `runFullPipeline`**

Insert right after the block that computes `ps.lockBytes` and just before `maybeStartPrefetch`:

```go
mprefetch := maybeStartMetadataPrefetch(ctx, ps, opts)
defer mprefetch.Wait()
```

`defer mprefetch.Wait()` ensures the prefetch is awaited even on early returns from `runFullPipeline`. The existing artifact `prefetch.Wait()` stays as-is; both call sites end up being idempotent noops on the noop instance.

- [ ] **Step 5: Add verbose timing line**

Find the verbose timing block in the same function (`if opts.Verbose { ... }`) that already prints `read manifest`, `resolve`, `fetch`, etc. Add a line for metadata prefetch:

```go
if opts.Verbose {
    if mp := mprefetch; mp != nil {
        mp.stats.mu.Lock()
        w := mp.stats.warmed
        d := mp.stats.duration
        mp.stats.mu.Unlock()
        if w > 0 {
            fmt.Fprintf(stderr, "  metadata-prefetch %6dms (%d warmed)\n", d.Milliseconds(), w)
        }
    }
}
```

If the existing block prints via a `Timings` accumulator instead of `fmt.Fprintf` directly, mirror that pattern instead of introducing raw `Fprintf` — the point is a single new line in the phase table.

- [ ] **Step 6: Add helpers to the test file if not already present**

Concretely:

```go
type sleepySourceLookup struct {
    delay    time.Duration
    versions map[string]*registry.PackageMetadata
}

func (s *sleepySourceLookup) Lookup(ctx context.Context, name string) (*registry.PackageMetadata, error) {
    select {
    case <-time.After(s.delay):
    case <-ctx.Done():
        return nil, ctx.Err()
    }
    if v, ok := s.versions[name]; ok {
        return v, nil
    }
    return nil, registry.ErrPackageNotFound
}

func fakeMultiPkgManifest() *manifest.Manifest {
    return &manifest.Manifest{
        Name:    "acme/app",
        Require: map[string]string{"a/a": "^1", "b/b": "^1", "c/c": "^1", "d/d": "^1", "e/e": "^1"},
    }
}

func fakeMultiPkgVersions() map[string]*registry.PackageMetadata {
    // For each of 5 names, expose a single 1.0.0 version with a fake dist URL.
    // Adapt to the resolver/testlookup pattern already used elsewhere in
    // the test package — that pattern is preferred if present.
    // ...
}

func writeManifest(t *testing.T, m *manifest.Manifest) string { ... }
func timeInstall(t *testing.T, opts Options) time.Duration    { ... }
```

If the orchestrator's existing test suite already ships `testlookup.New`-style helpers for building multi-package sources, use those instead. Look at `internal/orchestrator/pipeline_test.go` and `internal/orchestrator/orchestrator_test.go` first; only introduce new helpers if there's no existing analog.

- [ ] **Step 7: Verify GREEN**

Run: `go test ./internal/orchestrator/ -v -run 'TestMetadataPrefetch'`

Expected: PASS. If the wall-time assertion is flaky, tighten the delay (e.g. bump to 80ms) so the parallel/serial gap is unambiguous.

- [ ] **Step 8: Commit**

```bash
git add internal/orchestrator/pipeline.go internal/orchestrator/pipeline_test.go
git commit -m "feat(orchestrator): kick off metadata prefetch in runFullPipeline

Wires maybeStartMetadataPrefetch next to the existing artifact prefetch
in runFullPipeline. Deferred Wait() ensures the pool is drained even
on early returns. Verbose mode reports 'metadata-prefetch NNNms (X warmed)'
in the timing block."
```

---

## Task 4: CLI flag

**Files:**
- Modify: `internal/cli/root.go`
- Modify: `internal/cli/install.go` (or wherever `orchestrator.Options` is constructed for install)
- Modify: `internal/cli/update.go` (same wiring)
- Modify: `internal/cli/plugins_flag_test.go` (or a sibling test file)

**Interfaces:**
- Produces: `--no-metadata-prefetch` persistent flag reachable from `install` and `update`.

- [ ] **Step 1: Add the flag**

In `internal/cli/root.go`, near the existing flag declarations, add:

```go
var flagNoMetadataPrefetch bool
```

and inside `newRootCmd`:

```go
root.PersistentFlags().BoolVar(&flagNoMetadataPrefetch, "no-metadata-prefetch", false,
    "disable resolver-metadata prefetch (benchmarking hook)")
```

- [ ] **Step 2: Wire into Options**

In `internal/cli/install.go` (and mirror in `update.go`), where `orchestrator.Options{...}` is constructed, add:

```go
NoMetadataPrefetch: flagNoMetadataPrefetch,
```

- [ ] **Step 3: Write the flag-reach test**

If `internal/cli/plugins_flag_test.go` already has a similar test for `--no-prefetch`, model the new test after it. If not, create `internal/cli/no_metadata_prefetch_flag_test.go`:

```go
package cli

import (
	"context"
	"testing"

	"github.com/torstendittmann/gomposer/internal/orchestrator"
)

func TestNoMetadataPrefetchFlagReachesOptions(t *testing.T) {
	var got orchestrator.Options
	restore := swapInstallRunner(func(_ context.Context, opts orchestrator.Options) error {
		got = opts
		return nil
	})
	defer restore()

	root := newRootCmd("dev")
	root.SetArgs([]string{"install", "--no-metadata-prefetch"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !got.NoMetadataPrefetch {
		t.Errorf("Options.NoMetadataPrefetch = false, want true")
	}
}
```

If a helper like `swapInstallRunner` doesn't already exist in the CLI test suite, look for the closest equivalent (something that lets tests intercept `orchestrator.Install` without hitting the real network). If nothing exists, follow the pattern in the existing `TestNoPrefetchFlag`-style test (grep for `NoPrefetch` in `internal/cli/`).

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/cli/ -run TestNoMetadataPrefetch -v`

Expected: PASS.

- [ ] **Step 5: Full suite**

Run: `go test ./...`

Expected: PASS everywhere.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/root.go internal/cli/install.go internal/cli/update.go internal/cli/*_test.go
git commit -m "feat(cli): --no-metadata-prefetch flag

Persistent root flag reaches orchestrator.Options via install and
update. Off by default; used mainly for benchmarks and debugging."
```

---

## Task 5: E2E sanity + docs

**Files:** none created; light doc edits only.

- Modify: `README.md`

**Interfaces:** none.

- [ ] **Step 1: Manual e2e**

```sh
go build -o /tmp/gomposer-mprefetch ./cmd/gomposer
mkdir -p /tmp/mprefetch-check && cd /tmp/mprefetch-check
cat > composer.json <<'JSON'
{
  "name": "smoke/mprefetch",
  "require": {
    "monolog/monolog": "^3.0",
    "guzzlehttp/guzzle": "^7.0",
    "symfony/console": "^6.0",
    "symfony/dotenv": "^6.0"
  }
}
JSON
rm -rf ~/Library/Caches/gomposer/packagist ~/Library/Caches/gomposer/parsedcache
/tmp/gomposer-mprefetch install -v 2>&1 | tail -20
```

Expected: verbose timing block includes a `metadata-prefetch` line with `X warmed`; the resolve phase should feel noticeably faster than a wiped-cache install without the flag. Then compare against `--no-metadata-prefetch`:

```sh
rm -rf ~/Library/Caches/gomposer/packagist ~/Library/Caches/gomposer/parsedcache vendor gomposer.lock
/tmp/gomposer-mprefetch install -v --no-metadata-prefetch 2>&1 | tail -20
```

Expected: no `metadata-prefetch` line; resolve phase noticeably slower on cold cache. Record both timings for the commit description if desired.

- [ ] **Step 2: README flag table update**

In `README.md`, find the "Common flags" table and add:

```markdown
| `--no-metadata-prefetch` | Disable registry-metadata prefetch (benchmarking hook). |
```

Positioned alphabetically or near `--no-prefetch`.

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: mention --no-metadata-prefetch in README flag table"
```

---

## Metadata prefetch: acceptance check

After all tasks:

- `go test ./...` is green.
- `gomposer install -v` on a wiped Packagist cache prints a `metadata-prefetch NNNms (X warmed)` line in the timing block.
- Cold install wall-clock is noticeably faster than the same install with `--no-metadata-prefetch`.
- `gomposer install --no-metadata-prefetch` produces no metadata-prefetch line and behaves identically to the pre-branch binary.
- Warm-cache installs (parsedcache hits) show `metadata-prefetch X warmed in ~Yms` where Y is close to zero — cache hits skip the HTTP round-trip.

If any of these fails, fix forward before declaring the plan done.
