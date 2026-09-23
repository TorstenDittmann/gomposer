package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func executeInit(t *testing.T, args ...string) (string, error) {
	t.Helper()
	flagQuiet = false
	var out strings.Builder
	cmd := newRootCmd("dev")
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"init"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func TestInitWritesComposerJSON(t *testing.T) {
	dir := t.TempDir()
	out, err := executeInit(t,
		"--project", dir,
		"--name", "acme/demo",
		"--description", "Demo package",
		"--author", "Ada Lovelace <ada@example.com>",
		"--type", "library",
		"--license", "MIT",
		"--homepage", "https://example.com",
		"--require", "psr/log:^3.0",
		"--require-dev", "phpunit/phpunit=^10",
		"--stability", "dev",
		"--autoload", "src",
	)
	if err != nil {
		t.Fatalf("init: %v\n%s", out, err)
	}
	if !strings.Contains(out, "Writing") {
		t.Fatalf("missing write line:\n%s", out)
	}
	if !strings.Contains(out, `namespace Acme\Demo`) {
		t.Fatalf("missing PSR-4 tip:\n%s", out)
	}

	body, err := os.ReadFile(filepath.Join(dir, "composer.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("json: %v\n%s", err, body)
	}
	if doc["name"] != "acme/demo" {
		t.Fatalf("name = %v", doc["name"])
	}
	if doc["description"] != "Demo package" {
		t.Fatalf("description = %v", doc["description"])
	}
	require := doc["require"].(map[string]any)
	if require["psr/log"] != "^3.0" {
		t.Fatalf("require = %#v", require)
	}
	requireDev := doc["require-dev"].(map[string]any)
	if requireDev["phpunit/phpunit"] != "^10" {
		t.Fatalf("require-dev = %#v", requireDev)
	}
	autoload := doc["autoload"].(map[string]any)["psr-4"].(map[string]any)
	if autoload[`Acme\Demo\`] != "src/" {
		t.Fatalf("autoload = %#v", autoload)
	}
	if _, err := os.Stat(filepath.Join(dir, "src")); err != nil {
		t.Fatalf("src dir: %v", err)
	}

	_, err = executeInit(t, "--project", dir, "--name", "acme/demo")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected already exists, got %v", err)
	}
}

func TestInitExistingManifestDoesNotCreateAutoloadDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"name":"acme/existing"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := executeInit(t, "--project", dir, "--name", "acme/demo", "--autoload", "src")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected already exists, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "src")); !os.IsNotExist(err) {
		t.Fatalf("autoload dir should not be created when manifest exists, stat err = %v", err)
	}
}

func TestInitRejectsInvalidName(t *testing.T) {
	dir := t.TempDir()
	_, err := executeInit(t, "--project", dir, "--name", "BadName")
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("error = %v", err)
	}
}

func TestInitDefaultName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "My_App")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("USER", "Alice")
	out, err := executeInit(t, "--project", dir)
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	body, err := os.ReadFile(filepath.Join(dir, "composer.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"name": "alice/my-app"`) {
		t.Fatalf("body:\n%s", body)
	}
}

func TestPascalCaseNamespace(t *testing.T) {
	if got := psr4Namespace("acme/demo-pkg"); got != `Acme\DemoPkg\` {
		t.Fatalf("got %q", got)
	}
}

func TestParseInitRequirementForms(t *testing.T) {
	cases := map[string]requirementSpec{
		"psr/log:^3.0":      {Name: "psr/log", Constraint: "^3.0"},
		"psr/log=^3.0":      {Name: "psr/log", Constraint: "^3.0"},
		"psr/log ^3.0":      {Name: "psr/log", Constraint: "^3.0"},
		"ext-json:*":        {Name: "ext-json", Constraint: "*"},
	}
	for input, want := range cases {
		got, err := parseInitRequirement(input)
		if err != nil {
			t.Fatalf("%q: %v", input, err)
		}
		if got != want {
			t.Fatalf("%q: got %#v want %#v", input, got, want)
		}
	}
}
