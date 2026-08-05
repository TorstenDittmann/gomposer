package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/torstendittmann/gomposer/internal/auth"
	"github.com/torstendittmann/gomposer/internal/cache"
	"github.com/torstendittmann/gomposer/internal/constraint"
	"github.com/torstendittmann/gomposer/internal/lock"
	"github.com/torstendittmann/gomposer/internal/manifest"
	"github.com/torstendittmann/gomposer/internal/registry"
	"github.com/torstendittmann/gomposer/internal/registry/multisource"
	"github.com/torstendittmann/gomposer/internal/registry/packagist"
	"github.com/torstendittmann/gomposer/internal/registry/vcs"
)

type outdatedPackage struct {
	Name        string   `json:"name"`
	Current     string   `json:"current"`
	Wanted      string   `json:"wanted"`
	Latest      string   `json:"latest"`
	Status      string   `json:"status"`
	Direct      bool     `json:"direct"`
	Dev         bool     `json:"dev"`
	Constraints []string `json:"constraints"`
}

type lockedOutdatedPackage struct {
	Package lock.Package
	Direct  bool
	Dev     bool
}

var (
	errOutdatedFound = errors.New("outdated packages found")
	outdatedSourceFn = defaultOutdatedSource
)

func newOutdatedCmd() *cobra.Command {
	var projectDir, format string
	var direct, strict bool
	cmd := &cobra.Command{
		Use:   "outdated [package]",
		Short: "Show locked packages with newer published versions",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "text" && format != "json" {
				return fmt.Errorf("invalid --format value %q (want text or json)", format)
			}
			if len(args) == 1 && direct {
				return errors.New("outdated: a package name cannot be combined with --direct")
			}
			selectedDir, lockDir, selected, file, err := loadInspectionProject(projectDir, "outdated")
			if err != nil {
				return err
			}
			_ = selectedDir
			packages, incoming, err := outdatedInputs(selected, file, flagNoDev)
			if err != nil {
				return err
			}
			if len(args) == 1 {
				name := args[0]
				pkg, ok := packages[name]
				if !ok {
					return fmt.Errorf("outdated: package %q is not present in gomposer.lock", name)
				}
				if pkg.Package.Type == "workspace" {
					return fmt.Errorf("outdated: package %q is a local workspace package", name)
				}
				packages = map[string]lockedOutdatedPackage{name: pkg}
			}
			if direct {
				for name, pkg := range packages {
					if !pkg.Direct {
						delete(packages, name)
					}
				}
			}
			rootManifest, err := manifest.Load(lockDir)
			if err != nil {
				return err
			}
			workspaces, err := manifest.DiscoverWorkspaces(lockDir, rootManifest, nil)
			if err != nil {
				return fmt.Errorf("outdated: %w", err)
			}
			addOutdatedManifestConstraints(incoming, packages, rootManifest, flagNoDev)
			for _, workspace := range workspaces {
				addOutdatedManifestConstraints(incoming, packages, workspace.Manifest, flagNoDev)
			}
			for name := range incoming {
				sort.Strings(incoming[name])
				incoming[name] = uniqueStrings(incoming[name])
			}
			source, err := outdatedSourceFn(rootManifest)
			if err != nil {
				return fmt.Errorf("outdated: registry: %w", err)
			}
			rows, err := findOutdated(cmd.Context(), source, packages, incoming, file.Stability.MinimumStability)
			if err != nil {
				return err
			}
			if !flagQuiet {
				if format == "json" {
					err = writeShowJSON(cmd.OutOrStdout(), struct {
						Packages []outdatedPackage `json:"packages"`
					}{Packages: rows})
				} else if len(rows) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "All locked packages are up to date.")
				} else {
					writeOutdated(cmd.OutOrStdout(), rows)
				}
				if err != nil {
					return err
				}
			}
			if strict && len(rows) > 0 {
				return markHandled(errOutdatedFound)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&projectDir, "project", "", "project directory containing composer.json (defaults to cwd)")
	cmd.Flags().BoolVarP(&direct, "direct", "D", false, "check only dependencies declared by the selected manifest")
	cmd.Flags().StringVarP(&format, "format", "f", "text", "output format: text or json")
	cmd.Flags().BoolVar(&strict, "strict", false, "return a non-zero status when outdated packages are found")
	return cmd
}

