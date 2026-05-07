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
