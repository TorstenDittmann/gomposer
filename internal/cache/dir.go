// Package cache exposes the cache root path used by all cache layers.
// Sub-packages (httpcache, parsedcache) live under that root.
package cache

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

const dirName = "gomposer"

// Root returns the absolute path to the gomposer cache directory.
// It does NOT create the directory; callers create per-layer subdirs.
//
// Resolution order:
//  1. $XDG_CACHE_HOME/gomposer (if set, regardless of OS)
//  2. macOS: $HOME/Library/Caches/gomposer
//  3. other: $HOME/.cache/gomposer
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
