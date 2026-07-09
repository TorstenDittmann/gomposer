// Package manifest — contenthash.go implements Composer's
// Locker::getContentHash algorithm so gomposer's composer.lock carries a
// content-hash that upstream Composer accepts.
package manifest

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// relevantKeys mirrors Composer\Package\Locker::$relevantKeys. Changing
// this set breaks cross-tool compat.
var relevantKeys = map[string]struct{}{
	"name":              {},
	"version":           {},
	"require":           {},
	"require-dev":       {},
	"conflict":          {},
	"replace":           {},
	"provide":           {},
	"minimum-stability": {},
	"prefer-stable":     {},
	"repositories":      {},
	"extra":             {},
}

// orderedObject is a JSON object that preserves insertion order.
//
// Composer's PHP implementation decodes composer.json into associative
// arrays (which preserve their original key order) and only reorders the
// *top-level* relevant-keys map via ksort(); nested structures (e.g. the
// package list inside "require") keep whatever order they had in the
// source file. Go's map[string]any cannot represent that: encoding/json
// always sorts every map's keys alphabetically, at every nesting level.
// Since the content-hash is a literal MD5 of the encoded bytes, that
// reordering silently produces a different (wrong) hash for any manifest
// whose nested objects have more than one key in non-alphabetical order.
// orderedObject exists to reproduce Composer's exact byte output.
type orderedObject struct {
	keys []string
	vals []any
}

// get returns the value for key and whether it was present.
func (o *orderedObject) get(key string) (any, bool) {
	for i, k := range o.keys {
		if k == key {
			return o.vals[i], true
		}
	}
	return nil, false
}

// set appends a key/value pair. Callers are responsible for not
// introducing duplicate keys.
func (o *orderedObject) set(key string, val any) {
	o.keys = append(o.keys, key)
	o.vals = append(o.vals, val)
}

// Len, Less, and Swap implement sort.Interface, sorting by key. This is
// used once, on the top-level filtered object, to reproduce PHP's
// ksort($relevantContent).
func (o *orderedObject) Len() int           { return len(o.keys) }
func (o *orderedObject) Less(i, j int) bool { return o.keys[i] < o.keys[j] }
func (o *orderedObject) Swap(i, j int) {
	o.keys[i], o.keys[j] = o.keys[j], o.keys[i]
	o.vals[i], o.vals[j] = o.vals[j], o.vals[i]
}

// MarshalJSON emits o as a compact JSON object, keys in slice order,
// values recursively marshaled the same order-preserving way. Escaping
// is deliberately minimal here (quotes/backslash/control chars only, no
// HTML-escaping, no slash or non-ASCII transform): phpCompatibleJSON
// applies the PHP-specific slash/non-ASCII transform afterward, as a
// single pass over the fully-encoded bytes.
func (o *orderedObject) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(jsonEscapeString(k))
		buf.WriteByte(':')
		vb, err := marshalOrderedValue(o.vals[i])
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// marshalOrderedValue encodes a value produced by decodeOrderedValue
// (or, for arrays/leaves reached while walking one, their natural Go
// representation) into compact JSON, preserving object key order.
func marshalOrderedValue(v any) ([]byte, error) {
	switch val := v.(type) {
	case *orderedObject:
		return val.MarshalJSON()
	case []any:
		var buf bytes.Buffer
		buf.WriteByte('[')
		for i, item := range val {
			if i > 0 {
				buf.WriteByte(',')
			}
			b, err := marshalOrderedValue(item)
			if err != nil {
				return nil, err
			}
			buf.Write(b)
		}
		buf.WriteByte(']')
		return buf.Bytes(), nil
	case string:
		return []byte(jsonEscapeString(val)), nil
	case json.Number:
		return marshalJSONNumber(val)
	case bool:
		if val {
			return []byte("true"), nil
		}
		return []byte("false"), nil
	case nil:
		return []byte("null"), nil
	default:
		return nil, fmt.Errorf("manifest: content-hash: unsupported value type %T", v)
	}
}

// marshalJSONNumber re-encodes a json.Number the way PHP's
// json_encode(json_decode($x)) normalizes numeric literals: "1.50" becomes
// "1.5", "1e2" becomes "100". json.Number preserves the exact source text,
// which would otherwise leak non-canonical formatting straight into the
// content-hash. Integers that fit in int64 are round-tripped via
// strconv.FormatInt to avoid float64 precision loss; everything else goes
// through float64, whose Go json.Marshal formatting matches PHP's shortest
// round-trip float representation.
func marshalJSONNumber(val json.Number) ([]byte, error) {
	s := string(val)
	if !strings.ContainsAny(s, ".eE") {
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return []byte(strconv.FormatInt(i, 10)), nil
		}
	}
	f, err := val.Float64()
	if err != nil {
		return nil, fmt.Errorf("manifest: content-hash: parse number %q: %w", s, err)
	}
	return json.Marshal(f)
}

