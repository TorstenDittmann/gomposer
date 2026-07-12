package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/torstendittmann/gomposer/internal/cache"
)

// seedLayer writes a file of n bytes into the named layer's directory.
func seedLayer(t *testing.T, name, file string, n int) {
	t.Helper()
	l, ok := cache.LayerByName(name)
	if !ok {
		t.Fatalf("unknown layer %q", name)
	}
	dir, err := l.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), bytes.Repeat([]byte("x"), n), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runCache executes the CLI with args and returns captured output.
func runCache(t *testing.T, args ...string) (string, error) {
	t.Helper()
	flagQuiet = false // reset shared flag state between tests
	var out bytes.Buffer
	root := newRootCmd("dev")
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestCacheInfoListsLayersAndTotal(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	seedLayer(t, "store", "a.zip", 5)
	out, err := runCache(t, "cache")
	if err != nil {
		t.Fatalf("cache: %v", err)
	}
	for _, want := range []string{"store", "metadata", "resolution", "vcs", "total", "5 B"} {
		if !strings.Contains(out, want) {
			t.Errorf("cache output missing %q:\n%s", want, out)
		}
	}
}

func TestCacheDirPrintsOnlyPath(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", xdg)
	out, err := runCache(t, "cache", "dir")
	if err != nil {
		t.Fatalf("cache dir: %v", err)
	}
	if want := filepath.Join(xdg, "gomposer") + "\n"; out != want {
		t.Errorf("cache dir output = %q, want %q", out, want)
	}
}

func TestCacheClearAllReportsFreed(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	seedLayer(t, "store", "a.zip", 5)
	seedLayer(t, "metadata", "m.json", 3)
	out, err := runCache(t, "cache", "clear")
	if err != nil {
		t.Fatalf("cache clear: %v", err)
	}
	for _, want := range []string{"cleared store (5 B)", "cleared metadata (3 B)", "cleared resolution (0 B)", "cleared vcs (0 B)", "freed 8 B"} {
		if !strings.Contains(out, want) {
			t.Errorf("clear output missing %q:\n%s", want, out)
		}
	}
	for _, name := range []string{"store", "metadata"} {
		l, _ := cache.LayerByName(name)
		dir, _ := l.Path()
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s dir still exists after clear", name)
		}
	}
}

func TestCacheClearSelectiveLeavesOthers(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	seedLayer(t, "store", "a.zip", 5)
	seedLayer(t, "metadata", "m.json", 3)
	out, err := runCache(t, "cache", "clear", "metadata")
	if err != nil {
		t.Fatalf("cache clear metadata: %v", err)
	}
	if !strings.Contains(out, "cleared metadata (3 B)") {
		t.Errorf("clear output:\n%s", out)
	}
	if strings.Contains(out, "freed") {
		t.Errorf("single-layer clear must omit the total line:\n%s", out)
	}
	if size, _ := cache.LayerStore.Size(); size != 5 {
		t.Errorf("store layer touched by selective clear; size = %d, want 5", size)
	}
}

func TestCacheClearUnknownLayerFailsBeforeClearing(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	seedLayer(t, "store", "a.zip", 5)
	_, err := runCache(t, "cache", "clear", "store", "bogus")
	if err == nil || !strings.Contains(err.Error(), `unknown cache layer "bogus"`) {
		t.Fatalf("err = %v, want unknown-layer error naming bogus", err)
	}
	if size, _ := cache.LayerStore.Size(); size != 5 {
		t.Errorf("store cleared despite arg error; size = %d, want 5", size)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{999, "999 B"},
		{1000, "1.0 kB"},
		{1500, "1.5 kB"},
		{142_300_000, "142.3 MB"},
		{3_100_000_000, "3.1 GB"},
		{2_500_000_000_000, "2500.0 GB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.n); got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}
