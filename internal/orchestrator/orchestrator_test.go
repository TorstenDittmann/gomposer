package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/torstendittmann/composer-go/internal/lock"
	"github.com/torstendittmann/composer-go/internal/registry"
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

// fakeSource implements registry.SourceLookup for tests.
type fakeSource struct {
	pkgs map[string]*registry.PackageMetadata
}

func (f *fakeSource) Lookup(_ context.Context, name string) (*registry.PackageMetadata, error) {
	if v, ok := f.pkgs[name]; ok {
		return v, nil
	}
	return nil, registry.ErrPackageNotFound
}

func TestResolveProducesLockFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"),
		[]byte(`{"name":"vendor/pkg","require":{"acme/leaf":"1.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	src := &fakeSource{pkgs: map[string]*registry.PackageMetadata{
		"acme/leaf": {Name: "acme/leaf", Versions: []registry.PackageVersion{{
			Name: "acme/leaf", Version: "1.0.0", VersionNorm: "1.0.0.0",
			Dist: registry.Dist{Type: "zip", URL: "http://fixture/leaf-1.0.0.zip", Sha: "deadbeef"},
		}}},
	}}

	got, err := resolveOnly(context.Background(), Options{ProjectDir: dir, Source: src})
	if err != nil {
		t.Fatalf("resolveOnly: %v", err)
	}
	if len(got.Packages) != 1 || got.Packages[0].Name != "acme/leaf" {
		t.Errorf("Packages = %+v", got.Packages)
	}
	if got.PlatformFingerprint != "php-unknown" {
		t.Errorf("PlatformFingerprint = %q", got.PlatformFingerprint)
	}
	if got.SchemaVersion != lock.SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, lock.SchemaVersion)
	}
}
