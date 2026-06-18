# Stage 3 / Plan 2: Verbose Timing Breakdown Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When `--verbose` is set, print a per-phase timing breakdown to stderr at the end of an install or update. The breakdown lists every pipeline phase (read manifest, resolve, fetch, materialize, autoload, scripts, write lock) with elapsed milliseconds, plus a small set of counters: packages resolved, packages fetched (cold-vs-warm-store hits), and total bytes downloaded.

**Architecture.** A new `Timings` value in `internal/orchestrator/timing.go` collects timestamped phase entries and a small counters bag. The orchestrator wraps each phase with `t.Begin("name")` / `t.End("name")`. The `realfetcher.Fetcher` grows a single `OnFetch func(name string, bytes int, fromCache bool)` hook so the orchestrator can record cold-vs-warm-store hits and bytes downloaded without leaking the fetcher's internals into the timing type. At the end of `runFullPipeline`, when `opts.Verbose` is set, we render a fixed-width block to `os.Stderr`. Quiet mode suppresses output.

**Tech Stack:** Go standard library (`time`, `fmt`, `sort`, `sync`). No new dependencies.

---

## File structure

| Path | Responsibility |
|------|---------------|
| `internal/orchestrator/timing.go` | `Timings` type: `Begin/End`, counters, `Render(io.Writer)` |
| `internal/orchestrator/timing_test.go` | Unit tests for `Timings` mechanics + render output |
| `internal/fetcher/fetcher.go` | Add `OnFetch` callback field; invoke on hit / miss with byte count |
| `internal/fetcher/fetcher_test.go` | Assert callback fires once per Fetch with correct fromCache + bytes |
| `internal/orchestrator/pipeline.go` | Wire `Timings` through every phase; pass callback into the adapter |
| `internal/orchestrator/orchestrator.go` | Plumb `Timings` from `run` and render at the end |
| `internal/orchestrator/pipeline_test.go` | End-to-end timing assertion: verbose run prints expected block to stderr |

---

## Task 1: Timings type — phase entries

**Files:**
- Create: `internal/orchestrator/timing.go`
- Create: `internal/orchestrator/timing_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/orchestrator/timing_test.go`:

```go
package orchestrator

import (
	"testing"
	"time"
)

func TestTimingsBeginEndOrder(t *testing.T) {
	tt := NewTimings()
	tt.Begin("resolve")
	time.Sleep(2 * time.Millisecond)
	tt.End("resolve")

	tt.Begin("fetch")
	time.Sleep(1 * time.Millisecond)
	tt.End("fetch")

	phases := tt.Phases()
	if len(phases) != 2 {
		t.Fatalf("Phases len = %d, want 2", len(phases))
	}
	if phases[0].Name != "resolve" || phases[1].Name != "fetch" {
		t.Errorf("phase order = %v, want [resolve fetch]", phases)
	}
	if phases[0].Elapsed <= 0 {
		t.Errorf("resolve elapsed = %v, want >0", phases[0].Elapsed)
	}
}

func TestTimingsEndWithoutBeginIsNoop(t *testing.T) {
	tt := NewTimings()
	// Calling End without a matching Begin must not panic and must not
	// add a phase entry. Robustness matters because pipeline branches may
	// skip a phase entirely.
	tt.End("never-started")
	if len(tt.Phases()) != 0 {
		t.Errorf("phases = %d, want 0", len(tt.Phases()))
	}
}

func TestTimingsTotal(t *testing.T) {
	tt := NewTimings()
	tt.Begin("a")
	time.Sleep(2 * time.Millisecond)
	tt.End("a")
	tt.Begin("b")
	time.Sleep(2 * time.Millisecond)
	tt.End("b")

	total := tt.Total()
	if total < 4*time.Millisecond {
		t.Errorf("Total = %v, want >= 4ms", total)
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/orchestrator/ -run TestTimings`

Expected: build error referencing `NewTimings`, `Phases`, `Total`.

- [ ] **Step 3: Implement Timings**

Create `internal/orchestrator/timing.go`:

