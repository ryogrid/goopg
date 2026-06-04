package executor

import (
	"fmt"
	"context"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/mvcc"
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

	// scan is the underlying TID-providing leaf operator
	// resolved at Open time — found by walking the child chain
	// past Project / Filter wrappers. Each row that bubbles up
	// through child.Next() can be traced back to (block, slot)
	// via scan.currentTID. nil when the child tree has no
	// supported leaf (e.g. Values); in that case Next() falls
	// through to pass-through and only the relation-level lock
	// from Open applies. Both seqScanOp (M0021 step 2a) and
	// indexScanOp (M0021 step 2c) implement currentTIDProvider.
	scan currentTIDProvider
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

	// maxDrain, when > 0, limits drainAndStamp to at most maxDrain rows.
	// Used by EXISTS (SELECT ... FOR UPDATE) to stop after the first match
	// instead of scanning the full inner table. M0100-0005.
	maxDrain int
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
	// newPtr/newPtrValid: set when stampLock followed a committed-update
	// CTID chain to a live successor. lockRowsOp.Next() refetches the
	// row from newPtr so callers see the post-update values.
	newPtr      storage.ItemPointer
	newPtrValid bool
}

func newLockRowsOp(p *planner.LockRows, child Operator) *lockRowsOp {
	return &lockRowsOp{plan: p, child: child}
}

// currentTIDProvider is the interface a scan leaf implements to
// expose the (rel, ItemPointer) of its most recently emitted
// row. Implemented by *seqScanOp (M0021 step 2a) and
// *indexScanOp (M0021 step 2c). lockRowsOp resolves this at
// Open via findScanLeaf and queries it after each child.Next
// to stamp per-row lock-only xmax.
type currentTIDProvider interface {
	currentTID() (storage.RelFileNode, storage.ItemPointer, bool)
}

