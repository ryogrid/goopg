package executor

import (
	"context"
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/multixact"
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
	// lockStrength is the HeapXmax* lock-mode infomask bit corresponding
	// to the locks slice, resolved once at Open from the four-way row-lock
	// strength (M0118-0003):
	//   FOR KEY SHARE      → HeapXmaxKeyShrLock
	//   FOR SHARE          → HeapXmaxShrLock
	//   FOR NO KEY UPDATE  → HeapXmaxExclLock              (lockKeysUpdated=false)
	//   FOR UPDATE         → HeapXmaxExclLock + KEYS_UPDATED (lockKeysUpdated=true)
	// Multiple LockedRels targeting the same scan would need merging under
	// strongest-wins; v0 keeps that deferred and uses the first
	// LockedRel's strength.
	lockStrength uint16
	// lockKeysUpdated distinguishes FOR UPDATE (true) from FOR NO KEY UPDATE
	// (false) — both stamp HeapXmaxExclLock, but FOR UPDATE additionally
	// reserves the key via HEAP_KEYS_UPDATED so a concurrent FOR KEY SHARE
	// correctly conflicts with it. Mirrors heap_lock_tuple's new_infomask2.
	lockKeysUpdated bool

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

	// filterPred / filterCols: extracted from the child chain at Open time.
	// When stampLock follows a committed-update CTID chain to a live successor
	// (EPQ for SELECT FOR UPDATE), the filter predicate is re-evaluated against
	// the new row — matching PostgreSQL's EvalPlanQualFetchRowMark behaviour.
	filterPred planner.Expr
	filterCols []catalog.Column

	// waitPolicy is the effective NOWAIT / SKIP LOCKED / (default) blocking
	// policy for the per-tuple lock acquisition in stampLock. Resolved once at
	// Open from Locks[0].WaitPolicy (v0 assumes a single policy per LockRows;
	// multi-clause merge is deferred, consistent with lockStrength). M0118-0003.
	waitPolicy planner.LockWaitPolicy
	// lockRelName is the name of the first locked relation, used to format the
	// NOWAIT "could not obtain lock on row in relation \"%s\"" diagnostic with
	// the upstream-canonical relation name. Resolved at Open from Locks[0].
	lockRelName string
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

// findFilterPred walks the child chain past Project wrappers and returns the
// predicate of the first filterOp found. Returns nil when no filter is present.
func findFilterPred(op Operator) planner.Expr {
	for {
		switch v := op.(type) {
		case *filterOp:
			return v.pred
		case *projectOp:
			op = v.child
		default:
			return nil
		}
	}
}

// filterPredMaxColRef returns the maximum ColumnRef.Index found anywhere in
// expr, or -1 if there are none. Used to detect when a filter predicate
// references columns from a non-locked join input so EPQ recheck can be safely
// skipped (M0100-0010).
func filterPredMaxColRef(expr planner.Expr) int {
	max := -1
	var walk func(planner.Expr)
	walk = func(e planner.Expr) {
		if e == nil {
			return
		}
		if cr, ok := e.(*planner.ColumnRef); ok {
			if cr.Index > max {
				max = cr.Index
			}
			return
		}
		switch x := e.(type) {
		case *planner.BinaryOp:
			walk(x.Left)
			walk(x.Right)
		case *planner.UnaryOp:
			walk(x.Operand)
		case *planner.CastExpr:
			walk(x.Operand)
		case *planner.IsNullExpr:
			walk(x.Operand)
		case *planner.IsBoolExpr:
			walk(x.Operand)
		case *planner.IsDistinctFromExpr:
			walk(x.Left)
			walk(x.Right)
		case *planner.FuncCall:
			for _, a := range x.Args {
				walk(a)
			}
		case *planner.CaseExpr:
			walk(x.Operand)
			for _, w := range x.Whens {
				walk(w.When)
				walk(w.Then)
			}
			walk(x.Else)
		case *planner.InExpr:
			walk(x.Operand)
			for _, v := range x.List {
				walk(v)
			}
		}
	}
	walk(expr)
	return max
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
		case *joinOp:
			// For joins, prefer the left (outer) child because its scan
			// cursor stays at the current row while the inner scan loops.
			// When the left child is not a real-table scan (e.g. a VALUES
			// or correlated subquery), fall back to the right child so that
			// FOR UPDATE OF a real table still gets a TID even when the
			// planner reordered the join to put VALUES outermost.
			if left := findScanLeaf(v.left); left != nil {
				return left
			}
			op = v.right
		case *nestedLoopIndexJoinOp:
			// Prefer outer; fall back to inner (indexScanOp) when outer is
			// not a real-table scan (e.g. VALUES, subquery).
			if left := findScanLeaf(v.outer); left != nil {
				return left
			}
			return v.inner
		case *multiHashJoinOp:
			// Hash join: the probe side scans the driving (non-build) table.
			// When probeOp has not yet been assigned (pre-Open), fall through.
			if v.probeOp != nil {
				if p := findScanLeaf(v.probeOp); p != nil {
					return p
				}
			}
			// Fall back: check build children for a real-table scan.
			for _, c := range v.children {
				if p := findScanLeaf(c); p != nil {
					return p
				}
			}
			return nil
		default:
			return nil
		}
	}
}

