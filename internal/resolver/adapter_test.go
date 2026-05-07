package resolver

import (
	"reflect"
	"testing"

	"github.com/torstendittmann/composer-go/internal/registry"
)

func TestToLockPackages(t *testing.T) {
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
	prod, dev := ToLockPackages(r)
	if len(prod) != 1 || prod[0].Name != "a/a" {
		t.Errorf("prod = %+v", prod)
	}
	if prod[0].Source.Ref != "abc" {
		t.Errorf("prod[0].Source.Ref = %q, want abc", prod[0].Source.Ref)
	}
	if prod[0].Dist.Sha256 != "deadbeef" {
		t.Errorf("prod[0].Dist.Sha256 = %q", prod[0].Dist.Sha256)
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
