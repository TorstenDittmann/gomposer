# Discovery-driven Metadata Prefetch (Scope C) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Warm the registry-metadata cache for a version's transitive requires as soon as the solver commits that version — the "Scope C" callout from the metadata-prefetch spec — so cold installs no longer sit in a serial `/p2/<name>.json` fetch loop during resolve.

**Architecture:** The resolver gains an optional `OnVersionDecided` callback fired from `decide()` right after a version is committed. The metadata prefetcher grows a `Add(names []string)` method that enqueues additional lookups into the already-running errgroup. The orchestrator wires the two together at pipeline construction; nothing else changes.

**Tech Stack:** Go 1.25, `golang.org/x/sync/errgroup` (already in use). No new dependencies.

## Global Constraints

- **Callback contract:** `OnVersionDecided func(pkgName string, requires map[string]string)` fires from the resolver's `decide()` **every time** a version is committed. No first-seen dedup in the callback layer — the pool is responsible.
- **`Add` contract:** fire-and-forget, safe for concurrent callers, silently drops names that fail the workspace filter, the platform-req filter, or the shared `seen` set.
- **Seed the `seen` set** at construction with the initial A+B warm set so discovery-driven enqueues don't re-fetch what's already flying.
- **Filtering matches `collectMetadataPrefetchNames`:**
  - `platform.IsPlatformReq(name)` → skip.
  - Name is in the workspace set → skip.
  - Name already in `seen` → skip.
- **Cancel invariant:** cancelling the pool's context stops both A+B workers and any subsequent Add-spawned workers. `TestMetadataPrefetchCancelsOnCacheHit` must remain green.
- **Zero external API break:** `MetadataPrefetcher.Wait/Cancel/Stats` signatures unchanged. The new state fields are unexported.

---

## File structure

| Path | Responsibility |
|------|---------------|
| `internal/resolver/solve.go` | Add `Input.OnVersionDecided` field. |
| `internal/resolver/versions.go` | `versionLister` carries the callback so `decide()` can invoke it via the lister. |
| `internal/resolver/decide.go` | Invoke the callback after a version is committed to the partial solution. |
| `internal/resolver/decide_test.go` (or `solve_test.go`) | Assert the callback fires on every commit with the right args. |
| `internal/orchestrator/metadata_prefetch.go` | Hoist pool state (`g`, `ctx`, `source`, `seen`, `seenMu`, `wsNames`) onto `MetadataPrefetcher`. Add `Add(names []string)`. Seed `seen` with initial warm set. |
| `internal/orchestrator/metadata_prefetch_test.go` | Contention test on `Add`; seed-set dedup test. |
| `internal/orchestrator/pipeline.go` | Build `OnVersionDecided` closure and pass into `resolver.Input`. |
| `internal/orchestrator/pipeline_test.go` | Fresh-install wall-time-reduction integration test. |

---

## Task 1: Hoist prefetcher state onto `MetadataPrefetcher`

**Files:**
- Modify: `internal/orchestrator/metadata_prefetch.go`
- Modify: `internal/orchestrator/metadata_prefetch_test.go`

**Interfaces:**
- No exported API change. Existing `Wait`, `Cancel`, `Stats`, `newNoopMetadataPrefetcher`, `maybeStartMetadataPrefetch` signatures unchanged.
- Internal shape change: `MetadataPrefetcher` gains fields — `g *errgroup.Group`, `ctx context.Context`, `source registry.SourceLookup`, `wsNames map[string]struct{}`, `seenMu sync.Mutex`, `seen map[string]struct{}` — so a future `Add` method can reuse them.

- [ ] **Step 1: Write a regression test**

Append to `internal/orchestrator/metadata_prefetch_test.go`:

```go
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
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/orchestrator/ -run TestMaybeStartMetadataPrefetchSeedsSeenWithInitialWarmSet -v`

Expected: compile error — `p.seenMu`, `p.seen` don't exist yet.

