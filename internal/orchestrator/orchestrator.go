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
	"os"
	"path/filepath"
	"runtime"

	"github.com/torstendittmann/composer-go/internal/manifest"
	"github.com/torstendittmann/composer-go/internal/registry"
)

// Options configures a single Install or Update run.
type Options struct {
	// ProjectDir is the directory containing composer.json. Required.
	ProjectDir string
	// NoDev mirrors --no-dev: skip require-dev and enforce platform
	// requirements strictly.
	NoDev bool
	// Verbose enables phase-timing logs.
	Verbose bool
	// Workers caps the parallel-fetch worker count. Zero -> runtime.NumCPU().
	Workers int
	// NoNetwork is a test hook: if set, the orchestrator must complete
	// without making network calls. Used by unit tests with empty manifests
	// and by future "offline mode" flags.
	NoNetwork bool
	// Source overrides the default Packagist source. Tests inject a fake
	// here. Production callers leave it nil.
	Source registry.SourceLookup

	// Test-only injection points. Production callers leave these nil and
	// the orchestrator constructs the real implementations.
	Fetcher      Fetcher
	Materializer Materializer
	Autoloader   Autoloader
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

// run is filled in by subsequent tasks. Stage 1: empty manifest path.
func run(ctx context.Context, opts Options, m *manifest.Manifest, forceResolve bool) error {
	if len(m.Require) == 0 && len(m.RequireDev) == 0 {
		// Nothing to do; the empty-pipeline test exercises this branch.
		return nil
	}
	return errors.New("orchestrator: pipeline not yet wired (later tasks)")
}
