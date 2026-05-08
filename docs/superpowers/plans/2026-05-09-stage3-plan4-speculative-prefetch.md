# Stage 3 / Plan 4: Lock-Driven Speculative Prefetch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Cut wall-clock time on the lock-unchanged install path (the everyday case) by overlapping package downloads with the resolver. When `composer-go.lock` exists, kick off background downloads of every package listed in the lock as soon as we have parsed the lockfile bytes — in parallel with the resolver phase. The fetcher already deduplicates by sha256 store key, so when the resolver produces a result that agrees with the lock (the common case), every package is already on disk and `fetchAll` becomes a sequence of warm-store hits. When the resolver picks a different version, the speculative download is wasted bandwidth, but the artifact is still in the content-addressed store and benefits future runs.

This plan implements **optimistic op 1** from the design spec. The complement, optimistic op 2 (pipelined extract), already shipped with stage 1.

**Architecture:**

- A new `internal/orchestrator/prefetch.go` introduces a `Prefetcher` that, given a parsed `*lock.File` and the production `Fetcher`, spawns one goroutine per locked package bounded by `runtime.NumCPU()` via `errgroup.SetLimit`. Errors are intentionally swallowed — the resolver pass is the source of truth for what actually needs to be installed and will surface anything genuinely missing.
- The `Prefetcher` returns a `Wait()` handle so the orchestrator can join the speculative work at the latest opportunity (just before `fetchAll`). It also honors the parent context: if the user hits Ctrl-C during the resolver, the prefetch goroutines stop too.
- `runFullPipeline` is rewired so that, immediately after `newPipelineState` (which has loaded `lockBytes` from disk), if a lockfile parsed successfully and prefetch is enabled, we kick off the prefetcher goroutine with a fork of the run context. We then proceed to `resolveOrCache` exactly as before. After the resolver returns, the prefetcher's `Wait()` is called before `fetchAll` to ensure no double-flight on the same store key (and to surface any prefetch-context-cancellation that propagated from the parent).
- A new `Options.NoPrefetch bool` field (CLI flag `--no-prefetch`, default OFF, i.e. prefetch is **on**) lets benchmarks measure the isolated impact and gives users an escape hatch.
- Skip prefetch when:
  - `forceResolve` is true (the `update` path — we have no reason to trust the existing lock).
  - `opts.NoNetwork` is true (test-only flag; prefetch would violate the no-network contract).
  - `opts.NoPrefetch` is true (explicit opt-out).
  - The lockfile is absent (`len(ps.lockBytes) == 0`) or fails to parse (corrupt lock — fall back to resolve).
  - There is no production `Fetcher` available (defensive — if `defaultDeps` somehow wired a nil fetcher, prefetch is disabled rather than panic).

**Why this is safe.**

- The fetcher is content-addressed by sha256. A second concurrent `Fetch` call for the same package observes `store.Has(sha)` and returns immediately without touching the network, regardless of whether the prefetcher already downloaded it, is mid-download, or hasn't started yet (the store's tmp-rename is atomic, so `Has` is monotonic — it never goes from true to false).
- The fetcher's mid-download state is a temp file with a randomized suffix inside the store dir; two concurrent downloads of the same sha simply each write their own tmp file and the second `os.Rename` is the one that wins (or the first wins and the second sees `os.ErrExist` and returns success — both branches already handled in `fetcher.Fetch`).
- The prefetcher never advertises progress to the user nor mutates the lockfile. The resolver remains authoritative.

**Tech Stack:** stdlib + `golang.org/x/sync/errgroup` (already in `go.mod` from Plan 1/4). No new dependencies.

**Depends on:**

- Stage 1 / Plan 4 — `internal/fetcher.Fetcher.Fetch` is the workhorse; the content-addressed `internal/store.Store` is what makes double-fetch safe.
- Stage 1 / Plan 6 — `runFullPipeline` and `pipelineState` are the wiring points.
- Stage 1 / Plan 1 — `lock.Decode` is what surfaces a parsed lockfile from `ps.lockBytes`.

