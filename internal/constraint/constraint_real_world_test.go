package constraint

import "testing"

// realWorldEntry is one corpus row. If wantVersion is non-empty, the test
// asserts c.Satisfies(ParseVersion(wantVersion)) == wantSatisfy.
// If wantVersion is empty, only the parse is checked.
type realWorldEntry struct {
	source      string // human-readable origin, for failure messages
	constraint  string
	wantVersion string
	wantSatisfy bool
}

// realWorldCorpus is a sweep of constraint strings copied from real
// composer.json files of widely-used PHP packages. Every entry must parse
// without error; populated wantVersion fields additionally pin satisfaction.
var realWorldCorpus = []realWorldEntry{
	// --- PHP platform constraints ---
	{"symfony/console:php", ">=8.2", "8.3.0", true},
	{"laravel/framework:php", "^8.2", "8.3.0", true},
	{"phpunit/phpunit:php", ">=8.1 <8.4", "8.2.0", true},
	{"phpunit/phpunit:php-strict", ">=8.1,<8.4", "8.4.0", false},
	{"doctrine/orm:php", "^7.4 || ^8.0", "8.1.0", true},
	{"monolog/monolog:php", ">=8.1", "8.2.5", true},
	{"guzzlehttp/guzzle:php", "^7.2.5 || ^8.0", "8.1.0", true},
	{"psr/log:php", ">=8.0.0", "8.0.0", true},

	// --- Caret / tilde (Symfony / Laravel ecosystem) ---
	{"symfony/console:caret", "^6.4 || ^7.0", "7.0.5", true},
	{"laravel/framework:caret", "^11.0", "11.5.0", true},
	{"laravel/sanctum:tilde", "~4.0", "4.1.0", true},
	{"laravel/passport:tilde", "~12.0", "12.0.0", true},
	{"doctrine/dbal:caret", "^3.6 || ^4.0", "4.1.0", true},
	{"phpunit/phpunit:caret", "^10.5 || ^11.0", "11.2.0", true},
	{"mockery/mockery:caret", "^1.6", "1.6.5", true},
	{"fakerphp/faker:caret", "^1.23", "1.24.0", true},

	// --- Hyphen ranges ---
	{"hyphen-full", "1.0.0 - 2.0.0", "1.5.0", true},
	{"hyphen-partial-rhs", "1.0 - 2.0", "2.0.5", true},
	{"hyphen-partial-rhs-bound", "1.0 - 2.0", "2.1.0", false},
	{"hyphen-major-only", "1.0 - 2", "2.99.99", true},
	{"hyphen-major-bound", "1.0 - 2", "3.0.0", false},
	{"hyphen-php-style", "8.0 - 8.5", "8.4.0", true},
	{"hyphen-php-style-out", "8.0 - 8.5", "8.6.0", false},

	// --- Comma AND-separators (Drupal core idiom, some PSR consumers) ---
	{"drupal-core:php", ">=8.1.0,<8.4", "8.2.0", true},
	{"drupal-core:php-out", ">=8.1.0,<8.4", "8.4.0", false},
	{"comma-and-spaces", ">=1.0 , <2.0", "1.5.0", true},
	{"comma-three-way", ">=1.0,<2.0,!=1.5.0", "1.5.0", false},
	{"comma-three-way-ok", ">=1.0,<2.0,!=1.5.0", "1.4.0", true},

	// --- Operator spacing ---
	{"spaced->=", ">= 1.0", "1.0.0", true},
	{"spaced-<", "< 2.0", "1.9.9", true},
	{"spaced-^", "^ 1.2", "1.5.0", true},
	{"spaced-mixed", ">= 1.0 < 2.0", "1.5.0", true},

	// --- Wildcards ---
	{"wildcard-star", "1.*", "1.99.0", true},
	{"wildcard-x", "1.x", "1.5.0", true},
	{"wildcard-minor", "1.2.*", "1.2.5", true},
	{"wildcard-out-of-band", "1.2.*", "1.3.0", false},
	{"wildcard-universal", "*", "9.9.9", true},

	// --- Stability suffix ---
	{"stab-suffix-dev", "1.0.0@dev", "1.0.0", true},
	{"stab-suffix-stable", "^2.0@stable", "2.5.0", true},
	{"stab-suffix-beta", "^3.0@beta", "3.0.0", true},

	// --- Dev branches and aliases ---
	{"dev-master", "dev-master", "dev-master", true},
	{"dev-main", "dev-main", "dev-main", true},
	{"dev-slashed", "dev-feature/awesome", "dev-feature/awesome", true},
	{"dev-pinned-sha", "dev-main#abc1234", "", false}, // parse-only
	{"alias-1x-dev", "1.x-dev", "1.x-dev", true},
	{"alias-2x-dev", "2.x-dev", "2.x-dev", true},
	{"alias-1.0.x-dev", "1.0.x-dev", "1.0.x-dev", true},
	{"caret-matches-alias", "^1.0", "1.x-dev", true},
	{"caret-rejects-alias", "^1.0", "2.x-dev", false},

	// --- Inline aliases ("as") ---
	{"inline-alias", "dev-feat-foo as 2.0.1", "dev-feat-foo", true},
	{"inline-alias-spaced", "dev-feat-foo  as  2.0.1", "dev-feat-foo", true},

	// --- OR alternatives ---
	{"single-pipe-or", "^7.2|^8.0", "7.4.0", true},
	{"double-pipe-or", "^7.2 || ^8.0", "8.1.0", true},
	{"triple-or", "^6.0 || ^7.0 || ^8.0", "8.2.0", true},

	// --- Pre-release variants ---
	{"prerelease-rc", "1.0.0-RC1", "1.0.0-RC1", true},
	{"prerelease-beta", "^1.0@beta", "1.0.0-beta1", false}, // numeric major bound
	{"prerelease-alpha", "1.2.3-alpha", "1.2.3-alpha", true},

	// --- Leading v tolerance ---
	{"leading-v-exact", "v1.2.3", "1.2.3", true},
	{"leading-v-caret", "^v1.2.3", "1.2.5", true},

	// --- Partial versions on either side of common operators ---
	{"partial-ge", ">=1", "1.0.0", true},
	{"partial-lt", "<2", "1.99.99", true},
	{"partial-eq", "1.0", "1.0.0", true},
}

func TestRealWorldConstraintCorpus(t *testing.T) {
	for _, e := range realWorldCorpus {
		c, err := Parse(e.constraint)
		if err != nil {
			t.Errorf("[%s] Parse(%q): %v", e.source, e.constraint, err)
			continue
		}
		if e.wantVersion == "" {
			continue
		}
		v, err := ParseVersion(e.wantVersion)
		if err != nil {
			t.Errorf("[%s] ParseVersion(%q): %v", e.source, e.wantVersion, err)
			continue
		}
		if got := c.Satisfies(v); got != e.wantSatisfy {
			t.Errorf("[%s] %q satisfies %q = %v, want %v",
				e.source, e.wantVersion, e.constraint, got, e.wantSatisfy)
		}
	}
}

// TestRealWorldCorpusSize is a guard against future PRs accidentally
// shrinking the corpus. The number is intentionally a floor, not a target.
func TestRealWorldCorpusSize(t *testing.T) {
	if len(realWorldCorpus) < 50 {
		t.Errorf("real-world corpus shrank to %d entries; floor is 50", len(realWorldCorpus))
	}
}
