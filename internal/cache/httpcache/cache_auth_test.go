package httpcache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCacheAppliesCredentials(t *testing.T) {
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("ETag", `"v"`)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c, err := New(t.TempDir(), srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	c.Credentials = CredentialsFunc(func(host string) (string, bool) {
		return "Bearer TOKEN", true
	})

	if _, err := c.Get(context.Background(), srv.URL+"/x"); err != nil {
		t.Fatal(err)
	}
	if sawAuth != "Bearer TOKEN" {
		t.Errorf("Authorization = %q, want Bearer TOKEN", sawAuth)
	}
}

func TestCacheNoCredentialsResolver(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("unexpected Authorization: %q", got)
		}
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	c, _ := New(t.TempDir(), srv.Client())
	if _, err := c.Get(context.Background(), srv.URL); err != nil {
		t.Fatal(err)
	}
}
