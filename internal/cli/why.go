package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/torstendittmann/gomposer/internal/lock"
	"github.com/torstendittmann/gomposer/internal/manifest"
)

type whyNode struct {
	Name      string `json:"name"`
	Version   string `json:"version,omitempty"`
	Dev       bool   `json:"dev"`
	Root      bool   `json:"root"`
	Workspace bool   `json:"workspace"`
}

type whyEdge struct {
	Name       string `json:"name"`
	Version    string `json:"version,omitempty"`
	Constraint string `json:"constraint"`
	Dev        bool   `json:"dev"`
	Root       bool   `json:"root"`
	Workspace  bool   `json:"workspace"`
	Target     string `json:"-"`
}

type whyTreeNode struct {
	Name       string        `json:"name"`
	Version    string        `json:"version,omitempty"`
	Constraint string        `json:"constraint,omitempty"`
	Dev        bool          `json:"dev"`
	Root       bool          `json:"root"`
	Workspace  bool          `json:"workspace"`
	Cycle      bool          `json:"cycle,omitempty"`
	RequiredBy []whyTreeNode `json:"requiredBy,omitempty"`
}

type whyGraph struct {
	nodes   map[string]whyNode
	reverse map[string][]whyEdge
}

func newWhyCmd() *cobra.Command {
	var (
		projectDir string
		recursive  bool
		tree       bool
		format     string
	)
	cmd := &cobra.Command{
		Use:     "why <package>",
		Aliases: []string{"depends"},
		Short:   "Explain why a package is installed",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "text" && format != "json" {
				return fmt.Errorf("invalid --format value %q (want text or json)", format)
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
			graph, err := loadWhyGraph(selectedDir, lockDir, flagNoDev)
			if err != nil {
				return err
			}
			target := args[0]
			if _, known := graph.nodes[target]; !known && len(graph.reverse[target]) == 0 {
				return fmt.Errorf("why: package %q is not present in gomposer.lock or the selected dependency graph", target)
			}
			if flagQuiet {
				return nil
			}
			if tree {
				node := buildWhyTree(target, graph, nil)
				if format == "json" {
					return writeShowJSON(cmd.OutOrStdout(), struct {
						Tree whyTreeNode `json:"tree"`
					}{Tree: node})
				}
				writeWhyTree(cmd.OutOrStdout(), node)
				return nil
			}
			edges := graph.reverse[target]
			if recursive {
				edges = recursiveWhy(target, graph)
			}
			if format == "json" {
				return writeShowJSON(cmd.OutOrStdout(), struct {
					Package    string    `json:"package"`
					Dependents []whyEdge `json:"dependents"`
				}{Package: target, Dependents: edges})
			}
			if len(edges) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "Package %q is not required by any package in the selected project.\n", target)
				return nil
			}
			writeWhyList(cmd.OutOrStdout(), edges)
			return nil
		},
	}
	cmd.Flags().StringVar(&projectDir, "project", "", "project directory containing composer.json (defaults to cwd)")
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "show all transitive dependents")
	cmd.Flags().BoolVarP(&tree, "tree", "t", false, "render reverse dependency paths as a tree")
	cmd.Flags().StringVarP(&format, "format", "f", "text", "output format: text or json")
	return cmd
}

func loadWhyGraph(selectedDir, lockDir string, noDev bool) (*whyGraph, error) {
	selected, err := manifest.Load(selectedDir)
	if err != nil {
		return nil, err
	}
	body, err := os.ReadFile(filepath.Join(lockDir, "gomposer.lock"))
	if err != nil {
		return nil, fmt.Errorf("why: read %s: %w", filepath.Join(lockDir, "gomposer.lock"), err)
	}
	file, err := lock.Decode(body)
	if err != nil {
		return nil, fmt.Errorf("why: %w", err)
	}

	graph := &whyGraph{nodes: make(map[string]whyNode), reverse: make(map[string][]whyEdge)}
	packages := append([]lock.Package(nil), file.Packages...)
	devNames := make(map[string]bool)
	if !noDev {
		packages = append(packages, file.PackagesDev...)
		for _, pkg := range file.PackagesDev {
			devNames[pkg.Name] = true
		}
	}
	for _, pkg := range packages {
		graph.nodes[pkg.Name] = whyNode{Name: pkg.Name, Version: pkg.Version, Dev: devNames[pkg.Name], Workspace: pkg.Type == "workspace"}
	}

	rootManifest, err := manifest.Load(lockDir)
	if err != nil {
		return nil, err
	}
	workspaces, err := manifest.DiscoverWorkspaces(lockDir, rootManifest, nil)
	if err != nil {
		return nil, fmt.Errorf("why: %w", err)
	}
	workspaceManifests := make(map[string]*manifest.Manifest, len(workspaces))
	for _, workspace := range workspaces {
		workspaceManifests[workspace.Name] = workspace.Manifest
	}

	addManifestNode := func(m *manifest.Manifest, root, workspace bool) {
		name := m.Name
		if name == "" {
			name = "__root__"
		}
		node := graph.nodes[name]
		node.Name, node.Root, node.Workspace = name, root, workspace
		if node.Version == "" {
			node.Version = m.Version
		}
		graph.nodes[name] = node
		addWhyRequirements(graph, node, m.Require, false)
		if !noDev {
			addWhyRequirements(graph, node, m.RequireDev, true)
		}
	}

	isWorkspaceMember := filepath.Clean(selectedDir) != filepath.Clean(lockDir)
	if isWorkspaceMember {
		addManifestNode(selected, true, true)
		for name, m := range workspaceManifests {
			if name != selected.Name {
				addManifestNode(m, false, true)
			}
		}
	} else {
		addManifestNode(rootManifest, true, false)
		for _, workspace := range workspaces {
			addManifestNode(workspace.Manifest, false, true)
		}
	}

	for _, pkg := range packages {
		node := graph.nodes[pkg.Name]
		// Workspace manifests are authoritative because synthetic workspace
		// lock records intentionally omit their requirements.
		if _, workspace := workspaceManifests[pkg.Name]; workspace {
			continue
		}
		addWhyRequirements(graph, node, pkg.Require, false)
	}

	if isWorkspaceMember {
		restrictWhyGraph(graph, selected.Name)
	}
	for target := range graph.reverse {
		sortWhyEdges(graph.reverse[target])
	}
	return graph, nil
}

