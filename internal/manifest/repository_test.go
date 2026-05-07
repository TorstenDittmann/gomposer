package manifest

import (
	"strings"
	"testing"
)

func TestRepositoryValidate(t *testing.T) {
	cases := []struct {
		name string
		in   Repository
		want string // empty = OK
	}{
		{"git ok", Repository{Type: "vcs", URL: "https://example/x.git"}, ""},
		{"git alias", Repository{Type: "git", URL: "https://example/x.git"}, ""},
		{"composer rejected", Repository{Type: "composer", URL: "https://x"}, "CG204"},
		{"path rejected", Repository{Type: "path", URL: "../lib"}, "CG205"},
		{"missing url", Repository{Type: "vcs"}, "missing `url`"},
		{"missing type", Repository{URL: "https://x"}, "missing `type`"},
		{"unknown", Repository{Type: "fish", URL: "x"}, "CG208"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.in.Validate()
			if c.want == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Validate err = %v, want substring %q", err, c.want)
			}
		})
	}
}
