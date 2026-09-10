package scripts

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func skipIfNoSh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh not available on windows; stage-4 will add cmd.exe support")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not in PATH")
	}
}

func TestRunShellSucceeds(t *testing.T) {
	skipIfNoSh(t)
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "ok")
	r := New()
	err := r.Run(context.Background(), EventPostInstall, Options{
		ProjectDir: dir,
		Scripts: map[string][]string{
			"post-install-cmd": {"touch " + sentinel},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("sentinel not created: %v", err)
	}
}

func TestRunShellSequenceFailFast(t *testing.T) {
	skipIfNoSh(t)
	dir := t.TempDir()
	first := filepath.Join(dir, "first")
	never := filepath.Join(dir, "never")
	r := New()
	err := r.Run(context.Background(), EventPostInstall, Options{
		ProjectDir: dir,
		Scripts: map[string][]string{
			"post-install-cmd": {
				"touch " + first,
				"exit 7",
				"touch " + never,
			},
		},
	})
	if err == nil {
		t.Fatal("expected error from exit 7")
	}
	if _, err := os.Stat(first); err != nil {
		t.Errorf("first sentinel should exist: %v", err)
	}
	if _, err := os.Stat(never); err == nil {
		t.Error("never sentinel must NOT exist (fail-fast)")
	}
}

func TestRunShellSetsGomposerEnv(t *testing.T) {
	skipIfNoSh(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "env")
	r := New()
	err := r.Run(context.Background(), EventPostInstall, Options{
		ProjectDir: dir,
		Scripts: map[string][]string{
			"post-install-cmd": {`printf "%s" "$GOMPOSER" > ` + out},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "1" {
		t.Errorf("GOMPOSER = %q, want 1", got)
	}
}

func TestRunShellWorkingDir(t *testing.T) {
	skipIfNoSh(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "pwd")
	r := New()
	err := r.Run(context.Background(), EventPostInstall, Options{
		ProjectDir: dir,
		Scripts: map[string][]string{
			"post-install-cmd": {"pwd > " + out},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	// On macOS /tmp is symlinked to /private/tmp; resolve before comparing.
	wantResolved, _ := filepath.EvalSymlinks(dir)
	gotResolved, _ := filepath.EvalSymlinks(strings.TrimSpace(string(got)))
	if gotResolved != wantResolved {
		t.Errorf("pwd = %q, want %q", gotResolved, wantResolved)
	}
}

func TestRunErrorRedactsBody(t *testing.T) {
	skipIfNoSh(t)
	dir := t.TempDir()
	long := strings.Repeat("z", 300) + " ; exit 1"
	r := New()
	err := r.Run(context.Background(), EventPostInstall, Options{
		ProjectDir: dir,
		Scripts: map[string][]string{
			"post-install-cmd": {long},
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "post-install-cmd") {
		t.Errorf("error missing event name: %q", msg)
	}
	if strings.Contains(msg, strings.Repeat("z", 200)) {
		t.Errorf("error contains unredacted body: %q", msg)
	}
}

func TestRunRefCycleDetected(t *testing.T) {
	dir := t.TempDir()
	r := New()
	err := r.Run(context.Background(), EventPostInstall, Options{
		ProjectDir: dir,
		Scripts: map[string][]string{
			"post-install-cmd": {"@a"},
			"a":                {"@b"},
			"b":                {"@a"},
		},
	})
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error should mention cycle: %v", err)
	}
}

func TestRunRefUnknown(t *testing.T) {
	dir := t.TempDir()
	r := New()
	err := r.Run(context.Background(), EventPostInstall, Options{
		ProjectDir: dir,
		Scripts:    map[string][]string{"post-install-cmd": {"@nope"}},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown ref") {
		t.Errorf("expected unknown-ref error, got %v", err)
	}
}

func TestRunRefExecutesIndirect(t *testing.T) {
	skipIfNoSh(t)
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "ok")
	r := New()
	err := r.Run(context.Background(), EventPostInstall, Options{
		ProjectDir: dir,
		Scripts: map[string][]string{
			"post-install-cmd": {"@build"},
			"build":            {"touch " + sentinel},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("sentinel from @build ref not created: %v", err)
	}
}

func TestRunPHPCallable(t *testing.T) {
	if os.Getenv("GOMPOSER_TEST_PHP") != "1" {
		t.Skip("set GOMPOSER_TEST_PHP=1 with php on PATH to run")
	}
	if _, err := exec.LookPath("php"); err != nil {
		t.Skip("php not in PATH")
	}
	dir := t.TempDir()
	// Minimal vendor/autoload.php that defines App\Hook::run writing a sentinel.
	if err := os.MkdirAll(filepath.Join(dir, "vendor"), 0o755); err != nil {
		t.Fatal(err)
	}
	autoload := `<?php
namespace App;
class Hook { public static function run() { file_put_contents(__DIR__ . '/../ok', '1'); } }
`
	if err := os.WriteFile(filepath.Join(dir, "vendor", "autoload.php"), []byte(autoload), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New()
	err := r.Run(context.Background(), EventPostInstall, Options{
		ProjectDir: dir,
		Scripts:    map[string][]string{"post-install-cmd": {`App\Hook::run`}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ok")); err != nil {
		t.Errorf("php callable did not run: %v", err)
	}
}

func TestRunNoEventIsNoop(t *testing.T) {
	r := New()
	err := r.Run(context.Background(), EventPostInstall, Options{
		ProjectDir: t.TempDir(),
		Scripts:    map[string][]string{},
	})
	if err != nil {
		t.Errorf("empty scripts map should be a no-op, got %v", err)
	}
}

// TestVerboseAnnouncesEvent is a regression guard: ensures the verbose path
// in runShell does not panic or error when Verbose=true. Visual verification
// (the "> <body>" prefix appearing on stderr) is covered in the plan's
// stage-2 acceptance smoke test.
func TestRunNamedMissingIsError(t *testing.T) {
	r := New()
	err := r.RunNamed(context.Background(), "test", Options{
		ProjectDir: t.TempDir(),
		Scripts:    map[string][]string{"other": {"true"}},
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestRunNamedEmptyBodyIsNoop(t *testing.T) {
	r := New()
	err := r.RunNamed(context.Background(), "test", Options{
		ProjectDir: t.TempDir(),
		Scripts:    map[string][]string{"test": {}},
	})
	if err != nil {
		t.Fatalf("empty present script should be a no-op, got %v", err)
	}
}

func TestRunNamedExtraArgsAppendedToShell(t *testing.T) {
	skipIfNoSh(t)
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "from-arg")
	spaced := filepath.Join(dir, "hello world")
	r := New()
	err := r.RunNamed(context.Background(), "mark", Options{
		ProjectDir: dir,
		Scripts:    map[string][]string{"mark": {"touch"}},
		ExtraArgs:  []string{sentinel, spaced},
	})
	if err != nil {
		t.Fatalf("RunNamed: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("extra arg was not appended to touch: %v", err)
	}
	if _, err := os.Stat(spaced); err != nil {
		t.Errorf("spaced extra arg was not quoted for touch: %v", err)
	}
}

func TestAppendArgsQuotesSpacesAndQuotes(t *testing.T) {
	got := appendArgs("cmd", []string{"hello world", "it's"})
	want := `cmd 'hello world' 'it'"'"'s'`
	if got != want {
		t.Errorf("appendArgs = %q, want %q", got, want)
	}
	if appendArgs("cmd", nil) != "cmd" {
		t.Errorf("empty args should be a no-op")
	}
}

func TestRunNamedTimeoutKillsProcess(t *testing.T) {
	skipIfNoSh(t)
	dir := t.TempDir()
	r := New()
	err := r.RunNamed(context.Background(), "sleep", Options{
		ProjectDir: dir,
		Scripts:    map[string][]string{"sleep": {"sleep 5"}},
		Timeout:    200 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v, want timeout", err)
	}
}

func TestRunNamedRefStillResolves(t *testing.T) {
	skipIfNoSh(t)
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "ok")
	r := New()
	err := r.RunNamed(context.Background(), "test", Options{
		ProjectDir: dir,
		Scripts: map[string][]string{
			"test":  {"@build"},
			"build": {"touch"},
		},
		ExtraArgs: []string{sentinel},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("sentinel from @build ref with extra args not created: %v", err)
	}
}

func TestVerboseAnnouncesEvent(t *testing.T) {
	skipIfNoSh(t)
	dir := t.TempDir()
	r := New()
	err := r.Run(context.Background(), EventPostInstall, Options{
		ProjectDir: dir,
		Verbose:    true,
		Scripts:    map[string][]string{"post-install-cmd": {"true"}},
	})
	if err != nil {
		t.Fatal(err)
	}
}
