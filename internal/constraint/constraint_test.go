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
