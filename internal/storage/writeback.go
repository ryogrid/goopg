package storage

import "sync/atomic"

// writeback.go implements pg_stat_io's writeback/writeback_time columns
// (M0122-0003 follow-up; see .ralph/deferral_ledger.md 2026-07-05 row on
// pg_stat_io fsyncs/fsync_time for the sibling op this narrows). Upstream
// (postgres/src/backend/storage/buffer/bufmgr.c's ScheduleBufferTagForWriteback
// / IssuePendingWritebacks) accumulates dirty writes into a per-context
// WritebackContext (one each for the checkpointer, the bgwriter, and — when
// backend_flush_after is set — the issuing backend), coalesces them into up
// to WRITEBACK_MAX_PENDING_FLUSHES per-relation-segment ranges, and issues
// sync_file_range(2) once *_flush_after pages have accumulated.
//
// goopg simplifies the accounting to a single running page counter per
// context (no multi-range coalescing) that triggers one SyncFileRangeHint
// call, over the just-written relation's current extent, once the
// threshold is crossed — a real kernel write-behind hint at the real
// GUC-configured cadence, not a fabricated counter. The simplification
// (one relation per hint instead of a coalesced batch across whichever
// relations were touched since the last hint) is recorded in the deferral
// ledger. (The sibling "background writer/checkpointer writes/write_bytes/
// write_time stay 0" simplification this file's doc comment used to name is
// now closed — see Pool.sharedBgwriterWrittenCount/sharedCheckpointWrittenCount
// in bufpool.go and their OnBgwriterWriteWait/Done/OnCheckpointerWriteWait/Done
// hooks, a distinct real counter from this file's writeback-hint accounting.)
// writeback_time is added separately by the caller's
// On*WritebackDone hook (initdb.Open), which measures the real elapsed
// time around SyncFileRangeHint the same way OnFlushDone/OnExtendDone
// measure write_time/extend_time.
func (p *Pool) accountCheckpointerWrite(rel RelFileNode) {
	p.accountWrite(rel, p.checkpointFlushAfterBlocks.Load(), &p.pendingCheckpointFlushBlocks,
		&p.sharedCheckpointWritebackCount, p.OnCheckpointerWritebackWait, p.OnCheckpointerWritebackDone)
}

func (p *Pool) accountBgwriterWrite(rel RelFileNode) {
	p.accountWrite(rel, p.bgwriterFlushAfterBlocks.Load(), &p.pendingBgwriterFlushBlocks,
		&p.sharedBgwriterWritebackCount, p.OnBgwriterWritebackWait, p.OnBgwriterWritebackDone)
}

// accountBackendWrite resolves the calling backend's own effective
// backend_flush_after threshold via BackendFlushAfterOverride (upstream's
// GUC is PGC_USERSET, so each session may diverge from the boot-time
// default) before falling back to backendFlushAfterBlocks — the
// process-wide default set at boot / by a caller with no per-session
// context (M0122-0003 writeback follow-up, "backend_flush_after applied
// process-wide" simplification).
func (p *Pool) accountBackendWrite(rel RelFileNode) {
	threshold := p.backendFlushAfterBlocks.Load()
	if p.BackendFlushAfterOverride != nil {
		if n, ok := p.BackendFlushAfterOverride(); ok {
			threshold = n
		}
	}
	p.accountWrite(rel, threshold, &p.pendingBackendFlushBlocks,
		&p.sharedBackendWritebackCount, p.OnBackendWritebackWait, p.OnBackendWritebackDone)
}

// accountWrite is the shared threshold-crossing logic behind
// accountCheckpointerWrite/accountBgwriterWrite/accountBackendWrite. It
// takes the resolved threshold and the specific counters/hooks as
// parameters rather than a context enum so each call site stays a plain,
// greppable, independently-labelled method matching this package's
// existing per-context counter style.
func (p *Pool) accountWrite(rel RelFileNode, threshold int32, pendingBlocks *atomic.Int64,
	writebackCount *atomic.Int64, onWait, onDone func()) {
	if threshold <= 0 {
		return
	}
	if pendingBlocks.Add(1) < int64(threshold) {
		return
	}
	pendingBlocks.Store(0)

	if onWait != nil {
		onWait()
	}
	err := p.mgr.SyncFileRangeHint(rel)
	if onDone != nil {
		onDone()
	}
	if err != nil {
		// ErrWritebackUnsupported (or any real error): no writeback
		// actually happened, so don't count one.
		return
	}
	writebackCount.Add(1)
}