**Acceptance target.** On the lock-unchanged install path against the design spec's benchmark corpus (Laravel skeleton, Symfony skeleton, Drupal install, one larger real project), warm-cache **and** lock-unchanged installs reach ≥5x Composer 2 wall-clock — the design spec's stage 3 target. The point of this plan is not to hit that target single-handedly (concurrency tuning in plan 5 contributes too), but to provide the largest single contributor: making download time vanish into the resolver's critical path.

---

## File structure

| Path | Responsibility |
|------|---------------|
| `internal/orchestrator/prefetch.go` | `Prefetcher` type, `startPrefetch` constructor, `Wait` join. |
| `internal/orchestrator/prefetch_test.go` | Unit tests with fake fetcher / fake source asserting Fetch fires before Solve. |
| `internal/orchestrator/orchestrator.go` | New `Options.NoPrefetch` field. |
| `internal/orchestrator/pipeline.go` | Wire prefetcher into `runFullPipeline`. |
| `cmd/composer-go/install.go` | Surface `--no-prefetch` flag. |
| `cmd/composer-go/update.go` | Surface `--no-prefetch` flag (no-op there since we always skip prefetch on update, but the flag exists for symmetry). |
| `internal/orchestrator/bench_prefetch_test.go` | Benchmark / acceptance harness with a slow `httptest.Server` and a synthetic 30-package lockfile. |

---

## Task 1: Add `Options.NoPrefetch` and CLI flag

**Files:**
- Modify: `/Users/torstendittmann/Documents/skunk/composer-go/internal/orchestrator/orchestrator.go`
- Modify: `/Users/torstendittmann/Documents/skunk/composer-go/cmd/composer-go/install.go`
- Modify: `/Users/torstendittmann/Documents/skunk/composer-go/cmd/composer-go/update.go`

The flag has to land first because every later test uses it (we want isolation: most tests assert "prefetch fires"; a few assert "with `NoPrefetch` it does not"). Default is `false`, i.e. prefetch is on by default.

- [ ] **Step 1: Add the field to `Options`**

In `internal/orchestrator/orchestrator.go`, add to the `Options` struct, immediately after `NoNetwork`:

```go
// NoPrefetch disables stage-3 lock-driven speculative prefetch. Default
// (false) means prefetch is on. Mostly useful for benchmarks that want
// to measure the isolated wall-clock contribution of optimistic op 1.
//
// Prefetch is also implicitly disabled when:
//   - forceResolve is true (the update path),
//   - NoNetwork is true,
//   - the lockfile is absent or fails to parse.
NoPrefetch bool
```

- [ ] **Step 2: Surface the CLI flag**

In `cmd/composer-go/install.go`, add a flag binding next to the existing `--no-dev`, `--no-scripts`, etc. The cobra wiring follows the existing pattern verbatim:

```go
cmd.Flags().BoolVar(&opts.NoPrefetch, "no-prefetch", false, "disable lock-driven speculative prefetch (benchmark hook)")
```

Repeat in `cmd/composer-go/update.go`. The flag is technically a no-op for `update` (we already skip prefetch on `forceResolve=true`), but parity matters — a user who scripts `composer-go install --no-prefetch` would be confused if `update` rejected it.

- [ ] **Step 3: Verify build**

Run:

```bash
cd /Users/torstendittmann/Documents/skunk/composer-go
go build ./...
```

Expected: clean build. No tests fail because nothing references `NoPrefetch` yet.

- [ ] **Step 4: Commit**

```bash
git add internal/orchestrator/orchestrator.go cmd/composer-go/install.go cmd/composer-go/update.go
git commit -m "feat(orchestrator): add NoPrefetch option and --no-prefetch CLI flag"
```

---

## Task 2: `Prefetcher` skeleton — type, constructor, Wait

**Files:**
- Create: `/Users/torstendittmann/Documents/skunk/composer-go/internal/orchestrator/prefetch.go`
- Create: `/Users/torstendittmann/Documents/skunk/composer-go/internal/orchestrator/prefetch_test.go`

We start with a unit-testable shell: given a `*lock.File` and a `Fetcher`, fire one `Fetch` call per package in a bounded errgroup, swallow errors, and expose a `Wait()` that callers use to join.

- [ ] **Step 1: Write the failing test**

