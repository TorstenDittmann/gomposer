// Package registry abstracts package-metadata sources. The resolver depends
// only on the Source interface; concrete sources (packagist, vcs) live in
// sub-packages.
package registry

import (
	"context"
)

// PackageMetadata is the metadata about a single package, across all
// versions known to the source.
type PackageMetadata struct {
	Name     string
	Versions []PackageVersion
}

// PackageVersion is the metadata for one published version.
type PackageVersion struct {
	Name        string
	Version     string            // raw version string as published, e.g. "3.5.0" or "dev-main"
	VersionNorm string            // normalized form, used for stable comparison
	Time        string            // RFC3339 timestamp from Packagist v2 "time" field.
	Source      Source            // git source ref
	Dist        Dist              // download artifact (zip)
	Require     map[string]string // production deps (raw constraint strings)
	RequireDev  map[string]string
	Autoload    Autoload
	AutoloadDev Autoload
	Suggest     map[string]string
	// Type is the package type ("library", "composer-plugin", etc.).
	// composer-plugin packages must be detected and skipped by the orchestrator.
	Type string
}

type Source struct {
	Type string // typically "git"
	URL  string
	Ref  string // commit sha or tag
}

type Dist struct {
	Type string // "zip"
	URL  string
	Sha  string // sha256 if available; empty otherwise (verified after download)
}

type Autoload struct {
	PSR4                map[string]any // values may be string or []string
	PSR0                map[string]any
	Files               []string
	Classmap            []string
	ExcludeFromClassmap []string
}

// SourceLookup is the interface the resolver consumes. Implementations:
//   - packagist.Client: fetches from packagist.org with HTTP cache
//   - (future) vcs.Client: clones git repos
//   - testlookup.Static: in-memory canned data for unit tests
//
// Implementations MUST return ErrPackageNotFound (declared below) for
// genuinely-missing packages so the resolver can distinguish "no such
// package" from "transient error."
type SourceLookup interface {
	// Lookup returns metadata for a package by canonical name (e.g.,
	// "monolog/monolog"). The returned PackageMetadata.Versions slice
	// is sorted in source order; callers must not assume sort.
	Lookup(ctx context.Context, name string) (*PackageMetadata, error)
}

// ErrPackageNotFound is returned by SourceLookup implementations when a
// package definitively does not exist in the source. Use errors.Is to test.
var ErrPackageNotFound = errPackageNotFound{}

type errPackageNotFound struct{}

func (errPackageNotFound) Error() string { return "registry: package not found" }
