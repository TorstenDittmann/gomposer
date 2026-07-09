package resolver

import (
	"reflect"
	"testing"

	"github.com/torstendittmann/gomposer/internal/manifest"
	"github.com/torstendittmann/gomposer/internal/registry"
)

func TestBuildLockPackages(t *testing.T) {
	v, _ := mustV(t, "1.2.3"), 0
	r := &Result{
		Packages: []ResolvedPackage{{
			Name:    "a/a",
			Version: v,
			Record: registry.PackageVersion{
				Name:    "a/a",
				Version: "1.2.3",
				Source:  registry.Source{Type: "git", URL: "git://a", Ref: "abc"},
				Dist:    registry.Dist{Type: "zip", URL: "https://a.zip", Sha: "deadbeef"},
				Require: map[string]string{"php": ">=8.1"},
			},
		}},
		PackagesDev: []ResolvedPackage{{
			Name:    "d/d",
			Version: v,
			Record: registry.PackageVersion{
				Name:    "d/d",
				Version: "1.2.3",
				Source:  registry.Source{Type: "git", URL: "git://d", Ref: "def"},
				Dist:    registry.Dist{Type: "zip", URL: "https://d.zip", Sha: "cafebabe"},
			},
		}},
	}
	m := &manifest.Manifest{}
	out, err := BuildLock(r, m, []byte(`{"name":"x/y"}`))
	if err != nil {
		t.Fatalf("BuildLock: %v", err)
	}
	prod, dev := out.Packages, out.PackagesDev
	if len(prod) != 1 || prod[0].Name != "a/a" {
		t.Errorf("prod = %+v", prod)
	}
	if prod[0].Source.Reference != "abc" {
		t.Errorf("prod[0].Source.Reference = %q, want abc", prod[0].Source.Reference)
	}
	if prod[0].Dist.Shasum != "deadbeef" {
		t.Errorf("prod[0].Dist.Shasum = %q", prod[0].Dist.Shasum)
	}
	if prod[0].Require["php"] != ">=8.1" {
		t.Errorf("require not preserved: %v", prod[0].Require)
	}
	if len(dev) != 1 || dev[0].Name != "d/d" {
		t.Errorf("dev = %+v", dev)
	}
}

func TestAutoloadToMapIncludesAllFields(t *testing.T) {
	al := registry.Autoload{
		PSR4:                map[string]any{"Acme\\": "src/"},
		Files:               []string{"bootstrap.php"},
		Classmap:            []string{"legacy/"},
		ExcludeFromClassmap: []string{"**/Tests/"},
	}
	m := autoloadToMap(al)
	if m["psr-4"] == nil {
		t.Errorf("psr-4 missing")
	}
	if !reflect.DeepEqual(m["files"], []string{"bootstrap.php"}) {
		t.Errorf("files = %v", m["files"])
	}
	if !reflect.DeepEqual(m["classmap"], []string{"legacy/"}) {
		t.Errorf("classmap = %v", m["classmap"])
	}
	if !reflect.DeepEqual(m["exclude-from-classmap"], []string{"**/Tests/"}) {
		t.Errorf("exclude-from-classmap = %v", m["exclude-from-classmap"])
	}
}

func TestAdapterEmitsComposerFlavoredLock(t *testing.T) {
	manifestBytes := []byte(`{
	  "name": "acme/app",
	  "require": {"acme/lib": "^1.0", "php": ">=8.1"},
	  "minimum-stability": "stable"
	}`)
	m := &manifest.Manifest{
		Require:          map[string]string{"acme/lib": "^1.0", "php": ">=8.1"},
		MinimumStability: "stable",
	}
	r := &Result{}
	out, err := BuildLock(r, m, manifestBytes)
	if err != nil {
		t.Fatalf("BuildLock: %v", err)
	}
	if len(out.Readme) < 1 || out.Readme[0] == "" {
		t.Errorf("_readme not populated")
	}
	if out.ContentHash == "" {
		t.Errorf("content-hash empty")
	}
	if out.PluginAPIVersion != "2.6.0" {
		t.Errorf("plugin-api-version = %q", out.PluginAPIVersion)
	}
	if out.Platform["php"] != ">=8.1" {
		t.Errorf("platform[php] = %q, want >=8.1", out.Platform["php"])
	}
}

func TestAdapterMapsStabilityFlagRanks(t *testing.T) {
	cases := map[string]int{
		"dev": 20, "alpha": 15, "beta": 10, "RC": 5, "rc": 5, "stable": 0,
	}
	for flag, want := range cases {
		if got := stabilityRank(flag); got != want {
			t.Errorf("stabilityRank(%q) = %d, want %d", flag, got, want)
		}
	}
}

func TestAdapterNotificationURLForPackagist(t *testing.T) {
	// Sources coming from a git URL under packagist mirror emit the
	// Packagist notification-url; VCS-only sources get "".
	packagistLike := registry.Source{Type: "git", URL: "https://api.github.com/repos/acme/lib/zipball/x"}
	if got := notificationURLFor(packagistLike, true); got != "https://packagist.org/downloads/" {
		t.Errorf("packagist source: %q", got)
	}
	vcsOnly := registry.Source{Type: "git", URL: "git@github.com:acme/private.git"}
	if got := notificationURLFor(vcsOnly, false); got != "" {
		t.Errorf("vcs source: %q", got)
	}
}
