package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sync/errgroup"

	autoloadpkg "github.com/torstendittmann/composer-go/internal/autoload"
	"github.com/torstendittmann/composer-go/internal/cache"
	realfetcher "github.com/torstendittmann/composer-go/internal/fetcher"
	"github.com/torstendittmann/composer-go/internal/lock"
	"github.com/torstendittmann/composer-go/internal/manifest"
	"github.com/torstendittmann/composer-go/internal/platform"
	"github.com/torstendittmann/composer-go/internal/registry"
	"github.com/torstendittmann/composer-go/internal/registry/packagist"
	"github.com/torstendittmann/composer-go/internal/resolver"
	"github.com/torstendittmann/composer-go/internal/store"
)

// Fetcher downloads a single locked package and returns a content-store key.
// Implemented by an adapter over internal/fetcher (Plan 4).
type Fetcher interface {
	Fetch(ctx context.Context, pkg lock.Package) (string, error)
}

// Materializer populates a destination directory from a content-store key.
// Implemented by an adapter over internal/fetcher (Plan 4).
type Materializer interface {
	Materialize(ctx context.Context, key, dest string) error
}

// Autoloader generates vendor/autoload.php and the composer/ helper files.
// Implemented by internal/autoload (Plan 5).
type Autoloader interface {
	Generate(ctx context.Context, projectDir string, packages []lock.Package, m *manifest.Manifest) error
}

// pipelineState carries values across phases. Built once at the top of run().
type pipelineState struct {
	opts          Options
	manifest      *manifest.Manifest
	manifestBytes []byte
	lockBytes     []byte // existing lock contents, if any (nil means none)
	platform      *platform.Platform // structured, may be nil when ignore-all
	platformStr   string             // fingerprint string (cache key input)
	cacheKey      string
	ignoreSet     map[string]bool
}

func newPipelineState(opts Options, m *manifest.Manifest) (*pipelineState, error) {
	manifestBytes, err := os.ReadFile(filepath.Join(opts.ProjectDir, "composer.json"))
	if err != nil {
		return nil, fmt.Errorf("orchestrator: read manifest bytes: %w", err)
	}
	lockBytes, _ := os.ReadFile(filepath.Join(opts.ProjectDir, "composer-go.lock"))

	ignore := buildIgnoreSet(opts.IgnorePlatformReqs)

	var pf *platform.Platform
	if !ignore["*"] {
		pf, err = platform.Probe()
		if err != nil {
			return nil, fmt.Errorf("orchestrator: %w", err)
		}
	}
	pfStr := pf.Fingerprint()
	return &pipelineState{
		opts:          opts,
		manifest:      m,
		manifestBytes: manifestBytes,
		lockBytes:     lockBytes,
		platform:      pf,
		platformStr:   pfStr,
		cacheKey:      computeCacheKey(manifestBytes, lockBytes, pfStr),
		ignoreSet:     ignore,
	}, nil
}

func buildIgnoreSet(list []string) map[string]bool {
	out := make(map[string]bool, len(list))
	for _, n := range list {
		out[n] = true
	}
	return out
}

// resolveFunc is the resolver entry point, indirected for tests.
var resolveFunc = func(ctx context.Context, ps *pipelineState, src registry.SourceLookup, includeDev bool) (*resolver.Result, error) {
	return resolver.Solve(ctx, resolver.Input{
		Manifest:            ps.manifest,
		Source:              src,
		IncludeDev:          includeDev,
		Platform:            ps.platform,
		IgnorePlatformReqs:  ps.ignoreSet,
		PlatformFingerprint: ps.platformStr,
	})
}

// resolveOrCache returns a fully populated lock.File. It either:
//   - returns the existing lockfile if present and forceResolve is false (install path),
//   - returns the cached resolution if (manifest, lock, platform) matches,
//   - or runs the resolver and caches the result.
//
// forceResolve=true skips the existing lock and the cache (Update path).
func resolveOrCache(ctx context.Context, ps *pipelineState, forceResolve bool) (*lock.File, error) {
	// If a lockfile exists and we're not forcing re-resolution, use it directly.
	// This is the happy path for `install` when the lock is up to date.
	if !forceResolve && len(ps.lockBytes) > 0 {
		if existing, err := lock.Decode(ps.lockBytes); err == nil {
			return existing, nil
		}
		// Corrupt lockfile: fall through to resolve.
	}

	if !forceResolve {
		if cached, ok, err := loadResolution(ps.cacheKey); err == nil && ok {
			return cached, nil
		}
	}

	src := ps.opts.Source
	if src == nil {
		return nil, fmt.Errorf("orchestrator: no registry source configured")
	}

	res, err := resolveFunc(ctx, ps, src, !ps.opts.NoDev)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: resolve: %w", err)
	}

	f := buildLockFile(ps, res)
	// Best-effort cache write. Resolution proceeds even if the cache write fails.
	_ = storeResolution(ps.cacheKey, f)
	return f, nil
}