- [ ] **Step 3: Refactor `maybeStartMetadataPrefetch`**

Current shape uses closure locals for `g`, `ctx`, `source`, and does not track `seen` at all. Rewrite:

```go
func maybeStartMetadataPrefetch(ctx context.Context, ps *pipelineState, opts Options) *MetadataPrefetcher {
    if opts.NoMetadataPrefetch || opts.NoNetwork || opts.Source == nil {
        return newNoopMetadataPrefetcher()
    }
    names := collectMetadataPrefetchNames(ps, !opts.NoDev)
    if len(names) == 0 {
        return newNoopMetadataPrefetcher()
    }

    cancelCtx, cancel := context.WithCancel(ctx)
    g, gctx := errgroup.WithContext(cancelCtx)
    g.SetLimit(workerCount(opts.Workers))

    // Seed the seen set with the initial warm set so discovery-driven
    // Add() calls don't re-enqueue what A/B already fired.
    seen := make(map[string]struct{}, len(names))
    for _, n := range names {
        seen[n] = struct{}{}
    }

    // Workspace name set for Add's filter. collectMetadataPrefetchNames
    // already excluded workspace names from the initial set, but
    // discovery-driven callers pass arbitrary require maps.
    wsNames := workspaceNameSet(ps.workspaces)

    p := &MetadataPrefetcher{
        cancel:  cancel,
        g:       g,
        ctx:     gctx,
        source:  opts.Source,
        wsNames: wsNames,
        seen:    seen,
    }
    p.wg.Add(1)
    start := time.Now()
    go func() {
        defer p.wg.Done()
        defer cancel()
        for _, name := range names {
            name := name
            g.Go(func() error {
                if _, err := opts.Source.Lookup(gctx, name); err != nil {
                    return nil
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

And update `MetadataPrefetcher`:

```go
type MetadataPrefetcher struct {
    wg     sync.WaitGroup
    stats  prefetchStats
    cancel context.CancelFunc

    // Hoisted from the maybeStart goroutine so Add() can reuse them.
    // Zero-valued on the noop instance; Add short-circuits on g == nil.
    g       *errgroup.Group
    ctx     context.Context
    source  registry.SourceLookup
    wsNames map[string]struct{}
    seenMu  sync.Mutex
    seen    map[string]struct{}
}
```

Add helper:

```go
// workspaceNameSet returns a lookup set of workspace names, or an empty
// non-nil map when the project has no workspaces. Used by Add() to
// filter out workspace names surfaced through OnVersionDecided.
func workspaceNameSet(ws []manifest.Workspace) map[string]struct{} {
    out := make(map[string]struct{}, len(ws))
    for _, w := range ws {
        out[w.Name] = struct{}{}
    }
    return out
}
```

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/orchestrator/ -v -run 'TestMaybeStart|TestNoop|TestMetadataPrefetcher'`

Expected: all pass (existing tests) + the new seed-set test.

- [ ] **Step 5: Full suite check**

Run: `go test ./...`

Expected: green. If cache-hit-cancel tests break, the cancel plumbing is wrong — inspect the deferred `cancel()` vs the outer `p.cancel`.

- [ ] **Step 6: Commit**

```bash
git add internal/orchestrator/metadata_prefetch.go internal/orchestrator/metadata_prefetch_test.go
git commit -m "refactor(orchestrator): hoist prefetcher pool state onto struct

Prepares for Add() by moving errgroup, context, source, and the workspace
name set from the maybeStartMetadataPrefetch goroutine's closure onto
MetadataPrefetcher fields. Also seeds the new 'seen' map with the initial
warm set (root requires + lock package names) so a future Add call won't
re-enqueue what A/B already dispatched. No external API change."
```

---

## Task 2: `MetadataPrefetcher.Add(names []string)`

**Files:**
- Modify: `internal/orchestrator/metadata_prefetch.go`
- Modify: `internal/orchestrator/metadata_prefetch_test.go`

