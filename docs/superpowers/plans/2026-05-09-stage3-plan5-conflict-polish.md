# Stage 3 / Plan 5: Resolver Conflict-Message Polish

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the bullet-list `ConflictError.Error()` rendering with a structured PubGrub derivation report — the human-readable "Because A and B, X. So Y." chain that Cargo, uv, and Dart pub all settle on. The new renderer walks the conflict tree depth-first, generates one prose line per `Incompatibility` keyed off its `Cause`, deduplicates repeated derivations, indents nested causes, and wraps long lines at 100 columns. The existing `collectLeafCauses` summary stays as a one-line headline so users see the bottom line before the full derivation.

**Why this is necessary.** The current `ConflictError.Error()` lists leaf causes ("dependency clash: a requires b...", "no published versions of c...") with no relationship between them. Users see _what_ leaves contradict but not _how_ they reached the contradiction; on a 3+ package transitive chain the output is actively misleading because the depender / dependee labelling collapses two different reasons into the same string. The PubGrub derivation format ("Because A 1.0.0 requires B ^1.0 and B 1.5.0 requires C ^2.0, A 1.0.0 requires C ^2.0. So, because root requires A 1.0.0 and C 1.0.0, version solving failed.") is the de-facto standard now and is what users have learned to expect from package managers. Adopting it brings gomposer up to par.

**Architecture:**

- A new file `internal/resolver/render.go` holds the renderer. `error.go` keeps `ConflictError`, `collectLeafCauses`, `termConstraintLabel`, and `ErrNoVersionsForPackage` but loses `renderDerivation` (replaced) and rewires `ConflictError.Error()` to call into `render.go`.
- The renderer is a depth-first walk over the conflict tree rooted at `ConflictError.Root`. Each `*Incompatibility` produces one prose sentence selected by a type-switch on `Cause`. Conflict-derived incompatibilities (`CauseConflict`) recurse into their two parents and stitch the children together with "Because <a> and <b>, <result>."; leaves render as standalone sentences.
- A `lineBuffer` helper handles indentation (two spaces per nesting level) and 100-column wrapping. Wrapping is whitespace-only — no hyphenation, no breaking inside `package@version` tokens — so output stays grep-able.
- Deduplication: a `map[*Incompatibility]int` records which incompatibilities have already been rendered and assigns each a numeric label (`(1)`, `(2)`, …). A duplicate render emits "and (2)" instead of repeating the full derivation. This mirrors Dart pub's behavior on diamond conflicts and keeps output bounded even when the conflict tree has heavy reuse.
- The terminal headline (the one-line summary) keeps the existing `collectLeafCauses` summary, prefixed with `resolver: conflict —`. The full derivation follows under a `derivation:` heading.

**Tech Stack:** Go 1.22+, standard library only (`strings`, `fmt`, `unicode/utf8`). No new dependencies.

**Depends on:** Stage 1 Plan 3 — `internal/resolver/{term,incompatibility,error,solve}.go` and the `Cause*` types.

---

## File structure

| Path | Responsibility |
|------|---------------|
| `internal/resolver/render.go` | Structured PubGrub derivation renderer; `RenderConflict(root *Incompatibility) string` |
| `internal/resolver/render_test.go` | Unit tests over handcrafted incompatibility trees |
| `internal/resolver/error.go` | `ConflictError.Error()` calls `RenderConflict`; keep `collectLeafCauses` as the headline source |
| `internal/resolver/testdata/expected/two_package_conflict.txt` | Fixture: A requires B^1, root requires B^2 |
| `internal/resolver/testdata/expected/three_package_chain.txt` | Fixture: A → B → C with C version mismatch |
| `internal/resolver/testdata/expected/unknown_package.txt` | Fixture: leaf with `CauseUnknownPackage` |
| `internal/resolver/testdata/expected/no_versions.txt` | Fixture: leaf with `CauseNoVersions` |
| `internal/resolver/testdata/expected/diamond.txt` | Fixture: derivation reused twice; verifies `(1)` / "and (1)" deduplication |

---

## Task 1: Add fixtures and a failing snapshot test

