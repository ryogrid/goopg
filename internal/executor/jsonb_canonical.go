package executor

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/goopg/goopg/internal/pgnodes"
)

// jsonb canonicalisation (M0119-0006, 64th slice).
//
// goopg stores a jsonb value as the JSON text it was handed, verbatim, where
// PostgreSQL stores a parsed JsonbContainer and re-emits it through jsonb_out
// every time it is read. The user-visible consequence is a text divergence on
// every non-canonical literal: `'{"b":1,"a":2}'::jsonb` renders as itself on
// goopg where PG renders `{"a": 2, "b": 1}`. This file closes that divergence
// at the INPUT boundary — the value is canonicalised once, when it is stored —
// leaving the separate heap-image divergence (goopg stores the text varlena
// where upstream stores a JEntry tree) to its own slice.
//
// The canonical form is jsonb_out's compact rendering (jsonb.c
// JsonbToCStringWorker with indent=false):
//
//   - objects are `{` `"key": value` pairs joined by `, ` and closed `}`;
//     arrays are `[` elements joined by `, ` and closed `]`;
//   - object keys are ordered by LENGTH first, then bytewise —
//     lengthCompareJsonbStringValue (jsonb_util.c), NOT Go's lexicographic
//     sort, which disagrees whenever two keys differ in length ("aa" sorts
//     before "b" bytewise, after it by length);
//   - duplicate keys collapse last-wins (uniqueifyJsonbObject keeps the pair
//     with the highest `order`, which is the one parsed last);
//   - strings are escaped by escape_json_char (json.c): \b \f \n \r \t \" \\
//     plus \u00xx for control bytes below 0x20; every other byte — including
//     non-ASCII UTF-8 — is passed through verbatim (no HTML escaping, no
//     \uXXXX for >= 0x80, unlike encoding/json's marshaler);
//   - numbers are round-tripped through numeric_in / numeric_out, so `1e0`
//     becomes `1`, `3e1` becomes `30`, `1.00` keeps its scale, and `-0.0`
//     becomes `0.0` (numeric drops the sign of a zero).

// jsonbKind enumerates the JSON value shapes. It is private to this file: the
// tree is only built to be re-emitted canonically, never stored or compared.
type jsonbKind int

const (
	jsonbNull jsonbKind = iota
	jsonbBool
	jsonbNumber
	jsonbString
	jsonbArray
	jsonbObject
)

// jsonbValue is an ordered JSON value. Object members keep their INPUT order so
// the last-wins duplicate rule can be applied before the canonical sort.
type jsonbValue struct {
	kind jsonbKind
	b    bool
	num  string // raw number literal (json.Number's text)
	str  string // unescaped string payload
	arr  []jsonbValue
	obj  []jsonbMember
}

type jsonbMember struct {
	key string
	val jsonbValue
}

// invalidJSONBError is the error jsonb_from_cstring reports for malformed input
// (json_ereport, ERRCODE_INVALID_TEXT_REPRESENTATION). The message is "type
// json", not "type jsonb", matching the wording upstream shares with `json`.
func invalidJSONBError() *ExecError {
	return &ExecError{Code: "22P02", Message: "invalid input syntax for type json"}
}

// canonicalizeJSONB parses s as a single jsonb value and returns its jsonb_out
// canonical rendering. It returns invalidJSONBError for malformed input or for
// a string that holds more than one value (`{} {}`).
func canonicalizeJSONB(s string) (string, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()

	v, err := parseJSONBValue(dec)
	if err != nil {
		return "", invalidJSONBError()
	}
	// The whole string must be exactly one value: anything but a trailing EOF
	// (the decoder skips whitespace) is trailing garbage.
	if _, err := dec.Token(); err != io.EOF {
		return "", invalidJSONBError()
	}

	var b strings.Builder
	if err := appendJSONBCanonical(&b, v); err != nil {
		return "", invalidJSONBError()
	}
	return b.String(), nil
}

