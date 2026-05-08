package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"
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
