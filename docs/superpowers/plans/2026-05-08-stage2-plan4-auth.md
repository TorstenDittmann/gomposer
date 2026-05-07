# Stage 2 / Plan 4: Auth Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Read auth credentials from `~/.composer/auth.json` AND `~/.config/composer-go/auth.json` (the latter wins on conflict), expose a hostname-keyed `Lookup` API, and apply credentials to outgoing HTTP requests issued by `httpcache.Cache`. VCS git auth is handled via a `GIT_ASKPASS` shim for HTTPS clones; SSH-based git auth remains delegated to the user's git config.

**Architecture:** A new `internal/auth` package owns parsing, merging, and lookup. The schema mirrors Composer's `auth.json` exactly so existing files Just Work. `httpcache.Cache` gains an optional `Credentials` resolver: when set, the cache injects the matching `Authorization` header into requests by hostname. The git wrapper writes a one-shot askpass script to a temp file at clone time.

**Tech Stack:** Go stdlib `encoding/json`, `net/http`, `os`, `path/filepath`, `runtime`. No new external deps.

**Depends on:**
- Stage 1 Plan 2 (`internal/cache/httpcache`) — we extend `Cache` with a credentials hook.
- Stage 2 Plan 2 (VCS git wrapper) — the askpass shim attaches to its clone command. If Plan 2 is not yet merged, Task 9 may be deferred and re-applied.

---

## File structure

| Path | Responsibility |
|------|---------------|
| `internal/auth/file.go` | `auth.json` schema + `parseFile`/`loadOptional` (single file) |
| `internal/auth/file_test.go` | Schema fixtures, missing-file tolerance, malformed JSON |
| `internal/auth/store.go` | `Store` type: parses both files, merges, exposes `Lookup` |
| `internal/auth/store_test.go` | Precedence (user > composer), per-host lookup, all kinds |
| `internal/auth/redact.go` | String redactor — strips secrets from formatted log lines |
| `internal/auth/redact_test.go` | Coverage of password/token/oauth substrings |
| `internal/auth/perms.go` | World-readable warning helper for Unix |
| `internal/auth/perms_test.go` | Permission warning emitted for 0o644, silent for 0o600 |
| `internal/auth/git_askpass.go` | Build a temporary askpass script for `git clone`/`fetch` |
| `internal/auth/git_askpass_test.go` | Script content, env var wiring, cleanup |
| `internal/cache/httpcache/cache.go` | (modified) optional `Credentials` resolver injects headers |
| `internal/cache/httpcache/cache_auth_test.go` | Round-trip: cache injects header, server validates |

---

## Task 1: `auth.json` schema + single-file loader

**Files:**
- Create: `internal/auth/file.go`
- Create: `internal/auth/file_test.go`

The on-disk schema mirrors Composer's. We decode to a typed Go struct and tolerate missing keys. A missing file is *not* an error; it just yields a zero-valued struct.

- [ ] **Step 1: Write the failing test**

Create `internal/auth/file_test.go`:

```go
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
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/auth/...`

Expected: build error on `parseFile`, `loadOptional`, missing types.

- [ ] **Step 3: Implement loader**

Create `internal/auth/file.go`:

```go
// Package auth parses Composer-compatible auth.json files and exposes
// hostname-keyed credential lookups for HTTP and git operations.
//
// The on-disk schema mirrors Composer's exactly so existing files Just
// Work. Two locations are read on startup; see Store.Load for precedence.
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// file mirrors Composer's auth.json schema.
type file struct {
	HTTPBasic   map[string]basicCred  `json:"http-basic"`
	Bearer      map[string]string     `json:"bearer"`
	GitHubOAuth map[string]string     `json:"github-oauth"`
	GitLabToken map[string]gitLabCred `json:"gitlab-token"`
	GitLabOAuth map[string]string     `json:"gitlab-oauth"`
}

type basicCred struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type gitLabCred struct {
	Username string `json:"username"`
	Token    string `json:"token"`
}

// parseFile reads and decodes path. Missing file is an error here; callers
// who want optional behavior should use loadOptional.
func parseFile(path string) (*file, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("auth: parse %s: %w", path, err)
	}
	return &f, nil
}

// loadOptional returns parseFile(path) on hit, a zero-valued *file on miss.
// Any error other than ErrNotExist is propagated.
func loadOptional(path string) (*file, error) {
	f, err := parseFile(path)
	if err == nil {
		return f, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return &file{}, nil
	}
	return nil, err
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/auth/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/auth
git commit -m "feat(auth): parse Composer-compatible auth.json"
```

