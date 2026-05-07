# Stage 1 / Plan 2: Metadata Client + Caches Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fetch package metadata from Packagist's v2 API, persist responses on disk with ETag-based conditional GETs (cache layer 1), and cache the decoded form keyed by content hash (cache layer 4). Expose a single `registry.Source` interface that the resolver consumes.

**Architecture:** A `Source` interface so the resolver does not know whether metadata came from Packagist, a future VCS adapter, or a fixture. The Packagist implementation wraps an `http.Client` with a disk-backed cache. The parsed-manifest cache sits one level up: it serializes decoded `PackageMetadata` to a binary format keyed by sha256 of the raw JSON. Both caches live under `$XDG_CACHE_HOME/composer-go/`.

**Tech Stack:** Go stdlib `net/http`, `encoding/json`, `encoding/gob`, `crypto/sha256`. No new external deps.

**Depends on:** Plan 1 (Foundations) — uses `internal/constraint.Version` for sorting.

---

## File structure

| Path | Responsibility |
|------|---------------|
| `internal/cache/dir.go` | Resolves the cache root (`$XDG_CACHE_HOME` etc.); exposes `CacheRoot()` |
| `internal/cache/dir_test.go` | Tests for cache root resolution |
| `internal/cache/httpcache/cache.go` | Disk-backed HTTP cache with ETag/Last-Modified |
| `internal/cache/httpcache/cache_test.go` | Round-trip tests against an `httptest.Server` |
| `internal/cache/parsedcache/cache.go` | Content-hash-keyed gob cache for decoded structs |
| `internal/cache/parsedcache/cache_test.go` | Encode/decode/eviction tests |
| `internal/registry/source.go` | `Source` interface + `PackageMetadata`, `PackageVersion` types |
| `internal/registry/packagist/packagist.go` | Packagist v2 implementation of `Source` |
| `internal/registry/packagist/packagist_test.go` | Tests using `httptest.Server` serving canned v2 JSON |

---

## Task 1: Cache root resolution

**Files:**
- Create: `internal/cache/dir.go`
- Create: `internal/cache/dir_test.go`

- [ ] **Step 1: Write the failing test**

```go
package cache

import (
	"path/filepath"
	"testing"
)

func TestRootHonoursXDG(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/tmp/xdg-cache")
	t.Setenv("HOME", "/home/u")
	got, err := Root()
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join("/tmp/xdg-cache", "composer-go") {
		t.Errorf("Root = %q, want /tmp/xdg-cache/composer-go", got)
	}
}

func TestRootFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "/home/u")
	got, err := Root()
	if err != nil {
		t.Fatal(err)
	}
	// On macOS we want ~/Library/Caches/composer-go; elsewhere ~/.cache/composer-go.
	// Test environment is darwin — adjust if running on linux CI.
	if got != filepath.Join("/home/u", "Library", "Caches", "composer-go") &&
		got != filepath.Join("/home/u", ".cache", "composer-go") {
		t.Errorf("Root = %q, want HOME-rooted cache path", got)
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/cache/`

Expected: build error on `Root`.

- [ ] **Step 3: Implement Root**

Create `internal/cache/dir.go`:

```go
// Package cache exposes the cache root path used by all cache layers.
// Sub-packages (httpcache, parsedcache) live under that root.
package cache

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

const dirName = "composer-go"

// Root returns the absolute path to the composer-go cache directory.
// It does NOT create the directory; callers create per-layer subdirs.
//
// Resolution order:
//  1. $XDG_CACHE_HOME/composer-go (if set, regardless of OS)
//  2. macOS: $HOME/Library/Caches/composer-go
//  3. other: $HOME/.cache/composer-go
func Root() (string, error) {
	if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		return filepath.Join(x, dirName), nil
	}
	home := os.Getenv("HOME")
	if home == "" {
		return "", errors.New("cache: $HOME is unset and $XDG_CACHE_HOME is unset")
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Caches", dirName), nil
	}
	return filepath.Join(home, ".cache", dirName), nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/cache/`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cache
git commit -m "feat(cache): resolve cache root with XDG and platform fallbacks"
```

---

## Task 2: Registry types and Source interface

**Files:**
- Create: `internal/registry/source.go`

These types are referenced by every later plan. They mirror the subset of Packagist v2 fields we actually use.

- [ ] **Step 1: Write the file**

Create `internal/registry/source.go`:

```go
// Package registry abstracts package-metadata sources. The resolver depends
// only on the Source interface; concrete sources (packagist, vcs) live in
// sub-packages.
package registry