```go
package orchestrator

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// Phase is a single named span in the pipeline.
type Phase struct {
	Name    string
	Elapsed time.Duration
}

// Counters holds the small set of per-run numeric metrics rendered alongside
// the phase timings. Fields are owned by the orchestrator; callers update
// them under the Timings mutex via the dedicated Add* methods.
type Counters struct {
	PackagesResolved int
	PackagesFetched  int
	CacheHits        int   // package fetches served from the content store
	BytesDownloaded  int64 // bytes pulled over the network this run
}

// Timings records phase durations and run-wide counters for verbose output.
// It is safe for concurrent use; the fetcher callback fires from worker
// goroutines.
type Timings struct {
	mu       sync.Mutex
	starts   map[string]time.Time
	phases   []Phase
	counters Counters
}

// NewTimings returns an empty Timings.
func NewTimings() *Timings {
	return &Timings{starts: make(map[string]time.Time)}
}

// Begin records the start time for the named phase. Repeated calls with the
// same name overwrite the previous start; phases are not nested.
func (t *Timings) Begin(name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.starts[name] = time.Now()
	t.mu.Unlock()
}

// End closes the named phase and appends an entry to the phase list. End
// without a matching Begin is a no-op so optional phases (scripts, write
// lock on a no-op run) do not require defensive code at every call site.
func (t *Timings) End(name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	start, ok := t.starts[name]
	if !ok {
		return
	}
	delete(t.starts, name)
	t.phases = append(t.phases, Phase{Name: name, Elapsed: time.Since(start)})
}

// Phases returns the recorded phases in the order they ended.
func (t *Timings) Phases() []Phase {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Phase, len(t.phases))
	copy(out, t.phases)
	return out
}

// Total is the sum of all phase elapsed times. We sum recorded phases rather
// than measuring wall time so concurrent overlap (e.g. prefetch) shows up
// honestly in the breakdown without inflating the total.
func (t *Timings) Total() time.Duration {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var sum time.Duration
	for _, p := range t.phases {
		sum += p.Elapsed
	}
	return sum
}

// Counters returns a copy of the current counters.
func (t *Timings) Counters() Counters {
	if t == nil {
		return Counters{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.counters
}

// SetPackagesResolved records the resolver result count.
func (t *Timings) SetPackagesResolved(n int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.counters.PackagesResolved = n
	t.mu.Unlock()
}

// AddFetch is the fetcher callback. fromCache=true means a warm store hit
// (no network); fromCache=false means we downloaded `bytes` bytes.
func (t *Timings) AddFetch(_ string, bytes int, fromCache bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.counters.PackagesFetched++
	if fromCache {
		t.counters.CacheHits++
	} else {
		t.counters.BytesDownloaded += int64(bytes)
	}
	t.mu.Unlock()
}

// Render writes a fixed-width breakdown to w. Output goes to stderr at the
// end of a verbose run.
func (t *Timings) Render(w io.Writer) {
	if t == nil {
		return
	}
	t.mu.Lock()
	phases := make([]Phase, len(t.phases))
	copy(phases, t.phases)
	c := t.counters
	t.mu.Unlock()

	fmt.Fprintln(w, "gomposer: timing")
	for _, p := range phases {
		annot := annotate(p.Name, c)
		if annot == "" {
			fmt.Fprintf(w, "  %-16s %5d ms\n", p.Name, p.Elapsed.Milliseconds())
		} else {
			fmt.Fprintf(w, "  %-16s %5d ms %s\n", p.Name, p.Elapsed.Milliseconds(), annot)
		}
	}
	var total time.Duration
	for _, p := range phases {
		total += p.Elapsed
	}
	fmt.Fprintf(w, "  -------- total   %5d ms\n", total.Milliseconds())
}

// annotate returns the parenthesized counter suffix for a phase, or "" if
// the phase has no counters attached.
func annotate(name string, c Counters) string {
	switch name {
	case "resolve":
		if c.PackagesResolved == 0 {
			return ""
		}
		return fmt.Sprintf("(%d packages)", c.PackagesResolved)
	case "fetch":
		if c.PackagesFetched == 0 {
			return ""
		}
		cold := c.PackagesFetched - c.CacheHits
		return fmt.Sprintf("(%d/%d cold, %d KB)", cold, c.PackagesFetched, c.BytesDownloaded/1024)
	}
	return ""
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/orchestrator/ -run TestTimings`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/orchestrator/timing.go internal/orchestrator/timing_test.go
git commit -m "feat(orchestrator): Timings type with phase spans and counters"
```

---

## Task 2: Render output format

**Files:**
- Modify: `internal/orchestrator/timing_test.go`

The render format is load-bearing for users and benchmark scrapers. Lock it down with a snapshot test.

- [ ] **Step 1: Write the failing test**

Append to `internal/orchestrator/timing_test.go`:

```go
import (
	"bytes"
	"strings"
)

