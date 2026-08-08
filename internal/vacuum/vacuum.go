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

	"github.com/goopg/goopg/internal/multixact"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/storage"
)

// Stats summarises the outcome of one Vacuum invocation across every
// block in the relation.
type Stats struct {
	Pages        int // blocks visited
	Live         int // tuples that survived this pass
	Dead         int // tuples reclaimed (LP_NORMAL -> LP_UNUSED)
	Frozen       int // tuples whose xmin was rewritten to FrozenTransactionID
	OldestXmin   storage.TransactionID
	NewFrozenXID storage.TransactionID // lowest unfrozen xmin after this pass (0 if all frozen)
	// DeadTIDs is the list of heap (block, offset) pointers that were
	// reclaimed in this pass. Index vacuum uses these to remove stale
	// index entries (M0047-0002).
	DeadTIDs []storage.ItemPointer
}

// VacuumOptions controls optional vacuum behaviours beyond the core dead-tuple
// reclamation. All fields are optional; zero values disable the feature.
type VacuumOptions struct {
	// FSM, when non-nil, receives updated free-space entries after each page
	// prune so subsequent INSERTs can reuse freed space (M0046-0003).
	FSM *storage.FSM
	// VM, when non-nil, gets ALL_VISIBLE bits set per page after the prune
	// so index-only scans can skip heap fetches (M0046-0004).
	VM *storage.VisibilityMap
	// FreezeBelow, when > 0, activates tuple freezing (M0046-0005).
	// Any tuple with xmin < FreezeBelow is rewritten to FrozenTransactionID
	// so XID wraparound cannot make it invisible.
	FreezeBelow storage.TransactionID
	// Horizon, when > 0, overrides the dead-tuple reclamation cutoff that
	// vacuumCore otherwise derives from mgr.OldestXmin(). The VACUUM operator
	// passes the session-local horizon (mgr.OldestXminForProc) for TEMPORARY
	// relations so a concurrent session's older snapshot does not pin reclamation
	// of temp-table rows it cannot see (horizons.spec, M0118-0009).
	Horizon storage.TransactionID
}

// VacuumWithOptions is the full-featured Vacuum entry point. All optional
// behaviours (FSM, VM, freeze) are controlled through opts.
func VacuumWithOptions(pool *storage.Pool, mgr *mvcc.Manager, rel storage.RelFileNode,
	opts VacuumOptions) (Stats, error) {
	return vacuumCore(pool, mgr, rel, opts)
}

// Vacuum runs a heap page-prune across every block of rel using the
// MVCC-derived oldest-xmin horizon. Pages that no longer contain dead
// tuples are still pinned and re-flushed only if any tuple was
// reclaimed; otherwise they're left untouched.
//
// Reclamation goes through the HOT-chain / multixact-aware
// `storage.PageVacuumPrune` (the sibling of the opportunistic
// `PagePruneOpt`): dead HOT chain roots become `ItemIDRedirect` so the
// index entry keeps resolving to the live tip, and an updater-bearing
// multixact xmax is resolved to its updater before the horizon compare.
// When the buffer pool has a `LogHeapPruneOpt` hook wired (the normal
// runtime case), each pruned page emits a logical prune redo record
// carrying the redirect + unused slot lists — replay reproduces the
// same redirects bit-for-bit. When the hook is absent (test pools),
// Vacuum falls back to MarkDirty so the FPI-on-every-dirty path keeps
// the change durable.
//
// VACUUM does not touch indexes — see Reindex for the bridge until
// B-tree page deletion lands.
func Vacuum(pool *storage.Pool, mgr *mvcc.Manager, rel storage.RelFileNode) (Stats, error) {
	return vacuumCore(pool, mgr, rel, VacuumOptions{})
}

// VacuumWithFSM is Vacuum with a Free Space Map (M0046-0003).
func VacuumWithFSM(pool *storage.Pool, mgr *mvcc.Manager, rel storage.RelFileNode, fsm *storage.FSM) (Stats, error) {
	return vacuumCore(pool, mgr, rel, VacuumOptions{FSM: fsm})
}

// VacuumWithFSMAndVM runs vacuum, updating both the FSM (M0046-0003) and the
// Visibility Map (M0046-0004).
func VacuumWithFSMAndVM(pool *storage.Pool, mgr *mvcc.Manager, rel storage.RelFileNode,
	fsm *storage.FSM, vm *storage.VisibilityMap) (Stats, error) {
	return vacuumCore(pool, mgr, rel, VacuumOptions{FSM: fsm, VM: vm})
}