// findScanLeafForRel finds the currentTIDProvider for a specific relation
// (identified by its storage.RelFileNode), preferring the leftmost occurrence.
// Called after o.child.Open so seqScanOp.rel and indexScanOp.ctx are set.
// Used to ensure the lockRowsOp captures TIDs from the correct scan in complex
// joins where the locked table is not the leftmost leaf (M0100-0010).
func findScanLeafForRel(op Operator, targetRel storage.RelFileNode) currentTIDProvider {
	for {
		switch v := op.(type) {
		case *seqScanOp:
			if v.rel == targetRel {
				return v
			}
			return nil
		case *indexScanOp:
			if v.ctx != nil && v.ctx.Catalog.RelFileNode(v.plan.Table) == targetRel {
				return v
			}
			return nil
		case *projectOp:
			op = v.child
		case *filterOp:
			op = v.child
		case *setOp:
			// setOp implements currentTIDProvider for partition unions;
			// we can't check its target rel, fall back to generic findScanLeaf.
			return nil
		case *joinOp:
			if left := findScanLeafForRel(v.left, targetRel); left != nil {
				return left
			}
			op = v.right
		case *nestedLoopIndexJoinOp:
			if left := findScanLeafForRel(v.outer, targetRel); left != nil {
				return left
			}
			if v.inner != nil && v.inner.ctx != nil &&
				v.inner.ctx.Catalog.RelFileNode(v.inner.plan.Table) == targetRel {
				return v.inner
			}
			return nil
		case *multiHashJoinOp:
			if v.probeOp != nil {
				if p := findScanLeafForRel(v.probeOp, targetRel); p != nil {
					return p
				}
			}
			for _, c := range v.children {
				if p := findScanLeafForRel(c, targetRel); p != nil {
					return p
				}
			}
			return nil
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
		// Resolve the four-way row-lock strength to its tuple-lock infomask bits
		// (M0118-0003). FOR KEY SHARE and FOR SHARE are distinct read-intent
		// strengths — a no-key UPDATE conflicts with FOR SHARE but not FOR KEY
		// SHARE — and FOR UPDATE additionally reserves the key (HEAP_KEYS_UPDATED)
		// vs FOR NO KEY UPDATE. Mirrors heap_lock_tuple's per-mode new_infomask.
		switch o.plan.Locks[0].Strength {
		case planner.LockStrengthForKeyShare:
			o.lockStrength = storage.HeapXmaxKeyShrLock
		case planner.LockStrengthForShare:
			o.lockStrength = storage.HeapXmaxShrLock
		case planner.LockStrengthForNoKeyUpdate:
			o.lockStrength = storage.HeapXmaxExclLock
		default:
			// FOR UPDATE — strongest; reserves the key just as a key-changing
			// UPDATE writes it, so a concurrent FOR KEY SHARE conflicts.
			o.lockStrength = storage.HeapXmaxExclLock
			o.lockKeysUpdated = true
		}
		// Resolve the wait policy and relation name once for stampLock's
		// per-tuple acquisition (NOWAIT / SKIP LOCKED). M0118-0003.
		o.waitPolicy = o.plan.Locks[0].WaitPolicy
		if o.plan.Locks[0].Table != nil {
			o.lockRelName = o.plan.Locks[0].Table.Name
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
		case planner.LockWaitBlock, planner.LockWaitSkipLocked:
			// SKIP LOCKED affects only per-row locks: the relation-level
			// RowShareLock is always acquired with the normal blocking
			// policy. Mirrors upstream — the LockWaitPolicy governs
			// heap_lock_tuple, not the relation lock. The actual row
			// skipping happens per-tuple in stampLock / stampLockInner.
			// M0118-0003.
			err = ctx.acquireRelLock(rel, lockmgr.RowShareLock)
		case planner.LockWaitNoWait:
			// NOWAIT: try once and bail with 55P03 if the
			// relation lock isn't immediately grantable.
			// Mirrors upstream's "could not obtain lock on
			// row" diagnostic at the relation-coarse layer
			// goopg has today.
			err = ctx.tryAcquireRelLock(rel, lockmgr.RowShareLock)
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
	// Prefer the scan for the first physical locked relation so that
	// the TID tracked by drainAndStamp comes from the right table.
	// This matters when the locked table is not the leftmost scan leaf
	// in a complex join (e.g. FOR UPDATE OF jt where jt is not outer).
	o.scan = findScanLeaf(o.child) // default fallback
	for i := range o.plan.Locks {
		if o.plan.Locks[i].Table != nil {
			targetRel := ctx.Catalog.RelFileNode(o.plan.Locks[i].Table)
			if scan := findScanLeafForRel(o.child, targetRel); scan != nil {
				o.scan = scan
			}
			break
		}
	}
	// Extract filter predicate for EPQ re-eval after CTID chain follow.
	o.filterPred = findFilterPred(o.child)
	if len(o.plan.Locks) > 0 && o.plan.Locks[0].Table != nil {
		o.filterCols = o.plan.Locks[0].Table.Columns
	}
	// Only keep filterPred for EPQ if all ColumnRefs it contains are within
	// the locked-table column range. Predicates that reference other join
	// inputs (e.g. a VALUES clause) have column indices beyond filterCols;
	// evaluating them against only the re-fetched heap tuple would return an
	// out-of-range error and incorrectly skip the row (M0100-0010).
	if o.filterPred != nil && filterPredMaxColRef(o.filterPred) >= len(o.filterCols) {
		o.filterPred = nil
	}
	// When the planner lifted a LIMIT above this LockRows (SKIP LOCKED), cap the
	// drain at LIMIT (+OFFSET) successfully-locked rows so the row lock claims
	// only as many rows as the LIMIT demands — skipped (contended) rows do not
	// count toward the cap (see drainAndStamp). Evaluated here exactly like
	// limitOp.Open. nil LimitCount → unbounded drain (existing behaviour, and
	// the EXISTS maxDrain=1 set by existsImpl is preserved). M0118-0003.
	if o.plan.LimitCount != nil {
		v, err := evalExpr(o.plan.LimitCount, nil, ctx)
		if err != nil {
			return err
		}
		if !v.IsNull() && v.Kind == KindInt && v.Int >= 0 {
			drain := v.Int
			if o.plan.OffsetCount != nil {
				ov, oerr := evalExpr(o.plan.OffsetCount, nil, ctx)
				if oerr != nil {
					return oerr
				}
				if !ov.IsNull() && ov.Kind == KindInt && ov.Int > 0 {
					drain += ov.Int
				}
			}
			o.maxDrain = int(drain)
		}
	}
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
		if newLockedCols, err := o.refetchRow(entry.rel, entry.newPtr); err != nil {
			return nil, err
		} else if newLockedCols != nil {
			// Merge the re-fetched locked-table values into the full join row
			// for every LockedRel that references the same physical relation.
			// Using lk.ColOffset (set by the planner from rangeBinding.offset)
			// handles two important cases:
			//   1. Self-joins: same table at multiple offsets (e.g. jointest a
			//      at 0 and jointest b at 2) — both ranges are updated.
			//   2. Non-leftmost locked table: e.g. FOR UPDATE OF jt where jt
			//      columns are at offset 4, not 0.
			merged := cloneRow(entry.row)
			applied := false
			for _, lk := range o.plan.Locks {
				if lk.Table == nil {
					continue
				}
				if o.ctx.Catalog.RelFileNode(lk.Table) != entry.rel {
					continue
				}
				for i, v := range newLockedCols {
					pos := lk.ColOffset + i
					if pos < len(merged) {
						merged[pos] = v
					}
				}
				applied = true
			}
			if !applied {
				// Fallback when no LockedRel matched (should not happen for
				// well-formed plans; assume offset 0 for safety).
				for i, v := range newLockedCols {
					if i < len(merged) {
						merged[i] = v
					}
				}
			}
			ms := SlotFromRow(o.Schema(), merged)
			ms.hasCTID = true
			ms.ctidBlock = uint32(entry.newPtr.Block)
			ms.ctidOff = entry.newPtr.Offset
			return ms, nil
		}
	}
	ms := SlotFromRow(o.Schema(), entry.row)
	if entry.haveTID {
		ms.hasCTID = true
		ms.ctidBlock = uint32(entry.ptr.Block)
		ms.ctidOff = entry.ptr.Offset
	}
	return ms, nil
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
	successCount := 0
	for {
		// Stop once we have acquired enough successfully locked rows.
		if o.maxDrain > 0 && successCount >= o.maxDrain {
			_ = o.child.Close()
			break
		}
		slot, err := o.child.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return err
		}
		// Materialize at retention boundary: rows may be held until
		// stampLock completes (potentially blocking on a concurrent xmax).
		ms := slot.Materialize()
		row := ms.Row()
		entry := pendingLockedRow{row: row}
		if o.scan != nil {
			if rel, ptr, ok := o.scan.currentTID(); ok {
				entry.rel = rel
				entry.ptr = ptr
				entry.haveTID = true
			}
		}
		// Fallback: ctid embedded in slot by eager NL join operator when the
		// scan was drained and closed during Open() (M0100-0010).
		if !entry.haveTID && ms.hasCTID && len(o.plan.Locks) > 0 && o.plan.Locks[0].Table != nil {
			entry.rel = o.ctx.Catalog.RelFileNode(o.plan.Locks[0].Table)
			entry.ptr = storage.ItemPointer{Block: storage.BlockNumber(ms.ctidBlock), Offset: ms.ctidOff}
			entry.haveTID = true
		}
		if entry.haveTID {
			successor, followed, epqSkipped, err := o.stampLock(entry.rel, entry.ptr)
			if err != nil {
				return err
			}
			if epqSkipped {
				// EPQ recheck rejected the updated row — the row no longer
				// satisfies the WHERE predicate. Do not yield it; continue
				// draining the child scan for more qualifying candidates.
				// This matches PG's EvalPlanQualFetchRowMark behaviour where
				// a failed recheck causes the scan to advance to the next row.
				continue
			}
			if followed && successor != (storage.ItemPointer{}) {
				entry.newPtr = successor
				entry.newPtrValid = true
			}
		}
		o.pending = append(o.pending, entry)
		successCount++
	}
	return nil
}

// stampLock acquires a tuple-level lock and stamps the lock-only xmax on the
// heap tuple at ptr. Returns (successorPtr, followed, epqSkipped, err):
//   - followed=false: stamped at ptr (or nothing stamped for dead-end cases)
//   - followed=true: followed a committed-update CTID chain; successorPtr is the
//     live tuple that was stamped. Caller should update entry.newPtr.
//   - epqSkipped=true: EPQ recheck filter rejected the row; caller must not
//     yield this row and should continue draining for more candidates.
//
// When a real non-lock-only xmax from another xact is present:
//   - If the xmax is still in-progress: waits for it (produces <waiting ...>
//     in isolation tests) and then checks the final state.
//   - If the xmax committed: follows the CTID chain to the live successor.
//   - If the xmax aborted: the row is live; re-stamps at original ptr.
func (o *lockRowsOp) stampLock(rel storage.RelFileNode, ptr storage.ItemPointer) (storage.ItemPointer, bool, bool, error) {
	// M0093: SELECT FOR UPDATE/SHARE stamps lock-only xmax with the
	// transaction's XID; materialise it BEFORE acquiring the tuple
	// lock so the lock holder's identity is the real XID (mismatched
	// holder identity breaks UPDATE's blocks-on-foreign-lock check).
	if err := o.ctx.MaterializeWriterXID(); err != nil {
		return storage.ItemPointer{}, false, false, err
	}
	// Acquire the tuple-level lock first so a concurrent UPDATE
	// that races with us can't slip through between the xmax
	// stamp and the lock registration. The wait policy decides how
	// contention is handled: NOWAIT fails fast with 55P03, mirroring
	// heapam.c's "could not obtain lock on row in relation \"%s\""
	// (relation-qualified, ERRCODE_LOCK_NOT_AVAILABLE); the default
	// blocks until the conflicting holder releases. The lockmgr tuple
	// tag is the cross-session gate — a concurrent FOR UPDATE holder
	// makes TryAcquire return ErrLockNotAvailable. M0118-0003.
	switch o.waitPolicy {
	case planner.LockWaitNoWait:
		if err := o.ctx.tryAcquireTupleLock(rel, ptr, o.tupleLockMode()); err != nil {
			if ee, ok := err.(*ExecError); ok && ee.Code == "55P03" {
				return storage.ItemPointer{}, false, false, &ExecError{
					Code:    "55P03",
					Pos:     o.plan.Pos(),
					Message: fmt.Sprintf(`could not obtain lock on row in relation "%s"`, o.lockRelName),
				}
			}
			return storage.ItemPointer{}, false, false, err
		}
	case planner.LockWaitSkipLocked:
		// SKIP LOCKED: try the tuple lock once. If a concurrent session holds
		// it (it is mid-statement on this row), skip the row silently rather
		// than blocking — mirrors heap_lock_tuple returning HeapTupleWouldBlock
		// under LockWaitSkip. Cross-statement conflicts are detected later from
		// the persisted lock-only xmax in stampLockInner (goopg's lockmgr tuple
		// lock is statement-scoped, so a committed-but-active holder's lock has
		// already been released here but its heap xmax persists). M0118-0003.
		if err := o.ctx.tryAcquireTupleLock(rel, ptr, o.tupleLockMode()); err != nil {
			if ee, ok := err.(*ExecError); ok && ee.Code == "55P03" {
				return storage.ItemPointer{}, false, true, nil // epqSkipped: drop contended row
			}
			return storage.ItemPointer{}, false, false, err
		}
	default:
		if err := o.ctx.acquireTupleLock(rel, ptr, o.tupleLockMode()); err != nil {
			return storage.ItemPointer{}, false, false, err
		}
	}
	return o.stampLockInner(rel, ptr, 0)
}

// stampLockInner is the recursive inner loop for stampLock, bounded by depth
// to prevent infinite chains. depth=0 on first call.
func (o *lockRowsOp) stampLockInner(rel storage.RelFileNode, ptr storage.ItemPointer, depth int) (storage.ItemPointer, bool, bool, error) {
	if depth > 16 {
		return storage.ItemPointer{}, false, false, nil // chain too deep
	}
	slot, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: ptr.Block})
	if err != nil {
		return storage.ItemPointer{}, false, false, err
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
			// Non-key update + FOR (KEY) SHARE: the two locks do not conflict
			// (AccessShareLock vs ExclusiveLock). Rather than silently dropping
			// our lock — M0100-0005f skipped here because the single-holder xmax
			// had nowhere to record a second holder — combine our share locker
			// with the in-progress/committed no-key updater into a MultiXactId
			// that records BOTH (the updater-bearing MultiXact producer,
			// M0118-0003). The combined set has an updater, so HintBits clears
			// HEAP_XMAX_LOCK_ONLY: unlike the lock-only case this multi is NOT
			// transparent to visibility, which is why every read / wait-on-
			// deleter consumer was made multixact-aware first (producer gate,
			// docs/design/0118-0002).
			if o.ctx.MultiXact != nil && o.ctx.TxnMgr != nil {
				multiPtr, formed, merr := o.stampMultiUpdaterLock(slot, ptr, tup.Header, keysUpdated)
				if merr != nil {
					slot.Unlock()
					o.ctx.Pool.Unpin(slot)
					return storage.ItemPointer{}, false, false, merr
				}
				if formed {
					slot.Unlock()
					o.ctx.Pool.Unpin(slot)
					return multiPtr, false, false, nil
				}
				// No surviving holder to combine with (updater gone/aborted):
				// fall through to preserve the M0100-0005f skip below.
			}
			// Preserve M0100-0005f: do not overwrite the real updater's xmax.
			slot.Unlock()
			o.ctx.Pool.Unpin(slot)
			return storage.ItemPointer{}, false, false, nil
		}

		xmax := tup.Header.Xmax
		ctid := tup.Header.CTID
		slot.Unlock()
		o.ctx.Pool.Unpin(slot)

		// Wait for in-progress updater to commit or abort.
		isActive := o.ctx.TxnMgr != nil && o.ctx.TxnMgr.IsXIDActive(xmax)
		if isActive {
			qctx := o.ctx.Ctx
			if qctx == nil {
				qctx = context.Background()
			}
			if werr := o.ctx.TxnMgr.WaitForXID(qctx, xmax); werr != nil {
				// Context cancelled — skip this row silently.
				return storage.ItemPointer{}, false, false, nil
			}
		}

		if o.ctx.TxnMgr != nil && o.ctx.TxnMgr.HasAbortedXID(xmax) {
			// Updater rolled back: row is live at original ptr. Stamp it.
			ptr2, followed2, err2 := o.stampAtPtr(rel, ptr)
			return ptr2, followed2, false, err2
		}

		// Updater committed: under RR/SER raise a serialization error.
		// Under RC: follow CTID chain to find the live successor.
		if o.ctx.Tx.Isolation != mvcc.IsolationReadCommitted {
			return storage.ItemPointer{}, false, false, &ExecError{
				Code:    "40001",
				Message: "could not serialize access due to concurrent update",
			}
		}
		next := ctid
		if next.Block == ptr.Block && next.Offset == ptr.Offset {
			// CTID points to self — deleted row, no live successor.
			return storage.ItemPointer{}, false, false, nil
		}
		// Acquire lockmgr lock on the successor before reading it.
		if err := o.ctx.acquireTupleLock(rel, next, o.tupleLockMode()); err != nil {
			return storage.ItemPointer{}, false, false, err
		}
		succ, _, succEPQSkipped, err := o.stampLockInner(rel, next, depth+1)
		if err != nil {
			return storage.ItemPointer{}, false, false, err
		}
		if succ == (storage.ItemPointer{}) {
			// Propagate epqSkipped from recursive call so callers further
			// up the chain know the row was rejected by EPQ, not just deleted.
			return storage.ItemPointer{}, false, succEPQSkipped, nil
		}
		// EPQ re-eval: re-evaluate filter predicate against the live successor
		// row, matching PostgreSQL's EvalPlanQualFetchRowMark behaviour. This
		// fires any side-effecting quals (e.g. noisy_oper NOTICEs) against the
		// updated row values. If the filter no longer passes, skip the row and
		// signal the caller to continue draining for more candidates.
		if o.filterPred != nil && len(o.filterCols) > 0 {
			if skip := o.epqRecheckFilter(rel, succ); skip {
				return storage.ItemPointer{}, false, true, nil // epqSkipped
			}
		}
		// Indicate that the entry's row data should be refetched.
		return succ, true, false, nil
	}

	// Another transaction holds a row-level *lock* (lock-only xmax) on this
	// tuple. Unlike a real updater (handled above) the row is not relocated,
	// but a conflicting request must still honour the holder rather than
	// silently overwriting its xmax. The lockmgr tuple lock is statement-scoped
	// (released at each Query message's end in dispatch.go), so cross-statement
	// conflict detection relies on this persisted lock-only xmax — the same
	// durable signal the real-updater path reads above.
	if gerr == nil &&
		tup.Header.Xmax != storage.InvalidTransactionID &&
		storage.IsHeapTupleLockOnly(tup.Header.Infomask) &&
		tup.Header.Xmax != o.ctx.Tx.XID &&
		tupleLockConflicts(o.lockMemberStatus(), tup.Header.Infomask, tup.Header.Infomask2) {

		// Conflict resolution honours the wait policy (M0118-0003):
		//   SKIP LOCKED -> drop the contended row silently.
		//   NOWAIT      -> fail fast with 55P03 (relation-qualified).
		//   blocking    -> wait for the holder to commit/abort, then retry
		//     from the top. The holder may have committed its lock (released,
		//     so we stamp our own), aborted (row live again), or upgraded to a
		//     real update (the real-updater branch above takes over on retry).
		//     Mirrors that branch's WaitForXID so the isolation scheduler sees
		//     the step block (<waiting ...>) and resume on the holder's COMMIT.
		// When the conflicting xmax is a MultiXactId (HEAP_XMAX_IS_MULTI), the
		// holders are resolved through the shared member store — the raw value
		// must NOT be passed to IsXIDActive / WaitForXID, which expect a single
		// TransactionID from a different numbering space. M0118-0003.
		active := o.activeLockHolders(tup.Header.Xmax, tup.Header.Infomask)
		if len(active) > 0 {
			slot.Unlock()
			o.ctx.Pool.Unpin(slot)
			switch o.waitPolicy {
			case planner.LockWaitSkipLocked:
				// SKIP LOCKED: silently drop the contended row.
				return storage.ItemPointer{}, false, true, nil // epqSkipped
			case planner.LockWaitNoWait:
				return storage.ItemPointer{}, false, false, &ExecError{
					Code:    "55P03",
					Pos:     o.plan.Pos(),
					Message: fmt.Sprintf(`could not obtain lock on row in relation "%s"`, o.lockRelName),
				}
			default:
				// Blocking FOR UPDATE/FOR SHARE: wait for every still-active
				// holder to finish (a multi may name several), then re-evaluate
				// the tuple from scratch.
				qctx := o.ctx.Ctx
				if qctx == nil {
					qctx = context.Background()
				}
				for _, hx := range active {
					if werr := o.ctx.TxnMgr.WaitForXID(qctx, hx); werr != nil {
						// Context cancelled — skip this row silently.
						return storage.ItemPointer{}, false, false, nil
					}
				}
				return o.stampLockInner(rel, ptr, depth+1)
			}
		}
		// No holder still active (committed/aborted): the row lock(s) are
		// released, so fall through and stamp our own lock-only xmax.
	}

	// Tuple is live (no real updater xmax from another xact). Stamp it.
	if gerr != nil {
		slot.Unlock()
		o.ctx.Pool.Unpin(slot)
		return storage.ItemPointer{}, false, false, nil
	}
	// MultiXact producer (M0118-0003): if the tuple already carries a lock-only
	// xmax from another transaction that does NOT conflict with our request
	// (e.g. a second FOR SHARE holder), combine the holders into a MultiXactId
	// rather than overwriting the existing holder's xmax. Reaching here with a
	// foreign lock-only xmax implies tupleLockConflicts was false (a conflicting
	// holder is handled by the wait/skip/nowait branch above), so the combined
	// member set is lock-only and stays transparent to visibility — HintBits
	// sets HEAP_XMAX_LOCK_ONLY whenever there is no updater member.
	if o.ctx.MultiXact != nil &&
		tup.Header.Xmax != storage.InvalidTransactionID &&
		storage.IsHeapTupleLockOnly(tup.Header.Infomask) &&
		tup.Header.Xmax != o.ctx.Tx.XID {
		multiPtr, combined, merr := o.stampMultiLock(slot, ptr, tup.Header)
		if merr != nil {
			slot.Unlock()
			o.ctx.Pool.Unpin(slot)
			return storage.ItemPointer{}, false, false, merr
		}
		if combined {
			slot.Unlock()
			o.ctx.Pool.Unpin(slot)
			return multiPtr, false, false, nil
		}
		// No surviving active holder to combine with — fall through to the
		// single-holder stamp below.
	}
	if err := storage.PageSetHeapTupleLockOnly(slot.Page(), ptr.Offset, o.ctx.Tx.XID, o.lockStrength); err != nil {
		slot.Unlock()
		o.ctx.Pool.Unpin(slot)
		return storage.ItemPointer{}, false, false, err
	}
	// FOR UPDATE reserves the key (HEAP_KEYS_UPDATED) so a later FOR KEY SHARE
	// holder decodes it as StatusForUpdate and conflicts; the weaker strengths
	// clear any stale bit from a prior FOR UPDATE lock on this line pointer.
	if err := storage.PageSetHeapTupleLockKeysUpdated(slot.Page(), ptr.Offset, o.lockKeysUpdated); err != nil {
		slot.Unlock()
		o.ctx.Pool.Unpin(slot)
		return storage.ItemPointer{}, false, false, err
	}
	derr := markHeapLockDirty(o.ctx.Pool, slot, rel, ptr.Block, ptr.Offset, o.ctx.Tx.XID, o.lockStrength)
	slot.Unlock()
	o.ctx.Pool.Unpin(slot)
	return ptr, false, false, derr
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
	if gerr == nil && o.anotherRealUpdaterArrived(tup.Header) {
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
	if err := storage.PageSetHeapTupleLockKeysUpdated(slot.Page(), ptr.Offset, o.lockKeysUpdated); err != nil {
		slot.Unlock()
		o.ctx.Pool.Unpin(slot)
		return storage.ItemPointer{}, false, err
	}
	derr := markHeapLockDirty(o.ctx.Pool, slot, rel, ptr.Block, ptr.Offset, o.ctx.Tx.XID, o.lockStrength)
	slot.Unlock()
	o.ctx.Pool.Unpin(slot)
	return ptr, false, derr
}

