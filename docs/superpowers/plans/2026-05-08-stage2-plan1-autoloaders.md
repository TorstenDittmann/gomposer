# Stage 2 / Plan 1: `files` and `classmap` Autoloader Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend the Stage-1 autoloader generator with full `files` and `classmap` support so that Symfony, Laravel, and any package that ships globals via a files-list or relies on a Composer classmap installs and boots correctly. PSR-0 remains intentionally unsupported (warn-and-skip — already documented in the spec).

**Architecture:**

- `files` is a pure-collection problem: gather per-package relative paths, sort by `(package name, listed order)` with the root manifest emitted *last*, and emit `vendor/composer/autoload_files.php` plus the `composerRequire<HASH>` helpers in `autoload_real.php`. We mirror Composer's exact bootstrap shape (`$GLOBALS['__composer_autoload_files']`) so projects that work under real Composer continue to work under gomposer.
- `classmap` requires a tokeniser. We implement a minimal PHP token scanner in pure Go — **no shelling out to `php`**. The scanner is just powerful enough to walk the file recognising `namespace`, `class`, `interface`, `trait`, `enum` declarations while skipping strings, comments, heredocs/nowdocs, and anonymous classes (`new class { ... }`). The output is a sorted `qualified-name → file-path` map written into `vendor/composer/autoload_classmap.php` and merged into `autoload_static.php`.
- Both features extend the existing render pipeline. The Stage-1 templates already emit empty `autoload_files.php` and `autoload_classmap.php` slots; Stage-2 fills those slots and adds the `composerRequire<HASH>` block in `autoload_real.php`.
- The generator stays byte-deterministic: the same `Options` always produces identical output. New ordering rules are explicit and testable.
- Existing public surface (`autoload.Generate`, `autoload.Options`, `autoload.Entry`) is preserved. The `Entry` struct gains nothing (the new data flows through the existing `registry.Autoload` already on it). `Options` does not change shape.

**Tech Stack:** Go stdlib only (`bufio`, `unicode`, `unicode/utf8`, `path/filepath`, `io/fs`, `sort`, `strings`). No new external deps. The PHP tokenizer is custom — bringing in a dependency for this would be over-kill (we only need ~7 token kinds) and would add a moving part for what is otherwise a stable contract.

**Depends on:**
- Stage 1 / Plan 5 (Autoloader) — extends `internal/autoload/`. Reuses `Entry`, `Options`, `renderData`, `renderAll`, `phpString`, `phpDir`, snapshot scaffolding.
- Stage 1 / Plan 1 (Foundations) — uses `manifest.Autoload.{Files,Classmap}`.
- Stage 1 / Plan 2 (Metadata) — `registry.Autoload.{Files,Classmap,PSR0}`. Does not require any change to `registry.Autoload`; it already carries `Files []string` and `Classmap []string`.

This plan is independent of any other Stage-2 work (VCS, scripts, platform). Plan-2-and-onwards can land in any order relative to this one.

---

## File structure

| Path | Responsibility |
|------|---------------|
| `internal/autoload/files.go` | Collect, sort, and uniquify `files` entries from root + packages |
| `internal/autoload/files_test.go` | Unit tests for files-collection ordering + dedup |
| `internal/autoload/classmap.go` | Walk classmap dirs/files, parse PHP, build `qualified-name → file-path` map |
| `internal/autoload/classmap_test.go` | Unit tests for classmap collection (incl. exclude-from-classmap globs) |
| `internal/autoload/phpscan.go` | Minimal PHP token scanner — class/interface/trait/enum/namespace extraction |
| `internal/autoload/phpscan_test.go` | Token scanner unit tests + table-driven fixtures |
| `internal/autoload/exclude.go` | Compile + match `exclude-from-classmap` glob patterns (`**/Tests/`) |
| `internal/autoload/exclude_test.go` | Glob matcher tests |
| `internal/autoload/templates.go` | **Modify** — rewire `autoloadFilesTpl`, `autoloadClassmapTpl`, `autoloadStaticTpl`, `autoloadRealTpl` |
| `internal/autoload/templates_test.go` | **Modify** — adjust template sanity tests for new placeholders |
| `internal/autoload/generator.go` | **Modify** — `Generate` collects files + classmap, threads them into `renderData` |
| `internal/autoload/generator_test.go` | **Modify** — extend snapshot suite to cover new outputs |
| `internal/autoload/psr4.go` | **Modify** — add a `WarnPSR0` helper that the generator calls per-package (warn-and-skip) |
| `internal/autoload/testdata/fixture-project/...` | **Modify** — add `files`-using package and a `classmap`-using package |
| `internal/autoload/testdata/expected/autoload_files.php` | **Replace** — non-empty snapshot |
| `internal/autoload/testdata/expected/autoload_classmap.php` | **Replace** — non-empty snapshot |
| `internal/autoload/testdata/expected/autoload_real.php` | **Replace** — now contains `composerRequire<HASH>` helpers |
| `internal/autoload/testdata/expected/autoload_static.php` | **Replace** — `$classMap` populated |
| `internal/autoload/testdata/polyfill-mbstring/...` | New Composer-shaped fixture for `files` (mirrors `symfony/polyfill-mbstring` v1.x layout) |
| `internal/autoload/testdata/legacy-classmap/...` | Synthetic classmap fixture: namespaced + global classes, traits, enums, anonymous-class trap |

---

## Task 1: `files` collection and ordering

**Files:**
- Create: `internal/autoload/files.go`
- Create: `internal/autoload/files_test.go`

The `files` autoloader is dead simple in spirit (require each path once at boot) but real Composer pins down a precise emission order: per-package, alphabetical by package name, listed order within a package, and the root manifest's `files` last. Two packages can list the same file (rare, but it happens via path repos); we dedupe by absolute output path.

- [ ] **Step 1: Write the failing test**

Create `internal/autoload/files_test.go`:

```go
package autoload

import (
	"reflect"
	"testing"

	"github.com/torstendittmann/gomposer/internal/manifest"
	"github.com/torstendittmann/gomposer/internal/registry"
)

func TestCollectFilesEmpty(t *testing.T) {
	got := CollectFiles(manifest.Autoload{}, nil)
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}

func TestCollectFilesSortsByPackageNameThenListedOrder(t *testing.T) {
	entries := []Entry{
		{
			Name:        "zeta/last",
			InstallPath: "vendor/zeta/last",
			Autoload:    registry.Autoload{Files: []string{"zz.php", "aa.php"}},
		},
		{
			Name:        "alpha/first",
			InstallPath: "vendor/alpha/first",
			Autoload:    registry.Autoload{Files: []string{"b.php", "a.php"}},
		},
	}
	got := CollectFiles(manifest.Autoload{}, entries)
	want := []FileEntry{
		{Path: "vendor/alpha/first/b.php", PackageName: "alpha/first"},
		{Path: "vendor/alpha/first/a.php", PackageName: "alpha/first"},
		{Path: "vendor/zeta/last/zz.php", PackageName: "zeta/last"},
		{Path: "vendor/zeta/last/aa.php", PackageName: "zeta/last"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCollectFilesPutsRootLast(t *testing.T) {
	root := manifest.Autoload{Files: []string{"app/bootstrap.php"}}
	entries := []Entry{
		{
			Name:        "alpha/first",
			InstallPath: "vendor/alpha/first",
			Autoload:    registry.Autoload{Files: []string{"helpers.php"}},
		},
	}
	got := CollectFiles(root, entries)
	if got[0].PackageName != "alpha/first" {
		t.Errorf("vendor entry should come first, got %s", got[0].PackageName)
	}
	if got[len(got)-1].Path != "app/bootstrap.php" {
		t.Errorf("root manifest entry should come last, got %v", got[len(got)-1])
	}
}

func TestCollectFilesDeduplicatesByOutputPath(t *testing.T) {
	entries := []Entry{
		{
			Name:        "alpha/first",
			InstallPath: "vendor/alpha/first",
			Autoload:    registry.Autoload{Files: []string{"helpers.php"}},
		},
		{
			Name:        "beta/second",
			InstallPath: "vendor/alpha/first", // unusual, but possible via path repos
			Autoload:    registry.Autoload{Files: []string{"helpers.php"}},
		},
	}
	got := CollectFiles(manifest.Autoload{}, entries)
	if len(got) != 1 {
		t.Errorf("expected 1 entry after dedup, got %d: %v", len(got), got)
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/autoload/...`

Expected: build error referencing undefined `CollectFiles` and `FileEntry`.

- [ ] **Step 3: Implement**

Create `internal/autoload/files.go`:

```go
package autoload

import (
	"path"
	"sort"

	"github.com/torstendittmann/gomposer/internal/manifest"
)

// FileEntry is one entry in the emitted $files array. PackageName is the
// owning package's canonical name ("vendor/foo") or empty string for the
// root manifest. We keep it on the struct so emission order can be
// asserted in tests and rendered into a stable hash key.
type FileEntry struct {
	Path        string
	PackageName string
}

// CollectFiles returns the merged, ordered list of `files` entries to emit
// in vendor/composer/autoload_files.php. Order matches real Composer:
//   1. Vendor entries, sorted alphabetically by package name. Within a
//      package, listed order is preserved.
//   2. Root manifest entries, last, in listed order.
//
// Duplicates by output path are dropped (first occurrence wins).
func CollectFiles(root manifest.Autoload, entries []Entry) []FileEntry {
	// Stable copy so we don't mutate caller's slice, sorted by package name.
	sorted := make([]Entry, len(entries))
	copy(sorted, entries)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})

	out := make([]FileEntry, 0)
	seen := make(map[string]struct{})

	add := func(p, pkg string) {
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, FileEntry{Path: p, PackageName: pkg})
	}

	for _, e := range sorted {
		for _, f := range e.Autoload.Files {
			add(path.Join(e.InstallPath, f), e.Name)
		}
	}
	for _, f := range root.Files {
		add(f, "")
	}
	return out
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/autoload/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/autoload/files.go internal/autoload/files_test.go
git commit -m "feat(autoload): collect and order files entries"
```

---

## Task 2: PHP token scanner — minimal but correct

**Files:**
- Create: `internal/autoload/phpscan.go`
- Create: `internal/autoload/phpscan_test.go`

We need a tokeniser that can find class declarations. Full PHP grammar is irrelevant: we just need to recognise enough lexical structure that strings, comments, and heredocs do not trick us into seeing a fake `class` keyword. The token kinds we care about:

- `<?php`, `<?=`, `?>` — open/close tags
- T_NAMESPACE — `namespace`
- T_USE — `use` (we skip its argument list to the next `;` so a `use Foo\Bar;` does not emit `Bar`)
- T_NEW — `new` (used to detect anonymous-class)
- T_CLASS, T_INTERFACE, T_TRAIT, T_ENUM
- T_ABSTRACT, T_FINAL, T_READONLY (modifiers — skipped during scanning)
- T_STRING — bare identifier
- T_NS_SEPARATOR — `\`
- `{`, `}`, `(`, `)`, `;`, `=`
- Whitespace (skipped)
- Comments: `//`, `#`, `/* ... */` (skipped, including `/** ... */` doc comments)
- Strings: `'...'`, `"..."`, with backslash escapes
- Heredoc/nowdoc: `<<<LABEL` ... `LABEL;` — content is fully skipped (we never need its tokens)

We do NOT need to handle inline HTML between `?>` and the next `<?php` — that text is not PHP code, so no class declarations live there.

- [ ] **Step 1: Write the failing tests**

Create `internal/autoload/phpscan_test.go`:

```go
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
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/autoload/... -run TestScan`

Expected: build error referencing `scanClasses`.

- [ ] **Step 3: Implement the scanner**

Create `internal/autoload/phpscan.go`:

```go
package autoload

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// scanClasses returns fully-qualified names of every class, interface,
// trait, or enum DECLARED in src. Anonymous classes (`new class`) are
// excluded. The returned slice preserves source order; duplicates within
// a single file (legal PHP if guarded by `if (!class_exists)`) are kept
// in source order.
//
// The scanner is a hand-rolled tokeniser. We intentionally implement only
// the lexical structures needed to identify declarations:
//   - PHP open/close tags
//   - whitespace, // / # / /* */ comments
//   - single- and double-quoted strings (with backslash escapes)
//   - heredoc / nowdoc (content fully skipped — we never need its tokens)
//   - identifiers and the `\` namespace separator
//   - the keywords namespace, use, new, class, interface, trait, enum
//
// Anything else (operators, numbers, parentheses) is passed through as a
// single byte. This works because we only ever inspect identifier and
// keyword tokens; unknown bytes never fool us into thinking we saw a
// declaration.
func scanClasses(src []byte) ([]string, error) {
	s := &phpScanner{src: src}
	if err := s.skipUntilOpenTag(); err != nil {
		return nil, err
	}

	var out []string
	var ns string             // current namespace, "" for global
	var bracketDepth int      // matched braces for bracketed `namespace X { ... }`
	bracketedNS := []string{} // stack of namespaces when inside bracketed form

	prevSig := tokOther // last "significant" token (non-whitespace, non-comment)

	for !s.eof() {
		tok, lit, err := s.next()
		if err != nil {
			return nil, err
		}
		switch tok {
		case tokEOF:
			return out, nil
		case tokWS, tokComment:
			continue
		case tokCloseTag:
			if err := s.skipUntilOpenTag(); err != nil {
				return nil, err
			}
			prevSig = tokOther
			continue
		case tokIdent:
			switch strings.ToLower(lit) {
			case "namespace":
				name, bracketed, err := s.readNamespaceName()
				if err != nil {
					return nil, err
				}
				if bracketed {
					bracketedNS = append(bracketedNS, ns)
					ns = name
				} else {
					ns = name
				}
				prevSig = tokIdent
				continue
			case "use":
				if err := s.skipUseStatement(); err != nil {
					return nil, err
				}
				prevSig = tokOther
				continue
			case "class", "interface", "trait", "enum":
				// Anonymous-class detection: `new class`.
				if prevSig == tokNew {
					prevSig = tokIdent
					continue
				}
				name, ok, err := s.readDeclName()
				if err != nil {
					return nil, err
				}
				if ok {
					out = append(out, qualify(ns, name))
				}
				prevSig = tokIdent
				continue
			case "new":
				prevSig = tokNew
				continue
			default:
				prevSig = tokIdent
				continue
			}
		case tokLBrace:
			bracketDepth++
			prevSig = tokOther
		case tokRBrace:
			bracketDepth--
			if len(bracketedNS) > 0 && bracketDepth < 0 {
				ns = bracketedNS[len(bracketedNS)-1]
				bracketedNS = bracketedNS[:len(bracketedNS)-1]
				bracketDepth = 0
			}
			prevSig = tokOther
		default:
			prevSig = tok
		}
	}
	return out, nil
}

func qualify(ns, name string) string {
	if ns == "" {
		return name
	}
	return ns + "\\" + name
}

type tokKind int

const (
	tokOther tokKind = iota
	tokWS
	tokComment
	tokIdent
	tokNew
	tokLBrace
	tokRBrace
	tokSemi
	tokCloseTag
	tokEOF
)

type phpScanner struct {
	src []byte
	pos int
}

func (s *phpScanner) eof() bool { return s.pos >= len(s.src) }

func (s *phpScanner) skipUntilOpenTag() error {
	for s.pos < len(s.src) {
		if s.pos+5 <= len(s.src) && string(s.src[s.pos:s.pos+5]) == "<?php" {
			s.pos += 5
			return nil
		}
		if s.pos+3 <= len(s.src) && string(s.src[s.pos:s.pos+3]) == "<?=" {
			s.pos += 3
			return nil
		}
		s.pos++
	}
	return nil // EOF without finding another open tag is fine
}

func (s *phpScanner) next() (tokKind, string, error) {
	if s.eof() {
		return tokEOF, "", nil
	}
	c := s.src[s.pos]

	// Whitespace
	if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
		for !s.eof() && isWS(s.src[s.pos]) {
			s.pos++
		}
		return tokWS, "", nil
	}

	// Comments and # comment
	if c == '/' && s.pos+1 < len(s.src) {
		nxt := s.src[s.pos+1]
		if nxt == '/' {
			for !s.eof() && s.src[s.pos] != '\n' {
				s.pos++
			}
			return tokComment, "", nil
		}
		if nxt == '*' {
			s.pos += 2
			for s.pos+1 < len(s.src) && !(s.src[s.pos] == '*' && s.src[s.pos+1] == '/') {
				s.pos++
			}
			if s.pos+1 >= len(s.src) {
				return tokEOF, "", errors.New("phpscan: unterminated /* comment")
			}
			s.pos += 2
			return tokComment, "", nil
		}
	}
	if c == '#' {
		for !s.eof() && s.src[s.pos] != '\n' {
			s.pos++
		}
		return tokComment, "", nil
	}

	// Close tag
	if c == '?' && s.pos+1 < len(s.src) && s.src[s.pos+1] == '>' {
		s.pos += 2
		return tokCloseTag, "", nil
	}

	// String literals
	if c == '\'' || c == '"' {
		if err := s.skipString(c); err != nil {
			return tokEOF, "", err
		}
		return tokOther, "", nil
	}

	// Heredoc / nowdoc
	if c == '<' && s.pos+2 < len(s.src) && s.src[s.pos+1] == '<' && s.src[s.pos+2] == '<' {
		if err := s.skipHeredoc(); err != nil {
			return tokEOF, "", err
		}
		return tokOther, "", nil
	}

	// Identifier / keyword (PHP allows _ and digits inside, must not start digit)
	if isIdentStart(c) {
		start := s.pos
		for !s.eof() && isIdentCont(s.src[s.pos]) {
			s.pos++
		}
		return tokIdent, string(s.src[start:s.pos]), nil
	}

	// Punctuation
	switch c {
	case '{':
		s.pos++
		return tokLBrace, "{", nil
	case '}':
		s.pos++
		return tokRBrace, "}", nil
	case ';':
		s.pos++
		return tokSemi, ";", nil
	}

	// Anything else: pass through one byte.
	s.pos++
	return tokOther, string(c), nil
}

func (s *phpScanner) skipString(quote byte) error {
	s.pos++ // opening quote
	for s.pos < len(s.src) {
		c := s.src[s.pos]
		if c == '\\' && s.pos+1 < len(s.src) {
			s.pos += 2
			continue
		}
		if c == quote {
			s.pos++
			return nil
		}
		s.pos++
	}
	return errors.New("phpscan: unterminated string")
}

// skipHeredoc consumes a heredoc/nowdoc starting at <<<. We do NOT need
// to honour PHP's interpolation rules — the body cannot contain a top-
// level `class` declaration. We just scan forward for the closing label
// at the start of a line.
func (s *phpScanner) skipHeredoc() error {
	s.pos += 3 // <<<
	// Optional whitespace, optional ' or " around the label.
	for s.pos < len(s.src) && (s.src[s.pos] == ' ' || s.src[s.pos] == '\t') {
		s.pos++
	}
	quoted := false
	if s.pos < len(s.src) && (s.src[s.pos] == '\'' || s.src[s.pos] == '"') {
		quoted = true
		s.pos++
	}
	labelStart := s.pos
	for s.pos < len(s.src) && isIdentCont(s.src[s.pos]) {
		s.pos++
	}
	label := string(s.src[labelStart:s.pos])
	if label == "" {
		return errors.New("phpscan: heredoc with empty label")
	}
	if quoted {
		if s.pos >= len(s.src) || (s.src[s.pos] != '\'' && s.src[s.pos] != '"') {
			return errors.New("phpscan: heredoc unterminated label quote")
		}
		s.pos++
	}
	// Skip to end of line.
	for s.pos < len(s.src) && s.src[s.pos] != '\n' {
		s.pos++
	}
	if s.pos < len(s.src) {
		s.pos++ // newline
	}
	// Scan lines until we find one that begins (after optional whitespace
	// from PHP 7.3+ flexible heredocs) with the label.
	for s.pos < len(s.src) {
		// Optional indent
		lineStart := s.pos
		for s.pos < len(s.src) && (s.src[s.pos] == ' ' || s.src[s.pos] == '\t') {
			s.pos++
		}
		if s.pos+len(label) <= len(s.src) && string(s.src[s.pos:s.pos+len(label)]) == label {
			after := s.pos + len(label)
			if after >= len(s.src) || !isIdentCont(s.src[after]) {
				s.pos = after
				return nil
			}
		}
		// Not a closing label — skip rest of line.
		s.pos = lineStart
		for s.pos < len(s.src) && s.src[s.pos] != '\n' {
			s.pos++
		}
		if s.pos < len(s.src) {
			s.pos++
		}
	}
	return errors.New("phpscan: unterminated heredoc")
}

// readNamespaceName parses a namespace declaration's name and detects
// whether it is bracketed (`namespace X { ... }`) or unbracketed
// (`namespace X;`). Empty namespace ("namespace { ... }") returns "" and
// bracketed=true.
func (s *phpScanner) readNamespaceName() (name string, bracketed bool, err error) {
	var parts []string
	for {
		s.skipWSAndComments()
		if s.eof() {
			return "", false, errors.New("phpscan: unexpected EOF in namespace decl")
		}
		c := s.src[s.pos]
		if c == ';' {
			s.pos++
			return strings.Join(parts, "\\"), false, nil
		}
		if c == '{' {
			s.pos++
			return strings.Join(parts, "\\"), true, nil
		}
		if c == '\\' {
			s.pos++
			continue
		}
		if isIdentStart(c) {
			start := s.pos
			for !s.eof() && isIdentCont(s.src[s.pos]) {
				s.pos++
			}
			parts = append(parts, string(s.src[start:s.pos]))
			continue
		}
		return "", false, fmt.Errorf("phpscan: unexpected %q in namespace decl", c)
	}
}

// skipUseStatement consumes everything up to and including the next `;`.
// Strings, comments, and braces inside the use statement are honoured so
// that group-use forms (`use Foo\{Bar, Baz};`) terminate cleanly.
func (s *phpScanner) skipUseStatement() error {
	depth := 0
	for !s.eof() {
		tok, _, err := s.next()
		if err != nil {
			return err
		}
		switch tok {
		case tokLBrace:
			depth++
		case tokRBrace:
			depth--
		case tokSemi:
			if depth <= 0 {
				return nil
			}
		case tokEOF:
			return nil
		}
	}
	return nil
}

// readDeclName reads the identifier following class/interface/trait/enum.
// Returns ok=false if the next significant token is not an identifier
// (defensive — protects against malformed sources).
func (s *phpScanner) readDeclName() (string, bool, error) {
	s.skipWSAndComments()
	if s.eof() {
		return "", false, nil
	}
	c := s.src[s.pos]
	if !isIdentStart(c) {
		return "", false, nil
	}
	start := s.pos
	for !s.eof() && isIdentCont(s.src[s.pos]) {
		s.pos++
	}
	return string(s.src[start:s.pos]), true, nil
}

func (s *phpScanner) skipWSAndComments() {
	for !s.eof() {
		c := s.src[s.pos]
		if isWS(c) {
			s.pos++
			continue
		}
		if c == '/' && s.pos+1 < len(s.src) {
			nxt := s.src[s.pos+1]
			if nxt == '/' {
				for !s.eof() && s.src[s.pos] != '\n' {
					s.pos++
				}
				continue
			}
			if nxt == '*' {
				s.pos += 2
				for s.pos+1 < len(s.src) && !(s.src[s.pos] == '*' && s.src[s.pos+1] == '/') {
					s.pos++
				}
				if s.pos+1 < len(s.src) {
					s.pos += 2
				}
				continue
			}
		}
		if c == '#' {
			for !s.eof() && s.src[s.pos] != '\n' {
				s.pos++
			}
			continue
		}
		return
	}
}

func isWS(c byte) bool       { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}
func isIdentCont(c byte) bool {
	return isIdentStart(c) || unicode.IsDigit(rune(c))
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/autoload/... -run TestScan -v`