---

## Task 2: Credentials kind enum + public types

**Files:**
- Create: `internal/auth/store.go` (initial skeleton; expanded in Task 3)

The resolver and `httpcache` consume credentials through a tagged `Credentials` value with a `Kind`. Authoring this first lets later tasks reference the public surface without churn.

- [ ] **Step 1: Write the file**

Create `internal/auth/store.go`:

```go
package auth

import (
	"net/http"
	"strings"
)

// Kind tags the credential variant. Each kind corresponds to a section
// in auth.json.
type Kind int

const (
	KindNone Kind = iota
	KindHTTPBasic
	KindBearer
	KindGitHubOAuth
	KindGitLabToken
	KindGitLabOAuth
)

func (k Kind) String() string {
	switch k {
	case KindHTTPBasic:
		return "http-basic"
	case KindBearer:
		return "bearer"
	case KindGitHubOAuth:
		return "github-oauth"
	case KindGitLabToken:
		return "gitlab-token"
	case KindGitLabOAuth:
		return "gitlab-oauth"
	}
	return "none"
}

// Credentials carries one resolved credential. Only the fields appropriate
// for the Kind are populated; the rest are zero.
type Credentials struct {
	Kind     Kind
	Host     string
	Username string // http-basic, gitlab-token
	Password string // http-basic
	Token    string // bearer, github-oauth, gitlab-token, gitlab-oauth
}

// Empty reports whether c carries no usable credential.
func (c Credentials) Empty() bool { return c.Kind == KindNone }

// Apply attaches c to req as an Authorization header. No-op on Empty.
//
// Mapping:
//   - http-basic     -> req.SetBasicAuth(Username, Password)
//   - bearer         -> Authorization: Bearer <Token>
//   - github-oauth   -> Authorization: token <Token>          (GitHub convention)
//   - gitlab-token   -> Private-Token: <Token>                (GitLab convention)
//   - gitlab-oauth   -> Authorization: Bearer <Token>
func (c Credentials) Apply(req *http.Request) {
	if c.Empty() || req == nil {
		return
	}
	switch c.Kind {
	case KindHTTPBasic:
		req.SetBasicAuth(c.Username, c.Password)
	case KindBearer, KindGitLabOAuth:
		req.Header.Set("Authorization", "Bearer "+c.Token)
	case KindGitHubOAuth:
		req.Header.Set("Authorization", "token "+c.Token)
	case KindGitLabToken:
		req.Header.Set("Private-Token", c.Token)
	}
}

// normHost lowercases and strips a trailing port from h, so "GitHub.com:443"
// and "github.com" both match a single auth.json entry.
func normHost(h string) string {
	h = strings.ToLower(h)
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	return h
}
```

- [ ] **Step 2: Verify it builds**

Run: `go build ./internal/auth/...`

Expected: clean build (no tests yet for this file alone — covered by Task 3).

- [ ] **Step 3: Commit**

```bash
git add internal/auth/store.go
git commit -m "feat(auth): Credentials value type with Kind enum and Apply"
```

---

## Task 3: `Store` — load, merge, lookup

**Files:**
- Modify: `internal/auth/store.go`
- Create: `internal/auth/store_test.go`

A `Store` parses both `~/.composer/auth.json` and `~/.config/composer-go/auth.json`, merges them with the user-config file winning per host per kind, and exposes `Lookup(host) (Credentials, ok)`. Lookup precedence within a single host is fixed (see implementation comments).

- [ ] **Step 1: Write the failing test**

Create `internal/auth/store_test.go`:

```go
package auth

import (
	"os"
	"path/filepath"
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
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/auth/...`

Expected: build error on `loadStore`, `Store`, `Lookup`.

- [ ] **Step 3: Implement Store**

Append to `internal/auth/store.go`:

