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
	"github.com/goopg/goopg/internal/storage"
)

const pgAttrdefRelOID = 2604 // pg_attrdef

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