// anotherRealUpdaterArrived reports whether the tuple now carries a non-self
// updater stamp that landed while we waited (used to skip re-stamping a row
// another writer has since updated/deleted). A lock-only xmax — including a
// lock-only multixact — is not an updater and is ignored. For an
// updater-bearing multixact, t_xmax is a MultiXactId: resolve its updater
// member rather than comparing the MultiXactId to our own XID.
func (o *lockRowsOp) anotherRealUpdaterArrived(h storage.HeapTupleHeader) bool {
	if h.Xmax == storage.InvalidTransactionID || storage.IsHeapTupleLockOnly(h.Infomask) {
		return false
	}
	effXmax := h.Xmax
	if storage.IsHeapTupleXmaxMulti(h.Infomask) {
		effXmax = multixactUpdaterXID(o.ctx.MultiXact, h.Xmax)
		if effXmax == storage.InvalidTransactionID {
			// Unresolvable or locker-only multi — not a real updater.
			return false
		}
	}
	return effXmax != o.ctx.Tx.XID
}

// epqRecheckFilter reads the heap tuple at (rel, ptr), decodes it using
// o.filterCols, and re-evaluates o.filterPred. Returns true (skip) when the
// predicate fails or the tuple cannot be read. Returns false (keep) when the
// predicate passes — side-effecting quals fire as a result.
func (o *lockRowsOp) epqRecheckFilter(rel storage.RelFileNode, ptr storage.ItemPointer) (skip bool) {
	slot, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: ptr.Block})
	if err != nil {
		return true
	}
	slot.RLock()
	tup, gerr := storage.PageGetHeapTuple(slot.Page(), ptr.Offset)
	slot.RUnlock()
	o.ctx.Pool.Unpin(slot)
	if gerr != nil {
		return true
	}
	row, decErr := DecodeHeapTupleRow(o.filterCols, tup, nil)
	if decErr != nil {
		return true
	}
	pv, perr := evalExpr(o.filterPred, row, o.ctx)
	if perr != nil || pv.IsNull() || pv.Kind != KindBool || !pv.BoolValue() {
		return true
	}
	return false
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