func TestTimingsRender(t *testing.T) {
	tt := NewTimings()
	// Synthesize phases without calling Begin/End so durations are
	// deterministic.
	tt.phases = []Phase{
		{Name: "read manifest", Elapsed: 1 * time.Millisecond},
		{Name: "resolve", Elapsed: 50 * time.Millisecond},
		{Name: "fetch", Elapsed: 200 * time.Millisecond},
		{Name: "materialize", Elapsed: 30 * time.Millisecond},
		{Name: "autoload", Elapsed: 10 * time.Millisecond},
		{Name: "scripts", Elapsed: 5 * time.Millisecond},
		{Name: "write lock", Elapsed: 2 * time.Millisecond},
	}
	tt.counters = Counters{
		PackagesResolved: 12,
		PackagesFetched:  12,
		CacheHits:        4,
		BytesDownloaded:  512 * 1024,
	}

	var buf bytes.Buffer
	tt.Render(&buf)
	got := buf.String()

	want := []string{
		"gomposer: timing",
		"read manifest        1 ms",
		"resolve             50 ms (12 packages)",
		"fetch              200 ms (8/12 cold, 512 KB)",
		"materialize         30 ms",
		"autoload            10 ms",
		"scripts              5 ms",
		"write lock           2 ms",
		"-------- total     298 ms",
	}
	for _, line := range want {
		if !strings.Contains(got, line) {
			t.Errorf("missing line %q in:\n%s", line, got)
		}
	}
}
```

- [ ] **Step 2: Run; expect to mostly pass**

Run: `go test ./internal/orchestrator/ -run TestTimingsRender`

Expected: PASS if column widths in `Render` match. If a width assertion fails, adjust the `%-16s` and `%5d` format specifiers in `timing.go` until the snapshot matches. The exact column width is the contract; do not change it casually.

- [ ] **Step 3: Commit**

```bash
git add internal/orchestrator/timing_test.go
git commit -m "test(orchestrator): pin Timings.Render output format"
```

---

## Task 3: Fetcher OnFetch callback

**Files:**
- Modify: `internal/fetcher/fetcher.go`
- Modify: `internal/fetcher/fetcher_test.go`

The orchestrator records cold-vs-warm hits by attaching a callback to the fetcher. The callback fires exactly once per `Fetch` call, after the result is decided (hit or miss). For misses we report the actual byte count copied through the hasher.

- [ ] **Step 1: Write the failing test**

Append to `internal/fetcher/fetcher_test.go`:

```go
func TestFetchOnFetchCallback(t *testing.T) {
	dir := t.TempDir()
	s, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}

	body := []byte("zip-bytes-here")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	type call struct {
		name      string
		bytes     int
		fromCache bool
	}
	var calls []call
	f := New(s, srv.Client())
	f.OnFetch = func(name string, bytes int, fromCache bool) {
		calls = append(calls, call{name, bytes, fromCache})
	}

	pv := registry.PackageVersion{
		Name: "vendor/pkg",
		Dist: registry.Dist{Type: "zip", URL: srv.URL},
	}
	// Cold fetch: must report bytes>0, fromCache=false.
	if _, err := f.Fetch(context.Background(), pv); err != nil {
		t.Fatalf("cold Fetch: %v", err)
	}
	// Warm fetch with explicit Sha so the store-hit shortcut triggers.
	pv2 := pv
	pv2.Dist.Sha = calls[0].name // not used; placeholder so the compiler is happy
	// Re-fetch with the sha we just learned: should be a cache hit.
	pv3 := pv
	// Compute sha of body for the warm-hit short-circuit.
	sum := sha256.Sum256(body)
	pv3.Dist.Sha = hex.EncodeToString(sum[:])
	if _, err := f.Fetch(context.Background(), pv3); err != nil {
		t.Fatalf("warm Fetch: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("OnFetch fired %d times, want 2", len(calls))
	}
	if calls[0].fromCache {
		t.Errorf("first call fromCache=true, want false")
	}
	if calls[0].bytes != len(body) {
		t.Errorf("first call bytes=%d, want %d", calls[0].bytes, len(body))
	}
	if !calls[1].fromCache {
		t.Errorf("second call fromCache=false, want true")
	}
	if calls[1].bytes != 0 {
		t.Errorf("second call bytes=%d, want 0 on cache hit", calls[1].bytes)
	}
}
```

(Add `"crypto/sha256"`, `"encoding/hex"`, and `"net/http/httptest"` imports to the test file if they are not already present.)

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/fetcher/ -run TestFetchOnFetchCallback`