// jsonEscapeString applies plain JSON string escaping (quotes, backslash,
// and control characters) without HTML-escaping, slash-escaping, or
// non-ASCII escaping — those PHP-specific transforms are applied later,
// in one pass, by phpCompatibleJSON.
func jsonEscapeString(s string) string {
	var buf strings.Builder
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&buf, `\u%04x`, r)
			} else {
				buf.WriteRune(r)
			}
		}
	}
	buf.WriteByte('"')
	return buf.String()
}

// decodeOrderedValue reads the next JSON value from dec, preserving
// object key order. Objects decode to *orderedObject, arrays to []any,
// and scalars to string / json.Number / bool / nil. dec must have
// UseNumber() set so numeric literals round-trip exactly.
func decodeOrderedValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := &orderedObject{}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyTok.(string)
				if !ok {
					return nil, fmt.Errorf("manifest: content-hash: expected object key, got %v", keyTok)
				}
				val, err := decodeOrderedValue(dec)
				if err != nil {
					return nil, err
				}
				obj.set(key, val)
			}
			if _, err := dec.Token(); err != nil { // consume '}'
				return nil, err
			}
			return obj, nil
		case '[':
			arr := []any{}
			for dec.More() {
				val, err := decodeOrderedValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, val)
			}
			if _, err := dec.Token(); err != nil { // consume ']'
				return nil, err
			}
			return arr, nil
		default:
			return nil, fmt.Errorf("manifest: content-hash: unexpected delimiter %v", t)
		}
	case string, json.Number, bool, nil:
		return t, nil
	default:
		return nil, fmt.Errorf("manifest: content-hash: unexpected token %v (%T)", tok, tok)
	}
}

// ContentHash computes Composer's content-hash of the manifest bytes.
// Callers pass the raw composer.json contents (not the parsed Manifest
// struct) so field-name drift can't cause hash drift.
func ContentHash(manifestBytes []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(manifestBytes))
	dec.UseNumber()
	rootVal, err := decodeOrderedValue(dec)
	if err != nil {
		return "", fmt.Errorf("manifest: content-hash: decode: %w", err)
	}
	root, ok := rootVal.(*orderedObject)
	if !ok {
		return "", fmt.Errorf("manifest: content-hash: manifest root is not a JSON object")
	}

	filtered := &orderedObject{}
	for i, k := range root.keys {
		if _, ok := relevantKeys[k]; ok {
			filtered.set(k, root.vals[i])
		}
	}
	// Composer also carries config.platform when present.
	if cfgVal, ok := root.get("config"); ok {
		if cfg, ok := cfgVal.(*orderedObject); ok {
			if plat, ok := cfg.get("platform"); ok && plat != nil {
				filtered.set("config", &orderedObject{keys: []string{"platform"}, vals: []any{plat}})
			}
		}
	}
	// Mirrors PHP's ksort($relevantContent): sort only the top-level
	// keys; nested structures keep their original file order.
	sort.Sort(filtered)

	encoded, err := phpCompatibleJSON(filtered)
	if err != nil {
		return "", fmt.Errorf("manifest: content-hash: encode: %w", err)
	}
	sum := md5.Sum(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// phpCompatibleJSON produces bytes equivalent to PHP's default
// json_encode: no indentation, slashes escaped as \/, non-ASCII
// characters escaped as \uXXXX (surrogate pairs for code points above
// U+FFFF). HTML metacharacters (<, >, &) are NOT escaped — PHP doesn't
// escape them by default either, and neither does Go with
// SetEscapeHTML(false).
//
// Object key order is whatever v's own JSON encoding produces: for
// *orderedObject (used by ContentHash) that's insertion order per the
// custom MarshalJSON above; for plain map[string]any (used directly by
// callers/tests that don't care about nested order) encoding/json sorts
// keys alphabetically.
func phpCompatibleJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// json.Encoder appends a trailing newline; strip it.
	raw := bytes.TrimRight(buf.Bytes(), "\n")

	// Walk the encoded bytes and apply two transforms:
	//   1. every literal '/' in a string value becomes '\/'
	//   2. every rune outside the ASCII range becomes '\uXXXX' (surrogate
	//      pair for code points >= 0x10000).
	// Structural characters (', ', :, {, }, [, ]) never contain / or
	// non-ASCII, so a byte-level pass is safe.
	var out strings.Builder
	out.Grow(len(raw))
	i := 0
	for i < len(raw) {
		c := raw[i]
		if c == '/' {
			out.WriteString(`\/`)
			i++
			continue
		}
		if c < 0x80 {
			out.WriteByte(c)
			i++
			continue
		}
		r, size := utf8.DecodeRune(raw[i:])
		if r == utf8.RuneError && size == 1 {
			// Invalid UTF-8; pass the byte through untouched.
			out.WriteByte(c)
			i++
			continue
		}
		if r <= 0xFFFF {
			fmt.Fprintf(&out, `\u%04x`, r)
		} else {
			// Split into UTF-16 surrogate pair (PHP does this).
			r2 := r - 0x10000
			hi := 0xD800 | ((r2 >> 10) & 0x3FF)
			lo := 0xDC00 | (r2 & 0x3FF)
			fmt.Fprintf(&out, `\u%04x\u%04x`, hi, lo)
		}
		i += size
	}
	return []byte(out.String()), nil
}
