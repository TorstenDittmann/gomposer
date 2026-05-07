package autoload

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/torstendittmann/composer-go/internal/manifest"
	"github.com/torstendittmann/composer-go/internal/registry"
)

func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCollectClassmapDirRecurses(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "vendor/acme/foo/src/A.php",
		`<?php namespace Acme\Foo; class A {}`)
	writeFile(t, root, "vendor/acme/foo/src/sub/B.php",
		`<?php namespace Acme\Foo\Sub; class B {}`)
	writeFile(t, root, "vendor/acme/foo/src/legacy.inc",
		`<?php class Legacy {}`)

	entries := []Entry{
		{
			Name:        "acme/foo",
			InstallPath: "vendor/acme/foo",
			Autoload:    registry.Autoload{Classmap: []string{"src/"}},
		},
	}
	got, err := CollectClassmap(root, manifest.Autoload{}, entries)
	if err != nil {
		t.Fatalf("CollectClassmap: %v", err)
	}
	want := map[string]string{
		"Acme\\Foo\\A":      "vendor/acme/foo/src/A.php",
		"Acme\\Foo\\Sub\\B": "vendor/acme/foo/src/sub/B.php",
		"Legacy":            "vendor/acme/foo/src/legacy.inc",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCollectClassmapHonoursExclude(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "vendor/acme/foo/src/Real.php",
		`<?php namespace Acme; class Real {}`)
	writeFile(t, root, "vendor/acme/foo/src/Tests/HiddenTest.php",
		`<?php namespace Acme\Tests; class HiddenTest {}`)

	entries := []Entry{
		{
			Name:                "acme/foo",
			InstallPath:         "vendor/acme/foo",
			Autoload:            registry.Autoload{Classmap: []string{"src/"}},
			ExcludeFromClassmap: []string{"**/Tests/"},
		},
	}
	got, err := CollectClassmap(root, manifest.Autoload{}, entries)
	if err != nil {
		t.Fatalf("CollectClassmap: %v", err)
	}
	if _, ok := got["Acme\\Tests\\HiddenTest"]; ok {
		t.Errorf("excluded class leaked into classmap: %v", got)
	}
	if _, ok := got["Acme\\Real"]; !ok {
		t.Errorf("non-excluded class missing from classmap: %v", got)
	}
}

func TestCollectClassmapAcceptsSingleFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "vendor/acme/foo/Stand.php",
		`<?php class Standalone {}`)
	entries := []Entry{
		{
			Name:        "acme/foo",
			InstallPath: "vendor/acme/foo",
			Autoload:    registry.Autoload{Classmap: []string{"Stand.php"}},
		},
	}
	got, err := CollectClassmap(root, manifest.Autoload{}, entries)
	if err != nil {
		t.Fatal(err)
	}
	if got["Standalone"] != "vendor/acme/foo/Stand.php" {
		t.Errorf("got %v", got)
	}
}

func TestCollectClassmapMissingPathErrors(t *testing.T) {
	root := t.TempDir()
	entries := []Entry{
		{
			Name:        "acme/foo",
			InstallPath: "vendor/acme/foo",
			Autoload:    registry.Autoload{Classmap: []string{"does-not-exist/"}},
		},
	}
	_, err := CollectClassmap(root, manifest.Autoload{}, entries)
	if err == nil {
		t.Error("expected error for missing classmap entry")
	}
}
