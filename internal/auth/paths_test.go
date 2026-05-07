package auth

import (
	"path/filepath"
	"testing"
)

func TestDefaultPathsXDG(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	t.Setenv("XDG_CONFIG_HOME", "/cfg")
	composer, user, err := defaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if composer != filepath.Join("/home/u", ".composer", "auth.json") {
		t.Errorf("composer = %q", composer)
	}
	if user != filepath.Join("/cfg", "composer-go", "auth.json") {
		t.Errorf("user = %q", user)
	}
}

func TestDefaultPathsXDGUnset(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	t.Setenv("XDG_CONFIG_HOME", "")
	_, user, err := defaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if user != filepath.Join("/home/u", ".config", "composer-go", "auth.json") {
		t.Errorf("user = %q", user)
	}
}
