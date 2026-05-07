package fetcher

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/torstendittmann/composer-go/internal/registry"
	"github.com/torstendittmann/composer-go/internal/store"
)

func TestMaterializeStripsTopLevelDir(t *testing.T) {
	// Mimic Composer's wrapping: every entry under "Seldaek-monolog-abc123/...".
	zipBytes := makeZip(t, map[string]string{
		"vendor-pkg-abc123/composer.json": `{"name":"vendor/pkg"}`,
		"vendor-pkg-abc123/src/Foo.php":   "<?php class Foo {}",
		"vendor-pkg-abc123/README.md":     "hi",
	})
	sha := sha256Hex(zipBytes)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(zipBytes)
	}))
	defer srv.Close()

	st, _ := store.New(t.TempDir())
	f := New(st, srv.Client())
	pv := registry.PackageVersion{
		Name:    "vendor/pkg",
		Version: "1.0.0",
		Dist:    registry.Dist{Type: "zip", URL: srv.URL + "/x.zip", Sha: sha},
	}
	if err := f.Fetch(context.Background(), pv); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	target := filepath.Join(t.TempDir(), "vendor", "vendor", "pkg")
	if err := f.Materialize(context.Background(), pv, target); err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	for _, rel := range []string{"composer.json", "src/Foo.php", "README.md"} {
		if _, err := os.Stat(filepath.Join(target, rel)); err != nil {
			t.Errorf("missing %s in target: %v", rel, err)
		}
	}
	body, err := os.ReadFile(filepath.Join(target, "composer.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("vendor/pkg")) {
		t.Errorf("composer.json content not preserved: %s", body)
	}
}

func TestMaterializeWithoutWrapperDir(t *testing.T) {
	// Some dists are flat — no top-level wrapper. We must handle both.
	zipBytes := makeZip(t, map[string]string{
		"composer.json": `{"name":"vendor/flat"}`,
		"src/Foo.php":   "<?php",
	})
	sha := sha256Hex(zipBytes)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(zipBytes)
	}))
	defer srv.Close()

	st, _ := store.New(t.TempDir())
	f := New(st, srv.Client())
	pv := registry.PackageVersion{
		Name: "vendor/flat", Version: "1.0.0",
		Dist: registry.Dist{Type: "zip", URL: srv.URL + "/x.zip", Sha: sha},
	}
	if err := f.Fetch(context.Background(), pv); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "vendor", "vendor", "flat")
	if err := f.Materialize(context.Background(), pv, target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "composer.json")); err != nil {
		t.Errorf("missing composer.json: %v", err)
	}
}

func TestMaterializeRejectsTraversal(t *testing.T) {
	// Defense-in-depth: a malicious zip entry containing ".." must not escape.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("../../etc/evil")
	_, _ = w.Write([]byte("nope"))
	_ = zw.Close()
	zipBytes := buf.Bytes()
	sha := sha256Hex(zipBytes)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(zipBytes)
	}))
	defer srv.Close()
	st, _ := store.New(t.TempDir())
	f := New(st, srv.Client())
	pv := registry.PackageVersion{
		Name: "vendor/evil", Version: "1.0.0",
		Dist: registry.Dist{Type: "zip", URL: srv.URL + "/x.zip", Sha: sha},
	}
	if err := f.Fetch(context.Background(), pv); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "vendor", "vendor", "evil")
	if err := f.Materialize(context.Background(), pv, target); err == nil {
		t.Fatal("expected traversal rejection")
	}

	// Sanity: the test relies on runtime.GOOS being a sane unix-ish value.
	if runtime.GOOS == "" {
		t.Fatal("runtime.GOOS empty?")
	}
}

func TestCloneFileFallthroughChain(t *testing.T) {
	// CloneOrCopy succeeds on every platform: at minimum the io.Copy
	// fallback is always available. We only assert that the destination
	// ends up byte-for-byte equal to the source.
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	payload := []byte("clonefile or bust")
	if err := os.WriteFile(src, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CloneOrCopy(src, dst); err != nil {
		t.Fatalf("CloneOrCopy: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("dst mismatch: got %q want %q", got, payload)
	}
}

func TestCloneFilePlatformSpecific(t *testing.T) {
	switch runtime.GOOS {
	case "darwin":
		// Best-effort: clonefile only works on APFS. t.TempDir() is APFS on
		// modern macOS so we expect success or graceful fallthrough.
	case "linux":
		// FICLONE only works on btrfs/xfs+reflink/zfs/bcachefs. CI usually
		// runs ext4, so we treat fallthrough as the expected outcome.
	default:
		t.Skipf("no reflink primitive on %s", runtime.GOOS)
	}
	// The point is that calling CloneOrCopy never panics regardless of FS.
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CloneOrCopy(src, dst); err != nil {
		t.Errorf("CloneOrCopy on %s failed: %v", runtime.GOOS, err)
	}
}