func addWhyRequirements(graph *whyGraph, from whyNode, requirements map[string]string, dev bool) {
	for _, target := range sortedMapKeys(requirements) {
		graph.reverse[target] = append(graph.reverse[target], whyEdge{
			Name: from.Name, Version: from.Version, Constraint: requirements[target], Dev: dev || from.Dev,
			Root: from.Root, Workspace: from.Workspace, Target: target,
		})
	}
}

func restrictWhyGraph(graph *whyGraph, root string) {
	reachable := map[string]bool{root: true}
	for changed := true; changed; {
		changed = false
		for target, edges := range graph.reverse {
			for _, edge := range edges {
				if reachable[edge.Name] && !reachable[target] {
					reachable[target], changed = true, true
				}
			}
		}
	}
	for target, edges := range graph.reverse {
		filtered := edges[:0]
		for _, edge := range edges {
			if reachable[edge.Name] {
				filtered = append(filtered, edge)
			}
		}
		if len(filtered) == 0 {
			delete(graph.reverse, target)
		} else {
			graph.reverse[target] = filtered
		}
	}
	for name := range graph.nodes {
		if !reachable[name] {
			delete(graph.nodes, name)
		}
	}
}

func recursiveWhy(target string, graph *whyGraph) []whyEdge {
	queue := []string{target}
	seenTargets := map[string]bool{target: true}
	seenEdges := make(map[string]bool)
	var out []whyEdge
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, edge := range graph.reverse[current] {
			key := edge.Name + "\x00" + edge.Target + "\x00" + edge.Constraint
			if !seenEdges[key] {
				seenEdges[key] = true
				out = append(out, edge)
			}
			if !seenTargets[edge.Name] {
				seenTargets[edge.Name] = true
				queue = append(queue, edge.Name)
			}
		}
	}
	sortWhyEdges(out)
	return out
}

func sortWhyEdges(edges []whyEdge) {
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Name != edges[j].Name {
			return edges[i].Name < edges[j].Name
		}
		if edges[i].Target != edges[j].Target {
			return edges[i].Target < edges[j].Target
		}
		return edges[i].Constraint < edges[j].Constraint
	})
}

func buildWhyTree(target string, graph *whyGraph, ancestors map[string]bool) whyTreeNode {
	nodeInfo := graph.nodes[target]
	node := whyTreeNode{Name: target, Version: nodeInfo.Version, Dev: nodeInfo.Dev, Root: nodeInfo.Root, Workspace: nodeInfo.Workspace}
	if ancestors[target] {
		node.Cycle = true
		return node
	}
	path := make(map[string]bool, len(ancestors)+1)
	for name := range ancestors {
		path[name] = true
	}
	path[target] = true
	for _, edge := range graph.reverse[target] {
		child := buildWhyTree(edge.Name, graph, path)
		child.Constraint = edge.Constraint
		child.Dev = edge.Dev || child.Dev
		child.Root = edge.Root || child.Root
		child.Workspace = edge.Workspace || child.Workspace
		node.RequiredBy = append(node.RequiredBy, child)
	}
	return node
}

func writeWhyList(w io.Writer, edges []whyEdge) {
	nameWidth, versionWidth := 0, 1
	for _, edge := range edges {
		if len(edge.Name) > nameWidth {
			nameWidth = len(edge.Name)
		}
		if len(valueOrDash(edge.Version)) > versionWidth {
			versionWidth = len(valueOrDash(edge.Version))
		}
	}
	for _, edge := range edges {
		dev := ""
		if edge.Dev {
			dev = " [dev]"
		}
		fmt.Fprintf(w, "%-*s  %-*s  requires  %s  (%s)%s\n", nameWidth, edge.Name, versionWidth, valueOrDash(edge.Version), edge.Target, edge.Constraint, dev)
	}
}

func writeWhyTree(w io.Writer, node whyTreeNode) {
	fmt.Fprintln(w, formatWhyTreeNode(node, false))
	writeWhyTreeChildren(w, node.RequiredBy, "")
}

func writeWhyTreeChildren(w io.Writer, nodes []whyTreeNode, prefix string) {
	for i, node := range nodes {
		last := i == len(nodes)-1
		branch, childPrefix := "├── ", prefix+"│   "
		if last {
			branch, childPrefix = "└── ", prefix+"    "
		}
		fmt.Fprintln(w, prefix+branch+formatWhyTreeNode(node, true))
		writeWhyTreeChildren(w, node.RequiredBy, childPrefix)
	}
}

func formatWhyTreeNode(node whyTreeNode, dependent bool) string {
	label := node.Name
	if node.Version != "" {
		label += " " + node.Version
	}
	if dependent {
		label += " requires " + node.Constraint
	}
	if node.Dev {
		label += " [dev]"
	}
	if node.Cycle {
		label += " [cycle]"
	}
	return label
}
