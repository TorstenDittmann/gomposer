# Resolve-phase Progress Hook Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend the Stage 3 `Progress` interface with a resolve triad symmetric to fetch/extract, and wire the resolver's per-Lookup work to `IncResolve` so cold-cache installs no longer sit through silent seconds.

**Architecture:** New `BeginResolve` / `IncResolve` / `EndResolve` on the `Progress` interface. `resolver.Input` gains an optional `OnLookup func(pkgName string)` callback fired synchronously from `versionLister.versions()` before each real `src.Lookup`. The orchestrator builds a closure that guards the first `Inc` behind a `sync.Once` so `Begin` only fires when work actually happens (cache-hit runs stay silent). `ttyProgress` renders `resolving <cur>  <label>` with no denominator when the hint is 0.

**Tech Stack:** Go 1.25, standard library. No new dependencies.

## Global Constraints

- **Interface additions:** `BeginResolve(hint int)`, `IncResolve(name string)`, `EndResolve()`. Mirror `BeginFetch` / `IncFetch` / `EndFetch` signatures.
- **`hint == 0`** means "unknown total" — `ttyProgress` renders count only, no denominator or progress bar.
- **`OnLookup` fires only on network-path lookups.** Cache-hit reads inside `versionLister` short-circuit before the callback point, so ticks match user-visible work.
- **`sync.Once` bracket.** The first `IncResolve` also fires `BeginResolve`. `EndResolve` fires unconditionally after `resolveOrCache` returns, but is a no-op on `ttyProgress` when the phase state is empty (existing `endPhase` guard).
- **Metadata prefetch pool bypasses `OnLookup`.** The pool goes directly through `opts.Source.Lookup` with no `Input`, so it can't accidentally tick the progress. Verified.
- **Nil `opts.Progress` = current behavior.** Existing tests pass unchanged; new fields default to nil.

## File structure

