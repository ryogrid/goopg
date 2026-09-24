package vacuum

import (
	"sort"

	"github.com/goopg/goopg/internal/storage"
)

// VacuumDeadItems is VACUUM's second heap pass (lazy_vacuum_heap_rel /
// lazy_vacuum_heap_page, vacuumlazy.c; M0145-0008v). The caller has removed
// every index entry pointing at deadTIDs from EVERY index of rel; only then
// may those LP_DEAD items become LP_UNUSED, since an unused item can be
// reused. Per block it marks the items unused, truncates the trailing unused
// line pointers (storage.PageVacuumDeadItems) and logs PG's
// XLOG_HEAP2_PRUNE_VACUUM_CLEANUP record. No tuple byte moves, so the
// exclusive content lock suffices, as in PG.
//
// It does nothing on a cluster without the heap_lp_lifecycle capability
// (storage.HeapLinePointerLifecycle): an older cluster may hold LP_UNUSED
// items an index still references, and truncation there would hand one of
// them to a new tuple. It reports how many items it set unused.
func VacuumDeadItems(pool *storage.Pool, rel storage.RelFileNode, deadTIDs []storage.ItemPointer) (int, error) {
	if !storage.HeapLinePointerLifecycle() || len(deadTIDs) == 0 {
		return 0, nil
	}
	tids := append([]storage.ItemPointer(nil), deadTIDs...)
	sort.Slice(tids, func(i, j int) bool {
		if tids[i].Block != tids[j].Block {
			return tids[i].Block < tids[j].Block
		}
		return tids[i].Offset < tids[j].Offset
	})
	logCleanup := pool.LogHeapVacuumCleanup()
	total := 0
	for i := 0; i < len(tids); {
		blk := tids[i].Block
		j := i
		for j < len(tids) && tids[j].Block == blk {
			j++
		}
		n, err := vacuumDeadItemsOnPage(pool, rel, blk, tids[i:j], logCleanup)
		if err != nil {
			return total, err
		}
		total += n
		i = j
	}
	return total, nil
}

// vacuumDeadItemsOnPage runs the second pass on one block. Items that are no
// longer LP_DEAD are skipped rather than trusted: only the ones still dead
// are marked, and the WAL record lists exactly those.
func vacuumDeadItemsOnPage(pool *storage.Pool, rel storage.RelFileNode, blk storage.BlockNumber, tids []storage.ItemPointer, logCleanup storage.LogHeapVacuumCleanupFunc) (int, error) {
	slot, err := pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
	if err != nil {
		return 0, err
	}
	defer pool.Unpin(slot)
	slot.Lock()
	defer slot.Unlock()
	page := slot.Page()
	var slots []uint16
	for k, t := range tids {
		if k > 0 && t.Offset == tids[k-1].Offset {
			continue
		}
		if dead, derr := storage.PageItemIsDead(page, t.Offset); derr == nil && dead {
			slots = append(slots, t.Offset)
		}
	}
	if len(slots) == 0 {
		return 0, nil
	}
	n, err := storage.PageVacuumDeadItems(page, slots)
	if err != nil {
		return 0, err
	}
	if logCleanup != nil {
		if err := pool.MarkDirtyChangeRecord(slot, func() (storage.LSN, error) {
			return logCleanup(rel, blk, slots)
		}); err != nil {
			return 0, err
		}
	} else {
		pool.MarkDirty(slot)
	}
	return n, nil
}
