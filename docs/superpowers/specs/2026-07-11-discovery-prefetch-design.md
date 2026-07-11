# Discovery-driven metadata prefetch (Scope C)

## Motivation

The metadata prefetch shipped in June covers two shapes:

- **A. Root-only:** fires `Lookup` for every direct entry in `manifest.Require` + `RequireDev` before the solver starts.
- **B. Lock-driven:** if a lock exists, prefetches every package it lists.

Both were called out at ship time as necessary-but-insufficient. On the two cases that matter most, they leave the resolve phase silent:

- **First install (no lock, deep tree):** we prefetch the 5–20 direct requires; the resolver still walks transitive `require` maps serially. For Appwrite's 76 packages the observed wall-clock hit `resolve = 6.7 s` cold.
- **`gomposer update` (lock ignored, deep tree):** same shape — lock-driven prefetch is skipped because it's a fresh solve; only the direct-require warming survives.

This spec closes the loop with **Scope C — discovery-driven prefetch**: as the solver commits a version, dispatch that version's transitive `require` map to the same pool so the next iteration hits warm cache. Matches Composer 2's `curl_multi` approach.

## Scope

Ship exactly:

1. New optional callback `OnVersionDecided func(pkgName string, requires map[string]string)` on `resolver.Input`, invoked from `decide()` right after a version is committed to the partial solution.
2. New method `MetadataPrefetcher.Add(names []string)` — enqueues additional names into the running pool. Idempotent (dedupes against a `seen` map), platform-req and workspace-name filtered, safe under concurrent callers.
3. Orchestrator wires the two together: `resolver.Input.OnVersionDecided = func(_ string, reqs map[string]string) { mprefetch.Add(namesOf(reqs)) }`.

## Non-goals

- Prefetching every version in the resolver's candidate list. That's fan-out at the wrong step — the solver picks one, and the other candidates rarely become relevant. Only *decided* versions trigger.
- Priority queue / older-first / newer-first ordering. FIFO is fine; the errgroup limit gates concurrency.
- Per-package retry semantics. The resolver's own on-demand `Lookup` is still authoritative; prefetch errors stay counted-and-ignored as they are today.
- Prefetching for VCS-sourced packages. Only Packagist-shaped registries benefit from metadata warming.

## Design

### 1. Resolver callback (`internal/resolver/solve.go`)

Add to the existing `Input` struct:

```go
// OnVersionDecided, if non-nil, is invoked from decide() every time a
// version is committed to the partial solution. The orchestrator uses
// this to dispatch the just-decided version's transitive requires to a
// background metadata prefetch pool.
//
// The callback fires unconditionally (no first-seen dedup here — the
// orchestrator's pool is responsible for that). Called synchronously
// from decide(); implementations must return quickly. If the callback
// panics, decide() itself will panic; keep the implementation trivial.
OnVersionDecided func(pkgName string, requires map[string]string)
```

Invocation site: `internal/resolver/decide.go`, in the branch where a version is added to the partial solution. Look for `ps.Decide(...)` (or equivalent) and add:

```go
if in.OnVersionDecided != nil {
    in.OnVersionDecided(name, chosen.Record.Require)
}
```

Where `chosen` is the `listedVersion` being committed. Threading `in` through decide requires a small signature change — `decide()` currently takes the `versionLister` but not the `Input`. Two options:

- (a) Attach the callback to the `versionLister` at construction (`newVersionLister` takes a hook).
- (b) Pass the callback down through `decide()` explicitly.

(a) is cleaner. Plan uses (a).

### 2. Prefetcher `Add` (`internal/orchestrator/metadata_prefetch.go`)

Add:

```go
// Add enqueues additional names into the running pool. Safe to call from
// multiple goroutines. Names that fail the workspace / platform filter or
// were already enqueued are silently dropped. Fire-and-forget: returns
// immediately.
func (p *MetadataPrefetcher) Add(names []string) {
    if p == nil || p.g == nil {
        return
    }
    for _, name := range names {
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

Shape changes to `MetadataPrefetcher`:

- Store `g *errgroup.Group`, `ctx context.Context`, `source registry.SourceLookup` on the struct (currently these live in closure state — hoist them so `Add` can reuse the pool).
- Add `seenMu sync.Mutex` and `seen map[string]struct{}`.
- Seed `seen` at construction with the initial warm set (A+B names) so C doesn't re-enqueue what A/B already warmed.

Workspace filtering piggybacks on the existing `wsNames` filter in `collectMetadataPrefetchNames`; extract it into a struct field or a closure the constructor holds and `Add` calls before the platform check.

### 3. Orchestrator wire-in (`internal/orchestrator/pipeline.go`)

In `runFullPipeline`, after `mprefetch := maybeStartMetadataPrefetch(...)`, build the callback and pass it into the resolver via `resolver.Input`:

```go
resolveIn := resolver.Input{
    // existing fields...
    OnVersionDecided: func(_ string, reqs map[string]string) {
        names := make([]string, 0, len(reqs))
        for name := range reqs {
            names = append(names, name)
        }
        mprefetch.Add(names)
    },
}
```

The wire-in is exactly one field on the existing `Input`; nothing else in the pipeline changes.

### 4. Cancel semantics

Existing `Cancel()` remains correct: cancelling the pool's context stops both the initial workers and any workers spawned by `Add`. On a resolution-cache hit `Cancel` fires; `Add` may still be called by the resolver in the interim on the non-cache path — those `g.Go` closures observe the cancelled ctx and exit immediately.

### 5. Verbose

The verbose block already reports `metadata-prefetch N warmed`. Discovery-driven contributions land in the same counter naturally. No copy change.

## Tests

- **Unit — `Add` under contention.** Spawn 5 goroutines calling `Add` on overlapping name sets; assert each unique name reaches `Lookup` exactly once. Assert workspace names + platform names are filtered.
- **Unit — `Add` respects prior warm set.** Seed prefetcher with names A, B; call `Add({A, C})`; assert only C reaches `Lookup`.
- **Unit — resolver callback fires on every commit.** Fake source with N packages; assert the callback was invoked N times with the resolved names and their non-nil `require` maps.
- **Integration — fresh-install wall-time.** Slow-fake source with 40ms `Lookup` per package, N=10 packages with transitive deps forming a chain. With discovery-driven prefetch ON, total install strictly faster than with `--no-metadata-prefetch`. Existing wall-time-reduction test's shape.
- **Integration — cache-hit still cancels fast.** Re-use the existing `TestMetadataPrefetchCancelsOnCacheHit`. Must still pass: cache-hit + Cancel drains the pool immediately even if `Add` was called mid-flight.

## Related follow-ups (not in this spec)

- **Concurrency-safe parsedcache dedup.** Currently if two goroutines fetch the same package concurrently, both write to the parsedcache — wasted work but correct. Belt-and-suspenders. Independent of C.
- **Prefetch progress line under `-v`.** The existing `metadata-prefetch N warmed` reports the total but not the C-specific contribution. Splitting could help debug.
- **Cross-workspace resolver progress.** Distinct from prefetch; a UX improvement that streams which package the solver is currently working on.
