# Prefetch Download Progress Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the lock-driven speculative prefetcher's downloads feed the existing `fetching` progress phase, so one honest `gomposer: fetching N/M` line spans the entire download window instead of the current silent prefetch + one-frame flash.

**Architecture:** A shared `fetchTicker` (dedup-by-name `IncFetch` fan-in) is created by `maybeStartPrefetch`, which fires `BeginFetch(N)` *before* dispatching speculative downloads and hands the ticker to `fetchAll`, which skips its own `BeginFetch` and routes increments through the ticker. On non-prefetch paths `fetchAll` behaves exactly as today. No renderer, interface, or fetcher-layer changes.

**Tech Stack:** Go 1.25, stdlib + `golang.org/x/sync` (errgroup). Tests with `go test -race`.

**Spec:** `docs/superpowers/specs/2026-07-12-prefetch-progress-design.md` — read it before starting.

## Global Constraints

- Go 1.25; dependencies limited to stdlib + `golang.org/x/sync`.
- The `Progress` interface (canonical `internal/cli/progress.go`, mirror `internal/orchestrator/orchestrator.go`) is NOT modified — this feature only changes *when and from where* existing methods are called.
- Phase label stays `fetching` / `fetched`. No changes to `internal/cli/progress.go`.
- `IncFetch` label format is exactly `name + " " + version` (matches current `fetchAll`).
- Dedup key is the package **name** only (a lock cannot contain two versions of one package).
- Prefetch failures remain swallowed; a failed speculative fetch must NOT tick — `fetchAll` retries it authoritatively and ticks it then.
- `BeginFetch(N)` must fire BEFORE `startPrefetch` dispatches workers (a tick landing before `BeginFetch` would be zeroed by `beginPhase`).
- N (the announced total) = the lock's packages + dev packages when `!NoDev`, filtered through `nonWorkspacePackages` — exactly the set `fetchAll` later receives on the trusted-lockfile path.
- Exactly one `BeginFetch` and one `EndFetch` per install, on every path.
- All new/changed concurrency-sensitive tests run under `-race -count=3`.
- Commit messages: conventional-commit style, no `Co-Authored-By` trailer.

---

### Task 1: `fetchTicker` — dedup fan-in for IncFetch

**Files:**
- Create: `internal/orchestrator/fetchticker.go`
- Test: `internal/orchestrator/fetchticker_test.go`

**Interfaces:**
- Consumes: `Progress` interface and `progressOrNoop` helper, both already in `internal/orchestrator` (`pipeline.go:37-42`); `noopProgress` struct (`pipeline.go:44`).
- Produces: `type fetchTicker struct{...}` with `newFetchTicker(prog Progress) *fetchTicker` and method `tick(name, version string)`. Tasks 2 and 3 rely on these exact names. The `prog` field is exported-within-package (lowercase, but accessed by Task 3 as `ticker.prog`).

- [ ] **Step 1: Write the failing tests**

Create `internal/orchestrator/fetchticker_test.go`:

```go
package orchestrator

import (
	"sync"
	"testing"
)

// countingFetchProgress records IncFetch labels; every other Progress
// method is an inherited noop.
type countingFetchProgress struct {
	noopProgress
	mu     sync.Mutex
	labels []string
}

func (c *countingFetchProgress) IncFetch(label string) {
	c.mu.Lock()
	c.labels = append(c.labels, label)
	c.mu.Unlock()
}

func TestFetchTickerDedupsByName(t *testing.T) {
	prog := &countingFetchProgress{}
	ft := newFetchTicker(prog)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ft.tick("vendor/a", "1.0.0")
			ft.tick("vendor/b", "2.0.0")
		}()
	}
	wg.Wait()

	prog.mu.Lock()
	defer prog.mu.Unlock()
	if len(prog.labels) != 2 {
		t.Fatalf("IncFetch fired %d times, want 2 (deduped); labels=%v", len(prog.labels), prog.labels)
	}
	want := map[string]bool{"vendor/a 1.0.0": true, "vendor/b 2.0.0": true}
	for _, l := range prog.labels {
		if !want[l] {
			t.Errorf("unexpected label %q", l)
		}
	}
}

func TestFetchTickerNilProgressIsSafe(t *testing.T) {
	ft := newFetchTicker(nil)
	ft.tick("vendor/a", "1.0.0") // must not panic
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/orchestrator -run TestFetchTicker -v`
Expected: FAIL to compile with `undefined: newFetchTicker`

