package orchestrator

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLiveInstallEmitsPluginWarning installs phpstan/extension-installer (a
// real composer-plugin) and asserts that a warning lands on the WarnWriter.
// Gated on GOMPOSER_LIVE_NETWORK=1.
func TestLiveInstallEmitsPluginWarning(t *testing.T) {
	if os.Getenv("GOMPOSER_LIVE_NETWORK") != "1" {
		t.Skip("set GOMPOSER_LIVE_NETWORK=1 to run against real Packagist")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{
  "name": "gomposer-test/plugin-warn",
  "require": { "phpstan/extension-installer": "^1.4" }
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if err := Install(context.Background(), Options{ProjectDir: dir, WarnWriter: &stderr}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !strings.Contains(stderr.String(), "phpstan/extension-installer") ||
		!strings.Contains(stderr.String(), "composer-plugin") {
		t.Errorf("expected plugin warning, got: %q", stderr.String())
	}
	// Plugin must be installed despite the warning.
	if _, err := os.Stat(filepath.Join(dir, "vendor", "phpstan", "extension-installer")); err != nil {
		t.Errorf("plugin not materialized: %v", err)
	}
}
