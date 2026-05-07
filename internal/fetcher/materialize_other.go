//go:build !darwin && !linux

package fetcher

import "os"

// CloneOrCopy on non-APFS, non-Linux platforms tries hardlink first, then
// byte-for-byte copy. There is no reflink primitive available portably.
func CloneOrCopy(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	return copyFileBytes(src, dst)
}
