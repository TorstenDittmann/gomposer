package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallRequiresManifest(t *testing.T) {
	dir := t.TempDir()
	err := Install(context.Background(), Options{ProjectDir: dir})
	if err == nil {
		t.Fatal("Install with no composer.json should error")
	}
}

func TestInstallReadsManifest(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"),
		[]byte(`{"name":"vendor/pkg"}`), 0o644); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}
	// With NoNetwork=true and an empty require list, Install must succeed
	// without contacting Packagist.
	err := Install(context.Background(), Options{ProjectDir: dir, NoNetwork: true})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
}

func TestCacheKeyChangesWithManifest(t *testing.T) {
	a := computeCacheKey([]byte(`{"name":"a"}`), nil, "php-unknown")
	b := computeCacheKey([]byte(`{"name":"b"}`), nil, "php-unknown")
	if a == b {
		t.Errorf("expected different keys for different manifests, got %q", a)
	}
}

func TestCacheKeyStableForSameInputs(t *testing.T) {
	a := computeCacheKey([]byte(`{"name":"a"}`), []byte("lock"), "php-unknown")
	b := computeCacheKey([]byte(`{"name":"a"}`), []byte("lock"), "php-unknown")
	if a != b {
		t.Errorf("expected stable key, got %q vs %q", a, b)
	}
}

func TestCacheKeyChangesWithPlatform(t *testing.T) {
	a := computeCacheKey([]byte(`m`), nil, "php-unknown")
	b := computeCacheKey([]byte(`m`), nil, "php-8.2.0;ext-json")
	if a == b {
		t.Errorf("expected different keys for different platforms")
	}
}
