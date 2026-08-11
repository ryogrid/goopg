package executor

// B5 Slice C (retire RmgrGoopgCatalog=128): a user view / materialized view's
// defining query now journals as a real pg_rewrite _RETURN rule heap row
// (per-DB, base/<dbOid>/2618) via XLOG_HEAP_INSERT, replacing the goopg-private
// RecordKindCreateView(103) / RecordKindCreateMatView(102) records. A real PG18
// standby replays the heap insert (no rmid-128).
//
// ev_action carries one of two forms (see canonicalViewEvAction): a canonical
// PG18 pg_node_tree for the view shapes pgnodes.ResolveViewQuery supports, or
// goopg's pg_node_tree-as-text fallback (shared with pg_attrdef.adbin /
// pg_statistic_ext.stxexprs) for everything else. goopg's own reload
// (loadViewsFromHeap) discriminates on the leading "({" and re-parses the SQL
// text form to rebuild the view AST.
//
// M0123-S3 superseded this file's original "relhasrules stays FALSE for user
// views" note: buildUserPGClassRow now stamps relhasrules = tbl.RuleIsCanonical
// (pg18_user_catalog_rows.go), so a real PG DOES enter the rule-loading path
// for a canonical view. M0131-S5 removed the last blocker behind it — the heap
// row is now indexed in 2692/2693, because RelationBuildRuleLock finds the rule
// only through 2693 and has no seq-scan fallback (see writeViewRewriteRow).
// Column layout: postgres/src/include/catalog/pg_rewrite.h (8 cols).

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/pgnodes"
	"github.com/goopg/goopg/internal/storage"
)

const pgRewriteRelOID = 2618 // pg_rewrite

// viewRuleName is the rulename PG gives every ON-SELECT view rule
// (ViewSelectRuleName, postgres/src/include/rewrite/rewriteDefine.h). It is
// also half the 2693 index key, so the heap row and the leaf must use the
// same literal.
const viewRuleName = "_RETURN"

// pgRewriteEvClassOffset is the byte offset of ev_class (column 3) in the
// canonical pg_rewrite tuple data: oid(4) + rulename(name, 64) = 68.
const pgRewriteEvClassOffset = 68

// PGRewriteColumnsPG18 mirrors Form_pg_rewrite (8 cols, PG18 physical order).
// The initdb bootstrap (pgRewriteColDefs) delegates here so the runtime writer
// and the bootstrap seed cannot drift.
func PGRewriteColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}, Ordinal: 0},
		{Name: "rulename", Type: catalog.Type{Name: "name"}, Ordinal: 1},
		{Name: "ev_class", Type: catalog.Type{Name: "oid"}, Ordinal: 2},
		{Name: "ev_type", Type: catalog.Type{Name: "char"}, Ordinal: 3},
		{Name: "ev_enabled", Type: catalog.Type{Name: "char"}, Ordinal: 4},
		{Name: "is_instead", Type: catalog.Type{Name: "bool"}, Ordinal: 5},
		{Name: "ev_qual", Type: catalog.Type{Name: "pg_node_tree"}, Ordinal: 6},
		{Name: "ev_action", Type: catalog.Type{Name: "pg_node_tree"}, Ordinal: 7},
	}
}

// pgRewriteRel is the connecting database's own pg_rewrite heap.
func pgRewriteRel(ctx *Context) storage.RelFileNode {
	return storage.RelFileNode{DBOid: tableCatalogHeapDBOid(ctx), RelOid: pgRewriteRelOID, Fork: storage.MainFork}
}

