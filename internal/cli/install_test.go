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

func TestInstallAcceptsIgnorePlatformReqRepeated(t *testing.T) {
	// Reset flag state before the test to avoid cross-test contamination.
	flagIgnorePlatformReqs = nil
	root := newRootCmd()
	root.SetArgs([]string{"install",
		"--project", t.TempDir(),
		"--ignore-platform-req=php",
		"--ignore-platform-req=ext-curl",
	})
	// Will fail at orchestrator level (no manifest), but we only assert
	// flag parsing works.
	_ = root.Execute()
	if len(flagIgnorePlatformReqs) != 2 {
		t.Errorf("flagIgnorePlatformReqs = %+v", flagIgnorePlatformReqs)
	}
}