The renderer is best specified by the output it produces. We start by writing the test harness and the expected-output fixtures, watch them fail, then build `render.go` against them. The fixtures double as documentation: anyone reading the package can open `testdata/expected/two_package_conflict.txt` and see exactly what the user sees.

**Files:**
- Create: `internal/resolver/render_test.go`
- Create: `internal/resolver/testdata/expected/two_package_conflict.txt`
- Create: `internal/resolver/testdata/expected/three_package_chain.txt`
- Create: `internal/resolver/testdata/expected/unknown_package.txt`
- Create: `internal/resolver/testdata/expected/no_versions.txt`
- Create: `internal/resolver/testdata/expected/diamond.txt`

- [ ] **Step 1: Write the test harness**

Create `internal/resolver/render_test.go`:

```go
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
```

- [ ] **Step 2: Author the expected-output fixtures**

These are the lines the renderer must produce. Indentation is two spaces per nesting level. Lines are wrapped at 100 columns.

Create `internal/resolver/testdata/expected/two_package_conflict.txt`:

```
resolver: conflict — no compatible set of versions found:
  - dependency clash: a/x requires b/y but the chosen version cannot be reconciled

derivation:
  Because a/x 1.0.0 requires b/y ^1.0 and your manifest requires a/x ^1.0,
  every solution requires b/y ^1.0.
  So, because your manifest requires b/y ^2.0, version solving failed.
```

Create `internal/resolver/testdata/expected/three_package_chain.txt`:

```
resolver: conflict — no compatible set of versions found:
  - dependency clash: a/x requires b/y but the chosen version cannot be reconciled
  - dependency clash: b/y requires c/z but the chosen version cannot be reconciled

derivation:
  Because b/y 1.0.0 requires c/z ^1.0 and a/x 1.0.0 requires b/y ^1.0,
  a/x 1.0.0 requires c/z ^1.0.
  And because your manifest requires a/x ^1.0, every solution requires c/z ^1.0.
  So, because your manifest requires c/z ^2.0, version solving failed.
```

Create `internal/resolver/testdata/expected/unknown_package.txt`:

```
resolver: conflict — no compatible set of versions found:
  - package "acme/missing" is not available from any configured registry

derivation:
  Because acme/missing is not available from any configured registry
  and your manifest requires acme/missing ^1.0, version solving failed.
```

Create `internal/resolver/testdata/expected/no_versions.txt`:

```
resolver: conflict — no compatible set of versions found:
  - no published versions of "acme/strict" satisfy the requested constraint

derivation:
  Because no published version of acme/strict matches ^99.0
  and your manifest requires acme/strict ^99.0, version solving failed.
```

Create `internal/resolver/testdata/expected/diamond.txt`:

```
resolver: conflict — no compatible set of versions found:
  - dependency clash: a/x requires d/w but the chosen version cannot be reconciled
  - dependency clash: b/y requires d/w but the chosen version cannot be reconciled

derivation:
  (1) Because your manifest requires d/w ^2.0 and a/x 1.0.0 requires d/w ^1.0,
      a/x 1.0.0 is forbidden.
  And because your manifest requires a/x ^1.0, every solution conflicts with (1).
  And, because b/y 1.0.0 requires d/w ^1.0 and (1), b/y 1.0.0 is forbidden.
  So, because your manifest requires b/y ^1.0, version solving failed.
```

Treat these texts as the contract. If the renderer's word choice diverges (e.g., "every solution" vs "any solution"), update the fixtures so they keep matching the renderer — but never silently — and call out the change in the commit message. The exact prose is less important than its consistency across cases.

- [ ] **Step 3: Verify the test fails**

Run: `go test ./internal/resolver/... -run TestRender`

Expected: build error on the missing `RenderConflict` symbol. That is the cue to start Task 2.

- [ ] **Step 4: Commit fixtures + tests**

```bash
git add internal/resolver/render_test.go internal/resolver/testdata
git commit -m "test(resolver): fixtures for structured conflict-message renderer"
```

The commit intentionally lands the failing test before any production code; downstream tasks each turn on more of the renderer until all five fixtures match.

---

## Task 2: Skeleton renderer — headline, leaf causes, terminal failure