// canonicalViewEvAction resolves a view/matview's pg_rewrite ev_action into one
// of two forms (M0123-S3 sub-slice 2c):
//
//	canonical — a plain view whose defining SELECT is inside the single-base-
//	relation subset pgnodes.ResolveViewQuery supports serializes to a real PG18
//	pg_node_tree ("({QUERY ...})"). canonical=true is the caller's signal to set
//	tbl.RuleIsCanonical (⇒ pg_class.relhasrules=true) so a real PG18 standby
//	EVALUATES the rule (SELECT * FROM view works on the standby).
//
//	SQL text — every unsupported view (JOIN/aggregate/subquery/…) and every
//	materialized view keeps goopg's node-tree-as-text fallback and
//	canonical=false, so relhasrules stays false and the standby never tries to
//	expand a rule it cannot parse. Graceful-degradation invariant (02e §3):
//	unsupported shape ⇒ fall back, never FATAL, never partial-emit.
//
// This MUST be resolved BEFORE buildUserPGClassRow writes the pg_class heap row
// (syncTableToCatalogHeap sets tbl.RuleIsCanonical up front): the streamed heap
// row is what a PG standby reads, and a relhasrules=false row would leave the
// standby unable to expand a canonical rule it did stream into pg_rewrite.
func canonicalViewEvAction(ctx *Context, tbl *catalog.Table, sqlText string) (evAction string, canonical bool) {
	if tbl.View != nil && !tbl.IsMatView {
		if q, err := pgnodes.ResolveViewQuery(tbl.View, viewRelationResolver{ctx: ctx}); err == nil {
			return pgnodes.OutRuleAction([]pgnodes.Node{q}), true
		}
	}
	return sqlText, false
}

// writeViewRewriteRow writes the _RETURN ON-SELECT rule row for a view or
// materialized view (ev_class = the relation's pg_class OID) with an already-
// resolved ev_action (see canonicalViewEvAction). Called from
// syncTableToCatalogHeap; the caller stamps the old rule via
// deleteCatalogRowsForOID on an ALTER re-sync / rolled-back CREATE, so this is a
// fresh write. goopg's own reload (loadViewsFromHeap) discriminates the two
// ev_action forms by the leading "({" and rebuilds the view AST accordingly.
func writeViewRewriteRow(ctx *Context, tbl *catalog.Table, evAction string) error {
	rel := pgRewriteRel(ctx)
	ruleOID := ctx.Catalog.AllocOID()
	row := Row{
		NewIntDatum(int64(ruleOID)),  // oid (rule OID; identity only)
		NewStringDatum(viewRuleName), // rulename
		NewIntDatum(int64(tbl.OID)),  // ev_class
		NewIntDatum(int64('1')),      // ev_type = CMD_SELECT
		NewIntDatum(int64('O')),      // ev_enabled = ALWAYS
		NewBoolDatum(true),           // is_instead
		NewStringDatum("<>"),         // ev_qual (empty node tree)
		NewStringDatum(evAction),     // ev_action (canonical node tree or SQL text)
	}
	tid, err := writeHeapRowCanonical(ctx, rel, PGRewriteColumnsPG18(), row)
	if err != nil {
		return err
	}
	// M0131-S5: index the row in pg_rewrite's two declared btrees. goopg's own
	// reload seq-scans base/<dbOid>/2618 and needs neither, but a real PG's
	// RelationBuildRuleLock reaches the rule ONLY through 2693 (indexOK=true
	// with no seq-scan fallback), so an unindexed row makes the view
	// unqueryable there (42809) even though pg_get_viewdef — which goes through
	// SPI, i.e. a planned seq-scan — reconstructs it correctly. 2693 first: it
	// is the load-bearing one.
	if err := insertPgRewriteRelRulenameIndexEntry(ctx, tbl.OID, viewRuleName, tid); err != nil {
		return err
	}
	return insertPgRewriteOidIndexEntry(ctx, ruleOID, tid)
}

// viewRelationResolver adapts the live catalog to pgnodes.RelationResolver so
// ResolveViewQuery can look up the single base relation a supported view selects
// from. It resolves against the connection's current database and returns
// (nil,false) — which the resolver treats as ErrUnsupported → SQL-text fallback —
// for anything outside the plain-scalar-builtin-column subset.
type viewRelationResolver struct {
	ctx *Context
}

