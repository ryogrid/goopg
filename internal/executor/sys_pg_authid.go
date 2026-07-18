package executor

// B4.5 (docs/design/wal-pg-identical-stream/02d §3 B4): CREATE/ALTER/DROP ROLE
// journal real pg_authid heap rows (SHARED, global/1260) via XLOG_HEAP_INSERT/
// DELETE, replacing the bespoke whole-file rewriter (SyncPgAuthidFile) and its
// RecordKindRoleState(67)/DropRole(68)/AlterRoleRename(72) crash-tail records.
// This is the last clean B4 slice; it retires the goopg-private role-DDL WAL
// kinds so a real PG18 standby rebuilds pg_authid purely from the heap stream.
//
// pg_authid is BOOT-CRITICAL auth state, and — unlike the other shared
// catalogs converted in B4.2–B4.4 (heap-only, seq-scan) — its unique indexes
// pg_authid_oid_index (2677) and pg_authid_rolname_index (2676) ARE
// materialized in global/ (initdb bootstraps them so a real PG standby's
// syscache lookups by OID and by rolname resolve). So each write maintains
// both indexes, exactly like B4.1's pg_tablespace (2697/2698).
//
// The registry is goopg's runtime auth truth (a restart reloads it from this
// heap via reloadRolesFromAuthidHeap); the bootstrap superuser (OID 10) and
// the 16 predefined pg_* roles live in the initdb-written base page and are
// never re-synced here. Each CREATE/ALTER re-syncs the single mutated role
// (stamp by oid + write current); DROP stamps by the oid captured before the
// registry removal. Column layout: postgres/src/include/catalog/pg_authid.h.
// Twin of bootstrapPostgresRoleWithPassword's row shape — keep in sync.

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/storage"
)

const (
	pgAuthidRelOID          = 1260
	pgAuthidOidIndexOID     = 2677 // pg_authid_oid_index
	pgAuthidRolnameIndexOID = 2676 // pg_authid_rolname_index
)

// pgAuthidRel is the shared pg_authid relation. DBOid 0 → global/; the B4.1a
// WAL encoder stamps the block-ref locator with spcOid=1664/dbOid=0 for the
// standby.
func pgAuthidRel() storage.RelFileNode {
	return storage.RelFileNode{DBOid: 0, RelOid: pgAuthidRelOID, Fork: storage.MainFork}
}

// SyncAuthidRow re-syncs the single pg_authid row for oid: it stamps any live
// row for that oid deleted, then writes the role's current state and maintains
// the 2676/2677 indexes. CREATE and ALTER (attribute change or RENAME, which
// only mutates rolname while preserving oid) both route here. Exported for the
// server-layer role-DDL intercept, which drives it from its own transaction
// (Server.syncAuthidHeapRow).
func SyncAuthidRow(ctx *Context, oid uint32, rolname string, super, canLogin, createDB, createRole, replication, bypassRLS bool, connLimit int32, rolpassword, validUntil string) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	stampAuthidRow(ctx, oid)
	row := buildAuthidUserRow(int64(oid), rolname, super, canLogin, createDB, createRole,
		replication, bypassRLS, connLimit, rolpassword, validUntil)
	tid, err := writeHeapRowCanonical(ctx, pgAuthidRel(), pgAuthidSyncCols(), row)
	if err != nil {
		return err
	}
	blk, off := uint32(tid.Block), tid.Offset
	if err := insertCanonicalSysBtreeLeafInDB(ctx, 0, pgAuthidOidIndexOID,
		buildIndexTupleOidKey(blk, off, oid), cmpKeyUint32); err != nil {
		return err
	}
	if err := insertCanonicalSysBtreeLeafInDB(ctx, 0, pgAuthidRolnameIndexOID,
		buildIndexTupleNameKey(blk, off, rolname), cmpKeyName); err != nil {
		return err
	}
	return nil
}

// DeleteAuthidRow stamps the live pg_authid row for oid deleted (DROP ROLE).
// The caller captures oid before removing the role from the registry.
func DeleteAuthidRow(ctx *Context, oid uint32) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	stampAuthidRow(ctx, oid)
	return nil
}

// stampAuthidRow marks every live pg_authid row for oid (column 0 = oid)
// deleted. The caller has materialized the writer XID.
func stampAuthidRow(ctx *Context, oid uint32) {
	stampCatalogRows(ctx, pgAuthidRel(), ctx.Tx.XID, func(data []byte) bool {
		return len(data) >= 4 && binary.LittleEndian.Uint32(data[0:4]) == oid
	})
}
