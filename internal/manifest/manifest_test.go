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

func TestParseRequires(t *testing.T) {
	input := []byte(`{
		"name": "vendor/pkg",
		"require": {
			"php": ">=8.1",
			"monolog/monolog": "^3.0"
		},
		"require-dev": {
			"phpunit/phpunit": "^10.0"
		}
	}`)
	m, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := m.Require["monolog/monolog"]; got != "^3.0" {
		t.Errorf("Require[monolog/monolog] = %q, want ^3.0", got)
	}
	if got := m.Require["php"]; got != ">=8.1" {
		t.Errorf("Require[php] = %q, want >=8.1", got)
	}
	if got := m.RequireDev["phpunit/phpunit"]; got != "^10.0" {
		t.Errorf("RequireDev[phpunit/phpunit] = %q, want ^10.0", got)
	}
}
