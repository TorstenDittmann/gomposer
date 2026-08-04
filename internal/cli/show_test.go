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

func seedShowProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{
  "name": "acme/app",
  "require": {"acme/root": "^2.0"},
  "require-dev": {"acme/test": "^1.0"}
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &lock.File{
		SchemaVersion: lock.SchemaVersion,
		Packages: []lock.Package{
			{Name: "psr/log", Version: "3.0.2", Type: "library"},
			{Name: "acme/root", Version: "2.1.0", Type: "library", Source: lock.Source{Type: "git", URL: "https://example.test/root.git", Ref: "abc123"}, Dist: lock.Dist{Type: "zip", URL: "https://example.test/root.zip", Sha256: "deadbeef"}, Require: map[string]string{"psr/log": "^3.0", "php": "^8.2"}},
		},
		PackagesDev: []lock.Package{
			{Name: "acme/test", Version: "1.4.0", Type: "library", Require: map[string]string{"psr/log": "^3.0"}},
		},
	}
	body, err := f.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gomposer.lock"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func executeShow(t *testing.T, args ...string) (string, error) {
	t.Helper()
	flagNoDev = false
	flagQuiet = false
	var out bytes.Buffer
	cmd := newRootCmd("dev")
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"show"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func TestShowListsLockedPackagesSortedWithScope(t *testing.T) {
	dir := seedShowProject(t)
	out, err := executeShow(t, "--project", dir)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	root := strings.Index(out, "acme/root")
	testPkg := strings.Index(out, "acme/test")
	psr := strings.Index(out, "psr/log")
	if root < 0 || testPkg < 0 || psr < 0 || !(root < testPkg && testPkg < psr) {
		t.Fatalf("packages not sorted:\n%s", out)
	}
	for _, want := range []string{"2.1.0", "direct", "dev", "1.4.0", "3.0.2"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestShowDirectFiltersTransitives(t *testing.T) {
	dir := seedShowProject(t)
	out, err := executeShow(t, "--project", dir, "--direct")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "acme/root") || !strings.Contains(out, "acme/test") || strings.Contains(out, "psr/log") {
		t.Fatalf("direct output:\n%s", out)
	}
}

func TestShowNoDevExcludesDevelopmentPackages(t *testing.T) {
	dir := seedShowProject(t)
	out, err := executeShow(t, "--project", dir, "--no-dev")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "acme/test") || !strings.Contains(out, "acme/root") {
		t.Fatalf("--no-dev output:\n%s", out)
	}
}

func TestShowPackageDetails(t *testing.T) {
	dir := seedShowProject(t)
	out, err := executeShow(t, "--project", dir, "acme/root")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"name", "acme/root", "version", "2.1.0", "source", "https://example.test/root.git", "dist", "deadbeef", "psr/log", "^3.0", "php", "^8.2"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail missing %q:\n%s", want, out)
		}
	}
}

func TestShowTreeRendersResolvedDependencies(t *testing.T) {
	dir := seedShowProject(t)
	out, err := executeShow(t, "--project", dir, "--tree")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "acme/root 2.1.0") || !strings.Contains(out, "└── psr/log 3.0.2 (^3.0)") {
		t.Fatalf("tree output:\n%s", out)
	}
	if strings.Contains(out, "php ^8.2") {
		t.Fatalf("unlocked platform requirement rendered as package:\n%s", out)
	}
}

func TestShowJSONIsStructuredAndDeterministic(t *testing.T) {
	dir := seedShowProject(t)
	out, err := executeShow(t, "--project", dir, "--format=json")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Packages []struct {
			Name      string            `json:"name"`
			Direct    bool              `json:"direct"`
			Dev       bool              `json:"dev"`
			Requires  map[string]string `json:"requires"`
			Workspace bool              `json:"workspace"`
		} `json:"packages"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if len(result.Packages) != 3 || result.Packages[0].Name != "acme/root" || !result.Packages[0].Direct || result.Packages[0].Dev {
		t.Fatalf("JSON packages = %+v", result.Packages)
	}
	if result.Packages[0].Requires["psr/log"] != "^3.0" {
		t.Errorf("requires = %+v", result.Packages[0].Requires)
	}
}

func TestShowTreeJSONHandlesCycles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"name":"acme/app","require":{"acme/a":"*"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &lock.File{SchemaVersion: lock.SchemaVersion, Packages: []lock.Package{
		{Name: "acme/a", Version: "1.0.0", Require: map[string]string{"acme/b": "^1.0"}},
		{Name: "acme/b", Version: "1.0.0", Require: map[string]string{"acme/a": "^1.0"}},
	}}
	body, err := f.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gomposer.lock"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := executeShow(t, "--project", dir, "--tree", "--format=json")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Tree []showTreeNode `json:"tree"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid tree JSON: %v\n%s", err, out)
	}
	if len(result.Tree) != 1 || len(result.Tree[0].Dependencies) != 1 || len(result.Tree[0].Dependencies[0].Dependencies) != 1 || !result.Tree[0].Dependencies[0].Dependencies[0].Cycle {
		t.Fatalf("cycle tree = %+v", result.Tree)
	}
}

func TestShowWorkspaceUsesRootLockAndSelectedManifestForDirect(t *testing.T) {
	rootDir := t.TempDir()
	workspaceDir := filepath.Join(rootDir, "packages", "api")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "composer.json"), []byte(`{"name":"acme/root","workspaces":["packages/*"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "composer.json"), []byte(`{"name":"acme/api","version":"1.0.0","require":{"acme/shared":"workspace:*"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &lock.File{SchemaVersion: lock.SchemaVersion, Packages: []lock.Package{
		{Name: "acme/api", Version: "1.0.0", Type: "workspace"},
		{Name: "acme/shared", Version: "1.0.0", Type: "workspace"},
		{Name: "psr/log", Version: "3.0.2"},
	}}
	body, err := f.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "gomposer.lock"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := executeShow(t, "--project", workspaceDir, "--direct")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "acme/shared") || !strings.Contains(out, "workspace") || strings.Contains(out, "psr/log") || strings.Contains(out, "acme/api") {
		t.Fatalf("workspace direct output:\n%s", out)
	}
}

func TestShowRejectsInvalidModesAndUnknownPackage(t *testing.T) {
	dir := seedShowProject(t)
	if _, err := executeShow(t, "--project", dir, "--format=yaml"); err == nil {
		t.Fatal("invalid format succeeded")
	}
	if _, err := executeShow(t, "--project", dir, "--direct", "acme/root"); err == nil {
		t.Fatal("package plus --direct succeeded")
	}
	if _, err := executeShow(t, "--project", dir, "unknown/pkg"); err == nil || !strings.Contains(err.Error(), "not present") {
		t.Fatalf("unknown package error = %v", err)
	}
}
