# Stage 3 / Plan 6: Terminal Progress UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an optional, single-line terminal progress UI that auto-detects TTY, redraws fetch and extract progress in place during install, and falls back to plain log lines when stderr is not a TTY (CI, pipes, redirects). After each phase prints a summary line; after the install completes, prints a one-line wall-time + package-count summary. No fullscreen TUI — just `\r\x1b[K` line clears with a throttled redraw.

**Architecture:** A `Progress` interface in `internal/cli/progress.go` with two implementations: `noopProgress` (plain log lines, the existing default) and `ttyProgress` (ANSI redraws). The orchestrator phases call into a `Progress` field on `Options`; the fetcher and materializer pools call `IncFetch` / `IncExtract` after each successful unit. Progress is suppressed entirely under `--quiet`. TTY detection uses `golang.org/x/term.IsTerminal` against the writer's underlying file descriptor.

**Tech Stack:** Go 1.22+, `golang.org/x/term` (TTY detection), standard library `io`, `time`, `sync`. No new third-party UI libraries.

---

## File structure

| Path | Responsibility |
|------|---------------|
| `internal/cli/progress.go` | `Progress` interface, `noopProgress`, `ttyProgress`, `New()` constructor |
| `internal/cli/progress_test.go` | Unit tests against a `bytes.Buffer` writer |
| `internal/orchestrator/orchestrator.go` | New `Progress` field on `Options`; default to noop when nil |
| `internal/orchestrator/pipeline.go` | `fetchAll` / `materializeAll` call `IncFetch` / `IncExtract`; `runFullPipeline` brackets each phase with `BeginFetch` / `EndFetch` etc., and emits the final summary |
| `internal/cli/install.go` | Construct `Progress` from `--quiet` and stderr; pass on `Options` |
| `internal/cli/update.go` | Same wiring for the update path |

---

## Task 1: Progress interface + noop implementation

**Files:**
- Create: `internal/cli/progress.go`
- Create: `internal/cli/progress_test.go`

The `Progress` interface is intentionally synchronous and minimal. It is invoked from many goroutines in the fetch/extract pools; implementations are responsible for their own locking. The interface intentionally does not expose anything visual — callers describe events ("we just fetched X"), the implementation decides whether to redraw, append a log line, or do nothing.

- [ ] **Step 1: Write the failing test**

Create `internal/cli/progress_test.go`:

```go
package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestNoopProgressIsSilent(t *testing.T) {
	var buf bytes.Buffer
	p := newNoopProgress(&buf)
	p.BeginFetch(10)
	p.IncFetch("vendor/a")
	p.IncFetch("vendor/b")
	p.EndFetch()
	p.BeginExtract(10)
	p.IncExtract("vendor/a")
	p.EndExtract()
	p.Done(2)
	if buf.Len() != 0 {
		t.Errorf("noopProgress should write nothing, got %q", buf.String())
	}
}

func TestProgressInterfaceSatisfied(t *testing.T) {
	// Compile-time check: both implementations satisfy Progress.
	var _ Progress = (*noopProgress)(nil)
	var _ Progress = (*ttyProgress)(nil)
}

func TestNewProgressQuietReturnsNoop(t *testing.T) {
	var buf bytes.Buffer
	p := NewProgress(&buf, ProgressOptions{Quiet: true})
	if _, ok := p.(*noopProgress); !ok {
		t.Errorf("Quiet=true should yield noopProgress, got %T", p)
	}
	// Verify it doesn't print on a non-tty either way.
	p.BeginFetch(1)
	p.IncFetch("x")
	p.EndFetch()
	if !strings.Contains(buf.String(), "") || buf.Len() != 0 {
		t.Errorf("Quiet noop wrote output: %q", buf.String())
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/cli/...`

Expected: build error referencing `Progress`, `newNoopProgress`, `ttyProgress`, `NewProgress`, `ProgressOptions`.

- [ ] **Step 3: Write the interface and noop**

