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

func TestParseAutoload(t *testing.T) {
	input := []byte(`{
		"name": "vendor/pkg",
		"autoload": {
			"psr-4": { "App\\": "src/" },
			"files": ["src/helpers.php"],
			"classmap": ["legacy/"]
		},
		"autoload-dev": {
			"psr-4": { "App\\Tests\\": "tests/" }
		}
	}`)
	m, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := m.Autoload.PSR4["App\\"]; got != "src/" {
		t.Errorf("PSR4[App\\] = %q, want src/", got)
	}
	if len(m.Autoload.Files) != 1 || m.Autoload.Files[0] != "src/helpers.php" {
		t.Errorf("Files = %v, want [src/helpers.php]", m.Autoload.Files)
	}
	if len(m.Autoload.Classmap) != 1 || m.Autoload.Classmap[0] != "legacy/" {
		t.Errorf("Classmap = %v, want [legacy/]", m.Autoload.Classmap)
	}
	if got := m.AutoloadDev.PSR4["App\\Tests\\"]; got != "tests/" {
		t.Errorf("AutoloadDev.PSR4[App\\Tests\\] = %q, want tests/", got)
	}
}

func TestParseStability(t *testing.T) {
	input := []byte(`{
		"name": "vendor/pkg",
		"minimum-stability": "beta",
		"prefer-stable": true
	}`)
	m, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.MinimumStability != "beta" {
		t.Errorf("MinimumStability = %q, want beta", m.MinimumStability)
	}
	if !m.PreferStable {
		t.Errorf("PreferStable = false, want true")
	}
}

func TestParseStabilityDefaults(t *testing.T) {
	input := []byte(`{ "name": "vendor/pkg" }`)
	m, _ := Parse(input)
	if m.MinimumStability != "" {
		t.Errorf("MinimumStability = %q, want \"\" (caller picks default)", m.MinimumStability)
	}
	if m.PreferStable {
		t.Errorf("PreferStable = true, want false")
	}
}
