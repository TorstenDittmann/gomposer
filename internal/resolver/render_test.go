package resolver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/torstendittmann/gomposer/internal/constraint"
)

// loadFixture reads testdata/expected/<name>.txt and trims one trailing newline
// so editors that auto-add a final newline don't cause spurious diffs.
func loadFixture(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join("testdata", "expected", name+".txt")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read fixture %s: %v", p, err)
	}
	return strings.TrimRight(string(data), "\n")
}

// posTerm is a small helper for building positive terms in tests.
func posTerm(t *testing.T, pkg, raw string) Term {
	t.Helper()
	c, err := constraint.Parse(raw)
	if err != nil {
		t.Fatalf("constraint.Parse(%q): %v", raw, err)
	}
	return Term{Package: pkg, Constraint: c, Positive: true}
}

func negTerm(t *testing.T, pkg, raw string) Term {
	tt := posTerm(t, pkg, raw)
	tt.Positive = false
	return tt
}

// rootTerm is the synthetic $root term used by the solver for direct requires.
func rootTerm(t *testing.T) Term {
	return posTerm(t, "$root", "*")
}

func TestRenderTwoPackageConflict(t *testing.T) {
	// Scenario:
	//   root requires A ^1.0
	//   A 1.0.0 depends on B ^1.0
	//   root requires B ^2.0
	rootA := NewIncompatibility(
		[]Term{rootTerm(t), negTerm(t, "a/x", "^1.0")},
		CauseRoot{},
	)
	rootB := NewIncompatibility(
		[]Term{rootTerm(t), negTerm(t, "b/y", "^2.0")},
		CauseRoot{},
	)
	aDepB := NewIncompatibility(
		[]Term{posTerm(t, "a/x", "1.0.0"), negTerm(t, "b/y", "^1.0")},
		CauseDependency{Depender: "a/x", Dependee: "b/y"},
	)
	// Conflict: A requires B^1, root requires B^2 → root chooses A which forces
	// B^1, contradicting root's B^2 require.
	step1 := NewIncompatibility(
		[]Term{rootTerm(t), negTerm(t, "b/y", "^1.0")},
		CauseConflict{Conflict: aDepB, Other: rootA},
	)
	failure := NewIncompatibility(
		[]Term{},
		CauseConflict{Conflict: step1, Other: rootB},
	)
	got := RenderConflict(failure)
	want := loadFixture(t, "two_package_conflict")
	if got != want {
		t.Errorf("two-package conflict rendering mismatch.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderThreePackageChain(t *testing.T) {
	// root requires A ^1.0
	// A 1.0.0 → B ^1.0
	// B 1.0.0 → C ^1.0
	// root requires C ^2.0
	rootA := NewIncompatibility(
		[]Term{rootTerm(t), negTerm(t, "a/x", "^1.0")}, CauseRoot{},
	)
	rootC := NewIncompatibility(
		[]Term{rootTerm(t), negTerm(t, "c/z", "^2.0")}, CauseRoot{},
	)
	aDepB := NewIncompatibility(
		[]Term{posTerm(t, "a/x", "1.0.0"), negTerm(t, "b/y", "^1.0")},
		CauseDependency{Depender: "a/x", Dependee: "b/y"},
	)
	bDepC := NewIncompatibility(
		[]Term{posTerm(t, "b/y", "1.0.0"), negTerm(t, "c/z", "^1.0")},
		CauseDependency{Depender: "b/y", Dependee: "c/z"},
	)
	abDepC := NewIncompatibility(
		[]Term{posTerm(t, "a/x", "1.0.0"), negTerm(t, "c/z", "^1.0")},
		CauseConflict{Conflict: aDepB, Other: bDepC},
	)
	step := NewIncompatibility(
		[]Term{rootTerm(t), negTerm(t, "c/z", "^1.0")},
		CauseConflict{Conflict: abDepC, Other: rootA},
	)
	failure := NewIncompatibility(
		[]Term{},
		CauseConflict{Conflict: step, Other: rootC},
	)
	got := RenderConflict(failure)
	want := loadFixture(t, "three_package_chain")
	if got != want {
		t.Errorf("three-chain rendering mismatch.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderUnknownPackageLeaf(t *testing.T) {
	// root requires acme/missing ^1.0; the registry doesn't know about it.
	rootReq := NewIncompatibility(
		[]Term{rootTerm(t), negTerm(t, "acme/missing", "^1.0")}, CauseRoot{},
	)
	unknown := NewIncompatibility(
		[]Term{posTerm(t, "acme/missing", "*")},
		CauseUnknownPackage{Package: "acme/missing"},
	)
	failure := NewIncompatibility(
		[]Term{},
		CauseConflict{Conflict: unknown, Other: rootReq},
	)
	got := RenderConflict(failure)
	want := loadFixture(t, "unknown_package")
	if got != want {
		t.Errorf("unknown-package rendering mismatch.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderNoVersionsLeaf(t *testing.T) {
	// root requires acme/strict ^99.0; package exists but no version satisfies.
	rootReq := NewIncompatibility(
		[]Term{rootTerm(t), negTerm(t, "acme/strict", "^99.0")}, CauseRoot{},
	)
	noVer := NewIncompatibility(
		[]Term{posTerm(t, "acme/strict", "^99.0")},
		CauseNoVersions{Package: "acme/strict"},
	)
	failure := NewIncompatibility(
		[]Term{},
		CauseConflict{Conflict: noVer, Other: rootReq},
	)
	got := RenderConflict(failure)
	want := loadFixture(t, "no_versions")
	if got != want {
		t.Errorf("no-versions rendering mismatch.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderDiamondDeduplication(t *testing.T) {
	// Diamond: A depends on D^1, B depends on D^1, root requires D^2.
	// The same `rootD` incompatibility appears twice in the conflict tree.
	// We expect the second occurrence to render as "and (1)" referencing the
	// first by its label.
	rootA := NewIncompatibility(
		[]Term{rootTerm(t), negTerm(t, "a/x", "^1.0")}, CauseRoot{},
	)
	rootB := NewIncompatibility(
		[]Term{rootTerm(t), negTerm(t, "b/y", "^1.0")}, CauseRoot{},
	)
	rootD := NewIncompatibility(
		[]Term{rootTerm(t), negTerm(t, "d/w", "^2.0")}, CauseRoot{},
	)
	aDepD := NewIncompatibility(
		[]Term{posTerm(t, "a/x", "1.0.0"), negTerm(t, "d/w", "^1.0")},
		CauseDependency{Depender: "a/x", Dependee: "d/w"},
	)
	bDepD := NewIncompatibility(
		[]Term{posTerm(t, "b/y", "1.0.0"), negTerm(t, "d/w", "^1.0")},
		CauseDependency{Depender: "b/y", Dependee: "d/w"},
	)
	aClash := NewIncompatibility(
		[]Term{rootTerm(t)},
		CauseConflict{Conflict: aDepD, Other: rootD},
	)
	bClash := NewIncompatibility(
		[]Term{rootTerm(t)},
		CauseConflict{Conflict: bDepD, Other: rootD},
	)
	step := NewIncompatibility(
		[]Term{rootTerm(t)},
		CauseConflict{Conflict: aClash, Other: rootA},
	)
	failure := NewIncompatibility(
		[]Term{},
		CauseConflict{Conflict: step, Other: NewIncompatibility(
			[]Term{rootTerm(t)},
			CauseConflict{Conflict: bClash, Other: rootB},
		)},
	)
	got := RenderConflict(failure)
	want := loadFixture(t, "diamond")
	if got != want {
		t.Errorf("diamond rendering mismatch.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderConflictNilSafe(t *testing.T) {
	if got := RenderConflict(nil); got != "resolver: no solution exists" {
		t.Errorf("nil root: got %q", got)
	}
}

// TestConflictErrorErrorPrefixesHeadline asserts that ConflictError.Error()
// keeps the one-line summary on the first line so existing log greps still
// fire. The full derivation follows on subsequent lines.
func TestConflictErrorErrorPrefixesHeadline(t *testing.T) {
	rootReq := NewIncompatibility(
		[]Term{rootTerm(t), negTerm(t, "acme/missing", "^1.0")}, CauseRoot{},
	)
	unknown := NewIncompatibility(
		[]Term{posTerm(t, "acme/missing", "*")},
		CauseUnknownPackage{Package: "acme/missing"},
	)
	failure := NewIncompatibility(
		[]Term{},
		CauseConflict{Conflict: unknown, Other: rootReq},
	)
	err := &ConflictError{Root: failure}
	out := err.Error()
	first, _, _ := strings.Cut(out, "\n")
	if !strings.HasPrefix(first, "resolver: conflict") {
		t.Errorf("first line should start with the headline; got: %q", first)
	}
	if !strings.Contains(out, "acme/missing") {
		t.Errorf("derivation must mention the unknown package: %q", out)
	}
}

func TestWriteWrappedRespectsColumnLimit(t *testing.T) {
	cases := []struct {
		name   string
		indent string
		text   string
	}{
		{"short fits on one line", "  ", "Because A and B, X."},
		{"long wraps at whitespace", "  ", strings.Repeat("alpha beta gamma ", 20)},
		{"single oversize token overflows", "  ",
			"prefix " + strings.Repeat("x", 200) + " suffix"},
		{"deep indent shrinks usable width", strings.Repeat("  ", 10),
			"Because foo and bar, baz."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			writeWrapped(&b, tc.indent, tc.text)
			for i, line := range strings.Split(strings.TrimRight(b.String(), "\n"), "\n") {
				if !strings.HasPrefix(line, tc.indent) {
					t.Errorf("line %d missing indent: %q", i, line)
				}
				// Lines may exceed the limit only when they contain a single
				// token longer than the available width. Verify by checking
				// that any over-limit line has at most one space outside the
				// indent (i.e., a single token that overflows).
				body := strings.TrimPrefix(line, tc.indent)
				if len(line) > maxRenderColumns && strings.Count(body, " ") > 0 {
					t.Errorf("line %d over %d cols and has wrappable space: %q",
						i, maxRenderColumns, line)
				}
			}
		})
	}
}

func TestWriteWrappedNormalizesWhitespace(t *testing.T) {
	var b strings.Builder
	writeWrapped(&b, "  ", "tab\there\n\nand   a   double space")
	out := b.String()
	if strings.Contains(out, "\t") {
		t.Errorf("tab survived: %q", out)
	}
	if strings.Contains(strings.TrimSpace(out), "  ") {
		// after normalization, no run of >1 space should remain inside line bodies
		t.Errorf("double-space survived: %q", out)
	}
}
