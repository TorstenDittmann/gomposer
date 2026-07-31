package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPlainProgressPrintsStablePhaseLines(t *testing.T) {
	var out bytes.Buffer
	p := newPlainProgress(&out, ProgressOptions{Operation: "install", ProjectDir: "/work/app"})
	p.BeginStage("prepare", 0)
	p.EndStage("prepare", "ready")
	p.BeginResolve(0)
	p.IncResolve("psr/log")
	p.SetStageDetail("resolve", "1 package · cached")
	p.EndResolve()
	p.BeginFetch(1)
	p.RecordFetch("psr/log", 2048, false)
	p.IncFetch("psr/log 3.0.0")
	p.EndFetch()
	p.Done(1)

	got := out.String()
	for _, want := range []string{
		"gomposer: prepare: ready",
		"gomposer: resolve: 1 package · cached",
		"gomposer: download: 1 downloaded · 2.0 kB",
		"gomposer: installed 1 package",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.ContainsAny(got, "\r\x1b") {
		t.Fatalf("plain output contains terminal control bytes: %q", got)
	}
}

func TestTTYProgressBuffersDownloadBehindResolve(t *testing.T) {
	var out bytes.Buffer
	p := newTTYProgressWithOptions(&out, ProgressOptions{ForceTTY: true, Width: 100, Operation: "install"}, false)
	p.BeginStage("resolve", 0)
	p.BeginFetch(2)
	p.IncFetch("a/a 1.0.0")
	p.EndStage("resolve", "2 packages")
	p.IncFetch("b/b 1.0.0")
	p.EndFetch()
	p.Done(2)

	got := out.String()
	resolveDone := strings.Index(got, "✓ Resolve")
	downloadDone := strings.Index(got, "✓ Download")
	if resolveDone < 0 || downloadDone < 0 || resolveDone > downloadDone {
		t.Fatalf("completed rows out of order:\n%s", got)
	}
}

func TestTTYProgressWarningDeduplicatesAndRedraws(t *testing.T) {
	var out bytes.Buffer
	p := newTTYProgressWithOptions(&out, ProgressOptions{ForceTTY: true, Width: 80}, false)
	p.BeginStage("resolve", 0)
	p.Warning("plugin execution is disabled")
	p.Warning("plugin execution is disabled")
	p.EndStage("resolve", "1 package")
	p.Done(1)
	if got := strings.Count(out.String(), "plugin execution is disabled"); got != 1 {
		t.Fatalf("warning rendered %d times:\n%s", got, out.String())
	}
	if !strings.Contains(out.String(), "\r\x1b[2K") {
		t.Fatalf("warning did not clear active row: %q", out.String())
	}
}

func TestColorPolicy(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if colorEnabled(ColorAuto, true) {
		t.Fatal("NO_COLOR should disable auto color")
	}
	if !colorEnabled(ColorAlways, false) {
		t.Fatal("always should force color")
	}
	if colorEnabled(ColorNever, true) {
		t.Fatal("never should disable color")
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	if colorEnabled(ColorAuto, true) {
		t.Fatal("TERM=dumb should disable auto color")
	}
}

func TestColorAlwaysStylesRedirectedOutputWithoutAnimation(t *testing.T) {
	var out bytes.Buffer
	p := newPlainProgress(&out, ProgressOptions{Color: ColorAlways})
	p.BeginStage("prepare", 0)
	p.EndStage("prepare", "ready")
	got := out.String()
	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("forced color missing from redirected output: %q", got)
	}
	if strings.Contains(got, "\r") || strings.Contains(got, "⠋") {
		t.Fatalf("redirected output animated under forced color: %q", got)
	}
}

func TestQuietProgressStillPrintsFailures(t *testing.T) {
	var out bytes.Buffer
	p := newNoopProgress(&out)
	p.Fail(context.Canceled)
	if got := out.String(); !strings.Contains(got, "Cancelled") {
		t.Fatalf("quiet failure missing: %q", got)
	}
}

func TestExitCodeCancellation(t *testing.T) {
	if got := ExitCode(markHandled(context.Canceled)); got != 130 {
		t.Fatalf("ExitCode(cancel) = %d, want 130", got)
	}
	if got := ExitCode(errors.New("boom")); got != 1 {
		t.Fatalf("ExitCode(error) = %d, want 1", got)
	}
}

func TestInvalidColorValue(t *testing.T) {
	flagColor = string(ColorAuto)
	root := newRootCmd("dev")
	root.SetArgs([]string{"cache", "--color=rainbow"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "invalid --color") {
		t.Fatalf("err = %v, want invalid --color", err)
	}
}
