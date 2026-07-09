// Package lock handles composer.lock read and write.
//
// The on-disk shape mirrors Composer's own composer.lock so upstream
// Composer can consume what gomposer emits. See
// docs/superpowers/specs/2026-06-18-composer-lock-compat-design.md for
// the design rationale and field-level mapping.
package lock

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// File is the top-level composer.lock structure. JSON tags mirror
// Composer's Locker::save output verbatim.
type File struct {
	Readme            []string          `json:"_readme,omitempty"`
	ContentHash       string            `json:"content-hash"`
	Packages          []Package         `json:"packages"`
	PackagesDev       []Package         `json:"packages-dev"`
	Aliases           []Alias           `json:"aliases"`
	MinimumStability  string            `json:"minimum-stability"`
	StabilityFlags    map[string]int    `json:"stability-flags"`
	PreferStable      bool              `json:"prefer-stable"`
	PreferLowest      bool              `json:"prefer-lowest"`
	Platform          map[string]string `json:"platform"`
	PlatformDev       map[string]string `json:"platform-dev"`
	PlatformOverrides map[string]string `json:"platform-overrides,omitempty"`
	PluginAPIVersion  string            `json:"plugin-api-version,omitempty"`
}

// Package is one locked package. Field set matches the subset of
// Composer's per-package output that gomposer populates. Optional-in-
// Composer fields we don't emit (authors, license, description,
// keywords, homepage, funding, support) are omitted; Composer accepts
// their absence.
type Package struct {
	Name            string            `json:"name"`
	Version         string            `json:"version"`
	Type            string            `json:"type,omitempty"`
	Source          Source            `json:"source,omitempty"`
	Dist            Dist              `json:"dist,omitempty"`
	Require         map[string]string `json:"require,omitempty"`
	Autoload        map[string]any    `json:"autoload,omitempty"`
	NotificationURL string            `json:"notification-url,omitempty"`
	Time            string            `json:"time,omitempty"`
}

type Source struct {
	Type      string `json:"type"`
	URL       string `json:"url"`
	Reference string `json:"reference"`
}

type Dist struct {
	Type      string `json:"type"`
	URL       string `json:"url"`
	Reference string `json:"reference,omitempty"`
	Shasum    string `json:"shasum"`
}

type Alias struct {
	Package string `json:"package"`
	Version string `json:"version"`
	Alias   string `json:"alias"`
}

// Encode serializes deterministically. 4-space indent (matching Composer's
// JsonFile::encode under JSON_PRETTY_PRINT) + SetEscapeHTML(false) +
// trailing "\n". Map keys are sorted by encoding/json.
func (f *File) Encode() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "    ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(f); err != nil {
		return nil, fmt.Errorf("lock: encode: %w", err)
	}
	return buf.Bytes(), nil
}

// Decode parses a composer.lock. Unknown fields are ignored (Composer may
// add optional metadata we don't consume). Callers that need round-trip
// preservation should track that separately.
func Decode(data []byte) (*File, error) {
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("lock: decode: %w", err)
	}
	return &f, nil
}
