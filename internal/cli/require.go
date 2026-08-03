package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/torstendittmann/gomposer/internal/constraint"
	"github.com/torstendittmann/gomposer/internal/manifest"
	"github.com/torstendittmann/gomposer/internal/orchestrator"
)

// requireFn is indirected so command tests can exercise the manifest
// transaction without contacting a registry.
var requireFn = orchestrator.Update

type requirementSpec struct {
	Name       string
	Constraint string
}

var composerPackageName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9_.-]*[a-z0-9])?/[a-z0-9](?:[a-z0-9_.-]*[a-z0-9])?$`)
var composerPlatformSuffix = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9_.-]*[a-z0-9])?$`)

func newRequireCmd() *cobra.Command {
	var (
		projectDir         string
		dev                bool
		allowPlugins       []string
		noPrefetch         bool
		noMetadataPrefetch bool
	)
	cmd := &cobra.Command{
		Use:   "require [--dev] package[:constraint]...",
		Short: "Add dependencies to composer.json and install them",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			specs := make([]requirementSpec, 0, len(args))
			for _, arg := range args {
				spec, err := parseRequirement(arg)
				if err != nil {
					return err
				}
				specs = append(specs, spec)
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
				Quiet: flagQuiet, Color: ColorMode(flagColor), Operation: "require", ProjectDir: runDir,
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
			err = requireTransaction(ctx, filepath.Join(targetDir, "composer.json"), filepath.Join(runDir, "gomposer.lock"), specs, dev, orchestrator.Options{
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
	cmd.Flags().BoolVarP(&dev, "dev", "D", false, "add packages to require-dev")
	cmd.Flags().StringVar(&projectDir, "project", "", "project directory containing composer.json (defaults to cwd)")
	cmd.Flags().BoolVar(&noPrefetch, "no-prefetch", false, "disable lock-driven speculative prefetch (benchmark hook)")
	cmd.Flags().BoolVar(&noMetadataPrefetch, "no-metadata-prefetch", false, "disable resolver-metadata prefetch (benchmarking hook)")
	cmd.Flags().StringSliceVar(&allowPlugins, "allow-plugins", nil,
		"accepted for Composer compatibility; no-op (gomposer does not run plugins, so this flag has no effect)")
	cmd.Flags().Lookup("allow-plugins").NoOptDefVal = "*"
	return cmd
}

func parseRequirement(input string) (requirementSpec, error) {
	name, version, hasVersion := strings.Cut(strings.TrimSpace(input), ":")
	if !validRequirementName(name) {
		return requirementSpec{}, fmt.Errorf("invalid package name %q", name)
	}
	if !hasVersion {
		version = "*"
	} else if strings.TrimSpace(version) == "" {
		return requirementSpec{}, fmt.Errorf("requirement %q has an empty constraint", input)
	}
	version = strings.TrimSpace(version)
	if _, err := constraint.Parse(version); err != nil {
		return requirementSpec{}, fmt.Errorf("invalid constraint for %s: %w", name, err)
	}
	return requirementSpec{Name: name, Constraint: version}, nil
}

func validRequirementName(name string) bool {
	if composerPackageName.MatchString(name) {
		return true
	}
	if name == "php" || name == "php-64bit" || name == "hhvm" || name == "composer-plugin-api" || name == "composer-runtime-api" {
		return true
	}
	for _, prefix := range []string{"ext-", "lib-"} {
		if strings.HasPrefix(name, prefix) {
			return composerPlatformSuffix.MatchString(strings.TrimPrefix(name, prefix))
		}
	}
	return false
}

func updateManifestRequirements(data []byte, specs []requirementSpec, dev bool) ([]byte, error) {
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
	for _, spec := range specs {
		target[spec.Name] = spec.Constraint
		delete(other, spec.Name)
	}

	targetJSON, err := json.Marshal(target)
	if err != nil {
		return nil, err
	}
	doc[targetField] = targetJSON
	if len(other) == 0 {
		delete(doc, otherField)
	} else {
		otherJSON, err := json.Marshal(other)
		if err != nil {
			return nil, err
		}
		doc[otherField] = otherJSON
	}

	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("encode composer.json: %w", err)
	}
	// Validate gomposer's richer custom fields (repositories and scripts)
	// before replacing the user's file.
	if _, err := manifest.Parse(out.Bytes()); err != nil {
		return nil, fmt.Errorf("encode composer.json: %w", err)
	}
	return out.Bytes(), nil
}

func decodeRequirementMap(raw json.RawMessage, field string) (map[string]string, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return make(map[string]string), nil
	}
	var reqs map[string]string
	if err := json.Unmarshal(raw, &reqs); err != nil {
		return nil, fmt.Errorf("parse composer.json: %s must be an object of string constraints: %w", field, err)
	}
	if reqs == nil {
		reqs = make(map[string]string)
	}
	return reqs, nil
}

type fileSnapshot struct {
	data   []byte
	mode   os.FileMode
	exists bool
}

func snapshotFile(path string) (fileSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fileSnapshot{mode: 0o644}, nil
		}
		return fileSnapshot{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return fileSnapshot{}, err
	}
	return fileSnapshot{data: data, mode: info.Mode().Perm(), exists: true}, nil
}

func requireTransaction(ctx context.Context, manifestPath, lockPath string, specs []requirementSpec, dev bool, opts orchestrator.Options) error {
	manifestBefore, err := snapshotFile(manifestPath)
	if err != nil {
		return fmt.Errorf("require: read composer.json: %w", err)
	}
	if !manifestBefore.exists {
		return fmt.Errorf("require: read composer.json: %w", &os.PathError{Op: "open", Path: manifestPath, Err: os.ErrNotExist})
	}
	lockBefore, err := snapshotFile(lockPath)
	if err != nil {
		return fmt.Errorf("require: read gomposer.lock: %w", err)
	}
	updated, err := updateManifestRequirements(manifestBefore.data, specs, dev)
	if err != nil {
		return err
	}
	if err := writeAtomic(manifestPath, updated, manifestBefore.mode); err != nil {
		return fmt.Errorf("require: write composer.json: %w", err)
	}

	if err := requireFn(ctx, opts); err != nil {
		restoreErr := errors.Join(
			restoreFile(manifestPath, manifestBefore),
			restoreFile(lockPath, lockBefore),
		)
		if restoreErr != nil {
			return errors.Join(err, fmt.Errorf("require: rollback failed: %w", restoreErr))
		}
		return err
	}
	return nil
}

func restoreFile(path string, snapshot fileSnapshot) error {
	if snapshot.exists {
		return writeAtomic(path, snapshot.data, snapshot.mode)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) (retErr error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if retErr != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(mode.Perm()); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return nil
}
