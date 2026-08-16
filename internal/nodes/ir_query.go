package nodes

// This file adds the query-tree subset of the canonical pg_node_tree IR needed
// to serialize a view rewrite action (pg_rewrite.ev_action) so a real PG18
// standby can plan/execute goopg's stored user views. It complements the scalar
// IR in ir.go. Scope (M0123-S3 sub-slice 1): the single-base-relation SELECT
// view shape only — one RTE_RELATION range-table entry, an optional WHERE qual,
// and a flat target list of Vars / scalar expressions. That covers views such
// as `CREATE VIEW v AS SELECT a, b FROM t WHERE a > 0`.
//
// The bytes produced by Out for these tags are BYTE-IDENTICAL to PostgreSQL 18's
// nodeToString output (verified in query_roundtrip_test.go against live-captured
// ev_action goldens). Field order per tag mirrors postgres/src/backend/nodes/
// outfuncs.c (and the generated outfuncs.funcs.c). Read is the inverse and
// doubles as the "is this a supported canonical view?" gate: the many Query
// fields that must stay at their view-default values are validated on Read, and
// any deviation (aggregates, sublinks, joins, etc.) is reported as an error —
// exactly the caller's signal to fall back to storing the view as SQL text.
//
// Design: docs/design/0123-0004-pgnodes-query-serializer.md.

// Bitmapset mirrors a PostgreSQL Bitmapset serialized as "(b m0 m1 ...)" (an
// empty set is "(b)"). Members are the raw serialized integers exactly as they
// appear on the wire; any producer-side offset encoding (e.g. selectedCols
// stores attno - FirstLowInvalidHeapAttributeNumber) is the resolver's concern,
// not the codec's.
type Bitmapset []int32

// Alias mirrors _outAlias: aliasname (a WRITE_STRING_FIELD char*) and colnames
// (a List of String value nodes, each written quoted as "name").
type Alias struct {
	Aliasname string
	Colnames  []string
}

func (*Alias) nodeTag() string { return "ALIAS" }

// Var mirrors _outVar (primnodes.h struct Var). For the supported view shape
// varlevelsup is always 0 and varnullingrels is always empty, but every field is
// modeled so the codec round-trips faithfully.
type Var struct {
	Varno            int32     // varno (range-table index)
	Varattno         int32     // varattno (attribute number)
	Vartype          uint32    // vartype (type OID)
	Vartypmod        int32     // vartypmod
	Varcollid        uint32    // varcollid (collation OID)
	Varnullingrels   Bitmapset // varnullingrels
	Varlevelsup      int32     // varlevelsup
	Varreturningtype int32     // varreturningtype (VarReturningType enum)
	Varnosyn         int32     // varnosyn
	Varattnosyn      int32     // varattnosyn
	Location         int32     // location
}

func (*Var) nodeTag() string { return "VAR" }

// RangeTblRef mirrors _outRangeTblRef.
type RangeTblRef struct {
	Rtindex int32 // rtindex
}

func (*RangeTblRef) nodeTag() string { return "RANGETBLREF" }

// FromExpr mirrors _outFromExpr: fromlist (List of RangeTblRef) and quals
// (an optional WHERE clause expression; nil prints "<>").
type FromExpr struct {
	Fromlist []Node
	Quals    Node
}

func (*FromExpr) nodeTag() string { return "FROMEXPR" }

// TargetEntry mirrors _outTargetEntry.
type TargetEntry struct {
	Expr            Node   // expr
	Resno           int32  // resno (result attribute number)
	Resname         string // resname (result column name; "" -> "<>")
	Ressortgroupref int32  // ressortgroupref
	Resorigtbl      uint32 // resorigtbl (source table OID; 0 for a computed col)
	Resorigcol      int32  // resorigcol (source column number)
	Resjunk         bool   // resjunk
}

func (*TargetEntry) nodeTag() string { return "TARGETENTRY" }

// RangeTblEntry mirrors _outRangeTblEntry for rtekind == RTE_RELATION (0), the
// only kind a single-base-relation view produces. Alias and Eref are stored as
// Node so a nil (absent) Alias serializes as "<>".
type RangeTblEntry struct {
	Alias         Node   // alias (usually nil -> "<>")
	Eref          Node   // eref (*Alias with the column aliases)
	Rtekind       int32  // rtekind (0 = RTE_RELATION)
	Relid         uint32 // relid
	Inh           bool   // inh
	Relkind       byte   // relkind ('r', 'v', ...)
	Rellockmode   int32  // rellockmode
	Perminfoindex int32  // perminfoindex (1-based index into rteperminfos)
	Lateral       bool   // lateral
	InFromCl      bool   // inFromCl
}

func (*RangeTblEntry) nodeTag() string { return "RANGETBLENTRY" }

// RTEPermissionInfo mirrors _outRTEPermissionInfo.
type RTEPermissionInfo struct {
	Relid         uint32    // relid
	Inh           bool      // inh
	RequiredPerms uint64    // requiredPerms (AclMode bitmask; 2 = ACL_SELECT)
	CheckAsUser   uint32    // checkAsUser (role OID; 0 = current)
	SelectedCols  Bitmapset // selectedCols
	InsertedCols  Bitmapset // insertedCols
	UpdatedCols   Bitmapset // updatedCols
}

func (*RTEPermissionInfo) nodeTag() string { return "RTEPERMISSIONINFO" }

// Query mirrors _outQuery for the single-base-relation SELECT view shape. Only
// the fields that carry data for that shape are modeled; every other Query field
// is fixed at its view-default value. Out emits the full fixed skeleton so the
// bytes match PostgreSQL exactly; Read validates each fixed field and reports an
// error on any deviation, which is the caller's "not a supported canonical view,
// fall back to SQL text" signal.
type Query struct {
	CommandType  int32  // commandType (1 = CMD_SELECT)
	Rtable       []Node // rtable (RangeTblEntry list)
	RtePermInfos []Node // rteperminfos (RTEPermissionInfo list)
	Jointree     Node   // jointree (*FromExpr)
	TargetList   []Node // targetList (TargetEntry list)
}

func (*Query) nodeTag() string { return "QUERY" }
