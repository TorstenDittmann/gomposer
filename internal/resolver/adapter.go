package resolver

import (
	"fmt"
	"strings"

	"github.com/torstendittmann/gomposer/internal/constraint"
	"github.com/torstendittmann/gomposer/internal/lock"
	"github.com/torstendittmann/gomposer/internal/manifest"
	"github.com/torstendittmann/gomposer/internal/platform"
	"github.com/torstendittmann/gomposer/internal/registry"
)

// BuildLock converts a resolver Result plus the manifest it was resolved
// against into a fully populated Composer-shaped lock.File. It fills the
// content-hash (from manifestBytes), the _readme boilerplate, stability
// flags, platform/platform-dev requirements, minimum-stability,
// prefer-stable/prefer-lowest, plugin-api-version, and per-package
// notification-url/time.
//
// The orchestrator may still overwrite Source/Dist after fetching (e.g. to
// reflect a verified sha256 rather than the registry-advertised one).
func BuildLock(r *Result, m *manifest.Manifest, manifestBytes []byte) (*lock.File, error) {
	hash, err := manifest.ContentHash(manifestBytes)
	if err != nil {
		return nil, fmt.Errorf("resolver: build lock: %w", err)
	}

	stabilityFlags := map[string]int{}
	collectStabilityFlags(stabilityFlags, m.Require)
	collectStabilityFlags(stabilityFlags, m.RequireDev)

	platformReqs := map[string]string{}
	for name, raw := range m.Require {
		if platform.IsPlatformReq(name) {
			platformReqs[name] = raw
		}
	}
	platformDevReqs := map[string]string{}
	for name, raw := range m.RequireDev {
		if platform.IsPlatformReq(name) {
			platformDevReqs[name] = raw
		}
	}

	var prod, dev []ResolvedPackage
	if r != nil {
		prod = r.Packages
		dev = r.PackagesDev
	}
	packages := make([]lock.Package, 0, len(prod))
	for _, p := range prod {
		packages = append(packages, toLockPackage(p))
	}
	packagesDev := make([]lock.Package, 0, len(dev))
	for _, p := range dev {
		packagesDev = append(packagesDev, toLockPackage(p))
	}

	minStab := ""
	if m != nil {
		minStab = m.MinimumStability
	}
	if minStab == "" {
		minStab = "stable"
	}

	var preferStable bool
	if m != nil {
		preferStable = m.PreferStable
	}

	return &lock.File{
		Readme: []string{
			"This file locks the dependencies of your project to a known state",
			"Read more about it at https://getcomposer.org/doc/01-basic-usage.md#installing-dependencies",
			"This file is @generated automatically",
		},
		ContentHash:      hash,
		Packages:         packages,
		PackagesDev:      packagesDev,
		Aliases:          []lock.Alias{},
		MinimumStability: minStab,
		StabilityFlags:   stabilityFlags,
		PreferStable:     preferStable,
		PreferLowest:     false,
		Platform:         platformReqs,
		PlatformDev:      platformDevReqs,
		PluginAPIVersion: "2.6.0",
	}, nil
}

// collectStabilityFlags parses each raw constraint in reqs and, when it
// carries an explicit "@<stability>" suffix, records its numeric rank into
// out under the package name.
func collectStabilityFlags(out map[string]int, reqs map[string]string) {
	for name, raw := range reqs {
		c, err := constraint.Parse(raw)
		if err != nil {
			continue
		}
		if flag := c.StabilityFlag(); flag != "" {
			out[name] = stabilityRank(flag)
		}
	}
}

// stabilityRank maps a Composer stability-flag string to its numeric rank
// used in composer.lock's stability-flags map. Ranks are Composer's
// BasePackage::STABILITY_* constants.
func stabilityRank(flag string) int {
	switch strings.ToLower(flag) {
	case "dev":
		return 20
	case "alpha":
		return 15
	case "beta":
		return 10
	case "rc":
		return 5
	default:
		return 0
	}
}

// notificationURLFor picks the notification-url a locked package advertises.
// Packagist-sourced packages get "https://packagist.org/downloads/"; VCS-
// sourced packages get an empty string (Composer's convention).
func notificationURLFor(_ registry.Source, isPackagist bool) string {
	if isPackagist {
		return "https://packagist.org/downloads/"
	}
	return ""
}

func toLockPackage(p ResolvedPackage) lock.Package {
	rec := p.Record
	versionStr := rec.Version
	if versionStr == "" {
		versionStr = p.Version.Original
	}
	return lock.Package{
		Name:    p.Name,
		Version: versionStr,
		Type:    rec.Type, // forwarded for plugin detection in the orchestrator
		Source: lock.Source{
			Type:      rec.Source.Type,
			URL:       rec.Source.URL,
			Reference: rec.Source.Ref,
		},
		Dist: lock.Dist{
			Type:      rec.Dist.Type,
			URL:       rec.Dist.URL,
			Reference: rec.Source.Ref,
			Shasum:    rec.Dist.Sha,
		},
		Require:         rec.Require,
		Autoload:        autoloadToMap(rec.Autoload),
		NotificationURL: notificationURLFor(rec.Source, rec.SourceKind == "packagist"),
		Time:            rec.Time,
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