// findScanLeaf walks the child operator chain past Project /
// Filter wrappers to surface a TID-providing leaf operator
// (seqScanOp / indexScanOp). Returns nil when the leaf is
// neither (e.g. Values, CTEScan); lockRowsOp falls through to
// pass-through Next in that case with only relation-level
// lock applied.
func findScanLeaf(op Operator) currentTIDProvider {
	for {
		switch v := op.(type) {
		case *seqScanOp:
			return v
		case *indexScanOp:
			return v
		case *projectOp:
			op = v.child
		case *filterOp:
			op = v.child
		case *setOp:
			// Partition UNION ALL: setOp implements currentTIDProvider and
			// delegates to whichever child is currently active (left while
			// !leftDone, right once leftDone). M0100-0005 follow-up.
			return v
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
			// FOR SHARE uses a shared (read-intent) lock.
			// FOR KEY SHARE is handled by lockStrengthFromParser and maps to ForShare.
			o.lockStrength = storage.HeapXmaxShrLock
		default:
			// FOR UPDATE (and FOR NO KEY UPDATE mapped to ForUpdate) use an exclusive lock.
			o.lockStrength = storage.HeapXmaxExclLock
		}
	}
	for i := range o.plan.Locks {
		lk := &o.plan.Locks[i]
		// Materialized views do not support row-level locking.
		if lk.Table != nil && lk.Table.IsMatView {
			return &ExecError{
				Code:    "55000",
				Pos:     o.plan.Pos(),
				Message: fmt.Sprintf(`cannot lock rows in materialized view "%s"`, lk.Table.Name),
			}
		}
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
	o.scan = findScanLeaf(o.child)
	return nil
}

// Next implements the two-pass lock-then-yield protocol. First
// call: drain the child chain (capturing TID per row), then run
// the stamp pass (per-row PageSetHeapTupleLockOnly + WAL emit),
// then yield the buffered rows. Subsequent calls return rows
// from the buffer. EOF when the buffer is exhausted.
func (o *lockRowsOp) Next() (TupleSlot, error) {
	if !o.drained {
		if err := o.drainAndStamp(); err != nil {
			return nil, err
		}
	}
	if o.pos >= len(o.pending) {
		return nil, EOF
	}
	entry := o.pending[o.pos]
	o.pos++
	// When stampLock followed a committed-update chain, refetch the row
	// from the live successor slot so callers see updated values.
	if entry.newPtrValid {
		if row, err := o.refetchRow(entry.rel, entry.newPtr); err != nil {
			return nil, err
		} else if row != nil {
			return asSlot(o.Schema(), row), nil
		}
	}
	return asSlot(o.Schema(), entry.row), nil
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
	hitLimit := false
	for {
		if o.maxDrain > 0 && len(o.pending) >= o.maxDrain {
			hitLimit = true
			break
		}
		slot, err := o.child.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return err
		}
		// Materialize at retention boundary: lockRowsOp's two-pass
		// protocol holds rows across the entire stamp loop.
		// (M0071-0010 Stage B.)
		row := slot.Materialize().Row()
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
	// When we stopped early (maxDrain limit hit), the child scan still holds
	// its page RLock. Close the child to release it before the stamp pass
	// acquires exclusive page locks — otherwise we deadlock. M0100-0005.
	if hitLimit {
		_ = o.child.Close()
	}
	for i := range o.pending {
		e := &o.pending[i]
		if !e.haveTID {
			continue
		}
		successor, followed, err := o.stampLock(e.rel, e.ptr)
		if err != nil {
			return err
		}
		if followed {
			e.newPtr = successor
			e.newPtrValid = true
		}
	}
	return nil
}

// stampLock acquires a tuple-level lock and stamps the lock-only xmax on the
// heap tuple at ptr. Returns (successorPtr, followed, err):
//   - followed=false: stamped at ptr (or nothing stamped for dead-end cases)
//   - followed=true: followed a committed-update CTID chain; successorPtr is the
//     live tuple that was stamped. Caller should update entry.newPtr.
//
// When a real non-lock-only xmax from another xact is present:
//   - If the xmax is still in-progress: waits for it (produces <waiting ...>
//     in isolation tests) and then checks the final state.
//   - If the xmax committed: follows the CTID chain to the live successor.
//   - If the xmax aborted: the row is live; re-stamps at original ptr.
func (o *lockRowsOp) stampLock(rel storage.RelFileNode, ptr storage.ItemPointer) (storage.ItemPointer, bool, error) {
	// M0093: SELECT FOR UPDATE/SHARE stamps lock-only xmax with the
	// transaction's XID; materialise it BEFORE acquiring the tuple
	// lock so the lock holder's identity is the real XID (mismatched
	// holder identity breaks UPDATE's blocks-on-foreign-lock check).
	if err := o.ctx.MaterializeWriterXID(); err != nil {
		return storage.ItemPointer{}, false, err
	}
	// Acquire the tuple-level lock first so a concurrent UPDATE
	// that races with us can't slip through between the xmax
	// stamp and the lock registration.
	if err := o.ctx.acquireTupleLock(rel, ptr, o.tupleLockMode()); err != nil {
		return storage.ItemPointer{}, false, err
	}
	return o.stampLockInner(rel, ptr, 0)
}

// stampLockInner is the recursive inner loop for stampLock, bounded by depth
// to prevent infinite chains. depth=0 on first call.
func (o *lockRowsOp) stampLockInner(rel storage.RelFileNode, ptr storage.ItemPointer, depth int) (storage.ItemPointer, bool, error) {
	if depth > 16 {
		return storage.ItemPointer{}, false, nil // chain too deep
	}
	slot, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: ptr.Block})
	if err != nil {
		return storage.ItemPointer{}, false, err
	}
	slot.Lock()
	// M0100-0005f + M0100-0005-lcku: handle real non-lock-only xmax from
	// another transaction. Two cases:
	// (a) Non-key update (HeapKeysUpdated not set) AND our lock is FOR KEY SHARE:
	//     preserve M0100-0005f semantics — skip stamping without waiting.
	//     FOR KEY SHARE does not conflict with non-key-column updates.
	// (b) Key-column update (HeapKeysUpdated set) OR our lock is FOR UPDATE:
	//     wait for the updater, then follow the CTID chain (RC) or raise 40001 (RR/SER).
	tup, gerr := storage.PageGetHeapTuple(slot.Page(), ptr.Offset)
	if gerr == nil &&
		tup.Header.Xmax != storage.InvalidTransactionID &&
		!storage.IsHeapTupleLockOnly(tup.Header.Infomask) &&
		tup.Header.Xmax != o.ctx.Tx.XID {

		keysUpdated := (tup.Header.Infomask2 & storage.HeapKeysUpdated) != 0
		keyConflict := o.lockStrength == storage.HeapXmaxExclLock || keysUpdated
		if !keyConflict {
			// Non-key update with FOR KEY SHARE: no conflict.
			// Preserve M0100-0005f: do not overwrite real updater's xmax.
			slot.Unlock()
			o.ctx.Pool.Unpin(slot)
			return storage.ItemPointer{}, false, nil
		}

		xmax := tup.Header.Xmax
		ctid := tup.Header.CTID
		slot.Unlock()
		o.ctx.Pool.Unpin(slot)

		// Wait for in-progress updater to commit or abort.
		if o.ctx.TxnMgr != nil && o.ctx.TxnMgr.IsXIDActive(xmax) {
			qctx := o.ctx.Ctx
			if qctx == nil {
				qctx = context.Background()
			}
			if werr := o.ctx.TxnMgr.WaitForXID(qctx, xmax); werr != nil {
				// Context cancelled — skip this row silently.
				return storage.ItemPointer{}, false, nil
			}
		}

		if o.ctx.TxnMgr != nil && o.ctx.TxnMgr.HasAbortedXID(xmax) {
			// Updater rolled back: row is live at original ptr. Stamp it.
			return o.stampAtPtr(rel, ptr)
		}

		// Updater committed: under RR/SER raise a serialization error.
		// Under RC: follow CTID chain to find the live successor.
		if o.ctx.Tx.Isolation != mvcc.IsolationReadCommitted {
			return storage.ItemPointer{}, false, &ExecError{
				Code:    "40001",
				Message: "could not serialize access due to concurrent update",
			}
		}
		next := ctid
		if next.Block == ptr.Block && next.Offset == ptr.Offset {
			// CTID points to self — deleted row, no live successor.
			return storage.ItemPointer{}, false, nil
		}
		// Acquire lockmgr lock on the successor before reading it.
		if err := o.ctx.acquireTupleLock(rel, next, o.tupleLockMode()); err != nil {
			return storage.ItemPointer{}, false, err
		}
		succ, _, err := o.stampLockInner(rel, next, depth+1)
		if err != nil {
			return storage.ItemPointer{}, false, err
		}
		if succ == (storage.ItemPointer{}) {
			return storage.ItemPointer{}, false, nil
		}
		// Indicate that the entry's row data should be refetched.
		return succ, true, nil
	}

	// Tuple is live (no real updater xmax from another xact). Stamp it.
	if gerr != nil {
		slot.Unlock()
		o.ctx.Pool.Unpin(slot)
		return storage.ItemPointer{}, false, nil
	}
	if err := storage.PageSetHeapTupleLockOnly(slot.Page(), ptr.Offset, o.ctx.Tx.XID, o.lockStrength); err != nil {
		slot.Unlock()
		o.ctx.Pool.Unpin(slot)
		return storage.ItemPointer{}, false, err
	}
	derr := markHeapLockDirty(o.ctx.Pool, slot, rel, ptr.Block, ptr.Offset, o.ctx.Tx.XID, o.lockStrength)
	slot.Unlock()
	o.ctx.Pool.Unpin(slot)
	return ptr, false, derr
}

