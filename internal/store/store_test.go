package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestPutThenHasThenOpen(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	payload := []byte("hello, store")
	sum := sha256Hex(payload)

	if s.Has(sum) {
		t.Fatalf("Has on cold store returned true")
	}

	if err := s.Put(sum, bytes.NewReader(payload)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if !s.Has(sum) {
		t.Fatalf("Has after Put returned false")
	}

	rc, err := s.OpenReader(sum)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("round-trip mismatch: got %q, want %q", got, payload)
	}
}

func TestPutIsAtomic(t *testing.T) {
	// After a successful Put, no .tmp file should remain in the store dir.
	dir := t.TempDir()
	s, _ := New(dir)
	payload := []byte("x")
	if err := s.Put(sha256Hex(payload), bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("residual tmp file after Put: %s", e.Name())
		}
	}
}

func TestOpenReaderMissReturnsError(t *testing.T) {
	s, _ := New(t.TempDir())
	_, err := s.OpenReader("0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("expected error on miss")
	}
}
