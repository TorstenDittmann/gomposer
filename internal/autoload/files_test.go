package autoload

import (
	"reflect"
	"testing"

	"github.com/torstendittmann/gomposer/internal/manifest"
	"github.com/torstendittmann/gomposer/internal/registry"
)

func TestCollectFilesEmpty(t *testing.T) {
	got := CollectFiles(manifest.Autoload{}, nil)
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}

func TestCollectFilesSortsByPackageNameThenListedOrder(t *testing.T) {
	entries := []Entry{
		{
			Name:        "zeta/last",
			InstallPath: "vendor/zeta/last",
			Autoload:    registry.Autoload{Files: []string{"zz.php", "aa.php"}},
		},
		{
			Name:        "alpha/first",
			InstallPath: "vendor/alpha/first",
			Autoload:    registry.Autoload{Files: []string{"b.php", "a.php"}},
		},
	}
	got := CollectFiles(manifest.Autoload{}, entries)
	want := []FileEntry{
		{Path: "vendor/alpha/first/b.php", PackageName: "alpha/first"},
		{Path: "vendor/alpha/first/a.php", PackageName: "alpha/first"},
		{Path: "vendor/zeta/last/zz.php", PackageName: "zeta/last"},
		{Path: "vendor/zeta/last/aa.php", PackageName: "zeta/last"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCollectFilesPutsRootLast(t *testing.T) {
	root := manifest.Autoload{Files: []string{"app/bootstrap.php"}}
	entries := []Entry{
		{
			Name:        "alpha/first",
			InstallPath: "vendor/alpha/first",
			Autoload:    registry.Autoload{Files: []string{"helpers.php"}},
		},
	}
	got := CollectFiles(root, entries)
	if got[0].PackageName != "alpha/first" {
		t.Errorf("vendor entry should come first, got %s", got[0].PackageName)
	}
	if got[len(got)-1].Path != "app/bootstrap.php" {
		t.Errorf("root manifest entry should come last, got %v", got[len(got)-1])
	}
}

func TestCollectFilesDeduplicatesByOutputPath(t *testing.T) {
	entries := []Entry{
		{
			Name:        "alpha/first",
			InstallPath: "vendor/alpha/first",
			Autoload:    registry.Autoload{Files: []string{"helpers.php"}},
		},
		{
			Name:        "beta/second",
			InstallPath: "vendor/alpha/first", // unusual, but possible via path repos
			Autoload:    registry.Autoload{Files: []string{"helpers.php"}},
		},
	}
	got := CollectFiles(manifest.Autoload{}, entries)
	if len(got) != 1 {
		t.Errorf("expected 1 entry after dedup, got %d: %v", len(got), got)
	}
}
