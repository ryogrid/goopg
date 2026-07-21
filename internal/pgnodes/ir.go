// Package pgnodes implements a canonical PostgreSQL 18 pg_node_tree serializer
// and reader for the scalar-expression subset goopg needs so that a real PG18
// standby can EVALUATE goopg's stored user column DEFAULTs (pg_attrdef.adbin),
// extended-statistics expressions (pg_statistic_ext.stxexprs) and, later,
// view rewrite actions (pg_rewrite.ev_action).
//
// The text produced by Out is BYTE-IDENTICAL to PostgreSQL's nodeToString
// output (which is exactly what is stored in pg_attrdef.adbin): field order
// mirrors postgres/src/backend/nodes/outfuncs.c per tag, and the by-value /
// by-reference datum wire form mirrors outfuncs.c:outDatum. Read is the inverse
// (a pg_strtok / nodeRead mirror of read.c + readfuncs.c).
//
// Scope (M0123-S1): the scalar IR only — Const, FuncExpr, OpExpr, RelabelType,
// CoerceViaIO, SQLValueFunction. Query/RangeTblEntry/Var (view rewrite) land in
// S3. No resolver and no writer wiring yet; this slice is a pure codec with a
// golden round-trip gate against real-PG adbin strings (pgnodes_test.go).
//
// Design: docs/design/wal-pg-identical-stream/02e-content-fidelity-and-durability.md §3
// and docs/design/0123-0001-pgnodes-scalar-serializer.md.
package pgnodes

// Node is the interface implemented by every canonical pg_node_tree IR node.
// nodeTag returns the uppercase S-expression label PostgreSQL writes after the
// opening brace (e.g. "CONST"), matching WRITE_NODE_TYPE in outfuncs.c.
type Node interface {
	nodeTag() string
}

// Const mirrors postgres/src/include/nodes/primnodes.h struct Const. Only the
// fields serialized by _outConst are represented. Datum holds the raw wire
// bytes of constvalue (see datum.go): for by-value types it is always the
// 8-byte little-endian Datum word PostgreSQL emits regardless of constlen; for
// by-reference types it is the full in-memory varlena (4-byte header + data).
// Datum is ignored when ConstIsNull is true.
type Const struct {
	ConstType   uint32 // consttype  (type OID)
	ConstTypmod int32  // consttypmod
	ConstCollid uint32 // constcollid (collation OID; 100 = default for text)
	ConstLen    int32  // constlen    (typlen; -1 for varlena)
	ConstByval  bool   // constbyval
	ConstIsNull bool   // constisnull
	Location    int32  // location    (parse location; -1 when normalized)
	Datum       []byte // constvalue wire bytes (see comment above)
}

func (*Const) nodeTag() string { return "CONST" }

// FuncExpr mirrors _outFuncExpr in outfuncs.funcs.c.
type FuncExpr struct {
	Funcid         uint32 // funcid
	Funcresulttype uint32 // funcresulttype
	Funcretset     bool   // funcretset
	Funcvariadic   bool   // funcvariadic
	Funcformat     int32  // funcformat (CoercionForm enum)
	Funccollid     uint32 // funccollid
	Inputcollid    uint32 // inputcollid
	Args           []Node // args
	Location       int32  // location
}

func (*FuncExpr) nodeTag() string { return "FUNCEXPR" }

// OpExpr mirrors _outOpExpr in outfuncs.funcs.c.
type OpExpr struct {
	Opno         uint32 // opno
	Opfuncid     uint32 // opfuncid
	Opresulttype uint32 // opresulttype
	Opretset     bool   // opretset
	Opcollid     uint32 // opcollid
	Inputcollid  uint32 // inputcollid
	Args         []Node // args
	Location     int32  // location
}

func (*OpExpr) nodeTag() string { return "OPEXPR" }

