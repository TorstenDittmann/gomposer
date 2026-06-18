package autoload

import (
	"reflect"
	"testing"

	"github.com/torstendittmann/gomposer/internal/manifest"
	"github.com/torstendittmann/gomposer/internal/registry"
)

func TestCollectPSR4FromRoot(t *testing.T) {
	root := manifest.Autoload{
		PSR4: map[string]string{
			"App\\":        "src/",
			"App\\Tests\\": "tests",
		},
	}
	out := CollectPSR4(".", root, nil)
	want := map[string][]string{
		"App\\":        {"src/"},
		"App\\Tests\\": {"tests/"},
	}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("got %v, want %v", out, want)
	}
}

func TestCollectPSR4FromVendorEntry(t *testing.T) {
	entries := []Entry{
		{
			Name:        "acme/foo",
			Version:     "1.0.0",
			InstallPath: "vendor/acme/foo",
			Autoload: registry.Autoload{
				PSR4: map[string]any{
					"Acme\\Foo\\": "src/",
				},
			},
		},
	}
	out := CollectPSR4(".", manifest.Autoload{}, entries)
	if got := out["Acme\\Foo\\"]; len(got) != 1 || got[0] != "vendor/acme/foo/src/" {
		t.Errorf("got %v", out)
	}
}

func TestCollectPSR4MergesMultipleDirs(t *testing.T) {
	entries := []Entry{
		{
			Name:        "acme/foo",
			InstallPath: "vendor/acme/foo",
			Autoload: registry.Autoload{
				PSR4: map[string]any{
					"Shared\\": []any{"src-a/", "src-b/"},
				},
			},
		},
		{
			Name:        "acme/bar",
			InstallPath: "vendor/acme/bar",
			Autoload: registry.Autoload{
				PSR4: map[string]any{
					"Shared\\": "src/",
				},
			},
		},
	}
	out := CollectPSR4(".", manifest.Autoload{}, entries)
	got := out["Shared\\"]
	want := []string{"vendor/acme/foo/src-a/", "vendor/acme/foo/src-b/", "vendor/acme/bar/src/"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCollectPSR4NormalizesTrailingSlash(t *testing.T) {
	entries := []Entry{
		{
			Name:        "acme/foo",
			InstallPath: "vendor/acme/foo",
			Autoload: registry.Autoload{
				PSR4: map[string]any{
					"Acme\\":  "src",  // no trailing slash
					"Other\\": "src/", // already trailing
				},
			},
		},
	}
	out := CollectPSR4(".", manifest.Autoload{}, entries)
	if got := out["Acme\\"]; got[0] != "vendor/acme/foo/src/" {
		t.Errorf("Acme = %v, want trailing slash normalized", got)
	}
	if got := out["Other\\"]; got[0] != "vendor/acme/foo/src/" {
		t.Errorf("Other = %v", got)
	}
}

func TestSortedPrefixes(t *testing.T) {
	in := map[string][]string{
		"Z\\":   {"z/"},
		"A\\":   {"a/"},
		"App\\": {"src/"},
	}
	got := SortedPrefixes(in)
	if got[0] != "A\\" || got[1] != "App\\" || got[2] != "Z\\" {
		t.Errorf("got %v, want sorted lexicographically", got)
	}
}
