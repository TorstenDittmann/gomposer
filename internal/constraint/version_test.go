package constraint

import "testing"

func TestParseVersionStable(t *testing.T) {
	v, err := ParseVersion("1.2.3")
	if err != nil {
		t.Fatalf("ParseVersion: %v", err)
	}
	if v.Major != 1 || v.Minor != 2 || v.Patch != 3 {
		t.Errorf("got %d.%d.%d, want 1.2.3", v.Major, v.Minor, v.Patch)
	}
	if v.Stability != Stable {
		t.Errorf("Stability = %v, want Stable", v.Stability)
	}
}

func TestParseVersionWithLeadingV(t *testing.T) {
	v, err := ParseVersion("v1.2.3")
	if err != nil {
		t.Fatalf("ParseVersion: %v", err)
	}
	if v.Major != 1 {
		t.Errorf("Major = %d, want 1", v.Major)
	}
}

func TestParseVersionPreRelease(t *testing.T) {
	cases := []struct {
		input string
		stab  Stability
	}{
		{"1.2.3-alpha", Alpha},
		{"1.2.3-alpha.1", Alpha},
		{"1.2.3-beta1", Beta},
		{"1.2.3-RC1", RC},
		{"1.2.3-rc.2", RC},
	}
	for _, tc := range cases {
		v, err := ParseVersion(tc.input)
		if err != nil {
			t.Errorf("%s: %v", tc.input, err)
			continue
		}
		if v.Stability != tc.stab {
			t.Errorf("%s: Stability = %v, want %v", tc.input, v.Stability, tc.stab)
		}
	}
}

func TestParseVersionDev(t *testing.T) {
	v, err := ParseVersion("dev-main")
	if err != nil {
		t.Fatalf("ParseVersion: %v", err)
	}
	if v.Stability != Dev {
		t.Errorf("Stability = %v, want Dev", v.Stability)
	}
	if v.Branch != "main" {
		t.Errorf("Branch = %q, want main", v.Branch)
	}
}

func TestVersionCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.4", -1},
		{"1.2.3", "1.2.3", 0},
		{"2.0.0", "1.99.99", 1},
		{"1.2.3-alpha", "1.2.3-beta", -1},
		{"1.2.3-RC1", "1.2.3", -1},
		{"1.2.3-RC1", "1.2.3-RC2", -1},
	}
	for _, tc := range cases {
		va, _ := ParseVersion(tc.a)
		vb, _ := ParseVersion(tc.b)
		got := va.Compare(vb)
		if got != tc.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
