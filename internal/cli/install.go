package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/torstendittmann/gomposer/internal/orchestrator"
)

// installFn is the function called to run the install operation.
// It can be swapped during testing.
var installFn = orchestrator.Install

func newInstallCmd() *cobra.Command {
	var (
		projectDir         string
		allowPlugins       []string // accepted for Composer-CLI compatibility; no-op (gomposer does not run plugins)
		noPrefetch         bool
		noMetadataPrefetch bool
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install dependencies into vendor/ from composer.json (using gomposer.lock if present)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if projectDir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				projectDir = wd
			}
			if root, ok := findWorkspaceRoot(projectDir); ok {
				projectDir = root
			}
			progress := NewProgress(cmd.ErrOrStderr(), ProgressOptions{
				Quiet: flagQuiet, Color: ColorMode(flagColor), Operation: "install", ProjectDir: projectDir,
			})
			if p, ok := progress.(journeyProgress); ok {
				p.BeginStage("prepare", 0)
			}
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			ignored := append([]string(nil), flagIgnorePlatformReqs...)
			if flagIgnorePlatform {
				ignored = append(ignored, "*")
			}
			_ = allowPlugins // explicitly unused
			err := installFn(ctx, orchestrator.Options{
				ProjectDir:         projectDir,
				NoDev:              flagNoDev,
				NoScripts:          flagNoScripts,
				Verbose:            flagVerbose,
				Quiet:              flagQuiet,
				IgnorePlatformReqs: ignored,
				NoPrefetch:         noPrefetch,
				NoMetadataPrefetch: noMetadataPrefetch,
				Progress:           progress,
				WarnWriter:         cmd.ErrOrStderr(),
			})
			if err != nil {
				if p, ok := progress.(journeyProgress); ok {
					p.Fail(err)
					return markHandled(err)
				}
			}
			return err
		},
	}
	cmd.Flags().StringVar(&projectDir, "project", "", "project directory containing composer.json (defaults to cwd)")
	cmd.Flags().BoolVar(&noPrefetch, "no-prefetch", false, "disable lock-driven speculative prefetch (benchmark hook)")
	cmd.Flags().BoolVar(&noMetadataPrefetch, "no-metadata-prefetch", false, "disable resolver-metadata prefetch (benchmarking hook)")
	cmd.Flags().StringSliceVar(&allowPlugins, "allow-plugins", nil,
		"accepted for Composer compatibility; no-op (gomposer does not run plugins, so this flag has no effect)")
	// Allow bare `--allow-plugins` with no value (Composer accepts that form).
	cmd.Flags().Lookup("allow-plugins").NoOptDefVal = "*"
	return cmd
}
