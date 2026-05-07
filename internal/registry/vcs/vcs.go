// Package vcs implements registry.SourceLookup against a single git URL.
//
// One Client wraps one repository. Calls to Lookup enumerate the repo's
// tags and tracked branches, parse each ref's composer.json, and return one
// PackageVersion per ref:
//
//   - tags become normalized versions (leading "v" tolerated by the
//     constraint parser), e.g. "refs/tags/v1.2.3" -> "1.2.3";
//   - branches become "dev-<branch>" rows;
//   - extra.branch-alias entries (e.g. {"dev-main":"1.x-dev"}) produce
//     additional synthesized rows so that range constraints like "^1.0"
//     can match a development branch.
//
// The package shells out to git rather than embedding go-git: it keeps the
// binary small, reuses the user's existing SSH and credential helper
// configuration, and avoids reimplementing git's wire protocol. Auth is out
// of scope for this package; plan 4 layers auth.json handling on top.
//
// Caching:
//   - the bare mirror lives at <CacheRoot>/mirrors/<sha256(url)>.git;
//   - per-(url, sha) refManifest values are persisted via parsedcache so
//     warm runs skip `git show` entirely;
//   - `git fetch` is rate-limited by Config.FetchTTL (default 60s) so
//     back-to-back lookups in one process do not refetch.
package vcs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/torstendittmann/composer-go/internal/cache/parsedcache"
	"github.com/torstendittmann/composer-go/internal/manifest"
	"github.com/torstendittmann/composer-go/internal/registry"
)

// Config configures a single VCS-backed lookup. One Config corresponds to
// one repositories[] entry in composer.json.
type Config struct {
	URL       string        // git clone URL (https or ssh)
	CacheRoot string        // root directory for the bare mirror; required
	FetchTTL  time.Duration // minimum interval between `git fetch` calls; 0 -> default 60s
	Git       Git           // git wrapper (allow overriding the binary in tests)
}

// Client is a registry.SourceLookup over one git URL.
type Client struct {
	cfg       Config
	mirrorDir string
	parsed    *parsedcache.Cache[refManifest]

	mu        sync.Mutex
	lastFetch time.Time
	cachedPkg string
	headBr    string
	versions  *registry.PackageMetadata // memoised lookup result
}

// refManifest is the parsedcache value: the decoded composer.json + the
// bytes hash so the resolver does not re-`git show` on a warm run.
type refManifest struct {
	Name        string
	Type        string
	Require     map[string]string
	RequireDev  map[string]string
	Autoload    registry.Autoload
	AutoloadDev registry.Autoload
	Suggest     map[string]string
	BranchAlias map[string]string // dev-foo -> 1.x-dev
}

// Options is shared configuration for a batch of VCS clients.
type Options struct {
	CacheRoot string
	FetchTTL  time.Duration
	Git       Git
}

// New creates a Client. CacheRoot is created lazily.
func New(cfg Config) (*Client, error) {
	if cfg.URL == "" {
		return nil, errors.New("vcs: URL is required")
	}
	if cfg.CacheRoot == "" {
		return nil, errors.New("vcs: CacheRoot is required")
	}
	if cfg.FetchTTL == 0 {
		cfg.FetchTTL = 60 * time.Second
	}
	mirror := filepath.Join(cfg.CacheRoot, "mirrors", urlKey(cfg.URL)+".git")
	parsedDir := filepath.Join(cfg.CacheRoot, "parsed")
	pc, err := parsedcache.New[refManifest](parsedDir)
	if err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, mirrorDir: mirror, parsed: pc}, nil
}

// NewFromManifest builds one Client per supported repository entry. It
// returns an error for unsupported types so misconfigurations surface early.
func NewFromManifest(repos []manifest.Repository, opts Options) ([]*Client, error) {
	var out []*Client
	for _, r := range repos {
		if err := r.Validate(); err != nil {
			return nil, err
		}
		if !r.IsGit() {
			continue
		}
		c, err := New(Config{
			URL:       r.URL,
			CacheRoot: opts.CacheRoot,
			FetchTTL:  opts.FetchTTL,
			Git:       opts.Git,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func urlKey(url string) string {
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:])
}

// ensureMirror clones if missing, otherwise refreshes if outside the TTL.
func (c *Client) ensureMirror(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := os.Stat(c.mirrorDir); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := c.cfg.Git.CloneMirror(ctx, c.cfg.URL, c.mirrorDir); err != nil {
			return err
		}
		c.lastFetch = time.Now()
		return nil
	}
	if time.Since(c.lastFetch) < c.cfg.FetchTTL {
		return nil
	}
	if err := c.cfg.Git.Fetch(ctx, c.mirrorDir); err != nil {
		return err
	}
	c.lastFetch = time.Now()
	return nil
}

// PackageName returns the `name` field from composer.json on the default
// branch. The orchestrator uses this to register name -> client mappings.
func (c *Client) PackageName(ctx context.Context) (string, error) {
	if c.cachedPkg != "" {
		return c.cachedPkg, nil
	}
	if err := c.ensureMirror(ctx); err != nil {
		return "", err
	}
	head, err := c.cfg.Git.HeadBranch(ctx, c.mirrorDir)
	if err != nil {
		return "", err
	}
	body, err := c.cfg.Git.Show(ctx, c.mirrorDir, head, "composer.json")
	if err != nil {
		return "", err
	}
	if len(body) == 0 {
		return "", fmt.Errorf("vcs: %s default branch has no composer.json", c.cfg.URL)
	}
	var m struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return "", fmt.Errorf("vcs: %s: invalid composer.json on %s: %w", c.cfg.URL, head, err)
	}
	if m.Name == "" {
		return "", fmt.Errorf("vcs: %s composer.json has no `name`", c.cfg.URL)
	}
	c.cachedPkg = m.Name
	c.headBr = head
	return c.cachedPkg, nil
}