```go
// Store is the merged credential index.
type Store struct {
	merged file // post-merge view
}

// loadStore reads both files (either may be empty string to skip) and
// merges with user winning per-host-per-kind on collision.
func loadStore(composerPath, userPath string) (*Store, error) {
	composer := &file{}
	user := &file{}
	if composerPath != "" {
		f, err := loadOptional(composerPath)
		if err != nil {
			return nil, err
		}
		composer = f
	}
	if userPath != "" {
		f, err := loadOptional(userPath)
		if err != nil {
			return nil, err
		}
		user = f
	}
	return &Store{merged: mergeFiles(composer, user)}, nil
}

// Load resolves the conventional file paths for the current user and
// loads them. Missing files are tolerated. Returns a usable empty Store
// if both are missing.
func Load() (*Store, error) {
	composerPath, userPath, err := defaultPaths()
	if err != nil {
		return nil, err
	}
	return loadStore(composerPath, userPath)
}

// Lookup returns the credential registered for host (case-insensitive,
// port-insensitive). When multiple kinds share a host, the priority is:
//
//	bearer > http-basic > github-oauth > gitlab-token > gitlab-oauth
//
// In practice users rarely register more than one kind per host. The order
// favours the most specific/scoped header form first.
func (s *Store) Lookup(host string) (Credentials, bool) {
	if s == nil {
		return Credentials{}, false
	}
	h := normHost(host)
	if t, ok := s.merged.Bearer[h]; ok && t != "" {
		return Credentials{Kind: KindBearer, Host: h, Token: t}, true
	}
	if b, ok := s.merged.HTTPBasic[h]; ok && (b.Username != "" || b.Password != "") {
		return Credentials{Kind: KindHTTPBasic, Host: h, Username: b.Username, Password: b.Password}, true
	}
	if t, ok := s.merged.GitHubOAuth[h]; ok && t != "" {
		return Credentials{Kind: KindGitHubOAuth, Host: h, Token: t}, true
	}
	if g, ok := s.merged.GitLabToken[h]; ok && g.Token != "" {
		return Credentials{Kind: KindGitLabToken, Host: h, Username: g.Username, Token: g.Token}, true
	}
	if t, ok := s.merged.GitLabOAuth[h]; ok && t != "" {
		return Credentials{Kind: KindGitLabOAuth, Host: h, Token: t}, true
	}
	return Credentials{}, false
}

// mergeFiles produces a new file where, for every map, user entries
// override composer entries on host collision. Hosts present in only one
// side are preserved as-is.
func mergeFiles(composer, user *file) file {
	out := file{
		HTTPBasic:   map[string]basicCred{},
		Bearer:      map[string]string{},
		GitHubOAuth: map[string]string{},
		GitLabToken: map[string]gitLabCred{},
		GitLabOAuth: map[string]string{},
	}
	for h, v := range composer.HTTPBasic {
		out.HTTPBasic[normHost(h)] = v
	}
	for h, v := range user.HTTPBasic {
		out.HTTPBasic[normHost(h)] = v
	}
	for h, v := range composer.Bearer {
		out.Bearer[normHost(h)] = v
	}
	for h, v := range user.Bearer {
		out.Bearer[normHost(h)] = v
	}
	for h, v := range composer.GitHubOAuth {
		out.GitHubOAuth[normHost(h)] = v
	}
	for h, v := range user.GitHubOAuth {
		out.GitHubOAuth[normHost(h)] = v
	}
	for h, v := range composer.GitLabToken {
		out.GitLabToken[normHost(h)] = v
	}
	for h, v := range user.GitLabToken {
		out.GitLabToken[normHost(h)] = v
	}
	for h, v := range composer.GitLabOAuth {
		out.GitLabOAuth[normHost(h)] = v
	}
	for h, v := range user.GitLabOAuth {
		out.GitLabOAuth[normHost(h)] = v
	}
	return out
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/auth/...`

Expected: PASS for all five `TestStore*` cases.

- [ ] **Step 5: Commit**

```bash
git add internal/auth
git commit -m "feat(auth): merged Store with hostname Lookup and kind precedence"
```

---

## Task 4: Default file paths

**Files:**
- Modify: `internal/auth/store.go` (add `defaultPaths`)
- Create: `internal/auth/paths_test.go`

`Load()` calls `defaultPaths()` which resolves:

- composer file: `$HOME/.composer/auth.json`
- user file: `$XDG_CONFIG_HOME/composer-go/auth.json`, falling back to `$HOME/.config/composer-go/auth.json`

We honour `$XDG_CONFIG_HOME` when set so the same env-var conventions apply across cache and config.

- [ ] **Step 1: Write the failing test**

Create `internal/auth/paths_test.go`:

```go
package auth

import (
	"path/filepath"
	"testing"
)

func TestDefaultPathsXDG(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	t.Setenv("XDG_CONFIG_HOME", "/cfg")
	composer, user, err := defaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if composer != filepath.Join("/home/u", ".composer", "auth.json") {
		t.Errorf("composer = %q", composer)
	}
	if user != filepath.Join("/cfg", "composer-go", "auth.json") {
		t.Errorf("user = %q", user)
	}
}

func TestDefaultPathsXDGUnset(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	t.Setenv("XDG_CONFIG_HOME", "")
	_, user, err := defaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if user != filepath.Join("/home/u", ".config", "composer-go", "auth.json") {
		t.Errorf("user = %q", user)
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/auth/...`

