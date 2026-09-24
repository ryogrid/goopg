package executor

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// pruneOnAccessFirstOID is PG's FirstNormalObjectId: relations below it are
// catalogs, which goopg keeps out of on-access pruning.
const pruneOnAccessFirstOID = 16384

// pruneHeapPageOnAccess is heap_page_prune_opt (pruneheap.c, M0145-0008t):
// a scan that has just pinned a heap page prunes it when its pd_prune_xid is
// older than the horizon and the page is short of free space, so dead tuple
// space is reclaimed by the readers that pass over it, not only by VACUUM or a
// HOT update that finds its page full. PG calls it from
// heap_prepare_pagescan, from heapam_index_fetch_tuple on a buffer switch and
// from the bitmap heap scan's page fetch; goopg's three scan operators call it
// at the same points.
//
// The caller holds exactly one pin on slot and no content lock. The prune
// runs only under a cleanup lock taken without waiting, so a page someone
// else has pinned is skipped, as in PG. It is skipped on a standby
// (RecoveryInProgress), when enable_opportunistic_prune is off, and for
// catalog relations (ledgered: goopg's catalog heaps have writers outside
// the executor). Errors are swallowed: the prune is advisory, and the scan
// proceeds on the page as it is.
func pruneHeapPageOnAccess(ctx *Context, slot *storage.Slot, tbl *catalog.Table, rel storage.RelFileNode, blk storage.BlockNumber) {
	if ctx == nil || slot == nil || !ctx.EnableOpportunisticPrune || ctx.IsStandby ||
		ctx.TxnMgr == nil || ctx.Pool == nil || tbl == nil || tbl.OID < pruneOnAccessFirstOID {
		return
	}
	slot.RLock()
	hinted := storage.PagePruneXIDSet(slot.Page())
	slot.RUnlock()
	if !hinted {
		return
	}
	horizon := ctx.TxnMgr.OldestXmin()
	fillfactor := storage.HeapDefaultFillfactor
	if tbl.Fillfactor > 0 {
		fillfactor = tbl.Fillfactor
	}
	slot.RLock()
	want := storage.PagePruneOnAccessWanted(slot.Page(), horizon, fillfactor)
	slot.RUnlock()
	if !want || !ctx.Pool.ConditionalLockForCleanup(slot) {
		return
	}
	defer slot.Unlock()
	// Re-check under the lock, as PG does: another backend may have pruned
	// or filled the page between the two locks.
	if !storage.PagePruneOnAccessWanted(slot.Page(), horizon, fillfactor) {
		return
	}
	result, err := storage.PagePruneOpt(slot.Page(), horizon)
	if err != nil || result.Reclaimed() == 0 {
		return
	}
	_ = markHeapPruneOptDirty(ctx.Pool, slot, rel, blk, result)
}
