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
	"bytes"
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
			s, newBlk, err := ctx.Pool.PinNewWithXID(dstRel, ctx.Tx.XID)
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
		// Skip identical blocks: the mirror walks EVERY block of every
		// mirrored catalog on every DDL, but only the 1-2 pages the DDL
		// touched actually differ. Copying + force-FPI'ing unchanged pages
		// (B2.1b standby fix) multiplied WAL by the catalog's block count
		// per statement and made the regress suite crawl.
		if bytes.Equal(dstSlot.Page(), pageBytes) {
			dstSlot.Unlock()
			ctx.Pool.Unpin(dstSlot)
			continue
		}
		copy(dstSlot.Page(), pageBytes)
		// MarkDirtyForceFPI (not MarkDirty): the standby reads base/5, and
		// maybeEmitFPI's once-per-checkpoint suppression would swallow a
		// second change to the same mirrored page (B2.1b).
		ctx.Pool.MarkDirtyForceFPI(dstSlot)
		dstSlot.Unlock()
		ctx.Pool.Unpin(dstSlot)
	}
	return nil
}

// mirrorTouchedCatalogsToPostgresDB mirrors every relfile touched by
// `syncTableToCatalogHeap` (and `syncIndexToCatalogHeap`, which is a
// strict subset) from DBOid=1 to DBOid=5.
func mirrorTouchedCatalogsToPostgresDB(ctx *Context) error {
	for _, oid := range mirroredCatalogOIDs() {
		if err := mirrorCatalogRelToPostgresDB(ctx, oid); err != nil {
			return err
		}
	}
	return nil
}

// mirroredCatalogOIDs is the relfile set mirrorTouchedCatalogsToPostgresDB
// copies from DBOid=1 to DBOid=5. Split out from the loop so a regression test
// can assert membership without running a whole DDL fixture: every entry here
// is load-bearing for a real PG attached with dbname=postgres, and each
// omission has historically been its own multi-loop blocker.
func mirroredCatalogOIDs() []uint32 {
	return []uint32{
		catalog.RelationRelationId,  // 1259 pg_class
		catalog.AttributeRelationId, // 1249 pg_attribute
		catalog.IndexRelationId,     // 2610 pg_index — syncIndexToCatalogHeap's
		// pg_index row was never mirrored here, so loadUserIndexesFromHeap's Pass
		// 2 heap scan (which reads from cat.DBOID(), almost always PostgresDBOid
		// in real usage — see detectCatalogDBOID) never found a live pg_index row
		// for any user index: every CREATE INDEX heap-recovery on restart silently
		// fell back to the WAL-replay path instead (M0119-0004 index-reloptions
		// follow-up — this is what made the new reloptions-survival plumbing a
		// no-op until this line was added).
		pgClassOidIndexOID,             // 2662
		pgClassRelnameNspIndexOID,      // 2663
		pgAttributeRelidAttnumIndexOID, // 2659
		// B1.1 (doc 02a §2.2 review BLOCKER-3): pg_namespace converted to
		// heap journaling — its heap + both indexes MUST be in this set or
		// schemas written to base/1 vanish on restart (recovery reloads
		// from the postgres DB's copies).
		pgNamespaceRelOID,          // 2615 pg_namespace
		pgNamespaceNspnameIndexOID, // 2684
		pgNamespaceOidIndexOID,     // 2685
		// B1.2: pg_proc heap; B2-prep added runtime maintenance of both
		// indexes (descent path), so they must mirror alongside the heap.
		pgProcRelOID,                 // 1255
		pgProcOidIndexOID,            // 2690
		pgProcPronameArgsNspIndexOID, // 2691
		// B1.3b: the sequence reload reads pg_sequence + pg_depend from
		// base/5 (cat.DBOID()) — without these the registry reload found
		// ZERO rows (B1.3's TID reseed silently no-op'd the same way).
		pgSequenceRelOID,           // 2224 pg_sequence
		pgSequenceSeqrelidIndexOID, // 5002
		pgDependRelOID,             // 2608 pg_depend (narrow OWNED-BY rows)
		// B5 Slice C: pg_rewrite _RETURN rule rows for user views/matviews are
		// written to base/1 by syncTableToCatalogHeap (writeViewRewriteRow); the
		// standby (dbname=postgres) reads base/5, so the heap must mirror.
		pgRewriteRelOID, // 2618 pg_rewrite
		// M0131-S5: both pg_rewrite indexes, for the same reason blocker #8
		// applied to 2678/2679 below. writeViewRewriteRow indexes the rule row
		// in base/<tableCatalogHeapDBOid>; an attached PG connects
		// dbname=postgres and reads base/5, where an unmirrored index is an
		// EMPTY index — and RelationBuildRuleLock's scan of 2693 is indexOK
		// with no seq-scan fallback, so the view stays unqueryable (42809).
		pgRewriteRelRulenameIndexOID, // 2693
		pgRewriteOidIndexOID,         // 2692
		// M-NIGHTLY AI-20260810-011258-003: pg_attrdef + both of its declared
		// indexes. syncTableToCatalogHeap writes column DEFAULTs to base/1
		// (writeAttrdefRow); a PG 18.3 standby attached with dbname=postgres
		// reads base/5, and its AttrDefaultFetch (relcache.c) resolves them
		// through AttrDefaultIndexId = 2656 with no seq-scan fallback. Without
		// all three here the standby's relcache build emits
		//   WARNING: 1 pg_attrdef record(s) missing for relation "<rel>"
		// and the promoted PG evaluates the column as if it had no default.
		pgAttrdefRelOID,               // 2604 pg_attrdef
		pgAttrdefAdrelidAdnumIndexOID, // 2656
		pgAttrdefOidIndexOID,          // 2657
		// AI-20260810-011258-003 blocker #8: both pg_index indexes. The
		// pg_index HEAP was already mirrored, but a standby reading base/5
		// resolves a relation's index list ONLY through 2678
		// (RelationGetIndexList) and each row through 2679 (INDEXRELID
		// syscache); unmirrored, the promoted PG saw every goopg-created index
		// as nonexistent and did no index maintenance for its own writes.
		pgIndexIndrelidIndexOID,   // 2678
		pgIndexIndexrelidIndexOID, // 2679
	}
}