Expected: build error on `defaultPaths`.

- [ ] **Step 3: Implement**

Append to `internal/auth/store.go`:

```go
import_os_pkg_marker_for_diff_clarity := 0
_ = import_os_pkg_marker_for_diff_clarity
```

(That marker is illustrative — the real change is to add `"errors"`, `"os"`, `"path/filepath"` to the imports if not already present, then append the function below.)

```go
func defaultPaths() (composerPath, userPath string, err error) {
	home := os.Getenv("HOME")
	if home == "" {
		return "", "", errors.New("auth: $HOME is unset")
	}
	composerPath = filepath.Join(home, ".composer", "auth.json")
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		userPath = filepath.Join(x, "composer-go", "auth.json")
	} else {
		userPath = filepath.Join(home, ".config", "composer-go", "auth.json")
	}
	return composerPath, userPath, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/auth/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/auth
git commit -m "feat(auth): defaultPaths honours XDG_CONFIG_HOME"
```

---

## Task 5: Secret redactor for log lines

**Files:**
- Create: `internal/auth/redact.go`
- Create: `internal/auth/redact_test.go`

We never log raw secrets. Code paths that log credential-shaped data route through `Redact(s)`, which scrubs values that look like passwords/tokens/oauth strings. This is best-effort hygiene, not a substitute for never logging the value.

- [ ] **Step 1: Write the failing test**

Create `internal/auth/redact_test.go`:

```go
package auth

import (
	"strings"
	"testing"
)

func TestRedactKnownPatterns(t *testing.T) {
	cases := []struct {
		in   string
		bad  string // substring that must NOT appear
	}{
		{`Authorization: Bearer ghp_abc123`, "ghp_abc123"},
		{`{"password":"hunter2"}`, "hunter2"},
		{`{"token":"glt_xyz"}`, "glt_xyz"},
		{`{"oauth":"OAUTH_TOKEN"}`, "OAUTH_TOKEN"},
		{`Private-Token: glt_xyz`, "glt_xyz"},
	}
	for _, tc := range cases {
		out := Redact(tc.in)
		if strings.Contains(out, tc.bad) {
			t.Errorf("Redact(%q) leaked %q in %q", tc.in, tc.bad, out)
		}
		if !strings.Contains(out, "REDACTED") {
			t.Errorf("Redact(%q) did not insert REDACTED: %q", tc.in, out)
		}
	}
}

func TestRedactPassThrough(t *testing.T) {
	in := "GET /p2/monolog/monolog.json 200"
	if Redact(in) != in {
		t.Errorf("Redact altered safe string: %q", Redact(in))
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/auth/...`

Expected: build error on `Redact`.

- [ ] **Step 3: Implement Redact**

Create `internal/auth/redact.go`:

```go
package auth

import "regexp"

// We match the most common shapes Composer/composer-go produces. The goal
// is hygiene against accidental logging — code that handles a Credentials
// value should still avoid passing it to a logger in the first place.
var redactPatterns = []*regexp.Regexp{
	// "Authorization: Bearer <tok>" / "Authorization: token <tok>"
	regexp.MustCompile(`(?i)(authorization:\s*(?:bearer|token)\s+)\S+`),
	// "Private-Token: <tok>"
	regexp.MustCompile(`(?i)(private-token:\s*)\S+`),
	// "password":"...", "token":"...", "oauth":"..."
	regexp.MustCompile(`(?i)("(?:password|token|oauth)"\s*:\s*")[^"]*(")`),
}

// Redact returns s with credential-shaped substrings replaced by REDACTED.
// Safe to call on arbitrary strings; non-matching input is returned as-is.
func Redact(s string) string {
	out := s
	out = redactPatterns[0].ReplaceAllString(out, `${1}REDACTED`)
	out = redactPatterns[1].ReplaceAllString(out, `${1}REDACTED`)
	out = redactPatterns[2].ReplaceAllString(out, `${1}REDACTED${2}`)
	return out
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/auth/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/auth
git commit -m "feat(auth): Redact() scrubs credential shapes from log strings"
```

---

## Task 6: World-readable file permissions warning

**Files:**
- Create: `internal/auth/perms.go`
- Create: `internal/auth/perms_test.go`

When loading either auth file on Unix, emit a warning if the file is world-readable. We don't fail loading — users may be on filesystems where chmod isn't meaningful — but we make the noise visible.

- [ ] **Step 1: Write the failing test**

Create `internal/auth/perms_test.go`:

```go
package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPermissionWarningOnUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("perm warnings are Unix-only")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	warn := warnIfInsecurePermissions(path)
	if warn == "" {
		t.Errorf("expected a warning for 0644 file; got empty string")
	}
}

func TestNoWarningForSafePerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("perm warnings are Unix-only")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if w := warnIfInsecurePermissions(path); w != "" {
		t.Errorf("unexpected warning for 0600: %q", w)
	}
}

func TestNoWarningForMissingFile(t *testing.T) {
	if w := warnIfInsecurePermissions(filepath.Join(t.TempDir(), "absent")); w != "" {
		t.Errorf("expected silence for missing file; got %q", w)
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/auth/...`

Expected: build error on `warnIfInsecurePermissions`.

- [ ] **Step 3: Implement**

Create `internal/auth/perms.go`:

```go
package auth

import (
	"fmt"
	"os"
	"runtime"
)

// warnIfInsecurePermissions inspects path and returns a non-empty warning
// string if the file is world- or group-readable on Unix. Empty string
// means "no concern" (file missing, on Windows, or restricted to owner).
//
// Callers route the returned message through their logger.
func warnIfInsecurePermissions(path string) string {
	if runtime.GOOS == "windows" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	mode := info.Mode().Perm()
	if mode&0o077 != 0 {
		return fmt.Sprintf("auth: %s has permissions %#o; recommend `chmod 600 %s`", path, mode, path)
	}
	return ""
}
```

- [ ] **Step 4: Wire into Load**

Modify `Load()` in `internal/auth/store.go` to collect warnings and expose them:

```go
// Warnings returns advisory messages produced while loading (currently
// only file-permission complaints). Callers should print these to stderr.
func (s *Store) Warnings() []string {
	return s.warnings
}
```

Add `warnings []string` to `Store`, populate from `warnIfInsecurePermissions(composerPath)` and `warnIfInsecurePermissions(userPath)` in `loadStore`. Add a quick test:

```go
// In store_test.go — append:
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
```

Add `"runtime"` to the test imports.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/auth/...`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/auth
git commit -m "feat(auth): warn on world-readable auth.json (Unix)"
```

---

## Task 7: `httpcache.Cache` accepts a credentials resolver

**Files:**
- Modify: `internal/cache/httpcache/cache.go`
- Create: `internal/cache/httpcache/cache_auth_test.go`

We add an optional `Credentials` resolver to `Cache`. When set, `Cache.Get` calls it with the request hostname and applies the returned credential (if any) to the outgoing `*http.Request` *before* it's sent. Cached responses on disk remain identical regardless of auth — credentials gate the request, not the cache key.

- [ ] **Step 1: Write the failing test**

Create `internal/cache/httpcache/cache_auth_test.go`:

```go
package httpcache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCacheAppliesCredentials(t *testing.T) {
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("ETag", `"v"`)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c, err := New(t.TempDir(), srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	c.Credentials = CredentialsFunc(func(host string) (string, bool) {
		return "Bearer TOKEN", true
	})

	if _, err := c.Get(context.Background(), srv.URL+"/x"); err != nil {
		t.Fatal(err)
	}
	if sawAuth != "Bearer TOKEN" {
		t.Errorf("Authorization = %q, want Bearer TOKEN", sawAuth)
	}
}

func TestCacheNoCredentialsResolver(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("unexpected Authorization: %q", got)
		}
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	c, _ := New(t.TempDir(), srv.Client())
	if _, err := c.Get(context.Background(), srv.URL); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/cache/httpcache/...`

Expected: build error on `c.Credentials`, `CredentialsFunc`.

- [ ] **Step 3: Modify cache.go**

Add to `internal/cache/httpcache/cache.go`:

```go
// CredentialsResolver returns a value suitable for the Authorization header
// for the given hostname, plus an ok flag.
//
// Implementations typically wrap auth.Store: callers convert the resolved
// auth.Credentials into a single header value via Credentials.AuthorizationHeader().
type CredentialsResolver interface {
	AuthHeader(host string) (value string, ok bool)
}

// CredentialsFunc adapts a function to CredentialsResolver.
type CredentialsFunc func(host string) (string, bool)

func (f CredentialsFunc) AuthHeader(host string) (string, bool) { return f(host) }
```

Add a public `Credentials CredentialsResolver` field to `Cache`:

```go
type Cache struct {
	dir         string
	client      *http.Client
	Credentials CredentialsResolver // optional; nil means no auth injection
}
```

Inside `Get`, after `req` is created and conditional headers are set, before `c.client.Do(req)`:

```go
if c.Credentials != nil {
	if v, ok := c.Credentials.AuthHeader(req.URL.Host); ok && v != "" {
		// Bearer/basic-encoded values are caller-formed; basic-auth callers
		// should produce "Basic <base64>" themselves or use a richer hook.
		req.Header.Set("Authorization", v)
	}
}
```

The `Authorization` header set via `req.SetBasicAuth` from a `Credentials.Apply` call still works in the richer hook used by registry clients (Task 8) — the simple string-valued resolver here is the minimum the cache needs.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/cache/httpcache/...`

Expected: PASS for both new tests; existing tests unaffected.

- [ ] **Step 5: Commit**

```bash
git add internal/cache/httpcache
git commit -m "feat(httpcache): optional CredentialsResolver injects Authorization header"
```

---

## Task 8: Registry/client adapter — wire `auth.Store` into Packagist

**Files:**
- Modify: `internal/registry/packagist/packagist.go`
- Modify: `internal/registry/packagist/packagist_test.go`

Add an optional `Auth *auth.Store` to packagist `Config`. When present, the client constructs a `CredentialsResolver` that converts `auth.Credentials` into the right header string per kind, and assigns it to the underlying `httpcache.Cache.Credentials`.

This is also the integration point future "private composer-type repository" clients will use: any code that constructs an `httpcache.Cache` for a private host should attach the same resolver.

- [ ] **Step 1: Add Credentials.AuthorizationHeader helper**

Append to `internal/auth/store.go`:

```go
// AuthorizationHeader returns the value to set as `Authorization` for HTTP
// Bearer-style credentials. Empty string for kinds that use a different
// header (gitlab-token uses Private-Token); see Apply for the full mapping.
//
// http-basic returns "Basic <base64(user:pass)>".
func (c Credentials) AuthorizationHeader() string {
	switch c.Kind {
	case KindBearer, KindGitLabOAuth:
		return "Bearer " + c.Token
	case KindGitHubOAuth:
		return "token " + c.Token
	case KindHTTPBasic:
		raw := c.Username + ":" + c.Password
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(raw))
	}
	return ""
}
```

Add `"encoding/base64"` to the import block.

For `KindGitLabToken`, the right header is `Private-Token`, not `Authorization`. We expose a richer hook so callers can set arbitrary headers:

```go
// HTTPHeader returns (name, value, ok). Use this when AuthorizationHeader
// is not enough (gitlab-token uses Private-Token).
func (c Credentials) HTTPHeader() (string, string, bool) {
	if c.Kind == KindGitLabToken && c.Token != "" {
		return "Private-Token", c.Token, true
	}
	if v := c.AuthorizationHeader(); v != "" {
		return "Authorization", v, true
	}
	return "", "", false
}
```

- [ ] **Step 2: Wire into Packagist client**

Modify `internal/registry/packagist/packagist.go`:

```go
import (
	// existing imports...
	"github.com/torstendittmann/composer-go/internal/auth"
)

type Config struct {
	BaseURL    string
	CacheDir   string
	HTTPClient *http.Client
	Auth       *auth.Store // optional; if nil, requests are unauthenticated
}
```

Inside `New`, after building `hc`:

```go
if cfg.Auth != nil {
	hc.Credentials = httpcache.CredentialsFunc(func(host string) (string, bool) {
		c, ok := cfg.Auth.Lookup(host)
		if !ok {
			return "", false
		}
		// We only set Authorization here; gitlab-token uses Private-Token,
		// which httpcache's simple resolver cannot express. The registry
		// client wraps that case explicitly below.
		return c.AuthorizationHeader(), true
	})
}
```

For the gitlab-token (`Private-Token`) case, extend `httpcache` later if a real registry needs it. For Plan 4, `KindGitLabToken` is consumed primarily by the VCS git path (Task 9); HTTP API calls authenticated with `Private-Token` are not on the stage-2 hot path.

Document this limitation in a comment above the assignment.

- [ ] **Step 3: Test the integration**

Add to `internal/registry/packagist/packagist_test.go`:

```go
func TestPackagistAttachesAuth(t *testing.T) {
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleResponse))
	}))
	defer srv.Close()

	dir := t.TempDir()
	authFile := filepath.Join(dir, "user.json")
	host := srv.Listener.Addr().String()
	host = host[:len(host)] // keep as-is; auth lookup normalises ports.
	body := `{"bearer":{"` + host + `":"TOK"}}`
	if err := os.WriteFile(authFile, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := auth.LoadStoreForTest("", authFile)
	if err != nil {
		t.Fatal(err)
	}

	c, err := New(Config{
		BaseURL:    srv.URL,
		CacheDir:   t.TempDir(),
		HTTPClient: srv.Client(),
		Auth:       store,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Lookup(context.Background(), "monolog/monolog"); err != nil {
		t.Fatal(err)
	}
	if sawAuth != "Bearer TOK" {
		t.Errorf("Authorization = %q, want Bearer TOK", sawAuth)
	}
}
```

`auth.LoadStoreForTest` is a small exported wrapper over `loadStore`:

```go
// In internal/auth/store.go (or a separate file_test_helpers.go inside the
// package, exported because the test lives in another package):

// LoadStoreForTest is exported so other packages' tests can build a Store
// from explicit paths without going through Load(). It is not part of the
// stable public API.
func LoadStoreForTest(composerPath, userPath string) (*Store, error) {
	return loadStore(composerPath, userPath)
}
```

Imports for `packagist_test.go`: add `os`, `filepath`, and `github.com/torstendittmann/composer-go/internal/auth`.

Hostname-port note: the `httptest.Server` listens on `127.0.0.1:NNNN`. `auth.normHost` strips the port, so the auth file should key on `127.0.0.1`. Adjust the test fixture accordingly:

```go
body := `{"bearer":{"127.0.0.1":"TOK"}}`
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/registry/...`

Expected: PASS, including the new `TestPackagistAttachesAuth`.

- [ ] **Step 5: Commit**

```bash
git add internal/auth internal/registry
git commit -m "feat(registry/packagist): inject auth headers via auth.Store"
```

---

## Task 9: Git askpass shim for HTTPS clones

**Files:**
- Create: `internal/auth/git_askpass.go`
- Create: `internal/auth/git_askpass_test.go`

For HTTPS git URLs (the typical Packagist `dist`/`source` case for private VCS), git authenticates via a credential helper. We write a tiny shell script that echoes the right token, point `GIT_ASKPASS` at it, and `GIT_TERMINAL_PROMPT=0` to fail loudly rather than block on a TTY.

SSH URLs are untouched — those use the user's existing `~/.ssh` config.

The script is regenerated per clone (cheap) and removed when the caller invokes the returned cleanup. Token is passed via env var `COMPOSER_GO_GIT_TOKEN` so it never appears in `ps`.

- [ ] **Step 1: Write the failing test**

Create `internal/auth/git_askpass_test.go`:

```go
package auth

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestAskpassEmitsToken(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("askpass shim is sh-based")
	}
	c := Credentials{Kind: KindGitHubOAuth, Host: "github.com", Token: "ghp_TEST"}
	env, cleanup, err := PrepareGitEnv(c)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	// Find GIT_ASKPASS path in env.
	var askpass string
	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_ASKPASS=") {
			askpass = strings.TrimPrefix(kv, "GIT_ASKPASS=")
		}
	}
	if askpass == "" {
		t.Fatal("env missing GIT_ASKPASS")
	}
	if _, err := os.Stat(askpass); err != nil {
		t.Fatalf("askpass script missing: %v", err)
	}

	// Run the script with the token in env; it should print it to stdout.
	cmd := exec.Command(askpass, "Password for 'https://github.com':")
	cmd.Env = append(os.Environ(), "COMPOSER_GO_GIT_TOKEN=ghp_TEST")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "ghp_TEST" {
		t.Errorf("askpass output = %q, want ghp_TEST", string(out))
	}
}

func TestAskpassEmptyForNoCredentials(t *testing.T) {
	env, cleanup, err := PrepareGitEnv(Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_ASKPASS=") {
			t.Errorf("did not expect GIT_ASKPASS for empty credentials: %q", kv)
		}
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/auth/...`

Expected: build error on `PrepareGitEnv`.

- [ ] **Step 3: Implement**

Create `internal/auth/git_askpass.go`:

```go
package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// PrepareGitEnv builds env-var entries that should be appended to the
// command env of a `git clone`/`git fetch` run, so that HTTPS auth
// succeeds for hosts where we have credentials.
//
// On Windows we currently return no extras (askpass shell scripts don't
// apply); SSH-based git auth covers the remainder.
//
// The returned cleanup removes the temporary script. It is always
// non-nil and safe to call.
func PrepareGitEnv(c Credentials) (env []string, cleanup func(), err error) {
	cleanup = func() {}
	if c.Empty() || runtime.GOOS == "windows" {
		return nil, cleanup, nil
	}

	user, token := gitUserToken(c)
	if token == "" {
		return nil, cleanup, nil
	}

	dir, err := os.MkdirTemp("", "composer-go-askpass-")
	if err != nil {
		return nil, cleanup, err
	}
	scriptPath := filepath.Join(dir, "askpass.sh")
	const tmpl = `#!/bin/sh
case "$1" in
  Username*) printf '%%s' "${COMPOSER_GO_GIT_USER:-x-access-token}" ;;
  *)         printf '%%s' "$COMPOSER_GO_GIT_TOKEN" ;;
esac
`
	if err := os.WriteFile(scriptPath, []byte(fmt.Sprintf(tmpl)), 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, cleanup, err
	}

	cleanup = func() { _ = os.RemoveAll(dir) }
	env = []string{
		"GIT_ASKPASS=" + scriptPath,
		"GIT_TERMINAL_PROMPT=0",
		"COMPOSER_GO_GIT_TOKEN=" + token,
		"COMPOSER_GO_GIT_USER=" + user,
	}
	return env, cleanup, nil
}

// gitUserToken maps a Credentials value to (username, token) for use with
// HTTPS git remotes. The username is mostly cosmetic for token-bearing
// hosts (GitHub/GitLab accept any non-empty username), but we choose
// reasonable defaults.
func gitUserToken(c Credentials) (user, token string) {
	switch c.Kind {
	case KindHTTPBasic:
		return c.Username, c.Password
	case KindBearer:
		return "x-access-token", c.Token
	case KindGitHubOAuth:
		return "x-access-token", c.Token
	case KindGitLabToken:
		// GitLab accepts personal access tokens with any username; "oauth2"
		// is the canonical placeholder per GitLab docs.
		return "oauth2", c.Token
	case KindGitLabOAuth:
		return "oauth2", c.Token
	}
	return "", ""
}
```

(The double-`%` in the template is intentional — `fmt.Sprintf` consumes one. The outer `printf` in the shell needs to remain a `%s`.)

- [ ] **Step 4: Run tests**

Run: `go test ./internal/auth/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/auth
git commit -m "feat(auth): GIT_ASKPASS shim for HTTPS git clones"
```

---

## Task 10: Project-local file precedence — documented decision

**Files:**
- Modify: `internal/auth/store.go` (doc only)
- Modify: this plan (no — leave the section in code)

Composer also reads `<projectDir>/auth.json`. For Stage 2 we deliberately do *not* support project-local files: the surface area is small, the security story is worse (auth files committed by accident), and adopters in this stage are running CI/dev where the user file is sufficient.

- [ ] **Step 1: Document the decision**

Add a doc comment block to `Load()` in `internal/auth/store.go`:

```go
// Load returns a Store populated from the user's two known auth files.
//
// Precedence on host collision:
//
//	$XDG_CONFIG_HOME/composer-go/auth.json   (or ~/.config/composer-go/auth.json)
//	      wins over
//	~/.composer/auth.json
//
// Stage-2 deliberately does not read a project-local auth.json. Composer
// supports `<projectDir>/auth.json` and we may add it later, but for now
// the user-scoped files are the only sources. Adopters needing per-project
// credentials should use an env-var-driven config or the user file.
func Load() (*Store, error) { /* ... */ }
```

No tests required for a doc-only change; verify the comment renders correctly with `go doc`:

Run: `go doc github.com/torstendittmann/composer-go/internal/auth Load`

Expected: comment block visible, no rendering errors.

- [ ] **Step 2: Commit**

```bash
git add internal/auth/store.go
git commit -m "docs(auth): clarify precedence and project-local deferral"
```

---

## Plan 4 acceptance check

- `go test ./...` is green offline.
- A user `auth.json` containing `bearer.<host>=TOK` causes `httpcache.Cache.Get(<host>/...)` to send `Authorization: Bearer TOK` (verified by `TestCacheAppliesCredentials`).
- `auth.Load()` tolerates either or both files being absent and never panics.
- World-readable auth.json on Unix surfaces a warning via `Store.Warnings()`.
- `auth.PrepareGitEnv` produces a working `GIT_ASKPASS` script that, given `COMPOSER_GO_GIT_TOKEN` in the environment, prints the token to stdout.
- No raw secrets appear in any logger call: every code path that formats a credential-containing string runs through `auth.Redact` (search the codebase to confirm — `grep -R "credentials" internal/` should turn up only redacted formatting).
- The Packagist client accepts an `Auth *auth.Store` and forwards credentials to the HTTP cache for any host registered in the auth files.
