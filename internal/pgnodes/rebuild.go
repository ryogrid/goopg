package pgnodes

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
		isImplicitFloat4ToFloat8Cast(f) {
		return rec(f.Args[0])
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
	case OidBool:
		// The by-value word is 0 (false) or 1 (true); see datum.go:NewBoolConst.
		return &parser.BooleanConst{Value: int64FromByvalWord(c.Datum) != 0}, nil
	case OidText:
		return &parser.StringConst{Value: textFromVarlena(c.Datum)}, nil
	case OidNumeric:
		// Decode the packed NumericData back to its canonical decimal text
		// (preserving dscale trailing zeros so a re-resolve is a fixed point);
		// a negative value becomes UnaryOp{-, |v|}, matching doNegate.
		v, err := decodeNumericVar(c.Datum)
		if err != nil {
			return nil, fmt.Errorf("pgnodes: Rebuild: numeric Const: %w", err)
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
