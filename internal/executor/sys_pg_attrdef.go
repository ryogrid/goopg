package executor

// B5 Slice B (retire RmgrGoopgCatalog=128): column DEFAULT expressions now
// journal as real pg_attrdef heap rows (per-DB, base/<dbOid>/2604) via
// XLOG_HEAP_INSERT, replacing the goopg-private RecordKindColumnDefaults(69)
// record. pg_attrdef was a VIRTUAL table (rows synthesized on demand); it is
// now heap-backed. A real PG18 standby replays the heap inserts (no rmid-128).
//
// Heap-only, no index (2656/2657 are not materialized in goopg — the reload
// seq-scans base/<dbOid>/2604). adbin is stored as SQL text (goopg's
// established pg_node_tree-as-text convention, same as pg_index.indpred),
// re-parsed via parser.ParseExpr on reload — canonical node-tree bytes are a
// separate concern (only matter when PG evaluates the default, not at WAL
// replay). Column layout: postgres/src/include/catalog/pg_attrdef.h.

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/pgnodes"
	"github.com/goopg/goopg/internal/storage"
)

const pgAttrdefRelOID = 2604 // pg_attrdef

// canonicalAttrdefText computes the pg_attrdef.adbin text for a defaulted column.
// M0123-S2 (sub-slice 2): when the column's DEFAULT expression is fully
// representable as a canonical PG18 pg_node_tree AND its top-level result type
// matches the column type exactly (pgnodes.ResolveForColumn), it emits the
// canonical S-expression (a leading-'{' string a real PG18 standby can parse via
// stringToNode and EVALUATE — e.g. INSERT ... DEFAULT VALUES on a promoted
// standby); otherwise it degrades to goopg's established SQL-text convention
// (FormatExprForAttrdef, re-parsed by the reload). All-or-nothing per 02e §3: a
// partial canonical emit is never produced, and a type mismatch (DEFAULT 0 on a
// numeric/smallint column) falls back to text so a standby never inserts a
// mistyped Datum. The canonical form is pure ASCII (datum bytes are rendered as
// decimals in nodeToString), so it stores byte-identically through NewStringDatum
// and the reload's StringValue() decode — no codec change; the leading '{' is the
// reload discriminator between the two forms.
func canonicalAttrdefText(col catalog.Column) string {
	if col.DefaultExpr == nil {
		return ""
	}
	targetOID := catalog.TypeNameToOID(col.Type.Name)
	if node, ok := pgnodes.ResolveForColumn(col.DefaultExpr, targetOID); ok {
		return pgnodes.Out(node)
	}
	return catalog.FormatExprForAttrdef(col.DefaultExpr)
}

// PGAttrdefColumnsPG18 mirrors FormData_pg_attrdef (oid, adrelid, adnum, adbin).
// Exported for the initdb heap reload (loadColumnDefaultsFromHeap).
func PGAttrdefColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}, Ordinal: 0},
		{Name: "adrelid", Type: catalog.Type{Name: "oid"}, Ordinal: 1},
		{Name: "adnum", Type: catalog.Type{Name: "int2"}, Ordinal: 2},
		{Name: "adbin", Type: catalog.Type{Name: "text"}, Ordinal: 3},
	}
}

// writeAttrdefRow writes one pg_attrdef heap row for a table's defaulted column
// (adrelid = table OID, adnum = 1-based ordinal, adbin = the default expression
// as SQL text) into the connecting database's own pg_attrdef heap
// (base/<dbOid>/2604 via tableCatalogHeapDBOid — same routing as the pg_class /
// pg_attribute rows written alongside it). Heap-only. The row's own oid is a
// fresh allocation (identity only; the reload keys on adrelid/adnum).
func writeAttrdefRow(ctx *Context, adrelid uint32, adnum int16, adbin string) error {
	rel := storage.RelFileNode{DBOid: tableCatalogHeapDBOid(ctx), RelOid: pgAttrdefRelOID, Fork: storage.MainFork}
	row := Row{
		NewIntDatum(int64(ctx.Catalog.AllocOID())),
		NewIntDatum(int64(adrelid)),
		NewIntDatum(int64(adnum)),
		NewStringDatum(adbin),
	}
	_, err := writeHeapRowCanonical(ctx, rel, PGAttrdefColumnsPG18(), row)
	return err
}
