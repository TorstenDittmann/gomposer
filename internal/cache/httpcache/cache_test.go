package httpcache

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestCacheServes304FromDisk(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("ETag", `"abc"`)
		if r.Header.Get("If-None-Match") == `"abc"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"hello": "world"}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	c, err := New(dir, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}

	// First call: cold cache -> origin reached, body stored.
	body1, err := c.Get(context.Background(), srv.URL+"/x")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(body1) != `{"hello": "world"}` {
		t.Errorf("body1 = %q", string(body1))
	}

	// Second call: warm cache -> If-None-Match sent, 304 returned, body served from disk.
	body2, err := c.Get(context.Background(), srv.URL+"/x")
	if err != nil {
		t.Fatalf("Get(2): %v", err)
	}
	if string(body2) != `{"hello": "world"}` {
		t.Errorf("body2 = %q", string(body2))
	}

	if atomic.LoadInt32(&hits) != 2 {
		t.Errorf("server hits = %d, want 2 (one OK, one 304)", hits)
	}
}

func TestCacheReturnsErrorOn5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c, _ := New(t.TempDir(), http.DefaultClient)
	_, err := c.Get(context.Background(), srv.URL+"/x")
	if err == nil {
		t.Fatal("expected error on 500")
	}
}

// drain prevents response-body resource warnings in tests.
func drain(r io.Reader) { _, _ = io.Copy(io.Discard, r) }
