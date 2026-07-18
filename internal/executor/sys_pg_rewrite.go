package executor

// B5 Slice C (retire RmgrGoopgCatalog=128): a user view / materialized view's
// defining query now journals as a real pg_rewrite _RETURN rule heap row
// (per-DB, base/<dbOid>/2618) via XLOG_HEAP_INSERT, replacing the goopg-private
// RecordKindCreateView(103) / RecordKindCreateMatView(102) records. A real PG18
// standby replays the heap insert (no rmid-128).
//
// NARROW removal (see the deferral ledger): ev_action stores the view's SELECT
// as SQL TEXT (goopg's pg_node_tree-as-text convention, shared with
// pg_attrdef.adbin / pg_statistic_ext.stxexprs), NOT a canonical nodeToString
// dump — goopg has no node-tree serializer. pg_class.relhasrules stays FALSE for
// user views, so a real PG standby does not try to expand the rule it cannot
// parse (the view replays and exists, but querying it on the standby needs
// canonical ev_action + relhasrules=true — a SEPARATE, blocked track). goopg's
// own reload (loadViewsFromHeap) re-parses the SQL text to rebuild the view AST.
// Column layout: postgres/src/include/catalog/pg_rewrite.h (8 cols).

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

const pgRewriteRelOID = 2618 // pg_rewrite

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

// writeViewRewriteRow writes the _RETURN ON-SELECT rule row for a view or
// materialized view (ev_class = the relation's pg_class OID, ev_action = its
// defining SELECT as SQL text). Called from syncTableToCatalogHeap after the
// pg_class row; the caller stamps the old rule via deleteCatalogRowsForOID on an
// ALTER re-sync / rolled-back CREATE, so this is a fresh write.
func writeViewRewriteRow(ctx *Context, tbl *catalog.Table, query string) error {
	rel := pgRewriteRel(ctx)
	row := Row{
		NewIntDatum(int64(ctx.Catalog.AllocOID())), // oid (rule OID; identity only)
		NewStringDatum("_RETURN"),                  // rulename
		NewIntDatum(int64(tbl.OID)),                // ev_class
		NewIntDatum(int64('1')),                    // ev_type = CMD_SELECT
		NewIntDatum(int64('O')),                    // ev_enabled = ALWAYS
		NewBoolDatum(true),                         // is_instead
		NewStringDatum("<>"),                       // ev_qual (empty node tree)
		NewStringDatum(query),                      // ev_action (SQL text — node-tree-as-text)
	}
	if _, err := writeHeapRowCanonical(ctx, rel, PGRewriteColumnsPG18(), row); err != nil {
		return err
	}
	return nil
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
