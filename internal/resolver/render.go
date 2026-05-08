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

	// Pre-pass: count visits so we know which subtrees deserve a label.
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

	r.write(&b, root, "  ")
	return strings.TrimRight(b.String(), "\n")
}

// renderer holds bookkeeping for a single RenderConflict invocation.
type renderer struct {
	// labels assigns a `(N)` label to incompatibilities that get referenced
	// more than once. The first encounter is rendered in full and labelled;
	// subsequent encounters use "(N)" instead of re-walking the subtree.
	labels map[*Incompatibility]int
	// visits counts visit counts per node in a pre-pass so we know which
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

		opener := "Because"
		joiner := "And because"
		if ic.IsFailure() {
			joiner = "So, because"
		}

		// Choose phrasing based on what kind of children we have.
		leftLeaf := isLeaf(c.Conflict)
		rightLeaf := isLeaf(c.Other)

		switch {
		case leftLeaf && rightLeaf:
			// Two-clause sentence: "Because A and B,\n<derived>."
			writeWrapped(b, indent, fmt.Sprintf(
				"%s%s %s and %s,",
				labelPrefix, opener, clauseFor(c.Conflict, r), clauseFor(c.Other, r),
			))
			writeWrapped(b, indent, derived+".")
		case !leftLeaf && rightLeaf:
			// Render the recursive parent first, then close with the leaf.
			r.write(b, c.Conflict, indent)
			writeWrapped(b, indent, fmt.Sprintf(
				"%s%s %s, %s.",
				labelPrefix, joiner, clauseFor(c.Other, r), derived,
			))
		case leftLeaf && !rightLeaf:
			r.write(b, c.Other, indent)
			writeWrapped(b, indent, fmt.Sprintf(
				"%s%s %s, %s.",
				labelPrefix, joiner, clauseFor(c.Conflict, r), derived,
			))
		default: // both recursive
			r.write(b, c.Conflict, indent)
			r.write(b, c.Other, indent)
			writeWrapped(b, indent, fmt.Sprintf(
				"%sAnd, because the above derivations hold, %s.",
				labelPrefix, derived,
			))
		}
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
// their full prose; already-labelled nodes render as "(N)". Leaf nodes that
// appear multiple times in the tree are labeled on first inline use so later
// occurrences can reference the label.
func clauseFor(ic *Incompatibility, r *renderer) string {
	if ic == nil {
		return ""
	}
	if n, ok := r.labels[ic]; ok {
		return fmt.Sprintf("(%d)", n)
	}
	// Assign a dedup label if this node is visited more than once.
	var text string
	switch c := ic.Cause.(type) {
	case CauseRoot:
		text = "your manifest requires " + rootRequireText(ic)
	case CauseNoVersions:
		text = fmt.Sprintf("no published version of %s matches %s",
			c.Package, positiveTermConstraint(ic, c.Package))
	case CauseUnknownPackage:
		text = fmt.Sprintf("%s is not available from any configured registry", c.Package)
	case CauseDependency:
		text = fmt.Sprintf("%s requires %s",
			dependerLabel(ic, c.Depender), dependeeLabel(ic, c.Dependee))
	default:
		// Conflict child without a label: shouldn't happen on the inline path
		// because the recursive case prints it first and then references it.
		// Fall back to a stringified form so we never panic.
		text = ic.String()
	}
	// If this leaf is visited multiple times, label it on first use.
	if r.visits[ic] >= 2 {
		n := len(r.labels) + 1
		r.labels[ic] = n
		return fmt.Sprintf("(%d) %s", n, text)
	}
	return text
}

// derivedClause names what the incompatibility *says*, used as the right-hand
// side of "Because X and Y, <derived>.". For the terminal failure node this
// is the literal "version solving failed". For derived intermediate nodes it
// summarizes the term set in plain English.
func derivedClause(ic *Incompatibility) string {
	if ic.IsFailure() {
		return "version solving failed"
	}
	// Collect non-$root terms.
	var pos, neg *Term
	for i := range ic.Terms {
		if ic.Terms[i].Package == "$root" {
			continue
		}
		t := &ic.Terms[i]
		if t.Positive && pos == nil {
			pos = t
		} else if !t.Positive && neg == nil {
			neg = t
		}
	}
	// When there is exactly one positive (depender) + one negative (dependee)
	// term, the incompatibility expresses a dependency: "P V requires Q C".
	if pos != nil && neg != nil {
		pv := pos.Constraint.Original
		if pv == "" {
			pv = "*"
		}
		qc := neg.Constraint.Original
		if qc == "" {
			qc = "*"
		}
		return fmt.Sprintf("%s %s requires %s %s", pos.Package, pv, neg.Package, qc)
	}
	if neg != nil {
		c := neg.Constraint.Original
		if c == "" {
			c = "*"
		}
		return fmt.Sprintf("every solution requires %s %s", neg.Package, c)
	}
	if pos != nil {
		c := pos.Constraint.Original
		if c == "" {
			c = "*"
		}
		// Positive term in a conjunction-forbidden incompatibility means
		// "this version is forbidden". Phrase as such.
		return fmt.Sprintf("%s %s is forbidden", pos.Package, c)
	}
	return "no solution remains"
}

// writeWrapped writes s to b at the given indent, wrapping long lines on
// whitespace at maxRenderColumns. Continuation lines reuse indent. Tokens
// that exceed the available width on their own (e.g., long URLs) overflow
// rather than break.
func writeWrapped(b *strings.Builder, indent, s string) {
	words := strings.Fields(s)
	if len(words) == 0 {
		return
	}
	if len(indent) >= maxRenderColumns-10 {
		// Pathological deep indent: emit each word on its own line so we
		// don't loop forever trying to wrap.
		for _, w := range words {
			fmt.Fprintf(b, "%s%s\n", indent, w)
		}
		return
	}
	width := maxRenderColumns
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
