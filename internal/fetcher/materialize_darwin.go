//go:build darwin

package fetcher

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// CloneOrCopy copies src to dst, preferring APFS clonefile, then hardlink,
// then byte-for-byte copy. On macOS clonefile shares the underlying APFS
// extents until either side is mutated — effectively free for installs
// that only ever read vendor/.
//
// Errors from clonefile that mean "not supported on this filesystem" are
// swallowed and we fall through. Unexpected errors are returned as-is.
func CloneOrCopy(src, dst string) error {
	// 1. clonefile.
	switch err := unix.Clonefile(src, dst, 0); {
	case err == nil:
		return nil
	case errors.Is(err, unix.ENOTSUP), errors.Is(err, unix.EXDEV),
		errors.Is(err, unix.ENOSYS), errors.Is(err, unix.EINVAL),
		errors.Is(err, os.ErrExist):
		// fall through
	default:
		return err
	}

	// 2. hardlink.
	if err := os.Link(src, dst); err == nil {
		return nil
	}

	// 3. copy.
	return copyFileBytes(src, dst)
}