Create `internal/cli/progress.go`:

```go
// Package cli — Progress reports install pipeline progress to stderr.
//
// Two implementations exist:
//
//   - noopProgress: silent. Used under --quiet, and also when the writer is
//     not a TTY (CI, pipes, redirects). The orchestrator already emits
//     warnings via charmbracelet/log; the noop progress purposely adds
//     nothing on top.
//   - ttyProgress: ANSI in-place redraws. A throttled goroutine rewrites a
//     single status line ("composer-go: fetching 12/47 [====   ] symfony/console v6.4.5")
//     at most every redrawInterval. After each phase completes, it prints
//     a final summary line. After Done(), it prints the wall-time summary.
//
// Implementations must be safe for concurrent IncFetch / IncExtract calls
// from the fetcher and materializer worker pools.
package cli

import (
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/term"
)

// Progress is the orchestrator-facing API. All methods are no-ops when the
// implementation chooses not to render (e.g. non-TTY, --quiet).
type Progress interface {
	BeginFetch(total int)
	IncFetch(name string)
	EndFetch()

	BeginExtract(total int)
	IncExtract(name string)
	EndExtract()

	// Done is called once after the whole install pipeline has finished.
	// packageCount is the number of packages installed (production + dev).
	Done(packageCount int)
}

// ProgressOptions controls construction of a Progress.
type ProgressOptions struct {
	// Quiet forces the noop implementation regardless of TTY detection.
	Quiet bool
	// ForceTTY bypasses the IsTerminal check (useful in tests).
	ForceTTY bool
}

// NewProgress picks an implementation based on Quiet and TTY detection.
// w should be os.Stderr in production. Tests pass a *bytes.Buffer plus
// ForceTTY: true to drive ttyProgress without a real terminal.
func NewProgress(w io.Writer, opts ProgressOptions) Progress {
	if opts.Quiet {
		return newNoopProgress(w)
	}
	if opts.ForceTTY || isTerminal(w) {
		return newTTYProgress(w)
	}
	return newNoopProgress(w)
}

// isTerminal returns true when w is *os.File pointing at a TTY. Anything
// else (bytes.Buffer in tests, io.Pipe, io.Discard) returns false.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// noopProgress silently swallows every event.
type noopProgress struct{ w io.Writer }

func newNoopProgress(w io.Writer) *noopProgress { return &noopProgress{w: w} }

func (p *noopProgress) BeginFetch(int)        {}
func (p *noopProgress) IncFetch(string)       {}
func (p *noopProgress) EndFetch()             {}
func (p *noopProgress) BeginExtract(int)      {}
func (p *noopProgress) IncExtract(string)     {}
func (p *noopProgress) EndExtract()           {}
func (p *noopProgress) Done(int)              {}

// ttyProgress is filled in by Task 2.
type ttyProgress struct{}

func newTTYProgress(io.Writer) *ttyProgress { return &ttyProgress{} }

func (p *ttyProgress) BeginFetch(int)    {}
func (p *ttyProgress) IncFetch(string)   {}
func (p *ttyProgress) EndFetch()         {}
func (p *ttyProgress) BeginExtract(int)  {}
func (p *ttyProgress) IncExtract(string) {}
func (p *ttyProgress) EndExtract()       {}
func (p *ttyProgress) Done(int)          {}

// Reserve symbols used by Task 2 so the package builds clean.
var _ = atomic.Int64{}
var _ = sync.Mutex{}
var _ = time.Now
var _ = fmt.Sprintf
```

- [ ] **Step 4: Add the `golang.org/x/term` dependency**

Run: `go get golang.org/x/term@latest`

Expected: `go.mod` and `go.sum` updated.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/cli/...`

Expected: PASS for `TestNoopProgressIsSilent`, `TestProgressInterfaceSatisfied`, `TestNewProgressQuietReturnsNoop`.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/cli/progress.go internal/cli/progress_test.go
git commit -m "feat(cli): add Progress interface with silent noop default"
```

