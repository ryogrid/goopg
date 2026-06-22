package executor

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/mctx"
	"github.com/goopg/goopg/internal/multixact"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// maxEPQRetries is the maximum number of EvalPlanQual re-checks before
// escalating to SQLSTATE 40001. M0098-0004.
const maxEPQRetries = 3

// maxEPQRetriesRC is the EvalPlanQual re-check backstop under READ COMMITTED.
// PostgreSQL never surfaces a serialization failure for plain UPDATE/DELETE
// row contention under READ COMMITTED — it blocks (XactLockTableWait) and
// re-evaluates against the latest row version until it can apply the change.
// goopg mirrors that here: each re-check is paced by epqWait blocking on the
// current xmax holder, and the wait-for-graph still breaks genuine deadlock
// cycles. This high backstop exists only to bound a pathological livelock
// (sustained lapping with no holder ever yielding) rather than to enforce a
// first-update-wins policy; under any realistic workload the update applies
// long before it is reached. Hitting it under high pgbench TPC-B contention
// was the "current transaction is aborted" client-abort cascade.
const maxEPQRetriesRC = 100000

// epqRetryLimit returns the EvalPlanQual retry budget before escalating to a
// serialization failure, by isolation level. READ COMMITTED retries (blocking)
// essentially until success; REPEATABLE READ / SERIALIZABLE surface 40001
// promptly to preserve first-update-wins semantics.
func epqRetryLimit(iso mvcc.IsolationLevel) int {
	if iso == mvcc.IsolationReadCommitted {
		return maxEPQRetriesRC
	}
	return maxEPQRetries
}

// maxWFGHops is the maximum chain length walked during WFG cycle detection.
// Limits the O(N) scan to a constant bound under adversarial workloads.
const maxWFGHops = 64

// Process-global wait-for graph for EPQ deadlock detection.
// Maps waitingXID → blockingXID. Protected by wfgMu. M0099-0004.
var (
	wfgMu        sync.Mutex
	waitForGraph = make(map[storage.TransactionID]storage.TransactionID)
)

// Self-modification error sentinels (ERRCODE_TRIGGERED_DATA_CHANGE_VIOLATION = 09000).
// Raised when a sub-command triggered by the current command already modified the tuple.
var (
	errTupleAlreadyModifiedByUpdate = &ExecError{
		Code:    "09000",
		Message: "tuple to be updated was already modified by an operation triggered by the current command",
	}
	errTupleAlreadyModifiedByDelete = &ExecError{
		Code:    "09000",
		Message: "tuple to be deleted was already modified by an operation triggered by the current command",
	}
)

// registerWFGAndCheckCycle adds the edge myXID→blockingXID and walks the
// graph up to maxWFGHops looking for a cycle (deadlock). Returns true when
// a cycle is detected; the edge is removed before returning (caller must NOT
// call deregisterWFG). Returns false when no cycle is found; the caller must
// call deregisterWFG after the wait completes.
func registerWFGAndCheckCycle(myXID, blockingXID storage.TransactionID) bool {
	wfgMu.Lock()
	defer wfgMu.Unlock()
	waitForGraph[myXID] = blockingXID
	cur := blockingXID
	for i := 0; i < maxWFGHops; i++ {
		if cur == myXID {
			delete(waitForGraph, myXID)
			return true
		}
		next, ok := waitForGraph[cur]
		if !ok {
			return false
		}
		cur = next
	}
	return false
}

// deregisterWFG removes myXID from the wait-for graph after a snapshot
// refresh completes.
func deregisterWFG(myXID storage.TransactionID) {
	wfgMu.Lock()
	delete(waitForGraph, myXID)
	wfgMu.Unlock()
}

// epqWait detects deadlock cycles via the wait-for graph (WFG), blocks on
// the holder XID, then refreshes the snapshot. Returns true if a deadlock
// cycle is confirmed — caller must immediately escalate to SQLSTATE 40001.
// Returns false otherwise (caller retries via the EPQ loop).
//
// WFG cycle detection (M0099-0004) provides earlier deadlock identification:
// a confirmed 2-node cycle (TX1→TX2, TX2→TX1) yields 40001 immediately for
// one participant. WaitForXID blocks until the holder commits or aborts —
// all callers release page pins before reaching here, so no pin-hold
// deadlock is possible. Context cancellation (connection close, query
// timeout) is propagated via commitCond.Broadcast inside WaitForXID.
// M0098-0004, M0099-0004, M0100-0003.
func epqWait(ctx *Context, xmax storage.TransactionID) (deadlock bool) {
	if ctx.TxnMgr == nil {
		return false
	}
	if ctx.Tx.XID != storage.InvalidTransactionID {
		if registerWFGAndCheckCycle(ctx.Tx.XID, xmax) {
			return true
		}
		defer deregisterWFG(ctx.Tx.XID)
	}
	// PG parity: block until the holder transaction commits or aborts.
	// All four call sites release page pins before reaching here
	// (verified at lines 923-924, 1159-1160, 1333-1334, 1520-1521).
	if ctx.Ctx != nil {
		// Ignore cancellation errors — treat as a non-deadlock signal
		// and fall through to the snapshot refresh + caller retry.
		_ = ctx.TxnMgr.WaitForXID(ctx.Ctx, xmax)
	}
	// Refresh the snapshot so the next epqRecheckVisible call sees any
	// committed changes from the conflicting transaction.
	if snap, serr := ctx.TxnMgr.SnapshotFor(ctx.Tx); serr == nil {
		ctx.Snap = snap.Clone()
	}
	return false
}

// epqRecheckVisible re-reads the tuple at (rel, blk, slot) and reports
// whether it is still visible under the current snapshot. Returns false if
// the row was committed by the conflicting transaction (skip the row),
// true if the conflicting transaction aborted (row is still live, retry).
// M0098-0004.
func epqRecheckVisible(ctx *Context, rel storage.RelFileNode, blk storage.BlockNumber, slot uint16) (bool, error) {
	s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
	if err != nil {
		return false, err
	}
	s.RLock()
	tup, gerr := storage.PageGetHeapTuple(s.Page(), slot)
	s.RUnlock()
	ctx.Pool.Unpin(s)
	if gerr != nil {
		return false, nil // page read error → treat as not visible
	}
	return mvcc.TupleVisible(tup.Header, ctx.Snap, ctx.Tx.XID, ctx.MultiXact), nil
}

// epqFollowHOT follows the HOT chain from (rel, blk, slot) to find the
// latest visible version after a concurrent UPDATE committed (M0100-0004).
// Re-evaluates pred (WHERE) against the latest tuple; returns the slot,
// decoded row, and true if a matching version was found. Returns (0, nil,
// false) when the chain terminates, the tuple is dead, or WHERE fails.
//
// Only valid for HOT updates (same-page chain). For cross-page (non-HOT)
// updates the old tuple has no HeapHotUpdated and followHOTChain returns
// not-found — the row is skipped (v0 compromise).
// epqFollowHOT follows the HOT chain from (rel, blk, slot) to find the latest
// visible version of the row, evaluates pred against it, and returns
// (newSlot, row, hotFound, predOk). hotFound is true when the HOT chain was
// traversed regardless of pred result; predOk is true when pred passed.
// Callers must NOT fall through to epqFollowChain when hotFound=true, because
// the HOT chain and CTID chain share the same tail tuple — a second evaluation
// would emit duplicate side-effecting NOTICE calls.
// origSnap, if non-nil, is used temporarily for predicate evaluation —
// matching PostgreSQL's EvalPlanQual semantics where the chain-follow uses a
// refreshed snapshot but sub-plan quals run against the original BEGIN-time
// snapshot (so correlated subqueries see pre-commit values).
func epqFollowHOT(ctx *Context, rel storage.RelFileNode, blk storage.BlockNumber,
	slot uint16, cols []catalog.Column, pred planner.Expr, origSnap *mvcc.Snapshot) (uint16, Row, bool, bool) {
	s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
	if err != nil {
		return 0, nil, false, false
	}
	s.RLock()
	latestTup, latestSlot, found := followHOTChain(s.Page(), slot, ctx.Snap, ctx.Tx.XID, ctx.MultiXact)
	s.RUnlock()
	ctx.Pool.Unpin(s)
	if !found {
		return 0, nil, false, false // no HOT chain — caller may try epqFollowChain
	}
	latestRow, decErr := DecodeHeapTupleRow(cols, latestTup, nil)
	if decErr != nil {
		return 0, nil, true, false // HOT chain found but decode failed
	}
	if pred != nil {
		var pv Datum
		var perr error
		// Wrap latestRow in a MaterializedSlot with the successor CTID so that
		// CTIDExpr predicates (e.g. WHERE ctid = '(0,3)') evaluate against the
		// NEW tuple's TID rather than the original. Without this, CTID conditions
		// in UPDATE WHERE always return NULL after EPQ chain-follow (M0100-0010).
		ms := &MaterializedSlot{
			row:       latestRow,
			hasCTID:   true,
			ctidBlock: uint32(blk),
			ctidOff:   latestSlot,
		}
		if origSnap != nil {
			// Temporarily restore the original snapshot so correlated
			// sub-plans (EXISTS, scalar subqueries) evaluate against
			// pre-commit data, matching PG's EvalPlanQual behaviour.
			savedSnap := ctx.Snap
			ctx.Snap = *origSnap
			pv, perr = evalExprSlot(pred, ms, ctx)
			ctx.Snap = savedSnap
		} else {
			pv, perr = evalExprSlot(pred, ms, ctx)
		}
		if perr != nil || pv.IsNull() || pv.Kind != KindBool || !pv.BoolValue() {
			return 0, nil, true, false // HOT chain found but pred failed
		}
	}
	return latestSlot, latestRow, true, true
}

// epqSlotMovedToAnotherPartition reports whether the tuple at
// (rel, blk, slot) carries the upstream "moved to another partition"
// sentinel in its t_ctid (block=Invalid, offset=MovedPartitionsOffsetNumber).
// EPQ retries (UPDATE/DELETE/LockRows) consult this after detecting that
// the original tuple's xmax committed and `epqFollowHOT` returned
// not-found — if the sentinel is set, the row was UPDATEd into a different
// partition relation, and the caller must raise the upstream
// `tuple to be locked was already moved to another partition due to
// concurrent update` error instead of silently skipping the row.
// Returns false on any read error (caller falls back to skip).
func epqSlotMovedToAnotherPartition(ctx *Context, rel storage.RelFileNode,
	blk storage.BlockNumber, slot uint16) bool {
	s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
	if err != nil {
		return false
	}
	s.RLock()
	tup, gerr := storage.PageGetHeapTuple(s.Page(), slot)
	s.RUnlock()
	ctx.Pool.Unpin(s)
	if gerr != nil {
		return false
	}
	return storage.IsMovedToAnotherPartition(tup.Header.CTID)
}

// epqChainCheckMovedPartition walks the UPDATE chain starting at (rel, blk,
// slot) via t_ctid and reports whether any tuple in the chain carries the
// moved-to-another-partition sentinel.  Unlike `epqSlotMovedToAnotherPartition`
// (single-slot check), this follows the chain across pages — required when a
// row was updated multiple times in the same xact and only the LAST in-xact
// version's t_ctid was stamped with the sentinel.  Example: s1 does UPDATE
// SET b='X' WHERE a=7 (stamps xmax + ctid→new on the original), THEN UPDATE
// SET a=11 WHERE a=7 (stamps xmax + ctid=MovedPartitions on the second
// version).  A caller that recorded the original slot must follow the chain
// to discover the sentinel.
//
// Termination: stops at a self-CTID (latest version), an invalid offset, the
// sentinel itself, or after maxChain steps (defensive).
func epqChainCheckMovedPartition(ctx *Context, rel storage.RelFileNode,
	blk storage.BlockNumber, slot uint16) bool {
	// Strategy 1 (fast path): walk the t_ctid chain.  PG always updates the
	// old tuple's t_ctid to point to the new version on UPDATE; HOT
	// in-partition updates in goopg do too (PageStampHotOldTuple).
	const maxChain = 64
	curBlk, curSlot := blk, slot
	for i := 0; i < maxChain; i++ {
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: curBlk})
		if err != nil {
			break
		}
		s.RLock()
		tup, gerr := storage.PageGetHeapTuple(s.Page(), curSlot)
		s.RUnlock()
		ctx.Pool.Unpin(s)
		if gerr != nil {
			break
		}
		if storage.IsMovedToAnotherPartition(tup.Header.CTID) {
			return true
		}
		ctid := tup.Header.CTID
		if ctid.Block == curBlk && ctid.Offset == curSlot {
			break // latest version
		}
		if ctid.Offset == 0 {
			break
		}
		curBlk = ctid.Block
		curSlot = ctid.Offset
	}
	// Strategy 2 (fallback): non-HOT UPDATEs in goopg currently do NOT
	// update the old tuple's t_ctid to point to the new version
	// (PageSetHeapTupleXmax stamps xmax only).  If the chain walk above
	// terminated without crossing into a newer version, scan the relation
	// for a sentinel-stamped tuple stamped by the same xact as the original
	// slot's xmax — this is the tuple that was the SOURCE of a cross-
	// partition UPDATE within the same xact chain.
	srcXmax := storage.InvalidTransactionID
	{
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err == nil {
			s.RLock()
			tup, gerr := storage.PageGetHeapTuple(s.Page(), slot)
			s.RUnlock()
			ctx.Pool.Unpin(s)
			if gerr == nil {
				srcXmax = tup.Header.Xmax
			}
		}
	}
	if srcXmax == storage.InvalidTransactionID {
		return false
	}
	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return false
	}
	for b := storage.BlockNumber(0); b < nBlocks; b++ {
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: b})
		if err != nil {
			continue
		}
		s.RLock()
		page := s.Page()
		if storage.IsNew(page) {
			s.RUnlock()
			ctx.Pool.Unpin(s)
			continue
		}
		count, cerr := storage.PageLinePointerCount(page)
		if cerr != nil {
			s.RUnlock()
			ctx.Pool.Unpin(s)
			continue
		}
		for slotIdx := uint16(1); slotIdx <= uint16(count); slotIdx++ {
			tup, terr := storage.PageGetHeapTuple(page, slotIdx)
			if terr != nil {
				continue
			}
			if tup.Header.Xmax != srcXmax {
				continue
			}
			if storage.IsMovedToAnotherPartition(tup.Header.CTID) {
				s.RUnlock()
				ctx.Pool.Unpin(s)
				return true
			}
		}
		s.RUnlock()
		ctx.Pool.Unpin(s)
	}
	return false
}

// epqFollowChain walks the cross-page t_ctid chain starting at (rel, blk,
// slot) to find the latest tuple version that is visible to ctx.Snap and
// matches pred. Used as a fallback when epqFollowHOT (HOT same-page chain)
// returns not-found because the concurrent UPDATE was non-HOT and therefore
// links via raw t_ctid rather than the HeapHotUpdated bit (M0100-0005z).
//
// origSnap, if non-nil, is used temporarily for predicate evaluation —
// matching PostgreSQL's EvalPlanQual semantics where correlated sub-plans
// (EXISTS, scalar subqueries) run against the original BEGIN-time snapshot.
//
// Returns (newBlk, newSlot, row, true) when a matching version is found,
// or (0, 0, nil, false) when the chain terminates (sentinel, self-CTID,
// invalid offset, or chain depth exceeded) without finding a visible match.
//
// Predicate is evaluated only at the chain tail (the latest version); upstream
// EPQ semantics: if the latest visible version still matches WHERE, the
// updater proceeds against it; otherwise the row is skipped.
func epqFollowChain(ctx *Context, rel storage.RelFileNode, blk storage.BlockNumber,
	slot uint16, cols []catalog.Column, pred planner.Expr, origSnap *mvcc.Snapshot) (storage.BlockNumber, uint16, Row, bool) {
	_, found, movedPart := epqFollowChainFull(ctx, rel, blk, slot, cols, pred, origSnap)
	if movedPart {
		return 0, 0, nil, false
	}
	return found.blk, found.slot, found.row, found.ok
}

type epqChainResult struct {
	blk  storage.BlockNumber
	slot uint16
	row  Row
	ok   bool
}

// isChainTailCTID reports whether ctid is a t_ctid chain-tail sentinel — i.e.
// there is no live successor version to follow from (curBlk, curSlot). True
// when ctid:
//   - has an invalid block number (no successor was ever stamped), or
//   - has a zero offset (goopg's initial CTID is {InvalidBlockNumber,0}; a
//     DELETE leaves it untouched, so a deleted-but-never-updated row reports
//     a tail here), or
//   - points at (curBlk, curSlot) itself (the latest version self-CTID).
//
// Shared by epqFollowChainFull (EPQ chain walk) and lockRowsOp.stampLockInner
// (FOR UPDATE/SHARE chain-follow after a committed updater) so both sibling
// paths terminate identically; following a sentinel CTID would Pin a
// non-existent block and surface storage.ErrShortRead.
func isChainTailCTID(ctid storage.ItemPointer, curBlk storage.BlockNumber, curSlot uint16) bool {
	return ctid.Block == storage.InvalidBlockNumber || ctid.Offset == 0 ||
		(ctid.Block == curBlk && ctid.Offset == curSlot)
}

// epqFollowChainFull walks the t_ctid chain and returns the live successor row.
// movedPart is true if the chain ended because of a moved-partition sentinel.
// origSnap, if non-nil, is used temporarily for predicate evaluation so that
// correlated sub-plans run against the original snapshot (PG EvalPlanQual semantics).
func epqFollowChainFull(ctx *Context, rel storage.RelFileNode, blk storage.BlockNumber,
	slot uint16, cols []catalog.Column, pred planner.Expr, origSnap *mvcc.Snapshot) (relNode storage.RelFileNode, found epqChainResult, movedPart bool) {
	const maxChain = 64
	curBlk, curSlot := blk, slot
	for i := 0; i < maxChain; i++ {
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: curBlk})
		if err != nil {
			return rel, epqChainResult{}, false
		}
		s.RLock()
		tup, gerr := storage.PageGetHeapTuple(s.Page(), curSlot)
		s.RUnlock()
		ctx.Pool.Unpin(s)
		if gerr != nil {
			return rel, epqChainResult{}, false
		}
		// Sentinel: row was moved to another partition.
		// Report this to the caller so it can raise the appropriate error.
		if storage.IsMovedToAnotherPartition(tup.Header.CTID) {
			return rel, epqChainResult{}, true
		}
		ctid := tup.Header.CTID
		// Chain terminates when CTID is invalid (no successor stamped) or
		// points at self (latest version sentinel). At the tail, evaluate
		// visibility + predicate against this tuple.
		atTail := isChainTailCTID(ctid, curBlk, curSlot)
		if atTail {
			if !mvcc.TupleVisible(tup.Header, ctx.Snap, ctx.Tx.XID, ctx.MultiXact) {
				return rel, epqChainResult{}, false
			}
			row, decErr := DecodeHeapTupleRow(cols, tup, nil)
			if decErr != nil {
				return rel, epqChainResult{}, false
			}
			if pred != nil {
				var pv Datum
				var perr error
				if origSnap != nil {
					// Temporarily restore the original snapshot so correlated
					// sub-plans (EXISTS, scalar subqueries) evaluate against
					// pre-commit data, matching PG's EvalPlanQual behaviour.
					savedSnap := ctx.Snap
					ctx.Snap = *origSnap
					pv, perr = evalExpr(pred, row, ctx)
					ctx.Snap = savedSnap
				} else {
					pv, perr = evalExpr(pred, row, ctx)
				}
				if perr != nil || pv.IsNull() || pv.Kind != KindBool || !pv.BoolValue() {
					return rel, epqChainResult{}, false
				}
			}
			return rel, epqChainResult{blk: curBlk, slot: curSlot, row: row, ok: true}, false
		}
		// Follow the link to the next version.
		curBlk = ctid.Block
		curSlot = ctid.Offset
	}
	return rel, epqChainResult{}, false
}

// stampOldCtid updates the t_ctid field of the tuple at (rel, blk, slot)
// to point at newPtr. Called after a non-HOT cross-page UPDATE writes the
// new tuple version, so that EPQ chain followers (epqFollowChain) can
// locate the latest version. Visibility (xmin/xmax) is untouched.
//
// Errors that would corrupt the page are returned to the caller; transient
// "slot already overwritten" cases (ErrUnsupportedItem) are swallowed since
// the chain link is best-effort.
func stampOldCtid(ctx *Context, rel storage.RelFileNode, blk storage.BlockNumber,
	slot uint16, newPtr storage.ItemPointer) error {
	s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
	if err != nil {
		return err
	}
	s.Lock()
	cerr := storage.PageSetHeapTupleCtid(s.Page(), slot, newPtr)
	s.Unlock()
	ctx.Pool.Unpin(s)
	if cerr != nil {
		if errors.Is(cerr, storage.ErrUnsupportedItem) || errors.Is(cerr, storage.ErrInvalidSlot) {
			return nil
		}
		return cerr
	}
	return nil
}

// errMovedToAnotherPartition is the canonical PG error raised when EPQ
// rechecks (UPDATE/DELETE/SELECT FOR UPDATE/triggers/lockRows) walk to a
// tuple that has been cross-partition UPDATEd by a concurrent committed
// transaction. SQLSTATE 0A000 matches upstream `errcode_for_partition`.
func errMovedToAnotherPartition(pos int) *ExecError {
	return &ExecError{
		Code:    "0A000",
		Pos:     pos,
		Message: "tuple to be locked was already moved to another partition due to concurrent update",
	}
}

// paddedMutex is a 64-byte cache-line-padded mutex. Lined up in an array,
// adjacent stripes occupy distinct cache lines so contending writers do
// not pay coherence traffic on a stripe they did not intend to lock.
// M0107-0007a (Phase D4 — heap-extend-lock striping).
type paddedMutex struct {
	mu sync.Mutex
	_  [56]byte // pad sync.Mutex (8 B) to 64 B (one cache line)
}

// heapExtendLockStripes is the number of stripes per relation extend
// lock set. Matches PG's `NUM_XLOGINSERT_LOCKS = 8` and the design's
// extend-lock plan in `docs/design/perf-optimize/07-wal-fsm-insert.md`.
const heapExtendLockStripes = 8

// heapExtendLockSet is a per-relation set of 8 stripe mutexes. A backend
// extending `rel` picks `locks[procNum & 0x7]`, allowing up to 8 parallel
// extenders per relation. PinNew already serialises on the bufpool's
// `pinMu` for victim claim + bufmap publish, and storage.Manager.Extend
// hands out distinct block numbers per call — so concurrent extension
// from different stripes is safe; the prior single-mutex existed only to
// avoid wasted PinNew churn, not for correctness.
type heapExtendLockSet struct {
	locks [heapExtendLockStripes]paddedMutex
}

var heapExtendLocks sync.Map // map[storage.RelFileNode]*heapExtendLockSet

// lockHeapExtend acquires one of the relation's 8 extend-lock stripes,
// chosen by `procNum & 0x7`. Returns the unlock closure. Pass
// `ctx.ProcNum` from `executor.Context` at call sites; non-backend
// callers (tests, background workers) may pass 0.
// lockHeapExtend acquires the per-relation heap-extension lock for the
// stripe selected by procNum (M0107-0007 slice A — 8-way striped). It
// returns the unlock callback and a contention flag: contended == true
// means the lock was already held by another stripe-mate when we
// arrived (TryLock failed), which the caller uses as the proxy for
// "extend by extendBatchSize and FSM-register the extras"; contended ==
// false means the uncontended fast path can keep extending one page at
// a time. The PG counterpart is the
// `RelationExtensionLockWaiterCount`-driven `extraBlocks` heuristic in
// `RelationGetBufferForTuple` — see
// `docs/design/perf-optimize/07-wal-fsm-insert.md` §3.
func lockHeapExtend(rel storage.RelFileNode, procNum int32) (release func(), contended bool) {
	v, _ := heapExtendLocks.LoadOrStore(rel, &heapExtendLockSet{})
	set := v.(*heapExtendLockSet)
	mu := &set.locks[uint32(procNum)&(heapExtendLockStripes-1)].mu
	if mu.TryLock() {
		return mu.Unlock, false
	}
	mu.Lock()
	return mu.Unlock, true
}

// seqScanOp walks every block of a heap relation, yielding visible
// tuples decoded into the planner's column ordering. Visibility is
// checked against ctx.Snap; tuples whose xmin/xmax are outside the
// snapshot's horizon are skipped.
type seqScanOp struct {
	// schema/tbl/pos/rel are extracted from *planner.SeqScan at construction
	// time (Phase C.3 migration: seqScanOp no longer holds a GC-traced
	// *planner.SeqScan pointer; the planner struct can be freed after Open).
	schema planner.Schema
	tbl    *catalog.Table
	pos    int
	rel    storage.RelFileNode // cached once in Open; avoids catalog lock per Next call

	ctx  *Context
	cols []catalog.Column

	nBlocks  storage.BlockNumber
	curBlock storage.BlockNumber
	curSlot  uint16
	slotMax  int
	pinned   *storage.Slot

	// activePage holds the current page bytes regardless of source
	// (pool slot or ring buffer). Set alongside pinned (for pool) or
	// independently (for ring). Readers use this instead of
	// o.pinned.Page() so ring-buffered pages work transparently.
	activePage storage.Page

	// ring is the SeqScan strategy ring (M0048-0002). When non-nil,
	// cache misses are served from private ring buffers instead of
	// evicting pool pages.  Activated when nBlocks > pool.Capacity()/4.
	ring *storage.ScanRing

	// prefetchedThru is the highest block (exclusive) we've
	// already issued a Pool.Prefetch hint for. SeqScan walks
	// blocks strictly forward, so the prefetcher just needs to
	// keep `seqScanLookahead` blocks ahead of curBlock.
	prefetchedThru storage.BlockNumber

	// scanRow is the per-Next() decode buffer (M0054-0005a). The
	// pre-fix path called `DecodeRow` on every visible tuple,
	// allocating a fresh `Row` slice each time. We now allocate
	// `scanRow` once on first use and decode in place via
	// `DecodeRowInto`, returning a defensive `cloneRow` so callers
	// that retain the row across `Next()` calls (sortOp, hash-join
	// build, etc.) keep their own copy. This drops the
	// per-row leaf-allocation cost the M0054-0004 pprof survey
	// flagged as `runtime.findObject` flat 29.30 % under Q9.
	scanRow Row

	// sctx is the per-page mctx backing varchar / char / text /
	// bytea Datums emitted by DecodeRowIntoMctx. Reset() at
	// the per-block boundary frees all variable-length payload
	// allocated for the previous page's tuples; consumers that
	// retain rows past the boundary must call slot.Materialize()
	// to deep-copy. (M0073-0004; M0107-0001: arena→sctx.)
	sctx *mctx.Context

	// M0092-0007: embedded slot reused across every Next() call.
	// The returned `&o.slot` pointer is stable across calls; its
	// `row` field is overwritten per emission. Caller must
	// consume / Materialize before the next Next() invocation.
	slot MaterializedSlot

	// enumTypes[i] is non-nil when cols[i] is a user-defined enum type.
	// Used to convert KindString heap datums to KindEnum for correct ORDER BY. M0097-enum.
	enumTypes []*catalog.EnumType
}

// seqScanLookahead is the number of blocks ahead of the current
// scan position seqScanOp keeps prefetched. Mirrors upstream's
// `effective_io_concurrency` default scope and is enough to
// pipeline a single sequential scan against typical SSD
// latencies. A future loop turns this into a tunable GUC.
const seqScanLookahead storage.BlockNumber = 4

func newSeqScanOp(p *planner.SeqScan) *seqScanOp {
	return &seqScanOp{
		schema: p.Output(),
		tbl:    p.Table,
		pos:    p.Pos(),
		cols:   p.Table.Columns,
	}
}

func (o *seqScanOp) Schema() planner.Schema { return o.schema }

func (o *seqScanOp) Open(ctx *Context) error {
	if ctx.Pool == nil || ctx.Catalog == nil {
		return &ExecError{Code: "XX000", Pos: o.pos, Message: "SeqScan requires storage handles in Context"}
	}
	// Reject scans of unpopulated materialized views (WITH NO DATA / before REFRESH). M0097-0025.
	if o.tbl != nil && o.tbl.IsMatView && !o.tbl.IsPopulated {
		return &ExecError{Code: "55000", Pos: o.pos,
			Message: fmt.Sprintf("materialized view %q has not been populated", o.tbl.Name),
			Hint:    "Use the REFRESH MATERIALIZED VIEW command."}
	}
	o.ctx = ctx
	// Pre-compute which columns are enum types so Next() can inject KindEnum datums
	// for correct ORDER BY semantics (M0097-enum).
	if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
		o.enumTypes = make([]*catalog.EnumType, len(o.cols))
		for i, col := range o.cols {
			if et, isEnum := im.LookupEnum(col.Type.Name); isEnum {
				o.enumTypes[i] = et
			}
		}
	}
	// Cache rel once — avoids the catalog RLock on every Next() call.
	o.rel = ctx.Catalog.RelFileNode(o.tbl)
	// M0118-0001: a SERIALIZABLE seq scan takes a relation-level SIREAD
	// predicate lock so a concurrent writer's INSERT of a matching row forms
	// the rw-conflict (phantom). Mirrors PredicateLockRelation in upstream
	// heap_beginscan; temp / matview relations are excluded exactly as
	// PredicateLockingNeededForRelation does (system catalogs gated in the hook).
	if o.tbl == nil || (!o.tbl.Temp && !o.tbl.IsMatView) {
		ssiRecordRelationRead(ctx, o.rel)
	}
	if err := ctx.acquireRelLock(o.rel, lockmgr.AccessShareLock); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.pos
		}
		return err
	}
	n, err := ctx.Pool.NBlocks(o.rel)
	if err != nil {
		return err
	}
	o.nBlocks = n
	o.curBlock = 0
	o.curSlot = 0
	o.slotMax = 0
	o.prefetchedThru = 0
	// M0073-0004 / M0107-0001: per-operator mctx for varchar / char /
	// text / bytea payload. Reset on block-advance; Release on Close.
	o.sctx = mctx.Acquire(ctx.Mctx, mctx.KindExpr)
	// Activate the ring strategy when the relation is large enough that a
	// full sequential scan would evict most hot pages from the shared pool.
	// Threshold: pool capacity / 4, matching upstream's heuristic.
	if ctx.Pool != nil && int(n) > ctx.Pool.Capacity()/4 {
		o.ring = storage.NewScanRing(ctx.Pool, o.rel)
	}
	o.refillPrefetchWindow(o.rel)
	return nil
}

// refillPrefetchWindow keeps `seqScanLookahead` blocks ahead of
// curBlock prefetched via Pool.Prefetch. With prefetching
// disabled (no AIO engine attached) Pool.Prefetch is a no-op,
// so this loop is cheap.
func (o *seqScanOp) refillPrefetchWindow(rel storage.RelFileNode) {
	target := o.curBlock + seqScanLookahead
	if target > o.nBlocks {
		target = o.nBlocks
	}
	for o.prefetchedThru < target {
		o.ctx.Pool.Prefetch(storage.BufferTag{Rel: rel, Block: o.prefetchedThru})
		o.prefetchedThru++
	}
}

func (o *seqScanOp) Close() error {
	if o.ring != nil {
		o.ring.Close()
		o.ring = nil
		o.activePage = nil
	} else if o.pinned != nil {
		// M0100-0005e: page RLock is now scoped per tuple inside
		// Next() (acquired before PageGetHeapTuple, released before
		// yielding the slot). At Close-time we hold only the pin
		// — drop it. A double-RUnlock here surfaces as
		// `sync: RUnlock of unlocked RWMutex` for any consumer
		// (e.g. Limit, top-N Sort, or any client that closes
		// before exhausting the scan). M0100-0005y caught this
		// via `SELECT tableoid::regclass FROM t LIMIT 1`.
		o.ctx.Pool.Unpin(o.pinned)
		o.pinned = nil
		o.activePage = nil
	}
	if o.scanRow != nil {
		releaseRow(o.scanRow)
		o.scanRow = nil
	}
	if o.sctx != nil {
		o.sctx.Release()
		o.sctx = nil
	}
	return nil
}

