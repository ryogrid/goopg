package optimizer

import "github.com/goopg/goopg/internal/parser"

// builtinRangeTypeNames are the six range types of pg_range.dat.
var builtinRangeTypeNames = map[string]bool{
	"int4range": true, "int8range": true, "numrange": true,
	"daterange": true, "tsrange": true, "tstzrange": true,
}

// RangeOperandType is the range type of an operand of a range operator, or "".
func RangeOperandType(e Expr) string {
	t := exprType(e)
	if t.IsArray {
		return ""
	}
	if n := commonTypeCanonical(t.Name); builtinRangeTypeNames[n] {
		return n
	}
	return ""
}

// rangeOpCall resolves an operator with a range operand to its pg_operator
// function (rangetypes.c), by static type as oper() does. An untyped literal
// beside a range takes the range's type, which is how PG resolves
// `r && '[1,5)'`.
func rangeOpCall(pos int, op parser.OpCode, l, r Expr) (Expr, bool) {
	lt, rt := RangeOperandType(l), RangeOperandType(r)
	if lt == "" && rt == "" {
		return nil, false
	}
	literal := func(e Expr, typ string) Expr {
		if sc, ok := e.(*StringConst); ok {
			// Explicit: parse analysis turns it into a Const of the range type,
			// which EXPLAIN prints with its type label.
			return &CastExpr{pos: sc.Pos(), Operand: sc, TargetType: typ, SourceType: "unknown", Explicit: true}
		}
		return e
	}
	_, lUntyped := l.(*StringConst)
	_, rUntyped := r.(*StringConst)
	if lt != "" && rUntyped {
		r, rt = literal(r, lt), lt
	} else if rt != "" && lUntyped {
		l, lt = literal(l, rt), rt
	}
	both := lt != "" && rt != ""
	fn, ret := "", "bool"
	switch op {
	case parser.OpContains:
		if lt == "" {
			return nil, false
		}
		fn = "range_contains_elem"
		if both {
			fn = "range_contains"
		}
	case parser.OpContainedBy:
		if rt == "" {
			return nil, false
		}
		fn = "elem_contained_by_range"
		if both {
			fn = "range_contained_by"
		}
	case parser.OpOverlap, parser.OpBitShiftLeft, parser.OpBitShiftRight, parser.OpOverLeft,
		parser.OpOverRight, parser.OpAdjacent, parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe:
		// = and <> stay ordinary operators: the canonical text already
		// compares equal for the discrete types, and joins key on them.
		if !both {
			return nil, false
		}
		fn = map[parser.OpCode]string{
			parser.OpOverlap: "range_overlaps", parser.OpBitShiftLeft: "range_before",
			parser.OpBitShiftRight: "range_after", parser.OpOverLeft: "range_overleft",
			parser.OpOverRight: "range_overright", parser.OpAdjacent: "range_adjacent",
			parser.OpLt: "range_lt",
			parser.OpLe: "range_le", parser.OpGt: "range_gt", parser.OpGe: "range_ge",
		}[op]
	case parser.OpAdd, parser.OpMul, parser.OpSub:
		if !both {
			return nil, false
		}
		fn = map[parser.OpCode]string{parser.OpAdd: "range_union", parser.OpMul: "range_intersect",
			parser.OpSub: "range_minus"}[op]
		ret = lt
	default:
		return nil, false
	}
	// The range type rides in the name: constant folding can turn a range
	// operand into an untyped string, and the executor needs the subtype.
	typ := lt
	if typ == "" {
		typ = rt
	}
	return &FuncCall{pos: pos, Name: fn + RangeOpTypeSep + typ, Args: []Expr{l, r}, ReturnType: ret, Operator: op.String()}, true
}

// RangeOpTypeSep separates a range operator function's name from the range
// type it was resolved for ("range_overlaps|int4range").
const RangeOpTypeSep = "|"
