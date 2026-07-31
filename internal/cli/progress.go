// Package cli contains the adaptive terminal reporter used by install and
// update. Interactive terminals get a live checklist; redirected output gets
// stable, grep-friendly phase lines; --quiet gets a true no-op.
package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
)

// Progress is the narrow callback surface consumed by the orchestrator's
// concurrent resolve/fetch/materialize paths.
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

// journeyProgress is an optional richer surface. The orchestrator detects it
// with a type assertion; existing test fakes that only implement Progress keep
// working unchanged.
type journeyProgress interface {
	Progress
	BeginStage(name string, total int)
	EndStage(name, detail string)
	SetStageDetail(name, detail string)
	Warning(message string)
	RecordFetch(name string, bytes int, fromCache bool)
	Fail(err error)
}

type ColorMode string

const (
	ColorAuto   ColorMode = "auto"
	ColorAlways ColorMode = "always"
	ColorNever  ColorMode = "never"
)

type ProgressOptions struct {
	Quiet      bool
	ForceTTY   bool
	Color      ColorMode
	Operation  string
	ProjectDir string
	Width      int // tests; zero queries the terminal and falls back to 80
}

func NewProgress(w io.Writer, opts ProgressOptions) Progress {
	if opts.Quiet {
		return newNoopProgress(w)
	}
	tty := opts.ForceTTY || isTerminal(w)
	color := colorEnabled(opts.Color, tty)
	if tty {
		return newTTYProgressWithOptions(w, opts, color)
	}
	return newPlainProgress(w, opts)
}

