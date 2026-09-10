package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/torstendittmann/gomposer/internal/orchestrator"
)

func executeRun(t *testing.T, args ...string) (string, error) {
	t.Helper()
	flagVerbose = false
	flagQuiet = false
	var out bytes.Buffer
	cmd := newRootCmd("dev")
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"run"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func TestRunRequiresScriptName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"name":"acme/app"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := executeRun(t, "--project", dir)
	if err == nil || !strings.Contains(err.Error(), "script name is required") {
		t.Fatalf("error = %v, want script name required", err)
	}
}

func TestRunRejectsNegativeTimeout(t *testing.T) {
	_, err := executeRun(t, "--timeout", "-1", "test")
	if err == nil || !strings.Contains(err.Error(), "--timeout must be >= 0") {
		t.Fatalf("error = %v, want timeout validation", err)
	}
}

func TestRunListPrintsSortedNames(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{
  "name": "acme/app",
  "scripts": {
    "zeta": "true",
    "test": "phpunit",
    "post-install-cmd": "echo hi"
  }
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := executeRun(t, "--project", dir, "--list")
	if err != nil {
		t.Fatalf("run --list: %v\n%s", err, out)
	}
	got := strings.TrimSpace(out)
	want := "post-install-cmd\ntest\nzeta"
	if got != want {
		t.Fatalf("list output = %q, want %q", got, want)
	}
}

func TestRunListFailsWithoutManifest(t *testing.T) {
	_, err := executeRun(t, "--project", t.TempDir(), "--list")
	if err == nil || !strings.Contains(err.Error(), "read manifest") {
		t.Fatalf("error = %v, want missing manifest", err)
	}
}

func TestRunForwardsExtraArgsAndTimeout(t *testing.T) {
	orig := runScriptFn
	t.Cleanup(func() { runScriptFn = orig })
	var got orchestrator.ScriptCommand
	runScriptFn = func(_ context.Context, cmd orchestrator.ScriptCommand) error {
		got = cmd
		return nil
	}
	dir := t.TempDir()
	out, err := executeRun(t, "--project", dir, "--timeout", "12", "test", "--", "--filter=Foo", "bar")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if got.Name != "test" {
		t.Errorf("Name = %q", got.Name)
	}
	if got.Timeout != 12*time.Second {
		t.Errorf("Timeout = %s", got.Timeout)
	}
	if len(got.ExtraArgs) != 2 || got.ExtraArgs[0] != "--filter=Foo" || got.ExtraArgs[1] != "bar" {
		t.Errorf("ExtraArgs = %v", got.ExtraArgs)
	}
}

func TestRunTimeoutZeroMeansNoDeadline(t *testing.T) {
	orig := runScriptFn
	t.Cleanup(func() { runScriptFn = orig })
	var got orchestrator.ScriptCommand
	runScriptFn = func(_ context.Context, cmd orchestrator.ScriptCommand) error {
		got = cmd
		return nil
	}
	dir := t.TempDir()
	if _, err := executeRun(t, "--project", dir, "--timeout", "0", "test"); err != nil {
		t.Fatal(err)
	}
	if got.Timeout != 0 {
		t.Errorf("Timeout = %s, want 0", got.Timeout)
	}
}

func TestRunDefaultTimeoutIs300s(t *testing.T) {
	orig := runScriptFn
	t.Cleanup(func() { runScriptFn = orig })
	var got orchestrator.ScriptCommand
	runScriptFn = func(_ context.Context, cmd orchestrator.ScriptCommand) error {
		got = cmd
		return nil
	}
	dir := t.TempDir()
	if _, err := executeRun(t, "--project", dir, "test"); err != nil {
		t.Fatal(err)
	}
	if got.Timeout != 300*time.Second {
		t.Errorf("Timeout = %s, want 300s", got.Timeout)
	}
}

func TestRunExecutesNamedScript(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "ok")
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{
  "name": "acme/app",
  "scripts": { "mark": "touch ok" }
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := executeRun(t, "--project", dir, "--timeout", "10", "mark")
	if err != nil {
		t.Fatalf("run mark: %v\n%s", err, out)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("script did not run: %v", err)
	}
}

func TestRunExtraArgsReachShell(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "from-cli")
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{
  "name": "acme/app",
  "scripts": { "mark": "touch" }
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := executeRun(t, "--project", dir, "--timeout", "10", "mark", "--", sentinel)
	if err != nil {
		t.Fatalf("run mark -- args: %v\n%s", err, out)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("extra args did not reach touch: %v", err)
	}
}

func TestRunFromWorkspaceMemberUsesMemberScripts(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{
  "name": "acme/monorepo",
  "workspaces": ["packages/*"],
  "scripts": { "root": "touch root.txt" }
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	member := filepath.Join(dir, "packages", "shared")
	if err := os.MkdirAll(member, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(member, "composer.json"), []byte(`{
  "name": "acme/shared",
  "version": "1.0.0",
  "scripts": { "test": "touch member.txt" }
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := executeRun(t, "--project", member, "--timeout", "10", "test")
	if err != nil {
		t.Fatalf("run from member: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(member, "member.txt")); err != nil {
		t.Fatalf("member script did not run in member dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "root.txt")); err == nil {
		t.Fatal("root script ran when invoked from a member")
	}
}

func TestRunFromWorkspaceRootUsesRootScripts(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{
  "name": "acme/monorepo",
  "workspaces": ["packages/*"],
  "scripts": { "root": "touch root.txt" }
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	member := filepath.Join(dir, "packages", "shared")
	if err := os.MkdirAll(member, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(member, "composer.json"), []byte(`{
  "name": "acme/shared",
  "version": "1.0.0",
  "scripts": { "test": "touch member.txt" }
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := executeRun(t, "--project", dir, "--timeout", "10", "root")
	if err != nil {
		t.Fatalf("run from root: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "root.txt")); err != nil {
		t.Fatalf("root script did not run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(member, "member.txt")); err == nil {
		t.Fatal("member script ran when invoked from the root")
	}
}

func TestRunScriptAlias(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{
  "name": "acme/app",
  "scripts": { "mark": "touch ok" }
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	flagVerbose = false
	flagQuiet = false
	var out bytes.Buffer
	cmd := newRootCmd("dev")
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"run-script", "--project", dir, "--timeout", "10", "mark"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("run-script alias: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "ok")); err != nil {
		t.Fatalf("alias did not run script: %v", err)
	}
}

func TestRunUnknownScript(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"name":"acme/app","scripts":{"test":"true"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := executeRun(t, "--project", dir, "missing")
	if err == nil || !strings.Contains(err.Error(), "script not found") {
		t.Fatalf("error = %v, want not found", err)
	}
}
