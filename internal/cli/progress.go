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

func (p *noopProgress) BeginFetch(int)   {}
func (p *noopProgress) IncFetch(string)  {}
func (p *noopProgress) EndFetch()        {}
func (p *noopProgress) BeginExtract(int) {}
func (p *noopProgress) IncExtract(string) {}
func (p *noopProgress) EndExtract()      {}
func (p *noopProgress) Done(int)         {}

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

func (p *ttyProgress) BeginFetch(total int)   { p.beginPhase("fetching", total) }
func (p *ttyProgress) BeginExtract(total int) { p.beginPhase("extracting", total) }

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
