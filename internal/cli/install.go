package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/torstendittmann/composer-go/internal/orchestrator"
)

func newInstallCmd() *cobra.Command {
	var projectDir string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install dependencies into vendor/ from composer.json (using composer-go.lock if present)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if projectDir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				projectDir = wd
			}
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			ignored := append([]string(nil), flagIgnorePlatformReqs...)
			if flagIgnorePlatform {
				ignored = append(ignored, "*")
			}
			return orchestrator.Install(ctx, orchestrator.Options{
				ProjectDir:         projectDir,
				NoDev:              flagNoDev,
				NoScripts:          flagNoScripts,
				Verbose:            flagVerbose,
				Quiet:              flagQuiet,
				IgnorePlatformReqs: ignored,
			})
		},
	}
	cmd.Flags().StringVar(&projectDir, "project", "", "project directory containing composer.json (defaults to cwd)")
	return cmd
}
