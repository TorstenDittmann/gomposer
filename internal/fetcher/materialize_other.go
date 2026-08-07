//go:build !darwin && !linux

package fetcher

// CloneOrCopy uses a byte-for-byte copy where no reflink primitive is
// available. Hardlinks are unsafe because projects may modify vendor files.
func CloneOrCopy(src, dst string) error {
	return copyFileBytes(src, dst)
}
