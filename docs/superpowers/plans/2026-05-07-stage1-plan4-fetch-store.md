# Stage 1 / Plan 4: Fetcher + Content-Addressed Package Store Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Download package zips from `registry.PackageVersion.Dist.URL`, persist them in a content-addressed store keyed by sha256 (cache layer 2), and materialize them into `vendor/<vendor>/<name>/` using the fastest filesystem primitive available (APFS clonefile, Linux FICLONE, hardlink, copy). All downloads run in a bounded concurrent worker pool, and per-package the download stream is teed into a sha256 hasher so verification overlaps with disk write — the "pipelined" half of optimistic op 2.

**Architecture:**

- `internal/store` is a thin content-addressed blob store. Zips are stored at `<cacheRoot>/store/<sha256>.zip` plus a `.tmp` rename dance so a torn write never appears under its final name. The store itself does not understand zips — it only stores opaque bytes addressed by sha256.
- `internal/fetcher` is the I/O layer. Given a `registry.PackageVersion`, it consults the store; on a miss it streams the body of `Dist.URL` through a `io.TeeReader` into both the on-disk `.tmp` file and a sha256 hasher. After the body is fully drained, the hash is compared against `Dist.Sha` (when non-empty), then atomically renamed into the store. A `Materialize` helper expands a stored zip into a target directory using a strategy chain.
- The materialization strategy chain is platform-split via build tags:
  - `materialize_darwin.go` — APFS `clonefile(2)` first, then hardlink, then copy.
  - `materialize_linux.go` — `FICLONE` ioctl first, then hardlink, then copy.
  - `materialize_other.go` — hardlink, then copy.
  Each step falls through on `EXDEV`, `ENOTSUP`, `EOPNOTSUPP`, or any error other than `ENOENT` on the source.
- Concurrency is `golang.org/x/sync/errgroup` with `SetLimit(runtime.NumCPU())` for downloads and a separate group for extractions. The orchestrator that wires plan 5 (resolver) and plan 6 (autoloader) sits above this plan and is not implemented here.

**Pipelined-extract honesty.** `archive/zip` requires `io.ReaderAt` + a known size, so the spec-mandated streaming-into-the-zip-reader-as-bytes-arrive is impossible without a custom zip parser. We instead overlap **download + sha256 hashing + temp-file write** in a single `io.TeeReader` pipeline, then run extraction once the file is on disk. The user-visible win is identical for any package larger than the network's BDP: extraction starts the moment the last byte hits disk, and we never re-read the file to compute its hash. A future plan can replace this with a streaming central-directory parser if profiling justifies it.

**Tech Stack:** Go stdlib `archive/zip`, `crypto/sha256`, `io`, `net/http`, `os`. New deps: `golang.org/x/sys/unix` (for `Clonefile` and `Ioctl`), `golang.org/x/sync/errgroup`.

**Depends on:**

- Plan 1 (Foundations) — uses `lock.Package`-shaped data only at the orchestrator boundary; this plan does not touch the lockfile.
- Plan 2 (Metadata + caches) — uses `registry.PackageVersion`, `registry.Dist`, `cache.Root`.

---

## File structure

| Path | Responsibility |
|------|---------------|
| `internal/store/store.go` | `Store` type with `Has`, `Put`, `OpenReader`, `Path` |
| `internal/store/store_test.go` | Round-trip + atomicity tests against `bytes.Reader` |
| `internal/fetcher/fetcher.go` | `Fetcher` type with `Fetch(ctx, PackageVersion)` |
| `internal/fetcher/fetcher_test.go` | `httptest.Server` + on-the-fly zip fixture |
| `internal/fetcher/materialize.go` | Strategy-chain dispatch + zip extraction loop |
| `internal/fetcher/materialize_darwin.go` | `clonefile(2)` fast path; build tag `darwin` |
| `internal/fetcher/materialize_linux.go` | `FICLONE` ioctl fast path; build tag `linux` |
| `internal/fetcher/materialize_other.go` | Hardlink/copy only; build tag `!darwin && !linux` |
| `internal/fetcher/materialize_test.go` | Extraction smoke test, platform-gated reflink test |
| `internal/fetcher/pool.go` | Bounded `errgroup` helpers for batch download/extract |
| `internal/fetcher/pool_test.go` | Concurrency limits + cancellation propagation |