// nextVisible advances through (block, slot) pairs and returns the
// next tuple visible to the snapshot, or EOF.
func (o *seqScanOp) Next() (TupleSlot, error) {
	rel := o.rel
	for {
		if o.pinned == nil && o.activePage == nil {
			if o.curBlock >= o.nBlocks {
				return nil, EOF
			}
			// Poll for query cancellation at each new block boundary.
			if o.ctx.Ctx != nil {
				if err := o.ctx.Ctx.Err(); err != nil {
					return nil, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
				}
			}
			if o.ring != nil {
				// Ring strategy: cache hit → pool slot (with RLock);
				// cache miss → private ring buffer (no pool eviction).
				page, err := o.ring.AcquirePage(o.curBlock)
				if err != nil {
					return nil, err
				}
				o.activePage = page
			} else {
				slot, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: o.curBlock})
				if err != nil {
					return nil, err
				}
				// Pin only — the page RLock is now scoped per tuple
				// decode (see inner loop). Holding the RLock across
				// full-page iteration would block concurrent writers
				// for the duration of any blocking parent (e.g.
				// SELECT FOR KEY SHARE whose WHERE clause calls
				// pg_advisory_lock()), creating cross-session
				// deadlocks. M0100-0005 lock-committed-update fix:
				// release the RLock immediately after each tuple is
				// materialised so writers on the same page can
				// proceed while the parent operator processes the
				// yielded slot. Page eviction is still prevented by
				// the pin alone.
				o.pinned = slot
				o.activePage = slot.Page()
			}
			page := o.activePage
			if storage.IsNew(page) {
				o.releasePinned()
				o.curBlock++
				continue
			}
			// Brief RLock around the line-pointer count read; the
			// count is captured into o.slotMax so the per-tuple loop
			// can iterate without holding the RLock between yields.
			if o.pinned != nil {
				o.pinned.RLock()
			}
			count, err := storage.PageLinePointerCount(page)
			if o.pinned != nil {
				o.pinned.RUnlock()
			}
			if err != nil {
				o.releasePinned()
				return nil, err
			}
			o.slotMax = count
			o.curSlot = 1
		}
		for int(o.curSlot) <= o.slotMax {
			page := o.activePage
			// Brief RLock around tuple decode + arena copy. After
			// release, parent operators (filterOp / lockRowsOp /
			// projectOp) can run user-defined predicates that block
			// on advisory or row-level locks without pinning the
			// page in shared mode — a critical correctness property
			// for SELECT … FOR KEY SHARE on a row whose UPDATE is
			// in flight on the same page.
			if o.pinned != nil {
				o.pinned.RLock()
			}
			tuple, err := storage.PageGetHeapTuple(page, o.curSlot)
			o.curSlot++
			if err != nil {
				if o.pinned != nil {
					o.pinned.RUnlock()
				}
				// Corrupt or unsupported tuples are silently
				// skipped — scanning should not fail on
				// partial page writes or WAL-replay debris.
				continue
			}
			if !mvcc.TupleVisibleSubxact(tuple.Header, o.ctx.Snap, o.ctx.Tx.XID, o.ctx.TxnMgr, o.ctx.MultiXact) {
				if o.pinned != nil {
					o.pinned.RUnlock()
				}
				// M0118-0001: SSI phantom conflict-out. The tuple is physically
				// present but invisible to us because a concurrent transaction
				// inserted it; register the reader→inserter rw-edge so an
				// INSERT-before-READ ordering still forms the dangerous structure
				// (heapam.c HeapCheckForSerializableConflictOut, !visible path).
				if err := ssiRecordInvisibleTupleRead(o.ctx, rel, tuple.Header.Xmin); err != nil {
					return nil, err
				}
				continue
			}
			// M0115-0004: lazily cache HeapXminCommitted in the on-page infomask
			// after visibility is confirmed. Only cache when xmin was confirmed
			// via the snapshot — NOT when visibility is due to the self-visible
			// path (xmin == our own XID or sub-xact ancestor), because that
			// tuple may still be rolled back via ROLLBACK TO SAVEPOINT.
			needsXminHintBit := o.pinned != nil &&
				tuple.Header.Infomask&storage.HeapXminCommitted == 0 &&
				tuple.Header.Infomask&storage.HeapXminInvalid == 0 &&
				!mvcc.IsSelfXID(tuple.Header.Xmin, o.ctx.Tx.XID, o.ctx.TxnMgr)
			// CTE snapshot isolation: skip rows written by DML CTEs so the
			// outer SELECT sees the pre-CTE state (PostgreSQL semantics).
			if o.ctx.CTEWriteFence != nil {
				if _, inFence := o.ctx.CTEWriteFence[storage.ItemPointer{Block: o.curBlock, Offset: o.curSlot - 1}]; inFence {
					if o.pinned != nil {
						o.pinned.RUnlock()
					}
					continue
				}
			}
			// M0104-0007: SSI read-path hook. Tuple is visible to this
			// reader — install a tuple-grain SIREAD predicate lock and an
			// rw-conflict edge to the producing writer (xmin). Helper
			// short-circuits to a single inline check for RC/RR readers.
			// curSlot was already advanced past the just-fetched slot.
			// M0118-0001: a non-nil error means this read closed a dangerous
			// structure to an already-committed writer and the reader must
			// abort mid-statement (40001). Release the per-tuple page RLock
			// before returning; Close()/the pin handles buffer release.
			if err := ssiRecordTupleRead(o.ctx, rel, o.curBlock, o.curSlot-1, tuple.Header.Xmin, tuple.Header.Xmax); err != nil {
				if o.pinned != nil {
					o.pinned.RUnlock()
				}
				return nil, err
			}
			// M0054-0005a: decode into the reusable o.scanRow
			// buffer. M0073-0004: route varchar / char / text /
			// bytea payload through the per-page arena so per-
			// tuple `make([]byte)` allocs are amortised across
			// the page (one alloc per ~64 KiB). Reset is bound
			// to the curBlock++ boundary below; consumers that
			// retain rows past the boundary call slot.Materialize
			// which deep-copies arena bytes via cloneRowOwned.
			if o.scanRow == nil || len(o.scanRow) != len(o.cols) {
				o.scanRow = acquireRow(len(o.cols))
			}
			// Use bitmap + natts to correctly decode rows that have NULL
			// columns or were stored before ALTER TABLE ADD COLUMN expanded
			// the schema. HeapNattsMask = 0x07FF; storedNatts==0 means natts
			// was not explicitly set (legacy goopg rows without PG format).
			storedNatts := int(tuple.Header.Infomask2 & 0x07FF)
			if err := DecodeRowIntoMctxPGTuple(o.scanRow, o.cols, tuple.Data, tuple.Bitmap, storedNatts, o.sctx); err != nil {
				if o.pinned != nil {
					o.pinned.RUnlock()
				}
				continue
			}
			row := o.scanRow
			// Detoast any out-of-line column values (M0046-0006).
			// DetoastRow may return a fresh row when it allocates
			// large detoasted strings; either way the result is
			// safe to clone.
			if needsDetoast(row) {
				detoasted, err := DetoastRow(o.ctx, rel, o.cols, row)
				if err != nil {
					if o.pinned != nil {
						o.pinned.RUnlock()
					}
					continue // skip undetoastable tuple
				}
				row = detoasted
			}
			// M0100-0005 lock-committed-update fix: materialize the
			// row (deep-copy arena-backed Datums into owned bytes)
			// BEFORE releasing the page RLock. The yielded slot
			// must be safe to read after the page becomes writable
			// to other sessions; a concurrent UPDATE could otherwise
			// tear the bytes the parent is decoding.
			row = cloneRowOwned(row)
			// Inject KindEnum datums for enum-typed columns (M0097-enum).
			if len(o.enumTypes) > 0 {
				for i, et := range o.enumTypes {
					if et == nil || i >= len(row) {
						continue
					}
					d := row[i]
					if d.Kind != KindString && d.Kind != KindBytes {
						continue
					}
					label := d.StringValue()
					for _, ev := range et.Values {
						if ev.Label == label {
							row[i] = NewEnumDatum(ev.SortOrder, label)
							break
						}
					}
				}
			}
			if o.pinned != nil {
				o.pinned.RUnlock()
			}
			// M0115-0004: write HeapXminCommitted to the on-page infomask.
			// Acquire WLock briefly; o.pinned pin prevents eviction between
			// RUnlock and Lock. The write is not WAL-logged (re-derived on
			// recovery from pg_xact).
			if needsXminHintBit {
				o.pinned.Lock()
				storage.SetXminHintBit(o.activePage, o.curSlot-1, true)
				o.pinned.Unlock()
				o.ctx.Pool.MarkDirtyHintBit(o.pinned)
			}
			// M0092-0007: stack-aliased slot reused across
			// Next() calls; matches the M0092-0002 contract
			// (consumers materialize at retention boundaries).
			// scanRow is reused across the per-page tuple
			// loop; rows that need retention go through
			// slot.Materialize().
			o.slot.schema = o.schema
			o.slot.row = row
			// M0097-0038: inject current TID for CTIDExpr evaluation.
			o.slot.hasCTID = true
			o.slot.ctidBlock = uint32(o.curBlock)
			o.slot.ctidOff = uint16(o.curSlot - 1)
			return &o.slot, nil
		}
		o.releasePinned()
		o.curBlock++
		// M0073-0004: rewind the per-page byte arena. All slots
		// emitted from the just-finished page have either been
		// consumed by the parent or had their arena Datums
		// promoted to owned []byte via slot.Materialize() at the
		// retention boundary (sortOp.Open / windowOp.Open /
		// lockRowsOp.drainAndStamp / executor.Run; aggregateOp's
		// targeted MaterializeArena in evalGroupKey + applyAgg).
		// Reset rewinds chunk len to 0 but keeps capacity, so the
		// next page's decode reuses the same backing bytes — no
		// per-page allocation in steady state.
		if o.sctx != nil {
			o.sctx.Reset()
		}
		// As the scan walks forward, top up the prefetch window
		// so the next-but-one block is being read by the AIO
		// engine while we decode the current page.
		o.refillPrefetchWindow(rel)
	}
}

// currentTID returns the (rel, ItemPointer) of the most recently
// returned row, or ok=false when no row has been returned yet on
// this scan / page (or the scan has advanced past its last row).
// Used by lockRowsOp (M0021 tuple-level locking step 2) to stamp
// per-row lock-only xmax on the heap tuple after the scan
// surfaces it. Caller must invoke between Next-returns-row and
// the next Next call (the scan may release the page pin on the
// next Next, but the (block, slot) pair stays valid until then).
func (o *seqScanOp) currentTID() (storage.RelFileNode, storage.ItemPointer, bool) {
	if o.pinned == nil || o.curSlot == 0 {
		return storage.RelFileNode{}, storage.ItemPointer{}, false
	}
	return o.rel, storage.ItemPointer{Block: o.curBlock, Offset: o.curSlot - 1}, true
}

func (o *seqScanOp) releasePinned() {
	if o.ring != nil {
		o.ring.ReleasePage()
		o.activePage = nil
	} else if o.pinned != nil {
		// M0100-0005: page RLock is now scoped per tuple inside
		// Next() (acquired before PageGetHeapTuple, released
		// before yielding the slot). When releasePinned runs we
		// hold only the pin — drop it.
		o.ctx.Pool.Unpin(o.pinned)
		o.pinned = nil
		o.activePage = nil
	}
}

// insertOp consumes child rows (typically Values), encodes them with
// xmin = ctx.Tx.XID, and writes them through the buffer pool. Each
// successful insert bumps RowsAffected.
type insertOp struct {
	plan         *planner.Insert
	ctx          *Context
	child        Operator
	rowsAffected int64
	done         bool

	// retRows / retIdx: collected RETURNING rows; iterated via Next()
	// after all inserts are applied (M0100-0005).
	retRows []Row
	retIdx  int
}

// RowsAffected satisfies executor.RowCounter.
func (o *insertOp) RowsAffected() int64 { return o.rowsAffected }

func newInsertOp(p *planner.Insert, child Operator) *insertOp {
	return &insertOp{plan: p, child: child}
}

func (o *insertOp) Schema() planner.Schema {
	if len(o.plan.Returning) > 0 {
		return o.plan.ReturningSchema
	}
	return nil
}

// appendInsertRetRow evaluates the plan's RETURNING expressions against
// the just-inserted row (parent column order) and appends the result to
// o.retRows. No-op when RETURNING is absent. M0100-0005.
func (o *insertOp) appendInsertRetRow(row Row) {
	if len(o.plan.Returning) == 0 {
		return
	}
	retRow := make(Row, len(o.plan.Returning))
	for i, expr := range o.plan.Returning {
		v, _ := evalExpr(expr, row, o.ctx)
		retRow[i] = v
	}
	o.retRows = append(o.retRows, retRow)
}

func (o *insertOp) Open(ctx *Context) error {
	if ctx.Pool == nil || ctx.Catalog == nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "Insert requires storage handles in Context"}
	}
	o.ctx = ctx
	rel := ctx.Catalog.RelFileNode(o.plan.Table)
	if err := ctx.acquireRelLock(rel, lockmgr.RowExclusiveLock); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.plan.Pos()
		}
		return err
	}
	return o.child.Open(ctx)
}

func (o *insertOp) Close() error { return o.child.Close() }

// Next runs the insert as a one-shot side effect on first call. With
// RETURNING (M0100-0005) the inserted rows are accumulated in o.retRows
// and streamed out on subsequent calls. Without RETURNING the wire layer
// issues `INSERT N`.
func (o *insertOp) Next() (TupleSlot, error) {
	if o.done {
		if len(o.plan.Returning) > 0 && o.retIdx < len(o.retRows) {
			row := o.retRows[o.retIdx]
			o.retIdx++
			return SlotFromRow(o.plan.ReturningSchema, row), nil
		}
		return nil, EOF
	}
	o.done = true
	rel := o.ctx.Catalog.RelFileNode(o.plan.Table)
	cols := o.plan.Table.Columns
	isPartitioned := o.plan.Table.PartitionMethod != ""
	// BEFORE STATEMENT triggers fire once before any rows are processed.
	// Mark the table as in active DML first so the DDL-during-active-query
	// guard (execCreatePartitionChild) can detect concurrent DDL. M0097-0023.
	if len(o.plan.Table.Triggers) > 0 {
		if bsess, ok := o.ctx.Session.(*BasicSession); ok {
			bsess.MarkTableActive(o.plan.Table.OID)
			defer bsess.UnmarkTableActive(o.plan.Table.OID)
		}
		if err := fireStatementTriggers(o.ctx, o.plan.Table, "before", "insert"); err != nil {
			return nil, err
		}
	}
	// insertMissing[i]=true for every target column the source row does
	// not provide. Computed once per Open since ColumnIndex is immutable
	// across rows; applyDefaultsForMissing reads it to evaluate per-column
	// DEFAULT expressions (rung 14). Generated and SERIAL columns are
	// marked missing too, but applyDefaultsForMissing leaves them alone
	// (DefaultExpr is nil for those) so the existing computeGeneratedColumns
	// / nextval paths below stay authoritative.
	insertMissing := make([]bool, len(cols))
	for i := range insertMissing {
		insertMissing[i] = true
	}
	for _, tgtIdx := range o.plan.ColumnIndex {
		if tgtIdx >= 0 && tgtIdx < len(insertMissing) {
			insertMissing[tgtIdx] = false
		}
	}
	for {
		srcSlot, err := o.child.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		src := slotRow(srcSlot)
		// Reorder source row -> table column order via plan.ColumnIndex.
		row := make(Row, len(cols))
		for i := range cols {
			row[i] = NullDatum
		}
		for srcIdx, tgtIdx := range o.plan.ColumnIndex {
			row[tgtIdx] = src[srcIdx]
		}

		// M0103-0007 rung 14: fill DEFAULT expressions for any target column
		// the INSERT did not provide a value for. An explicit column list
		// like `INSERT INTO t (a, b) VALUES (...)` leaves columns not in the
		// list unmapped; PostgreSQL fills those with the column's DEFAULT
		// (or NULL when there is no DEFAULT) BEFORE SERIAL auto-generation
		// runs, mirroring upstream ExecInitStoredGenerated/ExecComputeStoredGenerated
		// ordering. The same helper rung 13 uses on the apply path applies
		// here: missing[i]=true for every column NOT in plan.ColumnIndex.
		applyDefaultsForMissing(cols, row, insertMissing)

		// Auto-generate values for SERIAL / BIGSERIAL / SMALLSERIAL columns
		// and GENERATED [ALWAYS|BY DEFAULT] AS IDENTITY columns.
		// M0097-0009: if a serial/identity column's slot is still NullDatum (not provided
		// in the INSERT), call nextval on the implicit sequence. An explicit NULL
		// (column in INSERT target list but value is NULL) must NOT trigger
		// auto-generation — it falls through to the NOT NULL check below.
		for i, col := range cols {
			// Skip: already has a value, or was explicitly supplied (even as NULL).
			if !row[i].IsNull() || !insertMissing[i] {
				continue
			}
			seqName := ""
			switch strings.ToLower(col.Type.Name) {
			case "serial", "serial4", "bigserial", "serial8", "smallserial", "serial2":
				seqName = strings.ToLower(o.plan.Table.Name) + "_" + strings.ToLower(col.Name) + "_seq"
				// If the standard tablename_colname_seq was renamed, look up by ownership.
				if LookupSequence(seqName) == nil {
					if owned := FindSequenceOwnedBy(strings.ToLower(o.plan.Table.Name) + "." + strings.ToLower(col.Name)); owned != "" {
						seqName = owned
					}
				}
			default:
				if col.IdentityColumn {
					seqName = strings.ToLower(o.plan.Table.Name) + "_" + strings.ToLower(col.Name) + "_seq"
				}
			}
			if seqName == "" {
				continue
			}
			seqArgs := []Datum{NewStringDatum(seqName)}
			v, err := evalNextval(seqArgs, o.ctx)
			if err == nil && !v.IsNull() {
				row[i] = v
			}
		}

		// Integer range enforcement: coerce explicitly-provided int values to
		// the column's declared type (catches smallint/int4 out-of-range and
		// bigint overflow from over-wide numeric literals).
		for i, col := range cols {
			if insertMissing[i] || row[i].IsNull() {
				continue
			}
			var coerced Datum
			var cerr error
			switch strings.ToLower(col.Type.Name) {
			case "int2", "smallint", "smallserial", "serial2":
				coerced, cerr = evalCast(row[i], "int2", o.plan.Pos())
			case "int4", "integer", "int", "serial", "serial4":
				coerced, cerr = evalCast(row[i], "int4", o.plan.Pos())
			case "int8", "bigint", "bigserial", "serial8":
				coerced, cerr = evalCast(row[i], "int8", o.plan.Pos())
			default:
				continue
			}
			if cerr != nil {
				return nil, cerr
			}
			row[i] = coerced
		}

		// BEFORE INSERT triggers (M0096-0012).
		if len(o.plan.Table.Triggers) > 0 {
			newRow, ok := fireTriggers(o.ctx, o.plan.Table, "before", "insert", nil, row)
			if !ok {
				continue // trigger returned NULL — skip this row
			}
			row = newRow
		}

		// NOT NULL constraint enforcement.
		// For partitioned tables, defer until after routing so the error names
		// the leaf partition (matches PG behavior).
		if !isPartitioned {
			for i, col := range cols {
				if col.NotNull && i < len(row) && row[i].IsNull() {
					return nil, &ExecError{
						Code:    "23502",
						Message: fmt.Sprintf("null value in column %q of relation %q violates not-null constraint", col.Name, o.plan.Table.Name),
						Detail:  formatRowForDetail(cols, row),
					}
				}
			}
		}

		// CHECK constraint enforcement (M0097-0014).
		if len(o.plan.Table.CheckConstraints) > 0 {
			if err := checkConstraints(o.ctx, o.plan.Table, row); err != nil {
				return nil, err
			}
		}

		// Partition routing (M0096-0007): if the target table is partitioned,
		// route the row to the appropriate partition child BEFORE the FK check
		// so that violation MESSAGEs name the leaf partition (matches upstream
		// PG; M0100-0005m).  Parent's FK definitions apply to routed inserts.
		targetRel := rel
		// Compute generated columns (GENERATED ALWAYS AS … STORED) before writing.
		// M0096-0008.
		_ = computeGeneratedColumns(cols, row)

		var routedPart *catalog.Table
		if isPartitioned {
			if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
				var routeErr error
				routedPart, routeErr = routeToPartition(o.plan.Table, row, im, o.ctx)
				if routeErr != nil {
					return nil, routeErr
				}
				if routedPart == nil {
					return nil, &ExecError{Code: "23514", Pos: o.plan.Pos(),
						Message: fmt.Sprintf("no partition of relation %q found for row", o.plan.Table.Name)}
				}
			}
		}

		// FK referential integrity check (M0096-0011): verify parent rows exist
		// before writing.  Uses the plan table's ForeignKeys (parent partition's
		// FKs apply to routed child inserts too).  reportTbl = routedPart so
		// the MESSAGE names the leaf partition when partition-routed.
		if len(o.plan.Table.ForeignKeys) > 0 {
			if err := checkFKInsert(o.ctx, o.plan.Table, routedPart, row); err != nil {
				return nil, err
			}
		}

		if isPartitioned && routedPart != nil {
			partTable := routedPart
			targetRel = o.ctx.Catalog.RelFileNode(partTable)
			// Remap row from parent column order to partition child column order.
			// Partition children may have columns in a different order (ATTACH
			// PARTITION allows mismatched column order). M0096-0013.
			partRow := remapRowForPartition(o.plan.Table.Columns, partTable.Columns, row)
			// NOT NULL check at the leaf partition level (PG names the child table).
			for i, col := range partTable.Columns {
				if col.NotNull && i < len(partRow) && partRow[i].IsNull() {
					return nil, &ExecError{
						Code:    "23502",
						Message: fmt.Sprintf("null value in column %q of relation %q violates not-null constraint", col.Name, partTable.Name),
						Detail:  formatRowForDetail(partTable.Columns, partRow),
					}
				}
			}
			// Recompute generated columns using partition child's schema.
			_ = computeGeneratedColumns(partTable.Columns, partRow)
			if uerr := checkUniqueIndexesForInsert(o.ctx, partTable, partTable.Columns, partRow, o.plan.Pos()); uerr != nil {
				return nil, uerr
			}
			if uerr := checkExclusionConstraintsForInsert(o.ctx, partTable, partTable.Columns, partRow, o.plan.Pos()); uerr != nil {
				return nil, uerr
			}
			ptr, werr := writeHeapRowReturning(o.ctx, targetRel, partTable.Columns, partRow)
			if werr != nil {
				return nil, werr
			}
			if ierr := emitCanonicalHeapInsert(o.ctx, targetRel, ptr); ierr != nil {
				return nil, ierr
			}
			// M0104-0007 / M0118-0001: SSI write-path hook on the newly
			// inserted tuple's (block, slot). Conflict-in installs an rw-edge
			// against any SERIALIZABLE reader that holds a covering predicate
			// lock (page or relation grain), and aborts this INSERT in place
			// (40001) when it closes a dangerous structure to a committed pivot.
			if serr := ssiRecordTupleWrite(o.ctx, targetRel, ptr.Block, ptr.Offset); serr != nil {
				return nil, serr
			}
			maintainUniqueIndexesForInsert(o.ctx, partTable, partTable.Columns, partRow, ptr)
			o.appendInsertRetRow(row)
			o.rowsAffected++
			continue
		}
		// No matching partition found (or non-partitioned) — write to parent.
		if uerr := checkUniqueIndexesForInsert(o.ctx, o.plan.Table, cols, row, o.plan.Pos()); uerr != nil {
			return nil, uerr
		}
		if uerr := checkExclusionConstraintsForInsert(o.ctx, o.plan.Table, cols, row, o.plan.Pos()); uerr != nil {
			return nil, uerr
		}
		ptr, werr := writeHeapRowReturning(o.ctx, targetRel, cols, row)
		if werr != nil {
			return nil, werr
		}
		if ierr := emitCanonicalHeapInsert(o.ctx, targetRel, ptr); ierr != nil {
			return nil, ierr
		}
		// M0104-0007 / M0118-0001: SSI write-path hook for the non-partitioned
		// insert path; aborts in place (40001) on a committed-pivot structure.
		if serr := ssiRecordTupleWrite(o.ctx, targetRel, ptr.Block, ptr.Offset); serr != nil {
			return nil, serr
		}
		maintainUniqueIndexesForInsert(o.ctx, o.plan.Table, cols, row, ptr)
		// AFTER INSERT triggers (M0097-0140).
		if len(o.plan.Table.Triggers) > 0 {
			fireTriggers(o.ctx, o.plan.Table, "after", "insert", nil, row)
		}
		o.appendInsertRetRow(row)
		o.rowsAffected++
	}
	// Yield the first RETURNING row inline (subsequent rows come from the
	// done-branch in Next()). Without RETURNING, return EOF as before so
	// the wire layer issues `INSERT N`.
	if len(o.plan.Returning) > 0 && o.retIdx < len(o.retRows) {
		row := o.retRows[o.retIdx]
		o.retIdx++
		return SlotFromRow(o.plan.ReturningSchema, row), nil
	}
	return nil, EOF
}

// formatRowForDetail formats a row for NOT NULL violation DETAIL messages.
// Produces: "Failing row contains (v1, v2, ...)."
func formatRowForDetail(cols []catalog.Column, row Row) string {
	parts := make([]string, len(cols))
	for i := range cols {
		if i >= len(row) || row[i].IsNull() {
			parts[i] = "null"
			continue
		}
		parts[i] = row[i].Format()
	}
	return "Failing row contains (" + strings.Join(parts, ", ") + ")."
}

// routeToPartition finds the partition child table that matches the given row
// remapRowForPartition reorders a row from the parent's column layout to the
// partition child's column layout. PostgreSQL's ATTACH PARTITION allows the
// child to have columns in a different order (as long as names and types match).
// We remap by matching column names. M0096-0013.
func remapRowForPartition(parentCols, childCols []catalog.Column, row Row) Row {
	if len(parentCols) == len(childCols) {
		same := true
		for i := range childCols {
			if childCols[i].Name != parentCols[i].Name {
				same = false
				break
			}
		}
		if same {
			return row // fast path: same ordering
		}
	}
	// Build name→value map from parent row.
	byName := make(map[string]Datum, len(parentCols))
	for i, c := range parentCols {
		if i < len(row) {
			byName[strings.ToLower(c.Name)] = row[i]
		}
	}
	out := make(Row, len(childCols))
	for i, c := range childCols {
		if v, ok := byName[strings.ToLower(c.Name)]; ok {
			out[i] = v
		} else {
			out[i] = NullDatum
		}
	}
	return out
}

// partitionColOrderMatches reports whether parentCols and childCols have the
// same column names in the same order (same layout, no remapping needed).
// M0097-0028.
func partitionColOrderMatches(parentCols, childCols []catalog.Column) bool {
	if len(parentCols) != len(childCols) {
		return false
	}
	for i := range parentCols {
		if !strings.EqualFold(parentCols[i].Name, childCols[i].Name) {
			return false
		}
	}
	return true
}

// based on the parent's partition key. Returns nil if no partition matches.
// M0096-0007.
func routeToPartition(parent *catalog.Table, row Row, im *catalog.InMemory, ctx *Context) (*catalog.Table, error) {
	return routeToPartitionDepth(parent, row, im, ctx, 0)
}

// evalPartitionKeyExpr evaluates a partition key expression (e.g. NOT a, abs(b),
// (a+b)/2) against the given row. Used when the partition key is an expression
// rather than a plain column reference. M0097-0023.
func evalPartitionKeyExpr(expr parser.Expr, cols []catalog.Column, row Row) (Datum, error) {
	switch x := expr.(type) {
	case *parser.ColumnRef:
		for i, col := range cols {
			if strings.EqualFold(col.Name, x.Column) {
				if i < len(row) {
					return row[i], nil
				}
				return NullDatum, nil
			}
		}
		return NullDatum, nil
	case *parser.UnaryOp:
		v, err := evalPartitionKeyExpr(x.Operand, cols, row)
		if err != nil {
			return NullDatum, err
		}
		if v.IsNull() {
			return NullDatum, nil
		}
		switch x.Op {
		case parser.OpNot:
			b := v.Kind == KindString && strings.EqualFold(v.StringValue(), "t") ||
				v.Kind == KindBool && v.Int != 0 ||
				v.Kind == KindInt && v.Int != 0
			return NewBoolDatum(!b), nil
		case parser.OpUnaryNeg:
			if v.Kind == KindInt {
				return NewIntDatum(-v.Int), nil
			}
		}
	case *parser.BinaryOp:
		l, err := evalPartitionKeyExpr(x.Left, cols, row)
		if err != nil {
			return NullDatum, err
		}
		r, err2 := evalPartitionKeyExpr(x.Right, cols, row)
		if err2 != nil {
			return NullDatum, err2
		}
		if l.IsNull() || r.IsNull() {
			return NullDatum, nil
		}
		lv := int64(0)
		rv := int64(0)
		if l.Kind == KindInt {
			lv = l.Int
		}
		if r.Kind == KindInt {
			rv = r.Int
		}
		switch x.Op {
		case parser.OpAdd:
			return NewIntDatum(lv + rv), nil
		case parser.OpSub:
			return NewIntDatum(lv - rv), nil
		case parser.OpMul:
			return NewIntDatum(lv * rv), nil
		case parser.OpDiv:
			if rv == 0 {
				return NullDatum, nil
			}
			return NewIntDatum(lv / rv), nil
		}
	case *parser.FuncCall:
		if len(x.Args) == 1 {
			arg, err := evalPartitionKeyExpr(x.Args[0], cols, row)
			if err != nil {
				return NullDatum, err
			}
			if arg.IsNull() {
				return NullDatum, nil
			}
			switch strings.ToLower(x.Name.Name) {
			case "abs":
				if arg.Kind == KindInt {
					v := arg.Int
					if v < 0 {
						v = -v
					}
					return NewIntDatum(v), nil
				}
			}
		}
		// Unknown function — return null so routing falls through to "no partition found".
		return NullDatum, nil
	case *parser.IntegerConst:
		return NewIntDatum(x.Value), nil
	case *parser.BooleanConst:
		return NewBoolDatum(x.Value), nil
	case *parser.NullConst:
		return NullDatum, nil
	}
	return NullDatum, fmt.Errorf("unsupported partition key expression type %T", expr)
}

// routeToPartitionDepth recurses through nested partition hierarchies. The
// depth guard (max 8) prevents infinite loops on circular catalog states.
func routeToPartitionDepth(parent *catalog.Table, row Row, im *catalog.InMemory, ctx *Context, depth int) (*catalog.Table, error) {
	if (len(parent.PartitionKey) == 0 && len(parent.PartitionKeyExprs) == 0) || depth > 8 {
		return nil, nil
	}

	// resolvePartitionKeyDatum returns the datum for the i-th partition key entry,
	// either by reading the named column or evaluating the key expression. M0097-0023.
	resolvePartitionKeyDatum := func(i int) (Datum, error) {
		if i < len(parent.PartitionKeyExprs) && parent.PartitionKeyExprs[i] != nil {
			return evalPartitionKeyExpr(parent.PartitionKeyExprs[i], parent.Columns, row)
		}
		keyColName := parent.PartitionKey[i]
		for j, col := range parent.Columns {
			if strings.EqualFold(col.Name, keyColName) {
				if j < len(row) {
					return row[j], nil
				}
				return NullDatum, nil
			}
		}
		return NullDatum, nil
	}

	// Find the column index for the first partition key (used by LIST/HASH).
	keyDatum, err := resolvePartitionKeyDatum(0)
	if err != nil {
		return nil, err
	}

	var child *catalog.Table
	switch parent.PartitionMethod {
	case "LIST":
		keyStr := ""
		if keyDatum.Kind == KindInt {
			keyStr = fmt.Sprintf("%d", keyDatum.Int)
		} else if keyDatum.Kind == KindString {
			keyStr = keyDatum.StringValue()
		} else if keyDatum.Kind == KindBool {
			// Try both long ("true"/"false") and short ("t"/"f") boolean formats.
			// Partition bound values come from string literals like 'true'/'false'.
			if keyDatum.Int != 0 {
				keyStr = "true"
			} else {
				keyStr = "false"
			}
		} else if keyDatum.IsNull() {
			keyStr = "null" // matches FOR VALUES IN (null)
		}
		child = im.FindPartitionForValue(parent.OID, keyStr)
		// Also try short boolean format if no match with long format.
		if child == nil && keyDatum.Kind == KindBool {
			short := "f"
			if keyDatum.Int != 0 {
				short = "t"
			}
			child = im.FindPartitionForValue(parent.OID, short)
		}
	case "RANGE":
		// Build a string-formatted key tuple covering all partition key columns.
		// This supports both single-column (PartitionKey len=1) and multi-column
		// (PartitionKey len>1) RANGE partitioning.
		keyStrs := make([]string, 0, len(parent.PartitionKey))
		for ki := range parent.PartitionKey {
			d, kerr := resolvePartitionKeyDatum(ki)
			if kerr != nil {
				return nil, kerr
			}
			switch d.Kind {
			case KindInt:
				keyStrs = append(keyStrs, fmt.Sprintf("%d", d.Int))
			case KindString:
				keyStrs = append(keyStrs, d.StringValue())
			case KindNumeric:
				keyStrs = append(keyStrs, d.StringValue())
			default:
				if d.IsNull() {
					keyStrs = append(keyStrs, "null")
				} else {
					keyStrs = append(keyStrs, d.Format())
				}
			}
		}
		child = im.FindRangePartitionForDatums(parent.OID, keyStrs)
	case "HASH":
		opClass := ""
		if len(parent.PartitionKeyOpClasses) > 0 {
			opClass = parent.PartitionKeyOpClasses[0]
		}
		if opClass != "" && ctx != nil {
			// Use custom operator class hash function (FUNCTION 2). M0097-0022.
			hashFuncName, hasFn := im.LookupOpClassHashFunc(opClass)
			if hasFn {
				routines := ctx.Catalog.Routines()
				if routines != nil {
					rs := routines.LookupByName(parser.ObjectName{Name: hashFuncName})
					var bestRoutine *catalog.Routine
					for _, r := range rs {
						if len(r.ArgTypes) == 2 {
							bestRoutine = r
							break
						}
					}
					if bestRoutine != nil {
						seedDatum := NewIntDatum(int64(hashPartitionSeed))
						hResult, herr := executeStoredRoutine(bestRoutine, []Datum{keyDatum, seedDatum}, ctx, 0)
						if herr != nil {
							return nil, herr
						}
						if !hResult.IsNull() {
							h := uint64(hResult.Int)
							child = im.FindHashPartitionByHash(parent.OID, h)
						}
					}
				}
			}
		}
		if child == nil && opClass == "" {
			// Default built-in hash: use string representation of key. M0097-0015.
			keyStr := ""
			if keyDatum.Kind == KindInt {
				keyStr = fmt.Sprintf("%d", keyDatum.Int)
			} else if keyDatum.Kind == KindString {
				keyStr = keyDatum.StringValue()
			} else {
				keyStr = keyDatum.Format()
			}
			child = im.FindHashPartitionForValue(parent.OID, keyStr)
		}
	}
	if child == nil {
		return nil, nil
	}
	// Recurse into nested partitions (multi-level partition hierarchies).
	// The child row may need to be remapped to the child's column order first.
	if len(child.PartitionKey) > 0 {
		childRow := remapRowForPartition(parent.Columns, child.Columns, row)
		if nested, err := routeToPartitionDepth(child, childRow, im, ctx, depth+1); err != nil {
			return nil, err
		} else if nested != nil {
			return nested, nil
		}
	}
	return child, nil
}

