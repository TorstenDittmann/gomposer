// Package platform exposes a stable fingerprint of the PHP environment that
// will execute the user's project. The fingerprint flows into every cache
// key so that a different PHP runtime invalidates resolution-result and
// classmap caches automatically.
//
// Stage 1 implementation is a stub: it returns "php-unknown". This keeps the
// shape of every downstream cache stable while the real probe is built in
// Stage 2 (see docs/superpowers/specs/2026-05-07-composer-go-design.md,
// section "Stage 2 — Real-world coverage", "Platform req detection").
//
// When Stage 2 lands and replaces this implementation with a real probe,
// every cache entry produced by Stage 1 will naturally miss because their
// key contains "php-unknown" while the new entries will contain something
// like "php-8.2.14;ext-mbstring;ext-json;...". That is the desired behavior.
package platform

// Fingerprint returns a stable string identifying the runtime PHP. Stage 1
// always returns "php-unknown".
func Fingerprint() (string, error) {
	return "php-unknown", nil
}