---

## Task 1: Add `golang.org/x/sys` and `golang.org/x/sync` dependencies

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: Add the modules**

Run:

```bash
cd /Users/torstendittmann/Documents/skunk/composer-go
go get golang.org/x/sys@latest
go get golang.org/x/sync@latest
```

Expected: both modules added to `go.mod`, `go.sum` populated.

- [ ] **Step 2: Verify they resolve**

Run: `go build ./...`

Expected: clean build (the modules are pulled but not yet used).

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add golang.org/x/{sys,sync} for fetcher and store"
```

---

## Task 2: Store — `Has` / `Path` / round-trip

**Files:**
- Create: `/Users/torstendittmann/Documents/skunk/composer-go/internal/store/store.go`
- Create: `/Users/torstendittmann/Documents/skunk/composer-go/internal/store/store_test.go`

The store is the simplest piece: a directory with one file per sha256. Tests can drive it with `bytes.Reader`; no HTTP needed yet.

- [ ] **Step 1: Write the failing test**

Create `/Users/torstendittmann/Documents/skunk/composer-go/internal/store/store_test.go`:

```go
package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestPutThenHasThenOpen(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	payload := []byte("hello, store")
	sum := sha256Hex(payload)

	if s.Has(sum) {
		t.Fatalf("Has on cold store returned true")
	}

	if err := s.Put(sum, bytes.NewReader(payload)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if !s.Has(sum) {
		t.Fatalf("Has after Put returned false")
	}

	rc, err := s.OpenReader(sum)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("round-trip mismatch: got %q, want %q", got, payload)
	}
}

func TestPutIsAtomic(t *testing.T) {
	// After a successful Put, no .tmp file should remain in the store dir.
	dir := t.TempDir()
	s, _ := New(dir)
	payload := []byte("x")
	if err := s.Put(sha256Hex(payload), bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("residual tmp file after Put: %s", e.Name())
		}
	}
}

