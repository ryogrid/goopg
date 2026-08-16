package nodes

// rebuild.go — M0123-S2: the inverse of resolver_expr.go. On startup the
// per-database heap reload reads a stored pg_attrdef.adbin (a canonical
// pg_node_tree, discriminated by a leading '{') with Read, then Rebuild turns
// the IR back into a goopg parser.Expr so goopg can re-evaluate the DEFAULT the
// same way it did before the restart. (SQL-text adbin values keep going through
// parser.ParseExpr; the discriminator lives in the reload, not here.)
//
// Rebuild handles exactly the node shapes resolver_expr.go emits this sub-slice
// (int4/int8/text Const, negative-int Const → unary minus, OpExpr → BinaryOp);
// anything else is a reader/producer mismatch and returns an error so the
// reload can surface it rather than silently drop a default.

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"sync"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// Rebuild converts a canonical scalar IR node back into a goopg default-
// expression AST. It is the reload-time inverse of ResolveExpr.
func Rebuild(n Node) (parser.Expr, error) {
	switch v := n.(type) {
	case *Const:
		return rebuildConst(v)
	case *OpExpr:
		return rebuildOpExpr(v)
	case *DistinctExpr:
		return rebuildDistinctExpr(v)
	case *FuncExpr:
		return rebuildFuncExpr(v)
	case *RelabelType:
		return rebuildRelabelType(v, Rebuild)
	case *BoolExpr:
		return rebuildBoolExpr(v)
	case *NullTest:
		return rebuildNullTest(v)
	case *BooleanTest:
		return rebuildBooleanTest(v)
	case *CaseExpr:
		return rebuildCaseExpr(v)
	default:
		return nil, fmt.Errorf("pgnodes: Rebuild: unsupported node %T", n)
	}
}

// rebuildBoolExpr reconstructs an AND/OR/NOT BOOLEXPR into goopg's AST. goopg has
// no n-ary boolean node, so an n-arg AND/OR folds back into a LEFT-nested chain
// of BinaryOps (`[a b c]` → `(a AND b) AND c`) — the exact tree PG's makeAndExpr
// flattened on the way in, so resolve→Rebuild→re-resolve is a fixed point. NOT
// is a single-operand UnaryOp.
func rebuildBoolExpr(b *BoolExpr) (parser.Expr, error) {
	return rebuildBoolExprWith(b, Rebuild)
}

// rebuildBoolExprWith rebuilds a BOOLEXPR through the injected recursion `rec`.
// NOT -> a single UnaryOp{OpNot}; AND/OR -> a left-nested chain of BinaryOps
// (the inverse of the forward flattening: an n-arg BoolExpr becomes
// `((a op b) op c) …`). The query-scoped reload threads
// viewRebuildScope.rebuildExpr so a bool operand may itself be a column Var.
func rebuildBoolExprWith(b *BoolExpr, rec func(Node) (parser.Expr, error)) (parser.Expr, error) {
	switch b.Boolop {
	case BoolExprNot:
		if len(b.Args) != 1 {
			return nil, fmt.Errorf("pgnodes: Rebuild: NOT BoolExpr with %d args (want 1)", len(b.Args))
		}
		operand, err := rec(b.Args[0])
		if err != nil {
			return nil, err
		}
		return &parser.UnaryOp{Op: parser.OpNot, Operand: operand}, nil
	case BoolExprAnd, BoolExprOr:
		op := parser.OpAnd
		if b.Boolop == BoolExprOr {
			op = parser.OpOr
		}
		if len(b.Args) < 2 {
			return nil, fmt.Errorf("pgnodes: Rebuild: AND/OR BoolExpr with %d args (want >=2)", len(b.Args))
		}
		acc, err := rec(b.Args[0])
		if err != nil {
			return nil, err
		}
		for _, a := range b.Args[1:] {
			r, err := rec(a)
			if err != nil {
				return nil, err
			}
			acc = &parser.BinaryOp{Op: op, Left: acc, Right: r}
		}
		return acc, nil
	default:
		return nil, fmt.Errorf("pgnodes: Rebuild: bad boolop %d", b.Boolop)
	}
}

// rebuildNullTest reconstructs `x IS [NOT] NULL`.
func rebuildNullTest(nt *NullTest) (parser.Expr, error) {
	return rebuildNullTestWith(nt, Rebuild)
}

// rebuildNullTestWith rebuilds a NULLTEST through the injected recursion `rec`
// (so the argument may be a column Var in the view-reload path).
func rebuildNullTestWith(nt *NullTest, rec func(Node) (parser.Expr, error)) (parser.Expr, error) {
	operand, err := rec(nt.Arg)
	if err != nil {
		return nil, err
	}
	return &parser.IsNullExpr{Operand: operand, Negated: nt.NullTestType == IsNotNull}, nil
}

// rebuildBooleanTest reconstructs `x IS [NOT] TRUE/FALSE/UNKNOWN`.
func rebuildBooleanTest(bt *BooleanTest) (parser.Expr, error) {
	return rebuildBooleanTestWith(bt, Rebuild)
}

// rebuildBooleanTestWith rebuilds a BOOLEANTEST through the injected recursion
// `rec` (so the argument may be a column Var in the view-reload path). The
// booltesttype ordinal is the exact inverse of resolver_expr.go's
// booleanTestType, so resolve→Rebuild→re-resolve is a fixed point.
func rebuildBooleanTestWith(bt *BooleanTest, rec func(Node) (parser.Expr, error)) (parser.Expr, error) {
	operand, err := rec(bt.Arg)
	if err != nil {
		return nil, err
	}
	out := &parser.IsBoolExpr{Operand: operand}
	switch bt.BoolTestType {
	case IsTrue:
		out.TestTrue = true
	case IsNotTrue:
		out.TestTrue, out.Negated = true, true
	case IsFalse:
		out.TestFalse = true
	case IsNotFalse:
		out.TestFalse, out.Negated = true, true
	case IsUnknown:
		// neither TestTrue nor TestFalse; not negated
	case IsNotUnknown:
		out.Negated = true
	default:
		return nil, fmt.Errorf("pgnodes: Rebuild: bad booltesttype %d", bt.BoolTestType)
	}
	return out, nil
}