// tupleLockConflicts reports whether a newly requested row-lock strength
// conflicts with the strength already recorded in a lock-only xmax infomask.
// goopg distinguishes two effective strengths: FOR UPDATE → HeapXmaxExclLock
// (pure exclusive bit) and FOR SHARE / KEY SHARE → HeapXmaxShrLock (both bits).
// FOR UPDATE conflicts with any held row lock; a shared request conflicts only
// with an exclusive (FOR UPDATE) holder. This mirrors the relevant rows of
// PostgreSQL's row-lock conflict table for the single-locker (non-multixact)
// case. M0118-0003.
// tupleLockConflicts reports whether a new row-lock request of member status
// `reqStatus` conflicts with the lock-only holder currently recorded in a
// tuple's `heldInfomask` / `heldInfomask2`. It decodes the holder's strength
// (lockOnlyMemberStatus) and defers to multixact.StatusesConflict — the
// verbatim port of upstream's row-lock compatibility matrix — so the full
// four-way semantics hold: e.g. a FOR KEY SHARE request does NOT conflict with
// a held FOR NO KEY UPDATE lock, while FOR SHARE does. M0118-0003.
func tupleLockConflicts(reqStatus multixact.Status, heldInfomask, heldInfomask2 uint16) bool {
	if heldInfomask&storage.HeapXmaxLockMask == 0 {
		return false
	}
	return multixact.StatusesConflict(lockOnlyMemberStatus(heldInfomask, heldInfomask2), reqStatus)
}