// DistinctExpr mirrors _outDistinctExpr in outfuncs.funcs.c. PG relies on
// "DistinctExpr and OpExpr being same struct" (make_distinct_op just
// NodeSetTag(result, T_DistinctExpr) on a make_op OpExpr), so the field list —
// and hence the codec — is byte-identical to OpExpr; only the WRITE_NODE_TYPE
// token differs ("DISTINCTEXPR"). We reproduce that relationship with a defined
// type over OpExpr. `a IS DISTINCT FROM b` resolves the `=` operator for the
// operand types (opresulttype must be bool) and tags the node DISTINCTEXPR;
// `a IS NOT DISTINCT FROM b` is that DistinctExpr wrapped in a NOT BoolExpr
// (transformAExprDistinct: makeBoolExpr(NOT_EXPR, [DistinctExpr])).
type DistinctExpr OpExpr

func (*DistinctExpr) nodeTag() string { return "DISTINCTEXPR" }

// RelabelType mirrors _outRelabelType in outfuncs.funcs.c. It is a
// binary-compatible type coercion (e.g. int4 literal -> oid).
type RelabelType struct {
	Arg           Node   // arg
	Resulttype    uint32 // resulttype
	Resulttypmod  int32  // resulttypmod
	Resultcollid  uint32 // resultcollid
	Relabelformat int32  // relabelformat (CoercionForm enum)
	Location      int32  // location
}

func (*RelabelType) nodeTag() string { return "RELABELTYPE" }

// CoerceViaIO mirrors _outCoerceViaIO in outfuncs.funcs.c. It is an I/O-based
// type coercion (call the source type's output function, then the target
// type's input function).
type CoerceViaIO struct {
	Arg          Node   // arg
	Resulttype   uint32 // resulttype
	Resultcollid uint32 // resultcollid
	Coerceformat int32  // coerceformat (CoercionForm enum)
	Location     int32  // location
}

func (*CoerceViaIO) nodeTag() string { return "COERCEVIAIO" }

// SQLValueFunction mirrors _outSQLValueFunction in outfuncs.funcs.c. It is a
// keyword-spelled runtime value such as CURRENT_TIMESTAMP or CURRENT_USER.
type SQLValueFunction struct {
	Op       int32  // op (SQLValueFunctionOp enum)
	Type     uint32 // type (result type OID)
	Typmod   int32  // typmod
	Location int32  // location
}

func (*SQLValueFunction) nodeTag() string { return "SQLVALUEFUNCTION" }

// BoolExprType enum values mirror postgres/src/include/nodes/primnodes.h
// (AND_EXPR, OR_EXPR, NOT_EXPR). _outBoolExpr writes these as the bare tokens
// "and"/"or"/"not" (a do-it-yourself enum representation), NOT as integers.
const (
	BoolExprAnd int32 = iota // AND_EXPR
	BoolExprOr               // OR_EXPR
	BoolExprNot              // NOT_EXPR
)

// BoolExpr mirrors _outBoolExpr in outfuncs.c (a custom_read_write node). AND/OR
// are n-ary: PG's makeAndExpr/makeOrExpr flatten a left-nested chain of the same
// operator (`a AND b AND c`) into ONE BoolExpr with three args, so the resolver
// reproduces that flattening. NOT is always a single-arg BoolExpr.
type BoolExpr struct {
	Boolop   int32  // boolop (BoolExprType: 0=and, 1=or, 2=not)
	Args     []Node // args
	Location int32  // location
}

func (*BoolExpr) nodeTag() string { return "BOOLEXPR" }

// NullTestType enum values mirror postgres/src/include/nodes/primnodes.h
// (IS_NULL, IS_NOT_NULL).
const (
	IsNull    int32 = iota // IS_NULL
	IsNotNull              // IS_NOT_NULL
)

// NullTest mirrors _outNullTest (generated outfuncs.funcs.c): arg, nulltesttype,
// argisrow, location. The result is always boolean. argisrow is false for the
// scalar subset (a row-valued IS NULL sets it true, which this slice does not
// emit).
type NullTest struct {
	Arg          Node  // arg
	NullTestType int32 // nulltesttype (0=IS NULL, 1=IS NOT NULL)
	ArgIsRow     bool  // argisrow
	Location     int32 // location
}

func (*NullTest) nodeTag() string { return "NULLTEST" }

