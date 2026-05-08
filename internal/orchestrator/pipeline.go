package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/torstendittmann/composer-go/internal/auth"
	autoloadpkg "github.com/torstendittmann/composer-go/internal/autoload"
	"github.com/torstendittmann/composer-go/internal/cache"
	realfetcher "github.com/torstendittmann/composer-go/internal/fetcher"
	"github.com/torstendittmann/composer-go/internal/lock"
	"github.com/torstendittmann/composer-go/internal/manifest"
	"github.com/torstendittmann/composer-go/internal/platform"
	"github.com/torstendittmann/composer-go/internal/plugins"
	"github.com/torstendittmann/composer-go/internal/registry"
	"github.com/torstendittmann/composer-go/internal/registry/multisource"
	"github.com/torstendittmann/composer-go/internal/registry/packagist"
	"github.com/torstendittmann/composer-go/internal/registry/vcs"
	"github.com/torstendittmann/composer-go/internal/resolver"
	"github.com/torstendittmann/composer-go/internal/scripts"
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
		// Only --no-dev mode hard-fails on platform mismatches; default mode
		// keeps incompatible versions in the candidate pool and reports
		// warnings post-resolution.
		StrictPlatform: ps.opts.NoDev,
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
	if err := defaultDeps(&opts, m, nil); err != nil {
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

// fireEvent invokes the user's scripts for `event`. No-op when:
//   - opts.NoScripts is true (CLI flag),
//   - opts.Scripts is nil (test path with no runner injected),
//   - the manifest has no entries for this event.
func fireEvent(ctx context.Context, event scripts.Event, opts Options, m *manifest.Manifest) error {
	if opts.NoScripts || opts.Scripts == nil {
		return nil
	}
	return opts.Scripts.Run(ctx, event, scripts.Options{
		ProjectDir: opts.ProjectDir,
		Scripts:    m.Scripts,
		Verbose:    opts.Verbose,
	})
}

// firePhase wraps fireEvent with timing accumulation. The `scripts` phase is
// the sum of all four lifecycle event firings; we add to it incrementally.
func firePhase(ctx context.Context, t *Timings, event scripts.Event, opts Options, m *manifest.Manifest) error {
	if opts.NoScripts || opts.Scripts == nil {
		return nil
	}
	start := time.Now()
	err := opts.Scripts.Run(ctx, event, scripts.Options{
		ProjectDir: opts.ProjectDir,
		Scripts:    m.Scripts,
		Verbose:    opts.Verbose,
	})
	if t != nil {
		// Append directly so multiple calls collapse to a single phase entry.
		t.AddScriptsTime(time.Since(start))
	}
	return err
}

// runFullPipeline ties all phases together.
func runFullPipeline(ctx context.Context, opts Options, m *manifest.Manifest, forceResolve bool, t *Timings) error {
	if err := defaultDeps(&opts, m, t); err != nil {
		return err
	}

	preCmd := scripts.EventPreInstall
	postCmd := scripts.EventPostInstall
	if forceResolve {
		preCmd = scripts.EventPreUpdate
		postCmd = scripts.EventPostUpdate
	}

	if err := firePhase(ctx, t, preCmd, opts, m); err != nil {
		return err
	}

	t.Begin("read manifest")
	ps, err := newPipelineState(opts, m)
	t.End("read manifest")
	if err != nil {
		return err
	}

	t.Begin("resolve")
	lockFile, err := resolveOrCache(ctx, ps, forceResolve)
	t.End("resolve")
	if err != nil {
		return err
	}
	t.SetPackagesResolved(len(lockFile.Packages) + len(lockFile.PackagesDev))

	// Stage-2 plugin policy: detect composer-plugin / composer-installer
	// packages and emit one warning per plugin to stderr. The packages
	// themselves still flow through fetch + materialize — they are installed
	// into vendor/ but never executed. See
	// docs/superpowers/plans/2026-05-08-stage2-plan6-plugin-warning.md.
	if warnings := plugins.Inspect(lockFile, m); len(warnings) > 0 {
		w := opts.WarnWriter
		if w == nil {
			w = os.Stderr
		}
		plugins.Render(w, warnings)
	}

	all := append([]lock.Package(nil), lockFile.Packages...)
	if !opts.NoDev {
		all = append(all, lockFile.PackagesDev...)
	}

	// Platform warnings: emit, persist on the lockfile so cache-hit runs can
	// re-emit them, and (in --no-dev) escalate to a hard error.
	warnings, err := evaluatePlatformWarnings(all, ps.platform, ps.ignoreSet, opts.NoDev, opts.Quiet, os.Stderr)
	if err != nil {
		return err
	}
	if len(warnings) > 0 {
		lockFile.Warnings = warnings
	} else if !opts.NoDev {
		// Replay-on-cache-hit: if we're using a cached/existing lock and it
		// already has warnings, re-emit them now.
		if !opts.Quiet {
			for _, w := range lockFile.Warnings {
				fmt.Fprintln(os.Stderr, "composer-go: "+w)
			}
		}
	}

	t.Begin("fetch")
	keys, err := fetchAll(ctx, all, opts.Fetcher, workerCount(opts.Workers))
	if err != nil {
		t.End("fetch")
		return err
	}
	// Back-fill Dist.Sha256 from the fetched keys so the lockfile records the
	// actual content hash. Packagist sometimes ships empty shasums; we trust
	// the streaming hash computed during download.
	backfillSha(lockFile.Packages, keys)
	backfillSha(lockFile.PackagesDev, keys)
	t.End("fetch")

	t.Begin("materialize")
	matErr := materializeAll(ctx, opts.ProjectDir, all, keys, opts.Materializer, workerCount(opts.Workers))
	t.End("materialize")
	if matErr != nil {
		return matErr
	}

	if err := firePhase(ctx, t, scripts.EventPreAutoloadDump, opts, m); err != nil {
		return err
	}

	t.Begin("autoload")
	alErr := generateAutoloader(ctx, opts.ProjectDir, all, m, opts.Autoloader)
	t.End("autoload")
	if alErr != nil {
		return alErr
	}

	if err := firePhase(ctx, t, scripts.EventPostAutoloadDump, opts, m); err != nil {
		return err
	}

	t.Begin("write lock")
	wlErr := writeLock(opts.ProjectDir, lockFile)
	t.End("write lock")
	if wlErr != nil {
		return wlErr
	}

	if err := firePhase(ctx, t, postCmd, opts, m); err != nil {
		return err
	}
	t.FlushScripts()
	return nil
}

// evaluatePlatformWarnings walks every package's require map, runs
// platform.Check, and produces:
//   - a slice of formatted warning strings (for the lockfile + future replay),
//   - prints each to `stderr` unless `quiet` is set,
//   - errors if `noDev` is true and any non-lib-* violation occurred.
//
// lib-* violations are coalesced into a single info-level message printed
// at most once per call.
func evaluatePlatformWarnings(
	pkgs []lock.Package,
	pf *platform.Platform,
	ignored map[string]bool,
	noDev bool,
	quiet bool,
	stderr io.Writer,
) ([]string, error) {
	if pf == nil {
		// Platform was skipped (e.g. --ignore-platform); nothing to check.
		return nil, nil
	}
	var (
		warnings  []string
		hardFails []string
		sawLib    bool
	)
	for _, p := range pkgs {
		violations := platform.Check(p.Require, pf, ignored)
		for _, v := range violations {
			if v.Kind == platform.ViolationLibIgnored {
				sawLib = true
				continue
			}
			line := formatViolation(p.Name, v)
			warnings = append(warnings, line)
			hardFails = append(hardFails, line)
			if !quiet {
				fmt.Fprintln(stderr, "composer-go: "+line)
			}
		}
	}
	if sawLib {
		const libLine = "ignoring lib-* platform requirements (not implemented)"
		warnings = append(warnings, libLine)
		if !quiet {
			fmt.Fprintln(stderr, "composer-go: "+libLine)
		}
	}
	if noDev && len(hardFails) > 0 {
		return warnings, fmt.Errorf("orchestrator: platform requirements unsatisfied (--no-dev): %d violation(s)", len(hardFails))
	}
	return warnings, nil
}

func formatViolation(pkg string, v platform.Violation) string {
	switch v.Kind {
	case platform.ViolationMissing:
		return fmt.Sprintf("%s requires %s %q but %s", pkg, v.Req, v.Constraint, v.Have)
	case platform.ViolationVersion:
		return fmt.Sprintf("%s requires %s %q (have %s)", pkg, v.Req, v.Constraint, v.Have)
	case platform.ViolationUnparseable:
		return fmt.Sprintf("%s requires %s %q (unparseable constraint)", pkg, v.Req, v.Constraint)
	default:
		return fmt.Sprintf("%s requires %s %q", pkg, v.Req, v.Constraint)
	}
}

// fetcherAdapter wraps fetcher.Fetcher to implement the orchestrator Fetcher
// interface. It downloads the package zip and returns the SHA256 as the key.
// For VCS-sourced packages with no Dist URL, it falls back to git-archiving
// the source ref via the matching vcs.Client.
type fetcherAdapter struct {
	f          *realfetcher.Fetcher
	store      *store.Store
	vcsClients []*vcs.Client // matched against pkg.Source.URL when Dist is empty
}

func (a *fetcherAdapter) Fetch(ctx context.Context, pkg lock.Package) (string, error) {
	// Pure-VCS packages have an empty Dist (Packagist-tagged releases provide
	// a Dist; resolved-from-VCS branches do not). Fall back to git archive.
	if pkg.Dist.URL == "" && pkg.Source.Type == "git" && pkg.Source.URL != "" {
		return a.fetchViaGitArchive(ctx, pkg)
	}
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

func (a *fetcherAdapter) fetchViaGitArchive(ctx context.Context, pkg lock.Package) (string, error) {
	client := a.findVCSClient(pkg.Source.URL)
	if client == nil {
		return "", fmt.Errorf("orchestrator: %s: source URL %q has no matching vcs repository entry", pkg.Name, pkg.Source.URL)
	}
	tmp, err := os.CreateTemp(filepath.Dir(a.store.Path("x")), "vcs-*.zip")
	if err != nil {
		return "", fmt.Errorf("orchestrator: %s: create tmp: %w", pkg.Name, err)
	}
	tmpPath := tmp.Name()
	hasher := sha256.New()
	mw := io.MultiWriter(tmp, hasher)
	if err := client.Archive(ctx, pkg.Source.Ref, mw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("orchestrator: %s: %w", pkg.Name, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	sha := hex.EncodeToString(hasher.Sum(nil))
	dest := a.store.Path(sha)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		_ = os.Remove(tmpPath)
		if a.store.Has(sha) {
			return sha, nil
		}
		return "", fmt.Errorf("orchestrator: %s: rename: %w", pkg.Name, err)
	}
	return sha, nil
}

func (a *fetcherAdapter) findVCSClient(sourceURL string) *vcs.Client {
	for _, c := range a.vcsClients {
		if c.URL() == sourceURL {
			return c
		}
	}
	return nil
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
func defaultDeps(opts *Options, m *manifest.Manifest, t *Timings) error {
	cacheRoot, err := cache.Root()
	if err != nil {
		return err
	}

	// VCS clients are built once and reused both by the Source aggregator and
	// the fetcher (which falls back to git-archive for VCS-source packages
	// with no Dist URL).
	var vcsClients []*vcs.Client
	if m != nil && len(m.Repositories) > 0 {
		vcsClients, _ = vcs.NewFromManifest(m.Repositories, vcs.Options{
			CacheRoot: filepath.Join(cacheRoot, "vcs"),
		})
	}

	if opts.Source == nil {
		// Auth store: best-effort load from default paths. A missing or
		// unreadable file is non-fatal (no credentials applied).
		authStore, _ := auth.Load()

		// Packagist client (always present; serves as the fallback).
		pc, err := packagist.New(packagist.Config{
			CacheDir: filepath.Join(cacheRoot, "packagist"),
			Auth:     authStore,
		})
		if err != nil {
			return err
		}

		// Aggregate: VCS first (so explicit repos win over Packagist),
		// then Packagist as fallback.
		if len(vcsClients) > 0 {
			lookups := make([]registry.SourceLookup, 0, len(vcsClients)+1)
			for _, c := range vcsClients {
				lookups = append(lookups, c)
			}
			lookups = append(lookups, pc)
			opts.Source = multisource.NewWithLookups(lookups)
		} else {
			opts.Source = pc
		}
	}
	if opts.Fetcher == nil || opts.Materializer == nil {
		s, err := store.New(filepath.Join(cacheRoot, "store"))
		if err != nil {
			return err
		}
		f := realfetcher.New(s, nil)
		if t != nil {
			f.OnFetch = t.AddFetch
		}
		if opts.Fetcher == nil {
			opts.Fetcher = &fetcherAdapter{f: f, store: s, vcsClients: vcsClients}
		}
		if opts.Materializer == nil {
			opts.Materializer = &materializerAdapter{f: f}
		}
	}
	if opts.Autoloader == nil {
		opts.Autoloader = &autoloaderAdapter{}
	}
	if opts.Scripts == nil && !opts.NoScripts {
		opts.Scripts = scripts.New()
	}
	return nil
}