// rebuildCaseExpr rebuilds a scalar CaseExpr into a parser.CaseExpr.
func rebuildCaseExpr(c *CaseExpr) (parser.Expr, error) {
	return rebuildCaseExprWith(c, Rebuild)
}

// rebuildCaseExprWith is the inverse of resolveCaseExprWith: it rebuilds a
// CaseExpr into a parser.CaseExpr, threading the injected recursion so a CASE
// inside a view qual rebuilds its Var operands. A NULL Const Defresult is the
// synthesized "no ELSE" default (transformCaseExpr), so it rebuilds back to an
// omitted ELSE — a re-resolve re-synthesizes identical bytes (the fixed point).
//
// For the simple form (Arg != nil) it mirrors ruleutils get_rule_expr: the
// operand rebuilds to Operand, and each WHEN condition is the OpExpr
// `placeholder = val`, so only its RHS (the second arg) is emitted as the
// parser WHEN value — the placeholder itself never surfaces in SQL text.
func rebuildCaseExprWith(c *CaseExpr, rec func(Node) (parser.Expr, error)) (parser.Expr, error) {
	out := &parser.CaseExpr{}
	if c.Arg != nil {
		operand, err := rec(c.Arg)
		if err != nil {
			return nil, err
		}
		out.Operand = operand
	}
	for _, a := range c.Args {
		w, ok := a.(*CaseWhen)
		if !ok {
			return nil, fmt.Errorf("pgnodes: Rebuild: CASEEXPR arg is %T, want *CaseWhen", a)
		}
		cond, err := rebuildCaseWhenCond(w.Expr, c.Arg != nil, rec)
		if err != nil {
			return nil, err
		}
		res, err := rec(w.Result)
		if err != nil {
			return nil, err
		}
		out.Whens = append(out.Whens, parser.CaseWhen{When: cond, Then: res})
	}
	if cst, ok := c.Defresult.(*Const); ok && cst.ConstIsNull {
		out.Else = nil
	} else {
		el, err := rec(c.Defresult)
		if err != nil {
			return nil, err
		}
		out.Else = el
	}
	return out, nil
}

// rebuildCaseWhenCond rebuilds one WHEN condition. In the searched form
// (!simple) the stored node is the boolean condition itself. In the simple form
// it is the OpExpr `placeholder = val`; ruleutils deparses just the RHS, so this
// unwraps the OpExpr and rebuilds its second arg (the WHEN value). The left arg
// is the CaseTestExpr placeholder and is intentionally dropped — the operand is
// rebuilt separately into CaseExpr.Operand.
func rebuildCaseWhenCond(cond Node, simple bool, rec func(Node) (parser.Expr, error)) (parser.Expr, error) {
	if !simple {
		return rec(cond)
	}
	op, ok := cond.(*OpExpr)
	if !ok {
		return nil, fmt.Errorf("pgnodes: Rebuild: simple-form CASE WHEN is %T, want *OpExpr", cond)
	}
	if len(op.Args) != 2 {
		return nil, fmt.Errorf("pgnodes: Rebuild: simple-form CASE WHEN OpExpr has %d args, want 2", len(op.Args))
	}
	if _, ok := op.Args[0].(*CaseTestExpr); !ok {
		return nil, fmt.Errorf("pgnodes: Rebuild: simple-form CASE WHEN left arg is %T, want *CaseTestExpr", op.Args[0])
	}
	return rec(op.Args[1])
}

// rebuildFuncExpr reconstructs a plain function call whose arguments are scalar
// nodes (the S2 DEFAULT-expression scope, where Rebuild is the recursion).
func rebuildFuncExpr(f *FuncExpr) (parser.Expr, error) {
	return rebuildFuncExprWith(f, Rebuild)
}