- [ ] **Step 3: Write the implementation**

Create `internal/orchestrator/fetchticker.go`:

```go
package orchestrator

import "sync"

// fetchTicker dedups IncFetch by package name so a package fetched
// speculatively by prefetch and re-verified warm by fetchAll ticks
// exactly once. Safe for concurrent use from prefetch worker
// goroutines and fetchAll worker goroutines simultaneously.
type fetchTicker struct {
	mu   sync.Mutex
	seen map[string]struct{}
	prog Progress
}

// newFetchTicker wraps prog (nil-safe via progressOrNoop) in a ticker.
// The caller that opens the fetching phase should do so via
// ticker.prog so Begin/Inc/End all route through the same instance.
func newFetchTicker(prog Progress) *fetchTicker {
	return &fetchTicker{seen: make(map[string]struct{}), prog: progressOrNoop(prog)}
}

// tick reports name as fetched exactly once; repeat calls for the same
// name are ignored. The rendered label matches fetchAll's existing
// format: "name version".
func (ft *fetchTicker) tick(name, version string) {
	ft.mu.Lock()
	if _, dup := ft.seen[name]; dup {
		ft.mu.Unlock()
		return
	}
	ft.seen[name] = struct{}{}
	ft.mu.Unlock()
	ft.prog.IncFetch(name + " " + version)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/orchestrator -run TestFetchTicker -race -count=3 -v`
Expected: PASS (both tests, 3 runs, no race)

- [ ] **Step 5: Run the package suite, then commit**

Run: `go test ./internal/orchestrator -race -count=1`
Expected: ok (no regressions)

```bash
git add internal/orchestrator/fetchticker.go internal/orchestrator/fetchticker_test.go
git commit -m "feat(orchestrator): fetchTicker, dedup fan-in for unified fetch progress"
```

---

### Task 2: `startPrefetch` callback + `prefetchPackages` helper

**Files:**
- Modify: `internal/orchestrator/prefetch.go` (`startPrefetch` at lines 85-129, `maybeStartPrefetch` at lines 62-74)
- Test: `internal/orchestrator/prefetch_test.go` (6 existing `startPrefetch` call sites at lines 77, 98, 118, 132, 147, 354 + two new tests)

**Interfaces:**
- Consumes: `nonWorkspacePackages(pkgs []lock.Package) []lock.Package` (`pipeline.go:358`), `workerCount` (existing), `lock.File` / `lock.Package`.
- Produces: `startPrefetch(ctx context.Context, pkgs []lock.Package, f Fetcher, limit int, onFetched func(name, version string)) *Prefetcher` and `prefetchPackages(lf *lock.File, includeDev bool) []lock.Package`. Task 3 relies on both exact signatures. `maybeStartPrefetch`'s public shape is UNCHANGED in this task (still returns `*Prefetcher` only, passes `nil` callback) — Task 3 reshapes it.

- [ ] **Step 1: Write the failing tests**

Append to `internal/orchestrator/prefetch_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/orchestrator -run 'TestPrefetcherFiresOnFetched|TestPrefetchPackages' -v`
Expected: FAIL to compile with `undefined: prefetchPackages` (and a signature mismatch on `startPrefetch`)

- [ ] **Step 3: Reshape `startPrefetch` and add `prefetchPackages`**

In `internal/orchestrator/prefetch.go`, replace the whole `startPrefetch` function (lines 76-129) with:

```go
// prefetchPackages builds the speculative download list from a decoded
// lock: production packages plus (optionally) dev packages, minus
// synthetic workspace entries, which have no dist to download. On the
// trusted-lockfile path this is exactly the set fetchAll later
// receives, so a progress total announced from this list is never
// revised.
func prefetchPackages(lf *lock.File, includeDev bool) []lock.Package {
	pkgs := make([]lock.Package, 0, len(lf.Packages)+len(lf.PackagesDev))
	pkgs = append(pkgs, lf.Packages...)
	if includeDev {
		pkgs = append(pkgs, lf.PackagesDev...)
	}
	return nonWorkspacePackages(pkgs)
}

// startPrefetch begins downloading every package in pkgs using f.
// Errors are intentionally swallowed: the resolver pass is the
// authoritative gate, and any genuine missing-package or network
// failure will surface there with the right error message.
//
// onFetched, when non-nil, fires after each successful speculative
// download (or warm-store hit) with the package's name and version.
// Failed fetches do not fire it — fetchAll retries those
// authoritatively and reports them through the same shared ticker.
// Called from prefetch worker goroutines; implementations must be
// concurrency-safe and cheap.
//
// limit caps in-flight downloads. Pass <=0 for runtime.NumCPU().
func startPrefetch(ctx context.Context, pkgs []lock.Package, f Fetcher, limit int, onFetched func(name, version string)) *Prefetcher {
	if len(pkgs) == 0 || f == nil {
		return &Prefetcher{}
	}
	if limit <= 0 {
		limit = runtime.NumCPU()
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(limit)

	// Dispatch goroutines from a background goroutine so startPrefetch returns
	// immediately. Without this, errgroup.SetLimit causes g.Go to block the
	// caller when the concurrency cap is reached — defeating the purpose of
	// starting downloads "in the background while the resolver runs".
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range pkgs {
			p := pkgs[i] // capture
			g.Go(func() error {
				// Swallow errors: prefetch is opportunistic. The resolver +
				// fetchAll is what surfaces problems. Returning an error here
				// would cancel the errgroup and abort sibling downloads we'd
				// happily have completed.
				_, err := f.Fetch(gctx, p)
				if err == nil && onFetched != nil {
					onFetched(p.Name, p.Version)
				}
				return nil
			})
		}
		_ = g.Wait()
	}()

	return &Prefetcher{wait: func() {
		<-done // wait for all g.Go calls + all goroutines to finish
	}}
}
```

In the same file, update the tail of `maybeStartPrefetch` (its signature and skip conditions stay untouched in this task) — replace:

```go
	lf, err := lock.Decode(ps.lockBytes)
	if err != nil {
		return &Prefetcher{}
	}
	return startPrefetch(ctx, lf, opts.Fetcher, !opts.NoDev, workerCount(opts.Workers))
```

with:

```go
	lf, err := lock.Decode(ps.lockBytes)
	if err != nil {
		return &Prefetcher{}
	}
	return startPrefetch(ctx, prefetchPackages(lf, !opts.NoDev), opts.Fetcher, workerCount(opts.Workers), nil)
```

- [ ] **Step 4: Update the 6 existing test call sites**

In `internal/orchestrator/prefetch_test.go`, apply the mechanical transform `startPrefetch(ctx, lf, f, includeDev, limit)` → `startPrefetch(ctx, prefetchPackages(lf, includeDev), f, limit, nil)`:

- Line 77: `pf := startPrefetch(context.Background(), prefetchPackages(lf, true), f, 4, nil)`
- Line 98: `pf := startPrefetch(context.Background(), prefetchPackages(lf, false), f, 4, nil)`
- Line 118: `pf := startPrefetch(context.Background(), prefetchPackages(lf, false), f, 4, nil)`
- Line 132: `pf := startPrefetch(context.Background(), prefetchPackages(lf, false), f, 3, nil)`
- Line 147: `pf := startPrefetch(ctx, prefetchPackages(lf, false), f, 4, nil)`
- Line 354: `pf := startPrefetch(context.Background(), prefetchPackages(lf, false), f, 4, nil)`

