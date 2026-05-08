package resolver

import (
	"github.com/torstendittmann/composer-go/internal/lock"
	"github.com/torstendittmann/composer-go/internal/registry"
)

// ToLockPackages converts a resolver Result into the slices the orchestrator
// writes into lock.File. The output preserves Source/Dist from the registry
// record AS-IS — the orchestrator may overwrite them after fetching (e.g.,
// to reflect verified sha256 rather than the registry-advertised one).
//
// Suggest is intentionally NOT copied here in stage 1; orchestrator can add
// it later if needed for `composer-go suggest` (post-MVP).
func ToLockPackages(r *Result) (prod, dev []lock.Package) {
	if r == nil {
		return nil, nil
	}
	prod = make([]lock.Package, 0, len(r.Packages))
	for _, p := range r.Packages {
		prod = append(prod, toLockPackage(p))
	}
	dev = make([]lock.Package, 0, len(r.PackagesDev))
	for _, p := range r.PackagesDev {
		dev = append(dev, toLockPackage(p))
	}
	return prod, dev
}

func toLockPackage(p ResolvedPackage) lock.Package {
	versionStr := p.Record.Version
	if versionStr == "" {
		versionStr = p.Version.Original
	}
	return lock.Package{
		Name:    p.Name,
		Version: versionStr,
		Type:    p.Record.Type, // forwarded for plugin detection in the orchestrator
		Source: lock.Source{
			Type: p.Record.Source.Type,
			URL:  p.Record.Source.URL,
			Ref:  p.Record.Source.Ref,
		},
		Dist: lock.Dist{
			Type:   p.Record.Dist.Type,
			URL:    p.Record.Dist.URL,
			Sha256: p.Record.Dist.Sha,
		},
		Require:  p.Record.Require,
		Autoload: autoloadToMap(p.Record.Autoload),
	}
}

// autoloadToMap converts a registry.Autoload into the loose map[string]any
// shape used by lock.Package, preserving only non-empty fields so the
// lockfile diff stays tight.
func autoloadToMap(a registry.Autoload) map[string]any {
	out := map[string]any{}
	if len(a.PSR4) > 0 {
		out["psr-4"] = a.PSR4
	}
	if len(a.PSR0) > 0 {
		out["psr-0"] = a.PSR0
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
