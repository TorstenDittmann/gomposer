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
	Name string `json:"name"`
	Type string `json:"type"`
}

// Parse decodes a composer.json byte slice. The error message includes the
// offset on JSON syntax errors so callers can surface useful diagnostics.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	return &m, nil
}