// extractScanAndPredicate walks an Update/Delete child plan and pulls
// out the underlying scan target relation plus an optional predicate
// the runtime should apply per row. The runtime's scanMatching is
// inherently sequential — it walks every block of the relation —
// so an IndexScan plan is treated as "SeqScan with a synthesised
// `<indexed_col> = key` equality predicate". This is correct (the
// predicate filters the same tuples the index would have probed)
// but does not exploit the index for fast access; that
// optimisation is a follow-up. Filter(IndexScan) combines the
// outer Filter's predicate with the synthesised key predicate
// via AND.
//
// Surfaces an explicit XX000 for plan shapes the executor doesn't
// recognise — pre-existing planner-bug guard.
func extractScan(child planner.Node) (seq *planner.SeqScan, pred planner.Expr, idx *planner.IndexScan, err error) {
	switch c := child.(type) {
	case *planner.SeqScan:
		return c, nil, nil, nil
	case *planner.IndexScan:
		// Convert to SeqScan+predicate for the fallback path,
		// but also return the IndexScan so the caller can use
		// the B-tree directly.
		scan := &planner.SeqScan{Table: c.Table}
		return scan, indexScanPredicate(c), c, nil
	case *planner.Filter:
		switch inner := c.Child.(type) {
		case *planner.SeqScan:
			return inner, c.Predicate, nil, nil
		case *planner.IndexScan:
			scan := &planner.SeqScan{Table: inner.Table}
			idxPred := indexScanPredicate(inner)
			var combined planner.Expr
			if idxPred == nil {
				// Range scan — no synthesised equality predicate;
				// the Filter predicate alone is the full condition.
				combined = c.Predicate
			} else {
				combined = &planner.BinaryOp{
					Op:    parser.OpAnd,
					Left:  c.Predicate,
					Right: idxPred,
				}
			}
			return scan, combined, inner, nil
		}
		return nil, nil, nil, &ExecError{Code: "XX000", Pos: c.Pos(), Message: "Update/Delete: Filter child is not SeqScan or IndexScan"}
	}
	return nil, nil, nil, &ExecError{Code: "XX000", Pos: child.Pos(), Message: "Update/Delete: unsupported child plan"}
}

// indexScanPredicate synthesises a `<indexed_col> = key` equality
// predicate from a planner.IndexScan node so the runtime's
// scanMatching loop (which always seq-scans) filters correctly
// against the index's key target. The IndexScan's resolved
// `Key` expression carries the rhs; the lhs reconstructs as a
// ColumnRef pointing at the indexed column's table-output
// ordinal. v0 indexes are single-column so Index.Columns[0] is
// the relevant name; resolving against the IndexScan's parent
// schema gives the correct output index for ColumnRef.
//
// Range scans (Key == nil) return nil — UPDATE/DELETE with range
// predicates fall through to seq-scan, which is correct and safe.
func indexScanPredicate(ix *planner.IndexScan) planner.Expr {
	if ix.Key == nil {
		// Range scan: no equality predicate to synthesise.
		// The caller (extractScan) will combine this nil with
		// any Filter predicate already present. Returning nil
		// here causes the update/delete path to fall through to
		// a full seq-scan with Filter, which is always correct.
		return nil
	}
	col := ix.Index.Columns[0]
	out := ix.Output()
	for i, sc := range out {
		if sc.Name == col {
			return &planner.BinaryOp{
				Op:    parser.OpEq,
				Left:  &planner.ColumnRef{Index: i, Name: col, Type: sc.Type},
				Right: ix.Key,
			}
		}
	}
	// Catalog inconsistency — index references a column that
	// isn't on the table's output schema. Conservative: drop
	// the predicate (over-match into the seq-scan body); the
	// planner-side resolver should have caught this.
	return nil
}

// hotUpdateEligible returns true when a HOT update is legal for the
// given Update plan: no column that is being changed participates in
// any index on the target table. When this returns true the executor
// may write the new tuple version to the same page as the old one and
// skip any index inserts. If no indexes exist, all updates are
// HOT-eligible (the same-page placement is still beneficial for
// space reuse even without an index-cost saving).
// idxRowHasConcurrentXmax peeks at the page under a brief RLock to check
// whether the tuple at (blk, slot) has a concurrent (non-self) xmax stamp.
// This is used before firing a BEFORE UPDATE trigger: if a concurrent xmax is
// present (DELETE or UPDATE in flight), we defer the trigger to the EPQ loop
// so it only fires when EPQ confirms the row will actually be written.
func idxRowHasConcurrentXmax(ctx *Context, rel storage.RelFileNode, blk storage.BlockNumber, slot uint16) bool {
	s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
	if err != nil {
		return false
	}
	s.RLock()
	t, gerr := storage.PageGetHeapTuple(s.Page(), slot)
	s.RUnlock()
	ctx.Pool.Unpin(s)
	if gerr != nil {
		return false
	}
	return isConcurrentlyUpdated(t.Header, ctx.Tx.XID, &ctx.Snap, ctx.MultiXact)
}

func hotUpdateEligible(plan *planner.Update, ctx *Context) bool {
	indexes := ctx.Catalog.IndexesOnTable(plan.Table)
	for _, idx := range indexes {
		for _, idxCol := range idx.Columns {
			for i, set := range plan.Set {
				if set == nil {
					continue
				}
				if plan.Table.Columns[i].Name == idxCol {
					return false // indexed column is being changed
				}
			}
		}
	}
	return true
}

// markHeapPruneOptDirty emits a logical opportunistic-pruning WAL record
// (RecordKindHeapPruneOpt, M0046-0002) and marks the page dirty. Falls
// back to a conservative MarkDirty (full FPI) when no WAL hook is wired.
// The caller must hold the page's exclusive content lock.
func markHeapPruneOptDirty(
	pool *storage.Pool, slot *storage.Slot,
	rel storage.RelFileNode, blk storage.BlockNumber,
	result storage.PruneResult,
) error {
	logPrune := pool.LogHeapPruneOpt()
	if logPrune == nil {
		pool.MarkDirty(slot)
		return nil
	}
	return pool.MarkDirtyChangeRecord(slot, func() (storage.LSN, error) {
		return logPrune(rel, blk, result.Redirects, result.Unused)
	})
}

// markHeapHotUpdateDirty is the WAL-logging counterpart of
// markHeapInsertDirty / markHeapDeleteDirty for the HOT path: it
// emits one atomic HeapHotUpdate record covering both the old-tuple
// stamp and the new-tuple insert on the same page. Falls back to a
// conservative MarkDirty (full FPI on next checkpoint) when no WAL
// hook is wired.
func markHeapHotUpdateDirty(
	pool *storage.Pool, slot *storage.Slot,
	rel storage.RelFileNode, blk storage.BlockNumber,
	oldLineSlot uint16, xmax storage.TransactionID,
	tupleBytes []byte,
) error {
	logHot := pool.LogHeapHotUpdate()
	if logHot == nil {
		pool.MarkDirty(slot)
		return nil
	}
	// MarkDirtyLogicalChange — see markHeapInsertDirty for the
	// rationale.
	return pool.MarkDirtyLogicalChange(slot, func() (storage.LSN, error) {
		return logHot(rel, blk, oldLineSlot, xmax, tupleBytes)
	})
}

// isConcurrentlyUpdated reports whether the tuple has been updated or
// deleted by a transaction OTHER than myXID. Used under the page's
// exclusive Lock by the UPDATE / DELETE / HOT-update paths to detect
// the concurrent-xmax-stamp race that produces orphan visible tuples
// in MVCC (M0090-0002).
//
// snap is the current statement snapshot; it is passed for context but
// the aborted-xmax disambiguation is handled in the EPQ retry loops
// (see updateOp, deleteOp): when epqRecheckVisible finds the row still
// visible AND snap.HasInProgress(xmax) is false, the xmax was aborted,
// so the loop breaks instead of retrying → avoids permanent 40001 on
// rolled-back HOT update chains (M0099 fix).
//
// A lock-only xmax (SELECT FOR UPDATE) is NOT treated as a concurrent
// update — the lock holder does not own the row's write intent.
//
// mxs is the process-shared multixact store (nil only in unit tests that
// build single-xid headers). When the xmax is an updater-bearing
// multixact (IS_MULTI && !LOCK_ONLY), the real updater xid is resolved
// from the member store before the abort/self checks — the raw
// MultiXactId must never be fed to a single-xid API (myXID equality,
// snap.HasAborted). Mirrors activeLockHolders' multi resolution. M0118-0003.
func isConcurrentlyUpdated(h storage.HeapTupleHeader, myXID storage.TransactionID, snap *mvcc.Snapshot, mxs *multixact.Store) bool {
	// effXmax is the transaction id that actually updated/deleted the
	// tuple. For an updater-bearing multixact xmax, h.Xmax is a
	// MultiXactId, not a transaction id — resolve the updater member.
	effXmax := h.Xmax
	if storage.IsHeapTupleXmaxMulti(h.Infomask) && !storage.IsHeapTupleLockOnly(h.Infomask) {
		if mxs == nil {
			// Cannot resolve the multi; the IS_MULTI/!LOCK_ONLY bits say an
			// updater is present, so conservatively treat the row as
			// concurrently updated (forces an EPQ recheck under lock).
			return true
		}
		members, ok := mxs.Members(multixact.MultiXactId(h.Xmax))
		if !ok {
			return true
		}
		upd, has := multixact.GetUpdateXid(members)
		if !has {
			// Only lockers in the multi (no updater) — not a concurrent
			// update. (An all-locker multi should carry LOCK_ONLY; handle
			// the inconsistent case defensively rather than mis-reading the
			// MultiXactId as a deleter.)
			return false
		}
		effXmax = upd
	}

	// "Our own xmax stamp" — re-update in the same transaction is
	// always legal, regardless of HeapHotUpdated or other bits set
	// by our prior write.
	if effXmax != storage.InvalidTransactionID && effXmax == myXID {
		return false
	}
	// Beyond this point, any xmax/HOT marker is from a DIFFERENT
	// transaction.
	if h.Infomask&storage.HeapHotUpdated != 0 {
		// M0100-0005: If the HOT-updating transaction already aborted, the
		// HeapHotUpdated flag is stale — the row is not actually "concurrently
		// updated", so proceed directly without EPQ retry.
		if snap != nil && effXmax != storage.InvalidTransactionID && snap.HasAborted(effXmax) {
			return false
		}
		return true
	}
	if effXmax == storage.InvalidTransactionID {
		return false
	}
	if h.Infomask&storage.HeapXmaxInvalid != 0 {
		// Xmax is hinted as not-a-deleter; matches the
		// HeapXmaxInvalid semantics defined in heap.go:64.
		return false
	}
	if storage.IsHeapTupleLockOnly(h.Infomask) {
		return false
	}
	// If the deleting/updating transaction already aborted, the xmax is stale
	// — the row was never actually deleted or updated. M0100-0007.
	if snap != nil && snap.HasAborted(effXmax) {
		return false
	}
	return true
}

// multixactUpdaterXID resolves the update-member transaction id of an
// updater-bearing MultiXactId xmax (the value a tuple stores in t_xmax when
// IS_MULTI is set and LOCK_ONLY is clear). It returns InvalidTransactionID
// when the store is nil, the multixact is unknown, or the multixact carries
// only lockers (no updater). Callers MUST have already checked
// storage.IsHeapTupleXmaxMulti(infomask): feeding a raw MultiXactId to a
// single-transaction API such as mvcc.Manager.IsXIDActive / WaitForXID reads
// the multixact id as an unrelated transaction id and is a correctness bug.
func multixactUpdaterXID(mxs *multixact.Store, xmax storage.TransactionID) storage.TransactionID {
	if mxs == nil {
		return storage.InvalidTransactionID
	}
	members, ok := mxs.Members(multixact.MultiXactId(xmax))
	if !ok {
		return storage.InvalidTransactionID
	}
	upd, has := multixact.GetUpdateXid(members)
	if !has {
		return storage.InvalidTransactionID
	}
	return upd
}

// multixactFirstActiveMember returns the first member of a (typically
// lock-only) MultiXactId xmax that is still active and is not self, or
// InvalidTransactionID when every member has settled or only self holds the
// lock. A waiter blocks on the returned xid and re-probes; one wait per
// remaining holder converges because settled members drop out of IsXIDActive.
// Callers MUST have already checked storage.IsHeapTupleXmaxMulti(infomask).
func multixactFirstActiveMember(mxs *multixact.Store, txnMgr *mvcc.Manager, self, xmax storage.TransactionID) storage.TransactionID {
	if mxs == nil || txnMgr == nil {
		return storage.InvalidTransactionID
	}
	members, ok := mxs.Members(multixact.MultiXactId(xmax))
	if !ok {
		return storage.InvalidTransactionID
	}
	for _, m := range members {
		if m.Xid != self && txnMgr.IsXIDActive(m.Xid) {
			return m.Xid
		}
	}
	return storage.InvalidTransactionID
}

// concurrentModifierXID resolves the transaction id an UPDATE / DELETE / MERGE
// EvalPlanQual wait must block on for a tuple that isConcurrentlyUpdated has
// just flagged. For a single-xid xmax it is the xmax itself; for an
// updater-bearing MultiXactId (IS_MULTI && !LOCK_ONLY) the raw t_xmax is a
// MultiXactId — NOT a transaction id — so the real updater member is resolved
// from the member store before it ever reaches epqWait / WaitForXID / the
// wait-for graph / snap.HasInProgress / HasAbortedXID / IsXIDActive. This is the
// write-path twin of stampLockInner's read-side multi resolution (commit
// ab3881e8) and of isConcurrentlyUpdated's own resolution (which the caller has
// already run, so an updater member is present). Falls back to the raw xmax only
// when the multi is unresolvable (store nil / membership lost after a restart),
// matching the conservative path isConcurrentlyUpdated took to return true.
// M0118-0004.
func concurrentModifierXID(hdr storage.HeapTupleHeader, mxs *multixact.Store) storage.TransactionID {
	if storage.IsHeapTupleXmaxMulti(hdr.Infomask) && !storage.IsHeapTupleLockOnly(hdr.Infomask) {
		if upd := multixactUpdaterXID(mxs, hdr.Xmax); upd != storage.InvalidTransactionID {
			return upd
		}
	}
	return hdr.Xmax
}

// stampUpdaterXmaxPreservingLockers is the UPDATE/DELETE producer twin of the
// row-lock path's stampMultiLock / stampMultiUpdaterLock (operators_lockrows.go):
// when the old tuple already carries a pre-existing *lock-only* xmax naming one
// or more still-active foreign lockers (e.g. a FOR KEY SHARE holder), the writer
// must preserve those lockers into a {updater + survivors} MultiXactId instead of
// clobbering them with a plain single-xid updater stamp. This mirrors upstream
// heap_update / heap_delete, which call MultiXactIdCreate/Expand on the
// HEAP_XMAX_IS_LOCKED_ONLY pre-existing-locker path so a concurrent non-
// conflicting locker is not silently dropped.
//
// keysUpdated is whether the write changes a key column (DELETE and key-changing
// UPDATE → StatusUpdate; no-key UPDATE → StatusNoKeyUpdate). It is recorded as
// our updater member's status and drives the HEAP_KEYS_UPDATED hint bit.
//
// Returns ok=true with the MultiXactId and hint bits when a preserving multi
// should be stamped (the caller must use the multi-aware stamp primitive);
// ok=false when there is no surviving foreign locker to preserve — the common
// case — so the caller keeps the plain single-xid stamp. The bounded gate (a
// pre-existing active foreign lock-only holder) never arises on the pgbench
// TPC-B / TPC-H hot path, so the plain stamp remains the fast path. M0118-0004.
// effectiveWriterXID returns the (sub)transaction id that heap mutations in
// this statement must stamp as the old tuple's xmax (and the deletion's WAL
// record). Inside an open savepoint it is the session's current sub-XID; the
// per-statement ctx.Tx.XID is always rebuilt from the connection's top-level
// transaction (dispatch.go `ectx.Tx = tx`) and so never reflects an open
// savepoint on its own — the live sub-XID is read from the session, exactly as
// lockRowsOp.writerXID does for the row-lock path. Stamping xmax under the
// sub-XID is what lets ROLLBACK TO SAVEPOINT revert a DELETE/UPDATE:
// MarkSubxactAborted flips the sub-XID dead, so the subxact-aware visibility
// check (SeesCommittedXIDWithSubxacts) treats the deletion as never-happened and
// the tuple stays live. Mirrors upstream heap_delete/heap_update stamping
// GetCurrentTransactionId(), which returns the current subtransaction id.
// Outside a savepoint EffectiveWriterXID() == the top-level XID, so this is a
// strict no-op. M0118-0009 (docs/design/0118-0013).
func effectiveWriterXID(ctx *Context) storage.TransactionID {
	if sess, ok := ctx.Session.(*BasicSession); ok {
		if x := sess.EffectiveWriterXID(); x != storage.InvalidTransactionID {
			return x
		}
	}
	return ctx.Tx.XID
}

func stampUpdaterXmaxPreservingLockers(ctx *Context, hdr storage.HeapTupleHeader, keysUpdated bool) (multixact.MultiXactId, uint16, uint16, bool, error) {
	if ctx.MultiXact == nil || ctx.TxnMgr == nil {
		return 0, 0, 0, false, nil
	}
	// Only the lock-only case is preserved here. A non-lock-only (updater-bearing)
	// xmax means a concurrent modify, which isConcurrentlyUpdated/EPQ handles
	// upstream of the stamp; reaching the stamp with such an xmax is not this
	// producer's concern.
	if hdr.Xmax == storage.InvalidTransactionID || !storage.IsHeapTupleLockOnly(hdr.Infomask) {
		return 0, 0, 0, false, nil
	}

	// Enumerate the existing lock holders (single-xid or multi).
	var existing []multixact.Member
	if storage.IsHeapTupleXmaxMulti(hdr.Infomask) {
		members, ok := ctx.MultiXact.Members(multixact.MultiXactId(hdr.Xmax))
		if !ok {
			// Membership lost (stamped before a restart with no persisted store):
			// nothing resolvable to combine with; keep the plain stamp.
			return 0, 0, 0, false, nil
		}
		existing = members
	} else {
		existing = []multixact.Member{{Xid: hdr.Xmax, Status: lockOnlyMemberStatus(hdr.Infomask, hdr.Infomask2)}}
	}

	// Keep only still-active foreign lockers whose lock mode does NOT conflict
	// with our update. A dead locker's lock is released; a *conflicting* locker
	// must be waited for, not carried into the multi — preserving it would let an
	// update coexist with a lock it should block on. (The conflict-wait itself is
	// handled upstream of the stamp; an unwaited conflicting locker is dropped by
	// the plain single-xid stamp, the pre-existing behaviour, until that wait
	// lands.) This makes a key-changing UPDATE / DELETE (StatusUpdate, which
	// conflicts with every lock mode incl. FOR KEY SHARE) a no-op here, while a
	// no-key UPDATE (StatusNoKeyUpdate) preserves a non-conflicting FOR KEY SHARE
	// holder — the spec scenario. Self is dropped (re-added below as the updater).
	ourStatus := updaterMemberStatus(keysUpdated)
	ourXID := effectiveWriterXID(ctx) // sub-XID inside a savepoint; else top-level
	ourTop := ctx.TxnMgr.TopLevelXid(ourXID)
	survivors := make([]multixact.Member, 0, len(existing)+1)
	for _, m := range existing {
		if m.Xid == ourXID {
			continue // exactly our writer: re-added once below as the updater
		}
		// A member from our own transaction tree but at an OUTER level (the
		// top-level xact, or a savepoint we are nested inside) is a lock this
		// transaction took before the current savepoint. It never conflicts with
		// us (same xact) and MUST be carried forward unchanged so ROLLBACK TO this
		// savepoint restores it: dropping the savepoint's own updater member then
		// leaves the outer locker as the surviving xmax, so a DELETE/UPDATE inside
		// a savepoint over a row this xact already locked FOR KEY SHARE keeps that
		// lock alive after the rollback (delete-abort-savept). Bypasses the
		// foreign-locker liveness/conflict filtering below (self never conflicts).
		if ctx.TxnMgr.TopLevelXid(m.Xid) == ourTop {
			survivors = append(survivors, m)
			continue
		}
		if !ctx.TxnMgr.IsXIDActive(m.Xid) {
			continue // dead foreign locker: lock already released
		}
		if multixact.StatusesConflict(m.Status, ourStatus) {
			continue // conflicting foreign locker: must be waited for, not preserved
		}
		survivors = append(survivors, m)
	}
	if len(survivors) == 0 {
		return 0, 0, 0, false, nil
	}
	survivors = append(survivors, multixact.Member{Xid: ourXID, Status: ourStatus})

	multi, err := ctx.MultiXact.CreateFromMembers(survivors)
	if err != nil {
		return 0, 0, 0, false, err
	}
	infomask, infomask2 := multixact.HintBits(survivors)
	return multi, infomask, infomask2, true, nil
}

// stampUpdaterXmaxNonHOT is the non-HOT (delete-half / DELETE) convenience
// wrapper around stampUpdaterXmaxPreservingLockers: it stamps the old tuple's
// xmax at `slot` on `page` with a {updater + surviving lockers} MultiXactId when
// a pre-existing foreign non-conflicting locker must be preserved, else falls
// back to the plain single-xid updater stamp. Unlike the HOT path it writes no
// CTID / HEAP_HOT_UPDATED (the new version lives in a different slot/page and the
// chain link is written separately by the caller). The bounded gate means the
// plain stamp remains the fast path for every hot-path write. M0118-0004.
func stampUpdaterXmaxNonHOT(ctx *Context, page storage.Page, slot uint16, hdr storage.HeapTupleHeader, keysUpdated bool) error {
	multi, im, im2, ok, err := stampUpdaterXmaxPreservingLockers(ctx, hdr, keysUpdated)
	if err != nil {
		return err
	}
	if ok {
		return storage.PageSetHeapTupleXmaxMulti(page, slot, storage.TransactionID(multi), im, im2)
	}
	if serr := storage.PageSetHeapTupleXmax(page, slot, effectiveWriterXID(ctx)); serr != nil {
		return serr
	}
	// A key-changing UPDATE / DELETE must mark the old tuple HEAP_KEYS_UPDATED so
	// a concurrent FOR KEY SHARE locker recognises the conflict and WAITS on the
	// updater instead of following the CTID chain to the (uncommitted) new
	// version. The multi path above already encodes this through the updater
	// member's StatusUpdate (multixact.HintBits sets the bit); the single-xid
	// fallback — taken when there is no pre-existing locker to combine with, the
	// common case — must set it explicitly. Mirrors upstream heap_update /
	// heap_delete stamping HEAP_KEYS_UPDATED on the old tuple. Without it, s2's
	// FOR KEY SHARE in aborted-keyrevoke (perms 7-9) reads keysUpdated=false and
	// proceeds out of order. M0118-0009.
	if keysUpdated {
		return storage.PageSetHeapTupleKeysUpdated(page, slot)
	}
	return nil
}

// tryApplyHOTUpdate attempts a same-page HOT update of the tuple at
// (blk, oldSlot). It:
//  1. Encodes newRow with HeapOnlyTuple set in the tuple infomask.
//  2. Tries PageAddHeapTuple on the same page; returns (false, nil) on
//     ErrNoSpaceInPage so the caller falls back to the normal path.
//  3. On success, stamps the old slot via PageStampHotOldTuple and
//     emits one atomic HeapHotUpdate WAL record.
//
// The caller must not hold the page's content lock — this function
// acquires and releases it internally.
func tryApplyHOTUpdate(
	ctx *Context,
	rel storage.RelFileNode,
	cols []catalog.Column,
	blk storage.BlockNumber,
	oldSlot uint16,
	newRow Row,
) (bool, error) {
	// M0093: materialise the transaction's XID BEFORE the
	// isConcurrentlyUpdated race check (line 646). Calling it
	// after would feed XID=0 into the check, letting a foreign
	// xmax stamp slip through as a false negative (orphan visible
	// tuples — the M0090 invariant we explicitly guard).
	if err := ctx.MaterializeWriterXID(); err != nil {
		return false, err
	}
	// Always encode in PG-native physical format (M0111-0002): one on-disk
	// heap-tuple format for HOT and non-HOT updates alike. goopg reads it back
	// by selecting the decoder from the tuple header (natts/bitmap).
	body, encErr := EncodeRowPG(cols, newRow)
	if encErr != nil {
		var ee *ExecError
		if errors.As(encErr, &ee) {
			return false, ee
		}
		return false, &ExecError{Code: "XX000", Message: encErr.Error()}
	}
	bitmap := NullBitmapPG(newRow)
	// xmin = effective writer XID (sub-XID inside an open savepoint) so a HOT new
	// version created in a savepoint disappears on ROLLBACK TO, mirroring the
	// old-tuple xmax stamp below (PageStampHotOldTuple under effectiveWriterXID);
	// no-op outside a savepoint. M0118-0009.
	xmin := effectiveWriterXID(ctx)
	var tup storage.HeapTuple
	if len(bitmap) > 0 {
		tup = storage.NewHeapTupleWithNulls(xmin, storage.InvalidTransactionID, bitmap, body)
	} else {
		tup = storage.NewHeapTuple(xmin, storage.InvalidTransactionID, body)
	}
	tup.Header.Infomask |= storage.HeapOnlyTuple
	tup.Header.SetNatts(len(cols))
	tup.Header.Infomask |= storage.HeapXmaxInvalid
	tupleBytes, err := tup.MarshalBinary()
	if err != nil {
		return false, err
	}

	s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
	if err != nil {
		return false, err
	}
	s.Lock()

	// Race check: between the scan-time RLock release and this Lock
	// acquire, a concurrent UPDATE/DELETE or an opportunistic prune
	// in another session may have flipped the old slot out of
	// LP_NORMAL. Detect that here, before adding the new tuple, so
	// we don't leave an orphan tuple body that a later
	// PageStampHotOldTuple would have abandoned. Caller treats
	// (false, nil) as "skip this row" — same fall-through as the
	// page-full case.
	if oldItem, ierr := storage.PageGetItemID(s.Page(), oldSlot); ierr == nil &&
		oldItem.Flags != storage.ItemIDNormal {
		s.Unlock()
		ctx.Pool.Unpin(s)
		return false, nil
	}

	// M0090-0002: under the exclusive Lock the page is frozen.
	// Re-read the old tuple and detect whether ANOTHER transaction
	// has already stamped xmax / set HeapHotUpdated. Without this
	// check, two concurrent HOT-updates of the same row both call
	// PageStampHotOldTuple — the second one OVERWRITES the first's
	// xmax + CTID, orphaning the first's new tuple in a state
	// where it remains visible under MVCC. The accumulated orphans
	// are the cause of the pgbench scale-100 1,610-visible-rows
	// symptom in pgbench_branches.
	//
	// EvalPlanQual (M0098-0004, M0099-0004): on concurrent xmax conflict,
	// wait for the conflicting transaction (with deadlock detection) and
	// fall back to the delete+insert path so it can re-check visibility.
	if oldTuple, gerr := storage.PageGetHeapTuple(s.Page(), oldSlot); gerr == nil &&
		isConcurrentlyUpdated(oldTuple.Header, ctx.Tx.XID, &ctx.Snap, ctx.MultiXact) {
		xmax := concurrentModifierXID(oldTuple.Header, ctx.MultiXact)
		s.Unlock()
		ctx.Pool.Unpin(s)
		if epqWait(ctx, xmax) {
			// Deadlock detected — surface 40001 immediately rather than
			// looping into the delete+insert EPQ path.
			return false, &ExecError{
				Code:    "40001",
				Message: "could not serialize access due to concurrent update (deadlock)",
			}
		}
		return false, nil // fall back to delete+insert; caller re-checks
	}

	newSlot, addErr := storage.PageAddHeapTuple(s.Page(), tup)
	if addErr != nil && errors.Is(addErr, storage.ErrNoSpaceInPage) {
		// Page full: attempt opportunistic pruning before giving up on HOT.
		if ctx.EnableOpportunisticPrune && ctx.TxnMgr != nil {
			oldestXmin := ctx.TxnMgr.OldestXmin()
			result, pruneErr := storage.PagePruneOpt(s.Page(), oldestXmin)
			if pruneErr == nil && (len(result.Redirects)+len(result.Unused)) > 0 {
				// Emit WAL for the prune BEFORE the HOT-insert WAL so replay
				// restores space first.
				if pderr := markHeapPruneOptDirty(ctx.Pool, s, rel, blk, result); pderr == nil {
					newSlot, addErr = storage.PageAddHeapTuple(s.Page(), tup)
				}
			}
		}
	}
	if addErr != nil {
		s.Unlock()
		ctx.Pool.Unpin(s)
		if errors.Is(addErr, storage.ErrNoSpaceInPage) {
			return false, nil // caller falls back to normal path
		}
		return false, addErr
	}

	// Stamp the old tuple's xmax + HOT chain link. If the old tuple already
	// carries a pre-existing non-conflicting foreign locker (a lock-only xmax,
	// e.g. FOR KEY SHARE), preserve it into a {updater + survivors} MultiXactId
	// instead of clobbering it with the plain single-xid stamp (M0118-0004
	// producer). A HOT update never changes a key column, so the updater status
	// is always no-key (keysUpdated=false). The HeapHotUpdate WAL record still
	// carries the single updater xid (markHeapHotUpdateDirty below), so crash
	// recovery degrades to the single-xid stamp — correct, since the transient
	// lockers do not survive a crash (multixact WAL persistence deferred; see
	// docs/design/0118-0002).
	stampErr := func() error {
		if oldHdrTup, herr := storage.PageGetHeapTuple(s.Page(), oldSlot); herr == nil {
			multi, im, im2, ok, perr := stampUpdaterXmaxPreservingLockers(ctx, oldHdrTup.Header, false)
			if perr != nil {
				return perr
			}
			if ok {
				return storage.PageStampHotOldTupleMulti(s.Page(), oldSlot, storage.TransactionID(multi), im, im2, effectiveWriterXID(ctx), blk, newSlot)
			}
		}
		return storage.PageStampHotOldTuple(s.Page(), oldSlot, effectiveWriterXID(ctx), blk, newSlot)
	}()
	if stampErr != nil {
		s.Unlock()
		ctx.Pool.Unpin(s)
		if errors.Is(stampErr, storage.ErrUnsupportedItem) || errors.Is(stampErr, storage.ErrInvalidSlot) {
			// PagePruneOpt above (page-full fallback) can invalidate
			// the old slot in a tight window between our pre-check
			// and this stamp. Caller treats (false, nil) as "skip
			// this row" — same fall-through as the page-full case.
			return false, nil
		}
		return false, stampErr
	}

	derr := markHeapHotUpdateDirty(ctx.Pool, s, rel, blk, oldSlot, effectiveWriterXID(ctx), tupleBytes)
	s.Unlock()
	ctx.Pool.Unpin(s)
	if derr == nil && ctx.InDMLCTE && ctx.CTEWriteFence != nil {
		newItemPtr := storage.ItemPointer{Block: blk, Offset: newSlot}
		oldItemPtr := storage.ItemPointer{Block: blk, Offset: oldSlot}
		if _, inFence := ctx.CTEWriteFence[oldItemPtr]; inFence {
			if ctx.CTENewToOld != nil {
				if orig, ok := ctx.CTENewToOld[oldItemPtr]; ok {
					if ctx.CTESelfModifiedErrors == nil {
						ctx.CTESelfModifiedErrors = make(map[storage.ItemPointer]struct{})
					}
					ctx.CTESelfModifiedErrors[orig] = struct{}{}
				}
			}
		}
		ctx.CTEWriteFence[newItemPtr] = struct{}{}
		if ctx.CTENewToOld != nil {
			ctx.CTENewToOld[newItemPtr] = oldItemPtr
		}
	}
	return true, derr
}

