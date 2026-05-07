package auth

import "regexp"

// We match the most common shapes Composer/composer-go produces. The goal
// is hygiene against accidental logging — code that handles a Credentials
// value should still avoid passing it to a logger in the first place.
var redactPatterns = []*regexp.Regexp{
	// "Authorization: Bearer <tok>" / "Authorization: token <tok>"
	regexp.MustCompile(`(?i)(authorization:\s*(?:bearer|token)\s+)\S+`),
	// "Private-Token: <tok>"
	regexp.MustCompile(`(?i)(private-token:\s*)\S+`),
	// "password":"...", "token":"...", "oauth":"..."
	regexp.MustCompile(`(?i)("(?:password|token|oauth)"\s*:\s*")[^"]*(")`),
}

// Redact returns s with credential-shaped substrings replaced by REDACTED.
// Safe to call on arbitrary strings; non-matching input is returned as-is.
func Redact(s string) string {
	out := s
	out = redactPatterns[0].ReplaceAllString(out, `${1}REDACTED`)
	out = redactPatterns[1].ReplaceAllString(out, `${1}REDACTED`)
	out = redactPatterns[2].ReplaceAllString(out, `${1}REDACTED${2}`)
	return out
}
