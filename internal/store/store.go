// Package store is a content-addressed blob store. It does not understand
// zips or any other format; it stores opaque byte streams keyed by sha256.
//
// Layout:   <root>/<sha256>.zip
// Atomic:   writes go to <sha256>.zip.tmp, fsync, rename into place.
//
// The .zip suffix is convention only — the store does not validate format.
// It chose this suffix because the only producer in this project is the
// fetcher and every artifact it stores is a Composer dist zip.
package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Store is a content-addressed blob store rooted at a single directory.
// It is safe for concurrent use: Put writes via tmp-then-rename, and Has
// and OpenReader only read.
type Store struct {
	root string
}

// New opens (or creates) a store rooted at root. The directory is created
// with 0o755 if missing.
func New(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("store: root must be non-empty")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("store: mkdir %s: %w", root, err)
	}
	return &Store{root: root}, nil
}

// Path returns the absolute path of the artifact for sha. The file may or
// may not exist; callers should check Has first.
func (s *Store) Path(sha string) string {
	return filepath.Join(s.root, sha+".zip")
}

// Has reports whether sha is present in the store.
func (s *Store) Has(sha string) bool {
	_, err := os.Stat(s.Path(sha))
	return err == nil
}

// Put writes the bytes of r into the store under sha. It is the caller's
// responsibility to ensure the bytes hash to sha; the store does not
// re-verify (the fetcher hashes during streaming and refuses to call Put
// unless the hash matches).
//
// On success the file at Path(sha) exists with permission 0o644. On any
// error the .tmp scratch file is removed.
func (s *Store) Put(sha string, r io.Reader) (retErr error) {
	final := s.Path(sha)
	tmp := final + ".tmp"

	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("store: open tmp: %w", err)
	}
	defer func() {
		if retErr != nil {
			_ = os.Remove(tmp)
		}
	}()

	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return fmt.Errorf("store: copy: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("store: fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("store: close: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("store: rename: %w", err)
	}
	return nil
}

// OpenReader returns a reader over the stored artifact. The caller must
// Close it. Returns an error wrapping os.ErrNotExist on a miss so callers
// can branch with errors.Is.
func (s *Store) OpenReader(sha string) (io.ReadCloser, error) {
	f, err := os.Open(s.Path(sha))
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", sha, err)
	}
	return f, nil
}
