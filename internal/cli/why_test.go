package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/torstendittmann/gomposer/internal/lock"
)

func executeWhy(t *testing.T, args ...string) (string, error) {
	t.Helper()
	flagNoDev = false
	flagQuiet = false
	var out bytes.Buffer
	cmd := newRootCmd("dev")
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"why"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func TestWhyShowsImmediateDependent(t *testing.T) {
	dir := seedShowProject(t)
	out, err := executeWhy(t, "--project", dir, "psr/log")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"acme/root", "2.1.0", "requires  psr/log", "(^3.0)", "acme/test", "[dev]"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "acme/app") {
		t.Fatalf("default output included transitive root:\n%s", out)
	}
}

func TestWhyShowsRootAndRecursiveChain(t *testing.T) {
	dir := seedShowProject(t)
	out, err := executeWhy(t, "--project", dir, "acme/root")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "acme/app") || !strings.Contains(out, "requires  acme/root  (^2.0)") {
		t.Fatalf("direct root output:\n%s", out)
	}

	out, err = executeWhy(t, "--project", dir, "--recursive", "psr/log")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "acme/app") || !strings.Contains(out, "acme/root") {
		t.Fatalf("recursive output:\n%s", out)
	}
}

func TestWhyTreeAndJSON(t *testing.T) {
	dir := seedShowProject(t)
	out, err := executeWhy(t, "--project", dir, "--tree", "psr/log")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "psr/log 3.0.2") || !strings.Contains(out, "acme/root 2.1.0 requires ^3.0") || !strings.Contains(out, "acme/app requires ^2.0") {
		t.Fatalf("tree output:\n%s", out)
	}

	out, err = executeWhy(t, "--project", dir, "--format=json", "psr/log")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Package    string    `json:"package"`
		Dependents []whyEdge `json:"dependents"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if result.Package != "psr/log" || len(result.Dependents) != 2 || result.Dependents[0].Name != "acme/root" {
		t.Fatalf("JSON result = %+v", result)
	}
}

func TestWhyNoDevAndPlatformRequirement(t *testing.T) {
	dir := seedShowProject(t)
	out, err := executeWhy(t, "--project", dir, "--no-dev", "psr/log")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "acme/test") || !strings.Contains(out, "acme/root") {
		t.Fatalf("no-dev output:\n%s", out)
	}
	out, err = executeWhy(t, "--project", dir, "php")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "acme/root") || !strings.Contains(out, "^8.2") {
		t.Fatalf("platform output:\n%s", out)
	}
}

func TestWhyTreeHandlesCycles(t *testing.T) {
	dir := t.TempDir()
	writeWhyFile(t, filepath.Join(dir, "composer.json"), `{"name":"acme/app","require":{"acme/a":"*"}}`)
	writeWhyLock(t, dir, &lock.File{SchemaVersion: lock.SchemaVersion, Packages: []lock.Package{
		{Name: "acme/a", Version: "1.0.0", Require: map[string]string{"acme/b": "^1.0"}},
		{Name: "acme/b", Version: "1.0.0", Require: map[string]string{"acme/a": "^1.0"}},
	}})
	out, err := executeWhy(t, "--project", dir, "--tree", "acme/a")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[cycle]") {
		t.Fatalf("cycle output:\n%s", out)
	}
}

func TestWhyWorkspaceMemberExcludesUnrelatedSibling(t *testing.T) {
	rootDir := t.TempDir()
	apiDir := filepath.Join(rootDir, "packages", "api")
	otherDir := filepath.Join(rootDir, "packages", "other")
	if err := os.MkdirAll(apiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeWhyFile(t, filepath.Join(rootDir, "composer.json"), `{"name":"acme/root","workspaces":["packages/*"]}`)
	writeWhyFile(t, filepath.Join(apiDir, "composer.json"), `{"name":"acme/api","version":"1.0.0","require":{"acme/shared":"workspace:*"}}`)
	writeWhyFile(t, filepath.Join(otherDir, "composer.json"), `{"name":"acme/other","version":"1.0.0","require":{"psr/log":"^3.0"}}`)
	sharedDir := filepath.Join(rootDir, "packages", "shared")
	if err := os.MkdirAll(sharedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeWhyFile(t, filepath.Join(sharedDir, "composer.json"), `{"name":"acme/shared","version":"1.0.0","require":{"psr/log":"^3.0"}}`)
	writeWhyLock(t, rootDir, &lock.File{SchemaVersion: lock.SchemaVersion, Packages: []lock.Package{
		{Name: "acme/api", Version: "1.0.0", Type: "workspace"},
		{Name: "acme/other", Version: "1.0.0", Type: "workspace"},
		{Name: "acme/shared", Version: "1.0.0", Type: "workspace"},
		{Name: "psr/log", Version: "3.0.2"},
	}})
	out, err := executeWhy(t, "--project", apiDir, "--recursive", "psr/log")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "acme/shared") || !strings.Contains(out, "acme/api") || strings.Contains(out, "acme/other") {
		t.Fatalf("workspace member output:\n%s", out)
	}
}

func TestWhyRejectsUnknownPackageAndInvalidFormat(t *testing.T) {
	dir := seedShowProject(t)
	if _, err := executeWhy(t, "--project", dir, "unknown/pkg"); err == nil || !strings.Contains(err.Error(), "not present") {
		t.Fatalf("unknown package error = %v", err)
	}
	if _, err := executeWhy(t, "--project", dir, "--format=yaml", "psr/log"); err == nil {
		t.Fatal("invalid format succeeded")
	}
}

func writeWhyFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeWhyLock(t *testing.T, dir string, file *lock.File) {
	t.Helper()
	body, err := file.Encode()
	if err != nil {
		t.Fatal(err)
	}
	writeWhyFile(t, filepath.Join(dir, "gomposer.lock"), string(body))
}