**Interfaces:**
- New method: `func (p *MetadataPrefetcher) Add(names []string)` — fire-and-forget, concurrent-safe, deduped.
- Consumes: `p.g`, `p.ctx`, `p.source`, `p.wsNames`, `p.seen` from Task 1.

- [ ] **Step 1: Failing tests**

Append to `internal/orchestrator/metadata_prefetch_test.go`:

```go
func TestMetadataPrefetcherAddDedupesAcrossConcurrentCallers(t *testing.T) {
	src := newFakeSourceLookup()
	ps := &pipelineState{
		manifest: &manifest.Manifest{Require: map[string]string{"a/a": "^1"}},
	}
	opts := Options{Source: src}
	p := maybeStartMetadataPrefetch(context.Background(), ps, opts)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Overlapping name sets across 5 goroutines; every name
			// should be Lookup'd exactly once (or zero times if it was
			// in the seed set, like a/a).
			p.Add([]string{"a/a", "b/b", "c/c", "d/d"})
		}()
	}
	wg.Wait()
	p.Wait()

	// a/a was in the initial warm set (seeded).
	// b/b, c/c, d/d each Lookup once.
	if got := src.callsFor("a/a"); got != 1 {
		t.Errorf("a/a Lookup count = %d, want 1 (initial warm set only)", got)
	}
	for _, name := range []string{"b/b", "c/c", "d/d"} {
		if got := src.callsFor(name); got != 1 {
			t.Errorf("%s Lookup count = %d, want 1 (dedup failed)", name, got)
		}
	}
}

func TestMetadataPrefetcherAddFiltersPlatformReqs(t *testing.T) {
	src := newFakeSourceLookup()
	ps := &pipelineState{
		manifest: &manifest.Manifest{Require: map[string]string{"a/a": "^1"}},
	}
	opts := Options{Source: src}
	p := maybeStartMetadataPrefetch(context.Background(), ps, opts)

	p.Add([]string{"php", "ext-json", "lib-curl", "b/b"})
	p.Wait()

	for _, name := range []string{"php", "ext-json", "lib-curl"} {
		if got := src.callsFor(name); got != 0 {
			t.Errorf("platform req %s hit Lookup: %d", name, got)
		}
	}
	if got := src.callsFor("b/b"); got != 1 {
		t.Errorf("b/b Lookup count = %d, want 1", got)
	}
}

func TestMetadataPrefetcherAddFiltersWorkspaceNames(t *testing.T) {
	src := newFakeSourceLookup()
	ps := &pipelineState{
		manifest: &manifest.Manifest{Require: map[string]string{"a/a": "^1"}},
		workspaces: []manifest.Workspace{
			{Name: "acme/shared"},
			{Name: "acme/api"},
		},
	}
	opts := Options{Source: src}
	p := maybeStartMetadataPrefetch(context.Background(), ps, opts)

	// A workspace name arriving via Add (e.g. a transitive require) must
	// be filtered — workspaces are already known locally.
	p.Add([]string{"acme/shared", "acme/api", "b/b"})
	p.Wait()

	for _, name := range []string{"acme/shared", "acme/api"} {
		if got := src.callsFor(name); got != 0 {
			t.Errorf("workspace %s hit Lookup: %d", name, got)
		}
	}
	if got := src.callsFor("b/b"); got != 1 {
		t.Errorf("b/b Lookup count = %d, want 1", got)
	}
}

func TestMetadataPrefetcherAddOnNoopIsSafe(t *testing.T) {
	p := newNoopMetadataPrefetcher()
	// Must not panic and must not attempt any Lookup.
	p.Add([]string{"a/a", "b/b"})
	p.Wait() // returns immediately
}
```

