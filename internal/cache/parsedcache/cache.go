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
