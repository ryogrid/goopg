package executor

// B4.1e (docs/design/wal-pg-identical-stream/02d §3 B4): CREATE/DROP TABLESPACE
// journals a real pg_tablespace heap row (+ its two shared btree leaves)
// instead of the bespoke RecordKindCreateTablespace(124)/DropTablespace(125).
// pg_tablespace is a SHARED catalog (global/1213); its files pre-exist from
// initdb (bootstrapPgTablespace*), so there is no SMGR_CREATE — just a heap
// INSERT (CREATE) or xmax-stamp (DROP), routed to global/ by the DBOid==0
// sentinel (the B4.1a WAL encoder stamps spcOid=1664/dbOid=0 for the standby).
//
// pg_tablespace's two indexes (2697 oid / 2698 spcname) ARE boot-critical —
// PG's TABLESPACEOID syscache lookup during InitPostgres uses them — so unlike
// the index-less pg_shdepend/pg_am writers they are maintained at runtime.
// spcacl/spcoptions are always NULL (goopg supports only in-place tablespaces
// with no ACL/options). No base/5 mirror: a shared catalog in global/ is
// already visible to every database. Column layout:
// postgres/src/include/catalog/pg_tablespace.h.

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

const (
	pgTablespaceRelOID         = 1213
	pgTablespaceOidIndexOID    = 2697
	pgTablespaceSpcnameIndexID = 2698
)

// PGTablespaceColumnsPG18 mirrors FormData_pg_tablespace (5 columns). Kept in
// lockstep with initdb's pgTablespaceColDefs.
func PGTablespaceColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "spcname", Type: catalog.Type{Name: "name"}},
		{Name: "spcowner", Type: catalog.Type{Name: "oid"}},
		{Name: "spcacl", Type: catalog.Type{Name: "aclitem", IsArray: true}},
		{Name: "spcoptions", Type: catalog.Type{Name: "text", IsArray: true}},
	}
}

func buildPGTablespaceRow(oid uint32, name string, ownerOID uint32) Row {
	if ownerOID == 0 {
		ownerOID = 10 // BOOTSTRAP_SUPERUSERID
	}
	return Row{
		NewIntDatum(int64(oid)),
		NewStringDatum(name),
		NewIntDatum(int64(ownerOID)),
		NullDatum, // spcacl
		NullDatum, // spcoptions
	}
}

func pgTablespaceRel() storage.RelFileNode {
	return storage.RelFileNode{DBOid: 0, RelOid: pgTablespaceRelOID, Fork: storage.MainFork}
}

// writeTablespaceCatalogRow journals CREATE TABLESPACE: a pg_tablespace heap
// INSERT plus its oid (2697) and spcname (2698) btree leaves, all in global/.
func writeTablespaceCatalogRow(ctx *Context, oid uint32, name string, ownerOID uint32) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	tid, err := writeHeapRowCanonical(ctx, pgTablespaceRel(), PGTablespaceColumnsPG18(),
		buildPGTablespaceRow(oid, name, ownerOID))
	if err != nil {
		return err
	}
	blk, off := uint32(tid.Block), tid.Offset
	if err := insertCanonicalSysBtreeLeafInDB(ctx, 0, pgTablespaceOidIndexOID,
		buildIndexTupleOidKey(blk, off, oid), cmpKeyUint32); err != nil {
		return err
	}
	if err := insertCanonicalSysBtreeLeafInDB(ctx, 0, pgTablespaceSpcnameIndexID,
		buildIndexTupleNameKey(blk, off, name), cmpKeyName); err != nil {
		return err
	}
	return nil
}

// deleteTablespaceCatalogRow stamps xmax on the tablespace's pg_tablespace row
// (DROP TABLESPACE). oid is column 0.
func deleteTablespaceCatalogRow(ctx *Context, oid uint32) {
	if !catalogHeapSyncAvailable(ctx) {
		return
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return
	}
	stampCatalogRows(ctx, pgTablespaceRel(), ctx.Tx.XID, func(data []byte) bool {
		return len(data) >= 4 && binary.LittleEndian.Uint32(data[0:4]) == oid
	})
}
