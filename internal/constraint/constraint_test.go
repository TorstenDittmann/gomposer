package constraint

import (
	"strings"
	"testing"
)

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

func TestParseInlineAliasStripsAs(t *testing.T) {
	c, err := Parse("dev-feat-phased-chunk-upload-api as 2.0.1")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	v, _ := ParseVersion("dev-feat-phased-chunk-upload-api")
	if !c.Satisfies(v) {
		t.Errorf("dev-feat-phased-chunk-upload-api should satisfy itself when alias-stripped")
	}
}

func TestParseStabilitySuffix(t *testing.T) {
	cases := []struct {
		constraint, version string
		want                bool
	}{
		{"1.0.0@stable", "1.0.0", true},
		{"1.0.0@dev", "1.0.0", true},
		{"^2.0@beta", "2.5.0", true},
		{"^2.0@beta", "1.9.9", false},
		{">=1.0@dev", "1.5.0", true},
		{">=1.0@dev,<2.0@dev", "1.5.0", true},
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

func TestParseVersionStabilitySuffixRecorded(t *testing.T) {
	v, err := ParseVersion("1.0.0@dev")
	if err != nil {
		t.Fatalf("ParseVersion: %v", err)
	}
	if v.Major != 1 || v.Minor != 0 || v.Patch != 0 {
		t.Errorf("got %d.%d.%d, want 1.0.0", v.Major, v.Minor, v.Patch)
	}
	if v.StabilityFlag != "dev" {
		t.Errorf("StabilityFlag = %q, want dev", v.StabilityFlag)
	}
}

func TestParseHyphenRange(t *testing.T) {
	cases := []struct {
		constraint, version string
		want                bool
	}{
		// Full right side: inclusive upper bound.
		{"1.0.0 - 2.0.0", "1.0.0", true},
		{"1.0.0 - 2.0.0", "2.0.0", true},
		{"1.0.0 - 2.0.0", "2.0.1", false},
		{"1.0.0 - 2.0.0", "0.9.9", false},
		// Partial right side: bumped exclusive upper bound.
		{"1.0 - 2.0", "2.0.5", true},  // <2.1.0 covers 2.0.5
		{"1.0 - 2.0", "2.1.0", false}, // bumped upper is 2.1.0 exclusive
		{"1.0 - 2", "2.99.99", true},  // <3.0.0 covers 2.99.99
		{"1.0 - 2", "3.0.0", false},
		// Partial left side: filled with zeroes.
		{"1 - 2.0.0", "1.0.0", true},
		{"1 - 2.0.0", "0.9.9", false},
		// Hyphen ranges combine with other terms via comma/space AND.
		{"1.0 - 2.0,!=1.5.0", "1.5.0", false},
		{"1.0 - 2.0,!=1.5.0", "1.4.0", true},
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

func TestParseSpacedOperators(t *testing.T) {
	cases := []struct {
		constraint, version string
		want                bool
	}{
		{">= 1.0", "1.0.0", true},
		{">  1.0", "1.0.1", true},
		{"<= 2.0", "2.0.0", true},
		{"<  2.0", "1.9.9", true},
		{"!= 1.0.0", "1.0.0", false},
		{"!= 1.0.0", "1.0.1", true},
		{"^ 1.2.3", "1.2.3", true},
		{"~ 1.2.3", "1.2.5", true},
		{">= 1.0 < 2.0", "1.5.0", true},
		{">= 1.0,< 2.0", "1.5.0", true},
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

func TestParseCommaAsAnd(t *testing.T) {
	cases := []struct {
		constraint, version string
		want                bool
	}{
		{">=1.0,<2.0", "1.5.0", true},
		{">=1.0,<2.0", "2.0.0", false},
		{">=1.0, <2.0", "1.5.0", true},
		{">=1.0 ,<2.0", "1.5.0", true},
		{">=1.0 , <2.0", "1.5.0", true},
		{">=1.0,<2.0,!=1.5.0", "1.5.0", false},
		{">=1.0,<2.0,!=1.5.0", "1.4.0", true},
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

func TestIsExplicitDevWithSlash(t *testing.T) {
	cases := map[string]bool{
		"dev-feature/foo":       true,
		"dev-fix/bug-123":       true,
		"dev-release/v2":        true,
		"dev-feature/foo#abcd1": true,
		// Slashed branch with mixed alternative is NOT explicit-dev.
		"dev-feature/foo || ^1.0": false,
	}
	for in, want := range cases {
		c, err := Parse(in)
		if err != nil {
			t.Errorf("Parse(%q): %v", in, err)
			continue
		}
		if got := c.IsExplicitDev(); got != want {
			t.Errorf("IsExplicitDev(%q) = %v, want %v", in, got, want)
		}
		if want {
			body := strings.TrimPrefix(in, "dev-")
			if i := strings.IndexByte(body, '#'); i >= 0 {
				body = body[:i]
			}
			if got := c.ExplicitDevBranch(); got != body {
				t.Errorf("ExplicitDevBranch(%q) = %q, want %q", in, got, body)
			}
		}
	}
}

func TestBranchAliasVariants(t *testing.T) {
	parseCases := []struct {
		input string
		major int
		minor int
		isDev bool
	}{
		{"1.x-dev", 1, 0, true},
		{"2.x-dev", 2, 0, true},
		{"10.x-dev", 10, 0, true},
		{"0.x-dev", 0, 0, true},
		{"1.0.x-dev", 1, 0, true},
		{"1.2.x-dev", 1, 2, true},
		// Without -dev suffix Composer still aliases x-segments to dev.
		{"1.x", 1, 0, true},
		{"1.2.x", 1, 2, true},
	}
	for _, tc := range parseCases {
		v, err := ParseVersion(tc.input)
		if err != nil {
			t.Errorf("ParseVersion(%q): %v", tc.input, err)
			continue
		}
		if v.Major != tc.major {
			t.Errorf("%s: Major = %d, want %d", tc.input, v.Major, tc.major)
		}
		if v.Minor != tc.minor {
			t.Errorf("%s: Minor = %d, want %d", tc.input, v.Minor, tc.minor)
		}
		if (v.Stability == Dev) != tc.isDev {
			t.Errorf("%s: Stability = %v, want Dev=%v", tc.input, v.Stability, tc.isDev)
		}
	}

	// Caret/tilde matching against branch-alias versions.
	matchCases := []struct {
		constraint, version string
		want                bool
	}{
		{"^1.0", "1.x-dev", true},
		{"^1.0", "2.x-dev", false},
		{"^2.0", "2.x-dev", true},
		{"~1.2", "1.2.x-dev", true},
		{"~1.2", "1.3.x-dev", true}, // ~1.2 = >=1.2 <2.0.0, so 1.3.x-dev is in range
		{">=1.0", "1.x-dev", true},
		{">=2.0", "1.x-dev", false},
	}
	for _, tc := range matchCases {
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

func TestConstraintParseWorkspaceStar(t *testing.T) {
	c, err := Parse("workspace:*")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !c.IsWorkspace {
		t.Errorf("IsWorkspace = false, want true")
	}
	// workspace:* matches every version (like plain "*").
	v, _ := ParseVersion("1.2.3")
	if !c.Satisfies(v) {
		t.Errorf("workspace:* rejected 1.2.3")
	}
	v, _ = ParseVersion("999.0.0")
	if !c.Satisfies(v) {
		t.Errorf("workspace:* rejected 999.0.0")
	}
}

func TestConstraintParseWorkspaceCaret(t *testing.T) {
	c, err := Parse("workspace:^1.0")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !c.IsWorkspace {
		t.Errorf("IsWorkspace = false, want true")
	}
	// ^1.0 semantics: matches 1.x, rejects 2.x.
	v, _ := ParseVersion("1.5.0")
	if !c.Satisfies(v) {
		t.Errorf("workspace:^1.0 rejected 1.5.0")
	}
	v, _ = ParseVersion("2.0.0")
	if c.Satisfies(v) {
		t.Errorf("workspace:^1.0 admitted 2.0.0")
	}
}

func TestConstraintParseWorkspaceExactVersion(t *testing.T) {
	c, err := Parse("workspace:1.2.3")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !c.IsWorkspace {
		t.Errorf("IsWorkspace = false, want true")
	}
	v, _ := ParseVersion("1.2.3")
	if !c.Satisfies(v) {
		t.Errorf("workspace:1.2.3 rejected 1.2.3")
	}
	v, _ = ParseVersion("1.2.4")
	if c.Satisfies(v) {
		t.Errorf("workspace:1.2.3 admitted 1.2.4")
	}
}

// Sanity: normal constraints without the prefix don't get the flag.
func TestConstraintNoWorkspaceFlagOnRegular(t *testing.T) {
	c, err := Parse("^1.0")
	if err != nil {
		t.Fatal(err)
	}
	if c.IsWorkspace {
		t.Errorf("plain ^1.0 has IsWorkspace = true")
	}
}