// updateOp scans the target relation and rewrites visible matching
// tuples. The primary strategy is a HOT update (same-page insert,
// no index entry added) when no indexed column is being changed and
// the page has space. Falls back to the classic delete+insert pattern
// when HOT is ineligible or the page is full.
type updateOp struct {
	plan         *planner.Update
	scan         *planner.SeqScan
	pred         planner.Expr
	ctx          *Context
	rowsAffected int64
	done         bool

	// idxScan, when non-nil, is the IndexScan from the child plan.
	// updateOp uses the B-tree to find matching tuples (O(log n))
	// instead of the full SeqScan path (O(n)). Set by newUpdateOp
	// when the planner produced an IndexScan.
	idxScan *planner.IndexScan

	// retRows / retIdx: collected RETURNING rows; iterated via Next()
	// after all updates are applied (M0100-0005).
	retRows []Row
	retIdx  int
}

// RowsAffected satisfies executor.RowCounter.
func (o *updateOp) RowsAffected() int64 { return o.rowsAffected }

func newUpdateOp(p *planner.Update) (*updateOp, error) {
	scan, pred, idxScan, err := extractScan(p.Child)
	if err != nil {
		return nil, err
	}
	return &updateOp{plan: p, scan: scan, pred: pred, idxScan: idxScan}, nil
}

func (o *updateOp) Schema() planner.Schema { return o.plan.ReturningSchema }

// appendUpdateRetRow evaluates the plan's RETURNING expressions against
// newRow and appends the result to o.retRows. No-op when RETURNING is absent.
func (o *updateOp) appendUpdateRetRow(newRow Row) {
	o.appendUpdateRetRowWithFrom(newRow, nil)
}

// appendUpdateRetRowWithFrom is the UPDATE … FROM variant: RETURNING
// expressions may reference columns from joined FROM tables. The
// planner resolves those references with column indices that follow
// the target columns, so we build a combined eval row
// `[newRow..., fromPortion...]` to satisfy them. fromPortion may be nil
// for the plain UPDATE path.
func (o *updateOp) appendUpdateRetRowWithFrom(newRow Row, fromPortion Row) {
	if len(o.plan.Returning) == 0 {
		return
	}
	evalRow := newRow
	if len(fromPortion) > 0 {
		evalRow = make(Row, 0, len(newRow)+len(fromPortion))
		evalRow = append(evalRow, newRow...)
		evalRow = append(evalRow, fromPortion...)
	}
	retRow := make(Row, len(o.plan.Returning))
	for i, expr := range o.plan.Returning {
		v, _ := evalExpr(expr, evalRow, o.ctx)
		retRow[i] = v
	}
	o.retRows = append(o.retRows, retRow)
}

func (o *updateOp) Open(ctx *Context) error {
	if ctx.Pool == nil || ctx.Catalog == nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "Update requires storage handles in Context"}
	}
	o.ctx = ctx
	rel := ctx.Catalog.RelFileNode(o.plan.Table)
	if err := ctx.acquireRelLock(rel, lockmgr.RowExclusiveLock); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.plan.Pos()
		}
		return err
	}
	return nil
}

func (o *updateOp) Close() error { return nil }

// updateViaIndex uses the B-tree to find the tuple to update (O(log n))
// instead of scanning all pages. Falls back to the path in Next() when
// no IndexScan is available.
func (o *updateOp) updateViaIndex(rel storage.RelFileNode, cols []catalog.Column) (TupleSlot, error) {
	ix := o.idxScan
	idxRel := o.ctx.Catalog.IndexRelFileNode(ix.Index)
	tree, err := btree.Open(o.ctx.Pool, idxRel)
	if err != nil {
		return nil, &ExecError{Code: "XX000", Pos: ix.Pos(), Message: err.Error()}
	}

	// Evaluate the index key — same logic as indexScanOp.lookupKey.
	v, err := evalExpr(ix.Key, nil, o.ctx)
	if err != nil {
		return nil, err
	}
	if v.IsNull() {
		return nil, nil
	}
	col, ok := o.ctx.Catalog.LookupColumn(ix.Table, ix.Index.Columns[0])
	if !ok {
		return nil, &ExecError{Code: "XX000", Pos: ix.Pos(),
			Message: fmt.Sprintf("indexed column %q not found on table %q", ix.Index.Columns[0], ix.Table.Name)}
	}
	keyBytes, encErr := encodeBTreeKeyForColumn(v, col, ix.Key.Pos())
	if encErr != nil {
		return nil, encErr
	}

	// Scan the B-tree for matching entries.
	type pendingUpdate struct {
		blk    storage.BlockNumber
		slot   uint16
		newRow Row
		oldRow Row // for BEFORE UPDATE trigger firing
	}
	pending := make([]pendingUpdate, 0, 1) // pre-alloc for common 1-row match
	heapRel := rel

	hiBytes := keyBytes
	if len(ix.Index.Columns) > 1 {
		hiBytes = appendCompositeUpperPadding(keyBytes)
	}
	err = tree.RangeScan(keyBytes, hiBytes, func(_ []byte, ptr storage.ItemPointer) (bool, error) {
		slot, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: heapRel, Block: ptr.Block})
		if err != nil {
			return false, err
		}
		slot.RLock()
		// Follow the HOT chain: the index pointer may be stale (pointing
		// to an earlier version whose CTID leads to the live version).
		tuple, actualSlot, found := followHOTChain(slot.Page(), ptr.Offset, o.ctx.Snap, o.ctx.Tx.XID, o.ctx.MultiXact)
		slot.RUnlock()
		o.ctx.Pool.Unpin(slot)
		if !found {
			return true, nil
		}
		// Check for foreign tuple lock (M0021 step 2b) on the live version.
		if foreignLockOnly(tuple.Header, o.ctx.Tx.XID) {
			livePtr := storage.ItemPointer{Block: ptr.Block, Offset: actualSlot}
			if err := o.ctx.acquireTupleLock(rel, livePtr, lockmgr.ExclusiveLock); err != nil {
				return false, err
			}
			// Re-read after lock released — follow chain again.
			slot2, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: heapRel, Block: ptr.Block})
			if err != nil {
				return false, err
			}
			slot2.RLock()
			tuple, actualSlot, found = followHOTChain(slot2.Page(), ptr.Offset, o.ctx.Snap, o.ctx.Tx.XID, o.ctx.MultiXact)
			slot2.RUnlock()
			o.ctx.Pool.Unpin(slot2)
			if !found {
				return true, nil
			}
		}
		var decRow Row
		decRow, err = make(Row, len(cols)), nil
		decNatts := int(tuple.Header.Infomask2 & 0x07FF)
		if decErr := DecodeRowIntoMctxPGTuple(decRow, cols, tuple.Data, tuple.Bitmap, decNatts, nil); decErr != nil {
			return false, decErr
		}
		row := decRow

		// Build new row from SET expressions.
		// Clear multi-column subquery cache so each row gets a fresh evaluation.
		clear(o.ctx.MultiAssignSubqCache)
		newRow := make(Row, len(cols))
		for i := range cols {
			if o.plan.Set[i] == nil {
				newRow[i] = row[i]
				continue
			}
			v, err := evalExpr(o.plan.Set[i], row, o.ctx)
			if err != nil {
				return false, err
			}
			newRow[i] = v
		}
		// Recompute GENERATED ALWAYS AS … STORED columns after SET. M0096-0008.
		_ = computeGeneratedColumns(cols, newRow)
		// M0111-0001: restore columns that became null during decode→rebuild.
		for i, c := range cols {
			if c.GeneratedExpr == "" && newRow[i].IsNull() && !row[i].IsNull() {
				newRow[i] = row[i]
			}
		}
		pending = append(pending, pendingUpdate{
			blk:    ptr.Block,
			slot:   actualSlot, // use live slot, not the index-pointed slot
			newRow: newRow,
			oldRow: cloneRow(row),
		})
		return true, nil
	})
	if err != nil {
		return nil, err
	}

	// Modification phase: HOT update when eligible, else delete+insert.
	hotEligible := hotUpdateEligible(o.plan, o.ctx)
	idxTbl := o.plan.Table
	// M0118-0003 (write-path half): an UPDATE that changes an index *key*
	// column (hotEligible is false — INCLUDE columns are excluded from a
	// covering index's key list, so this matches the HEAP_KEYS_UPDATED stamp
	// below) is a StatusUpdate that conflicts with every lock strength; a no-key
	// UPDATE is a StatusNoKeyUpdate that does NOT conflict with FOR KEY SHARE.
	// Classify once so the write-path wait matches how the locker decoded it.
	updReqStatus := multixact.StatusNoKeyUpdate
	if !hotEligible {
		updReqStatus = multixact.StatusUpdate
	}
	for _, pu := range pending {
		// Honour a row lock propagated forward onto this live version by
		// heap_lock_updated_tuple before writing: wait for every still-active
		// foreign holder whose strength conflicts with this UPDATE. A lock-only
		// xmax does not relocate the row, so pu.blk/pu.slot stay valid.
		if err := waitForConflictingRowLock(o.ctx, rel, pu.blk, pu.slot, updReqStatus, o.plan.Pos()); err != nil {
			return nil, err
		}
		// Fire BEFORE UPDATE trigger if the row has no concurrent xmax (i.e. a
		// HOT-eligible write or a plain non-concurrent update). When a concurrent
		// xmax is present (concurrent DELETE / UPDATE in progress), defer the
		// trigger to the EPQ loop so it only fires when EPQ confirms the row will
		// actually be written. M0100-0005-merge-delete-fix.
		trigFiredViaIdx := false
		if len(idxTbl.Triggers) > 0 && !idxRowHasConcurrentXmax(o.ctx, rel, pu.blk, pu.slot) {
			retRow, ok := fireTriggers(o.ctx, idxTbl, "before", "update", pu.oldRow, pu.newRow)
			if !ok {
				continue // RETURN NULL — skip this row
			}
			pu.newRow = retRow
			trigFiredViaIdx = true
		}
		used := false
		if hotEligible {
			var err error
			used, err = tryApplyHOTUpdate(o.ctx, rel, cols, pu.blk, pu.slot, pu.newRow)
			if err != nil {
				return nil, err
			}
		}
		epqSkip := false
		if !used {
			// HOT ineligible or page full — fall back to normal delete+insert.
			// EvalPlanQual retry loop (M0098-0004): retry up to maxEPQRetries
			// times when a concurrent xmax conflict is detected.
			epqDoUpdate := false // set when abort confirmed; bypasses EPQ on next iter
			for epqRetry := 0; ; epqRetry++ {
				s, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: pu.blk})
				if err != nil {
					return nil, err
				}
				s.Lock()
				// M0090-0002: detect concurrent xmax-stamp under the
				// exclusive Lock before our own stamp. Capture old tuple
				// bytes for WAL logical record (M0094-0002).
				oldTup, oldGerr := storage.PageGetHeapTuple(s.Page(), pu.slot)
				// M0100-0005: when epqDoUpdate is set (abort confirmed on previous
				// iteration), skip the EPQ check and fall through to the update code.
				if !epqDoUpdate && oldGerr == nil && isConcurrentlyUpdated(oldTup.Header, o.ctx.Tx.XID, &o.ctx.Snap, o.ctx.MultiXact) {
					xmax := concurrentModifierXID(oldTup.Header, o.ctx.MultiXact)
					s.Unlock()
					o.ctx.Pool.Unpin(s)
					if epqRetry >= epqRetryLimit(o.ctx.Tx.Isolation) {
						return nil, &ExecError{
							Code:    "40001",
							Pos:     o.plan.Pos(),
							Message: "could not serialize access due to concurrent update",
						}
					}
					// Save origSnap before epqWait: epqWait itself refreshes
					// ctx.Snap (so epqRecheckVisible sees committed changes), so
					// origSnap must be captured here — before that first refresh —
					// to hold the true query-start snapshot. epqFollowHOT uses it
					// to evaluate sub-plan quals (EXISTS, scalar subqueries) with
					// the original snapshot, matching PG's EvalPlanQual semantics
					// (chain-follow uses refreshed snapshot; qual eval uses origSnap).
					origSnap := o.ctx.Snap
					if epqWait(o.ctx, xmax) {
						return nil, &ExecError{
							Code:    "40001",
							Pos:     o.plan.Pos(),
							Message: "could not serialize access due to concurrent update (deadlock)",
						}
					}
					// RC: refresh snapshot again so committed deletes/updates are
					// visible without relying on the frozen BEGIN-time snapshot.
					// Without this, the committed xmax stays in snap.InProgress
					// and epqRecheckVisible returns true indefinitely (tight loop
					// until maxEPQRetries → spurious 40001). M0100-0005-merge-delete.
					if o.ctx.Tx.Isolation == mvcc.IsolationReadCommitted && o.ctx.TxnMgr != nil {
						if newSnap, snapErr := o.ctx.TxnMgr.SnapshotFor(o.ctx.Tx); snapErr == nil {
							o.ctx.Snap = newSnap
						}
					}
					// M0100-0004: EPQ chain-following for RC; 40001 for RR.
					visible, _ := epqRecheckVisible(o.ctx, rel, pu.blk, pu.slot)
					if visible {
						// xmax aborted; row still exists at original slot.
						if !o.ctx.Snap.HasInProgress(xmax) {
							// M0100-0005: mark as do-update and retry so the
							// update code (PageSetHeapTupleXmax + writeHeapRow)
							// executes on the next iteration, bypassing EPQ.
							epqDoUpdate = true
							continue
						}
						// M0100-0011: RR/Serializable firstSnap is frozen at BEGIN;
						// HasInProgress may still return true for an XID that has
						// since aborted. Confirm via the manager's global abort list.
						if o.ctx.TxnMgr != nil && o.ctx.TxnMgr.HasAbortedXID(xmax) {
							epqDoUpdate = true
							continue
						}
						continue // still in-progress; retry
					}
					// Concurrent tx committed — row was updated or deleted.
					if o.ctx.Tx.Isolation != mvcc.IsolationReadCommitted {
						errMsg := "could not serialize access due to concurrent update"
						if sp, perr := o.ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: pu.blk}); perr == nil {
							sp.RLock()
							if ot, gerr := storage.PageGetHeapTuple(sp.Page(), pu.slot); gerr == nil {
								ctid := ot.Header.CTID
								// goopg initial CTID is {InvalidBlockNumber,0}; stampOldCtid
								// only runs on UPDATE. So InvalidBlockNumber means deleted.
								if ctid.Block == storage.InvalidBlockNumber {
									errMsg = "could not serialize access due to concurrent delete"
								}
							}
							sp.RUnlock()
							o.ctx.Pool.Unpin(sp)
						}
						return nil, &ExecError{Code: "40001", Pos: o.plan.Pos(),
							Message: errMsg}
					}
					// RC: follow HOT chain and re-evaluate WHERE + SET.
					// Before deciding the chain terminated, check the
					// moved-to-another-partition sentinel on the old slot —
					// cross-partition UPDATEs leave no HOT chain because the
					// new version lives in a different relation. PG raises a
					// distinct error in that case.
					if epqSlotMovedToAnotherPartition(o.ctx, rel, pu.blk, pu.slot) {
						return nil, errMovedToAnotherPartition(o.plan.Pos())
					}
					newBlk := pu.blk
					newSlot, baseRow, hotFound, predOk := epqFollowHOT(o.ctx, rel, pu.blk, pu.slot, cols, o.pred, &origSnap)
					chainFound := predOk
					if !chainFound && !hotFound {
						// Non-HOT cross-page chain (M0100-0005z): fall back
						// to raw t_ctid chain walk.
						if cBlk, cSlot, cRow, cFound := epqFollowChain(o.ctx, rel, pu.blk, pu.slot, cols, o.pred, &origSnap); cFound {
							newBlk, newSlot, baseRow, chainFound = cBlk, cSlot, cRow, true
						}
					}
					if !chainFound {
						epqSkip = true
						break
					}
					// Re-bind SET expressions against the latest row.
					for i := range cols {
						if i < len(o.plan.Set) && o.plan.Set[i] != nil {
							v, e := evalExpr(o.plan.Set[i], baseRow, o.ctx)
							if e != nil {
								return nil, e
							}
							pu.newRow[i] = v
						} else {
							pu.newRow[i] = baseRow[i]
						}
					}
					_ = computeGeneratedColumns(cols, pu.newRow)
			// M0111-0001: restore columns that became null during decode→rebuild.
			for i, c := range cols {
				if c.GeneratedExpr == "" && pu.newRow[i].IsNull() && !pu.oldRow[i].IsNull() {
					pu.newRow[i] = pu.oldRow[i]
				}
			}
					pu.blk = newBlk
					pu.slot = newSlot
					continue // re-run loop to stamp xmax on new slot
				}
				var oldTupleBytes []byte
				if oldGerr == nil {
					oldTupleBytes, _ = oldTup.MarshalBinary()
				}
				if oldGerr != nil {
					// PageGetHeapTuple failed for this slot
					// (e.g. concurrent prune / page compaction
					// invalidated it after scan-time). Skip.
					s.Unlock()
					o.ctx.Pool.Unpin(s)
					epqSkip = true
					break
				}
				// Fire BEFORE UPDATE trigger after EPQ validation confirms the row
				// will be updated. Fires at most once per pending update. Unlock
				// the page first so trigger SQL can proceed safely.
				if !trigFiredViaIdx && len(idxTbl.Triggers) > 0 {
					trigFiredViaIdx = true
					s.Unlock()
					o.ctx.Pool.Unpin(s)
					retRow, trigOK := fireTriggers(o.ctx, idxTbl, "before", "update", pu.oldRow, pu.newRow)
					if !trigOK {
						// RETURN NULL — skip this row
						epqSkip = true
						break
					}
					pu.newRow = retRow
					// Re-pin for the write.
					var rerr error
					s, rerr = o.ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: pu.blk})
					if rerr != nil {
						return nil, rerr
					}
					s.Lock()
					// Re-check for concurrent modification during trigger execution.
					freshTup, freshErr := storage.PageGetHeapTuple(s.Page(), pu.slot)
					if freshErr != nil || isConcurrentlyUpdated(freshTup.Header, o.ctx.Tx.XID, &o.ctx.Snap, o.ctx.MultiXact) {
						s.Unlock()
						o.ctx.Pool.Unpin(s)
						if freshErr != nil {
							epqSkip = true
							break
						}
						// Concurrent update during trigger — EPQ-retry with same trigger skip.
						continue
					}
					// Refresh oldTupleBytes from re-pinned page.
					if freshErr == nil {
						oldTupleBytes, _ = freshTup.MarshalBinary()
					}
				}
				// Determine if this UPDATE is a cross-partition move so we
				// can stamp the moved-partition sentinel (M0100-0005n) and route
				// the write to the correct destination partition.
				targetWriteRel := rel
				targetWriteCols := cols
				var destPartIdx *catalog.Table
				isCrossPartitionMoveIdx := false
				if imW, ok := o.ctx.Catalog.(*catalog.InMemory); ok && len(idxTbl.PartitionKey) > 0 {
					dp, dpErr := routeToPartition(idxTbl, pu.newRow, imW, o.ctx)
					if dpErr != nil {
						return nil, dpErr
					}
					if dp != nil {
						dpRel := o.ctx.Catalog.RelFileNode(dp)
						if dpRel != rel {
							isCrossPartitionMoveIdx = true
						}
						targetWriteRel = dpRel
						targetWriteCols = dp.Columns
						destPartIdx = dp
						_ = computeGeneratedColumns(dp.Columns, pu.newRow)
					}
				}
				var stampIdxErr error
				if isCrossPartitionMoveIdx {
					stampIdxErr = storage.PageSetHeapTupleMovedPartition(s.Page(), pu.slot, effectiveWriterXID(o.ctx))
				} else if oldHdrTup, herr := storage.PageGetHeapTuple(s.Page(), pu.slot); herr == nil {
					// Preserve a pre-existing non-conflicting foreign locker into a
					// {updater + survivors} multi (M0118-0004 producer). Non-HOT
					// UPDATE is treated key-changing by goopg (KeysUpdated stamped
					// below), so the updater status is StatusUpdate (keysUpdated=true).
					stampIdxErr = stampUpdaterXmaxNonHOT(o.ctx, s.Page(), pu.slot, oldHdrTup.Header, true)
				} else {
					stampIdxErr = storage.PageSetHeapTupleXmax(s.Page(), pu.slot, effectiveWriterXID(o.ctx))
				}
				if stampIdxErr != nil {
					s.Unlock()
					o.ctx.Pool.Unpin(s)
					if errors.Is(stampIdxErr, storage.ErrUnsupportedItem) || errors.Is(stampIdxErr, storage.ErrInvalidSlot) {
						// Concurrent UPDATE/DELETE or opportunistic
						// prune flipped this slot out of LP_NORMAL
						// after scan-time. Skip the row.
						epqSkip = true
						break
					}
					return nil, stampIdxErr
				}
				derr := markHeapDeleteDirtyAndClearVM(o.ctx, s, rel, pu.blk, pu.slot, effectiveWriterXID(o.ctx), oldTupleBytes)
				s.Unlock()
				o.ctx.Pool.Unpin(s)
				if derr != nil {
					return nil, derr
				}
				// Check unique constraints AFTER stamping xmax so isLiveForUniqueCheck
				// treats the old row version as dead. Mirrors the normal seqscan update path.
				// Skip indexes whose key columns are unchanged: a no-key-change UPDATE
				// cannot violate its own uniqueness, and probing would spuriously flag a
				// concurrently-updated sibling version of the same key as a live duplicate
				// (pgbench TPC-B UPDATE pgbench_tellers contention). Cross-partition moves
				// force a full probe against the destination relation.
				{
					chkTbl := idxTbl
					if destPartIdx != nil {
						chkTbl = destPartIdx
					}
					if uerr := checkUniqueIndexesForUpdate(o.ctx, chkTbl, targetWriteCols, pu.oldRow, pu.newRow, isCrossPartitionMoveIdx, o.plan.Pos()); uerr != nil {
						return nil, uerr
					}
				}
				newPtr, werr := writeHeapRowReturning(o.ctx, targetWriteRel, targetWriteCols, pu.newRow)
				if werr != nil {
					return nil, werr
				}
				if o.ctx.InDMLCTE && o.ctx.CTEWriteFence != nil {
					oldPtr := storage.ItemPointer{Block: pu.blk, Offset: pu.slot}
					if _, inFence := o.ctx.CTEWriteFence[oldPtr]; inFence {
						if o.ctx.CTENewToOld != nil {
							if orig, ok := o.ctx.CTENewToOld[oldPtr]; ok {
								if o.ctx.CTESelfModifiedErrors == nil {
									o.ctx.CTESelfModifiedErrors = make(map[storage.ItemPointer]struct{})
								}
								o.ctx.CTESelfModifiedErrors[orig] = struct{}{}
							}
						}
					}
					o.ctx.CTEWriteFence[newPtr] = struct{}{}
					if o.ctx.CTENewToOld != nil {
						o.ctx.CTENewToOld[newPtr] = oldPtr
					}
				}
				// Maintain unique/PK btree entries for the new row version.
				if destPartIdx != nil {
					maintainUniqueIndexesForInsert(o.ctx, destPartIdx, targetWriteCols, pu.newRow, newPtr)
				} else {
					maintainUniqueIndexesForInsert(o.ctx, idxTbl, cols, pu.newRow, newPtr)
				}
				// M0100-0005z: link old tuple to new version via t_ctid for
				// EPQ chain followers (non-cross-partition only).
				if !isCrossPartitionMoveIdx {
					if cerr := stampOldCtid(o.ctx, rel, pu.blk, pu.slot, newPtr); cerr != nil {
						return nil, cerr
					}
				}
				// Mark HeapKeysUpdated when an indexed column changed (HOT was
				// not eligible). FOR KEY SHARE uses this bit to decide whether to
				// wait on this xmax. Mirrors upstream heap_update's HEAP_KEYS_UPDATED
				// stamping via PageSetHeapTupleKeysUpdated.
				if !hotEligible && !isCrossPartitionMoveIdx {
					if s2, perr := o.ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: pu.blk}); perr == nil {
						s2.Lock()
						_ = storage.PageSetHeapTupleKeysUpdated(s2.Page(), pu.slot)
						s2.Unlock()
						o.ctx.Pool.Unpin(s2)
					}
				}
				// Emit canonical WAL for this UPDATE.
				if o.ctx.LogCanonical != nil {
					if derr := emitCanonicalHeapDelete(o.ctx, rel, pu.blk, pu.slot); derr != nil {
						return nil, derr
					}
					if ierr := emitCanonicalHeapInsert(o.ctx, targetWriteRel, newPtr); ierr != nil {
						return nil, ierr
					}
				}
				break
			}
		}
		if !epqSkip {
			// M0118-0001: SSI write-path hook on the prior live slot, mirroring
			// the seqscan-based updateOp path (operators_storage.go ~3210). The
			// index-based UPDATE path previously skipped this, so a SERIALIZABLE
			// UPDATE driven by a PRIMARY KEY / index lookup never installed the
			// rw-conflict-in edge from concurrent SIREAD holders — a sibling-path
			// gap that made total-cash's `wy2`-before-`rxy1` permutations miss the
			// dangerous structure. The conflict target is the OLD tuple's
			// (rel, blk, slot), the same slot a concurrent reader predicate-locks.
			if serr := ssiRecordTupleWrite(o.ctx, rel, pu.blk, pu.slot); serr != nil {
				return nil, serr
			}
			// AFTER UPDATE triggers (M0097-0140).
			if len(idxTbl.Triggers) > 0 {
				fireTriggers(o.ctx, idxTbl, "after", "update", pu.oldRow, pu.newRow)
			}
			o.appendUpdateRetRow(pu.newRow)
			o.rowsAffected++
		}
	}
	// M0100-0005: yield first RETURNING row inline; subsequent rows
	// are iterated by the o.done branch in updateOp.Next().
	if o.retIdx < len(o.retRows) {
		row := o.retRows[o.retIdx]
		o.retIdx++
		return SlotFromRow(o.plan.ReturningSchema, row), nil
	}
	return nil, EOF
}

