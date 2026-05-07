package manifest

import (
	"reflect"
	"testing"
)

func TestScriptsSingleString(t *testing.T) {
	data := []byte(`{
		"name": "vendor/pkg",
		"scripts": { "post-install-cmd": "php artisan migrate" }
	}`)
	m, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := m.Scripts["post-install-cmd"]
	want := []string{"php artisan migrate"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Scripts[post-install-cmd] = %v, want %v", got, want)
	}
}

func TestScriptsArray(t *testing.T) {
	data := []byte(`{
		"name": "vendor/pkg",
		"scripts": {
			"post-install-cmd": [
				"@php artisan key:generate",
				"@php artisan storage:link"
			]
		}
	}`)
	m, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := m.Scripts["post-install-cmd"]
	want := []string{"@php artisan key:generate", "@php artisan storage:link"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Scripts[post-install-cmd] = %v, want %v", got, want)
	}
}

func TestScriptsMixedEvents(t *testing.T) {
	data := []byte(`{
		"name": "vendor/pkg",
		"scripts": {
			"pre-install-cmd": "echo before",
			"post-install-cmd": ["echo after-1", "echo after-2"],
			"post-autoload-dump": "App\\Bootstrap::run"
		}
	}`)
	m, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.Scripts) != 3 {
		t.Fatalf("len(Scripts) = %d, want 3: %+v", len(m.Scripts), m.Scripts)
	}
	if got := m.Scripts["pre-install-cmd"]; !reflect.DeepEqual(got, []string{"echo before"}) {
		t.Errorf("pre-install-cmd = %v", got)
	}
	if got := m.Scripts["post-install-cmd"]; !reflect.DeepEqual(got, []string{"echo after-1", "echo after-2"}) {
		t.Errorf("post-install-cmd = %v", got)
	}
	if got := m.Scripts["post-autoload-dump"]; !reflect.DeepEqual(got, []string{"App\\Bootstrap::run"}) {
		t.Errorf("post-autoload-dump = %v", got)
	}
}

func TestScriptsAbsentIsNil(t *testing.T) {
	m, err := Parse([]byte(`{"name":"vendor/pkg"}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Scripts != nil {
		t.Errorf("Scripts = %v, want nil for missing field", m.Scripts)
	}
}

func TestScriptsRejectsNonStringEntry(t *testing.T) {
	data := []byte(`{"scripts": {"x": 42}}`)
	if _, err := Parse(data); err == nil {
		t.Error("Parse should reject numeric script body")
	}
}

func TestScriptsRejectsArrayWithNonString(t *testing.T) {
	data := []byte(`{"scripts": {"x": ["ok", 7]}}`)
	if _, err := Parse(data); err == nil {
		t.Error("Parse should reject array with non-string element")
	}
}
