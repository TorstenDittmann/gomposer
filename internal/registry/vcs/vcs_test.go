package vcs

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/torstendittmann/gomposer/internal/manifest"
	"github.com/torstendittmann/gomposer/internal/registry"
)

// makeBareRepoMulti creates a bare repo with the given files committed on
// each ref. Map key is ref ("refs/heads/main", "refs/tags/v1.0.0"); value is
// the composer.json bytes for that ref.
func makeBareRepoMulti(t *testing.T, refs map[string]string) string {
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
	// Process heads first so main exists, then branches, then tags.
	processRef := func(ref, body string) {
		switch {
		case strings.HasPrefix(ref, "refs/heads/"):
			branch := strings.TrimPrefix(ref, "refs/heads/")
			if branch != "main" {
				mustRun(work, "checkout", "-q", "-b", branch)
			}
			_ = writeFileBytes(filepath.Join(work, "composer.json"), []byte(body))
			mustRun(work, "add", ".")
			mustRun(work, "commit", "-q", "-m", "ref "+ref)
			mustRun(work, "checkout", "-q", "main")
		case strings.HasPrefix(ref, "refs/tags/"):
			tag := strings.TrimPrefix(ref, "refs/tags/")
			_ = writeFileBytes(filepath.Join(work, "composer.json"), []byte(body))
			mustRun(work, "add", ".")
			// Use --allow-empty in case content is identical to a prior commit.
			cmd := exec.Command("git", "commit", "--allow-empty", "-q", "-m", "tag "+tag)
			cmd.Dir = work
			cmd.Env = append(cmd.Environ(),
				"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t",
				"GIT_COMMITTER_EMAIL=t@x", "HOME="+root)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("git commit for tag %s: %v\n%s", tag, err, out)
			}
			mustRun(work, "tag", tag)
		}
	}
	// Heads first (deterministic ordering)
	for ref, body := range refs {
		if strings.HasPrefix(ref, "refs/heads/") {
			processRef(ref, body)
		}
	}
	for ref, body := range refs {
		if strings.HasPrefix(ref, "refs/tags/") {
			processRef(ref, body)
		}
	}
	mustRun(root, "clone", "-q", "--bare", work, bare)
	return "file://" + bare
}

func TestClientReportsPackageName(t *testing.T) {
	url := makeBareRepo(t, `{"name":"acme/widget","require":{"php":">=8.0"}}`)
	cacheRoot := t.TempDir()
	c, err := New(Config{URL: url, CacheRoot: filepath.Join(cacheRoot, "vcs")})
	if err != nil {
		t.Fatal(err)
	}
	name, err := c.PackageName(context.Background())
	if err != nil {
		t.Fatalf("PackageName: %v", err)
	}
	if name != "acme/widget" {
		t.Errorf("name = %q", name)
	}
}

func TestClientLookupEnumeratesTagsAndBranches(t *testing.T) {
	url := makeBareRepoMulti(t, map[string]string{
		"refs/heads/main":  `{"name":"acme/widget","require":{"php":">=8.0"}}`,
		"refs/tags/v1.0.0": `{"name":"acme/widget","require":{"php":">=8.0"}}`,
		"refs/tags/v1.1.0": `{"name":"acme/widget","require":{"php":">=8.0"}}`,
	})
	c, err := New(Config{URL: url, CacheRoot: filepath.Join(t.TempDir(), "vcs")})
	if err != nil {
		t.Fatal(err)
	}
	md, err := c.Lookup(context.Background(), "acme/widget")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	got := map[string]bool{}
	for _, v := range md.Versions {
		got[v.Version] = true
	}
	for _, want := range []string{"1.0.0", "1.1.0", "dev-main"} {
		if !got[want] {
			t.Errorf("missing version %q in %+v", want, got)
		}
	}
}

