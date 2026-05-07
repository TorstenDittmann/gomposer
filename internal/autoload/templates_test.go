package autoload

import (
	"strings"
	"testing"
)

func TestPhpStringEscapesBackslashAndQuote(t *testing.T) {
	got := phpString(`App\Foo`)
	if got != `'App\\Foo'` {
		t.Errorf("got %s", got)
	}
	got = phpString("can't")
	if got != `'can\'t'` {
		t.Errorf("got %s", got)
	}
}

func TestPhpDirVendorPath(t *testing.T) {
	got := phpDir("vendor/acme/foo/src/")
	want := `$vendorDir . '/acme/foo/src'`
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestPhpDirProjectPath(t *testing.T) {
	got := phpDir("src/")
	if got != `$baseDir . '/src'` {
		t.Errorf("got %s", got)
	}
}

func TestRenderAllProducesAllSlots(t *testing.T) {
	d := renderData{
		InitClass:  "ComposerAutoloaderInit" + strings.Repeat("a", 32),
		Hash:       strings.Repeat("a", 32),
		PSR4:       map[string][]string{"App\\": {"src/"}},
		SortedPSR4: []string{"App\\"},
	}
	out, err := renderAll(d)
	if err != nil {
		t.Fatalf("renderAll: %v", err)
	}
	for _, p := range []string{
		"vendor/autoload.php",
		"vendor/composer/autoload_real.php",
		"vendor/composer/autoload_psr4.php",
		"vendor/composer/autoload_namespaces.php",
		"vendor/composer/autoload_classmap.php",
		"vendor/composer/autoload_files.php",
		"vendor/composer/autoload_static.php",
		"vendor/composer/installed.php",
		"vendor/composer/InstalledVersions.php",
	} {
		if _, ok := out[p]; !ok {
			t.Errorf("missing output: %s", p)
		}
	}
}