Establish the file shape and handle the easy cases first: the headline, the four leaf causes, and the terminal `IsFailure()` incompatibility. Conflict-derived nodes still print as a placeholder; that gets fleshed out in Task 3.

**Files:**
- Create: `internal/resolver/render.go`
- Modify: `internal/resolver/error.go` — `ConflictError.Error()` calls `RenderConflict`.

- [ ] **Step 1: Implement `render.go` (skeleton)**

Create `internal/resolver/render.go`:

```go
package resolver

import (
	"fmt"
	"strings"
)

// maxRenderColumns is the soft column limit used for line wrapping. 100 cols
// fits in a typical 120-col terminal with room for log-line prefixes (file:
// line, level, etc.) without forcing horizontal scrolling on 80-col mobile
// SSH sessions.
const maxRenderColumns = 100

// RenderConflict produces the user-facing string for a conflict-resolution
// failure. The shape is:
//
//	resolver: conflict — no compatible set of versions found:
//	  - <headline-1>
//	  - <headline-2>
//
//	derivation:
//	  Because A and B, X.
//	  And because Y, Z.
//	  So, because P, version solving failed.
//
// Headlines come from collectLeafCauses (defined in error.go). The derivation
// section is a depth-first, deduplicating walk of the incompatibility tree
// rooted at root.
func RenderConflict(root *Incompatibility) string {
	if root == nil {
		return "resolver: no solution exists"
	}

	var b strings.Builder
	b.WriteString("resolver: conflict — no compatible set of versions found:\n")
	for _, h := range collectLeafCauses(root) {
		fmt.Fprintf(&b, "  - %s\n", h)
	}
	b.WriteString("\nderivation:\n")

	r := newRenderer()
	r.write(&b, root, "  ")
	return strings.TrimRight(b.String(), "\n")
}

// renderer holds bookkeeping for a single RenderConflict invocation.
type renderer struct {
	// labels assigns a `(N)` label to incompatibilities that get referenced
	// more than once. The first encounter is rendered in full and labelled;
	// subsequent encounters use "(N)" instead of re-walking the subtree.
	labels map[*Incompatibility]int
	// seen counts visit counts per node in a pre-pass so we know which
	// subtrees deserve a label.
	visits map[*Incompatibility]int
}

func newRenderer() *renderer {
	return &renderer{
		labels: map[*Incompatibility]int{},
		visits: map[*Incompatibility]int{},
	}
}

// write emits the prose for ic at the given indent prefix.
//
// For Task 2 we handle leaf causes and the terminal failure node only;
// conflict recursion is filled in by Task 3.
func (r *renderer) write(b *strings.Builder, ic *Incompatibility, indent string) {
	if ic == nil {
		return
	}
	switch c := ic.Cause.(type) {
	case CauseRoot:
		writeWrapped(b, indent, "your manifest requires "+rootRequireText(ic)+".")
	case CauseNoVersions:
		writeWrapped(b, indent,
			fmt.Sprintf("no published version of %s matches %s.",
				c.Package, positiveTermConstraint(ic, c.Package)))
	case CauseUnknownPackage:
		writeWrapped(b, indent,
			fmt.Sprintf("%s is not available from any configured registry.", c.Package))
	case CauseDependency:
		writeWrapped(b, indent,
			fmt.Sprintf("%s requires %s.",
				dependerLabel(ic, c.Depender), dependeeLabel(ic, c.Dependee)))
	case CauseConflict:
		// Filled in by Task 3. For Task 2 we emit a placeholder line so the
		// skeleton still produces *some* output and tests can iterate.
		fmt.Fprintf(b, "%sTODO(conflict): %s\n", indent, ic.String())
	default:
		fmt.Fprintf(b, "%s%s\n", indent, ic.String())
	}
}

// rootRequireText returns "<package> <constraint>" for the dep term of a
// CauseRoot incompatibility, which always has shape {$root@*, !dep@C}.
func rootRequireText(ic *Incompatibility) string {
	for _, t := range ic.Terms {
		if t.Package == "$root" {
			continue
		}
		c := t.Constraint.Original
		if c == "" {
			c = "*"
		}
		return fmt.Sprintf("%s %s", t.Package, c)
	}
	return ic.String()
}

// positiveTermConstraint returns the constraint string for the term over pkg
// inside ic, falling back to "*" when not present.
func positiveTermConstraint(ic *Incompatibility, pkg string) string {
	for _, t := range ic.Terms {
		if t.Package != pkg {
			continue
		}
		c := t.Constraint.Original
		if c == "" {
			c = "*"
		}
		return c
	}
	return "*"
}

// dependerLabel renders "P V" where V is taken from the depender's positive
// term in ic. We always have one positive term over the depender; the solver
// constructs CauseDependency that way.
func dependerLabel(ic *Incompatibility, depender string) string {
	for _, t := range ic.Terms {
		if t.Package == depender && t.Positive {
			c := t.Constraint.Original
			if c == "" {
				c = "*"
			}
			return fmt.Sprintf("%s %s", depender, c)
		}
	}
	return depender
}

// dependeeLabel renders "Q C" where C is the constraint the depender places
// on the dependee, taken from the negative term over the dependee in ic.
func dependeeLabel(ic *Incompatibility, dependee string) string {
	for _, t := range ic.Terms {
		if t.Package == dependee && !t.Positive {
			c := t.Constraint.Original
			if c == "" {
				c = "*"
			}
			return fmt.Sprintf("%s %s", dependee, c)
		}
	}
	return dependee
}

// writeWrapped writes s to b at the given indent, wrapping long lines on
// whitespace at maxRenderColumns. Continuation lines reuse indent. Tokens
// that exceed the available width on their own (e.g., long URLs) overflow
// rather than break.
func writeWrapped(b *strings.Builder, indent, s string) {
	width := maxRenderColumns - len(indent)
	if width < 20 { // pathological deep nesting: just emit, don't fight it
		fmt.Fprintf(b, "%s%s\n", indent, s)
		return
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return
	}
	line := indent + words[0]
	for _, w := range words[1:] {
		if len(line)+1+len(w) > maxRenderColumns {
			b.WriteString(line)
			b.WriteByte('\n')
			line = indent + w
			continue
		}
		line += " " + w
	}
	b.WriteString(line)
	b.WriteByte('\n')
}
```