(Line numbers are pre-edit; adjust as the file shifts. The inline `/* includeDev */` comments on lines 77 and 98 move into the `prefetchPackages` argument.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/orchestrator -run 'TestPrefetcher|TestPrefetchPackages|TestPrefetchRacesResolver' -race -count=3 -v`
Expected: PASS — all pre-existing prefetch tests (semantics unchanged, nil callback) plus the two new ones

- [ ] **Step 6: Run the full package suite, then commit**

Run: `go build ./... && go test ./internal/orchestrator -race -count=1`
Expected: build ok, tests ok

```bash
git add internal/orchestrator/prefetch.go internal/orchestrator/prefetch_test.go
git commit -m "feat(orchestrator): startPrefetch onFetched callback + prefetchPackages helper"
```

---

### Task 3: Pipeline wire-in — phase opens at prefetch start, ticker shared with fetchAll

**Files:**
- Modify: `internal/orchestrator/prefetch.go` (`maybeStartPrefetch`, lines 49-74)
- Modify: `internal/orchestrator/pipeline.go` (`fetchAll` at lines 301-335, call sites at lines 499 and 581)
- Modify: `internal/orchestrator/orchestrator_test.go` (two `fetchAll` call sites at lines 141 and 158)
- Test: `internal/orchestrator/pipeline_test.go` (extend `recordingProgress` at lines 821-863; add two integration tests)

**Interfaces:**
- Consumes: `newFetchTicker(prog Progress) *fetchTicker`, `ticker.tick(name, version string)`, `ticker.prog` (Task 1); `prefetchPackages(lf, includeDev)` and 5-arg `startPrefetch` (Task 2); existing test helpers `writeManifestObj` (`pipeline_test.go:473`), `fakeFetcher`/`fakeMaterializer`/`fakeAutoloader` (`orchestrator_test.go:118/165/205`), `testlookup.New`/`testlookup.Pkg`.
- Produces: `maybeStartPrefetch(ctx, ps, opts, forceResolve) (*Prefetcher, *fetchTicker)` (second return nil when prefetch skipped); `fetchAll(ctx, pkgs, f, workers, prog Progress, ticker *fetchTicker)`. These are final — no later task.

- [ ] **Step 1: Extend the `recordingProgress` fake**

In `internal/orchestrator/pipeline_test.go`, replace the struct definition and the `BeginFetch`/`IncFetch` methods (lines 821-834):

```go
type recordingProgress struct {
	mu          sync.Mutex
	events      []string
	resolves    []string // names passed to IncResolve
	fetches     []string // labels passed to IncFetch
	fetchTotals []int    // totals passed to BeginFetch
}

func (r *recordingProgress) record(evt string) {
	r.mu.Lock()
	r.events = append(r.events, evt)
	r.mu.Unlock()
}

func (r *recordingProgress) BeginFetch(n int) {
	r.mu.Lock()
	r.fetchTotals = append(r.fetchTotals, n)
	r.mu.Unlock()
	r.record("BeginFetch")
}

func (r *recordingProgress) IncFetch(label string) {
	r.mu.Lock()
	r.fetches = append(r.fetches, label)
	r.mu.Unlock()
}
```

(All other methods stay as they are.) Add one helper next to `sawEvent`:

```go
func (r *recordingProgress) countEvent(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e == name {
			n++
		}
	}
	return n
}
```

- [ ] **Step 2: Write the failing integration tests**

The load-bearing assertion is **ordering**: with today's code `BeginFetch` fires from `fetchAll`, *after* `prefetch.Wait()` — i.e. after every speculative download already happened silently. The new contract is `BeginFetch` before the *first* fetch of the run. To observe ordering across the fetcher and the progress fake, the test routes both through one event log: a tiny `orderedFetcher` records `"Fetch <name>"` into the same `recordingProgress` the pipeline gets.

Append to `internal/orchestrator/pipeline_test.go`:

```go
// orderedFetcher records each Fetch into the shared recordingProgress
// event log so tests can assert ordering between fetcher activity and
// progress events. Same-package sibling of fakeFetcher.
type orderedFetcher struct {
	rp *recordingProgress
}

func (o *orderedFetcher) Fetch(_ context.Context, pkg lock.Package) (string, error) {
	o.rp.record("Fetch " + pkg.Name)
	return "store-key-" + pkg.Name, nil
}

