package autoload

import (
	"log/slog"
	"path"
	"sort"
	"strings"

	"github.com/torstendittmann/composer-go/internal/manifest"
	"github.com/torstendittmann/composer-go/internal/registry"
)

// Entry is one resolved package as the orchestrator hands it to the
// autoloader generator. InstallPath is relative to the project root and
// uses forward slashes regardless of host OS (we never embed host-OS path
// separators in generated PHP).
type Entry struct {
	Name        string
	Version     string
	InstallPath string // e.g. "vendor/acme/foo"
	Autoload    registry.Autoload
	// ExcludeFromClassmap holds the package's autoload.exclude-from-classmap
	// patterns. Each is a glob in Composer's dialect (`**/Tests/`,
	// `**/*Test.php`); see exclude.go for the full grammar. Empty for
	// packages that don't declare one.
	ExcludeFromClassmap []string
}

// CollectPSR4 merges PSR-4 prefixes from the root manifest's autoload
// section and every vendor entry. Returned values:
//   - keys are PSR-4 namespace prefixes (e.g. "Acme\\Foo\\")
//   - values are project-relative directory paths with a trailing slash
//
// Order within each value slice preserves first-seen order: root entries
// before vendor entries, vendor entries in the supplied order. Composer
// does the same so that root paths win for class lookup ties.
//
// projectDir is currently unused (paths are project-relative) but is kept
// in the signature for symmetry with future absolute-path generators.
func CollectPSR4(projectDir string, root manifest.Autoload, entries []Entry) map[string][]string {
	out := map[string][]string{}

	// Root manifest first.
	for prefix, dir := range root.PSR4 {
		out[prefix] = appendUnique(out[prefix], normalizeDir(dir))
	}

	// Vendor entries in supplied order.
	for _, e := range entries {
		for prefix, raw := range e.Autoload.PSR4 {
			for _, dir := range toStringSlice(raw) {
				rel := joinSlash(e.InstallPath, dir)
				out[prefix] = appendUnique(out[prefix], normalizeDir(rel))
			}
		}
	}
	return out
}

// SortedPrefixes returns the keys of m sorted lexicographically.
// Used everywhere we serialize PSR-4 maps so output is byte-stable.
func SortedPrefixes(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// toStringSlice normalizes the polymorphic JSON value (string OR []string)
// that Composer accepts in psr-4 entries.
func toStringSlice(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	}
	return nil
}

// normalizeDir ensures the directory ends with exactly one "/".
// Empty string is preserved as empty (means "package install root").
func normalizeDir(d string) string {
	if d == "" {
		return ""
	}
	d = strings.TrimRight(d, "/")
	return d + "/"
}

// joinSlash joins forward-slash paths without ever introducing a host
// path separator.
func joinSlash(a, b string) string {
	if b == "" {
		return strings.TrimRight(a, "/") + "/"
	}
	return path.Join(a, b)
}

func appendUnique(s []string, x string) []string {
	for _, e := range s {
		if e == x {
			return s
		}
	}
	return append(s, x)
}

// WarnPSR0 logs one warning per package (and one for the root manifest)
// that declares non-empty autoload.psr-0. Stage 2 explicitly does not
// implement PSR-0; calling code is already aware of this from the spec,
// but a runtime warning helps surface unsupported packages during real
// installs.
//
// The warnings are emitted via slog at level Warn so they are visible
// without --verbose. Tests can capture them via slog.SetDefault(...).
func WarnPSR0(root manifest.Autoload, entries []Entry) {
	if len(root.PSR0) > 0 {
		slog.Warn("autoload: PSR-0 not supported, skipping",
			"package", "<root>", "namespaces", keysOf(root.PSR0))
	}
	for _, e := range entries {
		if len(e.Autoload.PSR0) > 0 {
			slog.Warn("autoload: PSR-0 not supported, skipping",
				"package", e.Name, "namespaces", anyKeysOf(e.Autoload.PSR0))
		}
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func anyKeysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
