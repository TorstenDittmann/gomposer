package fetcher

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/torstendittmann/composer-go/internal/registry"
	"github.com/torstendittmann/composer-go/internal/store"
)

func TestFetchAllParallel(t *testing.T) {
	zips := map[string][]byte{
		"a": makeZip(t, map[string]string{"a/composer.json": `{"name":"v/a"}`}),
		"b": makeZip(t, map[string]string{"b/composer.json": `{"name":"v/b"}`}),
		"c": makeZip(t, map[string]string{"c/composer.json": `{"name":"v/c"}`}),
	}
	var concurrent int32
	var maxConcurrent int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := atomic.AddInt32(&concurrent, 1)
		defer atomic.AddInt32(&concurrent, -1)
		for {
			peak := atomic.LoadInt32(&maxConcurrent)
			if now <= peak || atomic.CompareAndSwapInt32(&maxConcurrent, peak, now) {
				break
			}
		}
		key := r.URL.Path[len("/"):]
		_, _ = w.Write(zips[key])
	}))
	defer srv.Close()

	st, _ := store.New(t.TempDir())
	f := New(st, srv.Client())

	pvs := []registry.PackageVersion{
		{Name: "v/a", Version: "1", Dist: registry.Dist{Type: "zip", URL: srv.URL + "/a", Sha: sha256Hex(zips["a"])}},
		{Name: "v/b", Version: "1", Dist: registry.Dist{Type: "zip", URL: srv.URL + "/b", Sha: sha256Hex(zips["b"])}},
		{Name: "v/c", Version: "1", Dist: registry.Dist{Type: "zip", URL: srv.URL + "/c", Sha: sha256Hex(zips["c"])}},
	}

	if err := f.FetchAll(context.Background(), pvs, 2); err != nil {
		t.Fatalf("FetchAll: %v", err)
	}
	for _, pv := range pvs {
		if !st.Has(pv.Dist.Sha) {
			t.Errorf("missing %s in store after FetchAll", pv.Name)
		}
	}
	// At most 2 in flight at once.
	if maxConcurrent > 2 {
		t.Errorf("maxConcurrent = %d, want <=2", maxConcurrent)
	}
}

func TestFetchAllPropagatesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	st, _ := store.New(t.TempDir())
	f := New(st, srv.Client())
	pvs := []registry.PackageVersion{
		{Name: "v/a", Version: "1", Dist: registry.Dist{Type: "zip", URL: srv.URL + "/a"}},
	}
	if err := f.FetchAll(context.Background(), pvs, 4); err == nil {
		t.Fatal("expected error from 500")
	}
}

func TestMaterializeAll(t *testing.T) {
	zipBytes := makeZip(t, map[string]string{
		"pkg/composer.json": `{"name":"v/p"}`,
		"pkg/src/X.php":     "<?php",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(zipBytes)
	}))
	defer srv.Close()
	st, _ := store.New(t.TempDir())
	f := New(st, srv.Client())
	pv := registry.PackageVersion{
		Name: "v/p", Version: "1",
		Dist: registry.Dist{Type: "zip", URL: srv.URL + "/x", Sha: sha256Hex(zipBytes)},
	}
	if err := f.FetchAll(context.Background(), []registry.PackageVersion{pv}, 2); err != nil {
		t.Fatal(err)
	}
	vendorRoot := filepath.Join(t.TempDir(), "vendor")
	if err := f.MaterializeAll(context.Background(), []registry.PackageVersion{pv}, vendorRoot, 2); err != nil {
		t.Fatalf("MaterializeAll: %v", err)
	}
	if _, err := os.Stat(filepath.Join(vendorRoot, "v", "p", "composer.json")); err != nil {
		t.Errorf("missing materialized file: %v", err)
	}
}
