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
	"time"

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/access/transam/multixact"
	"github.com/goopg/goopg/internal/storage"
)

// Stats summarises the outcome of one Vacuum invocation across every
// block in the relation.
type Stats struct {
	Pages  int // blocks visited
	Live   int // tuples that survived this pass
	Dead   int // tuples reclaimed (LP_NORMAL -> LP_UNUSED)
	Frozen int // tuples whose xmin was rewritten to FrozenTransactionID
	// SkippedAllVisible / SkippedAllFrozen count blocks jumped by the VM
	// skip. Callers must not advance relfrozenxid when SkippedAllVisible > 0
	// on a non-aggressive pass (vacuumlazy.c skippedallvis guard); all-frozen
	// skips never stall advancement.
	SkippedAllVisible int
	SkippedAllFrozen  int
	OldestXmin        storage.TransactionID
	NewFrozenXID      storage.TransactionID // lowest unfrozen xmin after this pass (0 if all frozen)
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
	// Cost-based throttling (upstream vacuum_cost_* family). Zero delay
	// disables pacing entirely (PG default for manual VACUUM).
	CostDelayMS   int64
	CostLimit     int64
	CostPageHit   int64
	CostPageMiss  int64
	CostPageDirty int64
	// Truncate drops trailing all-empty blocks after the scan
	// (vacuum_truncate GUC / reloption / statement param).
	Truncate bool
	// FailsafeAge, when > 0 and the horizon's XID age reaches it, disables
	// skipping AND cost delays for this pass (upstream failsafe,
	// vacuumlazy.c:793–800 analog; GUC vacuum_failsafe_age).
	FailsafeAge int64
	// Aggressive forces a full scan of every block, ignoring VM skips —
	// upstream's cutoff-driven escalation / DISABLE_PAGE_SKIPPING
	// (vacuumlazy.c:793–800). Anti-wraparound autovacuum and VACUUM (FREEZE)
	// set this.
	Aggressive bool
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
func VacuumWithOptions(pool *storage.Pool, mgr *transam.Manager, rel storage.RelFileNode,
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
func Vacuum(pool *storage.Pool, mgr *transam.Manager, rel storage.RelFileNode) (Stats, error) {
	return vacuumCore(pool, mgr, rel, VacuumOptions{})
}

// VacuumWithFSM is Vacuum with a Free Space Map (M0046-0003).
func VacuumWithFSM(pool *storage.Pool, mgr *transam.Manager, rel storage.RelFileNode, fsm *storage.FSM) (Stats, error) {
	return vacuumCore(pool, mgr, rel, VacuumOptions{FSM: fsm})
}

// VacuumWithFSMAndVM runs vacuum, updating both the FSM (M0046-0003) and the
// Visibility Map (M0046-0004).
func VacuumWithFSMAndVM(pool *storage.Pool, mgr *transam.Manager, rel storage.RelFileNode,
	fsm *storage.FSM, vm *storage.VisibilityMap) (Stats, error) {
	return vacuumCore(pool, mgr, rel, VacuumOptions{FSM: fsm, VM: vm})
}

func vacuumCore(pool *storage.Pool, mgr *transam.Manager, rel storage.RelFileNode,
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
	var costSeen map[storage.BlockNumber]bool
	var costBalance int64

	// Failsafe escalation (upstream vacuum_failsafe_age): when the horizon's
	// age is critical, behave aggressively and drop cost pacing so the pass
	// finishes fast and advances relfrozenxid.
	if opts.FailsafeAge > 0 && mgr != nil {
		if nextXID := mgr.NextXID(); nextXID > horizon &&
			int64(nextXID-horizon) >= opts.FailsafeAge {
			opts.Aggressive = true
			opts.CostDelayMS = 0
		}
	}
	lastNonEmpty := storage.BlockNumber(0)
	skipping := !opts.Aggressive
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		// VM skip (upstream heap_vac_scan_next_block): a non-aggressive pass
		// jumps all-visible blocks entirely — EXCEPT the final block, which
		// upstream always scans for tail-truncation decisions
		// (access/heap/vacuumlazy.c:1726–1729). All-frozen skips are counted
		// separately so they do not stall relfrozenxid advancement.
		isLastBlock := blk == nBlocks-1
		if skipping && opts.VM != nil && !isLastBlock &&
			opts.VM.AllVisible(rel, blk) {
			if opts.VM.AllFrozen(rel, blk) {
				stats.SkippedAllFrozen++
			} else {
				stats.SkippedAllVisible++
			}
			// A skipped block was NOT examined, so it must be assumed
			// non-empty: it is all-visible, which for a heap page means it
			// holds live tuples. Upstream advances `vacrel->nonempty_pages`
			// on this very path (heap_vac_scan_next_block, vacuumlazy.c) —
			// without it, a trailing run of skipped all-visible blocks looks
			// empty to the truncation step below and live data is dropped.
			lastNonEmpty = blk
			continue
		}
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
		if cnt, cerr := storage.PageLinePointerCount(page); cerr == nil && (cnt > 0 || liveOnPage > 0) {
			lastNonEmpty = blk
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
			lastNonEmpty = blk
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
		// Update VM bits (M0046-0004 + probe-audit two-bit extension):
		// ALL_VISIBLE when every remaining tuple is visible to the horizon;
		// additionally ALL_FROZEN when the freeze pass ran and every live
		// tuple sits at-or-below this pass's freeze cutoff.
		if opts.VM != nil {
			switch {
			case opts.FreezeBelow > 0 && storage.PageAllFrozen(page, opts.FreezeBelow):
				opts.VM.SetAllFrozen(rel, blk)
			case storage.PageAllVisible(page, horizon):
				opts.VM.SetAllVisible(rel, blk)
			default:
				opts.VM.ClearBlock(rel, blk)
			}
		}
		// Cost-based throttling (vacuum.c:2472–2490): accumulate per-page
		// cost; when over the limit, sleep a proportional slice capped at
		// 4×delay. First touch in this pass counts as a miss (page had to
		// come from disk), later touches as hits.
		if opts.CostDelayMS > 0 {
			if costSeen == nil {
				costSeen = make(map[storage.BlockNumber]bool)
			}
			c := opts.CostPageHit
			if !costSeen[blk] {
				c = opts.CostPageMiss
				costSeen[blk] = true
			}
			if pageDirty {
				c += opts.CostPageDirty
			}
			costBalance += c
			if costBalance >= opts.CostLimit {
				d := opts.CostDelayMS * costBalance / opts.CostLimit
				if d > 4*opts.CostDelayMS {
					d = 4 * opts.CostDelayMS
				}
				time.Sleep(time.Duration(d) * time.Millisecond)
				costBalance = 0
			}
		}

		slot.Unlock()
		pool.Unpin(slot)
		stats.Pages++
		// stats.Live / stats.Dead are accumulated in the reclamation pass above.
	}

	// Tail truncation (vacuum_truncate): drop trailing never-populated /
	// fully-empty blocks. WAL-first emission + invalidation + smgr shrink are
	// encapsulated in the pool helper; a missing hook (test harnesses) makes
	// this a no-op rather than an unsafe truncate.
	if opts.Truncate {
		keep := lastNonEmpty + 1
		if keep < nBlocks {
			_ = pool.TruncateRelationTail(rel, keep)
		}
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
func Analyze(pool *storage.Pool, mgr *transam.Manager, rel storage.RelFileNode, mxs *multixact.Store) (AnalyzeStats, error) {
	tx, err := mgr.Begin(transam.IsolationReadCommitted)
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
			if !transam.TupleVisible(t.Header, snap, tx.XID, storage.InvalidCommandId, nil, mxs) {
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