// rebuildFuncExprWith reconstructs a plain function call, rebuilding each
// argument through the caller-supplied recursion. The funcid is reverse-mapped
// to its proname (catalog.RegprocName, the same PG18 seed the forward resolver
// used) and the call is emitted unqualified (proname only): goopg resolves
// built-ins by bare name, and the forward resolver accepts an empty schema, so
// resolve→Rebuild→re-resolve is a fixed point. The recursion parameter lets the
// query-tree scope (rebuild_query.go) reuse this to rebuild FuncExprs whose
// arguments may be column Vars, not only scalars.
func rebuildFuncExprWith(f *FuncExpr, rec func(Node) (parser.Expr, error)) (parser.Expr, error) {
	// An implicit int->numeric coercion (int4_numeric/int8_numeric emitted by
	// coerce_to_target_type) has no SQL call syntax: PG re-derives it from the
	// bare integer literal in a numeric context. Rebuild to the inner argument so
	// a re-resolve re-wraps the identical FuncExpr (fixed point), rather than a
	// spurious numeric(<int>) function call.
	if isImplicitIntToNumericCast(f) || isImplicitInt4ToInt8Cast(f) ||
		isImplicitIntToInt2Cast(f) ||
		isImplicitFloat4ToFloat8Cast(f) || isImplicitToFloat8Cast(f) || isImplicitToFloat4Cast(f) ||
		isImplicitNumericLengthCoercion(f) ||
			isImplicitVarcharLengthCoercion(f) || isImplicitBpcharLengthCoercion(f) ||
			isImplicitTimestampLengthCoercion(f) ||
			isImplicitTimeLengthCoercion(f) ||
			isImplicitTimestamptzLengthCoercion(f) ||
			isImplicitTimeTZLengthCoercion(f) ||
			isImplicitBitLengthCoercion(f) || isImplicitVarBitLengthCoercion(f) {
		return rec(f.Args[0])
	}
	// An EXPLICIT `::numeric(p,s)` length coercion (numeric(numeric,int4) = funcid
	// 1703, funcformat 1) rebuilds to a typmod-qualified CastExpr. Args[0] is the
	// operand coerced to numeric (its own implicit int→numeric wrap, if any, unwraps
	// via rec); Args[1] is the int4 typmod Const, decoded back to (p,s). Re-resolving
	// `inner::numeric(p,s)` re-emits the identical FuncExpr (fixed point). Placed
	// before explicitCastTypeName because 1703 is not in that single-arg table.
	if p, s, ok := numericTypmodCastPS(f); ok {
		inner, err := rec(f.Args[0])
		if err != nil {
			return nil, err
		}
		return &parser.CastExpr{
			Operand: inner,
			Type:    parser.ObjectName{Name: "numeric"},
			Typmods: []int64{p, s},
		}, nil
	}
	// An EXPLICIT numeric-family cast (funcformat 1) keeps its `::type` node in
	// adbin, so — unlike the implicit casts above — it rebuilds to a CastExpr, not
	// the bare argument. Re-resolving `inner::type` re-emits the identical
	// funcformat-1 FuncExpr (fixed point). The funcformat==1 guard distinguishes it
	// from the implicit int4→int8 (481) / int→numeric (1740/1781, funcformat 2)
	// forms handled just above.
	if typeName, ok := explicitCastTypeName(f); ok {
		inner, err := rec(f.Args[0])
		if err != nil {
			return nil, err
		}
		return &parser.CastExpr{Operand: inner, Type: parser.ObjectName{Name: typeName}}, nil
	}
	name, ok := catalog.RegprocName(f.Funcid)
	if !ok {
		return nil, fmt.Errorf("pgnodes: Rebuild: unknown function OID %d", f.Funcid)
	}
	args := make([]parser.Expr, 0, len(f.Args))
	for _, a := range f.Args {
		arg, err := rec(a)
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
	}
	return &parser.FuncCall{Name: parser.ObjectName{Name: name}, Args: args}, nil
}

// rebuildRelabelType reconstructs a RELABELTYPE. The forward path emits two forms,
// both stripping a typmod'd numeric to bare `numeric` (typmod -1) via
// wrapNumericRelabelToBare, distinguished by relabelformat:
//
//   - relabelformat 2 (IMPLICIT, sub-slice 24): a bare `numeric` COLUMN whose stored
//     DEFAULT carries a typmod. Like the implicit casts in rebuildFuncExprWith it has
//     no SQL syntax — pg_get_expr renders only the inner expression — so it rebuilds
//     to its argument. Re-resolving that argument (the `::numeric(p,s)` cast) in the
//     same bare numeric column context re-wraps the identical RelabelType.
//   - relabelformat 1 (EXPLICIT, sub-slice 25): an explicit `(inner)::numeric` cast of
//     a typmod'd numeric operand. pg_get_expr renders the visible `::numeric` syntax,
//     so it rebuilds to a bare (no-typmod) `::numeric` CastExpr. Re-resolving
//     `inner::numeric` re-emits the identical relabelformat-1 RelabelType.
//
// Either way the rebuild is a fixed point. Any other relabelformat is not emitted by
// the forward path and is rejected rather than silently dropped.
func rebuildRelabelType(r *RelabelType, rec func(Node) (parser.Expr, error)) (parser.Expr, error) {
	switch r.Relabelformat {
	case 2:
		return rec(r.Arg)
	case 1:
		inner, err := rec(r.Arg)
		if err != nil {
			return nil, err
		}
		return &parser.CastExpr{Operand: inner, Type: parser.ObjectName{Name: "numeric"}}, nil
	default:
		return nil, fmt.Errorf("pgnodes: Rebuild: unsupported RelabelType relabelformat %d", r.Relabelformat)
	}
}

// numericCastPackedTypmod reports whether f is the explicit `::numeric(p,s)` length
// coercion the forward path emits (resolveNumericTypmodCast) — numeric(numeric,
// int4) = funcid 1703, funcformat 1 (EXPLICIT), a numeric result, and exactly two
// args whose second is a non-null int4 typmod Const — and returns that Const's
// packed atttypmod. ResolveForColumnTypmod uses it to gate on a column-typmod match.
func numericCastPackedTypmod(f *FuncExpr) (int32, bool) {
	if f.Funcid != 1703 || f.Funcformat != 1 || f.Funcresulttype != OidNumeric || len(f.Args) != 2 {
		return 0, false
	}
	tc, isConst := f.Args[1].(*Const)
	if !isConst || tc.ConstType != OidInt4 || tc.ConstIsNull || len(tc.Datum) < 8 {
		return 0, false
	}
	return int32(int64FromByvalWord(tc.Datum)), true
}

// numericTypmodCastPS decodes a `::numeric(p,s)` cast's precision and scale by
// inverting numerictypmodin: t = typmod - VARHDRSZ(4), p = t >> 16, s = t & 0x7ff.
// Returns false for any FuncExpr numericCastPackedTypmod rejects, or a typmod below
// VARHDRSZ (never emitted by the forward path).
func numericTypmodCastPS(f *FuncExpr) (p, s int64, ok bool) {
	packed, isCast := numericCastPackedTypmod(f)
	if !isCast {
		return 0, 0, false
	}
	t := int64(packed) - 4
	if t < 0 {
		return 0, 0, false
	}
	return t >> 16, t & 0x7ff, true
}

