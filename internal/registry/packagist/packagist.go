// Package packagist implements registry.SourceLookup against
// repo.packagist.org's v2 metadata API.
package packagist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/torstendittmann/composer-go/internal/cache/httpcache"
	"github.com/torstendittmann/composer-go/internal/cache/parsedcache"
	"github.com/torstendittmann/composer-go/internal/registry"
)

const defaultBaseURL = "https://repo.packagist.org"

type Config struct {
	BaseURL    string       // override for testing; default is repo.packagist.org
	CacheDir   string       // root cache dir; subdirs are created automatically
	HTTPClient *http.Client // default: http.DefaultClient
}

type Client struct {
	baseURL string
	http    *httpcache.Cache
	parsed  *parsedcache.Cache[registry.PackageMetadata]
}

func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.CacheDir == "" {
		return nil, errors.New("packagist: CacheDir is required")
	}
	httpDir := filepath.Join(cfg.CacheDir, "http")
	parsedDir := filepath.Join(cfg.CacheDir, "parsed")
	hc, err := httpcache.New(httpDir, cfg.HTTPClient)
	if err != nil {
		return nil, err
	}
	pc, err := parsedcache.New[registry.PackageMetadata](parsedDir)
	if err != nil {
		return nil, err
	}
	return &Client{baseURL: cfg.BaseURL, http: hc, parsed: pc}, nil
}

// Lookup implements registry.SourceLookup.
func (c *Client) Lookup(ctx context.Context, name string) (*registry.PackageMetadata, error) {
	url := c.baseURL + "/p2/" + name + ".json"
	body, err := c.http.Get(ctx, url)
	if err != nil {
		// httpcache returns an error containing the status code; map 404 -> ErrPackageNotFound.
		// We use a string match because httpcache reports "unexpected status N".
		if isNotFound(err) {
			return nil, fmt.Errorf("%s: %w", name, registry.ErrPackageNotFound)
		}
		return nil, err
	}

	if v, ok, _ := c.parsed.Load(body); ok {
		out := v
		return &out, nil
	}

	md, err := decodeV2(name, body)
	if err != nil {
		return nil, err
	}
	if err := c.parsed.Store(body, *md); err != nil {
		// Cache failures are non-fatal.
		_ = err
	}
	return md, nil
}

func isNotFound(err error) bool {
	// httpcache returns: "httpcache: unexpected status 404 for ..."
	return err != nil && (containsAll(err.Error(), "status 404") || containsAll(err.Error(), "status 410"))
}

func containsAll(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// --- v2 schema ---

type v2Response struct {
	Packages map[string][]v2Version `json:"packages"`
}

type v2Version struct {
	Name              string       `json:"name"`
	Version           string       `json:"version"`
	VersionNormalized string       `json:"version_normalized"`
	Type              string       `json:"type"`
	Source            v2Source     `json:"source"`
	Dist              v2Dist       `json:"dist"`
	Require           stringMap    `json:"require"`
	RequireDev        stringMap    `json:"require-dev"`
	Autoload          v2Autoload   `json:"autoload"`
	AutoloadDev       v2Autoload   `json:"autoload-dev"`
	Suggest           stringMap    `json:"suggest"`
}

// stringMap unmarshals either a JSON object of string→string or the
// Packagist v2 sentinel "__unset" (and other non-object values, defensively)
// as nil. Some published versions of long-lived packages ship `"require":
// "__unset"` to indicate "no requirements."
type stringMap map[string]string

func (m *stringMap) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || data[0] != '{' {
		*m = nil
		return nil
	}
	tmp := map[string]string{}
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	*m = tmp
	return nil
}

type v2Source struct {
	Type      string `json:"type"`
	URL       string `json:"url"`
	Reference string `json:"reference"`
}

type v2Dist struct {
	Type   string `json:"type"`
	URL    string `json:"url"`
	Shasum string `json:"shasum"`
}

type v2Autoload struct {
	PSR4     map[string]any `json:"psr-4"`
	PSR0     map[string]any `json:"psr-0"`
	Files    []string       `json:"files"`
	Classmap []string       `json:"classmap"`
}

func decodeV2(name string, body []byte) (*registry.PackageMetadata, error) {
	var resp v2Response
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("packagist: decode %s: %w", name, err)
	}
	versions, ok := resp.Packages[name]
	if !ok || len(versions) == 0 {
		return nil, fmt.Errorf("%s: %w", name, registry.ErrPackageNotFound)
	}
	out := &registry.PackageMetadata{Name: name, Versions: make([]registry.PackageVersion, 0, len(versions))}
	for _, v := range versions {
		out.Versions = append(out.Versions, registry.PackageVersion{
			Name:        v.Name,
			Version:     v.Version,
			VersionNorm: v.VersionNormalized,
			Type:        v.Type,
			Source:      registry.Source{Type: v.Source.Type, URL: v.Source.URL, Ref: v.Source.Reference},
			Dist:        registry.Dist{Type: v.Dist.Type, URL: v.Dist.URL, Sha: v.Dist.Shasum},
			Require:     map[string]string(v.Require),
			RequireDev:  map[string]string(v.RequireDev),
			Autoload:    registry.Autoload(v.Autoload),
			AutoloadDev: registry.Autoload(v.AutoloadDev),
			Suggest:     map[string]string(v.Suggest),
		})
	}
	return out, nil
}