// TestPipelinePrefetchFeedsUnifiedFetchProgress: on a trusted-lockfile
// install, the fetching phase opens exactly once and BEFORE any
// download starts (today it opens only after every speculative
// download silently finished), every package ticks exactly once even
// though prefetch AND fetchAll both Fetch it, and the phase closes
// exactly once.
func TestPipelinePrefetchFeedsUnifiedFetchProgress(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	src := testlookup.New(map[string][]registry.PackageVersion{
		"acme/a": {testlookup.Pkg("acme/a", "1.0.0", nil)},
		"acme/b": {testlookup.Pkg("acme/b", "1.0.0", nil)},
	})
	opts := Options{
		ProjectDir: writeManifestObj(t, &manifest.Manifest{
			Name:    "acme/app",
			Require: map[string]string{"acme/a": "^1.0", "acme/b": "^1.0"},
		}),
		Source:       src,
		Fetcher:      &fakeFetcher{},
		Materializer: &fakeMaterializer{},
		Autoloader:   &fakeAutoloader{},
		NoScripts:    true,
	}
	// First install writes gomposer.lock (no lockfile yet, so no prefetch).
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install #1: %v", err)
	}

	// Second install: trusted lockfile → prefetch path. Fetcher and
	// Progress share one event log for ordering assertions.
	rp := &recordingProgress{}
	opts.Progress = rp
	opts.Fetcher = &orderedFetcher{rp: rp}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install #2: %v", err)
	}

	rp.mu.Lock()
	events := append([]string(nil), rp.events...)
	totals := append([]int(nil), rp.fetchTotals...)
	labels := append([]string(nil), rp.fetches...)
	rp.mu.Unlock()

	// Ordering: BeginFetch precedes every Fetch — the phase opens when
	// speculative downloads START, not after they finished.
	beginIdx, firstFetchIdx, fetchCount := -1, -1, 0
	for i, e := range events {
		switch {
		case e == "BeginFetch" && beginIdx == -1:
			beginIdx = i
		case len(e) > 6 && e[:6] == "Fetch ":
			fetchCount++
			if firstFetchIdx == -1 {
				firstFetchIdx = i
			}
		}
	}
	if beginIdx == -1 || firstFetchIdx == -1 || beginIdx > firstFetchIdx {
		t.Errorf("BeginFetch (idx %d) must precede first Fetch (idx %d); events=%v",
			beginIdx, firstFetchIdx, events)
	}
	// Prefetch + fetchAll each Fetch both packages: 2 speculative + 2
	// authoritative = 4 fetcher calls, but only 2 progress ticks. This
	// double-fetch is what the ticker's dedup exists for.
	if fetchCount != 4 {
		t.Errorf("second install made %d Fetch calls, want 4 (2 prefetch + 2 fetchAll); events=%v", fetchCount, events)
	}
	if n := rp.countEvent("BeginFetch"); n != 1 {
		t.Errorf("BeginFetch fired %d times, want exactly 1; events=%v", n, events)
	}
	if len(totals) != 1 || totals[0] != 2 {
		t.Errorf("BeginFetch totals = %v, want [2]", totals)
	}
	if len(labels) != 2 {
		t.Errorf("IncFetch fired %d times, want 2 (deduped across prefetch+fetchAll); labels=%v", len(labels), labels)
	}
	seen := map[string]bool{}
	for _, l := range labels {
		if seen[l] {
			t.Errorf("duplicate IncFetch label %q", l)
		}
		seen[l] = true
	}
	if n := rp.countEvent("EndFetch"); n != 1 {
		t.Errorf("EndFetch fired %d times, want exactly 1", n)
	}
}