Create `/Users/torstendittmann/Documents/skunk/composer-go/internal/orchestrator/prefetch_test.go`:

```go
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
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/orchestrator/... -run TestPrefetcher`

Expected: build error (`undefined: startPrefetch`).

- [ ] **Step 3: Implement the Prefetcher**

Create `/Users/torstendittmann/Documents/skunk/composer-go/internal/orchestrator/prefetch.go`:

```go
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
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/orchestrator/... -run TestPrefetcher`

Expected: PASS for all five `TestPrefetcher*` cases.

- [ ] **Step 5: Commit**

```bash
git add internal/orchestrator/prefetch.go internal/orchestrator/prefetch_test.go
git commit -m "feat(orchestrator): Prefetcher type with bounded concurrent best-effort downloads"
```

---

## Task 3: Resolver-vs-prefetch race assertion

**Files:**
- Modify: `/Users/torstendittmann/Documents/skunk/composer-go/internal/orchestrator/prefetch_test.go`

The load-bearing claim of optimistic op 1: with a slow source, every locked package sees a `Fetch` call **before** the resolver finishes. We simulate the slow resolver with `time.Sleep` and a watcher goroutine that polls `Calls()`.

- [ ] **Step 1: Append the race test**

```go
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
```

- [ ] **Step 2: Run tests**

