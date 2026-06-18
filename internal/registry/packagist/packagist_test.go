package packagist

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/torstendittmann/gomposer/internal/auth"
	"github.com/torstendittmann/gomposer/internal/registry"
)

const sampleResponse = `{
  "packages": {
    "monolog/monolog": [
      {
        "name": "monolog/monolog",
        "version": "3.5.0",
        "version_normalized": "3.5.0.0",
        "type": "library",
        "source": {"type": "git", "url": "https://github.com/Seldaek/monolog.git", "reference": "abc"},
        "dist":   {"type": "zip", "url": "https://api.github.com/repos/Seldaek/monolog/zipball/abc", "shasum": "deadbeef"},
        "require": {"php": ">=8.1"},
        "autoload": {"psr-4": {"Monolog\\": "src/Monolog"}}
      },
      {
        "name": "monolog/monolog",
        "version": "3.4.0",
        "version_normalized": "3.4.0.0",
        "type": "library",
        "source": {"type": "git", "url": "https://github.com/Seldaek/monolog.git", "reference": "def"},
        "dist":   {"type": "zip", "url": "https://api.github.com/repos/Seldaek/monolog/zipball/def", "shasum": "cafebabe"},
        "require": {"php": ">=8.1"}
      }
    ]
  }
}`

func TestLookupHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/p2/monolog/monolog.json") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleResponse))
	}))
	defer srv.Close()

	c, err := New(Config{BaseURL: srv.URL, CacheDir: t.TempDir(), HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	md, err := c.Lookup(context.Background(), "monolog/monolog")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if md.Name != "monolog/monolog" {
		t.Errorf("Name = %q", md.Name)
	}
	if len(md.Versions) != 2 {
		t.Fatalf("Versions = %d, want 2", len(md.Versions))
	}
	v := md.Versions[0]
	if v.Version != "3.5.0" || v.VersionNorm != "3.5.0.0" {
		t.Errorf("v[0] version mismatch: %+v", v)
	}
	if v.Dist.URL == "" || v.Dist.Type != "zip" {
		t.Errorf("v[0] dist mismatch: %+v", v.Dist)
	}
	if v.Source.Ref != "abc" {
		t.Errorf("v[0] source ref = %q, want abc", v.Source.Ref)
	}
	if v.Type != "library" {
		t.Errorf("v[0] type = %q, want library", v.Type)
	}
}

// TestLookupSkipsVersionsWithNoInstallMethod mirrors a real Packagist
// anomaly observed for symfony/http-client v8.1.x-dev (2026-06): the
// version arrives with both `dist` and `source` set to JSON null, meaning
// no install method exists. Such versions must be dropped at the registry
// boundary; otherwise the resolver can pick one and the fetcher aborts the
// install with `unsupported dist type ""`.
func TestLookupSkipsVersionsWithNoInstallMethod(t *testing.T) {
	// The client fetches /p2/<name>.json (tagged) and /p2/<name>~dev.json
	// (branches) and merges. Put the distless version on the dev side, the
	// good version on the stable side — mirrors the real symfony anomaly.
	const stableHalf = `{"packages":{"vendor/pkg":[
		{"name":"vendor/pkg","version":"v1.0.0","version_normalized":"1.0.0.0","type":"library",
		 "source":{"type":"git","url":"https://example.invalid/vendor/pkg.git","reference":"good"},
		 "dist":{"type":"zip","url":"https://example.invalid/pkg-1.0.0.zip","shasum":"deadbeef"},
		 "require":{}}
	]}}`
	const devHalf = `{"packages":{"vendor/pkg":[
		{"name":"vendor/pkg","version":"dev-broken","version_normalized":"dev-broken","type":"library",
		 "source":null,"dist":null,"require":{}}
	]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "~dev.json") {
			_, _ = w.Write([]byte(devHalf))
			return
		}
		_, _ = w.Write([]byte(stableHalf))
	}))
	defer srv.Close()

	c, err := New(Config{BaseURL: srv.URL, CacheDir: t.TempDir(), HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	md, err := c.Lookup(context.Background(), "vendor/pkg")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(md.Versions) != 1 {
		t.Fatalf("Versions = %d, want 1 (dev-broken should be filtered)", len(md.Versions))
	}
	if md.Versions[0].Version != "v1.0.0" {
		t.Errorf("kept the wrong version: %q", md.Versions[0].Version)
	}
}

func TestLookupNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c, _ := New(Config{BaseURL: srv.URL, CacheDir: t.TempDir(), HTTPClient: srv.Client()})
	_, err := c.Lookup(context.Background(), "no/such")
	if !errors.Is(err, registry.ErrPackageNotFound) {
		t.Errorf("err = %v, want ErrPackageNotFound", err)
	}
}

func TestLiveLookupMonolog(t *testing.T) {
	if os.Getenv("GOMPOSER_LIVE_NETWORK") != "1" {
		t.Skip("set GOMPOSER_LIVE_NETWORK=1 to run")
	}
	c, err := New(Config{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	md, err := c.Lookup(context.Background(), "monolog/monolog")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(md.Versions) < 1 {
		t.Errorf("expected at least one published version")
	}
}

func TestPackagistAttachesAuth(t *testing.T) {
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleResponse))
	}))
	defer srv.Close()

	dir := t.TempDir()
	authFile := filepath.Join(dir, "user.json")
	body := `{"bearer":{"127.0.0.1":"TOK"}}`
	if err := os.WriteFile(authFile, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := auth.LoadStoreForTest("", authFile)
	if err != nil {
		t.Fatal(err)
	}

	c, err := New(Config{
		BaseURL:    srv.URL,
		CacheDir:   t.TempDir(),
		HTTPClient: srv.Client(),
		Auth:       store,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Lookup(context.Background(), "monolog/monolog"); err != nil {
		t.Fatal(err)
	}
	if sawAuth != "Bearer TOK" {
		t.Errorf("Authorization = %q, want Bearer TOK", sawAuth)
	}
}