`fakeSourceLookup.callsFor(name string) int` — extend the fake to expose per-name counts if it doesn't already; the existing test file's fake should already track this.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/orchestrator/ -run 'TestMetadataPrefetcherAdd' -v`

Expected: compile error on `Add`.

- [ ] **Step 3: Implement `Add`**

Add to `internal/orchestrator/metadata_prefetch.go`:

```go
// Add enqueues additional names into the running metadata prefetch pool.
// Safe to call from multiple goroutines. Names that fail the workspace /
// platform filter or were already enqueued (either as part of the
// initial warm set or by a prior Add) are silently dropped. Fire-and-
// forget — returns immediately without waiting for the lookups to
// complete. Callers should still Wait on the prefetcher at the end of
// the pipeline to drain the pool.
//
// Safe to call on a noop instance (constructed via
// newNoopMetadataPrefetcher): it's a no-op.
func (p *MetadataPrefetcher) Add(names []string) {
    if p == nil || p.g == nil {
        return
    }
    for _, name := range names {
        if _, ok := p.wsNames[name]; ok {
            continue
        }
        if platform.IsPlatformReq(name) {
            continue
        }
        p.seenMu.Lock()
        if _, dup := p.seen[name]; dup {
            p.seenMu.Unlock()
            continue
        }
        p.seen[name] = struct{}{}
        p.seenMu.Unlock()

        name := name
        p.g.Go(func() error {
            if _, err := p.source.Lookup(p.ctx, name); err != nil {
                return nil
            }
            p.stats.mu.Lock()
            p.stats.warmed++
            p.stats.mu.Unlock()
            return nil
        })
    }
}
```

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/orchestrator/ -run 'TestMetadataPrefetcherAdd' -race -v`

Expected: PASS on all four tests, including under `-race`.

- [ ] **Step 5: Full-suite check**

Run: `go test ./...`

Expected: green. Existing `TestMetadataPrefetchReducesResolveWallTime` and `TestMetadataPrefetchCancelsOnCacheHit` should still pass — the pool's semantics haven't changed for the initial warm set.

- [ ] **Step 6: Commit**

```bash
git add internal/orchestrator/metadata_prefetch.go internal/orchestrator/metadata_prefetch_test.go
git commit -m "feat(orchestrator): MetadataPrefetcher.Add for discovery-driven prefetch

Add() enqueues additional package names into the running errgroup, so
a resolver-side hook can dispatch a just-decided version's transitive
requires as work for the pool. Safe under concurrent callers via a
mutex-guarded 'seen' map (seeded by maybeStart with the initial warm
set); filters workspace names and platform reqs like the initial set
does; no-ops on a noop prefetcher. Fire-and-forget: never blocks the
caller."
```

---

## Task 3: Resolver `OnVersionDecided` callback

**Files:**
- Modify: `internal/resolver/solve.go`
- Modify: `internal/resolver/versions.go`
- Modify: `internal/resolver/decide.go`
- Modify: `internal/resolver/solve_test.go` (or a sibling `_test.go` file)

**Interfaces:**
- New field on `resolver.Input`: `OnVersionDecided func(pkgName string, requires map[string]string)`.
- Optional — nil means current behavior.
- `versionLister` stores the callback (attached in `newVersionLister`) so `decide()` can invoke it via the lister.

- [ ] **Step 1: Failing test**

Append to `internal/resolver/solve_test.go`:

```go
func TestSolveFiresOnVersionDecidedForEveryCommit(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {testlookup.Pkg("a/a", "1.0.0", map[string]string{"b/b": "^1.0"})},
		"b/b": {testlookup.Pkg("b/b", "1.0.0", nil)},
	})
	m := &manifest.Manifest{
		Name:    "user/app",
		Require: map[string]string{"a/a": "^1.0"},
	}

	var mu sync.Mutex
	seen := map[string]map[string]string{}
	res, err := Solve(context.Background(), Input{
		Manifest: m,
		Source:   src,
		OnVersionDecided: func(name string, requires map[string]string) {
			mu.Lock()
			defer mu.Unlock()
			// Copy the map — the resolver may reuse the underlying map.
			cp := make(map[string]string, len(requires))
			for k, v := range requires {
				cp[k] = v
			}
			seen[name] = cp
		},
	})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if len(res.Packages) != 2 {
		t.Fatalf("Packages = %d, want 2", len(res.Packages))
	}

	mu.Lock()
	defer mu.Unlock()
	if _, ok := seen["a/a"]; !ok {
		t.Errorf("callback never fired for a/a; seen = %v", seen)
	}
	if _, ok := seen["b/b"]; !ok {
		t.Errorf("callback never fired for b/b; seen = %v", seen)
	}
	if len(seen["a/a"]) != 1 || seen["a/a"]["b/b"] != "^1.0" {
		t.Errorf("callback for a/a saw requires = %v, want {b/b: ^1.0}", seen["a/a"])
	}
}

func TestSolveTolerateNilOnVersionDecided(t *testing.T) {
	// Sanity: nil callback is the existing behavior; must not panic.
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {testlookup.Pkg("a/a", "1.0.0", nil)},
	})
	m := &manifest.Manifest{Name: "user/app", Require: map[string]string{"a/a": "^1.0"}}
	if _, err := Solve(context.Background(), Input{Manifest: m, Source: src}); err != nil {
		t.Fatalf("Solve with nil callback: %v", err)
	}
}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/resolver/ -run TestSolveFiresOnVersionDecided -v`

Expected: compile error on `Input.OnVersionDecided`.

- [ ] **Step 3: Add the field to `resolver.Input`**

In `internal/resolver/solve.go`, next to the existing `Input` fields:

```go
// OnVersionDecided, when non-nil, fires from decide() every time a
// version is committed to the partial solution. The orchestrator uses
// this to dispatch the just-decided version's transitive requires to a
// background metadata prefetch pool.
//
// Called synchronously from decide(); implementations must return
// quickly. No first-seen dedup here — the callback fires every time.
OnVersionDecided func(pkgName string, requires map[string]string)
```

- [ ] **Step 4: Thread the callback through `versionLister`**

In `internal/resolver/versions.go`, add a field to `versionLister`:

```go
onDecided func(pkgName string, requires map[string]string)
```

Update `newVersionLister` to accept and stash it. The plan uses attachment at construction rather than threading through `decide()` because the lister already flows through the entire solve, and `decide()`'s signature stays clean.

Actually — `newVersionLister` is called from `Solve()` which has direct access to `in.OnVersionDecided`. Wire it there:

```go
vl := newVersionLister(in.Source, minStab)
vl.onDecided = in.OnVersionDecided
```

Or add a setter method. Field-set is fine given the tight coupling.

- [ ] **Step 5: Invoke the callback in `decide.go`**

In `internal/resolver/decide.go`, find the point where `ps.Decide(...)` (or equivalent commit) is called — the branch where `decideMade` is returned. Add immediately after:

```go
if vl.onDecided != nil {
    vl.onDecided(chosen.Record.Name, chosen.Record.Require)
}
```

`chosen` is the `listedVersion` being committed. If the local variable has a different name in the file, use that; the point is to pass the committed version's `Record.Name` and `Record.Require`.

- [ ] **Step 6: Verify GREEN**

Run: `go test ./internal/resolver/ -race -v`

Expected: PASS — both new tests + every existing resolver test.

- [ ] **Step 7: Commit**

```bash
git add internal/resolver/solve.go internal/resolver/versions.go internal/resolver/decide.go internal/resolver/solve_test.go
git commit -m "feat(resolver): OnVersionDecided callback for discovery-driven prefetch

Adds an optional Input.OnVersionDecided hook that fires from decide()
every time a version is committed to the partial solution. The
orchestrator will use this to dispatch the just-decided version's
transitive requires to the metadata prefetch pool. Nil callback == pre-
existing behavior. Callback is fired unconditionally on each commit;
the pool handles dedup."
```

