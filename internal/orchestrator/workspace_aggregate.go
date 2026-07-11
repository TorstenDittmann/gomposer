// Workspaces — aggregate the root manifest and every workspace manifest
// into the virtual super-manifest fed to the resolver. Cross-workspace
// requires (the workspace:* / workspace:<constraint> protocol) are
// validated and stripped: workspaces are already known locally, no need
// to route them through the registry.
package orchestrator

import (
	"fmt"

	"github.com/torstendittmann/gomposer/internal/constraint"
	"github.com/torstendittmann/gomposer/internal/manifest"
)

// BuildAggregateManifest returns the manifest the resolver actually sees.
// It's the union of root's requires and every workspace's requires, minus
// every workspace:-prefixed entry. Duplicate external requires with
// compatible constraints are collapsed to the first-seen; conflicts are
// left to the resolver (its PubGrub derivation names the specific
// packages, which is more useful than an aggregate-time error).
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
			if _, dup := dst[name]; !dup {
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
