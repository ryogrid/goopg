package executor

// record_in for composite (row) types (postgres/src/backend/utils/adt/
// rowtypes.c): a value cast or assigned to a composite type has each field
// run through its field type's input function and the record re-rendered by
// record_out. goopg carried a composite as the text it was built from, so
// ROW(1.0, 'a', 1) into a (float8, text, int4) type stayed "(1.0,a,1)" where
// PG stores and prints (1,a,1), and an array field kept its input spelling
// ("{a, b, c}" for PG's {a,b,c}). subscription/t/002_types.pl compares
// exactly those texts (M0149-0046c).

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/utils/adt/array"
)

// compositeCastTarget reports the field columns when targetType names a
// composite type in ctx's catalog. A built-in type name answers false
// without a catalog lookup.
func compositeCastTarget(ctx *Context, targetType string) ([]catalog.Column, bool) {
	if ctx == nil || targetType == "" {
		return nil, false
	}
	base := compositeFieldBaseTypeName(targetType)
	// TypeNameToOID falls back to text for a name it does not know, so a
	// built-in is a name mapping elsewhere (or text itself).
	if oid := catalog.TypeNameToOID(base); oid != catalog.OIDText || base == "text" {
		return nil, false
	}
	return compositeKeyFields(ctx, base)
}

// recordIn normalises a composite value's text s against the type's fields
// (record_in + record_out). A field count other than the type's is PG's
// 22P02 "malformed record literal" with "Too few columns." / "Too many
// columns.".
func recordIn(ctx *Context, typeName, s string, fields []catalog.Column, pos int) (string, error) {
	vals, nulls, err := parseRecordText(s)
	if err != nil {
		return "", &ExecError{Code: "22P02", Pos: pos,
			Message: fmt.Sprintf("malformed record literal: %q", s), Detail: capitalizeDetail(err.Error())}
	}
	if len(vals) < len(fields) {
		return "", &ExecError{Code: "22P02", Pos: pos,
			Message: fmt.Sprintf("malformed record literal: %q", s), Detail: "Too few columns."}
	}
	if len(vals) > len(fields) {
		return "", &ExecError{Code: "22P02", Pos: pos,
			Message: fmt.Sprintf("malformed record literal: %q", s), Detail: "Too many columns."}
	}
	parts := make([]string, len(fields))
	for i := range fields {
		if nulls[i] {
			continue
		}
		d, err := typedFieldInput(ctx, fields[i].Type, vals[i], pos)
		if err != nil {
			return "", err
		}
		if !d.IsNull() {
			parts[i] = recordFieldText(d)
		}
	}
	return "(" + strings.Join(parts, ",") + ")", nil
}

// typedFieldInput is one field's (or array element's) input function: an
// array field re-renders element by element, a composite field recurses,
// everything else goes through evalCast to the field type.
func typedFieldInput(ctx *Context, typ catalog.Type, text string, pos int) (Datum, error) {
	if typ.IsArray {
		s, err := arrayInElems(ctx, typ.Name, text, pos)
		if err != nil {
			return Datum{}, err
		}
		return NewStringDatum(s), nil
	}
	if fields, ok := compositeCastTarget(ctx, typ.Name); ok {
		s, err := recordIn(ctx, typ.Name, text, fields, pos)
		if err != nil {
			return Datum{}, err
		}
		return NewStringDatum(s), nil
	}
	return evalCast(NewStringDatum(text), typ.Name, pos, ctx)
}

// arrayInElems is array_in + array_out for a one-dimensional array literal
// whose elements are elemType: each non-NULL element through the element's
// input, re-rendered with array_out's quoting; the unquoted NULL token stays
// NULL. A multidimensional literal is returned unchanged (goopg's array
// codec stores one dimension only).
func arrayInElems(ctx *Context, elemType, text string, pos int) (string, error) {
	s := strings.TrimSpace(text)
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' || arrayLiteralHasNestedBrace(s[1:len(s)-1]) {
		return text, nil
	}
	delim := array.ElemDelim(elemType)
	elems := parseTextArrayElemsDelim(s, delim)
	var b strings.Builder
	b.WriteByte('{')
	for i, e := range elems {
		if i > 0 {
			b.WriteByte(delim)
		}
		if !e.Quoted && strings.EqualFold(strings.TrimSpace(e.Text), "NULL") {
			b.WriteString("NULL")
			continue
		}
		d, err := typedFieldInput(ctx, catalog.Type{Name: elemType}, e.Text, pos)
		if err != nil {
			return "", err
		}
		if d.IsNull() {
			b.WriteString("NULL")
			continue
		}
		b.WriteString(quoteArrayTextElem(string(d.AppendValueText(nil))))
	}
	b.WriteByte('}')
	return b.String(), nil
}

