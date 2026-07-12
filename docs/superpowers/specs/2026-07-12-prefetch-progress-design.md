# Prefetch download progress

## Motivation

On the most common install path — a project with a trusted `gomposer.lock` — the lock-driven speculative prefetcher (`internal/orchestrator/prefetch.go`) downloads every package in the lock with zero progress output. `prefetch.Wait()` blocks until all downloads finish, then `fetchAll` runs against a fully warm store and completes in milliseconds. The user sees a long silent wait where the actual downloading happens, a "fetching" line that flashes for one frame, and then normal extract progress.

The fetch progress bar today can only ever measure store lookups on this path, never real downloads. This spec makes prefetch downloads feed the same `fetching` phase, so one honest `gomposer: fetching N/M` line spans the entire download window.

## Scope

- `startPrefetch` gains an `onFetched func(name, version string)` callback, fired after each *successful* speculative fetch.
- Prefetch's dispatch list is filtered to non-workspace packages (workspace entries have no dist and their Fetch always errors; today those errors are swallowed, but they must not distort the announced total).
- The pipeline opens the `fetching` phase when prefetch actually starts — `BeginFetch(N)` with N exactly equal to the package set `fetchAll` will later receive.
- A shared dedup-by-name ticker feeds `IncFetch` from both prefetch completions and `fetchAll`, so a package fetched by prefetch and re-verified warm by `fetchAll` ticks exactly once.
- `fetchAll` learns not to re-`Begin` when the pipeline already opened the phase. Its deferred `EndFetch` still closes the phase in both modes.
- Display: package count only (per existing renderer), label stays `fetching` / `fetched`. No renderer changes.

## Non-goals

- **Byte-level progress.** `Fetcher.OnFetch` reports byte counts, but there is no upfront byte total to render a meaningful bar against. Package count is the progress unit, consistent with resolve/extract.
- **Fetcher-layer changes.** The `OnFetch` hook keeps feeding timings only. Progress wiring stays at the orchestrator call sites, where `fakeFetcher`-based tests can drive it.
- **Surfacing prefetch failures.** Prefetch errors remain swallowed; `fetchAll` is the authoritative gate and surfaces real failures with the right message. A prefetch-failed package simply ticks later, when `fetchAll` fetches it.
- **A separate "prefetching" phase.** One phase, one summary line. Two phases would print two summaries for what the user perceives as one download step.

## Design

### Why the total is exact

`maybeStartPrefetch` runs only when a decodable `gomposer.lock` exists and `forceResolve` is false (`prefetch.go:62-74`). On that same condition, `resolveOrCache` short-circuits and returns the decoded lock verbatim (`pipeline.go:237-242`). Therefore whenever prefetch runs:

- the resolver never runs (no resolve-progress overlap on the shared TTY line), and
- `fetchAll` receives exactly the lock's packages, filtered by `!opts.NoDev` and `nonWorkspacePackages` — a set computable at prefetch start.

`BeginFetch(N)` announced at prefetch start is therefore never revised.

### Prefetcher callback

`internal/orchestrator/prefetch.go` — `startPrefetch` is reshaped to take the already-filtered package list (its caller now needs that list anyway to announce the total), plus the callback:

```go
// startPrefetch begins downloading every package in pkgs using f.
// onFetched, when non-nil, fires after each successful speculative
// download (or warm-store hit) with the package's name and version.
// Failed fetches do not fire it — fetchAll retries those
// authoritatively and reports them through the same shared ticker.
// Called from prefetch worker goroutines; implementations must be
// concurrency-safe and cheap.
func startPrefetch(ctx context.Context, pkgs []lock.Package, f Fetcher, limit int, onFetched func(name, version string)) *Prefetcher
```

Inside the worker:

```go
g.Go(func() error {
    _, err := f.Fetch(gctx, p)
    if err == nil && onFetched != nil {
        onFetched(p.Name, p.Version)
    }
    return nil
})
```

A new `prefetchPackages(lf *lock.File, includeDev bool) []lock.Package` helper builds the list: `lf.Packages` + optional `lf.PackagesDev`, filtered through the existing `nonWorkspacePackages` — exactly the set `fetchAll` will later receive.

### Shared ticker

`internal/orchestrator/pipeline.go`:

