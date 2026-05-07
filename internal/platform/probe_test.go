package platform

import "testing"

func TestParseProbeOutputBasic(t *testing.T) {
	in := []byte(`{"php":"8.2.14","ext":{"json":"","mbstring":"","openssl":"3.1.4"}}`)
	p, err := parseProbeOutput(in)
	if err != nil {
		t.Fatalf("parseProbeOutput: %v", err)
	}
	if p.PHPVersion.Original != "8.2.14" {
		t.Errorf("PHPVersion.Original = %q", p.PHPVersion.Original)
	}
	if _, ok := p.Extensions["json"]; !ok {
		t.Errorf("ext-json missing")
	}
	if v := p.Extensions["openssl"]; v.Original != "3.1.4" {
		t.Errorf("openssl version = %q", v.Original)
	}
}

func TestParseProbeOutputEmptyExtVersion(t *testing.T) {
	in := []byte(`{"php":"8.3.0","ext":{"core":""}}`)
	p, err := parseProbeOutput(in)
	if err != nil {
		t.Fatalf("parseProbeOutput: %v", err)
	}
	v, ok := p.Extensions["core"]
	if !ok {
		t.Fatalf("ext-core missing")
	}
	// Empty version means "any version present"; we represent that with
	// the zero Version. Callers must treat this as a wildcard.
	if v.Original != "" {
		t.Errorf("expected empty version, got %q", v.Original)
	}
}

func TestParseProbeOutputMalformed(t *testing.T) {
	if _, err := parseProbeOutput([]byte("not json")); err == nil {
		t.Error("expected parse error")
	}
}
