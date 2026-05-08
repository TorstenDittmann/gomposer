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