---

## Task 2: ttyProgress — throttled in-place redraw

**Files:**
- Modify: `internal/cli/progress.go`
- Modify: `internal/cli/progress_test.go`

`ttyProgress` keeps two atomic counters (fetched, extracted), a current label (the most recent package name), a phase string, and a `lastDraw` timestamp guarded by a mutex. Every Inc* call updates state and conditionally redraws if at least `redrawInterval` (50ms) has elapsed since the last draw. The phase boundaries (BeginFetch, EndFetch, etc.) force a draw so the user sees the very first event and a complete final state.

The redraw sequence is `\r\x1b[K` (carriage return, clear-to-end-of-line) followed by the new line. Phase summaries are printed with a trailing `\n` so they remain in scrollback.

- [ ] **Step 1: Write the failing test**

Append to `internal/cli/progress_test.go`:

```go
import (
	// add to existing imports:
	"time"
)

func TestTTYProgressEmitsClearAndProgress(t *testing.T) {
	var buf bytes.Buffer
	p := newTTYProgress(&buf)
	p.BeginFetch(2)
	p.IncFetch("vendor/a v1.0.0")
	// Sleep past the 50ms throttle so the second Inc redraws.
	time.Sleep(60 * time.Millisecond)
	p.IncFetch("vendor/b v2.0.0")
	p.EndFetch()

	out := buf.String()
	if !strings.Contains(out, "\r\x1b[K") {
		t.Errorf("expected line-clear escape \\r\\x1b[K in output, got %q", out)
	}
	if !strings.Contains(out, "fetching") {
		t.Errorf("expected phase label \"fetching\", got %q", out)
	}
	if !strings.Contains(out, "2/2") {
		t.Errorf("expected final 2/2 count, got %q", out)
	}
	if !strings.Contains(out, "vendor/b v2.0.0") {
		t.Errorf("expected most recent package label, got %q", out)
	}
}

func TestTTYProgressDoneSummary(t *testing.T) {
	var buf bytes.Buffer
	p := newTTYProgress(&buf)
	p.BeginFetch(1)
	p.IncFetch("vendor/a")
	p.EndFetch()
	p.BeginExtract(1)
	p.IncExtract("vendor/a")
	p.EndExtract()
	p.Done(1)
	out := buf.String()
	if !strings.Contains(out, "1 package") {
		t.Errorf("expected final summary with package count, got %q", out)
	}
	// The summary must be on its own line — i.e. preceded by the line clear.
	if !strings.Contains(out, "\r\x1b[K") {
		t.Errorf("expected final clear before summary, got %q", out)
	}
}

func TestTTYProgressThrottle(t *testing.T) {
	var buf bytes.Buffer
	p := newTTYProgress(&buf)
	p.BeginFetch(100)
	for i := 0; i < 50; i++ {
		p.IncFetch("vendor/x")
	}
	// Without sleeping, only the BeginFetch draw + at most one throttled
	// redraw should have fired. We don't assert an exact byte count, but the
	// number of \r\x1b[K sequences should be far less than 50.
	clears := strings.Count(buf.String(), "\r\x1b[K")
	if clears > 5 {
		t.Errorf("throttle ineffective: %d redraws for 50 increments", clears)
	}
	p.EndFetch()
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/cli/...`

Expected: tests fail (ttyProgress writes nothing yet).

- [ ] **Step 3: Replace the stub `ttyProgress`**

In `internal/cli/progress.go`, replace the stub `ttyProgress` block with:

```go
const (
	redrawInterval = 50 * time.Millisecond
	barWidth       = 10
)

// ttyProgress redraws a single line on stderr, throttled to at most one draw
// per redrawInterval. State is shared between fetch and extract phases (only
// one phase runs at a time in the orchestrator).
type ttyProgress struct {
	w io.Writer

	mu       sync.Mutex
	phase    string // "fetching", "extracting", or ""
	total    int
	current  atomic.Int64
	label    string
	lastDraw time.Time

	startTime time.Time
}

func newTTYProgress(w io.Writer) *ttyProgress {
	return &ttyProgress{w: w, startTime: time.Now()}
}

func (p *ttyProgress) BeginFetch(total int)    { p.beginPhase("fetching", total) }
func (p *ttyProgress) BeginExtract(total int)  { p.beginPhase("extracting", total) }

func (p *ttyProgress) beginPhase(name string, total int) {
	p.mu.Lock()
	p.phase = name
	p.total = total
	p.label = ""
	p.current.Store(0)
	p.lastDraw = time.Time{} // force the first redraw
	p.mu.Unlock()
	p.maybeDraw(true)
}

func (p *ttyProgress) IncFetch(name string)   { p.inc(name) }
func (p *ttyProgress) IncExtract(name string) { p.inc(name) }

func (p *ttyProgress) inc(name string) {
	p.current.Add(1)
	p.mu.Lock()
	p.label = name
	p.mu.Unlock()
	p.maybeDraw(false)
}

func (p *ttyProgress) EndFetch()   { p.endPhase("fetched") }
func (p *ttyProgress) EndExtract() { p.endPhase("extracted") }

func (p *ttyProgress) endPhase(verb string) {
	// Force one final redraw at 100% before printing the summary.
	p.maybeDraw(true)
	p.mu.Lock()
	total := p.total
	p.phase = ""
	p.mu.Unlock()
	fmt.Fprintf(p.w, "\r\x1b[Kcomposer-go: %s %d packages\n", verb, total)
}

func (p *ttyProgress) Done(packageCount int) {
	elapsed := time.Since(p.startTime).Round(10 * time.Millisecond)
	fmt.Fprintf(p.w, "\r\x1b[Kcomposer-go: installed %d package%s in %s\n",
		packageCount, plural(packageCount), elapsed)
}

// maybeDraw renders the current line. force=true bypasses the throttle.
// Concurrent callers serialize on p.mu so writes don't interleave.
func (p *ttyProgress) maybeDraw(force bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if !force && now.Sub(p.lastDraw) < redrawInterval {
		return
	}
	if p.phase == "" {
		return
	}
	cur := int(p.current.Load())
	if cur > p.total {
		cur = p.total
	}
	bar := renderBar(cur, p.total)
	fmt.Fprintf(p.w, "\r\x1b[Kcomposer-go: %s %d/%d  %s  %s",
		p.phase, cur, p.total, bar, p.label)
	p.lastDraw = now
}

func renderBar(cur, total int) string {
	if total <= 0 {
		return "[" + repeat(" ", barWidth) + "]"
	}
	filled := cur * barWidth / total
	if filled > barWidth {
		filled = barWidth
	}
	return "[" + repeat("=", filled) + repeat(" ", barWidth-filled) + "]"
}

func repeat(s string, n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
```

Remove the leftover `var _ = ...` reservations from Task 1 — they're no longer needed now that the symbols are used.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/cli/...`

Expected: all PASS, including the throttle test (≤5 redraws for 50 fast increments).

- [ ] **Step 5: Commit**

```bash
git add internal/cli/progress.go internal/cli/progress_test.go
git commit -m "feat(cli): ttyProgress with throttled in-place redraw"
```

---

## Task 3: Wire Progress through Options + pipeline

**Files:**
- Modify: `internal/orchestrator/orchestrator.go`
- Modify: `internal/orchestrator/pipeline.go`

The pipeline needs to call into `Progress` from three places: phase boundaries in `runFullPipeline`, per-package events inside `fetchAll`, and per-package events inside `materializeAll`. To avoid an import cycle (cli depends on orchestrator), define the `Progress` interface again inside the orchestrator package as a structural copy. The `cli.Progress` value is assigned through the interface — Go's structural typing makes this transparent.

