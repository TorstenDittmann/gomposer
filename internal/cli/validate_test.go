package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/torstendittmann/gomposer/internal/lock"
)

func executeValidate(t *testing.T, args ...string) (string, error) {
	t.Helper()
	flagQuiet = false
	flagVerbose = false
	var out strings.Builder
	cmd := newRootCmd("dev")
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"validate"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func TestValidateValidManifest(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "composer.json"), `{
  "name": "acme/app",
  "description": "Demo",
  "license": "MIT",
  "require": { "php": "^8.1" }
}`)
	out, err := executeValidate(t, "--project", dir)
	if err != nil {
		t.Fatalf("validate: %v\n%s", err, out)
	}
	if !strings.Contains(out, "is valid") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestValidatePublishAndWarnings(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "composer.json"), `{
  "name": "acme/app",
  "require": { "psr/log": "*" }
}`)
	out, err := executeValidate(t, "--project", dir)
	if !errors.Is(err, errValidateErrors) {
		t.Fatalf("error = %v, want errValidateErrors\n%s", err, out)
	}
	if ExitCode(err) != 2 {
		t.Fatalf("exit = %d, want 2", ExitCode(err))
	}
	if !strings.Contains(out, "description") || !strings.Contains(out, "unbound version") {
		t.Fatalf("output:\n%s", out)
	}

	out, err = executeValidate(t, "--project", dir, "--no-check-publish")
	if err != nil {
		t.Fatalf("validate --no-check-publish: %v\n%s", err, out)
	}
	out, err = executeValidate(t, "--project", dir, "--no-check-publish", "--strict")
	if !errors.Is(err, errValidateWarnings) {
		t.Fatalf("error = %v, want warnings\n%s", err, out)
	}
	if ExitCode(err) != 1 {
		t.Fatalf("exit = %d, want 1", ExitCode(err))
	}
}

func TestValidateInvalidJSONAndMissing(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "composer.json"), `{bad`)
	out, err := executeValidate(t, "--project", dir)
	if !errors.Is(err, errValidateErrors) {
		t.Fatalf("error = %v\n%s", err, out)
	}
	if !strings.Contains(out, "valid JSON") {
		t.Fatalf("output:\n%s", out)
	}

	_, err = executeValidate(t, "--project", filepath.Join(dir, "missing"))
	if !errors.Is(err, errValidateMissing) {
		t.Fatalf("error = %v, want missing", err)
	}
	if ExitCode(err) != 3 {
		t.Fatalf("exit = %d, want 3", ExitCode(err))
	}
}

func TestValidateLockOutOfDate(t *testing.T) {
	dir := t.TempDir()
	manifest := []byte(`{
  "name": "acme/app",
  "description": "Demo",
  "license": "MIT",
  "require": { "psr/log": "^3.0" }
}`)
	writeFile(t, filepath.Join(dir, "composer.json"), string(manifest))
	sum := sha256.Sum256([]byte(`{"name":"acme/app"}`))
	writeDumpAutoloadLock(t, dir, &lock.File{
		ManifestContentHash: "sha256:" + hex.EncodeToString(sum[:]),
		Packages: []lock.Package{{
			Name: "psr/log", Version: "1.0.0",
		}},
	})
	out, err := executeValidate(t, "--project", dir)
	if !errors.Is(err, errValidateErrors) {
		t.Fatalf("error = %v\n%s", err, out)
	}
	if !strings.Contains(out, "lock file is not up to date") {
		t.Fatalf("missing hash warning:\n%s", out)
	}
	if !strings.Contains(out, "does not satisfy") {
		t.Fatalf("missing constraint mismatch:\n%s", out)
	}

	out, err = executeValidate(t, "--project", dir, "--no-check-lock")
	if err != nil {
		t.Fatalf("--no-check-lock: %v\n%s", err, out)
	}
}

