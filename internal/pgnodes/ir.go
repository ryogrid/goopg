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