func loadInspectionProject(projectDir, command string) (selectedDir, lockDir string, selected *manifest.Manifest, file *lock.File, err error) {
	selectedDir = projectDir
	if selectedDir == "" {
		selectedDir, err = os.Getwd()
		if err != nil {
			return
		}
	}
	selectedDir, err = filepath.Abs(selectedDir)
	if err != nil {
		return
	}
	lockDir = selectedDir
	if root, ok := findWorkspaceRoot(selectedDir); ok {
		lockDir = root
	}
	selected, err = manifest.Load(selectedDir)
	if err != nil {
		return
	}
	lockPath := filepath.Join(lockDir, "gomposer.lock")
	body, readErr := os.ReadFile(lockPath)
	if readErr != nil {
		err = fmt.Errorf("%s: read %s: %w", command, lockPath, readErr)
		return
	}
	file, err = lock.Decode(body)
	if err != nil {
		err = fmt.Errorf("%s: %w", command, err)
	}
	return
}

func outdatedInputs(selected *manifest.Manifest, file *lock.File, noDev bool) (map[string]lockedOutdatedPackage, map[string][]string, error) {
	direct := make(map[string]bool)
	for name := range selected.Require {
		direct[name] = true
	}
	if !noDev {
		for name := range selected.RequireDev {
			direct[name] = true
		}
	}
	packages := make(map[string]lockedOutdatedPackage)
	add := func(pkg lock.Package, dev bool) error {
		if old, exists := packages[pkg.Name]; exists && old.Package.Version != pkg.Version {
			return fmt.Errorf("outdated: package %q is locked at multiple versions", pkg.Name)
		}
		packages[pkg.Name] = lockedOutdatedPackage{Package: pkg, Direct: direct[pkg.Name], Dev: dev}
		return nil
	}
	for _, pkg := range file.Packages {
		if err := add(pkg, false); err != nil {
			return nil, nil, err
		}
	}
	if !noDev {
		for _, pkg := range file.PackagesDev {
			if err := add(pkg, true); err != nil {
				return nil, nil, err
			}
		}
	}
	incoming := make(map[string][]string)
	addRequirements := func(requirements map[string]string) {
		for name, raw := range requirements {
			if _, exists := packages[name]; exists {
				incoming[name] = append(incoming[name], raw)
			}
		}
	}
	addRequirements(selected.Require)
	if !noDev {
		addRequirements(selected.RequireDev)
	}
	for _, pkg := range packages {
		addRequirements(pkg.Package.Require)
	}
	for name := range incoming {
		sort.Strings(incoming[name])
		incoming[name] = uniqueStrings(incoming[name])
	}
	return packages, incoming, nil
}

func addOutdatedManifestConstraints(incoming map[string][]string, packages map[string]lockedOutdatedPackage, m *manifest.Manifest, noDev bool) {
	add := func(requirements map[string]string) {
		for name, raw := range requirements {
			if _, exists := packages[name]; exists {
				incoming[name] = append(incoming[name], raw)
			}
		}
	}
	add(m.Require)
	if !noDev {
		add(m.RequireDev)
	}
}