// capitalizeDetail turns the record tokenizer's lower-case reason into PG's
// DETAIL spelling ("Missing left parenthesis.").
func capitalizeDetail(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:] + "."
}

// rangeColumnType reports whether typeName is a range type (a built-in or a
// CREATE TYPE … AS RANGE type), for range_in at assignment and in array
// casts (M0149-0046d). Built-in scalar names answer false without a catalog
// lookup.
func rangeColumnType(ctx *Context, typeName string) bool {
	lower := strings.ToLower(strings.TrimSpace(typeName))
	if _, ok := builtinRangeTypes[lower]; ok {
		return true
	}
	if oid := catalog.TypeNameToOID(lower); oid != catalog.OIDText || lower == "text" {
		return false
	}
	_, ok := lookupRangeTypeForIO(lower, ctx)
	return ok
}

// evalArrayAssignSubscript is array_set_element for `UPDATE … SET col[i] =
// v` (M0149-0046i): the old array with element i replaced, padded with
// NULLs when i is past the end. Elements keep their array_out quoting (a
// composite element stays "(…)"), a NULL v stores NULL, and a NULL array
// assigned at i builds a fresh one. PG's out-of-range rules: a subscript
// below 1 on a non-empty array is 2202E "array subscript out of range"
// (goopg stores lower bound 1 only), and growing past MaxArraySize is
// 54000.
func evalArrayAssignSubscript(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) != 3 {
		return NullDatum, &ExecError{Code: "XX000", Pos: x.Pos(), Message: "malformed array element assignment"}
	}
	arr, err := evalExprSlot(x.Args[0], slot, ctx)
	if err != nil {
		return NullDatum, err
	}
	subD, err := evalExprSlot(x.Args[1], slot, ctx)
	if err != nil {
		return NullDatum, err
	}
	val, err := evalExprSlot(x.Args[2], slot, ctx)
	if err != nil {
		return NullDatum, err
	}
	if subD.IsNull() {
		return NullDatum, &ExecError{Code: "22004", Pos: x.Pos(), Message: "array subscript in assignment must not be null"}
	}
	if subD.Kind == KindString {
		subD = tryParseStringAs(KindInt, subD.StringValue())
	}
	if subD.Kind != KindInt {
		return NullDatum, &ExecError{Code: "42804", Pos: x.Pos(), Message: "array subscript must have type integer"}
	}
	elemType, _ := strings.CutSuffix(x.ReturnType, "[]")
	var elems []string
	if !arr.IsNull() {
		for _, e := range parseTextArrayElemsDelim(arr.StringValue(), array.ElemDelim(elemType)) {
			if !e.Quoted && strings.EqualFold(strings.TrimSpace(e.Text), "NULL") {
				elems = append(elems, "NULL")
			} else {
				elems = append(elems, quoteArrayTextElem(e.Text))
			}
		}
	}
	const maxArraySize = 134217727
	i := subD.Int
	if i < 1 {
		if len(elems) > 0 || i < -maxArraySize {
			return NullDatum, &ExecError{Code: "2202E", Pos: x.Pos(), Message: "array subscript out of range"}
		}
		return NullDatum, &ExecError{Code: "0A000", Pos: x.Pos(),
			Message: "array lower bounds other than 1 are not supported"}
	}
	if i > maxArraySize {
		return NullDatum, &ExecError{Code: "54000", Pos: x.Pos(),
			Message: fmt.Sprintf("array size exceeds the maximum allowed (%d)", maxArraySize)}
	}
	for int64(len(elems)) < i {
		elems = append(elems, "NULL")
	}
	if val.IsNull() {
		elems[i-1] = "NULL"
	} else {
		d, err := typedFieldInput(ctx, catalog.Type{Name: elemType}, string(val.AppendValueText(nil)), x.Pos())
		if err != nil {
			return NullDatum, err
		}
		elems[i-1] = quoteArrayTextElem(string(d.AppendValueText(nil)))
	}
	delim := string(array.ElemDelim(elemType))
	return NewStringDatum("{" + strings.Join(elems, delim) + "}"), nil
}