---

## Task 4: Pipeline wire-in

**Files:**
- Modify: `internal/orchestrator/pipeline.go`

**Interfaces:**
- No new exports.
- `resolveFunc` (or wherever `resolver.Input` is constructed) now sets `OnVersionDecided` when the metadata prefetcher is real (non-noop).

- [ ] **Step 1: Locate the resolver Input construction**

Grep in `internal/orchestrator/pipeline.go` for `resolver.Input{`. The resolver is invoked from `resolveFunc` (or an equivalent seam). The `Input` literal is where the new field lands.

- [ ] **Step 2: Wire the callback**

In `runFullPipeline`, after `mprefetch := maybeStartMetadataPrefetch(...)` and before the resolver call, build the closure. Since `resolveFunc` is a `var` and the callback needs `mprefetch` in scope, pass `mprefetch` through the `resolveFunc` signature — or (simpler) construct the callback in-line at the call site:

```go
resolveIn := resolver.Input{
    Manifest:            ps.aggregateManifest,
    Source:              src,
    IncludeDev:          !ps.opts.NoDev,
    Platform:            ps.platform,
    IgnorePlatformReqs:  ps.ignoreSet,
    PlatformFingerprint: ps.platformStr,
    StrictPlatform:      ps.opts.NoDev,
    OnVersionDecided: func(_ string, reqs map[string]string) {
        if len(reqs) == 0 {
            return
        }
        names := make([]string, 0, len(reqs))
        for name := range reqs {
            names = append(names, name)
        }
        mprefetch.Add(names)
    },
}
res, err := resolver.Solve(ctx, resolveIn)
```

If the current code uses a `resolveFunc` seam for testability, keep the seam. Options: (a) pass `mprefetch` through the seam signature (small API change), (b) attach the callback to `pipelineState` and let `resolveFunc` read it. (b) is nicer for tests that inject a fake `resolveFunc`.

Pick whichever slots in cleanly. The plan does not mandate one over the other — verify existing tests still pass.

- [ ] **Step 3: Run tests**

Run: `go test ./internal/orchestrator/...`

Expected: green. If a test that mocks `resolveFunc` breaks, adapt it — the resolver's `Input` shape has grown by one field.

- [ ] **Step 4: Full-suite check**

Run: `go test ./...`

Expected: green.

- [ ] **Step 5: Commit**

```bash
git add internal/orchestrator/pipeline.go
git commit -m "feat(orchestrator): wire discovery-driven metadata prefetch

runFullPipeline now attaches an OnVersionDecided callback to
resolver.Input that dispatches the just-decided version's transitive
requires to the metadata prefetch pool. Empty require maps short-
circuit. Cancel + noop paths unchanged."
```

---

## Task 5: Integration test — fresh-install wall-time reduction

**Files:**
- Modify: `internal/orchestrator/pipeline_test.go`

**Interfaces:** none new.

The existing `TestMetadataPrefetchReducesResolveWallTime` exercises the A+B path against a slow-fake source with root-only requires. This task adds a companion test exercising the C path: a fresh install with no lock and a chain of transitive requires, asserting that discovery-driven prefetch measurably reduces wall time compared to the same install with metadata prefetch disabled.

- [ ] **Step 1: Failing test**

Append to `internal/orchestrator/pipeline_test.go`:

