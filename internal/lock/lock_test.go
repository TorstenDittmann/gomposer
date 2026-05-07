package lock

import (
	"bytes"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	in := &File{
		SchemaVersion:       1,
		Generator:           Generator{Name: "composer-go", Version: "0.0.0-test"},
		ManifestContentHash: "sha256:abc",
		PlatformFingerprint: "php-8.2.0;ext-mbstring",
		Stability:           Stability{MinimumStability: "stable", PreferStable: true},
		Packages: []Package{{
			Name:    "monolog/monolog",
			Version: "3.5.0",
			Source:  Source{Type: "git", URL: "https://github.com/Seldaek/monolog.git", Ref: "abc123"},
			Dist:    Dist{Type: "zip", URL: "https://api.github.com/repos/Seldaek/monolog/zipball/abc123", Sha256: "sha256:deadbeef"},
			Require: map[string]string{"php": ">=8.1"},
		}},
	}

	data, err := in.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	out, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if out.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", out.SchemaVersion)
	}
	if len(out.Packages) != 1 || out.Packages[0].Name != "monolog/monolog" {
		t.Errorf("Packages mismatch after round trip: %+v", out.Packages)
	}
	if out.Packages[0].Dist.Sha256 != "sha256:deadbeef" {
		t.Errorf("Dist.Sha256 lost in round trip")
	}

	again, _ := in.Encode()
	if !bytes.Equal(data, again) {
		t.Errorf("Encode is not deterministic")
	}
}

func TestDecodeRejectsUnknownSchema(t *testing.T) {
	data := []byte(`{"schemaVersion": 99}`)
	_, err := Decode(data)
	if err == nil {
		t.Errorf("Decode should reject schemaVersion=99")
	}
}
