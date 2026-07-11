package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/torstendittmann/gomposer/internal/manifest"
)

var (
	flagVerbose            bool
	flagNoDev              bool
	flagNoScripts          bool
	flagQuiet              bool
	flagIgnorePlatform     bool
	flagIgnorePlatformReqs []string
)

func newRootCmd(version string) *cobra.Command {
	root := &cobra.Command{
		Use:           "gomposer",
		Short:         "A fast Go-based PHP package manager",
		Long:          "gomposer installs PHP packages described in composer.json. It is a compatible consumer of composer.json but writes its own gomposer.lock.",
		Version:       version,
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
// to stderr by Execute itself, so callers should not double-print. The
// version string is what `--version` prints; callers pass their own build-
// tagged value or "dev" for unstamped builds.
func Execute(version string) error {
	root := newRootCmd(version)
	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "gomposer: %v\n", err)
		return err
	}
	return nil
}

// findWorkspaceRoot walks up from cwd looking for a composer.json whose
// parsed Workspaces field is non-empty. Stops at the filesystem root or
// on entering an ancestor whose own composer.json declares no
// workspaces AND contains a .git directory (project boundary). Returns
// (dir, true) on success, ("", false) otherwise.
func findWorkspaceRoot(cwd string) (string, bool) {
	cur := cwd
	for {
		manifestPath := filepath.Join(cur, "composer.json")
		if body, err := os.ReadFile(manifestPath); err == nil {
			var m manifest.Manifest
			if json.Unmarshal(body, &m) == nil && len(m.Workspaces) > 0 {
				return cur, true
			}
			// Own composer.json without workspaces + .git → project
			// boundary; don't cross it.
			if _, err := os.Stat(filepath.Join(cur, ".git")); err == nil {
				return "", false
			}
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false // filesystem root
		}
		cur = parent
	}
}
