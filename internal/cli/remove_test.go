package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/torstendittmann/gomposer/internal/orchestrator"
)

func swapRemoveRunner(mock func(context.Context, orchestrator.Options) error) func() {
	old := removeFn
	removeFn = mock
	return func() { removeFn = old }
}

func TestUpdateManifestRemovals(t *testing.T) {
	original := []byte(`{
  "name": "acme/app",
  "description": "kept",
  "require": {"monolog/monolog": "^3.0", "psr/log": "^3.0"},
  "require-dev": {"phpunit/phpunit": "^12.0"}
}`)

	updated, err := updateManifestRemovals(original, []string{"monolog/monolog", "psr/log"}, false)
	if err != nil {
		t.Fatalf("updateManifestRemovals: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(updated, &doc); err != nil {
		t.Fatal(err)
	}
	if _, exists := doc["require"]; exists {
		t.Errorf("empty require field was not removed: %s", updated)
	}
	if string(doc["description"]) != `"kept"` || !bytes.Contains(doc["require-dev"], []byte("phpunit/phpunit")) {
		t.Errorf("unrelated fields changed: %s", updated)
	}
}

func TestUpdateManifestRemovalsRequiresCorrectSection(t *testing.T) {
	manifest := []byte(`{"require":{"monolog/monolog":"^3.0"},"require-dev":{"phpunit/phpunit":"^12.0"}}`)
	if _, err := updateManifestRemovals(manifest, []string{"phpunit/phpunit"}, false); err == nil || !strings.Contains(err.Error(), "--dev") {
		t.Fatalf("wrong-section error = %v, want --dev hint", err)
	}
	if _, err := updateManifestRemovals(manifest, []string{"unknown/pkg"}, false); err == nil || !strings.Contains(err.Error(), "not a direct dependency") {
		t.Fatalf("missing-package error = %v", err)
	}

	// Validation is all-or-nothing: a valid first package does not disappear
	// when a later package is invalid.
	if _, err := updateManifestRemovals(manifest, []string{"monolog/monolog", "unknown/pkg"}, false); err == nil {
		t.Fatal("mixed valid/invalid removal succeeded")
	}
}

func TestRemoveCommandUpdatesManifestBeforeRunner(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "composer.json")
	if err := os.WriteFile(manifestPath, []byte(`{"name":"acme/app","require":{"monolog/monolog":"^3.0","psr/log":"^3.0"}}`), 0o640); err != nil {
		t.Fatal(err)
	}

	var got orchestrator.Options
	restore := swapRemoveRunner(func(_ context.Context, opts orchestrator.Options) error {
		got = opts
		body, err := os.ReadFile(manifestPath)
		if err != nil {
			return err
		}
		var doc struct {
			Require map[string]string `json:"require"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			return err
		}
		if _, exists := doc.Require["monolog/monolog"]; exists || doc.Require["psr/log"] != "^3.0" {
			return fmt.Errorf("require = %+v", doc.Require)
		}
		return nil
	})
	defer restore()

	cmd := newRootCmd("dev")
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"remove", "--project", dir, "monolog/monolog"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.ProjectDir != dir {
		t.Errorf("ProjectDir = %q, want %q", got.ProjectDir, dir)
	}
	if info, err := os.Stat(manifestPath); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o640 {
		t.Errorf("composer.json mode = %o, want 640", info.Mode().Perm())
	}
}

func TestRemoveCommandSupportsDevAndWorkspace(t *testing.T) {
	rootDir := t.TempDir()
	workspaceDir := filepath.Join(rootDir, "packages", "api")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "composer.json"), []byte(`{"name":"acme/root","workspaces":["packages/*"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	workspaceManifest := filepath.Join(workspaceDir, "composer.json")
	if err := os.WriteFile(workspaceManifest, []byte(`{"name":"acme/api","version":"1.0.0","require-dev":{"phpunit/phpunit":"^12.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	restore := swapRemoveRunner(func(_ context.Context, opts orchestrator.Options) error {
		if opts.ProjectDir != rootDir {
			return fmt.Errorf("ProjectDir = %q, want %q", opts.ProjectDir, rootDir)
		}
		body, err := os.ReadFile(workspaceManifest)
		if err != nil {
			return err
		}
		if bytes.Contains(body, []byte("phpunit/phpunit")) {
			return errors.New("dev dependency remained in workspace manifest")
		}
		return nil
	})
	defer restore()

	cmd := newRootCmd("dev")
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"remove", "--dev", "--project", workspaceDir, "phpunit/phpunit"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestRemoveCommandRollsBackManifestAndLock(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "composer.json")
	lockPath := filepath.Join(dir, "gomposer.lock")
	originalManifest := []byte(`{"name":"acme/app","require":{"monolog/monolog":"^3.0"}}`)
	originalLock := []byte("old lock\n")
	if err := os.WriteFile(manifestPath, originalManifest, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, originalLock, 0o600); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("update failed")
	restore := swapRemoveRunner(func(_ context.Context, _ orchestrator.Options) error {
		if err := os.WriteFile(lockPath, []byte("new lock"), 0o644); err != nil {
			return err
		}
		return boom
	})
	defer restore()

	cmd := newRootCmd("dev")
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"remove", "--project", dir, "monolog/monolog"})
	if err := cmd.Execute(); !errors.Is(err, boom) {
		t.Fatalf("Execute error = %v, want %v", err, boom)
	}
	assertFileContent(t, manifestPath, originalManifest)
	assertFileContent(t, lockPath, originalLock)
}
