package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/torstendittmann/gomposer/internal/scripts"
)

type recordingNamedRunner struct {
	name string
	opts scripts.Options
	err  error
}

func (r *recordingNamedRunner) RunNamed(_ context.Context, name string, opts scripts.Options) error {
	r.name = name
	r.opts = opts
	return r.err
}

func TestRunScriptRequiresProjectDir(t *testing.T) {
	err := RunScript(context.Background(), ScriptCommand{Name: "test"})
	if err == nil || !strings.Contains(err.Error(), "ProjectDir is required") {
		t.Fatalf("error = %v, want ProjectDir required", err)
	}
}

func TestRunScriptRequiresName(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `{"name":"acme/app","scripts":{"test":"true"}}`)
	err := RunScript(context.Background(), ScriptCommand{ProjectDir: dir})
	if err == nil || !strings.Contains(err.Error(), "script name is required") {
		t.Fatalf("error = %v, want name required", err)
	}
}

func TestRunScriptRequiresManifest(t *testing.T) {
	err := RunScript(context.Background(), ScriptCommand{ProjectDir: t.TempDir(), Name: "test"})
	if err == nil || !strings.Contains(err.Error(), "read manifest") {
		t.Fatalf("error = %v, want missing manifest", err)
	}
}

func TestRunScriptUnknownName(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `{"name":"acme/app","scripts":{"test":"true"}}`)
	err := RunScript(context.Background(), ScriptCommand{ProjectDir: dir, Name: "missing"})
	if !errors.Is(err, scripts.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestRunScriptPassesOptions(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `{
		"name":"acme/app",
		"scripts":{"test":"phpunit"}
	}`)
	rec := &recordingNamedRunner{}
	err := RunScript(context.Background(), ScriptCommand{
		ProjectDir: dir,
		Name:       "test",
		ExtraArgs:  []string{"--filter=Foo"},
		Timeout:    5 * time.Second,
		Verbose:    true,
		Runner:     rec,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.name != "test" {
		t.Errorf("name = %q", rec.name)
	}
	if rec.opts.ProjectDir != dir {
		t.Errorf("ProjectDir = %q, want %q", rec.opts.ProjectDir, dir)
	}
	if !reflect.DeepEqual(rec.opts.ExtraArgs, []string{"--filter=Foo"}) {
		t.Errorf("ExtraArgs = %v", rec.opts.ExtraArgs)
	}
	if rec.opts.Timeout != 5*time.Second {
		t.Errorf("Timeout = %s", rec.opts.Timeout)
	}
	if !rec.opts.Verbose {
		t.Error("Verbose not forwarded")
	}
	if got := rec.opts.Scripts["test"]; len(got) != 1 || got[0] != "phpunit" {
		t.Errorf("Scripts[test] = %v", rec.opts.Scripts["test"])
	}
}

func TestRunScriptUsesNearestManifest(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `{"name":"acme/monorepo","workspaces":["packages/*"],"scripts":{"root":"echo root"}}`)
	member := filepath.Join(dir, "packages", "shared")
	if err := os.MkdirAll(member, 0o755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, member, `{"name":"acme/shared","version":"1.0.0","scripts":{"test":"echo member"}}`)
	nested := filepath.Join(member, "src")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	rec := &recordingNamedRunner{}
	if err := RunScript(context.Background(), ScriptCommand{
		ProjectDir: nested,
		Name:       "test",
		Runner:     rec,
	}); err != nil {
		t.Fatal(err)
	}
	if rec.name != "test" {
		t.Errorf("name = %q, want test (member script)", rec.name)
	}
	if rec.opts.ProjectDir != member {
		t.Errorf("ProjectDir = %q, want member %q", rec.opts.ProjectDir, member)
	}
	if _, ok := rec.opts.Scripts["root"]; ok {
		t.Error("selected the root manifest instead of the member")
	}
}

func TestRunScriptRootUsesRootManifest(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `{"name":"acme/monorepo","workspaces":["packages/*"],"scripts":{"root":"echo root"}}`)
	member := filepath.Join(dir, "packages", "shared")
	if err := os.MkdirAll(member, 0o755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, member, `{"name":"acme/shared","version":"1.0.0","scripts":{"test":"echo member"}}`)

	rec := &recordingNamedRunner{}
	if err := RunScript(context.Background(), ScriptCommand{
		ProjectDir: dir,
		Name:       "root",
		Runner:     rec,
	}); err != nil {
		t.Fatal(err)
	}
	if rec.opts.ProjectDir != dir {
		t.Errorf("ProjectDir = %q, want root", rec.opts.ProjectDir)
	}
	if _, ok := rec.opts.Scripts["test"]; ok {
		t.Error("selected the member manifest instead of the root")
	}
}

func TestListScriptsSorted(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `{"name":"acme/app","scripts":{"zeta":"true","alpha":"true","post-install-cmd":"true"}}`)
	got, err := ListScripts(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha", "post-install-cmd", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListScripts = %v, want %v", got, want)
	}
}

func TestListScriptsEmpty(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `{"name":"acme/app"}`)
	got, err := ListScripts(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("ListScripts = %v, want empty", got)
	}
}