- [ ] **Step 2: Rewire `ConflictError.Error()` and remove the obsolete fallback**

In `internal/resolver/error.go`:

1. Replace the body of `ConflictError.Error()` with a single call to `RenderConflict`:

   ```go
   func (e *ConflictError) Error() string {
       return RenderConflict(e.Root)
   }
   ```

2. Delete `renderDerivation` — its sole caller is gone.

3. Keep `collectLeafCauses` and `termConstraintLabel`. The renderer reuses both.

4. Drop the now-unused `"fmt"` and `"strings"` imports if nothing else references them. Run `go build ./internal/resolver/...` to confirm.

- [ ] **Step 3: Run the leaf-only fixtures**

Run: `go test ./internal/resolver/... -run "TestRenderUnknownPackageLeaf|TestRenderNoVersionsLeaf|TestRenderConflictNilSafe|TestConflictErrorErrorPrefixesHeadline"`

Expected: PASS for the two leaf fixtures (their conflict tree is shallow enough that the placeholder branch never fires for the meaningful nodes; the headline + a single `Because <leaf> and <root>, version solving failed.` line is what the fixture demands — adjust the fixture text now if your output diverges in trivial ways).

The two-package, three-package, and diamond tests still fail — that is expected; Task 3 finishes them.

- [ ] **Step 4: Commit**

```bash
git add internal/resolver/render.go internal/resolver/error.go
git commit -m "feat(resolver): structured renderer skeleton + leaf-cause prose"
```

---

## Task 3: Recursive `CauseConflict` rendering with deduplication

Walk into `CauseConflict` nodes. The classic PubGrub formulation produces three line shapes, governed by whether each parent is a leaf or a recursive node:

1. Both parents are leaves (or already-labelled): one sentence.
   `Because <a> and <b>, <derived>.`