| Path | Responsibility |
|------|---------------|
| `internal/cli/progress.go` | Add 3 methods to `Progress`; add no-ops to `noopProgress`; add resolving phase to `ttyProgress` including the hint-0 render path. |
| `internal/cli/progress_test.go` | `TestTTYProgressEmitsResolveLine`, `TestNoopProgressIsSilentIncludesResolve`. |
| `internal/resolver/solve.go` | Add `OnLookup func(pkgName string)` to `Input`. |
| `internal/resolver/versions.go` | `versionLister.onLookup` field; invoke before `vl.src.Lookup`. |
| `internal/resolver/versions_test.go` | Assert callback fires once per unique lookup, not on cache re-read. |
| `internal/orchestrator/pipeline.go` | Build the `sync.Once`-guarded closure; wire to `resolveFunc`; call `EndResolve` after `resolveOrCache`. |
| `internal/orchestrator/orchestrator.go` | Add the 3 new methods to the internal `Progress` interface copy (currently mirrors CLI's shape). |
| `internal/orchestrator/pipeline_test.go` | Recording `Progress` fake; assert `IncResolve` fires on fresh install and does NOT fire on resolution-cache-hit re-run. |

---

## Task 1: Progress interface + noop + ttyProgress rendering

**Files:**
- Modify: `internal/cli/progress.go`
- Modify: `internal/cli/progress_test.go`
- Modify: `internal/orchestrator/orchestrator.go` (the internal `Progress` interface mirror — see brief errata below)

**Interfaces:**
- Consumes: nothing new.
- Produces: 3 new methods on `Progress`. All existing signatures unchanged.

- [ ] **Step 1: Locate the internal Progress mirror**

`internal/orchestrator/orchestrator.go` defines a `Progress` interface (a local copy of `cli.Progress`, kept to avoid an `orchestrator → cli` import cycle). It's at ~line 43. Adding methods to `cli.Progress` without also adding them to the orchestrator's mirror will break compilation — bookkeep both together.

Grep to confirm:

```bash
grep -n "type Progress interface\|BeginFetch\|BeginExtract" internal/orchestrator/orchestrator.go
```

- [ ] **Step 2: Write the failing tests**

Append to `internal/cli/progress_test.go`:

```go
func TestTTYProgressEmitsResolveLine(t *testing.T) {
	var buf bytes.Buffer
	p := newTTYProgress(&buf)
	p.BeginResolve(0)
	p.IncResolve("psr/log")
	p.IncResolve("monolog/monolog")
	p.EndResolve()

	out := buf.String()
	if !strings.Contains(out, "\r\x1b[K") {
		t.Errorf("expected line-clear escape in output, got %q", out)
	}
	if !strings.Contains(out, "resolving") {
		t.Errorf("expected phase label \"resolving\", got %q", out)
	}
	if !strings.Contains(out, "monolog/monolog") {
		t.Errorf("expected most recent package label, got %q", out)
	}
	if !strings.Contains(out, "resolved 2 packages") {
		t.Errorf("expected phase summary, got %q", out)
	}
	// Hint=0 → no denominator, no bar. Absence check:
	if strings.Contains(out, "/") && !strings.Contains(out, "psr/log") && !strings.Contains(out, "monolog/monolog") {
		t.Errorf("unexpected denominator/bar for hint=0, got %q", out)
	}
}

func TestNoopProgressIsSilentOnResolve(t *testing.T) {
	var buf bytes.Buffer
	p := newNoopProgress(&buf)
	p.BeginResolve(0)
	p.IncResolve("psr/log")
	p.EndResolve()
	if buf.Len() != 0 {
		t.Errorf("noopProgress should write nothing on resolve, got %q", buf.String())
	}
}
```

Note: the throttled redraw in `ttyProgress` may not emit both `Inc` calls' output — the 50ms throttle collapses back-to-back Incs to one redraw. That's fine — the test asserts the *most recent* label appears, which is `monolog/monolog`.

- [ ] **Step 3: Verify RED**

Run: `go test ./internal/cli/ -run 'TestTTYProgressEmitsResolveLine|TestNoopProgressIsSilentOnResolve' -v`

Expected: compile errors on `BeginResolve` / `IncResolve` / `EndResolve`.

- [ ] **Step 4: Extend the `Progress` interface**

In `internal/cli/progress.go`:

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

Also update `internal/orchestrator/orchestrator.go`'s local `Progress` interface with the same three methods, in the same order. Comment on the orchestrator's copy already notes it must stay in sync with `cli.Progress`.

- [ ] **Step 5: Implement on `noopProgress`**

```go
func (p *noopProgress) BeginResolve(int)   {}
func (p *noopProgress) IncResolve(string)  {}
func (p *noopProgress) EndResolve()        {}
```

- [ ] **Step 6: Implement on `ttyProgress`**

Reuse the existing throttled-redraw pattern:

```go
func (p *ttyProgress) BeginResolve(hint int) { p.beginPhase("resolving", hint) }
func (p *ttyProgress) IncResolve(name string) { p.inc(name) }
func (p *ttyProgress) EndResolve()            { p.endPhase("resolved") }
```

Then adapt the internal draw path in `maybeDraw` (or wherever the line is formatted) to handle `total == 0`:

```go
// Current (roughly):
//   fmt.Fprintf(p.w, "\r\x1b[Kgomposer: %s %d/%d  %s  %s", p.phase, cur, p.total, bar, p.label)
// New:
if p.total > 0 {
    fmt.Fprintf(p.w, "\r\x1b[Kgomposer: %s %d/%d  %s  %s", p.phase, cur, p.total, bar, p.label)
} else {
    fmt.Fprintf(p.w, "\r\x1b[Kgomposer: %s %d  %s", p.phase, cur, p.label)
}
```

`endPhase` already prints a summary line `gomposer: <verb> N packages\n` — no change needed. The verb passed to `endPhase("resolved")` produces `resolved N packages`.

- [ ] **Step 7: Verify GREEN**

Run: `go test ./internal/cli/... -v`

Expected: all pass including the two new tests.

- [ ] **Step 8: Full-suite check**

Run: `go build ./...`

Expected: green. If the orchestrator's `Progress` mirror wasn't updated in Step 4, this build fails at whichever consumer (`newNoopProgress`-equivalent inside orchestrator, or the callers that pass `opts.Progress`) has an implementation without the new methods.

- [ ] **Step 9: Commit**

```bash
git add internal/cli/progress.go internal/cli/progress_test.go internal/orchestrator/orchestrator.go
git commit -m "feat(progress): resolve-phase triad on Progress interface

Adds BeginResolve(hint int) / IncResolve(name) / EndResolve() alongside
the existing fetch/extract triads. noopProgress stays silent (three
no-ops); ttyProgress reuses the existing throttled-redraw pattern and
handles hint=0 by rendering the running count with no denominator or
progress bar. Also updates the internal orchestrator.Progress mirror
so downstream implementations don't drift out of sync."
```

---

## Task 2: Resolver `OnLookup` callback

**Files:**
- Modify: `internal/resolver/solve.go`
- Modify: `internal/resolver/versions.go`
- Modify: `internal/resolver/versions_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `Input.OnLookup func(pkgName string)` — optional; nil means no callback.
  - `versionLister.onLookup` field, stashed by `Solve` after `newVersionLister` (or via a small setter — the existing wiring uses direct field assignment for `onDecided`, mirror that).

- [ ] **Step 1: Failing test**

Append to `internal/resolver/versions_test.go` (or a sibling — grep for the existing versionLister tests):

```go
func TestVersionListerFiresOnLookupPerUniqueLookup(t *testing.T) {
	// Fake source that records every Lookup call.
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {testlookup.Pkg("a/a", "1.0.0", nil)},
	})

	var mu sync.Mutex
	seen := []string{}
	vl := newVersionLister(src, "stable")
	vl.onLookup = func(name string) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, name)
	}

	// First call fires OnLookup.
	if _, err := vl.versions(context.Background(), "a/a"); err != nil {
		t.Fatalf("versions: %v", err)
	}
	// Second call hits the versionLister's internal cache — no OnLookup.
	if _, err := vl.versions(context.Background(), "a/a"); err != nil {
		t.Fatalf("versions #2: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 {
		t.Errorf("OnLookup fired %d times, want 1 (cache re-read should not tick)", len(seen))
	}
	if len(seen) > 0 && seen[0] != "a/a" {
		t.Errorf("OnLookup received %q, want a/a", seen[0])
	}
}

