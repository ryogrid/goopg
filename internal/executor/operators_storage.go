package executor

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/activity"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/mctx"
	"github.com/goopg/goopg/internal/multixact"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/pgarray"
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

// errTupleAlreadyModified builds PostgreSQL's TM_SelfModified error for a DML
// statement that reached — via an EvalPlanQual chain-follow — a row version a
// LATER command of its own execution wrote. Upstream raises it from
// nodeModifyTable.c `ExecUpdate` (line 2656 in PG 18.3) and `ExecDelete` with
// ERRCODE_TRIGGERED_DATA_CHANGE_VIOLATION, whose SQLSTATE is 27000 (09000 is
// ERRCODE_TRIGGERED_ACTION_EXCEPTION — a different class; goopg used it until
// this was first exercised end-to-end). Message and hint are verbatim upstream.
//
// A constructor rather than a package-level sentinel because the Pos varies per
// call site and *ExecError is mutable: stamping Pos on a shared value would be
// a data race between concurrent backends.
func errTupleAlreadyModified(verb string, pos int) *ExecError {
	return &ExecError{
		Code:    "27000",
		Pos:     pos,
		Message: "tuple to be " + verb + " was already modified by an operation triggered by the current command",
		Hint:    "Consider using an AFTER trigger instead of a BEFORE trigger to propagate changes to other rows.",
	}
}

// wfgDebug dumps the exact wait-for cycle to stderr whenever
// registerWFGAndCheckCycle reports a deadlock (`GOOPG_DEBUG_WFG=1`).
//
// It exists because the WFG's deadlock verdict is only as good as its edges,
// and a wrong edge is indistinguishable from a real cycle at the call site:
// the caller just sees `dl == true` and raises 40001. pgbench TPC-B is
// deadlock-free by construction (every transaction takes its row locks in the
// fixed order accounts → tellers → branches, so wait-for edges only ever point
// "forward" and cannot close), yet the nightly stage reports thousands of
// `could not serialize access due to concurrent update (deadlock)` errors —
// so the reported cycles must contain at least one edge that does not
// correspond to a live wait. Printing the participants (and the age of each
// edge) is the only way to identify which one. Off by default; this is a
// diagnostic for AI-20260810-011258-006, not a supported log channel.
var wfgDebug = os.Getenv("GOOPG_DEBUG_WFG") == "1"

// wfgEdgeInfo is the per-edge provenance kept only while wfgDebug is on: when
// the edge was registered and which epqWait call site created it. Held in a
// side map so the hot path keeps its plain XID→XID edge.
type wfgEdgeInfo struct {
	since  time.Time
	site   string
	target string
}

// wfgDebugTarget records, per waiting XID, the exact tuple its next epqWait is
// about to block on. Written by wfgNoteTarget at the call site (which has the
// RelFileNode/block/slot epqWait itself never sees) and folded into the cycle
// dump. Debug-only (GOOPG_DEBUG_WFG=1).
var wfgDebugTarget = make(map[storage.TransactionID]string)

func wfgNoteTarget(xid storage.TransactionID, rel storage.RelFileNode, blk storage.BlockNumber, slot uint16, h storage.HeapTupleHeader) {
	// superseded=true means the version we are about to block on already has a
	// successor (t_ctid points elsewhere): PG would have walked the chain to the
	// head before deciding whom to wait for, so an edge derived here is an edge
	// to a transaction that may not hold the row's current version at all.
	superseded := h.CTID.Block != blk || h.CTID.Offset != slot
	t := fmt.Sprintf("rel=%d blk=%d slot=%d xmin=%d xmax=%d ctid=(%d,%d) superseded=%v im=%#x",
		rel.RelOid, blk, slot, h.Xmin, h.Xmax, h.CTID.Block, h.CTID.Offset, superseded, h.Infomask)
	wfgMu.Lock()
	wfgDebugTarget[xid] = t
	wfgMu.Unlock()
}

var wfgDebugInfo = make(map[storage.TransactionID]wfgEdgeInfo)

// wfgDebugActivity is the pg_stat_activity registry, captured from the first
// waiting Context so the cycle dump can name the SQL each participant is
// running. Debug-only; nil until the first wait under GOOPG_DEBUG_WFG=1.
var wfgDebugActivity atomic.Pointer[activity.ActivityRegistry]

// registerWFGAndCheckCycle adds the edge myXID→blockingXID and walks the
// graph up to maxWFGHops looking for a cycle (deadlock). Returns true when
// a cycle is detected; the edge is removed before returning (caller must NOT
// call deregisterWFG). Returns false when no cycle is found; the caller must
// call deregisterWFG after the wait completes.
func registerWFGAndCheckCycle(myXID, blockingXID storage.TransactionID) bool {
	wfgMu.Lock()
	defer wfgMu.Unlock()
	waitForGraph[myXID] = blockingXID
	if wfgDebug {
		wfgDebugInfo[myXID] = wfgEdgeInfo{since: time.Now(), site: wfgCallSite(), target: wfgDebugTarget[myXID]}
	}
	cur := blockingXID
	for i := 0; i < maxWFGHops; i++ {
		if cur == myXID {
			if wfgDebug {
				dumpWFGCycle(myXID, blockingXID)
				delete(wfgDebugInfo, myXID)
			}
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

// dumpWFGCycle prints the cycle myXID→blockingXID→…→myXID that
// registerWFGAndCheckCycle just closed. Called with wfgMu held.
func dumpWFGCycle(myXID, blockingXID storage.TransactionID) {
	var b strings.Builder
	now := time.Now()
	edge := func(from storage.TransactionID) string {
		i, ok := wfgDebugInfo[from]
		if !ok {
			return "(no info)"
		}
		return i.site + " " + i.target + " age=" + now.Sub(i.since).Round(time.Microsecond).String()
	}
	fmt.Fprintf(&b, "WFG deadlock: %d [%s]", myXID, edge(myXID))
	cur := blockingXID
	for i := 0; i < maxWFGHops; i++ {
		fmt.Fprintf(&b, " -> %d", cur)
		if cur == myXID {
			break
		}
		fmt.Fprintf(&b, " [%s]", edge(cur))
		next, ok := waitForGraph[cur]
		if !ok {
			b.WriteString(" -> (no edge)")
			break
		}
		cur = next
	}
	fmt.Fprintf(&b, "  [graph size %d]\n", len(waitForGraph))
	if reg := wfgDebugActivity.Load(); reg != nil {
		for _, be := range reg.Snapshot() {
			if be.BackendXID == "" || be.BackendXID == "0" {
				continue
			}
			x, err := strconv.ParseUint(be.BackendXID, 10, 64)
			if err != nil {
				continue
			}
			xid := storage.TransactionID(x)
			if xid != myXID {
				if _, onPath := wfgDebugInfo[xid]; !onPath {
					continue
				}
			}
			fmt.Fprintf(&b, "    xid %d: %s\n", xid, strings.Join(strings.Fields(be.Query), " "))
		}
	}
	os.Stderr.WriteString(b.String())
}

// deregisterWFG removes myXID from the wait-for graph after a snapshot
// refresh completes.
func deregisterWFG(myXID storage.TransactionID) {
	wfgMu.Lock()
	delete(waitForGraph, myXID)
	if wfgDebug {
		delete(wfgDebugInfo, myXID)
	}
	wfgMu.Unlock()
}

// wfgCallSite names the epqWait/waitPgClassInplaceXID caller that created the
// current edge (skip: wfgCallSite, registerWFGAndCheckCycle, epqWait).
func wfgCallSite() string {
	if _, file, line, ok := runtime.Caller(3); ok {
		return filepath.Base(file) + ":" + strconv.Itoa(line)
	}
	return "?"
}

// waitPgClassInplaceXID is the deadlock-aware wait that the intra-grant-inplace
// pg_class virtual-tuple locks serialise on. goopg has no real pg_class heap
// tuple, so GRANT/REVOKE (the ACL-change "xmax"), explicit rowmarks
// (SELECT … FROM pg_class … FOR …), and the in-place relhasindex update
// (ALTER TABLE ADD PRIMARY KEY → heap_inplace_update) all serialise on a
// recorded writer XID instead of a heavyweight tuple lock. Before blocking on
// blockingXID we register the edge ctx.Tx.XID→blockingXID in the shared
// wait-for graph and walk it for a cycle; a cycle is a deadlock (e.g.
// intra-grant-inplace permutation `b2 sfnku2 b1 grant1 addk2`, where GRANT
// awaits the rowmark's xmax while ADD PRIMARY KEY blocks behind GRANT) and the
// caller raises SQLSTATE 40P01. Otherwise we block on WaitForXID until the
// holder commits or aborts. The caller must have materialised its own writer
// XID so the edge it registers is observable by the peer closing the cycle.
// Design 0118-0114 (intra-grant-inplace perms 7-8).
func waitPgClassInplaceXID(ctx *Context, blockingXID storage.TransactionID) (deadlock bool, timeout *ExecError) {
	if ctx == nil || ctx.TxnMgr == nil || ctx.Ctx == nil {
		return false, nil
	}
	if blockingXID == storage.InvalidTransactionID || blockingXID == ctx.Tx.XID {
		return false, nil
	}
	if ctx.Tx.XID != storage.InvalidTransactionID {
		if registerWFGAndCheckCycle(ctx.Tx.XID, blockingXID) {
			// We are the deadlock victim. Flag the statement so the wire
			// dispatch layer aborts this transaction's XID in place on the
			// failure path, unblocking the peer that is waiting on our
			// catalog-tuple xmax immediately rather than at our explicit
			// ROLLBACK. Design 0118-0115 (intra-grant-inplace perm 8).
			ctx.DeadlockVictim = true
			return true, nil
		}
		defer deregisterWFG(ctx.Tx.XID)
	}
	if werr := ctx.TxnMgr.WaitForXID(ctx.Ctx, blockingXID); werr != nil {
		if ee := lockWaitTimeoutError(werr); ee != nil {
			return false, ee
		}
		// Plain cancellation (connection close / statement timeout via ctx):
		// fall through; the caller proceeds as if the holder finished.
	}
	return false, nil
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
// The second result, timeout, is non-nil when the wait was aborted by the
// session's lock_timeout or statement_timeout: the caller must surface it
// (SQLSTATE 57014) instead of retrying. A plain client cancellation is still
// swallowed (returns false, nil) so connection teardown falls through to the
// snapshot refresh + caller retry as before. M0118-0009.
func epqWait(ctx *Context, xmax storage.TransactionID) (deadlock bool, timeout *ExecError) {
	if ctx.TxnMgr == nil {
		return false, nil
	}
	if ctx.Tx.XID != storage.InvalidTransactionID {
		if wfgDebug && ctx.Activity != nil {
			wfgDebugActivity.CompareAndSwap(nil, ctx.Activity)
		}
		if registerWFGAndCheckCycle(ctx.Tx.XID, xmax) {
			return true, nil
		}
		defer deregisterWFG(ctx.Tx.XID)
	}
	// PG parity: block until the holder transaction commits or aborts.
	// All four call sites release page pins before reaching here
	// (verified at lines 923-924, 1159-1160, 1333-1334, 1520-1521).
	if ctx.Ctx != nil {
		if werr := ctx.TxnMgr.WaitForXID(ctx.Ctx, xmax); werr != nil {
			if ee := lockWaitTimeoutError(werr); ee != nil {
				return false, ee
			}
			// Plain cancellation: treat as a non-deadlock signal and fall
			// through to the snapshot refresh + caller retry.
		}
	}
	// Refresh the snapshot so the next epqRecheckVisible call sees any
	// committed changes from the conflicting transaction.
	if snap, serr := ctx.TxnMgr.SnapshotFor(ctx.Tx); serr == nil {
		ctx.Snap = snap.Clone()
	}
	return false, nil
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
	return mvcc.TupleVisible(tup.Header, ctx.Snap, ctx.Tx.XID, ctx.CmdID, ctx.comboStore(), ctx.MultiXact), nil
}

// epqXmaxSettled classifies a concurrent modifier xid for an EvalPlanQual
// recheck under REPEATABLE READ / SERIALIZABLE, using the transaction manager
// authoritatively rather than the (frozen) snapshot's InProgress set. It must be
// called only AFTER epqWait has returned, i.e. xmax is no longer running.
//
// The trap it closes: a transaction that began AFTER our frozen RR/SSI snapshot
// is absent from snap.InProgress, so the legacy "absent ⇒ aborted" shortcut
// (`!snap.HasInProgress(xmax)`) misclassifies a concurrently-COMMITTED updater
// as aborted and proceeds with our write — a silent lost update where PG raises
// SQLSTATE 40001. Consulting the manager makes the decision robust to the timing
// at which the RR/SSI snapshot was pinned. 0118-0105.
//
// Returns aborted=true when xmax rolled back (caller proceeds with its write);
// committed=true when xmax committed (caller raises 40001); both false when xmax
// is still active (caller retries). When no manager is wired (unit-test paths)
// both are false so callers fall back to their legacy snapshot heuristic.
func epqXmaxSettled(ctx *Context, xmax storage.TransactionID) (aborted, committed bool) {
	if ctx.TxnMgr == nil {
		return false, false
	}
	if ctx.TxnMgr.HasAbortedXID(xmax) {
		return true, false
	}
	if ctx.TxnMgr.IsXIDActive(xmax) {
		return false, false
	}
	return false, true
}

// epqSerializationErr builds the SQLSTATE 40001 error for an EvalPlanQual abort,
// distinguishing a concurrent DELETE from a concurrent UPDATE by the original
// tuple's CTID: goopg leaves a DELETE'd tuple's CTID at the initial
// {InvalidBlockNumber,0} (stampOldCtid only runs on UPDATE), so an
// InvalidBlockNumber block means the row was deleted. 0118-0105.
func epqSerializationErr(ctx *Context, rel storage.RelFileNode, blk storage.BlockNumber, slot uint16, pos int) *ExecError {
	errMsg := "could not serialize access due to concurrent update"
	if sp, perr := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk}); perr == nil {
		sp.RLock()
		if ot, gerr := storage.PageGetHeapTuple(sp.Page(), slot); gerr == nil {
			// A never-updated row is a chain tail — self-pointing t_ctid (PG
			// convention) or the legacy {Invalid,0} sentinel; DELETE leaves
			// t_ctid untouched. A real successor t_ctid means UPDATE.
			if isChainTailCTID(ot.Header.CTID, blk, slot) {
				errMsg = "could not serialize access due to concurrent delete"
			}
		}
		sp.RUnlock()
		ctx.Pool.Unpin(sp)
	}
	return &ExecError{Code: "40001", Pos: pos, Message: errMsg}
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
	latestTup, latestSlot, found := followHOTChain(s.Page(), slot, ctx.Snap, ctx.Tx.XID, ctx.MultiXact, ctx.CmdID, ctx.comboStore())
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

// maxCTIDChainWalk bounds the CROSS-page t_ctid chain walks below. Unlike a
// HOT chain (one page, so storage.MaxHeapTuplesPerPage is the exact bound), a
// non-HOT update chain spans pages and has no structural length limit — PG's
// equivalents (heap_get_latest_tid, heap_lock_updated_tuple_rec) loop with no
// cap at all and rely on the chain being acyclic. This is therefore a pure
// corruption backstop, deliberately far above any reachable chain length: the
// arbitrary 64 it replaced (M0131-S32) silently truncated real chains, which
// is a wrong answer, whereas an over-large backstop costs only time on an
// already-corrupt page.
const maxCTIDChainWalk = 1 << 20

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
	const maxChain = maxCTIDChainWalk
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
	const maxChain = maxCTIDChainWalk
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
			if !mvcc.TupleVisible(tup.Header, ctx.Snap, ctx.Tx.XID, ctx.CmdID, ctx.comboStore(), ctx.MultiXact) {
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

// epqChainPendingWriter walks the t_ctid chain from (blk, slot) to its tail and
// reports the XID of a still-IN-PROGRESS transaction that owns the tail version,
// i.e. the reason epqFollowHOT/epqFollowChain just failed to produce a live
// successor.
//
// Why this exists (M0131-S32.1). The EvalPlanQual retry loop treats "chain
// follow found nothing" as "the row is gone" and SKIPS the row — silently, while
// still reporting `UPDATE 1`. That conflation is wrong whenever the chain tip was
// written by a transaction that has not committed yet: with N sessions updating
// one row, session D waits for A, refreshes its snapshot, walks to A's new
// version — and by then B has already replaced it with a version B has not
// committed. The tail is invisible, chain-follow reports not-found, and D's
// increment is DROPPED. At 8 sessions × 200 increments that lost 1112 of 1946
// write attempts (analysis/concurrent-hotrow.sh).
//
// Upstream never skips here: ExecUpdate loops on TM_Updated, and
// heap_lock_tuple/EvalPlanQualFetch block on the in-progress updater
// (XactLockTableWait) before re-fetching, so the update is applied to whatever
// version wins (postgres/src/backend/executor/nodeModifyTable.c ExecUpdate,
// access/heap/heapam.c heap_lock_tuple). Under READ COMMITTED that wait is
// unbounded; goopg's caller paces it with epqWait + the maxEPQRetriesRC backstop.
//
// Only an in-progress XMIN counts. A tail whose xmin ABORTED, or whose xmax
// committed (a real DELETE), means there is genuinely nothing to update, and the
// caller's existing skip is correct.
func epqChainPendingWriter(ctx *Context, rel storage.RelFileNode, blk storage.BlockNumber,
	slot uint16) (storage.TransactionID, bool) {
	curBlk, curSlot := blk, slot
	for i := 0; i < maxCTIDChainWalk; i++ {
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: curBlk})
		if err != nil {
			return 0, false
		}
		s.RLock()
		tup, gerr := storage.PageGetHeapTuple(s.Page(), curSlot)
		s.RUnlock()
		ctx.Pool.Unpin(s)
		if gerr != nil {
			return 0, false
		}
		if storage.IsMovedToAnotherPartition(tup.Header.CTID) {
			return 0, false
		}
		if isChainTailCTID(tup.Header.CTID, curBlk, curSlot) {
			xmin := tup.Header.Xmin
			if xmin == storage.InvalidTransactionID || xmin == ctx.Tx.XID {
				return 0, false
			}
			// epqXmaxSettled classifies any XID authoritatively via the
			// transaction manager (not by snapshot membership, which misses a
			// writer that started after our refresh — 0118-0105).
			aborted, committed := epqXmaxSettled(ctx, xmin)
			if aborted || committed {
				return 0, false
			}
			return xmin, true
		}
		curBlk, curSlot = tup.Header.CTID.Block, tup.Header.CTID.Offset
	}
	return 0, false
}

// epqChainTailLiveButUnseen reports whether the t_ctid chain from (blk, slot)
// ends at a version that is LIVE (xmin committed, not deleted) but which our
// current snapshot cannot see yet.
//
// Why this exists (M0131-S32.3). Both chain-follow helpers decide which version
// is "the latest" by SNAPSHOT visibility — epqFollowHOT passes ctx.Snap to
// followHOTChain, and epqFollowChainFull calls mvcc.TupleVisible at the tail.
// That is wrong for an EvalPlanQual re-fetch. The EPQ loop refreshes ctx.Snap,
// then walks the chain; if the winning writer commits in the window BETWEEN the
// refresh and the walk, its version is live on the page but outside our
// snapshot, so both helpers report not-found. epqChainPendingWriter then reports
// "not pending" (the writer has committed, so it is neither in-flight nor
// aborted), and the caller takes the silent skip — the statement's write is
// DROPPED while it still reports `UPDATE 1`. Measured at 6 lost increments in
// 42487 pgbench transactions on 5 hot rows, matching epq_chain_notfound exactly.
//
// Upstream has no such window because EvalPlanQual does not use a snapshot to
// find the latest version at all: heap_lock_tuple follows t_ctid until it
// reaches a version whose xmax is invalid or aborted, whatever its xmin commit
// time (postgres/src/backend/access/heap/heapam.c heap_lock_tuple, and
// EvalPlanQualFetch in executor/execMain.c). Only the *qual* is re-evaluated
// against the original snapshot.
//
// Returning true tells the caller to refresh its snapshot and take another EPQ
// lap rather than skip. The predicate is deliberately narrow so it cannot
// livelock: it is false once the version IS visible (the next lap makes it so),
// false for a genuinely deleted row, and false for an aborted/in-flight writer
// (epqChainPendingWriter owns that case).
func epqChainTailLiveButUnseen(ctx *Context, rel storage.RelFileNode, blk storage.BlockNumber,
	slot uint16) bool {
	curBlk, curSlot := blk, slot
	for i := 0; i < maxCTIDChainWalk; i++ {
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: curBlk})
		if err != nil {
			return false
		}
		s.RLock()
		tup, gerr := storage.PageGetHeapTuple(s.Page(), curSlot)
		s.RUnlock()
		ctx.Pool.Unpin(s)
		if gerr != nil {
			return false
		}
		if storage.IsMovedToAnotherPartition(tup.Header.CTID) {
			return false
		}
		if !isChainTailCTID(tup.Header.CTID, curBlk, curSlot) {
			curBlk, curSlot = tup.Header.CTID.Block, tup.Header.CTID.Offset
			continue
		}
		xmin := tup.Header.Xmin
		if xmin == storage.InvalidTransactionID || xmin == ctx.Tx.XID {
			return false
		}
		// Only a COMMITTED creator qualifies: in-flight is epqChainPendingWriter's
		// case (wait for it), aborted means there is nothing live here.
		if aborted, committed := epqXmaxSettled(ctx, xmin); aborted || !committed {
			return false
		}
		// A committed xmax on a chain-tail version is a real DELETE (an UPDATE
		// would have stamped t_ctid at the successor, so this would not be the
		// tail) — skipping the row is then correct.
		if xmax := tup.Header.Xmax; xmax != storage.InvalidTransactionID && xmax != ctx.Tx.XID {
			if aborted, committed := epqXmaxSettled(ctx, xmax); committed && !aborted {
				return false
			}
		}
		return !mvcc.TupleVisible(tup.Header, ctx.Snap, ctx.Tx.XID, ctx.CmdID,
			ctx.comboStore(), ctx.MultiXact)
	}
	return false
}

// epqRefreshSnapForRetry refreshes ctx.Snap under READ COMMITTED so the next
// EvalPlanQual lap sees everything committed since the last refresh. A no-op at
// RR/Serializable, where the snapshot is frozen for the whole transaction.
func epqRefreshSnapForRetry(ctx *Context) {
	if ctx.Tx.Isolation != mvcc.IsolationReadCommitted || ctx.TxnMgr == nil {
		return
	}
	if newSnap, err := ctx.TxnMgr.SnapshotFor(ctx.Tx); err == nil {
		ctx.Snap = newSnap
	}
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

	// lockParentOID is the partitioned parent this leaf was expanded from (a
	// scan THROUGH the parent locks the parent too); 0 for a direct leaf scan.
	lockParentOID uint32

	// skipIfVanished is set on an inheritance-child scan: if the child relation
	// was dropped by a concurrent committed transaction while this scan waited
	// on its lock, skip it (zero rows) instead of erroring. M0118-0008
	// (alter-table-4 perm 3).
	skipIfVanished bool

	// privilegeCheckRole / privilegeCheckRoleSet override which role's SELECT
	// grant is checked against tbl: set when this scan sits inside an inlined,
	// non-security_invoker view (planner.SeqScan.PrivilegeCheckRole/Set).
	// M0122-0008 (view-owner privilege gap).
	privilegeCheckRole    string
	privilegeCheckRoleSet bool

	// inheritParentOID is the inheritance parent this child scan was expanded
	// from (0 for a non-inheritance scan). After the child's lock is acquired
	// (a concurrent ALTER on the child has committed) the child's column types
	// are re-validated against the parent's; a mismatch raises the upstream
	// "does not match parent's type" error. M0118-0008 (alter-table-4 perm 4).
	inheritParentOID uint32

	ctx  *Context
	cols []catalog.Column

	// arrayStyle carries the session DateStyle/TimeZone down into the heap
	// decode, which is where goopg (unlike upstream, which defers to array_out
	// at output time) flattens an array column to its "{…}" text. Resolved once
	// in Open, and only for a relation that actually has an array column —
	// arrayStyleLive gates the whole thing so a scan of an array-free table
	// pays nothing. M0119-0006.
	arrayStyle     pgarray.OutputStyle
	arrayStyleLive bool

	// ssiGistPred is the spatial WHERE predicate of the Filter directly above
	// this scan, handed down at build time (Build / buildRec). When the scan runs
	// under SERIALIZABLE on a table carrying a GiST index on the predicate's point
	// column, the scan takes per-matching-tuple grid-cell SIREAD predicate locks
	// (design 0118-0137) instead of a relation-grain lock, reproducing PG's
	// page-level GiST predicate locking (predicate-gist spec). nil for every
	// non-gist scan (the common case) — pure no-op.
	ssiGistPred planner.Expr
	// gistSSIIdxOID / gistSSIColIdx are resolved in Open from ssiGistPred + the
	// table's GiST index: the index OID used as the predicate-lock relation and
	// the position of the indexed point column in cols. gistSSIIdxOID==0 disables
	// the whole gist-SSI path (so the relation-grain lock + per-tuple heap SIREAD
	// run as before). gistScratch is a reusable decode buffer for INVISIBLE
	// tuples (concurrent-insert conflict-out), which the normal flow never decodes.
	gistSSIIdxOID uint32
	gistSSIColIdx int
	gistScratch   Row

	// ssiGinPred is the `<gincol> @> <const array>` WHERE predicate of the Filter
	// directly above this scan, handed down at build time (same site as
	// ssiGistPred). Under SERIALIZABLE on a table carrying a GIN index on the
	// predicate's array column, the scan takes GIN key-grain SIREAD predicate locks
	// on the search keys (design 0118-0140) instead of a relation-grain lock,
	// reproducing PG's per-key GIN index predicate locking (predicate-gin spec).
	// nil for every non-gin scan — pure no-op.
	ssiGinPred planner.Expr
	// ginSSIIdxOID / ginSSIColIdx are resolved in Open from ssiGinPred + the
	// table's GIN index: the index OID used as the predicate-lock relation and the
	// position of the indexed array column in cols. ginSSIIdxOID==0 disables the
	// gin-SSI path (relation-grain lock + per-tuple heap SIREAD run as before).
	// ginSSIFastUpdate is the index's fastupdate state at Open; ginSearchKeys are
	// the extracted search keys (array elements of the @> right operand).
	ginSSIIdxOID     uint32
	ginSSIColIdx     int
	ginSSIFastUpdate bool
	ginSearchKeys    []string

	nBlocks  storage.BlockNumber
	curBlock storage.BlockNumber
	curSlot  uint16

	// pscan, when non-nil, makes this a PARALLEL scan: curBlock is then
	// claimed from a shared atomic allocator instead of being incremented
	// locally, so N workers' blocks partition the relation. Everything else
	// in this struct stays per-worker — the pin, the decode buffer, the
	// emitted slot, the per-page arena, the ring and the prefetch watermark.
	// P4 of docs/design/parallel-query/ (chapter 04).
	pscan *parallelScanState
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

	// statReturned counts visible tuples this scan has yielded; recorded into
	// cumulative relation stats (numscans + tuples_returned) at Close.
	// M0118-0009 (`stats`, rung 6; design 0118-0128).
	statReturned int64

	// enumTypes[i] is non-nil when cols[i] is a user-defined enum type.
	// Used to convert KindString heap datums to KindEnum for correct ORDER BY. M0097-enum.
	enumTypes []*catalog.EnumType

	// typeACLCat / typeACLColIdx / typeACLOidIdx drive the pg_type.typacl
	// heap-decode override (M0119-0004-ACLHEAP). pg_type is heap-backed for
	// PG18-standby basebackup parity, and a USAGE GRANT/REVOKE stores typacl as
	// a PG-native _aclitem binary blob (decoded to KindBytes by
	// decodePhysicalPGValueMctx). When scanning pg_type this hook renders that
	// blob to canonical aclitemout text — resolving grantee/grantor OIDs back to
	// role names via the catalog — so a goopg-served `SELECT typacl FROM pg_type`
	// (pg_dump's getTypes) reads the same text pg_class.relacl projects virtually.
	// typeACLColIdx is -1 for every non-pg_type scan (the common case → no-op).
	typeACLCat    *catalog.InMemory
	typeACLColIdx int
	typeACLOidIdx int

	// attrACLCat / attrACLColIdx drive the pg_attribute.attacl heap-decode override
	// (M0119-0004-ACLHEAP, attacl half) — the column analogue of the typacl hook
	// above. A column GRANT/REVOKE stores attacl as a PG-native _aclitem binary blob
	// (KindBytes); when scanning pg_attribute this hook renders it to canonical
	// aclitemout text so pg_dump's getTableAttrs reads the granted column ACL.
	// attrACLColIdx is -1 for every non-pg_attribute scan (the common case → no-op).
	attrACLCat    *catalog.InMemory
	attrACLColIdx int

	// dbACLCat / dbACLColIdx drive the pg_database.datacl heap-decode override
	// (M0119-0004-ACLHEAP, datacl half) — the database analogue of the typacl
	// hook above. A `GRANT … ON DATABASE …` stores datacl as a PG-native
	// _aclitem binary blob (KindBytes); when scanning pg_database this hook
	// renders it to canonical aclitemout text so pg_dump's getDatabases reads
	// the granted database ACL. dbACLColIdx is -1 for every non-pg_database
	// scan (the common case → no-op).
	dbACLCat    *catalog.InMemory
	dbACLColIdx int
}