Expected: build error on `f.OnFetch`.

- [ ] **Step 3: Add the field and invoke it**

Edit `internal/fetcher/fetcher.go`. Replace the `Fetcher` struct and `Fetch` method:

```go
// Fetcher coordinates downloads into a store. It is safe for concurrent use.
type Fetcher struct {
	store *store.Store
	http  *http.Client
	// OnFetch, if non-nil, is invoked exactly once per Fetch call after the
	// outcome is known. fromCache=true means the bytes were already in the
	// store and no network was used (bytes is then 0). fromCache=false
	// means we downloaded `bytes` bytes.
	//
	// The hook is expected to be cheap and non-blocking; the orchestrator
	// uses it to drive a Timings counter from worker goroutines.
	OnFetch func(name string, bytes int, fromCache bool)
}
```

In `Fetch`, replace the warm-hit shortcut:

```go
	if pv.Dist.Sha != "" && f.store.Has(pv.Dist.Sha) {
		if f.OnFetch != nil {
			f.OnFetch(pv.Name, 0, true)
		}
		return pv.Dist.Sha, nil
	}
```

After the `io.Copy` succeeds, capture the byte count. Replace:

```go
	hasher := sha256.New()
	tee := io.TeeReader(resp.Body, hasher)
	if _, err := io.Copy(tmp, tee); err != nil {
		cleanup()
		return "", fmt.Errorf("fetcher: %s: copy: %w", pv.Name, err)
	}
```

with:

```go
	hasher := sha256.New()
	tee := io.TeeReader(resp.Body, hasher)
	n, err := io.Copy(tmp, tee)
	if err != nil {
		cleanup()
		return "", fmt.Errorf("fetcher: %s: copy: %w", pv.Name, err)
	}
```

Then immediately before the final `return finalSha, nil`, fire the callback:

```go
	if f.OnFetch != nil {
		f.OnFetch(pv.Name, int(n), false)
	}
	return finalSha, nil
```

The `n` is also passed through the rename branch's success path; mirror the callback there as well so a "rename failed because dest exists" path still reports the cold download:

```go
	if err := os.Rename(tmpPath, f.store.Path(finalSha)); err != nil {
		_ = os.Remove(tmpPath)
		if errors.Is(err, os.ErrExist) || f.store.Has(finalSha) {
			if f.OnFetch != nil {
				f.OnFetch(pv.Name, int(n), false)
			}
			return finalSha, nil
		}
		return "", fmt.Errorf("fetcher: %s: rename: %w", pv.Name, err)
	}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/fetcher/...`

Expected: all PASS, including the new callback test.

- [ ] **Step 5: Commit**

```bash
git add internal/fetcher
git commit -m "feat(fetcher): OnFetch callback for cold/warm + bytes accounting"
```

---

## Task 4: Plumb Timings through the orchestrator

**Files:**
- Modify: `internal/orchestrator/orchestrator.go`
- Modify: `internal/orchestrator/pipeline.go`

The `Timings` value lives on the run, not on `Options` (callers do not own it). It is created in `run`, threaded into `runFullPipeline`, and rendered at the bottom when `opts.Verbose` is set and `opts.Quiet` is not.

- [ ] **Step 1: Adjust `run`**

In `internal/orchestrator/orchestrator.go`, replace `run` with:

```go
func run(ctx context.Context, opts Options, m *manifest.Manifest, forceResolve bool) error {
	if len(m.Require) == 0 && len(m.RequireDev) == 0 {
		return nil
	}
	if opts.NoNetwork {
		return errors.New("orchestrator: NoNetwork is set but manifest has requires")
	}
	t := NewTimings()
	err := runFullPipeline(ctx, opts, m, forceResolve, t)
	if opts.Verbose && !opts.Quiet {
		t.Render(os.Stderr)
	}
	return err
}
```

- [ ] **Step 2: Wrap each phase in pipeline.go**

In `internal/orchestrator/pipeline.go`, change the `runFullPipeline` signature and wrap every phase with `t.Begin` / `t.End`. The new signature is:

```go
func runFullPipeline(ctx context.Context, opts Options, m *manifest.Manifest, forceResolve bool, t *Timings) error {
```

