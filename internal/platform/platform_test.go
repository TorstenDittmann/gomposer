package platform

import (
	"errors"
	"strings"
	"testing"

	"github.com/torstendittmann/gomposer/internal/constraint"
)

func TestProbeReturnsCachedResult(t *testing.T) {
	resetProbeCacheForTests()
	t.Cleanup(resetProbeCacheForTests)

	calls := 0
	originalRunProbe := runProbe
	t.Cleanup(func() { runProbe = originalRunProbe })
	runProbe = func() (*Platform, error) {
		calls++
		v, _ := constraint.ParseVersion("8.2.14")
		return &Platform{
			PHPVersion: v,
			Extensions: map[string]constraint.Version{"json": {}, "mbstring": {}},
		}, nil
	}

	p1, err := Probe()
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	p2, err := Probe()
	if err != nil {
		t.Fatalf("Probe (cached): %v", err)
	}
	if p1 != p2 {
		t.Errorf("Probe should return cached pointer; got %p vs %p", p1, p2)
	}
	if calls != 1 {
		t.Errorf("runProbe called %d times, want 1", calls)
	}
}

func TestProbeMissingPHPErrorHints(t *testing.T) {
	resetProbeCacheForTests()
	t.Cleanup(resetProbeCacheForTests)

	originalRunProbe := runProbe
	t.Cleanup(func() { runProbe = originalRunProbe })
	runProbe = func() (*Platform, error) {
		return nil, errors.New("platform: php executable not found: hint: brew install php apt install php-cli")
	}

	_, err := Probe()
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "brew install php") {
		t.Errorf("missing brew hint in %q", msg)
	}
	if !strings.Contains(msg, "apt install php-cli") {
		t.Errorf("missing apt hint in %q", msg)
	}
}

func TestFingerprintShape(t *testing.T) {
	v, _ := constraint.ParseVersion("8.2.14")
	p := &Platform{
		PHPVersion: v,
		Extensions: map[string]constraint.Version{
			"json":     {},
			"mbstring": {},
			"openssl":  mustVer(t, "3.1.4"),
		},
	}
	got := p.Fingerprint()
	want := "php-8.2.14;ext-json;ext-mbstring;ext-openssl@3.1.4"
	if got != want {
		t.Errorf("Fingerprint = %q, want %q", got, want)
	}
}

func TestNilPlatformFingerprint(t *testing.T) {
	var p *Platform
	if got := p.Fingerprint(); got != "php-unknown" {
		t.Errorf("Fingerprint(nil) = %q, want php-unknown", got)
	}
}

func mustVer(t *testing.T, s string) constraint.Version {
	t.Helper()
	v, err := constraint.ParseVersion(s)
	if err != nil {
		t.Fatalf("ParseVersion(%q): %v", s, err)
	}
	return v
}