- [ ] **Step 1: Add Progress to Options**

In `internal/orchestrator/orchestrator.go`, near the existing `Options` struct, add:

```go
// Progress receives phase + per-package events from the pipeline. nil means
// no progress reporting (use noopProgress for an explicit silent default).
//
// Defined as a local interface to avoid importing the cli package; any
// implementation that satisfies the cli.Progress contract will work.
type Progress interface {
	BeginFetch(total int)
	IncFetch(name string)
	EndFetch()
	BeginExtract(total int)
	IncExtract(name string)
	EndExtract()
	Done(packageCount int)
}
```

Then add a field to `Options`:

```go
// Progress, if non-nil, receives fetch/extract progress events. Suppressed
// when Quiet is set; callers should pass nil or a noop Progress in that
// case to avoid double-suppression confusion.
Progress Progress
```

- [ ] **Step 2: Add a private accessor that defaults nil to noop**

In `internal/orchestrator/pipeline.go`, near the top:

```go
// progressOrNoop returns opts.Progress if set, otherwise a silent stub. Every
// pipeline call site should go through this helper so phase code never has
// to nil-check.
func progressOrNoop(p Progress) Progress {
	if p == nil {
		return noopProgress{}
	}
	return p
}

type noopProgress struct{}

func (noopProgress) BeginFetch(int)    {}
func (noopProgress) IncFetch(string)   {}
func (noopProgress) EndFetch()         {}
func (noopProgress) BeginExtract(int)  {}
func (noopProgress) IncExtract(string) {}
func (noopProgress) EndExtract()       {}
func (noopProgress) Done(int)          {}
```

- [ ] **Step 3: Call Progress from `fetchAll`**

Update the `fetchAll` signature to take a `Progress` argument (or read from a closed-over scope; the simpler choice is a parameter). Modify `internal/orchestrator/pipeline.go`:

```go
func fetchAll(ctx context.Context, pkgs []lock.Package, f Fetcher, workers int, prog Progress) (map[string]string, error) {
	if workers < 1 {
		workers = 1
	}
	prog = progressOrNoop(prog)
	prog.BeginFetch(len(pkgs))
	defer prog.EndFetch()

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)

	var mu sync.Mutex
	keys := make(map[string]string, len(pkgs))

	for i := range pkgs {
		p := pkgs[i]
		g.Go(func() error {
			key, err := f.Fetch(gctx, p)
			if err != nil {
				return fmt.Errorf("orchestrator: fetch %s: %w", p.Name, err)
			}
			mu.Lock()
			keys[p.Name] = key
			mu.Unlock()
			prog.IncFetch(p.Name + " " + p.Version)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return keys, nil
}
```

- [ ] **Step 4: Call Progress from `materializeAll`**

Same pattern:

```go
func materializeAll(ctx context.Context, projectDir string, pkgs []lock.Package, keys map[string]string, m Materializer, workers int, prog Progress) error {
	if workers < 1 {
		workers = 1
	}
	prog = progressOrNoop(prog)
	prog.BeginExtract(len(pkgs))
	defer prog.EndExtract()

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)
	for i := range pkgs {
		p := pkgs[i]
		key, ok := keys[p.Name]
		if !ok {
			return fmt.Errorf("orchestrator: missing store key for %s", p.Name)
		}
		dest := vendorPath(projectDir, p.Name)
		g.Go(func() error {
			if err := m.Materialize(gctx, key, dest); err != nil {
				return fmt.Errorf("orchestrator: materialize %s: %w", p.Name, err)
			}
			prog.IncExtract(p.Name + " " + p.Version)
			return nil
		})
	}
	return g.Wait()
}
```

- [ ] **Step 5: Update `runFullPipeline` callers + final summary**

In `runFullPipeline`, replace the existing `fetchAll` and `materializeAll` invocations with the new signature. After `writeLock` and the post-event but before returning, call `Done`:

