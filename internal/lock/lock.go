// Package lock handles gomposer.lock read and write.
//
// The on-disk format is documented in
// docs/superpowers/specs/2026-05-07-gomposer-design.md (section "Lockfile
// format"). Field renames here MUST be reflected in the spec.
package lock

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// SchemaVersion is the on-disk format version this build understands.
// Decode rejects files with a different SchemaVersion to force a clean
// rebuild rather than guessing at compatibility.
const SchemaVersion = 1

type File struct {
	SchemaVersion       int       `json:"schemaVersion"`
	Generator           Generator `json:"generator"`
	ManifestContentHash string    `json:"manifestContentHash"`
	PlatformFingerprint string    `json:"platformFingerprint"`
	Stability           Stability `json:"stability"`
	Packages            []Package `json:"packages"`
	PackagesDev         []Package `json:"packagesDev,omitempty"`
	Aliases             []Alias   `json:"aliases,omitempty"`
	// Warnings, if non-empty, are human-readable strings the orchestrator
	// should print to stderr after a cache hit. They mirror what would have
	// been printed during a fresh resolution and exist so cache-hit runs
	// produce identical UX.
	//
	// We store them in the lockfile (NOT only in the resolution-result
	// cache) because the JSON lockfile is the canonical source of truth a
	// user can inspect, and a future `gomposer check` should be able to
	// re-print them without re-resolving.
	Warnings []string `json:"warnings,omitempty"`
}

type Generator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Stability struct {
	MinimumStability string `json:"minimumStability"`
	PreferStable     bool   `json:"preferStable"`
}

type Package struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// Type is the composer "type" value ("library", "composer-plugin",
	// "composer-installer", etc.). The orchestrator uses it to detect plugins
	// and emit a warning; it is otherwise informational.
	Type     string            `json:"type,omitempty"`
	Source   Source            `json:"source"`
	Dist     Dist              `json:"dist"`
	Require  map[string]string `json:"require,omitempty"`
	Autoload map[string]any    `json:"autoload,omitempty"`
	Suggest  map[string]string `json:"suggest,omitempty"`
}

type Source struct {
	Type string `json:"type"`
	URL  string `json:"url"`
	Ref  string `json:"ref"`
}

type Dist struct {
	Type   string `json:"type"`
	URL    string `json:"url"`
	Sha256 string `json:"sha256"`
}

type Alias struct {
	Package string `json:"package"`
	Version string `json:"version"`
	Alias   string `json:"alias"`
}

// Encode serializes the lockfile deterministically: 2-space indent, sorted
// map keys (Go's encoding/json sorts maps by default), trailing newline.
func (f *File) Encode() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(f); err != nil {
		return nil, fmt.Errorf("lock: encode: %w", err)
	}
	return buf.Bytes(), nil
}

// Decode parses a lockfile and rejects unknown schema versions.
func Decode(data []byte) (*File, error) {
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("lock: decode: %w", err)
	}
	if f.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("lock: unsupported schemaVersion %d (this build supports %d) — delete gomposer.lock to rebuild", f.SchemaVersion, SchemaVersion)
	}
	return &f, nil
}
