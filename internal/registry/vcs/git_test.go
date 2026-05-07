package vcs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeBareRepo creates a bare git repo with one commit on `main` containing
// the given composer.json bytes. Returns the file:// URL of the bare repo.
func makeBareRepo(t *testing.T, manifest string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	work := filepath.Join(root, "work")
	bare := filepath.Join(root, "bare.git")
	mustRun := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t",
			"GIT_COMMITTER_EMAIL=t@x", "HOME="+root)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	mustRun(root, "init", "-q", "-b", "main", work)
	if err := writeFile(filepath.Join(work, "composer.json"), manifest); err != nil {
		t.Fatal(err)
	}
	mustRun(work, "add", ".")
	mustRun(work, "commit", "-q", "-m", "init")
	mustRun(root, "clone", "-q", "--bare", work, bare)
	return "file://" + bare
}

func writeFile(path, body string) error {
	return writeFileBytes(path, []byte(body))
}

func writeFileBytes(path string, b []byte) error {
	return os.WriteFile(path, b, 0o644)
}

func TestLsRemoteAndShow(t *testing.T) {
	url := makeBareRepo(t, `{"name":"acme/widget","require":{"php":">=8.0"}}`)
	root := t.TempDir()
	bare := filepath.Join(root, "mirror.git")
	g := Git{}
	if err := g.CloneMirror(context.Background(), url, bare); err != nil {
		t.Fatalf("CloneMirror: %v", err)
	}
	refs, err := g.LsRemote(context.Background(), bare)
	if err != nil {
		t.Fatalf("LsRemote: %v", err)
	}
	var sawMain bool
	for _, r := range refs {
		if strings.HasSuffix(r.Name, "refs/heads/main") || r.Name == "main" {
			sawMain = true
		}
	}
	if !sawMain {
		t.Errorf("expected refs/heads/main in %+v", refs)
	}
	body, err := g.Show(context.Background(), bare, "main", "composer.json")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if !strings.Contains(string(body), `"acme/widget"`) {
		t.Errorf("Show body = %q", body)
	}
}
