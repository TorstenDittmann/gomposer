# In-flight metadata request dedup

## Motivation

The discovery-driven metadata prefetch that just merged (2026-07-11) has a known ceiling: on a serial-chain dependency graph, its speedup is theoretically zero because the pool's `Lookup(x)` and the resolver's synchronous `Lookup(x)` fire almost simultaneously — both miss the parsed-cache, both do the full HTTP round-trip, both parse and store. The wasted work is bounded by wall-clock (both callers arrive within ~microseconds) but perfectly symmetric.

The final review of the discovery-prefetch PR called this out explicitly: "Without in-flight dedup, a chain (a→b→c→d→e) genuinely cannot show a win." The test suite works around this by reshaping the fixture to a fan-out; real projects sit somewhere between chain and fan-out.

Adding in-flight request dedup at the packagist client layer lets the second caller wait for the first's result instead of duplicating the work. This amplifies discovery-driven prefetch from "wide-tree win" to "wide + chain win", and matches the coalescing Composer 2 gets from `curl_multi`.

## Scope

Single change, well-scoped:

- `internal/registry/packagist/packagist.go` — `Client.Lookup(ctx, name)` wraps its body in `singleflight.Group.Do(name, ...)`. The existing body moves verbatim into a new unexported `lookupUncoalesced`.
- Add `golang.org/x/sync/singleflight` to the module dependencies (same package tree we already use for `errgroup`).

That's it. No changes to the resolver, orchestrator, cache layers, or CLI.

## Non-goals

- **`DoChan` + per-caller-ctx select.** Simpler `Do` is enough for the common case (see cancellation semantics below). Can be added later if leader-cancellation becomes a real problem.
- **Deduping VCS-registry lookups.** Composer + git semantics differ; VCS clones are already expensive and warrant their own analysis.
- **Deduping the two HTTP fetches inside a single `Lookup` call** (`/p2/<name>.json` + `/p2/<name>~dev.json`). They happen serially in the current code; parallelization is orthogonal.
- **Cross-process dedup.** Multiple gomposer processes hitting the same package still race — the parsedcache disk layer catches them on the *next* run, and process-level in-flight dedup isn't part of `singleflight`'s contract.

## Design

```go
import "golang.org/x/sync/singleflight"

type Client struct {
    // existing fields...
    sf singleflight.Group
}

func (c *Client) Lookup(ctx context.Context, name string) (*registry.PackageMetadata, error) {
    result, err, _ := c.sf.Do(name, func() (any, error) {
        return c.lookupUncoalesced(ctx, name)
    })
    if err != nil {
        return nil, err
    }
    return result.(*registry.PackageMetadata), nil
}
```

`lookupUncoalesced` is the current body of `Lookup` — untouched, just renamed. The singleflight key is the package name (`"vendor/pkg"`), matching what the resolver and prefetch pool both use.

### Cancellation semantics

`singleflight.Group.Do` groups callers by key. The first caller (leader) runs the callback with its own context; subsequent callers (followers) wait for the leader's result. If the leader's context is cancelled, the callback's HTTP request fails, and every follower sees the same error.

In our pipeline this is safe:

- The **resolver's ctx** is inherited from `runFullPipeline`'s ctx, which is only cancelled when the whole run exits.
- The **metadata prefetcher's ctx** is a child of `runFullPipeline`'s ctx via `context.WithCancel`. It's cancelled on resolution-cache hit — but that happens *before* `resolveFunc` runs and *before* the discovery-driven callback fires. So on the discovery-driven path, both callers have a live ctx.

Contagion could bite if the pipeline itself cancels mid-resolve (e.g., a hard timeout). At that point both callers were about to fail anyway — the shared cancel is not worse than independent cancels. Accept the risk.

### Group lifetime

`singleflight.Group` retains its key mapping only while a call is in flight; the mapping clears as soon as the leader's callback returns. So two concurrent calls at t=0 and t=0.05s dedup; a second call at t=0.5s (after the leader completed) does not dedup — but by t=0.5s the parsed-cache is warm, so the second call hits the disk cache and returns without touching HTTP.

No explicit `sf.Forget(name)` call is needed.

### Interaction with parsed-cache

- Follower arrives during leader's work → gets leader's result. Never touches parsedcache directly on that call.
- Follower arrives after leader → parsedcache is warm → returns without touching HTTP.

Net: for any burst of concurrent same-name calls, exactly one HTTP round-trip. Serialized same-name calls after the burst hit parsedcache from cold.

## Tests

**Unit — coalescing (`internal/registry/packagist/packagist_test.go`):**

Fire two `Lookup("vendor/pkg")` goroutines against an `httptest.Server` that counts requests and blocks the first response until both goroutines are waiting. Assert:

- Both goroutines get the same non-error result.
- The test server recorded exactly one request.

**Unit — no cross-name dedup:**

Fire concurrent `Lookup("vendor/a")` and `Lookup("vendor/b")` — assert both HTTP endpoints see one request each (no false dedup between different names).

**Unit — cancellation contagion (documenting behavior):**

First caller with a cancellable ctx, cancel mid-flight, assert the second caller sees the same cancellation error. This test locks in the expected behavior; a future switch to `DoChan` semantics will need to change it.

**Integration — chain-shape wall-time in the orchestrator suite:**

Copy the *original* Task 5 test (before it was reshaped to fan-out) as `TestMetadataPrefetchWarmTransitivesOnChain`. With in-flight dedup it should now pass — the pool's `Lookup(b)` and the resolver's `Lookup(b)` coalesce into one, so total install time is dominated by `depth × per-lookup-latency` instead of `chain-length × per-lookup-latency × 2`. Expected: baseline ~480ms, prefetch ~90-100ms.

If the chain test doesn't show the expected speedup, in-flight dedup isn't actually engaging — signal that the wire-in is wrong.

## Related follow-ups (not this pass)

- **`sf.DoChan` + per-caller-ctx select.** If leader-cancellation ever becomes a real bug source.
- **Cross-tool dedup via file-lock on the parsedcache.** If parallel `gomposer install` runs against the same project become a real workflow.
- **VCS-registry dedup.** Separate design; VCS clones are lock-heavy already.
