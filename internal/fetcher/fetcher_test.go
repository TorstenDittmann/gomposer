package fetcher

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/torstendittmann/composer-go/internal/registry"
	"github.com/torstendittmann/composer-go/internal/store"
)

// makeZip returns the bytes of a zip containing the given files (path -> contents).
func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip.Create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("zip.Write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip.Close: %v", err)
	}
	return buf.Bytes()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestFetchColdStoreThenWarmHit(t *testing.T) {
	zipBytes := makeZip(t, map[string]string{
		"README.md":   "hello",
		"src/Foo.php": "<?php class Foo {}",
	})
	wantSha := sha256Hex(zipBytes)

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipBytes)
	}))
	defer srv.Close()

	storeDir := filepath.Join(t.TempDir(), "store")
	st, err := store.New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	f := New(st, srv.Client())

	pv := registry.PackageVersion{
		Name:    "vendor/pkg",
		Version: "1.0.0",
		Dist: registry.Dist{
			Type: "zip",
			URL:  srv.URL + "/vendor/pkg-1.0.0.zip",
			Sha:  wantSha,
		},
	}

	// Cold: should download.
	if _, err := f.Fetch(context.Background(), pv); err != nil {
		t.Fatalf("Fetch cold: %v", err)
	}
	if !st.Has(wantSha) {
		t.Fatalf("store missing artifact after cold fetch")
	}
	if hits != 1 {
		t.Errorf("hits = %d, want 1", hits)
	}

	// Warm: store hit, no network.
	if _, err := f.Fetch(context.Background(), pv); err != nil {
		t.Fatalf("Fetch warm: %v", err)
	}
	if hits != 1 {
		t.Errorf("hits = %d after warm fetch, want 1 (no second download)", hits)
	}

	// Bytes round-trip via OpenReader.
	rc, err := st.OpenReader(wantSha)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, zipBytes) {
		t.Errorf("stored bytes differ from served bytes")
	}
}

func TestFetchRejectsShaMismatch(t *testing.T) {
	zipBytes := makeZip(t, map[string]string{"a": "b"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(zipBytes)
	}))
	defer srv.Close()

	st, _ := store.New(t.TempDir())
	f := New(st, srv.Client())

	pv := registry.PackageVersion{
		Name:    "vendor/pkg",
		Version: "1.0.0",
		Dist: registry.Dist{
			Type: "zip",
			URL:  srv.URL + "/x.zip",
			Sha:  "0000000000000000000000000000000000000000000000000000000000000000",
		},
	}
	_, err := f.Fetch(context.Background(), pv)
	if err == nil {
		t.Fatal("expected sha mismatch error")
	}
	// Store must NOT contain the bogus artifact under either sha.
	if st.Has(sha256Hex(zipBytes)) {
		t.Errorf("store kept artifact after sha mismatch")
	}
}

func TestFetchEmptyShaSkipsVerification(t *testing.T) {
	// Composer occasionally serves dists without a published sha. The fetcher
	// should still store the artifact under the computed hash.
	zipBytes := makeZip(t, map[string]string{"x": "y"})
	wantSha := sha256Hex(zipBytes)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(zipBytes)
	}))
	defer srv.Close()

	st, _ := store.New(t.TempDir())
	f := New(st, srv.Client())
	pv := registry.PackageVersion{
		Name:    "vendor/pkg",
		Version: "1.0.0",
		Dist:    registry.Dist{Type: "zip", URL: srv.URL + "/x.zip", Sha: ""},
	}
	if _, err := f.Fetch(context.Background(), pv); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !st.Has(wantSha) {
		t.Errorf("store missing artifact under computed sha %s", wantSha)
	}
}
