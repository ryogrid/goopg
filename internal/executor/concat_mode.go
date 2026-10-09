package executor

import (
	"encoding/json"
	"io"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// `||` by static operand type (M0146-0074).
//
// Arrays, jsonb and text all travel through the executor as string Datums,
// so evalConcat used to decide from the values alone: any operand that
// looked like `{…}` was treated as an array. `'a' || '{9}'::text` therefore
// returned `{a,9}`, and two jsonb objects were merged as array elements.
// PG resolves `||` from the operand types at parse time:
//   - text-like operands use textcat, anytextcat or textanycat (varlena.c),
//     a plain byte concatenation;
//   - array operands use array_cat, array_append or array_prepend;
//   - jsonb operands use jsonb_concat (jsonfuncs.c).
// concatModeOf makes the same choice from the expressions' static types. An
// operand whose type cannot be resolved, or is an array or polymorphic type,
// keeps the shape-based path. goopg's array handling is that path, and a
// mis-typed array expression must not lose it.

type concatMode uint8

const (
	// concatGuess: static types unknown or array-typed; decide by shape.
	concatGuess concatMode = iota
	// concatText: textcat / anytextcat / textanycat / byteacat.
	concatText
	// concatJSONB: jsonb_concat.
	concatJSONB
)

// concatModeOf is the `||` operator PG resolves for operands l and r. An
// untyped string literal next to jsonb resolves to jsonb, as an unknown
// literal adopts the other operand's type; jsonb next to a typed character
// string is text concatenation.
func concatModeOf(l, r optimizer.Expr) concatMode {
	lt, lok := optimizer.ExprResultType(l)
	rt, rok := optimizer.ExprResultType(r)
	lj, rj := lok && isJSONBType(lt), rok && isJSONBType(rt)
	if (lj && (rj || isBareStringLit(r))) || (rj && isBareStringLit(l)) {
		return concatJSONB
	}
	if !lok || !rok || isArrayOrPolymorphicType(lt) || isArrayOrPolymorphicType(rt) {
		return concatGuess
	}
	// jsonb beside a character string is textanycat / anytextcat.
	if (lj && !isCharacterStringType(rt)) || (rj && !isCharacterStringType(lt)) {
		return concatGuess
	}
	return concatText
}

func isJSONBType(t catalog.Type) bool {
	return !t.IsArray && strings.EqualFold(t.Name, "jsonb")
}

// isArrayOrPolymorphicType reports an array type in any spelling a resolved
// type can carry (IsArray, `text[]` from a cast, `_text`), or a polymorphic
// pseudo-type whose concrete type is decided per call.
func isArrayOrPolymorphicType(t catalog.Type) bool {
	name := strings.ToLower(t.Name)
	if t.IsArray || strings.HasSuffix(name, "]") || strings.HasPrefix(name, "_") {
		return true
	}
	return strings.HasPrefix(name, "any")
}

// jsonbConcat is jsonb_concat (jsonfuncs.c, IteratorConcat):
//   - two objects merge, and the right operand's value wins for a shared key;
//   - otherwise the result is an array of the left operand's elements
//     followed by the right operand's. A non-array operand (an object or a
//     scalar) counts as one element.
//
// Both operands go through jsonb input, so a malformed literal is reported
// as for any jsonb cast.
func jsonbConcat(l, r string) (Datum, error) {
	lv, err := parseJSONBText(l)
	if err != nil {
		return Datum{}, err
	}
	rv, err := parseJSONBText(r)
	if err != nil {
		return Datum{}, err
	}
	var out jsonbValue
	if lv.kind == jsonbObject && rv.kind == jsonbObject {
		out = jsonbValue{kind: jsonbObject, obj: append(append([]jsonbMember(nil), lv.obj...), rv.obj...)}
	} else {
		elems := func(v jsonbValue) []jsonbValue {
			if v.kind == jsonbArray {
				return v.arr
			}
			return []jsonbValue{v}
		}
		out = jsonbValue{kind: jsonbArray, arr: append(append([]jsonbValue(nil), elems(lv)...), elems(rv)...)}
	}
	var b strings.Builder
	if err := appendJSONBCanonical(&b, out); err != nil {
		return Datum{}, invalidJSONBError()
	}
	return NewStringDatum(b.String()), nil
}

// parseJSONBText parses s as exactly one jsonb value.
func parseJSONBText(s string) (jsonbValue, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	v, err := parseJSONBValue(dec)
	if err != nil {
		return jsonbValue{}, invalidJSONBError()
	}
	if _, err := dec.Token(); err != io.EOF {
		return jsonbValue{}, invalidJSONBError()
	}
	return v, nil
}
