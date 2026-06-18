package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	flagVerbose            bool
	flagNoDev              bool
	flagNoScripts          bool
	flagQuiet              bool
	flagIgnorePlatform     bool
	flagIgnorePlatformReqs []string
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "gomposer",
		Short:         "A fast Go-based PHP package manager",
		Long:          "gomposer installs PHP packages described in composer.json. It is a compatible consumer of composer.json but writes its own gomposer.lock.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().BoolVarP(&flagVerbose, "verbose", "v", false, "verbose output with timing breakdown")
	root.PersistentFlags().BoolVar(&flagNoDev, "no-dev", false, "skip require-dev dependencies; enforce platform requirements strictly")
	root.PersistentFlags().BoolVar(&flagNoScripts, "no-scripts", false, "skip every user-defined script entry (CI / debugging)")
	root.PersistentFlags().BoolVarP(&flagQuiet, "quiet", "q", false, "suppress non-error output")
	root.PersistentFlags().BoolVar(&flagIgnorePlatform, "ignore-platform", false, "skip ALL platform requirement checks (php / ext-*)")
	root.PersistentFlags().StringArrayVar(&flagIgnorePlatformReqs, "ignore-platform-req", nil,
		"skip a specific platform requirement (repeatable, e.g. --ignore-platform-req=php --ignore-platform-req=ext-curl)")

	root.AddCommand(newInstallCmd())
	root.AddCommand(newUpdateCmd())
	return root
}

// Execute runs the CLI and returns an error on failure. Errors are printed
// to stderr by Execute itself, so callers should not double-print.
func Execute() error {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "gomposer: %v\n", err)
		return err
	}
	return nil
}
