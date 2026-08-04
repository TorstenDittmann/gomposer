package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/torstendittmann/gomposer/internal/lock"
	"github.com/torstendittmann/gomposer/internal/manifest"
)

type showPackage struct {
	Name      string            `json:"name"`
	Version   string            `json:"version"`
	Type      string            `json:"type,omitempty"`
	Dev       bool              `json:"dev"`
	Direct    bool              `json:"direct"`
	Workspace bool              `json:"workspace"`
	Source    lock.Source       `json:"source"`
	Dist      lock.Dist         `json:"dist"`
	Requires  map[string]string `json:"requires,omitempty"`
}

type showTreeNode struct {
	Name         string         `json:"name"`
	Version      string         `json:"version"`
	Constraint   string         `json:"constraint,omitempty"`
	Dev          bool           `json:"dev"`
	Workspace    bool           `json:"workspace"`
	Cycle        bool           `json:"cycle,omitempty"`
	Dependencies []showTreeNode `json:"dependencies,omitempty"`
}

func newShowCmd() *cobra.Command {
	var (
		projectDir string
		direct     bool
		tree       bool
		format     string
	)
	cmd := &cobra.Command{
		Use:   "show [package]",
		Short: "Inspect packages recorded in gomposer.lock",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "text" && format != "json" {
				return fmt.Errorf("invalid --format value %q (want text or json)", format)
			}
			if len(args) == 1 && (direct || tree) {
				return errors.New("show: a package name cannot be combined with --direct or --tree")
			}
			selectedDir := projectDir
			if selectedDir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				selectedDir = wd
			}
			selectedDir, err := filepath.Abs(selectedDir)
			if err != nil {
				return err
			}
			lockDir := selectedDir
			if root, ok := findWorkspaceRoot(selectedDir); ok {
				lockDir = root
			}
			packages, byName, err := loadShowPackages(selectedDir, lockDir, flagNoDev)
			if err != nil {
				return err
			}
			if len(args) == 1 {
				pkg, ok := byName[args[0]]
				if !ok {
					return fmt.Errorf("show: package %q is not present in gomposer.lock", args[0])
				}
				if flagQuiet {
					return nil
				}
				if format == "json" {
					return writeShowJSON(cmd.OutOrStdout(), pkg)
				}
				writeShowDetail(cmd.OutOrStdout(), pkg)
				return nil
			}
			if direct {
				packages = filterDirectPackages(packages)
			}
			if flagQuiet {
				return nil
			}
			if tree {
				nodes := buildShowTree(packages, byName)
				if format == "json" {
					return writeShowJSON(cmd.OutOrStdout(), struct {
						Tree []showTreeNode `json:"tree"`
					}{Tree: nodes})
				}
				writeShowTree(cmd.OutOrStdout(), nodes)
				return nil
			}
			if format == "json" {
				return writeShowJSON(cmd.OutOrStdout(), struct {
					Packages []showPackage `json:"packages"`
				}{Packages: packages})
			}
			writeShowList(cmd.OutOrStdout(), packages)
			return nil
		},
	}
	cmd.Flags().StringVar(&projectDir, "project", "", "project directory containing composer.json (defaults to cwd)")
	cmd.Flags().BoolVarP(&direct, "direct", "D", false, "show only dependencies declared by the selected manifest")
	cmd.Flags().BoolVarP(&tree, "tree", "t", false, "render the resolved dependency tree")
	cmd.Flags().StringVarP(&format, "format", "f", "text", "output format: text or json")
	return cmd
}

func loadShowPackages(selectedDir, lockDir string, noDev bool) ([]showPackage, map[string]showPackage, error) {
	m, err := manifest.Load(selectedDir)
	if err != nil {
		return nil, nil, err
	}
	lockPath := filepath.Join(lockDir, "gomposer.lock")
	body, err := os.ReadFile(lockPath)
	if err != nil {
		return nil, nil, fmt.Errorf("show: read %s: %w", lockPath, err)
	}
	file, err := lock.Decode(body)
	if err != nil {
		return nil, nil, fmt.Errorf("show: %w", err)
	}
	direct := make(map[string]bool, len(m.Require)+len(m.RequireDev))
	for name := range m.Require {
		direct[name] = true
	}
	if !noDev {
		for name := range m.RequireDev {
			direct[name] = true
		}
	}

	packages := make([]showPackage, 0, len(file.Packages)+len(file.PackagesDev))
	add := func(pkg lock.Package, dev bool) {
		packages = append(packages, showPackage{
			Name: pkg.Name, Version: pkg.Version, Type: pkg.Type,
			Dev: dev, Direct: direct[pkg.Name], Workspace: pkg.Type == "workspace",
			Source: pkg.Source, Dist: pkg.Dist, Requires: pkg.Require,
		})
	}
	for _, pkg := range file.Packages {
		add(pkg, false)
	}
	if !noDev {
		for _, pkg := range file.PackagesDev {
			add(pkg, true)
		}
	}
	sort.Slice(packages, func(i, j int) bool { return packages[i].Name < packages[j].Name })
	byName := make(map[string]showPackage, len(packages))
	for _, pkg := range packages {
		byName[pkg.Name] = pkg
	}
	return packages, byName, nil
}

func filterDirectPackages(packages []showPackage) []showPackage {
	out := make([]showPackage, 0, len(packages))
	for _, pkg := range packages {
		if pkg.Direct {
			out = append(out, pkg)
		}
	}
	return out
}

