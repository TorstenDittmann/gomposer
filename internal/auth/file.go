// Package auth parses Composer-compatible auth.json files and exposes
// hostname-keyed credential lookups for HTTP and git operations.
//
// The on-disk schema mirrors Composer's exactly so existing files Just
// Work. Two locations are read on startup; see Store.Load for precedence.
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// file mirrors Composer's auth.json schema.
type file struct {
	HTTPBasic   map[string]basicCred  `json:"http-basic"`
	Bearer      map[string]string     `json:"bearer"`
	GitHubOAuth map[string]string     `json:"github-oauth"`
	GitLabToken map[string]gitLabCred `json:"gitlab-token"`
	GitLabOAuth map[string]string     `json:"gitlab-oauth"`
}

type basicCred struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type gitLabCred struct {
	Username string `json:"username"`
	Token    string `json:"token"`
}

// parseFile reads and decodes path. Missing file is an error here; callers
// who want optional behavior should use loadOptional.
func parseFile(path string) (*file, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("auth: parse %s: %w", path, err)
	}
	return &f, nil
}

// loadOptional returns parseFile(path) on hit, a zero-valued *file on miss.
// Any error other than ErrNotExist is propagated.
func loadOptional(path string) (*file, error) {
	f, err := parseFile(path)
	if err == nil {
		return f, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return &file{}, nil
	}
	return nil, err
}
