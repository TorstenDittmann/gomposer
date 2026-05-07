package autoload

import (
	"strings"
	"testing"
)

func TestInitHashIs32HexChars(t *testing.T) {
	got := InitHash("/home/u/projects/blog")
	if len(got) != 32 {
		t.Fatalf("len = %d, want 32", len(got))
	}
	for _, r := range got {
		ok := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
		if !ok {
			t.Fatalf("non-hex rune %q in %q", r, got)
		}
	}
}

func TestInitHashIsDeterministic(t *testing.T) {
	a := InitHash("/abs/path")
	b := InitHash("/abs/path")
	if a != b {
		t.Errorf("hash not deterministic: %s vs %s", a, b)
	}
}

func TestInitHashDiffersByPath(t *testing.T) {
	a := InitHash("/abs/path/one")
	b := InitHash("/abs/path/two")
	if a == b {
		t.Errorf("hashes should differ: %s == %s", a, b)
	}
}

func TestInitClassName(t *testing.T) {
	name := InitClassName("/abs/path")
	if !strings.HasPrefix(name, "ComposerAutoloaderInit") {
		t.Errorf("name = %q, want ComposerAutoloaderInit prefix", name)
	}
	if len(name) != len("ComposerAutoloaderInit")+32 {
		t.Errorf("name length = %d", len(name))
	}
}