// renderHeapACLColumnInto renders a heap-backed ACL column — pg_type.typacl,
// pg_attribute.attacl, or pg_database.datacl — stored as a PG-native _aclitem
// blob (KindBytes) — to canonical aclitemout text in place. The
// seqScanOp.Next() hot path renders these inline with a pre-resolved column
// index; this shared variant covers the index-scan path so pg_dump reads the
// same text regardless of which plan a catalog query picks (getTypes
// seq-scans pg_type; getColumnACLs index-scans pg_attribute by attrelid). A
// no-op for non-catalog tables, a non-InMemory catalog, and a NULL ACL
// column. M0119-0004-ACLHEAP.
func renderHeapACLColumnInto(cat catalog.Catalog, tbl *catalog.Table, cols []catalog.Column, row Row) {
	if tbl == nil {
		return
	}
	var aclName string
	switch tbl.OID {
	case catalog.TypeRelationId:
		aclName = "typacl"
	case catalog.AttributeRelationId:
		aclName = "attacl"
	case catalog.PgDatabaseRelationOID:
		aclName = "datacl"
	default:
		return
	}
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		return
	}
	for i := range cols {
		if i >= len(row) {
			return
		}
		if cols[i].Name != aclName {
			continue
		}
		if d := row[i]; d.Kind == KindBytes {
			if txt, err := decodeAclItemArrayText(d.BytesValue(), im.RoleNameForOID); err == nil {
				row[i] = NewStringDatum(txt)
			}
		}
		return
	}
}

// seqScanLookahead is the number of blocks ahead of the current
// scan position seqScanOp keeps prefetched. Mirrors upstream's
// `effective_io_concurrency` default scope and is enough to
// pipeline a single sequential scan against typical SSD
// latencies. A future loop turns this into a tunable GUC.
const seqScanLookahead storage.BlockNumber = 4

// validateInheritedColumnTypes mirrors PostgreSQL's make_inh_translation_list
// (optimizer/util/appendinfo.c): for every parent column it finds the child
// column of the same name and verifies the declared types still match. goopg
// only produces a mismatch via a concurrent `ALTER TABLE child ALTER COLUMN c
// TYPE …` (CREATE TABLE … INHERITS copies the parent's column types), so this
// fires exactly when upstream does — after the child's lock is acquired during
// inheritance expansion. A mismatch raises ERRCODE_INVALID_COLUMN_DEFINITION
// (42611) with the parent attribute name and the child relation name, matching
// upstream. Types are compared by canonical class so that equivalent aliases
// (integer/int4, double precision/float8) never trip a false positive on a
// legitimate inheritance scan. M0118-0008 (alter-table-4 perm 4).
func validateInheritedColumnTypes(im *catalog.InMemory, parent, child *catalog.Table) error {
	for _, pc := range parent.Columns {
		if pc.Dropped {
			continue
		}
		var cc *catalog.Column
		for i := range child.Columns {
			if child.Columns[i].Dropped {
				continue
			}
			if strings.EqualFold(child.Columns[i].Name, pc.Name) {
				cc = &child.Columns[i]
				break
			}
		}
		if cc == nil {
			// A missing inherited column is a distinct (rare) failure mode handled
			// elsewhere; do not synthesize a type-mismatch error for it.
			continue
		}
		if canonicalTypeClass(im, pc.Type) != canonicalTypeClass(im, cc.Type) {
			return &ExecError{
				Code:    "42611",
				Message: fmt.Sprintf("attribute %q of relation %q does not match parent's type", pc.Name, child.Name),
			}
		}
	}
	return nil
}

// canonicalTypeClass reduces a catalog type to a comparison token so that
// equivalent spellings collapse to the same value (integer/int4/int → "int4",
// double precision/float8/float → "float8", …), resolving domains to their base
// type first. The array flag is folded in; the typmod args are intentionally NOT
// compared (a coarser check than PostgreSQL's exact atttypmod) so the only thing
// that can trip validateInheritedColumnTypes is a genuine base-type change.
func canonicalTypeClass(im *catalog.InMemory, t catalog.Type) string {
	name := strings.ToLower(t.Name)
	if im != nil {
		name = strings.ToLower(im.ResolveColumnType(name))
	}
	switch name {
	case "int", "integer", "int4", "serial", "serial4":
		name = "int4"
	case "smallint", "int2", "smallserial", "serial2":
		name = "int2"
	case "bigint", "int8", "bigserial", "serial8":
		name = "int8"
	case "real", "float4":
		name = "float4"
	case "double precision", "double", "float8", "float":
		name = "float8"
	case "decimal", "numeric":
		name = "numeric"
	case "varchar", "character varying":
		name = "varchar"
	case "char", "character", "bpchar":
		name = "bpchar"
	case "bool", "boolean":
		name = "bool"
	}
	if t.IsArray {
		name += "[]"
	}
	return name
}

func newSeqScanOp(p *planner.SeqScan) *seqScanOp {
	return &seqScanOp{
		schema:                p.Output(),
		tbl:                   p.Table,
		pos:                   p.Pos(),
		cols:                  p.Table.Columns,
		lockParentOID:         p.LockParentOID,
		skipIfVanished:        p.SkipIfVanished,
		inheritParentOID:      p.InheritParentOID,
		privilegeCheckRole:    p.PrivilegeCheckRole,
		privilegeCheckRoleSet: p.PrivilegeCheckRoleSet,
	}
}

func (o *seqScanOp) Schema() planner.Schema { return o.schema }

// gistRowMatches evaluates the GiST spatial-SSI predicate against an
// already-decoded leaf-local row, returning whether the row matches the scan's
// spatial filter. Used only in gist-SSI mode (gistSSIIdxOID != 0); returns false
// on any eval error or non-boolean result. Design 0118-0137.
func (o *seqScanOp) gistRowMatches(row Row) bool {
	res, err := evalExpr(o.ssiGistPred, row, o.ctx)
	if err != nil || res.Kind != KindBool {
		return false
	}
	return res.BoolValue()
}

// gistTupleMatches decodes an INVISIBLE tuple into the reusable gist scratch row
// and evaluates the spatial predicate. The normal scan flow never decodes
// invisible tuples, so this path exists to gate the concurrent-insert
// conflict-out by spatial match (design 0118-0137). MUST be called with the page
// RLock held — tuple.Data views the page bytes. Returns false on decode/eval
// failure.
// decodeScanRow decodes a visible tuple into the reusable scan row, routing
// through the styled decoder only when the relation has an array column (see
// seqScanOp.arrayStyle). The unstyled call is byte-identical to the styled one
// under the boot-default GUCs, so the branch is a cost guard, not a behaviour
// switch.
func (o *seqScanOp) decodeScanRow(data, bitmap []byte, storedNatts int) error {
	if o.arrayStyleLive {
		return DecodeRowIntoMctxPGTupleStyled(o.scanRow, o.cols, data, bitmap, storedNatts, o.sctx, o.arrayStyle)
	}
	return DecodeRowIntoMctxPGTuple(o.scanRow, o.cols, data, bitmap, storedNatts, o.sctx)
}

func (o *seqScanOp) gistTupleMatches(tuple storage.HeapTuple) bool {
	if o.gistScratch == nil || len(o.gistScratch) != len(o.cols) {
		o.gistScratch = make(Row, len(o.cols))
	}
	storedNatts := int(tuple.Header.Infomask2 & 0x07FF)
	if err := DecodeRowIntoMctxPGTuple(o.gistScratch, o.cols, tuple.Data, tuple.Bitmap, storedNatts, o.sctx); err != nil {
		return false
	}
	return o.gistRowMatches(o.gistScratch)
}

// ginSearchKeys extracts the GIN search keys from this scan's
// `<gincol> @> <const array>` predicate (o.ssiGinPred). Returns the
// canonical-text elements of the constant array operand and ok=true only for the
// supported `@>` form with the gin column on the contains side; any other shape
// returns ok=false so the caller keeps relation-grain locking (never under-locks).
// Design 0118-0140.
func (o *seqScanOp) extractGinSearchKeys(colName string) ([]string, bool) {
	bin, ok := o.ssiGinPred.(*planner.BinaryOp)
	if !ok || bin.Op != parser.OpContains {
		return nil, false
	}
	// `col @> array[...]`: Left is the gin column, Right is the constant array.
	lc, lok := bin.Left.(*planner.ColumnRef)
	if !lok || !strings.EqualFold(lc.Name, colName) {
		return nil, false
	}
	d, err := evalExpr(bin.Right, nil, o.ctx)
	if err != nil || d.Kind != KindString {
		return nil, false
	}
	return parseTextArray(d.StringValue()), true
}

func (o *seqScanOp) Open(ctx *Context) error {
	if ctx.Pool == nil || ctx.Catalog == nil {
		return &ExecError{Code: "XX000", Pos: o.pos, Message: "SeqScan requires storage handles in Context"}
	}
	// S2a (design bundle ch.04 §4.2): Open on an already-open scan is a
	// rewind, mirroring indexScanOp's `o.ctx != nil` reopen detection. The
	// full path below would leak the previous mctx arena and scan ring
	// (both released only in Close), re-record SIREADs, and re-run
	// privilege/lock work. All of that state is statement-scoped and
	// unchanged within a statement (locks are statement-scoped; PostgreSQL
	// likewise checks privileges once at executor startup, not per
	// rescan), so the rewind only resets position state. A re-Open under
	// a *different* Context falls through to the full path after
	// releasing the held resources — its caches (enum/ACL columns, rel)
	// derive from the plan node and stay valid, but snapshot-dependent
	// state must be re-established.
	if o.sctx != nil {
		if o.ctx == ctx {
			return o.rewind()
		}
		o.releaseScanState()
	}
	if o.tbl != nil && !dmlPrivilegePermittedAs(ctx, o.tbl, "SELECT", selectPrivilegeCheckRole(ctx, o.privilegeCheckRoleSet, o.privilegeCheckRole)) {
		return &ExecError{Code: "42501", Pos: o.pos, Message: fmt.Sprintf("permission denied for table %s", o.tbl.Name)}
	}
	// Reject scans of unpopulated materialized views (WITH NO DATA / before REFRESH). M0097-0025.
	if o.tbl != nil && o.tbl.IsMatView && !o.tbl.IsPopulated {
		return &ExecError{Code: "55000", Pos: o.pos,
			Message: fmt.Sprintf("materialized view %q has not been populated", o.tbl.Name),
			Hint:    "Use the REFRESH MATERIALIZED VIEW command."}
	}
	o.ctx = ctx
	// Resolve the array element output style once per scan (M0119-0006). Done
	// after the reopen/rewind branch above so a re-Open under a different
	// Context — the only way the GUCs can differ mid-operator — re-reads them.
	o.arrayStyleLive = colsHaveArray(o.cols)
	if o.arrayStyleLive {
		o.arrayStyle = arrayOutputStyle(ctx)
	}
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
	// M0119-0004-ACLHEAP: arm the pg_type.typacl heap-decode override only when
	// scanning the heap-backed pg_type catalog. Resolved once here (column
	// positions are stable) so Next() does a single bool/index check per row.
	o.typeACLColIdx = -1
	o.typeACLOidIdx = -1
	if o.tbl != nil && o.tbl.OID == catalog.TypeRelationId {
		if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
			for i, col := range o.cols {
				switch col.Name {
				case "typacl":
					o.typeACLColIdx = i
				case "oid":
					o.typeACLOidIdx = i
				}
			}
			if o.typeACLColIdx >= 0 && o.typeACLOidIdx >= 0 {
				o.typeACLCat = im
			}
		}
	}
	// M0119-0004-ACLHEAP (attacl half): arm the pg_attribute.attacl heap-decode
	// override only when scanning the heap-backed pg_attribute catalog. Resolved
	// once here (column positions are stable) so Next() does a single bool/index
	// check per row. The blob is written by a column GRANT/REVOKE (execAttrACLChange
	// → resyncAttrACLHeapRow) and rendered back to canonical aclitemout text here.
	o.attrACLColIdx = -1
	if o.tbl != nil && o.tbl.OID == catalog.AttributeRelationId {
		if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
			for i, col := range o.cols {
				if col.Name == "attacl" {
					o.attrACLColIdx = i
					break
				}
			}
			if o.attrACLColIdx >= 0 {
				o.attrACLCat = im
			}
		}
	}
	// M0119-0004-ACLHEAP (datacl half): arm the pg_database.datacl heap-decode
	// override only when scanning the heap-backed pg_database catalog. Resolved
	// once here (column positions are stable) so Next() does a single bool/index
	// check per row. The blob is written by a `GRANT … ON DATABASE …`
	// (execDatabaseACLChange → resyncDatabaseACLHeapRow) and rendered back to
	// canonical aclitemout text here.
	o.dbACLColIdx = -1
	if o.tbl != nil && o.tbl.OID == catalog.PgDatabaseRelationOID {
		if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
			for i, col := range o.cols {
				if col.Name == "datacl" {
					o.dbACLColIdx = i
					break
				}
			}
			if o.dbACLColIdx >= 0 {
				o.dbACLCat = im
			}
		}
	}
	// Cache rel once — avoids the catalog RLock on every Next() call.
	o.rel = ctx.Catalog.RelFileNode(o.tbl)
	// M0118-0002 (design 0118-0137): GiST spatial-SSI mode. A SERIALIZABLE scan
	// with a spatial filter on a GiST-indexed point column locks per-matching-
	// tuple grid cells on the index (Next loop below) instead of the whole
	// relation, reproducing PG's page-level GiST predicate locking and its reduced
	// false positives (predicate-gist). Resolved here while ctx/catalog are in
	// hand; gistSSIIdxOID==0 leaves the legacy relation-grain path untouched.
	if o.ssiGistPred != nil && ssiActive(ctx) && (o.tbl == nil || (!o.tbl.Temp && !o.tbl.IsMatView)) {
		if oid, colIdx, ok := ssiGistIndexForTable(ctx, o.tbl, o.cols); ok {
			o.gistSSIIdxOID = oid
			o.gistSSIColIdx = colIdx
		}
	}
	// M0118-0002 (design 0118-0140): GIN key-grain-SSI mode. A SERIALIZABLE
	// `<arraycol> @> array[...]` scan on a GIN-indexed array column locks the
	// posting-tree page of each search key on the index instead of the whole
	// relation, reproducing PG's per-key GIN index predicate locking and its
	// reduced false positives (predicate-gin). The key SIREADs are taken here
	// (the search keys come from the constant @> right operand — independent of
	// which tuples match, so a non-existing key still locks its page); the matching
	// INSERT conflicts-in on each inserted element's page. ginSSIIdxOID==0 leaves
	// the legacy relation-grain path untouched (including when the predicate shape
	// is unsupported, so we never under-lock).
	if o.gistSSIIdxOID == 0 && o.ssiGinPred != nil && ssiActive(ctx) && (o.tbl == nil || (!o.tbl.Temp && !o.tbl.IsMatView)) {
		if oid, colIdx, fu, ok := ssiGinIndexForTable(ctx, o.tbl, o.cols); ok {
			if keys, kok := o.extractGinSearchKeys(o.cols[colIdx].Name); kok {
				o.ginSSIIdxOID = oid
				o.ginSSIColIdx = colIdx
				o.ginSSIFastUpdate = fu
				o.ginSearchKeys = keys
				ssiRecordGinKeyRead(ctx, o.rel.DBOid, oid, keys, fu)
			}
		}
	}
	// M0118-0001: a SERIALIZABLE seq scan takes a relation-level SIREAD
	// predicate lock so a concurrent writer's INSERT of a matching row forms
	// the rw-conflict (phantom). Mirrors PredicateLockRelation in upstream
	// heap_beginscan; temp / matview relations are excluded exactly as
	// PredicateLockingNeededForRelation does (system catalogs gated in the hook).
	// In GiST/GIN index-SSI mode the finer index-page locks replace this coarse
	// lock (taking both would re-coarsen to the relation grain and over-abort).
	if o.gistSSIIdxOID == 0 && o.ginSSIIdxOID == 0 && (o.tbl == nil || (!o.tbl.Temp && !o.tbl.IsMatView)) {
		ssiRecordRelationRead(ctx, o.rel)
	}
	if err := ctx.acquireRelLock(o.rel, lockmgr.AccessShareLock); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.pos
		}
		return err
	}
	if err := ctx.acquireScanReadLockTxn(o.rel); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.pos
		}
		return err
	}
	// M0118-0008 (alter-table-4 perm 3): an inheritance-child scan that waited on
	// this child's lock may find the child gone — a concurrent transaction
	// committed a DROP of it while we blocked. Mirror PostgreSQL's
	// try_table_open → NULL during inheritance expansion: skip the child (zero
	// rows) rather than recreating its relfile (NBlocks would O_CREATE it) or
	// erroring. Only inheritance children set skipIfVanished; a direct scan of a
	// dropped table still errors elsewhere.
	if o.skipIfVanished && o.tbl != nil {
		if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
			if _, exists := im.LookupTableByOID(o.tbl.OID); !exists {
				o.nBlocks = 0
				o.curBlock = 0
				return nil
			}
			// M0118-0008 (alter-table-4 perm 4): now that we hold the child's lock
			// (any concurrent ALTER on it has committed), re-validate the child's
			// column types against the inheritance parent's, exactly as PostgreSQL's
			// make_inh_translation_list does after locking each child during
			// inheritance expansion (optimizer/util/appendinfo.c). A column whose
			// type no longer matches the parent's (e.g. a concurrent
			// `ALTER COLUMN a TYPE float`) raises ERRCODE_INVALID_COLUMN_DEFINITION.
			if o.inheritParentOID != 0 {
				if parent, ok2 := im.LookupTableByOID(o.inheritParentOID); ok2 {
					if err := validateInheritedColumnTypes(im, parent, o.tbl); err != nil {
						if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
							ee.Pos = o.pos
						}
						return err
					}
				}
			}
		}
	}
	// PostgreSQL locks every index of a scanned relation in AccessShare too
	// (get_relation_info opens all indexes regardless of the chosen scan method),
	// so a bare SELECT on a leaf partition blocks a concurrent DROP INDEX behind
	// the open reader. M0118-0008 (partition-drop-index-locking).
	if err := ctx.acquireScanIndexReadLocksTxn(o.tbl); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.pos
		}
		return err
	}
	// Scanning a partition THROUGH its partitioned parent locks the parent
	// (AccessShare) as well — PostgreSQL locks the whole hierarchy from the
	// queried root. A concurrent AccessExclusive holder on the parent (e.g. a
	// DROP of a sibling partition pending detach, which grabs the parent lock)
	// therefore blocks this scan until it commits. M0118-0008
	// (detach-partition-concurrently-3).
	if o.lockParentOID != 0 {
		if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
			if parentTbl, ok2 := im.LookupTableByOID(o.lockParentOID); ok2 {
				if err := ctx.acquireScanReadLockTxn(ctx.Catalog.RelFileNode(parentTbl)); err != nil {
					if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
						ee.Pos = o.pos
					}
					return err
				}
			}
		}
	}
	n, err := ctx.Pool.NBlocks(o.rel)
	if err != nil {
		return err
	}
	o.nBlocks = n
	// Parallel scan: publish the relation size to the shared allocator. Every
	// worker's scan calls this with the same value during Open; sync.Once makes
	// the first one authoritative and supplies the happens-before edge.
	o.pscan.setBoundary(n)
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
	//
	// P4: the ring is DISABLED for a parallel scan. storage.ScanRing has no
	// synchronisation at all, so each worker would need its own — and N
	// private rings consume N times the buffers the single-scan heuristic
	// was sized for, while every worker sees the same nBlocks and so all N
	// would activate together. Correct sizing under parallelism is an
	// empirical question (chapter 04 §4.1); until it is measured, workers go
	// through the shared pool.
	//
	// Two consequences worth stating rather than discovering: this removes
	// pool-pollution protection for exactly the large relations the ring
	// exists to protect, and — because the hint-bit path is gated on
	// `o.pinned != nil`, which is nil under the ring — it turns hint-bit
	// writing ON for those scans. Both are latched and PG-consistent, but
	// neither is implied by the phrase "disable a buffering strategy".
	if o.pscan == nil && ctx.Pool != nil && int(n) > ctx.Pool.Capacity()/4 {
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
	// P4 (chapter 04 §4.2): prefetch is disabled for a parallel scan. With a
	// shared allocator a worker's next block is no longer curBlock+1 — it is
	// whatever the allocator hands out — so a per-worker lookahead window
	// prefetches blocks this worker will probably never read, and N workers
	// would each do it. The I/O concurrency prefetch emulates is supplied
	// directly by N workers each issuing a synchronous page read.
	if o.pscan != nil {
		return
	}
	target := o.curBlock + seqScanLookahead
	if target > o.nBlocks {
		target = o.nBlocks
	}
	for o.prefetchedThru < target {
		o.ctx.Pool.Prefetch(storage.BufferTag{Rel: rel, Block: o.prefetchedThru})
		o.prefetchedThru++
	}
}

// releaseScanState drops every resource the scan holds between Open and
// Close: the ring (or the bare page pin), the borrowed scan row, and the
// per-operator mctx arena. Shared by Close and the full-reopen fallback in
// Open so the two release paths cannot drift. Position fields are left to
// the caller.
func (o *seqScanOp) releaseScanState() {
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
}

// rewind re-positions an already-open scan at block 0 for a rescan
// (S2a, design bundle ch.04 §4.2). It reuses the held mctx arena —
// after a Reset, exactly the lifetime contract the scan's own
// block-advance Reset already imposes on arena-backed datums — and
// releases any page pin / ring position a partial drain left behind.
// NBlocks is re-read (the relation may have grown mid-statement); the
// ring is recreated rather than rewound because storage.ScanRing has no
// documented rewind contract, and a fresh ring is provably equivalent
// to the fresh-Open path. Cumulative stats are NOT recorded here — the
// scan is not finished; statReturned keeps accumulating and Close
// records the total once.
func (o *seqScanOp) rewind() error {
	if o.ring != nil {
		o.ring.Close()
		o.ring = nil
		o.activePage = nil
	} else if o.pinned != nil {
		o.ctx.Pool.Unpin(o.pinned)
		o.pinned = nil
		o.activePage = nil
	}
	if o.scanRow != nil {
		releaseRow(o.scanRow)
		o.scanRow = nil
	}
	n, err := o.ctx.Pool.NBlocks(o.rel)
	if err != nil {
		return err
	}
	o.nBlocks = n
	// Parallel scan: publish the relation size to the shared allocator. Every
	// worker's scan calls this with the same value during Open; sync.Once makes
	// the first one authoritative and supplies the happens-before edge.
	o.pscan.setBoundary(n)
	o.curBlock = 0
	o.curSlot = 0
	o.slotMax = 0
	o.prefetchedThru = 0
	o.sctx.Reset()
	if o.pscan == nil && o.ctx.Pool != nil && int(n) > o.ctx.Pool.Capacity()/4 {
		o.ring = storage.NewScanRing(o.ctx.Pool, o.rel)
	}
	o.refillPrefetchWindow(o.rel)
	return nil
}

func (o *seqScanOp) Close() error {
	o.releaseScanState()
	// Record this sequential scan into cumulative relation stats: one scan that
	// read statReturned visible tuples (mirrors pgstat_count_heap_scan +
	// pgstat_count_heap_getnext). Non-transactional, gated by track_counts.
	// M0118-0009 (`stats`, rung 6; design 0118-0128).
	if o.ctx != nil && o.tbl != nil {
		recordRelScan(o.ctx, o.tbl.OID, o.statReturned)
	}
	return nil
}