// isImplicitNumericLengthCoercion reports whether f is the IMPLICIT numeric length
// coercion coerce_type_typmod adds when a numeric column's typmod differs from the
// stored default's (see wrapNumericLengthCoercion): numeric(numeric, int4) = funcid
// 1703, funcformat 2 (IMPLICIT), a numeric result, and two args whose second is a
// non-null int4 typmod Const. pg_get_expr renders it invisibly (an implicit cast has
// no `::type` syntax), so rebuild unwraps to Args[0]; a re-resolve through
// ResolveForColumnTypmod re-wraps the identical node (fixed point). The funcformat==2
// guard separates it from the EXPLICIT `::numeric(p,s)` cast (funcformat 1, same
// funcid 1703) that numericCastPackedTypmod/numericTypmodCastPS rebuild to a CastExpr.
func isImplicitNumericLengthCoercion(f *FuncExpr) bool {
	if f.Funcid != 1703 || f.Funcformat != 2 || f.Funcresulttype != OidNumeric || len(f.Args) != 2 {
		return false
	}
	tc, isConst := f.Args[1].(*Const)
	return isConst && tc.ConstType == OidInt4 && !tc.ConstIsNull
}

// isImplicitVarcharLengthCoercion reports whether f is the IMPLICIT varchar length
// coercion coerce_type_typmod adds when a varchar(N) column has a length qualifier
// (see wrapVarcharLengthCoercion): varchar(varchar,int4,bool) = funcid 669,
// funcformat 2 (IMPLICIT), a varchar result, and three args whose second is a non-null
// int4 typmod Const and third a bool isExplicit Const. pg_get_expr renders the implicit
// form invisibly, so rebuild unwraps to Args[0]; a re-resolve through
// ResolveForColumnTypmod re-wraps the identical node (fixed point).
func isImplicitVarcharLengthCoercion(f *FuncExpr) bool {
	if f.Funcid != 669 || f.Funcformat != 2 || f.Funcresulttype != OidVarchar || len(f.Args) != 3 {
		return false
	}
	tc, isConst := f.Args[1].(*Const)
	return isConst && tc.ConstType == OidInt4 && !tc.ConstIsNull
}

// isImplicitBpcharLengthCoercion reports whether f is the IMPLICIT bpchar length
// coercion (see wrapBpcharLengthCoercion): bpchar(bpchar,int4,bool) = funcid 668,
// funcformat 2.
func isImplicitBpcharLengthCoercion(f *FuncExpr) bool {
	if f.Funcid != 668 || f.Funcformat != 2 || f.Funcresulttype != OidBpchar || len(f.Args) != 3 {
		return false
	}
	tc, isConst := f.Args[1].(*Const)
	return isConst && tc.ConstType == OidInt4 && !tc.ConstIsNull
}

// isImplicitTimestampLengthCoercion reports whether f is the IMPLICIT timestamp
// length coercion coerce_type_typmod adds when a timestamp(N) column has a precision
// qualifier (see wrapTimestampLengthCoercion): timestamp(timestamp,int4) = funcid
// 1961, funcformat 2 (IMPLICIT), a timestamp result, and two args whose second is a
// non-null int4 typmod Const. pg_get_expr renders the implicit form invisibly, so
// rebuild unwraps to Args[0]; a re-resolve through ResolveForColumnTypmod re-wraps
// the identical node (fixed point).
func isImplicitTimestampLengthCoercion(f *FuncExpr) bool {
	if f.Funcid != 1961 || f.Funcformat != 2 || f.Funcresulttype != OidTimestamp || len(f.Args) != 2 {
		return false
	}
	tc, isConst := f.Args[1].(*Const)
	return isConst && tc.ConstType == OidInt4 && !tc.ConstIsNull
}

// isImplicitTimeLengthCoercion reports whether f is the IMPLICIT time length
// coercion coerce_type_typmod adds when a time(N) column has a precision
// qualifier (see wrapTimeLengthCoercion): time(time,int4) = funcid 1968,
// funcformat 2 (COERCE_IMPLICIT_CAST). Like the other length-coercion wrappers,
// rebuild unwraps to Args[0]; a re-resolve through ResolveForColumnTypmod re-wraps
// the identical node (fixed point).
func isImplicitTimeLengthCoercion(f *FuncExpr) bool {
	if f.Funcid != 1968 || f.Funcformat != 2 || f.Funcresulttype != OidTime || len(f.Args) != 2 {
		return false
	}
	tc, isConst := f.Args[1].(*Const)
	return isConst && tc.ConstType == OidInt4 && !tc.ConstIsNull
}

// isImplicitTimestamptzLengthCoercion reports whether f is the IMPLICIT timestamptz
// length coercion coerce_type_typmod adds when a timestamptz(N) column has a precision
// qualifier (see wrapTimestamptzLengthCoercion): timestamptz(timestamptz,int4) = funcid
// 1967, funcformat 2 (IMPLICIT), a timestamptz result, and two args whose second is a
// non-null int4 typmod Const. pg_get_expr renders the implicit form invisibly, so
// rebuild unwraps to Args[0]; a re-resolve through ResolveForColumnTypmod re-wraps
// the identical node (fixed point).
func isImplicitTimestamptzLengthCoercion(f *FuncExpr) bool {
	if f.Funcid != 1967 || f.Funcformat != 2 || f.Funcresulttype != OidTimestamptz || len(f.Args) != 2 {
		return false
	}
	tc, isConst := f.Args[1].(*Const)
	return isConst && tc.ConstType == OidInt4 && !tc.ConstIsNull
}