func TestVersionListerNilOnLookupIsSafe(t *testing.T) {
	src := testlookup.New(map[string][]registry.PackageVersion{
		"a/a": {testlookup.Pkg("a/a", "1.0.0", nil)},
	})
	vl := newVersionLister(src, "stable")
	// vl.onLookup is nil — must not panic.
	if _, err := vl.versions(context.Background(), "a/a"); err != nil {
		t.Fatalf("versions: %v", err)
	}
}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/resolver/ -run TestVersionListerFiresOnLookup -v`

Expected: compile error on `vl.onLookup`.

- [ ] **Step 3: Add `Input.OnLookup`**

In `internal/resolver/solve.go`, next to the existing `OnVersionDecided` field:

```go
// OnLookup, when non-nil, fires synchronously before the resolver
// issues a Lookup to the underlying registry source. Fires only on
// network-path lookups — cache-hit reads inside versionLister do not
// fire it, so the callback tick matches user-visible resolver work.
// Implementations must return quickly and not panic; called from the
// resolver's goroutine.
OnLookup func(pkgName string)
```

- [ ] **Step 4: Wire through `versionLister`**

In `internal/resolver/versions.go`, add to the `versionLister` struct alongside `onDecided`:

```go
onLookup func(pkgName string)
```

In `Solve()` at `solve.go`, right where `vl.onDecided = in.OnVersionDecided` is set:

```go
vl.onLookup = in.OnLookup
```

- [ ] **Step 5: Invoke in `versions()`**

Find the `versions(ctx, pkg)` method in `internal/resolver/versions.go`. Locate the cache check + the actual `Lookup` call. Insert the callback fire right before `Lookup`:

```go
func (vl *versionLister) versions(ctx context.Context, pkg string) ([]listedVersion, error) {
    if v, ok := vl.cache[pkg]; ok {
        return v, nil
    }
    if vl.notFound[pkg] {
        return nil, errPackageNotFound{pkg: pkg}
    }
    if vl.onLookup != nil {
        vl.onLookup(pkg)
    }
    md, err := vl.src.Lookup(ctx, pkg)
    // ... rest unchanged ...
}
```

Note: fires *before* `Lookup` so a slow-cache-warm follow-up (the second `versions("a/a")` call in the test) doesn't tick. Also fires only if the internal `cache` and `notFound` maps both miss — matches the actual "we're about to do work" moment.

- [ ] **Step 6: Verify GREEN**

Run: `go test ./internal/resolver/... -race -v`

Expected: PASS on all resolver tests including the two new ones.

- [ ] **Step 7: Commit**

```bash
git add internal/resolver/solve.go internal/resolver/versions.go internal/resolver/versions_test.go
git commit -m "feat(resolver): OnLookup callback for resolve-phase progress

