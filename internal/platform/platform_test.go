package platform

import "testing"

func TestStubFingerprint(t *testing.T) {
	got, err := Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	// Pinned value: changing this string is a deliberate cache-bust.
	// Stage 2 will swap to a real `php -r` probe; until then this stays "php-unknown".
	if got != "php-unknown" {
		t.Errorf("Fingerprint = %q, want php-unknown", got)
	}
}
