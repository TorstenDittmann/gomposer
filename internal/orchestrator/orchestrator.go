// Package orchestrator drives the full install/update pipeline. It is the
// only package in composer-go that knows the order of phases:
//
//	read manifest -> [maybe read lock] -> [maybe consult resolution cache] ->
//	resolve -> fetch -> materialize vendor/ -> generate autoloader ->
//	write lock.
//
// Every other package exposes a narrow API. The orchestrator owns the
// errgroup, the worker pool, and the cancellation context.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/torstendittmann/composer-go/internal/manifest"
	"github.com/torstendittmann/composer-go/internal/registry"
	"github.com/torstendittmann/composer-go/internal/scripts"
)

// ScriptsRunner runs lifecycle scripts. The default implementation executes
// subprocesses; tests inject a recording fake.
type ScriptsRunner interface {
	Run(ctx context.Context, event scripts.Event, opts scripts.Options) error
}

// Options configures a single Install or Update run.
type Options struct {
	// ProjectDir is the directory containing composer.json. Required.
	ProjectDir string
	// NoDev mirrors --no-dev: skip require-dev and enforce platform
	// requirements strictly.
	NoDev bool
	// NoScripts disables all lifecycle script firing (--no-scripts).
	NoScripts bool
	// Verbose enables phase-timing logs.
	Verbose bool
	// Workers caps the parallel-fetch worker count. Zero -> runtime.NumCPU().
	Workers int
	// NoNetwork is a test hook: if set, the orchestrator must complete
	// without making network calls. Used by unit tests with empty manifests
	// and by future "offline mode" flags.
	NoNetwork bool
	// NoPrefetch disables stage-3 lock-driven speculative prefetch. Default
	// (false) means prefetch is on. Mostly useful for benchmarks that want
	// to measure the isolated wall-clock contribution of optimistic op 1.
	//
	// Prefetch is also implicitly disabled when:
	//   - forceResolve is true (the update path),
	//   - NoNetwork is true,
	//   - the lockfile is absent or fails to parse.
	NoPrefetch bool
	// Source overrides the default Packagist source. Tests inject a fake
	// here. Production callers leave it nil.
	Source registry.SourceLookup

	// IgnorePlatformReqs is the parsed form of --ignore-platform-req
	// (repeatable). A value of "*" means "ignore all platform reqs"
	// (--ignore-platform).
	IgnorePlatformReqs []string

	// Quiet suppresses non-error output (warnings, info messages).
	Quiet bool

	// Test-only injection points. Production callers leave these nil and
	// the orchestrator constructs the real implementations.
	Fetcher      Fetcher
	Materializer Materializer
	Autoloader   Autoloader
	// Scripts is the runner for lifecycle events. Tests inject a fake;
	// production callers leave it nil and defaultDeps wires the real one.
	Scripts ScriptsRunner

	// WarnWriter receives stage-2 plugin warnings. Defaults to os.Stderr
	// when nil. Tests inject a buffer to assert on the rendered text.
	WarnWriter io.Writer
}

// Install runs the install pipeline: use the existing lockfile if present and
// up to date, otherwise resolve fresh.
func Install(ctx context.Context, opts Options) error {
	m, err := loadManifest(opts.ProjectDir)
	if err != nil {
		return err
	}
	return run(ctx, opts, m, false /* forceResolve */)
}

// Update runs the update pipeline: re-resolve every package regardless of
// the lockfile, then materialize.
func Update(ctx context.Context, opts Options) error {
	m, err := loadManifest(opts.ProjectDir)
	if err != nil {
		return err
	}
	return run(ctx, opts, m, true /* forceResolve */)
}

func loadManifest(projectDir string) (*manifest.Manifest, error) {
	if projectDir == "" {
		return nil, errors.New("orchestrator: ProjectDir is required")
	}
	path := filepath.Join(projectDir, "composer.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: read manifest: %w", err)
	}
	m, err := manifest.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: parse manifest: %w", err)
	}
	return m, nil
}

func workerCount(opt int) int {
	if opt > 0 {
		return opt
	}
	if n := runtime.NumCPU(); n > 0 {
		return n
	}
	return 4
}

func run(ctx context.Context, opts Options, m *manifest.Manifest, forceResolve bool) error {
	if len(m.Require) == 0 && len(m.RequireDev) == 0 {
		return nil
	}
	if opts.NoNetwork {
		return errors.New("orchestrator: NoNetwork is set but manifest has requires")
	}
	t := NewTimings()
	err := runFullPipeline(ctx, opts, m, forceResolve, t)
	if opts.Verbose && !opts.Quiet {
		t.Render(os.Stderr)
	}
	return err
}
