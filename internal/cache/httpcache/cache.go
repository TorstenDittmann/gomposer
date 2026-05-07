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

// CredentialsResolver returns a value suitable for the Authorization header
// for the given hostname, plus an ok flag.
//
// Implementations typically wrap auth.Store: callers convert the resolved
// auth.Credentials into a single header value via Credentials.AuthorizationHeader().
type CredentialsResolver interface {
	AuthHeader(host string) (value string, ok bool)
}

// CredentialsFunc adapts a function to CredentialsResolver.
type CredentialsFunc func(host string) (string, bool)

func (f CredentialsFunc) AuthHeader(host string) (string, bool) { return f(host) }

// Cache reads-through to an HTTP client and persists 200 responses
// keyed by sha256(URL).
type Cache struct {
	dir         string
	client      *http.Client
	Credentials CredentialsResolver // optional; nil means no auth injection
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

	if c.Credentials != nil {
		if v, ok := c.Credentials.AuthHeader(req.URL.Host); ok && v != "" {
			req.Header.Set("Authorization", v)
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