// isImplicitTimeTZLengthCoercion reports whether f is the IMPLICIT timetz length
// coercion coerce_type_typmod adds when a timetz(N) column has a precision
// qualifier (see wrapTimeTZLengthCoercion): timetz(timetz,int4) = funcid 1969,
// funcformat 2 (COERCE_IMPLICIT_CAST). Like the other time-family length-coercion
// wrappers, rebuild unwraps to Args[0]; a re-resolve through ResolveForColumnTypmod
// re-wraps the identical node (fixed point).
func isImplicitTimeTZLengthCoercion(f *FuncExpr) bool {
	if f.Funcid != 1969 || f.Funcformat != 2 || f.Funcresulttype != OidTimeTZ || len(f.Args) != 2 {
		return false
	}
	tc, isConst := f.Args[1].(*Const)
	return isConst && tc.ConstType == OidInt4 && !tc.ConstIsNull
}

// isImplicitBitLengthCoercion reports whether f is the IMPLICIT bit length
// coercion coerce_type_typmod adds when a bit(N) column has a length qualifier
// (see wrapBitLengthCoercion): bit(bit,int4,bool) = funcid 1685, funcformat 2
// (IMPLICIT), a bit result, and three args whose second is a non-null int4
// typmod Const and third a bool isExplicit Const. pg_get_expr renders the
// implicit form invisibly, so rebuild unwraps to Args[0]; a re-resolve through
// ResolveForColumnTypmod re-wraps the identical node (fixed point).
func isImplicitBitLengthCoercion(f *FuncExpr) bool {
	if f.Funcid != 1685 || f.Funcformat != 2 || f.Funcresulttype != OidBit || len(f.Args) != 3 {
		return false
	}
	tc, isConst := f.Args[1].(*Const)
	return isConst && tc.ConstType == OidInt4 && !tc.ConstIsNull
}

// isImplicitVarBitLengthCoercion reports whether f is the IMPLICIT varbit length
// coercion (see wrapVarBitLengthCoercion): varbit(varbit,int4,bool) = funcid 1687,
// funcformat 2.
func isImplicitVarBitLengthCoercion(f *FuncExpr) bool {
	if f.Funcid != 1687 || f.Funcformat != 2 || f.Funcresulttype != OidVarBit || len(f.Args) != 3 {
		return false
	}
	tc, isConst := f.Args[1].(*Const)
	return isConst && tc.ConstType == OidInt4 && !tc.ConstIsNull
}

// isImplicitIntToNumericCast reports whether f is the exact FuncExpr the forward
// resolver emits for a bare integer literal in a numeric context (see
// wrapIntToNumericCast): int4_numeric (1740) or int8_numeric (1781), an
// implicit-cast form (funcformat 2), with a single argument.
func isImplicitIntToNumericCast(f *FuncExpr) bool {
	return (f.Funcid == 1740 || f.Funcid == 1781) &&
		f.Funcformat == 2 &&
		f.Funcresulttype == OidNumeric &&
		len(f.Args) == 1
}

// isImplicitInt4ToInt8Cast reports whether f is the exact FuncExpr the forward
// resolver emits for an int4 result widened to a common int8 type (see
// wrapInt4ToInt8Cast): int8(int4) (481), an implicit-cast form (funcformat 2)
// with a single argument. Like the int→numeric cast it has no SQL call syntax —
// PG re-derives it from the bare integer in an int8 context — so rebuild unwraps
// to the inner argument for a re-resolve fixed point.
func isImplicitInt4ToInt8Cast(f *FuncExpr) bool {
	return f.Funcid == 481 &&
		f.Funcformat == 2 &&
		f.Funcresulttype == OidInt8 &&
		len(f.Args) == 1
}

// isImplicitIntToInt2Cast reports whether f is the exact FuncExpr the forward
// resolver emits for an int4 (or int8) literal wrapped in an implicit int2 cast:
// int2(int4) (314) or int2(int8) (714), funcformat 2, single argument. Like the
// int→numeric cast it has no SQL call syntax — PG re-derives it from the bare
// integer in an int2 context — so rebuild unwraps to the inner argument for a
// re-resolve fixed point.
func isImplicitIntToInt2Cast(f *FuncExpr) bool {
	return (f.Funcid == 314 || f.Funcid == 714) &&
		f.Funcformat == 2 &&
		f.Funcresulttype == OidInt2 &&
		len(f.Args) == 1
}

// isImplicitToFloat4Cast reports whether f is the exact FuncExpr the forward
// resolver emits for an int4/int8/numeric CASE result widened to a common
// float4 type (see wrapToFloat4Cast): float4(int4) (318), float4(int8) (652), or
// float4(numeric) (1745), all in the implicit-cast form (funcformat 2) with a
// single argument. Like the sibling numeric-family casts these have no SQL call
// syntax that would round-trip as an implicit cast — the CASE common-type walk
// re-derives them from the bare int/numeric result in a float4 context — so
// rebuild unwraps to the inner argument for a re-resolve fixed point. The
// funcformat==2 guard is load-bearing: the SAME OIDs (e.g. float4(int4) 318)
// appear with funcformat 0 for an explicit float4(<int>) conversion call and
// funcformat 1 for an explicit `::float4` cast, both of which must rebuild back
// to their own syntax, NOT unwrap.
func isImplicitToFloat4Cast(f *FuncExpr) bool {
	return (f.Funcid == 318 || f.Funcid == 652 || f.Funcid == 1745) &&
		f.Funcformat == 2 &&
		f.Funcresulttype == OidFloat4 &&
		len(f.Args) == 1
}

