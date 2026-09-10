package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/torstendittmann/gomposer/internal/orchestrator"
)

const defaultScriptTimeoutSec = 300

// runScriptFn is called to execute a named composer.json script.
// It can be swapped during testing.
var runScriptFn = orchestrator.RunScript

// listScriptsFn is called by `gomposer run --list`.
var listScriptsFn = orchestrator.ListScripts

func newRunCmd() *cobra.Command {
	var (
		projectDir string
		list       bool
		timeoutSec int
	)
	cmd := &cobra.Command{
		Use:     "run [script] [-- args...]",
		Aliases: []string{"run-script"},
		Short:   "Run a script defined in composer.json",
		Long:    "Run a named script from composer.json (custom names and lifecycle events). Extra arguments after -- are appended to shell script bodies. --list prints defined script names. --timeout is seconds; 0 means no timeout (default 300).",
		Args:    cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if timeoutSec < 0 {
				return fmt.Errorf("run: --timeout must be >= 0")
			}
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

			if list {
				names, err := listScriptsFn(projectDir)
				if err != nil {
					return err
				}
				for _, name := range names {
					fmt.Fprintln(cmd.OutOrStdout(), name)
				}
				return nil
			}
			if len(args) == 0 {
				return fmt.Errorf("run: script name is required (or pass --list)")
			}

			var timeout time.Duration
			if timeoutSec > 0 {
				timeout = time.Duration(timeoutSec) * time.Second
			}

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			return runScriptFn(ctx, orchestrator.ScriptCommand{
				ProjectDir: projectDir,
				Name:       args[0],
				ExtraArgs:  args[1:],
				Timeout:    timeout,
				Verbose:    flagVerbose,
			})
		},
	}
	cmd.Flags().StringVar(&projectDir, "project", "", "project directory containing composer.json (defaults to cwd)")
	cmd.Flags().BoolVarP(&list, "list", "l", false, "list scripts defined in composer.json")
	cmd.Flags().IntVar(&timeoutSec, "timeout", defaultScriptTimeoutSec, "script timeout in seconds; 0 for no timeout")
	return cmd
}
