package auth

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleAuthJSON = `{
  "http-basic": {
    "private.example.com": { "username": "alice", "password": "s3cret" }
  },
  "bearer": {
    "private.example.com": "BEARER_TOK"
  },
  "github-oauth": {
    "github.com": "ghp_aaa"
  },
  "gitlab-token": {
    "gitlab.example.com": { "username": "u", "token": "glt_x" }
  },
  "gitlab-oauth": {
    "gitlab.com": "glo_y"
  }
}`

func TestParseFileHappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte(sampleAuthJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := parseFile(path)
	if err != nil {
		t.Fatalf("parseFile: %v", err)
	}
	if u := f.HTTPBasic["private.example.com"].Username; u != "alice" {
		t.Errorf("http-basic username = %q, want alice", u)
	}
	if f.Bearer["private.example.com"] != "BEARER_TOK" {
		t.Errorf("bearer token mismatch")
	}
	if f.GitHubOAuth["github.com"] != "ghp_aaa" {
		t.Errorf("github-oauth token mismatch")
	}
	if f.GitLabToken["gitlab.example.com"].Token != "glt_x" {
		t.Errorf("gitlab-token mismatch")
	}
	if f.GitLabOAuth["gitlab.com"] != "glo_y" {
		t.Errorf("gitlab-oauth mismatch")
	}
}

func TestLoadOptionalMissingFile(t *testing.T) {
	f, err := loadOptional(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("loadOptional missing: %v", err)
	}
	if f == nil {
		t.Fatal("loadOptional returned nil for missing file; want zero-valued struct")
	}
	if len(f.HTTPBasic)+len(f.Bearer)+len(f.GitHubOAuth)+len(f.GitLabToken)+len(f.GitLabOAuth) != 0 {
		t.Errorf("expected empty maps; got %+v", f)
	}
}

func TestParseFileMalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	_ = os.WriteFile(path, []byte("{not json"), 0o600)
	_, err := parseFile(path)
	if err == nil {
		t.Fatal("expected error on malformed JSON")
	}
}