func buildLockFile(ps *pipelineState, res *resolver.Result) *lock.File {
	manifestHash := sha256.Sum256(ps.manifestBytes)
	prod, dev := resolver.ToLockPackages(res)
	return &lock.File{
		SchemaVersion:       lock.SchemaVersion,
		Generator:           lock.Generator{Name: "composer-go", Version: "0.1.0"},
		ManifestContentHash: "sha256:" + hex.EncodeToString(manifestHash[:]),
		PlatformFingerprint: ps.platformStr,
		Stability: lock.Stability{
			MinimumStability: ps.manifest.MinimumStability,
			PreferStable:     ps.manifest.PreferStable,
		},
		Packages:    prod,
		PackagesDev: dev,
	}
}

// resolveOnly is a test seam: run only the manifest + resolve phases.
func resolveOnly(ctx context.Context, opts Options) (*lock.File, error) {
	m, err := loadManifest(opts.ProjectDir)
	if err != nil {
		return nil, err
	}
	ps, err := newPipelineState(opts, m)
	if err != nil {
		return nil, err
	}
	return resolveOrCache(ctx, ps, true /* forceResolve, ignore cache in tests */)
}

// fetchAll downloads every package in pkgs concurrently with at most
// `workers` goroutines in flight. Returns map[name]storeKey.
func fetchAll(ctx context.Context, pkgs []lock.Package, f Fetcher, workers int) (map[string]string, error) {
	if workers < 1 {
		workers = 1
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)

	var mu sync.Mutex
	keys := make(map[string]string, len(pkgs))

	for i := range pkgs {
		p := pkgs[i] // copy for closure
		g.Go(func() error {
			key, err := f.Fetch(gctx, p)
			if err != nil {
				return fmt.Errorf("orchestrator: fetch %s: %w", p.Name, err)
			}
			mu.Lock()
			keys[p.Name] = key
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return keys, nil
}

func vendorPath(projectDir, packageName string) string {
	return filepath.Join(projectDir, "vendor", filepath.FromSlash(packageName))
}

// backfillSha sets pkg.Dist.Sha256 from keys[pkg.Name] when the dist sha is
// empty. Packagist v2 sometimes returns empty shasums for older entries; the
// fetcher computes the real sha during streaming download and that becomes
// the store key.
func backfillSha(pkgs []lock.Package, keys map[string]string) {
	for i := range pkgs {
		if pkgs[i].Dist.Sha256 == "" {
			if k, ok := keys[pkgs[i].Name]; ok {
				pkgs[i].Dist.Sha256 = k
			}
		}
	}
}

// materializeAll extracts each package from the store into vendor/.
func materializeAll(ctx context.Context, projectDir string, pkgs []lock.Package, keys map[string]string, m Materializer, workers int) error {
	if workers < 1 {
		workers = 1
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)
	for i := range pkgs {
		p := pkgs[i]
		key, ok := keys[p.Name]
		if !ok {
			return fmt.Errorf("orchestrator: missing store key for %s", p.Name)
		}
		dest := vendorPath(projectDir, p.Name)
		g.Go(func() error {
			if err := m.Materialize(gctx, key, dest); err != nil {
				return fmt.Errorf("orchestrator: materialize %s: %w", p.Name, err)
			}
			return nil
		})
	}
	return g.Wait()
}

func generateAutoloader(ctx context.Context, projectDir string, pkgs []lock.Package, m *manifest.Manifest, a Autoloader) error {
	if err := a.Generate(ctx, projectDir, pkgs, m); err != nil {
		return fmt.Errorf("orchestrator: autoload: %w", err)
	}
	return nil
}

// writeLock serializes f and writes it atomically to composer-go.lock.
func writeLock(projectDir string, f *lock.File) error {
	data, err := f.Encode()
	if err != nil {
		return fmt.Errorf("orchestrator: encode lock: %w", err)
	}
	final := filepath.Join(projectDir, "composer-go.lock")
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("orchestrator: write lock: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("orchestrator: rename lock: %w", err)
	}
	return nil
}

// runFullPipeline ties all phases together.
func runFullPipeline(ctx context.Context, opts Options, m *manifest.Manifest, forceResolve bool) error {
	if err := defaultDeps(&opts); err != nil {
		return err
	}
	ps, err := newPipelineState(opts, m)
	if err != nil {
		return err
	}
	lockFile, err := resolveOrCache(ctx, ps, forceResolve)
	if err != nil {
		return err
	}

	all := append([]lock.Package(nil), lockFile.Packages...)
	if !opts.NoDev {
		all = append(all, lockFile.PackagesDev...)
	}

	keys, err := fetchAll(ctx, all, opts.Fetcher, workerCount(opts.Workers))
	if err != nil {
		return err
	}
	// Back-fill Dist.Sha256 from the fetched keys so the lockfile records the
	// actual content hash. Packagist sometimes ships empty shasums; we trust
	// the streaming hash computed during download.
	backfillSha(lockFile.Packages, keys)
	backfillSha(lockFile.PackagesDev, keys)
	if err := materializeAll(ctx, opts.ProjectDir, all, keys, opts.Materializer, workerCount(opts.Workers)); err != nil {
		return err
	}
	if err := generateAutoloader(ctx, opts.ProjectDir, all, m, opts.Autoloader); err != nil {
		return err
	}
	if err := writeLock(opts.ProjectDir, lockFile); err != nil {
		return err
	}
	return nil
}

// fetcherAdapter wraps fetcher.Fetcher to implement the orchestrator Fetcher
// interface. It downloads the package zip and returns the SHA256 as the key.
type fetcherAdapter struct {
	f *realfetcher.Fetcher
}

func (a *fetcherAdapter) Fetch(ctx context.Context, pkg lock.Package) (string, error) {
	pv := registry.PackageVersion{
		Name: pkg.Name,
		Dist: registry.Dist{
			Type: pkg.Dist.Type,
			URL:  pkg.Dist.URL,
			Sha:  pkg.Dist.Sha256,
		},
	}
	sha, err := a.f.Fetch(ctx, pv)
	if err != nil {
		return "", err
	}
	return sha, nil
}

// materializerAdapter wraps fetcher.Fetcher to implement the orchestrator Materializer
// interface. The "key" is the sha256 used to look up the zip in the store.
type materializerAdapter struct {
	f *realfetcher.Fetcher
}

func (a *materializerAdapter) Materialize(ctx context.Context, key, dest string) error {
	// We need to reconstruct a registry.PackageVersion with the sha set so
	// the fetcher can locate the zip in the store.
	pv := registry.PackageVersion{
		Name: dest, // name is only used for error messages
		Dist: registry.Dist{
			Type: "zip",
			Sha:  key,
		},
	}
	return a.f.Materialize(ctx, pv, dest)
}

// autoloaderAdapter wraps autoload.Generate to implement the orchestrator Autoloader interface.
type autoloaderAdapter struct{}

func (a *autoloaderAdapter) Generate(ctx context.Context, projectDir string, pkgs []lock.Package, m *manifest.Manifest) error {
	entries := make([]autoloadpkg.Entry, 0, len(pkgs))
	for _, p := range pkgs {
		// InstallPath must be relative to projectDir; the generator builds
		// $baseDir-relative PHP expressions from it.
		installPath := filepath.ToSlash(filepath.Join("vendor", filepath.FromSlash(p.Name)))
		al, excl := autoloadFromLockMap(p.Autoload)
		entries = append(entries, autoloadpkg.Entry{
			Name:                p.Name,
			Version:             p.Version,
			InstallPath:         installPath,
			Autoload:            al,
			ExcludeFromClassmap: excl,
		})
	}
	return autoloadpkg.Generate(autoloadpkg.Options{
		ProjectDir:   projectDir,
		Entries:      entries,
		RootAutoload: m.Autoload,
	})
}

// autoloadFromLockMap converts the lock package's Autoload map (a
// JSON-decoded map[string]any) into a registry.Autoload struct and the
// per-package exclude-from-classmap glob list. The split return is so the
// orchestrator can attach exclude patterns to autoload.Entry, where they
// live (registry.Autoload itself is shared with the resolver, which has
// no business with autoloader exclusion rules).
func autoloadFromLockMap(raw map[string]any) (registry.Autoload, []string) {
	var al registry.Autoload
	if raw == nil {
		return al, nil
	}
	if v, ok := raw["psr-4"]; ok {
		if m, ok := v.(map[string]any); ok {
			al.PSR4 = m
		}
	}
	if v, ok := raw["psr-0"]; ok {
		if m, ok := v.(map[string]any); ok {
			al.PSR0 = m
		}
	}
	if v, ok := raw["files"]; ok {
		al.Files = anySliceToStrings(v)
	}
	if v, ok := raw["classmap"]; ok {
		al.Classmap = anySliceToStrings(v)
	}
	var excl []string
	if v, ok := raw["exclude-from-classmap"]; ok {
		excl = anySliceToStrings(v)
	}
	return al, excl
}

func anySliceToStrings(v any) []string {
	switch t := v.(type) {
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

// defaultDeps wires up the production Fetcher, Materializer, Autoloader, and
// (if absent) Source. Tests typically pre-populate Options so this returns
// quickly with what's already there.
func defaultDeps(opts *Options) error {
	cacheRoot, err := cache.Root()
	if err != nil {
		return err
	}
	if opts.Source == nil {
		c, err := packagist.New(packagist.Config{
			CacheDir: filepath.Join(cacheRoot, "packagist"),
		})
		if err != nil {
			return err
		}
		opts.Source = c
	}
	if opts.Fetcher == nil || opts.Materializer == nil {
		s, err := store.New(filepath.Join(cacheRoot, "store"))
		if err != nil {
			return err
		}
		f := realfetcher.New(s, nil)
		if opts.Fetcher == nil {
			opts.Fetcher = &fetcherAdapter{f: f}
		}
		if opts.Materializer == nil {
			opts.Materializer = &materializerAdapter{f: f}
		}
	}
	if opts.Autoloader == nil {
		opts.Autoloader = &autoloaderAdapter{}
	}
	return nil
}