The phases to instrument and the names to use:

| Phase | `Begin` / `End` name |
|-------|----------------------|
| `newPipelineState` (reads manifest bytes + lock bytes + probes platform) | `read manifest` |
| `resolveOrCache` (only when it actually resolves; the lock-hit branch is fast and labels cleanly anyway) | `resolve` |
| `fetchAll` + `backfillSha` | `fetch` |
| `materializeAll` | `materialize` |
| `generateAutoloader` | `autoload` |
| All `fireEvent` calls combined | `scripts` (begin once, accumulate; see step 3) |
| `writeLock` | `write lock` |

The `scripts` phase is special: there are four `fireEvent` calls (pre-cmd, pre-autoload-dump, post-autoload-dump, post-cmd) interleaved with other work. To keep the breakdown simple we report a single `scripts` phase that accumulates all four. Use a small helper:

```go
// firePhase wraps fireEvent with timing accumulation. The `scripts` phase is
// the sum of all four lifecycle event firings; we add to it incrementally.
func firePhase(ctx context.Context, t *Timings, event scripts.Event, opts Options, m *manifest.Manifest) error {
	if opts.NoScripts || opts.Scripts == nil {
		return nil
	}
	start := time.Now()
	err := opts.Scripts.Run(ctx, event, scripts.Options{
		ProjectDir: opts.ProjectDir,
		Scripts:    m.Scripts,
		Verbose:    opts.Verbose,
	})
	if t != nil {
		// Append directly so multiple calls collapse to a single phase entry.
		t.AddScriptsTime(time.Since(start))
	}
	return err
}
```

Add the corresponding method to `internal/orchestrator/timing.go`:

```go
// AddScriptsTime accumulates time spent in lifecycle scripts. Multiple calls
// across the pipeline collapse into a single "scripts" phase entry; the
// final entry is materialized by FlushScripts at the end of the run.
func (t *Timings) AddScriptsTime(d time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.scriptsTotal += d
}

// FlushScripts appends the accumulated scripts time as a single phase entry.
// Called at the end of runFullPipeline so "scripts" lands in the breakdown
// in a stable position regardless of which lifecycle events fired.
func (t *Timings) FlushScripts() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.scriptsTotal > 0 {
		t.phases = append(t.phases, Phase{Name: "scripts", Elapsed: t.scriptsTotal})
		t.scriptsTotal = 0
	}
}
```

Add the field on `Timings`:

```go
type Timings struct {
	mu           sync.Mutex
	starts       map[string]time.Time
	phases       []Phase
	counters     Counters
	scriptsTotal time.Duration
}
```

Now rewrite `runFullPipeline`. The structure is:

```go
func runFullPipeline(ctx context.Context, opts Options, m *manifest.Manifest, forceResolve bool, t *Timings) error {
	if err := defaultDeps(&opts, m, t); err != nil {
		return err
	}

	preCmd := scripts.EventPreInstall
	postCmd := scripts.EventPostInstall
	if forceResolve {
		preCmd = scripts.EventPreUpdate
		postCmd = scripts.EventPostUpdate
	}

	if err := firePhase(ctx, t, preCmd, opts, m); err != nil {
		return err
	}

	t.Begin("read manifest")
	ps, err := newPipelineState(opts, m)
	t.End("read manifest")
	if err != nil {
		return err
	}

	t.Begin("resolve")
	lockFile, err := resolveOrCache(ctx, ps, forceResolve)
	t.End("resolve")
	if err != nil {
		return err
	}
	t.SetPackagesResolved(len(lockFile.Packages) + len(lockFile.PackagesDev))

	// ... existing plugin / platform-warning blocks unchanged ...

	all := append([]lock.Package(nil), lockFile.Packages...)
	if !opts.NoDev {
		all = append(all, lockFile.PackagesDev...)
	}

	t.Begin("fetch")
	keys, err := fetchAll(ctx, all, opts.Fetcher, workerCount(opts.Workers))
	if err != nil {
		t.End("fetch")
		return err
	}
	backfillSha(lockFile.Packages, keys)
	backfillSha(lockFile.PackagesDev, keys)
	t.End("fetch")

	t.Begin("materialize")
	matErr := materializeAll(ctx, opts.ProjectDir, all, keys, opts.Materializer, workerCount(opts.Workers))
	t.End("materialize")
	if matErr != nil {
		return matErr
	}

	if err := firePhase(ctx, t, scripts.EventPreAutoloadDump, opts, m); err != nil {
		return err
	}
	t.Begin("autoload")
	alErr := generateAutoloader(ctx, opts.ProjectDir, all, m, opts.Autoloader)
	t.End("autoload")
	if alErr != nil {
		return alErr
	}
	if err := firePhase(ctx, t, scripts.EventPostAutoloadDump, opts, m); err != nil {
		return err
	}

	t.Begin("write lock")
	wlErr := writeLock(opts.ProjectDir, lockFile)
	t.End("write lock")
	if wlErr != nil {
		return wlErr
	}

	if err := firePhase(ctx, t, postCmd, opts, m); err != nil {
		return err
	}
	t.FlushScripts()
	return nil
}
```

