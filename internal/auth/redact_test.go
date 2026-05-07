package auth

import (
	"strings"
	"testing"
)

func TestRedactKnownPatterns(t *testing.T) {
	cases := []struct {
		in  string
		bad string // substring that must NOT appear
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