func (o *updateOp) Next() (TupleSlot, error) {
	if o.done {
		// Subsequent calls: iterate through RETURNING rows (M0100-0005).
		if o.retIdx >= len(o.retRows) {
			return nil, EOF
		}
		row := o.retRows[o.retIdx]
		o.retIdx++
		return SlotFromRow(o.plan.ReturningSchema, row), nil
	}
	o.done = true
	// M0093: UPDATE is unconditionally a write — materialise the
	// transaction's XID before the scan so foreignLockOnly /
	// isConcurrentlyUpdated / tuple-lock acquisition see the real
	// XID (zero would cause false-negative race detection and
	// would mis-classify our own locks as foreign).
	if err := o.ctx.MaterializeWriterXID(); err != nil {
		return nil, err
	}
	// Self-modification error setup: if a sub-command during the CTE phase
	// already modified a row we are about to update, scanMatching will raise
	// errTupleAlreadyModifiedByUpdate when it encounters the original tuple.
	if o.ctx.CTESelfModifiedErrors != nil {
		o.ctx.CTESelfModErr = errTupleAlreadyModifiedByUpdate
		defer func() { o.ctx.CTESelfModErr = nil }()
	}
	tbl := o.plan.Table
	cols := tbl.Columns
	rel := o.ctx.Catalog.RelFileNode(tbl)

	// UPDATE … FROM: nested-loop cross-product path. M0097-0065.
	if len(o.plan.FromScans) > 0 {
		return o.updateWithFrom(rel, cols)
	}

	// Use IndexScan (B-tree) when available — O(log n) instead of O(n).
	if o.idxScan != nil {
		return o.updateViaIndex(rel, cols)
	}

	// Fallback: full SeqScan path.

	// Two passes: first collect (block, slot, newRow) tuples to
	// rewrite, then issue the writes. Doing the writes in-line during
	// the scan would re-encounter our own newly inserted tuples on
	// later pages — pgbench's UPDATE-then-SELECT-self pattern would
	// loop forever. The two-pass approach trades a bit of memory for
	// straightforward iteration semantics.
	type pendingUpdate struct {
		rel            storage.RelFileNode
		blk            storage.BlockNumber
		slot           uint16
		cols           []catalog.Column // columns of the source relation
		newRow         Row
		retRow         Row            // parent-aligned row for RETURNING (nil = use newRow); M0097-0078
		inheritColMap  []int          // parent→child col mapping; non-nil only for inherit/partition children
		oldRow         Row            // for BEFORE UPDATE trigger firing
		scanTbl        *catalog.Table // table the row came from (M0100-0005o: partition-child triggers)
		beforeFired    bool           // BEFORE trigger already fired in Phase 1 (M0100-0011)
	}
	pending := make([]pendingUpdate, 0, 1)

	// Scan parent + partition/inheritance children. M0096-0013.
	// For inheritance children, track a column-map so SET/WHERE/RETURNING
	// expressions (resolved against parent ordinals) work on child rows. M0097-0078.
	updateScanTables := []*catalog.Table{tbl}
	var inheritChildOIDs map[uint32]bool
	if imU, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
		updateScanTables = append(updateScanTables, imU.PartitionChildren(tbl.OID)...)
		inheritChildren := imU.InheritanceChildren(tbl.OID)
		updateScanTables = append(updateScanTables, inheritChildren...)
		if len(inheritChildren) > 0 {
			inheritChildOIDs = make(map[uint32]bool, len(inheritChildren))
			for _, ic := range inheritChildren {
				inheritChildOIDs[ic.OID] = true
			}
		}
	}
	for _, scanTbl := range updateScanTables {
		scanRel := o.ctx.Catalog.RelFileNode(scanTbl)
		scanCols := scanTbl.Columns
		if scanTbl != tbl {
			if err := o.ctx.acquireRelLock(scanRel, lockmgr.RowExclusiveLock); err != nil {
				return nil, err
			}
		}
		captureRel := scanRel
		captureCols := scanCols
		isInheritChild := inheritChildOIDs != nil && inheritChildOIDs[scanTbl.OID]
		// For inheritance children, pass nil predicate to scanMatching and apply
		// it manually after remapping the child row to parent ordinals. M0097-0078.
		var inheritColMap []int
		if isInheritChild {
			inheritColMap = buildInheritColMap(cols, captureCols)
		}
		scanPred := o.pred
		if isInheritChild {
			scanPred = nil
		}
		if err := scanMatching(o.ctx, scanRel, scanCols, scanPred, func(blk storage.BlockNumber, slot uint16, row Row) error {
			// Clear multi-column subquery cache so each row gets a fresh evaluation.
			clear(o.ctx.MultiAssignSubqCache)
			evalRow := row
			if isInheritChild {
				evalRow = remapChildRowToParent(row, inheritColMap)
				if o.pred != nil {
					v, _ := evalExpr(o.pred, evalRow, o.ctx)
					if v.IsNull() || v.Kind != KindBool || !v.BoolValue() {
						return nil
					}
				}
			}
			var newRow Row
			if isInheritChild {
				// Evaluate SET exprs in parent column space, then map back to child.
				parentNewRow := make(Row, len(cols))
				for pi := range cols {
					if pi < len(o.plan.Set) && o.plan.Set[pi] != nil {
						v, err := evalExpr(o.plan.Set[pi], evalRow, o.ctx)
						if err != nil {
							return err
						}
						parentNewRow[pi] = v
					} else {
						parentNewRow[pi] = evalRow[pi]
					}
				}
				newRow = remapParentRowToChild(parentNewRow, row, cols, captureCols)
			} else {
				nCols := len(captureCols)
				newRow = make(Row, nCols)
				for i := range captureCols {
					setIdx := i
					if setIdx < len(o.plan.Set) && o.plan.Set[setIdx] != nil {
						v, err := evalExpr(o.plan.Set[setIdx], row, o.ctx)
						if err != nil {
							return err
						}
						newRow[i] = v
					} else {
						if i < len(row) {
							newRow[i] = row[i]
						}
					}
				}
			}
			_ = computeGeneratedColumns(captureCols, newRow)

			// M0100-0011: Phase 1 EPQ for all isolation levels — wait for any
			// in-progress xmax before processing the next row, so BEFORE trigger
			// and subsequent-row NOTICEs interleave correctly (PG per-row order).
			// RC: follow HOT chain after concurrent commit. RR/SSI: raise 40001.
			writeBlk, writeSlot := blk, slot
			oldRow := cloneRow(evalRow)
			beforeFiredP1 := false
			if !isInheritChild {
				for epqRetry := 0; ; epqRetry++ {
					s, perr := o.ctx.Pool.Pin(storage.BufferTag{Rel: captureRel, Block: writeBlk})
					if perr != nil {
						return perr
					}
					s.Lock()
					tupHdr, tErr := storage.PageGetHeapTuple(s.Page(), writeSlot)
					noConflict := tErr != nil || !isConcurrentlyUpdated(tupHdr.Header, o.ctx.Tx.XID, &o.ctx.Snap, o.ctx.MultiXact)
					xmax := concurrentModifierXID(tupHdr.Header, o.ctx.MultiXact)
					s.Unlock()
					o.ctx.Pool.Unpin(s)
					if noConflict {
						break
					}
					// Save origSnap before epqWait: epqWait refreshes ctx.Snap, so
					// origSnap must be captured here to hold the query-start snapshot.
					// epqFollowHOT/Chain use it to evaluate sub-plan quals (EXISTS,
					// scalar subqueries) with the original snapshot, matching PG's
					// EvalPlanQual semantics.
					origSnap := o.ctx.Snap
					if epqRetry >= epqRetryLimit(o.ctx.Tx.Isolation) {
						return &ExecError{Code: "40001", Pos: o.plan.Pos(),
							Message: "could not serialize access due to concurrent update"}
					}
					if epqWait(o.ctx, xmax) {
						return &ExecError{Code: "40001", Pos: o.plan.Pos(),
							Message: "could not serialize access due to concurrent update (deadlock)"}
					}
					visible, _ := epqRecheckVisible(o.ctx, captureRel, writeBlk, writeSlot)
					if visible {
						if !o.ctx.Snap.HasInProgress(xmax) {
							break // xmax aborted; row unchanged
						}
						// RR/SSI: snapshot is frozen at BEGIN; HasInProgress may still
						// return true for a xmax that has since settled globally.
						if o.ctx.TxnMgr != nil {
							if o.ctx.TxnMgr.HasAbortedXID(xmax) {
								break // xmax globally aborted; row unchanged
							}
							if !o.ctx.TxnMgr.IsXIDActive(xmax) {
								// xmax committed; frozen snapshot is stale. Raise 40001.
								errMsg := "could not serialize access due to concurrent update"
								if sp, perr := o.ctx.Pool.Pin(storage.BufferTag{Rel: captureRel, Block: writeBlk}); perr == nil {
									sp.RLock()
									if ot, gerr := storage.PageGetHeapTuple(sp.Page(), writeSlot); gerr == nil {
										ctid := ot.Header.CTID
										// goopg initial CTID is {InvalidBlockNumber,0}; stampOldCtid
										// only runs on UPDATE. InvalidBlockNumber ⇒ deleted.
										if ctid.Block == storage.InvalidBlockNumber {
											errMsg = "could not serialize access due to concurrent delete"
										}
									}
									sp.RUnlock()
									o.ctx.Pool.Unpin(sp)
								}
								return &ExecError{Code: "40001", Pos: o.plan.Pos(), Message: errMsg}
							}
						}
						continue
					}
					// Concurrent tx committed (visible=false via fresh RC snapshot).
					if o.ctx.Tx.Isolation != mvcc.IsolationReadCommitted {
						// Distinguish update vs delete: goopg initial CTID is
						// {InvalidBlockNumber,0}; stampOldCtid only runs on UPDATE.
						errMsg := "could not serialize access due to concurrent update"
						if sp, perr := o.ctx.Pool.Pin(storage.BufferTag{Rel: captureRel, Block: writeBlk}); perr == nil {
							sp.RLock()
							if ot, gerr := storage.PageGetHeapTuple(sp.Page(), writeSlot); gerr == nil {
								ctid := ot.Header.CTID
								if ctid.Block == storage.InvalidBlockNumber {
									errMsg = "could not serialize access due to concurrent delete"
								}
							}
							sp.RUnlock()
							o.ctx.Pool.Unpin(sp)
						}
						return &ExecError{Code: "40001", Pos: o.plan.Pos(), Message: errMsg}
					}
					// RC: follow HOT chain and re-evaluate WHERE + SET.
					if epqSlotMovedToAnotherPartition(o.ctx, captureRel, writeBlk, writeSlot) {
						return errMovedToAnotherPartition(o.plan.Pos())
					}
					newBlk := writeBlk
					newSlot, baseRow, hotFound, predOk := epqFollowHOT(o.ctx, captureRel, writeBlk, writeSlot, captureCols, o.pred, &origSnap)
					found := predOk
					if !found && !hotFound {
						if cBlk, cSlot, cRow, cFound := epqFollowChain(o.ctx, captureRel, writeBlk, writeSlot, captureCols, o.pred, &origSnap); cFound {
							newBlk, newSlot, baseRow, found = cBlk, cSlot, cRow, true
						}
					}
					if !found {
						return nil // row deleted by concurrent tx
					}
					clear(o.ctx.MultiAssignSubqCache)
					for i := range captureCols {
						if i < len(o.plan.Set) && o.plan.Set[i] != nil {
							v, e := evalExpr(o.plan.Set[i], baseRow, o.ctx)
							if e != nil {
								return e
							}
							newRow[i] = v
						} else {
							if i < len(baseRow) {
								newRow[i] = baseRow[i]
							}
						}
					}
					_ = computeGeneratedColumns(captureCols, newRow)
					oldRow = cloneRow(baseRow)
					writeBlk, writeSlot = newBlk, newSlot
					continue
				}
				if len(scanTbl.Triggers) > 0 {
					ret, ok := fireTriggers(o.ctx, scanTbl, "before", "update", oldRow, newRow)
					if !ok {
						return nil // RETURN NULL — skip row
					}
					newRow = ret
				}
				beforeFiredP1 = true
			}

			// Parent-aligned retRow so RETURNING exprs (parent ordinals) evaluate correctly.
			var retRow Row
			var storedColMap []int
			if isInheritChild && len(o.plan.Returning) > 0 {
				retRow = remapChildRowToParent(newRow, inheritColMap)
				storedColMap = inheritColMap
			}
			pending = append(pending, pendingUpdate{
				rel: captureRel, blk: writeBlk, slot: writeSlot, cols: captureCols,
				newRow: newRow, retRow: retRow, inheritColMap: storedColMap, oldRow: oldRow,
				scanTbl: scanTbl, beforeFired: beforeFiredP1,
			})
			return nil
		}); err != nil {
			return nil, err
		}
	}
	hotEligibleSeq := hotUpdateEligible(o.plan, o.ctx)
	// M0118-0003 (write-path half): classify this UPDATE for the row-lock wait
	// exactly as updateViaIndex does (key-column change → StatusUpdate, else
	// StatusNoKeyUpdate), so a propagated FOR KEY SHARE lock blocks a key-UPDATE
	// but not a no-key UPDATE — the same hotEligible signal that stamps
	// HEAP_KEYS_UPDATED. INCLUDE columns are excluded from a covering index's
	// key list, so a no-key UPDATE stays no-key here.
	updReqStatusSeq := multixact.StatusNoKeyUpdate
	if !hotEligibleSeq {
		updReqStatusSeq = multixact.StatusUpdate
	}
	for _, pu := range pending {
		// Honour a row lock propagated forward onto this live version by
		// heap_lock_updated_tuple before writing: wait for every still-active
		// conflicting foreign holder. A lock-only xmax does not relocate the
		// row, so pu.blk/pu.slot stay valid.
		puWaitRel := pu.rel
		if puWaitRel == (storage.RelFileNode{}) {
			puWaitRel = rel
		}
		if err := waitForConflictingRowLock(o.ctx, puWaitRel, pu.blk, pu.slot, updReqStatusSeq, o.plan.Pos()); err != nil {
			return nil, err
		}
		// Fire BEFORE UPDATE triggers (e.g. RAISE NOTICE) before writing.
		// M0100-0005o: when the row came from a partition/inheritance child,
		// fire that child's triggers — partition-key-update-1.spec defines
		// `footrg_mod_a` on `footrg1`, not on the parent `footrg`.
		scanTblForTrig := pu.scanTbl
		if scanTblForTrig == nil {
			scanTblForTrig = tbl
		}
		if !pu.beforeFired && len(scanTblForTrig.Triggers) > 0 {
			retRow, ok := fireTriggers(o.ctx, scanTblForTrig, "before", "update", pu.oldRow, pu.newRow)
			if !ok {
				continue // RETURN NULL — skip this row
			}
			pu.newRow = retRow
		}
		puRel := pu.rel
		if puRel == (storage.RelFileNode{}) {
			puRel = rel
		}
		puCols := pu.cols
		if puCols == nil {
			puCols = cols
		}
		used := false
		if hotEligibleSeq && puRel == rel {
			var err error
			used, err = tryApplyHOTUpdate(o.ctx, rel, cols, pu.blk, pu.slot, pu.newRow)
			if err != nil {
				return nil, err
			}
		}
		epqSkipSeq := false
		if !used {
			// EvalPlanQual retry loop (M0098-0004).
			epqDoUpdateSeq := false // abort-confirmed bypass flag
			for epqRetry := 0; ; epqRetry++ {
				s, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: puRel, Block: pu.blk})
				if err != nil {
					return nil, err
				}
				s.Lock()
				oldTup, oldGerr := storage.PageGetHeapTuple(s.Page(), pu.slot)
				if !epqDoUpdateSeq && oldGerr == nil && isConcurrentlyUpdated(oldTup.Header, o.ctx.Tx.XID, &o.ctx.Snap, o.ctx.MultiXact) {
					xmax := concurrentModifierXID(oldTup.Header, o.ctx.MultiXact)
					s.Unlock()
					o.ctx.Pool.Unpin(s)
					if epqRetry >= epqRetryLimit(o.ctx.Tx.Isolation) {
						return nil, &ExecError{
							Code:    "40001",
							Pos:     o.plan.Pos(),
							Message: "could not serialize access due to concurrent update",
						}
					}
					if epqWait(o.ctx, xmax) {
						return nil, &ExecError{
							Code:    "40001",
							Pos:     o.plan.Pos(),
							Message: "could not serialize access due to concurrent update (deadlock)",
						}
					}
					// M0100-0004: EPQ chain-following for RC; 40001 for RR.
					visible, _ := epqRecheckVisible(o.ctx, puRel, pu.blk, pu.slot)
					if visible {
						// xmax aborted; row still exists.
						if !o.ctx.Snap.HasInProgress(xmax) {
							epqDoUpdateSeq = true
							continue // bypass EPQ on next iter; update code executes
						}
						// M0100-0011: RR/Serializable firstSnap is frozen at BEGIN;
						// HasInProgress may still return true for an XID that has
						// since aborted. Confirm via the manager's global abort list.
						if o.ctx.TxnMgr != nil && o.ctx.TxnMgr.HasAbortedXID(xmax) {
							epqDoUpdateSeq = true
							continue
						}
						continue // still in-progress; retry
					}
					// Concurrent tx committed — row was updated or deleted.
					if o.ctx.Tx.Isolation != mvcc.IsolationReadCommitted {
						// Distinguish UPDATE vs DELETE: goopg initial CTID is
						// {InvalidBlockNumber,0}; stampOldCtid only runs on UPDATE.
						errMsg := "could not serialize access due to concurrent update"
						if sp, perr := o.ctx.Pool.Pin(storage.BufferTag{Rel: puRel, Block: pu.blk}); perr == nil {
							sp.RLock()
							if ot, gerr := storage.PageGetHeapTuple(sp.Page(), pu.slot); gerr == nil {
								ctid := ot.Header.CTID
								if ctid.Block == storage.InvalidBlockNumber {
									errMsg = "could not serialize access due to concurrent delete"
								}
							}
							sp.RUnlock()
							o.ctx.Pool.Unpin(sp)
						}
						return nil, &ExecError{Code: "40001", Pos: o.plan.Pos(),
							Message: errMsg}
					}
					// RC: follow HOT chain and re-evaluate WHERE + SET.
					// Cross-partition UPDATE sentinel check (see comment in the
					// idxScan EPQ branch).
					if epqSlotMovedToAnotherPartition(o.ctx, puRel, pu.blk, pu.slot) {
						return nil, errMovedToAnotherPartition(o.plan.Pos())
					}
					newBlk := pu.blk
					newSlot, baseRow, hotFound, predOk := epqFollowHOT(o.ctx, puRel, pu.blk, pu.slot, puCols, o.pred, nil)
					chainFound := predOk
					if !chainFound && !hotFound {
						// Non-HOT cross-page chain (M0100-0005z): updates that
						// land on a different page leave no HeapHotUpdated bit;
						// followHOTChain terminates immediately. Walk the raw
						// t_ctid chain instead.
						if cBlk, cSlot, cRow, cFound := epqFollowChain(o.ctx, puRel, pu.blk, pu.slot, puCols, o.pred, nil); cFound {
							newBlk, newSlot, baseRow, chainFound = cBlk, cSlot, cRow, true
						}
					}
					if !chainFound {
						epqSkipSeq = true
						break
					}
					// Re-bind SET expressions against the latest row.
					clear(o.ctx.MultiAssignSubqCache)
					for i := range puCols {
						setIdx := i
						if setIdx < len(o.plan.Set) && o.plan.Set[setIdx] != nil {
							v, e := evalExpr(o.plan.Set[setIdx], baseRow, o.ctx)
							if e != nil {
								return nil, e
							}
							pu.newRow[i] = v
						} else {
							if i < len(baseRow) {
								pu.newRow[i] = baseRow[i]
							}
						}
					}
					_ = computeGeneratedColumns(puCols, pu.newRow)
					// M0100-0005aa: refresh OLD with the EPQ-refetched row so any
					// BEFORE DELETE trigger fired later (cross-partition move) sees
					// the concurrent updater's changes — partition-key-update-4.spec
					// perm 2's BEFORE DELETE trigger reads OLD.b and inserts it into
					// triglog; without the refresh OLD still reflects the row as it
					// looked at scan-time, before s2's update2.
					pu.oldRow = cloneRow(baseRow)
					// Refresh the parent-aligned retRow so RETURNING reflects the
					// EPQ-rechecked values rather than the stale scan-time snapshot.
					if pu.retRow != nil && pu.inheritColMap != nil {
						pu.retRow = remapChildRowToParent(pu.newRow, pu.inheritColMap)
					}
					pu.blk = newBlk
					pu.slot = newSlot
					continue // re-run loop to stamp xmax on new slot
				}
				var oldTupleBytes []byte
				if oldGerr == nil {
					oldTupleBytes, _ = oldTup.MarshalBinary()
				}
				// For partition key UPDATE: route new row to correct partition.
				// Compute the destination FIRST so we know whether to stamp
				// the moved-partition sentinel on the old slot.
				targetWriteRel := puRel
				targetWriteCols := puCols
				var destPart *catalog.Table
				isCrossPartitionMove := false
				if imW, ok := o.ctx.Catalog.(*catalog.InMemory); ok && len(tbl.PartitionKey) > 0 {
					var routeErr error
					destPart, routeErr = routeToPartition(tbl, pu.newRow, imW, o.ctx)
					if routeErr != nil {
						return nil, routeErr
					}
					if destPart != nil {
						destRel := o.ctx.Catalog.RelFileNode(destPart)
						if destRel != puRel {
							isCrossPartitionMove = true
						}
						targetWriteRel = destRel
						targetWriteCols = destPart.Columns
						_ = computeGeneratedColumns(destPart.Columns, pu.newRow)
					}
				}
				// M0100-0005aa: cross-partition UPDATE = DELETE + INSERT internally,
				// so BEFORE DELETE triggers on the source partition must fire
				// (matches upstream ExecCrossPartitionUpdate -> ExecDelete).
				// partition-key-update-4.spec perm 2 has a BEFORE DELETE on the
				// source leaf footrg1 that records OLD into triglog.  Fires AFTER
				// the EPQ refetch so OLD reflects the concurrent updater's
				// committed changes.
				if isCrossPartitionMove && pu.scanTbl != nil && len(pu.scanTbl.Triggers) > 0 {
					_, ok := fireTriggers(o.ctx, pu.scanTbl, "before", "delete", pu.oldRow, nil)
					if !ok {
						// RETURN NULL — suppress the row.
						s.Unlock()
						o.ctx.Pool.Unpin(s)
						epqSkipSeq = true
						break
					}
				}
				var stampErr error
				if isCrossPartitionMove {
					stampErr = storage.PageSetHeapTupleMovedPartition(s.Page(), pu.slot, effectiveWriterXID(o.ctx))
				} else if oldHdrTup, herr := storage.PageGetHeapTuple(s.Page(), pu.slot); herr == nil {
					// Preserve a pre-existing non-conflicting foreign locker into a
					// {updater + survivors} multi (M0118-0004 producer). Non-HOT
					// UPDATE is key-changing in goopg, so keysUpdated=true.
					stampErr = stampUpdaterXmaxNonHOT(o.ctx, s.Page(), pu.slot, oldHdrTup.Header, true)
				} else {
					stampErr = storage.PageSetHeapTupleXmax(s.Page(), pu.slot, effectiveWriterXID(o.ctx))
				}
				if stampErr != nil {
					s.Unlock()
					o.ctx.Pool.Unpin(s)
					if errors.Is(stampErr, storage.ErrUnsupportedItem) {
						continue
					}
					return nil, stampErr
				}
				derr := markHeapDeleteDirtyAndClearVM(o.ctx, s, puRel, pu.blk, pu.slot, effectiveWriterXID(o.ctx), oldTupleBytes)
				s.Unlock()
				o.ctx.Pool.Unpin(s)
				if derr != nil {
					return nil, derr
				}
				// Check unique constraints on the new row AFTER stamping xmax on the
				// old tuple (so isLiveForUniqueCheck treats the old version as dead).
				// Skip indexes whose key columns are unchanged: a no-key-change UPDATE
				// cannot violate its own uniqueness, and probing would spuriously flag a
				// concurrently-updated sibling version of the same key as a live duplicate
				// (pgbench TPC-B UPDATE pgbench_tellers contention). Cross-partition moves
				// force a full probe against the destination relation.
				{
					chkTbl := pu.scanTbl
					if chkTbl == nil {
						chkTbl = tbl
					}
					if destPart != nil {
						chkTbl = destPart
					}
					if uerr := checkUniqueIndexesForUpdate(o.ctx, chkTbl, targetWriteCols, pu.oldRow, pu.newRow, isCrossPartitionMove, o.plan.Pos()); uerr != nil {
						return nil, uerr
					}
				}
				// M0100-0005t: capture the new ItemPointer so we can maintain the
				// destination partition's unique/PK indexes after a cross-partition
				// (or in-place partition) UPDATE.  Without this, ON CONFLICT
				// arbiters and the runtime unique-constraint check both miss the
				// freshly-moved row on the destination leaf.
				newPtr, werr := writeHeapRowReturning(o.ctx, targetWriteRel, targetWriteCols, pu.newRow)
				if werr != nil {
					return nil, werr
				}
				if o.ctx.InDMLCTE && o.ctx.CTEWriteFence != nil {
					oldPtr := storage.ItemPointer{Block: pu.blk, Offset: pu.slot}
					if _, inFence := o.ctx.CTEWriteFence[oldPtr]; inFence {
						if o.ctx.CTENewToOld != nil {
							if orig, ok := o.ctx.CTENewToOld[oldPtr]; ok {
								if o.ctx.CTESelfModifiedErrors == nil {
									o.ctx.CTESelfModifiedErrors = make(map[storage.ItemPointer]struct{})
								}
								o.ctx.CTESelfModifiedErrors[orig] = struct{}{}
							}
						}
					}
					o.ctx.CTEWriteFence[newPtr] = struct{}{}
					if o.ctx.CTENewToOld != nil {
						o.ctx.CTENewToOld[newPtr] = oldPtr
					}
				}
				if destPart != nil {
					maintainUniqueIndexesForInsert(o.ctx, destPart, targetWriteCols, pu.newRow, newPtr)
				} else if pu.scanTbl != nil {
					// Non-partition in-place UPDATE: maintain unique/PK btree entries for
					// the new row version. Enables ON CONFLICT arbiters and unique-constraint
					// checks to find the live committed row after a concurrent UPDATE.
					maintainUniqueIndexesForInsert(o.ctx, pu.scanTbl, targetWriteCols, pu.newRow, newPtr)
				}
				// M0100-0005z: link the old tuple to the new version via t_ctid
				// for in-place (non-cross-partition) updates so EPQ chain
				// followers can locate the latest version. Cross-partition
				// moves stamp a sentinel into t_ctid above and must not be
				// overwritten here.
				if !isCrossPartitionMove {
					if cerr := stampOldCtid(o.ctx, puRel, pu.blk, pu.slot, newPtr); cerr != nil {
						return nil, cerr
					}
				}
				// Emit canonical WAL for this UPDATE: DELETE of old page
				// (post xmax+ctid stamp) then INSERT of new page.
				if o.ctx.LogCanonical != nil {
					if derr := emitCanonicalHeapDelete(o.ctx, puRel, pu.blk, pu.slot); derr != nil {
						return nil, derr
					}
					if ierr := emitCanonicalHeapInsert(o.ctx, targetWriteRel, newPtr); ierr != nil {
						return nil, ierr
					}
				}
				break // success — exit epq retry loop
			} // end epq retry loop
		} // end if !used
		if !epqSkipSeq {
			// M0104-0007: SSI write-path hook on the prior live slot of the
			// updated tuple. Covers both HOT-update (in-place new version,
			// xmax stamped on old) and non-HOT (xmax stamp + writeHeapRow)
			// paths — the rw-conflict target is the SLOT that any concurrent
			// SERIALIZABLE reader would have predicate-locked.
			if serr := ssiRecordTupleWrite(o.ctx, puRel, pu.blk, pu.slot); serr != nil {
				return nil, serr
			}
			// AFTER UPDATE triggers (M0097-0140).
			scanTblForAfterTrig := pu.scanTbl
			if scanTblForAfterTrig == nil {
				scanTblForAfterTrig = tbl
			}
			if len(scanTblForAfterTrig.Triggers) > 0 {
				fireTriggers(o.ctx, scanTblForAfterTrig, "after", "update", pu.oldRow, pu.newRow)
			}
			// Use parent-aligned retRow for RETURNING when available (inheritance
			// children store a remapped row so RETURNING exprs work correctly). M0097-0078.
			if pu.retRow != nil {
				o.appendUpdateRetRow(pu.retRow)
			} else {
				o.appendUpdateRetRow(pu.newRow)
			}
			o.rowsAffected++
		}
	}
	// M0100-0005: yield first RETURNING row inline.
	if o.retIdx < len(o.retRows) {
		row := o.retRows[o.retIdx]
		o.retIdx++
		return SlotFromRow(o.plan.ReturningSchema, row), nil
	}
	return nil, EOF
}

// deleteOp scans the target relation and stamps xmax on visible
// matching tuples. v0 doesn't reclaim space here — VACUUM does that.
type deleteOp struct {
	plan         *planner.Delete
	scan         *planner.SeqScan
	pred         planner.Expr
	ctx          *Context
	rowsAffected int64
	done         bool
	idxScan      *planner.IndexScan
	retRows      []Row
	retIdx       int
}

// RowsAffected satisfies executor.RowCounter.
func (o *deleteOp) RowsAffected() int64 { return o.rowsAffected }

func newDeleteOp(p *planner.Delete) (*deleteOp, error) {
	scan, pred, idxScan, err := extractScan(p.Child)
	if err != nil {
		return nil, err
	}
	return &deleteOp{plan: p, scan: scan, pred: pred, idxScan: idxScan}, nil
}

func (o *deleteOp) Schema() planner.Schema { return o.plan.ReturningSchema }

// appendDeleteRetRow evaluates RETURNING expressions against the old row
// (before deletion) and appends to o.retRows (M0100-0005).
func (o *deleteOp) appendDeleteRetRow(oldRow Row) {
	o.appendDeleteRetRowWithUsing(oldRow, nil)
}

// appendDeleteRetRowWithUsing is the DELETE … USING variant (M0097-0076):
// RETURNING expressions may reference columns from joined USING tables.
// The planner resolves those references with column indices that follow
// the target columns, so we build a combined eval row
// `[oldRow..., usingPortion...]` to satisfy them. usingPortion may be
// nil for the plain DELETE path.
func (o *deleteOp) appendDeleteRetRowWithUsing(oldRow Row, usingPortion Row) {
	if len(o.plan.Returning) == 0 {
		return
	}
	evalRow := oldRow
	if len(usingPortion) > 0 {
		evalRow = make(Row, 0, len(oldRow)+len(usingPortion))
		evalRow = append(evalRow, oldRow...)
		evalRow = append(evalRow, usingPortion...)
	}
	retRow := make(Row, len(o.plan.Returning))
	for i, expr := range o.plan.Returning {
		v, _ := evalExpr(expr, evalRow, o.ctx)
		retRow[i] = v
	}
	o.retRows = append(o.retRows, retRow)
}

func (o *deleteOp) Open(ctx *Context) error {
	if ctx.Pool == nil || ctx.Catalog == nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "Delete requires storage handles in Context"}
	}
	o.ctx = ctx
	rel := ctx.Catalog.RelFileNode(o.plan.Table)
	if err := ctx.acquireRelLock(rel, lockmgr.RowExclusiveLock); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.plan.Pos()
		}
		return err
	}
	return nil
}

func (o *deleteOp) Close() error { return nil }

