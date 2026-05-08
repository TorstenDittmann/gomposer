package orchestrator

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/torstendittmann/composer-go/internal/constraint"
	"github.com/torstendittmann/composer-go/internal/lock"
	"github.com/torstendittmann/composer-go/internal/platform"
	"github.com/torstendittmann/composer-go/internal/registry"
)

func mustVer(t *testing.T, s string) constraint.Version {
	t.Helper()
	v, err := constraint.ParseVersion(s)
	if err != nil {
		t.Fatalf("ParseVersion(%q): %v", s, err)
	}
	return v
}

func TestEvaluatePlatformWarningsDefaultMode(t *testing.T) {
	pf := &platform.Platform{PHPVersion: mustVer(t, "8.2.0")}
	pkgs := []lock.Package{
		{Name: "acme/x", Version: "1.0.0", Require: map[string]string{"php": "^7.4"}},
	}
	var stderr bytes.Buffer
	warnings, err := evaluatePlatformWarnings(pkgs, pf, nil, false /*noDev*/, false /*quiet*/, &stderr)
	if err != nil {
		t.Fatalf("evaluatePlatformWarnings: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %+v", warnings)
	}
	if !strings.Contains(warnings[0], "acme/x") || !strings.Contains(warnings[0], "php") {
		t.Errorf("warning text: %q", warnings[0])
	}
	if !strings.Contains(stderr.String(), "acme/x") {
		t.Errorf("stderr did not contain warning: %q", stderr.String())
	}
}

func TestEvaluatePlatformWarningsNoDevFails(t *testing.T) {
	pf := &platform.Platform{PHPVersion: mustVer(t, "8.2.0")}
	pkgs := []lock.Package{
		{Name: "acme/x", Version: "1.0.0", Require: map[string]string{"php": "^7.4"}},
	}
	var stderr bytes.Buffer
	_, err := evaluatePlatformWarnings(pkgs, pf, nil, true /*noDev*/, false, &stderr)
	if err == nil {
		t.Error("expected error in --no-dev mode")
	}
}

func TestEvaluatePlatformWarningsIgnoreFlag(t *testing.T) {
	pf := &platform.Platform{PHPVersion: mustVer(t, "8.2.0")}
	pkgs := []lock.Package{
		{Name: "acme/x", Version: "1.0.0", Require: map[string]string{"php": "^7.4"}},
	}
	ignore := map[string]bool{"php": true}
	var stderr bytes.Buffer
	warnings, err := evaluatePlatformWarnings(pkgs, pf, ignore, true /*noDev*/, false, &stderr)
	if err != nil {
		t.Fatalf("ignored req should not fail: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings should be empty: %+v", warnings)
	}
}

func TestEvaluatePlatformWarningsQuiet(t *testing.T) {
	pf := &platform.Platform{PHPVersion: mustVer(t, "8.2.0")}
	pkgs := []lock.Package{
		{Name: "acme/x", Version: "1.0.0", Require: map[string]string{"php": "^7.4"}},
	}
	var stderr bytes.Buffer
	warnings, _ := evaluatePlatformWarnings(pkgs, pf, nil, false, true /*quiet*/, &stderr)
	if stderr.Len() != 0 {
		t.Errorf("--quiet should suppress stderr; got %q", stderr.String())
	}
	if len(warnings) != 1 {
		t.Errorf("warnings should still be recorded for the lockfile: %+v", warnings)
	}
}

func TestEvaluatePlatformWarningsLibStarOnce(t *testing.T) {
	pf := &platform.Platform{PHPVersion: mustVer(t, "8.2.0")}
	pkgs := []lock.Package{
		{Name: "a/x", Require: map[string]string{"lib-curl": ">=7.0"}},
		{Name: "a/y", Require: map[string]string{"lib-icu": ">=70"}},
	}
	var stderr bytes.Buffer
	warnings, err := evaluatePlatformWarnings(pkgs, pf, nil, false, false, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	libCount := 0
	for _, w := range warnings {
		if strings.Contains(w, "lib-*") {
			libCount++
		}
	}
	if libCount != 1 {
		t.Errorf("expected exactly one coalesced lib-* warning; got %d in %+v", libCount, warnings)
	}
}

func TestVerbosePrintsTimingBlock(t *testing.T) {
	// Capture stderr.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()

	dir := t.TempDir()
	manifestBytes := []byte(`{"name":"vendor/root","require":{"a/a":"^1.0"}}`)
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), manifestBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	opts := Options{
		ProjectDir:   dir,
		Verbose:      true,
		Fetcher:      &fakeFetcher{},
		Materializer: &fakeMaterializer{},
		Autoloader:   &fakeAutoloader{},
		Source: &fakeSource{pkgs: map[string]*registry.PackageMetadata{
			"a/a": {Name: "a/a", Versions: []registry.PackageVersion{{
				Name: "a/a", Version: "1.0.0", VersionNorm: "1.0.0.0",
				Dist: registry.Dist{Type: "zip", URL: "x", Sha: "deadbeef"},
			}}},
		}},
		NoScripts: true,
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install: %v", err)
	}

	w.Close()
	out, _ := io.ReadAll(r)
	got := string(out)

	for _, want := range []string{
		"composer-go: timing",
		"read manifest",
		"resolve",
		"fetch",
		"materialize",
		"autoload",
		"write lock",
		"-------- total",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("verbose output missing %q in:\n%s", want, got)
		}
	}
}

func TestQuietSuppressesTimingBlock(t *testing.T) {
	r, w, _ := os.Pipe()
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()

	dir := t.TempDir()
	manifestBytes := []byte(`{"name":"vendor/root","require":{"a/a":"^1.0"}}`)
	os.WriteFile(filepath.Join(dir, "composer.json"), manifestBytes, 0o644)

	opts := Options{
		ProjectDir:   dir,
		Verbose:      true,
		Quiet:        true,
		Fetcher:      &fakeFetcher{},
		Materializer: &fakeMaterializer{},
		Autoloader:   &fakeAutoloader{},
		Source: &fakeSource{pkgs: map[string]*registry.PackageMetadata{
			"a/a": {Name: "a/a", Versions: []registry.PackageVersion{{
				Name: "a/a", Version: "1.0.0", VersionNorm: "1.0.0.0",
				Dist: registry.Dist{Type: "zip", URL: "x", Sha: "deadbeef"},
			}}},
		}},
		NoScripts: true,
	}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install: %v", err)
	}
	w.Close()
	out, _ := io.ReadAll(r)
	if strings.Contains(string(out), "composer-go: timing") {
		t.Errorf("quiet+verbose should suppress timing, got:\n%s", out)
	}
}
