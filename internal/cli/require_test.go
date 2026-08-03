package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/torstendittmann/gomposer/internal/orchestrator"
)

func swapRequireRunner(mock func(context.Context, orchestrator.Options) error) func() {
	old := requireFn
	requireFn = mock
	return func() { requireFn = old }
}

func TestParseRequirement(t *testing.T) {
	tests := []struct {
		input      string
		wantName   string
		wantConstr string
		wantErr    bool
	}{
		{input: "monolog/monolog", wantName: "monolog/monolog", wantConstr: "*"},
		{input: "symfony/console:^7.0", wantName: "symfony/console", wantConstr: "^7.0"},
		{input: "acme/pkg:dev-main", wantName: "acme/pkg", wantConstr: "dev-main"},
		{input: "ext-redis", wantName: "ext-redis", wantConstr: "*"},
		{input: "", wantErr: true},
		{input: "monolog", wantErr: true},
		{input: "acme/pkg:", wantErr: true},
		{input: "bad package:^1", wantErr: true},
		{input: "ext-bad name", wantErr: true},
		{input: "acme/pkg:not-a-version", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseRequirement(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseRequirement(%q) succeeded: %+v", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRequirement(%q): %v", tt.input, err)
			}
			if got.Name != tt.wantName || got.Constraint != tt.wantConstr {
				t.Errorf("parseRequirement(%q) = %+v", tt.input, got)
			}
		})
	}
}

func TestUpdateManifestRequirementsPreservesUnknownFields(t *testing.T) {
	original := []byte(`{
  "name": "acme/app",
  "description": "kept",
  "require": {"psr/log": "^3.0"},
  "require-dev": {"phpunit/phpunit": "^11.0"},
  "config": {"sort-packages": true}
}`)
	specs := []requirementSpec{
		{Name: "symfony/console", Constraint: "^7.0"},
		{Name: "phpunit/phpunit", Constraint: "^12.0"},
	}

	updated, err := updateManifestRequirements(original, specs, false)
	if err != nil {
		t.Fatalf("updateManifestRequirements: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(updated, &doc); err != nil {
		t.Fatalf("updated manifest is invalid JSON: %v\n%s", err, updated)
	}
	if string(doc["description"]) != `"kept"` || !bytes.Contains(doc["config"], []byte("sort-packages")) {
		t.Fatalf("unknown fields were not preserved: %s", updated)
	}
	var prod map[string]string
	if err := json.Unmarshal(doc["require"], &prod); err != nil {
		t.Fatal(err)
	}
	if prod["symfony/console"] != "^7.0" || prod["phpunit/phpunit"] != "^12.0" {
		t.Errorf("require = %+v", prod)
	}
	if _, exists := doc["require-dev"]; exists {
		t.Errorf("empty require-dev field was not removed: %s", updated)
	}
	if len(updated) == 0 || updated[len(updated)-1] != '\n' {
		t.Error("updated manifest should end with a newline")
	}
}

func TestRequireCommandUpdatesDevManifestAndRunsUpdate(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "composer.json")
	if err := os.WriteFile(manifestPath, []byte(`{"name":"acme/app","require":{"acme/tool":"^1.0"}}`), 0o640); err != nil {
		t.Fatal(err)
	}

	var got orchestrator.Options
	restore := swapRequireRunner(func(_ context.Context, opts orchestrator.Options) error {
		got = opts
		body, err := os.ReadFile(manifestPath)
		if err != nil {
			return err
		}
		var doc struct {
			Require    map[string]string `json:"require"`
			RequireDev map[string]string `json:"require-dev"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			return err
		}
		if doc.RequireDev["acme/tool"] != "^2.0" {
			return errors.New("manifest was not updated before resolver ran")
		}
		if _, exists := doc.Require["acme/tool"]; exists {
			return errors.New("package was not moved out of require")
		}
		return nil
	})
	defer restore()

	root := newRootCmd("dev")
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"require", "--dev", "--project", dir, "acme/tool:^2.0"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.ProjectDir != dir || got.NoDev {
		t.Errorf("require options = %+v", got)
	}
	info, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("composer.json mode = %o, want 640", info.Mode().Perm())
	}
}

func TestRequireCommandMutatesSelectedWorkspaceAndUpdatesAtRoot(t *testing.T) {
	rootDir := t.TempDir()
	workspaceDir := filepath.Join(rootDir, "packages", "api")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "composer.json"), []byte(`{"name":"acme/root","workspaces":["packages/*"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	workspaceManifest := filepath.Join(workspaceDir, "composer.json")
	if err := os.WriteFile(workspaceManifest, []byte(`{"name":"acme/api","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	restore := swapRequireRunner(func(_ context.Context, opts orchestrator.Options) error {
		if opts.ProjectDir != rootDir {
			return fmt.Errorf("ProjectDir = %q, want workspace root %q", opts.ProjectDir, rootDir)
		}
		body, err := os.ReadFile(workspaceManifest)
		if err != nil {
			return err
		}
		var doc struct {
			Require map[string]string `json:"require"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			return err
		}
		if doc.Require["psr/log"] != "^3.0" {
			return fmt.Errorf("workspace require = %+v", doc.Require)
		}
		return nil
	})
	defer restore()

	cmd := newRootCmd("dev")
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"require", "--project", workspaceDir, "psr/log:^3.0"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestRequireCommandRollsBackManifestAndLockOnFailure(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "composer.json")
	lockPath := filepath.Join(dir, "gomposer.lock")
	originalManifest := []byte("{\n  \"name\": \"acme/app\"\n}\n")
	originalLock := []byte("old lock\n")
	if err := os.WriteFile(manifestPath, originalManifest, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, originalLock, 0o600); err != nil {
		t.Fatal(err)
	}

	boom := errors.New("resolution failed")
	restore := swapRequireRunner(func(_ context.Context, _ orchestrator.Options) error {
		if err := os.WriteFile(lockPath, []byte("new lock"), 0o644); err != nil {
			return err
		}
		return boom
	})
	defer restore()

	root := newRootCmd("dev")
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"require", "--project", dir, "monolog/monolog:^3.0"})
	if err := root.Execute(); !errors.Is(err, boom) {
		t.Fatalf("Execute error = %v, want %v", err, boom)
	}
	assertFileContent(t, manifestPath, originalManifest)
	assertFileContent(t, lockPath, originalLock)
	if info, err := os.Stat(lockPath); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Errorf("restored lock mode = %o, want 600", info.Mode().Perm())
	}
}

func TestRequireCommandRemovesNewLockOnFailure(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "composer.json")
	original := []byte(`{"name":"acme/app"}`)
	if err := os.WriteFile(manifestPath, original, 0o644); err != nil {
		t.Fatal(err)
	}
	restore := swapRequireRunner(func(_ context.Context, opts orchestrator.Options) error {
		if err := os.WriteFile(filepath.Join(opts.ProjectDir, "gomposer.lock"), []byte("partial"), 0o644); err != nil {
			return err
		}
		return errors.New("boom")
	})
	defer restore()

	root := newRootCmd("dev")
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"require", "--project", dir, "monolog/monolog"})
	if err := root.Execute(); err == nil {
		t.Fatal("Execute succeeded, want failure")
	}
	assertFileContent(t, manifestPath, original)
	if _, err := os.Stat(filepath.Join(dir, "gomposer.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new lock was not removed: %v", err)
	}
}

func assertFileContent(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s = %q, want %q", path, got, want)
	}
}