```go
// TestMetadataPrefetchWarmTransitivesOnFreshInstall asserts Scope C:
// on a fresh install with no lock, transitive requires get prefetched
// as the solver commits versions, so total wall time is lower than
// with metadata prefetch disabled entirely.
func TestMetadataPrefetchWarmTransitivesOnFreshInstall(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-sensitive; skipping under -short")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	// Chain: a -> b -> c -> d -> e, each takes 40ms on first Lookup.
	// Root only requires a; b/c/d/e are pure transitives that only a
	// discovery-driven prefetch can warm.
	chain := map[string][]registry.PackageVersion{
		"acme/a": {testlookup.Pkg("acme/a", "1.0.0", map[string]string{"acme/b": "^1.0"})},
		"acme/b": {testlookup.Pkg("acme/b", "1.0.0", map[string]string{"acme/c": "^1.0"})},
		"acme/c": {testlookup.Pkg("acme/c", "1.0.0", map[string]string{"acme/d": "^1.0"})},
		"acme/d": {testlookup.Pkg("acme/d", "1.0.0", map[string]string{"acme/e": "^1.0"})},
		"acme/e": {testlookup.Pkg("acme/e", "1.0.0", nil)},
	}

	baseOpts := func() Options {
		return Options{
			ProjectDir: writeManifest(t, &manifest.Manifest{
				Name:    "acme/app",
				Require: map[string]string{"acme/a": "^1.0"},
			}),
			Source:    &sleepySourceLookup{delay: 40 * time.Millisecond, versions: chain},
			NoPrefetch: true, // artifact prefetch off — isolate metadata prefetch
		}
	}

	// Baseline: no metadata prefetch.
	base := baseOpts()
	base.NoMetadataPrefetch = true
	tBaseline := timeInstall(t, base)

	// With discovery-driven prefetch (default on).
	on := baseOpts()
	on.NoMetadataPrefetch = false
	tPrefetch := timeInstall(t, on)

	if tPrefetch >= tBaseline {
		t.Errorf("discovery-driven prefetch did not speed up install: baseline=%v prefetch=%v", tBaseline, tPrefetch)
	}
}
```

Test uses `writeManifest`, `timeInstall`, `sleepySourceLookup`, and `t.Setenv("XDG_CACHE_HOME", t.TempDir())` — all patterns already used by `TestMetadataPrefetchReducesResolveWallTime`.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/orchestrator/ -run TestMetadataPrefetchWarmTransitivesOnFreshInstall -v`

Expected: FAIL — either compile error (helpers) or the assertion fails because Task 4's callback isn't wired.

- [ ] **Step 3: Should already GREEN from Tasks 1-4**

If Tasks 1-4 are all correct, this test passes without further code changes. Run it. If it fails, the wire-in is wrong — inspect the callback closure in Task 4 for issues (e.g., calling `mprefetch.Add` before `mprefetch` is initialized).

- [ ] **Step 4: `-race` check**

Run: `go test ./internal/orchestrator/ -race -run TestMetadataPrefetchWarmTransitives -v`

Expected: pass under `-race`. The `Add` path is the concurrency-sensitive one.

- [ ] **Step 5: Full-suite check**

Run: `go test ./...`

Expected: green.

- [ ] **Step 6: Commit**

```bash
git add internal/orchestrator/pipeline_test.go
git commit -m "test(orchestrator): assert fresh-install wall-time drop from discovery prefetch

Complements TestMetadataPrefetchReducesResolveWallTime (which exercised
root-only prefetch) with a chain of 5 transitive requires that only C
can warm. Asserts total install with prefetch is strictly faster than
with --no-metadata-prefetch."
```

---

## Discovery-driven prefetch: acceptance check

After all tasks:

- `go test ./...` is green with `-race`.
- `TestMetadataPrefetchReducesResolveWallTime` (A/B) and `TestMetadataPrefetchWarmTransitivesOnFreshInstall` (C) both pass.
- `TestMetadataPrefetchCancelsOnCacheHit` still passes — cancel drains the pool including any Add-spawned goroutines.
- On the Appwrite-sized real project (76 transitive requires, deep tree), a cold `gomposer install` shows a measurably lower `resolve` wall-time than the same install with `--no-metadata-prefetch`. The `metadata-prefetch N warmed` line reports a substantially higher `N` than the pre-C build.
- Verbose output shape unchanged — the `metadata-prefetch` line still fires only on cache-miss runs and reports total warmed.

If any of these fails, fix forward before declaring the plan done.
