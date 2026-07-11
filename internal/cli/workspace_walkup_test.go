package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindWorkspaceRootFindsAncestor(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "composer.json"), `{"name":"acme/monorepo","workspaces":["packages/*"]}`)
	writeFile(t, filepath.Join(dir, "packages", "shared", "composer.json"), `{"name":"acme/shared"}`)
	writeFile(t, filepath.Join(dir, "packages", "shared", "src", "Thing.php"), "<?php")

	got, ok := findWorkspaceRoot(filepath.Join(dir, "packages", "shared", "src"))
	if !ok {
		t.Fatalf("no workspace root found from packages/shared/src")
	}
	if abs, _ := filepath.EvalSymlinks(got); abs != resolveOrPanic(t, dir) {
		t.Errorf("got %q, want %q", got, dir)
	}
}

func TestFindWorkspaceRootStopsAtGitBoundary(t *testing.T) {
	dir := t.TempDir()
	// Inner dir has a .git; outer has a workspaces-declaring composer.json.
	// Walk from inner should NOT find the outer root because .git is a
	// project boundary.
	writeFile(t, filepath.Join(dir, "composer.json"), `{"name":"acme/monorepo","workspaces":["packages/*"]}`)
	mustMkdirAll(t, filepath.Join(dir, "unrelated", ".git"))
	writeFile(t, filepath.Join(dir, "unrelated", "composer.json"), `{"name":"other/thing"}`)

	got, ok := findWorkspaceRoot(filepath.Join(dir, "unrelated"))
	if ok {
		t.Errorf("expected no match, got %q", got)
	}
}

func TestFindWorkspaceRootReturnsFalseWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "composer.json"), `{"name":"acme/plain"}`)
	if _, ok := findWorkspaceRoot(dir); ok {
		t.Errorf("plain project matched as workspace root")
	}
}

// resolveOrPanic exists because macOS t.TempDir() may live under /private/var
// while EvalSymlinks resolves to /var.
func resolveOrPanic(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
