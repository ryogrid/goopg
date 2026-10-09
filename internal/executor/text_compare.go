package executor

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// Character-string comparison by static type (M0146-0053).
//
// Records, arrays and tids travel through the executor as text Datums, so
// compareDatum cannot tell `'(999,9)'::text` from the record `(999,9)`. Its
// KindString arm therefore guesses from the value's shape: two values that
// both start with `(` compare element-wise as rows, `{` as arrays, and
// pg_lsn- and uuid-shaped values by their own rules. For a real text value
// that guess is wrong — PG compares text with bttextcmp / varstr_cmp
// (src/backend/utils/adt/varlena.c), byte order under the C collation:
// `'(999,9)'::text > '(1241,10)'::text` is true, and `'(1,2)'::text =
// '(01,2)'::text` is false.
//
// PG picks the comparison function from the operand type at plan time. The
// sites below do the same: each knows the expression it compares, and when
// that expression's static type is a character-string type the values
// compare as plain strings. The type is consulted only when the values'
// shape would make compareDatum guess (textShapeAmbiguous), so the common
// path pays nothing. An expression whose type cannot be resolved keeps the
// guess, as before.

// isCharacterStringType reports whether t is a character-string type whose
// values compare as plain strings: text, varchar, bpchar (callers trim it
// first), name and "char".
func isCharacterStringType(t catalog.Type) bool {
	if t.IsArray {
		return false
	}
	switch strings.ToLower(t.Name) {
	case "text", "varchar", "character varying", "bpchar", "character", "char", "name":
		return true
	}
	return false
}

// exprIsCharacterString reports whether e's static type is a character-string
// type.
func exprIsCharacterString(e optimizer.Expr) bool {
	if e == nil {
		return false
	}
	t, ok := optimizer.ExprResultType(e)
	return ok && isCharacterStringType(t)
}

// textShapeAmbiguous reports whether compareDatum would compare a and b by a
// value-shape rule rather than as plain strings: both are owned strings and
// both look like rows, both like arrays, both like pg_lsn values, or either
// like a uuid.
func textShapeAmbiguous(a, b Datum) bool {
	if a.Kind != KindString || b.Kind != KindString {
		return false
	}
	as, bs := a.StringValue(), b.StringValue()
	if len(as) > 0 && len(bs) > 0 && as[0] == bs[0] && (as[0] == '(' || as[0] == '{') {
		return true
	}
	if looksLikePgLSN(as) && looksLikePgLSN(bs) {
		return true
	}
	return isValidUUIDStr(as) || isValidUUIDStr(bs)
}

// compareText compares two string Datums as plain strings (bttextcmp under
// the C collation).
func compareText(a, b Datum) int {
	return strings.Compare(a.StringValue(), b.StringValue())
}

// compareDatumTyped is compareDatum for values of expression e: when e is a
// character string and the values' shape would make compareDatum guess, they
// compare as plain strings.
func compareDatumTyped(a, b Datum, pos int, e optimizer.Expr) (int, error) {
	if textShapeAmbiguous(a, b) && exprIsCharacterString(e) {
		return compareText(a, b), nil
	}
	return compareDatum(a, b, pos)
}

// compareDatumPlain is compareDatum with the character-string decision
// already made by the caller (plain = the compared expression is a
// character string).
func compareDatumPlain(a, b Datum, pos int, plain bool) (int, error) {
	if plain && textShapeAmbiguous(a, b) {
		return compareText(a, b), nil
	}
	return compareDatum(a, b, pos)
}

// isComparisonOpCode reports whether op is one of the six ordering
// comparison operators.
func isComparisonOpCode(op parser.OpCode) bool {
	switch op {
	case parser.OpEq, parser.OpLt, parser.OpGt, parser.OpLe, parser.OpGe, parser.OpNe:
		return true
	}
	return false
}

// binaryTextComparison evaluates a comparison of two character-string
// operands as text: the bpchar operand rule first (the same helper both
// BinaryOp twins apply before evalBinary), then a plain string comparison.
func binaryTextComparison(op parser.OpCode, left, right Datum, lbp, rbp int64, lLit, rLit bool) Datum {
	left, right = comparisonOperandsAsBpchar(op, left, right, lbp, rbp, lLit, rLit)
	return NewBoolDatum(cmpResult(op, compareText(left, right)))
}

// characterStringSchemaCols flags the character-string columns of s (nil
// when there are none).
func characterStringSchemaCols(s optimizer.Schema) []bool {
	var flags []bool
	for i, c := range s {
		if isCharacterStringType(c.Type) {
			if flags == nil {
				flags = make([]bool, len(s))
			}
			flags[i] = true
		}
	}
	return flags
}

// groupExprAt returns the aggregate's i'th GROUP BY expression, or nil.
func groupExprAt(p *optimizer.Aggregate, i int) optimizer.Expr {
	if p == nil || i < 0 || i >= len(p.GroupExprs) {
		return nil
	}
	return p.GroupExprs[i]
}

// sortKeyExprAt returns keys[i].Expr, or nil.
func sortKeyExprAt(keys []optimizer.SortKey, i int) optimizer.Expr {
	if i < 0 || i >= len(keys) {
		return nil
	}
	return keys[i].Expr
}
