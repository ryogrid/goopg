package pgnodes

// resolver_expr.go — M0123-S2 (first sub-slice): resolve a goopg default-
// expression AST (parser.Expr) into the canonical pg_node_tree scalar IR so a
// real PG18 standby can EVALUATE the stored expression. This is the *forward*
// direction that pairs with S0's OID indexes (catalog.LookupOperatorForNode)
// and the S1 codec (Out/Read).
//
// Scope of THIS sub-slice: literal Consts (int4/int8/text), a unary minus
// folded onto an integer literal (PG's doNegate + make_const), binary
// operators (OpExpr) whose operand types forward-resolve to a built-in
// pg_operator row, and plain built-in function calls (FuncExpr) whose funcid
// forward-resolves via S0's catalog.LookupProcForNode and whose result type
// comes from the generated pg_proc return-type map (catalog.ProcResultType).
// Any expression outside this subset makes ResolveExpr return ErrUnsupported so
// the writer degrades to storing SQL text (all-or-nothing; see unsupported.go
// and 02e §3's graceful-degradation invariant — never partial-emit).
//
// Provenance for the coercion/typing decisions: PostgreSQL types an integer
// literal by its own magnitude (make_const in parse_node.c: int4 if it fits,
// else int8), folds `- <literal>` into a single negative Const (doNegate in
// gram.y), and coerces a bare string literal (type "unknown") to the context
// type — here only the text case is supported.