2. One parent is recursive, the other is a leaf: print the recursive one first as its own sentence, then continue with `And because <leaf>, <derived>.`.
3. The terminal node (the `IsFailure()` empty-set incompatibility): replace `<derived>` with `version solving failed`.

Deduplication: an incompatibility that appears in the tree more than once gets a `(N)` label on its first full rendering. Subsequent occurrences render as `(N)` inline rather than expanding. We compute occurrence counts in a pre-pass.

**Files:**
- Modify: `internal/resolver/render.go`

- [ ] **Step 1: Add the pre-pass that counts visits**

Inside `RenderConflict`, before calling `r.write`, populate `r.visits` by walking the tree:

```go
var count func(*Incompatibility)
count = func(ic *Incompatibility) {
	if ic == nil {
		return
	}
	r.visits[ic]++
	if r.visits[ic] > 1 {
		// Already counted children once; don't re-walk.
		return
	}
	if cc, ok := ic.Cause.(CauseConflict); ok {
		count(cc.Conflict)
		count(cc.Other)
	}
}
count(root)
```

Reusing `r.visits` later: `visits[ic] >= 2` means "this node deserves a label and gets referenced by `(N)` on every occurrence after the first".

- [ ] **Step 2: Implement the conflict branch**

Replace the `case CauseConflict` placeholder in `r.write` with a full implementation:

```go
case CauseConflict:
	// If this node has already been rendered in full earlier in the walk,
	// emit just its label instead of re-expanding.
	if n, ok := r.labels[ic]; ok {
		writeWrapped(b, indent, fmt.Sprintf("(%d)", n))
		return
	}

	// Decide whether to assign a label *now*. We label any conflict node
	// referenced more than once anywhere in the tree (visits >= 2) AND the
	// terminal failure node (always at the bottom, never labelled — labels
	// only target reusable subderivations).
	labelPrefix := ""
	if r.visits[ic] >= 2 && !ic.IsFailure() {
		n := len(r.labels) + 1
		r.labels[ic] = n
		labelPrefix = fmt.Sprintf("(%d) ", n)
	}

	// "<derived>" is the prose name for what this incompatibility says.
	derived := derivedClause(ic)

	// Choose phrasing based on what kind of children we have.
	leftLeaf := isLeaf(c.Conflict)
	rightLeaf := isLeaf(c.Other)

	switch {
	case leftLeaf && rightLeaf:
		writeWrapped(b, indent, fmt.Sprintf(
			"%sBecause %s and %s, %s.",
			labelPrefix, clauseFor(c.Conflict, r), clauseFor(c.Other, r), derived,
		))
	case !leftLeaf && rightLeaf:
		// Render the recursive parent first, then close with the leaf.
		r.write(b, c.Conflict, indent)
		writeWrapped(b, indent, fmt.Sprintf(
			"%sAnd because %s, %s.",
			labelPrefix, clauseFor(c.Other, r), derived,
		))
	case leftLeaf && !rightLeaf:
		r.write(b, c.Other, indent)
		writeWrapped(b, indent, fmt.Sprintf(
			"%sAnd because %s, %s.",
			labelPrefix, clauseFor(c.Conflict, r), derived,
		))
	default: // both recursive
		r.write(b, c.Conflict, indent)
		r.write(b, c.Other, indent)
		writeWrapped(b, indent, fmt.Sprintf(
			"%sAnd, because the above derivations hold, %s.",
			labelPrefix, derived,
		))
	}
```

The terminal failure node is special-cased inside `derivedClause`: when `ic.IsFailure()` returns true, the derived clause is the literal string `version solving failed`. Otherwise it's a description like `every solution requires d/w ^1.0`. The first sentence in the derivation block also gets a `So, ` prefix when it's the failure node — handled by `derivedClause` returning `"version solving failed"` and the outer caller's `Because`/`And because` words being swapped to `So, because` for the terminal case.

Refine the leaf-vs-failure prefix: change the terminal branch's wording to `So, because <leaf>, version solving failed.` by detecting `ic.IsFailure()` in `r.write` and choosing `So, because` instead of `Because`/`And because`. The cleanest place is at the top of the `case CauseConflict` block:

```go
opener := "Because"
joiner := "And because"
if ic.IsFailure() {
	opener = "So, because"
	joiner = "So, because"
}
```

Then thread `opener` / `joiner` into the four `fmt.Sprintf` calls above (replacing the literal `"Because"` / `"And because"`).

- [ ] **Step 3: Implement the supporting helpers**

Add to `render.go`:

```go
// isLeaf reports whether an incompatibility's cause is a non-conflict (one of
// the four base cases). A nil child counts as a leaf because RenderConflict
// emits nothing for it.
func isLeaf(ic *Incompatibility) bool {
	if ic == nil {
		return true
	}
	_, isConflict := ic.Cause.(CauseConflict)
	return !isConflict
}

// clauseFor returns the inline phrase used for a child incompatibility on the
// same line as its parent's "Because A and B" sentence. Leaves render as
// their full prose; already-labelled nodes render as "(N)".
func clauseFor(ic *Incompatibility, r *renderer) string {
	if ic == nil {
		return ""
	}
	if n, ok := r.labels[ic]; ok {
		return fmt.Sprintf("(%d)", n)
	}
	switch c := ic.Cause.(type) {
	case CauseRoot:
		return "your manifest requires " + rootRequireText(ic)
	case CauseNoVersions:
		return fmt.Sprintf("no published version of %s matches %s",
			c.Package, positiveTermConstraint(ic, c.Package))
	case CauseUnknownPackage:
		return fmt.Sprintf("%s is not available from any configured registry", c.Package)
	case CauseDependency:
		return fmt.Sprintf("%s requires %s",
			dependerLabel(ic, c.Depender), dependeeLabel(ic, c.Dependee))
	}
	// Conflict child without a label: shouldn't happen on the inline path
	// because the recursive case prints it first and then references it. Fall
	// back to a stringified form so we never panic.
	return ic.String()
}

// derivedClause names what the incompatibility *says*, used as the right-hand
// side of "Because X and Y, <derived>.". For the terminal failure node this
// is the literal "version solving failed". For derived intermediate nodes it
// summarizes the term set in plain English.
func derivedClause(ic *Incompatibility) string {
	if ic.IsFailure() {
		return "version solving failed"
	}
	// Pick a single "primary" non-$root term to lead the clause.
	var primary *Term
	for i := range ic.Terms {
		if ic.Terms[i].Package == "$root" {
			continue
		}
		primary = &ic.Terms[i]
		break
	}
	if primary == nil {
		return "no solution remains"
	}
	c := primary.Constraint.Original
	if c == "" {
		c = "*"
	}
	if primary.Positive {
		// Positive term in a conjunction-forbidden incompatibility means
		// "this version is forbidden". Phrase as such.
		return fmt.Sprintf("%s %s is forbidden", primary.Package, c)
	}
	// Negative term means "every solution must have <package> matching <c>".
	return fmt.Sprintf("every solution requires %s %s", primary.Package, c)
}
```

The phrasing is intentionally compact. If a fixture diff comes back with awkward English ("every solution requires a/x ^1.0 ^1.0" or similar duplication), trace it back to this function — it is the one place that decides what the derived clause says.

- [ ] **Step 4: Run the full test suite**

Run: `go test ./internal/resolver/...`

Expected: all five fixture tests pass. If a fixture diff is small and the renderer's text is the more sensible one, edit the fixture file rather than the renderer — but call out the fixture change explicitly in the commit message ("update fixture: 'every solution requires' reads more naturally than 'forces'").

If a fixture diff is _large_, the recursion logic is wrong; do not patch the fixture to paper over it. Step through with a debugger or `t.Logf("%+v", ic)` until the structural mismatch is obvious.

- [ ] **Step 5: Lint check**

