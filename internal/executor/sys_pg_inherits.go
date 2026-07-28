package executor

// root-0031: table INHERITANCE persistence via real pg_inherits heap rows
// (per-DB, base/<dbOid>/2611), mirroring B5 Slice B's pg_attrdef treatment.
//
// Before this, pg_inherits was a purely VIRTUAL catalog: its rows were
// synthesized on demand from the in-memory `InheritsParentOIDs` /
// `PartitionParentOID` fields (catalog.PGInheritsRowsForDBOid), and the only
// writer of the `inheritanceChildren` registry was CREATE TABLE ... INHERITS
// itself. Nothing wrote the parent→child edge to disk, and no reload pass
// rebuilt it, so a restart silently dropped every inheritance relationship:
// `SELECT * FROM parent` stopped seeing child rows and the inheritance-aware
// DDL checks (ALTER TABLE ... RENAME COLUMN's child name-collision recursion)
// became no-ops. See docs/design/root-0031-pg-inherits-restart-persistence.md.
//
// Heap-only, no index (2680 pg_inherits_relid_seqno_index is not materialized
// in goopg — the reload seq-scans base/<dbOid>/2611), exactly as pg_attrdef
// does. Column layout: postgres/src/include/catalog/pg_inherits.h.

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

const pgInheritsRelOID = 2611 // pg_inherits

// PGInheritsColumnsPG18 mirrors FormData_pg_inherits (inhrelid, inhparent,
// inhseqno, inhdetachpending). Exported for the initdb heap reload
// (loadInheritanceFromHeap) and kept byte-identical to the VIRTUAL
// pg_catalog.pg_inherits column list registered in catalog.registerSystemTables,
// so the heap row and the synthesized row describe the same tuple shape.
func PGInheritsColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "inhrelid", Type: catalog.Type{Name: "oid"}, Ordinal: 0},
		{Name: "inhparent", Type: catalog.Type{Name: "oid"}, Ordinal: 1},
		{Name: "inhseqno", Type: catalog.Type{Name: "int4"}, Ordinal: 2},
		{Name: "inhdetachpending", Type: catalog.Type{Name: "bool"}, Ordinal: 3},
	}
}

// writeInheritsRow writes one pg_inherits heap row for a (child, parent) edge
// into the connecting database's own pg_inherits heap (base/<dbOid>/2611 via
// tableCatalogHeapDBOid — the same routing as the pg_class / pg_attribute /
// pg_attrdef rows written alongside it).
//
// inhseqno is 1-based and matches the declaration order of the INHERITS (...)
// list, so the reload restores the parents in the order pg_dump must re-emit
// them (PG assigns seqno the same way in StoreCatalogInheritance1).
// inhdetachpending is always false: goopg's concurrent-detach epoch lives on
// the in-memory partition state, and this row only carries legacy inheritance.
func writeInheritsRow(ctx *Context, childOID, parentOID uint32, seqno int32) error {
	rel := storage.RelFileNode{DBOid: tableCatalogHeapDBOid(ctx), RelOid: pgInheritsRelOID, Fork: storage.MainFork}
	row := Row{
		NewIntDatum(int64(childOID)),
		NewIntDatum(int64(parentOID)),
		NewIntDatum(int64(seqno)),
		NewBoolDatum(false),
	}
	_, err := writeHeapRowCanonical(ctx, rel, PGInheritsColumnsPG18(), row)
	return err
}
