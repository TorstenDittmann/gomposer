package cache

import (
	"path/filepath"
	"testing"
)

func TestRootHonoursXDG(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/tmp/xdg-cache")
	t.Setenv("HOME", "/home/u")
	got, err := Root()
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join("/tmp/xdg-cache", "composer-go") {
		t.Errorf("Root = %q, want /tmp/xdg-cache/composer-go", got)
	}
}

func TestRootFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "/home/u")
	got, err := Root()
	if err != nil {
		t.Fatal(err)
	}
	// On macOS we want ~/Library/Caches/composer-go; elsewhere ~/.cache/composer-go.
	// Test environment is darwin — adjust if running on linux CI.
	if got != filepath.Join("/home/u", "Library", "Caches", "composer-go") &&
		got != filepath.Join("/home/u", ".cache", "composer-go") {
		t.Errorf("Root = %q, want HOME-rooted cache path", got)
	}
}