Run: `go vet ./internal/resolver/...` and `gofmt -l internal/resolver`.

Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add internal/resolver/render.go internal/resolver/testdata
git commit -m "feat(resolver): full PubGrub derivation prose with dedup labels"
```

---

## Task 4: Wrapping & indentation hardening

The basic `writeWrapped` lands long sentences in the right shape, but it doesn't handle a few real-world cases:

- Nested conflict children whose own lines also need indentation continuation.
- Sentences that contain quoted package names (`"vendor/name"`) — the wrap logic must not split inside the quotes.
- Tab characters and other whitespace from upstream prose (defensive: never trust caller text).

**Files:**
- Modify: `internal/resolver/render.go`
- Modify: `internal/resolver/render_test.go` — add wrapping-specific unit tests.

- [ ] **Step 1: Add wrap unit tests**

Append to `render_test.go`:

```go
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
```

- [ ] **Step 2: Tighten `writeWrapped`**

The current implementation already calls `strings.Fields(s)`, which splits on any whitespace and collapses runs. That covers the normalization test. The column test passes with the existing logic too — verify by running the new tests first:

Run: `go test ./internal/resolver/... -run TestWriteWrapped`

Expected: PASS. If any case fails, the bug is almost certainly in the `len(line)+1+len(w) > maxRenderColumns` arithmetic when `line` is empty or when `indent` already exceeds the limit. Tighten as:

```go
func writeWrapped(b *strings.Builder, indent, s string) {
	words := strings.Fields(s)
	if len(words) == 0 {
		return
	}
	width := maxRenderColumns
	if len(indent) >= maxRenderColumns-10 {
		// Pathological deep indent: emit each word on its own line so we
		// don't loop forever trying to wrap.
		for _, w := range words {
			fmt.Fprintf(b, "%s%s\n", indent, w)
		}
		return
	}
	line := indent + words[0]
	for _, w := range words[1:] {
		// +1 for the joining space.
		if len(line)+1+len(w) > width {
			b.WriteString(line)
			b.WriteByte('\n')
			line = indent + w
			continue
		}
		line += " " + w
	}
	b.WriteString(line)
	b.WriteByte('\n')
}
```

- [ ] **Step 3: Run the full suite**

Run: `go test ./internal/resolver/...`

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/resolver
git commit -m "test(resolver): pin wrap and indentation behavior of derivation renderer"
```

---

## Task 5: Integration check against the live solver

The unit tests above use handcrafted incompatibility trees. Real conflict trees come out of `solve.go`; their structure can differ in subtle ways from what we hand-build. This task drives the renderer end-to-end through the actual solver to confirm the output is sensible.

**Files:**
- Modify: `internal/resolver/solve_test.go` — add an end-to-end test that constructs a conflicting universe, runs `Solve`, and asserts on the rendered error.

- [ ] **Step 1: Append the integration test**

Add to `internal/resolver/solve_test.go` (use the existing in-memory `SourceLookup` test helper):

```go
func TestSolveConflictMessageReadsAsDerivation(t *testing.T) {
	src := newTestLookup(map[string][]string{
		"a/x": {"1.0.0"},
		"b/y": {"1.0.0", "2.0.0"},
	})
	src.SetRequires("a/x", "1.0.0", map[string]string{"b/y": "^1.0"})
	m := &manifest.Manifest{
		Require: map[string]string{
			"a/x": "^1.0",
			"b/y": "^2.0",
		},
	}
	_, err := Solve(context.Background(), Input{Manifest: m, Source: src})
	if err == nil {
		t.Fatalf("expected ConflictError, got nil")
	}
	ce := new(ConflictError)
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ConflictError, got %T: %v", err, err)
	}
	msg := ce.Error()
	for _, want := range []string{
		"resolver: conflict",
		"derivation:",
		"a/x",
		"b/y",
		"version solving failed",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("rendered message missing %q.\n--- full message ---\n%s", want, msg)
		}
	}
	// No "TODO(conflict)" placeholders should leak.
	if strings.Contains(msg, "TODO(conflict)") {
		t.Errorf("placeholder leaked into rendered output:\n%s", msg)
	}
}
```

If `newTestLookup`/`SetRequires` do not exist with those exact names, adapt to whatever Stage 1 Plan 3 / `testlookup` exposes. The test logic is the load-bearing part: real solver, real conflict tree, real rendered string.

- [ ] **Step 2: Run the integration test**

Run: `go test ./internal/resolver/... -run TestSolveConflictMessageReadsAsDerivation -v`

