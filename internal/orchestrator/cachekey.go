package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/torstendittmann/gomposer/internal/cache"
	"github.com/torstendittmann/gomposer/internal/lock"
)

// computeCacheKey is the resolution-result cache key. It MUST be:
//   - deterministic for the same inputs (so warm runs hit)
//   - sensitive to manifest, lock content, and platform (so stale entries
//     cannot be served on changed inputs)
//
// We hash a length-prefixed encoding so that, e.g., manifest=[ab]/lock=[]
// cannot collide with manifest=[a]/lock=[b].
func computeCacheKey(manifestBytes, lockBytes []byte, platform string) string {
	h := sha256.New()
	writeLengthed(h, manifestBytes)
	writeLengthed(h, lockBytes)
	writeLengthed(h, []byte(platform))
	return hex.EncodeToString(h.Sum(nil))
}

func writeLengthed(h interface{ Write(p []byte) (int, error) }, b []byte) {
	var lenBuf [8]byte
	n := uint64(len(b))
	for i := 0; i < 8; i++ {
		lenBuf[i] = byte(n >> (8 * i))
	}
	_, _ = h.Write(lenBuf[:])
	_, _ = h.Write(b)
}

// resolutionCacheDir returns the directory where resolution-result entries
// live. Each entry is a serialized lock.File keyed by computeCacheKey.
func resolutionCacheDir() (string, error) {
	root, err := cache.Root()
	if err != nil {
		return "", err
	}
	d := filepath.Join(root, cache.LayerResolution.Subdir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", err
	}
	return d, nil
}

// loadResolution returns (file, true, nil) on cache hit, or (nil, false, nil)
// on miss. Decode failures are treated as miss (and the corrupt file evicted)
// because cache integrity is enforced by the spec's "evict + refetch, never
// silently serve corrupt data" rule.
func loadResolution(key string) (*lock.File, bool, error) {
	dir, err := resolutionCacheDir()
	if err != nil {
		return nil, false, err
	}
	path := filepath.Join(dir, key+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	f, err := lock.Decode(data)
	if err != nil {
		_ = os.Remove(path)
		return nil, false, nil
	}
	return f, true, nil
}

// storeResolution writes a resolved lock.File to the cache. Cache write
// failures are non-fatal at the call site; we still return them so callers
// can log them with --verbose.
func storeResolution(key string, f *lock.File) error {
	dir, err := resolutionCacheDir()
	if err != nil {
		return err
	}
	data, err := f.Encode()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, key+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("orchestrator: write resolution cache: %w", err)
	}
	return os.Rename(tmp, path)
}
