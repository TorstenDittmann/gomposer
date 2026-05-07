// Package manifest parses composer.json files into a structured form.
// Parsing is pure: no network, no filesystem side effects.
package manifest

import (
	"encoding/json"
	"fmt"
)

// Manifest is the parsed view of a composer.json file. Fields not yet
// supported by composer-go are omitted; unknown fields in the input are
// ignored silently for forward-compatibility with future Composer features.
type Manifest struct {
	Name             string            `json:"name"`
	Type             string            `json:"type"`
	Require          map[string]string `json:"require,omitempty"`
	RequireDev       map[string]string `json:"require-dev,omitempty"`
	Autoload         Autoload          `json:"autoload,omitempty"`
	AutoloadDev      Autoload          `json:"autoload-dev,omitempty"`
	MinimumStability string            `json:"minimum-stability,omitempty"`
	PreferStable     bool              `json:"prefer-stable,omitempty"`
	// Repositories holds user-defined repository entries, in declaration
	// order. Only the JSON array form is accepted; the legacy map form is a
	// hard error (CG203). Validation of individual entries (supported types,
	// required fields) is performed by Repository.Validate, called by the
	// orchestrator at startup so misconfigurations surface before any I/O.
	Repositories []Repository `json:"-"`

	// Scripts maps event names ("post-install-cmd", etc.) to one or more
	// script bodies that fire sequentially. Composer's wire format accepts
	// either a single string or an array of strings per event; the custom
	// decoder below normalizes both into []string.
	Scripts map[string][]string `json:"-"`
}

// Repository is one entry from composer.json `repositories`. Fields beyond
// Type/URL are kept on the wire as a raw map so future stages can read them
// (Auth, Excludes, Only, etc.) without revising this struct.
type Repository struct {
	Type string         `json:"type"`
	URL  string         `json:"url"`
	Raw  map[string]any `json:"-"`
}

// rawManifest mirrors Manifest but with Repositories and Scripts as
// json.RawMessage so we can disambiguate special forms at decode time.
type rawManifest struct {
	Manifest
	Repositories json.RawMessage            `json:"repositories,omitempty"`
	Scripts      map[string]json.RawMessage `json:"scripts,omitempty"`
}

// Parse decodes a composer.json byte slice. The error message includes the
// offset on JSON syntax errors so callers can surface useful diagnostics.
func Parse(data []byte) (*Manifest, error) {
	var raw rawManifest
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	m := raw.Manifest
	if len(raw.Repositories) > 0 {
		repos, err := parseRepositories(raw.Repositories)
		if err != nil {
			return nil, err
		}
		m.Repositories = repos
	}
	if len(raw.Scripts) > 0 {
		scripts, err := decodeScripts(raw.Scripts)
		if err != nil {
			return nil, fmt.Errorf("manifest: scripts: %w", err)
		}
		m.Scripts = scripts
	}
	return &m, nil
}

// decodeScripts normalizes the per-event JSON body into []string. Accepts:
//   - a single JSON string  -> []string{value}
//   - a JSON array of strings -> the array
//
// Any other shape returns an error.
func decodeScripts(raw map[string]json.RawMessage) (map[string][]string, error) {
	out := make(map[string][]string, len(raw))
	for event, body := range raw {
		// Try string first.
		var s string
		if err := json.Unmarshal(body, &s); err == nil {
			out[event] = []string{s}
			continue
		}
		// Then array of strings.
		var arr []string
		if err := json.Unmarshal(body, &arr); err == nil {
			out[event] = arr
			continue
		}
		return nil, fmt.Errorf("event %q: must be a string or array of strings", event)
	}
	return out, nil
}

func parseRepositories(data []byte) ([]Repository, error) {
	// trim leading whitespace
	for i := 0; i < len(data); i++ {
		switch data[i] {
		case ' ', '\t', '\n', '\r':
			continue
		case '[':
			return parseRepositoriesArray(data)
		case '{':
			return nil, fmt.Errorf("manifest: legacy map form of `repositories` is not supported; use the array form (composer-go CG203)")
		case 'f': // false / disable-defaults convention
			return nil, nil
		}
		break
	}
	return nil, fmt.Errorf("manifest: invalid `repositories` value")
}

func parseRepositoriesArray(data []byte) ([]Repository, error) {
	var entries []map[string]any
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("manifest: repositories: %w", err)
	}
	out := make([]Repository, 0, len(entries))
	for _, e := range entries {
		typ, _ := e["type"].(string)
		url, _ := e["url"].(string)
		out = append(out, Repository{Type: typ, URL: url, Raw: e})
	}
	return out, nil
}
