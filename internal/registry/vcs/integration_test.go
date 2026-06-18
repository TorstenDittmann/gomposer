package vcs

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/torstendittmann/gomposer/internal/manifest"
	"github.com/torstendittmann/gomposer/internal/registry/multisource"
	"github.com/torstendittmann/gomposer/internal/resolver"
)

func TestResolverFindsTaggedVersion(t *testing.T) {
	url := makeBareRepoMulti(t, map[string]string{
		"refs/heads/main":  `{"name":"acme/widget"}`,
		"refs/tags/v1.0.0": `{"name":"acme/widget"}`,
		"refs/tags/v1.1.0": `{"name":"acme/widget"}`,
	})
	c, _ := New(Config{URL: url, CacheRoot: filepath.Join(t.TempDir(), "vcs")})
	src := multisource.New(c)
	m := &manifest.Manifest{
		Name:    "demo/app",
		Require: map[string]string{"acme/widget": "^1.0"},
	}
	res, err := resolver.Solve(context.Background(), resolver.Input{
		Manifest:   m,
		Source:     src,
		IncludeDev: false,
	})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if len(res.Packages) != 1 {
		t.Fatalf("Packages = %d", len(res.Packages))
	}
	if res.Packages[0].Record.Version != "1.1.0" {
		t.Errorf("picked %q, want 1.1.0", res.Packages[0].Record.Version)
	}
}

func TestResolverFindsExplicitDevMain(t *testing.T) {
	url := makeBareRepoMulti(t, map[string]string{
		"refs/heads/main":  `{"name":"acme/widget"}`,
		"refs/tags/v1.0.0": `{"name":"acme/widget"}`,
	})
	c, _ := New(Config{URL: url, CacheRoot: filepath.Join(t.TempDir(), "vcs")})
	src := multisource.New(c)
	m := &manifest.Manifest{
		Name:    "demo/app",
		Require: map[string]string{"acme/widget": "dev-main"},
		// Note: no minimum-stability change. Stage 2 plan 2 admits explicit dev-* requires.
	}
	res, err := resolver.Solve(context.Background(), resolver.Input{
		Manifest: m, Source: src, IncludeDev: false,
	})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if res.Packages[0].Record.Version != "dev-main" {
		t.Errorf("picked %q, want dev-main", res.Packages[0].Record.Version)
	}
}

func TestResolverFindsBranchAliasUnderCaret(t *testing.T) {
	url := makeBareRepoMulti(t, map[string]string{
		"refs/heads/main": `{
			"name":"acme/widget",
			"extra":{"branch-alias":{"dev-main":"1.x-dev"}}
		}`,
	})
	c, _ := New(Config{URL: url, CacheRoot: filepath.Join(t.TempDir(), "vcs")})
	src := multisource.New(c)
	m := &manifest.Manifest{
		Name:             "demo/app",
		Require:          map[string]string{"acme/widget": "^1.0"},
		MinimumStability: "dev", // alias matching still requires dev-stability admittance
	}
	res, err := resolver.Solve(context.Background(), resolver.Input{
		Manifest: m, Source: src, IncludeDev: false,
	})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	picked := res.Packages[0].Record.Version
	if picked != "1.x-dev" {
		t.Errorf("picked %q, want 1.x-dev", picked)
	}
}