// isImplicitFloat4ToFloat8Cast reports whether f is the exact FuncExpr the
// forward resolver emits for a float4 result widened to a common float8 type (see
// wrapFloat4ToFloat8Cast): float8(float4) (311), an implicit-cast form (funcformat
// 2) with a single argument. Like the int→numeric / int4→int8 casts it has no SQL
// call syntax that would round-trip as an implicit cast — the CASE common-type
// walk re-derives it from the bare float4 result in a float8 context — so rebuild
// unwraps to the inner argument for a re-resolve fixed point.
func isImplicitFloat4ToFloat8Cast(f *FuncExpr) bool {
	return f.Funcid == 311 &&
		f.Funcformat == 2 &&
		f.Funcresulttype == OidFloat8 &&
		len(f.Args) == 1
}

// isImplicitToFloat8Cast reports whether f is the exact FuncExpr the forward
// resolver emits for an int4/int8/numeric CASE result widened to a common float8
// type (see wrapToFloat8Cast): float8(int4) (316), float8(int8) (482), or
// float8(numeric) (1746), all in the implicit-cast form (funcformat 2) with a
// single argument. Like the sibling numeric-family casts these have no SQL call
// syntax that would round-trip as an implicit cast — the CASE common-type walk
// re-derives them from the bare int/numeric result in a float8 context — so
// rebuild unwraps to the inner argument for a re-resolve fixed point. The
// funcformat==2 guard is load-bearing: the SAME OIDs (e.g. float8(int4) 316)
// appear with funcformat 0 for an explicit float8(<int>) conversion call, which
// must rebuild back to that function call, NOT unwrap.
func isImplicitToFloat8Cast(f *FuncExpr) bool {
	return (f.Funcid == 316 || f.Funcid == 482 || f.Funcid == 1746) &&
		f.Funcformat == 2 &&
		f.Funcresulttype == OidFloat8 &&
		len(f.Args) == 1
}

// explicitCastTypeName reports whether f is one of the explicit numeric-family
// cast FuncExprs the forward resolver emits (resolveCastExpr / numericFamilyCast\
// Funcid — the full int/numeric/float matrix from that function's table) and
// returns the canonical target type name for the reconstructed `::type` cast. All
// are COERCE_EXPLICIT_CAST funcformat 1 with one argument. The funcformat==1 guard
// is load-bearing: several OIDs also appear with funcformat 2 as IMPLICIT casts —
// int8(int4)=481 (int4→int8 widening), 1740/1781 (int→numeric), float8(float4)=311
// and 316/482/1746 (CASE →float8 coercion) — all of which rebuild by unwrapping
// (isImplicit*) instead of reconstructing a `::type` node.
func explicitCastTypeName(f *FuncExpr) (string, bool) {
	if f.Funcformat != 1 || len(f.Args) != 1 {
		return "", false
	}
	switch f.Funcid {
	case 314, 714: // int2(int4), int2(int8)
		if f.Funcresulttype == OidInt2 {
			return "int2", true
		}
	case 480: // int4(int8)
		if f.Funcresulttype == OidInt4 {
			return "int4", true
		}
	case 481: // int8(int4)
		if f.Funcresulttype == OidInt8 {
			return "int8", true
		}
	case 1782, 1740, 1781: // int2/int4/int8 → numeric
		if f.Funcresulttype == OidNumeric {
			return "numeric", true
		}
	case 1783: // numeric → int2
		if f.Funcresulttype == OidInt2 {
			return "int2", true
		}
	case 1744: // numeric → int4
		if f.Funcresulttype == OidInt4 {
			return "int4", true
		}
	case 1779: // numeric → int8
		if f.Funcresulttype == OidInt8 {
			return "int8", true
		}
	// int2/int4/int8/numeric/float8 → float4 (float8(float4)=312 sits here too).
	case 236, 318, 652, 1745, 312: // *→float4
		if f.Funcresulttype == OidFloat4 {
			return "float4", true
		}
	// int2/int4/int8/numeric/float4 → float8. The funcformat==1 guard above is
	// load-bearing: 235/316/482/1746 and float8(float4)=311 also appear with
	// funcformat 2 as the IMPLICIT CASE-coercion casts (isImplicitToFloat8Cast /
	// isImplicitFloat4ToFloat8Cast), which rebuild by unwrapping instead.
	case 235, 316, 482, 1746, 311: // *→float8
		if f.Funcresulttype == OidFloat8 {
			return "float8", true
		}
	// float4/float8 → numeric.
	case 1742, 1743: // float→numeric
		if f.Funcresulttype == OidNumeric {
			return "numeric", true
		}
	// float4/float8 → int2 (int2(float4)=238, int2(float8)=237).
	case 238, 237: // float→int2
		if f.Funcresulttype == OidInt2 {
			return "int2", true
		}
	// float4/float8 → int4 (int4(float4)=319, int4(float8)=317).
	case 319, 317: // float→int4
		if f.Funcresulttype == OidInt4 {
			return "int4", true
		}
	// float4/float8 → int8 (int8(float4)=653, int8(float8)=483).
	case 653, 483: // float→int8
		if f.Funcresulttype == OidInt8 {
			return "int8", true
		}
	}
	return "", false
}