Adds an optional Input.OnLookup hook that fires synchronously from
versionLister.versions() immediately before an actual src.Lookup.
Cache-hit reads (from vl.cache or vl.notFound) do NOT fire it — the
callback tick matches network work, not attempts. The orchestrator
will use this to drive Progress.IncResolve so cold-cache installs
show a live counter during resolve."
```

---

## Task 3: Pipeline wire-in

**Files:**
- Modify: `internal/orchestrator/pipeline.go`
- Modify: `internal/orchestrator/pipeline_test.go`

**Interfaces:**
- Consumes: `Progress.BeginResolve/IncResolve/EndResolve` from Task 1; `resolver.Input.OnLookup` from Task 2.
- Produces: no new exports. `runFullPipeline` calls the resolve triad around the resolve span, guarded by `sync.Once` so cache-hit runs stay silent.

- [ ] **Step 1: Failing integration test**

Append to `internal/orchestrator/pipeline_test.go`:

```go
type recordingProgress struct {
	mu        sync.Mutex
	events    []string
	resolves  []string // names passed to IncResolve
}

func (r *recordingProgress) record(evt string) {
	r.mu.Lock()
	r.events = append(r.events, evt)
	r.mu.Unlock()
}

func (r *recordingProgress) BeginFetch(int)      { r.record("BeginFetch") }
func (r *recordingProgress) IncFetch(string)     {}
func (r *recordingProgress) EndFetch()           { r.record("EndFetch") }
func (r *recordingProgress) BeginExtract(int)    { r.record("BeginExtract") }
func (r *recordingProgress) IncExtract(string)   {}
func (r *recordingProgress) EndExtract()         { r.record("EndExtract") }
func (r *recordingProgress) BeginResolve(int)    { r.record("BeginResolve") }
func (r *recordingProgress) IncResolve(name string) {
	r.mu.Lock()
	r.resolves = append(r.resolves, name)
	r.mu.Unlock()
}
func (r *recordingProgress) EndResolve()      { r.record("EndResolve") }
func (r *recordingProgress) Done(int)         {}

func (r *recordingProgress) resolveCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.resolves)
}

func (r *recordingProgress) sawEvent(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if e == name {
			return true
		}
	}
	return false
}

// TestPipelineFiresResolveProgressOnFreshInstall asserts the resolve
// triad fires during a fresh install and NOT during a resolution-cache
// hit repeat install. The recording Progress fake records events + per-
// name IncResolve calls.
func TestPipelineFiresResolveProgressOnFreshInstall(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	src := testlookup.New(map[string][]registry.PackageVersion{
		"acme/a": {testlookup.Pkg("acme/a", "1.0.0", nil)},
	})
	rp := &recordingProgress{}

	opts := Options{
		ProjectDir: writeManifest(t, &manifest.Manifest{
			Name:    "acme/app",
			Require: map[string]string{"acme/a": "^1.0"},
		}),
		Source:     src,
		Progress:   rp,
		NoPrefetch: true,
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !rp.sawEvent("BeginResolve") {
		t.Errorf("fresh install did not fire BeginResolve; events=%v", rp.events)
	}
	if rp.resolveCount() == 0 {
		t.Errorf("fresh install fired 0 IncResolve calls (expected at least 1)")
	}
	if !rp.sawEvent("EndResolve") {
		t.Errorf("fresh install did not fire EndResolve")
	}

	// Repeat install: resolution cache hits, resolver never runs.
	rp2 := &recordingProgress{}
	opts.Progress = rp2
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install #2: %v", err)
	}
	if rp2.sawEvent("BeginResolve") {
		t.Errorf("cache-hit install fired BeginResolve; expected silent resolve")
	}
	if rp2.resolveCount() != 0 {
		t.Errorf("cache-hit install fired %d IncResolve calls (expected 0)", rp2.resolveCount())
	}
}
```

`writeManifest` and `testlookup` helpers already exist — grep to confirm.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/orchestrator/ -run TestPipelineFiresResolveProgress -v`

Expected: fail — no progress calls fire because the wire-in isn't in place yet.

- [ ] **Step 3: Wire the `sync.Once` closure into `resolveFunc`**

`resolveFunc` at `internal/orchestrator/pipeline.go:~163` currently constructs `resolver.Input` from `pipelineState`. Add:

```go
var resolveFunc = func(ctx context.Context, ps *pipelineState, src registry.SourceLookup, includeDev bool) (*resolver.Result, error) {
    var beginOnce sync.Once
    return resolver.Solve(ctx, resolver.Input{
        Manifest:            ps.aggregateManifest,
        Source:              src,
        IncludeDev:          includeDev,
        Platform:            ps.platform,
        IgnorePlatformReqs:  ps.ignoreSet,
        PlatformFingerprint: ps.platformStr,
        StrictPlatform:      ps.opts.NoDev,
        OnLookup: func(name string) {
            if ps.opts.Progress == nil {
                return
            }
            beginOnce.Do(func() { ps.opts.Progress.BeginResolve(0) })
            ps.opts.Progress.IncResolve(name)
        },
        // existing OnVersionDecided closure...
    })
}
```

- [ ] **Step 4: Fire `EndResolve` in `runFullPipeline`**

After `resolveOrCache` returns and before any early-return-on-error branch, fire `EndResolve` unconditionally. On cache-hit runs `BeginResolve` was never fired, and `ttyProgress.EndResolve`'s underlying `endPhase` is a no-op when `p.phase == ""` — verify this holds by reading `endPhase` in `internal/cli/progress.go`.

```go
lockFile, fromCache, err := resolveOrCache(ctx, ps, forceResolve)
if opts.Progress != nil {
    opts.Progress.EndResolve()
}
// existing err handling...
```

If `endPhase("resolved")` prints an unconditional summary line even on `phase == ""` (i.e., no `Begin` was fired), gate the call in the pipeline:

```go
// Only End if the resolver did work — cache-hit runs should stay silent.
// (This is the correct guard even if endPhase already no-ops; keeps the
// interface's semantics explicit at the call site.)
if !fromCache && opts.Progress != nil {
    opts.Progress.EndResolve()
}
```

Prefer the gated form — it makes the call-site semantics obvious.

- [ ] **Step 5: Verify GREEN**

Run: `go test ./internal/orchestrator/ -run TestPipelineFiresResolveProgress -race -v -count=3`

Expected: PASS across 3 iterations under `-race`.

- [ ] **Step 6: Full-suite check**

Run: `go test ./... -race`

Expected: green. If any existing pipeline test uses a `Progress` fake that lacks the new methods, adapt (add three no-op methods). Test-side pain here is minimal — most tests pass `nil` for Progress.

- [ ] **Step 7: Manual e2e sanity**

```sh
go build -o /tmp/gomposer-progress ./cmd/gomposer
mkdir -p /tmp/rp-check && cd /tmp/rp-check
cat > composer.json <<'JSON'
{"name":"smoke/x","require":{"monolog/monolog":"^3.0","psr/log":"^3.0"}}
JSON
rm -rf ~/Library/Caches/gomposer && /tmp/gomposer-progress install
```

Expected on a TTY: a live redraw during the resolve phase showing `gomposer: resolving N  <package>`, then the `resolved N packages` summary line, then the fetch/extract lines as before.

If stderr is not a TTY (`2>/tmp/log`), nothing extra should appear — noopProgress stays silent.

- [ ] **Step 8: Commit**

```bash
git add internal/orchestrator/pipeline.go internal/orchestrator/pipeline_test.go
git commit -m "feat(orchestrator): wire resolve-phase progress into runFullPipeline

resolveFunc's resolver.Input.OnLookup closure ticks Progress.IncResolve
for every network-path Lookup the resolver issues. A sync.Once guards
the first Inc so BeginResolve fires only when the resolver actually
does work — cache-hit runs stay silent. EndResolve fires only when the
resolve wasn't served from cache, matching the Begin/End semantics."
```

---

## Resolve-phase progress: acceptance check

After all tasks:

- `go test ./... -race` green.
- `TestTTYProgressEmitsResolveLine` and `TestNoopProgressIsSilentOnResolve` pass.
- `TestVersionListerFiresOnLookupPerUniqueLookup` fires the callback once per unique name; nil callback is a no-op.
- `TestPipelineFiresResolveProgressOnFreshInstall` fires the resolve triad on a fresh install and NOT on a resolution-cache-hit repeat.
- Manual smoke: `gomposer install` on a cold-cache real project shows a live redrawing line during resolve, followed by a `resolved N packages` summary.
- `gomposer install 2>/tmp/log` (non-TTY stderr) writes no ANSI escapes and no progress lines.

If any of these fails, fix forward before declaring the plan done.
