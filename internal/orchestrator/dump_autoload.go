package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/torstendittmann/gomposer/internal/lock"
	"github.com/torstendittmann/gomposer/internal/scripts"
)

// DumpAutoload regenerates vendor autoload files from the existing
// gomposer.lock without resolving, fetching, or materializing packages.
// Root autoload / autoload-dev is re-read from composer.json; package
// autoload maps come from the lock so dump and install cannot drift.
func DumpAutoload(ctx context.Context, opts Options) error {
	m, err := loadManifest(opts.ProjectDir)
	if err != nil {
		return err
	}
	lockFile, err := loadLockFile(opts.ProjectDir)
	if err != nil {
		return err
	}

	t := NewTimings()
	if opts.Autoloader == nil {
		opts.Autoloader = &autoloaderAdapter{}
	}
	if opts.Scripts == nil && !opts.NoScripts {
		opts.Scripts = scripts.New()
	}

	if err := firePhase(ctx, t, scripts.EventPreAutoloadDump, opts, m); err != nil {
		return err
	}

	beginStage(opts.Progress, "autoload", 0)
	t.Begin("autoload")
	err = generateAutoloader(ctx, newAutoloadRequest(opts.ProjectDir, lockFile, m, opts.NoDev), opts.Autoloader)
	t.End("autoload")
	if err != nil {
		return err
	}

	if err := firePhase(ctx, t, scripts.EventPostAutoloadDump, opts, m); err != nil {
		return err
	}
	endStage(opts.Progress, "autoload", "generated")
	t.FlushScripts()
	renderTimings(opts, t)
	return nil
}

func loadLockFile(projectDir string) (*lock.File, error) {
	path := filepath.Join(projectDir, "gomposer.lock")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("orchestrator: gomposer.lock not found; run gomposer install first")
		}
		return nil, fmt.Errorf("orchestrator: read lock: %w", err)
	}
	f, err := lock.Decode(data)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: parse lock: %w", err)
	}
	return f, nil
}
