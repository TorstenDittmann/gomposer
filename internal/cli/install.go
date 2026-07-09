package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/torstendittmann/gomposer/internal/orchestrator"
)

func newInstallCmd() *cobra.Command {
	var (
		projectDir   string
		allowPlugins []string // accepted for Composer-CLI compatibility; no-op (gomposer does not run plugins)
		noPrefetch   bool
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install dependencies into vendor/ from composer.json (using composer.lock if present)",
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
			_ = allowPlugins // explicitly unused
			return orchestrator.Install(ctx, orchestrator.Options{
				ProjectDir:         projectDir,
				NoDev:              flagNoDev,
				NoScripts:          flagNoScripts,
				Verbose:            flagVerbose,
				Quiet:              flagQuiet,
				IgnorePlatformReqs: ignored,
				NoPrefetch:         noPrefetch,
				Progress:           NewProgress(cmd.ErrOrStderr(), ProgressOptions{Quiet: flagQuiet}),
			})
		},
	}
	cmd.Flags().StringVar(&projectDir, "project", "", "project directory containing composer.json (defaults to cwd)")
	cmd.Flags().BoolVar(&noPrefetch, "no-prefetch", false, "disable lock-driven speculative prefetch (benchmark hook)")
	cmd.Flags().StringSliceVar(&allowPlugins, "allow-plugins", nil,
		"accepted for Composer compatibility; no-op (gomposer does not run plugins, so this flag has no effect)")
	// Allow bare `--allow-plugins` with no value (Composer accepts that form).
	cmd.Flags().Lookup("allow-plugins").NoOptDefVal = "*"
	return cmd
}