// LookupRelation resolves the view's FROM name to its column metadata. It is a
// no-match (SQL-text fallback) unless every column is a plain scalar built-in
// type, because a Var's serialized vartype/varcollid MUST equal the atttypid/
// attcollation the standby sees in its own pg_attribute — the exact same values
// buildUserPGAttributeRow writes. viewColumnCanonicalType reuses that writer so
// the two paths cannot drift (sibling-paths-must-agree).
func (r viewRelationResolver) LookupRelation(schema, name string) (*pgnodes.RelationInfo, bool) {
	tbl, ok := r.ctx.Catalog.LookupTable(
		parser.ObjectName{Schema: schema, Name: name},
		catalog.NamespaceDBOid(r.ctx.CurrentDatabaseOid))
	if !ok || tbl == nil {
		return nil, false
	}
	cols := make([]pgnodes.ColumnInfo, 0, len(tbl.Columns))
	for _, col := range tbl.Columns {
		typeOID, typmod, collation, ok := viewColumnCanonicalType(r.ctx.Catalog, tbl, col)
		if !ok {
			return nil, false // user/array/dropped column ⇒ not serializable
		}
		cols = append(cols, pgnodes.ColumnInfo{
			Name:      col.Name,
			Attno:     int32(col.Ordinal + 1),
			TypeOID:   typeOID,
			Typmod:    typmod,
			Collation: collation,
		})
	}
	return &pgnodes.RelationInfo{
		Relid:   tbl.OID,
		Relname: tbl.Name,
		Relkind: relkindByteForTable(tbl),
		Columns: cols,
	}, true
}

// pg_attribute physical column indices (pgAttributeColumnsPG18 order) that
// viewColumnCanonicalType reads back. Verified by a unit test against the
// schema column names so a layout change can't silently misalign them.
const (
	attTypidRowIdx     = 2  // atttypid
	attTypmodRowIdx    = 5  // atttypmod
	attCollationRowIdx = 19 // attcollation
)

// viewColumnCanonicalType returns the atttypid/atttypmod/attcollation a column
// serializes to in a canonical view Var, or ok=false when the column falls
// outside the plain-scalar-builtin subset (arrays and user-defined enum/
// composite/domain/range types, all of which the pgnodes query resolver does
// not model). It obtains the values from buildUserPGAttributeRow — the exact
// bytes the standby's pg_attribute carries — so a serialized Var's vartype
// always equals the standby's atttypid.
func viewColumnCanonicalType(cat catalog.Catalog, tbl *catalog.Table, col catalog.Column) (typeOID uint32, typmod int32, collation uint32, ok bool) {
	if col.Type.IsArray || col.DeclaredTypeName != "" {
		return 0, 0, 0, false // array / domain column: not in the supported subset
	}
	row := buildUserPGAttributeRow(cat, tbl, col)
	typeOID = uint32(row[attTypidRowIdx].Int)
	typmod = int32(row[attTypmodRowIdx].Int)
	collation = uint32(row[attCollationRowIdx].Int)
	// Any dynamic user-type OID (enum/composite/domain/range) is >= FirstUserOID;
	// the pgnodes resolver only understands built-in scalar OIDs.
	if typeOID >= catalog.FirstUserOID {
		return 0, 0, 0, false
	}
	return typeOID, typmod, collation, true
}

// relkindByteForTable mirrors buildUserPGClassRow's relkind derivation as the
// single byte the RTE_RELATION node stores (RangeTblEntry.relkind). A supported
// view's base relation is normally an ordinary table ('r'); the other kinds are
// included for completeness.
func relkindByteForTable(tbl *catalog.Table) byte {
	switch {
	case tbl.IsSequence:
		return 'S'
	case tbl.PartitionMethod != "":
		return 'p'
	case tbl.IsMatView:
		return 'm'
	case tbl.View != nil:
		return 'v'
	default:
		return 'r'
	}
}

// stampViewRewriteRows stamps xmax on every live pg_rewrite row whose ev_class
// matches relOID (the dropped/re-synced view). Used from deleteCatalogRowsForOID.
func stampViewRewriteRows(ctx *Context, dbOid uint32, relOID uint32, xmax storage.TransactionID) {
	rel := storage.RelFileNode{DBOid: dbOid, RelOid: pgRewriteRelOID, Fork: storage.MainFork}
	stampCatalogRows(ctx, rel, xmax, func(data []byte) bool {
		return len(data) >= pgRewriteEvClassOffset+4 &&
			binary.LittleEndian.Uint32(data[pgRewriteEvClassOffset:pgRewriteEvClassOffset+4]) == relOID
	})
}