func (o *deleteOp) Next() (TupleSlot, error) {
	if o.done {
		// Subsequent calls: iterate RETURNING rows (M0100-0005).
		if o.retIdx >= len(o.retRows) {
			return nil, EOF
		}
		row := o.retRows[o.retIdx]
		o.retIdx++
		return SlotFromRow(o.plan.ReturningSchema, row), nil
	}
	// If a CTE sub-command already modified a row we are about to delete,
	// scanMatching must surface the 09000 error (TM_SelfModified equivalent).
	if o.ctx.CTESelfModifiedErrors != nil {
		o.ctx.CTESelfModErr = errTupleAlreadyModifiedByDelete
		defer func() { o.ctx.CTESelfModErr = nil }()
	}
	// DELETE … USING: nested-loop cross-product path. M0097-0076.
	if len(o.plan.UsingScans) > 0 {
		return o.deleteWithUsing()
	}
	o.done = true
	// M0093: DELETE is unconditionally a write — materialise the
	// transaction's XID before the scan so foreign-lock checks see
	// the real XID.
	if err := o.ctx.MaterializeWriterXID(); err != nil {
		return nil, err
	}
	tbl := o.plan.Table
	rel := o.ctx.Catalog.RelFileNode(tbl)

	type victim struct {
		rel         storage.RelFileNode
		blk         storage.BlockNumber
		slot        uint16
		row         Row
		retRow      Row              // parent-aligned row for RETURNING (nil = use row); M0097-0078
		cols        []catalog.Column // for EPQ chain-following (M0100-0004)
		scanTbl     *catalog.Table   // table the row came from (M0100-0005o: partition-child triggers)
		beforeFired bool             // BEFORE trigger already fired in Phase 1 (M0100-0011)
	}
	// Collect victims from parent + partition/inheritance children. M0096-0013.
	// For inheritance children, remap rows to parent ordinals for predicate/RETURNING. M0097-0078.
	var victims []victim
	scanTables := []*catalog.Table{tbl}
	var delInheritChildOIDs map[uint32]bool
	if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
		scanTables = append(scanTables, im.PartitionChildren(tbl.OID)...)
		delInheritChildren := im.InheritanceChildren(tbl.OID)
		scanTables = append(scanTables, delInheritChildren...)
		if len(delInheritChildren) > 0 {
			delInheritChildOIDs = make(map[uint32]bool, len(delInheritChildren))
			for _, ic := range delInheritChildren {
				delInheritChildOIDs[ic.OID] = true
			}
		}
	}
	for _, scanTbl := range scanTables {
		scanRel := o.ctx.Catalog.RelFileNode(scanTbl)
		if scanTbl != tbl {
			if err := o.ctx.acquireRelLock(scanRel, lockmgr.RowExclusiveLock); err != nil {
				return nil, err
			}
		}
		captureRel := scanRel // capture for closure
		captureCols := scanTbl.Columns
		captureTbl := scanTbl
		isDelInheritChild := delInheritChildOIDs != nil && delInheritChildOIDs[scanTbl.OID]
		var delInheritColMap []int
		if isDelInheritChild {
			delInheritColMap = buildInheritColMap(tbl.Columns, captureCols)
		}
		delScanPred := o.pred
		if isDelInheritChild {
			delScanPred = nil // apply predicate manually after row remapping
		}
		if err := scanMatching(o.ctx, scanRel, scanTbl.Columns, delScanPred, func(blk storage.BlockNumber, slot uint16, row Row) error {
			var retRow Row
			if isDelInheritChild {
				parentAligned := remapChildRowToParent(row, delInheritColMap)
				if o.pred != nil {
					v, _ := evalExpr(o.pred, parentAligned, o.ctx)
					if v.IsNull() || v.Kind != KindBool || !v.BoolValue() {
						return nil
					}
				}
				if len(o.plan.Returning) > 0 {
					retRow = parentAligned
				}
			}
			// M0100-0011: Phase 1 inline EPQ for RC non-inheritance rows.
			// Wait for any in-progress xmax BEFORE scanMatching processes the
			// next row, so WHERE NOTICEs and trigger NOTICEs interleave per PG's
			// per-row scan semantics (key-a EPQ → key-b scan, not key-b before
			// key-a's EPQ wait).
			// M0100-0011: Phase 1 EPQ for all isolation levels — same rationale
			// as updateOp Phase 1: block on in-progress xmax before processing
			// the next row so BEFORE trigger + subsequent NOTICEs stay in order.
			deleteBlk, deleteSlot, deleteRow := blk, slot, row
			beforeFiredDel := false
			if !isDelInheritChild {
				for epqRetry := 0; ; epqRetry++ {
					s, perr := o.ctx.Pool.Pin(storage.BufferTag{Rel: captureRel, Block: deleteBlk})
					if perr != nil {
						return perr
					}
					s.Lock()
					tupHdr, tErr := storage.PageGetHeapTuple(s.Page(), deleteSlot)
					noConflict := tErr != nil || !isConcurrentlyUpdated(tupHdr.Header, o.ctx.Tx.XID, &o.ctx.Snap, o.ctx.MultiXact)
					xmax := concurrentModifierXID(tupHdr.Header, o.ctx.MultiXact)
					s.Unlock()
					o.ctx.Pool.Unpin(s)
					if noConflict {
						break
					}
					if epqRetry >= epqRetryLimit(o.ctx.Tx.Isolation) {
						return &ExecError{Code: "40001", Pos: o.plan.Pos(),
							Message: "could not serialize access due to concurrent update"}
					}
					if epqWait(o.ctx, xmax) {
						return &ExecError{Code: "40001", Pos: o.plan.Pos(),
							Message: "could not serialize access due to concurrent update (deadlock)"}
					}
					visible, _ := epqRecheckVisible(o.ctx, captureRel, deleteBlk, deleteSlot)
					if visible {
						if !o.ctx.Snap.HasInProgress(xmax) {
							break // xmax aborted; row unchanged, proceed with delete
						}
						// RR/SSI: snapshot frozen at BEGIN; check global state.
						if o.ctx.TxnMgr != nil {
							if o.ctx.TxnMgr.HasAbortedXID(xmax) {
								break // xmax globally aborted; row unchanged
							}
							if !o.ctx.TxnMgr.IsXIDActive(xmax) {
								// xmax committed; frozen snapshot is stale. Raise 40001.
								return &ExecError{Code: "40001", Pos: o.plan.Pos(),
									Message: "could not serialize access due to concurrent update"}
							}
						}
						continue
					}
					// Concurrent tx committed — row was updated or deleted.
					if o.ctx.Tx.Isolation != mvcc.IsolationReadCommitted {
						return &ExecError{Code: "40001", Pos: o.plan.Pos(),
							Message: "could not serialize access due to concurrent update"}
					}
					if epqSlotMovedToAnotherPartition(o.ctx, captureRel, deleteBlk, deleteSlot) {
						return errMovedToAnotherPartition(o.plan.Pos())
					}
					newBlk := deleteBlk
					newSlot, newRow, hotFound, predOk := epqFollowHOT(o.ctx, captureRel, deleteBlk, deleteSlot, captureCols, o.pred, nil)
					found := predOk
					if !found && !hotFound {
						if cBlk, cSlot, cRow, cFound := epqFollowChain(o.ctx, captureRel, deleteBlk, deleteSlot, captureCols, o.pred, nil); cFound {
							newBlk, newSlot, newRow, found = cBlk, cSlot, cRow, true
						}
					}
					if !found {
						return nil // row deleted (or predicate no longer matches) via concurrent tx
					}
					deleteBlk, deleteSlot, deleteRow = newBlk, newSlot, cloneRow(newRow)
					break
				}
				trigTbl := captureTbl
				if trigTbl == nil {
					trigTbl = tbl
				}
				if len(trigTbl.Triggers) > 0 {
					_, ok := fireTriggers(o.ctx, trigTbl, "before", "delete", cloneRow(deleteRow), nil)
					if !ok {
						return nil // trigger returned NULL — skip deletion
					}
				}
				beforeFiredDel = true
			}
			victims = append(victims, victim{rel: captureRel, blk: deleteBlk, slot: deleteSlot, row: cloneRow(deleteRow), retRow: retRow, cols: captureCols, scanTbl: captureTbl, beforeFired: beforeFiredDel})
			return nil
		}); err != nil {
			return nil, err
		}
	}
	// Fire BEFORE DELETE triggers and enforce FK constraints. M0096-0011/0012.
	// M0100-0005o: triggers may be defined on a partition/inheritance child
	// rather than the parent; fire the source-relation's triggers.
	filtered := victims[:0]
	for _, v := range victims {
		trigTbl := v.scanTbl
		if trigTbl == nil {
			trigTbl = tbl
		}
		if !v.beforeFired && len(trigTbl.Triggers) > 0 {
			_, ok := fireTriggers(o.ctx, trigTbl, "before", "delete", v.row, nil)
			if !ok {
				continue // trigger returned NULL — skip deletion
			}
		}
		if err := enforceFKOnDelete(o.ctx, tbl, v.row); err != nil {
			return nil, err
		}
		filtered = append(filtered, v)
	}
	victims = filtered
	for _, v := range victims {
		victimRel := v.rel
		if victimRel == (storage.RelFileNode{}) {
			victimRel = rel // fallback to parent rel
		}
		// M0118-0003 (write-path half): honour a row lock propagated forward
		// onto this live version by heap_lock_updated_tuple. A DELETE conflicts
		// with every lock strength incl. FOR KEY SHARE, so wait for every
		// still-active foreign holder before stamping our xmax. The lock-only
		// xmax does not relocate the row, so v.blk/v.slot stay valid; a genuine
		// concurrent updater is handled by the isConcurrentlyUpdated EPQ loop
		// below (this only covers pure row locks the lockmgr already released).
		if err := waitForConflictingRowLock(o.ctx, victimRel, v.blk, v.slot, multixact.StatusUpdate, o.plan.Pos()); err != nil {
			return nil, err
		}
		// EvalPlanQual retry loop (M0098-0004): on concurrent xmax conflict,
		// wait for the conflicting transaction and re-check visibility.
		epqSkipDel := false
		epqDoDelete := false // abort-confirmed bypass flag
		for epqRetry := 0; ; epqRetry++ {
			s, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: victimRel, Block: v.blk})
			if err != nil {
				return nil, err
			}
			s.Lock()
			// M0090-0002: detect concurrent xmax-stamp under the
			// exclusive Lock before our own stamp.
			oldTup, oldGerr := storage.PageGetHeapTuple(s.Page(), v.slot)
			if !epqDoDelete && oldGerr == nil && isConcurrentlyUpdated(oldTup.Header, o.ctx.Tx.XID, &o.ctx.Snap, o.ctx.MultiXact) {
				xmax := concurrentModifierXID(oldTup.Header, o.ctx.MultiXact)
				s.Unlock()
				o.ctx.Pool.Unpin(s)
				if epqRetry >= epqRetryLimit(o.ctx.Tx.Isolation) {
					return nil, &ExecError{
						Code:    "40001",
						Pos:     o.plan.Pos(),
						Message: "could not serialize access due to concurrent update",
					}
				}
				if epqWait(o.ctx, xmax) {
					return nil, &ExecError{
						Code:    "40001",
						Pos:     o.plan.Pos(),
						Message: "could not serialize access due to concurrent update (deadlock)",
					}
				}
				// M0100-0004: EPQ chain-following for RC; 40001 for RR.
				visible, _ := epqRecheckVisible(o.ctx, victimRel, v.blk, v.slot)
				if visible {
					// xmax aborted; row still exists.
					if !o.ctx.Snap.HasInProgress(xmax) {
						epqDoDelete = true
						continue // bypass EPQ on next iter; delete code executes
					}
					// M0100-0011: RR/Serializable firstSnap is frozen at BEGIN;
					// HasInProgress may still return true for an XID that has
					// since aborted. Confirm via the manager's global abort list.
					if o.ctx.TxnMgr != nil && o.ctx.TxnMgr.HasAbortedXID(xmax) {
						epqDoDelete = true
						continue
					}
					continue // still in-progress; retry
				}
				// Concurrent tx committed — row was updated or deleted.
				if o.ctx.Tx.Isolation != mvcc.IsolationReadCommitted {
					return nil, &ExecError{Code: "40001", Pos: o.plan.Pos(),
						Message: "could not serialize access due to concurrent update"}
				}
				// RC: follow HOT chain and re-evaluate WHERE.
				// Cross-partition UPDATE sentinel check: if the victim row was
				// moved to a different partition by a concurrent committed UPDATE,
				// raise the upstream "moved to another partition" error rather
				// than silently skipping the row.
				if epqSlotMovedToAnotherPartition(o.ctx, victimRel, v.blk, v.slot) {
					return nil, errMovedToAnotherPartition(o.plan.Pos())
				}
				victimCols := v.cols
				if victimCols == nil {
					victimCols = tbl.Columns
				}
				newBlk := v.blk
				newSlot, newRow, hotFound, predOk := epqFollowHOT(o.ctx, victimRel, v.blk, v.slot, victimCols, o.pred, nil)
				chainFound := predOk
				if !chainFound && !hotFound {
					// Non-HOT cross-page chain (M0100-0005z).
					if cBlk, cSlot, cRow, cFound := epqFollowChain(o.ctx, victimRel, v.blk, v.slot, victimCols, o.pred, nil); cFound {
						newBlk, newSlot, newRow, chainFound = cBlk, cSlot, cRow, true
					}
				}
				if !chainFound {
					epqSkipDel = true
					break
				}
				v.blk = newBlk
				v.slot = newSlot
				if newRow != nil {
					v.row = newRow // update for correct RETURNING after chain-follow
				}
				continue // re-run loop to stamp xmax on new slot
			}
			var oldTupleBytes []byte
			if oldGerr == nil {
				oldTupleBytes, _ = oldTup.MarshalBinary()
			}
			var delStampErr error
			if oldGerr == nil {
				// Preserve a pre-existing non-conflicting foreign locker (M0118-0004
				// producer). A DELETE is StatusUpdate (keysUpdated=true), which
				// conflicts with every lock mode, so this is a no-op unless a
				// non-conflicting locker somehow survives; wired for sibling-path
				// parity with the UPDATE delete-half.
				delStampErr = stampUpdaterXmaxNonHOT(o.ctx, s.Page(), v.slot, oldTup.Header, true)
			} else {
				delStampErr = storage.PageSetHeapTupleXmax(s.Page(), v.slot, effectiveWriterXID(o.ctx))
			}
			if err := delStampErr; err != nil {
				s.Unlock()
				o.ctx.Pool.Unpin(s)
				if errors.Is(err, storage.ErrUnsupportedItem) {
					epqSkipDel = true
					break
				}
				return nil, err
			}
			derr := markHeapDeleteDirtyAndClearVM(o.ctx, s, victimRel, v.blk, v.slot, effectiveWriterXID(o.ctx), oldTupleBytes)
			s.Unlock()
			o.ctx.Pool.Unpin(s)
			if derr != nil {
				return nil, derr
			}
			if cerr := emitCanonicalHeapDelete(o.ctx, victimRel, v.blk, v.slot); cerr != nil {
				return nil, cerr
			}
			break // success — exit epq retry loop
		} // end epq retry loop
		if !epqSkipDel {
			// M0104-0007: SSI write-path hook on the deleted tuple's slot.
			// The rw-conflict target is the slot a concurrent SERIALIZABLE
			// reader would have predicate-locked before the xmax stamp.
			if serr := ssiRecordTupleWrite(o.ctx, victimRel, v.blk, v.slot); serr != nil {
				return nil, serr
			}
			// AFTER DELETE triggers (M0097-0140).
			if len(tbl.Triggers) > 0 {
				delRow := v.row
				if v.retRow != nil {
					delRow = v.retRow
				}
				fireTriggers(o.ctx, tbl, "after", "delete", delRow, nil)
			}
			// Use parent-aligned retRow for RETURNING when available (inheritance children). M0097-0078.
			if v.retRow != nil {
				o.appendDeleteRetRow(v.retRow)
			} else {
				o.appendDeleteRetRow(v.row)
			}
			o.rowsAffected++
		}
	}
	// M0100-0005: yield first RETURNING row inline.
	if o.retIdx < len(o.retRows) {
		row := o.retRows[o.retIdx]
		o.retIdx++
		return SlotFromRow(o.plan.ReturningSchema, row), nil
	}
	return nil, EOF
}

// collectNodeRows opens the given plan node, drains it into a Row slice, then
// closes it. Used by UPDATE … FROM and DELETE … USING to materialise their
// source sets, which may be real-table SeqScans or arbitrary subquery nodes
// (including VALUES).
func collectNodeRows(node planner.Node, ctx *Context) ([]Row, error) {
	op, err := Build(node)
	if err != nil {
		return nil, err
	}
	if err := op.Open(ctx); err != nil {
		op.Close()
		return nil, err
	}
	defer op.Close()
	var rows []Row
	for {
		slot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, cloneRow(slot.Row()))
	}
	return rows, nil
}

// updateWithFrom implements UPDATE … FROM by collecting all FROM-table rows
// into memory, then doing a nested-loop cross-product against each target
// table row, applying o.plan.FromPred to find matching combinations, and
// scheduling the rewrite. M0097-0065.
func (o *updateOp) updateWithFrom(rel storage.RelFileNode, tgtCols []catalog.Column) (TupleSlot, error) {
	// Step 1: collect all rows from each FROM table.
	type fromRows struct {
		rows []Row
	}
	fromSets := make([]fromRows, len(o.plan.FromScans))
	for i, fromNode := range o.plan.FromScans {
		rows, err2 := collectNodeRows(fromNode, o.ctx)
		if err2 != nil {
			return nil, err2
		}
		fromSets[i] = fromRows{rows: rows}
	}

	// Step 2: scan target table (parent + inheritance children) without predicate;
	// for each target row, cross-product with FROM-table rows. M0097-0065, M0097-0078.
	type pendingUpdate struct {
		srcRel  storage.RelFileNode // source relation — xmax is stamped here
		rel     storage.RelFileNode // destination relation — new row is written here
		tgtCols []catalog.Column   // columns of destination relation
		tbl     *catalog.Table     // catalog table for source relation (nil = parent)
		blk     storage.BlockNumber
		slot    uint16
		newRow  Row
		retNewRow   Row // parent-aligned new row for RETURNING (nil = use newRow); M0097-0078
		oldRow      Row
		fromPortion Row // joined FROM-table columns for RETURNING; nil when RETURNING absent
	}
	var pending []pendingUpdate
	tgtColCount := len(tgtCols)
	needFromForReturning := len(o.plan.Returning) > 0

	// Collect inheritance children and partition children for the FROM target scan.
	// Partition children share the parent's column ordinals (no remapping), but may
	// have overridden GeneratedExpr values — use child.Columns for generated-column
	// recomputation. M0097-0078, M0100-0010.
	type fromScanTarget struct {
		rel    storage.RelFileNode
		cols   []catalog.Column
		colMap []int // nil = parent/partition child (no remapping); set for inheritance children
		tbl    *catalog.Table
	}
	fromScanTargets := []fromScanTarget{{rel: rel, cols: tgtCols, tbl: o.plan.Table}}
	if imFrom, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
		// Partition children: same column ordinals as parent, possibly overridden GeneratedExpr.
		for _, pc := range imFrom.PartitionChildren(o.plan.Table.OID) {
			if err := o.ctx.acquireRelLock(o.ctx.Catalog.RelFileNode(pc), lockmgr.RowExclusiveLock); err != nil {
				return nil, err
			}
			fromScanTargets = append(fromScanTargets, fromScanTarget{
				rel:  o.ctx.Catalog.RelFileNode(pc),
				cols: pc.Columns,
				tbl:  pc,
			})
		}
		// Inheritance children: require column remapping.
		for _, ic := range imFrom.InheritanceChildren(o.plan.Table.OID) {
			if err := o.ctx.acquireRelLock(o.ctx.Catalog.RelFileNode(ic), lockmgr.RowExclusiveLock); err != nil {
				return nil, err
			}
			fromScanTargets = append(fromScanTargets, fromScanTarget{
				rel:    o.ctx.Catalog.RelFileNode(ic),
				cols:   ic.Columns,
				colMap: buildInheritColMap(tgtCols, ic.Columns),
				tbl:    ic,
			})
		}
	}

	for _, fst := range fromScanTargets {
		fst := fst // capture
		if err := scanMatching(o.ctx, fst.rel, fst.cols, nil, func(blk storage.BlockNumber, slot uint16, rawRow Row) error {
			// For inheritance children: remap raw child row to parent col positions so
			// FromPred and SET exprs (which use parent ordinals) evaluate correctly. M0097-0078.
			var tgtRow Row
			if fst.colMap != nil {
				tgtRow = remapChildRowToParent(rawRow, fst.colMap)
			} else {
				tgtRow = rawRow
			}
			// Build all combinations of FROM rows via recursive enumeration.
			var recurse func(depth int, combinedRow Row) error
			recurse = func(depth int, combinedRow Row) error {
				if depth == len(fromSets) {
					// Full combined row available; evaluate predicate.
					if o.plan.FromPred != nil {
						v, _ := evalExpr(o.plan.FromPred, combinedRow, o.ctx)
						if v.IsNull() || v.Kind != KindBool || !v.BoolValue() {
							return nil
						}
					}
					// Clear multi-column subquery cache so each combined row gets a fresh evaluation.
					clear(o.ctx.MultiAssignSubqCache)
					// Predicate passed: compute new row in parent column space.
					parentNewRow := make(Row, tgtColCount)
					for i := range tgtCols {
						if i < len(o.plan.Set) && o.plan.Set[i] != nil {
							v, err := evalExpr(o.plan.Set[i], combinedRow, o.ctx)
							if err != nil {
								return err
							}
							parentNewRow[i] = v
						} else if i < len(tgtRow) {
							parentNewRow[i] = tgtRow[i]
						}
					}
					// For inheritance children, map back to child column order for writing.
					var actualNewRow Row
					var retNewRow Row
					if fst.colMap != nil {
						actualNewRow = remapParentRowToChild(parentNewRow, rawRow, tgtCols, fst.cols)
						retNewRow = parentNewRow // RETURNING exprs use parent ordinals
					} else {
						actualNewRow = parentNewRow
					}
					// Determine write destination: may be a different partition if the
					// partition key changed (cross-partition move). M0100-0010.
					writeRel := fst.rel
					writeCols := fst.cols
					writeTbl := fst.tbl
					if fst.tbl != nil && fst.tbl.PartitionParentOID != 0 {
						// fst is a partition child; check if the new row still routes here.
						if im2, ok2 := o.ctx.Catalog.(*catalog.InMemory); ok2 {
							parentTbl, parentOK := im2.LookupTableByOID(fst.tbl.PartitionParentOID)
							if parentOK {
								if destPart, _ := routeToPartition(parentTbl, parentNewRow, im2, o.ctx); destPart != nil && destPart.OID != fst.tbl.OID {
									writeRel = o.ctx.Catalog.RelFileNode(destPart)
									writeCols = destPart.Columns
									writeTbl = destPart
									actualNewRow = parentNewRow // destination uses parent ordinals
									retNewRow = parentNewRow
								}
							}
						}
					}
					_ = computeGeneratedColumns(writeCols, actualNewRow)
					var fromPortion Row
					if needFromForReturning && len(combinedRow) > tgtColCount {
						fromPortion = cloneRow(combinedRow[tgtColCount:])
					}
					pending = append(pending, pendingUpdate{
						srcRel: fst.rel, rel: writeRel, tgtCols: writeCols, tbl: writeTbl,
						blk: blk, slot: slot,
						newRow: actualNewRow, retNewRow: retNewRow,
						oldRow: cloneRow(rawRow), fromPortion: fromPortion,
					})
					return nil
				}
				for _, fromRow := range fromSets[depth].rows {
					next := append(combinedRow[:len(combinedRow):len(combinedRow)], fromRow...)
					if err := recurse(depth+1, next); err != nil {
						return err
					}
				}
				return nil
			}
			// Start recursion with the (parent-aligned) target row as base.
			return recurse(0, append(Row(nil), tgtRow...))
		}); err != nil {
			return nil, err
		}
	}

	// Step 3: apply pending updates. M0097-0065.
	// pu.srcRel = relation where xmax is stamped (source of the row).
	// pu.rel    = relation where the new row is written (destination; may differ on cross-partition move).
	// pu.tgtCols = columns of the destination relation. M0097-0078, M0100-0010.
	hotEligible := hotUpdateEligible(o.plan, o.ctx)
	seen := make(map[[2]uint64]bool)
	for _, pu := range pending {
		puSrcRel := pu.srcRel
		if puSrcRel == (storage.RelFileNode{}) {
			puSrcRel = rel
		}
		puRel := pu.rel
		if puRel == (storage.RelFileNode{}) {
			puRel = rel
		}
		puCols := pu.tgtCols
		if puCols == nil {
			puCols = tgtCols
		}
		key := [2]uint64{uint64(pu.blk), uint64(pu.slot)}
		if seen[key] {
			continue // already updated by an earlier FROM match
		}
		seen[key] = true

		// Fire BEFORE UPDATE triggers.
		if len(o.plan.Table.Triggers) > 0 {
			retRow, ok := fireTriggers(o.ctx, o.plan.Table, "before", "update", pu.oldRow, pu.newRow)
			if !ok {
				continue
			}
			pu.newRow = retRow
		}
		used := false
		if hotEligible && puSrcRel == rel && puRel == rel {
			var err error
			used, err = tryApplyHOTUpdate(o.ctx, rel, tgtCols, pu.blk, pu.slot, pu.newRow)
			if err != nil {
				return nil, err
			}
		}
		if !used {
			// Non-HOT update: stamp xmax on old tuple (in source rel), write new tuple
			// to destination rel (which may differ for cross-partition moves). M0100-0010.
			s, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: puSrcRel, Block: pu.blk})
			if err != nil {
				return nil, err
			}
			s.Lock()
			oldTup, oldGerr := storage.PageGetHeapTuple(s.Page(), pu.slot)
			if oldGerr != nil {
				s.Unlock()
				o.ctx.Pool.Unpin(s)
				continue // slot gone (concurrent prune)
			}
			if isConcurrentlyUpdated(oldTup.Header, o.ctx.Tx.XID, &o.ctx.Snap, o.ctx.MultiXact) {
				xmax := concurrentModifierXID(oldTup.Header, o.ctx.MultiXact)
				s.Unlock()
				o.ctx.Pool.Unpin(s)
				// EPQ: wait for the concurrent transaction and recheck. M0100-0010.
				if o.ctx.Tx.Isolation != mvcc.IsolationReadCommitted {
					return nil, &ExecError{Code: "40001", Pos: o.plan.Pos(),
						Message: "could not serialize access due to concurrent update"}
				}
				origSnap := o.ctx.Snap
				if epqWait(o.ctx, xmax) {
					return nil, &ExecError{Code: "40P01", Pos: o.plan.Pos(),
						Message: "deadlock detected"}
				}
				// Re-check: follow HOT chain for the latest version in the source relation.
				newSlot, epqRow, hotFound, _ := epqFollowHOT(o.ctx, puSrcRel, pu.blk, pu.slot, puCols, nil, &origSnap)
				epqBlk := pu.blk
				if !hotFound {
					// HOT chain failed. Check for moved-partition sentinel before
					// trying the CTID chain (non-HOT same-partition update). M0100-0010.
					if epqSlotMovedToAnotherPartition(o.ctx, puSrcRel, pu.blk, pu.slot) {
						return nil, &ExecError{Code: "55000", Pos: o.plan.Pos(),
							Message: "tuple to be locked was already moved to another partition due to concurrent update"}
					}
					cBlk, cSlot, cRow, cFound := epqFollowChain(o.ctx, puSrcRel, pu.blk, pu.slot, puCols, nil, &origSnap)
					if !cFound {
						continue // row deleted or chain exhausted
					}
					epqBlk, newSlot, epqRow = cBlk, cSlot, cRow
				}
				// CTE isolation: if the EPQ chain led to a CTE-written tuple, the CTE
				// already owns this row. Skip to preserve savedSnap isolation semantics.
				// Verify xmin == currentTx to avoid false positives when another table's
				// CTE-written rows coincidentally share the same {block,slot}. M0100-0010.
				if o.ctx.CTEWriteFence != nil {
					if _, inFence := o.ctx.CTEWriteFence[storage.ItemPointer{Block: epqBlk, Offset: newSlot}]; inFence {
						skipRow := false
						if s2, serr := o.ctx.Pool.Pin(storage.BufferTag{Rel: puSrcRel, Block: epqBlk}); serr == nil {
							s2.RLock()
							if tup2, terr := storage.PageGetHeapTuple(s2.Page(), newSlot); terr == nil {
								skipRow = mvcc.IsSelfXID(tup2.Header.Xmin, o.ctx.Tx.XID, o.ctx.TxnMgr)
							}
							s2.RUnlock()
							o.ctx.Pool.Unpin(s2)
						}
						if skipRow {
							continue
						}
					}
				}
				// Re-evaluate predicate against the new row + FROM portion.
				epqCombined := append(append(Row(nil), epqRow...), pu.fromPortion...)
				if o.plan.FromPred != nil {
					v, _ := evalExpr(o.plan.FromPred, epqCombined, o.ctx)
					if v.IsNull() || v.Kind != KindBool || !v.BoolValue() {
						continue
					}
				}
				// Re-compute SET expressions against the EPQ-fetched row.
				parentNewRow := make(Row, tgtColCount)
				for i := range tgtCols {
					if i < len(o.plan.Set) && o.plan.Set[i] != nil {
						v, evalErr := evalExpr(o.plan.Set[i], epqCombined, o.ctx)
						if evalErr != nil {
							return nil, evalErr
						}
						parentNewRow[i] = v
					} else if i < len(epqRow) {
						parentNewRow[i] = epqRow[i]
					}
				}
				// Re-route to partition if partition key changed. M0100-0010.
				epqWriteRel := puSrcRel
				epqWriteCols := puCols
				if pu.tbl != nil && pu.tbl.PartitionParentOID != 0 {
					if im2, ok2 := o.ctx.Catalog.(*catalog.InMemory); ok2 {
						parentTbl, parentOK := im2.LookupTableByOID(pu.tbl.PartitionParentOID)
						if parentOK {
							if destPart, _ := routeToPartition(parentTbl, parentNewRow, im2, o.ctx); destPart != nil && destPart.OID != pu.tbl.OID {
								epqWriteRel = o.ctx.Catalog.RelFileNode(destPart)
								epqWriteCols = destPart.Columns
							}
						}
					}
				}
				_ = computeGeneratedColumns(epqWriteCols, parentNewRow)
				pu.newRow = parentNewRow
				pu.retNewRow = nil // EPQ recomputed parentNewRow into pu.newRow; clear stale retNewRow so RETURNING uses pu.newRow
				pu.oldRow = cloneRow(epqRow)
				pu.blk = epqBlk
				pu.slot = newSlot
				puRel = epqWriteRel
				puCols = epqWriteCols
			}
			var oldTupleBytes []byte
			s, err = o.ctx.Pool.Pin(storage.BufferTag{Rel: puSrcRel, Block: pu.blk})
			if err != nil {
				return nil, err
			}
			s.Lock()
			oldTup, oldGerr = storage.PageGetHeapTuple(s.Page(), pu.slot)
			if oldGerr != nil {
				s.Unlock()
				o.ctx.Pool.Unpin(s)
				continue
			}
			oldTupleBytes, _ = oldTup.MarshalBinary()
			// Preserve a pre-existing non-conflicting foreign locker into a
			// {updater + survivors} multi (M0118-0004 producer); plain single-xid
			// stamp otherwise. keysUpdated=true (non-HOT write).
			stampErr := stampUpdaterXmaxNonHOT(o.ctx, s.Page(), pu.slot, oldTup.Header, true)
			if stampErr != nil {
				s.Unlock()
				o.ctx.Pool.Unpin(s)
				if errors.Is(stampErr, storage.ErrUnsupportedItem) || errors.Is(stampErr, storage.ErrInvalidSlot) {
					continue
				}
				return nil, stampErr
			}
			derr := markHeapDeleteDirtyAndClearVM(o.ctx, s, puSrcRel, pu.blk, pu.slot, effectiveWriterXID(o.ctx), oldTupleBytes)
			s.Unlock()
			o.ctx.Pool.Unpin(s)
			if derr != nil {
				return nil, derr
			}
			// For cross-partition move: also need to handle stampOldCtid from srcRel to new location.
			isCrossPartMove := puSrcRel != puRel
			newPtr, werr := writeHeapRowReturning(o.ctx, puRel, puCols, pu.newRow)
			if werr != nil {
				return nil, werr
			}
			if isCrossPartMove {
				if cerr := stampOldCtid(o.ctx, puSrcRel, pu.blk, pu.slot, newPtr); cerr != nil {
					return nil, cerr
				}
			}
			if o.ctx.InDMLCTE && o.ctx.CTEWriteFence != nil {
				oldPtr := storage.ItemPointer{Block: pu.blk, Offset: pu.slot}
				if _, inFence := o.ctx.CTEWriteFence[oldPtr]; inFence {
					if o.ctx.CTENewToOld != nil {
						if orig, ok := o.ctx.CTENewToOld[oldPtr]; ok {
							if o.ctx.CTESelfModifiedErrors == nil {
								o.ctx.CTESelfModifiedErrors = make(map[storage.ItemPointer]struct{})
							}
							o.ctx.CTESelfModifiedErrors[orig] = struct{}{}
						}
					}
				}
				o.ctx.CTEWriteFence[newPtr] = struct{}{}
				if o.ctx.CTENewToOld != nil {
					o.ctx.CTENewToOld[newPtr] = oldPtr
				}
			}
			maintainUniqueIndexesForInsert(o.ctx, o.plan.Table, puCols, pu.newRow, newPtr)
			if !isCrossPartMove {
				if cerr := stampOldCtid(o.ctx, puSrcRel, pu.blk, pu.slot, newPtr); cerr != nil {
					return nil, cerr
				}
			}
			if o.ctx.LogCanonical != nil {
				if derr := emitCanonicalHeapDelete(o.ctx, puSrcRel, pu.blk, pu.slot); derr != nil {
					return nil, derr
				}
				if ierr := emitCanonicalHeapInsert(o.ctx, puRel, newPtr); ierr != nil {
					return nil, ierr
				}
			}
		}
		if serr := ssiRecordTupleWrite(o.ctx, puSrcRel, pu.blk, pu.slot); serr != nil {
			return nil, serr
		}
		o.rowsAffected++
		// Use retNewRow for RETURNING when available (inheritance children). M0097-0078.
		retForRet := pu.newRow
		if pu.retNewRow != nil {
			retForRet = pu.retNewRow
		}
		o.appendUpdateRetRowWithFrom(retForRet, pu.fromPortion)
	}

	o.done = true
	if o.retIdx < len(o.retRows) {
		row := o.retRows[o.retIdx]
		o.retIdx++
		return SlotFromRow(o.plan.ReturningSchema, row), nil
	}
	return nil, EOF
}

// deleteWithUsing implements DELETE … USING by collecting all USING-table
// rows into memory, then doing a nested-loop cross-product against each
// target table row, applying o.plan.UsingPred to find matching
// combinations, and stamping xmax on the matched target tuples.
// M0097-0076. Mirrors updateOp.updateWithFrom.
func (o *deleteOp) deleteWithUsing() (TupleSlot, error) {
	o.done = true
	// DELETE is unconditionally a write — materialise the transaction's
	// XID before the scan so foreign-lock checks see the real XID.
	if err := o.ctx.MaterializeWriterXID(); err != nil {
		return nil, err
	}
	tbl := o.plan.Table
	rel := o.ctx.Catalog.RelFileNode(tbl)
	tgtCols := tbl.Columns
	tgtColCount := len(tgtCols)
	needUsingForReturning := len(o.plan.Returning) > 0

	// Step 1: collect all rows from each USING table.
	type usingRows struct {
		rows []Row
	}
	usingSets := make([]usingRows, len(o.plan.UsingScans))
	for i, usingNode := range o.plan.UsingScans {
		rows, err2 := collectNodeRows(usingNode, o.ctx)
		if err2 != nil {
			return nil, err2
		}
		usingSets[i] = usingRows{rows: rows}
	}

	// Step 2: scan target table (parent + inheritance children) without predicate;
	// cross-product with USING-table rows to collect victims. M0097-0076, M0097-0078.
	type victim struct {
		rel          storage.RelFileNode // source relation (parent or child)
		blk          storage.BlockNumber
		slot         uint16
		oldRow       Row // raw row in source table column order (for xmax stamping)
		retOldRow    Row // parent-aligned row for RETURNING (nil = use oldRow); M0097-0078
		usingPortion Row // joined USING-table columns for RETURNING; nil when RETURNING absent
	}
	var victims []victim
	seen := make(map[[2]uint64]bool)

	// Collect inheritance children for the USING target scan. M0097-0078.
	type usingScanTarget struct {
		rel    storage.RelFileNode
		cols   []catalog.Column
		colMap []int // nil = parent; set for inheritance children
	}
	usingScanTargets := []usingScanTarget{{rel: rel, cols: tgtCols}}
	if imDel, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
		for _, ic := range imDel.InheritanceChildren(tbl.OID) {
			if err := o.ctx.acquireRelLock(o.ctx.Catalog.RelFileNode(ic), lockmgr.RowExclusiveLock); err != nil {
				return nil, err
			}
			usingScanTargets = append(usingScanTargets, usingScanTarget{
				rel:    o.ctx.Catalog.RelFileNode(ic),
				cols:   ic.Columns,
				colMap: buildInheritColMap(tgtCols, ic.Columns),
			})
		}
	}

	for _, ust := range usingScanTargets {
		ust := ust // capture
		if err := scanMatching(o.ctx, ust.rel, ust.cols, nil, func(blk storage.BlockNumber, slot uint16, rawRow Row) error {
			key := [2]uint64{uint64(blk), uint64(slot)}
			if seen[key] {
				return nil
			}
			// For inheritance children: remap to parent column positions so
			// UsingPred (which uses parent ordinals) evaluates correctly. M0097-0078.
			var tgtRow Row
			if ust.colMap != nil {
				tgtRow = remapChildRowToParent(rawRow, ust.colMap)
			} else {
				tgtRow = rawRow
			}
			// Cross-product enumeration via recursion.
			var recurse func(depth int, combinedRow Row) error
			recurse = func(depth int, combinedRow Row) error {
				if seen[key] {
					return nil
				}
				if depth == len(usingSets) {
					if o.plan.UsingPred != nil {
						v, _ := evalExpr(o.plan.UsingPred, combinedRow, o.ctx)
						if v.IsNull() || v.Kind != KindBool || !v.BoolValue() {
							return nil
						}
					}
					var usingPortion Row
					if needUsingForReturning && len(combinedRow) > tgtColCount {
						usingPortion = cloneRow(combinedRow[tgtColCount:])
					}
					// For inheritance children, store parent-aligned tgtRow for RETURNING. M0097-0078.
					var retOldRow Row
					if ust.colMap != nil {
						retOldRow = cloneRow(tgtRow)
					}
					victims = append(victims, victim{
						rel: ust.rel, blk: blk, slot: slot,
						oldRow: cloneRow(rawRow), retOldRow: retOldRow,
						usingPortion: usingPortion,
					})
					seen[key] = true
					return nil
				}
				for _, useRow := range usingSets[depth].rows {
					next := append(combinedRow[:len(combinedRow):len(combinedRow)], useRow...)
					if err := recurse(depth+1, next); err != nil {
						return err
					}
					if seen[key] {
						return nil
					}
				}
				return nil
			}
			return recurse(0, append(Row(nil), tgtRow...))
		}); err != nil {
			return nil, err
		}
	}

	// Step 3: apply pending deletes. Uses v.rel for child tables. M0097-0076, M0097-0078.
	for _, v := range victims {
		vRel := v.rel
		if vRel == (storage.RelFileNode{}) {
			vRel = rel
		}
		// Fire BEFORE DELETE triggers and enforce FK constraints.
		if len(tbl.Triggers) > 0 {
			_, ok := fireTriggers(o.ctx, tbl, "before", "delete", v.oldRow, nil)
			if !ok {
				continue
			}
		}
		if err := enforceFKOnDelete(o.ctx, tbl, v.oldRow); err != nil {
			return nil, err
		}
		s, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: vRel, Block: v.blk})
		if err != nil {
			return nil, err
		}
		s.Lock()
		oldTup, oldGerr := storage.PageGetHeapTuple(s.Page(), v.slot)
		if oldGerr != nil {
			s.Unlock()
			o.ctx.Pool.Unpin(s)
			continue
		}
		if isConcurrentlyUpdated(oldTup.Header, o.ctx.Tx.XID, &o.ctx.Snap, o.ctx.MultiXact) {
			s.Unlock()
			o.ctx.Pool.Unpin(s)
			continue // skip concurrent-update EPQ for USING case
		}
		oldTupleBytes, _ := oldTup.MarshalBinary()
		// Preserve a pre-existing non-conflicting foreign locker (M0118-0004
		// producer). DELETE is StatusUpdate (keysUpdated=true) → no-op unless a
		// non-conflicting locker survives; wired for sibling-path parity.
		if stampErr := stampUpdaterXmaxNonHOT(o.ctx, s.Page(), v.slot, oldTup.Header, true); stampErr != nil {
			s.Unlock()
			o.ctx.Pool.Unpin(s)
			if errors.Is(stampErr, storage.ErrUnsupportedItem) {
				continue
			}
			return nil, stampErr
		}
		derr := markHeapDeleteDirtyAndClearVM(o.ctx, s, vRel, v.blk, v.slot, effectiveWriterXID(o.ctx), oldTupleBytes)
		s.Unlock()
		o.ctx.Pool.Unpin(s)
		if derr != nil {
			return nil, derr
		}
		if cerr := emitCanonicalHeapDelete(o.ctx, vRel, v.blk, v.slot); cerr != nil {
			return nil, cerr
		}
		if serr := ssiRecordTupleWrite(o.ctx, vRel, v.blk, v.slot); serr != nil {
			return nil, serr
		}
		// Use parent-aligned retOldRow for RETURNING when available. M0097-0078.
		delRetRow := v.oldRow
		if v.retOldRow != nil {
			delRetRow = v.retOldRow
		}
		o.appendDeleteRetRowWithUsing(delRetRow, v.usingPortion)
		o.rowsAffected++
	}

	// Yield first RETURNING row inline.
	if o.retIdx < len(o.retRows) {
		row := o.retRows[o.retIdx]
		o.retIdx++
		return SlotFromRow(o.plan.ReturningSchema, row), nil
	}
	return nil, EOF
}

