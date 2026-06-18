package fetcher

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/torstendittmann/gomposer/internal/registry"
)

// markerName is the per-package file we drop at the target root recording
// the sha of the zip last successfully extracted there. On a warm re-run
// we read it, compare against the locked sha, and skip the zip walk on
// match. The leading dot keeps it out of most autoloader scans.
const markerName = ".gomposer-sha"

// Materialize expands the stored zip for pv into target. The target
// directory is created if missing and pre-existing contents are
// overwritten file-by-file (callers that want a clean directory should
// remove it first).
//
// Warm-vendor fast path: if target/<markerName> already exists and its
// contents equal pv.Dist.Sha, we return without opening the zip. The
// marker is (re)written at the end of every successful real extract.
//
// Composer dists usually wrap their contents in a single top-level
// directory whose name we cannot predict. We detect that case (every entry
// shares the same first path component) and strip it.
//
// File contents are written via the platform-specific copy strategy
// chain — see materialize_{darwin,linux,other}.go. Symlinks are not yet
// honored; they are skipped with a warning. Directory entries create the
// directory; regular files are written through copyFile.
func (f *Fetcher) Materialize(ctx context.Context, pv registry.PackageVersion, target string) error {
	sha := pv.Dist.Sha
	if sha == "" {
		return fmt.Errorf("fetcher: %s: cannot materialize without sha (call Fetch first)", pv.Name)
	}
	if existing, err := os.ReadFile(filepath.Join(target, markerName)); err == nil && string(existing) == sha {
		return nil
	}
	if !f.store.Has(sha) {
		return fmt.Errorf("fetcher: %s: not in store (sha %s)", pv.Name, sha)
	}

	src := f.store.Path(sha)
	zr, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("fetcher: %s: open zip: %w", pv.Name, err)
	}
	defer zr.Close()

	strip := commonPrefix(zr.File)

	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("fetcher: %s: mkdir target: %w", pv.Name, err)
	}

	for _, ze := range zr.File {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel := strings.TrimPrefix(ze.Name, strip)
		if rel == "" {
			continue
		}
		// Path traversal guard: the cleaned, target-rooted path must remain
		// inside target.
		dst := filepath.Join(target, filepath.FromSlash(rel))
		if !strings.HasPrefix(dst, filepath.Clean(target)+string(os.PathSeparator)) && dst != filepath.Clean(target) {
			return fmt.Errorf("fetcher: %s: zip entry %q escapes target", pv.Name, ze.Name)
		}

		if ze.FileInfo().IsDir() {
			if err := os.MkdirAll(dst, ze.Mode().Perm()|0o700); err != nil {
				return fmt.Errorf("fetcher: %s: mkdir %s: %w", pv.Name, dst, err)
			}
			continue
		}
		if ze.Mode()&os.ModeSymlink != 0 {
			// TODO(stage 2): honour symlinks if any real package needs them.
			continue
		}

		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("fetcher: %s: mkdir parent: %w", pv.Name, err)
		}
		if err := writeZipEntry(ze, dst); err != nil {
			return fmt.Errorf("fetcher: %s: write %s: %w", pv.Name, rel, err)
		}
	}
	if err := os.WriteFile(filepath.Join(target, markerName), []byte(sha), 0o644); err != nil {
		return fmt.Errorf("fetcher: %s: write marker: %w", pv.Name, err)
	}
	return nil
}

// commonPrefix returns the single top-level directory component shared by
// every entry in files, with a trailing slash, or "" if no such prefix
// exists. It treats the zip as flat when even one entry lacks a slash.
func commonPrefix(files []*zip.File) string {
	if len(files) == 0 {
		return ""
	}
	first := files[0].Name
	slash := strings.IndexByte(first, '/')
	if slash < 0 {
		return ""
	}
	prefix := first[:slash+1]
	for _, ze := range files[1:] {
		if !strings.HasPrefix(ze.Name, prefix) {
			return ""
		}
	}
	return prefix
}

// writeZipEntry copies ze into dst. We always write through the temp +
// rename pattern so a panicked extraction never leaves a half-written file
// in vendor/.
func writeZipEntry(ze *zip.File, dst string) error {
	rc, err := ze.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, ze.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// errFallthrough is returned by platform fast-path implementations to
// signal "not supported on this filesystem, try the next strategy."
var errFallthrough = errors.New("fetcher: strategy not supported, falling through")

// copyFileBytes is the universal-fallback step of CloneOrCopy. It is
// intentionally unexported and platform-agnostic.
func copyFileBytes(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}