import (
	"errors"
	"math"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// ErrUnsupported is returned by ResolveExpr when the expression contains any
// shape outside the canonical scalar subset. Callers treat it as "store SQL
// text instead" — it is never a hard failure.
var ErrUnsupported = errors.New("pgnodes: expression outside canonical scalar subset")

// intoInt4Max is PG's boundary for typing an integer literal: a value whose
// magnitude fits a signed int4 is typed int4, otherwise int8. Mirrors
// make_const's `if (val >= INT_MIN && val <= INT_MAX)` for the already-signed
// value (after any doNegate fold).
func fitsInt4(v int64) bool { return v >= math.MinInt32 && v <= math.MaxInt32 }

// ResolveExpr converts a column-DEFAULT (or statistics-expression) AST into the
// canonical pg_node_tree scalar IR. targetType is the OID of the type the
// expression is stored for (the column type for a DEFAULT); 0 means "unknown /
// any" and is used when recursing into operator/function operands. It only
// affects a bare string literal, which PG coerces from "unknown" to the context
// type. Returns ErrUnsupported for anything outside the supported subset.
func ResolveExpr(e parser.Expr, targetType uint32) (Node, error) {
	n, _, err := resolve(e, targetType)
	return n, err
}

// ResolveForColumn is the writer-facing entry point for a column DEFAULT (or any
// value stored FOR a specific type). It returns the canonical IR only when the
// WHOLE expression resolves AND its top-level result type OID equals targetType
// exactly. The exact-match guard is a fidelity requirement, not a nicety: PG's
// build_column_default returns the stored expression already coerced to the
// attribute type (see postgres/src/backend/catalog/heap.c:build_column_default →
// coerce_to_target_type), so a real standby that stringToNode()'s a canonical
// adbin whose top node is, say, an int4 Const for a numeric/smallint column
// would insert a mistyped Datum. ResolveExpr itself does NOT check this (it types
// an integer literal purely by magnitude, ignoring the column context beyond the
// int8-widening / text-coercion cases), so the writer must. When the types do not
// match — DEFAULT 0 on numeric, DEFAULT 5 on smallint, a string literal on a
// non-text column — this returns (nil, false) and the writer degrades to SQL
// text (all-or-nothing; 02e §3). This keeps SupportsExpr's already-tested
// semantics unchanged for the resolver's own round-trip gate.
func ResolveForColumn(e parser.Expr, targetType uint32) (Node, bool) {
	n, typ, err := resolve(e, targetType)
	if err != nil || typ != targetType {
		return nil, false
	}
	return n, true
}

// resolve returns the IR node AND its result type OID (needed to forward-resolve
// an enclosing operator/function). The returned type is 0 only for a Const whose
// type is genuinely unknown, which cannot happen here (every leaf resolves to a
// concrete OID or errors).
func resolve(e parser.Expr, expected uint32) (Node, uint32, error) {
	switch v := e.(type) {
	case *parser.IntegerConst:
		return resolveIntLiteral(v.Value, expected)

	case *parser.NumericConst:
		// A decimal/scientific literal is typed numeric by PG's scanner
		// regardless of context (make_const on a T_Float token).
		return resolveNumericLiteral(v.Value, false)

	case *parser.BooleanConst:
		return NewBoolConst(v.Value), OidBool, nil

	case *parser.StringConst:
		// A bare string literal is PG type "unknown"; in a text context it is
		// text. Only the text (or unknown/any) context is supported here.
		if expected == OidText || expected == 0 {
			return NewTextConst(v.Value), OidText, nil
		}
		// In a timestamptz context PG folds the literal to a Const at parse time
		// (stringTypeToConst → timestamptz_in). Reproduce that only for the
		// deterministic subset (explicit offset / 'epoch'); a TimeZone-dependent
		// form falls back to SQL text (all-or-nothing; 02e §3).
		if expected == OidTimestamptz {
			if usec, ok := parseTimestamptzMicros(v.Value); ok {
				return NewTimestamptzConst(usec), OidTimestamptz, nil
			}
		}
		return nil, 0, ErrUnsupported

	case *parser.IsNullExpr:
		return resolveNullTest(v)

	case *parser.IsBoolExpr:
		return resolveBooleanTest(v)

	case *parser.CaseExpr:
		return resolveCaseExpr(v)

	case *parser.IsDistinctFromExpr:
		return resolveDistinctFrom(v)

	case *parser.UnaryOp:
		switch v.Op {
		case parser.OpUnaryPos:
			return resolve(v.Operand, expected)
		case parser.OpUnaryNeg:
			// PG folds `- <literal>` into one negative Const (doNegate).
			if lit, ok := v.Operand.(*parser.IntegerConst); ok {
				return resolveIntLiteral(-lit.Value, expected)
			}
			if lit, ok := v.Operand.(*parser.NumericConst); ok {
				return resolveNumericLiteral(lit.Value, true)
			}
			return nil, 0, ErrUnsupported
		case parser.OpNot:
			// NOT x → a single-arg BOOLEXPR (makeNotExpr; no flattening).
			return resolveBoolNot(v.Operand)
		default:
			return nil, 0, ErrUnsupported
		}

	case *parser.BinaryOp:
		return resolveBinaryOp(v)

	case *parser.FuncCall:
		return resolveFuncCall(v)

	default:
		return nil, 0, ErrUnsupported
	}
}

// resolveFuncCall resolves a plain built-in function call to a FuncExpr. Only
// the ordinary-call shape is supported: no aggregate/window decoration (OVER,
// FILTER, ORDER BY, WITHIN GROUP, DISTINCT), no star argument, and no VARIADIC
// spread. Each argument is forward-resolved with an unknown context, the funcid
// is looked up by (name + actual argument type OIDs) via S0's
// catalog.LookupProcForNode, and the result type comes from pg_proc.prorettype
// (catalog.ProcResultType). Anything outside this subset — including an
// argument the literal-typing rules cannot coerce to a seeded overload — yields
// ErrUnsupported so the writer degrades to SQL text (all-or-nothing).
//
// Provenance: PG builds this FuncExpr in make_fn_expr / ParseFuncOrColumn; a
// bare call carries funcformat = COERCE_EXPLICIT_CALL (0), funcretset/
// funcvariadic false for the scalar subset, and collations propagated from the
// (only text here) collatable operands — DEFAULT_COLLATION_OID (100).
func resolveFuncCall(f *parser.FuncCall) (Node, uint32, error) {
	if err := funcCallGuard(f); err != nil {
		return nil, 0, err
	}
	argNodes := make([]Node, 0, len(f.Args))
	argOIDs := make([]uint32, 0, len(f.Args))
	for _, a := range f.Args {
		n, t, err := resolve(a, 0)
		if err != nil {
			return nil, 0, err
		}
		argNodes = append(argNodes, n)
		argOIDs = append(argOIDs, t)
	}
	return buildFuncExpr(f.Name.Name, argNodes, argOIDs)
}

// funcCallGuard rejects any non-plain-call decoration on a function call. These
// never appear in a stored column DEFAULT / statistics expression / view target
// but guarding prevents silently emitting a FuncExpr that drops the
// decoration's semantics (aggregate/window/VARIADIC spread), and rejects a
// non-built-in schema qualifier (only unqualified or pg_catalog-qualified
// built-ins forward-resolve via S0's proc index). Shared by the scalar
// (resolver_expr.go) and query-scoped (resolver_query.go) resolvers.
func funcCallGuard(f *parser.FuncCall) error {
	if f.Star || f.Distinct || f.Over != nil || f.Filter != nil ||
		len(f.OrderBy) != 0 || len(f.WithinGroup) != 0 {
		return ErrUnsupported
	}
	for _, isVariadic := range f.Variadic {
		if isVariadic {
			return ErrUnsupported
		}
	}
	if f.Name.Schema != "" && f.Name.Schema != "pg_catalog" {
		return ErrUnsupported
	}
	return nil
}

// buildFuncExpr forward-resolves a built-in function by (name + already-resolved
// argument type OIDs) via S0's catalog.LookupProcForNode, takes its result type
// from the generated pg_proc return-type map (catalog.ProcResultType), and
// constructs the canonical FuncExpr with PG's collation propagation
// (DEFAULT_COLLATION_OID when a collatable operand/result is involved, else 0).
// Shared by both resolvers so each builds a byte-identical FuncExpr.
func buildFuncExpr(name string, argNodes []Node, argOIDs []uint32) (Node, uint32, error) {
	funcid, ok := catalog.LookupProcForNode(name, argOIDs)
	if !ok {
		return nil, 0, ErrUnsupported
	}
	resultType, ok := catalog.ProcResultType(funcid)
	if !ok || resultType == 0 {
		return nil, 0, ErrUnsupported
	}
	inputcollid := uint32(0)
	for _, t := range argOIDs {
		if isCollatable(t) {
			inputcollid = DefaultCollationOid
			break
		}
	}
	funccollid := uint32(0)
	if isCollatable(resultType) {
		funccollid = DefaultCollationOid
	}
	return &FuncExpr{
		Funcid:         funcid,
		Funcresulttype: resultType,
		Funcretset:     false,
		Funcvariadic:   false,
		Funcformat:     0, // COERCE_EXPLICIT_CALL
		Funccollid:     funccollid,
		Inputcollid:    inputcollid,
		Args:           argNodes,
		Location:       -1,
	}, resultType, nil
}

// resolveIntLiteral types a (possibly already-negated) integer value the way PG
// make_const does — int4 when it fits, else int8 — and honours a widening int8
// context (DEFAULT 5 on a bigint column stores an int8 Const).
func resolveIntLiteral(v int64, expected uint32) (Node, uint32, error) {
	var c Node
	var ityp uint32
	if expected == OidInt8 || !fitsInt4(v) {
		c, ityp = NewInt8Const(v), OidInt8
	} else {
		// expected is int4, unknown, or a compatible context: emit int4.
		c, ityp = NewInt4Const(int32(v)), OidInt4
	}
	// A bare integer literal assigned to a numeric column is typed int4/int8 by
	// the scanner (make_const), then coerce_to_target_type wraps it in an
	// IMPLICIT-CAST FuncExpr — int4_numeric (1740) or int8_numeric (1781) — so
	// PG's adbin/ev_action is a FuncExpr, not a numeric Const. Reproduce that
	// only in an exact numeric context; a plain int context keeps the bare Const.
	if expected == OidNumeric {
		return wrapIntToNumericCast(c, ityp), OidNumeric, nil
	}
	return c, ityp, nil
}

// wrapIntToNumericCast builds the implicit int->numeric coercion FuncExpr that
// PG's coerce_type emits for a bare integer literal in a numeric context.
// funcid: int4_numeric (1740) for int4, int8_numeric (1781) for int8 (a literal
// is never int2). funcformat = COERCE_IMPLICIT_CAST (2); no collation.
func wrapIntToNumericCast(arg Node, argType uint32) Node {
	funcid := uint32(1740) // int4_numeric
	if argType == OidInt8 {
		funcid = 1781 // int8_numeric
	}
	return &FuncExpr{
		Funcid:         funcid,
		Funcresulttype: OidNumeric,
		Funcretset:     false,
		Funcvariadic:   false,
		Funcformat:     2, // COERCE_IMPLICIT_CAST
		Funccollid:     0,
		Inputcollid:    0,
		Args:           []Node{arg},
		Location:       -1,
	}
}

// resolveNumericLiteral types a decimal/scientific literal as numeric (OID 1700)
// — PG's scanner types a T_Float token numeric regardless of the surrounding
// context. negative folds an outer unary minus into the packed Const the way
// gram.y's doNegate produces a single negative numeric Const.
func resolveNumericLiteral(text string, negative bool) (Node, uint32, error) {
	n, err := NewNumericConst(text, negative)
	if err != nil {
		return nil, 0, err
	}
	return n, OidNumeric, nil
}

// resolveBinaryOp resolves a two-operand operator to an OpExpr by forward-
// resolving both operands, then looking the operator up by spelling + operand
// type OIDs (S0's catalog.LookupOperatorForNode). Collation follows PG: the
// only collatable type this subset emits is text, so inputcollid/opcollid are
// DEFAULT_COLLATION_OID (100) when a text operand/result is involved, else 0.
func resolveBinaryOp(b *parser.BinaryOp) (Node, uint32, error) {
	switch b.Op {
	case parser.OpAnd:
		return resolveBoolBinary(b, BoolExprAnd)
	case parser.OpOr:
		return resolveBoolBinary(b, BoolExprOr)
	}
	lNode, lType, err := resolve(b.Left, 0)
	if err != nil {
		return nil, 0, err
	}
	rNode, rType, err := resolve(b.Right, 0)
	if err != nil {
		return nil, 0, err
	}
	return buildOpExpr(lNode, lType, rNode, rType, b.Op.String())
}

// resolveDistinctFrom resolves `a IS [NOT] DISTINCT FROM b` to a DISTINCTEXPR
// (with a NOT BoolExpr wrapper for the NOT form), mirroring PG's
// transformAExprDistinct → make_distinct_op. It threads the scalar `resolve` so
// operand literals type as usual.
func resolveDistinctFrom(d *parser.IsDistinctFromExpr) (Node, uint32, error) {
	return resolveDistinctFromWith(d, resolve)
}

// resolveDistinctFromWith resolves `a IS [NOT] DISTINCT FROM b` through the
// injected recursion `rec` (so the query-scoped path can thread
// queryScope.resolveExpr and let the operands be column Vars in a view qual).
// Both operands forward-resolve with an unknown context, then buildDistinctExpr
// looks up the `=` operator for their types and tags the node DISTINCTEXPR.
// `IS NOT DISTINCT FROM` wraps that DistinctExpr in a NOT BoolExpr exactly as
// transformAExprDistinct does (makeBoolExpr(NOT_EXPR, [DistinctExpr])).
//
// An UNDECORATED NULL literal on either side is special-cased to a NullTest on
// the other operand, mirroring PG's transformAExprDistinct →
// make_nulltest_from_distinct (parse_expr.c): "If either input is an undecorated
// NULL literal, transform to a NullTest on the other input. That's simpler than a
// full DistinctExpr, and it avoids needing to require that the datatype have an =
// operator." The rewrite fires BEFORE resolving operands (PG tests
// exprIsNullConstant on the raw A_Const), maps `IS DISTINCT FROM NULL` →
// IS_NOT_NULL and `IS NOT DISTINCT FROM NULL` → IS_NULL, and produces a plain
// NullTest with NO NOT wrapper (the negation is folded into nulltesttype).
// argisrow is false regardless of operand type. Only a bare NULL qualifies — a
// decorated `NULL::int` parses to a cast (not *parser.NullConst) and takes the
// ordinary DISTINCTEXPR path, exactly as PG's IsA(arg, A_Const) guard requires.
func resolveDistinctFromWith(d *parser.IsDistinctFromExpr, rec scopedResolve) (Node, uint32, error) {
	if arg, ok := distinctNullTestArg(d); ok {
		node, _, err := rec(arg, 0)
		if err != nil {
			return nil, 0, err
		}
		ntt := IsNotNull // IS DISTINCT FROM NULL  -> IS NOT NULL
		if d.Negated {
			ntt = IsNull // IS NOT DISTINCT FROM NULL -> IS NULL
		}
		return &NullTest{Arg: node, NullTestType: ntt, ArgIsRow: false, Location: -1}, OidBool, nil
	}
	lNode, lType, err := rec(d.Left, 0)
	if err != nil {
		return nil, 0, err
	}
	rNode, rType, err := rec(d.Right, 0)
	if err != nil {
		return nil, 0, err
	}
	node, _, err := buildDistinctExpr(lNode, lType, rNode, rType)
	if err != nil {
		return nil, 0, err
	}
	if d.Negated {
		return &BoolExpr{Boolop: BoolExprNot, Args: []Node{node}, Location: -1}, OidBool, nil
	}
	return node, OidBool, nil
}

// distinctNullTestArg reports whether an `a IS [NOT] DISTINCT FROM b` has an
// undecorated NULL literal on one side and, if so, returns the OTHER operand —
// the argument PG's make_nulltest_from_distinct wraps in a NullTest. It mirrors
// exprIsNullConstant (parse_expr.c): only a bare *parser.NullConst counts (a
// `NULL::type` cast is a different node and is NOT a null constant here). PG
// checks the right operand first, then the left, so `NULL IS DISTINCT FROM NULL`
// tests the left (right is NULL) and yields a NullTest over the left NULL — we
// preserve that ordering.
func distinctNullTestArg(d *parser.IsDistinctFromExpr) (parser.Expr, bool) {
	if _, ok := d.Right.(*parser.NullConst); ok {
		return d.Left, true
	}
	if _, ok := d.Left.(*parser.NullConst); ok {
		return d.Right, true
	}
	return nil, false
}

// buildDistinctExpr builds a DISTINCTEXPR over the `=` operator for the operand
// types. PG's make_distinct_op calls make_op on `=` and then just
// NodeSetTag(result, T_DistinctExpr) — the node is byte-identical to a plain
// `a = b` OpExpr except the tag — so we reuse buildOpExpr and re-tag. make_op's
// result must yield boolean (make_distinct_op errors otherwise), which every
// operator this subset resolves does.
func buildDistinctExpr(lNode Node, lType uint32, rNode Node, rType uint32) (Node, uint32, error) {
	node, resType, err := buildOpExpr(lNode, lType, rNode, rType, "=")
	if err != nil {
		return nil, 0, err
	}
	op := node.(*OpExpr)
	if op.Opresulttype != OidBool {
		return nil, 0, ErrUnsupported
	}
	d := DistinctExpr(*op)
	return &d, resType, nil
}

// scopedResolve is the recursion an operand is resolved through by the *With
// bool/null builders. The scalar resolver threads its own `resolve`; the
// query-scoped resolver (resolver_query.go) threads queryScope.resolveExpr so a
// BoolExpr/NullTest operand inside a view WHERE-qual or target may itself be a
// column Var. Both have the identical (expr, expectedType) -> (node, type, err)
// shape, so the builders emit byte-identical BOOLEXPR/NULLTEST nodes either way
// (M0123-S4 sub-slice 2: view-query bool/null wiring, mirroring how 2b made
// rebuildOpExpr/rebuildFuncExpr recursion-injectable).
type scopedResolve = func(parser.Expr, uint32) (Node, uint32, error)

// resolveBoolBinary resolves an AND/OR operator to a BOOLEXPR, reproducing PG's
// makeAndExpr/makeOrExpr flattening: `a AND b AND c` parses left-associatively
// as `(a AND b) AND c`, and PG collapses that into ONE BoolExpr with three args
// (it appends the right operand to a same-boolop left BoolExpr). We resolve the
// left operand first and, when it already resolved to a BoolExpr of the same
// boolop (the left-nested spine), append the right operand to its args instead
// of nesting — otherwise emit a two-arg BoolExpr. A parenthesised right side
// (`a AND (b AND c)`) is a distinct parse tree and stays nested, exactly as PG
// keeps it nested. The result type is always bool.
func resolveBoolBinary(b *parser.BinaryOp, boolop int32) (Node, uint32, error) {
	return resolveBoolBinaryWith(b, boolop, resolve)
}

// resolveBoolBinaryWith resolves an AND/OR operator to a BOOLEXPR, reproducing
// PG's makeAndExpr/makeOrExpr flattening (see resolveBoolBinary's comment for
// the full rationale). Operands resolve through `rec`, so the query-scoped
// resolver can thread queryScope.resolveExpr and flatten `a AND b AND c` over
// view columns exactly as the scalar path flattens it over literals.
func resolveBoolBinaryWith(b *parser.BinaryOp, boolop int32, rec scopedResolve) (Node, uint32, error) {
	lNode, _, err := rec(b.Left, OidBool)
	if err != nil {
		return nil, 0, err
	}
	rNode, _, err := rec(b.Right, OidBool)
	if err != nil {
		return nil, 0, err
	}
	if be, ok := lNode.(*BoolExpr); ok && be.Boolop == boolop {
		be.Args = append(be.Args, rNode)
		return be, OidBool, nil
	}
	return &BoolExpr{Boolop: boolop, Args: []Node{lNode, rNode}, Location: -1}, OidBool, nil
}

// resolveBoolNot resolves `NOT x` to a single-arg NOT BOOLEXPR (makeNotExpr does
// no flattening). Result type bool.
func resolveBoolNot(operand parser.Expr) (Node, uint32, error) {
	return resolveBoolNotWith(operand, resolve)
}

// resolveBoolNotWith resolves `NOT x` to a single-arg NOT BOOLEXPR (makeNotExpr
// does no flattening). Result type bool. The operand resolves through `rec`.
func resolveBoolNotWith(operand parser.Expr, rec scopedResolve) (Node, uint32, error) {
	n, _, err := rec(operand, OidBool)
	if err != nil {
		return nil, 0, err
	}
	return &BoolExpr{Boolop: BoolExprNot, Args: []Node{n}, Location: -1}, OidBool, nil
}

// resolveNullTest resolves `x IS [NOT] NULL` to a NULLTEST. The argument
// resolves in an unknown context (IS NULL applies to any type); argisrow is
// false for the scalar subset. Result type bool.
func resolveNullTest(e *parser.IsNullExpr) (Node, uint32, error) {
	return resolveNullTestWith(e, resolve)
}

// resolveNullTestWith resolves `x IS [NOT] NULL` to a NULLTEST. The argument
// resolves through `rec` in an unknown context (IS NULL applies to any type);
// argisrow is false for the scalar subset. Result type bool.
func resolveNullTestWith(e *parser.IsNullExpr, rec scopedResolve) (Node, uint32, error) {
	arg, _, err := rec(e.Operand, 0)
	if err != nil {
		return nil, 0, err
	}
	ntt := IsNull
	if e.Negated {
		ntt = IsNotNull
	}
	return &NullTest{Arg: arg, NullTestType: ntt, ArgIsRow: false, Location: -1}, OidBool, nil
}

// booleanTestType maps goopg's parser.IsBoolExpr flags (TestTrue/TestFalse/
// Negated) to PG's BoolTestType ordinal. Neither TestTrue nor TestFalse means
// IS [NOT] UNKNOWN (see internal/parser expr.go IsBoolExpr).
func booleanTestType(e *parser.IsBoolExpr) int32 {
	switch {
	case e.TestTrue:
		if e.Negated {
			return IsNotTrue
		}
		return IsTrue
	case e.TestFalse:
		if e.Negated {
			return IsNotFalse
		}
		return IsFalse
	default:
		if e.Negated {
			return IsNotUnknown
		}
		return IsUnknown
	}
}

// resolveBooleanTest resolves `x IS [NOT] TRUE/FALSE/UNKNOWN` to a BOOLEANTEST.
// The argument is boolean-valued (PG requires it), so it resolves in a bool
// context; the result type is always bool.
func resolveBooleanTest(e *parser.IsBoolExpr) (Node, uint32, error) {
	return resolveBooleanTestWith(e, resolve)
}

// resolveBooleanTestWith resolves a BOOLEANTEST through the injected recursion
// `rec` (so the argument may be a column Var in the view-qual path). Mirrors
// resolveNullTestWith; the operand is resolved in a bool context.
func resolveBooleanTestWith(e *parser.IsBoolExpr, rec scopedResolve) (Node, uint32, error) {
	arg, _, err := rec(e.Operand, OidBool)
	if err != nil {
		return nil, 0, err
	}
	return &BooleanTest{Arg: arg, BoolTestType: booleanTestType(e), Location: -1}, OidBool, nil
}

// resolveCaseExpr resolves a scalar (column-DEFAULT scope) CASE expression.
func resolveCaseExpr(e *parser.CaseExpr) (Node, uint32, error) {
	return resolveCaseExprWith(e, resolve)
}

// resolveCaseExprWith resolves a CASE to a canonical CaseExpr, threading the
// injected recursion so a CASE inside a view qual can resolve WHEN/result
// operands over base-relation Vars (mirrors resolveBooleanTestWith).
//
// Both forms are modeled. The searched form (`CASE WHEN cond THEN …`) leaves Arg
// nil and requires every WHEN condition to resolve to bool. The simple form
// (`CASE operand WHEN val …`) resolves the operand once into Arg and rewrites
// each `WHEN val` into the OpExpr `placeholder = val` — exactly PG's
// transformCaseExpr, which substitutes a CaseTestExpr for the operand. Only a
// Var/Const operand carrying an exact (operandType = valType) `=` operator is
// accepted so the placeholder is never coercion-wrapped; anything else degrades
// to SQL text.
//
// All results (plus any ELSE) must resolve to the SAME non-collatable type —
// select_common_type cross-type coercion is deferred; a mixed-type or collatable
// CASE returns ErrUnsupported so the writer degrades to SQL text (all-or-nothing).
func resolveCaseExprWith(e *parser.CaseExpr, rec scopedResolve) (Node, uint32, error) {
	if len(e.Whens) == 0 {
		return nil, 0, ErrUnsupported
	}
	// Simple form: resolve the operand once and build the CaseTestExpr placeholder
	// typed from it (transformCaseExpr: typeId/typeMod/collation from the operand).
	// The searched form leaves both nil.
	var (
		arg      Node
		argType  uint32
		testExpr *CaseTestExpr
	)
	if e.Operand != nil {
		aNode, aType, err := rec(e.Operand, 0)
		if err != nil {
			return nil, 0, err
		}
		tmod, collid, ok := operandTypmodCollid(aNode)
		if !ok {
			return nil, 0, ErrUnsupported // unmodeled operand shape
		}
		arg, argType = aNode, aType
		testExpr = &CaseTestExpr{Typeid: aType, Typemod: tmod, Collation: collid}
	}
	var (
		args     []Node
		casetype uint32
		seeded   bool
	)
	for _, w := range e.Whens {
		cond, err := resolveCaseWhenCond(w.When, testExpr, argType, rec)
		if err != nil {
			return nil, 0, err
		}
		res, resType, err := rec(w.Then, 0)
		if err != nil {
			return nil, 0, err
		}
		if !seeded {
			casetype, seeded = resType, true
		} else if resType != casetype {
			return nil, 0, ErrUnsupported // mixed result types — deferred
		}
		args = append(args, &CaseWhen{Expr: cond, Result: res, Location: -1})
	}

	var defresult Node
	if e.Else != nil {
		res, resType, err := rec(e.Else, 0)
		if err != nil {
			return nil, 0, err
		}
		if resType != casetype {
			return nil, 0, ErrUnsupported // mixed result types — deferred
		}
		defresult = res
	}

	constlen, constbyval, ok := caseTypeMeta(casetype)
	if !ok {
		return nil, 0, ErrUnsupported // unmodeled or collatable casetype
	}
	if defresult == nil {
		// ELSE omitted → PG synthesizes a typed NULL Const of casetype.
		defresult = newNullConst(casetype, constlen, constbyval)
	}
	return &CaseExpr{
		Casetype: casetype, Casecollid: 0, Arg: arg,
		Args: args, Defresult: defresult, Location: -1,
	}, casetype, nil
}

// resolveCaseWhenCond resolves one WHEN condition. In the searched form
// (testExpr==nil) the condition must itself resolve to bool. In the simple form
// it is PG's expanded `operand = val`: an OpExpr whose left arg is the
// CaseTestExpr placeholder and right arg is the resolved val (transformCaseExpr →
// makeSimpleA_Expr "="). buildOpExpr requires an exact (operandType = valType)
// operator so the placeholder is never wrapped in a coercion — the shape
// ruleutils recognizes; any other combination degrades to SQL text.
func resolveCaseWhenCond(when parser.Expr, testExpr *CaseTestExpr, argType uint32, rec scopedResolve) (Node, error) {
	if testExpr == nil {
		cond, condType, err := rec(when, OidBool)
		if err != nil {
			return nil, err
		}
		if condType != OidBool {
			// coerce_to_boolean would apply in PG; keep the subset bounded.
			return nil, ErrUnsupported
		}
		return cond, nil
	}
	valNode, valType, err := rec(when, argType)
	if err != nil {
		return nil, err
	}
	op, opType, err := buildOpExpr(testExpr, argType, valNode, valType, "=")
	if err != nil {
		return nil, err
	}
	if opType != OidBool {
		return nil, ErrUnsupported
	}
	return op, nil
}

// operandTypmodCollid extracts exprTypmod/exprCollation from the operand node
// types this subset accepts as a simple-form CASE test operand (Var, Const). The
// CaseTestExpr placeholder carries both so a standby substitutes a value of the
// operand's exact declared type/collation; any other operand shape degrades to
// SQL text.
func operandTypmodCollid(n Node) (typmod int32, collid uint32, ok bool) {
	switch v := n.(type) {
	case *Var:
		return v.Vartypmod, v.Varcollid, true
	case *Const:
		return v.ConstTypmod, v.ConstCollid, true
	default:
		return 0, 0, false
	}
}

// buildOpExpr looks the operator up by spelling + already-resolved operand type
// OIDs (S0's catalog.LookupOperatorForNode) and constructs the canonical OpExpr
// with PG's collation rule (the only collatable type this subset emits is text,
// so inputcollid/opcollid are DEFAULT_COLLATION_OID when a text operand/result
// is involved, else 0). Shared by the scalar (resolver_expr.go) and query-scoped
// (resolver_query.go) resolvers so both build a byte-identical OpExpr.
func buildOpExpr(lNode Node, lType uint32, rNode Node, rType uint32, spelling string) (Node, uint32, error) {
	op, ok := catalog.LookupOperatorForNode(spelling, lType, rType)
	if !ok {
		return nil, 0, ErrUnsupported
	}
	inputcollid := uint32(0)
	if isCollatable(lType) || isCollatable(rType) {
		inputcollid = DefaultCollationOid
	}
	opcollid := uint32(0)
	if isCollatable(op.ResultType) {
		opcollid = DefaultCollationOid
	}
	return &OpExpr{
		Opno:         op.OID,
		Opfuncid:     op.Code,
		Opresulttype: op.ResultType,
		Opretset:     false,
		Opcollid:     opcollid,
		Inputcollid:  inputcollid,
		Args:         []Node{lNode, rNode},
		Location:     -1,
	}, op.ResultType, nil
}

// isCollatable reports whether a type OID carries a collation in the subset the
// resolver emits. Only text is collatable here; varchar/bpchar/name coercions
// are not part of this sub-slice.
func isCollatable(oid uint32) bool { return oid == OidText }