Expected: PASS for every TestScan* subtest.

- [ ] **Step 5: Commit**

```bash
git add internal/autoload/phpscan.go internal/autoload/phpscan_test.go
git commit -m "feat(autoload): minimal PHP token scanner for classmap extraction"
```

---

## Task 3: `exclude-from-classmap` glob matching

**Files:**
- Create: `internal/autoload/exclude.go`
- Create: `internal/autoload/exclude_test.go`

`exclude-from-classmap` is a per-package convention used by Symfony, Laravel, and most non-trivial libraries. The patterns look like `**/Tests/`, `**/test/*Test.php`, or just `tests/`. We need a small matcher that handles `**` (any number of path segments), `*` (any chars within a segment), and trailing-slash "this is a directory" semantics.

`path/filepath.Match` is not enough (`**` is not supported). We compile each pattern to a regular expression once and reuse it.

- [ ] **Step 1: Write the failing tests**

Create `internal/autoload/exclude_test.go`:

```go
package autoload

import "testing"

func TestExcludePatternBasic(t *testing.T) {
	cases := []struct {
		pat, path string
		want      bool
	}{
		{"tests/", "tests/Foo.php", true},
		{"tests/", "tests/sub/Foo.php", true},
		{"tests/", "src/tests/Foo.php", false},
		{"**/Tests/", "src/Tests/A.php", true},
		{"**/Tests/", "deep/sub/Tests/A.php", true},
		{"**/Tests/", "src/A.php", false},
		{"**/*Test.php", "src/FooTest.php", true},
		{"**/*Test.php", "src/Foo.php", false},
		{"src/legacy/", "src/legacy/old.php", true},
	}
	for _, tc := range cases {
		m, err := compileExclude([]string{tc.pat})
		if err != nil {
			t.Errorf("%s: compile: %v", tc.pat, err)
			continue
		}
		if got := m.Match(tc.path); got != tc.want {
			t.Errorf("pattern=%s path=%s got=%v want=%v", tc.pat, tc.path, got, tc.want)
		}
	}
}

func TestExcludeMultiplePatterns(t *testing.T) {
	m, _ := compileExclude([]string{"**/Tests/", "**/Fixtures/"})
	if !m.Match("src/Foo/Tests/X.php") {
		t.Errorf("Tests/ should match")
	}
	if !m.Match("src/Foo/Fixtures/X.php") {
		t.Errorf("Fixtures/ should match")
	}
	if m.Match("src/Foo/X.php") {
		t.Errorf("plain src path should not match")
	}
}

func TestExcludeEmptyMatchesNothing(t *testing.T) {
	m, _ := compileExclude(nil)
	if m.Match("anything") {
		t.Errorf("empty matcher must match nothing")
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/autoload/... -run TestExclude`

Expected: build error referencing `compileExclude`.

- [ ] **Step 3: Implement**

Create `internal/autoload/exclude.go`:

```go
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
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/autoload/... -run TestExclude`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/autoload/exclude.go internal/autoload/exclude_test.go
git commit -m "feat(autoload): exclude-from-classmap glob matcher"
```

---

## Task 4: Classmap collection

**Files:**
- Create: `internal/autoload/classmap.go`
- Create: `internal/autoload/classmap_test.go`

`CollectClassmap` is the bridge between the resolved entries and the scanner. For each package, walk every `autoload.classmap` entry: if it is a file, scan it; if it is a directory, recursively scan every `.php` and `.inc` file under it. Honour `autoload.exclude-from-classmap` per-package. The output is a sorted `map[string]string` (qualified-name → path-relative-to-project).

We need to extend the data the generator sees. `registry.Autoload` already carries `Classmap []string`; `exclude-from-classmap` is not on the struct yet — we add it via `Entry.ExcludeFromClassmap` (typed as `[]string`) so the orchestrator's `lock.Package` extraction (a separate plan item) can fill it later. For root manifest, we read `manifest.Autoload.ExcludeFromClassmap` once it is wired up; this plan adds the helper but does not yet add the manifest field (a small follow-up after Stage-1 manifest changes).

For now, accept `excludePatterns []string` on the per-package input. The orchestrator wiring task at the end of this plan plumbs both.

- [ ] **Step 1: Write the failing tests**

Create `internal/autoload/classmap_test.go`:

```go
package autoload

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/torstendittmann/gomposer/internal/manifest"
	"github.com/torstendittmann/gomposer/internal/registry"
)

func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCollectClassmapDirRecurses(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "vendor/acme/foo/src/A.php",
		`<?php namespace Acme\Foo; class A {}`)
	writeFile(t, root, "vendor/acme/foo/src/sub/B.php",
		`<?php namespace Acme\Foo\Sub; class B {}`)
	writeFile(t, root, "vendor/acme/foo/src/legacy.inc",
		`<?php class Legacy {}`)

	entries := []Entry{
		{
			Name:        "acme/foo",
			InstallPath: "vendor/acme/foo",
			Autoload:    registry.Autoload{Classmap: []string{"src/"}},
		},
	}
	got, err := CollectClassmap(root, manifest.Autoload{}, entries)
	if err != nil {
		t.Fatalf("CollectClassmap: %v", err)
	}
	want := map[string]string{
		"Acme\\Foo\\A":     "vendor/acme/foo/src/A.php",
		"Acme\\Foo\\Sub\\B": "vendor/acme/foo/src/sub/B.php",
		"Legacy":           "vendor/acme/foo/src/legacy.inc",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCollectClassmapHonoursExclude(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "vendor/acme/foo/src/Real.php",
		`<?php namespace Acme; class Real {}`)
	writeFile(t, root, "vendor/acme/foo/src/Tests/HiddenTest.php",
		`<?php namespace Acme\Tests; class HiddenTest {}`)

	entries := []Entry{
		{
			Name:                 "acme/foo",
			InstallPath:          "vendor/acme/foo",
			Autoload:             registry.Autoload{Classmap: []string{"src/"}},
			ExcludeFromClassmap:  []string{"**/Tests/"},
		},
	}
	got, err := CollectClassmap(root, manifest.Autoload{}, entries)
	if err != nil {
		t.Fatalf("CollectClassmap: %v", err)
	}
	if _, ok := got["Acme\\Tests\\HiddenTest"]; ok {
		t.Errorf("excluded class leaked into classmap: %v", got)
	}
	if _, ok := got["Acme\\Real"]; !ok {
		t.Errorf("non-excluded class missing from classmap: %v", got)
	}
}

func TestCollectClassmapAcceptsSingleFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "vendor/acme/foo/Stand.php",
		`<?php class Standalone {}`)
	entries := []Entry{
		{
			Name:        "acme/foo",
			InstallPath: "vendor/acme/foo",
			Autoload:    registry.Autoload{Classmap: []string{"Stand.php"}},
		},
	}
	got, err := CollectClassmap(root, manifest.Autoload{}, entries)
	if err != nil {
		t.Fatal(err)
	}
	if got["Standalone"] != "vendor/acme/foo/Stand.php" {
		t.Errorf("got %v", got)
	}
}

func TestCollectClassmapMissingPathErrors(t *testing.T) {
	root := t.TempDir()
	entries := []Entry{
		{
			Name:        "acme/foo",
			InstallPath: "vendor/acme/foo",
			Autoload:    registry.Autoload{Classmap: []string{"does-not-exist/"}},
		},
	}
	_, err := CollectClassmap(root, manifest.Autoload{}, entries)
	if err == nil {
		t.Error("expected error for missing classmap entry")
	}
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/autoload/... -run TestCollectClassmap`

Expected: build error referencing `CollectClassmap` and `Entry.ExcludeFromClassmap`.

- [ ] **Step 3: Add `ExcludeFromClassmap` to `Entry`**

Modify `internal/autoload/psr4.go` (the existing definition of `Entry`):

```go
type Entry struct {
	Name                string
	Version             string
	InstallPath         string
	Autoload            registry.Autoload
	// ExcludeFromClassmap holds the package's autoload.exclude-from-classmap
	// patterns. Each is a glob in Composer's dialect (`**/Tests/`,
	// `**/*Test.php`); see exclude.go for the full grammar. Empty for
	// packages that don't declare one.
	ExcludeFromClassmap []string
}
```

- [ ] **Step 4: Implement `CollectClassmap`**

Create `internal/autoload/classmap.go`:

```go
package autoload

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/torstendittmann/gomposer/internal/manifest"
)

// CollectClassmap walks every classmap entry of every package (and the
// root manifest's autoload.classmap) and returns the merged
// qualified-name → project-relative-path map.
//
// projectDir must be absolute. Path values in the result are forward-
// slash regardless of host OS so they round-trip cleanly into PHP.
//
// On a same-name collision (two packages declare the same class), the
// FIRST occurrence wins; we keep the same first-wins behaviour Composer
// has used since 2.0. A diagnostic is appended to the returned warnings
// slice in a follow-up; for now collisions are silently absorbed.
func CollectClassmap(projectDir string, root manifest.Autoload, entries []Entry) (map[string]string, error) {
	out := make(map[string]string)

	// Vendor entries first. Order matches Entry order so first-wins is
	// stable.
	for _, e := range entries {
		excl, err := compileExclude(e.ExcludeFromClassmap)
		if err != nil {
			return nil, err
		}
		for _, raw := range e.Autoload.Classmap {
			if err := scanInto(out, projectDir, e.InstallPath, raw, excl); err != nil {
				return nil, fmt.Errorf("autoload: %s: %w", e.Name, err)
			}
		}
	}
	// Root manifest entries, paths are relative to projectDir directly.
	rootExcl, err := compileExclude(rootExcludePatterns(root))
	if err != nil {
		return nil, err
	}
	for _, raw := range root.Classmap {
		if err := scanInto(out, projectDir, "", raw, rootExcl); err != nil {
			return nil, fmt.Errorf("autoload: root manifest: %w", err)
		}
	}
	return out, nil
}

// rootExcludePatterns reads the root manifest's exclude-from-classmap. The
// manifest.Autoload struct may grow this field in a follow-up patch; until
// then we look for a typed accessor and fall back to nil.
func rootExcludePatterns(a manifest.Autoload) []string {
	// manifest.Autoload.ExcludeFromClassmap is added in a tiny follow-up to
	// Stage-1 Plan 1; until then, return nil so existing manifests keep
	// working unchanged.
	type hasExclude interface {
		excludeFromClassmap() []string
	}
	if h, ok := any(a).(hasExclude); ok {
		return h.excludeFromClassmap()
	}
	return nil
}

func scanInto(out map[string]string, projectDir, installPath, raw string, excl *excludeMatcher) error {
	relBase := raw
	if installPath != "" {
		relBase = filepath.ToSlash(filepath.Join(installPath, raw))
	}
	abs := filepath.Join(projectDir, filepath.FromSlash(relBase))

	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("classmap %q: %w", raw, err)
	}
	if !info.IsDir() {
		return scanFileInto(out, projectDir, abs, excl)
	}
	return filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if ext != ".php" && ext != ".inc" {
			return nil
		}
		return scanFileInto(out, projectDir, p, excl)
	})
}

func scanFileInto(out map[string]string, projectDir, abs string, excl *excludeMatcher) error {
	rel, err := filepath.Rel(projectDir, abs)
	if err != nil {
		return err
	}
	rel = filepath.ToSlash(rel)
	if excl.Match(rel) {
		return nil
	}
	src, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	classes, err := scanClasses(src)
	if err != nil {
		return fmt.Errorf("scan %s: %w", rel, err)
	}
	for _, c := range classes {
		if _, exists := out[c]; exists {
			continue // first-wins
		}
		out[c] = rel
	}
	return nil
}

// SortedClassmapKeys returns the keys of m sorted lexicographically. Used
// by templates so the emitted file is byte-stable.
func SortedClassmapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/autoload/... -run TestCollectClassmap -v`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/autoload/classmap.go internal/autoload/classmap_test.go internal/autoload/psr4.go
git commit -m "feat(autoload): collect classmap from package directories"
```

---

## Task 5: Manifest gains `exclude-from-classmap`

**Files:**
- Modify: `internal/manifest/autoload.go`
- Modify: `internal/manifest/manifest_test.go`
- Modify: `internal/autoload/classmap.go` (drop the interface fallback)

