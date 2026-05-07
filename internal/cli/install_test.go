package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallReadsManifest(t *testing.T) {
	dir := t.TempDir()
	manifest := []byte(`{"name": "vendor/pkg", "require": {"monolog/monolog": "^3.0"}}`)
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), manifest, 0o644); err != nil {
		t.Fatalf("write composer.json: %v", err)
	}

	var stdout bytes.Buffer
	root := newRootCmd()
	root.SetOut(&stdout)
	root.SetErr(&stdout)
	root.SetArgs([]string{"install", "--project", dir})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := stdout.String()
	if !bytes.Contains([]byte(got), []byte("vendor/pkg")) {
		t.Errorf("expected manifest summary in output, got %q", got)
	}
}
