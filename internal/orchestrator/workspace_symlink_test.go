package orchestrator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/torstendittmann/gomposer/internal/manifest"
)

// mustMkdirAll is a thin t.Helper() wrapper around os.MkdirAll for test setup.
func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func TestLinkWorkspacesCreatesVendorSymlinks(t *testing.T) {
	dir := t.TempDir()
	// Simulate a project with root vendor + one workspace with an existing
	// real vendor (which should get replaced).
	mustMkdirAll(t, filepath.Join(dir, "vendor"))
	mustMkdirAll(t, filepath.Join(dir, "packages", "shared", "vendor"))
	mustMkdirAll(t, filepath.Join(dir, "packages", "shared", "src"))
	mustMkdirAll(t, filepath.Join(dir, "vendor", "acme"))

	ws := []manifest.Workspace{{
		Name:     "acme/shared",
		Dir:      filepath.Join(dir, "packages", "shared"),
		Manifest: &manifest.Manifest{Name: "acme/shared", Version: "1.0.0"},
		Version:  "1.0.0",
	}}

	if err := linkWorkspaces(dir, ws); err != nil {
		t.Fatalf("linkWorkspaces: %v", err)
	}

	// packages/shared/vendor should now be a symlink → repo-root vendor.
	wsVendor := filepath.Join(dir, "packages", "shared", "vendor")
	target, err := os.Readlink(wsVendor)
	if err != nil {
		t.Fatalf("packages/shared/vendor is not a symlink: %v", err)
	}
	if target != filepath.Join("..", "..", "vendor") {
		t.Errorf("workspace vendor target = %q, want ../../vendor", target)
	}

	// vendor/acme/shared should be a symlink → workspace source dir.
	crossLink := filepath.Join(dir, "vendor", "acme", "shared")
	target, err = os.Readlink(crossLink)
	if err != nil {
		t.Fatalf("vendor/acme/shared is not a symlink: %v", err)
	}
	if target != filepath.Join("..", "..", "packages", "shared") {
		t.Errorf("cross-workspace link target = %q, want ../../packages/shared", target)
	}
}

func TestLinkWorkspacesIdempotent(t *testing.T) {
	dir := t.TempDir()
	mustMkdirAll(t, filepath.Join(dir, "vendor"))
	mustMkdirAll(t, filepath.Join(dir, "packages", "shared"))
	ws := []manifest.Workspace{{
		Name:     "acme/shared",
		Dir:      filepath.Join(dir, "packages", "shared"),
		Manifest: &manifest.Manifest{Name: "acme/shared"},
	}}
	if err := linkWorkspaces(dir, ws); err != nil {
		t.Fatal(err)
	}
	if err := linkWorkspaces(dir, ws); err != nil {
		t.Errorf("second run failed: %v", err)
	}
}
