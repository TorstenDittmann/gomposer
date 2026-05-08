// Package vcs implements registry.SourceLookup backed by `git`. We shell
// out rather than embedding go-git: it keeps the binary small, reuses the
// user's existing SSH and credential-helper setup, and avoids reimplementing
// git's wire protocol.
package vcs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Git is a stateless wrapper around the `git` binary.
type Git struct {
	// Binary is the git executable to invoke; defaults to "git".
	Binary string
}

func (g Git) bin() string {
	if g.Binary != "" {
		return g.Binary
	}
	return "git"
}

// Ref is one row from `git ls-remote`.
type Ref struct {
	SHA  string // 40-hex commit SHA
	Name string // full ref name, e.g. "refs/heads/main" or "refs/tags/v1.2.3"
}

// CloneMirror creates a bare mirror clone at dst. dst must not already exist.
// On success the directory contains a usable bare repository.
func (g Git) CloneMirror(ctx context.Context, url, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("vcs: clone target already exists: %s", dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, g.bin(), "clone", "--mirror", "--quiet", url, dst)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("vcs: clone %s: %w\n%s", url, err, out)
	}
	return nil
}

// Fetch refreshes a bare mirror; idempotent.
func (g Git) Fetch(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, g.bin(), "fetch", "--quiet", "--prune", "--tags", "origin")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("vcs: fetch %s: %w\n%s", dir, err, out)
	}
	return nil
}

// LsRemote enumerates refs in a local bare repo. Symbolic HEAD lines are
// skipped — callers that care about HEAD use HeadBranch instead.
func (g Git) LsRemote(ctx context.Context, dir string) ([]Ref, error) {
	cmd := exec.CommandContext(ctx, g.bin(), "ls-remote", "--heads", "--tags", ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("vcs: ls-remote %s: %w\n%s", dir, err, out)
	}
	var refs []Ref
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			continue
		}
		// Drop "^{}" peeled-tag rows; we treat the tag itself as the ref.
		if strings.HasSuffix(parts[1], "^{}") {
			continue
		}
		refs = append(refs, Ref{SHA: parts[0], Name: parts[1]})
	}
	return refs, nil
}

// HeadBranch returns the default branch name (e.g. "main"). On older gits
// without `symbolic-ref --short HEAD`, falls back to parsing `git remote
// show origin`.
func (g Git) HeadBranch(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, g.bin(), "symbolic-ref", "--short", "HEAD")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	// Fallback: parse remote show.
	cmd = exec.CommandContext(ctx, g.bin(), "remote", "show", "origin")
	cmd.Dir = dir
	out2, err2 := cmd.CombinedOutput()
	if err2 != nil {
		return "", fmt.Errorf("vcs: HEAD: %w\n%s", err, out)
	}
	for _, line := range strings.Split(string(out2), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "HEAD branch:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "HEAD branch:")), nil
		}
	}
	return "", fmt.Errorf("vcs: could not determine HEAD branch in %s", dir)
}

// Archive streams a zip archive of `ref` from the bare repo at dir to w.
// Used when a VCS-source package has no Dist URL (Packagist provides one
// for tagged releases; pure-git refs do not).
func (g Git) Archive(ctx context.Context, dir, ref string, w io.Writer) error {
	cmd := exec.CommandContext(ctx, g.bin(), "archive", "--format=zip", ref)
	cmd.Dir = dir
	var errBuf bytes.Buffer
	cmd.Stdout = w
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("vcs: archive %s: %w\n%s", ref, err, errBuf.String())
	}
	return nil
}

// Show returns the bytes of `path` at `ref` in the bare repo at dir. A
// missing path returns an empty slice and a nil error so callers can handle
// "ref has no composer.json" without inspecting error strings.
func (g Git) Show(ctx context.Context, dir, ref, path string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, g.bin(), "show", ref+":"+path)
	cmd.Dir = dir
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		txt := errBuf.String()
		if strings.Contains(txt, "exists on disk, but not in") ||
			strings.Contains(txt, "does not exist") ||
			strings.Contains(txt, "fatal: path") {
			return nil, nil
		}
		return nil, fmt.Errorf("vcs: show %s:%s: %w\n%s", ref, path, err, txt)
	}
	return out.Bytes(), nil
}
