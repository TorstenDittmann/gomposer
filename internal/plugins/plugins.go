// Package plugins detects Composer plugin packages in a resolved lockfile and
// produces human-readable warnings.
//
// gomposer intentionally does not run plugins (see
// docs/superpowers/specs/2026-05-07-gomposer-design.md, section
// "Non-goals" and "Plugin detection"). Plugin packages still install into
// vendor/ — they just never execute. This package draws the user's attention
// to that asymmetry so install-time surprises do not become run-time
// mysteries.
//
// Recognized plugin types:
//
//   - "composer-plugin"     — generic Composer plugin, hooks into events
//   - "composer-installer"  — custom installer (e.g., composer/installers)
//
// Both are warned about. The composer-installer warning specifically calls
// out that custom install paths will not take effect; packages will land in
// vendor/<vendor>/<name>/ regardless.
//
// Suppression: a project that knowingly accepts the limitation can set
//
//	"extra": { "gomposer": { "suppress-plugin-warnings": true } }
//
// in composer.json. Only the literal boolean true suppresses; any other
// value (including the string "true") is treated as not-set.
package plugins

import (
	"fmt"
	"io"

	"github.com/torstendittmann/gomposer/internal/lock"
	"github.com/torstendittmann/gomposer/internal/manifest"
)

// Warning describes a single plugin package the user should know about.
type Warning struct {
	Name    string
	Version string
	Type    string
	Message string
}

// pluginTypes is the set of package "type" values we treat as plugins.
// Keeping it as a small map (rather than an if-chain) makes future additions
// trivial and the test surface obvious.
var pluginTypes = map[string]bool{
	"composer-plugin":    true,
	"composer-installer": true,
}

// Inspect walks the resolved package set and returns one Warning per plugin
// package. Returns nil if the user has set extra.gomposer.suppress-plugin-warnings=true,
// or if no plugins are present.
//
// Both prod and dev packages are inspected: a dev-only plugin (e.g.,
// phpstan/extension-installer) misbehaves the same way as a prod plugin.
func Inspect(f *lock.File, m *manifest.Manifest) []Warning {
	if f == nil {
		return nil
	}
	if isSuppressed(m) {
		return nil
	}
	var out []Warning
	for _, p := range f.Packages {
		if w, ok := warningFor(p); ok {
			out = append(out, w)
		}
	}
	for _, p := range f.PackagesDev {
		if w, ok := warningFor(p); ok {
			out = append(out, w)
		}
	}
	return out
}

func warningFor(p lock.Package) (Warning, bool) {
	if !pluginTypes[p.Type] {
		return Warning{}, false
	}
	return Warning{
		Name:    p.Name,
		Version: p.Version,
		Type:    p.Type,
		Message: messageFor(p),
	}, true
}

func messageFor(p lock.Package) string {
	// Special-case the canonical composer/installers package: it is by far
	// the most common installer plugin and the failure mode (custom paths
	// silently ignored) hits WordPress, Drupal, TYPO3, and Magento projects
	// constantly. Be concrete about what won't work.
	if p.Name == "composer/installers" {
		return "composer/installers normally rewrites custom install paths " +
			"(e.g., wp-content/plugins/<name>, modules/contrib/<name>); " +
			"gomposer does not run installer plugins, so packages " +
			"that depend on it will land in vendor/<vendor>/<name>/ regardless."
	}
	switch p.Type {
	case "composer-installer":
		return "this is a custom installer plugin; gomposer does not run it, " +
			"so any custom install paths it would normally configure will not " +
			"take effect — packages will land in vendor/<vendor>/<name>/."
	default: // "composer-plugin"
		return "this plugin would normally hook into install/update events; " +
			"gomposer does not run plugins, so any package-installer " +
			"behavior you depend on (custom install paths, patching, " +
			"autoload tweaks, etc.) will not happen."
	}
}

// isSuppressed returns true iff the manifest sets
// extra.gomposer.suppress-plugin-warnings to the literal boolean true.
//
// We deliberately do NOT accept the string "true" or other truthy values:
// composer.json is JSON, the boolean type is unambiguous, and fuzzy matching
// invites accidental suppression.
func isSuppressed(m *manifest.Manifest) bool {
	if m == nil || m.Extra == nil {
		return false
	}
	cg, ok := m.Extra["gomposer"].(map[string]any)
	if !ok {
		return false
	}
	v, ok := cg["suppress-plugin-warnings"].(bool)
	return ok && v
}

// Render writes warnings to w in a stable, grep-friendly one-line-per-warning
// format. No-op when warnings is empty so callers can call Render
// unconditionally.
func Render(w io.Writer, warnings []Warning) {
	for _, x := range warnings {
		fmt.Fprintf(w, "warning: gomposer does not run plugins: %s@%s (type=%s) — %s\n",
			x.Name, x.Version, x.Type, x.Message)
	}
}
