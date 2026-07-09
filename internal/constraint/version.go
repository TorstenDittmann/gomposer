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
	// StabilityFlag is the literal value of an "@<stab>" suffix on the input
	// (e.g. "dev", "stable", "beta"). Empty when the suffix is absent. This
	// is recorded for the resolver's stability policy to consume; the parser
	// itself ignores it once stripped.
	StabilityFlag string
	// Original is the input string, retained for round-tripping.
	Original string
}

// ParseVersion parses a PHP-style version string.
func ParseVersion(s string) (Version, error) {
	original := s
	v := Version{Original: original, Stability: Stable}

	// "@<stability>" suffix overrides package-global minimum-stability for
	// this constraint only. Strip it before structural parsing; the suffix is
	// recorded on v.StabilityFlag for the resolver to consult.
	if at := strings.LastIndexByte(s, '@'); at >= 0 {
		flag := s[at+1:]
		if flag != "" && !strings.ContainsAny(flag, ".-+/ ") {
			v.StabilityFlag = flag
			s = s[:at]
		}
	}

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

	// Alias form: "1.x-dev", "1.2.x-dev", "1.x", "1.2.x" (Composer treats
	// both with and without -dev as dev-stability aliases — we accept both).
	if i := strings.Index(body, "x"); i >= 0 {
		// Tolerate optional trailing "-dev" / ".x-dev".
		head := strings.TrimRight(body[:i], ".")
		// Must look like an integer-and-dots prefix.
		ok := head != ""
		for _, ch := range head {
			if !(ch == '.' || (ch >= '0' && ch <= '9')) {
				ok = false
				break
			}
		}
		if ok {
			parts := strings.Split(head, ".")
			nums := []int{0, 0, 0}
			for j, p := range parts {
				if j >= 3 {
					break
				}
				n, err := strconv.Atoi(p)
				if err != nil {
					return v, fmt.Errorf("constraint: invalid alias version %q", s)
				}
				nums[j] = n
			}
			v.Major, v.Minor, v.Patch = nums[0], nums[1], nums[2]
			v.Stability = Dev
			v.Branch = s // keep the original form for diagnostics
			return v, nil
		}
	}

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
// meaningful). Branch-alias dev versions (e.g. "1.x-dev") have non-zero
// Major/Minor/Patch and compare numerically against each other but still
// below stable versions with the same numbers.
func (v Version) Compare(other Version) int {
	// Pure dev branches (like `dev-main`) have Branch set and no numeric
	// component; they sort below every numeric version and among each other
	// alphabetically. We discriminate by Branch, not by Major==0, because
	// caret/tilde upper bounds like `<0.4.0-dev` are numeric-with-Dev-
	// stability — they must NOT trigger this special case.
	vPureDev := v.Stability == Dev && v.Branch != ""
	oPureDev := other.Stability == Dev && other.Branch != ""
	if vPureDev && oPureDev {
		return strings.Compare(v.Branch, other.Branch)
	}
	if vPureDev {
		return -1
	}
	if oPureDev {
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