// foreignLockOnly reports whether `h` indicates the tuple is
// currently row-locked by another live transaction (M0021
// tuple-level locking step 2b). The xmax field carries the
// locker's xid; the HeapXmaxLockOnly infomask bit distinguishes
// a lock from a real delete. We wait on the lockmgr's
// transaction-scoped tuple tag — when the locker commits /
// aborts, ReleaseAll drops the tuple-tag holder and the waiting
// UPDATE / DELETE wakes up.
func foreignLockOnly(h storage.HeapTupleHeader, currentXID storage.TransactionID) bool {
	if h.Xmax == storage.InvalidTransactionID {
		return false
	}
	if h.Xmax == currentXID {
		return false
	}
	return storage.IsHeapTupleLockOnly(h.Infomask)
}

func scanMatching(ctx *Context, rel storage.RelFileNode, cols []catalog.Column, pred planner.Expr, fn func(blk storage.BlockNumber, slot uint16, row Row) error) error {
	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return err
	}
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return err
		}
		s.RLock()
		page := s.Page()
		if storage.IsNew(page) {
			s.RUnlock()
			ctx.Pool.Unpin(s)
			continue
		}
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			s.RUnlock()
			ctx.Pool.Unpin(s)
			return err
		}
		// Collect all visible tuples with decoded rows under RLock, then
		// evaluate predicates and call fn outside the lock. This ensures
		// per-row interleaving of WHERE side-effects (RAISE NOTICE) and
		// callbacks (BEFORE triggers), matching PG's per-row scan semantics.
		type visibleTuple struct {
			slot     uint16
			row      Row
			lockedBy storage.TransactionID
		}
		var visible []visibleTuple
		scanRow := make(Row, len(cols))
		for slot := uint16(1); slot <= uint16(count); slot++ {
			tuple, err := storage.PageGetHeapTuple(page, slot)
			if err != nil {
				if errors.Is(err, storage.ErrUnsupportedItem) || errors.Is(err, storage.ErrInvalidSlot) {
					continue
				}
				s.RUnlock()
				ctx.Pool.Unpin(s)
				return err
			}
			if !mvcc.TupleVisibleSubxact(tuple.Header, ctx.Snap, ctx.Tx.XID, ctx.TxnMgr, ctx.MultiXact) {
				// CTESelfModifiedErrors: if a sub-command during the CTE phase
				// modified this invisible (own-xmax) tuple, the outer
				// UPDATE/DELETE must raise ERRCODE_TRIGGERED_DATA_CHANGE_VIOLATION.
				if ctx.CTESelfModErr != nil && ctx.CTESelfModifiedErrors != nil {
					ptr := storage.ItemPointer{Block: blk, Offset: slot}
					if _, inErr := ctx.CTESelfModifiedErrors[ptr]; inErr {
						s.RUnlock()
						ctx.Pool.Unpin(s)
						return ctx.CTESelfModErr
					}
				}
				continue
			}
			// Skip rows written by DML CTEs — outer UPDATE/DELETE must see
			// pre-CTE state (PostgreSQL CTE snapshot-isolation semantics).
			if ctx.CTEWriteFence != nil {
				ptr := storage.ItemPointer{Block: blk, Offset: slot}
				if _, inFence := ctx.CTEWriteFence[ptr]; inFence {
					continue
				}
			}
			// M0104-0008: SSI read-path hook for the UPDATE / DELETE
			// scanMatching loop (mirrors the seqScanOp.Next site). A
			// SERIALIZABLE writer that reads a tuple here must install
			// a SIREAD predicate lock so concurrent peers detect the
			// rw-conflict, and must check conflict-out against both
			// xmin (visible writer) and xmax (concurrent overwriter
			// hidden by snapshot).
			// M0118-0001: a non-nil error means this read closed a dangerous
			// structure to an already-committed writer and the reading
			// UPDATE/DELETE must abort mid-statement (40001). Release the
			// page RLock + pin before returning, like the decode-error path.
			if err := ssiRecordTupleRead(ctx, rel, blk, slot, tuple.Header.Xmin, tuple.Header.Xmax); err != nil {
				s.RUnlock()
				ctx.Pool.Unpin(s)
				return err
			}
			storedNatts := int(tuple.Header.Infomask2 & 0x07FF)
			if err := DecodeRowIntoMctxPGTuple(scanRow, cols, tuple.Data, tuple.Bitmap, storedNatts, nil); err != nil {
				continue // skip undecodable tuples (e.g. schema mismatch)
			}
			rowCopy := make(Row, len(cols))
			copy(rowCopy, scanRow)
			visible = append(visible, visibleTuple{
				slot:     slot,
				row:      rowCopy,
				lockedBy: lockedByForeign(tuple.Header, ctx.Tx.XID),
			})
		}
		s.RUnlock()
		ctx.Pool.Unpin(s)
		// Process visible tuples one at a time: eval predicate then call fn
		// immediately, so WHERE side-effects (NOTICE) and trigger NOTICEs
		// interleave per-row rather than all predicates firing before all
		// callbacks.
		for _, vt := range visible {
			if pred != nil {
				predSlot := &MaterializedSlot{
					row:       vt.row,
					hasCTID:   true,
					ctidBlock: uint32(blk),
					ctidOff:   vt.slot,
				}
				v, err := evalExprSlot(pred, predSlot, ctx)
				if err != nil {
					return err
				}
				if v.IsNull() || v.Kind != KindBool || !v.BoolValue() {
					continue
				}
			}
			// M0021 step 2b: if the tuple is row-locked by
			// another live xact (HEAP_XMAX_LOCK_ONLY +
			// xmax != ours), block on the locker's tuple-tag
			// in the lockmgr. ReleaseAll on the locker's
			// commit/abort wakes us up; we then proceed
			// with the UPDATE / DELETE atomic stamp.
			if vt.lockedBy != storage.InvalidTransactionID {
				ptr := storage.ItemPointer{Block: blk, Offset: vt.slot}
				if err := ctx.acquireTupleLock(rel, ptr, lockmgr.ExclusiveLock); err != nil {
					return err
				}
			}
			if err := fn(blk, vt.slot, vt.row); err != nil {
				return err
			}
		}
	}
	return nil
}

// lockedByForeign returns the locking xid when `h` indicates the
// tuple is row-locked by another live xact (HEAP_XMAX_LOCK_ONLY
// + xmax != currentXID); InvalidTransactionID otherwise.
// Capturing this at scan time and using the captured value at
// the per-row dispatch loop avoids re-reading the page after
// we've released its RLock.
func lockedByForeign(h storage.HeapTupleHeader, currentXID storage.TransactionID) storage.TransactionID {
	if foreignLockOnly(h, currentXID) {
		return h.Xmax
	}
	return storage.InvalidTransactionID
}

// conflictingRowLockHolders returns the still-active foreign transactions that
// hold a row-level *lock* on tuple h whose strength conflicts with a write of
// the given request status. It is the write-path analogue of
// lockRowsOp.activeLockHolders: heap_lock_updated_tuple propagates a row lock
// forward onto the live version of an updated tuple (the locker half of
// M0118-0003, propagateLockForward), so a plain UPDATE / DELETE must honour
// that lock across statements. The lockmgr tuple tag is statement-scoped
// (ReleaseAll at each Query message's end), so a cross-statement holder has
// already dropped it — cross-statement blocking instead rides this persisted
// lock-only xmax, the same durable signal stampLockInner's wait branch reads.
//
// reqStatus is StatusUpdate for a DELETE or a key-column UPDATE (conflicts with
// every lock strength incl. FOR KEY SHARE) and StatusNoKeyUpdate for a no-key
// UPDATE (does NOT conflict with FOR KEY SHARE). Returns nil when the tuple
// carries no foreign lock-only xmax, when the held strength does not conflict,
// or when no naming holder is still active (the lock was already released).
//
// GOTCHA: a MultiXactId and a TransactionID share the uint32 space and
// HEAP_XMAX_IS_MULTI is the only disambiguator, so a multi xmax is resolved
// through the member store and never handed to IsXIDActive directly. A real
// updater/deleter (xmax not lock-only) is left to the isConcurrentlyUpdated /
// EPQ path; this helper only covers pure row locks.
func conflictingRowLockHolders(ctx *Context, h storage.HeapTupleHeader, reqStatus multixact.Status) []storage.TransactionID {
	if ctx.TxnMgr == nil {
		return nil
	}
	if h.Xmax == storage.InvalidTransactionID || h.Xmax == ctx.Tx.XID {
		return nil
	}
	if !storage.IsHeapTupleLockOnly(h.Infomask) {
		return nil
	}
	if storage.IsHeapTupleXmaxMulti(h.Infomask) {
		if ctx.MultiXact == nil {
			return nil
		}
		members, ok := ctx.MultiXact.Members(multixact.MultiXactId(h.Xmax))
		if !ok {
			return nil
		}
		var out []storage.TransactionID
		for _, m := range members {
			// Per-member conflict: a FOR KEY SHARE member does not block a
			// no-key UPDATE even when a FOR SHARE member in the same multi
			// does, so we wait only on the members we actually conflict with
			// (mirrors Do_MultiXactIdWait's per-member DoLockModesConflict).
			if m.Xid != ctx.Tx.XID && ctx.TxnMgr.IsXIDActive(m.Xid) &&
				multixact.StatusesConflict(m.Status, reqStatus) {
				out = append(out, m.Xid)
			}
		}
		return out
	}
	// Single-holder lock-only xmax: the strength lives in the infomask bits.
	if !tupleLockConflicts(reqStatus, h.Infomask, h.Infomask2) {
		return nil
	}
	if ctx.TxnMgr.IsXIDActive(h.Xmax) {
		return []storage.TransactionID{h.Xmax}
	}
	return nil
}

// waitForConflictingRowLock blocks the current write until no still-active
// foreign transaction holds a conflicting row lock on (rel,blk,slot). It is the
// write-path counterpart to the lock-only wait branch in stampLockInner: a row
// lock propagated forward by heap_lock_updated_tuple onto a live version must
// stall a conflicting UPDATE / DELETE until the holder commits or aborts (the
// heap_update / heap_delete MultiXactIdWait / XactLockTableWait step). A
// lock-only xmax never relocates the row, so blk/slot stay valid across the
// wait; a genuine concurrent updater is still handled by the caller's EPQ loop.
// Each holder is waited on via epqWait, which registers a wait-for-graph edge,
// so a lock cycle surfaces as a 40001 deadlock rather than hanging.
func waitForConflictingRowLock(ctx *Context, rel storage.RelFileNode, blk storage.BlockNumber, slot uint16, reqStatus multixact.Status, pos int) error {
	for {
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return err
		}
		s.RLock()
		tup, gerr := storage.PageGetHeapTuple(s.Page(), slot)
		var holders []storage.TransactionID
		if gerr == nil {
			holders = conflictingRowLockHolders(ctx, tup.Header, reqStatus)
		}
		s.RUnlock()
		ctx.Pool.Unpin(s)
		if len(holders) == 0 {
			return nil
		}
		for _, hx := range holders {
			if epqWait(ctx, hx) {
				return &ExecError{Code: "40001", Pos: pos,
					Message: "could not serialize access due to concurrent update (deadlock)"}
			}
		}
	}
}

// encodeIndexKeyFromCols builds a btree key for an index by looking up
// each index column by name in cols and encoding the corresponding row value.
// Returns nil (no error) when any key column is NULL (NULLs don't participate
// in unique constraints) or when the column is not found. M0100-0005.
// cat is optional (may be nil): when provided, KindString values on enum-typed
// columns are converted to KindEnum so encoding is consistent with the probe path.
func encodeIndexKeyFromCols(idx *catalog.Index, cols []catalog.Column, row Row, cat ...catalog.Catalog) ([]byte, error) {
	var im *catalog.InMemory
	if len(cat) > 0 && cat[0] != nil {
		im, _ = cat[0].(*catalog.InMemory)
	}
	var out []byte
	for _, idxColName := range idx.Columns {
		var col *catalog.Column
		var colOrd int
		for i := range cols {
			if strings.EqualFold(cols[i].Name, idxColName) {
				col = &cols[i]
				colOrd = i
				break
			}
		}
		if col == nil || colOrd >= len(row) {
			return nil, nil
		}
		v := row[colOrd]
		if v.IsNull() {
			return nil, nil // NULLs don't participate in unique constraints
		}
		// For enum columns: convert KindString labels to KindEnum (sort order)
		// so encoding matches the btree probe path. M0097-0022.
		if v.Kind == KindString && im != nil {
			if et, isEnum := im.LookupEnum(col.Type.Name); isEnum {
				label := v.StringValue()
				for _, ev := range et.Values {
					if ev.Label == label {
						v = NewEnumDatum(ev.SortOrder, label)
						break
					}
				}
			}
		}
		keyPart, err := encodeBTreeKeyForColumn(v, col, 0)
		if err != nil {
			return nil, err
		}
		out = append(out, keyPart...)
	}
	return out, nil
}

// maintainUniqueIndexesForInsert updates all unique/primary btree indexes
// on tbl after a heap row has been inserted at ptr. Ensures that subsequent
// index scans (updateViaIndex, planIndexScanFromWhere) can locate the row.
// Non-fatal: errors are silently swallowed so a missing or empty index
// does not prevent the INSERT from completing. M0100-0005.
func maintainUniqueIndexesForInsert(ctx *Context, tbl *catalog.Table, cols []catalog.Column, row Row, ptr storage.ItemPointer) {
	if ctx.Catalog == nil || ctx.Pool == nil {
		return
	}
	for _, idx := range ctx.Catalog.IndexesOnTable(tbl) {
		idxRel := ctx.Catalog.IndexRelFileNode(idx)
		tree, err := btree.Open(ctx.Pool, idxRel)
		if err != nil {
			continue
		}
		key, err := encodeIndexKeyFromCols(idx, cols, row, ctx.Catalog)
		if err != nil || key == nil {
			// Fall back to expression-column encoding for expression-based indexes
			// (e.g. CREATE UNIQUE INDEX ON t(lower(col))). encodeIndexKeyFromCols
			// returns nil when idx.Columns[i]=="" (expression column); evaluate the
			// stored ColExprs to produce the btree key so concurrent sessions can
			// detect the in-progress insert via the arbiter scan. M0100-0005.
			key = encodeExprIndexKey(ctx, idx, tbl, row)
			if key == nil {
				continue
			}
		}
		_ = tree.Insert(key, ptr)
	}
}

// encodeExprIndexKey encodes a btree key for an expression-based index by
// resolving and evaluating each ColExpr in idx against the given row.
// Returns nil if any expression column lacks a stored ColExpr, fails to
// evaluate, or produces NULL (NULLs don't participate in unique constraints).
// Used by maintainUniqueIndexesForInsert as a fallback when encodeIndexKeyFromCols
// returns nil (which happens for indexes like CREATE UNIQUE INDEX ON t(lower(col))).
func encodeExprIndexKey(ctx *Context, idx *catalog.Index, tbl *catalog.Table, row Row) []byte {
	if len(idx.ColExprs) == 0 {
		return nil
	}
	var hasExpr bool
	for i, colName := range idx.Columns {
		if colName == "" && i < len(idx.ColExprs) && idx.ColExprs[i] != nil {
			hasExpr = true
			break
		}
	}
	if !hasExpr {
		return nil
	}
	var out []byte
	for i, colName := range idx.Columns {
		if colName != "" {
			// Plain column — encode normally.
			var col *catalog.Column
			var colOrd int
			for j := range tbl.Columns {
				if strings.EqualFold(tbl.Columns[j].Name, colName) {
					col = &tbl.Columns[j]
					colOrd = j
					break
				}
			}
			if col == nil || colOrd >= len(row) {
				return nil
			}
			v := row[colOrd]
			if v.IsNull() {
				return nil
			}
			keyPart, err := encodeBTreeKeyForColumn(v, col, 0)
			if err != nil {
				return nil
			}
			out = append(out, keyPart...)
		} else if i < len(idx.ColExprs) && idx.ColExprs[i] != nil {
			// Expression column — resolve then evaluate.
			planExpr, err := planner.ResolveIndexPredicate(*idx.ColExprs[i], tbl)
			if err != nil || planExpr == nil {
				return nil
			}
			v, err := evalExpr(planExpr, row, ctx)
			if err != nil || v.IsNull() {
				return nil
			}
			k := encodeArbiterExprKey(v, 0)
			if k == nil {
				return nil
			}
			out = append(out, k...)
		} else {
			return nil // expression column without stored ColExpr
		}
	}
	return out
}

// checkUniqueIndexesForInsert enforces unique-constraint violations at INSERT
// time. For each unique/primary btree index on `tbl`, it computes the
// candidate key from `row` and probes the index for a matching live entry.
// A "live" match is a heap tuple whose xmin is committed (or in-progress in
// another session — handled below) and whose xmax is invalid, aborted, or
// in-progress in another session. If found, returns 23505 with an upstream-
// shaped MESSAGE; otherwise returns nil. The plain INSERT path calls this
// before writing the heap tuple. The upsertOp / ON CONFLICT path bypasses
// this check because it has already routed conflicts through its arbiter
// detector. Apply-worker insertions bypass it too because logical
// replication delivers committed-on-publisher rows that the subscriber's
// heap may not see yet (skip-on-duplicate is the right behaviour there).
//
// The visibility check is conservative: any tuple whose xmin is the current
// session OR is committed/in-progress in another session counts as a
// conflict. Aborted-xmin tuples and committed-then-deleted tuples (xmax
// committed) do not collide. This matches the upstream "the new tuple
// would create a duplicate" diagnostic emitted at INSERT time, while
// avoiding the full XID-wait dance (deferred to a later milestone).
func checkUniqueIndexesForInsert(ctx *Context, tbl *catalog.Table, cols []catalog.Column, row Row, pos int) error {
	if ctx.Catalog == nil || ctx.Pool == nil {
		return nil
	}
	rel := ctx.Catalog.RelFileNode(tbl)
	for _, idx := range ctx.Catalog.IndexesOnTable(tbl) {
		if !idx.Unique && !idx.Primary {
			continue
		}
		idxRel := ctx.Catalog.IndexRelFileNode(idx)
		tree, err := btree.Open(ctx.Pool, idxRel)
		if err != nil {
			continue
		}
		key, err := encodeIndexKeyFromCols(idx, cols, row, ctx.Catalog)
		if err != nil || key == nil {
			continue
		}
		detail := buildUniqueConstraintDetail(idx, cols, row)
		if raiseErr := uniqueCheckWithWait(ctx, rel, tree, key, idx.Name, detail, pos); raiseErr != nil {
			return raiseErr
		}
	}
	return nil
}

// indexKeyColumnsChanged reports whether any of idx's key columns differ
// between oldRow and newRow. Comparison is on the encoded index-key bytes so
// it matches exactly the value the btree stores (collation / type
// normalisation included). If either row cannot be encoded, it conservatively
// reports "changed" so the caller still performs the uniqueness probe.
func indexKeyColumnsChanged(idx *catalog.Index, cols []catalog.Column, oldRow, newRow Row, cat catalog.Catalog) bool {
	oldKey, oerr := encodeIndexKeyFromCols(idx, cols, oldRow, cat)
	newKey, nerr := encodeIndexKeyFromCols(idx, cols, newRow, cat)
	if oerr != nil || nerr != nil || oldKey == nil || newKey == nil {
		return true
	}
	return !bytes.Equal(oldKey, newKey)
}

// checkUniqueIndexesForUpdate enforces unique-constraint violations for the
// new version produced by an UPDATE.
//
// Unlike the INSERT-time check, it SKIPS any unique/primary index whose key
// columns are unchanged between oldRow and newRow: an UPDATE that does not
// alter an index's key cannot violate that index's uniqueness — the row
// already legitimately occupies that key slot, and the "duplicate" the probe
// would find is just another MVCC version of the very row being updated.
//
// Mirrors PostgreSQL, where a no-key-change UPDATE never raises 23505 against
// its own row (the index is not even touched for a HOT update, and for a
// non-HOT update _bt_check_unique recognises the prior versions as the same
// logical row). Without this scoping, a no-key-change UPDATE that falls back
// to the non-HOT path under concurrency (HOT blocked by a sibling client's
// in-flight xmax / a full page) re-probes the index and finds a concurrently
// updated version of the same key whose xmax is still in-flight; the
// INSERT-time visibility test classifies that as a live duplicate and raises a
// spurious "duplicate key value violates unique constraint" (the pgbench
// TPC-B `UPDATE pgbench_tellers` contention failure).
//
// forceAll bypasses the skip and probes every unique index regardless of
// whether the key changed. It is set for cross-partition moves, which are an
// internal DELETE+INSERT into a different relation that may already hold the
// key independently of this row's prior version.
func checkUniqueIndexesForUpdate(ctx *Context, tbl *catalog.Table, cols []catalog.Column, oldRow, newRow Row, forceAll bool, pos int) error {
	if ctx.Catalog == nil || ctx.Pool == nil {
		return nil
	}
	rel := ctx.Catalog.RelFileNode(tbl)
	for _, idx := range ctx.Catalog.IndexesOnTable(tbl) {
		if !idx.Unique && !idx.Primary {
			continue
		}
		if !forceAll && oldRow != nil && !indexKeyColumnsChanged(idx, cols, oldRow, newRow, ctx.Catalog) {
			// Key columns unchanged — cannot collide with itself. Skip the probe.
			continue
		}
		idxRel := ctx.Catalog.IndexRelFileNode(idx)
		tree, err := btree.Open(ctx.Pool, idxRel)
		if err != nil {
			continue
		}
		key, err := encodeIndexKeyFromCols(idx, cols, newRow, ctx.Catalog)
		if err != nil || key == nil {
			continue
		}
		detail := buildUniqueConstraintDetail(idx, cols, newRow)
		if raiseErr := uniqueCheckWithWait(ctx, rel, tree, key, idx.Name, detail, pos); raiseErr != nil {
			return raiseErr
		}
	}
	return nil
}

// checkExclusionConstraintsForInsert enforces exclusion-constraint violations at
// INSERT time. For btree EXCLUDE ... WITH = constraints (equality exclusion),
// this is equivalent to a uniqueness check. Returns 23P01 (exclusion_violation)
// if a live tuple with the same exclusion-column values already exists.
func checkExclusionConstraintsForInsert(ctx *Context, tbl *catalog.Table, cols []catalog.Column, row Row, pos int) error {
	if ctx.Catalog == nil || ctx.Pool == nil {
		return nil
	}
	rel := ctx.Catalog.RelFileNode(tbl)
	for _, idx := range ctx.Catalog.IndexesOnTable(tbl) {
		if !idx.IsExclusion {
			continue
		}
		switch idx.ExclusionOp {
		case "=":
			idxRel := ctx.Catalog.IndexRelFileNode(idx)
			tree, err := btree.Open(ctx.Pool, idxRel)
			if err != nil {
				continue
			}
			key, err := encodeIndexKeyFromCols(idx, cols, row, ctx.Catalog)
			if err != nil || key == nil {
				continue
			}
			detail := buildExclusionConstraintDetail(idx, cols, row)
			if raiseErr := exclusionCheckOnce(ctx, rel, tree, key, idx.Name, detail, pos); raiseErr != nil {
				return raiseErr
			}
		case "&&":
			// GiST overlap exclusion: seqscan heap and check box overlap.
			if raiseErr := checkGistOverlapExclusion(ctx, tbl, idx, cols, row, rel, pos); raiseErr != nil {
				return raiseErr
			}
		}
	}
	return nil
}

// checkGistOverlapExclusion enforces EXCLUDE USING gist (col WITH &&) by
// scanning the heap and checking whether any live tuple's box overlaps the
// new row's box value. Returns 23P01 on first conflict found.
func checkGistOverlapExclusion(ctx *Context, tbl *catalog.Table, idx *catalog.Index, cols []catalog.Column, row Row, rel storage.RelFileNode, pos int) error {
	if len(idx.Columns) == 0 {
		return nil
	}
	excColName := idx.Columns[0]
	// Find the new row's box value.
	newBoxStr := ""
	for i, col := range cols {
		if col.Name == excColName && i < len(row) {
			s, ok := datumAsString(row[i])
			if !ok {
				return nil // NULL box — no conflict
			}
			newBoxStr = s
			break
		}
	}
	if newBoxStr == "" {
		return nil
	}
	newUR, newLL, ok := parseBoxText(newBoxStr)
	if !ok {
		return nil
	}
	// Find the exclusion column index in tbl.Columns for decoding existing rows.
	excColIdx := -1
	for i, col := range tbl.Columns {
		if col.Name == excColName {
			excColIdx = i
			break
		}
	}
	if excColIdx < 0 {
		return nil
	}
	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return nil
	}
	decRow := make(Row, len(tbl.Columns))
	for b := storage.BlockNumber(0); b < nBlocks; b++ {
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: b})
		if err != nil {
			continue
		}
		s.RLock()
		page := s.Page()
		if storage.IsNew(page) {
			s.RUnlock()
			ctx.Pool.Unpin(s)
			continue
		}
		count, cerr := storage.PageLinePointerCount(page)
		if cerr != nil {
			s.RUnlock()
			ctx.Pool.Unpin(s)
			continue
		}
		for slotIdx := uint16(1); slotIdx <= uint16(count); slotIdx++ {
			tup, terr := storage.PageGetHeapTuple(page, slotIdx)
			if terr != nil {
				continue
			}
			if !isLiveForUniqueCheck(ctx, tup.Header.Xmin, tup.Header.Xmax) {
				continue
			}
			storedNatts := int(tup.Header.Infomask2 & 0x07FF)
			if decErr := DecodeRowIntoMctxPGTuple(decRow, tbl.Columns, tup.Data, tup.Bitmap, storedNatts, nil); decErr != nil {
				continue
			}
			if excColIdx >= len(decRow) {
				continue
			}
			existBoxStr, ok2 := datumAsString(decRow[excColIdx])
			if !ok2 {
				continue
			}
			exUR, exLL, ok3 := parseBoxText(existBoxStr)
			if !ok3 {
				continue
			}
			// Check overlap: !(a.ur < b.ll || b.ur < a.ll) on both axes.
			if !(newUR[0] < exLL[0] || exUR[0] < newLL[0] || newUR[1] < exLL[1] || exUR[1] < newLL[1]) {
				s.RUnlock()
				ctx.Pool.Unpin(s)
				detail := fmt.Sprintf("Key (%s)=(%s) conflicts with existing key (%s)=(%s).",
					excColName, newBoxStr, excColName, existBoxStr)
				return &ExecError{
					Code:    "23P01",
					Pos:     pos,
					Message: fmt.Sprintf("conflicting key value violates exclusion constraint %q", idx.Name),
					Detail:  detail,
				}
			}
		}
		s.RUnlock()
		ctx.Pool.Unpin(s)
	}
	return nil
}

// exclusionCheckOnce probes a btree exclusion index for a conflicting live tuple.
func exclusionCheckOnce(ctx *Context, rel storage.RelFileNode, tree *btree.BTree, key []byte, idxName, detail string, pos int) error {
	liveConflict := false
	_ = tree.RangeScan(key, key, func(_ []byte, ptr storage.ItemPointer) (bool, error) {
		slot, perr := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: ptr.Block})
		if perr != nil {
			return true, nil
		}
		slot.RLock()
		tuple, terr := storage.PageGetHeapTuple(slot.Page(), ptr.Offset)
		slot.RUnlock()
		ctx.Pool.Unpin(slot)
		if terr != nil {
			return true, nil
		}
		if isLiveForUniqueCheck(ctx, tuple.Header.Xmin, tuple.Header.Xmax) {
			liveConflict = true
			return false, nil
		}
		return true, nil
	})
	if liveConflict {
		return &ExecError{
			Code:    "23P01",
			Pos:     pos,
			Message: fmt.Sprintf("conflicting key value violates exclusion constraint %q", idxName),
			Detail:  detail,
		}
	}
	return nil
}

// buildExclusionConstraintDetail builds the DETAIL string for a 23P01 error:
// "Key (col1)=(val1) conflicts with existing key (col1)=(val1)."
func buildExclusionConstraintDetail(idx *catalog.Index, cols []catalog.Column, row Row) string {
	colNames := make([]string, 0, len(idx.Columns))
	colVals := make([]string, 0, len(idx.Columns))
	for _, idxCol := range idx.Columns {
		colNames = append(colNames, idxCol)
		val := ""
		for i, col := range cols {
			if col.Name == idxCol && i < len(row) {
				val = row[i].Format()
				break
			}
		}
		colVals = append(colVals, val)
	}
	key := fmt.Sprintf("Key (%s)=(%s)", strings.Join(colNames, ", "), strings.Join(colVals, ", "))
	return fmt.Sprintf("%s conflicts with existing key (%s)=(%s).",
		key, strings.Join(colNames, ", "), strings.Join(colVals, ", "))
}