func writeShowList(w io.Writer, packages []showPackage) {
	nameWidth, versionWidth := len("package"), len("version")
	for _, pkg := range packages {
		if len(pkg.Name) > nameWidth {
			nameWidth = len(pkg.Name)
		}
		if len(pkg.Version) > versionWidth {
			versionWidth = len(pkg.Version)
		}
	}
	for _, pkg := range packages {
		fmt.Fprintf(w, "%-*s  %-*s  %s\n", nameWidth, pkg.Name, versionWidth, pkg.Version, strings.Join(showLabels(pkg), ", "))
	}
}

func showLabels(pkg showPackage) []string {
	labels := make([]string, 0, 3)
	if pkg.Direct {
		labels = append(labels, "direct")
	}
	if pkg.Dev {
		labels = append(labels, "dev")
	}
	if pkg.Workspace {
		labels = append(labels, "workspace")
	}
	return labels
}

func writeShowDetail(w io.Writer, pkg showPackage) {
	writeDetailField(w, "name", pkg.Name)
	writeDetailField(w, "version", pkg.Version)
	writeDetailField(w, "type", valueOrDash(pkg.Type))
	scope := "production"
	if pkg.Dev {
		scope = "development"
	}
	writeDetailField(w, "scope", scope)
	if pkg.Direct {
		writeDetailField(w, "direct", "yes")
	} else {
		writeDetailField(w, "direct", "no")
	}
	if pkg.Workspace {
		writeDetailField(w, "workspace", "yes")
	}
	if pkg.Source.Type != "" || pkg.Source.URL != "" || pkg.Source.Ref != "" {
		writeDetailField(w, "source", joinNonEmpty(pkg.Source.Type, pkg.Source.URL, pkg.Source.Ref))
	}
	if pkg.Dist.Type != "" || pkg.Dist.URL != "" || pkg.Dist.Sha256 != "" {
		writeDetailField(w, "dist", joinNonEmpty(pkg.Dist.Type, pkg.Dist.URL, pkg.Dist.Sha256))
	}
	if len(pkg.Requires) > 0 {
		fmt.Fprintln(w, "requires")
		for _, name := range sortedMapKeys(pkg.Requires) {
			fmt.Fprintf(w, "  %-28s %s\n", name, pkg.Requires[name])
		}
	}
}

func writeDetailField(w io.Writer, name, value string) {
	fmt.Fprintf(w, "%-11s %s\n", name, value)
}

func valueOrDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func joinNonEmpty(values ...string) string {
	out := values[:0]
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return strings.Join(out, " ")
}

func sortedMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func buildShowTree(packages []showPackage, byName map[string]showPackage) []showTreeNode {
	roots := filterDirectPackages(packages)
	if len(roots) == 0 {
		incoming := make(map[string]bool)
		for _, pkg := range packages {
			for name := range pkg.Requires {
				if _, ok := byName[name]; ok {
					incoming[name] = true
				}
			}
		}
		for _, pkg := range packages {
			if !incoming[pkg.Name] {
				roots = append(roots, pkg)
			}
		}
		if len(roots) == 0 {
			roots = append(roots, packages...)
		}
	}
	nodes := make([]showTreeNode, 0, len(roots))
	for _, root := range roots {
		nodes = append(nodes, buildShowTreeNode(root, "", byName, nil))
	}
	return nodes
}

func buildShowTreeNode(pkg showPackage, requested string, byName map[string]showPackage, ancestors map[string]bool) showTreeNode {
	node := showTreeNode{Name: pkg.Name, Version: pkg.Version, Constraint: requested, Dev: pkg.Dev, Workspace: pkg.Workspace}
	if ancestors[pkg.Name] {
		node.Cycle = true
		return node
	}
	path := make(map[string]bool, len(ancestors)+1)
	for name := range ancestors {
		path[name] = true
	}
	path[pkg.Name] = true
	for _, name := range sortedMapKeys(pkg.Requires) {
		dependency, ok := byName[name]
		if !ok {
			continue
		}
		node.Dependencies = append(node.Dependencies, buildShowTreeNode(dependency, pkg.Requires[name], byName, path))
	}
	return node
}

func writeShowTree(w io.Writer, nodes []showTreeNode) {
	for _, node := range nodes {
		fmt.Fprintln(w, formatShowTreeNode(node))
		writeShowTreeChildren(w, node.Dependencies, "")
	}
}

func writeShowTreeChildren(w io.Writer, nodes []showTreeNode, prefix string) {
	for i, node := range nodes {
		last := i == len(nodes)-1
		branch, childPrefix := "├── ", prefix+"│   "
		if last {
			branch, childPrefix = "└── ", prefix+"    "
		}
		fmt.Fprintln(w, prefix+branch+formatShowTreeNode(node))
		writeShowTreeChildren(w, node.Dependencies, childPrefix)
	}
}

func formatShowTreeNode(node showTreeNode) string {
	label := node.Name + " " + node.Version
	if node.Constraint != "" {
		label += " (" + node.Constraint + ")"
	}
	labels := make([]string, 0, 2)
	if node.Dev {
		labels = append(labels, "dev")
	}
	if node.Workspace {
		labels = append(labels, "workspace")
	}
	if len(labels) > 0 {
		label += " [" + strings.Join(labels, ", ") + "]"
	}
	if node.Cycle {
		label += " [cycle]"
	}
	return label
}

func writeShowJSON(w io.Writer, value any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}
