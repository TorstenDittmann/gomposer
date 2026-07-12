# In-flight Metadata Request Dedup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Coalesce concurrent same-name metadata Lookups on the Packagist client so the discovery-driven prefetch pool and the resolver's synchronous Lookup share one HTTP round-trip instead of doubling them.

**Architecture:** `packagist.Client` gains a `singleflight.Group` field. `Lookup(ctx, name)` wraps its body in `sf.Do(name, ...)`. Existing body moves verbatim into a new unexported `lookupUncoalesced`. No changes to the resolver, orchestrator, cache layers, or CLI.

**Tech Stack:** Go 1.25, new stdlib-adjacent dep `golang.org/x/sync/singleflight` (same package tree we already use for `errgroup`).

## Global Constraints

- **Key granularity:** package name (`"vendor/pkg"`) — matches what the resolver and prefetch pool both use.
- **`Do` (not `DoChan`):** simpler; leader-cancellation contagion is accepted for the POC. Documented in the spec.
- **No `sf.Forget`:** the `singleflight.Group` clears keys as soon as the leader's callback returns, which is exactly the semantics we want. Followers arriving after the leader completes hit the disk cache instead.
- **Zero API break:** `Lookup(ctx, name) (*registry.PackageMetadata, error)` signature unchanged. Every caller works as-is.
- **`lookupUncoalesced` is unexported.** Only `Lookup` is public; test seams that need to bypass singleflight can use the internal function.

---

## File structure

| Path | Responsibility |
|------|---------------|
| `go.mod`, `go.sum` | Add `golang.org/x/sync/singleflight` (via `golang.org/x/sync` which is already required). |
| `internal/registry/packagist/packagist.go` | Rename existing `Lookup` body → `lookupUncoalesced`; new `Lookup` wraps with `sf.Do`. |
| `internal/registry/packagist/packagist_test.go` | Coalescing test, no-cross-name-dedup test, cancellation contagion test. |
| `internal/orchestrator/pipeline_test.go` | Add `TestMetadataPrefetchWarmTransitivesOnChain` alongside the existing fan-out test. |

---

## Task 1: `Client.Lookup` singleflight wrap + unit tests

**Files:**
- Modify: `go.mod` (only if `golang.org/x/sync` is already required — verify first)
- Modify: `internal/registry/packagist/packagist.go`
- Modify: `internal/registry/packagist/packagist_test.go`

**Interfaces:**
- `Client.Lookup(ctx context.Context, name string) (*registry.PackageMetadata, error)` — signature unchanged.
- New unexported `Client.lookupUncoalesced(ctx context.Context, name string) (*registry.PackageMetadata, error)` — contains the existing `Lookup` body verbatim.
- New unexported field on `Client`: `sf singleflight.Group` (zero-value, no constructor tweak needed).

- [ ] **Step 1: Verify the dep**

Run: `grep "golang.org/x/sync" go.mod`

Expected: line already present (we use `errgroup` from the same module). If not present, `go get golang.org/x/sync@latest` and commit go.mod/go.sum in Task 1's final commit.

- [ ] **Step 2: Write the failing coalescing test**

Append to `internal/registry/packagist/packagist_test.go`:

```go
func TestClientLookupCoalescesConcurrentSameName(t *testing.T) {
	// Two goroutines fire Lookup("vendor/pkg") at nearly the same instant.
	// Assert only one HTTP round-trip fires against the test server.
	var reqCount atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount.Add(1)
		// Block the first response until both goroutines are waiting on
		// singleflight. The test's Wait/release protocol ensures both callers
		// enter Lookup before either sees a response.
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"packages":{"vendor/pkg":[]}}`))
	}))
	defer srv.Close()

	c, err := New(Config{BaseURL: srv.URL, CacheDir: t.TempDir(), HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}

	// Launch both goroutines and give them a moment to enter Lookup.
	type result struct {
		md  *registry.PackageMetadata
		err error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			md, err := c.Lookup(context.Background(), "vendor/pkg")
			results <- result{md, err}
		}()
	}
	// Sleep briefly so both goroutines land inside sf.Do before we let the
	// server respond. A tiny sleep is enough — this is a scheduling nudge,
	// not a timing assertion.
	time.Sleep(50 * time.Millisecond)
	close(release)

	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			t.Errorf("Lookup #%d err: %v", i, r.err)
		}
	}
	if got := reqCount.Load(); got != 1 {
		t.Errorf("HTTP request count = %d, want 1 (singleflight not coalescing)", got)
	}
}
```

- [ ] **Step 3: Write the no-cross-name-dedup test**

```go
func TestClientLookupDoesNotCoalesceDifferentNames(t *testing.T) {
	var reqCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// Return an empty response for whichever name was asked; the counter
		// is the only thing we care about.
		_, _ = w.Write([]byte(`{"packages":{}}`))
	}))
	defer srv.Close()

	c, err := New(Config{BaseURL: srv.URL, CacheDir: t.TempDir(), HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for _, name := range []string{"vendor/a", "vendor/b"} {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			_, _ = c.Lookup(context.Background(), n)
		}(name)
	}
	wg.Wait()

	// Two distinct names → two /p2/*.json fetches minimum. The client also
	// asks for /p2/<name>~dev.json which returns 404 or empty; count against
	// a floor so we don't over-fit to internal client details.
	if got := reqCount.Load(); got < 2 {
		t.Errorf("HTTP request count = %d, want at least 2 (different names should not coalesce)", got)
	}
}
```

- [ ] **Step 4: Write the cancellation-contagion doc test**

```go
// TestClientLookupCancellationIsContagious pins the documented behavior of
// singleflight.Group.Do — the leader's context governs the shared call, so
// if the leader cancels, every follower observes the same error. If this
// test starts failing, someone switched to DoChan semantics and should
// update the spec.
func TestClientLookupCancellationIsContagious(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"packages":{"vendor/pkg":[]}}`))
		case <-r.Context().Done():
			// Server observes the client's cancelled request.
			return
		}
	}))
	defer srv.Close()

	c, err := New(Config{BaseURL: srv.URL, CacheDir: t.TempDir(), HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}

	// Leader with a cancellable ctx; follower with a live ctx.
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	followerCtx := context.Background()

	leaderErrCh := make(chan error, 1)
	followerErrCh := make(chan error, 1)

	go func() {
		_, err := c.Lookup(leaderCtx, "vendor/pkg")
		leaderErrCh <- err
	}()
	// Wait a beat for the leader to land inside sf.Do.
	time.Sleep(20 * time.Millisecond)
	go func() {
		_, err := c.Lookup(followerCtx, "vendor/pkg")
		followerErrCh <- err
	}()
	time.Sleep(20 * time.Millisecond)

	cancelLeader()

	// Both must observe an error. Exact error shape depends on how the HTTP
	// stack propagates cancellation — we just require non-nil on both.
	if err := <-leaderErrCh; err == nil {
		t.Errorf("leader Lookup returned nil error despite cancellation")
	}
	if err := <-followerErrCh; err == nil {
		t.Errorf("follower Lookup returned nil error — contagion documented in spec is broken")
	}

	close(release)
}
```

- [ ] **Step 5: Verify RED**

Run: `go test ./internal/registry/packagist/ -run 'TestClientLookup(Coalesces|DoesNot|CancellationIs)' -v`

Expected: compile succeeds (`singleflight` not yet imported in production), but tests fail — coalescing test sees 2 requests, cancellation test may pass by luck or fail depending on how the current code handles concurrent cancellation.

Actually, more precisely: without singleflight, the coalescing test's `reqCount` is 2 (both goroutines hit the server), which fails the `want 1` assertion. Confirm this is the RED state.

- [ ] **Step 6: Implement the singleflight wrap**

In `internal/registry/packagist/packagist.go`:

Add import:

```go
"golang.org/x/sync/singleflight"
```

Add to `Client` struct:

```go
type Client struct {
    // existing fields...
    sf singleflight.Group
}
```

Rename the current `Lookup` function body to `lookupUncoalesced`:

```go
// lookupUncoalesced is the raw Lookup body without singleflight
// wrapping. Callers should use Lookup which coalesces concurrent
// same-name requests through c.sf.
func (c *Client) lookupUncoalesced(ctx context.Context, name string) (*registry.PackageMetadata, error) {
    // ...the existing Lookup body, unchanged...
}
```

Add the new `Lookup`:

```go
// Lookup fetches metadata for name. Concurrent same-name calls are
// coalesced via singleflight: the first caller's underlying HTTP fetch
// serves every waiting caller. Callers with different names run
// independently. Cancellation is contagious — if the leader's context
// cancels mid-flight, every follower receives the same error. See
// docs/superpowers/specs/2026-07-11-inflight-dedup-design.md.
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

- [ ] **Step 7: Verify GREEN**

Run: `go test ./internal/registry/packagist/ -run 'TestClientLookup' -v -race -count=3`

Expected: all pass, including the three new tests, across three iterations under `-race`.

- [ ] **Step 8: Full-suite check**

Run: `go test ./...`

Expected: green. Existing packagist tests (`TestLookupHappyPath`, `TestLookupNotFound`, etc.) still pass unchanged — the singleflight wrap is transparent for single-caller paths.

- [ ] **Step 9: Commit**

```bash
git add internal/registry/packagist/packagist.go internal/registry/packagist/packagist_test.go go.mod go.sum
git commit -m "feat(packagist): coalesce in-flight Lookups via singleflight

Client.Lookup now wraps its body in singleflight.Group.Do keyed by
package name. Concurrent same-name callers share one HTTP round-trip;
the follower waits for the leader's result. Callers with different
names still run independently. Cancellation is contagious (leader's
ctx governs the shared call) — documented in the spec and locked in
by a test."
```

---

## Task 2: Chain-shape wall-time integration test

**Files:**
- Modify: `internal/orchestrator/pipeline_test.go`

**Interfaces:** none new.

The discovery-driven prefetch's original wall-time test was reshaped from a chain to a fan-out because a chain couldn't demonstrate the speedup without in-flight dedup. With Task 1's dedup landed, the chain shape should now show a real speedup. This task adds it as a companion regression guard.

- [ ] **Step 1: Write the failing test**

Append to `internal/orchestrator/pipeline_test.go` next to `TestMetadataPrefetchWarmTransitivesOnFreshInstall`:

```go
// TestMetadataPrefetchWarmTransitivesOnChain asserts the interaction of
// discovery-driven prefetch (Scope C) with in-flight request dedup at the
// Packagist client layer. On a serial chain a→b→c→d→e, without dedup the
// pool's Lookup(b) and the resolver's Lookup(b) both hit the source (zero
// speedup). With dedup, they coalesce; the pool's request stays in flight
// while the resolver's Lookup(b) joins as a follower and gets the same
// result. Total install time drops from ~6×delay to ~2×delay.
func TestMetadataPrefetchWarmTransitivesOnChain(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-sensitive; skipping under -short")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	// Chain: root → a → b → c → d → e. Each Lookup takes 80ms.
	// Baseline (no metadata prefetch): serial walk = 6 × 80 ≈ 480ms.
	// With prefetch + dedup: the pool warms b/c/d/e as soon as each parent
	// is decided, and the resolver's own Lookups join as followers. Net
	// wall time is dominated by the chain depth, not chain length ×
	// per-lookup latency. Expect < 480ms × 0.6.
	chain := map[string][]registry.PackageVersion{
		"acme/root": {testlookup.Pkg("acme/root", "1.0.0", map[string]string{"acme/a": "^1.0"})},
		"acme/a":    {testlookup.Pkg("acme/a", "1.0.0", map[string]string{"acme/b": "^1.0"})},
		"acme/b":    {testlookup.Pkg("acme/b", "1.0.0", map[string]string{"acme/c": "^1.0"})},
		"acme/c":    {testlookup.Pkg("acme/c", "1.0.0", map[string]string{"acme/d": "^1.0"})},
		"acme/d":    {testlookup.Pkg("acme/d", "1.0.0", map[string]string{"acme/e": "^1.0"})},
		"acme/e":    {testlookup.Pkg("acme/e", "1.0.0", nil)},
	}

	baseOpts := func() Options {
		return Options{
			ProjectDir: writeManifest(t, &manifest.Manifest{
				Name:    "acme/app",
				Require: map[string]string{"acme/root": "^1.0"},
			}),
			Source:    &sleepySourceLookup{delay: 80 * time.Millisecond, versions: chain},
			NoPrefetch: true,
		}
	}

	base := baseOpts()
	base.NoMetadataPrefetch = true
	tBaseline := timeInstall(t, base)

	on := baseOpts()
	on.NoMetadataPrefetch = false
	tPrefetch := timeInstall(t, on)

	// Threshold: chain shape's speedup is smaller than fan-out's (fan-out
	// dispatches N siblings in one shot; chain overlaps only pool-vs-
	// resolver for each hop). 40% target is comfortably above the
	// theoretical floor.
	if tPrefetch*10 >= tBaseline*6 {
		t.Errorf("chain-shape prefetch didn't hit expected speedup: baseline=%v prefetch=%v (want prefetch < 60%% of baseline)", tBaseline, tPrefetch)
	}
	if tPrefetch >= tBaseline {
		t.Errorf("chain-shape prefetch slower than baseline: baseline=%v prefetch=%v", tBaseline, tPrefetch)
	}
}
```

**Important:** `sleepySourceLookup` is a fake at the `registry.SourceLookup` level — it doesn't use singleflight because it's not the Packagist client. The dedup happens inside `packagist.Client`. For this test to demonstrate the win, `sleepySourceLookup` must go through the Packagist client…

**Except that's not how the orchestrator wires it.** `opts.Source` is a direct `registry.SourceLookup`; the resolver calls it directly, and so does the metadata prefetch pool. Both callers hit the same `sleepySourceLookup`, which has no singleflight.

So this test as written **still** cannot demonstrate the win — the singleflight lives in `packagist.Client`, not in an arbitrary `SourceLookup`.

Two options:

- **(a)** Extend `sleepySourceLookup` with its own inline singleflight so the fake models the real behavior. Clean and localized.
- **(b)** Swap the test to run against a real `packagist.Client` with a fake HTTP server. Closer to reality but heavier setup.

**Recommendation:** (a). Add a `singleflight.Group` to `sleepySourceLookup` and wrap its `Lookup` the same way `packagist.Client.Lookup` does. Update the doc comment on `sleepySourceLookup` to name this fact.

- [ ] **Step 2: Add singleflight to `sleepySourceLookup`**

Find `sleepySourceLookup` in `pipeline_test.go` and add:

```go
type sleepySourceLookup struct {
    delay    time.Duration
    versions map[string][]registry.PackageVersion
    sf       singleflight.Group // models packagist.Client's in-flight dedup
}
```

Wrap the existing `Lookup`:

```go
func (s *sleepySourceLookup) Lookup(ctx context.Context, name string) (*registry.PackageMetadata, error) {
    result, err, _ := s.sf.Do(name, func() (any, error) {
        return s.lookupUncoalesced(ctx, name)
    })
    if err != nil {
        return nil, err
    }
    return result.(*registry.PackageMetadata), nil
}

