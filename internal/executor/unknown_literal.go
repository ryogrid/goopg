package executor

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// Unknown-literal coercion, once per statement (M0122-0012 slice 43).
//
// PostgreSQL types a bare 'x' as unknown and coerces it to the other
// operand's type while the query is parsed (parse_oper.c oper() →
// parse_coerce.c coerce_type, which runs the type's input function on the
// literal text once), so `d_date = '2001-06-16'` compares date to date for
// every row. goopg keeps the literal a string (root-0019) and coerces it at
// comparison time: compareDatum's promoteCrossKind calls tryParseStringAs on
// the literal for EVERY row. For a date that is parseCopyTimestamp's layout
// loop, ~2.8 µs a row; a 73 049-row date_dim scan under TPC-DS Q83's
// `d_date IN ('2001-07-13', ...)` spent ~600 ms there.
//
// Both memos below rest on one identity: compareDatum(a, lit) begins with
// promoteCrossKind(a, lit), which for a non-string a and a string lit is
// (a, tryParseStringAs(a.Kind, lit)), and promoteCrossKind leaves a pair of
// equal kinds alone. So compareDatum(a, tryParseStringAs(a.Kind, lit)) is
// compareDatum(a, lit) for every a, and replacing the literal by its
// coerced value — computed once — changes no result. Each memo is used only
// when the opposite operand's RUNTIME kind is the kind it was coerced to;
// any other row takes the unchanged per-row path.

// unknownLitTargetKind is the Datum kind values of declared type t decode
// to, for the kinds tryParseStringAs can coerce a literal into. It only
// chooses which coercion to compute: the runtime kind test in
// coerceUnknownLit decides whether it is used, so a wrong answer here costs
// the speed-up, never a result.
func unknownLitTargetKind(t catalog.Type) (DatumKind, bool) {
	if t.IsArray {
		return 0, false
	}
	switch strings.ToLower(t.Name) {
	case "int2", "int4", "int8", "smallint", "integer", "int", "bigint", "serial", "bigserial", "smallserial":
		return KindInt, true
	case "numeric", "decimal":
		return KindNumeric, true
	case "date", "timestamp", "timestamptz", "timestamp without time zone", "timestamp with time zone":
		return KindTime, true
	case "interval":
		return KindInterval, true
	}
	return 0, false
}

// unknownLitCoercion pre-coerces the bare string literal of a compiled
// comparison. It declines unless every preamble branch of
// evalFastBinaryApply that could see the literal as a string is a no-op for
// the pair: a comparison operator, exactly one bare-literal side, no
// character-string pair (bit 4 of flags), no bpchar-typed side, a literal
// that does not look like a pg_lsn. A literal that does not parse as the
// target kind is not memoised: the per-row path then fails or falls back
// exactly as before.
func unknownLitCoercion(t *optimizer.BinaryOp, flags uint8) (Datum, DatumKind, bool) {
	if !isComparisonOpCode(t.Op) || flags&4 != 0 || flags&3 == 0 || flags&3 == 3 {
		return Datum{}, 0, false
	}
	if declaredBpcharTypmod(t.Left) != 0 || declaredBpcharTypmod(t.Right) != 0 {
		return Datum{}, 0, false
	}
	litExpr, other := t.Left, t.Right
	if flags&2 != 0 {
		litExpr, other = t.Right, t.Left
	}
	lit := litExpr.(*optimizer.StringConst).Value
	if looksLikePgLSN(lit) {
		return Datum{}, 0, false
	}
	ty, ok := optimizer.ExprResultType(other)
	if !ok {
		return Datum{}, 0, false
	}
	kind, ok := unknownLitTargetKind(ty)
	if !ok {
		return Datum{}, 0, false
	}
	d := tryParseStringAs(kind, lit)
	if d.Kind != kind {
		return Datum{}, 0, false
	}
	return d, kind, true
}

// coerceUnknownLit replaces a compiled comparison's literal operand by its
// pre-coerced value (ExprNode.constVal, kind payload[17]) when the opposite
// operand is of that kind, reporting whether it did.
func coerceUnknownLit(n *ExprNode, left, right *Datum) bool {
	k := DatumKind(n.payload[17])
	if n.payload[16]&2 != 0 {
		if right.Kind == KindString && left.Kind == k {
			*right = *n.constVal
			return true
		}
		return false
	}
	if left.Kind == KindString && right.Kind == k {
		*left = *n.constVal
		return true
	}
	return false
}

// coercedItems returns a literal IN list's items coerced to kind k, for the
// plain-equality loop of evalInExprScalarRow: entry i is valid when its Kind
// is k, and the zero Datum where the item is not a string literal or does
// not parse. Only the kinds whose compareEq arm is compareDatum itself are
// served — numeric (its own arm) and date/timestamp/interval (the cross-kind
// fallback); compareEq compares an integer to a string as text, so KindInt
// is not. The item is bpchar-normalised first exactly as the loop does
// (comparisonOperandsAsBpchar never touches the non-string operand, so a
// NULL stands in for it). The list is per statement and private to its
// Context, so the cache is filled lazily.
func (c *inListConst) coercedItems(k DatumKind, opBP int64, opLit bool) []Datum {
	if c == nil {
		return nil
	}
	switch k {
	case KindTime, KindInterval, KindNumeric:
	default:
		return nil
	}
	if c.coerced != nil && c.coercedKind == k {
		return c.coerced
	}
	out := make([]Datum, len(c.values))
	for i, v := range c.values {
		if v.IsNull() {
			continue
		}
		_, vv := comparisonOperandsAsBpchar(parser.OpEq, NullDatum, v, opBP, c.itemBP[i], opLit, c.itemLit[i])
		if vv.Kind != KindString {
			continue
		}
		if d := tryParseStringAs(k, vv.StringValue()); d.Kind == k {
			out[i] = d
		}
	}
	c.coerced, c.coercedKind = out, k
	return out
}
