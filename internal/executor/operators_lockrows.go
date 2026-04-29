package executor

import (
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// lockRowsOp is the runtime for `SELECT … FOR UPDATE / FOR SHARE`
// (M0021-0003 — Stage A). Acquires the upstream-canonical
// relation-level lock on each LockedRel.Table at Open time and
// passes child rows through unchanged.
//
// Stage A scope:
//
//   - Acquires `RowShareLock` on the relation regardless of
//     LockStrength. Mirrors upstream — `SELECT … FOR UPDATE` and
//     `SELECT … FOR SHARE` both take RowShareLock at the
//     relation level. RowShareLock conflicts with `ExclusiveLock`
//     and `AccessExclusiveLock` (DROP TABLE / ALTER TABLE), which
//     is the correctness property Stage A delivers: schema-change
//     readers of the locked rows can't yank the table out from
//     under them. RowShareLock is COMPATIBLE with `RowExclusiveLock`
//     (UPDATE / INSERT / DELETE) — concurrent writers proceed
//     unblocked at the relation level.
//
//   - The actual tuple-level pessimistic locking (xmax stamping
//     with HEAP_XMAX_LOCK_ONLY infomask, MVCC visibility hooks,
//     row-lock WAL records) is the deferred follow-up task
//     "Tuple-level pessimistic locking on top of M0012 lock
//     manager" — Stage A doesn't claim to provide tuple-level
//     blocking yet. Without it, concurrent UPDATEs to the same
//     row a SELECT FOR UPDATE just observed proceed without
//     blocking. The relation-level lock is the structural seam
//     that follow-up work attaches to.
//
//   - WaitPolicy NoWait / SkipLocked are accepted at parse and
//     analyze time for AST stability, but the executor rejects
//     non-Block policies here with `0A000` so unmigrated runtimes
//     never silently downgrade to default-blocking. M0021-0003
//     follow-up promotes the wait-policy paths.
//
// Locks acquired here are transaction-scoped — released by
// `LockMgr.ReleaseAll(backendID)` in `internal/server/dispatch.go`
// at commit/rollback, mirroring the existing relation-lock
// lifecycle (acquireRelLock callers don't release manually
// either).
type lockRowsOp struct {
	plan  *planner.LockRows
	ctx   *Context
	child Operator

	// scan is the underlying seqScanOp resolved at Open time —
	// found by walking the child chain past Project / Filter
	// wrappers. Each row that bubbles up through child.Next()
	// can be traced back to (block, slot) via scan.currentTID.
	// nil when the child tree has no seqScanOp leaf (e.g. an
	// IndexScan, which Stage A doesn't yet support per-row
	// stamping for); in that case Next() falls through to
	// pass-through behaviour and only the relation-level lock
	// from Open applies.
	scan *seqScanOp
	// lockStrength is the HeapXmax* infomask bit corresponding
	// to the locks slice (FOR UPDATE → ExclLock, FOR SHARE →
	// ShrLock). Resolved once at Open. Multiple LockedRels
	// targeting the same scan would need merging under
	// strongest-wins; v0 keeps that deferred and uses the first
	// LockedRel's strength.
	lockStrength uint16

	// Two-pass buffer. seqScanOp holds the page's RLock
	// across multiple Next() calls (RUnlock fires only at
	// page exhaustion / Close), so we can't grab the slot's
	// write Lock for xmax-stamping while the scan is mid-page.
	// First Next call drains the entire child chain, recording
	// (rel, ptr, row) per row, then runs the stamp pass, then
	// yields rows from the buffer. Memory cost is the result
	// set; SELECT FOR UPDATE typically targets a small range
	// so the buffering is acceptable for Stage A. Streaming
	// per-tuple stamping requires a deeper seqScan refactor
	// (one Pin/RLock per Next) and is deferred.
	pending []pendingLockedRow
	pos     int
	drained bool
}

type pendingLockedRow struct {
	rel storage.RelFileNode
	ptr storage.ItemPointer
	row Row
	// haveTID reports whether currentTID returned ok=true at
	// capture time; rows scanned through non-seqScan leaves
	// (IndexScan, Values) get haveTID=false and skip the
	// stamp pass — only the relation-level lock applies.
	haveTID bool
}

func newLockRowsOp(p *planner.LockRows, child Operator) *lockRowsOp {
	return &lockRowsOp{plan: p, child: child}
}

// findSeqScan walks the child operator chain past Project /
// Filter wrappers to surface the underlying *seqScanOp. Returns
// nil when the leaf isn't a seqScanOp (e.g. IndexScan, Values).
// Used by lockRowsOp.Open to wire the per-row currentTID hook.
func findSeqScan(op Operator) *seqScanOp {
	for {
		switch v := op.(type) {
		case *seqScanOp:
			return v
		case *projectOp:
			op = v.child
		case *filterOp:
			op = v.child
		default:
			return nil
		}
	}
}

func (o *lockRowsOp) Schema() planner.Schema { return o.plan.Output() }

func (o *lockRowsOp) Open(ctx *Context) error {
	o.ctx = ctx
	// Resolve the lock-strength bit for the heap-tuple
	// stamper. v0 supports a single FOR UPDATE / FOR SHARE
	// strength per LockRows (multi-clause merge under
	// strongest-wins is deferred); use the first LockedRel's
	// strength.
	if len(o.plan.Locks) > 0 {
		switch o.plan.Locks[0].Strength {
		case planner.LockStrengthForShare:
			o.lockStrength = storage.HeapXmaxShrLock
		default:
			o.lockStrength = storage.HeapXmaxExclLock
		}
	}
	for i := range o.plan.Locks {
		lk := &o.plan.Locks[i]
		rel := ctx.Catalog.RelFileNode(lk.Table)
		var err error
		switch lk.WaitPolicy {
		case planner.LockWaitBlock:
			err = ctx.acquireRelLock(rel, lockmgr.RowShareLock)
		case planner.LockWaitNoWait:
			// NOWAIT: try once and bail with 55P03 if the
			// relation lock isn't immediately grantable.
			// Mirrors upstream's "could not obtain lock on
			// row" diagnostic at the relation-coarse layer
			// goopg has today.
			err = ctx.tryAcquireRelLock(rel, lockmgr.RowShareLock)
		case planner.LockWaitSkipLocked:
			// SKIP LOCKED needs tuple-level lock probing to
			// silently drop contended rows from the result.
			// goopg's row-locking is relation-coarse — there
			// are no individual rows to skip, only the whole
			// relation. Reject here so users see the specific
			// "tuple-level pessimistic locking is the
			// follow-up" message rather than silently producing
			// an empty result on contention.
			return &ExecError{
				Code:    "0A000",
				Pos:     o.plan.Pos(),
				Message: "SKIP LOCKED requires tuple-level pessimistic locking (deferred follow-up to M0021)",
			}
		default:
			return &ExecError{
				Code:    "XX000",
				Pos:     o.plan.Pos(),
				Message: "unexpected wait policy",
			}
		}
		if err != nil {
			if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
				ee.Pos = o.plan.Pos()
			}
			return err
		}
	}
	if err := o.child.Open(ctx); err != nil {
		return err
	}
	o.scan = findSeqScan(o.child)
	return nil
}

