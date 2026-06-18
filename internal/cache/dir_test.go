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
	if got != filepath.Join("/tmp/xdg-cache", "gomposer") {
		t.Errorf("Root = %q, want /tmp/xdg-cache/gomposer", got)
	}
}

func TestRootFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "/home/u")
	got, err := Root()
	if err != nil {
		t.Fatal(err)
	}
	// On macOS we want ~/Library/Caches/gomposer; elsewhere ~/.cache/gomposer.
	// Test environment is darwin — adjust if running on linux CI.
	if got != filepath.Join("/home/u", "Library", "Caches", "gomposer") &&
		got != filepath.Join("/home/u", ".cache", "gomposer") {
		t.Errorf("Root = %q, want HOME-rooted cache path", got)
	}
}