// TestPipelineNoPrefetchFetchProgressUnchanged: with --no-prefetch the
// fetchAll-owned phase behaves exactly as before this feature — one
// BeginFetch with the package count, one tick per package, one EndFetch,
// and no speculative double-fetching.
func TestPipelineNoPrefetchFetchProgressUnchanged(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	src := testlookup.New(map[string][]registry.PackageVersion{
		"acme/a": {testlookup.Pkg("acme/a", "1.0.0", nil)},
		"acme/b": {testlookup.Pkg("acme/b", "1.0.0", nil)},
	})
	ff := &fakeFetcher{}
	opts := Options{
		ProjectDir: writeManifestObj(t, &manifest.Manifest{
			Name:    "acme/app",
			Require: map[string]string{"acme/a": "^1.0", "acme/b": "^1.0"},
		}),
		Source:       src,
		Fetcher:      ff,
		Materializer: &fakeMaterializer{},
		Autoloader:   &fakeAutoloader{},
		NoScripts:    true,
		NoPrefetch:   true,
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install #1: %v", err)
	}

	rp := &recordingProgress{}
	opts.Progress = rp
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install #2: %v", err)
	}

	if n := rp.countEvent("BeginFetch"); n != 1 {
		t.Errorf("BeginFetch fired %d times, want exactly 1", n)
	}
	rp.mu.Lock()
	totals := append([]int(nil), rp.fetchTotals...)
	labels := append([]string(nil), rp.fetches...)
	rp.mu.Unlock()
	if len(totals) != 1 || totals[0] != 2 {
		t.Errorf("BeginFetch totals = %v, want [2]", totals)
	}
	if len(labels) != 2 {
		t.Errorf("IncFetch fired %d times, want 2; labels=%v", len(labels), labels)
	}
	if n := rp.countEvent("EndFetch"); n != 1 {
		t.Errorf("EndFetch fired %d times, want exactly 1", n)
	}
	ff.mu.Lock()
	secondRunCalls := len(ff.calls) - 2
	ff.mu.Unlock()
	if secondRunCalls != 2 {
		t.Errorf("second install made %d Fetch calls, want 2 (no prefetch)", secondRunCalls)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail (RED)**

Run: `go test ./internal/orchestrator -run 'TestPipelinePrefetchFeedsUnified|TestPipelineNoPrefetchFetchProgress' -v`

Expected: `TestPipelinePrefetchFeedsUnifiedFetchProgress` **FAILS deterministically** on the ordering assertion — with today's code all 4 `Fetch` events (2 speculative + 2 authoritative) precede `BeginFetch`, because `fetchAll` only opens the phase after `prefetch.Wait()` returns. Failure message: `BeginFetch (idx 4) must precede first Fetch (idx 0)` (indices approximate).

`TestPipelineNoPrefetchFetchProgressUnchanged` **PASSES on today's code by design** — it is the control locking the legacy path's behavior so the wire-in can't regress it. Only the ordering test is the RED gate.

- [ ] **Step 4: Reshape `maybeStartPrefetch` to open the phase and return the ticker**

In `internal/orchestrator/prefetch.go`, replace `maybeStartPrefetch` (keep its skip-condition doc comment, extend it):

```go
// maybeStartPrefetch decides whether to kick off the speculative prefetch
// based on opts and the parsed pipeline state. It returns a non-nil
// *Prefetcher in every branch — callers can Wait() unconditionally without
// nil-checking. When prefetch is skipped, the returned Prefetcher's Wait
// is a no-op and the returned ticker is nil.
//
// When prefetch starts, maybeStartPrefetch opens the fetching progress
// phase (BeginFetch with the exact package count) and returns the shared
// fetchTicker that fetchAll must route its increments through. The phase
// MUST open before startPrefetch dispatches workers: a worker can
// complete a fetch (and tick) immediately, and a tick landing before
// BeginFetch would be zeroed by the renderer's beginPhase. The announced
// total is exact because prefetch only runs when the lockfile is trusted
// verbatim — fetchAll later receives exactly these packages.
//
// Skip conditions:
//   - forceResolve (update path): we have no reason to trust the lock.
//   - opts.NoNetwork: test-only flag; honour the no-network contract.
//   - opts.NoPrefetch: explicit user opt-out.
//   - len(ps.lockBytes) == 0: no lockfile to be confident in.
//   - lock.Decode fails: corrupt lock; fall back to resolver.
//   - opts.Fetcher == nil: defensive (defaultDeps wiring failure).
func maybeStartPrefetch(ctx context.Context, ps *pipelineState, opts Options, forceResolve bool) (*Prefetcher, *fetchTicker) {
	if forceResolve || opts.NoNetwork || opts.NoPrefetch {
		return &Prefetcher{}, nil
	}
	if len(ps.lockBytes) == 0 || opts.Fetcher == nil {
		return &Prefetcher{}, nil
	}
	lf, err := lock.Decode(ps.lockBytes)
	if err != nil {
		return &Prefetcher{}, nil
	}
	pkgs := prefetchPackages(lf, !opts.NoDev)
	if len(pkgs) == 0 {
		return &Prefetcher{}, nil
	}
	ticker := newFetchTicker(opts.Progress)
	ticker.prog.BeginFetch(len(pkgs))
	return startPrefetch(ctx, pkgs, opts.Fetcher, workerCount(opts.Workers), ticker.tick), ticker
}
```

- [ ] **Step 5: Reshape `fetchAll` and update its call sites**

In `internal/orchestrator/pipeline.go`, replace `fetchAll` (lines 301-335):

```go
// fetchAll downloads every package in pkgs concurrently with at most
// `workers` goroutines in flight. Returns map[name]storeKey.
//
// ticker, when non-nil, means the pipeline already opened the fetching
// phase (maybeStartPrefetch fired BeginFetch when speculative downloads
// began) and per-package increments must route through the shared dedup
// ticker so packages already ticked by prefetch don't double-count.
// When nil, fetchAll owns the whole phase itself. EndFetch fires here
// in both modes — exactly one summary per fetching phase.
func fetchAll(ctx context.Context, pkgs []lock.Package, f Fetcher, workers int, prog Progress, ticker *fetchTicker) (map[string]string, error) {
	if workers < 1 {
		workers = 1
	}
	if ticker == nil {
		ticker = newFetchTicker(prog)
		ticker.prog.BeginFetch(len(pkgs))
	}
	defer ticker.prog.EndFetch()

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
			ticker.tick(p.Name, p.Version)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return keys, nil
}
```

Note the removed `prog = progressOrNoop(prog)` line — `newFetchTicker` handles nil internally.

Update the two call sites in `internal/orchestrator/pipeline.go`:

Line 499 (in `runFullPipeline`):
```go
	prefetch, fetchTick := maybeStartPrefetch(ctx, ps, opts, forceResolve)
```

Line 581:
```go
	keys, err := fetchAll(ctx, fetchable, opts.Fetcher, workerCount(opts.Workers), opts.Progress, fetchTick)
```

Update the two call sites in `internal/orchestrator/orchestrator_test.go`:

Line 141: `keys, err := fetchAll(context.Background(), pkgs, ff, 2, nil, nil)`
Line 158: `if _, err := fetchAll(context.Background(), pkgs, ff, 2, nil, nil); err == nil {`

- [ ] **Step 6: Run the new tests to verify they pass**

Run: `go test ./internal/orchestrator -run 'TestPipelinePrefetchFeedsUnified|TestPipelineNoPrefetchFetchProgress' -race -count=3 -v`
Expected: PASS ×3, no race. The prefetch test's 4-calls/2-ticks assertions prove the dedup; the NoPrefetch test proves the legacy path is byte-identical.

- [ ] **Step 7: Run the full suite**

Run: `go build ./... && go test ./... -race -count=1`
Expected: all packages ok. Pay attention to `internal/orchestrator` prefetch timing tests (`TestPrefetchRacesResolver` etc.) — semantics unchanged, they must still pass.

- [ ] **Step 8: Manual e2e smoke test**

```bash
go build -o /tmp/gomposer ./cmd/gomposer
TMP=$(mktemp -d) && CACHE=$(mktemp -d)
cp cmd/bench/testdata/corpus/laravel-skeleton/composer.json "$TMP/"
cd "$TMP"
# Fresh install (no lock): resolve progress then fetchAll-owned fetch progress.
XDG_CACHE_HOME=$CACHE /tmp/gomposer install
# Cold-cache repeat (lock exists, store cleared): THE case this feature fixes —
# expect a live "fetching N/M" bar during the prefetch downloads.
rm -rf "$CACHE" && rm -rf vendor
XDG_CACHE_HOME=$(mktemp -d) /tmp/gomposer install
# Fully-warm repeat: fetch phase opens and completes instantly; one summary line.
/tmp/gomposer install
```

Expected on run 2: `gomposer: fetching K/N [=== ] vendor/pkg x.y.z` live during downloads, single `gomposer: fetched N packages` summary, then extract progress. No double summaries on any run.

- [ ] **Step 9: Commit**

```bash
git add internal/orchestrator/prefetch.go internal/orchestrator/pipeline.go internal/orchestrator/pipeline_test.go internal/orchestrator/orchestrator_test.go
git commit -m "feat(orchestrator): open fetching phase at prefetch start, share dedup ticker with fetchAll"
```

---

## Notes for the reviewer / implementer

- **Accepted cosmetic edge:** if the pipeline errors between prefetch start and `fetchAll` (e.g. a strict-platform hard fail), the fetching phase opened but never gets its summary line — the error message prints after an unfinished progress line. This matches today's behavior for any mid-phase error and is out of scope.
- **`ttyProgress.endPhase` self-guards** on an empty phase (added in the resolve-progress feature), so no path can print a stray summary; the exactly-one-`EndFetch` invariant is enforced at the orchestrator layer regardless.
- **Do not** modify `internal/cli/progress.go`, the `Progress` interface, or `internal/fetcher` — spec non-goals.
