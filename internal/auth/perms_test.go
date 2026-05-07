package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPermissionWarningOnUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("perm warnings are Unix-only")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	warn := warnIfInsecurePermissions(path)
	if warn == "" {
		t.Errorf("expected a warning for 0644 file; got empty string")
	}
}

func TestNoWarningForSafePerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("perm warnings are Unix-only")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if w := warnIfInsecurePermissions(path); w != "" {
		t.Errorf("unexpected warning for 0600: %q", w)
	}
}

func TestNoWarningForMissingFile(t *testing.T) {
	if w := warnIfInsecurePermissions(filepath.Join(t.TempDir(), "absent")); w != "" {
		t.Errorf("expected silence for missing file; got %q", w)
	}
}