The plugin-detection and platform-warning blocks between resolve and fetch stay unchanged from the current pipeline; copy them across verbatim into their existing position.

Replace the existing `fireEvent` calls in the file with `firePhase` calls (and remove the now-unused `fireEvent` helper, or leave it if other code paths call it — `grep -n fireEvent internal/orchestrator` will tell you).

- [ ] **Step 3: Wire the fetcher callback in `defaultDeps`**

In `internal/orchestrator/pipeline.go`, change `defaultDeps`'s signature to accept `*Timings`:

```go
func defaultDeps(opts *Options, m *manifest.Manifest, t *Timings) error {
```

Where it constructs the real fetcher, attach the callback:

```go
		f := realfetcher.New(s, nil)
		if t != nil {
			f.OnFetch = t.AddFetch
		}
```

- [ ] **Step 4: Add the time import**

Add `"time"` to the import block in `internal/orchestrator/pipeline.go` (used by `firePhase`).

- [ ] **Step 5: Build**

Run: `go build ./...`

Expected: clean build. If `resolveOnly` (a test seam in pipeline.go) does not compile, update its body to pass `nil` for the new `*Timings` arg:

```go
func resolveOnly(ctx context.Context, opts Options) (*lock.File, error) {
	m, err := loadManifest(opts.ProjectDir)
	if err != nil {
		return nil, err
	}
	if err := defaultDeps(&opts, m, nil); err != nil {
		return nil, err
	}
	ps, err := newPipelineState(opts, m)
	if err != nil {
		return nil, err
	}
	return resolveOrCache(ctx, ps, true)
}
```

- [ ] **Step 6: Run the orchestrator suite**

Run: `go test ./internal/orchestrator/...`

Expected: PASS. If a pre-existing test broke because it asserted exact stderr output, the most likely cause is a stray render block — confirm `opts.Verbose` is false by default.

- [ ] **Step 7: Commit**

```bash
git add internal/orchestrator
git commit -m "feat(orchestrator): wrap pipeline phases with Timings spans"
```

---

## Task 5: End-to-end verbose output assertion

**Files:**
- Modify: `internal/orchestrator/pipeline_test.go`

Verify that a verbose run prints the timing block to stderr, contains every expected phase label, and reports correct fetch counters from a fake fetcher that toggles `fromCache`.

- [ ] **Step 1: Read existing test scaffolding**

Open `internal/orchestrator/pipeline_test.go` and locate the existing fake fetcher / fake materializer / fake autoloader pattern. The new test reuses them.

- [ ] **Step 2: Append the test**

Append:

```go
func TestVerbosePrintsTimingBlock(t *testing.T) {
	// Capture stderr.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()

	dir := t.TempDir()
	manifest := []byte(`{"name":"vendor/root","require":{"a/a":"^1.0"}}`)
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}

	opts := Options{
		ProjectDir:   dir,
		Verbose:      true,
		Fetcher:      &fakeFetcher{},
		Materializer: &fakeMaterializer{},
		Autoloader:   &fakeAutoloader{},
		Source:       newFakeSource(map[string][]registry.PackageVersion{
			"a/a": {{Name: "a/a", Version: "1.0.0", Dist: registry.Dist{Type: "zip", URL: "x", Sha: "deadbeef"}}},
		}),
		NoScripts: true,
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install: %v", err)
	}

	w.Close()
	out, _ := io.ReadAll(r)
	got := string(out)

	for _, want := range []string{
		"gomposer: timing",
		"read manifest",
		"resolve",
		"fetch",
		"materialize",
		"autoload",
		"write lock",
		"-------- total",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("verbose output missing %q in:\n%s", want, got)
		}
	}
}
```