// Next implements the two-pass lock-then-yield protocol. First
// call: drain the child chain (capturing TID per row), then run
// the stamp pass (per-row PageSetHeapTupleLockOnly + WAL emit),
// then yield the buffered rows. Subsequent calls return rows
// from the buffer. EOF when the buffer is exhausted.
func (o *lockRowsOp) Next() (Row, error) {
	if !o.drained {
		if err := o.drainAndStamp(); err != nil {
			return nil, err
		}
	}
	if o.pos >= len(o.pending) {
		return nil, EOF
	}
	row := o.pending[o.pos].row
	o.pos++
	return row, nil
}

// drainAndStamp runs phases 1 and 2 of the two-pass protocol:
// pull every row from the child chain (capturing each row's
// scan TID inline so the seqScan's curBlock/curSlot is
// authoritative), then — once the child has hit EOF and
// released all page RLocks — run the stamp pass that pins each
// affected page exclusively, calls PageSetHeapTupleLockOnly,
// and emits the row-lock WAL record.
func (o *lockRowsOp) drainAndStamp() error {
	o.drained = true
	for {
		row, err := o.child.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return err
		}
		entry := pendingLockedRow{row: row}
		if o.scan != nil {
			if rel, ptr, ok := o.scan.currentTID(); ok {
				entry.rel = rel
				entry.ptr = ptr
				entry.haveTID = true
			}
		}
		o.pending = append(o.pending, entry)
	}
	for i := range o.pending {
		e := &o.pending[i]
		if !e.haveTID {
			continue
		}
		if err := o.stampLock(e.rel, e.ptr); err != nil {
			return err
		}
	}
	return nil
}

// stampLock pins the page exclusively, calls
// PageSetHeapTupleLockOnly, marks dirty through the LogHeapLock
// change-record hook (or falls back to MarkDirty when the
// pool's hook isn't configured — preserves crash safety via
// FPI emission), and unpins. Mirrors writeHeapRow's
// markHeapInsertDirty pattern.
func (o *lockRowsOp) stampLock(rel storage.RelFileNode, ptr storage.ItemPointer) error {
	slot, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: ptr.Block})
	if err != nil {
		return err
	}
	slot.Lock()
	if err := storage.PageSetHeapTupleLockOnly(slot.Page(), ptr.Offset, o.ctx.Tx.XID, o.lockStrength); err != nil {
		slot.Unlock()
		o.ctx.Pool.Unpin(slot)
		return err
	}
	derr := markHeapLockDirty(o.ctx.Pool, slot, rel, ptr.Block, ptr.Offset, o.ctx.Tx.XID, o.lockStrength)
	slot.Unlock()
	o.ctx.Pool.Unpin(slot)
	return derr
}

// markHeapLockDirty centralises the choice between
// MarkDirtyChangeRecord (when LogHeapLock is wired) and the
// conservative fallback MarkDirty (when none is). Mirrors
// markHeapInsertDirty's shape — caller holds slot.Lock; the
// change-record path reads page bytes inline, safe under
// exclusive content latch.
func markHeapLockDirty(
	pool *storage.Pool, slot *storage.Slot,
	rel storage.RelFileNode, blk storage.BlockNumber,
	lineSlot uint16, xmax storage.TransactionID, lockStrength uint16,
) error {
	logLock := pool.LogHeapLock()
	if logLock == nil {
		pool.MarkDirty(slot)
		return nil
	}
	return pool.MarkDirtyChangeRecord(slot, func() (storage.LSN, error) {
		return logLock(rel, blk, lineSlot, xmax, lockStrength)
	})
}

func (o *lockRowsOp) Close() error { return o.child.Close() }
