package autoload

import (
	"reflect"
	"testing"
)

func TestScanSimpleNamespacedClass(t *testing.T) {
	src := `<?php
namespace Acme\Foo;
class Bar {}
`
	got, err := scanClasses([]byte(src))
	if err != nil {
		t.Fatalf("scanClasses: %v", err)
	}
	want := []string{"Acme\\Foo\\Bar"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestScanGlobalClass(t *testing.T) {
	src := `<?php class Top {}`
	got, _ := scanClasses([]byte(src))
	if !reflect.DeepEqual(got, []string{"Top"}) {
		t.Errorf("got %v", got)
	}
}

func TestScanInterfaceTraitEnum(t *testing.T) {
	src := `<?php
namespace N;
interface I {}
trait T {}
enum E {}
`
	got, _ := scanClasses([]byte(src))
	want := []string{"N\\I", "N\\T", "N\\E"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestScanBracketedNamespaces(t *testing.T) {
	src := `<?php
namespace A {
    class X {}
}
namespace B {
    class Y {}
}
`
	got, _ := scanClasses([]byte(src))
	want := []string{"A\\X", "B\\Y"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestScanModifiersAreSkipped(t *testing.T) {
	src := `<?php
namespace N;
abstract class A {}
final class B {}
final readonly class C {}
`
	got, _ := scanClasses([]byte(src))
	want := []string{"N\\A", "N\\B", "N\\C"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestScanAnonymousClassIsExcluded(t *testing.T) {
	src := `<?php
namespace N;
$x = new class { public function f() {} };
class Real {}
`
	got, _ := scanClasses([]byte(src))
	if !reflect.DeepEqual(got, []string{"N\\Real"}) {
		t.Errorf("got %v, want only N\\Real", got)
	}
}

func TestScanIgnoresClassKeywordInsideStrings(t *testing.T) {
	cases := []string{
		`<?php $s = 'class Fake {}'; class Real {}`,
		`<?php $s = "class Fake {}"; class Real {}`,
		`<?php /* class Fake {} */ class Real {}`,
		`<?php // class Fake {}` + "\n" + `class Real {}`,
		`<?php # class Fake {}` + "\n" + `class Real {}`,
	}
	for _, src := range cases {
		got, _ := scanClasses([]byte(src))
		if !reflect.DeepEqual(got, []string{"Real"}) {
			t.Errorf("src %q: got %v, want [Real]", src, got)
		}
	}
}

func TestScanIgnoresClassKeywordInHeredoc(t *testing.T) {
	src := "<?php\n$s = <<<EOT\nclass Fake {}\nEOT;\nclass Real {}\n"
	got, _ := scanClasses([]byte(src))
	if !reflect.DeepEqual(got, []string{"Real"}) {
		t.Errorf("got %v, want [Real]", got)
	}
}

func TestScanIgnoresUseStatements(t *testing.T) {
	src := `<?php
namespace N;
use Other\Thing;
use Foo\{Bar, Baz};
class Real {}
`
	got, _ := scanClasses([]byte(src))
	if !reflect.DeepEqual(got, []string{"N\\Real"}) {
		t.Errorf("got %v, want [N\\Real]", got)
	}
}

func TestScanClassConstAndStaticAccess(t *testing.T) {
	// Foo::class and Foo::method() must not register as declarations.
	src := `<?php
namespace N;
class Foo {
    public function f() {
        return Bar::class;
    }
}
`
	got, _ := scanClasses([]byte(src))
	if !reflect.DeepEqual(got, []string{"N\\Foo"}) {
		t.Errorf("got %v", got)
	}
}

func TestScanConditionalClass(t *testing.T) {
	// Composer's authoritative classmap behaviour: classes inside
	// `if (false) { ... }` are still indexed.
	src := `<?php
namespace N;
if (false) {
    class Hidden {}
} else {
    class Visible {}
}
`
	got, _ := scanClasses([]byte(src))
	want := []string{"N\\Hidden", "N\\Visible"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestScanRejectsUnterminatedString(t *testing.T) {
	src := `<?php $s = 'unterminated`
	if _, err := scanClasses([]byte(src)); err == nil {
		t.Error("expected error on unterminated string")
	}
}
