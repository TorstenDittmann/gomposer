package manifest

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestManifestParsesWorkspacesField(t *testing.T) {
	body := []byte(`{"name":"acme/monorepo","workspaces":["packages/*","apps/*"]}`)
	m, err := Parse(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"packages/*", "apps/*"}
	if len(m.Workspaces) != len(want) {
		t.Fatalf("Workspaces = %v, want %v", m.Workspaces, want)
	}
	for i := range want {
		if m.Workspaces[i] != want[i] {
			t.Errorf("Workspaces[%d] = %q, want %q", i, m.Workspaces[i], want[i])
		}
	}
}

func TestDiscoverWorkspacesFindsAll(t *testing.T) {
	rootDir := filepath.Join("testdata", "workspaces-simple")
	root, err := Load(rootDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, err := DiscoverWorkspaces(rootDir, root, nil)
	if err != nil {
		t.Fatalf("DiscoverWorkspaces: %v", err)
	}
	sort.Slice(got, func(i, j int) bool { return got[i].Name < got[j].Name })
	if len(got) != 2 {
		t.Fatalf("got %d workspaces, want 2", len(got))
	}
	if got[0].Name != "acme/api" || got[1].Name != "acme/shared" {
		t.Errorf("names = %v", []string{got[0].Name, got[1].Name})
	}
	if got[1].Version != "1.0.0" {
		t.Errorf("shared.Version = %q, want 1.0.0", got[1].Version)
	}
	if !strings.HasSuffix(filepath.ToSlash(got[1].Dir), "packages/shared") {
		t.Errorf("shared.Dir = %q, want …/packages/shared", got[1].Dir)
	}
}

func TestDiscoverWorkspacesEmptyGlobWarns(t *testing.T) {
	// Root manifest with a glob that matches zero dirs. Warning to warnf; no
	// workspaces returned; no error.
	root := &Manifest{Workspaces: []string{"nowhere/*"}}
	var warnings []string
	got, err := DiscoverWorkspaces(t.TempDir(), root, func(format string, args ...any) {
		warnings = append(warnings, format)
	})
	if err != nil {
		t.Fatalf("DiscoverWorkspaces: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d workspaces, want 0", len(got))
	}
	if len(warnings) != 1 {
		t.Errorf("warnings = %v, want 1 warning", warnings)
	}
}

func TestDiscoverWorkspacesDuplicateNameHardFails(t *testing.T) {
	dir := t.TempDir()
	mkComposer := func(rel, name string) {
		abs := filepath.Join(dir, rel)
		if err := writeFile(t, abs, `{"name":"`+name+`"}`); err != nil {
			t.Fatal(err)
		}
	}
	mkComposer("packages/a/composer.json", "acme/thing")
	mkComposer("packages/b/composer.json", "acme/thing")
	root := &Manifest{Workspaces: []string{"packages/*"}}
	_, err := DiscoverWorkspaces(dir, root, nil)
	if err == nil {
		t.Fatal("expected error on duplicate name")
	}
	if !strings.Contains(err.Error(), "duplicate name") {
		t.Errorf("err = %v", err)
	}
}

func TestDiscoverWorkspacesEmptyArrayShortCircuits(t *testing.T) {
	root := &Manifest{Workspaces: []string{}}
	got, err := DiscoverWorkspaces(t.TempDir(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestDiscoverWorkspacesRejectsEmptyName(t *testing.T) {
	dir := t.TempDir()
	// Workspace with no "name" field — must fail.
	if err := writeFile(t, filepath.Join(dir, "packages", "nameless", "composer.json"), `{}`); err != nil {
		t.Fatal(err)
	}
	root := &Manifest{Workspaces: []string{"packages/*"}}
	_, err := DiscoverWorkspaces(dir, root, nil)
	if err == nil {
		t.Fatal("expected error on workspace with empty name")
	}
	if !strings.Contains(err.Error(), "has no name") {
		t.Errorf("err = %v — should mention the missing name", err)
	}
}

// writeFile creates parent dirs and writes body to path.
func writeFile(t *testing.T, path, body string) error {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}
