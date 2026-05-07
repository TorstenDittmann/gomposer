package autoload

import (
	"fmt"
	"regexp"
	"strings"
)

// excludeMatcher is a compiled set of `exclude-from-classmap` patterns.
// Patterns are glob-style with `**` and `*`. A trailing slash means "this
// is a directory; match the directory and everything under it."
type excludeMatcher struct {
	res []*regexp.Regexp
}

func (m *excludeMatcher) Match(rel string) bool {
	if m == nil {
		return false
	}
	for _, re := range m.res {
		if re.MatchString(rel) {
			return true
		}
	}
	return false
}

func compileExclude(patterns []string) (*excludeMatcher, error) {
	m := &excludeMatcher{}
	for _, p := range patterns {
		re, err := globToRegexp(p)
		if err != nil {
			return nil, fmt.Errorf("autoload: exclude %q: %w", p, err)
		}
		m.res = append(m.res, re)
	}
	return m, nil
}

// globToRegexp translates a Composer-style glob into an anchored regexp.
//
//	"tests/"        -> ^tests/.*$         (or ^tests/$)
//	"**/Tests/"     -> ^.*/Tests/.*$  AND ^Tests/.*$  (the former is enough
//	                                                   if we always have a
//	                                                   leading segment)
//	"**/*Test.php"  -> ^(.*/)?[^/]*Test\.php$
//
// `**` matches zero or more path segments. `*` matches zero or more
// non-slash characters. Other regex meta-characters in the input are
// escaped.
func globToRegexp(pat string) (*regexp.Regexp, error) {
	dirOnly := strings.HasSuffix(pat, "/")
	if dirOnly {
		pat = strings.TrimRight(pat, "/")
	}
	var b strings.Builder
	b.WriteString("^")
	i := 0
	for i < len(pat) {
		switch {
		case strings.HasPrefix(pat[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 3
		case strings.HasPrefix(pat[i:], "**"):
			b.WriteString(".*")
			i += 2
		case pat[i] == '*':
			b.WriteString("[^/]*")
			i++
		default:
			c := pat[i]
			if isRegexMeta(c) {
				b.WriteByte('\\')
			}
			b.WriteByte(c)
			i++
		}
	}
	if dirOnly {
		b.WriteString("(/.*)?$")
	} else {
		b.WriteString("$")
	}
	return regexp.Compile(b.String())
}

func isRegexMeta(c byte) bool {
	switch c {
	case '\\', '.', '+', '?', '(', ')', '[', ']', '{', '}', '^', '$', '|':
		return true
	}
	return false
}