// stampAtPtr stamps a lock-only xmax at ptr. Used when the original updater's
// xmax was aborted and the row is live (lockmgr lock already acquired by caller).
func (o *lockRowsOp) stampAtPtr(rel storage.RelFileNode, ptr storage.ItemPointer) (storage.ItemPointer, bool, error) {
	slot, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: ptr.Block})
	if err != nil {
		return storage.ItemPointer{}, false, err
	}
	slot.Lock()
	tup, gerr := storage.PageGetHeapTuple(slot.Page(), ptr.Offset)
	if gerr == nil &&
		tup.Header.Xmax != storage.InvalidTransactionID &&
		!storage.IsHeapTupleLockOnly(tup.Header.Infomask) &&
		tup.Header.Xmax != o.ctx.Tx.XID {
		// Another real updater arrived while we waited — skip.
		slot.Unlock()
		o.ctx.Pool.Unpin(slot)
		return storage.ItemPointer{}, false, nil
	}
	if gerr != nil {
		slot.Unlock()
		o.ctx.Pool.Unpin(slot)
		return storage.ItemPointer{}, false, nil
	}
	if err := storage.PageSetHeapTupleLockOnly(slot.Page(), ptr.Offset, o.ctx.Tx.XID, o.lockStrength); err != nil {
		slot.Unlock()
		o.ctx.Pool.Unpin(slot)
		return storage.ItemPointer{}, false, err
	}
	derr := markHeapLockDirty(o.ctx.Pool, slot, rel, ptr.Block, ptr.Offset, o.ctx.Tx.XID, o.lockStrength)
	slot.Unlock()
	o.ctx.Pool.Unpin(slot)
	return ptr, false, derr
}

