// Workspaces — aggregate the root manifest and every workspace manifest
// into the virtual super-manifest fed to the resolver. Cross-workspace
// requires (the workspace:* / workspace:<constraint> protocol) are
// validated and stripped: workspaces are already known locally, no need
// to route them through the registry.
package orchestrator

import (
	"fmt"
	"path/filepath"

	"github.com/torstendittmann/gomposer/internal/constraint"
	"github.com/torstendittmann/gomposer/internal/lock"
	"github.com/torstendittmann/gomposer/internal/manifest"
)

// BuildAggregateManifest returns the manifest the resolver actually sees.
// It's the union of root's requires and every workspace's requires, minus
// every workspace:-prefixed entry. When two owners (root and/or workspaces)
// require the same external package with different constraint strings, the
// constraints are intersected by concatenating them with a space — Composer's
// AND syntax — rather than picking one and dropping the other. Compatible
// constraints (e.g. "^6.0" and ">=6.2") intersect cleanly at solve time;
// genuinely incompatible ones (e.g. "^6.0" and "^7.0") surface as a real
// PubGrub derivation naming both owners, instead of being silently dropped.
func BuildAggregateManifest(root *manifest.Manifest, workspaces []manifest.Workspace, includeDev bool) (*manifest.Manifest, error) {
	if root == nil {
		return nil, fmt.Errorf("workspaces: nil root manifest")
	}
	byName := map[string]manifest.Workspace{}
	for _, w := range workspaces {
		byName[w.Name] = w
	}

	agg := &manifest.Manifest{
		Name:             root.Name,
		Version:          root.Version,
		Require:          map[string]string{},
		RequireDev:       map[string]string{},
		MinimumStability: root.MinimumStability,
		PreferStable:     root.PreferStable,
		Repositories:     root.Repositories,
	}

	// mergeRequires walks a source require map and adds non-workspace entries
	// to dst. Workspace entries are validated against the target workspace's
	// version.
	mergeRequires := func(dst, src map[string]string, ownerName string) error {
		for name, raw := range src {
			c, err := constraint.Parse(raw)
			if err != nil {
				return fmt.Errorf("workspaces: %s: parse %s: %w", ownerName, name, err)
			}
			if c.IsWorkspace {
				target, ok := byName[name]
				if !ok {
					return fmt.Errorf("workspaces: %s: workspace require %q not found in workspace set", ownerName, name)
				}
				// workspace:* has no tail constraint to check.
				tailIsStar := raw == "workspace:*"
				if !tailIsStar {
					if target.Version == "" {
						return fmt.Errorf("workspaces: %s requires %s (%s) but workspace has no version field", ownerName, name, raw)
					}
					v, err := constraint.ParseVersion(target.Version)
					if err != nil {
						return fmt.Errorf("workspaces: %s: parse target version %q: %w", target.Name, target.Version, err)
					}
					if !c.Satisfies(v) {
						return fmt.Errorf("workspaces: %s requires %s (%s) but workspace has version %q", ownerName, name, raw, target.Version)
					}
				}
				continue // don't leak to aggregate
			}
			if existing, dup := dst[name]; dup {
				// Intersect: Composer's AND syntax uses whitespace between
				// clauses. Skip if the constraints are already textually
				// identical (common case) to avoid redundant "^6.0 ^6.0".
				if existing == raw {
					continue
				}
				dst[name] = existing + " " + raw
			} else {
				dst[name] = raw
			}
		}
		return nil
	}

	if err := mergeRequires(agg.Require, root.Require, root.Name); err != nil {
		return nil, err
	}
	if includeDev {
		if err := mergeRequires(agg.RequireDev, root.RequireDev, root.Name); err != nil {
			return nil, err
		}
	}
	for _, w := range workspaces {
		if err := mergeRequires(agg.Require, w.Manifest.Require, w.Name); err != nil {
			return nil, err
		}
		if includeDev {
			if err := mergeRequires(agg.RequireDev, w.Manifest.RequireDev, w.Name); err != nil {
				return nil, err
			}
		}
	}
	return agg, nil
}

// workspaceLockPackages builds the synthetic `type: workspace` lock entries
// grafted onto the production package list. Each workspace becomes a
// first-class lock entry so the autoloader wires it up and warm re-installs
// can confirm the workspace directory still exists. Deliberately excludes
// Require: the resolved dep graph already flowed through the aggregate
// manifest and is materialized as external packages elsewhere in the lock.
func workspaceLockPackages(ps *pipelineState) []lock.Package {
	if len(ps.workspaces) == 0 {
		return nil
	}
	out := make([]lock.Package, 0, len(ps.workspaces))
	for _, w := range ps.workspaces {
		url := w.Dir
		if rel, err := filepath.Rel(ps.opts.ProjectDir, w.Dir); err == nil {
			url = filepath.ToSlash(rel)
		}
		var autoload map[string]any
		if w.Manifest != nil {
			autoload = workspaceAutoloadMap(w.Manifest.Autoload)
		}
		out = append(out, lock.Package{
			Name:    w.Name,
			Version: w.Version,
			Type:    "workspace",
			Source: lock.Source{
				Type: "path",
				URL:  url,
			},
			Autoload: autoload,
		})
	}
	return out
}

// workspaceAutoloadMap converts a manifest.Autoload (typed, string-keyed)
// into the loose map[string]any shape lock.Package.Autoload uses — the same
// shape resolver.autoloadToMap produces from registry.Autoload, but sourced
// from the manifest package's own Autoload type since workspace packages
// never pass through the registry.
func workspaceAutoloadMap(a manifest.Autoload) map[string]any {
	out := map[string]any{}
	if len(a.PSR4) > 0 {
		m := make(map[string]any, len(a.PSR4))
		for k, v := range a.PSR4 {
			m[k] = v
		}
		out["psr-4"] = m
	}
	if len(a.PSR0) > 0 {
		m := make(map[string]any, len(a.PSR0))
		for k, v := range a.PSR0 {
			m[k] = v
		}
		out["psr-0"] = m
	}
	if len(a.Files) > 0 {
		out["files"] = a.Files
	}
	if len(a.Classmap) > 0 {
		out["classmap"] = a.Classmap
	}
	if len(a.ExcludeFromClassmap) > 0 {
		out["exclude-from-classmap"] = a.ExcludeFromClassmap
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