// nextVisible advances through (block, slot) pairs and returns the
// next tuple visible to the snapshot, or EOF.
func (o *seqScanOp) Next() (TupleSlot, error) {
	rel := o.rel
	for {
		if o.pinned == nil && o.activePage == nil {
			// Serial: curBlock walks 0..nBlocks-1 locally. Parallel: the
			// next block is claimed from the shared allocator, so workers
			// partition the relation between them. advanceBlock() below
			// leaves curBlock unset for a parallel scan, so the claim
			// happens here, once per page, at the same point the serial
			// path tests its cursor.
			if o.pscan != nil {
				b, ok := o.pscan.nextBlock()
				if !ok {
					return nil, EOF
				}
				o.curBlock = b
			} else if o.curBlock >= o.nBlocks {
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
				o.advanceBlock()
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
			if !mvcc.TupleVisibleSubxact(tuple.Header, o.ctx.Snap, o.ctx.Tx.XID, o.ctx.TxnMgr, o.ctx.CmdID, o.ctx.comboStore(), o.ctx.MultiXact) {
				// M0118-0002 (design 0118-0137): in GiST spatial-SSI mode the
				// invisible-tuple conflict-out is gated by the spatial predicate —
				// only a concurrent insert that MATCHES this scan's region forms the
				// reader→inserter rw-edge (a relation-wide conflict-out would
				// re-introduce the false positives the grid-cell locking removes).
				// The decode reads tuple.Data, which views the page bytes, so it must
				// happen before the RUnlock.
				if o.gistSSIIdxOID != 0 {
					gistMatch := o.gistTupleMatches(tuple)
					if o.pinned != nil {
						o.pinned.RUnlock()
					}
					if gistMatch {
						if err := ssiRecordInvisibleTupleRead(o.ctx, rel, tuple.Header.Xmin); err != nil {
							return nil, err
						}
					}
					continue
				}
				// M0118-0002 (design 0118-0140): in GIN key-grain-SSI mode the
				// reader→inserter rw-edge is formed write-side (the INSERT
				// conflicts-in on the search-key page), so the relation-wide
				// invisible-tuple conflict-out is suppressed — it would re-introduce
				// the false positives the per-key locking removes.
				if o.ginSSIIdxOID != 0 {
					if o.pinned != nil {
						o.pinned.RUnlock()
					}
					continue
				}
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
			// M0104-0007: SSI read-path hook. Tuple is visible to this
			// reader — install a tuple-grain SIREAD predicate lock and an
			// rw-conflict edge to the producing writer (xmin). Helper
			// short-circuits to a single inline check for RC/RR readers.
			// curSlot was already advanced past the just-fetched slot.
			// M0118-0001: a non-nil error means this read closed a dangerous
			// structure to an already-committed writer and the reader must
			// abort mid-statement (40001). Release the per-tuple page RLock
			// before returning; Close()/the pin handles buffer release.
			// M0118-0002 (design 0118-0137): in GiST spatial-SSI mode the heap
			// per-tuple SIREAD is replaced by a grid-cell SIREAD on the index,
			// taken below once the row is decoded (taking the heap lock here too
			// would coarsen to a heap-page lock and re-introduce false positives).
			if o.gistSSIIdxOID == 0 && o.ginSSIIdxOID == 0 {
				if err := ssiRecordTupleRead(o.ctx, rel, o.curBlock, o.curSlot-1, tuple.Header.Xmin, tuple.Header.Xmax); err != nil {
					if o.pinned != nil {
						o.pinned.RUnlock()
					}
					return nil, err
				}
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
			if err := o.decodeScanRow(tuple.Data, tuple.Bitmap, storedNatts); err != nil {
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
			// M0118-0002 (design 0118-0137): GiST spatial-SSI read hook. A visible
			// tuple matching the spatial predicate takes a grid-cell SIREAD on the
			// GiST index (the phantom mechanism) plus the reader→writer conflict-out
			// (the write-before-read edge). Done after decode so the point value is
			// available; the page RLock is still held, so release it before any
			// 40001 mid-statement abort, exactly like the heap hook above.
			if o.gistSSIIdxOID != 0 && o.gistSSIColIdx < len(row) {
				if o.gistRowMatches(row) {
					if row[o.gistSSIColIdx].Kind == KindString {
						if pt, pok := parsePointText(row[o.gistSSIColIdx].StringValue()); pok {
							ssiRecordGistGridRead(o.ctx, o.rel.DBOid, o.gistSSIIdxOID, pt[0], pt[1])
						}
					}
					if serr := ssiConflictOutTupleRead(o.ctx, tuple.Header.Xmin, tuple.Header.Xmax); serr != nil {
						if o.pinned != nil {
							o.pinned.RUnlock()
						}
						return nil, serr
					}
				}
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
			// M0119-0004-ACLHEAP: render the heap-backed pg_type.typacl blob
			// (KindBytes _aclitem ArrayType, written by a USAGE GRANT/REVOKE)
			// to canonical aclitemout text, resolving grantee/grantor OIDs back
			// to role names via the catalog. row was deep-copied by
			// cloneRowOwned above, so this post-RUnlock mutation is safe. Dormant
			// for an unpopulated (NULL) typacl and every non-pg_type scan (-1).
			if o.typeACLColIdx >= 0 && o.typeACLColIdx < len(row) {
				if d := row[o.typeACLColIdx]; d.Kind == KindBytes {
					if txt, derr := decodeAclItemArrayText(d.BytesValue(), o.typeACLCat.RoleNameForOID); derr == nil {
						row[o.typeACLColIdx] = NewStringDatum(txt)
					}
				}
			}
			// M0119-0004-ACLHEAP (attacl half): the sibling pg_attribute.attacl
			// render — same KindBytes _aclitem decode, written by a column
			// GRANT/REVOKE. Dormant for a NULL attacl and every non-pg_attribute
			// scan (-1).
			if o.attrACLColIdx >= 0 && o.attrACLColIdx < len(row) {
				if d := row[o.attrACLColIdx]; d.Kind == KindBytes {
					if txt, derr := decodeAclItemArrayText(d.BytesValue(), o.attrACLCat.RoleNameForOID); derr == nil {
						row[o.attrACLColIdx] = NewStringDatum(txt)
					}
				}
			}
			// M0119-0004-ACLHEAP (datacl half): the sibling pg_database.datacl
			// render — same KindBytes _aclitem decode, written by a
			// `GRANT … ON DATABASE …`. Dormant for a NULL datacl and every
			// non-pg_database scan (-1).
			if o.dbACLColIdx >= 0 && o.dbACLColIdx < len(row) {
				if d := row[o.dbACLColIdx]; d.Kind == KindBytes {
					if txt, derr := decodeAclItemArrayText(d.BytesValue(), o.dbACLCat.RoleNameForOID); derr == nil {
						row[o.dbACLColIdx] = NewStringDatum(txt)
					}
				}
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
			// M0128-P6.1 resjunk-ctid rowmark: when the scan's schema has
			// been extended with a trailing ctid column, append the TID as
			// a string datum "(block,offset)" so it rides the row through
			// the plan tree and lockRowsOp reads it from there instead of
			// from the side-channel / walker.
			if len(o.schema) > len(o.cols) {
				for i := len(o.cols); i < len(o.schema); i++ {
					row = append(row, NewStringDatum(fmt.Sprintf("(%d,%d)", o.curBlock, o.curSlot-1)))
				}
			}
			o.slot.row = row
			// M0097-0038: inject current TID for CTIDExpr evaluation.
			o.slot.hasCTID = true
			o.slot.ctidBlock = uint32(o.curBlock)
			o.slot.ctidOff = uint16(o.curSlot - 1)
			o.statReturned++ // cumulative relation stats (tuples_returned)
			return &o.slot, nil
		}
		o.releasePinned()
		o.advanceBlock()
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

// advanceBlock moves the scan off the block it just finished.
//
// Serial scans step the local cursor. Parallel scans do NOT: the next block is
// claimed from the shared allocator at the top of Next(), so advancing here
// would skip a block. Setting the cursor past nBlocks is deliberate — it makes
// a parallel scan that somehow reaches the serial termination test fall out
// rather than loop, without needing a second flag.
//
// P4 of docs/design/parallel-query/ (chapter 04 §2).
func (o *seqScanOp) advanceBlock() {
	if o.pscan != nil {
		// The claim happens in Next(); nothing to do but ensure the local
		// cursor cannot be mistaken for a valid position.
		o.curBlock = o.nBlocks
		return
	}
	o.curBlock++
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

// dmlPrivilegePermitted reports whether the session's effective role may run
// an INSERT/UPDATE/DELETE against tbl. Mirrors PostgreSQL's ExecCheckRTPerms:
// the bootstrap superuser — a session that has NOT done SET ROLE/SET SESSION
// AUTHORIZATION to a non-superuser — always passes; a non-superuser role must
// own tbl (Table.Owner, case-insensitive) or hold the named privilege via
// GRANT (internal/catalog's tableACLs, the same store TRUNCATE/MAINTAIN
// already consult). Checked before any lock is acquired, matching
// PostgreSQL's pre-lock ACL check (M0118-0008 truncate-conflict precedent).
// M0097-0040.
func dmlPrivilegePermitted(ctx *Context, tbl *catalog.Table, priv string) bool {
	return dmlPrivilegePermittedAs(ctx, tbl, priv, ctx.NonSuperuserRole)
}

// dmlPrivilegePermittedAs is dmlPrivilegePermitted with an explicit
// checkRole: the three SELECT-gated scan operators (seqScanOp, indexScanOp,
// indexOnlyScanOp) pass the *view owner* here instead of ctx.NonSuperuserRole
// when the scan sits inside an inlined, non-security_invoker view —
// PostgreSQL runs a view's underlying-table reads as the view owner, not the
// querying role (planner.tagViewOwnerScans stamps the override at plan
// time), so `GRANT SELECT ON view TO role` alone (no matching base-table
// grant) still lets role query through it. The querying session's own
// superuser bypass always wins regardless of checkRole — a superuser
// session runs everything as itself, view-owner semantics or not.
// M0122-0008 (view-owner privilege gap).
func dmlPrivilegePermittedAs(ctx *Context, tbl *catalog.Table, priv, checkRole string) bool {
	if ctx.NonSuperuserRole == "" {
		return true // bootstrap superuser session: full privileges
	}
	if checkRole == "" {
		return true // effective role is the bootstrap superuser (e.g. a superuser-owned view)
	}
	if priv == "SELECT" && tbl != nil && catalog.IsSystemRelation(tbl.OID) {
		// PostgreSQL seeds every system catalog/view with an implicit PUBLIC
		// SELECT grant at initdb time (pg_init_privs) so any role can read
		// pg_catalog/information_schema for introspection (psql \d, pg_dump,
		// driver metadata queries). goopg has no per-catalog default-ACL
		// seeding, so mirror that outcome by always permitting SELECT on
		// relations below FirstNormalObjectId. M0097-0040 SELECT follow-up.
		return true
	}
	if tbl.Owner != "" && strings.EqualFold(tbl.Owner, checkRole) {
		return true
	}
	return ctx.Catalog.HasTablePrivilege(tbl.OID, checkRole, priv)
}

// selectPrivilegeCheckRole resolves the role whose SELECT grant a scan
// operator should check: the view owner when the scan was tagged by
// planner.tagViewOwnerScans (roleSet), otherwise the querying session's own
// role. M0122-0008 (view-owner privilege gap).
func selectPrivilegeCheckRole(ctx *Context, roleSet bool, role string) string {
	if roleSet {
		return role
	}
	return ctx.NonSuperuserRole
}

func (o *insertOp) Open(ctx *Context) error {
	if ctx.Pool == nil || ctx.Catalog == nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "Insert requires storage handles in Context"}
	}
	o.ctx = ctx
	if !dmlPrivilegePermitted(ctx, o.plan.Table, "INSERT") {
		return &ExecError{Code: "42501", Pos: o.plan.Pos(), Message: fmt.Sprintf("permission denied for table %s", o.plan.Table.Name)}
	}
	rel := ctx.Catalog.RelFileNode(o.plan.Table)
	if err := ctx.acquireRelLock(rel, lockmgr.RowExclusiveLock); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.plan.Pos()
		}
		return err
	}
	if err := ctx.acquireWriteLockTxn(rel); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.plan.Pos()
		}
		return err
	}
	return o.child.Open(ctx)
}

func (o *insertOp) Close() error {
	// Cumulative relation stats: stage inserted tuples (one live tuple each) for
	// the current transaction, gated by track_counts. M0118-0009 (`stats`, rung 7;
	// design 0118-0131). Autocommit statements fold to pending immediately.
	recordRelInsert(o.ctx, tableOIDFromCatalog(o.plan.Table), o.rowsAffected)
	return o.child.Close()
}

// Next runs the insert as a one-shot side effect on first call. With
// RETURNING (M0100-0005) the inserted rows are accumulated in o.retRows
// and streamed out on subsequent calls. Without RETURNING the wire layer
// issues `INSERT N`.
// coerceRowForConstraintChecks coerces row[i] to its declared column type in
// place wherever include(i) is true, mirroring PostgreSQL's assignment-cast
// semantics (transformAssignedExpr) so downstream FK/CHECK/domain/NOT-NULL
// constraint checks — and their DETAIL-message rendering — see a properly
// typed value instead of the raw literal Datum a VALUES/SET expression
// produced. Catches smallint/int4 out-of-range and bigint overflow, and
// turns a still-KindString date/timestamp/timestamptz/numeric literal into
// its typed Datum. Skips NULL values and array-typed columns (an array
// column's value is a "{...}" text literal, not a scalar — coercing it to
// the element type would misparse it; M0118-0002). Shared by insertOp.Next
// and every UPDATE new-row construction site (updateViaIndex, updateOp.Next,
// updateWithFrom — main path and EPQ retry); pattern_sibling_paths_must_agree.
func coerceRowForConstraintChecks(cols []catalog.Column, row Row, include func(i int) bool, ctx *Context, pos int) error {
	for i, col := range cols {
		if i >= len(row) || !include(i) || row[i].IsNull() || col.Type.IsArray {
			continue
		}
		var coerced Datum
		var cerr error
		switch strings.ToLower(col.Type.Name) {
		case "int2", "smallint", "smallserial", "serial2":
			coerced, cerr = evalCast(row[i], "int2", pos, ctx)
		case "int4", "integer", "int", "serial", "serial4":
			coerced, cerr = evalCast(row[i], "int4", pos, ctx)
		case "int8", "bigint", "bigserial", "serial8":
			coerced, cerr = evalCast(row[i], "int8", pos, ctx)
		case "date":
			coerced, cerr = evalCast(row[i], "date", pos, ctx)
		case "timestamp":
			coerced, cerr = evalCast(row[i], "timestamp", pos, ctx)
		case "timestamptz":
			coerced, cerr = evalCast(row[i], "timestamptz", pos, ctx)
		case "time", "time without time zone":
			// M0119-0006 (62nd slice): `time` joins the list so its declared
			// precision reaches the value — before this, a `time(2)` column's value
			// stayed KindString until encodeValuePG parsed it, so the typmod was
			// never applied anywhere on the INSERT path. evalCast parses the string;
			// the typmod round below is what upstream time_in does via
			// AdjustTimeForTypmod.
			coerced, cerr = evalCast(row[i], "time", pos, ctx)
			if cerr == nil {
				coerced = roundTimeDatumToPrecision(coerced, timeColumnPrecision(col.Type))
			}
		case "timetz", "time with time zone":
			// M0119-0006: timetz joins the list because its input function is the
			// only one here whose answer depends on a GUC that only `ctx` carries
			// — a zone-less literal takes the SESSION TimeZone's offset. Left as a
			// KindString the value would reach encodeValuePG, which has no ctx and
			// so silently stored `10:00:00+00` for every session.
			coerced, cerr = evalCast(row[i], "timetz", pos, ctx)
			if cerr == nil {
				coerced = roundTimeDatumToPrecision(coerced, timeColumnPrecision(col.Type))
			}
		case "interval":
			// M0119-0006 (63rd slice): `interval` joins the list so its declared
			// typmod reaches the value — before this, an interval column's value
			// stayed KindString until encodeValuePG parsed it, so the column's
			// `interval(N)` / range qualifier was never applied anywhere on the
			// INSERT path. Parse through the SAME ParseIntervalBody tokenizer
			// encodeValuePG uses (not evalCast's limited `<n> <unit>` arm), then
			// apply AdjustIntervalForTypmod exactly as interval_in does at input.
			coerced, cerr = roundIntervalDatumToTypmod(row[i], intervalColumnTypmod(col.Type))
		case "numeric", "decimal":
			coerced, cerr = evalCast(row[i], "numeric", pos, ctx)
		case "regproc", "regprocedure", "regclass", "regtype", "regrole", "regcollation":
			// M0119-0006 (reg* + cid 4-byte storage): resolve a bare quoted name
			// literal to its OID before the heap arm stores it — a reg* name must
			// be a catalog lookup (regclassin/regtypein/regprocin/regrolein/
			// regcollationin), not the numeric parse encodeValuePG's oid arm would
			// otherwise run on it. (67th slice: regrole/regcollation joined.)
			coerced, cerr = regIdentifierInput(row[i], col.Type.Name, ctx, pos)
		default:
			continue
		}
		if cerr != nil {
			return cerr
		}
		row[i] = coerced
	}
	return nil
}

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
		applyDefaultsForMissing(cols, row, insertMissing, ctxSeqDBOid(o.ctx))

		// Auto-generate values for SERIAL / BIGSERIAL / SMALLSERIAL columns
		// and GENERATED [ALWAYS|BY DEFAULT] AS IDENTITY columns.
		// M0097-0009: if a serial/identity column's slot is still NullDatum (not provided
		// in the INSERT), call nextval on the implicit sequence. An explicit NULL
		// (column in INSERT target list but value is NULL) must NOT trigger
		// auto-generation — it falls through to the NOT NULL check below.
		// Shared with the upsert (INSERT ... ON CONFLICT) sibling path (root-0020).
		autoGenerateSerialValues(o.ctx, o.plan.Table.Name, cols, row, insertMissing)

		// Type coercion: coerce explicitly-provided literal values to the
		// column's declared type before FK/CHECK/domain/NOT-NULL constraint
		// checks run below. Catches smallint/int4 out-of-range and bigint
		// overflow from over-wide numeric literals (integer cases), and — as
		// important — turns a still-KindString DATE/TIMESTAMP/TIMESTAMPTZ/
		// NUMERIC literal into its typed Datum so downstream constraint-
		// violation DETAIL messages render it the same way SELECT/COPY/CAST
		// output does (fkValsForDetail's DateStyle fix otherwise still saw
		// the raw un-coerced literal on the INSERT path; M-NIGHTLY
		// 2026-07-15 follow-up). Shared with every UPDATE new-row
		// construction site via coerceRowForConstraintChecks
		// (pattern_sibling_paths_must_agree).
		if err := coerceRowForConstraintChecks(cols, row, func(i int) bool { return !insertMissing[i] }, o.ctx, o.plan.Pos()); err != nil {
			return nil, err
		}

		// BEFORE INSERT triggers (M0096-0012).
		if len(o.plan.Table.Triggers) > 0 {
			newRow, ok, err := fireTriggers(o.ctx, o.plan.Table, "before", "insert", nil, row)
			if err != nil {
				return nil, err
			}
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

		// Domain NOT NULL / CHECK constraint enforcement for domain-typed
		// columns (M0122-0005). Deferred for partitioned tables until after
		// routing, matching the NOT NULL check above.
		if !isPartitioned {
			if err := checkDomainConstraintsForRow(o.ctx, cols, row); err != nil {
				return nil, err
			}
		}

		// WITH CHECK OPTION enforcement: the INSERT's original target was a
		// CHECK OPTION view rewritten onto Table. M0119-0004 slice-365 follow-up.
		if o.plan.ViewCheckQual != nil {
			if err := checkViewCheckOption(o.ctx, o.plan.ViewCheckQual, o.plan.ViewCheckName, row); err != nil {
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
				// Lock every intermediate partition along the routing path (esp. a
				// sub-partitioned DEFAULT) so a concurrent ATTACH PARTITION that holds
				// ACCESS EXCLUSIVE on the default blocks this routed INSERT — and vice
				// versa — until one transaction commits, then the live catalog is
				// re-checked. partition-concurrent-attach (design 0118-0079).
				if lerr := lockRoutingPathPartitions(o.ctx, o.plan.Table, routedPart); lerr != nil {
					if ee, ok := lerr.(*ExecError); ok && ee.Pos == 0 {
						ee.Pos = o.plan.Pos()
					}
					return nil, lerr
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
			// Default-partition constraint (M0118-0008, design 0118-0078): a row
			// routed to (or directly inserted into) a default partition must not
			// belong to a non-default sibling. After a concurrent ATTACH PARTITION
			// commits, the new sibling is visible by the time this INSERT's lock is
			// granted, so re-routing claims the row and we raise 23514 —
			// partition-concurrent-attach.
			if cerr := checkDefaultPartitionInsertConstraint(o.ctx, partTable, partTable.Columns, partRow, o.plan.Pos()); cerr != nil {
				return nil, cerr
			}
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
			// Domain NOT NULL / CHECK constraint enforcement at the leaf
			// partition level (PG names the child table). M0122-0005.
			if err := checkDomainConstraintsForRow(o.ctx, partTable.Columns, partRow); err != nil {
				return nil, err
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
			// M0104-0007 / M0118-0001: SSI write-path hook on the newly
			// inserted tuple's (block, slot). Conflict-in installs an rw-edge
			// against any SERIALIZABLE reader that holds a covering predicate
			// lock (page or relation grain), and aborts this INSERT in place
			// (40001) when it closes a dangerous structure to a committed pivot.
			if serr := ssiRecordTupleWrite(o.ctx, targetRel, ptr.Block, ptr.Offset); serr != nil {
				return nil, serr
			}
			// SSI hash-index bucket conflict-in (design 0118-0099): forms the
			// rw-edge against a SERIALIZABLE reader holding the inserted value's
			// bucket SIREAD on a hash index.
			if serr := ssiRecordHashIndexInsert(o.ctx, partTable, partTable.Columns, partRow, targetRel.DBOid); serr != nil {
				return nil, serr
			}
			// SSI GiST grid-cell conflict-in (design 0118-0137): forms the rw-edge
			// against a SERIALIZABLE reader holding the inserted point's grid-cell
			// SIREAD on a GiST index.
			if serr := ssiRecordGistIndexInsert(o.ctx, partTable, partTable.Columns, partRow, targetRel.DBOid); serr != nil {
				return nil, serr
			}
			// SSI GIN key-grain conflict-in (design 0118-0140): forms the rw-edge
			// against a SERIALIZABLE reader holding a matching search-key SIREAD on
			// a GIN index.
			if serr := ssiRecordGinIndexInsert(o.ctx, partTable, partTable.Columns, partRow, targetRel.DBOid); serr != nil {
				return nil, serr
			}
			maintainUniqueIndexesForInsert(o.ctx, partTable, partTable.Columns, partRow, ptr)
			o.appendInsertRetRow(row)
			o.rowsAffected++
			continue
		}
		// No matching partition found (or non-partitioned) — write to parent.
		// If the target is itself a (non-partitioned) partition child, enforce its
		// partition constraint — a direct INSERT into a leaf default partition must
		// not write a row owned by a non-default sibling (design 0118-0078). No-op
		// for non-partition-child tables (the walk exits when there is no parent).
		if cerr := checkDefaultPartitionInsertConstraint(o.ctx, o.plan.Table, cols, row, o.plan.Pos()); cerr != nil {
			return nil, cerr
		}
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
		// M0104-0007 / M0118-0001: SSI write-path hook for the non-partitioned
		// insert path; aborts in place (40001) on a committed-pivot structure.
		if serr := ssiRecordTupleWrite(o.ctx, targetRel, ptr.Block, ptr.Offset); serr != nil {
			return nil, serr
		}
		// SSI hash-index bucket conflict-in (design 0118-0099): forms the rw-edge
		// against a SERIALIZABLE reader holding the inserted value's bucket SIREAD
		// on a hash index.
		if serr := ssiRecordHashIndexInsert(o.ctx, o.plan.Table, cols, row, targetRel.DBOid); serr != nil {
			return nil, serr
		}
		// SSI GiST grid-cell conflict-in (design 0118-0137): forms the rw-edge
		// against a SERIALIZABLE reader holding the inserted point's grid-cell
		// SIREAD on a GiST index.
		if serr := ssiRecordGistIndexInsert(o.ctx, o.plan.Table, cols, row, targetRel.DBOid); serr != nil {
			return nil, serr
		}
		// SSI GIN key-grain conflict-in (design 0118-0140): forms the rw-edge
		// against a SERIALIZABLE reader holding a matching search-key SIREAD on a
		// GIN index.
		if serr := ssiRecordGinIndexInsert(o.ctx, o.plan.Table, cols, row, targetRel.DBOid); serr != nil {
			return nil, serr
		}
		maintainUniqueIndexesForInsert(o.ctx, o.plan.Table, cols, row, ptr)
		// AFTER INSERT triggers (M0097-0140).
		if len(o.plan.Table.Triggers) > 0 {
			if _, _, err := fireTriggers(o.ctx, o.plan.Table, "after", "insert", nil, row); err != nil {
				return nil, err
			}
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
	// Snapshot-relative partition visibility: a child marked detach-pending by an
	// in-progress ALTER TABLE … DETACH PARTITION … CONCURRENTLY is invisible to
	// any statement whose snapshot epoch is at or after the detach epoch, so
	// routing an INSERT to it must fail with "no partition found" — exactly as the
	// SELECT-side planner expansion omits it (sibling-paths discipline). A
	// REPEATABLE READ transaction (snapshot frozen before the detach) keeps routing
	// to it. Design 0118-0059 (M0118-0008 detach-partition-concurrently-1, s3ins2).
	if child != nil && ctx != nil && child.DetachPendingEpoch != 0 &&
		ctx.Snap.PartitionDetachEpoch >= child.DetachPendingEpoch {
		child = nil
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

// checkDefaultPartitionInsertConstraint enforces a DEFAULT partition's partition
// constraint at INSERT time: a row written into (or routed to) a default partition
// must NOT belong to any of its non-default sibling partitions. PostgreSQL attaches
// this implicit constraint (the negation of every sibling's bounds) to the default
// partition and checks it on every insert — direct or partition-routed — via
// ExecPartitionCheck. goopg has no per-row partition-constraint expression, so we
// reconstruct the check from the live catalog: walk the leaf's partition ancestry
// and, for every ancestor that is a DEFAULT partition, re-route the row's
// corresponding parent partition-key value through the parent's scheme; if it lands
// on a non-default sibling, the default's constraint is violated.
//
// This is what makes partition-concurrent-attach fail correctly: when a concurrent
// ALTER TABLE … ATTACH PARTITION commits while this INSERT is blocked on the default
// partition's lock, the newly attached non-default sibling is visible by the time
// the lock is granted, so the re-routing now claims the row and we raise 23514 —
// exactly as PG (design 0118-0078). leafCols/leafRow are in the routed leaf's column
// order; a partition child always carries every ancestor partition-key column by
// name, so an ancestor's key resolves directly from a leaf-ordered row. Returns nil
// when no default ancestor claims the row (the normal case, including ordinary
// routing to a default that legitimately owns the value).
func checkDefaultPartitionInsertConstraint(ctx *Context, leaf *catalog.Table, leafCols []catalog.Column, leafRow Row, pos int) error {
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	cur := leaf
	for guard := 0; cur != nil && cur.PartitionParentOID != 0 && guard < 8; guard++ {
		parent, ok := im.LookupTableByOID(cur.PartitionParentOID)
		if !ok || parent == nil {
			break
		}
		if isDefaultPartitionChild(cur) {
			sib := routePartitionKeyToImmediateChild(parent, leafCols, leafRow, im, ctx)
			if sib != nil && sib.OID != cur.OID {
				return &ExecError{Code: "23514", Pos: pos,
					Message: fmt.Sprintf("new row for relation %q violates partition constraint", cur.Name)}
			}
		}
		cur = parent
	}
	return nil
}

// lockRoutingPathPartitions takes a transaction-scoped ROW EXCLUSIVE lock on every
// INTERMEDIATE partition the router descended through on the way from the named
// INSERT target down to the routed leaf — i.e. every node strictly between the two.
// The named target itself is already locked in insertOp.Open and the leaf is the
// heap-write target (its locking is unchanged), so this fills in only the
// in-between partitions, mirroring PostgreSQL, where ExecInsert opens every
// partition a tuple is routed into in RowExclusiveLock.
//
// The load-bearing case is an intermediate DEFAULT partition that is itself
// sub-partitioned (partition-concurrent-attach): an INSERT INTO tpart whose row
// routes tpart → tpart_default → tpart_default_default must hold ROW EXCLUSIVE on
// tpart_default so a concurrent ALTER TABLE tpart ATTACH PARTITION — which holds
// ACCESS EXCLUSIVE on the default partition (design 0118-0076) — blocks it until
// the attach commits. Once the lock is granted, the just-committed sibling is
// visible in the live catalog, so checkDefaultPartitionInsertConstraint re-routes
// the row onto it and raises 23514 (perm 1/2). Symmetrically, when the INSERT
// commits first, ATTACH's AccessExclusive acquire on the default waits behind this
// ROW EXCLUSIVE until the INSERT's transaction ends, then the attach-side re-scan
// (checkDefaultPartitionDataConflict) finds the rows and raises 23P01 (perm 3).
//
// RowExclusive is self-compatible and conflicts only with DDL-grade lock modes, so
// ordinary concurrent partitioned INSERTs never block each other. A single-level
// partitioned table (leaf's parent IS the named target) has no intermediates, so
// this is a no-op there. M0118-0008 (design 0118-0079).
func lockRoutingPathPartitions(ctx *Context, named, leaf *catalog.Table) error {
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok || named == nil || leaf == nil || leaf.PartitionParentOID == 0 {
		return nil
	}
	cur, found := im.LookupTableByOID(leaf.PartitionParentOID)
	for guard := 0; found && cur != nil && cur.OID != named.OID && guard < 8; guard++ {
		if err := ctx.acquireWriteLockTxn(ctx.Catalog.RelFileNode(cur)); err != nil {
			return err
		}
		if cur.PartitionParentOID == 0 {
			break
		}
		cur, found = im.LookupTableByOID(cur.PartitionParentOID)
	}
	return nil
}

// isDefaultPartitionChild reports whether t is a DEFAULT partition.
func isDefaultPartitionChild(t *catalog.Table) bool {
	for _, pb := range t.PartitionBounds {
		if pb.IsDefault {
			return true
		}
	}
	return false
}

// routePartitionKeyToImmediateChild routes a row to the IMMEDIATE partition child
// of parent (no recursion into sub-partitions), reading parent's partition-key
// columns by NAME from leafCols/leafRow. Returns nil for unsupported key shapes
// (expression keys, HASH) so the caller never raises a false-positive constraint
// violation.
func routePartitionKeyToImmediateChild(parent *catalog.Table, leafCols []catalog.Column, leafRow Row, im *catalog.InMemory, ctx *Context) *catalog.Table {
	if len(parent.PartitionKey) == 0 {
		return nil // expression-key partitioning: out of scope, no false positive
	}
	resolve := func(name string) (Datum, bool) {
		for idx, c := range leafCols {
			if strings.EqualFold(c.Name, name) && idx < len(leafRow) {
				return leafRow[idx], true
			}
		}
		return NullDatum, false
	}
	switch parent.PartitionMethod {
	case "RANGE":
		keyStrs := make([]string, 0, len(parent.PartitionKey))
		for _, kc := range parent.PartitionKey {
			d, ok := resolve(kc)
			if !ok {
				return nil
			}
			keyStrs = append(keyStrs, partitionKeyDatumToRangeStr(d))
		}
		return im.FindRangePartitionForDatums(parent.OID, keyStrs)
	case "LIST":
		d, ok := resolve(parent.PartitionKey[0])
		if !ok {
			return nil
		}
		return im.FindPartitionForValue(parent.OID, partitionKeyDatumToListStr(d))
	}
	return nil
}

// partitionKeyDatumToRangeStr formats a datum the way FindRangePartitionForDatums
// expects (mirrors the RANGE arm of routeToPartitionDepth).
func partitionKeyDatumToRangeStr(d Datum) string {
	switch d.Kind {
	case KindInt:
		return fmt.Sprintf("%d", d.Int)
	case KindString, KindNumeric:
		return d.StringValue()
	default:
		if d.IsNull() {
			return "null"
		}
		return d.Format()
	}
}

// partitionKeyDatumToListStr formats a datum the way FindPartitionForValue expects
// (mirrors the LIST arm of routeToPartitionDepth).
func partitionKeyDatumToListStr(d Datum) string {
	switch d.Kind {
	case KindInt:
		return fmt.Sprintf("%d", d.Int)
	case KindString:
		return d.StringValue()
	case KindBool:
		if d.Int != 0 {
			return "true"
		}
		return "false"
	default:
		if d.IsNull() {
			return "null"
		}
		return d.Format()
	}
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
	indexes := ctx.Catalog.IndexesOnTable(plan.Table, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
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
	oldLineSlot, newLineSlot uint16, xmax storage.TransactionID,
	tupleBytes []byte,
) error {
	// Adopt PG's self-pointing t_ctid for the HOT new version (the chain tail),
	// matching what replay reconstructs (xl_heap_update carries no new t_ctid).
	// A2-pre made fresh inserts self-pointing; HOT new versions go through this
	// path, not markHeapInsertDirty, so stamp here too. isChainTailCTID treats
	// self identically to the legacy {Invalid,0}. Caller holds slot.Lock and has
	// already PageAddHeapTuple'd the new version at newLineSlot.
	if err := storage.PageSetHeapTupleCtid(slot.Page(), newLineSlot, storage.ItemPointer{Block: blk, Offset: newLineSlot}); err != nil {
		return err
	}
	logHot := pool.LogHeapHotUpdate()
	if logHot == nil {
		pool.MarkDirty(slot)
		return nil
	}
	// MarkDirtyLogicalChange — see markHeapInsertDirty for the
	// rationale.
	return pool.MarkDirtyLogicalChange(slot, func() (storage.LSN, error) {
		return logHot(rel, blk, oldLineSlot, newLineSlot, xmax, tupleBytes)
	})
}

// isSelfModifiedWrite reports whether the tuple carries a write intent
// (update/delete) stamped by myXID itself — upstream's TM_SelfModified, which
// makes ExecUpdate/ExecDelete skip the row instead of writing a second new
// version (postgres/src/backend/executor/nodeModifyTable.c).
//
// It is the sibling-safe form of the M0131-S32.1 guard: both the index-scan and
// the seq-scan update loops call THIS function, so the two can no longer drift
// apart. It reproduces the structure of upstream HeapTupleSatisfiesUpdate
// (postgres/src/backend/access/heap/heapam_visibility.c), which reaches the raw
// "is this xmax mine?" comparison only after two earlier arms have returned:
//
//   - HEAP_XMAX_IS_MULTI has its own branch, so a raw Xmax holding a
//     MultiXactId is never compared against a TransactionId. The two live in
//     different id spaces; a numeric collision would skip an unrelated row.
//   - HEAP_XMAX_IS_LOCKED_ONLY returns TM_BeingModified, *not* TM_SelfModified.
//     A row this transaction merely LOCKED (SELECT ... FOR UPDATE / FOR NO KEY
//     UPDATE) is still updatable by it.
//
// The missing LOCKED_ONLY arm is what M-NIGHTLY AI-20260813-005117-012 caught:
// `BEGIN; SELECT ... FOR UPDATE; UPDATE <key column> ...;` silently reported
// UPDATE 0. Only a key-changing update is HOT-ineligible, so it is the one
// shape that falls through to this guard — a non-key update takes the HOT path
// and never reaches it, which is why the bug stayed narrow enough to survive.
func isSelfModifiedWrite(h storage.HeapTupleHeader, myXID storage.TransactionID) bool {
	if h.Xmax == storage.InvalidTransactionID {
		return false
	}
	if storage.IsHeapTupleXmaxMulti(h.Infomask) {
		return false
	}
	if storage.IsHeapTupleLockOnly(h.Infomask) {
		return false
	}
	return h.Xmax == myXID
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
	if h.IsHotUpdated() {
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
	survivors, err := survivingLockersForUpdate(ctx, hdr, keysUpdated)
	if err != nil {
		return 0, 0, 0, false, err
	}
	if len(survivors) == 0 {
		return 0, 0, 0, false, nil
	}
	// Re-add our writer as the updater member alongside the preserved lockers.
	members := append(survivors, multixact.Member{Xid: effectiveWriterXID(ctx), Status: updaterMemberStatus(keysUpdated)})
	multi, err := ctx.MultiXact.CreateFromMembers(members)
	if err != nil {
		return 0, 0, 0, false, err
	}
	infomask, infomask2 := multixact.HintBits(members)
	return multi, infomask, infomask2, true, nil
}

// survivingLockersForUpdate returns the pre-existing lock-only holders on a
// tuple about to be updated/deleted by this transaction that must be PRESERVED
// (carried forward), i.e. NOT including our own updater member. It is the shared
// core of two producers: the old tuple's {updater + survivors} stamp
// (stampUpdaterXmaxPreservingLockers) AND the new tuple version's inherited lock
// xmax (carryForwardLockersToNewTuple) — upstream heap_update propagates the
// non-conflicting lockers both onto the old tuple's xmax (so a chain walker still
// honours them) and forward onto the new version (so the lock is not forgotten
// when the updater commits, multixact-no-forget).
//
// Retention rules (mirroring MultiXactIdExpand / compute_new_xmax_infomask):
//   - drop our own current writer member (re-added as the updater by the caller
//     that needs it);
//   - carry forward, unchanged, any member from our OWN transaction tree at an
//     outer level (top-level xact or an enclosing savepoint) — never conflicts
//     with us and ROLLBACK TO must restore it (delete-abort-savept);
//   - drop dead foreign lockers (lock already released);
//   - drop foreign lockers whose mode CONFLICTS with our update (they must be
//     waited for upstream, not preserved);
//   - keep still-active non-conflicting foreign lockers (e.g. FOR KEY SHARE
//     under a no-key UPDATE — the multixact-no-forget scenario).
//
// Returns an empty slice (the common case) when the xmax is not a preserved
// lock-only holder set, so callers keep the plain single-xid stamp. The gate (a
// pre-existing active foreign lock-only holder) never arises on the pgbench
// TPC-B / TPC-H hot path. M0118-0009 (docs/design/0118-0016).
func survivingLockersForUpdate(ctx *Context, hdr storage.HeapTupleHeader, keysUpdated bool) ([]multixact.Member, error) {
	if ctx.MultiXact == nil || ctx.TxnMgr == nil {
		return nil, nil
	}
	// Only the lock-only case is preserved here. A non-lock-only (updater-bearing)
	// xmax means a concurrent modify, which isConcurrentlyUpdated/EPQ handles
	// upstream of the stamp; reaching the stamp with such an xmax is not this
	// producer's concern.
	if hdr.Xmax == storage.InvalidTransactionID || !storage.IsHeapTupleLockOnly(hdr.Infomask) {
		return nil, nil
	}

	// Enumerate the existing lock holders (single-xid or multi).
	var existing []multixact.Member
	if storage.IsHeapTupleXmaxMulti(hdr.Infomask) {
		members, ok := ctx.MultiXact.Members(multixact.MultiXactId(hdr.Xmax))
		if !ok {
			// Membership lost (stamped before a restart with no persisted store):
			// nothing resolvable to combine with; keep the plain stamp.
			return nil, nil
		}
		existing = members
	} else {
		existing = []multixact.Member{{Xid: hdr.Xmax, Status: lockOnlyMemberStatus(hdr.Infomask, hdr.Infomask2)}}
	}

	ourStatus := updaterMemberStatus(keysUpdated)
	ourXID := effectiveWriterXID(ctx) // sub-XID inside a savepoint; else top-level
	ourTop := ctx.TxnMgr.TopLevelXid(ourXID)
	survivors := make([]multixact.Member, 0, len(existing)+1)
	for _, m := range existing {
		if m.Xid == ourXID {
			continue // exactly our writer: re-added by the updater-stamp caller
		}
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
	return survivors, nil
}

// carryForwardLockersToNewTuple stamps the new tuple version (at newSlot, already
// inserted on `page`) with the non-conflicting lockers inherited from the old
// tuple `oldHdr`, as a lock-only MultiXactId. This is the new-version half of
// PostgreSQL heap_update's lock propagation (compute_new_xmax_infomask): KEY
// SHARE / SHARE lockers that do not conflict with a no-key update are carried
// onto the successor so the lock survives the updater's commit — otherwise a
// later locker on the committed-updated row finds no holder and proceeds out of
// order (multixact-no-forget: after s2's no-key UPDATE commits, s3's FOR UPDATE
// must still wait on s1's inherited FOR KEY SHARE until s1 commits).
//
// No-op (returns nil) when there is nothing to carry — the common case and the
// pgbench hot path. M0118-0009 (docs/design/0118-0016).
func carryForwardLockersToNewTuple(ctx *Context, page storage.Page, oldHdr storage.HeapTupleHeader, newSlot uint16, keysUpdated bool) error {
	lockers, err := survivingLockersForUpdate(ctx, oldHdr, keysUpdated)
	if err != nil || len(lockers) == 0 {
		return err
	}
	multi, err := ctx.MultiXact.CreateFromMembers(lockers)
	if err != nil {
		return err
	}
	infomask, infomask2 := multixact.HintBits(lockers)
	return storage.PageSetHeapTupleXmaxMulti(page, newSlot, storage.TransactionID(multi), infomask, infomask2)
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
		if cerr := storage.PageSetHeapTupleXmaxMulti(page, slot, storage.TransactionID(multi), im, im2); cerr != nil {
			return cerr
		}
		// M0129-S8.3: stamp the deleting command id (cmax) on the old
		// tuple. Mirrors PG's heap_delete / heap_update calling
		// HeapTupleHeaderSetCmax after the xmax write, so the visibility
		// check can reveal the pre-image (cmax >= curcid) while hiding
		// tuples inserted by later commands (cmin >= curcid).
		return storage.PageSetHeapTupleCmax(page, slot, ctx.GetCurrentCommandId(true), false)
	}
	if serr := storage.PageSetHeapTupleXmax(page, slot, effectiveWriterXID(ctx)); serr != nil {
		return serr
	}
	// M0129-S8.3: stamp the deleting command id (cmax) on the old tuple.
	// See the multi-path comment above for the visibility rationale.
	if cerr := storage.PageSetHeapTupleCmax(page, slot, ctx.GetCurrentCommandId(true), false); cerr != nil {
		return cerr
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

// stampMovedPartitionOldTuple stamps the old tuple of a cross-partition UPDATE
// (a row whose new key routes it to a different partition child): xmax plus the
// {InvalidBlockNumber, MovedPartitionsOffsetNumber} sentinel CTID, and then the
// deleting command id (cmax).
//
// The cmax half is not optional. Upstream heap_delete calls
// HeapTupleHeaderSetCmax (heapam.c:3065) unconditionally and only *afterwards*
// adds HeapTupleHeaderSetMovedPartitions (heapam.c:3071) — the sentinel is an
// addition to the normal delete stamp, not a replacement for it. goopg's three
// cross-partition stamp sites previously wrote only the sentinel, leaving the
// tuple's stale t_cid (its cmin, from whichever command inserted it) to stand in
// for cmax. mvcc.TupleVisible then reaches the `effXmax == currentXID` arm and
// reads `cmax >= curcid` as "deleted by a later command — pre-image visible", so
// the deleting transaction kept seeing its own moved-away row. Other sessions
// were unaffected (they judge xmax by the snapshot, never by cmax), which is why
// the bug only ever surfaced in the writer's own re-scan.
//
// M-NIGHTLY AI-20260809-020705-018 (isolation spec merge-update, permutation
// `pa_merge1 pa_merge2a c1 pa_select2 c2`).
func stampMovedPartitionOldTuple(ctx *Context, page storage.Page, slot uint16) error {
	if err := storage.PageSetHeapTupleMovedPartition(page, slot, effectiveWriterXID(ctx)); err != nil {
		return err
	}
	return storage.PageSetHeapTupleCmax(page, slot, ctx.GetCurrentCommandId(true), false)
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
	// HEAP_ONLY_TUPLE lives in t_infomask2 (M0131-S11; htup_details.h:568).
	tup.Header.SetHeapOnly()
	tup.Header.SetNatts(len(cols))
	tup.Header.Infomask |= storage.HeapXmaxInvalid
	// HEAP_HASVARWIDTH: PG18 nocachegetattr crashes when this bit is
	// missing on a TupleDesc with varlena attrs. Mirrors PG's
	// heap_fill_tuple (heaptuple.c:326). M0118-0131.
	if pgRowHasVarWidth(cols, newRow) {
		tup.Header.Infomask |= storage.HeapHasVarWidth
	}
	// HEAP_HASEXTERNAL: PG's heap_deform_tuple needs this bit to skip
	// external TOAST pointers. Mirrors heap_fill_tuple (heaptuple.c:343).
	// M0118-0131.
	if pgRowHasExternal(cols, newRow) {
		tup.Header.Infomask |= storage.HeapHasExternal
	}
	// M0129-S8.3c: stamp the inserting command id (cmin).
	tup.Header.SetCmin(ctx.GetCurrentCommandId(true))
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
		s321Note(s321HOTSkipItemID)
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
		if wfgDebug {
			wfgNoteTarget(ctx.Tx.XID, rel, blk, oldSlot, oldTuple.Header)
		}
		if dl, terr := epqWait(ctx, xmax); terr != nil {
			return false, terr
		} else if dl {
			// Deadlock detected — surface 40001 immediately rather than
			// looping into the delete+insert EPQ path.
			return false, &ExecError{
				Code:    "40001",
				Message: "could not serialize access due to concurrent update (deadlock)",
			}
		}
		s321Note(s321HOTSkipConcurrent)
		return false, nil // fall back to delete+insert; caller re-checks
	}
	// M0131-S30.3 probe (temporary, GOOPG_PAGEIDENT_PROBE=1): this is the exact
	// site whose WAL record and on-disk page disagree. Under the content lock
	// the line-pointer count is stable, so a count BELOW the high-water mark
	// previously seen for {rel, blk} proves the slot's bytes belong to another
	// block. See internal/storage/pageident_probe.go.
	storage.PageIdentityObserve(storage.BufferTag{Rel: rel, Block: blk}, s.Page(), "hotUpdate")
	// Line-pointer count before the (unlogged) append, so the orphan-cleanup
	// arm below can assert the add/remove pair left the page untouched.
	lpBeforeAdd := -1
	if storage.PageIdentityProbeEnabled() {
		if n, cerr := storage.PageLinePointerCount(s.Page()); cerr == nil {
			lpBeforeAdd = n
		}
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
			s321Note(s321HOTNoSpace)
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
			// Carry the old tuple's non-conflicting lockers forward onto the new
			// version BEFORE re-stamping the old tuple's xmax (the stamp below
			// rewrites it). A HOT update never changes a key column, so the
			// updater status is no-key (keysUpdated=false) and a FOR KEY SHARE
			// holder is preserved (multixact-no-forget). M0118-0009.
			if cerr := carryForwardLockersToNewTuple(ctx, s.Page(), oldHdrTup.Header, newSlot, false); cerr != nil {
				return cerr
			}
			multi, im, im2, ok, perr := stampUpdaterXmaxPreservingLockers(ctx, oldHdrTup.Header, false)
			if perr != nil {
				return perr
			}
			if ok {
				if serr := storage.PageStampHotOldTupleMulti(s.Page(), oldSlot, storage.TransactionID(multi), im, im2, effectiveWriterXID(ctx), blk, newSlot); serr != nil {
					return serr
				}
				// M0129-S8.3: stamp cmax on the HOT old tuple.
				return storage.PageSetHeapTupleCmax(s.Page(), oldSlot, ctx.GetCurrentCommandId(true), false)
			}
		}
		if serr := storage.PageStampHotOldTuple(s.Page(), oldSlot, effectiveWriterXID(ctx), blk, newSlot); serr != nil {
			return serr
		}
		// M0129-S8.3: stamp cmax on the HOT old tuple.
		return storage.PageSetHeapTupleCmax(s.Page(), oldSlot, ctx.GetCurrentCommandId(true), false)
	}()
	if stampErr != nil {
		// Clean up the orphan new tuple: PageAddHeapTuple already
		// wrote it to the page, but the old-slot stamp failed
		// (e.g. PagePruneOpt invalidated the old slot). Without
		// cleanup the tuple persists as a live HEAP_ONLY_TUPLE
		// with no CTID link, wasting space and inflating the
		// line-pointer count. M0118-0131.
		//
		// Neither the add nor this removal emits WAL, so the pair MUST
		// leave the page exactly as the last logged record left it —
		// otherwise the buffer that reaches disk (it is already dirty
		// from the logged PagePruneOpt above) disagrees with the page
		// replay rebuilds. PageAddHeapTuple always appends, so newSlot
		// is the last line pointer and PageRemoveHeapTuple undoes it
		// exactly, pd_lower AND pd_upper. M0131-S30.5 / heap.go.
		if remErr := storage.PageRemoveHeapTuple(s.Page(), newSlot); remErr != nil {
			// Non-fatal: page is still structurally valid; the
			// orphan wastes space until the next VACUUM repacks
			// the page. Do not surface this to the client.
		} else if lpBeforeAdd >= 0 {
			// M0131-S30.3 probe: prove the unlogged pair is a page no-op.
			storage.PageIdentityAssertCount(storage.BufferTag{Rel: rel, Block: blk}, s.Page(), lpBeforeAdd, "hotOrphanClean")
		}
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

	// M0131-S30.3 probe step (a): the value about to be written into the record's
	// new_off must describe THIS page — newSlot == line-pointer count, and never
	// below the high-water count seen for {rel, blk}. Still under the content
	// lock, so the counts are stable.
	storage.PageIdentityAssertEmit(storage.BufferTag{Rel: rel, Block: blk}, s.Page(), newSlot, "hotUpdateEmit")
	derr := markHeapHotUpdateDirty(ctx.Pool, s, rel, blk, oldSlot, newSlot, effectiveWriterXID(ctx), tupleBytes)
	s.Unlock()
	ctx.Pool.Unpin(s)
	s321Note(s321HOTApplied)
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

// childFKsToRecheck returns the foreign keys declared ON the updated table
// whose referencing columns are modified by this UPDATE's SET list. PostgreSQL
// fires the RI_FKey_check (the referenced parent row must exist) AFTER trigger
// only when a key column actually changes, so an UPDATE that touches no FK
// column performs no parent lookup. Mirroring that both matches PG and bounds
// the new check's blast radius to FK-key UPDATEs on tables that have FKs.
// Computed once per UPDATE (the SET list is fixed). M0118-0008
// (detach-partition-concurrently-4).
func (o *updateOp) childFKsToRecheck() []catalog.ForeignKey {
	tbl := o.plan.Table
	if len(tbl.ForeignKeys) == 0 {
		return nil
	}
	var out []catalog.ForeignKey
	for _, fk := range tbl.ForeignKeys {
		for _, fc := range fk.Columns {
			idx := -1
			for i, col := range tbl.Columns {
				if strings.EqualFold(col.Name, fc) {
					idx = i
					break
				}
			}
			if idx >= 0 && idx < len(o.plan.Set) && o.plan.Set[idx] != nil {
				out = append(out, fk)
				break
			}
		}
	}
	return out
}

// recheckChildFKs verifies that newRow's modified FK key values still reference
// an existing parent row. newRow must be aligned to o.plan.Table.Columns; pass
// scanTbl so rows that came from an inheritance child (different column layout)
// are skipped — those children carry their own FKs and are not exercised here.
// M0118-0008 (detach-partition-concurrently-4).
func (o *updateOp) recheckChildFKs(fks []catalog.ForeignKey, newRow Row, scanTbl *catalog.Table) error {
	if len(fks) == 0 {
		return nil
	}
	if scanTbl != nil && scanTbl != o.plan.Table {
		return nil
	}
	return checkFKInsertForConstraints(o.ctx, o.plan.Table, nil, newRow, fks)
}

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
	if !dmlPrivilegePermitted(ctx, o.plan.Table, "UPDATE") {
		return &ExecError{Code: "42501", Pos: o.plan.Pos(), Message: fmt.Sprintf("permission denied for table %s", o.plan.Table.Name)}
	}
	rel := ctx.Catalog.RelFileNode(o.plan.Table)
	if err := ctx.acquireRelLock(rel, lockmgr.RowExclusiveLock); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.plan.Pos()
		}
		return err
	}
	if err := ctx.acquireWriteLockTxn(rel); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.plan.Pos()
		}
		return err
	}
	return nil
}

func (o *updateOp) Close() error {
	// Cumulative relation stats: stage updated tuples (each leaves a dead tuple;
	// goopg has no HOT update) for the current transaction. Gated by track_counts.
	// M0118-0009 (`stats`, rung 7; design 0118-0131).
	recordRelUpdate(o.ctx, tableOIDFromCatalog(o.plan.Table), o.rowsAffected)
	return nil
}

// nextVirtualPgDatabase implements `UPDATE pg_database SET ... WHERE ...`
// against the Virtual (no physical heap) pg_database table. goopg's
// pg_database is entirely computed by VirtualRows() — there is no
// RelFileNode/heap-tuple machinery to scan or rewrite, so this reads the
// current rows straight from VirtualRows(), evaluates o.pred/o.plan.Set
// against them exactly like the physical path would, and persists the one
// writable column (datconnlimit) through catalog.InMemory.SetDatabaseConnLimit
// — the same "runtime InMemory truth, no on-disk write" precedent
// CreateCollation/CreateExtension already use. Any SET column other than
// datconnlimit errors rather than silently discarding the write, since
// pretending an unsupported column persisted would be worse than refusing it.
// M-NIGHTLY AI-20260707-000712-004 follow-up / AC-002 residual #1.
func (o *updateOp) nextVirtualPgDatabase() (TupleSlot, error) {
	tbl := o.plan.Table
	im, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil, &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "pg_database UPDATE requires an InMemory catalog"}
	}
	datnameOrd, connLimitOrd := -1, -1
	for i, c := range tbl.Columns {
		switch {
		case strings.EqualFold(c.Name, "datname"):
			datnameOrd = i
		case strings.EqualFold(c.Name, "datconnlimit"):
			connLimitOrd = i
		}
	}
	for i, setExpr := range o.plan.Set {
		if setExpr != nil && i != connLimitOrd {
			colName := "?"
			if i < len(tbl.Columns) {
				colName = tbl.Columns[i].Name
			}
			return nil, &ExecError{Code: "0A000", Pos: o.plan.Pos(),
				Message: fmt.Sprintf("UPDATE pg_database is only supported for the datconnlimit column (got %q)", colName)}
		}
	}
	for _, rawRow := range tbl.VirtualRows() {
		row := make(Row, len(tbl.Columns))
		for j := range tbl.Columns {
			cell := ""
			if j < len(rawRow) {
				cell = rawRow[j]
			}
			v, err := evalExpr(planner.TypedVirtualCell(o.plan.Pos(), cell, tbl.Columns[j].Type.Name), nil, o.ctx)
			if err != nil {
				return nil, err
			}
			row[j] = v
		}
		if o.pred != nil {
			v, err := evalExpr(o.pred, row, o.ctx)
			if err != nil {
				return nil, err
			}
			if v.IsNull() || v.Kind != KindBool || !v.BoolValue() {
				continue
			}
		}
		o.rowsAffected++
		if connLimitOrd >= 0 && o.plan.Set[connLimitOrd] != nil {
			newVal, err := evalExpr(o.plan.Set[connLimitOrd], row, o.ctx)
			if err != nil {
				return nil, err
			}
			if newVal.IsNull() {
				return nil, &ExecError{Code: "23502", Pos: o.plan.Pos(),
					Message: "null value in column \"datconnlimit\" violates not-null constraint"}
			}
			datname := ""
			if datnameOrd >= 0 && datnameOrd < len(rawRow) {
				datname = rawRow[datnameOrd]
			}
			im.SetDatabaseConnLimit(datname, int32(newVal.Int))
			// M0122-0006: persist the new datconnlimit to the on-disk
			// pg_database heap row (global/1262) so it survives restart.
			// Best-effort — the in-memory registry is goopg's truth.
			if dbOid, ok := im.ResolveDatabaseOid(datname); ok && dbOid != 0 {
				_ = PersistDatConnLimit(o.ctx, dbOid, int32(newVal.Int))
			}
			row[connLimitOrd] = newVal
		}
		o.appendUpdateRetRow(row)
	}
	return nil, EOF
}

// nextVirtualPgAmproc implements `UPDATE pg_amproc SET amproc = ... WHERE
// ...` against the Virtual (no physical heap) pg_amproc table — the same
// "read from VirtualRows(), evaluate o.pred/o.plan.Set, persist through a
// dedicated InMemory mutator" shape as nextVirtualPgDatabase above. This is
// the exact statement pg_amcheck's upstream 005_opclass_damage.pl uses to
// inject corruption (`UPDATE pg_catalog.pg_amproc SET amproc =
// 'int4_desc_cmp'::regproc WHERE amproc = 'int4_asc_cmp'::regproc`); only
// the `amproc` column is writable, persisted via
// catalog.InMemory.SetAmProcMemberProc keyed by the matched row's own oid.
// M0119-0006 (005_opclass_damage UPDATE-path prerequisite — the btree AM
// still has no per-index comparator dispatch to make a corrupted amproc
// observable, a separate, larger gap tracked in the deferral ledger).
func (o *updateOp) nextVirtualPgAmproc() (TupleSlot, error) {
	tbl := o.plan.Table
	im, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil, &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "pg_amproc UPDATE requires an InMemory catalog"}
	}
	oidOrd, amprocOrd := -1, -1
	for i, c := range tbl.Columns {
		switch {
		case strings.EqualFold(c.Name, "oid"):
			oidOrd = i
		case strings.EqualFold(c.Name, "amproc"):
			amprocOrd = i
		}
	}
	for i, setExpr := range o.plan.Set {
		if setExpr != nil && i != amprocOrd {
			colName := "?"
			if i < len(tbl.Columns) {
				colName = tbl.Columns[i].Name
			}
			return nil, &ExecError{Code: "0A000", Pos: o.plan.Pos(),
				Message: fmt.Sprintf("UPDATE pg_amproc is only supported for the amproc column (got %q)", colName)}
		}
	}
	for _, rawRow := range tbl.VirtualRows() {
		row := make(Row, len(tbl.Columns))
		for j := range tbl.Columns {
			cell := ""
			if j < len(rawRow) {
				cell = rawRow[j]
			}
			v, err := evalExpr(planner.TypedVirtualCell(o.plan.Pos(), cell, tbl.Columns[j].Type.Name), nil, o.ctx)
			if err != nil {
				return nil, err
			}
			row[j] = v
		}
		if o.pred != nil {
			v, err := evalExpr(o.pred, row, o.ctx)
			if err != nil {
				return nil, err
			}
			if v.IsNull() || v.Kind != KindBool || !v.BoolValue() {
				continue
			}
		}
		o.rowsAffected++
		if amprocOrd >= 0 && o.plan.Set[amprocOrd] != nil {
			newVal, err := evalExpr(o.plan.Set[amprocOrd], row, o.ctx)
			if err != nil {
				return nil, err
			}
			if newVal.IsNull() {
				return nil, &ExecError{Code: "23502", Pos: o.plan.Pos(),
					Message: "null value in column \"amproc\" violates not-null constraint"}
			}
			oid := uint32(0)
			if oidOrd >= 0 && oidOrd < len(rawRow) {
				if n, err := strconv.ParseUint(rawRow[oidOrd], 10, 32); err == nil {
					oid = uint32(n)
				}
			}
			im.SetAmProcMemberProc(oid, uint32(newVal.Int))
			row[amprocOrd] = newVal
		}
		o.appendUpdateRetRow(row)
	}
	return nil, EOF
}

// updateViaIndex uses the B-tree to find the tuple to update (O(log n))
// instead of scanning all pages. Falls back to the path in Next() when
// no IndexScan is available.
func (o *updateOp) updateViaIndex(rel storage.RelFileNode, cols []catalog.Column) (TupleSlot, error) {
	ix := o.idxScan
	idxRel := o.ctx.Catalog.IndexRelFileNode(ix.Index)
	tree, err := openIndexBTree(o.ctx, ix.Index, idxRel)
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
	keyBytes, encErr := o.ctx.indexProbeKey(ix.Index, []indexProbeKeyPart{{col: col, val: v, pos: ix.Key.Pos()}})
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
	// M-NIGHTLY 2026-07-15 UPDATE-side follow-up: only columns this
	// statement's SET clause actually touches need coercion — an untouched
	// column's value came straight from storage and is already typed.
	setCol := func(i int) bool { return i < len(o.plan.Set) && o.plan.Set[i] != nil }

	hiBytes := keyBytes
	if len(ix.Index.Columns) > 1 {
		hiBytes = o.ctx.compositeUpperBound(ix.Index, keyBytes)
	}
	err = tree.RangeScan(keyBytes, hiBytes, func(_ []byte, ptr storage.ItemPointer) (bool, error) {
		slot, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: heapRel, Block: ptr.Block})
		if err != nil {
			return false, err
		}
		slot.RLock()
		// Follow the HOT chain: the index pointer may be stale (pointing
		// to an earlier version whose CTID leads to the live version).
		tuple, actualSlot, found := followHOTChain(slot.Page(), ptr.Offset, o.ctx.Snap, o.ctx.Tx.XID, o.ctx.MultiXact, o.ctx.CmdID, o.ctx.comboStore())
		slot.RUnlock()
		o.ctx.Pool.Unpin(slot)
		if !found {
			return true, nil
		}
		// No tuple-tag acquire on a foreign lock-only holder here: the
		// write path sleeps (and takes the tag, FIFO, member-skip) in
		// waitForConflictingRowLock, which the caller runs before the
		// stamp — acquiring up front on a possibly NON-conflicting held
		// lock would queue behind an unrelated parked waiter (design
		// 0021-0012).
		var decRow Row
		decRow, err = make(Row, len(cols)), nil
		decNatts := int(tuple.Header.Infomask2 & 0x07FF)
		if decErr := DecodeRowIntoMctxPGTuple(decRow, cols, tuple.Data, tuple.Bitmap, decNatts, nil); decErr != nil {
			return true, nil // skip undecodable tuple (consistent with scanMatching)
		}
		row := decRow

		// The B-tree scan only matches the index's own equality key (ix.Key);
		// a residual predicate (e.g. a view's WHERE qual wrapping the planned
		// IndexScan in a Filter) must still be evaluated here — the EPQ
		// recheck path below re-applies o.pred on a concurrent update, but the
		// common uncontended case never reaches it. M0119-0004/root-0025.
		if o.pred != nil {
			v, err := evalExpr(o.pred, row, o.ctx)
			if err != nil {
				return false, err
			}
			if v.IsNull() || v.Kind != KindBool || !v.BoolValue() {
				return true, nil
			}
		}

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
		// M-NIGHTLY 2026-07-15 UPDATE-side follow-up: coerce freshly-SET
		// literal values to their declared column type — the same step
		// insertOp.Next already applies to VALUES-clause literals — before
		// FK/CHECK/domain/NOT-NULL constraint checks run below.
		if err := coerceRowForConstraintChecks(cols, newRow, setCol, o.ctx, o.plan.Pos()); err != nil {
			return false, err
		}
		// Recompute GENERATED ALWAYS AS … STORED columns after SET. M0096-0008.
		_ = computeGeneratedColumns(cols, newRow)
		// WITH CHECK OPTION enforcement — see the identical check in the
		// SeqScan path's per-row callback for the rationale.
		if o.plan.ViewCheckQual != nil {
			if err := checkViewCheckOption(o.ctx, o.plan.ViewCheckQual, o.plan.ViewCheckName, newRow); err != nil {
				return false, err
			}
		}
		// AI-20260810-011258-006: one physical tuple must be updated AT MOST
		// ONCE by a single UPDATE. A non-HOT update inserts a fresh index entry
		// while the old entry stays until VACUUM, so a repeatedly-updated row
		// accumulates SEVERAL live index entries for the same key. Each is
		// scanned here and each resolves — via followHOTChain — to the SAME live
		// tuple, so without this guard `UPDATE … SET bal = bal + :d WHERE aid=?`
		// applied the delta once per surviving index entry (pgbench TPC-B
		// balance drift), and the per-tuple xmax stamps it issued for the
		// duplicates made two clients wait on each other's leftovers in the same
		// hot page — the false deadlocks in this action item. Upstream cannot
		// hit this: ExecUpdate is driven by the plan's tuple stream and
		// heap_update on an already-self-updated tuple returns TM_SelfModified
		// (postgres/src/backend/access/heap/heapam.c), it does not re-apply.
		for i := range pending {
			if pending[i].blk == ptr.Block && pending[i].slot == actualSlot {
				s321Note(s321ScanDedup)
				return true, nil
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
	fksToRecheckIdx := o.childFKsToRecheck()
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
			retRow, ok, err := fireTriggers(o.ctx, idxTbl, "before", "update", pu.oldRow, pu.newRow)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue // RETURN NULL — skip this row
			}
			pu.newRow = retRow
			trigFiredViaIdx = true
		}
		// RI_FKey_check: the new FK key value must reference an existing parent
		// row. M0118-0008 (detach-partition-concurrently-4).
		if err := o.recheckChildFKs(fksToRecheckIdx, pu.newRow, idxTbl); err != nil {
			return nil, err
		}
		// NOT NULL / CHECK / domain constraint enforcement on the finalized
		// new row, before attempting either write path below (M0122-0005
		// follow-up — UPDATE previously enforced none of these). Checked
		// here — rather than only in the "!used" non-HOT branch further
		// down — so a HOT update (which never reaches that branch) cannot
		// bypass it.
		if cerr := checkRowConstraintsForWrite(o.ctx, idxTbl, cols, pu.newRow); cerr != nil {
			return nil, cerr
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
				// M0131-S32.1 TM_SelfModified guard. After an EPQ chain-follow two
				// pending entries of the SAME statement can converge on one physical
				// tuple. isConcurrentlyUpdated deliberately ignores our OWN xmax, so
				// without this the second entry re-stamps a tuple we already killed
				// and inserts a SECOND new version — leaving two live rows for one
				// logical row (the pgbench_branches "~10x over-applied" signature).
				// Upstream heap_update returns TM_SelfModified for a tuple this
				// command already updated and ExecUpdate skips the row
				// (postgres/src/backend/executor/nodeModifyTable.c).
				//
				// M-NIGHTLY AI-20260813-005117-012: the IS_MULTI / LOCKED_ONLY
				// exclusions live in isSelfModifiedWrite so this guard and its
				// seq-scan twin below cannot drift apart.
				if oldGerr == nil && isSelfModifiedWrite(oldTup.Header, o.ctx.Tx.XID) {
					s.Unlock()
					o.ctx.Pool.Unpin(s)
					s321Note(s321EPQSelfModified)
					epqSkip = true
					break
				}
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
					if wfgDebug {
						wfgNoteTarget(o.ctx.Tx.XID, rel, pu.blk, pu.slot, oldTup.Header)
					}
						if dl, terr := epqWait(o.ctx, xmax); terr != nil {
						terr.Pos = o.plan.Pos()
						return nil, terr
					} else if dl {
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
						if o.ctx.Tx.Isolation != mvcc.IsolationReadCommitted && o.ctx.TxnMgr != nil {
							// RR/SSI: classify xmax authoritatively, not by snapshot
							// membership — a committer that started after our frozen
							// snapshot is absent from snap.InProgress (0118-0105).
							aborted, committed := epqXmaxSettled(o.ctx, xmax)
							if aborted {
								epqDoUpdate = true
								continue
							}
							if committed {
								return nil, epqSerializationErr(o.ctx, rel, pu.blk, pu.slot, o.plan.Pos())
							}
							continue // still active; retry
						}
						// RC (or no manager): legacy snapshot heuristic.
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
								// A never-updated row is a chain tail (self-pointing t_ctid or the
								// legacy {Invalid,0} sentinel); a real successor t_ctid means UPDATE.
								if isChainTailCTID(ctid, pu.blk, pu.slot) {
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
						// M0131-S32.1: not-found does NOT mean the row is gone.
						// If the chain tip belongs to a transaction still in
						// flight, PG's ExecUpdate would block on it and re-fetch
						// (nodeModifyTable.c) — skipping here silently DROPS this
						// statement's write while still reporting `UPDATE 1`.
						// Wait for that writer and re-run the EPQ loop; the RC
						// budget (maxEPQRetriesRC) bounds a pathological lap.
						if pendXID, pending := epqChainPendingWriter(o.ctx, rel, pu.blk, pu.slot); pending && !s321NoWait &&
							epqRetry < epqRetryLimit(o.ctx.Tx.Isolation) {
							s321Note(s321EPQChainPendingWait)
							if dl, terr := epqWait(o.ctx, pendXID); terr != nil {
								terr.Pos = o.plan.Pos()
								return nil, terr
							} else if dl {
								return nil, &ExecError{
									Code:    "40001",
									Pos:     o.plan.Pos(),
									Message: "could not serialize access due to concurrent update (deadlock)",
								}
							}
							if o.ctx.Tx.Isolation == mvcc.IsolationReadCommitted && o.ctx.TxnMgr != nil {
								if newSnap, snapErr := o.ctx.TxnMgr.SnapshotFor(o.ctx.Tx); snapErr == nil {
									o.ctx.Snap = newSnap
								}
							}
							continue
						}
						// M0131-S32.3: same silent drop, one commit-window later.
						// The tail is LIVE but its writer committed after our
						// last snapshot refresh, so the snapshot-driven
						// chain-follow reports not-found while nobody is in
						// flight for epqChainPendingWriter to wait on.
						if !s321NoWait && epqRetry < epqRetryLimit(o.ctx.Tx.Isolation) &&
							epqChainTailLiveButUnseen(o.ctx, rel, pu.blk, pu.slot) {
							s321Note(s321EPQChainStaleSnap)
							epqRefreshSnapForRetry(o.ctx)
							continue
						}
						s321Note(s321EPQChainNotFound)
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
					if err := coerceRowForConstraintChecks(cols, pu.newRow, setCol, o.ctx, o.plan.Pos()); err != nil {
						return nil, err
					}
					_ = computeGeneratedColumns(cols, pu.newRow)
					s321Note(s321EPQChainFollowed)
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
					s321Note(s321EPQOldGetFail)
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
					retRow, trigOK, trigErr := fireTriggers(o.ctx, idxTbl, "before", "update", pu.oldRow, pu.newRow)
					if trigErr != nil {
						// s already unlocked/unpinned above before firing.
						return nil, trigErr
					}
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
					stampIdxErr = stampMovedPartitionOldTuple(o.ctx, s.Page(), pu.slot)
				} else if oldHdrTup, herr := storage.PageGetHeapTuple(s.Page(), pu.slot); herr == nil {
					// Preserve a pre-existing non-conflicting foreign locker into a
					// {updater + survivors} multi (M0118-0004 producer). Non-HOT
					// UPDATE is treated key-changing by goopg (KeysUpdated stamped
					// below), so the updater status is StatusUpdate (keysUpdated=true).
					stampIdxErr = stampUpdaterXmaxNonHOT(o.ctx, s.Page(), pu.slot, oldHdrTup.Header, true)
				} else {
					stampIdxErr = storage.PageSetHeapTupleXmax(s.Page(), pu.slot, effectiveWriterXID(o.ctx))
					if stampIdxErr == nil {
						// M0129-S8.3: stamp cmax on the fallback path
						// (PageGetHeapTuple failed — plain xmax stamp
						// without locker preservation).
						stampIdxErr = storage.PageSetHeapTupleCmax(s.Page(), pu.slot, o.ctx.GetCurrentCommandId(true), false)
					}
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
				if _, _, err := fireTriggers(o.ctx, idxTbl, "after", "update", pu.oldRow, pu.newRow); err != nil {
					return nil, err
				}
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
	// pg_database has no physical heap backing (Table.Virtual, rows come
	// from VirtualRows()) — route through a dedicated read/match/write path
	// instead of the physical-heap machinery below. Scoped to pg_database
	// specifically (not every Virtual table) to keep this a narrow,
	// low-blast-radius addition. M-NIGHTLY AI-20260707-000712-004 follow-up
	// / AC-002 residual #1 (`UPDATE pg_database SET datconnlimit = ...`).
	if tbl := o.plan.Table; tbl.Virtual && tbl.Schema == "pg_catalog" {
		switch tbl.Name {
		case "pg_database":
			return o.nextVirtualPgDatabase()
		case "pg_amproc":
			return o.nextVirtualPgAmproc()
		}
	}
	// M0093: UPDATE is unconditionally a write — materialise the
	// transaction's XID before the scan so selfRowLockMember /
	// isConcurrentlyUpdated / tuple-lock acquisition see the real
	// XID (zero would cause false-negative race detection and
	// would mis-classify our own locks as foreign).
	if err := o.ctx.MaterializeWriterXID(); err != nil {
		return nil, err
	}
	tbl := o.plan.Table
	cols := tbl.Columns
	rel := o.ctx.Catalog.RelFileNode(tbl)

	// UPDATE … FROM: nested-loop cross-product path. M0097-0065.
	if len(o.plan.FromScans) > 0 {
		return o.updateWithFrom(rel, cols)
	}

	// Collect parent + partition/inheritance children up front. M0096-0013.
	// For inheritance children, track a column-map so SET/WHERE/RETURNING
	// expressions (resolved against parent ordinals) work on child rows. M0097-0078.
	updateScanTables := []*catalog.Table{tbl}
	var inheritChildOIDs map[uint32]bool
	if imU, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
		updateScanTables = append(updateScanTables, imU.PartitionChildren(tbl.OID)...)
		// Drop other-session temp inheritance children (RELATION_IS_OTHER_TEMP).
		// Design 0118-0036 (M0118-0008 inherit-temp).
		inheritChildren := catalog.AccessibleInheritanceChildren(imU.InheritanceChildren(tbl.OID), sessionTempOwner(o.ctx))
		updateScanTables = append(updateScanTables, inheritChildren...)
		if len(inheritChildren) > 0 {
			inheritChildOIDs = make(map[uint32]bool, len(inheritChildren))
			for _, ic := range inheritChildren {
				inheritChildOIDs[ic.OID] = true
			}
		}
	}

	// Use IndexScan (B-tree) when available — O(log n) instead of O(n).
	// Only safe when tbl has no partition/inheritance children: updateViaIndex
	// walks exactly one B-tree scoped to tbl's own storage, so a matching row
	// living in a child would be silently skipped (M0119-0004 discovery,
	// root-0025 item 5 follow-up — `.ralph/deferral_ledger.md`). With
	// children present, fall through to the multi-table SeqScan path below,
	// which already fans out correctly.
	//
	// `Key != nil` is the second precondition, and it is not cosmetic:
	// updateViaIndex probes ONE equality key (`evalExpr(ix.Key, …)` — the same
	// shape as indexScanOp.lookupKey's single-key branch). A planner-produced
	// range scan (`WHERE id BETWEEN a AND b` → LowKey/HighKey, Key nil) and a
	// composite equality probe (`Keys`, Key nil) both leave Key unset, and
	// dereferencing it panicked the backend with a nil-pointer deref inside
	// evalExprSlot — a crashed connection for an ordinary ranged UPDATE
	// (M0131-S27 discovery: the forward crash E2E's post-checkpoint
	// `UPDATE … WHERE id BETWEEN 600 AND 650`). The SeqScan path below handles
	// every predicate shape, so falling through is correct, not a workaround;
	// teaching updateViaIndex to drive a range/composite probe is the
	// performance follow-up recorded in the deferral ledger.
	if o.idxScan != nil && o.idxScan.Key != nil && len(updateScanTables) == 1 {
		return o.updateViaIndex(rel, cols)
	}

	// Fallback: full SeqScan path (also used for a parent table with
	// partition/inheritance children, even when tbl itself has a usable index).

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
	// M-NIGHTLY 2026-07-15 UPDATE-side follow-up: only columns this
	// statement's SET clause actually touches need coercion — an untouched
	// column's value came straight from storage and is already typed.
	setCol := func(i int) bool { return i < len(o.plan.Set) && o.plan.Set[i] != nil }

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
		if err := scanMatching(o.ctx, scanRel, scanTbl.OID, scanCols, scanPred, func(blk storage.BlockNumber, slot uint16, row Row) error {
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
			var newRow, parentNewRow Row
			if isInheritChild {
				// Evaluate SET exprs in parent column space, then map back to child.
				parentNewRow = make(Row, len(cols))
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
				if err := coerceRowForConstraintChecks(cols, parentNewRow, setCol, o.ctx, o.plan.Pos()); err != nil {
					return err
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
				if err := coerceRowForConstraintChecks(captureCols, newRow, setCol, o.ctx, o.plan.Pos()); err != nil {
					return err
				}
				// scanTbl==tbl or a partition child: PG requires a partition's
				// columns to exactly mirror the partitioned table's layout (only
				// traditional multiple-inheritance children can add/reorder
				// columns), so newRow is already in the base table's ordinal
				// space — no remap needed.
				parentNewRow = newRow
			}
			_ = computeGeneratedColumns(captureCols, newRow)

			// WITH CHECK OPTION enforcement, checked against parentNewRow (the
			// base table's own column ordinal space that ViewCheckQual was
			// resolved against) so it applies uniformly to the parent's own
			// rows, partition-child rows, and inheritance-child rows alike.
			// M0119-0004 slice-365 / root-0025 deferred item 5.
			if o.plan.ViewCheckQual != nil {
				if err := checkViewCheckOption(o.ctx, o.plan.ViewCheckQual, o.plan.ViewCheckName, parentNewRow); err != nil {
					return err
				}
			}

			// M0100-0011: Phase 1 EPQ for all isolation levels — wait for any
			// in-progress xmax before processing the next row, so BEFORE trigger
			// and subsequent-row NOTICEs interleave correctly (PG per-row order).
			// RC: follow HOT chain after concurrent commit. RR/SSI: raise 40001.
			writeBlk, writeSlot := blk, slot
			oldRow := cloneRow(evalRow)
			beforeFiredP1 := false
			if !isInheritChild {
				// M0131-S32.2: this EPQ loop runs INSIDE the scan callback, and
				// epqWait refreshes ctx.Snap — the very snapshot scanMatching is
				// using to decide which tuples to hand us. Leaking the refreshed
				// snapshot back into the scan lets the rest of the scan see
				// versions committed after statement start, so the SAME logical
				// row can be handed to this callback twice (once as the old
				// version, once as a successor written by another session) and the
				// statement then writes BOTH — forking one logical row into two
				// live versions (the over-application signature). Upstream keeps
				// the scan's snapshot fixed for the whole statement and confines
				// the EPQ re-fetch to the updating tuple
				// (postgres/src/backend/executor/execMain.c EvalPlanQual*), so the
				// refreshed snapshot must not outlive this loop.
				scanSnap := o.ctx.Snap
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
					if dl, terr := epqWait(o.ctx, xmax); terr != nil {
						terr.Pos = o.plan.Pos()
						return terr
					} else if dl {
						return &ExecError{Code: "40001", Pos: o.plan.Pos(),
							Message: "could not serialize access due to concurrent update (deadlock)"}
					}
					visible, _ := epqRecheckVisible(o.ctx, captureRel, writeBlk, writeSlot)
					if visible {
						if o.ctx.Tx.Isolation != mvcc.IsolationReadCommitted && o.ctx.TxnMgr != nil {
							// RR/SSI: classify xmax authoritatively, not by snapshot
							// membership — a committer that started after our frozen
							// snapshot is absent from snap.InProgress (0118-0105).
							aborted, committed := epqXmaxSettled(o.ctx, xmax)
							if aborted {
								break // xmax aborted; row unchanged
							}
							if committed {
								// frozen snapshot is stale; serialize-fail.
								return epqSerializationErr(o.ctx, captureRel, writeBlk, writeSlot, o.plan.Pos())
							}
							continue // still active; retry
						}
						// RC (or no manager): legacy snapshot heuristic.
						if !o.ctx.Snap.HasInProgress(xmax) {
							break // xmax aborted; row unchanged
						}
						if o.ctx.TxnMgr != nil && o.ctx.TxnMgr.HasAbortedXID(xmax) {
							break // xmax globally aborted; row unchanged
						}
						continue
					}
					// Concurrent tx committed (visible=false via fresh RC snapshot).
					if o.ctx.Tx.Isolation != mvcc.IsolationReadCommitted {
						// Distinguish update vs delete: chain-tail t_ctid (self or
						// {Invalid,0}) means never-updated; a successor means UPDATE.
						errMsg := "could not serialize access due to concurrent update"
						if sp, perr := o.ctx.Pool.Pin(storage.BufferTag{Rel: captureRel, Block: writeBlk}); perr == nil {
							sp.RLock()
							if ot, gerr := storage.PageGetHeapTuple(sp.Page(), writeSlot); gerr == nil {
								ctid := ot.Header.CTID
								if isChainTailCTID(ctid, writeBlk, writeSlot) {
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
						// M0131-S32.2: the SeqScan arm's scan-phase EPQ loop had
						// the same defect S32.1 fixed in the write-phase loops —
						// a failed chain-follow was read as "row deleted by a
						// concurrent tx" and the row was dropped from `pending`
						// silently, so the statement reported `UPDATE 0` while a
						// live successor existed. Under contention the tip is
						// routinely owned by a writer that has not committed yet
						// (its xmin is invisible to any snapshot), which is not a
						// delete. PG blocks on that updater (XactLockTableWait in
						// heap_lock_tuple, driven by ExecUpdate's TM_Updated loop)
						// and re-fetches. Do the same: wait, refresh the RC
						// snapshot, and re-run the loop; epqRetryLimit bounds it.
						// epqWait already refreshes ctx.Snap on return, so the
						// retry re-walks the chain under a snapshot that includes
						// the writer we just waited for.
						if pendXID, inflight := epqChainPendingWriter(o.ctx, captureRel, writeBlk, writeSlot); inflight && !s322NoWait &&
							epqRetry < epqRetryLimit(o.ctx.Tx.Isolation) {
							s321Note(s321ScanEPQPendingWait)
							if dl, terr := epqWait(o.ctx, pendXID); terr != nil {
								terr.Pos = o.plan.Pos()
								return terr
							} else if dl {
								return &ExecError{Code: "40001", Pos: o.plan.Pos(),
									Message: "could not serialize access due to concurrent update (deadlock)"}
							}
							continue
						}
						// M0131-S32.3: the chain tail may be a LIVE version whose
						// writer committed after our last snapshot refresh. Both
						// chain-follow helpers pick "the latest version" by
						// snapshot visibility, so such a tail reads as not-found
						// while epqChainPendingWriter (correctly) reports nobody
						// in flight — and the row is dropped. Refresh and re-lap.
						if !s322NoWait && epqRetry < epqRetryLimit(o.ctx.Tx.Isolation) &&
							epqChainTailLiveButUnseen(o.ctx, captureRel, writeBlk, writeSlot) {
							s321Note(s321ScanEPQStaleSnap)
							epqRefreshSnapForRetry(o.ctx)
							continue
						}
						s321Note(s321ScanEPQNotFound)
						o.ctx.Snap = scanSnap
						return nil // row genuinely deleted by concurrent tx
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
					if err := coerceRowForConstraintChecks(captureCols, newRow, setCol, o.ctx, o.plan.Pos()); err != nil {
						return err
					}
					_ = computeGeneratedColumns(captureCols, newRow)
					oldRow = cloneRow(baseRow)
					writeBlk, writeSlot = newBlk, newSlot
					continue
				}
				// Hand the scan back its own snapshot (see scanSnap above).
				o.ctx.Snap = scanSnap
				if len(scanTbl.Triggers) > 0 {
					ret, ok, err := fireTriggers(o.ctx, scanTbl, "before", "update", oldRow, newRow)
					if err != nil {
						return err
					}
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
	if len(pending) == 0 {
		s321Note(s321ScanNoRow)
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
	fksToRecheckSeq := o.childFKsToRecheck()
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
			retRow, ok, err := fireTriggers(o.ctx, scanTblForTrig, "before", "update", pu.oldRow, pu.newRow)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue // RETURN NULL — skip this row
			}
			pu.newRow = retRow
		}
		// RI_FKey_check: the new FK key value must reference an existing parent
		// row (after BEFORE triggers may have rewritten it). M0118-0008.
		if err := o.recheckChildFKs(fksToRecheckSeq, pu.newRow, pu.scanTbl); err != nil {
			return nil, err
		}
		puRel := pu.rel
		if puRel == (storage.RelFileNode{}) {
			puRel = rel
		}
		puCols := pu.cols
		if puCols == nil {
			puCols = cols
		}
		// NOT NULL / CHECK / domain constraint enforcement on the finalized
		// new row, before attempting either write path below (M0122-0005
		// follow-up — UPDATE previously enforced none of these). Checked here
		// — rather than only in the "!used" non-HOT branch further down — so
		// a HOT update (which never reaches that branch) cannot bypass it.
		{
			chkTblSeq := pu.scanTbl
			if chkTblSeq == nil {
				chkTblSeq = tbl
			}
			if cerr := checkRowConstraintsForWrite(o.ctx, chkTblSeq, puCols, pu.newRow); cerr != nil {
				return nil, cerr
			}
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
				// M0131-S32.1 TM_SelfModified guard. After an EPQ chain-follow two
				// pending entries of the SAME statement can converge on one physical
				// tuple. isConcurrentlyUpdated deliberately ignores our OWN xmax, so
				// without this the second entry re-stamps a tuple we already killed
				// and inserts a SECOND new version — leaving two live rows for one
				// logical row (the pgbench_branches "~10x over-applied" signature).
				// Upstream heap_update returns TM_SelfModified for a tuple this
				// command already updated and ExecUpdate skips the row
				// (postgres/src/backend/executor/nodeModifyTable.c).
				//
				// M-NIGHTLY AI-20260813-005117-012: shares isSelfModifiedWrite
				// with the index-scan twin above so the pair cannot diverge.
				if oldGerr == nil && isSelfModifiedWrite(oldTup.Header, o.ctx.Tx.XID) {
					s.Unlock()
					o.ctx.Pool.Unpin(s)
					s321Note(s321EPQSelfModified)
					epqSkipSeq = true
					break
				}
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
					if dl, terr := epqWait(o.ctx, xmax); terr != nil {
						terr.Pos = o.plan.Pos()
						return nil, terr
					} else if dl {
						return nil, &ExecError{
							Code:    "40001",
							Pos:     o.plan.Pos(),
							Message: "could not serialize access due to concurrent update (deadlock)",
						}
					}
					// M0100-0004: EPQ chain-following for RC; 40001 for RR.
					visible, _ := epqRecheckVisible(o.ctx, puRel, pu.blk, pu.slot)
					if visible {
						if o.ctx.Tx.Isolation != mvcc.IsolationReadCommitted && o.ctx.TxnMgr != nil {
							// RR/SSI: classify xmax authoritatively, not by snapshot membership —
							// a committer that started after our frozen snapshot is absent from
							// snap.InProgress (0118-0105).
							aborted, committed := epqXmaxSettled(o.ctx, xmax)
							if aborted {
								epqDoUpdateSeq = true
								continue // bypass EPQ on next iter; update code executes
							}
							if committed {
								return nil, epqSerializationErr(o.ctx, puRel, pu.blk, pu.slot, o.plan.Pos())
							}
							continue // still active; retry
						}
						// RC (or no manager): legacy snapshot heuristic.
						if !o.ctx.Snap.HasInProgress(xmax) {
							epqDoUpdateSeq = true
							continue // bypass EPQ on next iter; update code executes
						}
						if o.ctx.TxnMgr != nil && o.ctx.TxnMgr.HasAbortedXID(xmax) {
							epqDoUpdateSeq = true
							continue // bypass EPQ on next iter; update code executes
						}
						continue
					}
					// Concurrent tx committed — row was updated or deleted.
					if o.ctx.Tx.Isolation != mvcc.IsolationReadCommitted {
						// Distinguish UPDATE vs DELETE: chain-tail t_ctid (self or
						// {Invalid,0}) means never-updated; a successor means UPDATE.
						errMsg := "could not serialize access due to concurrent update"
						if sp, perr := o.ctx.Pool.Pin(storage.BufferTag{Rel: puRel, Block: pu.blk}); perr == nil {
							sp.RLock()
							if ot, gerr := storage.PageGetHeapTuple(sp.Page(), pu.slot); gerr == nil {
								ctid := ot.Header.CTID
								if isChainTailCTID(ctid, pu.blk, pu.slot) {
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
						// M0131-S32.1: not-found does NOT mean the row is gone.
						// If the chain tip belongs to a transaction still in
						// flight, PG's ExecUpdate would block on it and re-fetch
						// (nodeModifyTable.c) — skipping here silently DROPS this
						// statement's write while still reporting `UPDATE 1`.
						// Wait for that writer and re-run the EPQ loop; the RC
						// budget (maxEPQRetriesRC) bounds a pathological lap.
						if pendXID, pending := epqChainPendingWriter(o.ctx, puRel, pu.blk, pu.slot); pending && !s321NoWait &&
							epqRetry < epqRetryLimit(o.ctx.Tx.Isolation) {
							s321Note(s321EPQChainPendingWait)
							if dl, terr := epqWait(o.ctx, pendXID); terr != nil {
								terr.Pos = o.plan.Pos()
								return nil, terr
							} else if dl {
								return nil, &ExecError{
									Code:    "40001",
									Pos:     o.plan.Pos(),
									Message: "could not serialize access due to concurrent update (deadlock)",
								}
							}
							if o.ctx.Tx.Isolation == mvcc.IsolationReadCommitted && o.ctx.TxnMgr != nil {
								if newSnap, snapErr := o.ctx.TxnMgr.SnapshotFor(o.ctx.Tx); snapErr == nil {
									o.ctx.Snap = newSnap
								}
							}
							continue
						}
						// M0131-S32.3: see the index-arm site above — a tail that
						// is live but committed after our last snapshot refresh
						// must be re-lapped, not skipped.
						if !s321NoWait && epqRetry < epqRetryLimit(o.ctx.Tx.Isolation) &&
							epqChainTailLiveButUnseen(o.ctx, puRel, pu.blk, pu.slot) {
							s321Note(s321EPQChainStaleSnap)
							epqRefreshSnapForRetry(o.ctx)
							continue
						}
						s321Note(s321EPQChainNotFound)
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
					if err := coerceRowForConstraintChecks(puCols, pu.newRow, setCol, o.ctx, o.plan.Pos()); err != nil {
						return nil, err
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
					s321Note(s321EPQChainFollowed)
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
					_, ok, err := fireTriggers(o.ctx, pu.scanTbl, "before", "delete", pu.oldRow, nil)
					if err != nil {
						s.Unlock()
						o.ctx.Pool.Unpin(s)
						return nil, err
					}
					if !ok {
						// RETURN NULL — suppress the row.
						s.Unlock()
						o.ctx.Pool.Unpin(s)
						s321Note(s321EPQOldGetFail)
						epqSkipSeq = true
						break
					}
				}
				var stampErr error
				if isCrossPartitionMove {
					stampErr = stampMovedPartitionOldTuple(o.ctx, s.Page(), pu.slot)
				} else if oldHdrTup, herr := storage.PageGetHeapTuple(s.Page(), pu.slot); herr == nil {
					// Preserve a pre-existing non-conflicting foreign locker into a
					// {updater + survivors} multi (M0118-0004 producer). Non-HOT
					// UPDATE is key-changing in goopg, so keysUpdated=true.
					stampErr = stampUpdaterXmaxNonHOT(o.ctx, s.Page(), pu.slot, oldHdrTup.Header, true)
				} else {
					stampErr = storage.PageSetHeapTupleXmax(s.Page(), pu.slot, effectiveWriterXID(o.ctx))
					if stampErr == nil {
						// M0129-S8.3: stamp cmax on the fallback path.
						stampErr = storage.PageSetHeapTupleCmax(s.Page(), pu.slot, o.ctx.GetCurrentCommandId(true), false)
					}
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
				if _, _, err := fireTriggers(o.ctx, scanTblForAfterTrig, "after", "update", pu.oldRow, pu.newRow); err != nil {
					return nil, err
				}
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

// tryPgClassCatalogDelete handles `DELETE FROM pg_class WHERE { relname = '<rel>'
// | oid = <n> }` as a transaction-deferred table drop (intra-grant-inplace perm
// 10). PostgreSQL's heap_delete on the pg_class tuple stamps its delete xmax and
// keeps the row visible until the deleting transaction commits; a concurrent
// rowmark or in-place updater waits on that xmax, then finds the tuple gone once
// the delete commits. goopg has no pg_class heap, so we record the deleting
// transaction's writer XID as the pg_class delete xmax (a concurrent
// SELECT … FOR UPDATE waits on it via waitTablePendingDrop) and defer the actual
// catalog removal to COMMIT (the relation stays visible to other sessions until
// then). Returns handled=true when the delete targeted pg_class and was applied
// (or matched no live relation); false to fall through to the generic delete.
// Design 0118-0117.
func (o *deleteOp) tryPgClassCatalogDelete() (bool, error) {
	if o.plan.Table == nil || o.plan.Table.OID != catalog.RelationRelationId {
		return false, nil
	}
	im, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return false, nil
	}
	// Deferral requires an explicit transaction block — outside one (autocommit)
	// no spec exercises this, so fall through to the generic (no-op) path rather
	// than dropping the relation out from under the virtual catalog.
	bsess, isBasic := o.ctx.Session.(*BasicSession)
	if !isBasic || !bsess.InExplicitTransaction() {
		return false, nil
	}
	oid, ok := o.pgClassDeleteTargetOID()
	if !ok {
		// Unsupported predicate shape, or the relname resolved to no relation:
		// the delete matches no row. Treat as handled (0 rows) so we do not run
		// the generic heap scan against the virtual catalog.
		return true, nil
	}
	tbl, found := im.LookupTableByOID(oid)
	if !found || tbl == nil {
		return true, nil // already gone → 0 rows affected
	}
	if err := o.ctx.MaterializeWriterXID(); err != nil {
		return false, err
	}
	// Record the pg_class delete xmax BEFORE deferring so a concurrent rowmark
	// that records itself and then waits (waitTablePendingDrop) observes our XID.
	im.SetTablePendingDropXID(tbl.OID, uint32(o.ctx.Tx.XID))
	bsess.AddPendingTableDrop(PendingTableDrop{
		Name:           parser.ObjectName{Schema: tbl.Schema, Name: tbl.Name},
		Table:          tbl,
		SavepointDepth: bsess.SavepointDepth(),
	})
	o.rowsAffected++
	return true, nil
}

// pgClassDeleteTargetOID extracts the relation OID targeted by a single
// `relname = '<name>'` or `oid = <n>` equality predicate over pg_class. Returns
// ok=false for any other shape, or when a relname does not resolve to a live
// relation. Design 0118-0117 (intra-grant-inplace perm 10).
func (o *deleteOp) pgClassDeleteTargetOID() (uint32, bool) {
	bo, ok := o.pred.(*planner.BinaryOp)
	if !ok || bo.Op != parser.OpEq {
		return 0, false
	}
	cr, ok := bo.Left.(*planner.ColumnRef)
	constExpr := bo.Right
	if !ok {
		if cr, ok = bo.Right.(*planner.ColumnRef); !ok {
			return 0, false
		}
		constExpr = bo.Left
	}
	cols := o.plan.Table.Columns
	if cr.Index < 0 || cr.Index >= len(cols) {
		return 0, false
	}
	d, err := evalExpr(constExpr, nil, o.ctx)
	if err != nil || d.IsNull() {
		return 0, false
	}
	switch strings.ToLower(cols[cr.Index].Name) {
	case "oid":
		if d.Kind == KindInt && d.Int > 0 {
			return uint32(d.Int), true
		}
	case "relname":
		if name := d.StringValue(); name != "" {
			if tbl, ok := o.ctx.Catalog.LookupTable(parser.ObjectName{Name: name}); ok && tbl != nil {
				return tbl.OID, true
			}
		}
	}
	return 0, false
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
	if !dmlPrivilegePermitted(ctx, o.plan.Table, "DELETE") {
		return &ExecError{Code: "42501", Pos: o.plan.Pos(), Message: fmt.Sprintf("permission denied for table %s", o.plan.Table.Name)}
	}
	rel := ctx.Catalog.RelFileNode(o.plan.Table)
	if err := ctx.acquireRelLock(rel, lockmgr.RowExclusiveLock); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.plan.Pos()
		}
		return err
	}
	if err := ctx.acquireWriteLockTxn(rel); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.plan.Pos()
		}
		return err
	}
	return nil
}

func (o *deleteOp) Close() error {
	// Cumulative relation stats: stage deleted tuples (each removes a live tuple
	// and produces a dead one) for the current transaction. Gated by track_counts.
	// M0118-0009 (`stats`, rung 7; design 0118-0131).
	recordRelDelete(o.ctx, tableOIDFromCatalog(o.plan.Table), o.rowsAffected)
	return nil
}

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
	// DELETE … USING: nested-loop cross-product path. M0097-0076.
	if len(o.plan.UsingScans) > 0 {
		return o.deleteWithUsing()
	}
	o.done = true
	// intra-grant-inplace perm 10: `DELETE FROM pg_class WHERE relname = '<rel>'`
	// is a virtual-catalog tuple delete. goopg serves pg_class from the virtual
	// builder (no heap), so the generic scan below would match nothing; handle it
	// here as a transaction-deferred table drop that records the pg_class delete
	// xmax a concurrent rowmark (SELECT … FROM pg_class … FOR UPDATE) waits on.
	// Design 0118-0117.
	if handled, herr := o.tryPgClassCatalogDelete(); herr != nil {
		return nil, herr
	} else if handled {
		return nil, EOF
	}
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
		// Drop other-session temp inheritance children (RELATION_IS_OTHER_TEMP).
		// Design 0118-0036 (M0118-0008 inherit-temp).
		delInheritChildren := catalog.AccessibleInheritanceChildren(im.InheritanceChildren(tbl.OID), sessionTempOwner(o.ctx))
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
		if err := scanMatching(o.ctx, scanRel, scanTbl.OID, scanTbl.Columns, delScanPred, func(blk storage.BlockNumber, slot uint16, row Row) error {
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
					if dl, terr := epqWait(o.ctx, xmax); terr != nil {
						terr.Pos = o.plan.Pos()
						return terr
					} else if dl {
						return &ExecError{Code: "40001", Pos: o.plan.Pos(),
							Message: "could not serialize access due to concurrent update (deadlock)"}
					}
					visible, _ := epqRecheckVisible(o.ctx, captureRel, deleteBlk, deleteSlot)
					if visible {
						if o.ctx.Tx.Isolation != mvcc.IsolationReadCommitted && o.ctx.TxnMgr != nil {
							// RR/SSI: classify xmax authoritatively, not by snapshot membership —
							// a committer that started after our frozen snapshot is absent from
							// snap.InProgress (0118-0105).
							aborted, committed := epqXmaxSettled(o.ctx, xmax)
							if aborted {
								break // xmax aborted; row unchanged, proceed with delete
							}
							if committed {
								return epqSerializationErr(o.ctx, captureRel, deleteBlk, deleteSlot, o.plan.Pos())
							}
							continue // still active; retry
						}
						// RC (or no manager): legacy snapshot heuristic.
						if !o.ctx.Snap.HasInProgress(xmax) {
							break // xmax aborted; row unchanged, proceed with delete
						}
						if o.ctx.TxnMgr != nil && o.ctx.TxnMgr.HasAbortedXID(xmax) {
							break // xmax aborted; row unchanged, proceed with delete
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
					_, ok, err := fireTriggers(o.ctx, trigTbl, "before", "delete", cloneRow(deleteRow), nil)
					if err != nil {
						return err
					}
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
			_, ok, err := fireTriggers(o.ctx, trigTbl, "before", "delete", v.row, nil)
			if err != nil {
				return nil, err
			}
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
				if dl, terr := epqWait(o.ctx, xmax); terr != nil {
					terr.Pos = o.plan.Pos()
					return nil, terr
				} else if dl {
					return nil, &ExecError{
						Code:    "40001",
						Pos:     o.plan.Pos(),
						Message: "could not serialize access due to concurrent update (deadlock)",
					}
				}
				// M0100-0004: EPQ chain-following for RC; 40001 for RR.
				visible, _ := epqRecheckVisible(o.ctx, victimRel, v.blk, v.slot)
				if visible {
					if o.ctx.Tx.Isolation != mvcc.IsolationReadCommitted && o.ctx.TxnMgr != nil {
						// RR/SSI: classify xmax authoritatively, not by snapshot membership —
						// a committer that started after our frozen snapshot is absent from
						// snap.InProgress (0118-0105).
						aborted, committed := epqXmaxSettled(o.ctx, xmax)
						if aborted {
							epqDoDelete = true
							continue // bypass EPQ on next iter; delete code executes
						}
						if committed {
							return nil, epqSerializationErr(o.ctx, victimRel, v.blk, v.slot, o.plan.Pos())
						}
						continue // still active; retry
					}
					// RC (or no manager): legacy snapshot heuristic.
					if !o.ctx.Snap.HasInProgress(xmax) {
						epqDoDelete = true
						continue // bypass EPQ on next iter; delete code executes
					}
					if o.ctx.TxnMgr != nil && o.ctx.TxnMgr.HasAbortedXID(xmax) {
						epqDoDelete = true
						continue // bypass EPQ on next iter; delete code executes
					}
					continue
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
				if delStampErr == nil {
					// M0129-S8.3: stamp cmax on the fallback path.
					delStampErr = storage.PageSetHeapTupleCmax(s.Page(), v.slot, o.ctx.GetCurrentCommandId(true), false)
				}
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
				if _, _, err := fireTriggers(o.ctx, tbl, "after", "delete", delRow, nil); err != nil {
					return nil, err
				}
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

// rowDedupKey identifies a physical row across a possibly multi-relation
// scan (target table + inheritance children): a bare (block, slot) pair
// collides across relations since every table numbers its own blocks from
// 0. Shared by updateWithFrom and deleteViaUsing's inheritance fan-out.
type rowDedupKey struct {
	tag  storage.BufferTag
	slot uint16
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
	// M-NIGHTLY 2026-07-15 UPDATE-side follow-up: only columns this
	// statement's SET clause actually touches need coercion — an untouched
	// column's value came straight from storage and is already typed.
	setCol := func(i int) bool { return i < len(o.plan.Set) && o.plan.Set[i] != nil }

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
		// Inheritance children: require column remapping. Drop other-session
		// temp children (RELATION_IS_OTHER_TEMP). Design 0118-0036.
		for _, ic := range catalog.AccessibleInheritanceChildren(imFrom.InheritanceChildren(o.plan.Table.OID), sessionTempOwner(o.ctx)) {
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
		if err := scanMatching(o.ctx, fst.rel, 0, fst.cols, nil, func(blk storage.BlockNumber, slot uint16, rawRow Row) error {
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
					if err := coerceRowForConstraintChecks(tgtCols, parentNewRow, setCol, o.ctx, o.plan.Pos()); err != nil {
						return err
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
					// WITH CHECK OPTION enforcement: parentNewRow is always in the
					// base table's own column ordinal space regardless of fst
					// (tgtRow was remapped from child to parent ordinals above for
					// inheritance children; partition children and the base table
					// itself already share that layout), so the check applies
					// uniformly to parent, partition-child, and inheritance-child
					// rows alike. root-0025 deferred item 5 closed.
					if o.plan.ViewCheckQual != nil {
						if err := checkViewCheckOption(o.ctx, o.plan.ViewCheckQual, o.plan.ViewCheckName, parentNewRow); err != nil {
							return err
						}
					}
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
	// Classify once so the write-path wait (below) matches how a locker
	// decoded it, mirroring updateViaIndex/updateOp.Next (M0119-0009).
	updReqStatusFrom := multixact.StatusNoKeyUpdate
	if !hotEligible {
		updReqStatusFrom = multixact.StatusUpdate
	}
	fksToRecheckFrom := o.childFKsToRecheck()
	seen := make(map[rowDedupKey]bool)
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
		// Keyed by (relation, block, slot) — NOT just (block, slot): distinct
		// inheritance-child relations each number their own blocks from 0, so
		// two different tables' rows can share the same (blk, slot) pair. A
		// bare (blk, slot) key falsely treats a child table's row as "already
		// updated by an earlier FROM match" and silently skips it.
		key := rowDedupKey{tag: storage.BufferTag{Rel: puSrcRel, Block: pu.blk}, slot: pu.slot}
		if seen[key] {
			continue // already updated by an earlier FROM match
		}
		seen[key] = true

		// Honour a row lock propagated forward onto this live version by
		// heap_lock_updated_tuple before writing: wait for every still-active
		// foreign holder whose strength conflicts with this UPDATE (M0118-0003
		// write-path wait, wired here for UPDATE...FROM sibling-path parity —
		// M0119-0009; the plain updateViaIndex/updateOp.Next paths already had
		// this, updateWithFrom did not).
		if err := waitForConflictingRowLock(o.ctx, puSrcRel, pu.blk, pu.slot, updReqStatusFrom, o.plan.Pos()); err != nil {
			return nil, err
		}

		// Fire BEFORE UPDATE triggers.
		if len(o.plan.Table.Triggers) > 0 {
			retRow, ok, err := fireTriggers(o.ctx, o.plan.Table, "before", "update", pu.oldRow, pu.newRow)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			pu.newRow = retRow
		}
		// RI_FKey_check: the new FK key value must reference an existing parent
		// row. M0118-0008 (detach-partition-concurrently-4).
		if err := o.recheckChildFKs(fksToRecheckFrom, pu.newRow, pu.tbl); err != nil {
			return nil, err
		}
		// NOT NULL / CHECK / domain constraint enforcement on the finalized
		// new row, before attempting either write path below (M0122-0005
		// follow-up — UPDATE previously enforced none of these). Checked
		// here — rather than only in the "!used" non-HOT branch further down
		// — so a HOT update (which never reaches that branch) cannot bypass
		// it. puCols/pu.tbl already describe the destination (partition-
		// routed) relation's own column layout.
		{
			chkTblFrom := pu.tbl
			if chkTblFrom == nil {
				chkTblFrom = o.plan.Table
			}
			if cerr := checkRowConstraintsForWrite(o.ctx, chkTblFrom, puCols, pu.newRow); cerr != nil {
				return nil, cerr
			}
		}
		used := false
		// TM_SelfModified guard: before attempting HOT (which skips
		// the non-HOT path where the concurrent-modification check
		// lives), verify that the old tuple wasn't already stamped
		// by our own transaction through a different sub-command
		// (e.g. a function called from a CTE's RETURNING clause).
		// Do a quick RLock probe — the HOT path does its own
		// Pin+Lock later. M0129-S2.
		{
			if ts, tsErr := o.ctx.Pool.Pin(storage.BufferTag{Rel: puSrcRel, Block: pu.blk}); tsErr == nil {
				ts.RLock()
				if tsTup, tsGerr := storage.PageGetHeapTuple(ts.Page(), pu.slot); tsGerr == nil {
					if tsTup.Header.Xmax != storage.InvalidTransactionID && tsTup.Header.Xmax == o.ctx.Tx.XID {
						cmax := mvcc.GetCmax(tsTup.Header, o.ctx.comboStore())
						ts.RUnlock()
						o.ctx.Pool.Unpin(ts)
						// cmax == live CID (post-function-sync) → same sub-command → skip silently.
						// cmax != live CID → different sub-command (e.g. a volatile function
						// in RETURNING) → raise TM_SelfModified.
						// Uses GetCurrentCommandId(true), not CmdID: cmdCounter is synced
						// back from the child after a volatile function returns, so it
						// reflects function-driven command-counter advances. AI-007.
						liveCID := o.ctx.GetCurrentCommandId(true)
						if cmax != liveCID {
							return nil, errTupleAlreadyModified("updated", o.plan.Pos())
						}
						continue // same CID — silently skip this row (mirrors PG TM_Ok)
					}
				}
				ts.RUnlock()
				o.ctx.Pool.Unpin(ts)
			}
		}
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
				if dl, terr := epqWait(o.ctx, xmax); terr != nil {
					terr.Pos = o.plan.Pos()
					return nil, terr
				} else if dl {
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
				// TM_SelfModified guard: the EPQ chain can lead into a
				// version our own transaction wrote through a different
				// sub-command (e.g. a function called from a CTE's
				// RETURNING clause). Re-pin at the new slot and check cmax.
				// Different CID → error; same CID → skip silently. AI-007.
				if tms, tmsErr := o.ctx.Pool.Pin(storage.BufferTag{Rel: puSrcRel, Block: epqBlk}); tmsErr == nil {
					tms.RLock()
					if tmsTup, tmsGerr := storage.PageGetHeapTuple(tms.Page(), newSlot); tmsGerr == nil {
						if tmsTup.Header.Xmax != storage.InvalidTransactionID && tmsTup.Header.Xmax == o.ctx.Tx.XID {
							cmax := mvcc.GetCmax(tmsTup.Header, o.ctx.comboStore())
							liveCID := o.ctx.GetCurrentCommandId(true)
							tms.RUnlock()
							o.ctx.Pool.Unpin(tms)
							if cmax != liveCID {
								return nil, errTupleAlreadyModified("updated", o.plan.Pos())
							}
							// Same CID: TM_SelfModified — skip silently.
							continue
						}
					}
					tms.RUnlock()
					o.ctx.Pool.Unpin(tms)
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
				if err := coerceRowForConstraintChecks(tgtCols, parentNewRow, setCol, o.ctx, o.plan.Pos()); err != nil {
					return nil, err
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
			} else {
				// No concurrent modification: the Pin+Lock taken above still
				// targets the right (block, slot) and must be released before
				// the unconditional re-Pin+Lock below re-acquires it — without
				// this, the re-Lock deadlocks against the lock this same
				// goroutine is still holding (M-NIGHTLY with-regress hang;
				// only reachable when puSrcRel != rel, i.e. a row sourced from
				// an inheritance child, since same-rel rows take the HOT path
				// above and never reach here).
				s.Unlock()
				o.ctx.Pool.Unpin(s)
				// TM_SelfModified guard: isConcurrentlyUpdated already ruled
				// out a foreign xmax, but the tuple may carry our own xmax
				// from a different sub-command (e.g. a function called from a
				// CTE's RETURNING clause). A live tuple has xmax=Invalid; a
				// stale HOT-updated tuple has a foreign xmax (aborted). Only
				// our own xmax here means the tuple was already modified by
				// a sub-command of our transaction. Compare cmax against live CID.
				// M0129-S2 / AI-007.
				if oldTup.Header.Xmax != storage.InvalidTransactionID && oldTup.Header.Xmax == o.ctx.Tx.XID {
					cmax := mvcc.GetCmax(oldTup.Header, o.ctx.comboStore())
					liveCID := o.ctx.GetCurrentCommandId(true)
					if cmax != liveCID {
						return nil, errTupleAlreadyModified("updated", o.plan.Pos())
					}
					continue // same CID — silently skip this row
				}
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
			maintainUniqueIndexesForInsert(o.ctx, o.plan.Table, puCols, pu.newRow, newPtr)
			if !isCrossPartMove {
				if cerr := stampOldCtid(o.ctx, puSrcRel, pu.blk, pu.slot, newPtr); cerr != nil {
					return nil, cerr
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
		usingPortion Row              // joined USING-table columns for RETURNING; nil when RETURNING absent
			cols         []catalog.Column // source relation columns for EPQ chain-following (M0129-S2)
		}
	var victims []victim
	seen := make(map[rowDedupKey]bool)

	// Collect inheritance children for the USING target scan. M0097-0078.
	type usingScanTarget struct {
		rel    storage.RelFileNode
		cols   []catalog.Column
		colMap []int // nil = parent; set for inheritance children
	}
	usingScanTargets := []usingScanTarget{{rel: rel, cols: tgtCols}}
	if imDel, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
		// Drop other-session temp inheritance children. Design 0118-0036.
		for _, ic := range catalog.AccessibleInheritanceChildren(imDel.InheritanceChildren(tbl.OID), sessionTempOwner(o.ctx)) {
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
		if err := scanMatching(o.ctx, ust.rel, 0, ust.cols, nil, func(blk storage.BlockNumber, slot uint16, rawRow Row) error {
			// Keyed by (relation, block, slot) — see rowDedupKey: a bare
			// (blk, slot) key collides across different inheritance-child
			// relations and silently drops victims from the DELETE.
			key := rowDedupKey{tag: storage.BufferTag{Rel: ust.rel, Block: blk}, slot: slot}
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
					if len(combinedRow) > tgtColCount {
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
						cols: ust.cols,
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
		// Honour a row lock propagated forward onto this live version before
		// writing (M0118-0003 write-path wait, wired here for DELETE...USING
		// sibling-path parity — M0119-0009; the plain deleteOp.Next path
		// already had this, deleteWithUsing did not). DELETE is always
		// StatusUpdate — conflicts with every lock strength.
		if err := waitForConflictingRowLock(o.ctx, vRel, v.blk, v.slot, multixact.StatusUpdate, o.plan.Pos()); err != nil {
			return nil, err
		}

		// Fire BEFORE DELETE triggers and enforce FK constraints.
		if len(tbl.Triggers) > 0 {
			_, ok, err := fireTriggers(o.ctx, tbl, "before", "delete", v.oldRow, nil)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
		}
		if err := enforceFKOnDelete(o.ctx, tbl, v.oldRow); err != nil {
			return nil, err
		}
		// EvalPlanQual retry loop (M0129-S2): on concurrent xmax conflict,
		// wait for the conflicting transaction, follow the t_ctid chain,
		// re-evaluate both WHERE and USING predicates, and retry the stamp.
		// Mirror of deleteOp.Next stamp-phase EPQ. M0129-S2.
		epqSkipDel := false
		epqDoDelete := false // abort-confirmed bypass flag
		for epqRetry := 0; ; epqRetry++ {
			s, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: vRel, Block: v.blk})
			if err != nil {
				return nil, err
			}
			s.Lock()
			oldTup, oldGerr := storage.PageGetHeapTuple(s.Page(), v.slot)
			if oldGerr != nil {
				s.Unlock()
				o.ctx.Pool.Unpin(s)
				epqSkipDel = true
				break
			}
			if !epqDoDelete && isConcurrentlyUpdated(oldTup.Header, o.ctx.Tx.XID, &o.ctx.Snap, o.ctx.MultiXact) {
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
				if dl, terr := epqWait(o.ctx, xmax); terr != nil {
					terr.Pos = o.plan.Pos()
					return nil, terr
				} else if dl {
					return nil, &ExecError{
						Code:    "40001",
						Pos:     o.plan.Pos(),
						Message: "could not serialize access due to concurrent update (deadlock)",
					}
				}
				visible, _ := epqRecheckVisible(o.ctx, vRel, v.blk, v.slot)
				if visible {
					if o.ctx.Tx.Isolation != mvcc.IsolationReadCommitted && o.ctx.TxnMgr != nil {
						aborted, committed := epqXmaxSettled(o.ctx, xmax)
						if aborted {
							epqDoDelete = true
							continue
						}
						if committed {
							return nil, epqSerializationErr(o.ctx, vRel, v.blk, v.slot, o.plan.Pos())
						}
						continue
					}
					// RC (or no manager): legacy snapshot heuristic.
					if !o.ctx.Snap.HasInProgress(xmax) {
						epqDoDelete = true
						continue
					}
					if o.ctx.TxnMgr != nil && o.ctx.TxnMgr.HasAbortedXID(xmax) {
						epqDoDelete = true
						continue
					}
					continue
				}
				// Concurrent tx committed — row was updated or deleted.
				if o.ctx.Tx.Isolation != mvcc.IsolationReadCommitted {
					return nil, &ExecError{
						Code:    "40001",
						Pos:     o.plan.Pos(),
						Message: "could not serialize access due to concurrent update",
					}
				}
				// RC: cross-partition UPDATE sentinel check.
				if epqSlotMovedToAnotherPartition(o.ctx, vRel, v.blk, v.slot) {
					return nil, errMovedToAnotherPartition(o.plan.Pos())
				}
				// Follow HOT chain, then non-HOT chain. Re-evaluate both
				// the WHERE predicate (o.pred) and the USING join predicate
				// (o.plan.UsingPred) against the re-fetched live version.
				victimCols := v.cols
				if victimCols == nil {
					victimCols = tbl.Columns
				}
				newBlk := v.blk
				newSlot, newRow, hotFound, predOk := epqFollowHOT(o.ctx, vRel, v.blk, v.slot, victimCols, nil, nil)
				chainFound := predOk
				if !hotFound {
					if cBlk, cSlot, cRow, cFound := epqFollowChain(o.ctx, vRel, v.blk, v.slot, victimCols, nil, nil); cFound {
						newBlk, newSlot, newRow, chainFound = cBlk, cSlot, cRow, true
					}
				}
				if !chainFound {
					epqSkipDel = true
					break
				}
				// TM_SelfModified guard: the EPQ chain can lead into a
				// version our own transaction wrote through a different
				// sub-command (e.g. a function called from a CTE's
				// RETURNING clause). Re-pin at the new slot and check cmax.
				// Different CID → error; same CID → skip silently.
				if tms, tmsErr := o.ctx.Pool.Pin(storage.BufferTag{Rel: vRel, Block: newBlk}); tmsErr == nil {
					tms.RLock()
					if tmsTup, tmsGerr := storage.PageGetHeapTuple(tms.Page(), newSlot); tmsGerr == nil {
						if tmsTup.Header.Xmax != storage.InvalidTransactionID && tmsTup.Header.Xmax == o.ctx.Tx.XID {
							cmax := mvcc.GetCmax(tmsTup.Header, o.ctx.comboStore())
							liveCID := o.ctx.GetCurrentCommandId(true)
							tms.RUnlock()
							o.ctx.Pool.Unpin(tms)
							if cmax != liveCID {
								return nil, errTupleAlreadyModified("deleted", o.plan.Pos())
							}
							// Same CID: TM_SelfModified — skip silently.
							epqSkipDel = true
							break
						}
					}
					tms.RUnlock()
					o.ctx.Pool.Unpin(tms)
				}
				// Re-evaluate USING join predicate against new row + USING portion.
				if o.plan.UsingPred != nil {
					epqCombined := append(append(Row(nil), newRow...), v.usingPortion...)
					uv, _ := evalExpr(o.plan.UsingPred, epqCombined, o.ctx)
					if uv.IsNull() || uv.Kind != KindBool || !uv.BoolValue() {
						epqSkipDel = true
						break
					}
				}
				// Row survived EPQ: update victim and retry stamp.
				v.blk, v.slot = newBlk, newSlot
				v.oldRow = cloneRow(newRow)
				v.retOldRow = nil // EPQ-fetched row is in source-table layout
				continue
			}
			oldTupleBytes, _ := oldTup.MarshalBinary()
			// Preserve a pre-existing non-conflicting foreign locker (M0118-0004
			// producer). DELETE is StatusUpdate (keysUpdated=true) → no-op unless a
			// non-conflicting locker survives; wired for sibling-path parity.
			if stampErr := stampUpdaterXmaxNonHOT(o.ctx, s.Page(), v.slot, oldTup.Header, true); stampErr != nil {
				s.Unlock()
				o.ctx.Pool.Unpin(s)
				if errors.Is(stampErr, storage.ErrUnsupportedItem) {
					epqSkipDel = true
					break
				}
				return nil, stampErr
			}
			derr := markHeapDeleteDirtyAndClearVM(o.ctx, s, vRel, v.blk, v.slot, effectiveWriterXID(o.ctx), oldTupleBytes)
			s.Unlock()
			o.ctx.Pool.Unpin(s)
			if derr != nil {
				return nil, derr
			}
			break
		}
		if epqSkipDel {
			continue
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

// scanMatching sequentially scans `rel`, decoding every visible tuple, applying
// `pred`, and invoking `fn` for each match. statOID is the catalog OID of the
// relation for cumulative relation-stats accounting (0 = do not count, used by
// internal FK-maintenance scans that PG does not attribute to the user table):
// the whole call is one sequential scan reading every visible tuple, recorded
// (numscans + tuples_returned) at clean completion. M0118-0009 (`stats`, rung 6).
func scanMatching(ctx *Context, rel storage.RelFileNode, statOID uint32, cols []catalog.Column, pred planner.Expr, fn func(blk storage.BlockNumber, slot uint16, row Row) error) error {
	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return err
	}
	var examined int64 // visible tuples read across all blocks (tuples_returned)
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
			slot uint16
			row  Row
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
			if !mvcc.TupleVisibleSubxact(tuple.Header, ctx.Snap, ctx.Tx.XID, ctx.TxnMgr, ctx.CmdID, ctx.comboStore(), ctx.MultiXact) {
				// A tuple this statement's own command already killed is simply
				// not found by the DML scan. PG reaches it and takes ExecUpdate /
				// ExecDelete's TM_SelfModified arm with cmax == es_output_cid,
				// which returns NULL without touching the row — same row count,
				// same heap state (M0125-0053). The LATER-command case that PG
				// errors on cannot arrive here: such a version was written by
				// this statement, so its own cmin hides it from this scan. It is
				continue
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
				slot: slot,
				row:  rowCopy,
			})
		}
		s.RUnlock()
		ctx.Pool.Unpin(s)
		examined += int64(len(visible)) // tuples this scan returned (pre-qual)
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
			// No tuple-tag acquire on a foreign lock-only holder here
			// (formerly M0021 step 2b): blocking on a conflicting row
			// lock — including the FIFO tuple-tag acquire, with the
			// current-is-member skip — is waitForConflictingRowLock's
			// job, run by the UPDATE/DELETE callback before it stamps.
			// Acquiring up front regardless of conflict would queue a
			// non-conflicting write (e.g. a no-key UPDATE over a FOR
			// KEY SHARE holder) behind an unrelated parked waiter
			// holding the tag (design 0021-0012).
			if err := fn(blk, vt.slot, vt.row); err != nil {
				return err
			}
		}
	}
	// One sequential scan reading `examined` visible tuples (the UPDATE/DELETE
	// base scan); recorded into cumulative relation stats, gated by track_counts.
	// M0118-0009 (`stats`, rung 6; design 0118-0128).
	recordRelScan(ctx, statOID, examined)
	return nil
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

// selfRowLockMember is the write-path twin of lockRowsOp.selfIsLockMember:
// upstream's `current_is_member` — whether this backend (top-level xact or
// any of its subxacts) already holds a lock/updater membership on the
// tuple. Used to skip the pre-sleep tuple-tag acquire, which would
// otherwise deadlock an upgrader behind a waiter parked on our own held
// lock (heap_update/heap_delete's `if (!current_is_member)`). 0021-0012.
func selfRowLockMember(ctx *Context, h storage.HeapTupleHeader) bool {
	isSelf := func(xid storage.TransactionID) bool {
		if xid == storage.InvalidTransactionID {
			return false
		}
		if xid == ctx.Tx.XID {
			return true
		}
		if ctx.TxnMgr == nil {
			return false
		}
		return ctx.TxnMgr.TopLevelXid(xid) == ctx.TxnMgr.TopLevelXid(ctx.Tx.XID)
	}
	if h.Xmax == storage.InvalidTransactionID {
		return false
	}
	if storage.IsHeapTupleXmaxMulti(h.Infomask) {
		if ctx.MultiXact == nil {
			return false
		}
		members, ok := ctx.MultiXact.Members(multixact.MultiXactId(h.Xmax))
		if !ok {
			return false
		}
		for _, m := range members {
			if isSelf(m.Xid) {
				return true
			}
		}
		return false
	}
	return isSelf(h.Xmax)
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
	tupleTagged := false
	for {
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return err
		}
		s.RLock()
		tup, gerr := storage.PageGetHeapTuple(s.Page(), slot)
		var holders []storage.TransactionID
		selfMember := false
		if gerr == nil {
			holders = conflictingRowLockHolders(ctx, tup.Header, reqStatus)
			if len(holders) > 0 {
				selfMember = selfRowLockMember(ctx, tup.Header)
			}
		}
		s.RUnlock()
		ctx.Pool.Unpin(s)
		if len(holders) == 0 {
			return nil
		}
		// About to sleep on a conflicting holder: take the heavyweight tuple
		// tag first — heap_update/heap_delete's heap_acquire_tuplock before
		// MultiXactIdWait/XactLockTableWait — so concurrent waiters on this
		// row are granted in FIFO arrival order when the holder ends, instead
		// of racing on the WaitForXID broadcast. Skipped when this backend is
		// already a lock member on the tuple (upstream's
		// `if (!current_is_member)` guard): a parked waiter on the tag is
		// waiting for OUR held lock, so queueing behind it would deadlock
		// (tuplelock-upgrade-no-deadlock s3_delete after s3_keyshare).
		// Released at statement end (executor.ReleaseTupleLocks). 0021-0012.
		if !tupleTagged && !selfMember {
			ptr := storage.ItemPointer{Block: blk, Offset: slot}
			if err := ctx.acquireTupleLock(rel, ptr, tupleLockHwMode(reqStatus)); err != nil {
				return err
			}
			tupleTagged = true
		}
		for _, hx := range holders {
			if dl, terr := epqWait(ctx, hx); terr != nil {
				terr.Pos = pos
				return terr
			} else if dl {
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
	keyCols, vals, ok := indexRowKeyValues(idx, cols, row, cat...)
	if !ok {
		return nil, nil
	}
	var out []byte
	for i, v := range vals {
		keyPart, err := encodeBTreeKeyForColumn(v, keyCols[i], 0)
		if err != nil {
			return nil, err
		}
		out = append(out, keyPart...)
	}
	return out, nil
}

// indexRowKeyValues projects a heap row down to idx's KEY attributes: for each
// name in idx.Columns it resolves the column in cols (case-insensitively, the
// spelling the catalog stores), reads the row value at that column's position,
// and normalises enum labels.
//
// ok is false — not an error — when the projection cannot be made at all:
// a key column that is not in cols (an expression key, `Columns[i] == ""`), a
// position past the end of the row, or a NULL key value. Those are exactly the
// cases `encodeIndexKeyFromCols` has always answered with a nil key, and the
// callers read a nil key as "this row is not in this index" (NULLs do not
// participate in unique constraints; an expression index takes the
// `encodeExprIndexKey` fallback).
//
// M0130-S11.4 slice 3b-2c-ii-B2-c-iv split this out of the encoder because the
// PROJECTION is format-independent while the encoding is not: the tuple-format
// funnels in pgindex_btree.go need the same columns, the same row positions and
// the same enum normalisation, and deriving them a second time is precisely the
// sibling-pair divergence the ledger keeps recording. The returned columns are
// pointers INTO cols, parallel to vals and to the descriptor attributes
// buildPGIndexKeyDesc derives from the same idx.Columns order.
func indexRowKeyValues(idx *catalog.Index, cols []catalog.Column, row Row, cat ...catalog.Catalog) ([]*catalog.Column, []Datum, bool) {
	var im *catalog.InMemory
	if len(cat) > 0 && cat[0] != nil {
		im, _ = cat[0].(*catalog.InMemory)
	}
	keyCols := make([]*catalog.Column, 0, len(idx.Columns))
	vals := make([]Datum, 0, len(idx.Columns))
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
			return nil, nil, false
		}
		v := row[colOrd]
		if v.IsNull() {
			return nil, nil, false // NULLs don't participate in unique constraints
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
		keyCols = append(keyCols, col)
		vals = append(vals, v)
	}
	return keyCols, vals, true
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
	for _, idx := range ctx.Catalog.IndexesOnTable(tbl, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)) {
		idxRel := ctx.Catalog.IndexRelFileNode(idx)
		tree, err := openIndexBTree(ctx, idx, idxRel)
		if err != nil {
			continue
		}
		key, err := ctx.indexEntryKey(idx, cols, row, ptr)
		if err != nil || key == nil {
			// Fall back to expression-column encoding for expression-based indexes
			// (e.g. CREATE UNIQUE INDEX ON t(lower(col))). encodeIndexKeyFromCols
			// returns nil when idx.Columns[i]=="" (expression column); evaluate the
			// stored ColExprs to produce the btree key so concurrent sessions can
			// detect the in-progress insert via the arbiter scan. M0100-0005.
			//
			// The fallback is a BLOB-format repair, and only that: it concatenates
			// per-column blobs, so under the tuple format it would file a key the
			// tree cannot parse — an entry addressed by garbage rather than a
			// missing entry. An index the descriptor resolver ACCEPTS is never an
			// expression index anyway (buildPGIndexKeyDesc refuses those), so the
			// only way to arrive here with a descriptor is a refusal by the tuple
			// encoder itself, which must not be papered over.
			// M0130-S11.4 slice 3b-2c-ii-B2-c-vii.
			if ctx.pgIndexKeyDesc(idx) != nil {
				continue
			}
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
			k := encodeArbiterExprKey(ctx, v, planExpr, 0)
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

// rowHasNullKeyColumn reports whether any of idx's key columns is NULL in the
// candidate (cols/row). Gates the NULLS-NOT-DISTINCT heap-scan check: the normal
// btree path already enforces uniqueness when every key column is non-NULL, so
// the heap scan is needed only when a key column is actually NULL. Expression
// key columns (idx.Columns[i]=="") never match a real column name and so do not
// count as NULL here (NND on expression indexes is out of scope — design
// 0119-0004 §2.1). Design 0119-0004.
func rowHasNullKeyColumn(idx *catalog.Index, cols []catalog.Column, row Row) bool {
	for _, idxColName := range idx.Columns {
		if idxColName == "" {
			continue
		}
		for i := range cols {
			if strings.EqualFold(cols[i].Name, idxColName) && i < len(row) {
				if row[i].IsNull() {
					return true
				}
				break
			}
		}
	}
	return false
}

// nndKeyColumnsEqual reports whether two row versions carry an identical NULLS
// NOT DISTINCT key: the same NULL pattern across the index key columns AND equal
// encoded values for the non-NULL ones. A no-key-change UPDATE on such an index
// cannot collide with the row it replaces, so the caller skips the uniqueness
// probe (important on the ON CONFLICT DO UPDATE path, which runs the check
// before stamping the old tuple dead). Returns false on any structural mismatch
// (expression key, missing column) so the caller falls back to probing.
func nndKeyColumnsEqual(idx *catalog.Index, cols []catalog.Column, oldRow, newRow Row) bool {
	for _, idxColName := range idx.Columns {
		if idxColName == "" {
			return false // expression key — out of scope, treat as changed
		}
		ord := -1
		var col *catalog.Column
		for i := range cols {
			if strings.EqualFold(cols[i].Name, idxColName) {
				ord = i
				col = &cols[i]
				break
			}
		}
		if ord < 0 || ord >= len(oldRow) || ord >= len(newRow) {
			return false
		}
		oldNull := oldRow[ord].IsNull()
		newNull := newRow[ord].IsNull()
		if oldNull != newNull {
			return false
		}
		if oldNull {
			continue
		}
		oldKey, oerr := indexColumnFingerprint(oldRow[ord], col)
		newKey, nerr := indexColumnFingerprint(newRow[ord], col)
		if oerr != nil || nerr != nil || !bytes.Equal(oldKey, newKey) {
			return false
		}
	}
	return true
}

// nndDetail builds the 23505 DETAIL for a NULLS-NOT-DISTINCT conflict, rendering
// a NULL key column as the literal `null` (PostgreSQL prints
// `Key (a)=(null) already exists.`). Datum.Format() returns "" for KindNull, so
// NULL columns are mapped explicitly rather than via Format(). Design 0119-0004.
func nndDetail(idx *catalog.Index, cols []catalog.Column, row Row) string {
	colNames := make([]string, 0, len(idx.Columns))
	colVals := make([]string, 0, len(idx.Columns))
	for _, idxCol := range idx.Columns {
		colNames = append(colNames, idxCol)
		val := "null"
		for i, col := range cols {
			if strings.EqualFold(col.Name, idxCol) && i < len(row) {
				if !row[i].IsNull() {
					val = row[i].Format()
				}
				break
			}
		}
		colVals = append(colVals, val)
	}
	return fmt.Sprintf("Key (%s)=(%s) already exists.",
		strings.Join(colNames, ", "), strings.Join(colVals, ", "))
}

// checkNullsNotDistinctViaHeapScan enforces NULLS NOT DISTINCT uniqueness for a
// candidate row that has one or more NULL key columns on an NND index. Such rows
// are never stored in the btree (encodeIndexKeyFromCols returns nil), so the
// collision cannot be found by a btree probe; instead this seq-scans the heap
// for a live tuple whose index-key columns match the candidate's NULL pattern
// and non-NULL values exactly — NULL equals NULL, and a non-NULL column compares
// byte-equal under the column's index encoding (indexColumnFingerprint), so the
// comparison matches what the btree would consider equal. The fingerprint is
// per-COLUMN and TID-free on purpose: the candidate and the scanned rows are
// different heap tuples, so a tuple-format key would never compare equal and the
// scan would find no duplicate at all (M0130-S11.4 slice 3b-2c-ii-B2-c-viii). Returns the first
// matching tuple's ItemPointer and true. The ItemPointer is surfaced for the
// ON CONFLICT follow-up slice (design 0119-0004 §2.1); the plain INSERT/UPDATE
// callers only consume the boolean. Mirrors the heap-scan pattern of
// checkGistOverlapExclusion. Design 0119-0004.
func checkNullsNotDistinctViaHeapScan(ctx *Context, tbl *catalog.Table, idx *catalog.Index, cols []catalog.Column, row Row, rel storage.RelFileNode) (storage.ItemPointer, bool) {
	keyCols, ok := resolveNNDKeyColsFromRow(tbl, idx, cols, row)
	if !ok {
		return storage.ItemPointer{}, false
	}
	// Immediate check: the candidate row is not yet inserted, so the FIRST live
	// matching tuple is the duplicate (stopAt=1).
	count, ptr := scanNNDLiveMatches(ctx, tbl, rel, keyCols, 1)
	return ptr, count >= 1
}

// nndKeyCol is one resolved index key column for a NULLS NOT DISTINCT heap scan:
// the candidate's NULL-ness / encoded key plus the tbl.Columns ordinal used to
// read the decoded existing row. 0119-0004 (lifted to package scope for the
// deferred recheck path, 0119-0004-deferred-unique-nnd).
type nndKeyCol struct {
	tblOrd   int
	col      *catalog.Column
	candNull bool
	candKey  []byte // encoded candidate key (nil when candNull)
}

// resolveNNDKeyColsFromRow builds the per-key-column descriptors for an NND heap
// scan from a live candidate Row + its column layout. Returns ok=false on a
// structural problem (expression key column, missing column, encode failure) so
// the caller falls back to skipping the NND check. 0119-0004-deferred-unique-nnd.
func resolveNNDKeyColsFromRow(tbl *catalog.Table, idx *catalog.Index, cols []catalog.Column, row Row) ([]nndKeyCol, bool) {
	keyCols := make([]nndKeyCol, 0, len(idx.Columns))
	for _, idxColName := range idx.Columns {
		if idxColName == "" {
			return nil, false // expression NND index — out of scope
		}
		var candVal Datum
		foundCand := false
		for i := range cols {
			if strings.EqualFold(cols[i].Name, idxColName) && i < len(row) {
				candVal = row[i]
				foundCand = true
				break
			}
		}
		tblOrd, col := nndTableColumn(tbl, idxColName)
		if !foundCand || tblOrd < 0 || col == nil {
			return nil, false
		}
		kc := nndKeyCol{tblOrd: tblOrd, col: col, candNull: candVal.IsNull()}
		if !kc.candNull {
			enc, eerr := indexColumnFingerprint(candVal, col)
			if eerr != nil {
				return nil, false
			}
			kc.candKey = enc
		}
		keyCols = append(keyCols, kc)
	}
	return keyCols, true
}

// nndTableColumn resolves an index key column name to its tbl.Columns ordinal
// and *catalog.Column (case-insensitive), or (-1, nil) if absent.
func nndTableColumn(tbl *catalog.Table, name string) (int, *catalog.Column) {
	for j := range tbl.Columns {
		if strings.EqualFold(tbl.Columns[j].Name, name) {
			return j, &tbl.Columns[j]
		}
	}
	return -1, nil
}

// scanNNDLiveMatches seq-scans rel and counts live heap tuples whose index key
// columns match keyCols' NULL pattern + non-NULL encoded key bytes (NULL equals
// NULL, a non-NULL column compares byte-equal under its index encoding). It stops
// early once the count reaches stopAt. Returns the count and the first match's
// ItemPointer. Shared by the immediate NND check (stopAt=1: any match is a dup)
// and the deferred-at-COMMIT recheck (stopAt=2: the candidate row is itself one
// match, so ≥2 is the violation). 0119-0004-deferred-unique-nnd.
func scanNNDLiveMatches(ctx *Context, tbl *catalog.Table, rel storage.RelFileNode, keyCols []nndKeyCol, stopAt int) (int, storage.ItemPointer) {
	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return 0, storage.ItemPointer{}
	}
	matches := 0
	var first storage.ItemPointer
	decRow := make(Row, len(tbl.Columns))
	for b := storage.BlockNumber(0); b < nBlocks; b++ {
		s, perr := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: b})
		if perr != nil {
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
			match := true
			for _, kc := range keyCols {
				if kc.tblOrd >= len(decRow) {
					match = false
					break
				}
				existVal := decRow[kc.tblOrd]
				if kc.candNull {
					if !existVal.IsNull() {
						match = false
						break
					}
					continue
				}
				if existVal.IsNull() {
					match = false
					break
				}
				existKey, eerr := indexColumnFingerprint(existVal, kc.col)
				if eerr != nil || !bytes.Equal(existKey, kc.candKey) {
					match = false
					break
				}
			}
			if match {
				if matches == 0 {
					first = storage.ItemPointer{Block: b, Offset: slotIdx}
				}
				matches++
				if matches >= stopAt {
					s.RUnlock()
					ctx.Pool.Unpin(s)
					return matches, first
				}
			}
		}
		s.RUnlock()
		ctx.Pool.Unpin(s)
	}
	return matches, first
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
	for _, idx := range ctx.Catalog.IndexesOnTable(tbl, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)) {
		if !idx.Unique && !idx.Primary {
			continue
		}
		idxRel := ctx.Catalog.IndexRelFileNode(idx)
		tree, err := openIndexBTree(ctx, idx, idxRel)
		if err != nil {
			continue
		}
		key, err := ctx.indexRowProbeKey(idx, cols, row)
		if err != nil || key == nil {
			// NULLS NOT DISTINCT: a candidate row with NULL key column(s) has no
			// btree key (encodeIndexKeyFromCols returns nil) and is never stored
			// in the index, yet under NND such NULLs collide with an existing
			// NULL-keyed row. Fall back to a heap scan to detect that collision.
			// Gated on idx.NullsNotDistinct + an actual NULL key column so every
			// non-NND index and every non-NULL reason for a nil key (expression
			// column, arity mismatch) keeps the existing skip. Design 0119-0004.
			if err == nil && idx.NullsNotDistinct && rowHasNullKeyColumn(idx, cols, row) {
				// DEFERRABLE INITIALLY DEFERRED (or SET CONSTRAINTS … DEFERRED):
				// queue the NULL-pattern recheck for COMMIT instead of raising now,
				// so a transient NULL duplicate is tolerated mid-transaction.
				// 0119-0004-deferred-unique-nnd.
				if uniqueCheckDeferred(ctx, idx) {
					queueDeferredNNDUniqueCheck(ctx, tbl, idx, cols, row)
					continue
				}
				if _, found := checkNullsNotDistinctViaHeapScan(ctx, tbl, idx, cols, row, rel); found {
					return &ExecError{
						Code:    "23505",
						Pos:     pos,
						Message: fmt.Sprintf("duplicate key value violates unique constraint %q", idx.Name),
						Detail:  nndDetail(idx, cols, row),
					}
				}
			}
			continue
		}
		// DEFERRABLE INITIALLY DEFERRED (or SET CONSTRAINTS … DEFERRED): queue the
		// uniqueness re-probe for COMMIT instead of raising now. A transient
		// duplicate is allowed mid-transaction. 0119-0004.
		if uniqueCheckDeferred(ctx, idx) {
			queueDeferredUniqueCheck(ctx, tbl, idx, cols, row, key)
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
//
// This is a VALUE fingerprint, not a tree key: the two encodings are never
// handed to a btree, only compared with each other. It therefore goes through
// `indexKeyFingerprint` and must NOT move to `indexEntryKey` when the tuple
// format lands — a tuple key embeds the row's heap TID, and oldRow/newRow are
// different heap tuples, so every UPDATE would report "key changed" and re-probe
// every unique index. M0130-S11.4 slices 3b-2c-ii-B2-c-iv and -viii.
func indexKeyColumnsChanged(idx *catalog.Index, cols []catalog.Column, oldRow, newRow Row, cat catalog.Catalog) bool {
	oldKey, oerr := indexKeyFingerprint(idx, cols, oldRow, cat)
	newKey, nerr := indexKeyFingerprint(idx, cols, newRow, cat)
	if oerr != nil || nerr != nil {
		return true
	}
	if oldKey == nil || newKey == nil {
		// A NULL key column makes encodeIndexKeyFromCols return nil. Under the
		// default (NULLS DISTINCT) semantics such a row never collides, so treat
		// it as "changed" to force the probe (which then no-ops). Under NULLS
		// NOT DISTINCT the NULL columns DO form part of the key, so compare the
		// NULL pattern + non-NULL values directly: a no-key-change NULL→NULL
		// UPDATE is genuinely unchanged and must skip the probe to avoid
		// self-conflicting with the not-yet-stamped old version (the ON CONFLICT
		// DO UPDATE path checks before stamping). Design 0119-0004.
		if idx.NullsNotDistinct {
			return !nndKeyColumnsEqual(idx, cols, oldRow, newRow)
		}
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
	for _, idx := range ctx.Catalog.IndexesOnTable(tbl, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)) {
		if !idx.Unique && !idx.Primary {
			continue
		}
		if !forceAll && oldRow != nil && !indexKeyColumnsChanged(idx, cols, oldRow, newRow, ctx.Catalog) {
			// Key columns unchanged — cannot collide with itself. Skip the probe.
			continue
		}
		idxRel := ctx.Catalog.IndexRelFileNode(idx)
		tree, err := openIndexBTree(ctx, idx, idxRel)
		if err != nil {
			continue
		}
		key, err := ctx.indexRowProbeKey(idx, cols, newRow)
		if err != nil || key == nil {
			// NULLS NOT DISTINCT: new version has NULL key column(s). The old
			// version was already stamped xmax = effectiveWriterXID before this
			// check (operators_storage.go old-tuple stamp sites), so
			// isLiveForUniqueCheck classifies it as dead and the heap scan skips
			// it — a no-key-change NULL→NULL UPDATE never self-conflicts.
			// Design 0119-0004.
			if err == nil && idx.NullsNotDistinct && rowHasNullKeyColumn(idx, cols, newRow) {
				// DEFERRABLE INITIALLY DEFERRED (or SET CONSTRAINTS … DEFERRED):
				// queue the NULL-pattern recheck for COMMIT instead of raising now.
				// 0119-0004-deferred-unique-nnd.
				if uniqueCheckDeferred(ctx, idx) {
					queueDeferredNNDUniqueCheck(ctx, tbl, idx, cols, newRow)
					continue
				}
				if _, found := checkNullsNotDistinctViaHeapScan(ctx, tbl, idx, cols, newRow, rel); found {
					return &ExecError{
						Code:    "23505",
						Pos:     pos,
						Message: fmt.Sprintf("duplicate key value violates unique constraint %q", idx.Name),
						Detail:  nndDetail(idx, cols, newRow),
					}
				}
			}
			continue
		}
		// DEFERRABLE INITIALLY DEFERRED (or SET CONSTRAINTS … DEFERRED): queue the
		// uniqueness re-probe for COMMIT instead of raising now. 0119-0004.
		if uniqueCheckDeferred(ctx, idx) {
			queueDeferredUniqueCheck(ctx, tbl, idx, cols, newRow, key)
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
	for _, idx := range ctx.Catalog.IndexesOnTable(tbl, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)) {
		if !idx.IsExclusion {
			continue
		}
		// DEFERRABLE INITIALLY DEFERRED (or SET CONSTRAINTS … DEFERRED): queue the
		// candidate for a COMMIT-time re-probe instead of raising now, so a
		// transient conflict resolved before COMMIT is allowed. 0119-0004.
		if excludeCheckDeferred(ctx, idx) {
			queueDeferredExclusionCheck(ctx, tbl, idx, cols, row)
			continue
		}
		switch idx.ExclusionOp {
		case "=":
			idxRel := ctx.Catalog.IndexRelFileNode(idx)
			tree, err := openIndexBTree(ctx, idx, idxRel)
			if err != nil {
				continue
			}
			key, err := ctx.indexRowProbeKey(idx, cols, row)
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
	_, err := writeHeapRowReturning(ctx, rel, cols, row)
	return err
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
	// HEAP_HASVARWIDTH: PG18's nocachegetattr fast path crashes when
	// this bit is missing on a TupleDesc with varlena attrs. Mirrors
	// PG's heap_fill_tuple (postgres/src/backend/access/common/
	// heaptuple.c:326). The PG-canonical sibling
	// writeHeapRowReturningPG already sets this; the regular path was
	// missing it. M0118-0131.
	if pgRowHasVarWidth(cols, row) {
		tuple.Header.Infomask |= storage.HeapHasVarWidth
	}
	// HEAP_HASEXTERNAL: PG's heap_deform_tuple needs this bit to skip
	// external TOAST pointers when computing attribute offsets.
	// Mirrors PG's heap_fill_tuple (heaptuple.c:343). M0118-0131.
	if pgRowHasExternal(cols, row) {
		tuple.Header.Infomask |= storage.HeapHasExternal
	}
	// M0129-S8.3c: stamp the inserting command id (cmin).
	// GetCurrentCommandId(true) marks the counter as used so the next
	// CommandCounterIncrement actually advances, matching PG.s heap_insert
	// calling GetCurrentCommandId(true) before stamping cmin.
	tuple.Header.SetCmin(ctx.GetCurrentCommandId(true))
	tupleBytes, err := tuple.MarshalBinary()
	if err != nil {
		return ptr, err
	}

	// Emits the native RecordKindHeapInsert WAL record so the logical
	// decoder sees the change.
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
		firstBlk, err := batchExtendAndRegisterFSM(ctx.Pool, ctx.FSM, rel, ctx.Tx.XID)
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
	slot, blk, err := ctx.Pool.PinNewWithXID(rel, ctx.Tx.XID)
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

	tuple, tupleBytes, err := buildCatalogPGHeapTuple(ctx, cols, row)
	if err != nil {
		return ptr, err
	}

	logHeap := ctx.Pool.LogHeapInsert()
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
		firstBlk, err := batchExtendAndRegisterFSM(ctx.Pool, ctx.FSM, rel, ctx.Tx.XID)
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

	slot, blk, err := ctx.Pool.PinNewWithXID(rel, ctx.Tx.XID)
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

// buildCatalogPGHeapTuple encodes one PG-canonical catalog heap tuple —
// the shared body of writeHeapRowReturningPG (INSERT) and
// updateHeapRowCanonicalPG (the new version of an UPDATE). Factored in
// B0.2 so the two paths can never drift (sibling-path rule).
func buildCatalogPGHeapTuple(ctx *Context, cols []catalog.Column, row Row) (storage.HeapTuple, []byte, error) {
	var tuple storage.HeapTuple
	body, err := EncodeRowPG(cols, row)
	if err != nil {
		return tuple, nil, &ExecError{Code: "XX000", Message: err.Error()}
	}
	bitmap := NullBitmapPG(row)
	// xmin = effective writer XID (sub-XID inside an open savepoint) so a
	// savepoint-inserted row disappears on ROLLBACK TO; no-op outside a
	// savepoint. M0118-0009 — twin of writeHeapRowReturning.
	xmin := effectiveWriterXID(ctx)
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
	// HEAP_HASEXTERNAL: PG's heap_deform_tuple needs this bit
	// for TOAST-external columns. M0118-0131.
	if pgRowHasExternal(cols, row) {
		tuple.Header.Infomask |= storage.HeapHasExternal
	}
	// M0129-S8.3c: stamp the inserting command id (cmin).
	// GetCurrentCommandId(true) marks the counter as used so the next
	// CommandCounterIncrement actually advances, matching PG.s heap_insert
	// calling GetCurrentCommandId(true) before stamping cmin.
	tuple.Header.SetCmin(ctx.GetCurrentCommandId(true))
	tupleBytes, err := tuple.MarshalBinary()
	if err != nil {
		return tuple, nil, err
	}
	return tuple, tupleBytes, nil
}

// updateHeapRowCanonicalPG performs a PG-faithful NON-HOT catalog heap UPDATE
// (B0.2, doc 02a §3): verifies the old version at oldTID is live, writes the
// new PG-canonical version (same page when it fits, else the last/new page),
// stamps the old tuple with xmax + a forward t_ctid (no HOT bits), and emits
// ONE xl_heap_update record covering both changes. Returns the new TID — the
// caller's write-through cache must store it (TID-carrying cache contract).
// Index maintenance is the CALLER's job: an update touching any indexed
// column must insert fresh entries into every index on the catalog.
//
// Catalog DDL is serialized at the SQL layer, so no EPQ/concurrent-updater
// handling is needed; a non-live old tuple is a stale-TID bug and errors
// loudly (R-B0-3).
func updateHeapRowCanonicalPG(ctx *Context, rel storage.RelFileNode, cols []catalog.Column,
	oldTID storage.ItemPointer, newRow Row) (storage.ItemPointer, error) {
	var newTID storage.ItemPointer
	if err := ctx.MaterializeWriterXID(); err != nil {
		return newTID, err
	}
	tuple, tupleBytes, err := buildCatalogPGHeapTuple(ctx, cols, newRow)
	if err != nil {
		return newTID, err
	}
	xmax := effectiveWriterXID(ctx)
	logUpdate := ctx.Pool.LogHeapUpdate()

	verifyOldLive := func(page storage.Page) error {
		ht, err := storage.PageGetHeapTuple(page, oldTID.Offset)
		if err != nil {
			return &ExecError{Code: "XX000", Message: fmt.Sprintf("catalog update: stale TID (%d,%d): %v", oldTID.Block, oldTID.Offset, err)}
		}
		if ht.Header.Xmin == storage.InvalidTransactionID || ht.Header.Xmax != storage.InvalidTransactionID {
			return &ExecError{Code: "XX000", Message: fmt.Sprintf("catalog update: TID (%d,%d) is not the live version", oldTID.Block, oldTID.Offset)}
		}
		return nil
	}

	// Fast path: the new version fits on the old version's page — one page,
	// one lock, one record (PG's same-page non-HOT form: single block ref).
	oldSlotBuf, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: oldTID.Block})
	if err != nil {
		return newTID, err
	}
	oldSlotBuf.Lock()
	if err := verifyOldLive(oldSlotBuf.Page()); err != nil {
		oldSlotBuf.Unlock()
		ctx.Pool.Unpin(oldSlotBuf)
		return newTID, err
	}
	if newSlot, aerr := storage.PageAddHeapTuple(oldSlotBuf.Page(), tuple); aerr == nil {
		// Self-pointing t_ctid on the new version (chain tail) — what
		// replay's decodeXLogHeapInsertTuple reconstructs at new_offnum.
		if err := storage.PageSetHeapTupleCtid(oldSlotBuf.Page(), newSlot, storage.ItemPointer{Block: oldTID.Block, Offset: newSlot}); err != nil {
			oldSlotBuf.Unlock()
			ctx.Pool.Unpin(oldSlotBuf)
			return newTID, err
		}
		if err := storage.PageStampUpdatedOldTuple(oldSlotBuf.Page(), oldTID.Offset, xmax, oldTID.Block, newSlot); err != nil {
			oldSlotBuf.Unlock()
			ctx.Pool.Unpin(oldSlotBuf)
			return newTID, err
		}
		var derr error
		if logUpdate == nil {
			ctx.Pool.MarkDirty(oldSlotBuf)
		} else {
			derr = ctx.Pool.MarkDirtyLogicalChange(oldSlotBuf, func() (storage.LSN, error) {
				return logUpdate(rel, oldTID.Block, oldTID.Offset, oldTID.Block, newSlot, xmax, tupleBytes)
			})
		}
		if ctx.FSM != nil {
			ctx.FSM.RecordFreeSpaceForPage(rel, oldTID.Block, oldSlotBuf.Page())
		}
		if ctx.VM != nil {
			ctx.VM.ClearBlock(rel, oldTID.Block)
		}
		oldSlotBuf.Unlock()
		ctx.Pool.Unpin(oldSlotBuf)
		if derr != nil {
			return newTID, derr
		}
		return storage.ItemPointer{Block: oldTID.Block, Offset: newSlot}, nil
	} else if !errors.Is(aerr, storage.ErrNoSpaceInPage) {
		oldSlotBuf.Unlock()
		ctx.Pool.Unpin(oldSlotBuf)
		return newTID, aerr
	}
	// Old page is full: release it and take the cross-page path — new
	// version on the last (or a freshly extended) page, both pages locked
	// in block order, old re-verified under lock, ONE record for both.
	oldSlotBuf.Unlock()
	ctx.Pool.Unpin(oldSlotBuf)

	pinNewTarget := func() (*storage.Slot, storage.BlockNumber, error) {
		nBlocks, err := ctx.Pool.NBlocks(rel)
		if err != nil {
			return nil, 0, err
		}
		if nBlocks > 0 && nBlocks-1 != oldTID.Block {
			blk := nBlocks - 1
			s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
			if err != nil {
				return nil, 0, err
			}
			return s, blk, nil
		}
		s, blk, err := ctx.Pool.PinNewWithXID(rel, ctx.Tx.XID)
		if err != nil {
			return nil, 0, err
		}
		return s, blk, nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		newBuf, newBlk, err := pinNewTarget()
		if err != nil {
			return newTID, err
		}
		// Lock both pages in block order (upstream heap_update's deadlock
		// avoidance); newBlk > oldTID.Block for append/extend targets, but
		// keep the general rule.
		oldBuf, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: oldTID.Block})
		if err != nil {
			ctx.Pool.Unpin(newBuf)
			return newTID, err
		}
		if oldTID.Block < newBlk {
			oldBuf.Lock()
			newBuf.Lock()
		} else {
			newBuf.Lock()
			oldBuf.Lock()
		}
		unlockBoth := func() {
			newBuf.Unlock()
			oldBuf.Unlock()
			ctx.Pool.Unpin(newBuf)
			ctx.Pool.Unpin(oldBuf)
		}
		if err := verifyOldLive(oldBuf.Page()); err != nil {
			unlockBoth()
			return newTID, err
		}
		if storage.IsNew(newBuf.Page()) {
			if err := storage.InitPage(newBuf.Page()); err != nil {
				unlockBoth()
				return newTID, err
			}
		}
		newSlot, aerr := storage.PageAddHeapTuple(newBuf.Page(), tuple)
		if aerr != nil {
			unlockBoth()
			if errors.Is(aerr, storage.ErrNoSpaceInPage) {
				continue // last block was full — extend on the retry
			}
			return newTID, aerr
		}
		if err := storage.PageSetHeapTupleCtid(newBuf.Page(), newSlot, storage.ItemPointer{Block: newBlk, Offset: newSlot}); err != nil {
			unlockBoth()
			return newTID, err
		}
		if err := storage.PageStampUpdatedOldTuple(oldBuf.Page(), oldTID.Offset, xmax, newBlk, newSlot); err != nil {
			unlockBoth()
			return newTID, err
		}
		var derr error
		var recLSN storage.LSN
		if logUpdate == nil {
			ctx.Pool.MarkDirty(newBuf)
		} else {
			derr = ctx.Pool.MarkDirtyLogicalChange(newBuf, func() (storage.LSN, error) {
				lsn, lerr := logUpdate(rel, oldTID.Block, oldTID.Offset, newBlk, newSlot, xmax, tupleBytes)
				recLSN = lsn
				return lsn, lerr
			})
		}
		// The OLD page was mutated too (PageStampUpdatedOldTuple above) and is
		// described by the SAME record — so it must carry that record's LSN,
		// not just a dirty bit. A plain MarkDirty here left pd_lsn stale
		// whenever the page had already been imaged this checkpoint epoch,
		// which both breaks WAL-before-data for the xmax stamp and makes a
		// replaying PG re-apply the record over the page (M0131-S26).
		if recLSN != 0 {
			ctx.Pool.MarkDirtyCoveredByRecordLocked(oldBuf, recLSN)
		} else {
			ctx.Pool.MarkDirty(oldBuf)
		}
		if ctx.FSM != nil {
			ctx.FSM.RecordFreeSpaceForPage(rel, newBlk, newBuf.Page())
		}
		if ctx.VM != nil {
			ctx.VM.ClearBlock(rel, newBlk)
			ctx.VM.ClearBlock(rel, oldTID.Block)
		}
		unlockBoth()
		if derr != nil {
			return newTID, derr
		}
		return storage.ItemPointer{Block: newBlk, Offset: newSlot}, nil
	}
	return newTID, &ExecError{Code: "XX000", Message: "catalog update: freshly extended page did not accept tuple"}
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
	// Adopt PostgreSQL's self-pointing t_ctid for a freshly inserted tuple:
	// stamp {blk, lineSlot} in place of goopg's legacy {InvalidBlockNumber, 0}
	// sentinel. The slot's line pointer is assigned by the caller's
	// PageAddHeapTuple, so blk/lineSlot are only known here (post-append) —
	// this is the single choke point every fresh-insert path funnels through.
	// Both the stored page AND tupleBytes are stamped: goopg's native
	// RecordKindHeapInsert carries the whole tuple verbatim, so redo must
	// reconstruct the same self-pointing t_ctid (and the FPI captured below by
	// MarkDirtyLogicalChange must see it too). isChainTailCTID treats a
	// self-pointing t_ctid identically to the old sentinel. Caller holds slot.Lock.
	self := storage.ItemPointer{Block: blk, Offset: lineSlot}
	if err := storage.PageSetHeapTupleCtid(slot.Page(), lineSlot, self); err != nil {
		return err
	}
	if len(tupleBytes) >= 18 {
		binary.LittleEndian.PutUint32(tupleBytes[12:16], uint32(blk))
		binary.LittleEndian.PutUint16(tupleBytes[16:18], lineSlot)
	}

	if logHeap == nil {
		pool.MarkDirty(slot)
		return nil
	}
	// A9: XLOG_HEAP_INIT_PAGE — when this tuple is the FIRST on the page (the
	// page has exactly one line pointer after the insert), mark the record so a
	// heterogeneous PG standby PageInit's the (possibly not-yet-extended) page
	// before applying, instead of PANICking with "reference to an invalid page".
	// Mirrors PG's own first-insert-on-a-new-page behaviour; harmless for goopg's
	// own replay (opcode is masked by 0x70).
	initPage := false
	if cnt, cerr := storage.PageLinePointerCount(slot.Page()); cerr == nil && cnt == 1 {
		initPage = true
	}
	// MarkDirtyLogicalChange (not MarkDirtyChangeRecord): the logical
	// HeapInsert record MUST always be emitted so the M0008 logical
	// decoder sees the per-row change. MarkDirtyChangeRecord would
	// suppress the logical record on first-dirty-in-epoch in favour
	// of a bare PageImage — fine for redo, fatal for logical
	// replication. See docs/design/0103-0018-heap-fpi-and-logical-record-coexistence.md.
	return pool.MarkDirtyLogicalChange(slot, func() (storage.LSN, error) {
		return logHeap(rel, blk, lineSlot, tupleBytes, initPage)
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
	// Emits the native RecordKindHeapDelete WAL record so the logical
	// decoder (classifier.go) sees the change for pgoutput/logical replication.
	if err := markHeapDeleteDirty(ctx.Pool, slot, rel, blk, lineSlot, xmax, oldTuple); err != nil {
		return err
	}
	if ctx.VM != nil {
		ctx.VM.ClearBlock(rel, blk)
	}
	return nil
}
