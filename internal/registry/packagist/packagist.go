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

	"github.com/torstendittmann/gomposer/internal/auth"
	"github.com/torstendittmann/gomposer/internal/cache/httpcache"
	"github.com/torstendittmann/gomposer/internal/cache/parsedcache"
	"github.com/torstendittmann/gomposer/internal/registry"
)

const defaultBaseURL = "https://repo.packagist.org"

type Config struct {
	BaseURL    string       // override for testing; default is repo.packagist.org
	CacheDir   string       // root cache dir; subdirs are created automatically
	HTTPClient *http.Client // default: http.DefaultClient
	Auth       *auth.Store  // optional; if nil, requests are unauthenticated
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
	parsedDir := filepath.Join(cfg.CacheDir, "parsed-v2")
	hc, err := httpcache.New(httpDir, cfg.HTTPClient)
	if err != nil {
		return nil, err
	}
	if cfg.Auth != nil {
		// Note: KindGitLabToken uses Private-Token, not Authorization. That
		// header cannot be expressed through CredentialsFunc's single-string
		// interface; such hosts use Apply() in richer per-request hooks. For
		// stage-2, gitlab-token auth for HTTP API calls is deferred.
		hc.Credentials = httpcache.CredentialsFunc(func(host string) (string, bool) {
			c, ok := cfg.Auth.Lookup(host)
			if !ok {
				return "", false
			}
			return c.AuthorizationHeader(), true
		})
	}
	pc, err := parsedcache.New[registry.PackageMetadata](parsedDir)
	if err != nil {
		return nil, err
	}
	return &Client{baseURL: cfg.BaseURL, http: hc, parsed: pc}, nil
}

// Lookup implements registry.SourceLookup. Packagist v2 splits a package's
// metadata across two files: tagged releases in /p2/<name>.json and dev
// branches in /p2/<name>~dev.json. We fetch both and merge — most packages
// only have the first, so the ~dev variant is allowed to 404 silently.
func (c *Client) Lookup(ctx context.Context, name string) (*registry.PackageMetadata, error) {
	stableBody, err := c.http.Get(ctx, c.baseURL+"/p2/"+name+".json")
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("%s: %w", name, registry.ErrPackageNotFound)
		}
		return nil, err
	}
	devBody, devErr := c.http.Get(ctx, c.baseURL+"/p2/"+name+"~dev.json")
	if devErr != nil && !isNotFound(devErr) {
		return nil, devErr
	}

	// Composite cache key: the parsed cache is keyed by hash of all source
	// bytes, so concatenate stable + dev so a change in either invalidates.
	composite := append(append([]byte(nil), stableBody...), devBody...)
	if v, ok, _ := c.parsed.Load(composite); ok {
		out := v
		return &out, nil
	}

	md, err := decodeV2(name, stableBody)
	if err != nil {
		return nil, err
	}
	if len(devBody) > 0 {
		devMd, err := decodeV2(name, devBody)
		if err == nil {
			md.Versions = append(md.Versions, devMd.Versions...)
		}
		// A decode error on the ~dev file is non-fatal; we still serve the
		// stable versions. Errors here typically mean the file is empty or
		// missing the package key.
	}
	if err := c.parsed.Store(composite, *md); err != nil {
		_ = err // cache failures are non-fatal
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
	Time              string       `json:"time"`
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

func (s *v2Source) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || data[0] != '{' {
		*s = v2Source{}
		return nil
	}
	type raw v2Source
	var tmp raw
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	*s = v2Source(tmp)
	return nil
}

type v2Dist struct {
	Type   string `json:"type"`
	URL    string `json:"url"`
	Shasum string `json:"shasum"`
}

func (d *v2Dist) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || data[0] != '{' {
		*d = v2Dist{}
		return nil
	}
	type raw v2Dist
	var tmp raw
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	*d = v2Dist(tmp)
	return nil
}

type v2Autoload struct {
	PSR4                map[string]any `json:"psr-4"`
	PSR0                map[string]any `json:"psr-0"`
	Files               []string       `json:"files"`
	Classmap            []string       `json:"classmap"`
	ExcludeFromClassmap []string       `json:"exclude-from-classmap"`
}

// UnmarshalJSON tolerates the "__unset" sentinel and any other non-object
// value by treating it as an empty Autoload section.
func (a *v2Autoload) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || data[0] != '{' {
		*a = v2Autoload{}
		return nil
	}
	type raw v2Autoload
	var tmp raw
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	*a = v2Autoload(tmp)
	return nil
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
		// Drop versions with no install method. Packagist occasionally
		// publishes dev-branch entries with both `dist` and `source` set
		// to JSON null (observed for symfony/http-client 8.1.x-dev); the
		// fetcher has no way to install them and the resolver must not
		// pick them.
		if v.Dist.URL == "" && v.Source.URL == "" {
			continue
		}
		out.Versions = append(out.Versions, registry.PackageVersion{
			Name:        v.Name,
			Version:     v.Version,
			VersionNorm: v.VersionNormalized,
			Time:        v.Time,
			Type:        v.Type,
			Source:      registry.Source{Type: v.Source.Type, URL: v.Source.URL, Ref: v.Source.Reference},
			Dist:        registry.Dist{Type: v.Dist.Type, URL: v.Dist.URL, Sha: v.Dist.Shasum},
			Require:     map[string]string(v.Require),
			RequireDev:  map[string]string(v.RequireDev),
			Autoload:    registry.Autoload(v.Autoload),
			AutoloadDev: registry.Autoload(v.AutoloadDev),
			Suggest:     map[string]string(v.Suggest),
			SourceKind:  "packagist",
		})
	}
	return out, nil
}
