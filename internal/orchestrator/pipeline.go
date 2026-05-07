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
	platform      string
	cacheKey      string
}

func newPipelineState(opts Options, m *manifest.Manifest) (*pipelineState, error) {
	manifestBytes, err := os.ReadFile(filepath.Join(opts.ProjectDir, "composer.json"))
	if err != nil {
		return nil, fmt.Errorf("orchestrator: read manifest bytes: %w", err)
	}
	lockBytes, _ := os.ReadFile(filepath.Join(opts.ProjectDir, "composer-go.lock"))
	pf, err := platform.Fingerprint()
	if err != nil {
		return nil, fmt.Errorf("orchestrator: platform fingerprint: %w", err)
	}
	return &pipelineState{
		opts:          opts,
		manifest:      m,
		manifestBytes: manifestBytes,
		lockBytes:     lockBytes,
		platform:      pf,
		cacheKey:      computeCacheKey(manifestBytes, lockBytes, pf),
	}, nil
}

// resolveFunc is the resolver entry point, indirected for tests.
var resolveFunc = func(ctx context.Context, m *manifest.Manifest, src registry.SourceLookup, includeDev bool) (*resolver.Result, error) {
	return resolver.Solve(ctx, resolver.Input{
		Manifest:   m,
		Source:     src,
		IncludeDev: includeDev,
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

	res, err := resolveFunc(ctx, ps.manifest, src, !ps.opts.NoDev)
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
		PlatformFingerprint: ps.platform,
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
		al := registryAutoloadFromMap(p.Autoload)
		entries = append(entries, autoloadpkg.Entry{
			Name:        p.Name,
			Version:     p.Version,
			InstallPath: installPath,
			Autoload:    al,
		})
	}
	return autoloadpkg.Generate(autoloadpkg.Options{
		ProjectDir:   projectDir,
		Entries:      entries,
		RootAutoload: m.Autoload,
	})
}

// registryAutoloadFromMap converts the lock package's Autoload map (map[string]any)
// into a registry.Autoload struct. Stage 1 stores PSR4 as a nested map.
func registryAutoloadFromMap(raw map[string]any) registry.Autoload {
	var al registry.Autoload
	if raw == nil {
		return al
	}
	if psr4, ok := raw["psr-4"]; ok {
		if m, ok := psr4.(map[string]any); ok {
			al.PSR4 = m
		}
	}
	if psr0, ok := raw["psr-0"]; ok {
		if m, ok := psr0.(map[string]any); ok {
			al.PSR0 = m
		}
	}
	return al
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
