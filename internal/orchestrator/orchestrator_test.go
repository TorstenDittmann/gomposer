package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/torstendittmann/gomposer/internal/lock"
	"github.com/torstendittmann/gomposer/internal/manifest"
	platformpkg "github.com/torstendittmann/gomposer/internal/platform"
	"github.com/torstendittmann/gomposer/internal/registry"
	"github.com/torstendittmann/gomposer/internal/resolver"
)

// resetPlatformProbeForTest installs a fake PHP version (with a generic
// extension set) into the platform package's process cache. Idempotent.
func resetPlatformProbeForTest(t *testing.T, phpVersion string) {
	t.Helper()
	platformpkg.SetTestPlatform(t, phpVersion)
}

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
	platformpkg.SetTestPlatform(t, "8.2.0")
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
	if got.PlatformFingerprint != "php-8.2.0;ext-json;ext-mbstring" {
		t.Errorf("PlatformFingerprint = %q", got.PlatformFingerprint)
	}
	if got.SchemaVersion != lock.SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, lock.SchemaVersion)
	}
}

// --- Task 5: fetch phase tests ---

type fakeFetcher struct {
	mu       sync.Mutex
	calls    []string
	returnFn func(name string) (string, error)
}

func (f *fakeFetcher) Fetch(_ context.Context, pkg lock.Package) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, pkg.Name)
	f.mu.Unlock()
	if f.returnFn != nil {
		return f.returnFn(pkg.Name)
	}
	return "store-key-" + pkg.Name, nil
}

func TestFetchPhaseDownloadsAllPackages(t *testing.T) {
	pkgs := []lock.Package{
		{Name: "a/x", Version: "1.0.0", Dist: lock.Dist{Type: "zip", URL: "u1", Sha256: "s1"}},
		{Name: "b/y", Version: "2.0.0", Dist: lock.Dist{Type: "zip", URL: "u2", Sha256: "s2"}},
		{Name: "c/z", Version: "3.0.0", Dist: lock.Dist{Type: "zip", URL: "u3", Sha256: "s3"}},
	}
	ff := &fakeFetcher{}
	keys, err := fetchAll(context.Background(), pkgs, ff, 2, nil, nil)
	if err != nil {
		t.Fatalf("fetchAll: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("len(keys) = %d, want 3", len(keys))
	}
	for _, p := range pkgs {
		if keys[p.Name] != "store-key-"+p.Name {
			t.Errorf("keys[%s] = %q", p.Name, keys[p.Name])
		}
	}
}

func TestFetchPhaseSurfacesError(t *testing.T) {
	pkgs := []lock.Package{{Name: "bad/pkg", Dist: lock.Dist{URL: "u"}}}
	ff := &fakeFetcher{returnFn: func(string) (string, error) { return "", errors.New("network down") }}
	if _, err := fetchAll(context.Background(), pkgs, ff, 2, nil, nil); err == nil {
		t.Error("expected error when fetcher fails")
	}
}

// --- Task 6: materialize phase tests ---

type fakeMaterializer struct {
	mu    sync.Mutex
	wrote map[string]string // dest -> storeKey
}

func (m *fakeMaterializer) Materialize(_ context.Context, key, dest string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.wrote == nil {
		m.wrote = make(map[string]string)
	}
	m.wrote[dest] = key
	return os.MkdirAll(dest, 0o755)
}

func TestMaterializePhasePopulatesVendor(t *testing.T) {
	dir := t.TempDir()
	pkgs := []lock.Package{
		{Name: "vendor/a", Version: "1.0.0"},
		{Name: "vendor/b", Version: "1.0.0"},
	}
	keys := map[string]string{
		"vendor/a": "key-a",
		"vendor/b": "key-b",
	}
	mz := &fakeMaterializer{}
	if err := materializeAll(context.Background(), dir, pkgs, keys, mz, 2, nil); err != nil {
		t.Fatalf("materializeAll: %v", err)
	}
	if len(mz.wrote) != 2 {
		t.Fatalf("wrote %d, want 2: %+v", len(mz.wrote), mz.wrote)
	}
	wantA := filepath.Join(dir, "vendor", "vendor", "a")
	if got := mz.wrote[wantA]; got != "key-a" {
		t.Errorf("wrote[%s] = %q, want key-a", wantA, got)
	}
}

// --- Task 7: autoload phase tests ---

type fakeAutoloader struct {
	called      int
	gotPackages int
}

func (a *fakeAutoloader) Generate(_ context.Context, projectDir string, pkgs []lock.Package, m *manifest.Manifest) error {
	a.called++
	a.gotPackages = len(pkgs)
	vendorDir := filepath.Join(projectDir, "vendor")
	if err := os.MkdirAll(vendorDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(vendorDir, "autoload.php"), []byte("<?php // stub\n"), 0o644)
}

func TestAutoloadPhaseInvokesGenerator(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "vendor"), 0o755); err != nil {
		t.Fatal(err)
	}
	gen := &fakeAutoloader{}
	pkgs := []lock.Package{{Name: "psr/log", Version: "3.0.0"}}
	m := &manifest.Manifest{Name: "vendor/pkg"}
	if err := generateAutoloader(context.Background(), dir, pkgs, m, gen); err != nil {
		t.Fatalf("generateAutoloader: %v", err)
	}
	if gen.called != 1 {
		t.Errorf("called %d times, want 1", gen.called)
	}
	if gen.gotPackages != 1 {
		t.Errorf("packages received = %d, want 1", gen.gotPackages)
	}
	if _, err := os.Stat(filepath.Join(dir, "vendor", "autoload.php")); err != nil {
		t.Errorf("vendor/autoload.php should exist: %v", err)
	}
}

