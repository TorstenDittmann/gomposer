package main

import (
	"strings"
	"testing"
)

func TestParseFlagsDefaults(t *testing.T) {
	p, err := parseFlags([]string{"--corpus", "/some/dir"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if p.Corpus != "/some/dir" {
		t.Errorf("Corpus = %q", p.Corpus)
	}
	if p.Runs != 3 {
		t.Errorf("Runs = %d, want 3", p.Runs)
	}
	if len(p.Scenarios) != 3 {
		t.Errorf("Scenarios = %v, want all three", p.Scenarios)
	}
}

func TestParseFlagsCustomScenarios(t *testing.T) {
	p, err := parseFlags([]string{"--corpus", "/x", "--scenarios", "cold,warm"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if len(p.Scenarios) != 2 {
		t.Errorf("Scenarios = %v, want [cold warm]", p.Scenarios)
	}
}

func TestParseFlagsRejectsUnknownScenario(t *testing.T) {
	if _, err := parseFlags([]string{"--corpus", "/x", "--scenarios", "frozen"}); err == nil {
		t.Error("expected error on unknown scenario")
	}
	if _, err := parseFlags([]string{"--corpus", "/x", "--scenarios", "frozen"}); err != nil {
		if !strings.Contains(err.Error(), "frozen") {
			t.Errorf("error should mention 'frozen': %v", err)
		}
	}
}

func TestParseFlagsRequiresCorpus(t *testing.T) {
	if _, err := parseFlags(nil); err == nil {
		t.Error("expected error when --corpus missing")
	}
}