```go
keys, err := fetchAll(ctx, all, opts.Fetcher, workerCount(opts.Workers), opts.Progress)
// ...
if err := materializeAll(ctx, opts.ProjectDir, all, keys, opts.Materializer, workerCount(opts.Workers), opts.Progress); err != nil {
	return err
}
```

And just before the final `return nil`:

```go
progressOrNoop(opts.Progress).Done(len(all))
```

- [ ] **Step 6: Adjust existing pipeline tests for new signatures**

Run: `go test ./internal/orchestrator/...`

Expected: any test calling `fetchAll` or `materializeAll` directly will fail to compile. Update each call site to pass `nil` (which `progressOrNoop` resolves to the silent stub). Re-run until green.

- [ ] **Step 7: Commit**

```bash
git add internal/orchestrator/orchestrator.go internal/orchestrator/pipeline.go internal/orchestrator/pipeline_test.go
git commit -m "feat(orchestrator): emit Progress events from fetch and extract phases"
```

---

## Task 4: Wire Progress from the CLI commands

**Files:**
- Modify: `internal/cli/install.go`
- Modify: `internal/cli/update.go`

Both `install` and `update` commands construct a `Progress` once, pass it on `orchestrator.Options`, and rely on `--quiet` to demote to noop. The writer is always `cmd.ErrOrStderr()` so progress goes to stderr (so users can pipe stdout into another tool without the bar polluting it).

- [ ] **Step 1: Update install.go**

In `internal/cli/install.go`, where `orchestrator.Options{...}` is constructed, add:

```go
Progress: NewProgress(cmd.ErrOrStderr(), ProgressOptions{Quiet: flagQuiet}),
```

- [ ] **Step 2: Update update.go**

Same change in `internal/cli/update.go`.

- [ ] **Step 3: Manual smoke test**

Run:

```bash
go build ./cmd/composer-go
cd /tmp/cg-smoke      # or any project with composer.json
$OLDPWD/composer-go install
```

Expected on a TTY: a single redrawing line `composer-go: fetching N/M  [===   ]  vendor/pkg vX.Y.Z`, two phase summary lines (`fetched N packages`, `extracted N packages`), and a final `composer-go: installed N packages in NNNms` line.

Run with stderr redirected:

```bash
$OLDPWD/composer-go install 2>/tmp/cg-stderr.log
cat /tmp/cg-stderr.log
```

Expected: empty (or just any platform warnings). No ANSI escapes, no progress bar — `noopProgress` kicked in because stderr is a file, not a TTY.

Run with `--quiet`:

```bash
$OLDPWD/composer-go install --quiet
```

Expected: no progress output even on a TTY.

- [ ] **Step 4: Commit**

```bash
git add internal/cli/install.go internal/cli/update.go
git commit -m "feat(cli): pass Progress to orchestrator from install and update"
```

---

## Task 5: Integration test — Progress is invoked end-to-end

**Files:**
- Create: `internal/orchestrator/progress_test.go`

A small integration test using the existing in-process fakes (look at `pipeline_test.go` for the fake Fetcher / Materializer pattern). The test installs two packages and asserts the test Progress saw `BeginFetch(2)`, two `IncFetch` calls, `EndFetch`, the same shape for extract, and one `Done(2)`.

- [ ] **Step 1: Write the test**

Create `internal/orchestrator/progress_test.go`:

```go
package orchestrator

import (
	"context"
	"sync"
	"testing"
)

type recordedProgress struct {
	mu     sync.Mutex
	events []string
	fetched, extracted, done int
}

func (r *recordedProgress) record(s string) {
	r.mu.Lock()
	r.events = append(r.events, s)
	r.mu.Unlock()
}

func (r *recordedProgress) BeginFetch(n int)   { r.record("BeginFetch") }
func (r *recordedProgress) IncFetch(n string)  { r.mu.Lock(); r.fetched++; r.mu.Unlock() }
func (r *recordedProgress) EndFetch()          { r.record("EndFetch") }
func (r *recordedProgress) BeginExtract(n int) { r.record("BeginExtract") }
func (r *recordedProgress) IncExtract(n string){ r.mu.Lock(); r.extracted++; r.mu.Unlock() }
func (r *recordedProgress) EndExtract()        { r.record("EndExtract") }
func (r *recordedProgress) Done(n int)         { r.mu.Lock(); r.done = n; r.mu.Unlock() }

func TestProgressInvokedFromPipeline(t *testing.T) {
	rp := &recordedProgress{}
	// Build Options identical to an existing pipeline_test.go setup but
	// with rp wired in. (See pipeline_test.go for fake Fetcher/Materializer
	// definitions — reuse them rather than duplicating here.)
	opts := newTwoPackagePipelineOpts(t)
	opts.Progress = rp

	if err := runFullPipeline(context.Background(), opts, opts.testManifest, false); err != nil {
		t.Fatalf("runFullPipeline: %v", err)
	}

	if rp.fetched != 2 {
		t.Errorf("fetched count = %d, want 2", rp.fetched)
	}
	if rp.extracted != 2 {
		t.Errorf("extracted count = %d, want 2", rp.extracted)
	}
	if rp.done != 2 {
		t.Errorf("done = %d, want 2", rp.done)
	}
	want := []string{"BeginFetch", "EndFetch", "BeginExtract", "EndExtract"}
	if len(rp.events) != len(want) {
		t.Fatalf("events = %v, want %v", rp.events, want)
	}
	for i, w := range want {
		if rp.events[i] != w {
			t.Errorf("events[%d] = %q, want %q", i, rp.events[i], w)
		}
	}
}
```

If `pipeline_test.go` doesn't already export a helper like `newTwoPackagePipelineOpts`, factor one out in a small follow-up commit; the existing test setup builds essentially that.

- [ ] **Step 2: Run tests**

Run: `go test ./internal/orchestrator/...`

Expected: PASS, with all 4 phase events recorded in order, fetched=2, extracted=2, done=2.

- [ ] **Step 3: Commit**

```bash
git add internal/orchestrator/progress_test.go
git commit -m "test(orchestrator): assert Progress events from full pipeline"
```

---

## Task 6: Documentation hook

**Files:**
- Modify: `docs/superpowers/specs/2026-05-07-composer-go-design.md`

The spec already lists "Optional terminal progress UI" under Stage 3. Update that line to point at this plan and clarify the auto-detect behavior.

- [ ] **Step 1: Tighten the spec line**

In the Stage 3 / Components list, replace:

```
- Optional terminal progress UI (single-line or simple multi-line; no fullscreen TUI).
```

with:

```
- Optional terminal progress UI: single-line redraw on stderr when stderr
  is a TTY, plain log lines otherwise. Suppressed under --quiet. See
  `docs/superpowers/plans/2026-05-09-stage3-plan6-progress.md`.
```

- [ ] **Step 2: Commit**

```bash
git add docs/superpowers/specs/2026-05-07-composer-go-design.md
git commit -m "docs(spec): refine stage-3 progress UI line, link plan 6"
```

---

## Progress UI: acceptance check

After all tasks:

- `go test ./...` is green.
- `composer-go install` on a real project shows a single redrawing status line on a TTY, with phase summaries and a final wall-time line.
- `composer-go install 2>/tmp/log` produces no ANSI escapes in `/tmp/log` (noop path).
- `composer-go install --quiet` shows no progress output even on a TTY.
- The recorded test in Task 5 confirms `BeginFetch` / `IncFetch` / `EndFetch` / `BeginExtract` / `IncExtract` / `EndExtract` / `Done` are all invoked exactly once per phase boundary and once per package.
- Manual stress: an install with 50+ packages should not flicker — the throttle keeps redraws at ≤20/s.

If any of these fails, fix forward in a follow-up commit before declaring Plan 6 done.
