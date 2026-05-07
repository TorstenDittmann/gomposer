//go:build linux

package fetcher

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// CloneOrCopy copies src to dst, preferring FICLONE (btrfs/xfs reflink),
// then hardlink, then byte-for-byte copy. FICLONE returns EOPNOTSUPP /
// EXDEV on filesystems that don't support reflink — both are treated as
// "fall through to the next strategy."
//
// Implementation note: unix.IoctlFileClone is not present in
// golang.org/x/sys v0.43.0; we use unix.IoctlSetInt(dstFd, unix.FICLONE,
// srcFd) which has identical wire semantics (FICLONE ioctl, arg is the
// source fd). If a future module bump adds IoctlFileClone, prefer it.
func CloneOrCopy(src, dst string) error {
	// 1. FICLONE.
	srcF, err := os.Open(src)
	if err == nil {
		dstF, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err == nil {
			// unix.IoctlFileClone is absent in v0.43.0; fall back to
			// unix.IoctlSetInt which issues the exact same ioctl.
			ioErr := unix.IoctlSetInt(int(dstF.Fd()), unix.FICLONE, int(srcF.Fd()))
			closeErr := dstF.Close()
			_ = srcF.Close()
			if ioErr == nil && closeErr == nil {
				return nil
			}
			// On any error, remove the empty/partial dst and try the next
			// strategy.
			_ = os.Remove(dst)
			if ioErr != nil && !isReflinkUnsupported(ioErr) {
				return ioErr
			}
		} else {
			_ = srcF.Close()
		}
	}

	// 2. hardlink.
	if err := os.Link(src, dst); err == nil {
		return nil
	}

	// 3. copy.
	return copyFileBytes(src, dst)
}

func isReflinkUnsupported(err error) bool {
	return errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.ENOTSUP) ||
		errors.Is(err, unix.EXDEV) ||
		errors.Is(err, unix.EINVAL) ||
		errors.Is(err, unix.ENOSYS)
}
