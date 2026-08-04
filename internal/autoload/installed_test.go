package autoload

import "testing"

func TestNormalizeInstalledDefaultsAndSorts(t *testing.T) {
	got, err := normalizeInstalled(InstalledData{Packages: []InstalledPackage{
		{Name: "z/pkg", PrettyVersion: "v1.2.3", InstallPath: "vendor/z/pkg", Aliases: []string{"2.0.0.0", "2.0.0.0"}},
		{Name: "a/pkg", PrettyVersion: "dev-main", InstallPath: "vendor/a/pkg"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Root.Name != "__root__" || got.Root.Version != "1.0.0.0" || got.Root.Type != "project" {
		t.Fatalf("unexpected root defaults: %+v", got.Root)
	}
	if got.Packages[0].Name != "a/pkg" || got.Packages[1].Version != "1.2.3.0" {
		t.Fatalf("unexpected packages: %+v", got.Packages)
	}
	if len(got.Packages[1].Aliases) != 1 {
		t.Fatalf("aliases were not deduplicated: %+v", got.Packages[1].Aliases)
	}
}

func TestNormalizeInstalledRejectsDuplicatesAndTraversal(t *testing.T) {
	for _, data := range []InstalledData{
		{Root: InstalledRoot{Name: "acme/app"}, Packages: []InstalledPackage{{Name: "ACME/APP", InstallPath: "vendor/acme/app"}}},
		{Packages: []InstalledPackage{{Name: "acme/pkg", InstallPath: "../outside"}}},
	} {
		if _, err := normalizeInstalled(data); err == nil {
			t.Fatalf("normalizeInstalled(%+v) succeeded", data)
		}
	}
}

func TestPHPInstallPath(t *testing.T) {
	if got := phpInstallPath("vendor/acme/pkg"); got != `__DIR__ . '/..' . '/acme/pkg'` {
		t.Fatalf("phpInstallPath = %s", got)
	}
}