// lockOnlyMemberStatus maps a lock-only holder's recorded infomask lock-mode
// bits back to the multixact member Status used when combining holders into a
// MultiXactId. goopg stamps only two effective strengths (FOR SHARE / KEY SHARE
// → HeapXmaxShrLock; FOR UPDATE / NO KEY UPDATE → HeapXmaxExclLock), so the
// reverse mapping is exact for the values goopg actually produces:
//   - HeapXmaxShrLock    → ForShare       (a pure shared lock)
//   - HeapXmaxKeyShrLock → ForKeyShare    (FK key-share lock)
//   - HeapXmaxExclLock   → ForNoKeyUpdate (a lock-only exclusive row lock)
//
// The default falls back to ForKeyShare (the weakest), which can never
// over-state a holder's strength. M0118-0003.
// lockOnlyMemberStatus maps a lock-only holder's recorded infomask /
// infomask2 lock-mode bits back to the four-way multixact member Status used
// when combining holders into a MultiXactId or testing tuple-lock conflicts.
// The reverse mapping is exact for every strength goopg stamps (M0118-0003):
//   - HeapXmaxKeyShrLock              → ForKeyShare    (FOR KEY SHARE)
//   - HeapXmaxShrLock                 → ForShare       (FOR SHARE)
//   - HeapXmaxExclLock, KEYS_UPDATED  → ForUpdate      (FOR UPDATE)
//   - HeapXmaxExclLock, no KEYS_UPDATED → ForNoKeyUpdate (FOR NO KEY UPDATE)
//
// The default falls back to ForKeyShare (the weakest), which can never
// over-state a holder's strength.
func lockOnlyMemberStatus(infomask, infomask2 uint16) multixact.Status {
	switch infomask & storage.HeapXmaxLockMask {
	case storage.HeapXmaxShrLock:
		return multixact.StatusForShare
	case storage.HeapXmaxExclLock:
		if infomask2&storage.HeapKeysUpdated != 0 {
			return multixact.StatusForUpdate
		}
		return multixact.StatusForNoKeyUpdate
	case storage.HeapXmaxKeyShrLock:
		return multixact.StatusForKeyShare
	default:
		return multixact.StatusForKeyShare
	}
}