The root manifest may declare its own `exclude-from-classmap` (Symfony's skeleton does). We add the field to `manifest.Autoload` and tighten `rootExcludePatterns` to read it directly.

- [ ] **Step 1: Write the failing test**

Append to `internal/manifest/manifest_test.go`:

```go
func TestParseAutoloadExcludeFromClassmap(t *testing.T) {
	input := []byte(`{
		"name": "vendor/pkg",
		"autoload": {
			"classmap": ["src/"],
			"exclude-from-classmap": ["**/Tests/", "**/Fixtures/"]
		}
	}`)
	m, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"**/Tests/", "**/Fixtures/"}
	if !reflect.DeepEqual(m.Autoload.ExcludeFromClassmap, want) {
		t.Errorf("got %v, want %v", m.Autoload.ExcludeFromClassmap, want)
	}
}
```

(Add `import "reflect"` at the top of the file if not present.)

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/manifest/...`

Expected: build error on `m.Autoload.ExcludeFromClassmap`.

- [ ] **Step 3: Extend the struct**

Edit `internal/manifest/autoload.go`:

```go
type Autoload struct {
	PSR4                map[string]string `json:"psr-4,omitempty"`
	PSR0                map[string]string `json:"psr-0,omitempty"`
	Files               []string          `json:"files,omitempty"`
	Classmap            []string          `json:"classmap,omitempty"`
	ExcludeFromClassmap []string          `json:"exclude-from-classmap,omitempty"`
}
```

- [ ] **Step 4: Use the field directly**

In `internal/autoload/classmap.go`, replace the `rootExcludePatterns` helper:

```go
func rootExcludePatterns(a manifest.Autoload) []string {
	return a.ExcludeFromClassmap
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/manifest/... ./internal/autoload/...`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/manifest internal/autoload/classmap.go
git commit -m "feat(manifest): parse autoload.exclude-from-classmap"
```

---

## Task 6: Templates — `autoload_files.php`, `autoload_classmap.php`

**Files:**
- Modify: `internal/autoload/templates.go`
- Modify: `internal/autoload/templates_test.go`

We replace the empty `autoloadFilesTpl` and `autoloadClassmapTpl` with real renderers. Keys for the files array are MD5 of the package name + path (the same scheme Composer uses), so the bootstrap can dedupe across `composerRequire<HASH>` calls.

- [ ] **Step 1: Extend `renderData`**

In `internal/autoload/templates.go`, replace `renderData` with:

```go
type renderData struct {
	InitClass     string
	Hash          string
	PSR4          map[string][]string
	SortedPSR4    []string
	Files         []FileEntry          // ordered, deduped, ready to emit
	Classmap      map[string]string    // qualified-name -> project-relative path
	SortedClasses []string             // sorted keys of Classmap, supplied for determinism
}
```

- [ ] **Step 2: Write the new files template**

Replace `autoloadFilesTpl` with:

```go
const autoloadFilesTpl = `<?php

// autoload_files.php @generated by gomposer

$vendorDir = dirname(__DIR__);
$baseDir = dirname($vendorDir);

return array(
{{- range $f := .Files}}
    {{phpString (fileKey $f)}} => {{phpDir $f.Path}},
{{- end}}
);
`
```

Replace `autoloadClassmapTpl` with:

```go
const autoloadClassmapTpl = `<?php

// autoload_classmap.php @generated by gomposer

$vendorDir = dirname(__DIR__);
$baseDir = dirname($vendorDir);

return array(
{{- range $name := .SortedClasses}}
    {{phpString $name}} => {{phpDir (index $.Classmap $name)}},
{{- end}}
);
`
```

- [ ] **Step 3: Add the `fileKey` template function**

In `renderOne` (where the funcs are registered), append `"fileKey": fileKey,` and define:

```go
import "crypto/md5"
import "encoding/hex"

// fileKey is the deterministic hash key Composer uses in autoload_files
// arrays: md5(packageName . ":" . relativePath). For the root manifest
// (PackageName == "") Composer hashes md5(absolute baseDir + path); we
// stand in with md5("__root__:" + path) so two checkouts of the same
// project on different machines still produce the same key. The bootstrap
// only ever uses the keys for `isset($GLOBALS[...][...])` dedup so any
// stable string works.
func fileKey(f FileEntry) string {
	owner := f.PackageName
	if owner == "" {
		owner = "__root__"
	}
	sum := md5.Sum([]byte(owner + ":" + f.Path))
	return hex.EncodeToString(sum[:])
}
```

- [ ] **Step 4: Adjust `templates_test.go` sanity tests**

Replace the `TestRenderAllProducesAllSlots` test body (the existing slot list is fine, but `renderData` now has more fields that must be set):

```go
func TestRenderAllProducesAllSlots(t *testing.T) {
	d := renderData{
		InitClass:  "ComposerAutoloaderInit" + strings.Repeat("a", 32),
		Hash:       strings.Repeat("a", 32),
		PSR4:       map[string][]string{"App\\": {"src/"}},
		SortedPSR4: []string{"App\\"},
	}
	out, err := renderAll(d)
	if err != nil {
		t.Fatalf("renderAll: %v", err)
	}
	for _, p := range []string{
		"vendor/autoload.php",
		"vendor/composer/autoload_real.php",
		"vendor/composer/autoload_psr4.php",
		"vendor/composer/autoload_namespaces.php",
		"vendor/composer/autoload_classmap.php",
		"vendor/composer/autoload_files.php",
		"vendor/composer/autoload_static.php",
		"vendor/composer/installed.php",
		"vendor/composer/InstalledVersions.php",
	} {
		if _, ok := out[p]; !ok {
			t.Errorf("missing output: %s", p)
		}
	}
}
```

Add a focused test for the files key:

```go
func TestFileKeyIsStable(t *testing.T) {
	a := fileKey(FileEntry{Path: "vendor/x/y/z.php", PackageName: "x/y"})
	b := fileKey(FileEntry{Path: "vendor/x/y/z.php", PackageName: "x/y"})
	if a != b {
		t.Errorf("non-deterministic fileKey: %s vs %s", a, b)
	}
	if len(a) != 32 {
		t.Errorf("fileKey length = %d, want 32", len(a))
	}
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/autoload/...`

Expected: most tests PASS; snapshot tests will fail because the bytes have changed. Defer that fix to Task 9.

- [ ] **Step 6: Commit**

```bash
git add internal/autoload/templates.go internal/autoload/templates_test.go
git commit -m "feat(autoload): templates emit populated files and classmap arrays"
```

---

## Task 7: Templates — `autoload_real.php` requires the files

**Files:**
- Modify: `internal/autoload/templates.go`

Composer's bootstrap, post-2.0, uses `composerRequire<HASH>` helpers and a `$GLOBALS['__composer_autoload_files']` deduper. We mirror it exactly:

```php
$includeFiles = require __DIR__ . '/autoload_files.php';
foreach ($includeFiles as $fileIdentifier => $file) {
    composerRequire<HASH>($fileIdentifier, $file);
}
// ...
function composerRequire<HASH>($fileIdentifier, $file)
{
    if (empty($GLOBALS['__composer_autoload_files'][$fileIdentifier])) {
        $GLOBALS['__composer_autoload_files'][$fileIdentifier] = true;
        require $file;
    }
}
```

The require block is appended inside `getLoader` after `$loader->register(true);`. The function definition is at the bottom of `autoload_real.php`, OUTSIDE the class.

- [ ] **Step 1: Replace `autoloadRealTpl`**

Replace the existing `autoloadRealTpl` with:

```go
const autoloadRealTpl = `<?php

// autoload_real.php @generated by gomposer

class {{.InitClass}}
{
    private static $loader;

    public static function loadClassLoader($class)
    {
        if ('Composer\Autoload\ClassLoader' === $class) {
            require __DIR__ . '/ClassLoader.php';
        }
    }

    /**
     * @return \Composer\Autoload\ClassLoader
     */
    public static function getLoader()
    {
        if (null !== self::$loader) {
            return self::$loader;
        }

        spl_autoload_register(array('{{.InitClass}}', 'loadClassLoader'), true, true);
        self::$loader = $loader = new \Composer\Autoload\ClassLoader(\dirname(__DIR__));
        spl_autoload_unregister(array('{{.InitClass}}', 'loadClassLoader'));

        $useStaticLoader = PHP_VERSION_ID >= 50600 && !defined('HHVM_VERSION');
        if ($useStaticLoader) {
            require __DIR__ . '/autoload_static.php';
            call_user_func(\Composer\Autoload\ComposerStaticInit{{.Hash}}::getInitializer($loader));
        } else {
            $map = require __DIR__ . '/autoload_namespaces.php';
            foreach ($map as $namespace => $path) {
                $loader->set($namespace, $path);
            }

            $map = require __DIR__ . '/autoload_psr4.php';
            foreach ($map as $namespace => $path) {
                $loader->setPsr4($namespace, $path);
            }

            $classMap = require __DIR__ . '/autoload_classmap.php';
            if ($classMap) {
                $loader->addClassMap($classMap);
            }
        }

        $loader->register(true);

        $includeFiles = $useStaticLoader ? \Composer\Autoload\ComposerStaticInit{{.Hash}}::$files : require __DIR__ . '/autoload_files.php';
        foreach ($includeFiles as $fileIdentifier => $file) {
            composerRequire{{.Hash}}($fileIdentifier, $file);
        }

        return $loader;
    }
}

function composerRequire{{.Hash}}($fileIdentifier, $file)
{
    if (empty($GLOBALS['__composer_autoload_files'][$fileIdentifier])) {
        $GLOBALS['__composer_autoload_files'][$fileIdentifier] = true;

        require $file;
    }
}
`
```

- [ ] **Step 2: Extend `autoloadStaticTpl` to include `$files` and `$classMap`**

Replace `autoloadStaticTpl` with:

```go
const autoloadStaticTpl = `<?php

// autoload_static.php @generated by gomposer

namespace Composer\Autoload;

class ComposerStaticInit{{.Hash}}
{
    public static $files = array(
{{- range $f := .Files}}
        {{phpString (fileKey $f)}} => {{phpDir $f.Path}},
{{- end}}
    );

    public static $prefixLengthsPsr4 = array(
{{- range $prefix := .SortedPSR4}}
        {{phpString (firstChar $prefix)}} => array({{phpString $prefix}} => {{len $prefix}}),
{{- end}}
    );

    public static $prefixDirsPsr4 = array(
{{- range $prefix := .SortedPSR4}}
        {{phpString $prefix}} => array({{range $i, $dir := index $.PSR4 $prefix}}{{if $i}}, {{end}}{{phpDir $dir}}{{end}}),
{{- end}}
    );

    public static $classMap = array(
{{- range $name := .SortedClasses}}
        {{phpString $name}} => {{phpDir (index $.Classmap $name)}},
{{- end}}
    );

    public static function getInitializer(ClassLoader $loader)
    {
        return \Closure::bind(function () use ($loader) {
            $loader->prefixLengthsPsr4 = ComposerStaticInit{{.Hash}}::$prefixLengthsPsr4;
            $loader->prefixDirsPsr4 = ComposerStaticInit{{.Hash}}::$prefixDirsPsr4;
            $loader->classMap = ComposerStaticInit{{.Hash}}::$classMap;
        }, null, ClassLoader::class);
    }
}
`
```

- [ ] **Step 3: Build to confirm template parsing is OK**

Run: `go build ./internal/autoload/...`

Expected: clean build. Snapshot tests still red — fixed in Task 9.

- [ ] **Step 4: Commit**

```bash
git add internal/autoload/templates.go
git commit -m "feat(autoload): autoload_real and autoload_static include files and classmap"
```

---

## Task 8: Generator wires collection through to render

**Files:**
- Modify: `internal/autoload/generator.go`

`Generate` already calls `CollectPSR4`; we add `CollectFiles`, `CollectClassmap`, and PSR-0 warn-and-skip. Errors from classmap walking are surfaced to the caller — a missing classmap path is a hard error (matches Composer's behaviour: it dies loudly).

- [ ] **Step 1: Update `Generate`**

Replace the body of `Generate` in `internal/autoload/generator.go`:

```go
func Generate(opts Options) error {
	if !filepath.IsAbs(opts.ProjectDir) {
		return errors.New("autoload: ProjectDir must be absolute")
	}

	WarnPSR0(opts.RootAutoload, opts.Entries)

	psr4 := CollectPSR4(opts.ProjectDir, opts.RootAutoload, opts.Entries)
	files := CollectFiles(opts.RootAutoload, opts.Entries)
	classmap, err := CollectClassmap(opts.ProjectDir, opts.RootAutoload, opts.Entries)
	if err != nil {
		return err
	}

	data := renderData{
		InitClass:     InitClassName(opts.ProjectDir),
		Hash:          InitHash(opts.ProjectDir),
		PSR4:          psr4,
		SortedPSR4:    SortedPrefixes(psr4),
		Files:         files,
		Classmap:      classmap,
		SortedClasses: SortedClassmapKeys(classmap),
	}

	out, err := renderAll(data)
	if err != nil {
		return err
	}
	out["vendor/composer/ClassLoader.php"] = embedded.ClassLoaderPHP
	out["vendor/composer/LICENSE"] = embedded.LicenseText

	for rel, body := range out {
		abs := filepath.Join(opts.ProjectDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return fmt.Errorf("autoload: mkdir %s: %w", abs, err)
		}
		if err := writeAtomic(abs, body); err != nil {
			return fmt.Errorf("autoload: write %s: %w", abs, err)
		}
	}
	return nil
}
```

- [ ] **Step 2: Implement `WarnPSR0`**

Append to `internal/autoload/psr4.go`:

```go
import "log/slog"

// WarnPSR0 logs one warning per package (and one for the root manifest)
// that declares non-empty autoload.psr-0. Stage 2 explicitly does not
// implement PSR-0; calling code is already aware of this from the spec,
// but a runtime warning helps surface unsupported packages during real
// installs.
//
// The warnings are emitted via slog at level Warn so they are visible
// without --verbose. Tests can capture them via slog.SetDefault(...).
func WarnPSR0(root manifest.Autoload, entries []Entry) {
	if len(root.PSR0) > 0 {
		slog.Warn("autoload: PSR-0 not supported, skipping",
			"package", "<root>", "namespaces", keysOf(root.PSR0))
	}
	for _, e := range entries {
		if len(e.Autoload.PSR0) > 0 {
			slog.Warn("autoload: PSR-0 not supported, skipping",
				"package", e.Name, "namespaces", anyKeysOf(e.Autoload.PSR0))
		}
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func anyKeysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
```

- [ ] **Step 3: Run tests (snapshot still red, others should pass)**

Run: `go test ./internal/autoload/... -run 'Test(Collect|Scan|Exclude|FileKey|RenderAll|InitHash|InitClass|PhpString|PhpDir|GenerateRejectsRelativeProjectDir|GenerateWithNoEntries|GenerateWithNoAutoloadAtAll)'`

Expected: PASS for everything except the snapshot suite (which we update next).

- [ ] **Step 4: Commit**

```bash
git add internal/autoload/generator.go internal/autoload/psr4.go
git commit -m "feat(autoload): Generate threads files and classmap through render"
```

---

## Task 9: Refresh snapshot expected files for the new outputs

**Files:**
- Modify: `internal/autoload/testdata/expected/autoload_real.php`
- Modify: `internal/autoload/testdata/expected/autoload_files.php`
- Modify: `internal/autoload/testdata/expected/autoload_classmap.php`
- Modify: `internal/autoload/testdata/expected/autoload_static.php`
- Add: `internal/autoload/testdata/fixture-project/vendor/symfony/polyfill-mbstring/...` (Composer-shaped real-world fixture)
- Add: `internal/autoload/testdata/fixture-project/vendor/acme/legacy/...` (classmap source)
- Modify: `internal/autoload/generator_test.go`

The Stage-1 fixture only had two PSR-4 packages. We extend it with:

1. A `symfony/polyfill-mbstring`-shaped package contributing one file via `autoload.files`.
2. A synthetic `acme/legacy` package contributing classes via `autoload.classmap` (a directory walk), with one path excluded via `exclude-from-classmap`.

The two fixtures together exercise every path: real-world shape (1) and synthetic depth (2). Snapshots are regenerated using the same `WRITE_EXPECTED=1` mechanism Plan 5 introduced.

- [ ] **Step 1: Add the polyfill-mbstring fixture**

Create `internal/autoload/testdata/fixture-project/vendor/symfony/polyfill-mbstring/composer.json`:

```json
{
    "name": "symfony/polyfill-mbstring",
    "type": "library",
    "autoload": {
        "psr-4": { "Symfony\\Polyfill\\Mbstring\\": "" },
        "files": ["bootstrap.php"]
    }
}
```

Create `internal/autoload/testdata/fixture-project/vendor/symfony/polyfill-mbstring/bootstrap.php`:

```php
<?php

if (!function_exists('mb_strlen')) {
    function mb_strlen($s, $encoding = null) {
        return strlen($s);
    }
}
```

Create `internal/autoload/testdata/fixture-project/vendor/symfony/polyfill-mbstring/Mbstring.php`:

```php
<?php

namespace Symfony\Polyfill\Mbstring;

class Mbstring {}
```

- [ ] **Step 2: Add the classmap fixture**

Create `internal/autoload/testdata/fixture-project/vendor/acme/legacy/composer.json`:

```json
{
    "name": "acme/legacy",
    "type": "library",
    "autoload": {
        "classmap": ["src/"],
        "exclude-from-classmap": ["**/Tests/"]
    }
}
```

Create `internal/autoload/testdata/fixture-project/vendor/acme/legacy/src/Old.php`:

```php
<?php

namespace Acme\Legacy;

class Old
{
    public function speak(): string { return 'old'; }
}

interface Speaker {}

trait Loud {}
```

Create `internal/autoload/testdata/fixture-project/vendor/acme/legacy/src/sub/Sub.php`:

```php
<?php

namespace Acme\Legacy\Sub;

enum Color { case Red; case Green; }
```

Create `internal/autoload/testdata/fixture-project/vendor/acme/legacy/src/Tests/HiddenTest.php`:

```php
<?php

namespace Acme\Legacy\Tests;

class HiddenTest {}
```

(The test file should NOT appear in the classmap.)

- [ ] **Step 3: Update `fixtureEntries()` in `generator_test.go`**

Replace the `fixtureEntries` helper:

```go
func fixtureEntries() []Entry {
	return []Entry{
		{
			Name:        "acme/foo",
			Version:     "1.0.0",
			InstallPath: "vendor/acme/foo",
			Autoload: registry.Autoload{
				PSR4: map[string]any{"Acme\\Foo\\": "src/"},
			},
		},
		{
			Name:        "acme/bar",
			Version:     "1.0.0",
			InstallPath: "vendor/acme/bar",
			Autoload: registry.Autoload{
				PSR4: map[string]any{"Acme\\Bar\\": "src/"},
			},
		},
		{
			Name:        "acme/legacy",
			Version:     "1.0.0",
			InstallPath: "vendor/acme/legacy",
			Autoload: registry.Autoload{
				Classmap: []string{"src/"},
			},
			ExcludeFromClassmap: []string{"**/Tests/"},
		},
		{
			Name:        "symfony/polyfill-mbstring",
			Version:     "1.30.0",
			InstallPath: "vendor/symfony/polyfill-mbstring",
			Autoload: registry.Autoload{
				PSR4:  map[string]any{"Symfony\\Polyfill\\Mbstring\\": ""},
				Files: []string{"bootstrap.php"},
			},
		},
	}
}
```

- [ ] **Step 4: Snapshot regenerate**

Re-add the same one-shot helper Plan 5 used:

```go
func TestWriteExpected(t *testing.T) {
	if os.Getenv("WRITE_EXPECTED") != "1" {
		t.Skip("set WRITE_EXPECTED=1 to regenerate")
	}
	dir := filepath.Join("testdata", "fixture-project")
	abs, _ := filepath.Abs(dir)
	if err := Generate(Options{
		ProjectDir:   abs,
		Entries:      fixtureEntries(),
		RootAutoload: fixtureRoot(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"autoload.php",
		"autoload_real.php",
		"autoload_psr4.php",
		"autoload_namespaces.php",
		"autoload_classmap.php",
		"autoload_files.php",
		"autoload_static.php",
		"installed.php",
	} {
		var src string
		if name == "autoload.php" {
			src = filepath.Join(abs, "vendor", name)
		} else {
			src = filepath.Join(abs, "vendor", "composer", name)
		}
		body, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join("testdata", "expected", name)
		if err := os.WriteFile(dest, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
```

Run: `WRITE_EXPECTED=1 go test ./internal/autoload/... -run TestWriteExpected`

**Manual inspection:**
- `autoload_files.php` contains an entry with `vendor/symfony/polyfill-mbstring/bootstrap.php`.
- `autoload_classmap.php` contains `Acme\\Legacy\\Old`, `Acme\\Legacy\\Speaker`, `Acme\\Legacy\\Loud`, `Acme\\Legacy\\Sub\\Color`. It does NOT contain `Acme\\Legacy\\Tests\\HiddenTest`.
- `autoload_real.php` contains the `composerRequire<HASH>` block AND the foreach that iterates `$includeFiles`.
- `autoload_static.php` contains a non-empty `$files` array and a `$classMap` populated to match.

If anything looks wrong, fix the templates in Tasks 6/7 and re-run.

- [ ] **Step 5: Delete `TestWriteExpected`, run the snapshot suite**

Run: `go test ./internal/autoload/... -run TestSnapshot`

Expected: PASS.

- [ ] **Step 6: Clean up `vendor/composer/` from the source-controlled fixture**

The `WRITE_EXPECTED` run wrote real files into the fixture's vendor/composer/. Move them out of the fixture (we only want the polyfill + legacy fixture sources to be tracked):

```bash
rm -rf internal/autoload/testdata/fixture-project/vendor/composer/
rm -f internal/autoload/testdata/fixture-project/vendor/autoload.php
```

- [ ] **Step 7: Commit**

```bash
git add internal/autoload/testdata internal/autoload/generator_test.go
git commit -m "test(autoload): snapshot files+classmap with real-shape and synthetic fixtures"
```

---

## Task 10: Idempotency holds across files+classmap

**Files:**
- Modify: `internal/autoload/generator_test.go`

The Stage-1 idempotency test only checked `autoload_psr4.php`. With non-trivial `Files` and `Classmap` we need to assert the same property on the new outputs (Go maps iterate non-deterministically — if we accidentally reintroduce that, Stage 3 benchmarks will spit garbage diffs).

- [ ] **Step 1: Extend `TestGenerateIsIdempotent`**

Replace the existing body with:

```go
func TestGenerateIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		ProjectDir:   dir,
		Entries:      fixtureEntries(),
		RootAutoload: fixtureRoot(),
	}
	// Materialize fixture sources into dir so classmap walking finds them.
	src := filepath.Join("testdata", "fixture-project")
	if err := copyDir(src, dir); err != nil {
		t.Fatalf("copyDir: %v", err)
	}

	if err := Generate(opts); err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	first := readGenerated(t, dir)

	if err := Generate(opts); err != nil {
		t.Fatalf("second Generate: %v", err)
	}
	second := readGenerated(t, dir)

	for path, a := range first {
		if !bytes.Equal(a, second[path]) {
			t.Errorf("%s changed across regenerations", path)
		}
	}
}

func readGenerated(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, p := range []string{
		"vendor/autoload.php",
		"vendor/composer/autoload_real.php",
		"vendor/composer/autoload_psr4.php",
		"vendor/composer/autoload_classmap.php",
		"vendor/composer/autoload_files.php",
		"vendor/composer/autoload_static.php",
	} {
		body, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		out[p] = body
	}
	return out
}
```

- [ ] **Step 2: Run**

Run: `go test ./internal/autoload/... -run TestGenerateIsIdempotent`

Expected: PASS (re-run a few times locally to chase out hidden non-determinism).

- [ ] **Step 3: Commit**

```bash
git add internal/autoload/generator_test.go
git commit -m "test(autoload): idempotency covers files and classmap outputs"
```

---

## Task 11: End-to-end PHP test — files autoloader fires, classmap resolves

**Files:**
- Modify: `internal/autoload/generator_test.go`

The Stage-1 e2e test only checked PSR-4 class resolution. We extend it with:

1. A `function_exists('mb_strlen')` check post-`require 'vendor/autoload.php'` — proves the polyfill's `bootstrap.php` ran.
2. Class resolution for `Acme\Legacy\Old` — proves the classmap is wired into the loader.
3. `class_exists('Acme\Legacy\Tests\HiddenTest')` returns `false` — proves the exclude pattern worked.

- [ ] **Step 1: Extend the e2e test**

In `TestEndToEndPHPClassResolution`, replace the test cases with:

```go
cases := []struct {
	expr string
	want string
}{
	{`class_exists('App\\Foo') ? '1' : ''`, "1"},
	{`class_exists('Acme\\Foo\\Foo') ? '1' : ''`, "1"},
	{`class_exists('Acme\\Bar\\Bar') ? '1' : ''`, "1"},
	{`class_exists('Symfony\\Polyfill\\Mbstring\\Mbstring') ? '1' : ''`, "1"},
	{`class_exists('Acme\\Legacy\\Old') ? '1' : ''`, "1"},
	{`class_exists('Acme\\Legacy\\Tests\\HiddenTest') ? '1' : ''`, ""},
	{`function_exists('mb_strlen') ? '1' : ''`, "1"},
}
for _, tc := range cases {
	t.Run(tc.expr, func(t *testing.T) {
		script := "require 'vendor/autoload.php'; echo (" + tc.expr + ");"
		cmd := exec.Command("php", "-r", script)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("php failed: %v\noutput:\n%s", err, out)
		}
		if string(out) != tc.want {
			t.Errorf("(%s) = %q, want %q", tc.expr, string(out), tc.want)
		}
	})
}
```

(The Generate call inside the test already references `fixtureEntries()` from Task 9, so it picks up the new packages automatically.)

- [ ] **Step 2: Run with PHP available**

Run: `go test ./internal/autoload/... -run TestEndToEnd -v`

Expected: every subtest PASSes when `php` is on PATH.

- [ ] **Step 3: Run without PHP (skip path)**

Run: `PATH=/tmp/empty go test ./internal/autoload/... -run TestEndToEnd -v`

Expected: clean SKIP.

- [ ] **Step 4: Commit**

```bash
git add internal/autoload/generator_test.go
git commit -m "test(autoload): e2e covers files autoloader and classmap resolution"
```

---

## Task 12: Lock-package round-trip carries `files`, `classmap`, `exclude-from-classmap`

**Files:**
- Modify: `internal/orchestrator/pipeline.go` (`registryAutoloadFromMap`)
- Modify: `internal/orchestrator/orchestrator_test.go`

The orchestrator's `registryAutoloadFromMap` currently only extracts `psr-4` and `psr-0` from `lock.Package.Autoload`. With `files` and `classmap` in scope, we extend it to extract all relevant fields and to thread `exclude-from-classmap` (which lives at the top level of `autoload`, sibling to `psr-4`) through to `Entry.ExcludeFromClassmap`.

- [ ] **Step 1: Write the failing test**

Append to `internal/orchestrator/orchestrator_test.go`:

```go
func TestRegistryAutoloadFromMapExtractsFilesClassmap(t *testing.T) {
	in := map[string]any{
		"psr-4":                 map[string]any{"Acme\\": "src/"},
		"files":                 []any{"bootstrap.php", "helpers.php"},
		"classmap":              []any{"legacy/"},
		"exclude-from-classmap": []any{"**/Tests/"},
	}
	al, excl := autoloadFromLockMap(in)
	if al.PSR4["Acme\\"] != "src/" {
		t.Errorf("psr-4 lost")
	}
	if !reflect.DeepEqual(al.Files, []string{"bootstrap.php", "helpers.php"}) {
		t.Errorf("files = %v", al.Files)
	}
	if !reflect.DeepEqual(al.Classmap, []string{"legacy/"}) {
		t.Errorf("classmap = %v", al.Classmap)
	}
	if !reflect.DeepEqual(excl, []string{"**/Tests/"}) {
		t.Errorf("exclude = %v", excl)
	}
}
```

(Add `"reflect"` import.)

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/orchestrator/...`

Expected: build error referencing `autoloadFromLockMap`.

- [ ] **Step 3: Refactor `registryAutoloadFromMap`**

Rename to `autoloadFromLockMap` and broaden it. Replace the function in `pipeline.go`:

```go
// autoloadFromLockMap converts the lock package's Autoload map (a
// JSON-decoded map[string]any) into a registry.Autoload struct and the
// per-package exclude-from-classmap glob list. The split return is so the
// orchestrator can attach exclude patterns to autoload.Entry, where they
// live (registry.Autoload itself is shared with the resolver, which has
// no business with autoloader exclusion rules).
func autoloadFromLockMap(raw map[string]any) (registry.Autoload, []string) {
	var al registry.Autoload
	if raw == nil {
		return al, nil
	}
	if v, ok := raw["psr-4"]; ok {
		if m, ok := v.(map[string]any); ok {
			al.PSR4 = m
		}
	}
	if v, ok := raw["psr-0"]; ok {
		if m, ok := v.(map[string]any); ok {
			al.PSR0 = m
		}
	}
	if v, ok := raw["files"]; ok {
		al.Files = anySliceToStrings(v)
	}
	if v, ok := raw["classmap"]; ok {
		al.Classmap = anySliceToStrings(v)
	}
	var excl []string
	if v, ok := raw["exclude-from-classmap"]; ok {
		excl = anySliceToStrings(v)
	}
	return al, excl
}

func anySliceToStrings(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	}
	return nil
}
```

Update the caller in `autoloaderAdapter.Generate`:

```go
for _, p := range pkgs {
	installPath := filepath.ToSlash(filepath.Join("vendor", filepath.FromSlash(p.Name)))
	al, excl := autoloadFromLockMap(p.Autoload)
	entries = append(entries, autoloadpkg.Entry{
		Name:                p.Name,
		Version:             p.Version,
		InstallPath:         installPath,
		Autoload:            al,
		ExcludeFromClassmap: excl,
	})
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/orchestrator/... ./internal/autoload/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/orchestrator/pipeline.go internal/orchestrator/orchestrator_test.go
git commit -m "feat(orchestrator): plumb files, classmap, exclude into autoloader entries"
```

---

## Task 13: Resolver/pipeline fills `lock.Package.Autoload` with the new fields

**Files:**
- Modify: `internal/registry/packagist/decode.go` (or wherever `registry.PackageVersion` is constructed from JSON — check the codebase)
- Modify: `internal/orchestrator/pipeline.go` (the lock-construction code, where `lock.Package.Autoload` is populated)
- Modify: tests for either layer that currently round-trip a manifest through the pipeline

`registry.Autoload.Files` and `.Classmap` are already on the struct (Stage-1 Plan 1). We just need to verify the JSON decode at the metadata layer captures them, AND that the lock-construction step in the orchestrator copies the full autoload map (not just `psr-4`) into `lock.Package.Autoload`.

- [ ] **Step 1: Audit the metadata decoder**

Run: `grep -n "psr-4\|psr-0\|files\|classmap" internal/registry/packagist/*.go`

If the JSON struct tags only cover `psr-4` / `psr-0`, extend the decoder struct to include `files` and `classmap`. The tests in `internal/registry/packagist/` should already cover round-trip; add a fixture variant if not.

- [ ] **Step 2: Audit lock-construction**

Run: `grep -n "Autoload" internal/orchestrator/pipeline.go internal/lock/*.go`

`lock.Package.Autoload` is `map[string]any`, so it can carry anything. The construction site likely does something like `pkg.Autoload = map[string]any{"psr-4": ..., "psr-0": ...}`. Extend it to include `files`, `classmap`, and `exclude-from-classmap`. If the construction reads from `registry.Autoload` and the latter does not yet carry exclude-from-classmap, add a sibling `ExcludeFromClassmap []string` field on `registry.Autoload` first.

- [ ] **Step 3: Add an integration test**

In `internal/orchestrator/orchestrator_test.go`, add a test that resolves a fake source containing a package with all four autoload sections, runs through `Plan` (or whatever the resolution-then-lock function is named), and asserts the resulting `lock.Package.Autoload` map carries `files`, `classmap`, and `exclude-from-classmap`.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/registry/... ./internal/orchestrator/... ./internal/autoload/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(orchestrator): persist files, classmap, exclude into lock.Package.Autoload"
```

---

## Task 14: Doc update — Stage 2 autoloader status

**Files:**
- Modify: `docs/superpowers/specs/2026-05-07-gomposer-design.md` (Stage 2 components section)

The spec currently says, under Stage 2:

> - `files` autoloader output.
> - `classmap` autoloader: token-stream PHP scanner (not regex) over declared paths, emit static map.

Both are now implemented. Add a one-line "Status: implemented (2026-05-08, Stage 2 / Plan 1)" note next to the bullets, with an inline link to this plan file.

- [ ] **Step 1: Edit the spec**

Replace those two bullets with:

```
- `files` autoloader output. **Status:** implemented (Stage 2 / Plan 1).
- `classmap` autoloader: token-stream PHP scanner (not regex) over declared
  paths, emit static map. **Status:** implemented (Stage 2 / Plan 1).
```

- [ ] **Step 2: Commit**

```bash
git add docs/superpowers/specs/2026-05-07-gomposer-design.md
git commit -m "docs(spec): mark files and classmap autoloaders implemented"
```

---

## Plan 1 (Stage 2) acceptance check

After all tasks:

- `go test ./...` is green on macOS and Linux. `TestEndToEnd*` PASSes when `php` is on PATH and SKIPs cleanly when it isn't.
- `internal/autoload/testdata/expected/autoload_files.php` is non-empty and contains the polyfill-mbstring fixture entry, keyed by the deterministic md5 hash.
- `internal/autoload/testdata/expected/autoload_classmap.php` lists `Acme\Legacy\Old`, `Acme\Legacy\Speaker`, `Acme\Legacy\Loud`, and `Acme\Legacy\Sub\Color`. It does NOT list `Acme\Legacy\Tests\HiddenTest`.
- `internal/autoload/testdata/expected/autoload_real.php` defines `composerRequire<HASH>` and iterates `$includeFiles` after `$loader->register(true)`.
- `internal/autoload/testdata/expected/autoload_static.php` includes both `$files` and `$classMap` populated from the same data.
- Anonymous classes (`new class { ... }`) are excluded from the classmap (covered by `TestScanAnonymousClassIsExcluded`).
- The `class` keyword inside strings, comments, heredocs, and `Foo::class` constants does NOT register as a declaration (covered by `TestScanIgnoresClassKeyword*`).
- Conditional classes inside `if (false) { ... }` ARE indexed (covered by `TestScanConditionalClass`) — matches Composer's authoritative-classmap behaviour.
- `exclude-from-classmap` patterns with `**/`, `*`, and trailing-slash-as-directory all match correctly (covered by `TestExclude*`).
- `Generate(opts)` is byte-deterministic across repeated calls with files+classmap fixtures (covered by `TestGenerateIsIdempotent`).
- `internal/orchestrator` packs `files`, `classmap`, and `exclude-from-classmap` into `lock.Package.Autoload` and unpacks them back into `autoload.Entry` (covered by `TestRegistryAutoloadFromMapExtractsFilesClassmap`).
- PSR-0 packages emit a single `slog.Warn` per occurrence and otherwise behave identically to the Stage-1 autoloader.
- The public surface — `autoload.Generate`, `autoload.Options`, `autoload.Entry` (now with `ExcludeFromClassmap`) — is stable for any future Stage-2 plan that consumes it.

If any of these fails, fix forward in a follow-up commit before declaring Stage 2 / Plan 1 done. Specifically:

1. If a snapshot test fails because of trivial whitespace drift after a template tweak, diff `testdata/expected/<file>` against `<file>.actual` (the snapshot test writes the actual bytes alongside on mismatch). The bytes are the contract; do not casually re-snapshot — verify the difference is intended first.
2. If the e2e test fails on `Symfony\Polyfill\Mbstring\Mbstring`, the most likely cause is that the polyfill's `bootstrap.php` errored before the class loader picked up the PSR-4 entry. Check the order of `require` calls in `autoload_real.php`: PSR-4 setup must precede the `composerRequire<HASH>` loop.
3. If `TestScanIgnoresClassKeywordInHeredoc` fails after a scanner refactor, the most common cause is mishandling of flexible heredocs (PHP 7.3+). The scanner only needs to recognise the closing label at the start of a line, possibly indented; it does NOT need to honour the full PHP 7.3 closing-label-indent semantics — overly aggressive matching is fine because heredocs cannot contain real `class` declarations regardless.
4. If the orchestrator integration test fails with "no autoload data" after a metadata refactor, check that `internal/registry/packagist/` continues to decode `files`, `classmap`, and `exclude-from-classmap` from the upstream JSON.
