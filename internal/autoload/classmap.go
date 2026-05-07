package autoload

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/torstendittmann/composer-go/internal/manifest"
)

// CollectClassmap walks every classmap entry of every package (and the
// root manifest's autoload.classmap) and returns the merged
// qualified-name → project-relative-path map.
//
// projectDir must be absolute. Path values in the result are forward-
// slash regardless of host OS so they round-trip cleanly into PHP.
//
// On a same-name collision (two packages declare the same class), the
// FIRST occurrence wins; we keep the same first-wins behaviour Composer
// has used since 2.0. A diagnostic is appended to the returned warnings
// slice in a follow-up; for now collisions are silently absorbed.
func CollectClassmap(projectDir string, root manifest.Autoload, entries []Entry) (map[string]string, error) {
	out := make(map[string]string)

	// Vendor entries first. Order matches Entry order so first-wins is
	// stable.
	for _, e := range entries {
		excl, err := compileExclude(e.ExcludeFromClassmap)
		if err != nil {
			return nil, err
		}
		for _, raw := range e.Autoload.Classmap {
			if err := scanInto(out, projectDir, e.InstallPath, raw, excl); err != nil {
				return nil, fmt.Errorf("autoload: %s: %w", e.Name, err)
			}
		}
	}
	// Root manifest entries, paths are relative to projectDir directly.
	rootExcl, err := compileExclude(rootExcludePatterns(root))
	if err != nil {
		return nil, err
	}
	for _, raw := range root.Classmap {
		if err := scanInto(out, projectDir, "", raw, rootExcl); err != nil {
			return nil, fmt.Errorf("autoload: root manifest: %w", err)
		}
	}
	return out, nil
}

// rootExcludePatterns reads the root manifest's exclude-from-classmap. The
// manifest.Autoload struct may grow this field in a follow-up patch; until
// then we look for a typed accessor and fall back to nil.
func rootExcludePatterns(a manifest.Autoload) []string {
	// manifest.Autoload.ExcludeFromClassmap is added in a tiny follow-up to
	// Stage-1 Plan 1; until then, return nil so existing manifests keep
	// working unchanged.
	type hasExclude interface {
		excludeFromClassmap() []string
	}
	if h, ok := any(a).(hasExclude); ok {
		return h.excludeFromClassmap()
	}
	return nil
}

func scanInto(out map[string]string, projectDir, installPath, raw string, excl *excludeMatcher) error {
	relBase := raw
	if installPath != "" {
		relBase = filepath.ToSlash(filepath.Join(installPath, raw))
	}
	abs := filepath.Join(projectDir, filepath.FromSlash(relBase))

	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("classmap %q: %w", raw, err)
	}
	if !info.IsDir() {
		return scanFileInto(out, projectDir, abs, excl)
	}
	return filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if ext != ".php" && ext != ".inc" {
			return nil
		}
		return scanFileInto(out, projectDir, p, excl)
	})
}

func scanFileInto(out map[string]string, projectDir, abs string, excl *excludeMatcher) error {
	rel, err := filepath.Rel(projectDir, abs)
	if err != nil {
		return err
	}
	rel = filepath.ToSlash(rel)
	if excl.Match(rel) {
		return nil
	}
	src, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	classes, err := scanClasses(src)
	if err != nil {
		return fmt.Errorf("scan %s: %w", rel, err)
	}
	for _, c := range classes {
		if _, exists := out[c]; exists {
			continue // first-wins
		}
		out[c] = rel
	}
	return nil
}

// SortedClassmapKeys returns the keys of m sorted lexicographically. Used
// by templates so the emitted file is byte-stable.
func SortedClassmapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