// parseJSONBValue reads one value from dec using the token stream, which
// preserves object member order (and duplicates) that Decode into
// map[string]any would drop. Strings arrive already unescaped; numbers arrive
// as json.Number (the raw literal), so neither loses information.
func parseJSONBValue(dec *json.Decoder) (jsonbValue, error) {
	tok, err := dec.Token()
	if err != nil {
		return jsonbValue{}, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			var obj []jsonbMember
			for dec.More() {
				ktok, err := dec.Token()
				if err != nil {
					return jsonbValue{}, err
				}
				key, ok := ktok.(string)
				if !ok {
					return jsonbValue{}, fmt.Errorf("json object key is %T, want string", ktok)
				}
				val, err := parseJSONBValue(dec)
				if err != nil {
					return jsonbValue{}, err
				}
				obj = append(obj, jsonbMember{key: key, val: val})
			}
			if _, err := dec.Token(); err != nil { // consume '}'
				return jsonbValue{}, err
			}
			return jsonbValue{kind: jsonbObject, obj: obj}, nil
		case '[':
			var arr []jsonbValue
			for dec.More() {
				v, err := parseJSONBValue(dec)
				if err != nil {
					return jsonbValue{}, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil { // consume ']'
				return jsonbValue{}, err
			}
			return jsonbValue{kind: jsonbArray, arr: arr}, nil
		}
		return jsonbValue{}, fmt.Errorf("unexpected delimiter %q", t)
	case string:
		return jsonbValue{kind: jsonbString, str: t}, nil
	case json.Number:
		return jsonbValue{kind: jsonbNumber, num: t.String()}, nil
	case bool:
		return jsonbValue{kind: jsonbBool, b: t}, nil
	case nil:
		return jsonbValue{kind: jsonbNull}, nil
	}
	return jsonbValue{}, fmt.Errorf("unexpected json token %T", tok)
}

// appendJSONBCanonical writes v in jsonb_out's compact form.
func appendJSONBCanonical(b *strings.Builder, v jsonbValue) error {
	switch v.kind {
	case jsonbNull:
		b.WriteString("null")
	case jsonbBool:
		if v.b {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case jsonbNumber:
		s, err := canonicalizeJSONBNumber(v.num)
		if err != nil {
			return err
		}
		b.WriteString(s)
	case jsonbString:
		appendJSONBEscaped(b, v.str)
	case jsonbArray:
		b.WriteByte('[')
		for i, e := range v.arr {
			if i > 0 {
				b.WriteString(", ")
			}
			if err := appendJSONBCanonical(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case jsonbObject:
		b.WriteByte('{')
		pairs := canonicalJSONBObjectPairs(v.obj)
		for i, p := range pairs {
			if i > 0 {
				b.WriteString(", ")
			}
			appendJSONBEscaped(b, p.key)
			b.WriteString(": ")
			if err := appendJSONBCanonical(b, p.val); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	}
	return nil
}

// canonicalJSONBObjectPairs applies the last-wins duplicate rule then sorts by
// length-then-bytewise key order. Walking input order backwards keeps the LAST
// occurrence of each key; sorting after the dedup means no equal keys remain,
// so the sort need not be stable.
func canonicalJSONBObjectPairs(members []jsonbMember) []jsonbMember {
	seen := make(map[string]bool, len(members))
	out := make([]jsonbMember, 0, len(members))
	for i := len(members) - 1; i >= 0; i-- {
		k := members[i].key
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, members[i])
	}
	sort.Slice(out, func(i, j int) bool {
		return lengthCompareJSONBKey(out[i].key, out[j].key) < 0
	})
	return out
}

// lengthCompareJSONBKey is lengthCompareJsonbStringValue: equal lengths compare
// bytewise (memcmp), otherwise the shorter key sorts first.
func lengthCompareJSONBKey(a, b string) int {
	if len(a) == len(b) {
		return strings.Compare(a, b)
	}
	if len(a) > len(b) {
		return 1
	}
	return -1
}

// canonicalizeJSONBNumber round-trips a JSON number literal through numeric_in
// / numeric_out, which is exactly what jsonb_in_scalar → jsonb_put_escaped_value
// does (jsonb.c: JSON_TOKEN_NUMBER → numeric_in, output → numeric_out).
func canonicalizeJSONBNumber(lit string) (string, error) {
	body, err := pgnodes.NumericBodyFromText(lit)
	if err != nil {
		return "", err
	}
	return pgnodes.NumericTextFromBody(body)
}

// appendJSONBEscaped writes s as a JSON string literal using escape_json_char's
// exact rule set (json.c). Non-ASCII bytes pass through verbatim; only the
// seven named escapes and the sub-0x20 control range are escaped.
func appendJSONBEscaped(b *strings.Builder, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\b':
			b.WriteString("\\b")
		case '\f':
			b.WriteString("\\f")
		case '\n':
			b.WriteString("\\n")
		case '\r':
			b.WriteString("\\r")
		case '\t':
			b.WriteString("\\t")
		case '"':
			b.WriteString("\\\"")
		case '\\':
			b.WriteString("\\\\")
		default:
			if c < 0x20 {
				fmt.Fprintf(b, "\\u%04x", c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
}