func TestOpenReaderMissReturnsError(t *testing.T) {
	s, _ := New(t.TempDir())
	_, err := s.OpenReader("0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("expected error on miss")
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/store/...`

Expected: build error (`undefined: New`, `undefined: Store`).

- [ ] **Step 3: Implement the store**

Create `/Users/torstendittmann/Documents/skunk/composer-go/internal/store/store.go`:

```go
// Package store is a content-addressed blob store. It does not understand
// zips or any other format; it stores opaque byte streams keyed by sha256.
//
// Layout:   <root>/<sha256>.zip
// Atomic:   writes go to <sha256>.zip.tmp, fsync, rename into place.
//
// The .zip suffix is convention only — the store does not validate format.
// It chose this suffix because the only producer in this project is the
// fetcher and every artifact it stores is a Composer dist zip.
package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Store is a content-addressed blob store rooted at a single directory.
// It is safe for concurrent use: Put writes via tmp-then-rename, and Has
// and OpenReader only read.
type Store struct {
	root string
}

// New opens (or creates) a store rooted at root. The directory is created
// with 0o755 if missing.
func New(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("store: root must be non-empty")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("store: mkdir %s: %w", root, err)
	}
	return &Store{root: root}, nil
}

// Path returns the absolute path of the artifact for sha. The file may or
// may not exist; callers should check Has first.
func (s *Store) Path(sha string) string {
	return filepath.Join(s.root, sha+".zip")
}

// Has reports whether sha is present in the store.
func (s *Store) Has(sha string) bool {
	_, err := os.Stat(s.Path(sha))
	return err == nil
}

// Put writes the bytes of r into the store under sha. It is the caller's
// responsibility to ensure the bytes hash to sha; the store does not
// re-verify (the fetcher hashes during streaming and refuses to call Put
// unless the hash matches).
//
// On success the file at Path(sha) exists with permission 0o644. On any
// error the .tmp scratch file is removed.
func (s *Store) Put(sha string, r io.Reader) (retErr error) {
	final := s.Path(sha)
	tmp := final + ".tmp"

	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("store: open tmp: %w", err)
	}
	defer func() {
		if retErr != nil {
			_ = os.Remove(tmp)
		}
	}()

	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return fmt.Errorf("store: copy: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("store: fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("store: close: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("store: rename: %w", err)
	}
	return nil
}

// OpenReader returns a reader over the stored artifact. The caller must
// Close it. Returns an error wrapping os.ErrNotExist on a miss so callers
// can branch with errors.Is.
func (s *Store) OpenReader(sha string) (io.ReadCloser, error) {
	f, err := os.Open(s.Path(sha))
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", sha, err)
	}
	return f, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/store/...`

Expected: PASS for all three test functions.

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat(store): content-addressed blob store with atomic Put"
```

---

## Task 3: Fetcher — happy path against an `httptest.Server`

**Files:**
- Create: `/Users/torstendittmann/Documents/skunk/composer-go/internal/fetcher/fetcher.go`
- Create: `/Users/torstendittmann/Documents/skunk/composer-go/internal/fetcher/fetcher_test.go`

The fetcher is the only piece in this plan that talks HTTP. We test it with an `httptest.Server` that serves a zip generated in-memory at request time.

- [ ] **Step 1: Write the failing test**

Create `/Users/torstendittmann/Documents/skunk/composer-go/internal/fetcher/fetcher_test.go`:

```go
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
		"README.md":     "hello",
		"src/Foo.php":   "<?php class Foo {}",
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
	if err := f.Fetch(context.Background(), pv); err != nil {
		t.Fatalf("Fetch cold: %v", err)
	}
	if !st.Has(wantSha) {
		t.Fatalf("store missing artifact after cold fetch")
	}
	if hits != 1 {
		t.Errorf("hits = %d, want 1", hits)
	}

	// Warm: store hit, no network.
	if err := f.Fetch(context.Background(), pv); err != nil {
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
	err := f.Fetch(context.Background(), pv)
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
	if err := f.Fetch(context.Background(), pv); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !st.Has(wantSha) {
		t.Errorf("store missing artifact under computed sha %s", wantSha)
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/fetcher/...`

Expected: build error (`undefined: New`, `undefined: Fetcher`).

- [ ] **Step 3: Implement the fetcher**

Create `/Users/torstendittmann/Documents/skunk/composer-go/internal/fetcher/fetcher.go`:

```go
// Package fetcher downloads package zips, verifies them against the
// expected sha256, and persists them in a content-addressed store.
//
// The download is pipelined with verification: bytes flow through an
// io.TeeReader so the on-disk write and the running sha256 happen in
// lockstep with the network read. We do not pipeline extraction — that
// would require a streaming central-directory parser, which archive/zip
// does not provide. See plan 4 for the full trade-off discussion.
package fetcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/torstendittmann/composer-go/internal/registry"
	"github.com/torstendittmann/composer-go/internal/store"
)

// Fetcher coordinates downloads into a store. It is safe for concurrent use.
type Fetcher struct {
	store *store.Store
	http  *http.Client
}

// New returns a Fetcher backed by store and client. A nil client falls back
// to http.DefaultClient.
func New(s *store.Store, client *http.Client) *Fetcher {
	if client == nil {
		client = http.DefaultClient
	}
	return &Fetcher{store: s, http: client}
}

// Fetch ensures pv.Dist is present in the store. On a hit it returns nil
// without touching the network. On a miss it downloads pv.Dist.URL,
// streams the bytes through sha256 + a temp file, and renames into place
// only after verifying pv.Dist.Sha (when non-empty). On sha mismatch the
// temp file is removed and ErrShaMismatch is returned wrapped with the
// package name.
func (f *Fetcher) Fetch(ctx context.Context, pv registry.PackageVersion) error {
	if pv.Dist.Type != "zip" {
		return fmt.Errorf("fetcher: %s: unsupported dist type %q", pv.Name, pv.Dist.Type)
	}
	if pv.Dist.URL == "" {
		return fmt.Errorf("fetcher: %s: empty dist URL", pv.Name)
	}

	if pv.Dist.Sha != "" && f.store.Has(pv.Dist.Sha) {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pv.Dist.URL, nil)
	if err != nil {
		return fmt.Errorf("fetcher: %s: %w", pv.Name, err)
	}
	resp, err := f.http.Do(req)
	if err != nil {
		return fmt.Errorf("fetcher: %s: get: %w", pv.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetcher: %s: status %d", pv.Name, resp.StatusCode)
	}

	// Stream into a temp file inside the store dir (so the rename is
	// guaranteed same-filesystem). Hash in parallel via TeeReader.
	tmp, err := os.CreateTemp(filepath.Dir(f.store.Path("x")), "dl-*.zip")
	if err != nil {
		return fmt.Errorf("fetcher: %s: create tmp: %w", pv.Name, err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	hasher := sha256.New()
	tee := io.TeeReader(resp.Body, hasher)
	if _, err := io.Copy(tmp, tee); err != nil {
		cleanup()
		return fmt.Errorf("fetcher: %s: copy: %w", pv.Name, err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("fetcher: %s: fsync: %w", pv.Name, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("fetcher: %s: close: %w", pv.Name, err)
	}

	gotSha := hex.EncodeToString(hasher.Sum(nil))
	if pv.Dist.Sha != "" && pv.Dist.Sha != gotSha {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("fetcher: %s: %w (got %s, want %s)", pv.Name, ErrShaMismatch, gotSha, pv.Dist.Sha)
	}

	finalSha := pv.Dist.Sha
	if finalSha == "" {
		finalSha = gotSha
	}

	// Move temp into place. If a concurrent fetch beat us to it, that's fine
	// — the bytes are identical by construction.
	if err := os.Rename(tmpPath, f.store.Path(finalSha)); err != nil {
		_ = os.Remove(tmpPath)
		// If rename failed because the destination already exists on a
		// platform that disallows overwrite, treat as success.
		if errors.Is(err, os.ErrExist) || f.store.Has(finalSha) {
			return nil
		}
		return fmt.Errorf("fetcher: %s: rename: %w", pv.Name, err)
	}
	return nil
}

// ErrShaMismatch is returned by Fetch when the downloaded bytes do not hash
// to the expected sha. Use errors.Is to test.
var ErrShaMismatch = errors.New("dist sha mismatch")
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/fetcher/...`

Expected: PASS for `TestFetchColdStoreThenWarmHit`, `TestFetchRejectsShaMismatch`, `TestFetchEmptyShaSkipsVerification`.

- [ ] **Step 5: Commit**

```bash
git add internal/fetcher
git commit -m "feat(fetcher): pipelined download + sha-verified store insert"
```

---

## Task 4: Materialize — common dispatch + zip extraction

**Files:**
- Create: `/Users/torstendittmann/Documents/skunk/composer-go/internal/fetcher/materialize.go`
- Create: `/Users/torstendittmann/Documents/skunk/composer-go/internal/fetcher/materialize_test.go`

`Materialize` reads a stored zip and writes its contents into a target directory. Composer dists usually wrap the package contents in a single top-level directory (e.g. `Seldaek-monolog-abc123/`); we strip exactly one path component when present.

- [ ] **Step 1: Write the failing test**

Create `/Users/torstendittmann/Documents/skunk/composer-go/internal/fetcher/materialize_test.go`:

```go
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
		"vendor-pkg-abc123/composer.json":    `{"name":"vendor/pkg"}`,
		"vendor-pkg-abc123/src/Foo.php":      "<?php class Foo {}",
		"vendor-pkg-abc123/README.md":        "hi",
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
		"composer.json":  `{"name":"vendor/flat"}`,
		"src/Foo.php":    "<?php",
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
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/fetcher/...`

Expected: build error (`undefined: Materialize`).

- [ ] **Step 3: Implement materialize**

Create `/Users/torstendittmann/Documents/skunk/composer-go/internal/fetcher/materialize.go`:

```go
package fetcher

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/torstendittmann/composer-go/internal/registry"
)

// Materialize expands the stored zip for pv into target. The target
// directory is created if missing and pre-existing contents are
// overwritten file-by-file (callers that want a clean directory should
// remove it first).
//
// Composer dists usually wrap their contents in a single top-level
// directory whose name we cannot predict. We detect that case (every entry
// shares the same first path component) and strip it.
//
// File contents are written via the platform-specific copy strategy
// chain — see materialize_{darwin,linux,other}.go. Symlinks are not yet
// honored; they are skipped with a warning. Directory entries create the
// directory; regular files are written through copyFile.
func (f *Fetcher) Materialize(ctx context.Context, pv registry.PackageVersion, target string) error {
	sha := pv.Dist.Sha
	if sha == "" {
		return fmt.Errorf("fetcher: %s: cannot materialize without sha (call Fetch first)", pv.Name)
	}
	if !f.store.Has(sha) {
		return fmt.Errorf("fetcher: %s: not in store (sha %s)", pv.Name, sha)
	}

	src := f.store.Path(sha)
	zr, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("fetcher: %s: open zip: %w", pv.Name, err)
	}
	defer zr.Close()

	strip := commonPrefix(zr.File)

	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("fetcher: %s: mkdir target: %w", pv.Name, err)
	}

	for _, ze := range zr.File {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel := strings.TrimPrefix(ze.Name, strip)
		if rel == "" {
			continue
		}
		// Path traversal guard: the cleaned, target-rooted path must remain
		// inside target.
		dst := filepath.Join(target, filepath.FromSlash(rel))
		if !strings.HasPrefix(dst, filepath.Clean(target)+string(os.PathSeparator)) && dst != filepath.Clean(target) {
			return fmt.Errorf("fetcher: %s: zip entry %q escapes target", pv.Name, ze.Name)
		}

		if ze.FileInfo().IsDir() {
			if err := os.MkdirAll(dst, ze.Mode().Perm()|0o700); err != nil {
				return fmt.Errorf("fetcher: %s: mkdir %s: %w", pv.Name, dst, err)
			}
			continue
		}
		if ze.Mode()&os.ModeSymlink != 0 {
			// TODO(stage 2): honour symlinks if any real package needs them.
			continue
		}

		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("fetcher: %s: mkdir parent: %w", pv.Name, err)
		}
		if err := writeZipEntry(ze, dst); err != nil {
			return fmt.Errorf("fetcher: %s: write %s: %w", pv.Name, rel, err)
		}
	}
	return nil
}

// commonPrefix returns the single top-level directory component shared by
// every entry in files, with a trailing slash, or "" if no such prefix
// exists. It treats the zip as flat when even one entry lacks a slash.
func commonPrefix(files []*zip.File) string {
	if len(files) == 0 {
		return ""
	}
	first := files[0].Name
	slash := strings.IndexByte(first, '/')
	if slash < 0 {
		return ""
	}
	prefix := first[:slash+1]
	for _, ze := range files[1:] {
		if !strings.HasPrefix(ze.Name, prefix) {
			return ""
		}
	}
	return prefix
}

// writeZipEntry copies ze into dst. We always write through the temp +
// rename pattern so a panicked extraction never leaves a half-written file
// in vendor/.
func writeZipEntry(ze *zip.File, dst string) error {
	rc, err := ze.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, ze.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// errFallthrough is returned by platform fast-path implementations to
// signal "not supported on this filesystem, try the next strategy."
var errFallthrough = errors.New("fetcher: strategy not supported, falling through")
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/fetcher/...`

Expected: PASS for the three new materialize tests plus the three from task 3.

- [ ] **Step 5: Commit**

```bash
git add internal/fetcher
git commit -m "feat(fetcher): zip materialization with prefix-stripping and traversal guard"
```

---

## Task 5: Reflink fast paths (build-tag split)

**Files:**
- Create: `/Users/torstendittmann/Documents/skunk/composer-go/internal/fetcher/materialize_darwin.go`
- Create: `/Users/torstendittmann/Documents/skunk/composer-go/internal/fetcher/materialize_linux.go`
- Create: `/Users/torstendittmann/Documents/skunk/composer-go/internal/fetcher/materialize_other.go`

The fetcher's per-file write today goes through `io.Copy`. This task introduces `cloneFile(src, dst)` that tries `clonefile(2)` on macOS, `FICLONE` on Linux, and falls through to `errFallthrough` on every other platform. The function is wired into `writeZipEntry` only when the source already exists as a real file on disk — i.e. for "rematerialize from a different vendor dir" use cases that arrive in stage 3. For now we expose the helpers and exercise them from a platform-gated test, so plan 5's orchestrator can call them.

- [ ] **Step 1: Write the failing test**

Append to `/Users/torstendittmann/Documents/skunk/composer-go/internal/fetcher/materialize_test.go`:

```go
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
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/fetcher/...`

Expected: build error (`undefined: CloneOrCopy`).

- [ ] **Step 3: Add the platform helpers**

Create `/Users/torstendittmann/Documents/skunk/composer-go/internal/fetcher/materialize_darwin.go`:

```go
//go:build darwin

package fetcher

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// CloneOrCopy copies src to dst, preferring APFS clonefile, then hardlink,
// then byte-for-byte copy. On macOS clonefile shares the underlying APFS
// extents until either side is mutated — effectively free for installs
// that only ever read vendor/.
//
// Errors from clonefile that mean "not supported on this filesystem" are
// swallowed and we fall through. Unexpected errors are returned as-is.
func CloneOrCopy(src, dst string) error {
	// 1. clonefile.
	switch err := unix.Clonefile(src, dst, 0); {
	case err == nil:
		return nil
	case errors.Is(err, unix.ENOTSUP), errors.Is(err, unix.EXDEV),
		errors.Is(err, unix.ENOSYS), errors.Is(err, unix.EINVAL),
		errors.Is(err, os.ErrExist):
		// fall through
	default:
		return err
	}

	// 2. hardlink.
	if err := os.Link(src, dst); err == nil {
		return nil
	}

	// 3. copy.
	return copyFileBytes(src, dst)
}
```

Create `/Users/torstendittmann/Documents/skunk/composer-go/internal/fetcher/materialize_linux.go`:

```go
//go:build linux

package fetcher

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// CloneOrCopy copies src to dst, preferring FICLONE (btrfs/xfs reflink),
// then hardlink, then byte-for-byte copy. FICLONE returns EOPNOTSUPP /
// EXDEV on filesystems that don't support reflink — both are treated as
// "fall through to the next strategy."
func CloneOrCopy(src, dst string) error {
	// 1. FICLONE.
	srcF, err := os.Open(src)
	if err == nil {
		dstF, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err == nil {
			ioErr := unix.IoctlFileClone(int(dstF.Fd()), int(srcF.Fd()))
			closeErr := dstF.Close()
			_ = srcF.Close()
			if ioErr == nil && closeErr == nil {
				return nil
			}
			// On any error, remove the empty/partial dst and try the next
			// strategy. Note: IoctlFileClone is in golang.org/x/sys/unix
			// (it wraps the FICLONE ioctl). If a future module bump removes
			// the helper, swap to unix.IoctlSetInt(fd, unix.FICLONE, srcFd).
			_ = os.Remove(dst)
			if ioErr != nil && !isReflinkUnsupported(ioErr) {
				return ioErr
			}
		} else {
			_ = srcF.Close()
		}
	}

	// 2. hardlink.
	if err := os.Link(src, dst); err == nil {
		return nil
	}

	// 3. copy.
	return copyFileBytes(src, dst)
}

func isReflinkUnsupported(err error) bool {
	return errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.ENOTSUP) ||
		errors.Is(err, unix.EXDEV) ||
		errors.Is(err, unix.EINVAL) ||
		errors.Is(err, unix.ENOSYS)
}
```

Create `/Users/torstendittmann/Documents/skunk/composer-go/internal/fetcher/materialize_other.go`:

```go
//go:build !darwin && !linux

package fetcher

import "os"

// CloneOrCopy on non-APFS, non-Linux platforms tries hardlink first, then
// byte-for-byte copy. There is no reflink primitive available portably.
func CloneOrCopy(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	return copyFileBytes(src, dst)
}
```

Append the shared helper to `/Users/torstendittmann/Documents/skunk/composer-go/internal/fetcher/materialize.go`:

```go
// copyFileBytes is the universal-fallback step of CloneOrCopy. It is
// intentionally unexported and platform-agnostic.
func copyFileBytes(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}
```

> **Implementer note on `unix.IoctlFileClone`.** As of `golang.org/x/sys` v0.20+, `unix.IoctlFileClone(dstFd, srcFd int) error` exists on Linux and is the canonical helper. If your pinned module version lacks it, fall back to `unix.IoctlSetInt(dstFd, unix.FICLONE, srcFd)` — it has the same wire semantics. Verify with `go doc golang.org/x/sys/unix IoctlFileClone` before substituting.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/fetcher/...`

Expected: PASS on darwin and linux; PASS with reflink-skip on other platforms.

- [ ] **Step 5: Commit**

```bash
git add internal/fetcher
git commit -m "feat(fetcher): platform-specific CloneOrCopy with reflink/hardlink/copy chain"
```

---

## Task 6: Bounded concurrent batch fetch + extract

**Files:**
- Create: `/Users/torstendittmann/Documents/skunk/composer-go/internal/fetcher/pool.go`
- Create: `/Users/torstendittmann/Documents/skunk/composer-go/internal/fetcher/pool_test.go`

The orchestrator (plan 6) will pass us a slice of `registry.PackageVersion` and a `vendor/` root. We expose two helpers — `FetchAll` and `MaterializeAll` — both backed by `errgroup` with `SetLimit(runtime.NumCPU())`. They are independent so the orchestrator can begin extracting any package whose download has completed without waiting for the slowest download.

- [ ] **Step 1: Write the failing test**

Create `/Users/torstendittmann/Documents/skunk/composer-go/internal/fetcher/pool_test.go`:

```go
package fetcher

import (
	"context"
	"net/http"
	"net/http/httptest"
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
```

Add to imports if not already present: `"os"`.

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/fetcher/...`

Expected: build error (`undefined: FetchAll`, `undefined: MaterializeAll`).

- [ ] **Step 3: Implement the pool**

Create `/Users/torstendittmann/Documents/skunk/composer-go/internal/fetcher/pool.go`:

```go
package fetcher

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/torstendittmann/composer-go/internal/registry"
)

// FetchAll downloads every pv in parallel with at most `limit` requests in
// flight at once. A `limit` of 0 or negative means runtime.NumCPU().
//
// On the first error, FetchAll cancels the underlying context and returns
// that error; in-flight downloads abort via the cancelled context. Already-
// completed downloads remain in the store — they're idempotent by sha.
func (f *Fetcher) FetchAll(ctx context.Context, pvs []registry.PackageVersion, limit int) error {
	if limit <= 0 {
		limit = runtime.NumCPU()
	}
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(limit)
	for _, pv := range pvs {
		pv := pv
		g.Go(func() error { return f.Fetch(ctx, pv) })
	}
	return g.Wait()
}

// MaterializeAll expands every pv into vendorRoot/<vendor>/<name>/ in
// parallel with at most `limit` extractions running concurrently. Each pv
// must already be present in the store (call FetchAll first).
func (f *Fetcher) MaterializeAll(ctx context.Context, pvs []registry.PackageVersion, vendorRoot string, limit int) error {
	if limit <= 0 {
		limit = runtime.NumCPU()
	}
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(limit)
	for _, pv := range pvs {
		pv := pv
		g.Go(func() error {
			target := vendorTargetFor(vendorRoot, pv.Name)
			return f.Materialize(ctx, pv, target)
		})
	}
	return g.Wait()
}

// vendorTargetFor maps "vendor/pkg" to "<vendorRoot>/vendor/pkg". Composer
// package names are always "<vendor>/<name>" with a single slash.
func vendorTargetFor(vendorRoot, name string) string {
	parts := strings.SplitN(name, "/", 2)
	if len(parts) == 2 {
		return filepath.Join(vendorRoot, parts[0], parts[1])
	}
	return filepath.Join(vendorRoot, name)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/fetcher/...`

Expected: PASS for all `pool_test.go` cases.

- [ ] **Step 5: Commit**

```bash
git add internal/fetcher
git commit -m "feat(fetcher): bounded errgroup pool for parallel fetch and materialize"
```

---

## Task 7: Cancellation propagation smoke test

**Files:**
- Modify: `/Users/torstendittmann/Documents/skunk/composer-go/internal/fetcher/pool_test.go`

A regression guard: a cancelled context must abort in-flight downloads promptly.

- [ ] **Step 1: Append the test**

```go
func TestFetchAllRespectsContextCancel(t *testing.T) {
	// A handler that blocks until the request context is cancelled.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	st, _ := store.New(t.TempDir())
	f := New(st, srv.Client())
	pvs := []registry.PackageVersion{
		{Name: "v/slow", Version: "1", Dist: registry.Dist{Type: "zip", URL: srv.URL + "/slow"}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Cancel shortly after starting.
		cancel()
	}()
	if err := f.FetchAll(ctx, pvs, 1); err == nil {
		t.Fatal("expected cancellation error")
	}
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./internal/fetcher/...`

Expected: PASS — the cancelled request returns an error promptly rather than hanging.

- [ ] **Step 3: Commit**

```bash
git add internal/fetcher/pool_test.go
git commit -m "test(fetcher): assert FetchAll honours context cancellation"
```

---

## Task 8: Wire the store into the project cache layout

**Files:**
- Create: `/Users/torstendittmann/Documents/skunk/composer-go/internal/store/layout.go`
- Modify: `/Users/torstendittmann/Documents/skunk/composer-go/internal/store/store_test.go`

The spec says the store defaults to `<project>/.composer-go/store` so reflinks land on the same filesystem as `vendor/`. We expose a single helper that returns the conventional path and let plan 6 wire it up. We also keep `New(dir)` flexible for tests and for users who explicitly point at a shared cache.

- [ ] **Step 1: Write the failing test**

Append to `/Users/torstendittmann/Documents/skunk/composer-go/internal/store/store_test.go`:

```go
func TestProjectStoreDir(t *testing.T) {
	got := ProjectStoreDir("/work/proj")
	want := filepath.Join("/work/proj", ".composer-go", "store")
	if got != want {
		t.Errorf("ProjectStoreDir = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/store/...`

Expected: build error (`undefined: ProjectStoreDir`).

- [ ] **Step 3: Implement layout**

Create `/Users/torstendittmann/Documents/skunk/composer-go/internal/store/layout.go`:

```go
package store

import "path/filepath"

// ProjectStoreDir returns the conventional store path for a project rooted
// at projectDir. Co-locating the store under .composer-go/ keeps it on the
// same filesystem as vendor/, which is a precondition for reflink and
// hardlink to succeed.
//
// Users who want a shared cache across projects can pass an explicit path
// to New() instead.
func ProjectStoreDir(projectDir string) string {
	return filepath.Join(projectDir, ".composer-go", "store")
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/store/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat(store): ProjectStoreDir helper for per-project store layout"
```

---

## Plan 4 acceptance check

After all tasks:

- `go test ./...` is green on darwin and linux. On other platforms, only the platform-gated reflink test is skipped.
- `go build ./cmd/composer-go` still produces a binary; nothing in `cmd/` was modified by this plan.
- A small benchmark sanity check (manual, not committed): create a 5-package fixture set with `httptest.Server`, run `FetchAll` with `limit=4` on a warm store, and confirm zero network hits and zero new files in the store dir.
- Public surface stable for plan 6 (orchestrator):
  - `store.New(root string) (*store.Store, error)`, `(*Store).Has`, `(*Store).Put`, `(*Store).OpenReader`, `(*Store).Path`, `store.ProjectStoreDir`.
  - `fetcher.New(*store.Store, *http.Client) *Fetcher`, `(*Fetcher).Fetch`, `(*Fetcher).Materialize`, `(*Fetcher).FetchAll`, `(*Fetcher).MaterializeAll`, `fetcher.CloneOrCopy`, `fetcher.ErrShaMismatch`.
- The pipelined-extract trade-off is documented at the top of `internal/fetcher/fetcher.go` so future readers know why we tee+disk rather than parse-as-we-stream.
- Materialization correctness: a Composer-style wrapped zip and a flat zip both expand to the same on-disk layout under `vendor/<vendor>/<name>/`. A zip with `..` entries is rejected.

If any of these fails, fix forward in a follow-up commit before declaring Plan 4 done.
