package cache

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Layer identifies one cache subdirectory. Name is the user-facing
// identifier used in CLI arguments and output; Subdir is the on-disk
// directory name under Root(). They differ for "metadata", whose
// directory is "packagist" for historical layout reasons but whose
// user-facing concept is registry metadata.
type Layer struct {
	Name   string
	Subdir string
}

// The four cache layers. Every consumer of a cache subdirectory must
// source the name from here — see Layers() for the display-ordered set.
var (
	LayerStore      = Layer{Name: "store", Subdir: "store"}           // content-addressed package archives
	LayerMetadata   = Layer{Name: "metadata", Subdir: "packagist"}    // registry HTTP + parsed metadata
	LayerResolution = Layer{Name: "resolution", Subdir: "resolution"} // resolver result cache
	LayerVCS        = Layer{Name: "vcs", Subdir: "vcs"}               // VCS clone cache
)

// Layers returns the fixed registry in display order.
func Layers() []Layer {
	return []Layer{LayerStore, LayerMetadata, LayerResolution, LayerVCS}
}

// LayerByName looks a layer up by its user-facing name.
func LayerByName(name string) (Layer, bool) {
	for _, l := range Layers() {
		if l.Name == name {
			return l, true
		}
	}
	return Layer{}, false
}

// Path returns the absolute directory for the layer (Root()/Subdir).
// It does not create the directory.
func (l Layer) Path() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, l.Subdir), nil
}

// Size returns the total bytes of all regular files under the layer's
// directory. A missing directory is 0 bytes, not an error. Walk errors
// on individual entries abort with the error — a partial sum would
// silently lie.
func (l Layer) Size() (int64, error) {
	dir, err := l.Path()
	if err != nil {
		return 0, err
	}
	var total int64
	err = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("cache: size %s: %w", l.Name, err)
	}
	return total, nil
}

// Clear removes the layer's directory tree and returns the bytes freed
// (its Size immediately before removal). Clearing a missing directory
// is a no-op returning 0. Consumers recreate their directories on
// demand (store.New, the packagist caches, resolutionCacheDir, and the
// VCS cache all MkdirAll on first use), so Clear does not recreate
// anything.
func (l Layer) Clear() (int64, error) {
	size, err := l.Size()
	if err != nil {
		return 0, err
	}
	dir, err := l.Path()
	if err != nil {
		return 0, err
	}
	if err := os.RemoveAll(dir); err != nil {
		return 0, fmt.Errorf("cache: clear %s: %w", l.Name, err)
	}
	return size, nil
}
