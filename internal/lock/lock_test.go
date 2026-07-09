package lock

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestFileRoundTripsThroughEncodeDecode(t *testing.T) {
	orig := &File{
		Readme:      []string{"line one", "line two"},
		ContentHash: "abc123",
		Packages: []Package{{
			Name:            "acme/lib",
			Version:         "1.0.0",
			Type:            "library",
			Source:          Source{Type: "git", URL: "https://example.com/acme/lib.git", Reference: "deadbeef"},
			Dist:            Dist{Type: "zip", URL: "https://example.com/acme/lib.zip", Reference: "deadbeef", Shasum: "cafebabe"},
			Require:         map[string]string{"php": ">=8.1"},
			Autoload:        map[string]any{"psr-4": map[string]string{"Acme\\Lib\\": "src/"}},
			NotificationURL: "https://packagist.org/downloads/",
			Time:            "2026-05-01T00:00:00+00:00",
		}},
		PackagesDev:      []Package{},
		Aliases:          []Alias{{Package: "acme/lib", Version: "9999999-dev", Alias: "1.x-dev"}},
		MinimumStability: "stable",
		StabilityFlags:   map[string]int{"acme/lib": 5},
		PreferStable:     true,
		PreferLowest:     false,
		Platform:         map[string]string{"php": ">=8.1"},
		PlatformDev:      map[string]string{},
		PluginAPIVersion: "2.6.0",
	}
	data, err := orig.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	// Encode again and compare byte-for-byte to prove determinism.
	back, err := got.Encode()
	if err != nil {
		t.Fatalf("re-Encode: %v", err)
	}
	if !bytes.Equal(data, back) {
		t.Errorf("round-trip not byte-stable")
	}
}

// TestEncodedShapeMatchesComposer verifies the JSON key names and
// structure follow Composer's on-disk shape (hyphenated keys, packages/
// packages-dev/aliases at top level, per-package notification-url).
func TestEncodedShapeMatchesComposer(t *testing.T) {
	f := &File{
		ContentHash: "hash",
		Packages: []Package{{
			Name: "a/b", Version: "1.0.0",
			Dist: Dist{Type: "zip", URL: "https://example.com/x.zip", Shasum: "abc"},
		}},
		MinimumStability: "stable",
		StabilityFlags:   map[string]int{},
		Platform:         map[string]string{},
		PlatformDev:      map[string]string{},
	}
	data, err := f.Encode()
	if err != nil {
		t.Fatal(err)
	}
	// Decode into an anonymous map so we can assert key names.
	var top map[string]any
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"content-hash", "packages", "packages-dev", "aliases", "minimum-stability", "stability-flags", "prefer-stable", "prefer-lowest", "platform", "platform-dev"} {
		if _, ok := top[want]; !ok {
			t.Errorf("top-level key %q missing", want)
		}
	}
	pkgs, ok := top["packages"].([]any)
	if !ok || len(pkgs) != 1 {
		t.Fatalf("packages: %#v", top["packages"])
	}
	pkg := pkgs[0].(map[string]any)
	dist, ok := pkg["dist"].(map[string]any)
	if !ok {
		t.Fatalf("dist: %#v", pkg["dist"])
	}
	if _, ok := dist["shasum"]; !ok {
		t.Errorf("dist.shasum missing (should NOT be 'sha256')")
	}
}