func TestClientLookupWrongPackageName(t *testing.T) {
	url := makeBareRepoMulti(t, map[string]string{
		"refs/heads/main": `{"name":"acme/widget"}`,
	})
	c, _ := New(Config{URL: url, CacheRoot: filepath.Join(t.TempDir(), "vcs")})
	_, err := c.Lookup(context.Background(), "other/lib")
	if err == nil {
		t.Fatal("expected ErrPackageNotFound for wrong name")
	}
	if !errors.Is(err, registry.ErrPackageNotFound) {
		t.Errorf("err = %v, want ErrPackageNotFound", err)
	}
}

func TestClientExposesBranchAliasVersion(t *testing.T) {
	url := makeBareRepoMulti(t, map[string]string{
		"refs/heads/main": `{
			"name":"acme/widget",
			"require":{"php":">=8.0"},
			"extra":{"branch-alias":{"dev-main":"1.x-dev"}}
		}`,
	})
	c, _ := New(Config{URL: url, CacheRoot: filepath.Join(t.TempDir(), "vcs")})
	md, err := c.Lookup(context.Background(), "acme/widget")
	if err != nil {
		t.Fatal(err)
	}
	var sawDevMain, sawAlias bool
	for _, v := range md.Versions {
		if v.Version == "dev-main" {
			sawDevMain = true
		}
		if v.Version == "1.x-dev" {
			sawAlias = true
		}
	}
	if !sawDevMain || !sawAlias {
		t.Errorf("expected both dev-main and 1.x-dev rows, got dev-main=%v alias=%v", sawDevMain, sawAlias)
	}
}

func TestClientLookupCachesNegativeName(t *testing.T) {
	url := makeBareRepoMulti(t, map[string]string{
		"refs/heads/main": `{"name":"acme/widget"}`,
	})
	c, _ := New(Config{URL: url, CacheRoot: filepath.Join(t.TempDir(), "vcs")})
	if _, err := c.PackageName(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Move the bare repo aside; subsequent Lookup for the wrong name must
	// still return ErrPackageNotFound (no fresh git invocations needed).
	if err := os.RemoveAll(c.mirrorDir); err != nil {
		t.Fatal(err)
	}
	_, err := c.Lookup(context.Background(), "other/lib")
	if !errors.Is(err, registry.ErrPackageNotFound) {
		t.Fatalf("err = %v, want ErrPackageNotFound", err)
	}
}

func TestNewFromManifest(t *testing.T) {
	url := makeBareRepoMulti(t, map[string]string{
		"refs/heads/main": `{"name":"acme/widget"}`,
	})
	root := t.TempDir()
	clients, err := NewFromManifest([]manifest.Repository{
		{Type: "vcs", URL: url},
	}, Options{CacheRoot: filepath.Join(root, "vcs")})
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 1 {
		t.Fatalf("clients len = %d", len(clients))
	}
	name, err := clients[0].PackageName(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if name != "acme/widget" {
		t.Errorf("name = %q", name)
	}
}

func TestNewFromManifestSkipsUnsupported(t *testing.T) {
	root := t.TempDir()
	clients, err := NewFromManifest([]manifest.Repository{
		{Type: "composer", URL: "https://x"}, // unsupported, must error
	}, Options{CacheRoot: filepath.Join(root, "vcs")})
	if err == nil {
		t.Fatalf("expected error for composer-type, got %d clients", len(clients))
	}
}

func TestLiveLookupPublicRepo(t *testing.T) {
	if os.Getenv("GOMPOSER_LIVE_NETWORK") != "1" {
		t.Skip("set GOMPOSER_LIVE_NETWORK=1 to run")
	}
	c, err := New(Config{
		URL:       "https://github.com/Seldaek/monolog.git",
		CacheRoot: filepath.Join(t.TempDir(), "vcs"),
	})
	if err != nil {
		t.Fatal(err)
	}
	name, err := c.PackageName(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if name != "monolog/monolog" {
		t.Errorf("name = %q", name)
	}
	md, err := c.Lookup(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	if len(md.Versions) < 5 {
		t.Errorf("want >=5 versions, got %d", len(md.Versions))
	}
}
