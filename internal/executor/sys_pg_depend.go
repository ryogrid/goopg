package executor

// B1.3b (docs/design/wal-pg-identical-stream/02c §3): sequence OWNED BY
// journals as a real pg_depend row (deptype='a', the exact surface PG uses)
// so the startup reload reconstructs ownership without the retired
// RecordKindSequenceState. Narrow write+reload surface only — the pg_depend
// VIEW stays virtual and every other dependency class stays registry-only
// until B3 (the full pg_depend conversion). No index maintenance
// (2673/2674 stay bootstrap-empty; ledgered with the B3 row).

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// pgDependRelOID is pg_depend's relation OID.
const pgDependRelOID = 2608

// PGDependColumnsPG18 mirrors FormData_pg_depend (7 columns, no oid).
// Exported for the initdb reload.
func PGDependColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "classid", Type: catalog.Type{Name: "oid"}},
		{Name: "objid", Type: catalog.Type{Name: "oid"}},
		{Name: "objsubid", Type: catalog.Type{Name: "int4"}},
		{Name: "refclassid", Type: catalog.Type{Name: "oid"}},
		{Name: "refobjid", Type: catalog.Type{Name: "oid"}},
		{Name: "refobjsubid", Type: catalog.Type{Name: "int4"}},
		{Name: "deptype", Type: catalog.Type{Name: "char"}},
	}
}

func pgDependRel() storage.RelFileNode {
	return storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: pgDependRelOID,
		Fork:   storage.MainFork,
	}
}

// writeSequenceOwnedByDependRow journals `ALTER SEQUENCE ... OWNED BY
// table.column` / serial-implicit ownership as PG's auto-dependency row:
// (pg_class, seqrelid, 0) depends-auto-on (pg_class, tableOID, attnum).
// Any previous ownership row for the sequence is stamped first so
// re-pointing OWNED BY never leaves two live rows.
func writeSequenceOwnedByDependRow(ctx *Context, seqrelid, tableOID uint32, attnum int32) error {
	if err := ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	deleteSequenceOwnedByDependRow(ctx, seqrelid, ctx.Tx.XID)
	row := Row{
		NewIntDatum(int64(catalog.RelationRelationId)), // classid = pg_class
		NewIntDatum(int64(seqrelid)),                   // objid
		NewIntDatum(0),                                 // objsubid
		NewIntDatum(int64(catalog.RelationRelationId)), // refclassid
		NewIntDatum(int64(tableOID)),                   // refobjid
		NewIntDatum(int64(attnum)),                     // refobjsubid
		NewStringDatum("a"),                            // deptype = DEPENDENCY_AUTO
	}
	_, err := writeHeapRowCanonical(ctx, pgDependRel(), PGDependColumnsPG18(), row)
	return err
}

// deleteSequenceOwnedByDependRow stamps xmax on the sequence's ownership
// row(s) (DROP SEQUENCE / OWNED BY NONE / re-point).
func deleteSequenceOwnedByDependRow(ctx *Context, seqrelid uint32, xmax storage.TransactionID) {
	stampCatalogRows(ctx, pgDependRel(), xmax, func(data []byte) bool {
		if len(data) < 8 {
			return false
		}
		// classid at 0, objid at 4.
		return binary.LittleEndian.Uint32(data[0:4]) == catalog.RelationRelationId &&
			binary.LittleEndian.Uint32(data[4:8]) == seqrelid
	})
}

// mirrorDependCatalogFiles propagates the pg_depend heap to base/5.
func mirrorDependCatalogFiles(ctx *Context) {
	_ = mirrorCatalogRelToPostgresDB(ctx, pgDependRelOID)
}

// writeInheritsDependRow journals a `CREATE TABLE child INHERITS (parent)`
// relationship as a pg_depend row: (pg_class, childOID, 0) depends-normal-on
// (pg_class, parentOID, 0) with deptype='n'. This ensures pg_dump's
// dependency-based topological sort outputs parent tables before children,
// so a dump/restore round-trip in a per-database-namespace catalog
// correctly recreates inheritance hierarchies. DU-002 (M0119-0004).
func writeInheritsDependRow(ctx *Context, childOID, parentOID uint32) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	row := Row{
		NewIntDatum(int64(catalog.RelationRelationId)), // classid = pg_class
		NewIntDatum(int64(childOID)),                    // objid
		NewIntDatum(0),                                  // objsubid
		NewIntDatum(int64(catalog.RelationRelationId)), // refclassid
		NewIntDatum(int64(parentOID)),                   // refobjid
		NewIntDatum(0),                                  // refobjsubid
		NewStringDatum("n"),                             // deptype = DEPENDENCY_NORMAL
	}
	_, err := writeHeapRowCanonical(ctx, pgDependRel(), PGDependColumnsPG18(), row)
	return err
}