Run: `go test ./internal/orchestrator/... -run TestPrefetchRacesResolver`

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/orchestrator/prefetch_test.go
git commit -m "test(orchestrator): assert prefetch fires every Fetch before resolver returns"
```

---

## Task 4: Wire prefetch into `runFullPipeline`

**Files:**
- Modify: `/Users/torstendittmann/Documents/skunk/composer-go/internal/orchestrator/pipeline.go`

The integration point is right after `newPipelineState`: we have `ps.lockBytes` and the production `opts.Fetcher`, and the resolver has not yet run. We parse the lockfile (best-effort — corrupt locks fall through), kick off the prefetcher, then call `resolveOrCache` exactly as before. Just before `fetchAll`, we `Wait()` on the prefetcher so any in-flight downloads are guaranteed to be present (or cancelled) before we double-dispatch the same packages.

- [ ] **Step 1: Update `runFullPipeline`**

In `internal/orchestrator/pipeline.go`, make three small edits to the existing function:

1. After `ps, err := newPipelineState(opts, m)` returns successfully, kick off the prefetcher:

   ```go
   prefetch := maybeStartPrefetch(ctx, ps, opts, forceResolve)
   ```

2. On every error return path between `maybeStartPrefetch` and `fetchAll` (the existing returns from `resolveOrCache`, `evaluatePlatformWarnings`), add `prefetch.Wait()` before the `return err`. This drains in-flight goroutines — they observe the cancelled context and exit promptly.

3. Immediately before the existing `fetchAll(ctx, all, opts.Fetcher, ...)` call, add:

   ```go
   // Join the prefetcher: every speculative download has either completed
   // (warm-store hit for fetchAll) or been cancelled. Either way fetchAll
   // is authoritative.
   prefetch.Wait()
   ```

The rest of the function (backfillSha, materializeAll, autoload, writeLock, fireEvent post hooks) is byte-identical to the existing implementation.

- [ ] **Step 2: Add the `maybeStartPrefetch` helper**

Append to `/Users/torstendittmann/Documents/skunk/composer-go/internal/orchestrator/prefetch.go`:

```go
// maybeStartPrefetch decides whether to kick off the speculative prefetch
// based on opts and the parsed pipeline state. It returns a non-nil
// *Prefetcher in every branch — callers can Wait() unconditionally without
// nil-checking. When prefetch is skipped, the returned Prefetcher's Wait
// is a no-op.
//
// Skip conditions:
//   - forceResolve (update path): we have no reason to trust the lock.
//   - opts.NoNetwork: test-only flag; honour the no-network contract.
//   - opts.NoPrefetch: explicit user opt-out.
//   - len(ps.lockBytes) == 0: no lockfile to be confident in.
//   - lock.Decode fails: corrupt lock; fall back to resolver.
//   - opts.Fetcher == nil: defensive (defaultDeps wiring failure).
func maybeStartPrefetch(ctx context.Context, ps *pipelineState, opts Options, forceResolve bool) *Prefetcher {
	if forceResolve || opts.NoNetwork || opts.NoPrefetch {
		return &Prefetcher{}
	}
	if len(ps.lockBytes) == 0 || opts.Fetcher == nil {
		return &Prefetcher{}
	}
	lf, err := lock.Decode(ps.lockBytes)
	if err != nil {
		return &Prefetcher{}
	}
	return startPrefetch(ctx, lf, opts.Fetcher, !opts.NoDev, workerCount(opts.Workers))
}
```

You will need to import `"github.com/torstendittmann/composer-go/internal/lock"` in `prefetch.go` if not already present (the file from Task 2 imports it for the test fixtures, but the production file did not).

- [ ] **Step 3: Run the existing pipeline tests**

Run:

```bash
go test ./internal/orchestrator/...
```

Expected: PASS. The existing pipeline tests use injected fakes that set `opts.NoNetwork` or run via `update` (forceResolve=true), so they hit a skip branch and behave identically. Any test that constructs a `pipelineState` with a real lockfile + fake fetcher will, however, now see prefetch calls — that's the integration test added in Task 5.

- [ ] **Step 4: Commit**

```bash
git add internal/orchestrator/pipeline.go internal/orchestrator/prefetch.go
git commit -m "feat(orchestrator): wire lock-driven prefetch into runFullPipeline"
```

---

## Task 5: Integration test — prefetch + pipeline + warm fetchAll

**Files:**
- Modify: `/Users/torstendittmann/Documents/skunk/composer-go/internal/orchestrator/prefetch_test.go`

Now we test the actual integration: `runFullPipeline` with a real-shaped `lock.File` on disk, a fake fetcher whose calls we count, and a fake source whose `Solve` we observe. The assertion: the fake fetcher sees every locked package fetched at most once across the prefetcher + fetchAll combined (i.e. fetchAll observes warm-store hits via the fake's call count).

The fake fetcher in this test is "warm-aware": after the first call for a given name, subsequent calls increment a separate `warmHits` counter rather than `calls`. This mirrors the real Fetcher's behaviour against a content-addressed store: cold call does work, warm call is free.

- [ ] **Step 1: Write the integration test**

Append to `/Users/torstendittmann/Documents/skunk/composer-go/internal/orchestrator/prefetch_test.go`. The fixture helpers (`writeFixtureManifest`, `writeFixtureLock`, `recordingMaterializer`, `recordingAutoloader`) already exist in `pipeline_test.go` from stage 1 / plan 6 — reuse them; adapt signatures if minor.

```go
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
```

- [ ] **Step 2: Run tests**

Run: `go test ./internal/orchestrator/... -run TestPipelinePrefetch`

Expected: PASS for all three integration tests.

- [ ] **Step 3: Commit**

```bash
git add internal/orchestrator/prefetch_test.go
git commit -m "test(orchestrator): integration tests for prefetch warm-fetchAll handoff"
```

---

## Task 6: Acceptance benchmark — lock-unchanged install vs Composer

**Files:**
- Create: `/Users/torstendittmann/Documents/skunk/composer-go/internal/orchestrator/bench_prefetch_test.go`

A `Benchmark*` function (not a `Test*`) that synthesizes a 30-package lockfile and serves it from a slow `httptest.Server` (configurable per-package latency). The harness reports wall-clock for two runs:

1. With prefetch on (default).
2. With prefetch off (`opts.NoPrefetch = true`).

The ratio is the per-run wall-clock contribution of optimistic op 1 in isolation. It is not directly comparable to Composer because the `httptest.Server` is local and lossless — the ≥5x figure in the design spec is end-to-end against real Packagist, with real network RTTs.

We surface a `make bench-prefetch` target (in a follow-up Makefile change, not part of this plan) that runs this benchmark with `-benchtime=10x` for stable numbers. The benchmark belongs in this plan because (a) it's the easiest acceptance signal a reviewer can run locally, and (b) it pins the regression: if a future refactor breaks prefetch silently, the prefetch-on/prefetch-off ratio collapses to ~1.0 and the benchmark obviously regresses.

- [ ] **Step 1: Write the benchmark**

Create `/Users/torstendittmann/Documents/skunk/composer-go/internal/orchestrator/bench_prefetch_test.go`. Sketch:

```go
package orchestrator

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// BenchmarkPrefetchVsNoPrefetch reports the wall-clock contribution of
// optimistic op 1 in isolation. Run with:
//   go test -bench=. -benchtime=10x ./internal/orchestrator/...
//
// 30-package lockfile, served from a local httptest.Server with per-request
// latency. With prefetch on, downloads overlap the resolver pass (a stub
// source here, so essentially free). With prefetch off, they serialize.
// The reported `speedup` metric is hand-readable, not a CI gate.
func BenchmarkPrefetchVsNoPrefetch(b *testing.B) {
	const numPackages = 30
	const perRequestDelay = 20 * time.Millisecond

	zipBytes := makeBenchZip(b)
	wantSha := sha256OfBench(zipBytes)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(perRequestDelay)
		_, _ = w.Write(zipBytes)
	}))
	defer srv.Close()

	dir := b.TempDir()
	writeFixtureManifest(b, dir)
	names := make([]string, numPackages)
	for i := range names {
		names[i] = fmt.Sprintf("vendor/p%02d", i)
	}
	// writeFixtureLockWithDist: a benchmark-only sibling of writeFixtureLock
	// that stamps real Dist.URL + Dist.Sha256 so the production fetcher
	// actually attempts the download. Add it next to the existing fixtures
	// in pipeline_test.go.
	writeFixtureLockWithDist(b, dir, names, srv.URL+"/p.zip", wantSha)

	run := func(noPrefetch bool) time.Duration {
		_ = os.RemoveAll(filepath.Join(dir, ".composer-go")) // cold store each run
		opts := Options{ProjectDir: dir, Workers: 8, NoPrefetch: noPrefetch}
		t0 := time.Now()
		if err := Install(context.Background(), opts); err != nil {
			b.Fatalf("Install (noPrefetch=%v): %v", noPrefetch, err)
		}
		return time.Since(t0)
	}

	var withPrefetch, withoutPrefetch time.Duration
	for i := 0; i < b.N; i++ {
		withPrefetch += run(false)
		withoutPrefetch += run(true)
	}
	b.ReportMetric(float64(withPrefetch.Milliseconds())/float64(b.N), "ms/op-with-prefetch")
	b.ReportMetric(float64(withoutPrefetch.Milliseconds())/float64(b.N), "ms/op-without-prefetch")
	if withPrefetch > 0 {
		b.ReportMetric(float64(withoutPrefetch)/float64(withPrefetch), "speedup")
	}
}