// updaterMemberStatus maps a single-xid (non-multi) updater xmax to its
// MultiXactStatus, read from the tuple's HEAP_KEYS_UPDATED bit: a key-changing
// UPDATE/DELETE is StatusUpdate, a no-key UPDATE is StatusNoKeyUpdate. The xmax
// reaching the updater-bearing producer is non-lock-only — i.e. an actual
// update — so it is never a FOR-lock status. M0118-0003.
func updaterMemberStatus(keysUpdated bool) multixact.Status {
	if keysUpdated {
		return multixact.StatusUpdate
	}
	return multixact.StatusNoKeyUpdate
}

// lockMemberStatus maps this lockRowsOp's requested strength to the multixact
// member Status it records when it joins (or forms) a MultiXact on a tuple.
// goopg's two effective strengths map to the corresponding pure-lock statuses.
// lockMemberStatus maps this lockRowsOp's requested four-way strength to the
// multixact member Status it records when it joins (or forms) a MultiXact on a
// tuple. The decode twin is lockOnlyMemberStatus (the bits this writes are read
// back there). M0118-0003.
func (o *lockRowsOp) lockMemberStatus() multixact.Status {
	switch o.lockStrength & storage.HeapXmaxLockMask {
	case storage.HeapXmaxKeyShrLock:
		return multixact.StatusForKeyShare
	case storage.HeapXmaxShrLock:
		return multixact.StatusForShare
	case storage.HeapXmaxExclLock:
		if o.lockKeysUpdated {
			return multixact.StatusForUpdate
		}
		return multixact.StatusForNoKeyUpdate
	}
	return multixact.StatusForKeyShare
}

