package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/torstendittmann/gomposer/internal/manifest"
	"github.com/torstendittmann/gomposer/internal/registry"
)

type outdatedTestSource map[string][]registry.PackageVersion

func (s outdatedTestSource) Lookup(_ context.Context, name string) (*registry.PackageMetadata, error) {
	versions, ok := s[name]
	if !ok {
		return nil, registry.ErrPackageNotFound
	}
	return &registry.PackageMetadata{Name: name, Versions: versions}, nil
}

func executeOutdated(t *testing.T, source registry.SourceLookup, args ...string) (string, error) {
	t.Helper()
	old := outdatedSourceFn
	outdatedSourceFn = func(*manifest.Manifest) (registry.SourceLookup, error) { return source, nil }
	t.Cleanup(func() { outdatedSourceFn = old })
	flagNoDev, flagQuiet = false, false
	var out bytes.Buffer
	cmd := newRootCmd("dev")
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"outdated"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func TestOutdatedShowsWantedAndLatest(t *testing.T) {
	dir := seedShowProject(t)
	source := outdatedTestSource{
		"acme/root": {{Version: "2.1.0"}, {Version: "2.4.0"}, {Version: "3.0.0"}},
		"acme/test": {{Version: "1.4.0"}},
		"psr/log":   {{Version: "3.0.2"}, {Version: "3.0.3"}},
	}
	out, err := executeOutdated(t, source, "--project", dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"acme/root", "2.1.0", "2.4.0", "3.0.0", "constrained", "psr/log", "3.0.3"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "acme/test") {
		t.Fatalf("up-to-date package shown:\n%s", out)
	}
}

func TestOutdatedDirectNoDevAndJSON(t *testing.T) {
	dir := seedShowProject(t)
	source := outdatedTestSource{
		"acme/root": {{Version: "2.2.0"}},
		"acme/test": {{Version: "1.5.0"}},
		"psr/log":   {{Version: "3.0.3"}},
	}
	out, err := executeOutdated(t, source, "--project", dir, "--direct", "--no-dev", "--format=json")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Packages []outdatedPackage `json:"packages"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if len(result.Packages) != 1 || result.Packages[0].Name != "acme/root" || !result.Packages[0].Direct || result.Packages[0].Dev {
		t.Fatalf("packages = %+v", result.Packages)
	}
}

func TestOutdatedStrictReturnsHandledError(t *testing.T) {
	dir := seedShowProject(t)
	source := outdatedTestSource{
		"acme/root": {{Version: "2.2.0"}},
		"acme/test": {{Version: "1.4.0"}},
		"psr/log":   {{Version: "3.0.2"}},
	}
	out, err := executeOutdated(t, source, "--project", dir, "--strict")
	if err == nil || !isHandled(err) || !strings.Contains(out, "acme/root") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestOutdatedRejectsLookupFailure(t *testing.T) {
	dir := seedShowProject(t)
	source := failingOutdatedSource{}
	_, err := executeOutdated(t, source, "--project", dir)
	if err == nil || !strings.Contains(err.Error(), "lookup") {
		t.Fatalf("error = %v", err)
	}
}

type failingOutdatedSource struct{}

func (failingOutdatedSource) Lookup(_ context.Context, name string) (*registry.PackageMetadata, error) {
	return nil, fmt.Errorf("failed %s", name)
}
