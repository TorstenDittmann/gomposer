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

	"github.com/torstendittmann/composer-go/internal/lock"
	"github.com/torstendittmann/composer-go/internal/manifest"
	"github.com/torstendittmann/composer-go/internal/platform"
	"github.com/torstendittmann/composer-go/internal/registry"
	"github.com/torstendittmann/composer-go/internal/resolver"
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
//   - returns the cached resolution if (manifest, lock, platform) matches,
//   - or runs the resolver and caches the result.
//
// forceResolve=true skips the cache (Update path).
func resolveOrCache(ctx context.Context, ps *pipelineState, forceResolve bool) (*lock.File, error) {
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

// defaultDeps wires up the production Fetcher, Materializer, Autoloader, and
// (if absent) Source. Tests typically pre-populate Options so this returns
// quickly with what's already there.
func defaultDeps(opts *Options) error {
	if opts.Source == nil || opts.Fetcher == nil || opts.Materializer == nil || opts.Autoloader == nil {
		return fmt.Errorf("orchestrator: production deps not implemented in stage 1 (inject via Options for tests)")
	}
	return nil
}