// activeLockHolders returns the still-active transactions holding a row lock on
// a tuple whose xmax is `xmax` with hint bits `infomask`. For a single-xid
// lock-only xmax it is at most the one holder; for a MultiXactId xmax
// (HEAP_XMAX_IS_MULTI) it is the subset of members still in progress, resolved
// through the shared member store. The current transaction is never included
// (it never blocks on its own lock). A multi whose membership cannot be
// resolved (no store wired, or members not found after a restart) is treated as
// released rather than hanging the waiter. M0118-0003.
func (o *lockRowsOp) activeLockHolders(xmax storage.TransactionID, infomask uint16) []storage.TransactionID {
	if o.ctx.TxnMgr == nil {
		return nil
	}
	if storage.IsHeapTupleXmaxMulti(infomask) {
		if o.ctx.MultiXact == nil {
			return nil
		}
		members, ok := o.ctx.MultiXact.Members(multixact.MultiXactId(xmax))
		if !ok {
			return nil
		}
		var out []storage.TransactionID
		for _, m := range members {
			if m.Xid != o.ctx.Tx.XID && o.ctx.TxnMgr.IsXIDActive(m.Xid) {
				out = append(out, m.Xid)
			}
		}
		return out
	}
	if xmax != o.ctx.Tx.XID && o.ctx.TxnMgr.IsXIDActive(xmax) {
		return []storage.TransactionID{xmax}
	}
	return nil
}

// stampMultiLock combines this lock request with the existing active lock
// holder(s) recorded in `hdr` into a MultiXactId stamped on the tuple. It is
// the MultiXact producer half of the row-lock path (the consumer half is
// activeLockHolders + the wait/skip/nowait branch). The caller holds slot.Lock
// and retains ownership of unlock/unpin.
//
// Returns combined=true when it has stamped a multi xmax (the caller must not
// also stamp a single xmax); combined=false when there is no surviving active
// holder to combine with, in which case the caller falls back to the
// single-holder stamp. M0118-0003.
func (o *lockRowsOp) stampMultiLock(slot *storage.Slot, ptr storage.ItemPointer, hdr storage.HeapTupleHeader) (storage.ItemPointer, bool, error) {
	// Enumerate the existing holders. The producer is gated on a lock-only
	// foreign xmax, so every existing member is a pure lock holder.
	var existing []multixact.Member
	if storage.IsHeapTupleXmaxMulti(hdr.Infomask) {
		members, ok := o.ctx.MultiXact.Members(multixact.MultiXactId(hdr.Xmax))
		if !ok {
			// Membership lost (e.g. stamped before a restart with no persisted
			// store): nothing live to combine with; stamp a single xmax.
			return storage.ItemPointer{}, false, nil
		}
		existing = members
	} else {
		existing = []multixact.Member{{Xid: hdr.Xmax, Status: lockOnlyMemberStatus(hdr.Infomask, hdr.Infomask2)}}
	}

	// Keep only members whose transaction is still in progress — a dead
	// locker's lock is released and must not be carried forward. (Committed-
	// updater retention is part of the deferred updater-bearing multi producer.)
	survivors := make([]multixact.Member, 0, len(existing)+1)
	for _, m := range existing {
		if m.Xid == o.ctx.Tx.XID {
			continue // re-added once below with our current strength
		}
		if o.ctx.TxnMgr != nil && o.ctx.TxnMgr.IsXIDActive(m.Xid) {
			survivors = append(survivors, m)
		}
	}
	if len(survivors) == 0 {
		return storage.ItemPointer{}, false, nil
	}
	survivors = append(survivors, multixact.Member{Xid: o.ctx.Tx.XID, Status: o.lockMemberStatus()})

	multi, err := o.ctx.MultiXact.CreateFromMembers(survivors)
	if err != nil {
		return storage.ItemPointer{}, false, err
	}
	infomask, infomask2 := multixact.HintBits(survivors)
	if err := storage.PageSetHeapTupleXmaxMulti(slot.Page(), ptr.Offset, storage.TransactionID(multi), infomask, infomask2); err != nil {
		return storage.ItemPointer{}, false, err
	}
	// MultiXact membership is process-shared in-memory state, not yet persisted
	// through the heap-lock WAL record (which carries a single xid + strength, so
	// logging one record for the strongest holder would mis-describe the multi on
	// replay). Mark the page dirty without a logical lock record. Lock-only
	// multixact state is transient — the holders' transactions do not survive a
	// crash — so losing it on recovery is correct. WAL persistence of multixact
	// members is deferred (see docs/design/0118-0002). M0118-0003.
	o.ctx.Pool.MarkDirty(slot)
	return ptr, true, nil
}