(Names like `fakeFetcher`, `newFakeSource` reflect the existing test scaffolding in `pipeline_test.go` / `orchestrator_test.go`. If the helpers are named differently, swap to match. Add `"io"`, `"strings"`, `"os"`, `"path/filepath"` to imports if missing.)

- [ ] **Step 3: Run**

Run: `go test ./internal/orchestrator/ -run TestVerbosePrintsTimingBlock -v`

Expected: PASS. Output should include each phase label and a `total` line.

- [ ] **Step 4: Commit**

```bash
git add internal/orchestrator/pipeline_test.go
git commit -m "test(orchestrator): verbose run prints timing breakdown"
```

---

## Task 6: Quiet flag suppresses timing output

**Files:**
- Modify: `internal/orchestrator/pipeline_test.go`

`--quiet` already suppresses warnings; verify it also suppresses the timing block, even with `--verbose` set. (`-v --quiet` together is unusual but well-defined: warnings off, timing off, errors only.)

- [ ] **Step 1: Append the test**

```go
func TestQuietSuppressesTimingBlock(t *testing.T) {
	r, w, _ := os.Pipe()
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()

	dir := t.TempDir()
	manifest := []byte(`{"name":"vendor/root","require":{"a/a":"^1.0"}}`)
	os.WriteFile(filepath.Join(dir, "composer.json"), manifest, 0o644)

	opts := Options{
		ProjectDir:   dir,
		Verbose:      true,
		Quiet:        true,
		Fetcher:      &fakeFetcher{},
		Materializer: &fakeMaterializer{},
		Autoloader:   &fakeAutoloader{},
		Source: newFakeSource(map[string][]registry.PackageVersion{
			"a/a": {{Name: "a/a", Version: "1.0.0", Dist: registry.Dist{Type: "zip", URL: "x", Sha: "deadbeef"}}},
		}),
		NoScripts: true,
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install: %v", err)
	}
	w.Close()
	out, _ := io.ReadAll(r)
	if strings.Contains(string(out), "gomposer: timing") {
		t.Errorf("quiet+verbose should suppress timing, got:\n%s", out)
	}
}
```

- [ ] **Step 2: Run**

Run: `go test ./internal/orchestrator/ -run TestQuietSuppressesTimingBlock`

Expected: PASS, because `run` already gates render on `opts.Verbose && !opts.Quiet`.

- [ ] **Step 3: Commit**

```bash
git add internal/orchestrator/pipeline_test.go
git commit -m "test(orchestrator): --quiet suppresses verbose timing block"
```

---

## Task 7: Manual smoke

**Files:** none.

- [ ] **Step 1: Build**

Run:
```bash
go build ./cmd/gomposer
```

- [ ] **Step 2: Run a real install with `--verbose`**

In a project that already has a working `gomposer install` flow (e.g. a checkout with a Packagist `monolog/monolog` dependency):

```bash
./gomposer install --verbose 2> timing.log
cat timing.log
```

Expected: stderr contains a block like:

```
gomposer: timing
  read manifest        2 ms
  resolve             83 ms (4 packages)
  fetch              412 ms (3/4 cold, 218 KB)
  materialize         18 ms
  autoload            27 ms
  scripts              4 ms
  write lock           1 ms
  -------- total     547 ms
```

- [ ] **Step 3: Run a second time (warm cache)**

Run again. The fetch line should now read `(0/4 cold, 0 KB)` with a much smaller elapsed value.

- [ ] **Step 4: Run without `--verbose`**

```bash
./gomposer install 2> notiming.log
test ! -s notiming.log || grep -q timing notiming.log && echo FAIL || echo OK
```

Expected: `OK`. No `gomposer: timing` block on stderr.

---

## Acceptance check

After all tasks:

- `go test ./...` is green.
- `go build ./cmd/gomposer` produces a binary.
- `gomposer install --verbose` prints a 9-line timing block to stderr ending with `-------- total  N ms`.
- `gomposer install` (no `--verbose`) prints no timing block.
- `gomposer install --verbose --quiet` prints no timing block.
- The fetch counters are accurate: warm-cache run reports `0/N cold, 0 KB`; cold-cache run reports `N/N cold, K KB` matching the actual download size within rounding.
- The `Timings` type is safe for concurrent use under `-race`: `go test -race ./internal/orchestrator/...` is green.

If any of these fails, fix forward in a follow-up commit before declaring Plan 2 done.
