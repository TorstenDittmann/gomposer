// Package constraint implements PHP-style version and constraint parsing.
// PHP's semver dialect adds stability flags (stable/RC/beta/alpha/dev),
// dev-* branch versions, and a leading-v tolerance that off-the-shelf
// semver libraries do not handle correctly.
package constraint

import (
	"fmt"
	"strconv"
	"strings"
)

// Stability ranks pre-release labels. Higher value = more stable.
type Stability int

const (
	Dev Stability = iota
	Alpha
	Beta
	RC
	Stable
)

func (s Stability) String() string {
	switch s {
	case Dev:
		return "dev"
	case Alpha:
		return "alpha"
	case Beta:
		return "beta"
	case RC:
		return "RC"
	case Stable:
		return "stable"
	}
	return "unknown"
}

// Version is a parsed PHP-style version. For dev branches Major/Minor/Patch
// are zero and Branch is set; for normal versions Branch is empty.
type Version struct {
	Major     int
	Minor     int
	Patch     int
	Stability Stability
	// PreNum is the numeric suffix of a pre-release (e.g. 2 in "1.0.0-RC2").
	// Zero when absent.
	PreNum int
	// Branch is set only for dev-* versions.
	Branch string
	// Original is the input string, retained for round-tripping.
	Original string
}

// ParseVersion parses a PHP-style version string.
func ParseVersion(s string) (Version, error) {
	v := Version{Original: s, Stability: Stable}

	// dev-<branch>
	if strings.HasPrefix(s, "dev-") {
		v.Stability = Dev
		v.Branch = strings.TrimPrefix(s, "dev-")
		if v.Branch == "" {
			return v, fmt.Errorf("constraint: empty branch in %q", s)
		}
		return v, nil
	}

	// Strip leading v.
	body := strings.TrimPrefix(s, "v")

	// Split on first '-' or '+' to separate base from pre-release/build.
	base, pre := body, ""
	if i := strings.IndexAny(body, "-+"); i >= 0 {
		base = body[:i]
		pre = body[i+1:]
	}

	parts := strings.Split(base, ".")
	if len(parts) < 1 || len(parts) > 4 {
		return v, fmt.Errorf("constraint: invalid version %q", s)
	}
	nums := []int{0, 0, 0, 0}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return v, fmt.Errorf("constraint: invalid version %q: %w", s, err)
		}
		nums[i] = n
	}
	v.Major, v.Minor, v.Patch = nums[0], nums[1], nums[2]

	if pre != "" {
		stab, num := classifyPre(pre)
		v.Stability = stab
		v.PreNum = num
	}
	return v, nil
}

// classifyPre maps a pre-release suffix to a stability and trailing number.
// Recognized prefixes: alpha / a, beta / b, rc, patch / p (treated as
// stable). Anything else is treated as dev.
func classifyPre(pre string) (Stability, int) {
	low := strings.ToLower(pre)
	// Strip leading separators within the pre tag.
	low = strings.TrimLeft(low, "-.")
	switch {
	case strings.HasPrefix(low, "rc"):
		return RC, leadingNum(low[2:])
	case strings.HasPrefix(low, "beta"):
		return Beta, leadingNum(low[4:])
	case strings.HasPrefix(low, "b") && (len(low) == 1 || isDigit(low[1])):
		return Beta, leadingNum(low[1:])
	case strings.HasPrefix(low, "alpha"):
		return Alpha, leadingNum(low[5:])
	case strings.HasPrefix(low, "a") && (len(low) == 1 || isDigit(low[1])):
		return Alpha, leadingNum(low[1:])
	case strings.HasPrefix(low, "patch") || strings.HasPrefix(low, "pl") ||
		strings.HasPrefix(low, "p"):
		return Stable, 0
	default:
		return Dev, 0
	}
}

func leadingNum(s string) int {
	s = strings.TrimLeft(s, ".-")
	end := 0
	for end < len(s) && isDigit(s[end]) {
		end++
	}
	if end == 0 {
		return 0
	}
	n, _ := strconv.Atoi(s[:end])
	return n
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// Compare returns -1 if v < other, 0 if equal, +1 if v > other.
// Dev versions sort below all numeric versions; among themselves they
// compare alphabetically by branch (deterministic, not semantically
// meaningful).
func (v Version) Compare(other Version) int {
	// Dev vs dev: alphabetical by branch.
	if v.Stability == Dev && other.Stability == Dev {
		return strings.Compare(v.Branch, other.Branch)
	}
	if v.Stability == Dev {
		return -1
	}
	if other.Stability == Dev {
		return 1
	}
	if c := cmpInt(v.Major, other.Major); c != 0 {
		return c
	}
	if c := cmpInt(v.Minor, other.Minor); c != 0 {
		return c
	}
	if c := cmpInt(v.Patch, other.Patch); c != 0 {
		return c
	}
	if c := cmpInt(int(v.Stability), int(other.Stability)); c != 0 {
		return c
	}
	return cmpInt(v.PreNum, other.PreNum)
}

// Equal reports semantic equality. Two versions are equal iff Compare returns 0.
func (v Version) Equal(other Version) bool { return v.Compare(other) == 0 }

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
