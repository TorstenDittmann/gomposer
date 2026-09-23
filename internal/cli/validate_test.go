package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