func vacuumCore(pool *storage.Pool, mgr *mvcc.Manager, rel storage.RelFileNode,
	opts VacuumOptions) (Stats, error) {
	horizon := opts.Horizon
	if horizon == storage.InvalidTransactionID {
		horizon = mgr.OldestXmin()
	}
	nBlocks, err := pool.NBlocks(rel)
	if err != nil {
		return Stats{}, err
	}
	stats := Stats{OldestXmin: horizon}
	logPrune := pool.LogHeapPruneOpt()
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

		pageDirty := false

		// Dead-tuple reclamation pass. PageVacuumPrune is HOT-chain-aware
		// (dead chain roots become ItemIDRedirect so the index entry keeps
		// resolving to the live tip) and multixact-aware (an updater-bearing
		// multi xmax resolves its updater before the horizon compare). The old
		// naive "xmax < horizon → remove slot" pass broke HOT chains and treated
		// a raw MultiXactId as an xid — see the freeze-the-dead spec (M0118-0009).
		pr, liveOnPage, err := storage.PageVacuumPrune(page, horizon)
		if err != nil {
			slot.Unlock()
			pool.Unpin(slot)
			return stats, err
		}
		reclaimed := len(pr.Redirects) + len(pr.Unused)
		stats.Live += liveOnPage
		if reclaimed > 0 {
			if logPrune != nil {
				err = pool.MarkDirtyChangeRecord(slot, func() (storage.LSN, error) {
					return logPrune(rel, blk, pr.Redirects, pr.Unused)
				})
				if err != nil {
					slot.Unlock()
					pool.Unpin(slot)
					return stats, err
				}
			} else {
				pool.MarkDirty(slot)
			}
			pageDirty = true
			stats.Dead += reclaimed
			// Collect dead TIDs for index vacuum (M0047-0002). Only the
			// fully-removed (Unused) line pointers may carry an index entry
			// that must be cleared; redirected roots keep their index entry
			// valid. HOT-only Unused tuples have no index entry, so removing a
			// (nonexistent) entry for their TID is a harmless no-op.
			for _, s := range pr.Unused {
				stats.DeadTIDs = append(stats.DeadTIDs, storage.ItemPointer{Block: blk, Offset: s})
			}
		}

		// Tuple-freeze pass (M0046-0005): rewrite old xmin → FrozenTransactionID.
		if opts.FreezeBelow > 0 {
			fs, ferr := storage.PageFreezeOldTuples(page, opts.FreezeBelow)
			if ferr == nil && fs.Frozen > 0 {
				// M0080-0001: emit a logical heap-freeze record
				// when the hook is wired; falls back to MarkDirty
				// (FPI) for test harnesses without WAL.
				if logFrz := pool.LogHeapFreeze(); logFrz != nil {
					if err := pool.MarkDirtyChangeRecord(slot, func() (storage.LSN, error) {
						return logFrz(rel, blk, fs.FrozenSlots)
					}); err != nil {
						slot.Unlock()
						pool.Unpin(slot)
						return stats, err
					}
				} else {
					pool.MarkDirty(slot)
				}
				pageDirty = true
				stats.Frozen += fs.Frozen
			}
			// Track the minimum unfrozen xmin across all pages for relfrozenxid.
			if fs.MinUnfrozenXID != 0 {
				if stats.NewFrozenXID == 0 || fs.MinUnfrozenXID < stats.NewFrozenXID {
					stats.NewFrozenXID = fs.MinUnfrozenXID
				}
			}
			_ = pageDirty
		}

		// Record updated free space in FSM (M0046-0003).
		if opts.FSM != nil {
			opts.FSM.RecordFreeSpaceForPage(rel, blk, page)
		}
		// Set ALL_VISIBLE in VM (M0046-0004).
		if opts.VM != nil {
			if storage.PageAllVisible(page, horizon) {
				opts.VM.SetAllVisible(rel, blk)
			} else {
				opts.VM.ClearBlock(rel, blk)
			}
		}
		slot.Unlock()
		pool.Unpin(slot)
		stats.Pages++
		// stats.Live / stats.Dead are accumulated in the reclamation pass above.
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
func Analyze(pool *storage.Pool, mgr *mvcc.Manager, rel storage.RelFileNode, mxs *multixact.Store) (AnalyzeStats, error) {
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
			// MultiXact store threaded from the caller (the autovacuum
			// Launcher's process-shared store; nil disables the multi path).
			// Resolves an updater-bearing multi xmax to its updater before
			// judging visibility so the live-row count does not undercount a
			// live, only-row-locked tuple as invisible. M0118-0003.
			if !mvcc.TupleVisible(t.Header, snap, tx.XID, storage.InvalidCommandId, nil, mxs) {
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
