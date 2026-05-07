package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	flagVerbose bool
	flagNoDev   bool
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "composer-go",
		Short:         "A fast Go-based PHP package manager",
		Long:          "composer-go installs PHP packages described in composer.json. It is a compatible consumer of composer.json but writes its own composer-go.lock.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().BoolVarP(&flagVerbose, "verbose", "v", false, "verbose output with timing breakdown")
	root.PersistentFlags().BoolVar(&flagNoDev, "no-dev", false, "skip require-dev dependencies; enforce platform requirements strictly")

	root.AddCommand(newInstallCmd())
	root.AddCommand(newUpdateCmd())
	return root
}

// Execute runs the CLI and returns an error on failure. Errors are printed
// to stderr by Execute itself, so callers should not double-print.
func Execute() error {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "composer-go: %v\n", err)
		return err
	}
	return nil
}
