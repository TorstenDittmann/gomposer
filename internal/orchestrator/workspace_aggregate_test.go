package orchestrator

import (
	"strings"
	"testing"

	"github.com/torstendittmann/gomposer/internal/manifest"
)

func mkWorkspace(name, version string, require map[string]string) manifest.Workspace {
	return manifest.Workspace{
		Name:    name,
		Version: version,
		Dir:     "packages/" + strings.TrimPrefix(name, "acme/"),
		Manifest: &manifest.Manifest{
			Name:    name,
			Version: version,
			Require: require,
		},
	}
}

func TestBuildAggregateManifestUnionsExternalRequires(t *testing.T) {
	root := &manifest.Manifest{
		Name:    "acme/monorepo",
		Require: map[string]string{"psr/log": "^3.0"},
	}
	ws := []manifest.Workspace{
		mkWorkspace("acme/shared", "1.0.0", nil),
		mkWorkspace("acme/api", "", map[string]string{
			"symfony/console": "^6.0",
			"acme/shared":     "workspace:^1.0",
		}),
	}
	agg, err := BuildAggregateManifest(root, ws, true /* includeDev */)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, ok := agg.Require["acme/shared"]; ok {
		t.Errorf("workspace: require should be stripped from aggregate")
	}
	if agg.Require["psr/log"] != "^3.0" {
		t.Errorf("Require[psr/log] = %q", agg.Require["psr/log"])
	}
	if agg.Require["symfony/console"] != "^6.0" {
		t.Errorf("Require[symfony/console] = %q", agg.Require["symfony/console"])
	}
}

func TestBuildAggregateManifestRejectsUnknownWorkspaceTarget(t *testing.T) {
	root := &manifest.Manifest{Name: "acme/monorepo"}
	ws := []manifest.Workspace{
		mkWorkspace("acme/api", "", map[string]string{"acme/nope": "workspace:*"}),
	}
	_, err := BuildAggregateManifest(root, ws, true)
	if err == nil {
		t.Fatal("expected error on unknown workspace target")
	}
	if !strings.Contains(err.Error(), "not found in workspace set") {
		t.Errorf("err = %v", err)
	}
}

func TestBuildAggregateManifestRejectsVersionMismatch(t *testing.T) {
	root := &manifest.Manifest{Name: "acme/monorepo"}
	ws := []manifest.Workspace{
		mkWorkspace("acme/shared", "1.0.0", nil),
		mkWorkspace("acme/api", "", map[string]string{"acme/shared": "workspace:^2.0"}),
	}
	_, err := BuildAggregateManifest(root, ws, true)
	if err == nil {
		t.Fatal("expected version mismatch error")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Errorf("err = %v", err)
	}
}

func TestBuildAggregateManifestRejectsWorkspaceWithoutVersion(t *testing.T) {
	root := &manifest.Manifest{Name: "acme/monorepo"}
	ws := []manifest.Workspace{
		mkWorkspace("acme/shared", "", nil), // no version
		mkWorkspace("acme/api", "", map[string]string{"acme/shared": "workspace:^1.0"}),
	}
	_, err := BuildAggregateManifest(root, ws, true)
	if err == nil {
		t.Fatal("expected error on workspace without version")
	}
	if !strings.Contains(err.Error(), "no version") {
		t.Errorf("err = %v", err)
	}
}

func TestBuildAggregateManifestAllowsWorkspaceStarWithNoVersion(t *testing.T) {
	// workspace:* never checks version, so a version-less workspace is OK.
	root := &manifest.Manifest{Name: "acme/monorepo"}
	ws := []manifest.Workspace{
		mkWorkspace("acme/shared", "", nil),
		mkWorkspace("acme/api", "", map[string]string{"acme/shared": "workspace:*"}),
	}
	if _, err := BuildAggregateManifest(root, ws, true); err != nil {
		t.Errorf("workspace:* with no version should be OK: %v", err)
	}
}

func TestBuildAggregateManifestExcludesDevWhenAsked(t *testing.T) {
	root := &manifest.Manifest{Name: "acme/monorepo"}
	ws := []manifest.Workspace{
		{
			Name: "acme/api", Dir: "apps/api",
			Manifest: &manifest.Manifest{
				Name:       "acme/api",
				Require:    map[string]string{"psr/log": "^3.0"},
				RequireDev: map[string]string{"phpunit/phpunit": "^10.0"},
			},
		},
	}
	agg, err := BuildAggregateManifest(root, ws, false /* includeDev */)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := agg.Require["phpunit/phpunit"]; ok {
		t.Errorf("dev require leaked to aggregate under includeDev=false")
	}
	if agg.Require["psr/log"] != "^3.0" {
		t.Errorf("Require[psr/log] = %q", agg.Require["psr/log"])
	}
}
