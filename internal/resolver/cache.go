package resolver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// CachedSolver wraps Solve with a disk-backed result cache. The cache is
// keyed by (manifestContentHash, lockContentHash, platformFingerprint,
// IncludeDev, MinimumStability). On a hit the wrapped registry SourceLookup
// is not called at all.
type CachedSolver struct {
	dir string
}

// NewCachedSolver creates a cache rooted at dir. Sub-dirs are created lazily.
func NewCachedSolver(dir string) (*CachedSolver, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &CachedSolver{dir: dir}, nil
}

// Solve returns a cached result if one exists for the given key, otherwise
// runs Solve and stores the result.
//
// `manifestHash` and `lockHash` are computed by the caller (hex-encoded
// sha256 strings, with an empty string permitted when no lockfile exists).
// The platform fingerprint comes from `in.PlatformFingerprint`.
func (cs *CachedSolver) Solve(ctx context.Context, in Input, manifestHash, lockHash string) (*Result, error) {
	key := cs.key(in, manifestHash, lockHash)
	if r, ok := cs.load(key); ok {
		return r, nil
	}
	r, err := Solve(ctx, in)
	if err != nil {
		return nil, err
	}
	if err := cs.store(key, r); err != nil {
		// Cache failures are non-fatal — the result is still correct.
		_ = err
	}
	return r, nil
}

func (cs *CachedSolver) key(in Input, manifestHash, lockHash string) string {
	h := sha256.New()
	h.Write([]byte("v1\n"))
	h.Write([]byte("manifest:" + manifestHash + "\n"))
	h.Write([]byte("lock:" + lockHash + "\n"))
	h.Write([]byte("platform:" + in.PlatformFingerprint + "\n"))
	h.Write([]byte("dev:" + strconv.FormatBool(in.IncludeDev) + "\n"))
	h.Write([]byte("stab:" + in.MinimumStability + "\n"))
	return hex.EncodeToString(h.Sum(nil))
}

func (cs *CachedSolver) path(key string) string {
	return filepath.Join(cs.dir, key[:2], key+".gob")
}

func (cs *CachedSolver) load(key string) (*Result, bool) {
	f, err := os.Open(cs.path(key))
	if err != nil {
		return nil, false
	}
	defer f.Close()
	var r Result
	if err := gob.NewDecoder(f).Decode(&r); err != nil && !errors.Is(err, io.EOF) {
		// Corrupt entry: evict, treat as miss.
		_ = os.Remove(cs.path(key))
		return nil, false
	}
	return &r, true
}

func (cs *CachedSolver) store(key string, r *Result) error {
	if err := os.MkdirAll(filepath.Dir(cs.path(key)), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(r); err != nil {
		return fmt.Errorf("resolver/cache: encode: %w", err)
	}
	tmp := cs.path(key) + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, cs.path(key))
}
