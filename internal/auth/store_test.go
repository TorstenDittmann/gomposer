package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStoreLookupBasic(t *testing.T) {
	dir := t.TempDir()
	composer := filepath.Join(dir, "composer.json")
	user := filepath.Join(dir, "user.json")
	writeFile(t, composer, `{"http-basic":{"private.example.com":{"username":"a","password":"b"}}}`)

	s, err := loadStore(composer, user)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := s.Lookup("private.example.com")
	if !ok {
		t.Fatal("Lookup miss; want hit")
	}
	if c.Kind != KindHTTPBasic || c.Username != "a" || c.Password != "b" {
		t.Errorf("got %+v", c)
	}
}

func TestStoreUserOverridesComposer(t *testing.T) {
	dir := t.TempDir()
	composer := filepath.Join(dir, "composer.json")
	user := filepath.Join(dir, "user.json")
	writeFile(t, composer, `{"bearer":{"h":"OLD"}}`)
	writeFile(t, user, `{"bearer":{"h":"NEW"}}`)

	s, _ := loadStore(composer, user)
	c, ok := s.Lookup("h")
	if !ok || c.Token != "NEW" {
		t.Errorf("user did not win: ok=%v c=%+v", ok, c)
	}
}

func TestStoreLookupHostNormalisation(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "user.json")
	writeFile(t, user, `{"github-oauth":{"github.com":"ghp_x"}}`)
	s, _ := loadStore("", user)
	if c, ok := s.Lookup("GitHub.com:443"); !ok || c.Kind != KindGitHubOAuth {
		t.Errorf("got %+v ok=%v", c, ok)
	}
}

func TestStoreLookupMiss(t *testing.T) {
	s, _ := loadStore("", "")
	if c, ok := s.Lookup("anywhere"); ok {
		t.Errorf("unexpected hit: %+v", c)
	}
}

func TestStoreAllKindsResolve(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "user.json")
	writeFile(t, user, `{
		"http-basic":{"a":{"username":"u","password":"p"}},
		"bearer":{"b":"BTK"},
		"github-oauth":{"github.com":"GHO"},
		"gitlab-token":{"g":{"username":"gu","token":"GLT"}},
		"gitlab-oauth":{"o":"GLO"}
	}`)
	s, _ := loadStore("", user)
	cases := []struct {
		host string
		want Kind
	}{
		{"a", KindHTTPBasic},
		{"b", KindBearer},
		{"github.com", KindGitHubOAuth},
		{"g", KindGitLabToken},
		{"o", KindGitLabOAuth},
	}
	for _, tc := range cases {
		c, ok := s.Lookup(tc.host)
		if !ok || c.Kind != tc.want {
			t.Errorf("host=%s: ok=%v kind=%s, want %s", tc.host, ok, c.Kind, tc.want)
		}
	}
}

func TestStoreSurfacesPermWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "user.json")
	writeFile(t, path, `{}`)
	_ = os.Chmod(path, 0o644)
	s, _ := loadStore("", path)
	if len(s.Warnings()) == 0 {
		t.Error("expected at least one warning")
	}
}
