package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Workspace is one member of the workspace set discovered by
// DiscoverWorkspaces.
type Workspace struct {
	// Name is the workspace's composer.json "name", e.g. "acme/shared".
	Name string
	// Dir is the workspace's directory, absolute or as-passed to DiscoverWorkspaces.
	Dir string
	// Manifest is the parsed composer.json.
	Manifest *Manifest
	// Version is a convenience copy of Manifest.Version (may be empty).
	Version string
}

// DiscoverWorkspaces glob-expands root.Workspaces relative to rootDir,
// loads each matched directory's composer.json, and returns the collection.
// Dedup by name is enforced with a hard error. Glob patterns matching zero
// directories emit a warning via warnf (nil is treated as no-op). Empty or
// nil root.Workspaces returns (nil, nil).
func DiscoverWorkspaces(rootDir string, root *Manifest, warnf func(format string, args ...any)) ([]Workspace, error) {
	if root == nil || len(root.Workspaces) == 0 {
		return nil, nil
	}
	if warnf == nil {
		warnf = func(string, ...any) {}
	}
	seen := map[string]string{} // name -> first dir (for duplicate error msg)
	out := []Workspace{}
	for _, pattern := range root.Workspaces {
		abs := filepath.Join(rootDir, pattern)
		matches, err := filepath.Glob(abs)
		if err != nil {
			return nil, fmt.Errorf("workspaces: glob %q: %w", pattern, err)
		}
		if len(matches) == 0 {
			warnf("workspaces: pattern %q matched no directories", pattern)
			continue
		}
		sort.Strings(matches)
		for _, m := range matches {
			info, err := os.Stat(m)
			if err != nil || !info.IsDir() {
				continue // non-directory match; skip silently
			}
			composer := filepath.Join(m, "composer.json")
			if _, err := os.Stat(composer); os.IsNotExist(err) {
				continue // no composer.json here — treat as sibling README dir
			}
			ws, err := Load(m)
			if err != nil {
				return nil, fmt.Errorf("workspaces: load %s: %w", m, err)
			}
			name := ws.Name
			if name == "" {
				return nil, fmt.Errorf("workspaces: %s: composer.json has no name", m)
			}
			if prev, dup := seen[name]; dup {
				return nil, fmt.Errorf("workspaces: duplicate name %q at %s and %s", name, prev, m)
			}
			seen[name] = m
			out = append(out, Workspace{
				Name:     name,
				Dir:      m,
				Manifest: ws,
				Version:  ws.Version,
			})
		}
	}
	return out, nil
}
