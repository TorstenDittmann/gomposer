package platform

import (
	"testing"

	"github.com/torstendittmann/composer-go/internal/constraint"
)

func newTestPlatform(t *testing.T) *Platform {
	t.Helper()
	v, _ := constraint.ParseVersion("8.2.14")
	return &Platform{
		PHPVersion: v,
		Extensions: map[string]constraint.Version{
			"json":     {}, // loaded, no version
			"mbstring": {},
			"openssl":  mustVer(t, "3.1.4"),
		},
	}
}

func TestCheckPHPSatisfied(t *testing.T) {
	p := newTestPlatform(t)
	v := Check(map[string]string{"php": "^8.0"}, p, nil)
	if len(v) != 0 {
		t.Errorf("expected no violations, got %+v", v)
	}
}

func TestCheckPHPUnsatisfied(t *testing.T) {
	p := newTestPlatform(t)
	v := Check(map[string]string{"php": "^7.4"}, p, nil)
	if len(v) != 1 {
		t.Fatalf("violations = %+v", v)
	}
	if v[0].Req != "php" || v[0].Kind != ViolationVersion {
		t.Errorf("got %+v", v[0])
	}
}

func TestCheckExtensionMissing(t *testing.T) {
	p := newTestPlatform(t)
	v := Check(map[string]string{"ext-curl": "*"}, p, nil)
	if len(v) != 1 || v[0].Req != "ext-curl" || v[0].Kind != ViolationMissing {
		t.Errorf("got %+v", v)
	}
}

func TestCheckExtensionPresentWildcardOK(t *testing.T) {
	p := newTestPlatform(t)
	v := Check(map[string]string{"ext-json": "*"}, p, nil)
	if len(v) != 0 {
		t.Errorf("got %+v", v)
	}
}

func TestCheckExtensionPresentSpecificVersionUnknown(t *testing.T) {
	p := newTestPlatform(t)
	// ext-json reports empty version; a specific version constraint cannot
	// be evaluated -> treated as unsatisfied.
	v := Check(map[string]string{"ext-json": "^7.0"}, p, nil)
	if len(v) != 1 || v[0].Kind != ViolationVersion {
		t.Errorf("got %+v", v)
	}
}

func TestCheckExtensionVersionSatisfied(t *testing.T) {
	p := newTestPlatform(t)
	v := Check(map[string]string{"ext-openssl": "^3.0"}, p, nil)
	if len(v) != 0 {
		t.Errorf("got %+v", v)
	}
}

func TestCheckIgnoreSet(t *testing.T) {
	p := newTestPlatform(t)
	v := Check(map[string]string{"php": "^7.4"}, p, map[string]bool{"php": true})
	if len(v) != 0 {
		t.Errorf("ignored req should not produce violation, got %+v", v)
	}
}

func TestCheckLibStarIgnoredWithFlag(t *testing.T) {
	p := newTestPlatform(t)
	v := Check(map[string]string{"lib-curl": ">=7.0"}, p, nil)
	if len(v) != 1 || v[0].Kind != ViolationLibIgnored || v[0].Req != "lib-curl" {
		t.Errorf("got %+v", v)
	}
}

func TestIsPlatformReq(t *testing.T) {
	cases := map[string]bool{
		"php": true, "ext-json": true, "lib-curl": true,
		"php-64bit": true, "vendor/pkg": false, "ext-": true,
		// Package names whose vendor begins with a platform prefix must not
		// be classified as platform reqs. The `/` is the discriminator —
		// real platform reqs never contain it.
		"php-http/discovery":      false,
		"php-amqplib/php-amqplib": false,
		"ext-something/pkg":       false,
		"lib-foo/bar":             false,
	}
	for k, want := range cases {
		if got := IsPlatformReq(k); got != want {
			t.Errorf("IsPlatformReq(%q) = %v, want %v", k, got, want)
		}
	}
}