// stampMultiUpdaterLock is the updater-bearing MultiXact producer
// (M0118-0003) — the twin of stampMultiLock for branch (a) of stampLockInner,
// where the tuple carries a real (non-lock-only) in-progress or committed
// no-key updater xmax and our request is a shared (FOR KEY SHARE / FOR SHARE)
// lock. FOR KEY SHARE does not conflict with a no-key update, so instead of
// silently dropping our lock (the M0100-0005f skip) we combine our share
// locker with the updater into a MultiXactId that names BOTH holders.
//
// Unlike stampMultiLock the resulting member set has an updater, so HintBits
// clears HEAP_XMAX_LOCK_ONLY: the multi is updater-bearing and therefore NOT
// transparent to MVCC visibility — every read / wait-on-deleter consumer was
// made multixact-aware first (the producer gate, docs/design/0118-0002).
//
// Member retention mirrors MultiXactIdExpand (see multixact.Store.Expand):
// keep every still-in-progress holder plus a COMMITTED updater (committed ==
// not active AND not aborted — the committed no-key update is preserved so a
// later key-share / visibility consumer still resolves it), and drop dead pure
// lockers and an aborted updater. Returns (ptr, true, nil) when a multi was
// stamped; (zero, false, nil) when no holder survived to combine with (e.g.
// the updater aborted) so the caller preserves the M0100-0005f skip.
//
// goopg collapses FOR KEY SHARE and FOR SHARE to a single ShrLock strength, so
// our member is recorded as lockMemberStatus() (StatusForShare); the faithful
// 4-way FOR KEY SHARE distinction is deferred (docs/design/0118-0002 resume 3).
// HintBits is unaffected — the updater's strength dominates the lock-strength
// bits and neither share status reserves the key.
func (o *lockRowsOp) stampMultiUpdaterLock(slot *storage.Slot, ptr storage.ItemPointer, hdr storage.HeapTupleHeader, keysUpdated bool) (storage.ItemPointer, bool, error) {
	// Enumerate the existing holders. The producer is gated (branch (a)) on a
	// non-lock-only foreign xmax, so the set always contains an updater — either
	// a single-xid updater or an already updater-bearing multi.
	var existing []multixact.Member
	if storage.IsHeapTupleXmaxMulti(hdr.Infomask) {
		members, ok := o.ctx.MultiXact.Members(multixact.MultiXactId(hdr.Xmax))
		if !ok {
			// Membership lost (e.g. stamped before a restart with no persisted
			// store): nothing resolvable to combine with; keep the single xmax.
			return storage.ItemPointer{}, false, nil
		}
		existing = members
	} else {
		existing = []multixact.Member{{Xid: hdr.Xmax, Status: updaterMemberStatus(keysUpdated)}}
	}

	// Keep members still of interest (MultiXactIdExpand, multixact.c:561):
	// in-progress holders, plus a committed updater. Drop dead pure lockers and
	// an aborted updater.
	survivors := make([]multixact.Member, 0, len(existing)+1)
	for _, m := range existing {
		if m.Xid == o.ctx.Tx.XID {
			continue // re-added once below at our current strength
		}
		switch {
		case o.ctx.TxnMgr.IsXIDActive(m.Xid):
			survivors = append(survivors, m)
		case m.Status.IsUpdate() && !o.ctx.TxnMgr.HasAbortedXID(m.Xid):
			// Committed updater (not active AND not aborted): retained.
			survivors = append(survivors, m)
		}
	}
	if len(survivors) == 0 {
		// No live holder and no committed updater survived (e.g. the updater
		// aborted): nothing to combine with — caller preserves the skip.
		return storage.ItemPointer{}, false, nil
	}
	survivors = append(survivors, multixact.Member{Xid: o.ctx.Tx.XID, Status: o.lockMemberStatus()})

	multi, err := o.ctx.MultiXact.CreateFromMembers(survivors)
	if err != nil {
		return storage.ItemPointer{}, false, err
	}
	infomask, infomask2 := multixact.HintBits(survivors)
	if err := storage.PageSetHeapTupleXmaxMulti(slot.Page(), ptr.Offset, storage.TransactionID(multi), infomask, infomask2); err != nil {
		return storage.ItemPointer{}, false, err
	}
	// Same WAL-persistence caveat as stampMultiLock: membership is in-memory
	// process-shared state, marked dirty without a logical heap-lock record (the
	// record carries a single xid + strength and cannot describe a multi). WAL
	// persistence of multixact members is deferred (docs/design/0118-0002).
	o.ctx.Pool.MarkDirty(slot)
	return ptr, true, nil
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
