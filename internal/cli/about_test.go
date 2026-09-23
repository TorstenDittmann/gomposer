package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestAboutPrintsVersion(t *testing.T) {
	root := &cobra.Command{Use: "gomposer", Version: "test-version"}
	about := newAboutCmd()
	root.AddCommand(about)

	buf := new(bytes.Buffer)
	root.SetOut(buf)
	root.SetArgs([]string{"about"})
	if err := root.Execute(); err != nil {
		t.Fatalf("about: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "gomposer test-version") {
		t.Fatalf("output missing version: %q", got)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	wantProject := "project: " + wd
	if !strings.Contains(got, wantProject) {
		t.Fatalf("output missing project line %q: %q", wantProject, got)
	}
}

func TestAboutAbsoluteProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"name":"acme/demo"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	root := &cobra.Command{Use: "gomposer", Version: "test-version"}
	about := newAboutCmd()
	root.AddCommand(about)

	buf := new(bytes.Buffer)
	root.SetOut(buf)
	root.SetArgs([]string{"about", "--project", dir})
	if err := root.Execute(); err != nil {
		t.Fatalf("about: %v", err)
	}
	got := buf.String()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "project: "+abs) {
		t.Fatalf("absolute --project mangled: %q", got)
	}
	if !strings.Contains(got, "composer.json: present") {
		t.Fatalf("expected composer.json present: %q", got)
	}
}