func findOutdated(ctx context.Context, source registry.SourceLookup, packages map[string]lockedOutdatedPackage, incoming map[string][]string, minimumStability string) ([]outdatedPackage, error) {
	names := make([]string, 0, len(packages))
	for name, pkg := range packages {
		if pkg.Package.Type != "workspace" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	rows := make([]*outdatedPackage, len(names))
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for i, name := range names {
		i, name := i, name
		g.Go(func() error {
			metadata, err := source.Lookup(ctx, name)
			if err != nil {
				return fmt.Errorf("outdated: lookup %s: %w", name, err)
			}
			row, err := compareOutdated(packages[name], metadata, incoming[name], minimumStability)
			if err != nil {
				return fmt.Errorf("outdated: %s: %w", name, err)
			}
			rows[i] = row
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	out := make([]outdatedPackage, 0, len(rows))
	for _, row := range rows {
		if row != nil {
			out = append(out, *row)
		}
	}
	return out, nil
}

func compareOutdated(locked lockedOutdatedPackage, metadata *registry.PackageMetadata, rawConstraints []string, minimumStability string) (*outdatedPackage, error) {
	current, err := constraint.ParseVersion(locked.Package.Version)
	if err != nil {
		return nil, fmt.Errorf("invalid locked version %q: %w", locked.Package.Version, err)
	}
	constraints := make([]constraint.Constraint, 0, len(rawConstraints))
	for _, raw := range rawConstraints {
		parsed, err := constraint.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid constraint %q: %w", raw, err)
		}
		if !parsed.IsWorkspace {
			constraints = append(constraints, parsed)
		}
	}
	type candidate struct {
		raw    string
		parsed constraint.Version
	}
	var candidates []candidate
	floor := stabilityFloor(minimumStability)
	for _, record := range metadata.Versions {
		parsed, err := constraint.ParseVersion(record.Version)
		if err != nil || parsed.Stability < floor {
			continue
		}
		candidates = append(candidates, candidate{raw: record.Version, parsed: parsed})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if cmp := candidates[i].parsed.Compare(candidates[j].parsed); cmp != 0 {
			return cmp > 0
		}
		return candidates[i].raw > candidates[j].raw
	})
	if len(candidates) == 0 || candidates[0].parsed.Compare(current) <= 0 {
		return nil, nil
	}
	latest := candidates[0]
	wanted := candidate{raw: locked.Package.Version, parsed: current}
	for _, candidate := range candidates {
		matches := true
		for _, required := range constraints {
			if !required.Satisfies(candidate.parsed) {
				matches = false
				break
			}
		}
		if matches {
			wanted = candidate
			break
		}
	}
	status := "update available"
	if wanted.parsed.Compare(latest.parsed) < 0 {
		status = "constrained"
	}
	return &outdatedPackage{Name: locked.Package.Name, Current: locked.Package.Version, Wanted: wanted.raw, Latest: latest.raw, Status: status, Direct: locked.Direct, Dev: locked.Dev, Constraints: append([]string{}, rawConstraints...)}, nil
}

func stabilityFloor(raw string) constraint.Stability {
	switch strings.ToLower(raw) {
	case "dev":
		return constraint.Dev
	case "alpha":
		return constraint.Alpha
	case "beta":
		return constraint.Beta
	case "rc":
		return constraint.RC
	default:
		return constraint.Stable
	}
}

func defaultOutdatedSource(m *manifest.Manifest) (registry.SourceLookup, error) {
	metadataDir, err := cache.LayerMetadata.Path()
	if err != nil {
		return nil, err
	}
	vcsDir, err := cache.LayerVCS.Path()
	if err != nil {
		return nil, err
	}
	vcsClients, err := vcs.NewFromManifest(m.Repositories, vcs.Options{CacheRoot: vcsDir})
	if err != nil {
		return nil, err
	}
	authStore, _ := auth.Load()
	client, err := packagist.New(packagist.Config{CacheDir: metadataDir, Auth: authStore})
	if err != nil {
		return nil, err
	}
	lookups := make([]registry.SourceLookup, 0, len(vcsClients)+1)
	for _, client := range vcsClients {
		lookups = append(lookups, client)
	}
	lookups = append(lookups, client)
	return multisource.NewWithLookups(lookups), nil
}

func writeOutdated(w io.Writer, rows []outdatedPackage) {
	nameWidth, currentWidth, wantedWidth, latestWidth := 0, 0, 0, 0
	for _, row := range rows {
		nameWidth = max(nameWidth, len(row.Name))
		currentWidth = max(currentWidth, len(row.Current))
		wantedWidth = max(wantedWidth, len(row.Wanted))
		latestWidth = max(latestWidth, len(row.Latest))
	}
	for _, row := range rows {
		labels := row.Status
		if row.Direct {
			labels += ", direct"
		}
		if row.Dev {
			labels += ", dev"
		}
		fmt.Fprintf(w, "%-*s  %-*s  %-*s  %-*s  %s\n", nameWidth, row.Name, currentWidth, row.Current, wantedWidth, row.Wanted, latestWidth, row.Latest, labels)
	}
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}