func TestValidateLockFresh(t *testing.T) {
	dir := t.TempDir()
	manifest := []byte(`{
  "name": "acme/app",
  "description": "Demo",
  "license": "MIT",
  "require": { "psr/log": "^3.0" }
}`)
	writeFile(t, filepath.Join(dir, "composer.json"), string(manifest))
	sum := sha256.Sum256(manifest)
	writeDumpAutoloadLock(t, dir, &lock.File{
		ManifestContentHash: "sha256:" + hex.EncodeToString(sum[:]),
		Packages: []lock.Package{{
			Name: "psr/log", Version: "3.0.2",
		}},
	})
	out, err := executeValidate(t, "--project", dir)
	if err != nil {
		t.Fatalf("validate: %v\n%s", err, out)
	}
}

func TestValidateNoDevLockSkipsRequireDev(t *testing.T) {
	dir := t.TempDir()
	manifest := []byte(`{
  "name": "acme/app",
  "description": "Demo",
  "license": "MIT",
  "require": { "psr/log": "^3.0" },
  "require-dev": { "phpunit/phpunit": "^10" }
}`)
	writeFile(t, filepath.Join(dir, "composer.json"), string(manifest))
	sum := sha256.Sum256(manifest)
	writeDumpAutoloadLock(t, dir, &lock.File{
		ManifestContentHash: "sha256:" + hex.EncodeToString(sum[:]),
		Packages: []lock.Package{{
			Name: "psr/log", Version: "3.0.2",
		}},
		// packagesDev omitted — same shape as `gomposer update --no-dev`
	})
	out, err := executeValidate(t, "--project", dir)
	if err != nil {
		t.Fatalf("validate no-dev lock: %v\n%s", err, out)
	}
}

func TestValidateMissingManifestHash(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "composer.json"), `{
  "name": "acme/app",
  "description": "Demo",
  "license": "MIT",
  "require": {}
}`)
	writeDumpAutoloadLock(t, dir, &lock.File{
		ManifestContentHash: "",
	})
	out, err := executeValidate(t, "--project", dir)
	if !errors.Is(err, errValidateErrors) {
		t.Fatalf("error = %v\n%s", err, out)
	}
	if !strings.Contains(out, "missing manifestContentHash") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestValidateInvalidLicense(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "composer.json"), `{
  "name": "acme/app",
  "description": "Demo",
  "license": false,
  "require": {}
}`)
	out, err := executeValidate(t, "--project", dir)
	if !errors.Is(err, errValidateErrors) {
		t.Fatalf("error = %v\n%s", err, out)
	}
	if !strings.Contains(out, "license") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestValidateWorkspaceGlobWarningAndMemberRequire(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "composer.json"), `{
  "name": "acme/root",
  "description": "Demo",
  "license": "MIT",
  "workspaces": ["packages/*", "missing/*"],
  "require": {}
}`)
	writeFile(t, filepath.Join(dir, "packages", "shared", "composer.json"), `{
  "name": "acme/shared",
  "require": { "psr/log": "^3.0" }
}`)
	manifestBytes, err := os.ReadFile(filepath.Join(dir, "composer.json"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(manifestBytes)
	writeDumpAutoloadLock(t, dir, &lock.File{
		ManifestContentHash: "sha256:" + hex.EncodeToString(sum[:]),
		Packages: []lock.Package{
			{Name: "acme/shared", Version: "1.0.0", Type: "workspace", Source: lock.Source{Type: "path", URL: "packages/shared"}},
		},
	})
	out, err := executeValidate(t, "--project", dir)
	if !errors.Is(err, errValidateErrors) {
		t.Fatalf("error = %v\n%s", err, out)
	}
	if !strings.Contains(out, `pattern "missing/*" matched no directories`) {
		t.Fatalf("missing glob warning:\n%s", out)
	}
	if !strings.Contains(out, `Required package "psr/log" is missing`) {
		t.Fatalf("missing workspace require check:\n%s", out)
	}

	out, err = executeValidate(t, "--project", dir, "--no-check-lock", "--strict")
	if !errors.Is(err, errValidateWarnings) {
		t.Fatalf("strict glob warning: %v\n%s", err, out)
	}
}
