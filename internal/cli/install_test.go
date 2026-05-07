package cli

import (
	"bytes"
	"testing"
)

func TestInstallFailsWithoutManifest(t *testing.T) {
	var stdout bytes.Buffer
	root := newRootCmd()
	root.SetOut(&stdout)
	root.SetErr(&stdout)
	root.SetArgs([]string{"install", "--project", t.TempDir()})
	if err := root.Execute(); err == nil {
		t.Error("install with no composer.json should fail")
	}
}
