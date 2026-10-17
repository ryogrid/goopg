package executor

import (
	"encoding/json"
	"fmt"
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
	// concatArrayCat / concatArrayAppend / concatArrayPrepend: array_cat,
	// array_append, array_prepend (array_userfuncs.c). None is strict: a
	// NULL array operand acts as an empty array and a NULL element is
	// appended as a NULL element (M0146-0125).
	concatArrayCat
	concatArrayAppend
	concatArrayPrepend
)

// concatModeOf is the `||` operator PG resolves for operands l and r. An
// untyped string literal next to jsonb resolves to jsonb, as an unknown
// literal adopts the other operand's type; jsonb next to a typed character
// string is text concatenation.
func concatModeOf(l, r optimizer.Expr) concatMode {
	lt, lok := optimizer.ExprResultType(l)
	rt, rok := optimizer.ExprResultType(r)
	// Array operands (M0146-0125). An untyped NULL or string literal beside
	// an array adopts the array type, so it is array_cat (a literal that is
	// not an array is then malformed array input); a typed non-array operand
	// is the element of array_append / array_prepend. A polymorphic or
	// unresolved operand keeps the shape-based path.
	la, ra := lok && isConcreteArrayType(lt), rok && isConcreteArrayType(rt)
	switch {
	case la && (ra || isBareNullExpr(r) || isBareStringLit(r)):
		return concatArrayCat
	case ra && (isBareNullExpr(l) || isBareStringLit(l)):
		return concatArrayCat
	case la && rok && !isArrayOrPolymorphicType(rt):
		return concatArrayAppend
	case ra && lok && !isArrayOrPolymorphicType(lt):
		return concatArrayPrepend
	}
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

// isConcreteArrayType is an array type that is not a polymorphic
// pseudo-type (anyarray, anycompatiblearray).
func isConcreteArrayType(t catalog.Type) bool {
	return isArrayOrPolymorphicType(t) && !strings.HasPrefix(strings.ToLower(t.Name), "any")
}

// isBareNullExpr is an untyped NULL literal.
func isBareNullExpr(e optimizer.Expr) bool {
	_, ok := e.(*optimizer.NullConst)
	return ok
}

// arrayTextDims is an array literal's dimension count: its leading braces.
// "{}" has none.
func arrayTextDims(s string) int {
	if s == "{}" {
		return 0
	}
	n := 0
	for n < len(s) && s[n] == '{' {
		n++
	}
	return n
}

// isArrayText reports a value in array-literal form.
func isArrayText(s string) bool {
	return len(s) >= 2 && s[0] == '{' && s[len(s)-1] == '}'
}

// arrayElementText renders one element for array_out: NULL as the bare token
// NULL, and a value quoted when it is empty, spells NULL, or contains a quote,
// backslash, brace, comma or whitespace.
func arrayElementText(d Datum, ctx *Context) string {
	if d.IsNull() {
		return "NULL"
	}
	s := formatDatumDateStyle(d, ctx)
	needsQuote := s == "" || strings.EqualFold(s, "NULL")
	for _, c := range s {
		if c == '"' || c == '\\' || c == '{' || c == '}' || c == ',' || c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			needsQuote = true
			break
		}
	}
	if !needsQuote {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range s {
		if c == '"' || c == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
	b.WriteByte('"')
	return b.String()
}

// evalArrayConcat is array_cat / array_append / array_prepend over the
// text form goopg carries arrays in. array_cat returns NULL only when both
// operands are NULL, and otherwise treats a NULL or empty operand as absent.
// Equal dimension counts concatenate along the first dimension; arrays one
// dimension apart concatenate the smaller as one element of the larger.
func evalArrayConcat(left, right Datum, mode concatMode, ctx *Context) (Datum, error) {
	switch mode {
	case concatArrayAppend, concatArrayPrepend:
		arr, elem := left, right
		if mode == concatArrayPrepend {
			arr, elem = right, left
		}
		inner := ""
		if !arr.IsNull() {
			s := formatDatumDateStyle(arr, ctx)
			if isArrayText(s) {
				inner = s[1 : len(s)-1]
			}
		}
		e := arrayElementText(elem, ctx)
		switch {
		case inner == "":
			return NewStringDatum("{" + e + "}"), nil
		case mode == concatArrayAppend:
			return NewStringDatum("{" + inner + "," + e + "}"), nil
		default:
			return NewStringDatum("{" + e + "," + inner + "}"), nil
		}
	}
	if left.IsNull() && right.IsNull() {
		return NullDatum, nil
	}
	if left.IsNull() {
		return right, nil
	}
	if right.IsNull() {
		return left, nil
	}
	ls, rs := formatDatumDateStyle(left, ctx), formatDatumDateStyle(right, ctx)
	for _, v := range []string{ls, rs} {
		if !isArrayText(v) {
			// array_in on an untyped literal that is not an array.
			return Datum{}, &ExecError{Code: "22P02", Message: fmt.Sprintf("malformed array literal: %q", v),
				Detail: `Array value must start with "{" or dimension information.`}
		}
	}
	ld, rd := arrayTextDims(ls), arrayTextDims(rs)
	switch {
	case ld == 0:
		return right, nil
	case rd == 0:
		return left, nil
	case ld == rd:
		return NewStringDatum("{" + ls[1:len(ls)-1] + "," + rs[1:len(rs)-1] + "}"), nil
	case ld+1 == rd:
		return NewStringDatum("{" + ls + "," + rs[1:len(rs)-1] + "}"), nil
	case ld == rd+1:
		return NewStringDatum("{" + ls[1:len(ls)-1] + "," + rs + "}"), nil
	}
	return Datum{}, &ExecError{Code: "2202E", Message: "cannot concatenate incompatible arrays",
		Detail: fmt.Sprintf("Arrays of %d and %d dimensions are not compatible for concatenation.", ld, rd)}
}