// --- Task 8: writeLock tests ---

func TestWriteLockProducesValidJSON(t *testing.T) {
	dir := t.TempDir()
	f := &lock.File{
		SchemaVersion: lock.SchemaVersion,
		Generator:     lock.Generator{Name: "gomposer", Version: "0.1.0"},
		Packages:      []lock.Package{{Name: "psr/log", Version: "3.0.0"}},
	}
	if err := writeLock(dir, f); err != nil {
		t.Fatalf("writeLock: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "gomposer.lock"))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	out, err := lock.Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Packages) != 1 || out.Packages[0].Name != "psr/log" {
		t.Errorf("decoded lock: %+v", out)
	}
}

// --- Task 9: full pipeline tests ---

func TestInstallFullPipelineWithFakes(t *testing.T) {
	platformpkg.SetTestPlatform(t, "8.2.0")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"),
		[]byte(`{"name":"vendor/pkg","require":{"acme/leaf":"1.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{pkgs: map[string]*registry.PackageMetadata{
		"acme/leaf": {Name: "acme/leaf", Versions: []registry.PackageVersion{{
			Name: "acme/leaf", Version: "1.0.0", VersionNorm: "1.0.0.0",
			Dist: registry.Dist{Type: "zip", URL: "u", Sha: "s"},
		}}},
	}}
	opts := Options{
		ProjectDir:   dir,
		Source:       src,
		Fetcher:      &fakeFetcher{},
		Materializer: &fakeMaterializer{},
		Autoloader:   &fakeAutoloader{},
		Workers:      2,
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "gomposer.lock")); err != nil {
		t.Errorf("gomposer.lock not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "vendor", "autoload.php")); err != nil {
		t.Errorf("vendor/autoload.php not written: %v", err)
	}
}

func TestInstallUsesResolutionCacheOnSecondRun(t *testing.T) {
	platformpkg.SetTestPlatform(t, "8.2.0")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"),
		[]byte(`{"name":"vendor/pkg","require":{"acme/leaf":"1.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{pkgs: map[string]*registry.PackageMetadata{
		"acme/leaf": {Name: "acme/leaf", Versions: []registry.PackageVersion{{
			Name: "acme/leaf", Version: "1.0.0", VersionNorm: "1.0.0.0",
			Dist: registry.Dist{Type: "zip", URL: "u", Sha: "s"},
		}}},
	}}

	hits := 0
	originalResolve := resolveFunc
	t.Cleanup(func() { resolveFunc = originalResolve })
	resolveFunc = func(ctx context.Context, ps *pipelineState, _ registry.SourceLookup, includeDev bool) (*resolver.Result, error) {
		hits++
		return originalResolve(ctx, ps, src, includeDev)
	}

	opts := Options{
		ProjectDir:   dir,
		Source:       src,
		Fetcher:      &fakeFetcher{},
		Materializer: &fakeMaterializer{},
		Autoloader:   &fakeAutoloader{},
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Errorf("resolver invoked %d times across two Install calls; want 1 (second should hit cache)", hits)
	}
}

func TestRegistryAutoloadFromMapExtractsFilesClassmap(t *testing.T) {
	in := map[string]any{
		"psr-4":                 map[string]any{"Acme\\": "src/"},
		"files":                 []any{"bootstrap.php", "helpers.php"},
		"classmap":              []any{"legacy/"},
		"exclude-from-classmap": []any{"**/Tests/"},
	}
	al, excl := autoloadFromLockMap(in)
	if al.PSR4["Acme\\"] == nil {
		t.Errorf("psr-4 lost")
	}
	if !reflect.DeepEqual(al.Files, []string{"bootstrap.php", "helpers.php"}) {
		t.Errorf("files = %v", al.Files)
	}
	if !reflect.DeepEqual(al.Classmap, []string{"legacy/"}) {
		t.Errorf("classmap = %v", al.Classmap)
	}
	if !reflect.DeepEqual(excl, []string{"**/Tests/"}) {
		t.Errorf("exclude = %v", excl)
	}
}

func TestInstallEmitsPlatformWarningsAndPersistsOnLock(t *testing.T) {
	resetPlatformProbeForTest(t, "8.2.14")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"),
		[]byte(`{"name":"vendor/pkg","require":{"acme/leaf":"1.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{pkgs: map[string]*registry.PackageMetadata{
		"acme/leaf": {Name: "acme/leaf", Versions: []registry.PackageVersion{{
			Name: "acme/leaf", Version: "1.0.0", VersionNorm: "1.0.0.0",
			Dist:    registry.Dist{Type: "zip", URL: "u", Sha: "s"},
			Require: map[string]string{"php": "^7.4"},
		}}},
	}}

	opts := Options{
		ProjectDir:   dir,
		Source:       src,
		Fetcher:      &fakeFetcher{},
		Materializer: &fakeMaterializer{},
		Autoloader:   &fakeAutoloader{},
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "gomposer.lock"))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	f, err := lock.Decode(data)
	if err != nil {
		t.Fatalf("decode lock: %v", err)
	}
	if len(f.Warnings) == 0 {
		t.Errorf("expected platform warning persisted on lock; got %+v", f.Warnings)
	}
}

func TestInstallNoDevFailsOnPlatformMismatch(t *testing.T) {
	resetPlatformProbeForTest(t, "8.2.14")

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "composer.json"),
		[]byte(`{"name":"x/y","require":{"acme/leaf":"1.0.0"}}`), 0o644)
	src := &fakeSource{pkgs: map[string]*registry.PackageMetadata{
		"acme/leaf": {Name: "acme/leaf", Versions: []registry.PackageVersion{{
			Name: "acme/leaf", Version: "1.0.0", VersionNorm: "1.0.0.0",
			Dist:    registry.Dist{Type: "zip", URL: "u", Sha: "s"},
			Require: map[string]string{"php": "^7.4"},
		}}},
	}}
	opts := Options{
		ProjectDir:   dir,
		NoDev:        true,
		Source:       src,
		Fetcher:      &fakeFetcher{},
		Materializer: &fakeMaterializer{},
		Autoloader:   &fakeAutoloader{},
	}
	if err := Install(context.Background(), opts); err == nil {
		t.Error("--no-dev should fail when platform req unsatisfied")
	}
}

func TestInstallIgnorePlatformReqSuppresses(t *testing.T) {
	resetPlatformProbeForTest(t, "8.2.14")

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "composer.json"),
		[]byte(`{"name":"x/y","require":{"acme/leaf":"1.0.0"}}`), 0o644)
	src := &fakeSource{pkgs: map[string]*registry.PackageMetadata{
		"acme/leaf": {Name: "acme/leaf", Versions: []registry.PackageVersion{{
			Name: "acme/leaf", Version: "1.0.0", VersionNorm: "1.0.0.0",
			Dist:    registry.Dist{Type: "zip", URL: "u", Sha: "s"},
			Require: map[string]string{"php": "^7.4"},
		}}},
	}}
	opts := Options{
		ProjectDir:         dir,
		Source:             src,
		Fetcher:            &fakeFetcher{},
		Materializer:       &fakeMaterializer{},
		Autoloader:         &fakeAutoloader{},
		IgnorePlatformReqs: []string{"php"},
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "gomposer.lock"))
	f, _ := lock.Decode(data)
	for _, w := range f.Warnings {
		if strings.Contains(w, "acme/leaf") {
			t.Errorf("warning should be suppressed: %q", w)
		}
	}
}

func TestInstallEmitsPluginWarning(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"),
		[]byte(`{"name":"vendor/pkg","require":{"acme/plugin":"1.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{pkgs: map[string]*registry.PackageMetadata{
		"acme/plugin": {Name: "acme/plugin", Versions: []registry.PackageVersion{{
			Name: "acme/plugin", Version: "1.0.0", VersionNorm: "1.0.0.0",
			Type: "composer-plugin",
			Dist: registry.Dist{Type: "zip", URL: "u", Sha: "s"},
		}}},
	}}
	var stderr bytes.Buffer
	opts := Options{
		ProjectDir:   dir,
		Source:       src,
		Fetcher:      &fakeFetcher{},
		Materializer: &fakeMaterializer{},
		Autoloader:   &fakeAutoloader{},
		WarnWriter:   &stderr,
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !strings.Contains(stderr.String(), "acme/plugin") ||
		!strings.Contains(stderr.String(), "composer-plugin") {
		t.Errorf("expected plugin warning, got: %q", stderr.String())
	}
	// Plugin must STILL flow through fetch + materialize.
	if mz, ok := opts.Materializer.(*fakeMaterializer); ok {
		want := filepath.Join(dir, "vendor", "acme", "plugin")
		if _, ok := mz.wrote[want]; !ok {
			t.Errorf("plugin package not materialized; wrote=%+v", mz.wrote)
		}
	}
}

func TestInstallSuppressedByManifestExtra(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{
"name":"vendor/pkg",
"require":{"acme/plugin":"1.0.0"},
"extra":{"gomposer":{"suppress-plugin-warnings":true}}
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{pkgs: map[string]*registry.PackageMetadata{
		"acme/plugin": {Name: "acme/plugin", Versions: []registry.PackageVersion{{
			Name: "acme/plugin", Version: "1.0.0", VersionNorm: "1.0.0.0",
			Type: "composer-plugin",
			Dist: registry.Dist{Type: "zip", URL: "u", Sha: "s"},
		}}},
	}}
	var stderr bytes.Buffer
	opts := Options{
		ProjectDir: dir, Source: src,
		Fetcher: &fakeFetcher{}, Materializer: &fakeMaterializer{}, Autoloader: &fakeAutoloader{},
		WarnWriter: &stderr,
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if stderr.Len() != 0 {
		t.Errorf("expected no warning under suppression, got: %q", stderr.String())
	}
}
