// Workspaces post-materialize symlink pass. See
// docs/superpowers/specs/2026-07-10-workspaces-design.md, "Install &
// vendor layout". All symlinks emitted are relative.
package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/torstendittmann/gomposer/internal/manifest"
)

func linkWorkspaces(rootDir string, workspaces []manifest.Workspace) error {
	rootVendor := filepath.Join(rootDir, "vendor")
	for _, w := range workspaces {
		// 1. Workspace's own vendor/ → root vendor/.
		wsVendor := filepath.Join(w.Dir, "vendor")
		relRoot, err := filepath.Rel(w.Dir, rootVendor)
		if err != nil {
			return fmt.Errorf("workspaces: rel %s -> %s: %w", w.Dir, rootVendor, err)
		}
		if err := replaceSymlink(wsVendor, relRoot); err != nil {
			return fmt.Errorf("workspaces: symlink %s: %w", wsVendor, err)
		}

		// 2. vendor/<vendor>/<name> → workspace source dir.
		crossLink := filepath.Join(rootVendor, filepath.FromSlash(w.Name))
		if err := os.MkdirAll(filepath.Dir(crossLink), 0o755); err != nil {
			return fmt.Errorf("workspaces: mkdir %s: %w", filepath.Dir(crossLink), err)
		}
		relTarget, err := filepath.Rel(filepath.Dir(crossLink), w.Dir)
		if err != nil {
			return fmt.Errorf("workspaces: rel %s -> %s: %w", filepath.Dir(crossLink), w.Dir, err)
		}
		if err := replaceSymlink(crossLink, relTarget); err != nil {
			return fmt.Errorf("workspaces: symlink %s: %w", crossLink, err)
		}
	}
	return nil
}

// replaceSymlink writes linkPath → target atomically. If linkPath exists
// (as a file, dir, or existing symlink), it's removed first.
func replaceSymlink(linkPath, target string) error {
	if err := os.RemoveAll(linkPath); err != nil {
		return err
	}
	return os.Symlink(target, linkPath)
}
