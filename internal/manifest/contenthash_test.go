package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

// TestContentHashMatchesFixtures pins gomposer's ContentHash implementation
// to Composer's Locker::getContentHash. Each expected value was produced
// by running Composer against the same input manifest and reading
// composer.lock's "content-hash" field. Regenerate by running Composer
// against the fixture and updating the expected string below.
func TestContentHashMatchesFixtures(t *testing.T) {
	cases := []struct {
		file string
		want string
	}{
		// "name" is itself a relevant key, so this does NOT filter down
		// to {}: it filters to {"name":"acme\/tool"}. Verified against
		// real Composer 2.10.1 (`composer update --no-install`), whose
		// composer.lock reports this exact content-hash.
		{"empty.json", "66faaf7e5ae99901ef2a7bc55f502e44"},

		// Regenerated with Composer 2.10.1 / PHP 8.5.8: `composer update
		// --no-scripts --no-plugins --ignore-platform-reqs --no-install
		// --no-interaction` in a throwaway project containing the fixture
		// as composer.json, then reading composer.lock's "content-hash".
		// Update these when regenerating.
		{"minimal.json", "822d0ede6d9a7bac758028c759bc02ae"},
		{"with-config-platform.json", "ac15b7352f60dd1e58a5ef90191820bd"},
		{"with-repositories.json", "87c9d1e7b0d0de21b152e0b2822a5d2d"},
		{"with-unicode-extra.json", "e0f209596b8ff73817f55042c4b9416b"},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			path := filepath.Join("testdata", "contenthash", tc.file)
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			got, err := ContentHash(body)
			if err != nil {
				t.Fatalf("ContentHash: %v", err)
			}
			if got != tc.want {
				t.Errorf("ContentHash(%s) = %q, want %q", tc.file, got, tc.want)
			}
		})
	}
}

// TestPhpCompatibleJSONEscapesSlashes verifies the slash-escape transform
// on a value containing a URL. PHP's json_encode escapes / as \/ by
// default; Go's json.Marshal never does.
func TestPhpCompatibleJSONEscapesSlashes(t *testing.T) {
	in := map[string]any{"url": "https://example.com/path"}
	got, err := phpCompatibleJSON(in)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"url":"https:\/\/example.com\/path"}`
	if string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// TestPhpCompatibleJSONEscapesNonASCII verifies non-ASCII → \uXXXX. PHP's
// default json_encode escapes non-ASCII; Go emits raw UTF-8.
func TestPhpCompatibleJSONEscapesNonASCII(t *testing.T) {
	in := map[string]any{"note": "café"}
	got, err := phpCompatibleJSON(in)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"note":"caf\u00e9"}`
	if string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// TestPhpCompatibleJSONHandlesAstralPlaneAsSurrogatePair — PHP's
// json_encode encodes characters above U+FFFF as UTF-16 surrogate pairs
// (😊 for U+1F60A). We must match.
func TestPhpCompatibleJSONHandlesAstralPlaneAsSurrogatePair(t *testing.T) {
	in := map[string]any{"emoji": "😊"} // U+1F60A
	got, err := phpCompatibleJSON(in)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"emoji":"\ud83d\ude0a"}`
	if string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
