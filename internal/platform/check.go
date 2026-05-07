package platform

import (
	"strings"

	"github.com/torstendittmann/composer-go/internal/constraint"
)

// ViolationKind classifies why a platform req was not satisfied.
type ViolationKind int

const (
	// ViolationVersion: the req is for php or an installed extension whose
	// reported version doesn't satisfy the constraint.
	ViolationVersion ViolationKind = iota
	// ViolationMissing: the req is for an extension that is not loaded.
	ViolationMissing
	// ViolationLibIgnored: the req is `lib-*`, which composer-go does not
	// implement. Not a real failure; surfaced once per run as info.
	ViolationLibIgnored
	// ViolationUnparseable: the constraint string itself failed to parse.
	ViolationUnparseable
)

// Violation describes a single failed platform req check.
type Violation struct {
	Req        string        // "php", "ext-mbstring", "lib-curl", ...
	Constraint string        // raw constraint from the manifest
	Kind       ViolationKind
	// Have describes what the runtime actually has, suitable for messages.
	// Examples: "php 8.2.14", "ext-openssl 3.1.4", "ext-json (no version)",
	// "ext-curl not loaded".
	Have string
	// PlatformVersion is the runtime version when applicable; zero otherwise.
	PlatformVersion constraint.Version
}

// IsPlatformReq reports whether a require key denotes a platform req
// (php / ext-* / lib-*) rather than a regular package name. The classifier
// matches Composer's: any key starting with `php`, `ext-`, or `lib-`.
func IsPlatformReq(name string) bool {
	if name == "php" || strings.HasPrefix(name, "php-") {
		return true
	}
	if strings.HasPrefix(name, "ext-") {
		return true
	}
	if strings.HasPrefix(name, "lib-") {
		return true
	}
	return false
}

// Check evaluates `requires` against `p` and returns the unsatisfied reqs.
// `ignored` is consulted by exact key match: any req present in the map is
// treated as if it had no constraint. A nil/empty map is fine.
//
// Non-platform reqs in `requires` are ignored by Check; the caller is
// responsible for filtering.
func Check(requires map[string]string, p *Platform, ignored map[string]bool) []Violation {
	if len(requires) == 0 {
		return nil
	}
	out := make([]Violation, 0)
	for name, raw := range requires {
		if !IsPlatformReq(name) {
			continue
		}
		if ignored != nil && ignored[name] {
			continue
		}
		switch {
		case name == "php" || strings.HasPrefix(name, "php-"):
			out = append(out, checkPHP(name, raw, p)...)
		case strings.HasPrefix(name, "ext-"):
			out = append(out, checkExt(name, raw, p)...)
		case strings.HasPrefix(name, "lib-"):
			out = append(out, Violation{Req: name, Constraint: raw, Kind: ViolationLibIgnored})
		}
	}
	return out
}

func checkPHP(name, raw string, p *Platform) []Violation {
	c, err := constraint.Parse(raw)
	if err != nil {
		return []Violation{{Req: name, Constraint: raw, Kind: ViolationUnparseable}}
	}
	if p == nil || p.PHPVersion.Original == "" {
		return []Violation{{Req: name, Constraint: raw, Kind: ViolationMissing, Have: "php (unknown)"}}
	}
	// `php-64bit` and similar sub-keys are out of MVP scope; treat as a php
	// version check ignoring the suffix. (Composer treats php-64bit as a
	// platform-arch req; we evaluate against runtime only.)
	if !c.Satisfies(p.PHPVersion) {
		return []Violation{{
			Req: name, Constraint: raw, Kind: ViolationVersion,
			Have:            "php " + p.PHPVersion.Original,
			PlatformVersion: p.PHPVersion,
		}}
	}
	return nil
}

func checkExt(name, raw string, p *Platform) []Violation {
	extName := strings.TrimPrefix(name, "ext-")
	c, err := constraint.Parse(raw)
	if err != nil {
		return []Violation{{Req: name, Constraint: raw, Kind: ViolationUnparseable}}
	}
	have, ok := p.ExtensionVersion(extName)
	if !ok {
		return []Violation{{
			Req: name, Constraint: raw, Kind: ViolationMissing,
			Have: name + " not loaded",
		}}
	}
	if have.Original == "" {
		// Wildcard ("*") is satisfied by mere presence; anything more
		// specific cannot be evaluated and is treated as unsatisfied.
		if isWildcardConstraint(raw) {
			return nil
		}
		return []Violation{{
			Req: name, Constraint: raw, Kind: ViolationVersion,
			Have: name + " (no version)",
		}}
	}
	if !c.Satisfies(have) {
		return []Violation{{
			Req: name, Constraint: raw, Kind: ViolationVersion,
			Have:            name + " " + have.Original,
			PlatformVersion: have,
		}}
	}
	return nil
}

// isWildcardConstraint returns true for the few raw constraint strings
// that always match. We include the empty string defensively; callers
// pass raw strings from JSON.
func isWildcardConstraint(raw string) bool {
	s := strings.TrimSpace(raw)
	return s == "" || s == "*" || s == ">=0" || s == ">=0.0" || s == ">=0.0.0"
}
