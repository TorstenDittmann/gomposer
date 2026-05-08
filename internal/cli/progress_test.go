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
