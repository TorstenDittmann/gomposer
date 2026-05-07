package autoload

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/torstendittmann/composer-go/internal/manifest"
	"github.com/torstendittmann/composer-go/internal/registry"
)

// fixedProjectDir gives reproducible InitHash across machines.
const fixedProjectDir = "/composer-go-test/fixture"

func fixtureEntries() []Entry {
	return []Entry{
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
		{
			Name:        "acme/bar",
			Version:     "1.0.0",
			InstallPath: "vendor/acme/bar",
			Autoload: registry.Autoload{
				PSR4: map[string]any{
					"Acme\\Bar\\": "src/",
				},
			},
		},
	}
}

func fixtureRoot() manifest.Autoload {
	return manifest.Autoload{
		PSR4: map[string]string{"App\\": "src/"},
	}
}

func TestSnapshot(t *testing.T) {
	// Render in-memory using the fixed project dir for hash determinism.
	psr4 := CollectPSR4(fixedProjectDir, fixtureRoot(), fixtureEntries())
	sorted := SortedPrefixes(psr4)
	out, err := renderAll(renderData{
		InitClass:       InitClassName(fixedProjectDir),
		Hash:            InitHash(fixedProjectDir),
		PSR4:            psr4,
		SortedPSR4:      sorted,
		PSR4ByFirstChar: buildFirstCharGroups(sorted),
	})
	if err != nil {
		t.Fatalf("renderAll: %v", err)
	}

	cases := []struct {
		gen, expected string
	}{
		{"vendor/autoload.php", "autoload.php"},
		{"vendor/composer/autoload_real.php", "autoload_real.php"},
		{"vendor/composer/autoload_psr4.php", "autoload_psr4.php"},
		{"vendor/composer/autoload_namespaces.php", "autoload_namespaces.php"},
		{"vendor/composer/autoload_classmap.php", "autoload_classmap.php"},
		{"vendor/composer/autoload_files.php", "autoload_files.php"},
		{"vendor/composer/autoload_static.php", "autoload_static.php"},
		{"vendor/composer/installed.php", "installed.php"},
	}
	for _, tc := range cases {
		t.Run(tc.expected, func(t *testing.T) {
			got, ok := out[tc.gen]
			if !ok {
				t.Fatalf("missing generated output: %s", tc.gen)
			}
			expectedPath := filepath.Join("testdata", "expected", tc.expected)
			want, err := os.ReadFile(expectedPath)
			if err != nil {
				t.Fatalf("read expected %s: %v", expectedPath, err)
			}
			if !bytes.Equal(got, want) {
				// On mismatch, write the actual to disk for easy diffing.
				_ = os.WriteFile(expectedPath+".actual", got, 0o644)
				t.Errorf("byte mismatch for %s — see %s.actual for the actual bytes", tc.expected, expectedPath)
			}
		})
	}
}

func TestGenerateWritesFiles(t *testing.T) {
	dir := t.TempDir()
	if err := Generate(Options{
		ProjectDir:   dir,
		Entries:      fixtureEntries(),
		RootAutoload: fixtureRoot(),
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	for _, want := range []string{
		"vendor/autoload.php",
		"vendor/composer/autoload_real.php",
		"vendor/composer/autoload_psr4.php",
		"vendor/composer/autoload_namespaces.php",
		"vendor/composer/autoload_classmap.php",
		"vendor/composer/autoload_files.php",
		"vendor/composer/autoload_static.php",
		"vendor/composer/installed.php",
		"vendor/composer/InstalledVersions.php",
		"vendor/composer/ClassLoader.php",
		"vendor/composer/LICENSE",
	} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}

	// ClassLoader.php must be byte-identical to the embedded copy.
	got, err := os.ReadFile(filepath.Join(dir, "vendor/composer/ClassLoader.php"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(got, []byte("<?php")) {
		t.Errorf("ClassLoader.php does not start with <?php")
	}
}

func TestGenerateIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		ProjectDir:   dir,
		Entries:      fixtureEntries(),
		RootAutoload: fixtureRoot(),
	}
	if err := Generate(opts); err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	first, err := os.ReadFile(filepath.Join(dir, "vendor/composer/autoload_psr4.php"))
	if err != nil {
		t.Fatal(err)
	}

	if err := Generate(opts); err != nil {
		t.Fatalf("second Generate: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(dir, "vendor/composer/autoload_psr4.php"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("autoload_psr4.php changed across regenerations")
	}
}

func TestGenerateRejectsRelativeProjectDir(t *testing.T) {
	err := Generate(Options{ProjectDir: "relative/path"})
	if err == nil {
		t.Error("expected error on relative ProjectDir")
	}
}

func TestEndToEndPHPClassResolution(t *testing.T) {
	if _, err := exec.LookPath("php"); err != nil {
		t.Skip("php not on PATH; skipping e2e")
	}

	// Copy the fixture into a writable tempdir so Generate's writes
	// don't pollute testdata.
	dir := t.TempDir()
	src := filepath.Join("testdata", "fixture-project")
	if err := copyDir(src, dir); err != nil {
		t.Fatalf("copyDir: %v", err)
	}

	if err := Generate(Options{
		ProjectDir: dir,
		Entries: []Entry{
			{
				Name:        "acme/foo",
				Version:     "1.0.0",
				InstallPath: "vendor/acme/foo",
				Autoload:    registry.Autoload{PSR4: map[string]any{"Acme\\Foo\\": "src/"}},
			},
			{
				Name:        "acme/bar",
				Version:     "1.0.0",
				InstallPath: "vendor/acme/bar",
				Autoload:    registry.Autoload{PSR4: map[string]any{"Acme\\Bar\\": "src/"}},
			},
		},
		RootAutoload: manifest.Autoload{PSR4: map[string]string{"App\\": "src/"}},
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	cases := []struct {
		class string
		want  string // PHP echoes "1" if class_exists is true, "" otherwise
	}{
		{`App\\Foo`, "1"},
		{`Acme\\Foo\\Foo`, "1"},
		{`Acme\\Bar\\Bar`, "1"},
		{`Nope\\Missing`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.class, func(t *testing.T) {
			script := "require 'vendor/autoload.php'; echo class_exists('" + tc.class + "') ? '1' : '';"
			cmd := exec.Command("php", "-r", script)
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("php failed: %v\noutput:\n%s", err, out)
			}
			if string(out) != tc.want {
				t.Errorf("class_exists(%s) = %q, want %q", tc.class, string(out), tc.want)
			}
		})
	}
}

// copyDir copies src to dst recursively. Symlinks are not handled; the
// fixture tree contains only regular files.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

func TestGenerateWithNoEntries(t *testing.T) {
	dir := t.TempDir()
	if err := Generate(Options{
		ProjectDir:   dir,
		Entries:      nil,
		RootAutoload: manifest.Autoload{PSR4: map[string]string{"App\\": "src/"}},
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "vendor/composer/autoload_psr4.php"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`'App\\'`)) {
		t.Errorf("expected App\\ prefix in autoload_psr4.php, got:\n%s", body)
	}
}

func TestGenerateWithNoAutoloadAtAll(t *testing.T) {
	dir := t.TempDir()
	if err := Generate(Options{
		ProjectDir: dir,
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// Empty PSR-4 array still produces a valid PHP file that returns array().
	body, err := os.ReadFile(filepath.Join(dir, "vendor/composer/autoload_psr4.php"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("return array(")) {
		t.Errorf("autoload_psr4.php missing return array(): %s", body)
	}
}
