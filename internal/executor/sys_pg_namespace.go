package executor

// B1.1 (docs/design/wal-pg-identical-stream/02c §1): pg_namespace heap
// journaling. Schema DDL writes real pg_namespace heap rows + index entries
// (2684/2685) — the WAL stream carries ordinary XLOG_HEAP_* / XLOG_BTREE_*
// records like PostgreSQL — replacing the bespoke CreateSchema/DropSchema/
// AlterSchemaRename/AlterSchemaOwner RecordKinds. The schema registry stays
// the write-through cache and carries each schema's live heap TID
// (catalog.SchemaHeapTID, doc 02a §3.3/§6).

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// PGNamespaceColumnsPG18 mirrors initdb's pgNamespaceColDefs (Form_pg_namespace,
// postgres/src/include/catalog/pg_namespace.h): oid, nspname, nspowner, nspacl.
// Exported for the initdb reload descriptor.
func PGNamespaceColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "nspname", Type: catalog.Type{Name: "name"}},
		{Name: "nspowner", Type: catalog.Type{Name: "oid"}},
		{Name: "nspacl", Type: catalog.Type{Name: "aclitem[]"}},
	}
}

// buildPGNamespaceRow builds one pg_namespace row. nspacl is written as the
// empty aclitem[] ("{}"), matching the initdb bootstrap rows; per-schema
// GRANT rendering into the heap row is a follow-up that rides the ACL
// store's NamespaceACLText (the virtual view already renders it).
func buildPGNamespaceRow(oid uint32, name string, owner uint32) Row {
	return Row{
		NewIntDatum(int64(oid)),   // oid
		NewStringDatum(name),      // nspname (name, 63-byte clip in EncodeRowPG)
		NewIntDatum(int64(owner)), // nspowner
		NewStringDatum("{}"),      // nspacl (empty aclitem[]; initdb parity)
	}
}

// pgNamespaceRelOID is pg_namespace's relation OID
// (postgres/src/include/catalog/pg_namespace.h CATALOG line).
const pgNamespaceRelOID = 2615

// pgNamespaceRel returns the pg_namespace heap relfile for this connection's
// catalog-write database (same routing as every catalog heap write).
func pgNamespaceRel(ctx *Context) storage.RelFileNode {
	return storage.RelFileNode{
		DBOid:  tableCatalogHeapDBOid(ctx),
		RelOid: pgNamespaceRelOID,
		Fork:   storage.MainFork,
	}
}

// insertPgNamespaceIndexEntries adds the (nspname) and (oid) entries for a
// pg_namespace row at tid into indexes 2684/2685.
func insertPgNamespaceIndexEntries(ctx *Context, name string, oid uint32, tid storage.ItemPointer) error {
	if err := insertPgNamespaceNspnameIndexEntry(ctx, name, tid); err != nil {
		return fmt.Errorf("pg_namespace_nspname_index: %w", err)
	}
	if err := insertPgNamespaceOidIndexEntry(ctx, oid, tid); err != nil {
		return fmt.Errorf("pg_namespace_oid_index: %w", err)
	}
	return nil
}

// mirrorSchemaCatalogFiles propagates the just-written pg_namespace heap +
// index files to the postgres DB's copies (the base/1 → base/5 mirror that
// recovery reads from — doc 02a §2.2 review BLOCKER-3). Schema DDL does not
// pass through syncTableToCatalogHeap's funnel, so it invokes the mirror
// itself. Only relevant when the write landed in DefaultDBOid.
func mirrorSchemaCatalogFiles(ctx *Context) error {
	if tableCatalogHeapDBOid(ctx) != catalog.DefaultDBOid {
		return nil
	}
	return mirrorTouchedCatalogsToPostgresDB(ctx)
}

// SyncCompatSchemaToCatalogHeap is the server parse-recovery fallback's
// entry (dispatch.go registerCompatNoopSchema — CREATE SCHEMA forms the
// parser rejects): journals an already-registered schema's pg_namespace
// row WITHOUT a live transaction by stamping the frozen xid (2), the same
// always-visible provenance initdb bootstrap rows carry. The normal parsed
// path goes through syncSchemaToCatalogHeap under the statement's real xid.
func SyncCompatSchemaToCatalogHeap(pool *storage.Pool, im *catalog.InMemory, currentDBOid uint32, name string) error {
	ctx := NewContext()
	ctx.Pool = pool
	ctx.Catalog = im
	ctx.CurrentDatabaseOid = currentDBOid
	ctx.Tx.XID = storage.FrozenTransactionID
	oid := im.SchemaOID(name)
	owner := im.SchemaOwnerOID(name)
	return syncSchemaToCatalogHeap(ctx, im, name, oid, owner)
}

