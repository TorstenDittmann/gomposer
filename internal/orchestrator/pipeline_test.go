package orchestrator

import (
	"bytes"
	"strings"
	"testing"

	"github.com/torstendittmann/composer-go/internal/constraint"
	"github.com/torstendittmann/composer-go/internal/lock"
	"github.com/torstendittmann/composer-go/internal/platform"
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
