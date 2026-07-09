package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestAllowPluginsFlagAcceptedAsNoOp verifies the flag parses without error.
// We give the command a manifest with no requires so the orchestrator
// short-circuits — we are testing the flag plumbing, not the install path.
func TestAllowPluginsFlagAcceptedAsNoOp(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"),
		[]byte(`{"name":"vendor/pkg"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"install", "--project", dir, "--allow-plugins"},
		{"install", "--project", dir, "--allow-plugins=*"},
		{"install", "--project", dir, "--allow-plugins=composer/installers,phpstan/extension-installer"},
		{"update", "--project", dir, "--allow-plugins=*"},
	} {
		var out bytes.Buffer
		root := newRootCmd("dev")
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Errorf("args=%v: unexpected error: %v\noutput: %s", args, err, out.String())
		}
	}
}

func TestAllowPluginsHelpMentionsNoOp(t *testing.T) {
	var out bytes.Buffer
	root := newRootCmd("dev")
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"install", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("help: %v", err)
	}
	body := out.String()
	if !contains(body, "--allow-plugins") {
		t.Errorf("help missing --allow-plugins: %s", body)
	}
	if !contains(body, "no-op") && !contains(body, "ignored") {
		t.Errorf("help text must clarify --allow-plugins is a no-op: %s", body)
	}
}

func contains(haystack, needle string) bool {
	return bytes.Contains([]byte(haystack), []byte(needle))
}