Expected: PASS. The test prints the rendered string in `-v` mode; eyeball it for sanity. Anything that looks like garbage (unbalanced parentheses, repeated `because because`, dangling `(1)`) is a real bug — fix forward in `render.go` and rerun, do not skip.

- [ ] **Step 3: Run every resolver test**

Run: `go test ./internal/resolver/...`

Expected: green.

- [ ] **Step 4: Run every test the resolver feeds into**

Run: `go test ./internal/orchestrator/... ./internal/cli/...`

Expected: green. The orchestrator and CLI may have tests that snapshot error strings; if any of those break because the headline format changed slightly, update them in the same commit and call it out.

- [ ] **Step 5: Commit**

```bash
git add internal/resolver internal/orchestrator internal/cli
git commit -m "test(resolver): end-to-end check that real conflicts render as derivations"
```

---

## Task 6: Manual verification + spec note

Before declaring the plan complete, run a real failing install and read the output as a user would. The eye catches issues that snapshot tests miss — bad line breaks, awkward phrasing, label placement.

**Files:**
- No source changes expected.
- Optionally: `docs/superpowers/specs/2026-05-07-gomposer-design.md` (add one paragraph if missing, otherwise skip).

- [ ] **Step 1: Build and exercise**

```bash
go build -o gomposer ./cmd/gomposer
mkdir -p /tmp/conflict-demo && cd /tmp/conflict-demo
cat > composer.json <<'JSON'
{
  "name": "demo/conflict",
  "require": {
    "symfony/console": "^6.0",
    "symfony/polyfill-mbstring": "^2.0"
  }
}
JSON
./gomposer install --project /tmp/conflict-demo 2>&1 | head -50
```

Expected: a `resolver: conflict —` headline, then a blank line, then a `derivation:` block of indented "Because … and …" sentences ending in "version solving failed.". No `TODO(conflict)` strings, no naked `&{` printouts of `Incompatibility`, no over-100-column lines (eyeball with `awk '{ if (length > 100) print NR": "length" cols" }'`).

If the output looks wrong, fix `render.go` and rerun before continuing.

- [ ] **Step 2: Optional — update the design spec**

If the design spec has a section on user-facing error formatting, append one paragraph documenting the new shape. If it does not, skip — the renderer itself is the spec.

- [ ] **Step 3: Final test sweep**

Run: `go test ./...`

Expected: green.

- [ ] **Step 4: Commit (if step 2 produced changes)**

```bash
git add docs/superpowers/specs
git commit -m "docs(spec): note structured PubGrub derivation in error output"
```

---

## Stage 3 Plan 5 acceptance check

- [ ] `internal/resolver/render.go` exists and exports `RenderConflict(*Incompatibility) string`.
- [ ] `ConflictError.Error()` is a one-line wrapper that calls `RenderConflict`.
- [ ] The five fixture tests (`two_package_conflict`, `three_package_chain`, `unknown_package`, `no_versions`, `diamond`) pass byte-for-byte against `testdata/expected/*.txt`.
- [ ] `RenderConflict(nil)` returns `"resolver: no solution exists"` (no panic).
- [ ] Output begins with the `resolver: conflict — no compatible set of versions found:` headline followed by the bullet list from `collectLeafCauses`, then a blank line, then a `derivation:` block.
- [ ] Derivation lines are indented by two spaces per nesting level and wrap at 100 columns on whitespace; no line exceeds 100 cols unless it contains a single token wider than the available width.
- [ ] An incompatibility appearing more than once in the conflict tree is rendered in full once with a `(N)` label and referenced as `(N)` thereafter.
- [ ] The terminal `IsFailure()` node renders with a `So, because …, version solving failed.` sentence.
- [ ] `TestSolveConflictMessageReadsAsDerivation` runs the real solver and asserts the rendered string contains the expected anchors and no `TODO(conflict)` placeholders.
- [ ] `go test ./...` is green.
- [ ] Manual `gomposer install` against a constructed conflict produces output that reads naturally to a human and respects the 100-col wrap.

If any item fails, fix forward — do not loosen the fixtures to make tests pass; the fixtures are the contract.
