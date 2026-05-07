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
				// Skip modifiers: abstract, final, readonly come before class keyword.
				// They are already handled by being set as prevSig = tokIdent below.
				name, ok, err := s.readDeclName()
				if err != nil {
					return nil, err
				}
				if ok {
					out = append(out, qualify(ns, name))
				}
				prevSig = tokIdent
				continue
			case "abstract", "final", "readonly":
				// modifiers — set prevSig to something that does not block class detection
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

func isWS(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}
func isIdentCont(c byte) bool {
	return isIdentStart(c) || unicode.IsDigit(rune(c))
}