// BoolTestType enum values mirror postgres/src/include/nodes/primnodes.h
// (IS_TRUE, IS_NOT_TRUE, IS_FALSE, IS_NOT_FALSE, IS_UNKNOWN, IS_NOT_UNKNOWN).
// _outBooleanTest writes booltesttype as the plain integer ordinal (a
// WRITE_ENUM_FIELD), not a token — unlike BoolExpr's :boolop.
const (
	IsTrue       int32 = iota // IS_TRUE
	IsNotTrue                 // IS_NOT_TRUE
	IsFalse                   // IS_FALSE
	IsNotFalse                // IS_NOT_FALSE
	IsUnknown                 // IS_UNKNOWN
	IsNotUnknown              // IS_NOT_UNKNOWN
)

// BooleanTest mirrors _outBooleanTest (generated outfuncs.funcs.c): arg,
// booltesttype, location. The argument is always boolean and the result is
// always boolean (never NULL). This is the `expr IS [NOT] TRUE/FALSE/UNKNOWN`
// node — distinct from NullTest (`IS [NOT] NULL`).
type BooleanTest struct {
	Arg          Node  // arg (a boolean-valued expression)
	BoolTestType int32 // booltesttype (0=IS TRUE … 5=IS NOT UNKNOWN)
	Location     int32 // location
}

func (*BooleanTest) nodeTag() string { return "BOOLEANTEST" }

// CaseExpr mirrors _outCaseExpr (generated outfuncs.funcs.c): casetype,
// casecollid, arg, args, defresult, location. This is the SQL `CASE` node.
//
// Both forms are modeled. The *searched* form (`CASE WHEN cond THEN result …
// [ELSE result] END`) leaves Arg nil. The *simple* form (`CASE operand WHEN
// val …`) sets Arg to the resolved operand and rewrites each WHEN into an
// `operand = val` OpExpr whose left arg is a CaseTestExpr placeholder typed from
// the operand (transformCaseExpr in parse_expr.c: it substitutes the operand for
// every placeholder at evaluation time). The deparse inverse (ruleutils
// get_rule_expr) recognizes that shape and prints just the RHS of each OpExpr.
//
// casetype is the common result type across defresult + every WHEN result
// (select_common_type); this subset only emits canonical bytes when all of
// them resolve to the *same* non-collatable type, so casecollid is always 0.
// Args holds *CaseWhen nodes; when ELSE is omitted PG synthesizes a typed NULL
// Const of casetype as Defresult (transformCaseExpr coerces an untyped NULL
// A_Const to the common type).
type CaseExpr struct {
	Casetype   uint32 // casetype   (common result type OID)
	Casecollid uint32 // casecollid (0 for the non-collatable subset)
	Arg        Node   // arg        (nil for the searched form)
	Args       []Node // args       (list of *CaseWhen)
	Defresult  Node   // defresult  (ELSE result, or a typed NULL Const)
	Location   int32  // location
}

func (*CaseExpr) nodeTag() string { return "CASEEXPR" }

// CaseWhen mirrors _outCaseWhen: expr (the boolean condition), result (its
// value), location. One `WHEN cond THEN result` arm inside a CaseExpr.
type CaseWhen struct {
	Expr     Node  // expr   (boolean condition)
	Result   Node  // result (arm value, coerced to casetype)
	Location int32 // location
}

func (*CaseWhen) nodeTag() string { return "CASEWHEN" }

// CaseTestExpr mirrors _outCaseTestExpr: typeId, typeMod, collation. It is the
// placeholder PG substitutes the simple-form CASE operand into (parse_expr.c
// transformCaseExpr sets typeId/typeMod/collation from exprType/exprTypmod/
// exprCollation of the operand). It appears only as the left arg of the
// `operand = val` OpExpr inside each simple-form CaseWhen; it never stands alone.
type CaseTestExpr struct {
	Typeid    uint32 // typeId    (operand type OID)
	Typemod   int32  // typeMod   (operand typmod)
	Collation uint32 // collation (operand collation OID)
}

func (*CaseTestExpr) nodeTag() string { return "CASETESTEXPR" }
