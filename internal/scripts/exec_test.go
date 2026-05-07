package scripts

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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

func TestRunShellSetsComposerGoEnv(t *testing.T) {
	skipIfNoSh(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "env")
	r := New()
	err := r.Run(context.Background(), EventPostInstall, Options{
		ProjectDir: dir,
		Scripts: map[string][]string{
			"post-install-cmd": {`printf "%s" "$COMPOSER_GO" > ` + out},
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
		t.Errorf("COMPOSER_GO = %q, want 1", got)
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
	if os.Getenv("COMPOSER_GO_TEST_PHP") != "1" {
		t.Skip("set COMPOSER_GO_TEST_PHP=1 with php on PATH to run")
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
