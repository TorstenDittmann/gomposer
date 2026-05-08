package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadCorpusReturnsSortedFixtures(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"zeta", "alpha", "mid"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "composer.json"),
			[]byte(`{"name":"x/y"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := LoadCorpus(root)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	names := make([]string, len(got))
	for i, f := range got {
		names[i] = f.Name
	}
	want := []string{"alpha", "mid", "zeta"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
}

func TestLoadCorpusSkipsDirsWithoutComposerJSON(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "no-manifest"), 0o755); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(root, "good")
	if err := os.MkdirAll(good, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(good, "composer.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadCorpus(root)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(got) != 1 || got[0].Name != "good" {
		t.Errorf("got %+v, want one fixture named good", got)
	}
}

func TestLoadCorpusSkipsHiddenDirs(t *testing.T) {
	root := t.TempDir()
	hidden := filepath.Join(root, ".cache")
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hidden, "composer.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadCorpus(root)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want zero fixtures, got %+v", got)
	}
}

func TestLoadCorpusErrorsOnMissingRoot(t *testing.T) {
	if _, err := LoadCorpus(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Error("expected error on missing root")
	}
}
