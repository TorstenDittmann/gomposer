package vcs

import "strings"

// expandAliases returns the alias version strings that apply to ver, given
// the package's `extra.branch-alias` map. Only dev-* versions are aliased.
//
// Composer accepts branch-alias keys both with and without the "dev-"
// prefix. We honour both spellings.
func expandAliases(ver string, aliases map[string]string) []string {
	if len(aliases) == 0 || !strings.HasPrefix(ver, "dev-") {
		return nil
	}
	branch := strings.TrimPrefix(ver, "dev-")
	var out []string
	for k, v := range aliases {
		key := strings.TrimPrefix(k, "dev-")
		if key == branch && v != "" {
			out = append(out, v)
		}
	}
	return out
}
