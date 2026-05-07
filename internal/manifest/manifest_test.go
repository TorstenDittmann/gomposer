package manifest

import (
	"testing"
)

func TestParseMinimal(t *testing.T) {
	input := []byte(`{
		"name": "vendor/pkg",
		"type": "library"
	}`)
	m, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Name != "vendor/pkg" {
		t.Errorf("Name = %q, want vendor/pkg", m.Name)
	}
	if m.Type != "library" {
		t.Errorf("Type = %q, want library", m.Type)
	}
}
