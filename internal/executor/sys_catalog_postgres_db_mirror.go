package executor

// Mirror runtime catalog writes from the goopg DefaultDBOid (1, template1)
// to the PG-canonical PostgresDBOid (5, the "postgres" database). M0106-0010
// batched-40.
//
// Background: M0106-0010 bootstrap mirrors every nailed catalog file
// (`pg_class`=1259, `pg_attribute`=1249, the system btree indexes, …) to
// both `base/1/` and `base/5/` so a fresh PG18 standby cloning a goopg
// primary via `pg_basebackup` can resolve `pg_catalog.*` names through its
// postgres-database lens (DBOid=5). Until this loop, runtime DDL
// (`syncTableToCatalogHeap`, `syncIndexToCatalogHeap`) only wrote to
// `base/1/...` — so after a user-issued `CREATE TABLE`, `base/5/2663`
// (pg_class_relname_nsp_index) and `base/5/1259` (pg_class heap) remained
// frozen at bootstrap state. PG18 client backends connecting to dbname=
// postgres then raised `relation "public.foo" does not exist (42P01)` even
// though `base/1/...` carried the user-table row.
//
// This file adds `mirrorCatalogRelToPostgresDB` which copies every block of
// a `(DBOid=1, RelOid=X)` relfile into the corresponding
// `(DBOid=5, RelOid=X)` relfile. Both files start byte-identical (bootstrap
// guarantees it), and after this mirror runs the post-DDL state is
// byte-identical again. The mirror operates at the buffer-pool layer so
// the change goes through the normal dirty/checkpoint/fsync pipeline.
//
// Scope: catalog files only (the five touched by `syncTableToCatalogHeap`
// and `syncIndexToCatalogHeap`). User-table data files and follow-up
// WAL-replay of INSERT records into `base/5/...` are out of scope here —
// the CREATE TABLE catalog visibility is what unblocks PG-standby
// `parserOpenTable`. Once parse-analyze succeeds, the next blocker
// (user-table data file in `base/5/<relfilenode>`) is a follow-up loop.

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// mirrorCatalogRelToPostgresDB copies every block of (DBOid=1, RelOid=oid)
// into (DBOid=5, RelOid=oid). Both files are assumed to share the same
// content at function entry (bootstrap invariant) so the mirror is a
// straight per-block byte copy. The function pins each source block, copies
// the bytes into the destination block (extending the destination if
// necessary), and marks the destination dirty so the checkpointer flushes
// it to disk.
//
// This function is a no-op when `ctx.Pool == nil` (in-memory test fixture).
func mirrorCatalogRelToPostgresDB(ctx *Context, relOID uint32) error {
	if ctx == nil || ctx.Pool == nil {
		return nil
	}
	if catalog.DefaultDBOid == catalog.PostgresDBOid {
		return nil
	}
	srcRel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: relOID,
		Fork:   storage.MainFork,
	}
	dstRel := storage.RelFileNode{
		DBOid:  catalog.PostgresDBOid,
		RelOid: relOID,
		Fork:   storage.MainFork,
	}
	srcBlocks, err := ctx.Pool.NBlocks(srcRel)
	if err != nil {
		return fmt.Errorf("mirror catalog %d: src nblocks: %w", relOID, err)
	}
	dstBlocks, err := ctx.Pool.NBlocks(dstRel)
	if err != nil {
		return fmt.Errorf("mirror catalog %d: dst nblocks: %w", relOID, err)
	}
	for blk := storage.BlockNumber(0); blk < srcBlocks; blk++ {
		srcSlot, err := ctx.Pool.Pin(storage.BufferTag{Rel: srcRel, Block: blk})
		if err != nil {
			return fmt.Errorf("mirror catalog %d: pin src blk %d: %w", relOID, blk, err)
		}
		// Snapshot the source bytes under the slot's lock so the mirror
		// sees a stable view of any concurrent updaters.
		srcSlot.Lock()
		pageBytes := make([]byte, storage.BlockSize)
		copy(pageBytes, srcSlot.Page())
		srcSlot.Unlock()
		ctx.Pool.Unpin(srcSlot)

		var dstSlot *storage.Slot
		if blk >= dstBlocks {
			// Extend destination by one block per missing block. PinNew
			// allocates the next sequential block, so this only works
			// when we're appending in order (which we are because we
			// iterate blk from 0 ascending).
			s, newBlk, err := ctx.Pool.PinNew(dstRel)
			if err != nil {
				return fmt.Errorf("mirror catalog %d: extend dst at blk %d: %w", relOID, blk, err)
			}
			if newBlk != blk {
				ctx.Pool.Unpin(s)
				return fmt.Errorf("mirror catalog %d: dst extend produced blk %d, expected %d", relOID, newBlk, blk)
			}
			dstSlot = s
			dstBlocks = blk + 1
		} else {
			s, err := ctx.Pool.Pin(storage.BufferTag{Rel: dstRel, Block: blk})
			if err != nil {
				return fmt.Errorf("mirror catalog %d: pin dst blk %d: %w", relOID, blk, err)
			}
			dstSlot = s
		}
		dstSlot.Lock()
		copy(dstSlot.Page(), pageBytes)
		ctx.Pool.MarkDirty(dstSlot)
		dstSlot.Unlock()
		ctx.Pool.Unpin(dstSlot)
	}
	return nil
}

// mirrorTouchedCatalogsToPostgresDB mirrors every relfile touched by
// `syncTableToCatalogHeap` (and `syncIndexToCatalogHeap`, which is a
// strict subset) from DBOid=1 to DBOid=5.
func mirrorTouchedCatalogsToPostgresDB(ctx *Context) error {
	mirroredOIDs := []uint32{
		catalog.RelationRelationId,  // 1259 pg_class
		catalog.AttributeRelationId, // 1249 pg_attribute
		pgClassOidIndexOID,          // 2662
		pgClassRelnameNspIndexOID,   // 2663
		pgAttributeRelidAttnumIndexOID, // 2659
	}
	for _, oid := range mirroredOIDs {
		if err := mirrorCatalogRelToPostgresDB(ctx, oid); err != nil {
			return err
		}
	}
	return nil
}
