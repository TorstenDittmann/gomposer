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