import (
	"context"
)

// PackageMetadata is the metadata about a single package, across all
// versions known to the source.
type PackageMetadata struct {
	Name     string
	Versions []PackageVersion
}

// PackageVersion is the metadata for one published version.
type PackageVersion struct {
	Name        string
	Version     string            // raw version string as published, e.g. "3.5.0" or "dev-main"
	VersionNorm string            // normalized form, used for stable comparison
	Source      Source            // git source ref
	Dist        Dist              // download artifact (zip)
	Require     map[string]string // production deps (raw constraint strings)
	RequireDev  map[string]string
	Autoload    Autoload
	AutoloadDev Autoload
	Suggest     map[string]string
	// Type is the package type ("library", "composer-plugin", etc.).
	// composer-plugin packages must be detected and skipped by the orchestrator.
	Type string
}

type Source struct {
	Type string // typically "git"
	URL  string
	Ref  string // commit sha or tag
}

type Dist struct {
	Type string // "zip"
	URL  string
	Sha  string // sha256 if available; empty otherwise (verified after download)
}

type Autoload struct {
	PSR4     map[string]any // values may be string or []string
	PSR0     map[string]any
	Files    []string
	Classmap []string
}

// SourceLookup is the interface the resolver consumes. Implementations:
//   - packagist.Client: fetches from packagist.org with HTTP cache
//   - (future) vcs.Client: clones git repos
//   - testlookup.Static: in-memory canned data for unit tests
//
// Implementations MUST return ErrPackageNotFound (declared below) for
// genuinely-missing packages so the resolver can distinguish "no such
// package" from "transient error."
type SourceLookup interface {
	// Lookup returns metadata for a package by canonical name (e.g.,
	// "monolog/monolog"). The returned PackageMetadata.Versions slice
	// is sorted in source order; callers must not assume sort.
	Lookup(ctx context.Context, name string) (*PackageMetadata, error)
}

// ErrPackageNotFound is returned by SourceLookup implementations when a
// package definitively does not exist in the source. Use errors.Is to test.
var ErrPackageNotFound = errPackageNotFound{}

type errPackageNotFound struct{}

func (errPackageNotFound) Error() string { return "registry: package not found" }
```

- [ ] **Step 2: Verify it builds**

Run: `go build ./internal/registry/...`

Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add internal/registry
git commit -m "feat(registry): SourceLookup interface and metadata types"
```

---

## Task 3: HTTP cache — basic GET with on-disk store

**Files:**
- Create: `internal/cache/httpcache/cache.go`
- Create: `internal/cache/httpcache/cache_test.go`

The HTTP cache is a thin wrapper around `http.Client` that persists responses keyed by URL hash. On a second call it sends `If-None-Match`/`If-Modified-Since` and serves the cached body on `304 Not Modified`.

- [ ] **Step 1: Write the failing test**

Create `internal/cache/httpcache/cache_test.go`:

```go
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
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/cache/httpcache/...`

Expected: build error on `New`, `Get`.

- [ ] **Step 3: Implement HTTP cache**

Create `internal/cache/httpcache/cache.go`:

```go
// Package httpcache implements a disk-backed HTTP GET cache with
// conditional requests. It is intentionally minimal: GET-only, no
// query-string normalization, no max-age handling. Packagist returns
// strong ETags, which is what we rely on.
package httpcache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// entryHeader is the small JSON sidecar stored next to the body.
type entryHeader struct {
	URL          string `json:"url"`
	ETag         string `json:"etag"`
	LastModified string `json:"lastModified"`
	StatusCode   int    `json:"status"`
}

// Cache reads-through to an HTTP client and persists 200 responses
// keyed by sha256(URL).
type Cache struct {
	dir    string
	client *http.Client
}

// New creates a cache rooted at dir. Subdirectories are created lazily.
func New(dir string, client *http.Client) (*Cache, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Cache{dir: dir, client: client}, nil
}

// Get returns the response body for url, using cached entries with
// conditional revalidation when present.
func (c *Cache) Get(ctx context.Context, url string) ([]byte, error) {
	hdrPath, bodyPath := c.paths(url)
	hdr, body, hasCache := c.readCache(hdrPath, bodyPath)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if hasCache {
		if hdr.ETag != "" {
			req.Header.Set("If-None-Match", hdr.ETag)
		}
		if hdr.LastModified != "" {
			req.Header.Set("If-Modified-Since", hdr.LastModified)
		}
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		if !hasCache {
			return nil, errors.New("httpcache: 304 with no cache entry")
		}
		return body, nil
	case http.StatusOK:
		fresh, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		if err := c.writeCache(hdrPath, bodyPath, entryHeader{
			URL:          url,
			ETag:         resp.Header.Get("ETag"),
			LastModified: resp.Header.Get("Last-Modified"),
			StatusCode:   resp.StatusCode,
		}, fresh); err != nil {
			return nil, err
		}
		return fresh, nil
	}
	return nil, fmt.Errorf("httpcache: unexpected status %d for %s", resp.StatusCode, url)
}

func (c *Cache) paths(url string) (hdr, body string) {
	sum := sha256.Sum256([]byte(url))
	key := hex.EncodeToString(sum[:])
	sub := filepath.Join(c.dir, key[:2])
	return filepath.Join(sub, key+".hdr.json"), filepath.Join(sub, key+".body")
}

func (c *Cache) readCache(hdrPath, bodyPath string) (entryHeader, []byte, bool) {
	hdrData, err := os.ReadFile(hdrPath)
	if err != nil {
		return entryHeader{}, nil, false
	}
	var hdr entryHeader
	if err := json.Unmarshal(hdrData, &hdr); err != nil {
		return entryHeader{}, nil, false
	}
	body, err := os.ReadFile(bodyPath)
	if err != nil {
		return entryHeader{}, nil, false
	}
	return hdr, body, true
}

func (c *Cache) writeCache(hdrPath, bodyPath string, hdr entryHeader, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(hdrPath), 0o755); err != nil {
		return err
	}
	hdrData, err := json.Marshal(hdr)
	if err != nil {
		return err
	}
	if err := writeAtomic(bodyPath, body); err != nil {
		return err
	}
	return writeAtomic(hdrPath, hdrData)
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/cache/httpcache/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cache/httpcache
git commit -m "feat(httpcache): disk-backed GET cache with ETag revalidation"
```

---

## Task 4: Parsed-manifest cache

**Files:**
- Create: `internal/cache/parsedcache/cache.go`
- Create: `internal/cache/parsedcache/cache_test.go`

This cache stores the *decoded* form of metadata keyed by the content hash of the source bytes. On warm runs we skip JSON parsing entirely.

- [ ] **Step 1: Write the failing test**

Create `internal/cache/parsedcache/cache_test.go`:

```go
package parsedcache

import (
	"testing"
)

type sample struct {
	Name string
	Tags []string
}

func TestStoreAndLoad(t *testing.T) {
	c, err := New[sample](t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	in := sample{Name: "x", Tags: []string{"a", "b"}}
	if err := c.Store([]byte("source-bytes"), in); err != nil {
		t.Fatal(err)
	}

	out, ok, err := c.Load([]byte("source-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("Load returned ok=false on warm cache")
	}
	if out.Name != "x" || len(out.Tags) != 2 {
		t.Errorf("Loaded value mismatch: %+v", out)
	}
}

func TestLoadMissReturnsFalse(t *testing.T) {
	c, _ := New[sample](t.TempDir())
	_, ok, err := c.Load([]byte("never-seen"))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("Load returned ok=true on cold cache")
	}
}

func TestDifferentSourceProducesDifferentEntry(t *testing.T) {
	c, _ := New[sample](t.TempDir())
	_ = c.Store([]byte("a"), sample{Name: "A"})
	_ = c.Store([]byte("b"), sample{Name: "B"})

	a, _, _ := c.Load([]byte("a"))
	b, _, _ := c.Load([]byte("b"))
	if a.Name != "A" || b.Name != "B" {
		t.Errorf("entries collided: a=%+v b=%+v", a, b)
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/cache/parsedcache/...`

Expected: build error.

- [ ] **Step 3: Implement parsed cache**

Create `internal/cache/parsedcache/cache.go`:

```go
// Package parsedcache stores parsed values keyed by a content hash of the
// bytes they were parsed from. The intended use is "I have this raw JSON
// blob — do I already have a decoded form?".
package parsedcache

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Cache[T] is a typed disk-backed cache.
type Cache[T any] struct {
	dir string
}

// New creates a cache rooted at dir. Sub-dirs are created lazily.
func New[T any](dir string) (*Cache[T], error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Cache[T]{dir: dir}, nil
}

// Store serializes value and writes it under sha256(source).
func (c *Cache[T]) Store(source []byte, value T) error {
	path := c.path(source)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(&value); err != nil {
		return fmt.Errorf("parsedcache: encode: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load returns (value, true, nil) on cache hit, (zero, false, nil) on miss,
// or (zero, false, err) on I/O or decode failure (including corrupt entries,
// which the caller should treat as miss-and-evict).
func (c *Cache[T]) Load(source []byte) (T, bool, error) {
	var zero T
	path := c.path(source)
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return zero, false, nil
		}
		return zero, false, err
	}
	defer f.Close()
	var v T
	if err := gob.NewDecoder(f).Decode(&v); err != nil && !errors.Is(err, io.EOF) {
		// Treat decode failure as eviction signal.
		_ = os.Remove(path)
		return zero, false, nil
	}
	return v, true, nil
}

func (c *Cache[T]) path(source []byte) string {
	sum := sha256.Sum256(source)
	key := hex.EncodeToString(sum[:])
	return filepath.Join(c.dir, key[:2], key+".gob")
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/cache/parsedcache/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cache/parsedcache
git commit -m "feat(parsedcache): typed gob cache keyed by content hash"
```

---

## Task 5: Packagist client — single-package fetch (cold path)

**Files:**
- Create: `internal/registry/packagist/packagist.go`
- Create: `internal/registry/packagist/packagist_test.go`

Packagist v2 exposes per-package JSON at `https://repo.packagist.org/p2/<vendor>/<name>.json`. Top-level shape:

```json
{
  "packages": {
    "monolog/monolog": [
      { "name": "monolog/monolog", "version": "3.5.0", "version_normalized": "3.5.0.0",
        "dist": { "type": "zip", "url": "...", "shasum": "..." },
        "source": { "type": "git", "url": "...", "reference": "..." },
        "require": { "php": ">=8.1" },
        "type": "library", "autoload": { "psr-4": { "Monolog\\": "src/Monolog" } },
        ...
      },
      ...
    ]
  }
}
```

We decode that, skipping fields we don't use.

- [ ] **Step 1: Write the failing test**

Create `internal/registry/packagist/packagist_test.go`:

```go
package packagist

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/torstendittmann/composer-go/internal/registry"
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
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/registry/packagist/...`

Expected: build error.

- [ ] **Step 3: Implement Packagist client**

Create `internal/registry/packagist/packagist.go`:

```go
// Package packagist implements registry.SourceLookup against
// repo.packagist.org's v2 metadata API.
package packagist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/torstendittmann/composer-go/internal/cache/httpcache"
	"github.com/torstendittmann/composer-go/internal/cache/parsedcache"
	"github.com/torstendittmann/composer-go/internal/registry"
)

const defaultBaseURL = "https://repo.packagist.org"

type Config struct {
	BaseURL    string       // override for testing; default is repo.packagist.org
	CacheDir   string       // root cache dir; subdirs are created automatically
	HTTPClient *http.Client // default: http.DefaultClient
}

type Client struct {
	baseURL string
	http    *httpcache.Cache
	parsed  *parsedcache.Cache[registry.PackageMetadata]
}

func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.CacheDir == "" {
		return nil, errors.New("packagist: CacheDir is required")
	}
	httpDir := filepath.Join(cfg.CacheDir, "http")
	parsedDir := filepath.Join(cfg.CacheDir, "parsed")
	hc, err := httpcache.New(httpDir, cfg.HTTPClient)
	if err != nil {
		return nil, err
	}
	pc, err := parsedcache.New[registry.PackageMetadata](parsedDir)
	if err != nil {
		return nil, err
	}
	return &Client{baseURL: cfg.BaseURL, http: hc, parsed: pc}, nil
}

// Lookup implements registry.SourceLookup.
func (c *Client) Lookup(ctx context.Context, name string) (*registry.PackageMetadata, error) {
	url := c.baseURL + "/p2/" + name + ".json"
	body, err := c.http.Get(ctx, url)
	if err != nil {
		// httpcache returns an error containing the status code; map 404 -> ErrPackageNotFound.
		// We use a string match because httpcache reports "unexpected status N".
		if isNotFound(err) {
			return nil, fmt.Errorf("%s: %w", name, registry.ErrPackageNotFound)
		}
		return nil, err
	}

	if v, ok, _ := c.parsed.Load(body); ok {
		out := v
		return &out, nil
	}

	md, err := decodeV2(name, body)
	if err != nil {
		return nil, err
	}
	if err := c.parsed.Store(body, *md); err != nil {
		// Cache failures are non-fatal.
		_ = err
	}
	return md, nil
}

func isNotFound(err error) bool {
	// httpcache returns: "httpcache: unexpected status 404 for ..."
	return err != nil && (containsAll(err.Error(), "status 404") || containsAll(err.Error(), "status 410"))
}

func containsAll(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// --- v2 schema ---

type v2Response struct {
	Packages map[string][]v2Version `json:"packages"`
}

type v2Version struct {
	Name              string            `json:"name"`
	Version           string            `json:"version"`
	VersionNormalized string            `json:"version_normalized"`
	Type              string            `json:"type"`
	Source            v2Source          `json:"source"`
	Dist              v2Dist            `json:"dist"`
	Require           map[string]string `json:"require"`
	RequireDev        map[string]string `json:"require-dev"`
	Autoload          v2Autoload        `json:"autoload"`
	AutoloadDev       v2Autoload        `json:"autoload-dev"`
	Suggest           map[string]string `json:"suggest"`
}

type v2Source struct {
	Type      string `json:"type"`
	URL       string `json:"url"`
	Reference string `json:"reference"`
}

type v2Dist struct {
	Type   string `json:"type"`
	URL    string `json:"url"`
	Shasum string `json:"shasum"`
}

type v2Autoload struct {
	PSR4     map[string]any `json:"psr-4"`
	PSR0     map[string]any `json:"psr-0"`
	Files    []string       `json:"files"`
	Classmap []string       `json:"classmap"`
}

func decodeV2(name string, body []byte) (*registry.PackageMetadata, error) {
	var resp v2Response
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("packagist: decode %s: %w", name, err)
	}
	versions, ok := resp.Packages[name]
	if !ok || len(versions) == 0 {
		return nil, fmt.Errorf("%s: %w", name, registry.ErrPackageNotFound)
	}
	out := &registry.PackageMetadata{Name: name, Versions: make([]registry.PackageVersion, 0, len(versions))}
	for _, v := range versions {
		out.Versions = append(out.Versions, registry.PackageVersion{
			Name:        v.Name,
			Version:     v.Version,
			VersionNorm: v.VersionNormalized,
			Type:        v.Type,
			Source:      registry.Source{Type: v.Source.Type, URL: v.Source.URL, Ref: v.Source.Reference},
			Dist:        registry.Dist{Type: v.Dist.Type, URL: v.Dist.URL, Sha: v.Dist.Shasum},
			Require:     v.Require,
			RequireDev:  v.RequireDev,
			Autoload:    registry.Autoload(v.Autoload),
			AutoloadDev: registry.Autoload(v.AutoloadDev),
			Suggest:     v.Suggest,
		})
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/registry/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/registry
git commit -m "feat(registry/packagist): v2 lookup with HTTP and parsed caches"
```

---

## Task 6: Real-network smoke test (skipped by default)

**Files:**
- Modify: `internal/registry/packagist/packagist_test.go`

A `_live` test that hits real Packagist, gated on `COMPOSER_GO_LIVE_NETWORK=1`. This is dev-time confidence, not CI.

- [ ] **Step 1: Append the test**

```go
func TestLiveLookupMonolog(t *testing.T) {
	if os.Getenv("COMPOSER_GO_LIVE_NETWORK") != "1" {
		t.Skip("set COMPOSER_GO_LIVE_NETWORK=1 to run")
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
```

Add `"os"` to the imports.

- [ ] **Step 2: Run gated**

Run: `COMPOSER_GO_LIVE_NETWORK=1 go test ./internal/registry/packagist/... -run TestLive -v`

Expected: PASS, fetches dozens of versions.

- [ ] **Step 3: Run ungated**

Run: `go test ./internal/registry/packagist/... -v`

Expected: TestLive is SKIPPED, others PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/registry/packagist/packagist_test.go
git commit -m "test(packagist): live-network smoke test gated on env var"
```

---

## Plan 2 acceptance check

- `go test ./...` is green offline.
- `COMPOSER_GO_LIVE_NETWORK=1 go test ./internal/registry/packagist/... -run TestLive` fetches `monolog/monolog` from real Packagist.
- Re-running the live test on a warm cache produces zero `OK` responses (only `304 Not Modified`) — verify by adding logging temporarily if curious.
- Types `registry.PackageMetadata`, `registry.PackageVersion`, `registry.SourceLookup` are stable — Plans 3, 4, and 6 import them directly.