// Lookup implements registry.SourceLookup. The first call enumerates refs
// and parses each ref's composer.json; subsequent calls in the same process
// return the memoised result.
func (c *Client) Lookup(ctx context.Context, name string) (*registry.PackageMetadata, error) {
	pkgName, err := c.PackageName(ctx)
	if err != nil {
		return nil, err
	}
	if pkgName != name {
		return nil, fmt.Errorf("%s: %w", name, registry.ErrPackageNotFound)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.versions != nil {
		return c.versions, nil
	}
	refs, err := c.cfg.Git.LsRemote(ctx, c.mirrorDir)
	if err != nil {
		return nil, err
	}
	out := &registry.PackageMetadata{Name: name, Versions: make([]registry.PackageVersion, 0, len(refs))}
	for _, r := range refs {
		ver, ok := refToVersion(r.Name)
		if !ok {
			continue
		}
		rm, err := c.refManifest(ctx, r.Name, r.SHA)
		if err != nil {
			// A ref without a composer.json or with malformed JSON is
			// silently skipped — this matches Composer's tolerance.
			continue
		}
		if rm.Name != "" && rm.Name != name {
			// The branch/tag claims to be a different package; skip.
			continue
		}
		base := registry.PackageVersion{
			Name:        name,
			Version:     ver,
			VersionNorm: ver,
			Type:        rm.Type,
			Source:      registry.Source{Type: "git", URL: c.cfg.URL, Ref: r.SHA},
			Dist:        registry.Dist{},
			Require:     rm.Require,
			RequireDev:  rm.RequireDev,
			Autoload:    rm.Autoload,
			AutoloadDev: rm.AutoloadDev,
			Suggest:     rm.Suggest,
		}
		out.Versions = append(out.Versions, base)
		// Branch aliases — synthesize aliased rows for each "dev-<branch> as
		// X" pair. See alias.go for the expansion rules.
		for _, alias := range expandAliases(ver, rm.BranchAlias) {
			aliased := base
			aliased.Version = alias
			aliased.VersionNorm = alias
			out.Versions = append(out.Versions, aliased)
		}
	}
	c.versions = out
	return out, nil
}

// refManifest reads composer.json for one ref, with a parsedcache layer
// keyed by url+sha so warm runs skip the `git show` round-trip.
func (c *Client) refManifest(ctx context.Context, refName, sha string) (refManifest, error) {
	cacheKey := []byte(c.cfg.URL + "\x00" + sha)
	if v, ok, _ := c.parsed.Load(cacheKey); ok {
		return v, nil
	}
	body, err := c.cfg.Git.Show(ctx, c.mirrorDir, refName, "composer.json")
	if err != nil {
		return refManifest{}, err
	}
	if len(body) == 0 {
		return refManifest{}, fmt.Errorf("vcs: %s@%s: no composer.json", c.cfg.URL, refName)
	}
	rm, err := decodeRefManifest(body)
	if err != nil {
		return refManifest{}, err
	}
	_ = c.parsed.Store(cacheKey, rm)
	return rm, nil
}

func decodeRefManifest(body []byte) (refManifest, error) {
	var raw struct {
		Name        string            `json:"name"`
		Type        string            `json:"type"`
		Require     map[string]string `json:"require"`
		RequireDev  map[string]string `json:"require-dev"`
		Autoload    registry.Autoload `json:"autoload"`
		AutoloadDev registry.Autoload `json:"autoload-dev"`
		Suggest     map[string]string `json:"suggest"`
		Extra       struct {
			BranchAlias map[string]string `json:"branch-alias"`
		} `json:"extra"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return refManifest{}, err
	}
	return refManifest{
		Name:        raw.Name,
		Type:        raw.Type,
		Require:     raw.Require,
		RequireDev:  raw.RequireDev,
		Autoload:    raw.Autoload,
		AutoloadDev: raw.AutoloadDev,
		Suggest:     raw.Suggest,
		BranchAlias: raw.Extra.BranchAlias,
	}, nil
}

// refToVersion maps a full ref name to a published version string.
//
//	refs/tags/v1.2.3      -> "1.2.3"   (leading v stripped; ParseVersion is tolerant)
//	refs/tags/1.2.3       -> "1.2.3"
//	refs/heads/main       -> "dev-main"
//
// Returns ok=false for refs we should skip (HEAD, notes, pull requests, etc).
func refToVersion(ref string) (string, bool) {
	const tagPrefix = "refs/tags/"
	const headPrefix = "refs/heads/"
	switch {
	case len(ref) > len(tagPrefix) && ref[:len(tagPrefix)] == tagPrefix:
		t := ref[len(tagPrefix):]
		if len(t) > 0 && t[0] == 'v' {
			t = t[1:]
		}
		return t, true
	case len(ref) > len(headPrefix) && ref[:len(headPrefix)] == headPrefix:
		return "dev-" + ref[len(headPrefix):], true
	}
	return "", false
}