func colorEnabled(mode ColorMode, tty bool) bool {
	switch mode {
	case ColorAlways:
		return true
	case ColorNever:
		return false
	default:
		return tty && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	}
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

type noopProgress struct{ w io.Writer }

func newNoopProgress(w io.Writer) *noopProgress     { return &noopProgress{w: w} }
func (*noopProgress) BeginFetch(int)                {}
func (*noopProgress) IncFetch(string)               {}
func (*noopProgress) EndFetch()                     {}
func (*noopProgress) BeginExtract(int)              {}
func (*noopProgress) IncExtract(string)             {}
func (*noopProgress) EndExtract()                   {}
func (*noopProgress) BeginResolve(int)              {}
func (*noopProgress) IncResolve(string)             {}
func (*noopProgress) EndResolve()                   {}
func (*noopProgress) Done(int)                      {}
func (*noopProgress) BeginStage(string, int)        {}
func (*noopProgress) EndStage(string, string)       {}
func (*noopProgress) SetStageDetail(string, string) {}
func (*noopProgress) Warning(string)                {}
func (*noopProgress) RecordFetch(string, int, bool) {}
func (p *noopProgress) Fail(err error)              { renderPlainFailure(p.w, err) }

var stageOrder = []string{"prepare", "resolve", "download", "install", "autoload", "finalize"}

var stageLabels = map[string]string{
	"prepare": "Prepare", "resolve": "Resolve", "download": "Download",
	"install": "Install", "autoload": "Autoload", "finalize": "Finalize",
}

type stageState struct {
	started, ended, printed bool
	total, current          int
	cacheHits               int
	bytes                   int64
	fetchOutcomes           map[string]bool
	label, detail           string
	startedAt, endedAt      time.Time
}

type progressState struct {
	w          io.Writer
	op         string
	projectDir string
	startedAt  time.Time
	mu         sync.Mutex
	stages     map[string]*stageState
	warnings   map[string]struct{}
	failed     bool
}

func newProgressState(w io.Writer, opts ProgressOptions) progressState {
	op := opts.Operation
	if op == "" {
		op = "install"
	}
	return progressState{w: w, op: op, projectDir: opts.ProjectDir, startedAt: time.Now(), stages: map[string]*stageState{}, warnings: map[string]struct{}{}}
}

func (p *progressState) stage(name string) *stageState {
	s := p.stages[name]
	if s == nil {
		s = &stageState{}
		p.stages[name] = s
	}
	return s
}

// plainProgress is intentionally boring: one complete line per phase, no
// redraws. It is suitable for CI logs and redirected stderr.
type plainProgress struct {
	progressState
	color bool
}

func newPlainProgress(w io.Writer, opts ProgressOptions) *plainProgress {
	return &plainProgress{progressState: newProgressState(w, opts), color: opts.Color == ColorAlways}
}

func (p *plainProgress) paint(code, s string) string {
	if !p.color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (p *plainProgress) BeginStage(name string, total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.stage(name)
	if s.started {
		return
	}
	s.started, s.total, s.startedAt = true, total, time.Now()
}
func (p *plainProgress) SetStageDetail(name, detail string) {
	p.mu.Lock()
	p.stage(name).detail = detail
	p.mu.Unlock()
}
func (p *plainProgress) EndStage(name, detail string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.stage(name)
	if !s.started || s.ended {
		return
	}
	if detail != "" {
		s.detail = detail
	}
	s.ended, s.endedAt = true, time.Now()
	p.printCompletedLocked(name, s)
}
func (p *plainProgress) printCompletedLocked(name string, s *stageState) {
	if s.printed {
		return
	}
	detail := phaseDetail(name, s)
	if detail == "" {
		detail = "complete"
	}
	fmt.Fprintf(p.w, "%s %s: %s (%s)\n", p.paint("36", "gomposer:"), name, detail, formatDuration(s.endedAt.Sub(s.startedAt)))
	s.printed = true
}
func (p *plainProgress) Warning(message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.warnings[message]; ok {
		return
	}
	p.warnings[message] = struct{}{}
	fmt.Fprintf(p.w, "%s %s %s\n", p.paint("36", "gomposer:"), p.paint("33", "warning:"), message)
}
func (p *plainProgress) RecordFetch(name string, bytes int, fromCache bool) {
	p.mu.Lock()
	recordFetch(p.stage("download"), name, bytes, fromCache)
	p.mu.Unlock()
}
func (p *plainProgress) BeginFetch(n int)        { p.BeginStage("download", n) }
func (p *plainProgress) IncFetch(label string)   { p.advance("download", label) }
func (p *plainProgress) EndFetch()               { p.EndStage("download", "") }
func (p *plainProgress) BeginExtract(n int)      { p.BeginStage("install", n) }
func (p *plainProgress) IncExtract(label string) { p.advance("install", label) }
func (p *plainProgress) EndExtract()             { p.EndStage("install", "") }
func (p *plainProgress) BeginResolve(n int)      { p.BeginStage("resolve", n) }
func (p *plainProgress) IncResolve(label string) { p.advance("resolve", label) }
func (p *plainProgress) EndResolve()             { p.EndStage("resolve", "") }
func (p *plainProgress) advance(name, label string) {
	p.mu.Lock()
	s := p.stage(name)
	if !s.started {
		s.started = true
		s.startedAt = time.Now()
	}
	s.current++
	s.label = label
	p.mu.Unlock()
}
func (p *plainProgress) Done(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failed {
		return
	}
	verb := "installed"
	if p.op == "update" {
		verb = "updated"
	}
	if n == 0 {
		fmt.Fprintf(p.w, "%s nothing to install\n", p.paint("32", "gomposer:"))
		return
	}
	fmt.Fprintf(p.w, "%s %s %d package%s in %s\n", p.paint("32", "gomposer:"), verb, n, plural(n), formatDuration(time.Since(p.startedAt)))
}
func (p *plainProgress) Fail(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failed {
		return
	}
	p.failed = true
	if name := activeStage(p.stages); name != "" {
		fmt.Fprintf(p.w, "%s %s failed\n", p.paint("31", "gomposer:"), name)
	}
	renderPlainFailure(p.w, err)
}

type ttyProgress struct {
	progressState
	color    bool
	width    int
	frame    int
	lastDraw time.Time
	done     chan struct{}
	stopOnce sync.Once
	animWG   sync.WaitGroup
}

// newTTYProgress preserves the small constructor used by focused renderer
// tests. Production construction goes through NewProgress.
func newTTYProgress(w io.Writer) *ttyProgress {
	p := newTTYProgressWithOptions(w, ProgressOptions{ForceTTY: true, Width: 80}, false)
	// Focused unit tests drive redraws synchronously; stop the background
	// animator so a test that intentionally omits Done cannot leak a goroutine.
	p.stop()
	return p
}

func newTTYProgressWithOptions(w io.Writer, opts ProgressOptions, color bool) *ttyProgress {
	width := opts.Width
	if width == 0 {
		if f, ok := w.(*os.File); ok {
			if n, _, err := term.GetSize(int(f.Fd())); err == nil {
				width = n
			}
		}
	}
	if width <= 0 {
		width = 80
	}
	p := &ttyProgress{progressState: newProgressState(w, opts), color: color, width: width, done: make(chan struct{})}
	op := p.op
	project := opts.ProjectDir
	if project != "" {
		project = filepath.Clean(project)
	}
	if project == "" {
		fmt.Fprintf(w, "%s\n\n", p.paint("1;36", "gomposer "+op))
	} else {
		fmt.Fprintf(w, "%s %s\n\n", p.paint("1;36", "gomposer "+op), p.paint("2", project))
	}
	p.animWG.Add(1)
	go p.animate()
	return p
}

func (p *ttyProgress) animate() {
	defer p.animWG.Done()
	t := time.NewTicker(80 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			p.mu.Lock()
			if p.activeLocked() != "" {
				p.frame++
				p.maybeDrawActiveLocked(true)
			}
			p.mu.Unlock()
		case <-p.done:
			return
		}
	}
}
func (p *ttyProgress) stop() {
	p.stopOnce.Do(func() { close(p.done) })
	p.animWG.Wait()
}
func (p *ttyProgress) paint(code, s string) string {
	if !p.color || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}
func (p *ttyProgress) clearLocked() { fmt.Fprint(p.w, "\r\x1b[2K") }
func (p *ttyProgress) BeginStage(name string, total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.stage(name)
	if s.started {
		return
	}
	s.started = true
	s.total = total
	s.startedAt = time.Now()
	p.maybeDrawActiveLocked(true)
}
func (p *ttyProgress) SetStageDetail(name, detail string) {
	p.mu.Lock()
	p.stage(name).detail = detail
	p.mu.Unlock()
}
func (p *ttyProgress) EndStage(name, detail string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.stage(name)
	if !s.started || s.ended {
		return
	}
	if detail != "" {
		s.detail = detail
	}
	s.ended = true
	s.endedAt = time.Now()
	p.flushLocked()
	p.maybeDrawActiveLocked(true)
}
func (p *ttyProgress) activeLocked() string {
	return activeStage(p.stages)
}

func activeStage(stages map[string]*stageState) string {
	for _, name := range stageOrder {
		if s := stages[name]; s != nil && s.started && !s.ended {
			return name
		}
	}
	return ""
}
func (p *ttyProgress) flushLocked() {
	for _, name := range stageOrder {
		s := p.stages[name]
		if s == nil || !s.started {
			continue
		}
		if !s.ended {
			return
		}
		if s.printed {
			continue
		}
		p.clearLocked()
		detail := phaseDetail(name, s)
		elapsed := formatDuration(s.endedAt.Sub(s.startedAt))
		fmt.Fprintf(p.w, "%s %-11s %-*s %s\n", p.paint("32", "✓"), stageLabels[name], max(1, p.width-30), truncate(detail, max(1, p.width-30)), p.paint("2", elapsed))
		s.printed = true
	}
}
func (p *ttyProgress) maybeDrawActiveLocked(force bool) {
	if !force && time.Since(p.lastDraw) < 50*time.Millisecond {
		return
	}
	name := p.activeLocked()
	if name == "" {
		return
	}
	s := p.stage(name)
	p.clearLocked()
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	icon := p.paint("36", frames[p.frame%len(frames)])
	count := ""
	if s.total > 0 {
		count = fmt.Sprintf("%d/%d", min(s.current, s.total), s.total)
	} else if s.current > 0 {
		count = fmt.Sprintf("%d", s.current)
	}
	bar := ""
	if p.width >= 72 && s.total > 0 {
		bar = " " + p.paint("36", renderBar(s.current, s.total, 14))
	}
	prefix := fmt.Sprintf("%s %-11s %s%s ", icon, stageLabels[name], count, bar)
	avail := p.width - visibleWidth(prefix)
	label := truncate(s.label, max(0, avail))
	fmt.Fprint(p.w, prefix, p.paint("2", label))
	p.lastDraw = time.Now()
}
func (p *ttyProgress) Warning(message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.warnings[message]; ok {
		return
	}
	p.warnings[message] = struct{}{}
	p.clearLocked()
	fmt.Fprintf(p.w, "%s %s\n", p.paint("33", "⚠"), message)
	p.maybeDrawActiveLocked(true)
}
func (p *ttyProgress) RecordFetch(name string, bytes int, fromCache bool) {
	p.mu.Lock()
	recordFetch(p.stage("download"), name, bytes, fromCache)
	p.mu.Unlock()
}
func (p *ttyProgress) BeginFetch(n int)    { p.BeginStage("download", n) }
func (p *ttyProgress) IncFetch(s string)   { p.advance("download", s) }
func (p *ttyProgress) EndFetch()           { p.EndStage("download", "") }
func (p *ttyProgress) BeginExtract(n int)  { p.BeginStage("install", n) }
func (p *ttyProgress) IncExtract(s string) { p.advance("install", s) }
func (p *ttyProgress) EndExtract()         { p.EndStage("install", "") }
func (p *ttyProgress) BeginResolve(n int)  { p.BeginStage("resolve", n) }
func (p *ttyProgress) IncResolve(s string) { p.advance("resolve", s) }
func (p *ttyProgress) EndResolve()         { p.EndStage("resolve", "") }
func (p *ttyProgress) advance(name, label string) {
	p.mu.Lock()
	s := p.stage(name)
	if !s.started {
		s.started = true
		s.startedAt = time.Now()
	}
	s.current++
	s.label = label
	p.maybeDrawActiveLocked(false)
	p.mu.Unlock()
}
func (p *ttyProgress) Done(n int) {
	p.stop()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failed {
		return
	}
	p.clearLocked()
	verb := "Installed"
	if p.op == "update" {
		verb = "Updated"
	}
	if n == 0 {
		fmt.Fprintf(p.w, "%s Nothing to install\n", p.paint("32", "✓"))
		return
	}
	fmt.Fprintf(p.w, "\n%s %s %d package%s in %s\n", p.paint("32", "✓"), verb, n, plural(n), formatDuration(time.Since(p.startedAt)))
}
func (p *ttyProgress) Fail(err error) {
	p.stop()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failed {
		return
	}
	p.failed = true
	p.clearLocked()
	name := p.activeLocked()
	if name != "" {
		s := p.stage(name)
		fmt.Fprintf(p.w, "%s %s failed after %s\n\n", p.paint("31", "✗"), stageLabels[name], formatDuration(time.Since(s.startedAt)))
	}
	renderTTYFailure(p.w, p.paint, err)
}

func phaseDetail(name string, s *stageState) string {
	if s.detail != "" {
		return s.detail
	}
	switch name {
	case "resolve":
		n := s.current
		if s.total > 0 {
			n = s.total
		}
		if n > 0 {
			return fmt.Sprintf("%d packages", n)
		}
	case "download":
		downloaded := s.current - s.cacheHits
		if downloaded < 0 {
			downloaded = 0
		}
		parts := []string{fmt.Sprintf("%d downloaded", downloaded)}
		if s.cacheHits > 0 {
			parts = append(parts, fmt.Sprintf("%d cached", s.cacheHits))
		}
		if s.bytes > 0 {
			parts = append(parts, humanBytes(s.bytes))
		}
		return strings.Join(parts, " · ")
	case "install":
		n := s.current
		if s.total > 0 {
			n = s.total
		}
		return fmt.Sprintf("%d packages", n)
	}
	return "complete"
}

func recordFetch(s *stageState, name string, bytes int, fromCache bool) {
	if s.fetchOutcomes == nil {
		s.fetchOutcomes = make(map[string]bool)
	}
	prior, seen := s.fetchOutcomes[name]
	if seen {
		// A speculative download followed by an authoritative warm-store
		// verification is still one download, not one cache hit.
		if !prior || fromCache {
			return
		}
		s.cacheHits--
	} else if fromCache {
		s.cacheHits++
	}
	s.fetchOutcomes[name] = fromCache
	if !fromCache {
		s.bytes += int64(bytes)
	}
}
func renderBar(cur, total, width int) string {
	if total <= 0 {
		return "[" + strings.Repeat(" ", width) + "]"
	}
	cur = min(cur, total)
	filled := cur * width / total
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}
func visibleWidth(s string) int {
	for {
		i := strings.Index(s, "\x1b[")
		if i < 0 {
			break
		}
		j := strings.IndexByte(s[i:], 'm')
		if j < 0 {
			break
		}
		s = s[:i] + s[i+j+1:]
	}
	return utf8.RuneCountInString(s)
}
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}
func formatDuration(d time.Duration) string {
	if d < time.Millisecond {
		return "<1ms"
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return d.Round(10 * time.Millisecond).String()
}
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
