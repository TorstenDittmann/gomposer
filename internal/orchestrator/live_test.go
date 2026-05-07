package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLiveInstallPsrLog is the stage-1 acceptance test: install a real
// Packagist project end-to-end, then re-install on a warm cache in <100ms.
//
// Gated on COMPOSER_GO_LIVE_NETWORK=1.
func TestLiveInstallPsrLog(t *testing.T) {
	if os.Getenv("COMPOSER_GO_LIVE_NETWORK") != "1" {
		t.Skip("set COMPOSER_GO_LIVE_NETWORK=1 to run this test against real Packagist")
	}

	// Isolate caches: we want a clean cold path on the first run.
	cacheHome := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheHome)

	projectDir := t.TempDir()
	manifestPath := filepath.Join(projectDir, "composer.json")
	manifestContent := []byte(`{
  "name": "composer-go-test/live",
  "type": "library",
  "require": { "psr/log": "^3.0" }
}`)
	if err := os.WriteFile(manifestPath, manifestContent, 0o644); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}

	ctx := context.Background()

	// --- Cold install ---
	if err := Install(ctx, Options{ProjectDir: projectDir, Verbose: true}); err != nil {
		t.Fatalf("cold Install: %v", err)
	}

	if _, err := os.Stat(filepath.Join(projectDir, "vendor", "psr", "log")); err != nil {
		t.Errorf("vendor/psr/log not materialized: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "vendor", "autoload.php")); err != nil {
		t.Errorf("vendor/autoload.php not generated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "composer-go.lock")); err != nil {
		t.Errorf("composer-go.lock not written: %v", err)
	}

	// At least one PHP source file should be present in the materialized package.
	entries, err := os.ReadDir(filepath.Join(projectDir, "vendor", "psr", "log"))
	if err != nil || len(entries) == 0 {
		t.Errorf("vendor/psr/log appears empty: %v", err)
	}

	// --- Warm install ---
	// Wipe vendor/ to force re-materialization but keep all caches and the
	// existing lockfile. This simulates "user nuked vendor and re-ran install".
	if err := os.RemoveAll(filepath.Join(projectDir, "vendor")); err != nil {
		t.Fatalf("rm vendor: %v", err)
	}

	start := time.Now()
	if err := Install(ctx, Options{ProjectDir: projectDir}); err != nil {
		t.Fatalf("warm Install: %v", err)
	}
	elapsed := time.Since(start)
	t.Logf("warm install elapsed: %v", elapsed)

	if elapsed > 100*time.Millisecond {
		t.Errorf("warm install took %v, want <100ms (stage-1 acceptance criterion)", elapsed)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "vendor", "psr", "log")); err != nil {
		t.Errorf("vendor/psr/log not re-materialized on warm run: %v", err)
	}
}

// TestLiveUpdateRewritesLock exercises the update path against real Packagist.
// Gated identically.
func TestLiveUpdateRewritesLock(t *testing.T) {
	if os.Getenv("COMPOSER_GO_LIVE_NETWORK") != "1" {
		t.Skip("set COMPOSER_GO_LIVE_NETWORK=1")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"),
		[]byte(`{"name":"x/y","require":{"psr/log":"^3.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Update(context.Background(), Options{ProjectDir: dir}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "composer-go.lock")); err != nil {
		t.Errorf("composer-go.lock not written by update: %v", err)
	}
}
