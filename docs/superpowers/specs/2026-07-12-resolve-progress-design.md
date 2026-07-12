# Resolve-phase progress hook

## Motivation

The Stage 3 Progress interface starts at `BeginFetch`. On a cold-cache install this leaves the resolve phase silent — the "no progress for the first few seconds" the metadata-prefetch spec called out as a UX gap. With discovery-driven prefetch + in-flight dedup now shipped, the resolve phase is measurably faster, but from the user's perspective it's still a black box.

This adds a resolve triad symmetric to fetch/extract: `BeginResolve` / `IncResolve` / `EndResolve`. `ttyProgress` renders a redrawing status line during resolve; `noopProgress` stays silent.

## Scope

- `Progress` interface gains three methods.
- `noopProgress` and `ttyProgress` implement them; `ttyProgress` reuses the existing in-place redraw pattern.
- Resolver's `Input` gains an optional `OnLookup func(name string)` callback fired synchronously before each real `src.Lookup` call the resolver issues (cache-hit `Lookup` calls do NOT fire it — see semantics).
- The orchestrator brackets a fresh resolve with `BeginResolve(0)` / `EndResolve()` and wires `OnLookup` to `Progress.IncResolve`. On resolution-cache hit the brackets are skipped entirely (no resolver work means no progress phase).

## Non-goals

- **Metadata-prefetch pool's Lookups do NOT tick.** The pool goes through `opts.Source.Lookup` directly, bypassing `Input.OnLookup`. The user's mental model is "the resolver is working on X" — not "a background pool is prefetching X".
- **Solver work between Lookups.** Constraint propagation, backtracking, incompatibility construction — invisible. In practice Lookups dominate wall time on cold cache, so counting them is a good proxy.
- **Package count hint.** Resolver doesn't know N upfront. `BeginResolve` takes a hint `int` for future use and interface symmetry with `BeginFetch`, but the ttyProgress renderer displays only the running count with no denominator when hint is 0.
- **Verbose timing changes.** The `resolve` timing phase already exists. Progress and timing are independent surfaces.

## Design

### Interface

`internal/cli/progress.go`:

```go
type Progress interface {
    BeginFetch(total int)
    IncFetch(name string)
    EndFetch()

    BeginExtract(total int)
    IncExtract(name string)
    EndExtract()

    BeginResolve(hint int)
    IncResolve(name string)
    EndResolve()

    Done(packageCount int)
}
```

`noopProgress` gets three no-ops. `ttyProgress`:

- `beginPhase("resolving", hint)` on `BeginResolve` — reuses the existing throttled-redraw state.
- `inc(name)` on `IncResolve` — same increment path fetch/extract use.
- `endPhase("resolved")` on `EndResolve` — reuses the same summary formatting.

The existing `beginPhase` renders `gomposer: <phase> <cur>/<total>  [====   ]  <label>` when total > 0, and would need a minor adjustment to render `gomposer: <phase> <cur>  <label>` (no denominator, no bar) when total is 0. Small `if total > 0` branch inside the draw path.

### Resolver callback

`internal/resolver/solve.go` — add to `Input`:

```go
// OnLookup, when non-nil, fires synchronously before the resolver
// issues a Lookup to the underlying registry source. Fires only on
// network-path lookups — cache-hit reads inside versionLister do not
// fire it, so the callback tick matches user-visible resolver work.
// Implementations must return quickly and not panic; called from the
// resolver's goroutine.
OnLookup func(pkgName string)
```

`internal/resolver/versions.go`:

- `versionLister` gains `onLookup func(string)` field.
- `Solve` wires `vl.onLookup = in.OnLookup` right after `newVersionLister`.
- `versionLister.versions(ctx, pkg)` invokes `vl.onLookup(pkg)` immediately before `vl.src.Lookup(ctx, pkg)`. The existing `vl.cache[pkg]` check short-circuits before this point, so cache hits don't tick.

### Pipeline wire-in

`internal/orchestrator/pipeline.go`, in `runFullPipeline`:

- Move the existing `t.Begin("resolve")` sequence around `resolveOrCache`. Add a Progress bracket around the same span, but *only* when the resolve wasn't served from cache.

Two approaches:

**(A) Optimistic bracket, unbracket on cache-hit.** Call `BeginResolve(0)` before `resolveOrCache`, `EndResolve()` after. If `fromCache=true`, the phase had no `Inc` calls — the ttyProgress renderer emits an empty summary line. Ugly.

**(B) Delayed bracket via `OnLookup`.** Only fire `BeginResolve` on the first `OnLookup` invocation. The closure captures a `sync.Once` and calls `Begin` before the first `Inc`. `EndResolve` fires unconditionally after `resolveOrCache` returns, but is a no-op on the ttyProgress side if `Begin` never fired.

**(C) Bracket only on cache-miss.** Set `OnLookup` to a closure that ticks. After `resolveOrCache` returns, if `!fromCache`, retroactively fire `BeginResolve(0)` … `IncResolve(...)` — impossible without a queue.

**(B)** is the cleanest. The `Once` guard makes the first `Inc` also fire `Begin`; every subsequent tick is just a redraw. On cache-hit runs, `Begin` never fires and `End` becomes a noop on the ttyProgress side (guarded by a `phase == ""` check that already exists).

Concrete wire-in:

```go
progress := opts.Progress
if progress == nil {
    progress = noopProgress{}
}

var beginOnce sync.Once
onLookup := func(name string) {
    beginOnce.Do(func() { progress.BeginResolve(0) })
    progress.IncResolve(name)
}

// existing timing bracket...
t.Begin("resolve")
lockFile, fromCache, err := resolveOrCache(ctx, ps, forceResolve)
t.End("resolve")

// End is safe on ttyProgress even if Begin never fired — the phase
// state guard makes endPhase a no-op when phase is "".
progress.EndResolve()
```

`ps.opts.Progress` is threaded into `resolveFunc` via a closure so the `OnLookup` callback survives across `resolver.Input` construction. Given `pipelineState.opts` already carries `Progress`, no new field is needed.

### Interaction with existing timing

The `-v` timing block still shows `resolve XXXms` unchanged. The progress line is a live redraw during the phase; timing is the retrospective summary. Independent.

## Tests

- **Unit — `ttyProgress` renders resolve line.** Similar to `TestTTYProgressEmitsClearAndProgress` for fetch: call `BeginResolve(0)`, `IncResolve("psr/log")`, `IncResolve("monolog/monolog")`, `EndResolve()`; assert output contains `\r\x1b[K`, `resolving`, `monolog/monolog`, and a `resolved 2 packages` summary line.
- **Unit — `noopProgress` silent on resolve.** Extends `TestNoopProgressIsSilent`.
- **Unit — resolver fires `OnLookup` per unique Lookup.** Fake source counts calls; assert `OnLookup` fired once per unique name (matches call count, not attempt count — cache-hit re-reads don't fire).
- **Integration — pipeline exposes IncResolve.** In `internal/orchestrator/pipeline_test.go`, extend an existing integration test (or add one) with a recording `Progress` fake; assert `IncResolve` was called during a fresh install and not called on a resolution-cache-hit repeat install.

## Related follow-ups (not this pass)

- **Ticker-based smooth redraw** for the resolve phase — the current throttled-redraw (50ms) already smooths bursts, but a background ticker would keep the line alive during long single-Lookup waits. Not important now.
- **Metadata-prefetch line separate from resolve.** Currently `-v` prints `metadata-prefetch 800 ms (4 warmed)` under `resolve` in the timing block. Progress rendering could similarly separate the two visually. Cosmetic.
