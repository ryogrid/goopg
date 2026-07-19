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
	return ResolveForColumnTypmod(e, targetType, -1)
}

// ResolveForColumnTypmod is ResolveForColumn with the column's packed typmod. It
// exists because a top-level `::numeric(p,s)` length coercion (funcid 1703) is
// byte-identical to PG's stored adbin ONLY when the column's own typmod equals the
// cast's: PG's build_column_default coerces the stored default to the attribute's
// (type, typmod), and when the cast typmod differs from the column typmod (the
// common case being a bare `numeric` column, typmod -1) PG wraps the coercion in a
// RelabelType that re-labels to the column typmod — a node this slice does not
// model. So a top-level numeric length coercion whose embedded typmod ≠ targetTypmod
// degrades to SQL text (all-or-nothing; 02e §3). targetTypmod is -1 for a column with
// no length qualifier (every non-numeric-typmod caller via ResolveForColumn), which
// naturally rejects a `::numeric(p,s)` default on a bare `numeric` column.
func ResolveForColumnTypmod(e parser.Expr, targetType uint32, targetTypmod int32) (Node, bool) {
	n, typ, err := resolve(e, targetType)
	if err != nil || typ != targetType {
		return nil, false
	}
	if f, ok := n.(*FuncExpr); ok {
		if castTypmod, isNumericLenCoerce := numericCastPackedTypmod(f); isNumericLenCoerce && castTypmod != targetTypmod {
			return nil, false
		}
	}
	return n, true
}

