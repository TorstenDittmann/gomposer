package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/torstendittmann/gomposer/internal/lock"
)

func executeDumpAutoload(t *testing.T, args ...string) (string, error) {
	t.Helper()
	flagNoDev = false
	flagNoScripts = false
	flagQuiet = false
	flagVerbose = false
	var out bytes.Buffer
	cmd := newRootCmd("dev")
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"dump-autoload"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func writeDumpAutoloadLock(t *testing.T, dir string, f *lock.File) {
	t.Helper()
	if f.SchemaVersion == 0 {
		f.SchemaVersion = lock.SchemaVersion
	}
	body, err := f.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gomposer.lock"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeDumpClass(t *testing.T, path, ns, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "<?php\n\nnamespace " + ns + ";\n\nclass " + name + " {}\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func seedDumpAutoloadProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{
  "name": "acme/app",
  "autoload": { "classmap": ["src/"] },
  "autoload-dev": { "classmap": ["tests/"] },
  "scripts": {
    "pre-autoload-dump": "echo pre > dump-pre.txt",
    "post-autoload-dump": "echo post > dump-post.txt"
  }
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeDumpClass(t, filepath.Join(dir, "src", "App.php"), "Acme", "App")
	writeDumpClass(t, filepath.Join(dir, "tests", "AppTest.php"), "Acme", "AppTest")
	writeDumpAutoloadLock(t, dir, &lock.File{})
	return dir
}

func TestDumpAutoloadFailsWithoutLock(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"name":"acme/app"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := executeDumpAutoload(t, "--project", dir)
	if err == nil || !strings.Contains(err.Error(), "gomposer.lock not found") {
		t.Fatalf("error = %v, want missing lock", err)
	}
}

func TestDumpAutoloadRegeneratesClassmapWithoutInstall(t *testing.T) {
	dir := seedDumpAutoloadProject(t)
	out, err := executeDumpAutoload(t, "--project", dir)
	if err != nil {
		t.Fatalf("dump-autoload: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Generated autoload files") {
		t.Fatalf("missing success line:\n%s", out)
	}
	classmap := filepath.Join(dir, "vendor", "composer", "autoload_classmap.php")
	body, err := os.ReadFile(classmap)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `Acme\\App`) {
		t.Fatalf("classmap missing App:\n%s", body)
	}

	writeDumpClass(t, filepath.Join(dir, "src", "NewThing.php"), "Acme", "NewThing")
	if _, err := executeDumpAutoload(t, "--project", dir, "--quiet"); err != nil {
		t.Fatalf("second dump-autoload: %v", err)
	}
	body, err = os.ReadFile(classmap)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `Acme\\NewThing`) {
		t.Fatalf("classmap missing NewThing after dump:\n%s", body)
	}
}

func TestDumpAutoloadNoDevOmitsAutoloadDev(t *testing.T) {
	dir := seedDumpAutoloadProject(t)
	if _, err := executeDumpAutoload(t, "--project", dir, "--no-dev", "--no-scripts"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "vendor", "composer", "autoload_classmap.php"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if !strings.Contains(got, `Acme\\App`) {
		t.Fatalf("production class missing:\n%s", got)
	}
	if strings.Contains(got, `Acme\\AppTest`) {
		t.Fatalf("--no-dev included autoload-dev class:\n%s", got)
	}
}

func TestDumpAutoloadNoScriptsSkipsDumpHooks(t *testing.T) {
	dir := seedDumpAutoloadProject(t)
	if _, err := executeDumpAutoload(t, "--project", dir, "--no-scripts"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "dump-pre.txt")); err == nil {
		t.Fatal("pre-autoload-dump script ran under --no-scripts")
	}
	if _, err := os.Stat(filepath.Join(dir, "dump-post.txt")); err == nil {
		t.Fatal("post-autoload-dump script ran under --no-scripts")
	}
}

func TestDumpAutoloadRunsDumpHooksByDefault(t *testing.T) {
	dir := seedDumpAutoloadProject(t)
	if _, err := executeDumpAutoload(t, "--project", dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "dump-pre.txt")); err != nil {
		t.Fatalf("pre-autoload-dump did not run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "dump-post.txt")); err != nil {
		t.Fatalf("post-autoload-dump did not run: %v", err)
	}
}

func TestDumpAutoloadQuietSuppressesSuccessLine(t *testing.T) {
	dir := seedDumpAutoloadProject(t)
	out, err := executeDumpAutoload(t, "--project", dir, "--quiet", "--no-scripts")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Generated autoload files") {
		t.Fatalf("--quiet still printed success:\n%s", out)
	}
}

func TestDumpAutoloadFromWorkspaceMemberUsesRootVendor(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{
  "name": "acme/monorepo",
  "workspaces": ["packages/*"],
  "autoload": { "classmap": ["src/"] }
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeDumpClass(t, filepath.Join(dir, "src", "Root.php"), "Acme", "Root")
	member := filepath.Join(dir, "packages", "shared")
	if err := os.MkdirAll(member, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(member, "composer.json"), []byte(`{"name":"acme/shared","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeDumpAutoloadLock(t, dir, &lock.File{
		Packages: []lock.Package{{
			Name:    "acme/shared",
			Version: "1.0.0",
			Type:    "workspace",
			Source:  lock.Source{Type: "path", URL: "packages/shared"},
		}},
	})

	out, err := executeDumpAutoload(t, "--project", member, "--no-scripts")
	if err != nil {
		t.Fatalf("dump-autoload from member: %v\n%s", err, out)
	}
	classmap := filepath.Join(dir, "vendor", "composer", "autoload_classmap.php")
	body, err := os.ReadFile(classmap)
	if err != nil {
		t.Fatalf("root vendor classmap missing: %v", err)
	}
	if !strings.Contains(string(body), `Acme\\Root`) {
		t.Fatalf("root classmap missing Root:\n%s", body)
	}
	if _, err := os.Stat(filepath.Join(member, "vendor")); err == nil {
		t.Fatal("dump-autoload wrote vendor/ inside the workspace member")
	}
}

func TestDumpAutoloadAliasIsDumpautoload(t *testing.T) {
	dir := seedDumpAutoloadProject(t)
	flagNoDev = false
	flagNoScripts = true
	flagQuiet = true
	flagVerbose = false
	var out bytes.Buffer
	cmd := newRootCmd("dev")
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"dumpautoload", "--project", dir, "--quiet", "--no-scripts"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("dumpautoload alias: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "vendor", "autoload.php")); err != nil {
		t.Fatalf("alias did not generate autoload: %v", err)
	}
}
