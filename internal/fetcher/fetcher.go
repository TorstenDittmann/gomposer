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
	// OnFetch, if non-nil, is invoked exactly once per Fetch call after the
	// outcome is known. fromCache=true means the bytes were already in the
	// store and no network was used (bytes is then 0). fromCache=false
	// means we downloaded `bytes` bytes.
	//
	// The hook is expected to be cheap and non-blocking; the orchestrator
	// uses it to drive a Timings counter from worker goroutines.
	OnFetch func(name string, bytes int, fromCache bool)
}

// New returns a Fetcher backed by store and client. A nil client falls back
// to http.DefaultClient.
func New(s *store.Store, client *http.Client) *Fetcher {
	if client == nil {
		client = http.DefaultClient
	}
	return &Fetcher{store: s, http: client}
}

// Fetch ensures pv.Dist is present in the store. It returns the sha256 of
// the stored bytes (which is also the store key) on success. On a hit it
// returns the known sha without touching the network. On a miss it
// downloads pv.Dist.URL, streams the bytes through sha256 + a temp file,
// and renames into place only after verifying pv.Dist.Sha (when non-empty).
// On sha mismatch the temp file is removed and ErrShaMismatch is returned
// wrapped with the package name.
func (f *Fetcher) Fetch(ctx context.Context, pv registry.PackageVersion) (string, error) {
	if pv.Dist.Type != "zip" {
		return "", fmt.Errorf("fetcher: %s: unsupported dist type %q", pv.Name, pv.Dist.Type)
	}
	if pv.Dist.URL == "" {
		return "", fmt.Errorf("fetcher: %s: empty dist URL", pv.Name)
	}

	if pv.Dist.Sha != "" && f.store.Has(pv.Dist.Sha) {
		if f.OnFetch != nil {
			f.OnFetch(pv.Name, 0, true)
		}
		return pv.Dist.Sha, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pv.Dist.URL, nil)
	if err != nil {
		return "", fmt.Errorf("fetcher: %s: %w", pv.Name, err)
	}
	resp, err := f.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetcher: %s: get: %w", pv.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetcher: %s: status %d", pv.Name, resp.StatusCode)
	}

	// Stream into a temp file inside the store dir (so the rename is
	// guaranteed same-filesystem). Hash in parallel via TeeReader.
	tmp, err := os.CreateTemp(filepath.Dir(f.store.Path("x")), "dl-*.zip")
	if err != nil {
		return "", fmt.Errorf("fetcher: %s: create tmp: %w", pv.Name, err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	hasher := sha256.New()
	tee := io.TeeReader(resp.Body, hasher)
	n, err := io.Copy(tmp, tee)
	if err != nil {
		cleanup()
		return "", fmt.Errorf("fetcher: %s: copy: %w", pv.Name, err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return "", fmt.Errorf("fetcher: %s: fsync: %w", pv.Name, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("fetcher: %s: close: %w", pv.Name, err)
	}

	gotSha := hex.EncodeToString(hasher.Sum(nil))
	if pv.Dist.Sha != "" && pv.Dist.Sha != gotSha {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("fetcher: %s: %w (got %s, want %s)", pv.Name, ErrShaMismatch, gotSha, pv.Dist.Sha)
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
			if f.OnFetch != nil {
				f.OnFetch(pv.Name, int(n), false)
			}
			return finalSha, nil
		}
		return "", fmt.Errorf("fetcher: %s: rename: %w", pv.Name, err)
	}
	if f.OnFetch != nil {
		f.OnFetch(pv.Name, int(n), false)
	}
	return finalSha, nil
}

// ErrShaMismatch is returned by Fetch when the downloaded bytes do not hash
// to the expected sha. Use errors.Is to test.
var ErrShaMismatch = errors.New("dist sha mismatch")
