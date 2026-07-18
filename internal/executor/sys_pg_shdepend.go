package executor

// B4.1c (docs/design/wal-pg-identical-stream/02d §3 B4): CREATE/DROP of a
// SHARED object (tablespace, and later database/role) records a shared owner
// dependency in pg_shdepend, mirroring PostgreSQL's recordDependencyOnOwner →
// shdepAddDependency (catalog/pg_shdepend.c). goopg does not itself consult
// pg_shdepend (DROP ROLE / DROP OWNED are not dependency-checked here), so this
// is a WRITE-ONLY, standby-fidelity catalog: the heap row streams to a real PG
// standby so its pg_shdepend matches the primary.
//
// Like every non-boot-critical shared catalog in goopg (see
// bootstrapSharedCatalogPlaceholders — it deliberately materializes shared
// HEAPs but NOT their indexes, so PG falls back to a sequential scan), the two
// pg_shdepend indexes (1232 depender / 1233 reference) are absent from the
// cluster. A standby built from goopg's basebackup therefore has no index to
// keep in sync and reads pg_shdepend by seq scan, so a heap INSERT/xmax-stamp
// alone is faithful — no runtime index maintenance (same shape as the B3.7
// pg_am writer). Column layout: postgres/src/include/catalog/pg_shdepend.h.

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

const (
	pgShdependRelOID = 1214
	// pg_class OIDs of the catalogs a shared dependency references (classid)
	// and points at (refclassid). See the *_RelationId macros in the
	// corresponding catalog headers.
	pgTablespaceClassID = 1213 // TableSpaceRelationId
	pgAuthIdClassID     = 1260 // AuthIdRelationId (owner roles)
	// sharedDependencyOwner is SHARED_DEPENDENCY_OWNER ('o') from
	// catalog/dependency.h: the referenced role owns the dependent object.
	sharedDependencyOwner = "o"
)

// PGShdependColumnsPG18 mirrors FormData_pg_shdepend (7 columns). Exported for
// symmetry with the other B-phase catalog column builders (no reload consumes
// it — pg_shdepend is write-only).
func PGShdependColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "dbid", Type: catalog.Type{Name: "oid"}},
		{Name: "classid", Type: catalog.Type{Name: "oid"}},
		{Name: "objid", Type: catalog.Type{Name: "oid"}},
		{Name: "objsubid", Type: catalog.Type{Name: "int4"}},
		{Name: "refclassid", Type: catalog.Type{Name: "oid"}},
		{Name: "refobjid", Type: catalog.Type{Name: "oid"}},
		{Name: "deptype", Type: catalog.Type{Name: "char"}},
	}
}

func pgShdependRel() storage.RelFileNode {
	// DBOid 0 → global/ (shared catalog); the B4.1a WAL encoder stamps the
	// block-ref locator with spcOid=1664/dbOid=0 for the standby.
	return storage.RelFileNode{DBOid: 0, RelOid: pgShdependRelOID, Fork: storage.MainFork}
}

// writeShdependOwnerRow journals a SHARED_DEPENDENCY_OWNER pg_shdepend row: the
// dependent object (classID, objID) is owned by role ownerOID. For a shared
// dependent (tablespace/database/role) dbid is 0; objsubid is 0 (whole object).
func writeShdependOwnerRow(ctx *Context, classID, objID, ownerOID uint32) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	row := Row{
		NewIntDatum(0),                        // dbid — 0: the dependent is a shared object
		NewIntDatum(int64(classID)),           // classid
		NewIntDatum(int64(objID)),             // objid
		NewIntDatum(0),                        // objsubid — 0: whole object
		NewIntDatum(pgAuthIdClassID),          // refclassid — pg_authid
		NewIntDatum(int64(ownerOID)),          // refobjid — owner role OID
		NewStringDatum(sharedDependencyOwner), // deptype 'o'
	}
	if _, err := writeHeapRowCanonical(ctx, pgShdependRel(), PGShdependColumnsPG18(), row); err != nil {
		return err
	}
	return nil
}

// deleteShdependRowsForObject stamps xmax on every live pg_shdepend row whose
// dependent object is (classID, objID) — the DROP counterpart to
// writeShdependOwnerRow (deleteSharedDependencies in pg_shdepend.c). All seven
// columns are fixed-width and non-null, so the heap payload packs them at
// classid@4 / objid@8.
func deleteShdependRowsForObject(ctx *Context, classID, objID uint32) {
	if !catalogHeapSyncAvailable(ctx) {
		return
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return
	}
	stampCatalogRows(ctx, pgShdependRel(), ctx.Tx.XID, func(data []byte) bool {
		return len(data) >= 12 &&
			binary.LittleEndian.Uint32(data[4:8]) == classID &&
			binary.LittleEndian.Uint32(data[8:12]) == objID
	})
}