func makeBenchZip(tb testing.TB) []byte {
	tb.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("p/composer.json")
	_, _ = w.Write([]byte(`{"name":"vendor/p"}`))
	_ = zw.Close()
	return buf.Bytes()
}

func sha256OfBench(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
```

- [ ] **Step 2: Smoke-run the benchmark**

Run:

```bash
go test -bench=BenchmarkPrefetchVsNoPrefetch -benchtime=3x -run=^$ ./internal/orchestrator/...
```

Expected: the benchmark runs to completion. The `speedup` metric should be `>= 1.5` on a workstation. We deliberately avoid asserting a hard threshold inside the benchmark — CI noise floors and box variability swamp the signal at small `-benchtime`. Treat the metric as a hand-readable regression indicator.

- [ ] **Step 3: Commit**

```bash
git add internal/orchestrator/bench_prefetch_test.go
git commit -m "bench(orchestrator): wall-clock contribution of lock-driven prefetch"
```

---

## Task 7: Documentation — module-level comment + spec backreference

**Files:**
- Modify: `/Users/torstendittmann/Documents/skunk/composer-go/internal/orchestrator/prefetch.go`
- Modify: `/Users/torstendittmann/Documents/skunk/composer-go/internal/orchestrator/orchestrator.go`

A future maintainer reading `prefetch.go` should be able to reconstruct, in 30 seconds, **why** prefetch is best-effort (errors swallowed), **why** Wait is unconditional (no nil-check at the call site), and **why** content-addressing makes double-fetch safe. Same for the package-level doc comment on `orchestrator.go`, which currently lists install-pipeline phases without mentioning the parallel speculative download.

- [ ] **Step 1: Expand the package doc**

Edit the leading comment block on `orchestrator.go` (above `package orchestrator`) to mention the prefetch:

Add at the end of the existing block, before the `package` line:

```go
// On the install path (forceResolve=false), if a lockfile is present, the
// orchestrator ALSO kicks off a speculative prefetch that downloads every
// locked package in parallel with the resolver. This is "optimistic op 1"
// from the design spec: the fetcher is content-addressed by sha256, so
// double-fetching is cheap, and on the common case (lock matches resolver)
// fetchAll observes a warm store and the network IO disappears into the
// resolver's critical path. See internal/orchestrator/prefetch.go.
```

- [ ] **Step 2: Expand the prefetch.go header**

Above `package orchestrator` in `prefetch.go`, add:

```go
// Lock-driven speculative prefetch — "optimistic op 1" from the design
// spec. When a lockfile exists and we're on the install (not update) path,
// every package in the lock is dispatched to the production Fetcher in
// parallel with the resolver pass. On the common case the resolver agrees
// with the lock and fetchAll observes a fully warm store. On the rare case
// the resolver picks different versions, the speculative downloads are
// wasted bandwidth but their bytes still seed the content-addressed store
// for future runs.
//
// Errors from prefetch are intentionally swallowed: the resolver pass plus
// the authoritative fetchAll is what surfaces real failures with the right
// error message and the right stack frame. A failed prefetch never
// propagates to the user.
//
// Safety: the Fetcher is content-addressed by sha256, the on-disk store
// uses tmp-then-rename for atomicity, and concurrent Fetch calls for the
// same sha race only over the final os.Rename — both branches resolve to
// the same byte-for-byte file. See internal/store/store.go and
// internal/fetcher/fetcher.go for the underlying invariants.
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/orchestrator/...`

Expected: PASS. Documentation-only change.

- [ ] **Step 4: Commit**

```bash
git add internal/orchestrator/prefetch.go internal/orchestrator/orchestrator.go
git commit -m "docs(orchestrator): describe lock-driven prefetch in package + file headers"
```

---

## Plan 4 acceptance check

After all tasks:

- `go test ./...` is green on darwin and linux.
- `go test -bench=BenchmarkPrefetchVsNoPrefetch -benchtime=3x -run=^$ ./internal/orchestrator/...` runs to completion and reports a `speedup` metric ≥1.5 on a developer workstation. The benchmark is not a CI gate — it's a regression-spotter for humans.
- `composer-go install --no-prefetch` works (flag accepted, prefetch suppressed). `composer-go update --no-prefetch` works (flag accepted, no-op).
- The unit tests assert the load-bearing claim of optimistic op 1 directly: with a slow source, every locked package's `Fetch` is invoked before the resolver returns.
- The integration tests assert the warm-fetchAll handoff: after `Install`, every package was fetched exactly once cold, with at least one warm hit observed in the authoritative `fetchAll` pass.
- The skip matrix is exercised: `forceResolve=true` (Update), `NoPrefetch=true`, and an absent lockfile each produce zero warm hits.
- Public surface added:
  - `Options.NoPrefetch bool` (CLI: `--no-prefetch`).
  - Internal: `*Prefetcher`, `(*Prefetcher).Wait`, `startPrefetch`, `maybeStartPrefetch`. None of these are exported beyond the orchestrator package — prefetch is an internal pipeline concern.
- Spec alignment: this plan implements optimistic op 1 from `docs/superpowers/specs/2026-05-07-composer-go-design.md` ("When a lockfile exists, start downloading the top-N packages by size in parallel with the resolver"). We download **every** package, not the top-N — at the bandwidth profile of a typical Composer install, the gain from "all packages" over "top-N" is larger than the cost of a few wasted downloads on resolver disagreement, and the simpler policy is much easier to reason about.

If any of these fails, fix forward in a follow-up commit before declaring Plan 4 done.
