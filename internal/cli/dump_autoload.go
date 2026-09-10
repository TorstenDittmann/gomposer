package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/torstendittmann/gomposer/internal/orchestrator"
)

// dumpAutoloadFn is the function called to regenerate autoload files.
// It can be swapped during testing.
var dumpAutoloadFn = orchestrator.DumpAutoload

func newDumpAutoloadCmd() *cobra.Command {
	var projectDir string
	cmd := &cobra.Command{
		Use:     "dump-autoload",
		Aliases: []string{"dumpautoload"},
		Short:   "Regenerate vendor autoload files from gomposer.lock",
		Long:    "Regenerate vendor/autoload.php and the Composer helper files from the current gomposer.lock without resolving or reinstalling packages.",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if projectDir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				projectDir = wd
			}
			absDir, err := filepath.Abs(projectDir)
			if err != nil {
				return err
			}
			projectDir = absDir
			if root, ok := findWorkspaceRoot(projectDir); ok {
				projectDir = root
			}

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			err = dumpAutoloadFn(ctx, orchestrator.Options{
				ProjectDir: projectDir,
				NoDev:      flagNoDev,
				NoScripts:  flagNoScripts,
				Verbose:    flagVerbose,
				Quiet:      flagQuiet,
				WarnWriter: cmd.ErrOrStderr(),
			})
			if err != nil {
				return err
			}
			if !flagQuiet {
				fmt.Fprintln(cmd.OutOrStdout(), "Generated autoload files")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&projectDir, "project", "", "project directory containing composer.json (defaults to cwd)")
	return cmd
}