// rebuildConst reconstructs a literal. int4/int8 datums decode from the 8-byte
// by-value word (a negative value becomes UnaryOp{-, |v|}, matching how the
// parser tags `-N`); a text datum decodes from its varlena.
func rebuildConst(c *Const) (parser.Expr, error) {
	if c.ConstIsNull {
		return nil, fmt.Errorf("pgnodes: Rebuild: NULL Const has no goopg AST form")
	}
	switch c.ConstType {
	case OidInt4, OidInt8:
		v := int64FromByvalWord(c.Datum)
		if v < 0 {
			return &parser.UnaryOp{Op: parser.OpUnaryNeg, Operand: &parser.IntegerConst{Value: -v}}, nil
		}
		return &parser.IntegerConst{Value: v}, nil
	case OidInt2:
		// An int2 Const only arises from folding an unknown-type string literal
		// (`'5'::int2` / `col int2 DEFAULT '5'`) — a bare integer literal is int4 and
		// PG wraps it in an int4→int2 cast FuncExpr, never an int2 Const. Rebuild to a
		// STRING literal (not an IntegerConst) so a re-resolve in the int2 column
		// context routes back through foldStringLiteralConst → int2 Const (the fixed
		// point); an IntegerConst would resolve via resolveIntLiteral to an int4 Const
		// and break it. A negative value lives inside the literal, not a unary minus.
		return &parser.StringConst{Value: strconv.FormatInt(int64FromByvalWord(c.Datum), 10)}, nil
	case OidBool:
		// The by-value word is 0 (false) or 1 (true); see datum.go:NewBoolConst.
		return &parser.BooleanConst{Value: int64FromByvalWord(c.Datum) != 0}, nil
	case OidText, OidVarchar, OidBpchar:
		// text, varchar, and bpchar Consts share the same varlena wire format;
		// rebuild to the verbatim string literal. A re-resolve in the column
		// context re-folds through foldStringLiteralConst to the correct consttype
		// (the fixed point).
		return &parser.StringConst{Value: textFromVarlena(c.Datum)}, nil
	case OidOid:
		// An oid Const only arises from folding an unknown-type string literal
		// (`'5'::oid` / `col oid DEFAULT '5'`). Rebuild to the decimal STRING spelling
		// (like the int2 arm) so a re-resolve in the oid column context re-folds through
		// foldStringLiteralConst → oid Const (the fixed point). The datum word is a
		// zero-extended 32-bit unsigned value, recovered by masking to the low 4 bytes.
		return &parser.StringConst{Value: strconv.FormatUint(uint64(uint32(int64FromByvalWord(c.Datum))), 10)}, nil
	case OidFloat8:
		// A float8 Const only arises from folding an unknown-type string literal. The
		// datum word is the raw IEEE-754 double's bits; render the shortest decimal that
		// round-trips (FormatFloat 'g'/-1) so a re-resolve in the float8 column context
		// re-folds to the identical bits — the fixed point.
		f := math.Float64frombits(uint64(int64FromByvalWord(c.Datum)))
		return &parser.StringConst{Value: strconv.FormatFloat(f, 'g', -1, 64)}, nil
	case OidFloat4:
		// The float4 analogue: the datum word holds the 32-bit IEEE bits sign-extended,
		// recovered by masking to the low 4 bytes. FormatFloat with bitSize 32 emits the
		// shortest decimal that round-trips through parseFloat4FromString (fixed point).
		f := math.Float32frombits(uint32(int64FromByvalWord(c.Datum)))
		return &parser.StringConst{Value: strconv.FormatFloat(float64(f), 'g', -1, 32)}, nil
	case OidNumeric:
		// Decode the packed NumericData back to its canonical decimal text
		// (preserving dscale trailing zeros so a re-resolve is a fixed point);
		// a negative value becomes UnaryOp{-, |v|}, matching doNegate.
		v, err := decodeNumericVar(c.Datum)
		if err != nil {
			return nil, fmt.Errorf("pgnodes: Rebuild: numeric Const: %w", err)
		}
		if v.special != 0 {
			// A NaN/±Infinity numeric has no numeric-literal token; it only arises from
			// folding a STRING literal, so rebuild to that string spelling (mirroring the
			// int2 arm) — in a numeric column context it re-folds through
			// foldStringLiteralConst → NewNumericConst to this identical special Const.
			return &parser.StringConst{Value: v.specialText()}, nil
		}
		lit := &parser.NumericConst{Value: v.text()}
		if v.negative {
			return &parser.UnaryOp{Op: parser.OpUnaryNeg, Operand: lit}, nil
		}
		return lit, nil
	case OidTimestamptz:
		// Render the μs datum back into a canonical UTC timestamp literal (with an
		// explicit +00 offset) so a re-resolve in the timestamptz column context
		// reproduces the identical Const — the fixed point. A negative (pre-2000)
		// value is inside the literal string, not a unary minus.
		usec := int64FromByvalWord(c.Datum)
		return &parser.StringConst{Value: formatTimestamptzUTC(usec)}, nil
	case OidTime:
			// Render the μs-since-midnight datum back into a canonical "HH:MM:SS[.ffffff]"
			// literal so a re-resolve in the time column context reproduces the identical
			// Const — the fixed point.
			micros := int64FromByvalWord(c.Datum)
			return &parser.StringConst{Value: formatTime(micros)}, nil
		case OidTimeTZ:
			// Render the 12-byte timetz datum back into a canonical
			// "HH:MM:SS[.ffffff]±HH:MI" literal so a re-resolve reproduces the
			// identical Const — the fixed point.
			micros, off := decodeTimeTZDatum(c.Datum)
			return &parser.StringConst{Value: formatTimeTZ(micros, off)}, nil
		case OidTimestamp:
		// Same as timestamptz but WITHOUT the +00 offset — timestamp has no
		// timezone. A re-resolve in a timestamp column context folds through
		// parseTimestampMicros to the identical Const.
		usec := int64FromByvalWord(c.Datum)
		return &parser.StringConst{Value: formatTimestamp(usec)}, nil
	case OidDate:
		// Render the DateADT day count back into a canonical "YYYY-MM-DD" literal
		// which re-resolves to the identical Const in a date column context — the
		// fixed point. A pre-2000 (negative) day count lives inside the literal
		// string, not a unary minus.
		days := int32(int64FromByvalWord(c.Datum))
		return &parser.StringConst{Value: formatDate(days)}, nil
	case OidBit:
		// Rebuild the VarBit varlena to its canonical bit-string literal
		// (e.g. '10101010'). A re-resolve in a bit column context re-folds
		// through parseBitFromString → NewBitConst to the identical Const.
		bitLen := bitLenFromVarlena(c.Datum)
		data := bitDataFromVarlena(c.Datum)
		return &parser.StringConst{Value: formatBit(bitLen, data)}, nil
	case OidVarBit:
		// Same as bit but with consttype 1562.
		bitLen := bitLenFromVarlena(c.Datum)
		data := bitDataFromVarlena(c.Datum)
		return &parser.StringConst{Value: formatBit(bitLen, data)}, nil
	default:
		return nil, fmt.Errorf("pgnodes: Rebuild: unsupported Const type OID %d", c.ConstType)
	}
}