// NumericColumnTypmod packs a numeric column's (precision[, scale]) type arguments
// into PG's atttypmod, or returns -1 when the column carries no valid numeric
// length qualifier (bare `numeric`, or arguments outside numerictypmodin's range).
// The writer passes this to ResolveForColumnTypmod so a `::numeric(p,s)` DEFAULT is
// emitted canonically only for a matching numeric(p,s) column.
func NumericColumnTypmod(args []int64) int32 {
	if tm, ok := numericTypmodValue(args); ok {
		return tm
	}
	return -1
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

	case *parser.CastExpr:
		return resolveCastExpr(v)

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

// resolveCastExpr resolves an explicit `expr::type` cast. Scope: PG's numeric type
// category — the exact integers (int2/int4/int8), arbitrary-precision numeric (OID
// 1700), AND the binary floats (float4/float8, OIDs 700/701). PG's coerce_type
// applies the cast to the operand at its NATURAL type — a cast to the operand's
// own type is a no-op (coerce_type returns the node unchanged, so `5::int4` stays a
// bare int4 Const and `9999999999::int8` a bare int8 Const), and a genuine
// conversion becomes a COERCE_EXPLICIT_CAST (funcformat 1) FuncExpr naming the
// pg_cast conversion function (e.g. int8(int4) OID 481, numeric_int4 OID 1744,
// float8(int4) OID 316, numeric_float8 OID 1746). This is the funcformat-1 sibling
// of the implicit-cast helpers (wrapInt4ToInt8Cast / wrapIntToNumericCast /
// wrapToFloat8Cast, funcformat 2): same OIDs, different funcformat, because an
// explicit `::type` keeps its cast node in adbin while an implicit coercion PG
// re-derives from context. Any target outside {int2,int4,int8,numeric,float4,
// float8}, a typmod-qualified target (`::numeric(10,2)` — the length coercion is a
// distinct numeric(numeric,int4) call not modeled here), or a source PG models with
// a different cast method (RelabelType / IO coercion / const-fold) degrades to SQL
// text (all-or-nothing; 02e §3). The operand is resolved with an unknown context
// (expected=0) so its own magnitude typing decides int4-vs-int8-vs-numeric (a
// literal never types directly as float — a float source only arises via a nested
// `(x::floatT)::T`) before the cast is applied.
func resolveCastExpr(v *parser.CastExpr) (Node, uint32, error) {
	if v.Type.Schema != "" && v.Type.Schema != "pg_catalog" {
		return nil, 0, ErrUnsupported
	}
	targetOID := catalog.TypeNameToOID(v.Type.Name)
	if len(v.Typmods) != 0 {
		// Only a typmod-qualified numeric target (`::numeric(p[,s])`) is modeled:
		// PG's coerce_to_target_type follows the base int→numeric cast with a
		// length coercion numeric(numeric, int4). Every other typmod'd target
		// (varchar(N), timestamp(N), …) uses a different coercion node (CoerceViaIO
		// / a length-cast func with a different OID) and degrades to SQL text.
		if targetOID != OidNumeric {
			return nil, 0, ErrUnsupported
		}
		return resolveNumericTypmodCast(v)
	}
	if !isNumericFamilyType(targetOID) {
		return nil, 0, ErrUnsupported // only int2/int4/int8/numeric casts modeled here
	}
	arg, srcType, err := resolve(v.Operand, 0)
	if err != nil {
		return nil, 0, err
	}
	if srcType == targetOID {
		// A cast to the operand's own type is a no-op; PG emits no RelabelType or
		// FuncExpr for an exact type match, just the inner node.
		return arg, srcType, nil
	}
	funcid, resultType, ok := numericFamilyCastFuncid(srcType, targetOID)
	if !ok {
		return nil, 0, ErrUnsupported
	}
	return &FuncExpr{
		Funcid:         funcid,
		Funcresulttype: resultType,
		Funcretset:     false,
		Funcvariadic:   false,
		Funcformat:     1, // COERCE_EXPLICIT_CAST
		Funccollid:     0,
		Inputcollid:    0,
		Args:           []Node{arg},
		Location:       -1,
	}, resultType, nil
}

// resolveNumericTypmodCast resolves an explicit `expr::numeric(p[,s])` cast — a
// numeric target carrying a length typmod (sub-slice 22). PG's
// coerce_to_target_type first coerces the operand to bare numeric (coerce_type),
// then applies a length coercion numeric(numeric, int4) = funcid 1703 whose second
// argument is an int4 Const holding the packed typmod. The result:
//
//	FuncExpr 1703 (funcformat 1, EXPLICIT)
//	  ├─ arg0: operand coerced to numeric — an integer operand wraps in
//	  │        int4_numeric/int8_numeric (funcformat 2, IMPLICIT, via
//	  │        wrapIntToNumericCast); a numeric operand passes through unchanged
//	  └─ arg1: Const int4 = numerictypmodin(p,s)
//
// This is the byte shape PG stores when the DEFAULT's column typmod MATCHES the
// cast typmod (e.g. col numeric(10,2) DEFAULT (5::numeric(10,2))). When the column
// is bare `numeric` (typmod -1), PG wraps the whole thing in a RelabelType that
// strips the typmod back to the column's — that RelabelType node is not modeled
// here (ResolveForColumn sees only the column's type OID, not its typmod), so a
// typmod-mismatched column degrades to SQL text (all-or-nothing; 02e §3). Sources
// other than int4/int8/numeric (int2, the binary floats) reach numeric through a
// different helper set (int2_numeric=1782, float4_numeric=1742, float8_numeric=1743)
// not wired here and also degrade.
func resolveNumericTypmodCast(v *parser.CastExpr) (Node, uint32, error) {
	typmod, ok := numericTypmodValue(v.Typmods)
	if !ok {
		return nil, 0, ErrUnsupported
	}
	arg, srcType, err := resolve(v.Operand, 0)
	if err != nil {
		return nil, 0, err
	}
	var numericArg Node
	switch srcType {
	case OidNumeric:
		numericArg = arg // already numeric — coerce_type is a no-op
	case OidInt4, OidInt8:
		numericArg = wrapIntToNumericCast(arg, srcType) // implicit int→numeric (funcformat 2)
	default:
		return nil, 0, ErrUnsupported
	}
	return &FuncExpr{
		Funcid:         1703, // numeric(numeric, int4) length coercion
		Funcresulttype: OidNumeric,
		Funcretset:     false,
		Funcvariadic:   false,
		Funcformat:     1, // COERCE_EXPLICIT_CAST
		Funccollid:     0,
		Inputcollid:    0,
		Args:           []Node{numericArg, NewInt4Const(typmod)},
		Location:       -1,
	}, OidNumeric, nil
}

// numericTypmodValue reproduces PG's numerictypmodin (numeric.c): for numeric(p)
// scale defaults to 0, and the packed typmod is ((p << 16) | (s & 0x7ff)) +
// VARHDRSZ, where VARHDRSZ = 4. Precision must be 1..NUMERIC_MAX_PRECISION (1000).
// This slice accepts only the common 0 <= scale <= precision subset — a strict
// subset of PG's valid range (PG18 also permits negative and over-precision scale),
// so anything accepted here yields the identical typmod PG would compute; a scale
// outside the subset degrades (conservative all-or-nothing). Returns false for an
// arity numerictypmodin itself rejects (0 or >2 components).
func numericTypmodValue(typmods []int64) (int32, bool) {
	var prec, scale int64
	switch len(typmods) {
	case 1:
		prec, scale = typmods[0], 0
	case 2:
		prec, scale = typmods[0], typmods[1]
	default:
		return 0, false
	}
	if prec < 1 || prec > 1000 || scale < 0 || scale > prec {
		return 0, false
	}
	return int32(((prec << 16) | (scale & 0x7ff)) + 4), true
}

// isNumericFamilyType reports whether oid is one of the numeric-family types this
// slice models as an explicit-cast target: the exact integers (int2/int4/int8),
// arbitrary-precision numeric, AND the binary floats (float4/float8). All six are
// members of PG's TYPCATEGORY_NUMERIC, and every ordered pair among them has a
// pg_cast conversion function (castmethod 'f'), so any `expr::T` between them is a
// COERCE_EXPLICIT_CAST FuncExpr (sub-slice 21 added the float arms to the
// int/numeric core of sub-slices 19/20).
func isNumericFamilyType(oid uint32) bool {
	return oid == OidInt2 || oid == OidInt4 || oid == OidInt8 || oid == OidNumeric ||
		oid == OidFloat4 || oid == OidFloat8
}

// numericFamilyCastFuncid returns the pg_cast conversion-function OID (castmethod
// 'f') and its result type for an explicit cast between numeric-family types, and
// false for any pair outside {int2,int4,int8,numeric,float4,float8}² this slice
// models. A literal source is int4, int8, or numeric (make_const magnitude typing
// never yields int2 or a float directly, but a nested `(x::int2)::T` /
// `(x::float8)::T` can), so all six widths are covered as sources — the float and
// int2 source arms are reachable only through such nesting. Every OID is from
// pg_proc.dat / pg_cast.dat:
//
//	int→int:      int2(int4)=314, int8(int4)=481, int4(int8)=480, int2(int8)=714
//	int→numeric:  int2_numeric=1782, int4_numeric=1740, int8_numeric=1781
//	numeric→int:  numeric_int2=1783, numeric_int4=1744, numeric_int8=1779
//	int→float4:   float4(int2)=236, float4(int4)=318, float4(int8)=652
//	int→float8:   float8(int2)=235, float8(int4)=316, float8(int8)=482
//	numeric↔float: numeric_float4=1745, numeric_float8=1746,
//	              float4_numeric=1742, float8_numeric=1743
//	float↔float:  float8(float4)=311, float4(float8)=312
//	float4→int:   int2(float4)=238, int4(float4)=319, int8(float4)=653
//	float8→int:   int2(float8)=237, int4(float8)=317, int8(float8)=483
//
// The float8(float4)=311 / int→float8 / numeric_float8 OIDs are shared with the
// IMPLICIT CASE-coercion helpers (wrapFloat4ToFloat8Cast / wrapToFloat8Cast,
// funcformat 2); here they carry funcformat 1 because the `::type` is explicit —
// the funcformat distinguishes the two on the rebuild path (explicitCastTypeName
// vs isImplicit*).
func numericFamilyCastFuncid(fromType, toType uint32) (funcid, resultType uint32, ok bool) {
	switch {
	// integer → integer
	case fromType == OidInt4 && toType == OidInt2:
		return 314, OidInt2, true
	case fromType == OidInt4 && toType == OidInt8:
		return 481, OidInt8, true
	case fromType == OidInt8 && toType == OidInt4:
		return 480, OidInt4, true
	case fromType == OidInt8 && toType == OidInt2:
		return 714, OidInt2, true
	// integer → numeric
	case fromType == OidInt2 && toType == OidNumeric:
		return 1782, OidNumeric, true
	case fromType == OidInt4 && toType == OidNumeric:
		return 1740, OidNumeric, true
	case fromType == OidInt8 && toType == OidNumeric:
		return 1781, OidNumeric, true
	// numeric → integer
	case fromType == OidNumeric && toType == OidInt2:
		return 1783, OidInt2, true
	case fromType == OidNumeric && toType == OidInt4:
		return 1744, OidInt4, true
	case fromType == OidNumeric && toType == OidInt8:
		return 1779, OidInt8, true
	// integer → float4
	case fromType == OidInt2 && toType == OidFloat4:
		return 236, OidFloat4, true
	case fromType == OidInt4 && toType == OidFloat4:
		return 318, OidFloat4, true
	case fromType == OidInt8 && toType == OidFloat4:
		return 652, OidFloat4, true
	// integer → float8
	case fromType == OidInt2 && toType == OidFloat8:
		return 235, OidFloat8, true
	case fromType == OidInt4 && toType == OidFloat8:
		return 316, OidFloat8, true
	case fromType == OidInt8 && toType == OidFloat8:
		return 482, OidFloat8, true
	// numeric ↔ float
	case fromType == OidNumeric && toType == OidFloat4:
		return 1745, OidFloat4, true
	case fromType == OidNumeric && toType == OidFloat8:
		return 1746, OidFloat8, true
	case fromType == OidFloat4 && toType == OidNumeric:
		return 1742, OidNumeric, true
	case fromType == OidFloat8 && toType == OidNumeric:
		return 1743, OidNumeric, true
	// float ↔ float
	case fromType == OidFloat4 && toType == OidFloat8:
		return 311, OidFloat8, true
	case fromType == OidFloat8 && toType == OidFloat4:
		return 312, OidFloat4, true
	// float4 → integer
	case fromType == OidFloat4 && toType == OidInt2:
		return 238, OidInt2, true
	case fromType == OidFloat4 && toType == OidInt4:
		return 319, OidInt4, true
	case fromType == OidFloat4 && toType == OidInt8:
		return 653, OidInt8, true
	// float8 → integer
	case fromType == OidFloat8 && toType == OidInt2:
		return 237, OidInt2, true
	case fromType == OidFloat8 && toType == OidInt4:
		return 317, OidInt4, true
	case fromType == OidFloat8 && toType == OidInt8:
		return 483, OidInt8, true
	default:
		return 0, 0, false
	}
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

// wrapInt4ToInt8Cast builds the implicit int4->int8 width-coercion FuncExpr that
// PG's coerce_type emits for an int4 result promoted to a common int8 type (see
// pg_cast.dat: castsource int4 -> casttarget int8, castfunc int8(int4) OID 481,
// castcontext 'i', castmethod 'f'). funcresulttype int8; funcformat =
// COERCE_IMPLICIT_CAST (2); no collation. The int8 side never needs a cast.
func wrapInt4ToInt8Cast(arg Node) Node {
	return &FuncExpr{
		Funcid:         481, // int8(int4)
		Funcresulttype: OidInt8,
		Funcretset:     false,
		Funcvariadic:   false,
		Funcformat:     2, // COERCE_IMPLICIT_CAST
		Funccollid:     0,
		Inputcollid:    0,
		Args:           []Node{arg},
		Location:       -1,
	}
}

// wrapFloat4ToFloat8Cast builds the implicit float4->float8 width-coercion
// FuncExpr that PG's coerce_type emits for a float4 result promoted to a common
// float8 type (see pg_cast.dat: castsource float4 -> casttarget float8, castfunc
// float8(float4) OID 311 / prosrc ftod, castcontext 'i', castmethod 'f').
// funcresulttype float8; funcformat = COERCE_IMPLICIT_CAST (2); no collation. The
// float8 side never needs a cast.
func wrapFloat4ToFloat8Cast(arg Node) Node {
	return &FuncExpr{
		Funcid:         311, // float8(float4)
		Funcresulttype: OidFloat8,
		Funcretset:     false,
		Funcvariadic:   false,
		Funcformat:     2, // COERCE_IMPLICIT_CAST
		Funccollid:     0,
		Inputcollid:    0,
		Args:           []Node{arg},
		Location:       -1,
	}
}

// wrapToFloat8Cast builds the implicit <numeric-family>->float8 coercion FuncExpr
// PG's coerce_type emits for an int4/int8/numeric CASE result promoted to a
// common float8 type (float8 is TYPCATEGORY_NUMERIC's preferred type, so it wins
// whenever a float8 result is present in the CASE). The cast function is chosen
// by the source type per pg_cast.dat (all castcontext 'i' implicit, castmethod
// 'f'): float8(int4) 316, float8(int8) 482, float8(numeric) 1746. funcresulttype
// float8; funcformat = COERCE_IMPLICIT_CAST (2); no collation. The float8 side
// never needs a cast; float4->float8 has its own helper (wrapFloat4ToFloat8Cast,
// funcid 311). The caller (coerceCaseResult) guarantees a supported source type.
func wrapToFloat8Cast(arg Node, fromType uint32) Node {
	var funcid uint32
	switch fromType {
	case OidInt4:
		funcid = 316 // float8(int4)
	case OidInt8:
		funcid = 482 // float8(int8)
	case OidNumeric:
		funcid = 1746 // float8(numeric)
	default:
		return nil
	}
	return &FuncExpr{
		Funcid:         funcid,
		Funcresulttype: OidFloat8,
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
// Result types need NOT be identical: PG's transformCaseExpr runs
// select_common_type over the WHEN results plus any ELSE, then coerce_to_common_
// type inserts a cast on each mismatched result. This models two bounded numeric
// families (see selectCaseCommonType / coerceCaseResult): a mix drawn from the
// exact-integer/numeric family {int4,int8,numeric} folds to its widest member
// (numeric > int8 > int4), and a mix drawn from the binary-float family
// {float4,float8} folds to float8 — each narrower result wrapped in the exact
// implicit-cast FuncExpr (int_numeric 1740/1781, int8(int4) 481, or float8(float4)
// 311). A cross-family mix, or a collatable common type, returns ErrUnsupported so
// the writer degrades to SQL text (all-or-nothing).
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
	// Resolve all conditions + results first (un-coerced), collecting the result
	// types so the common type can be selected the way transformCaseExpr does.
	type arm struct {
		cond    Node
		res     Node
		resType uint32
	}
	var (
		arms        []arm
		resultTypes []uint32
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
		arms = append(arms, arm{cond, res, resType})
		resultTypes = append(resultTypes, resType)
	}
	var (
		elseRes  Node
		elseType uint32
		hasElse  bool
	)
	if e.Else != nil {
		res, resType, err := rec(e.Else, 0)
		if err != nil {
			return nil, 0, err
		}
		elseRes, elseType, hasElse = res, resType, true
		resultTypes = append(resultTypes, resType)
	}

	casetype, ok := selectCaseCommonType(resultTypes)
	if !ok {
		return nil, 0, ErrUnsupported // unmodeled cross-type mix — deferred
	}
	constlen, constbyval, ok := caseTypeMeta(casetype)
	if !ok {
		return nil, 0, ErrUnsupported // unmodeled or collatable casetype
	}

	// coerce_to_common_type: wrap each result in the cast to casetype, if needed.
	args := make([]Node, 0, len(arms))
	for _, a := range arms {
		res, err := coerceCaseResult(a.res, a.resType, casetype)
		if err != nil {
			return nil, 0, err
		}
		args = append(args, &CaseWhen{Expr: a.cond, Result: res, Location: -1})
	}
	var defresult Node
	if hasElse {
		res, err := coerceCaseResult(elseRes, elseType, casetype)
		if err != nil {
			return nil, 0, err
		}
		defresult = res
	} else {
		// ELSE omitted → PG synthesizes a typed NULL Const of casetype.
		defresult = newNullConst(casetype, constlen, constbyval)
	}
	return &CaseExpr{
		Casetype: casetype, Casecollid: 0, Arg: arg,
		Args: args, Defresult: defresult, Location: -1,
	}, casetype, nil
}

// selectCaseCommonType models the subset of PG's select_common_type
// (parse_coerce.c) that this canonicalizer supports for CASE results. When every
// result already shares one type, that type is returned unchanged. A genuine mix
// is modeled across PG's numeric type category (TYPCATEGORY_NUMERIC 'N') —
// {int4,int8,numeric,float4,float8} — which is exactly the set select_common_type
// converges over via its preferred-type walk: a narrower type implicitly coerces
// to a wider one but not the reverse, and float8 is the category's PREFERRED
// type, so it wins whenever any float8 result is present. Any type outside this
// set (or a collatable/other-category type) returns false and the CASE degrades
// to SQL text (all-or-nothing).
//
// The reachable common types are int4 < int8 < numeric < float8; a float4 result
// WITHOUT any float8 result would make float4 the common type (float4 beats
// numeric/int8/int4 in the walk) and PG then wraps the whole CASE in an outer
// float8(float4) column cast — this canonicalizer has no int/numeric->float4
// implicit-cast arm and emits no outer column cast, so that mix returns false and
// degrades. Every accepted common type therefore has a coerceCaseResult arm for
// each member present.
func selectCaseCommonType(types []uint32) (uint32, bool) {
	if len(types) == 0 {
		return 0, false
	}
	allSame := true
	for _, t := range types[1:] {
		if t != types[0] {
			allSame = false
			break
		}
	}
	if allSame {
		return types[0], true
	}
	hasInt8, hasNumeric, hasFloat4, hasFloat8 := false, false, false, false
	for _, t := range types {
		switch t {
		case OidInt4:
		case OidInt8:
			hasInt8 = true
		case OidNumeric:
			hasNumeric = true
		case OidFloat4:
			hasFloat4 = true
		case OidFloat8:
			hasFloat8 = true
		default:
			// Outside the numeric category (or a collatable type): unmodeled.
			return 0, false
		}
	}
	switch {
	case hasFloat8:
		// float8 is the numeric category's preferred type, so PG's walk stays on
		// it whenever any result is float8 — every other member coerces up to it.
		return OidFloat8, true
	case hasFloat4:
		// float4 common type (float4 present, no float8): PG would wrap the whole
		// CASE in an outer float8(float4) column cast; unmodeled -> degrade.
		return 0, false
	case hasNumeric:
		return OidNumeric, true
	case hasInt8:
		return OidInt8, true
	default:
		// All members are int4 (a mix drawn only from {int4} would have been
		// all-same); this arm keeps the switch total.
		return OidInt4, true
	}
}

// coerceCaseResult applies PG's coerce_to_common_type to one CASE result. It is
// the identity when the result already has the common type; otherwise it wraps
// the narrower result in the exact implicit-cast FuncExpr PG stores un-const-
// folded: int4/int8 → numeric via int4_numeric (1740) / int8_numeric (1781),
// int4 → int8 via int8(int4) (481), float4 → float8 via float8(float4) (311),
// and int4/int8/numeric → float8 via float8(int4)/float8(int8)/float8(numeric)
// (316/482/1746). No other coercion is modeled — the only mismatches
// selectCaseCommonType can yield are these narrowing→widening cases within PG's
// numeric type category, so this stays total for the accepted subset; anything
// else returns ErrUnsupported.
func coerceCaseResult(node Node, fromType, toType uint32) (Node, error) {
	if fromType == toType {
		return node, nil
	}
	if toType == OidNumeric && (fromType == OidInt4 || fromType == OidInt8) {
		return wrapIntToNumericCast(node, fromType), nil
	}
	if toType == OidInt8 && fromType == OidInt4 {
		return wrapInt4ToInt8Cast(node), nil
	}
	if toType == OidFloat8 && fromType == OidFloat4 {
		return wrapFloat4ToFloat8Cast(node), nil
	}
	if toType == OidFloat8 && (fromType == OidInt4 || fromType == OidInt8 || fromType == OidNumeric) {
		return wrapToFloat8Cast(node, fromType), nil
	}
	return nil, ErrUnsupported
}

// resolveCaseWhenCond resolves one WHEN condition. In the searched form
// (testExpr==nil) the condition must itself resolve to bool. In the simple form
// it is PG's expanded `operand = val`: an OpExpr whose left arg is the
// CaseTestExpr placeholder and right arg is the resolved val (transformCaseExpr →
// makeSimpleA_Expr "=" → make_op).
//
// The value is resolved with its NATURAL type (no coercion context), then PG's
// make_op two-phase operator resolution is modeled:
//
//  1. If a native "=" operator matches (operandType, valType) directly — including
//     a cross-type integer operator such as int8=int4 (opno 416, int84eq) — PG's
//     oper() uses it with the value LEFT UN-COERCED. This is the exact/binary-
//     compatible match phase.
//  2. Otherwise PG coerces the value up to the operand type via
//     coerce_to_common_type and selects the exact operator on the operand type —
//     e.g. a numeric operand with an int4 value has no numeric=int4 operator, so
//     the value is wrapped in int4_numeric (funcid 1740, funcformat 2) and
//     numeric_eq (opno 1752) applies (sub-slice 17 goldens
//     simple_numeric_operand_int_when_coerce*).
//
// The CaseTestExpr placeholder (the operator's left arg) is never itself wrapped
// in a coercion — the shape ruleutils recognizes — so a pair that would require
// coercing the operand (or a value coercion coerceCaseResult does not model)
// degrades to SQL text rather than emitting a divergent tree.
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
	valNode, valType, err := rec(when, 0)
	if err != nil {
		return nil, err
	}
	if _, native := catalog.LookupOperatorForNode("=", argType, valType); !native {
		// Phase 2: no native (operand, value) operator — coerce the value up to
		// the operand type, then require the exact operator on the operand type.
		coerced, cerr := coerceCaseResult(valNode, valType, argType)
		if cerr != nil {
			return nil, cerr
		}
		valNode, valType = coerced, argType
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
	case *FuncExpr:
		// A function/cast operand (e.g. an explicit `5::int8` = int8(int4)) carries
		// no typmod — exprTypmod(T_FuncExpr) is always -1 in PG — and its collation
		// is exprCollation == funccollid. transformCaseExpr builds the CaseTestExpr
		// from exactly these, so a simple-form CASE over a cast/function operand
		// emits a canonical placeholder rather than degrading.
		return -1, v.Funccollid, true
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
