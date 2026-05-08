package constraint

import "testing"

func TestParseExact(t *testing.T) {
	c, err := Parse("1.2.3")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	v, _ := ParseVersion("1.2.3")
	if !c.Satisfies(v) {
		t.Errorf("1.2.3 should satisfy =1.2.3")
	}
	v2, _ := ParseVersion("1.2.4")
	if c.Satisfies(v2) {
		t.Errorf("1.2.4 should not satisfy =1.2.3")
	}
}

func TestParseRangeOps(t *testing.T) {
	cases := []struct {
		constraint, version string
		want                bool
	}{
		{">=1.0", "1.0.0", true},
		{">=1.0", "0.9.0", false},
		{">1.0", "1.0.0", false},
		{">1.0", "1.0.1", true},
		{"<2.0", "1.9.9", true},
		{"<2.0", "2.0.0", false},
		{"<=2.0", "2.0.0", true},
		{"!=1.0.0", "1.0.0", false},
		{"!=1.0.0", "1.0.1", true},
	}
	for _, tc := range cases {
		c, err := Parse(tc.constraint)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.constraint, err)
			continue
		}
		v, _ := ParseVersion(tc.version)
		if got := c.Satisfies(v); got != tc.want {
			t.Errorf("%s satisfies %s = %v, want %v", tc.version, tc.constraint, got, tc.want)
		}
	}
}

func TestParseCaret(t *testing.T) {
	cases := []struct {
		constraint, version string
		want                bool
	}{
		{"^1.2.3", "1.2.3", true},
		{"^1.2.3", "1.9.9", true},
		{"^1.2.3", "1.2.2", false},
		{"^1.2.3", "2.0.0", false},
		{"^0.3.0", "0.3.5", true},
		{"^0.3.0", "0.4.0", false},
	}
	for _, tc := range cases {
		c, err := Parse(tc.constraint)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.constraint, err)
			continue
		}
		v, _ := ParseVersion(tc.version)
		if got := c.Satisfies(v); got != tc.want {
			t.Errorf("%s in %s = %v, want %v", tc.version, tc.constraint, got, tc.want)
		}
	}
}

func TestParseTilde(t *testing.T) {
	cases := []struct {
		constraint, version string
		want                bool
	}{
		{"~1.2.3", "1.2.3", true},
		{"~1.2.3", "1.2.9", true},
		{"~1.2.3", "1.3.0", false},
		{"~1.2", "1.5.0", true},
		{"~1.2", "2.0.0", false},
	}
	for _, tc := range cases {
		c, err := Parse(tc.constraint)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.constraint, err)
			continue
		}
		v, _ := ParseVersion(tc.version)
		if got := c.Satisfies(v); got != tc.want {
			t.Errorf("%s in %s = %v, want %v", tc.version, tc.constraint, got, tc.want)
		}
	}
}

func TestParseAliasVersion1xDev(t *testing.T) {
	v, err := ParseVersion("1.x-dev")
	if err != nil {
		t.Fatalf("ParseVersion: %v", err)
	}
	if v.Major != 1 {
		t.Errorf("Major = %d", v.Major)
	}
	if v.Stability != Dev {
		t.Errorf("Stability = %v", v.Stability)
	}
}

func TestCaretMatches1xDev(t *testing.T) {
	c, err := Parse("^1.0")
	if err != nil {
		t.Fatal(err)
	}
	v, _ := ParseVersion("1.x-dev")
	if !c.Satisfies(v) {
		t.Errorf("^1.0 should satisfy 1.x-dev")
	}
	v2, _ := ParseVersion("2.x-dev")
	if c.Satisfies(v2) {
		t.Errorf("^1.0 should NOT satisfy 2.x-dev")
	}
}

func TestIsExplicitDev(t *testing.T) {
	cases := map[string]bool{
		"dev-main":         true,
		"dev-master":       true,
		"dev-feature/foo":  true,
		"dev-main#abc1234": true,
		"^1.0":             false,
		"~2.3":             false,
		">=1.0,<2.0":       false,
		"":                 false,
		"dev-main || ^1.0": false, // mixed: not a pure explicit-dev require
	}
	for in, want := range cases {
		c, err := Parse(in)
		if err != nil && in != "" {
			// "" is genuinely invalid; skip
			t.Logf("parse %q: %v", in, err)
			continue
		}
		if got := c.IsExplicitDev(); got != want {
			t.Errorf("IsExplicitDev(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseWildcard(t *testing.T) {
	cases := []struct {
		constraint, version string
		want                bool
	}{
		{"1.1.*", "1.1.0", true},
		{"1.1.*", "1.1.3", true},
		{"1.1.*", "1.2.0", false},
		{"1.1.*", "1.0.9", false},
		{"1.*", "1.0.0", true},
		{"1.*", "1.99.99", true},
		{"1.*", "2.0.0", false},
		{"1.*", "0.9.9", false},
		{"1.x", "1.5.0", true},
		{"1.x", "2.0.0", false},
		{"1.2.x", "1.2.5", true},
		{"1.2.x", "1.3.0", false},
		{"*", "0.0.1", true},
		{"*", "9.9.9", true},
	}
	for _, tc := range cases {
		c, err := Parse(tc.constraint)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.constraint, err)
			continue
		}
		v, _ := ParseVersion(tc.version)
		if got := c.Satisfies(v); got != tc.want {
			t.Errorf("%s in %s = %v, want %v", tc.version, tc.constraint, got, tc.want)
		}
	}
}

func TestParseSinglePipeOr(t *testing.T) {
	cases := []struct {
		constraint, version string
		want                bool
	}{
		{"^7.2|^8.0", "8.3.0", true},
		{"^7.2|^8.0", "7.4.0", true},
		{"^7.2|^8.0", "9.0.0", false},
		{"^7.2|^8.0", "7.1.0", false},
		{"^7.2 || ^8.0", "8.3.0", true},
	}
	for _, tc := range cases {
		c, err := Parse(tc.constraint)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.constraint, err)
			continue
		}
		v, _ := ParseVersion(tc.version)
		if got := c.Satisfies(v); got != tc.want {
			t.Errorf("%s in %s = %v, want %v", tc.version, tc.constraint, got, tc.want)
		}
	}
}
