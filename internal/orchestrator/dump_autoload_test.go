package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/torstendittmann/gomposer/internal/lock"
	"github.com/torstendittmann/gomposer/internal/scripts"
)

func writeDumpLock(t *testing.T, dir string, f *lock.File) {
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

func TestDumpAutoloadRequiresLockfile(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `{"name":"acme/app"}`)
	err := DumpAutoload(context.Background(), Options{ProjectDir: dir, NoScripts: true})
	if err == nil || !strings.Contains(err.Error(), "gomposer.lock not found") {
		t.Fatalf("error = %v, want lock missing", err)
	}
}

func TestDumpAutoloadRequiresManifest(t *testing.T) {
	err := DumpAutoload(context.Background(), Options{ProjectDir: t.TempDir(), NoScripts: true})
	if err == nil || !strings.Contains(err.Error(), "read manifest") {
		t.Fatalf("error = %v, want missing manifest", err)
	}
}

func TestDumpAutoloadInvokesGeneratorFromLock(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `{"name":"acme/app"}`)
	writeDumpLock(t, dir, &lock.File{
		Packages:    []lock.Package{{Name: "acme/prod", Version: "1.0.0"}},
		PackagesDev: []lock.Package{{Name: "acme/dev", Version: "2.0.0"}},
	})
	gen := &fakeAutoloader{}
	if err := DumpAutoload(context.Background(), Options{
		ProjectDir: dir,
		Autoloader: gen,
		NoScripts:  true,
	}); err != nil {
		t.Fatalf("DumpAutoload: %v", err)
	}
	if gen.called != 1 {
		t.Errorf("called %d times, want 1", gen.called)
	}
	if gen.gotPackages != 2 {
		t.Errorf("packages = %d, want 2", gen.gotPackages)
	}
}

func TestDumpAutoloadNoDevOmitsDevPackages(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `{"name":"acme/app"}`)
	writeDumpLock(t, dir, &lock.File{
		Packages:    []lock.Package{{Name: "acme/prod", Version: "1.0.0"}},
		PackagesDev: []lock.Package{{Name: "acme/dev", Version: "2.0.0"}},
	})
	gen := &fakeAutoloader{}
	if err := DumpAutoload(context.Background(), Options{
		ProjectDir: dir,
		Autoloader: gen,
		NoDev:      true,
		NoScripts:  true,
	}); err != nil {
		t.Fatalf("DumpAutoload: %v", err)
	}
	if gen.gotPackages != 1 {
		t.Errorf("packages = %d, want 1", gen.gotPackages)
	}
}

func TestDumpAutoloadFiresDumpScripts(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `{
		"name":"acme/app",
		"scripts":{
			"pre-autoload-dump":"echo pre",
			"post-autoload-dump":"echo post"
		}
	}`)
	writeDumpLock(t, dir, &lock.File{})
	rec := &recordingRunner{}
	if err := DumpAutoload(context.Background(), Options{
		ProjectDir: dir,
		Autoloader: &fakeAutoloader{},
		Scripts:    rec,
	}); err != nil {
		t.Fatalf("DumpAutoload: %v", err)
	}
	want := []scripts.Event{scripts.EventPreAutoloadDump, scripts.EventPostAutoloadDump}
	if got := rec.seen(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("events = %v, want %v", got, want)
	}
}

func TestDumpAutoloadNoScriptsSkipsDumpHooks(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `{
		"name":"acme/app",
		"scripts":{"pre-autoload-dump":"echo pre","post-autoload-dump":"echo post"}
	}`)
	writeDumpLock(t, dir, &lock.File{})
	rec := &recordingRunner{}
	if err := DumpAutoload(context.Background(), Options{
		ProjectDir: dir,
		Autoloader: &fakeAutoloader{},
		Scripts:    rec,
		NoScripts:  true,
	}); err != nil {
		t.Fatalf("DumpAutoload: %v", err)
	}
	if got := rec.seen(); len(got) != 0 {
		t.Errorf("expected zero events with NoScripts, got %v", got)
	}
}

func TestDumpAutoloadDoesNotRewriteLock(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `{"name":"acme/app"}`)
	lockPath := filepath.Join(dir, "gomposer.lock")
	writeDumpLock(t, dir, &lock.File{Packages: []lock.Package{{Name: "acme/prod", Version: "1.0.0"}}})
	before, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := DumpAutoload(context.Background(), Options{
		ProjectDir: dir,
		Autoloader: &fakeAutoloader{},
		NoScripts:  true,
	}); err != nil {
		t.Fatalf("DumpAutoload: %v", err)
	}
	after, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("lockfile changed:\nbefore=%s\nafter=%s", before, after)
	}
}
