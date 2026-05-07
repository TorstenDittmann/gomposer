package auth

import (
	"fmt"
	"os"
	"runtime"
)

// warnIfInsecurePermissions inspects path and returns a non-empty warning
// string if the file is world- or group-readable on Unix. Empty string
// means "no concern" (file missing, on Windows, or restricted to owner).
//
// Callers route the returned message through their logger.
func warnIfInsecurePermissions(path string) string {
	if runtime.GOOS == "windows" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	mode := info.Mode().Perm()
	if mode&0o077 != 0 {
		return fmt.Sprintf("auth: %s has permissions %#o; recommend `chmod 600 %s`", path, mode, path)
	}
	return ""
}
