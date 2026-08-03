package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/torstendittmann/gomposer/internal/orchestrator"
)

// removeFn is indirected so command tests can inspect the manifest at the
// exact point the normal forced-update pipeline would start.
var removeFn = orchestrator.Update

func newRemoveCmd() *cobra.Command {
	var (
		projectDir         string
		dev                bool
		allowPlugins       []string
		noPrefetch         bool
		noMetadataPrefetch bool
	)
	cmd := &cobra.Command{
		Use:   "remove [--dev] package...",
		Short: "Remove direct dependencies and update the installed package set",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, name := range args {
				if !validRequirementName(name) {
					return fmt.Errorf("invalid package name %q", name)
				}
			}

			targetDir := projectDir
			if targetDir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				targetDir = wd
			}
			targetDir, err := filepath.Abs(targetDir)
			if err != nil {
				return err
			}
			runDir := targetDir
			if root, ok := findWorkspaceRoot(targetDir); ok {
				runDir = root
			}

			progress := NewProgress(cmd.ErrOrStderr(), ProgressOptions{
				Quiet: flagQuiet, Color: ColorMode(flagColor), Operation: "remove", ProjectDir: runDir,
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
			_ = allowPlugins
			err = removeTransaction(ctx, filepath.Join(targetDir, "composer.json"), filepath.Join(runDir, "gomposer.lock"), args, dev, orchestrator.Options{
				ProjectDir:         runDir,
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
	cmd.Flags().BoolVarP(&dev, "dev", "D", false, "remove packages from require-dev")
	cmd.Flags().StringVar(&projectDir, "project", "", "project directory containing composer.json (defaults to cwd)")
	cmd.Flags().BoolVar(&noPrefetch, "no-prefetch", false, "disable lock-driven speculative prefetch (benchmark hook)")
	cmd.Flags().BoolVar(&noMetadataPrefetch, "no-metadata-prefetch", false, "disable resolver-metadata prefetch (benchmarking hook)")
	cmd.Flags().StringSliceVar(&allowPlugins, "allow-plugins", nil,
		"accepted for Composer compatibility; no-op (gomposer does not run plugins, so this flag has no effect)")
	cmd.Flags().Lookup("allow-plugins").NoOptDefVal = "*"
	return cmd
}

func updateManifestRemovals(data []byte, names []string, dev bool) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse composer.json: %w", err)
	}
	if doc == nil {
		return nil, errors.New("parse composer.json: top-level value must be an object")
	}
	targetField, otherField := "require", "require-dev"
	if dev {
		targetField, otherField = otherField, targetField
	}
	target, err := decodeRequirementMap(doc[targetField], targetField)
	if err != nil {
		return nil, err
	}
	other, err := decodeRequirementMap(doc[otherField], otherField)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if _, ok := target[name]; ok {
			continue
		}
		if _, ok := other[name]; ok {
			if dev {
				return nil, fmt.Errorf("remove: %s is listed in require; omit --dev to remove it", name)
			}
			return nil, fmt.Errorf("remove: %s is listed in require-dev; use --dev to remove it", name)
		}
		return nil, fmt.Errorf("remove: %s is not a direct dependency in %s", name, targetField)
	}
	for _, name := range names {
		delete(target, name)
	}
	if err := setRequirementMap(doc, targetField, target); err != nil {
		return nil, err
	}
	return encodeManifestDocument(doc)
}

func removeTransaction(ctx context.Context, manifestPath, lockPath string, names []string, dev bool, opts orchestrator.Options) error {
	return dependencyTransaction(ctx, "remove", manifestPath, lockPath, opts, removeFn, func(data []byte) ([]byte, error) {
		return updateManifestRemovals(data, names, dev)
	})
}