func (s *sleepySourceLookup) lookupUncoalesced(ctx context.Context, name string) (*registry.PackageMetadata, error) {
    // ...the existing Lookup body...
}
```

If the fake already has an internal cache map (for chain-shape prior tests), the singleflight sits *around* it — Load-miss → sf.Do → sleep → Store.

- [ ] **Step 3: Verify RED then GREEN**

Run: `go test ./internal/orchestrator/ -run TestMetadataPrefetchWarmTransitivesOnChain -v -count=3`

Expected: after Step 2, the test passes deterministically.

If the assertion fails with `prefetch too close to baseline`, the wire-in is wrong — inspect that `s.sf` is not zero-valued at Lookup time (it's a value field, so it should be fine).

- [ ] **Step 4: Full-suite check**

Run: `go test ./... -race`

Expected: green.

- [ ] **Step 5: Commit**

```bash
git add internal/orchestrator/pipeline_test.go
git commit -m "test(orchestrator): chain-shape wall-time asserts in-flight dedup

The abandoned chain-shape test from the discovery-prefetch branch
(reshaped to fan-out because it couldn't show a win) now works —
in-flight dedup at the Packagist client layer coalesces the pool's
Lookup(x) and the resolver's Lookup(x). Companion regression guard
for the fan-out test."
```

---

## In-flight metadata dedup: acceptance check

After all tasks:

- `go test ./... -race` green.
- Coalescing unit test: two concurrent same-name Lookups against a slow server produce exactly one HTTP request.
- No-cross-name test: different names still fire independently.
- Cancellation contagion test: leader cancellation propagates to follower (documented behavior).
- Chain-shape integration test: baseline ~480ms, prefetch < 60% of baseline (~290ms or less).
- Fan-out integration test (existing, `TestMetadataPrefetchWarmTransitivesOnFreshInstall`): still passes with the same margins (dedup doesn't hurt the fan-out shape).
- On a real Appwrite-shaped install (`gomposer install -v` from a wiped cache), `resolve` phase wall time shows a measurable drop compared to `--no-metadata-prefetch`; the drop is larger than pre-dedup.

If any of these fails, fix forward before declaring the plan done.