```go
// fetchTicker dedups IncFetch by package name so a package fetched
// speculatively by prefetch and re-verified warm by fetchAll ticks
// exactly once. Safe for concurrent use from prefetch workers and
// fetchAll workers.
type fetchTicker struct {
    mu   sync.Mutex
    seen map[string]struct{}
    prog Progress
}

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

Label format `name + " " + version` matches `fetchAll`'s current `IncFetch(p.Name + " " + p.Version)`.

### Pipeline wire-in

In `runFullPipeline`, prefetch start becomes phase-opening. **Ordering constraint:** `startPrefetch` dispatches workers from a background goroutine immediately, so a fast worker could tick before the caller regains control. `BeginFetch(N)` must therefore fire *before* `startPrefetch` is called — otherwise `beginPhase` would zero a counter that already ticked. The clean shape: `maybeStartPrefetch` (which already decodes the lock and is the only place that knows whether prefetch will start) constructs the ticker, fires `BeginFetch(N)` on the pipeline's Progress, and only then calls `startPrefetch`:

```go
// inside maybeStartPrefetch, after all skip-condition checks pass:
pkgs := prefetchPackages(lf, !opts.NoDev)   // non-workspace, dev-filtered
if len(pkgs) == 0 {
    return &Prefetcher{}, nil
}
ticker := newFetchTicker(progressOrNoop(opts.Progress))
ticker.prog.BeginFetch(len(pkgs))
return startPrefetch(ctx, pkgs, opts.Fetcher, workerCount(opts.Workers), ticker.tick), ticker
```

`maybeStartPrefetch` returns the ticker (nil when prefetch was skipped); the pipeline passes it to `fetchAll`. Moving the package-list construction out of `startPrefetch` and into the caller keeps `startPrefetch`'s contract simple: dispatch exactly these packages, tick on success.

`fetchAll` gains phase-awareness:

```go
func fetchAll(ctx, pkgs, f, workers, prog Progress, ticker *fetchTicker) (map[string]string, error) {
    ...
    if ticker == nil {
        prog.BeginFetch(len(pkgs))
        // per-package: prog.IncFetch(p.Name + " " + p.Version)
    }
    defer prog.EndFetch()
    // per-package when ticker != nil: ticker.tick(p.Name, p.Version)
}
```

- `ticker == nil` (no-lockfile install, update, `--no-prefetch`, `--no-network`, corrupt lock): byte-identical behavior to today.
- `ticker != nil`: skip `BeginFetch` (the pipeline already opened the phase with the same count), route increments through the ticker.

`EndFetch` remains `fetchAll`'s deferred call in both modes — exactly one summary line. `ttyProgress.endPhase` self-guards on an empty phase, so no path can print a stray summary.

### Renderer

No changes. `beginPhase("fetching", N)` renders the existing determinate bar. During prefetch the line updates as speculative downloads complete; once `fetchAll` sweeps warm hits the counter is already at (or near) N. The window where cur == total with the phase still open renders fine in the existing draw path (cur is clamped to total).

### Error and cancellation behavior

- Prefetch failures: swallowed as today; the package ticks when `fetchAll` fetches it. If `fetchAll` also fails, the install errors as today — the phase's deferred `EndFetch` prints the count reached, unchanged from current behavior on fetch errors.
- Context cancellation during prefetch: workers stop, ticks stop; `fetchAll` (or the pipeline) surfaces the cancellation. No progress-specific handling.
- `Progress == nil`: the ticker is constructed around `progressOrNoop(opts.Progress)`, so both the `BeginFetch` fired inside `maybeStartPrefetch` and every tick route through the noop implementation — no nil checks at tick sites.

## Tests

- **Unit — prefetch fires `onFetched` per success.** Fake fetcher where one package errors: `onFetched` fires for the successes only, never for the failure; nil callback is safe. Workspace entries are absent from the dispatch (assert via fetcher call count).
- **Unit — `fetchTicker` dedups by name.** Concurrent ticks for the same name from multiple goroutines produce exactly one `IncFetch` (run under `-race`).
- **Integration — trusted-lockfile install shows unified fetch progress.** Seed a lockfile, cold store, recording progress fake: assert exactly one `BeginFetch(N)` (fired before any fetch tick), exactly N `IncFetch` events with no duplicate package names (prefetch + fetchAll double-fetch must not double-tick), exactly one `EndFetch`.
- **Integration — `--no-prefetch` path unchanged.** Same setup with `NoPrefetch: true`: today's exact event sequence (`BeginFetch` from `fetchAll`, N incs, `EndFetch`).
- **Existing tests.** All current prefetch tests pass nil callbacks; all current `fetchAll` call sites updated mechanically.

## Related follow-ups (not this pass)

- **Byte-rate display** (`fetching 17/42  3.2 MB/s`) if package-count granularity proves too coarse for slow links.
- **`gomposer cache:clear` command** — surfaced while debugging this gap; Composer-parity cache management is an easy standalone feature.