// refetchRow reads and decodes the heap tuple at (rel, ptr) using the table
// columns from o.plan.Locks. Returns nil when the relation is not in the lock
// list or the tuple cannot be decoded.
func (o *lockRowsOp) refetchRow(rel storage.RelFileNode, ptr storage.ItemPointer) (Row, error) {
	var cols []catalog.Column
	for i := range o.plan.Locks {
		if o.ctx.Catalog.RelFileNode(o.plan.Locks[i].Table) == rel {
			cols = o.plan.Locks[i].Table.Columns
			break
		}
	}
	if cols == nil {
		return nil, nil
	}
	slot, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: ptr.Block})
	if err != nil {
		return nil, err
	}
	slot.RLock()
	tup, gerr := storage.PageGetHeapTuple(slot.Page(), ptr.Offset)
	slot.RUnlock()
	o.ctx.Pool.Unpin(slot)
	if gerr != nil {
		return nil, nil
	}
	row := make(Row, len(cols))
	natts := int(tup.Header.Infomask2 & 0x07FF)
	if err := DecodeRowIntoMctxPGTuple(row, cols, tup.Data, tup.Bitmap, natts, nil); err != nil {
		return nil, err
	}
	return cloneRowOwned(row), nil
}

// tupleLockMode picks the lockmgr Mode used for the tuple-tag
// acquire based on the SELECT FOR clause's strength (M0021 step
// 4). FOR SHARE → RowShareLock so multiple shared holders
// coexist on the same tuple tag; FOR UPDATE → ExclusiveLock so
// a single writer blocks every other holder. Mirrors upstream's
// `tuple_lock_extended` mode mapping at the lockmgr level
// without needing MultiXact infrastructure for v0 — the
// lockmgr's existing conflict matrix already supplies the
// multi-holder semantics.
func (o *lockRowsOp) tupleLockMode() lockmgr.Mode {
	if o.lockStrength == storage.HeapXmaxShrLock || o.lockStrength == storage.HeapXmaxKeyShrLock {
		return lockmgr.RowShareLock
	}
	return lockmgr.ExclusiveLock
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

func (o *lockRowsOp) Close() error {
	o.pending = nil
	return o.child.Close()
}
