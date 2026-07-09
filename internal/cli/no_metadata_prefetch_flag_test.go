package cli

import (
	"context"
	"testing"

	"github.com/torstendittmann/gomposer/internal/orchestrator"
)

// swapInstallRunner temporarily replaces the install runner function
// with the given mock, and returns a restore function.
func swapInstallRunner(mock func(context.Context, orchestrator.Options) error) func() {
	old := installFn
	installFn = mock
	return func() {
		installFn = old
	}
}

// swapUpdateRunner temporarily replaces the update runner function
// with the given mock, and returns a restore function.
func swapUpdateRunner(mock func(context.Context, orchestrator.Options) error) func() {
	old := updateFn
	updateFn = mock
	return func() {
		updateFn = old
	}
}

func TestNoMetadataPrefetchFlagReachesOptionsOnInstall(t *testing.T) {
	var got orchestrator.Options
	restore := swapInstallRunner(func(_ context.Context, opts orchestrator.Options) error {
		got = opts
		return nil
	})
	defer restore()

	root := newRootCmd("dev")
	root.SetArgs([]string{"install", "--no-metadata-prefetch"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !got.NoMetadataPrefetch {
		t.Errorf("Options.NoMetadataPrefetch = false, want true")
	}
}

func TestNoMetadataPrefetchFlagReachesOptionsOnUpdate(t *testing.T) {
	var got orchestrator.Options
	restore := swapUpdateRunner(func(_ context.Context, opts orchestrator.Options) error {
		got = opts
		return nil
	})
	defer restore()

	root := newRootCmd("dev")
	root.SetArgs([]string{"update", "--no-metadata-prefetch"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !got.NoMetadataPrefetch {
		t.Errorf("Options.NoMetadataPrefetch = false, want true")
	}
}

func TestNoMetadataPrefetchFlagDefaultFalseOnInstall(t *testing.T) {
	var got orchestrator.Options
	restore := swapInstallRunner(func(_ context.Context, opts orchestrator.Options) error {
		got = opts
		return nil
	})
	defer restore()

	root := newRootCmd("dev")
	root.SetArgs([]string{"install"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.NoMetadataPrefetch {
		t.Errorf("Options.NoMetadataPrefetch = true, want false (default)")
	}
}