// uniqueCheckWithWait probes a unique btree for a conflicting live tuple.
// If the conflict row was inserted by another in-flight xact (xmin is active
// and not ours), it waits for that xact to commit or abort then re-checks:
//   - Committed + SERIALIZABLE/RR → 40001 serialization failure
//   - Committed + RC              → 23505 unique violation
//   - Aborted                     → no conflict, continue
//
// Mirrors upstream heap_check_unique's WaitForLockersMultiple path that
// produces the <waiting ...> interleaving seen in read-write-unique.spec.
func uniqueCheckWithWait(ctx *Context, rel storage.RelFileNode, tree *btree.BTree, key []byte, idxName, detail string, pos int) error {
	var inflightXmin storage.TransactionID
	var liveConflict bool
	var conflictPtr storage.ItemPointer

	scanOnce := func() {
		inflightXmin = storage.InvalidTransactionID
		liveConflict = false
		conflictPtr = storage.ItemPointer{}
		_ = tree.RangeScan(key, key, func(_ []byte, ptr storage.ItemPointer) (bool, error) {
			slot, perr := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: ptr.Block})
			if perr != nil {
				return true, nil
			}
			slot.RLock()
			tuple, terr := storage.PageGetHeapTuple(slot.Page(), ptr.Offset)
			slot.RUnlock()
			ctx.Pool.Unpin(slot)
			if terr != nil {
				return true, nil
			}
			xmin := tuple.Header.Xmin
			selfXID := ctx.Tx.XID
			// In-flight other-xact insert: must wait.
			if ctx.TxnMgr != nil && xmin != storage.InvalidTransactionID &&
				(selfXID == storage.InvalidTransactionID || xmin != selfXID) &&
				ctx.TxnMgr.IsXIDActive(xmin) {
				inflightXmin = xmin
				return false, nil
			}
			if isLiveForUniqueCheck(ctx, xmin, tuple.Header.Xmax) {
				liveConflict = true
				conflictPtr = ptr
				return false, nil
			}
			return true, nil
		})
	}

	scanOnce()
	if inflightXmin != storage.InvalidTransactionID {
		// Block until the other transaction commits or rolls back.
		_ = ctx.TxnMgr.WaitForXID(ctx.Ctx, inflightXmin)
		// Re-scan to check whether the row survived.
		scanOnce()
		// If another in-flight xact is holding the slot, treat as live.
		if inflightXmin != storage.InvalidTransactionID {
			liveConflict = true
		}
		if liveConflict {
			if ctx.Tx.Isolation == mvcc.IsolationSerializable {
				// M0118-0001: a post-wait unique conflict between two SERIALIZABLE
				// writers is a serialization failure (40001) only when THIS writer
				// is the pivot of a dangerous rw-structure — i.e. another
				// SERIALIZABLE reader holds a predicate lock covering the
				// conflicting key AND this writer has an out-conflict to an
				// already-committed xact. We must NOT blanket-raise 40001: that
				// over-fires for read-write-unique-4 permutations `r1 w1 w2 …`
				// and `r2 w1 w2 …`, where only one xact read first so there is no
				// pivot and upstream raises a plain 23505 duplicate-key error.
				//
				// Defer the decision to the SSI conflict-in walk against the
				// committed conflicting tuple's location (tuple → page →
				// relation holders, incl. retained-committed readers). It fires
				// 40001 for read-write-unique{,-2,-4} permutation 1 (both xacts
				// read first → the pivot closes to the just-committed peer) and
				// stays silent when no dangerous structure exists, where the
				// duplicate falls through to the 23505 below — mirroring
				// upstream's _bt_check_unique / CheckForSerializableConflictIn
				// ordering.
				if conflictPtr.Offset != 0 {
					if serr := ssiRecordTupleWrite(ctx, rel, conflictPtr.Block, conflictPtr.Offset); serr != nil {
						return serr
					}
					// No dangerous structure → fall through to 23505 below.
				} else {
					// liveConflict was forced by a THIRD still-in-flight xact
					// (no committed location to walk). Preserve the prior
					// conservative serialization failure.
					return &ExecError{
						Code:    "40001",
						Pos:     pos,
						Message: "could not serialize access due to read/write dependencies among transactions",
						Detail:  "Reason code: Canceled on identification as a pivot, during write.",
						Hint:    "The transaction might succeed if retried.",
					}
				}
			} else if ctx.Tx.Isolation == mvcc.IsolationRepeatableRead {
				return &ExecError{
					Code:    "40001",
					Pos:     pos,
					Message: "could not serialize access due to read/write dependencies among transactions",
					Detail:  "Reason code: Canceled on identification as a pivot, during write.",
					Hint:    "The transaction might succeed if retried.",
				}
			}
		} else {
			// Other xact rolled back — no conflict.
			return nil
		}
	}

	if liveConflict {
		return &ExecError{
			Code:    "23505",
			Pos:     pos,
			Message: fmt.Sprintf("duplicate key value violates unique constraint %q", idxName),
			Detail:  detail,
		}
	}
	return nil
}

// buildUniqueConstraintDetail builds the DETAIL string for a 23505 error:
// "Key (col1, col2, ...)=(val1, val2, ...) already exists."
func buildUniqueConstraintDetail(idx *catalog.Index, cols []catalog.Column, row Row) string {
	colNames := make([]string, 0, len(idx.Columns))
	colVals := make([]string, 0, len(idx.Columns))
	for _, idxCol := range idx.Columns {
		colNames = append(colNames, idxCol)
		val := ""
		for i, col := range cols {
			if col.Name == idxCol && i < len(row) {
				val = row[i].Format()
				break
			}
		}
		colVals = append(colVals, val)
	}
	return fmt.Sprintf("Key (%s)=(%s) already exists.",
		strings.Join(colNames, ", "),
		strings.Join(colVals, ", "))
}

// buildInheritColMap returns a mapping from parent column ordinal to child column
// ordinal, matched by name. Returns -1 for parent columns absent from the child.
// Used by inheritance-aware UPDATE/DELETE row remapping. M0097-0078.
func buildInheritColMap(parentCols, childCols []catalog.Column) []int {
	childByName := make(map[string]int, len(childCols))
	for i, c := range childCols {
		childByName[strings.ToLower(c.Name)] = i
	}
	m := make([]int, len(parentCols))
	for i, pc := range parentCols {
		if ci, ok := childByName[strings.ToLower(pc.Name)]; ok {
			m[i] = ci
		} else {
			m[i] = -1
		}
	}
	return m
}

// remapChildRowToParent builds a parent-length Row where position i holds the
// child column value for parent column i (via colMap). Missing entries are NullDatum.
func remapChildRowToParent(childRow Row, colMap []int) Row {
	out := make(Row, len(colMap))
	for i, ci := range colMap {
		if ci >= 0 && ci < len(childRow) {
			out[i] = childRow[ci]
		} else {
			out[i] = NullDatum
		}
	}
	return out
}

// remapParentRowToChild builds a child-length Row where each child column gets
// the updated value from parentRow at the matching parent ordinal (by name).
// Child-only columns (not in parent) are filled from childRaw unchanged.
func remapParentRowToChild(parentRow Row, childRaw Row, parentCols, childCols []catalog.Column) Row {
	parentByName := make(map[string]int, len(parentCols))
	for i, pc := range parentCols {
		parentByName[strings.ToLower(pc.Name)] = i
	}
	out := make(Row, len(childCols))
	for ci, cc := range childCols {
		if pi, ok := parentByName[strings.ToLower(cc.Name)]; ok {
			out[ci] = parentRow[pi]
		} else if ci < len(childRaw) {
			out[ci] = childRaw[ci]
		}
	}
	return out
}

// isLiveForUniqueCheck decides whether a tuple with the given xmin/xmax
// should be considered a live duplicate for INSERT-time uniqueness
// enforcement. Mirrors the conservative reading described on
// `checkUniqueIndexesForInsert`.
// isLiveForUniqueCheck decides whether a tuple with the given xmin/xmax
// should be considered a live duplicate for INSERT-time uniqueness
// enforcement. Mirrors the conservative reading described on
// `checkUniqueIndexesForInsert`.
func isLiveForUniqueCheck(ctx *Context, xmin, xmax storage.TransactionID) bool {
	if xmin == storage.InvalidTransactionID {
		return false
	}
	xminLive := false
	selfXID := ctx.Tx.XID
	if ctx.TxnMgr != nil {
		switch {
		case selfXID != storage.InvalidTransactionID && xmin == selfXID:
			// Inserted by our own xact in this transaction — live.
			xminLive = true
		case ctx.TxnMgr.IsXIDActive(xmin):
			xminLive = true
		case ctx.Snap.SeesCommittedXID(xmin):
			xminLive = true
		case ctx.Snap.HasAborted(xmin):
			xminLive = false
		case ctx.TxnMgr.HasAbortedXID(xmin):
			// Aborted after our snapshot was taken — not a live duplicate.
			// Without this arm the xid falls to `default` (live) and a MERGE
			// NOT MATCHED INSERT that waited for a concurrent aborter would
			// raise a spurious 23505. M0100-0005.
			xminLive = false
		default:
			// Unknown xmin (committed before snapshot start, or a
			// session-self insert that has not yet been added to the
			// snapshot) — treat as live so we err on the side of
			// rejecting the duplicate.
			xminLive = true
		}
	} else {
		xminLive = true
	}
	if !xminLive {
		return false
	}
	if xmax == storage.InvalidTransactionID {
		return true
	}
	// Speculative row self-cancelled: the inserting XID stamped xmax on
	// the row it just inserted (cancelSpeculativeRow). Dead to all
	// observers regardless of the XID's active state.
	if xmin == xmax {
		return false
	}
	// Deletion by our own xact (DELETE then INSERT in the same transaction
	// must succeed).  Without this short-circuit, an in-progress self-xmax
	// would be classified as "still live", and the follow-up INSERT would
	// raise 23505 against the row we just deleted.
	if selfXID != storage.InvalidTransactionID && xmax == selfXID {
		return false
	}
	if ctx.TxnMgr != nil {
		if ctx.TxnMgr.IsXIDActive(xmax) {
			// Concurrent delete by a different live xact — still
			// considered live until that xact commits.
			return true
		}
		if ctx.Snap.HasAborted(xmax) {
			return true
		}
	}
	// xmax appears committed → tuple was deleted by a committed xact;
	// not a live duplicate.
	return false
}

// writeHeapRow encodes the row and appends it to the relation. v0
// always writes to the last block, extending if no tuple fits there.
//
// Persistence: when the buffer pool has a heap-insert change-record
// hook configured (initdb.Open wires this), we use
// `Pool.MarkDirtyChangeRecord` so subsequent inserts on the same
// page in a checkpoint epoch emit a small logical record instead
// of a full FPI. See docs/design/0002-0003-redo-records.md.
func writeHeapRow(ctx *Context, rel storage.RelFileNode, cols []catalog.Column, row Row) error {
	ptr, err := writeHeapRowReturning(ctx, rel, cols, row)
	if err != nil {
		return err
	}
	return emitCanonicalHeapInsert(ctx, rel, ptr)
}

// writeHeapRowReturning is writeHeapRow's variant that surfaces the
// (block, slot) of the freshly-inserted tuple so callers that need
// to maintain secondary structures (UPSERT's arbiter index) can
// stitch the new ItemPointer into them. The non-returning variant
// is preserved for INSERT / UPDATE callers that don't need the
// location.
func writeHeapRowReturning(ctx *Context, rel storage.RelFileNode, cols []catalog.Column, row Row) (storage.ItemPointer, error) {
	var ptr storage.ItemPointer

	// M0093: lazily materialise the transaction's XID before any
	// xmin stamp. ToastLargeColumnsIfNeeded may itself call
	// NewHeapTuple for the TOAST chunk relation; doing this at the
	// top covers both the TOAST writes and the main-heap NewHeapTuple
	// below.
	if err := ctx.MaterializeWriterXID(); err != nil {
		return ptr, err
	}

	// TOAST oversized column values before encoding (M0046-0006).
	var toastErr error
	row, toastErr = ToastLargeColumnsIfNeeded(ctx, rel, cols, row)
	if toastErr != nil {
		return ptr, &ExecError{Code: "XX000", Message: toastErr.Error()}
	}

	// Always encode in PG-native physical format (M0111-0002): a single
	// on-disk heap-tuple format, byte-valid for a PG standby's
	// heap_deform_tuple. goopg reads it back by selecting the decoder from the
	// tuple header (natts/bitmap) in DecodeRowIntoMctxPGTuple.
	body, encErr := EncodeRowPG(cols, row)
	if encErr != nil {
		// Preserve ExecError (e.g. 22P02 invalid input, 22003 out of range)
		// so the SQLSTATE and message reach the client unchanged.
		var ee *ExecError
		if errors.As(encErr, &ee) {
			return ptr, ee
		}
		return ptr, &ExecError{Code: "XX000", Message: encErr.Error()}
	}
	bitmap := NullBitmapPG(row)
	// xmin = the effective writer XID: inside an open savepoint this is the
	// current sub-XID, so a row inserted in a savepoint (incl. an UPDATE's new
	// version) disappears when that savepoint is rolled back — its sub-XID flips
	// aborted and the subxact-aware visibility check hides it (aborted-keyrevoke,
	// M0118-0009). Outside a savepoint it equals ctx.Tx.XID, so this is a no-op.
	xmin := effectiveWriterXID(ctx)
	var tuple storage.HeapTuple
	if len(bitmap) > 0 {
		tuple = storage.NewHeapTupleWithNulls(xmin, storage.InvalidTransactionID, bitmap, body)
	} else {
		tuple = storage.NewHeapTuple(xmin, storage.InvalidTransactionID, body)
	}
	tuple.Header.SetNatts(len(cols))
	tuple.Header.Infomask |= storage.HeapXmaxInvalid
	tupleBytes, err := tuple.MarshalBinary()
	if err != nil {
		return ptr, err
	}

	// Always emit the native RecordKindHeapInsert WAL record so the logical
	// decoder sees the change. When ctx.LogCanonical != nil, the caller also
	// emits a canonical XLOG_HEAP_INSERT (FPI) via emitCanonicalHeapInsert.
	// PG physical standbys skip the native record; goopg's classifier skips
	// the canonical one. Both coexist safely in the WAL stream.
	logHeap := ctx.Pool.LogHeapInsert()
	tryAppendToBlock := func(blk storage.BlockNumber) (bool, error) {
		slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return false, err
		}
		// Hold the page's content lock across the
		// IsNew/InitPage/PageAddHeapTuple read-modify-write window
		// so concurrent writers serialise; without it, two writers
		// to the same block compute the same upper offset, both
		// memcpy their tuple over the same bytes, and the
		// later-rewritten line pointer points at a half-overwritten
		// payload — the "invalid t_hoff=0" symptom.
		slot.Lock()
		if storage.IsNew(slot.Page()) {
			if err := storage.InitPage(slot.Page()); err != nil {
				slot.Unlock()
				ctx.Pool.Unpin(slot)
				return false, err
			}
		}
		if lineSlot, err := storage.PageAddHeapTuple(slot.Page(), tuple); err == nil {
			derr := markHeapInsertDirty(ctx.Pool, slot, logHeap, rel, blk, lineSlot, tupleBytes)
			// Update FSM with remaining free space (M0046-0003).
			if ctx.FSM != nil {
				ctx.FSM.RecordFreeSpaceForPage(rel, blk, slot.Page())
			}
			// Clear VM: page was modified, no longer ALL_VISIBLE (M0046-0004).
			if ctx.VM != nil {
				ctx.VM.ClearBlock(rel, blk)
			}
			slot.Unlock()
			ctx.Pool.Unpin(slot)
			if derr == nil {
				ptr = storage.ItemPointer{Block: blk, Offset: lineSlot}
			}
			return true, derr
		} else if !errors.Is(err, storage.ErrNoSpaceInPage) {
			slot.Unlock()
			ctx.Pool.Unpin(slot)
			return false, err
		}
		// Page full: invalidate FSM entry so future lookups skip it.
		if ctx.FSM != nil {
			ctx.FSM.RecordFreeSpace(rel, blk, 0)
		}
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		return false, nil
	}

	// FSM consultation: ask the FSM for up to candidatesPerInsert pages
	// with enough room and pick the one with the lowest live pin count
	// (M0107-0007 slice C — pin-aware page selection per
	// docs/design/perf-optimize/07-wal-fsm-insert.md §3). Returns
	// (0, false) when every candidate is at or above hotPinThreshold,
	// signalling the caller to fall through to extension instead of
	// converging on a hot tail page.
	minFreeBytes := uint16(len(tupleBytes) + 4) // 4 = itemIDSize (line pointer size)
	if fsmBlk, ok := selectFSMCandidatePage(ctx.FSM, ctx.Pool, rel, minFreeBytes); ok {
		appended, err := tryAppendToBlock(fsmBlk)
		if err != nil {
			return ptr, err
		}
		if appended {
			return ptr, nil
		}
		// Stale FSM entry — invalidation was already done in
		// tryAppendToBlock above; fall through to normal path.
	}

	// Try the last existing block first.
	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return ptr, err
	}
	if nBlocks > 0 {
		appended, err := tryAppendToBlock(nBlocks - 1)
		if err != nil {
			return ptr, err
		}
		if appended {
			return ptr, nil
		}
	}

	// Extend. Serialise relation extension so concurrent writers don't
	// race on PinNew and corrupt pin accounting for the freshly-grown
	// tail block under heavy insert workloads. Striped 8-way by procNum
	// per M0107-0007 slice A so up to 8 stripes can extend in parallel;
	// `contended` is set when our TryLock failed, the proxy for "another
	// stripe-mate is already extending" that gates batched-extend per
	// M0107-0007 slice C ([[0107-0007g]] §3).
	unlock, contended := lockHeapExtend(rel, ctx.ProcNum)
	defer unlock()

	// Re-consult the FSM after taking the extend lock — a concurrent
	// stripe may have just batch-extended and registered fresh
	// candidates ([[0107-0007g]] §3 step 5). Picking one of those
	// extras here is the cross-stripe distribution mechanism: without
	// it we would extend again and converge on a new tail page.
	if fsmBlk, ok := selectFSMCandidatePage(ctx.FSM, ctx.Pool, rel, minFreeBytes); ok {
		appended, err := tryAppendToBlock(fsmBlk)
		if err != nil {
			return ptr, err
		}
		if appended {
			return ptr, nil
		}
	}

	// Tail-block re-check after taking the lock; another writer may
	// already have extended and/or inserted into the new tail block.
	nBlocks, err = ctx.Pool.NBlocks(rel)
	if err != nil {
		return ptr, err
	}
	if nBlocks > 0 {
		appended, err := tryAppendToBlock(nBlocks - 1)
		if err != nil {
			return ptr, err
		}
		if appended {
			return ptr, nil
		}
	}

	if contended {
		// Batch-extend: under contention, append extendBatchSize empty
		// pages in one syscall and FSM-register the extras so
		// subsequent inserters spread across them (M0107-0007 slice C
		// — [[0107-0007g]]). One disk-side syscall covers the burst
		// instead of one per stripe; the extras prime the cross-stripe
		// FSM re-check above.
		firstBlk, err := batchExtendAndRegisterFSM(ctx.Pool, ctx.FSM, rel)
		if err != nil {
			return ptr, err
		}
		appended, err := tryAppendToBlock(firstBlk)
		if err != nil {
			return ptr, err
		}
		if appended {
			return ptr, nil
		}
		// Unreachable: ExtendRelationBatch pre-inits every page with
		// max free space, so PageAddHeapTuple cannot return
		// ErrNoSpaceInPage on the first insert. A non-nil err would
		// have been returned by tryAppendToBlock above.
		return ptr, &ExecError{Code: "XX000", Message: "freshly batch-extended page did not accept tuple"}
	}

	// Uncontended single-extender path: PinNew avoids the post-extend
	// disk read of the batched primitive (the slot is pinned + dirty
	// in-memory before we ever touch the disk for content) and adds
	// only one page, matching the test-fixture invariant that a single
	// INSERT into a fresh relation grows it by exactly one page.
	slot, blk, err := ctx.Pool.PinNew(rel)
	if err != nil {
		return ptr, err
	}
	slot.Lock()
	lineSlot, err := storage.PageAddHeapTuple(slot.Page(), tuple)
	if err != nil {
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		return ptr, err
	}
	derr := markHeapInsertDirty(ctx.Pool, slot, logHeap, rel, blk, lineSlot, tupleBytes)
	// New page: record its free space in the FSM (M0046-0003).
	if ctx.FSM != nil {
		ctx.FSM.RecordFreeSpaceForPage(rel, blk, slot.Page())
	}
	// New page starts dirty — not ALL_VISIBLE (M0046-0004).
	if ctx.VM != nil {
		ctx.VM.ClearBlock(rel, blk)
	}
	slot.Unlock()
	ctx.Pool.Unpin(slot)
	if derr == nil {
		ptr = storage.ItemPointer{Block: blk, Offset: lineSlot}
	}
	return ptr, derr
}

// writeHeapRowReturningPG is identical to writeHeapRowReturning but encodes the
// row using PG-native format (EncodeRowPG + NullBitmapPG) instead of goopg's
// internal format. It also skips TOAST processing because catalog rows
// (pg_class, pg_attribute) are always small. This variant must be used when
// writing catalog rows that a vanilla PG standby will replay. M0106-0010.
func writeHeapRowReturningPG(ctx *Context, rel storage.RelFileNode, cols []catalog.Column, row Row) (storage.ItemPointer, error) {
	var ptr storage.ItemPointer

	if err := ctx.MaterializeWriterXID(); err != nil {
		return ptr, err
	}

	body, err := EncodeRowPG(cols, row)
	if err != nil {
		return ptr, &ExecError{Code: "XX000", Message: err.Error()}
	}
	bitmap := NullBitmapPG(row)
	// xmin = effective writer XID (sub-XID inside an open savepoint) so a
	// savepoint-inserted row disappears on ROLLBACK TO; no-op outside a
	// savepoint. M0118-0009 — twin of writeHeapRowReturning above.
	xmin := effectiveWriterXID(ctx)
	var tuple storage.HeapTuple
	if len(bitmap) > 0 {
		tuple = storage.NewHeapTupleWithNulls(xmin, storage.InvalidTransactionID, bitmap, body)
	} else {
		tuple = storage.NewHeapTuple(xmin, storage.InvalidTransactionID, body)
	}
	// Set t_infomask2 natts so PG's heap_deform_tuple can locate each attribute.
	// Set HEAP_XMAX_INVALID so PG's visibility code treats xmax as invalid
	// without testing the XID. Both are required for correct PG-standby reads.
	// M0106-0010 batched-36.
	tuple.Header.SetNatts(len(cols))
	tuple.Header.Infomask |= storage.HeapXmaxInvalid
	// HEAP_HASVARWIDTH: mirrors PG's heap_fill_tuple — set when the
	// tuple contains at least one non-null varlena value. PG18's
	// nocachegetattr fast path (heaptuple.c:642 — `Assert(j > attnum)`)
	// crashes if the bit is missing while the TupleDesc still
	// considers the catalog to have varlena attrs on the prefix
	// leading to the target attnum (e.g. relacl/reloptions/relpartbound
	// on pg_class). M0106-0010 batched-49.
	if pgRowHasVarWidth(cols, row) {
		tuple.Header.Infomask |= storage.HeapHasVarWidth
	}
	tupleBytes, err := tuple.MarshalBinary()
	if err != nil {
		return ptr, err
	}

	logHeap := ctx.Pool.LogHeapInsert()
	if ctx.LogCanonical != nil {
		logHeap = nil
	}
	tryAppendToBlock := func(blk storage.BlockNumber) (bool, error) {
		slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return false, err
		}
		slot.Lock()
		if storage.IsNew(slot.Page()) {
			if err := storage.InitPage(slot.Page()); err != nil {
				slot.Unlock()
				ctx.Pool.Unpin(slot)
				return false, err
			}
		}
		if lineSlot, err := storage.PageAddHeapTuple(slot.Page(), tuple); err == nil {
			derr := markHeapInsertDirty(ctx.Pool, slot, logHeap, rel, blk, lineSlot, tupleBytes)
			if ctx.FSM != nil {
				ctx.FSM.RecordFreeSpaceForPage(rel, blk, slot.Page())
			}
			if ctx.VM != nil {
				ctx.VM.ClearBlock(rel, blk)
			}
			slot.Unlock()
			ctx.Pool.Unpin(slot)
			if derr == nil {
				ptr = storage.ItemPointer{Block: blk, Offset: lineSlot}
			}
			return true, derr
		} else if !errors.Is(err, storage.ErrNoSpaceInPage) {
			slot.Unlock()
			ctx.Pool.Unpin(slot)
			return false, err
		}
		if ctx.FSM != nil {
			ctx.FSM.RecordFreeSpace(rel, blk, 0)
		}
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		return false, nil
	}

	// FSM consultation with pin-aware ranking (M0107-0007 slice C); same
	// rewrite as writeHeapRowReturning above, applied to the PG-canonical
	// path. See docs/design/perf-optimize/07-wal-fsm-insert.md §3.
	minFreeBytes := uint16(len(tupleBytes) + 4)
	if fsmBlk, ok := selectFSMCandidatePage(ctx.FSM, ctx.Pool, rel, minFreeBytes); ok {
		appended, err := tryAppendToBlock(fsmBlk)
		if err != nil {
			return ptr, err
		}
		if appended {
			return ptr, nil
		}
	}

	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return ptr, err
	}
	if nBlocks > 0 {
		appended, err := tryAppendToBlock(nBlocks - 1)
		if err != nil {
			return ptr, err
		}
		if appended {
			return ptr, nil
		}
	}

	// Striped extend lock (M0107-0007 slice A) + cross-stripe FSM
	// re-check + adaptive single-vs-batched extend tail, mirroring the
	// canonical writeHeapRowReturning above. See [[0107-0007g]] for the
	// rationale.
	unlock, contended := lockHeapExtend(rel, ctx.ProcNum)
	defer unlock()

	if fsmBlk, ok := selectFSMCandidatePage(ctx.FSM, ctx.Pool, rel, minFreeBytes); ok {
		appended, err := tryAppendToBlock(fsmBlk)
		if err != nil {
			return ptr, err
		}
		if appended {
			return ptr, nil
		}
	}

	nBlocks, err = ctx.Pool.NBlocks(rel)
	if err != nil {
		return ptr, err
	}
	if nBlocks > 0 {
		appended, err := tryAppendToBlock(nBlocks - 1)
		if err != nil {
			return ptr, err
		}
		if appended {
			return ptr, nil
		}
	}

	if contended {
		firstBlk, err := batchExtendAndRegisterFSM(ctx.Pool, ctx.FSM, rel)
		if err != nil {
			return ptr, err
		}
		appended, err := tryAppendToBlock(firstBlk)
		if err != nil {
			return ptr, err
		}
		if appended {
			return ptr, nil
		}
		return ptr, &ExecError{Code: "XX000", Message: "freshly batch-extended page did not accept tuple"}
	}

	slot, blk, err := ctx.Pool.PinNew(rel)
	if err != nil {
		return ptr, err
	}
	slot.Lock()
	lineSlot, err := storage.PageAddHeapTuple(slot.Page(), tuple)
	if err != nil {
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		return ptr, err
	}
	derr := markHeapInsertDirty(ctx.Pool, slot, logHeap, rel, blk, lineSlot, tupleBytes)
	if ctx.FSM != nil {
		ctx.FSM.RecordFreeSpaceForPage(rel, blk, slot.Page())
	}
	if ctx.VM != nil {
		ctx.VM.ClearBlock(rel, blk)
	}
	slot.Unlock()
	ctx.Pool.Unpin(slot)
	if derr == nil {
		ptr = storage.ItemPointer{Block: blk, Offset: lineSlot}
	}
	return ptr, derr
}

// markHeapInsertDirty centralises the choice between
// MarkDirtyChangeRecord (when a heap-insert WAL hook is wired)
// and the conservative fallback MarkDirty (when none is). The
// caller must hold slot.Lock; the change-record path also reads
// the page bytes inline, which is safe under exclusive content
// latch.
func markHeapInsertDirty(
	pool *storage.Pool, slot *storage.Slot,
	logHeap storage.LogHeapInsertFunc,
	rel storage.RelFileNode, blk storage.BlockNumber,
	lineSlot uint16, tupleBytes []byte,
) error {
	if logHeap == nil {
		pool.MarkDirty(slot)
		return nil
	}
	// MarkDirtyLogicalChange (not MarkDirtyChangeRecord): the logical
	// HeapInsert record MUST always be emitted so the M0008 logical
	// decoder sees the per-row change. MarkDirtyChangeRecord would
	// suppress the logical record on first-dirty-in-epoch in favour
	// of a bare PageImage — fine for redo, fatal for logical
	// replication. See docs/design/0103-0018-heap-fpi-and-logical-record-coexistence.md.
	return pool.MarkDirtyLogicalChange(slot, func() (storage.LSN, error) {
		return logHeap(rel, blk, lineSlot, tupleBytes)
	})
}

// markHeapDeleteDirty mirrors markHeapInsertDirty for the xmax
// stamp paths (UPDATE old image + DELETE). oldTuple carries the
// pre-delete heap-tuple bytes for logical replication; pass nil
// when not needed (DDL, UPSERT). When the pool has a LogHeapDelete
// hook configured, subsequent dirties of the same page in an epoch
// emit a logical record instead of a full FPI.
func markHeapDeleteDirty(
	pool *storage.Pool, slot *storage.Slot,
	rel storage.RelFileNode, blk storage.BlockNumber,
	lineSlot uint16, xmax storage.TransactionID,
	oldTuple []byte,
) error {
	logDel := pool.LogHeapDelete()
	if logDel == nil {
		pool.MarkDirty(slot)
		return nil
	}
	// MarkDirtyLogicalChange — see markHeapInsertDirty for the
	// rationale.
	return pool.MarkDirtyLogicalChange(slot, func() (storage.LSN, error) {
		return logDel(rel, blk, lineSlot, xmax, oldTuple)
	})
}

// markHeapDeleteDirtyAndClearVM is markHeapDeleteDirty + VM clear (M0046-0004).
// Any page that has a tuple deleted is no longer ALL_VISIBLE.
func markHeapDeleteDirtyAndClearVM(
	ctx *Context, slot *storage.Slot,
	rel storage.RelFileNode, blk storage.BlockNumber,
	lineSlot uint16, xmax storage.TransactionID,
	oldTuple []byte,
) error {
	// Always emit the native RecordKindHeapDelete WAL record so the logical
	// decoder (classifier.go) sees the change for pgoutput/logical replication.
	// When ctx.LogCanonical != nil, the caller additionally emits a canonical
	// XLOG_HEAP_DELETE record (FPI) after unpinning the slot. PG physical
	// standbys safely skip the native record (classifyXLogRecord routes it via
	// RmgrXLog/0xF0) and apply the canonical FPI; goopg's classifier skips the
	// canonical record and processes the native one.
	if err := markHeapDeleteDirty(ctx.Pool, slot, rel, blk, lineSlot, xmax, oldTuple); err != nil {
		return err
	}
	if ctx.VM != nil {
		ctx.VM.ClearBlock(rel, blk)
	}
	return nil
}

// emitCanonicalHeapInsert emits a PG-canonical XLOG_HEAP_INSERT record with
// a full-page image for the page at ptr. Called by insertOp and updateOp
// (for the new-tuple page) after writeHeapRowReturning, when ctx.LogCanonical
// is non-nil. The page is re-pinned to capture its post-insert state.
func emitCanonicalHeapInsert(ctx *Context, rel storage.RelFileNode, ptr storage.ItemPointer) error {
	if ctx.LogCanonical == nil || ctx.Pool == nil {
		return nil
	}
	slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: ptr.Block})
	if err != nil {
		return fmt.Errorf("canonical WAL pin insert: %w", err)
	}
	page := make(storage.Page, storage.BlockSize)
	slot.Lock()
	copy(page, slot.Page())
	endLSN, emitErr := catalog.PgCanonicalHeapInsert(rel, ptr.Block, page, ptr.Offset,
		uint32(ctx.Tx.XID), ctx.LogCanonical)
	if emitErr == nil && endLSN != 0 {
		storage.MustHeader(slot.Page()).SetLSN(storage.LSN(endLSN))
		ctx.Pool.MarkDirty(slot)
	}
	slot.Unlock()
	ctx.Pool.Unpin(slot)
	return emitErr
}

// emitCanonicalHeapDelete emits a PG-canonical XLOG_HEAP_DELETE record with
// a full-page image for the page at (rel, blk). Called by deleteOp and
// updateOp (for the old-tuple/xmax-stamp page) after
// markHeapDeleteDirtyAndClearVM and the slot is unpinned, when
// ctx.LogCanonical is non-nil. The page is re-pinned to capture its
// post-xmax-stamp state.
func emitCanonicalHeapDelete(ctx *Context, rel storage.RelFileNode, blk storage.BlockNumber, slot uint16) error {
	if ctx.LogCanonical == nil || ctx.Pool == nil {
		return nil
	}
	s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
	if err != nil {
		return fmt.Errorf("canonical WAL pin delete: %w", err)
	}
	page := make(storage.Page, storage.BlockSize)
	s.Lock()
	copy(page, s.Page())
	xid := uint32(ctx.Tx.XID)
	endLSN, emitErr := catalog.PgCanonicalHeapDelete(rel, blk, page, slot, xid, xid, ctx.LogCanonical)
	if emitErr == nil && endLSN != 0 {
		storage.MustHeader(s.Page()).SetLSN(storage.LSN(endLSN))
		ctx.Pool.MarkDirty(s)
	}
	s.Unlock()
	ctx.Pool.Unpin(s)
	return emitErr
}