// rebuildOpExpr reconstructs a binary operator whose operands are scalar nodes
// (the S2 DEFAULT-expression scope, where Rebuild is the recursion).
func rebuildOpExpr(o *OpExpr) (parser.Expr, error) {
	return rebuildOpExprWith(o, Rebuild)
}

// rebuildOpExprWith reconstructs a binary operator, rebuilding each operand
// through the caller-supplied recursion. The opno is reverse-mapped to its
// spelling (from the same pg_operator seed S0 resolved forward), then to a
// parser OpCode. The recursion parameter lets the query-tree scope
// (rebuild_query.go) reuse this to rebuild OpExprs whose operands may be column
// Vars, not only scalars.
func rebuildOpExprWith(o *OpExpr, rec func(Node) (parser.Expr, error)) (parser.Expr, error) {
	if len(o.Args) != 2 {
		return nil, fmt.Errorf("pgnodes: Rebuild: OpExpr with %d args (want 2)", len(o.Args))
	}
	name, ok := operatorNameForOID(o.Opno)
	if !ok {
		return nil, fmt.Errorf("pgnodes: Rebuild: unknown operator OID %d", o.Opno)
	}
	op := parser.ParseBinaryOp(name)
	if op == parser.OpUnknown {
		return nil, fmt.Errorf("pgnodes: Rebuild: operator %q has no goopg OpCode", name)
	}
	left, err := rec(o.Args[0])
	if err != nil {
		return nil, err
	}
	right, err := rec(o.Args[1])
	if err != nil {
		return nil, err
	}
	return &parser.BinaryOp{Op: op, Left: left, Right: right}, nil
}

// rebuildDistinctExpr reconstructs `a IS DISTINCT FROM b`. The NOT form
// (`IS NOT DISTINCT FROM`) is a NOT BoolExpr wrapping a DistinctExpr, so it is
// rebuilt by rebuildBoolExpr's NOT arm into `NOT (a IS DISTINCT FROM b)` — a
// distinct spelling that re-resolves to the identical BOOLEXPR/DISTINCTEXPR IR,
// so resolve→Rebuild→re-resolve is still a fixed point.
func rebuildDistinctExpr(d *DistinctExpr) (parser.Expr, error) {
	return rebuildDistinctExprWith(d, Rebuild)
}

// rebuildDistinctExprWith rebuilds a DISTINCTEXPR through the injected recursion
// `rec` (so operands may be column Vars in the view-reload path). The two args
// rebuild to the left/right of a non-negated IsDistinctFromExpr.
func rebuildDistinctExprWith(d *DistinctExpr, rec func(Node) (parser.Expr, error)) (parser.Expr, error) {
	if len(d.Args) != 2 {
		return nil, fmt.Errorf("pgnodes: Rebuild: DistinctExpr with %d args (want 2)", len(d.Args))
	}
	left, err := rec(d.Args[0])
	if err != nil {
		return nil, err
	}
	right, err := rec(d.Args[1])
	if err != nil {
		return nil, err
	}
	return &parser.IsDistinctFromExpr{Left: left, Right: right, Negated: false}, nil
}

// int64FromByvalWord decodes the little-endian 8-byte Datum word (see
// datum.go:byvalWord) as a signed int64, undoing Int32GetDatum/Int64GetDatum's
// sign extension.
func int64FromByvalWord(b []byte) int64 {
	return int64(binary.LittleEndian.Uint64(b[:8]))
}

// textFromVarlena decodes the in-memory 4-byte-header varlena (see
// datum.go:textVarlena): VARSIZE = header>>2, string = bytes [4:VARSIZE].
func textFromVarlena(b []byte) string {
	total := binary.LittleEndian.Uint32(b[:4]) >> 2
	return string(b[4:total])
}

// operatorNameForOID reverse-maps a pg_operator OID to its spelling, lazily
// building the index from the same PG18 seed S0's forward index uses. Only
// binary operators are indexed (the resolver emits OpExpr for binary ops only).
var (
	operatorNameByOIDOnce sync.Once
	operatorNameByOID     map[uint32]string
)

func operatorNameForOID(oid uint32) (string, bool) {
	operatorNameByOIDOnce.Do(func() {
		entries := catalog.PGOperatorAllEntries()
		operatorNameByOID = make(map[uint32]string, len(entries))
		for _, e := range entries {
			if e.Kind != 'b' {
				continue
			}
			// pg_operator OID is unique; first write wins (all rows distinct OIDs).
			if _, dup := operatorNameByOID[e.OID]; !dup {
				operatorNameByOID[e.OID] = e.Name
			}
		}
	})
	name, ok := operatorNameByOID[oid]
	return name, ok
}
