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
