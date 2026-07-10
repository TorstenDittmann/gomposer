# Metadata prefetch design

## Motivation

On a cold Packagist cache, the resolver's first several seconds are silent: for every package it decides to consider, it calls `SourceLookup.Lookup(ctx, name)` synchronously, which triggers a `/p2/<name>.json` HTTP round-trip. With Appwrite (~76 direct + transitive packages) this is 5–10 seconds of no visible progress, followed by rapid artifact fetch/extract once the solve completes. The existing progress UI (Stage 3 Plan 6) does not cover this phase — it starts at `BeginFetch`, which fires only for artifact zips.

Stage 3 Plan 4 already shipped an analogous optimization for artifact zips: when a lock is present, artifact downloads kick off in the background as soon as the lock is read, so the fetch phase overlaps with the resolver. Metadata is the missing symmetric case.

## Scope

Ship two flavors of metadata prefetch together:

**A. Root-only prefetch.** Every key in `manifest.Require` and (when `--no-dev` is not set) `manifest.RequireDev` is enqueued for `Lookup` before the solver starts. Trivially known upfront; small warm set (5–20 packages typically); helps the fresh-install case.

**B. Lock-driven prefetch.** If an existing `gomposer.lock` is present, every name in `Packages` and `PackagesDev` is enqueued alongside A. Deduplicated. Kills all cold-metadata latency on the dominant real-world case: re-install / lock-unchanged.

## Non-goals

- Discovery-driven prefetch (as the solver decides on a version, dispatch its transitive `require` map to the pool). Follow-up.
- Metadata prefetch for VCS registries. Cloning git is a different latency profile; only Packagist gets prefetch in this pass. The prefetcher no-ops for names it can't route to the Packagist adapter.
- Cross-run persistence beyond what the existing `httpcache` and `parsedcache` already do. No new cache tier.
- Concurrency-safe parsedcache dedup. If two goroutines race for the same package they'll both fetch and each will write its own parsedcache entry — wasted work but correct output.

## Architecture

New file `internal/orchestrator/metadata_prefetch.go`. One type + one constructor + one `Wait` method, matching the shape of the existing `Prefetcher` in `internal/orchestrator/prefetch.go`:

```go
type MetadataPrefetcher struct {
    // internal state — errgroup handle, cancel func
}

func maybeStartMetadataPrefetch(
    ctx context.Context,
    ps *pipelineState,
    opts Options,
) *MetadataPrefetcher

func (p *MetadataPrefetcher) Wait()
```

`maybeStartMetadataPrefetch` returns a non-nil pointer in every branch so callers can `Wait()` unconditionally. When prefetch is disabled or there's nothing to warm, the returned `*MetadataPrefetcher` has a no-op `Wait`.

Warm-set assembly (inside `maybeStartMetadataPrefetch`):

1. Start with an empty `set map[string]struct{}`.
2. Add every key in `ps.manifest.Require`.
3. If `!opts.NoDev`, add every key in `ps.manifest.RequireDev`.
4. If `ps.lockBytes` is non-empty, decode via `lock.Decode`; on success, add every `Packages[i].Name` and (unless `opts.NoDev`) every `PackagesDev[i].Name`.
5. Skip any name matching `platform.IsPlatformReq` — filters out `php`, `ext-*`, `lib-*` (which already excludes vendor packages with a `/` in the name after the recent hotfix). VCS-only package names are still routed through the multi-source registry; whichever child adapter recognizes the name handles the `Lookup` and non-Packagist adapters silently skip.

Kick off:

- Bounded pool via `errgroup.WithContext`, limit = `workerCount(opts.Workers)`. Matches artifact prefetch.
- Each goroutine calls `opts.RegistrySource.Lookup(ctx, name)` and discards the returned `PackageMetadata`. The purpose is warming the `httpcache` and `parsedcache` layers.
- Errors are captured but not propagated as pipeline failures. The resolver's own on-demand `Lookup` is authoritative — if the prefetch missed something, the solver's synchronous call will retry. A `--verbose` `-v` flag emits a one-line summary at the end (`metadata prefetch: 22 warmed, 0 errors`).

Wiring in `runFullPipeline` (`internal/orchestrator/pipeline.go`):

```go
// After manifest + lock parse, before resolve:
mprefetch := maybeStartMetadataPrefetch(ctx, ps, opts)
// ... existing prefetch call for artifacts ...
prefetch := maybeStartPrefetch(ctx, ps, opts, forceResolve)
// resolve, fetch, materialize as today
// ... at pipeline end:
mprefetch.Wait()
prefetch.Wait()
```

Both prefetchers run concurrently. Order of the two `Wait` calls doesn't matter — both are idempotent.

## Options + CLI

- Add `NoMetadataPrefetch bool` to `orchestrator.Options`. Zero-value means prefetch is on (opt-out).
- New CLI flag `--no-metadata-prefetch` on both `install` and `update` (persistent flag on the root command, symmetric to `--no-prefetch`). Help text: `"disable resolver-metadata prefetch (benchmarking hook)"`.
- Update the `-v` timing block: add one line `metadata-prefetch: <n> warmed in <dur>` when active.

## Tests

- **Unit — pool call count and dedup.** `internal/orchestrator/metadata_prefetch_test.go`. Fake `registry.SourceLookup` that atomically increments a per-name counter. Feed a manifest with 5 direct requires and a lock with 20 packages, 3 overlapping. Assert exactly 22 `Lookup` calls, no duplicates.
- **Unit — noop path.** When `opts.NoMetadataPrefetch` is true, the returned pointer's `Wait` completes immediately and the fake registry sees zero calls.
- **Unit — platform reqs filtered.** Manifest with `"php": ">=8.1"` and `"ext-curl": "*"` should not trigger `Lookup("php")` etc.
- **Integration — wall-time reduction.** In `internal/orchestrator/`, run the full pipeline twice against a slow-fake registry (each `Lookup` sleeps 50ms). With prefetch on, total wall-time should be at most a fraction of the without-prefetch time. Not asserting an exact ratio (flakiness), just `< 0.7×`.
- **Off-switch reachability.** Existing CLI test asserts that `--no-metadata-prefetch` reaches the orchestrator via a captured `Options`.

No changes to existing tests should be needed; the prefetch is additive and off-path from the resolver's contract.

## Related follow-ups (not in this pass)

- **Discovery-driven prefetch.** As the solver picks a version, dispatch its `require` map to the pool so the next iteration is warm.
- **Progress hook for the resolver phase.** The Progress interface (Stage 3 Plan 6) currently starts at `BeginFetch`. Adding `BeginResolve`/`IncResolve` would remove the "silent first few seconds" without any perf work — orthogonal to prefetch but complementary.
- **Concurrency-safe parsedcache dedup.** A once-per-key guard so racing goroutines share one HTTP call. Purely an efficiency win.
