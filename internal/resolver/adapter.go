package resolver

import (
	"github.com/torstendittmann/composer-go/internal/lock"
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
		Require: p.Record.Require,
	}
}
