package cache

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLayersRegistry(t *testing.T) {
	want := []Layer{
		{Name: "store", Subdir: "store"},
		{Name: "metadata", Subdir: "packagist"},
		{Name: "resolution", Subdir: "resolution"},
		{Name: "vcs", Subdir: "vcs"},
	}
	got := Layers()
	if len(got) != len(want) {
		t.Fatalf("Layers() returned %d layers, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Layers()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestLayerByName(t *testing.T) {
	for _, l := range Layers() {
		got, ok := LayerByName(l.Name)
		if !ok || got != l {
			t.Errorf("LayerByName(%q) = %+v, %v", l.Name, got, ok)
		}
	}
	if _, ok := LayerByName("bogus"); ok {
		t.Error("LayerByName(\"bogus\") should return false")
	}
}

func TestLayerPathJoinsRoot(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", xdg)
	dir, err := LayerMetadata.Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if want := filepath.Join(xdg, "gomposer", "packagist"); dir != want {
		t.Errorf("Path() = %q, want %q", dir, want)
	}
}

func TestLayerSizeSumsNestedFiles(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir, err := LayerStore.Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.zip"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "b.zip"), []byte("1234567"), 0o644); err != nil {
		t.Fatal(err)
	}
	size, err := LayerStore.Size()
	if err != nil {
		t.Fatalf("Size: %v", err)
	}
	if size != 12 {
		t.Errorf("Size() = %d, want 12 (5 + 7 nested)", size)
	}
}

func TestLayerSizeMissingDirIsZero(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	size, err := LayerVCS.Size()
	if err != nil {
		t.Fatalf("Size on missing dir: %v", err)
	}
	if size != 0 {
		t.Errorf("Size() = %d, want 0 for missing dir", size)
	}
}

func TestLayerClearRemovesAndReports(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir, err := LayerMetadata.Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x.json"), []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	freed, err := LayerMetadata.Clear()
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if freed != 3 {
		t.Errorf("Clear() freed %d, want 3", freed)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("layer dir still exists after Clear")
	}
	// Second clear: missing dir is a no-op returning 0.
	freed2, err := LayerMetadata.Clear()
	if err != nil {
		t.Fatalf("second Clear: %v", err)
	}
	if freed2 != 0 {
		t.Errorf("second Clear() freed %d, want 0", freed2)
	}
}
