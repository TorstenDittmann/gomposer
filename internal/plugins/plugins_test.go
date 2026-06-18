package plugins

import (
	"bytes"
	"strings"
	"testing"

	"github.com/torstendittmann/gomposer/internal/lock"
	"github.com/torstendittmann/gomposer/internal/manifest"
)

func TestInspectDetectsComposerPlugin(t *testing.T) {
	f := &lock.File{Packages: []lock.Package{
		{Name: "phpstan/extension-installer", Version: "1.4.0", Type: "composer-plugin"},
		{Name: "psr/log", Version: "3.0.0", Type: "library"},
	}}
	got := Inspect(f, &manifest.Manifest{})
	if len(got) != 1 {
		t.Fatalf("len(warnings) = %d, want 1: %+v", len(got), got)
	}
	if got[0].Name != "phpstan/extension-installer" {
		t.Errorf("Name = %q", got[0].Name)
	}
	if got[0].Type != "composer-plugin" {
		t.Errorf("Type = %q", got[0].Type)
	}
	if !strings.Contains(got[0].Message, "install/update events") {
		t.Errorf("Message did not explain plugin behavior: %q", got[0].Message)
	}
}

func TestInspectDetectsComposerInstaller(t *testing.T) {
	f := &lock.File{Packages: []lock.Package{
		{Name: "composer/installers", Version: "2.3.0", Type: "composer-installer"},
	}}
	got := Inspect(f, &manifest.Manifest{})
	if len(got) != 1 {
		t.Fatalf("len(warnings) = %d, want 1", len(got))
	}
	if !strings.Contains(got[0].Message, "custom install paths") {
		t.Errorf("composer-installer message must mention custom install paths: %q", got[0].Message)
	}
	if !strings.Contains(got[0].Message, "vendor/") {
		t.Errorf("composer-installer message must mention vendor/: %q", got[0].Message)
	}
}

func TestInspectInspectsDevPackages(t *testing.T) {
	f := &lock.File{PackagesDev: []lock.Package{
		{Name: "phpstan/extension-installer", Version: "1.4.0", Type: "composer-plugin"},
	}}
	got := Inspect(f, &manifest.Manifest{})
	if len(got) != 1 {
		t.Fatalf("len(warnings) = %d, want 1", len(got))
	}
}

func TestInspectIgnoresLibraries(t *testing.T) {
	f := &lock.File{Packages: []lock.Package{
		{Name: "psr/log", Version: "3.0.0", Type: "library"},
		{Name: "monolog/monolog", Version: "3.5.0", Type: ""},
	}}
	if got := Inspect(f, &manifest.Manifest{}); len(got) != 0 {
		t.Errorf("expected no warnings, got %+v", got)
	}
}

func TestInspectSuppressedByManifestExtra(t *testing.T) {
	f := &lock.File{Packages: []lock.Package{
		{Name: "phpstan/extension-installer", Version: "1.4.0", Type: "composer-plugin"},
	}}
	m := &manifest.Manifest{Extra: map[string]any{
		"gomposer": map[string]any{
			"suppress-plugin-warnings": true,
		},
	}}
	if got := Inspect(f, m); len(got) != 0 {
		t.Errorf("suppression flag should silence warnings, got %+v", got)
	}
}

func TestInspectSuppressionIgnoresOtherTruthyValues(t *testing.T) {
	// Only the literal boolean true suppresses. "true" the string does not —
	// composer.json has a real bool type and we don't want fuzzy matching.
	f := &lock.File{Packages: []lock.Package{
		{Name: "phpstan/extension-installer", Type: "composer-plugin"},
	}}
	m := &manifest.Manifest{Extra: map[string]any{
		"gomposer": map[string]any{"suppress-plugin-warnings": "true"},
	}}
	if got := Inspect(f, m); len(got) != 1 {
		t.Errorf("string \"true\" should NOT suppress; got %d warnings", len(got))
	}
}

func TestInspectSpecialCasesComposerInstallers(t *testing.T) {
	f := &lock.File{Packages: []lock.Package{
		{Name: "composer/installers", Version: "2.3.0", Type: "composer-installer"},
	}}
	got := Inspect(f, &manifest.Manifest{})
	if len(got) != 1 {
		t.Fatalf("len = %d", len(got))
	}
	// composer/installers is the canonical case. The message should be
	// concrete about WordPress/Drupal-style layouts breaking.
	msg := got[0].Message
	if !strings.Contains(msg, "composer/installers") {
		t.Errorf("special-case message missing package name: %q", msg)
	}
}

func TestRenderProducesOneLinePerWarning(t *testing.T) {
	ws := []Warning{
		{Name: "a/x", Version: "1.0.0", Type: "composer-plugin", Message: "msg-a"},
		{Name: "b/y", Version: "2.0.0", Type: "composer-installer", Message: "msg-b"},
	}
	var buf bytes.Buffer
	Render(&buf, ws)
	out := buf.String()
	if !strings.Contains(out, "a/x@1.0.0") || !strings.Contains(out, "b/y@2.0.0") {
		t.Errorf("Render output missing entries: %q", out)
	}
	if !strings.Contains(out, "msg-a") || !strings.Contains(out, "msg-b") {
		t.Errorf("Render output missing messages: %q", out)
	}
	if !strings.Contains(out, "warning") {
		t.Errorf("Render output should be visibly tagged as a warning: %q", out)
	}
}

func TestRenderEmptyIsNoOp(t *testing.T) {
	var buf bytes.Buffer
	Render(&buf, nil)
	if buf.Len() != 0 {
		t.Errorf("Render(nil) wrote %q, want empty", buf.String())
	}
}
