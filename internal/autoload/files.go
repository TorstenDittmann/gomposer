package autoload

import (
	"path"
	"sort"

	"github.com/torstendittmann/gomposer/internal/manifest"
)

// FileEntry is one entry in the emitted $files array. PackageName is the
// owning package's canonical name ("vendor/foo") or empty string for the
// root manifest. We keep it on the struct so emission order can be
// asserted in tests and rendered into a stable hash key.
type FileEntry struct {
	Path        string
	PackageName string
}

// CollectFiles returns the merged, ordered list of `files` entries to emit
// in vendor/composer/autoload_files.php. Order matches real Composer:
//  1. Vendor entries, sorted alphabetically by package name. Within a
//     package, listed order is preserved.
//  2. Root manifest entries, last, in listed order.
//
// Duplicates by output path are dropped (first occurrence wins).
func CollectFiles(root manifest.Autoload, entries []Entry) []FileEntry {
	// Stable copy so we don't mutate caller's slice, sorted by package name.
	sorted := make([]Entry, len(entries))
	copy(sorted, entries)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})

	out := make([]FileEntry, 0)
	seen := make(map[string]struct{})

	add := func(p, pkg string) {
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, FileEntry{Path: p, PackageName: pkg})
	}

	for _, e := range sorted {
		for _, f := range e.Autoload.Files {
			add(path.Join(e.InstallPath, f), e.Name)
		}
	}
	for _, f := range root.Files {
		add(f, "")
	}
	return out
}
