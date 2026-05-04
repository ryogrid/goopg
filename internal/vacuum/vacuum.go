// Package vacuum implements goopg's v0 VACUUM and ANALYZE.
//
// Scope and growth path are documented in
// docs/design/0016-vacuum-and-analyze.md. v0 reclaims dead heap tuples
// at page granularity, returns full-scan ANALYZE statistics, and
// provides a REINDEX bridge for B-tree cleanup until per-entry index
// removal lands.
package vacuum

import (
	"errors"

	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/storage"
)

// Stats summarises the outcome of one Vacuum invocation across every
// block in the relation.
type Stats struct {
	Pages      int // blocks visited
	Live       int // tuples that survived this pass
	Dead       int // tuples reclaimed (LP_NORMAL -> LP_UNUSED)
	OldestXmin storage.TransactionID
}

// Vacuum runs a heap page-prune across every block of rel using the
// MVCC-derived oldest-xmin horizon. Pages that no longer contain dead
// tuples are still pinned and re-flushed only if any tuple was
// reclaimed; otherwise they're left untouched.
//
// When the buffer pool has a `LogHeapVacuum` hook wired (the normal
// runtime case), each pruned page emits a logical heap-vacuum redo
// record carrying the reclaimed slot list — replay then re-runs
// `VacuumHeapPageBySlots` against the existing page bytes for a
// bit-exact prune. When the hook is absent (test pools), Vacuum
// falls back to MarkDirty so the FPI-on-every-dirty path keeps the
// change durable.
//
// VACUUM does not touch indexes — see Reindex for the bridge until
// B-tree page deletion lands.
func Vacuum(pool *storage.Pool, mgr *mvcc.Manager, rel storage.RelFileNode) (Stats, error) {
	return vacuumCore(pool, mgr, rel, nil, nil)
}

// VacuumWithFSM is Vacuum with a Free Space Map (M0046-0003). After
// reclaiming dead tuples on each page, the page's remaining free space
// is recorded in fsm so subsequent INSERT operations can find the page
// without extending the relation. fsm may be nil (identical to Vacuum).
func VacuumWithFSM(pool *storage.Pool, mgr *mvcc.Manager, rel storage.RelFileNode, fsm *storage.FSM) (Stats, error) {
	return vacuumCore(pool, mgr, rel, fsm, nil)
}

// VacuumWithFSMAndVM runs vacuum, updating both the FSM (M0046-0003) and the
// Visibility Map (M0046-0004). After each page prune, if all remaining tuples
// are universally visible (committed xmin < OldestXmin, no xmax), the VM bit
// is set so index-only scans can skip heap fetches on that page.
func VacuumWithFSMAndVM(pool *storage.Pool, mgr *mvcc.Manager, rel storage.RelFileNode,
	fsm *storage.FSM, vm *storage.VisibilityMap) (Stats, error) {
	return vacuumCore(pool, mgr, rel, fsm, vm)
}

func vacuumCore(pool *storage.Pool, mgr *mvcc.Manager, rel storage.RelFileNode,
	fsm *storage.FSM, vm *storage.VisibilityMap) (Stats, error) {
	horizon := mgr.OldestXmin()
	nBlocks, err := pool.NBlocks(rel)
	if err != nil {
		return Stats{}, err
	}
	stats := Stats{OldestXmin: horizon}
	isDead := func(h storage.HeapTupleHeader) bool {
		if h.Xmax == storage.InvalidTransactionID {
			return false
		}
		return h.Xmax < horizon
	}
	logVac := pool.LogHeapVacuum()
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		slot, err := pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return stats, err
		}
		page := slot.Page()
		// Skip uninitialised pages; an extension may have allocated
		// the block without writing tuples yet.
		if storage.IsNew(page) {
			pool.Unpin(slot)
			stats.Pages++
			continue
		}
		// Take the per-page content lock so concurrent readers /
		// writers can't tear the dead-set scan + repack + pd_lsn
		// stamp under MarkDirtyChangeRecord.
		slot.Lock()
		deadSlots, err := storage.CollectDeadHeapSlots(page, isDead)
		if err != nil {
			slot.Unlock()
			pool.Unpin(slot)
			return stats, err
		}
		ps, err := storage.VacuumHeapPageBySlots(page, deadSlots)
		if err != nil {
			slot.Unlock()
			pool.Unpin(slot)
			return stats, err
		}
		if ps.Dead > 0 {
			if logVac != nil {
				err = pool.MarkDirtyChangeRecord(slot, func() (storage.LSN, error) {
					return logVac(rel, blk, deadSlots)
				})
				if err != nil {
					slot.Unlock()
					pool.Unpin(slot)
					return stats, err
				}
			} else {
				pool.MarkDirty(slot)
			}
		}
		// Record updated free space in FSM so future inserts can reuse
		// the reclaimed space without extending the relation (M0046-0003).
		if fsm != nil {
			fsm.RecordFreeSpaceForPage(rel, blk, page)
		}
		// Set ALL_VISIBLE in VM if all remaining tuples on the page are
		// universally committed (M0046-0004). This enables index-only
		// scans to skip heap fetches entirely for this page.
		if vm != nil {
			if storage.PageAllVisible(page, horizon) {
				vm.SetAllVisible(rel, blk)
			} else {
				vm.ClearBlock(rel, blk)
			}
		}
		slot.Unlock()
		pool.Unpin(slot)
		stats.Pages++
		stats.Live += ps.Live
		stats.Dead += ps.Dead
	}
	return stats, nil
}

// AnalyzeStats is the v0 ANALYZE output. Sampling and per-column
// histograms are deferred — see the design doc.
type AnalyzeStats struct {
	Pages    int
	Rows     int     // visible (not just live-on-page) tuples
	AvgWidth float64 // average bytes-per-tuple including header
}

// Analyze walks every block of rel and returns row count plus average
// tuple width. v0 uses a fresh snapshot from mgr to count "currently
// live" tuples, matching upstream's reltuples definition.
func Analyze(pool *storage.Pool, mgr *mvcc.Manager, rel storage.RelFileNode) (AnalyzeStats, error) {
	tx, err := mgr.Begin(mvcc.IsolationReadCommitted)
	if err != nil {
		return AnalyzeStats{}, err
	}
	defer mgr.Rollback(tx)
	snap, err := mgr.SnapshotFor(tx)
	if err != nil {
		return AnalyzeStats{}, err
	}
	nBlocks, err := pool.NBlocks(rel)
	if err != nil {
		return AnalyzeStats{}, err
	}
	out := AnalyzeStats{Pages: int(nBlocks)}
	var totalBytes int64
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		slot, err := pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return out, err
		}
		page := slot.Page()
		if storage.IsNew(page) {
			pool.Unpin(slot)
			continue
		}
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			pool.Unpin(slot)
			return out, err
		}
		for s := uint16(1); s <= uint16(count); s++ {
			t, err := storage.PageGetHeapTuple(page, s)
			if errors.Is(err, storage.ErrUnsupportedItem) {
				// LP_UNUSED / LP_DEAD / LP_REDIRECT slot — not a live
				// tuple, but not a corruption signal either.
				continue
			}
			if err != nil {
				pool.Unpin(slot)
				return out, err
			}
			if !mvcc.TupleVisible(t.Header, snap, tx.XID) {
				continue
			}
			out.Rows++
			totalBytes += int64(int(t.Header.Hoff) + len(t.Data))
		}
		pool.Unpin(slot)
	}
	if out.Rows > 0 {
		out.AvgWidth = float64(totalBytes) / float64(out.Rows)
	}
	return out, nil
}