// syncSchemaToCatalogHeap journals CREATE SCHEMA: heap INSERT into
// base/<db>/2615 + both index entries, and seeds the TID cache. The registry
// mutation (RegisterSchema) has already happened — cache and heap are one
// operation inside the DDL (doc 02a §6); on heap failure the caller unwinds
// the registry.
func syncSchemaToCatalogHeap(ctx *Context, im *catalog.InMemory, name string, oid, owner uint32) error {
	tid, err := writeHeapRowCanonical(ctx, pgNamespaceRel(ctx), PGNamespaceColumnsPG18(), buildPGNamespaceRow(oid, name, owner))
	if err != nil {
		return fmt.Errorf("pg_namespace: %w", err)
	}
	if err := insertPgNamespaceIndexEntries(ctx, name, oid, tid); err != nil {
		return err
	}
	im.SetSchemaHeapTID(name, catalog.SchemaHeapTID{Block: uint32(tid.Block), Offset: tid.Offset})
	return mirrorSchemaCatalogFiles(ctx)
}

// updateSchemaCatalogHeapRow journals ALTER SCHEMA (rename/owner): a non-HOT
// heap UPDATE of the schema's row (xl_heap_update) + fresh entries in BOTH
// indexes (non-HOT updates insert into every index, PG semantics; old
// entries stay until vacuum). oldName locates the TID; the row is rewritten
// with newName/owner and the TID cache re-keyed.
func updateSchemaCatalogHeapRow(ctx *Context, im *catalog.InMemory, oldName, newName string, oid, owner uint32) error {
	tid, ok := im.SchemaHeapTID(oldName)
	if !ok {
		// Pre-conversion data dir (no heap row known): fall back to a fresh
		// INSERT so the heap converges on the new state.
		return syncSchemaToCatalogHeap(ctx, im, newName, oid, owner)
	}
	oldTID := storage.ItemPointer{Block: storage.BlockNumber(tid.Block), Offset: tid.Offset}
	newTID, err := updateHeapRowCanonicalPG(ctx, pgNamespaceRel(ctx), PGNamespaceColumnsPG18(), oldTID, buildPGNamespaceRow(oid, newName, owner))
	if err != nil {
		return fmt.Errorf("pg_namespace update: %w", err)
	}
	if err := insertPgNamespaceIndexEntries(ctx, newName, oid, newTID); err != nil {
		return err
	}
	im.RenameSchemaHeapTID(oldName, newName, catalog.SchemaHeapTID{Block: uint32(newTID.Block), Offset: newTID.Offset})
	return mirrorSchemaCatalogFiles(ctx)
}

// deleteSchemaCatalogHeapRow journals DROP SCHEMA: a heap DELETE
// (xl_heap_delete via markHeapDeleteDirty) of the schema's row; index
// entries stay until vacuum (PG semantics). Missing TID (pre-conversion
// dir) is a no-op — there is no row to delete.
func deleteSchemaCatalogHeapRow(ctx *Context, im *catalog.InMemory, name string) error {
	tid, ok := im.SchemaHeapTID(name)
	if !ok {
		return nil
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	rel := pgNamespaceRel(ctx)
	slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: storage.BlockNumber(tid.Block)})
	if err != nil {
		return err
	}
	slot.Lock()
	ht, err := storage.PageGetHeapTuple(slot.Page(), tid.Offset)
	if err != nil || ht.Header.Xmax != storage.InvalidTransactionID {
		// Stale TID — nothing live to delete; drop the cache entry.
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		im.DeleteSchemaHeapTID(name)
		return nil
	}
	oldTuple, err := ht.MarshalBinary()
	if err != nil {
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		return err
	}
	xmax := effectiveWriterXID(ctx)
	if err := storage.PageSetHeapTupleXmax(slot.Page(), tid.Offset, xmax); err != nil {
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		return err
	}
	derr := markHeapDeleteDirty(ctx.Pool, slot, rel, storage.BlockNumber(tid.Block), tid.Offset, xmax, oldTuple)
	slot.Unlock()
	ctx.Pool.Unpin(slot)
	if derr != nil {
		return derr
	}
	im.DeleteSchemaHeapTID(name)
	return mirrorSchemaCatalogFiles(ctx)
}