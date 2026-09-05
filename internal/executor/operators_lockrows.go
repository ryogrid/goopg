package executor

import (
	"context"
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage/lmgr"
	"github.com/goopg/goopg/internal/access/transam/multixact"
	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/storage"
)

// rowWaitTimeoutError maps a WaitForXID error that must abort the statement —
// the session's lock_timeout (lockwait.ErrLockTimeout) or statement_timeout
// (context.DeadlineExceeded) — to its SQLSTATE-57014 ExecError, tagged with
// the lock op's position. A plain client cancellation (context.Canceled)
// returns nil: the long-standing row-lock behaviour is to give up on the row
// silently when the connection goes away, so only the explicit timeouts
// surface to the client. M0118-0009 (timeouts).
func (o *lockRowsOp) rowWaitTimeoutError(werr error) *ExecError {
	ee := lockWaitTimeoutError(werr)
	if ee != nil {
		ee.Pos = o.plan.Pos()
	}
	return ee
}

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
	plan  *optimizer.LockRows
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
	filterPred optimizer.Expr
	filterCols []catalog.Column

	// waitPolicy is the effective NOWAIT / SKIP LOCKED / (default) blocking
	// policy for the per-tuple lock acquisition in stampLock. Resolved once at
	// Open from Locks[0].WaitPolicy (v0 assumes a single policy per LockRows;
	// multi-clause merge is deferred, consistent with lockStrength). M0118-0003.
	waitPolicy optimizer.LockWaitPolicy
	// lockRelName is the name of the first locked relation, used to format the
	// NOWAIT "could not obtain lock on row in relation \"%s\"" diagnostic with
	// the upstream-canonical relation name. Resolved at Open from Locks[0].
	lockRelName string

	// pgClassRowMarkOID / pgClassRowMarkXID record the explicit pg_class rowmark
	// this LockRows took up front in maybeRecordPgClassRowMark (so a concurrent
	// GRANT/REVOKE blocks behind it during the wait). If the post-wait scan then
	// yields no row — the relation's pg_class tuple was concurrently DELETEd and
	// committed — PG holds no tuple lock, so we retract the mark immediately
	// (ClearPgClassRowMark) and a peer waiting behind it proceeds without waiting
	// for our transaction to end. Design 0118-0117 (intra-grant-inplace perm 10).
	pgClassRowMarkOID uint32
	pgClassRowMarkXID uint32
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

func newLockRowsOp(p *optimizer.LockRows, child Operator) *lockRowsOp {
	return &lockRowsOp{plan: p, child: child}
}

// findFilterPred walks the child chain past Project wrappers and returns the
// predicate of the first filterOp found. Returns nil when no filter is present.
func findFilterPred(op Operator) optimizer.Expr {
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
func filterPredMaxColRef(expr optimizer.Expr) int {
	max := -1
	var walk func(optimizer.Expr)
	walk = func(e optimizer.Expr) {
		if e == nil {
			return
		}
		if cr, ok := e.(*optimizer.ColumnRef); ok {
			if cr.Index > max {
				max = cr.Index
			}
			return
		}
		switch x := e.(type) {
		case *optimizer.BinaryOp:
			walk(x.Left)
			walk(x.Right)
		case *optimizer.UnaryOp:
			walk(x.Operand)
		case *optimizer.CastExpr:
			walk(x.Operand)
		case *optimizer.IsNullExpr:
			walk(x.Operand)
		case *optimizer.IsBoolExpr:
			walk(x.Operand)
		case *optimizer.IsDistinctFromExpr:
			walk(x.Left)
			walk(x.Right)
		case *optimizer.FuncCall:
			for _, a := range x.Args {
				walk(a)
			}
		case *optimizer.CaseExpr:
			walk(x.Operand)
			for _, w := range x.Whens {
				walk(w.When)
				walk(w.Then)
			}
			walk(x.Else)
		case *optimizer.InExpr:
			walk(x.Operand)
			for _, v := range x.List {
				walk(v)
			}
		}
	}
	walk(expr)
	return max
}

// exprRefsColumnOrOuter reports whether expr references any column of a row
// (ColumnRef) or an outer/correlated input (OuterColumnRef). An index scan's
// key expression that satisfies this is a join/correlated lookup key rather
// than a row-local constant, so it must not be folded into the per-row EPQ
// recheck filter (its column indices live in a different coordinate space than
// the locked table's own columns). M0118-0009.
func exprRefsColumnOrOuter(expr optimizer.Expr) bool {
	found := false
	var walk func(optimizer.Expr)
	walk = func(e optimizer.Expr) {
		if e == nil || found {
			return
		}
		switch x := e.(type) {
		case *optimizer.ColumnRef:
			found = true
		case *optimizer.OuterColumnRef:
			found = true
		case *optimizer.BinaryOp:
			walk(x.Left)
			walk(x.Right)
		case *optimizer.UnaryOp:
			walk(x.Operand)
		case *optimizer.CastExpr:
			walk(x.Operand)
		case *optimizer.FuncCall:
			for _, a := range x.Args {
				walk(a)
			}
		}
	}
	walk(expr)
	return found
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
// (seqScanOp / indexScanOp). Returns (nil, nil) when the leaf is
// neither (e.g. Values, CTEScan); lockRowsOp falls through to
// pass-through Next in that case with only relation-level
// lock applied.
//
// Returns (nil, error) when it encounters an operator type it does not
// recognise — unknown shapes must error loudly rather than silently
// degrading FOR UPDATE to an unlocked pass-through (M0128-P0.2).
func findScanLeaf(op Operator) (currentTIDProvider, error) {
	for {
		switch v := op.(type) {
		// TID-providing scan leaves.
		case *seqScanOp:
			return v, nil
		case *indexScanOp:
			return v, nil
		// Pass-through operators — recurse through the single child.
		case *projectOp:
			op = v.child
		case *filterOp:
			op = v.child
		case *sortOp:
			op = v.child
		case *limitOp:
			op = v.child
		case *distinctOp:
			op = v.child
		case *distinctOnOp:
			op = v.child
		case *ordinalityOp:
			op = v.child
		case *windowOp:
			op = v.child
		case *projectSetOp:
			op = v.child
		case *materializeOp:
			op = v.child
		case *instrumentedOp:
			op = v.inner
		// setOp: partition UNION ALL — implements currentTIDProvider,
		// delegates to whichever child is currently active.
		case *setOp:
			return v, nil
		// Join operators — prefer the left (outer) child, fall back to right.
		case *joinOp:
			left, lerr := findScanLeaf(v.left)
			if lerr != nil {
				return nil, lerr
			}
			if left != nil {
				return left, nil
			}
			op = v.right
		case *nestedLoopIndexJoinOp:
			outer, oerr := findScanLeaf(v.outer)
			if oerr != nil {
				return nil, oerr
			}
			if outer != nil {
				return outer, nil
			}
			return nliInnerIndexScan(v.inner), nil
		// Known non-TID terminals — legitimate, no error.
		// A LockRows above one of these gets only the relation-level lock
		// and the slot side-channel (drainAndStamp fallback).
		case *valuesOp,
			*cteScanOp,
			*workTableScanOp,
			*materializedCTEScanOp,
			*indexOnlyScanOp,
			*scalarFuncScanOp,
			*fromUnnestOp,
			*generateSeriesOp,
			*generateSubscriptsOp,
			*userSrfScanOp,
			*rowsFromOp,
			*fromRegexpMatchesOp,
			*pgOptionsToTableOp,
			*pgPartitionTreeOp,
			*pgGetPublicationTablesOp,
			*pgGetSequenceDataOp,
			*pgAvailableWalSummariesOp,
			*pgInputErrorInfoOp,
			*tsTokenTypeOp,
			*callOp,
			*recursiveUnionOp:
			return nil, nil
		// Barrier / DML / utility operators — with the M0128-P6.1
		// resjunk-ctid rowmark, the ctid column survives through ANY
		// operator between the scan and LockRows, so these are harmless
		// fallthroughs rather than errors. The walker path is a fallback
		// for pre-resjunk plans; the preferred path reads CtidResno from
		// the row (drainAndStamp).
		case *gatherOp, *gatherMergeOp, *spillOp, *rowsOp,
			*batchReplayOp, *cteDMLPrefixOp,
			*insertOp, *updateOp, *deleteOp, *upsertOp, *mergeOp,
			*ddlOp, *lockRowsOp, *aggregateOp,
			*vacuumOp, *analyzeOp, *clusterOp,
			*checkpointOp, *explainOp, *transactionOp, *setTransactionOp,
			*setConstraintsOp, *utilitySettingsOp, *utilityNoOp,
			*verifyHeapamOp, *reindexOp:
			return nil, nil
		default:
			return nil, nil
		}
	}
}

// findScanLeafForRel finds the currentTIDProvider for a specific relation
// (identified by its storage.RelFileNode), preferring the leftmost occurrence.
// Called after o.child.Open so seqScanOp.rel and indexScanOp.ctx are set.
// Used to ensure the lockRowsOp captures TIDs from the correct scan in complex
// joins where the locked table is not the leftmost leaf (M0100-0010).
//
// Returns (nil, error) when it encounters an operator type it does not
// recognise — unknown shapes must error loudly rather than silently
// degrading FOR UPDATE to an unlocked pass-through (M0128-P0.2).
func findScanLeafForRel(op Operator, targetRel storage.RelFileNode, ctx *Context) (currentTIDProvider, error) {
	for {
		switch v := op.(type) {
		// TID-providing scan leaves — match on relation.
		case *seqScanOp:
			if v.rel == targetRel || (v.tbl != nil && ctx != nil && ctx.Catalog.RelFileNode(v.tbl) == targetRel) {
				return v, nil
			}
			return nil, nil
		case *indexScanOp:
			if (v.ctx != nil && v.ctx.Catalog.RelFileNode(v.plan.Table) == targetRel) || (ctx != nil && ctx.Catalog.RelFileNode(v.plan.Table) == targetRel) {
				return v, nil
			}
			return nil, nil
		// Pass-through operators — recurse through the single child.
		case *projectOp:
			op = v.child
		case *filterOp:
			op = v.child
		case *sortOp:
			op = v.child
		case *limitOp:
			op = v.child
		case *distinctOp:
			op = v.child
		case *distinctOnOp:
			op = v.child
		case *ordinalityOp:
			op = v.child
		case *windowOp:
			op = v.child
		case *projectSetOp:
			op = v.child
		case *materializeOp:
			op = v.child
		case *instrumentedOp:
			op = v.inner
		// setOp: implements currentTIDProvider but we can't check its
		// target rel — fall back to generic findScanLeaf.
		case *setOp:
			return nil, nil
		// Join operators — recurse into both children.
		case *joinOp:
			left, lerr := findScanLeafForRel(v.left, targetRel, ctx)
			if lerr != nil {
				return nil, lerr
			}
			if left != nil {
				return left, nil
			}
			op = v.right
		case *nestedLoopIndexJoinOp:
			outer, oerr := findScanLeafForRel(v.outer, targetRel, ctx)
			if oerr != nil {
				return nil, oerr
			}
			if outer != nil {
				return outer, nil
			}
			if is := nliInnerIndexScan(v.inner); is != nil && is.ctx != nil &&
				is.ctx.Catalog.RelFileNode(is.plan.Table) == targetRel {
				return is, nil
			}
			return nil, nil
		// Known non-TID terminals — legitimate, no error.
		case *valuesOp,
			*cteScanOp,
			*workTableScanOp,
			*materializedCTEScanOp,
			*indexOnlyScanOp,
			*scalarFuncScanOp,
			*fromUnnestOp,
			*generateSeriesOp,
			*generateSubscriptsOp,
			*userSrfScanOp,
			*rowsFromOp,
			*fromRegexpMatchesOp,
			*pgOptionsToTableOp,
			*pgPartitionTreeOp,
			*pgGetPublicationTablesOp,
			*pgGetSequenceDataOp,
			*pgAvailableWalSummariesOp,
			*pgInputErrorInfoOp,
			*tsTokenTypeOp,
			*callOp,
			*recursiveUnionOp:
			return nil, nil
		// Barrier / DML / utility operators — with the M0128-P6.1
		// resjunk-ctid rowmark, the ctid column survives through ANY
		// operator, so these are harmless fallthroughs.
		case *gatherOp, *gatherMergeOp, *spillOp, *rowsOp,
			*batchReplayOp, *cteDMLPrefixOp,
			*insertOp, *updateOp, *deleteOp, *upsertOp, *mergeOp,
			*ddlOp, *lockRowsOp, *aggregateOp,
			*vacuumOp, *analyzeOp, *clusterOp,
			*checkpointOp, *explainOp, *transactionOp, *setTransactionOp,
			*setConstraintsOp, *utilitySettingsOp, *utilityNoOp,
			*verifyHeapamOp, *reindexOp:
			return nil, nil
		default:
			return nil, nil
		}
	}
}

// markJoinPreserveCTID walks the operator tree below a LockRows and tags every
// joinOp with the relation whose heap ctid must survive the build-side drain
// (M0118-0009, eval-plan-qual). Each tagged join decides at its own Open whether
// targetRel actually lands on its build side; the tag is a harmless hint
// otherwise. Recurses through Project / Filter / Sort wrappers and into both
// join children so a join nested under another join is reached too.
//
// Sort must be recursed through, not stopped at (M-NIGHTLY, 2026-08-06). Both
// findScanLeaf and findScanLeafForRel deliberately return nil at a sortOp — a
// scan cursor is meaningless once the sort has drained and reordered its input,
// so `o.scan` is nil for every `ORDER BY ... FOR UPDATE` plan and the TID can
// only arrive through the slot side-channel (sortOp.ctids, re-attached in
// sortOp.Next; drainAndStamp's ms.hasCTID fallback consumes it). For a
// single-relation sort that already works, because seqScanOp stamps
// slot.hasCTID itself. Under a JOIN it did not: a joinOp only carries the heap
// ctid through when preserveCTIDRel is set, this walker stopped at the sortOp
// before ever reaching the join, so the sort recorded has=false for every row,
// drainAndStamp found neither a scan nor a slot TID, and lockRowsOp fell
// through to its unlocked pass-through path. The visible effect was a silent
// violation of FOR UPDATE: `SELECT ... FROM a, b WHERE ... ORDER BY ...
// FOR UPDATE OF a` neither blocked on a concurrently-updated row nor ran the
// EvalPlanQual recheck — it returned the stale pre-update row immediately,
// where PG blocks and returns the updated one. PG has no equivalent hazard: it
// carries the row mark as a resjunk `ctid` column that Sort preserves like any
// other column (nodeLockRows.c / preprocess_targetlist's rowmark junk attrs),
// rather than reconstructing it from the shape of the plan tree.
func markJoinPreserveCTID(op Operator, targetRel storage.RelFileNode) error {
	switch v := op.(type) {
	// Pass-through operators — recurse through the single child.
	case *projectOp:
		return markJoinPreserveCTID(v.child, targetRel)
	case *filterOp:
		return markJoinPreserveCTID(v.child, targetRel)
	case *sortOp:
		return markJoinPreserveCTID(v.child, targetRel)
	case *limitOp:
		return markJoinPreserveCTID(v.child, targetRel)
	case *distinctOp:
		return markJoinPreserveCTID(v.child, targetRel)
	case *distinctOnOp:
		return markJoinPreserveCTID(v.child, targetRel)
	case *ordinalityOp:
		return markJoinPreserveCTID(v.child, targetRel)
	case *windowOp:
		return markJoinPreserveCTID(v.child, targetRel)
	case *projectSetOp:
		return markJoinPreserveCTID(v.child, targetRel)
	case *materializeOp:
		return markJoinPreserveCTID(v.child, targetRel)
	case *instrumentedOp:
		return markJoinPreserveCTID(v.inner, targetRel)
	// Join operators — tag joinOp with preserveCTIDRel, recurse into children.
	case *joinOp:
		rel := targetRel
		v.preserveCTIDRel = &rel
		if err := markJoinPreserveCTID(v.left, targetRel); err != nil {
			return err
		}
		return markJoinPreserveCTID(v.right, targetRel)
	case *nestedLoopIndexJoinOp:
		// Inner is always *indexScanOp or *memoizeOp (nliInner), neither
		// of which can contain a joinOp — recurse outer only.
		return markJoinPreserveCTID(v.outer, targetRel)
	// Known terminals — no children, harmless no-op.
	case *seqScanOp, *indexScanOp, *setOp,
		*valuesOp, *cteScanOp, *workTableScanOp, *materializedCTEScanOp,
		*indexOnlyScanOp, *scalarFuncScanOp, *fromUnnestOp, *generateSeriesOp,
		*generateSubscriptsOp, *userSrfScanOp, *rowsFromOp, *fromRegexpMatchesOp,
		*pgOptionsToTableOp, *pgPartitionTreeOp, *pgGetPublicationTablesOp,
		*pgGetSequenceDataOp, *pgAvailableWalSummariesOp, *pgInputErrorInfoOp,
		*tsTokenTypeOp, *callOp, *recursiveUnionOp:
		return nil
	// M0128-P6.1 resjunk-ctid rowmark: with the ctid in the row as a
	// column, the side-channel preserveCTIDRel tag is a fallback for
	// pre-resjunk plans. Unknown operators (including gather/spill/
	// DML/utility) are harmless — the ctid column survives them.
	default:
		return nil
	}
}

// parseRowCTID extracts a storage.ItemPointer from a Datum.
// The Datum must be a tid value in the format "(block,offset)".
func parseRowCTID(d Datum) (storage.ItemPointer, bool) {
	var block int64
	var off int
	if d.IsNull() {
		return storage.ItemPointer{}, false
	}
	n, err := fmt.Sscanf(d.StringValue(), "(%d,%d)", &block, &off)
	if n != 2 || err != nil || block < 0 || off < 0 {
		return storage.ItemPointer{}, false
	}
	return storage.ItemPointer{Block: storage.BlockNumber(block), Offset: uint16(off)}, true
}

func (o *lockRowsOp) Schema() optimizer.Schema { return o.plan.Output() }

func (o *lockRowsOp) Open(ctx *Context) error {
	o.ctx = ctx
	// Per-execution state reset — Open doubles as this operator's ExecReScan
	// entry point. A correlated sublink that carries FOR UPDATE is re-run per
	// outer row, and again during an EvalPlanQual recheck (classifySubPlan maps
	// LockRows to rescanCloseOpen, so the retained tree is Close()d and Open()ed
	// rather than rebuilt). Close clears `pending` but left `drained`/`pos`
	// behind, so the second Open served EOF straight from the emptied buffer and
	// the inner plan never re-scanned: `EXISTS (... FOR UPDATE)` in an UPDATE's
	// WHERE silently evaluated to FALSE on the EPQ recheck and the outer row was
	// dropped instead of updated (eval-plan-qual `wnested2`; root-0030).
	// Upstream keeps no such buffer — ExecLockRows pulls one row at a time from
	// its subplan and ExecReScan resets the whole subtree (nodeLockRows.c).
	o.pending = nil
	o.pos = 0
	o.drained = false
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
		case optimizer.LockStrengthForKeyShare:
			o.lockStrength = storage.HeapXmaxKeyShrLock
		case optimizer.LockStrengthForShare:
			o.lockStrength = storage.HeapXmaxShrLock
		case optimizer.LockStrengthForNoKeyUpdate:
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
				Message: fmt.Sprintf(`cannot lock rows in materialized view "%s"`, lk.Table.Name),
			}
		}
		rel := ctx.Catalog.RelFileNode(lk.Table)
		var err error
		switch lk.WaitPolicy {
		case optimizer.LockWaitBlock, optimizer.LockWaitSkipLocked:
			// SKIP LOCKED affects only per-row locks: the relation-level
			// RowShareLock is always acquired with the normal blocking
			// policy. Mirrors upstream — the LockWaitPolicy governs
			// heap_lock_tuple, not the relation lock. The actual row
			// skipping happens per-tuple in stampLock / stampLockInner.
			// M0118-0003.
			err = ctx.acquireRelLock(rel, lmgr.RowShareLock)
		case optimizer.LockWaitNoWait:
			// NOWAIT: try once and bail with 55P03 if the
			// relation lock isn't immediately grantable.
			// Mirrors upstream's "could not obtain lock on
			// row" diagnostic at the relation-coarse layer
			// goopg has today.
			err = ctx.tryAcquireRelLock(rel, lmgr.RowShareLock)
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
	// M0118-0009 (eval-plan-qual): if the first locked relation can land on the
	// BUILD side of a lazy hash join below, that scan is drained + closed at the
	// join's Open and its currentTID is lost before drainAndStamp runs. Tell the
	// join(s) to preserve that relation's heap ctid through the build so the
	// emitted slot carries it (recovered via the ms.hasCTID fallback in
	// drainAndStamp). Must run BEFORE o.child.Open builds the hash table.
	if len(o.plan.Locks) > 0 && o.plan.Locks[0].Table != nil {
		targetRel := ctx.Catalog.RelFileNode(o.plan.Locks[0].Table)
		if err := markJoinPreserveCTID(o.child, targetRel); err != nil {
			return err
		}
	}
	// EX3-05 Cut A: this LockRows is the TID side-channel's consumer
	// (drainAndStamp's ms.hasCTID fallback). Enable sortOp.wantCTIDs on the
	// spine below BEFORE the child drains — the sort records TIDs only when
	// asked. Must precede child.Open: sortOp.Open pulls every row up front.
	markSortWantCTIDs(o.child)
	if err := o.child.Open(ctx); err != nil {
		return err
	}
	// Prefer the scan for the first physical locked relation so that
	// the TID tracked by drainAndStamp comes from the right table.
	// This matters when the locked table is not the leftmost scan leaf
	// in a complex join (e.g. FOR UPDATE OF jt where jt is not outer).
	scan, err := findScanLeaf(o.child) // default fallback
	if err != nil {
		return err
	}
	o.scan = scan
	for i := range o.plan.Locks {
		if o.plan.Locks[i].Table != nil {
			targetRel := ctx.Catalog.RelFileNode(o.plan.Locks[i].Table)
			if scanRel, scanErr := findScanLeafForRel(o.child, targetRel, ctx); scanErr != nil {
				return scanErr
			} else if scanRel != nil {
				o.scan = scanRel
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
	// When the locked scan is an index scan, the index's key condition (e.g.
	// `key = 1`) lives in the IndexScan node, NOT in a filterOp — so an EPQ
	// recheck against only the filterOp predicate would miss a key-column change
	// (lock-update-delete blocker2: a committed key-UPDATE relocates the row to
	// key=2, which no longer satisfies the original `key = 1`). Fold the
	// synthesised index predicate into the EPQ recheck predicate so
	// epqRecheckFilter re-applies it to the latest version, matching PG's
	// EvalPlanQual re-running the whole plan (index recheck included). The
	// synthesised ref points at the indexed column's table ordinal, so it stays
	// within filterCols; guard anyway for safety. M0118-0003.
	// Fold the index key condition ONLY when its key expression is row-local —
	// a constant such as `key = 1`. When the locked index scan is the inner of a
	// join its key is a join/correlated reference (e.g. `jt.id = y` where y is
	// another input's column), and indexScanPredicate emits that reference with
	// a column index in the OUTER/join coordinate space. Re-applying it in
	// epqRecheckFilter — which decodes only the locked table's own columns —
	// silently misreads that index as a locked-table column (`jt.id = jt.data`)
	// and wrongly drops the row (eval-plan-qual selectresultforupdate: the
	// post-update jt row was discarded, returning 0 rows). filterPredMaxColRef
	// can't catch this because the misaligned index happens to fall inside
	// [0,len(filterCols)). For a non-key UPDATE the join key is preserved on the
	// successor, so skipping its recheck is correct; key-column changes are still
	// caught by the CTID-chain logic. M0118-0009 (docs/design/0118-0106).
	// The foldable probe keys: the single Key, or one per SAOP descent
	// (B-14 — every element is a planner Const by gate, hence row-local
	// unless the gate is ever widened, which the per-element check
	// guards). Keys (composite) stays out, as before.
	probeRowLocal := false
	if ix, ok := o.scan.(*indexScanOp); ok && ix.plan != nil && len(o.filterCols) > 0 {
		switch {
		case ix.plan.Key != nil:
			probeRowLocal = !exprRefsColumnOrOuter(ix.plan.Key)
		case len(ix.plan.SAOPKeys) > 0:
			probeRowLocal = true
			for _, k := range ix.plan.SAOPKeys {
				if exprRefsColumnOrOuter(k) {
					probeRowLocal = false
					break
				}
			}
		}
		if probeRowLocal {
			if idxPred := indexScanPredicate(ix.plan); idxPred != nil &&
				filterPredMaxColRef(idxPred) < len(o.filterCols) {
				if o.filterPred == nil {
					o.filterPred = idxPred
				} else {
					o.filterPred = &optimizer.BinaryOp{Op: parser.OpAnd, Left: o.filterPred, Right: idxPred}
				}
			}
		}
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
	// intra-grant-inplace: a `SELECT … FROM pg_class WHERE oid = <rel> FOR …`
	// takes an explicit row lock on the pg_class tuple. goopg has no real
	// pg_class heap tuple, so the heap-stamping path above is a no-op for it;
	// instead record the rowmark in the catalog so a concurrent in-place catalog
	// update (ALTER TABLE ADD PRIMARY KEY → relhasindex) waits on it. Design
	// 0118-0113.
	if ee := o.maybeRecordPgClassRowMark(); ee != nil {
		return ee
	}
	return nil
}

// maybeRecordPgClassRowMark records an explicit pg_class tuple lock when this
// LockRows targets pg_class with a single `oid = <const>` predicate. A no-op
// for every other relation, so the normal heap row-lock path is byte-unchanged.
// Design 0118-0113 (intra-grant-inplace).
//
// After recording the mark it serialises behind any concurrent uncommitted
// GRANT/REVOKE on the same table, mirroring PG: SELECT … FROM pg_class … FOR
// UPDATE acquires the tuple lock (the rowmark, recorded first so a peer
// GRANT/REVOKE blocks behind it) then waits on the pg_class tuple xmax held by
// the in-flight ACL change (intra-grant-inplace perm 9: sfu3 waits behind
// grant1). Design 0118-0116. Returns a non-nil *ExecError only on a deadlock /
// lock-timeout during that wait.
func (o *lockRowsOp) maybeRecordPgClassRowMark() *ExecError {
	if len(o.plan.Locks) == 0 || o.plan.Locks[0].Table == nil {
		return nil
	}
	if o.plan.Locks[0].Table.OID != catalog.RelationRelationId {
		return nil
	}
	im, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	relOID, ok := o.pgClassFilterOID()
	if !ok {
		return nil
	}
	// The locker must hold a real (materialised) XID so the in-place updater can
	// WaitForXID on it.
	if err := o.ctx.MaterializeWriterXID(); err != nil {
		return nil
	}
	xid := uint32(o.writerXID())
	if xid == 0 {
		return nil
	}
	// Every row-lock strength conflicts with a concurrent in-place pg_class
	// update (heap_inplace_update's no-key update) except FOR KEY SHARE.
	conflicts := o.plan.Locks[0].Strength != optimizer.LockStrengthForKeyShare
	im.AddPgClassRowMark(relOID, xid, conflicts)
	o.pgClassRowMarkOID = relOID
	o.pgClassRowMarkXID = xid
	// Now block behind any uncommitted ACL change on the same table — the
	// rowmark is already recorded so a concurrent GRANT/REVOKE blocks behind us.
	if ee := waitTableACLChange(o.ctx, relOID); ee != nil {
		return ee
	}
	// Also block behind an uncommitted DROP TABLE deferred to COMMIT: PG's tuple
	// lock waits on the pg_class delete xmax, then finds the tuple gone once the
	// DROP commits. intra-grant-inplace perm 10 (sfu3 waits behind drop1). Design
	// 0118-0117.
	return waitTablePendingDrop(o.ctx, relOID)
}

// pgClassFilterOID extracts the relation OID from a `oid = <const>` equality in
// the child scan's filter predicate (the shape every intra-grant-inplace rowmark
// SELECT uses). Returns ok=false for any other predicate shape so the rowmark is
// simply not recorded. Design 0118-0113.
func (o *lockRowsOp) pgClassFilterOID() (uint32, bool) {
	bo, ok := findFilterPred(o.child).(*optimizer.BinaryOp)
	if !ok || bo.Op != parser.OpEq {
		return 0, false
	}
	cr, ok := bo.Left.(*optimizer.ColumnRef)
	constExpr := bo.Right
	if !ok {
		if cr, ok = bo.Right.(*optimizer.ColumnRef); !ok {
			return 0, false
		}
		constExpr = bo.Left
	}
	if cr.Index < 0 || cr.Index >= len(o.filterCols) ||
		!strings.EqualFold(o.filterCols[cr.Index].Name, "oid") {
		return 0, false
	}
	d, err := evalExpr(constExpr, nil, o.ctx)
	if err != nil || d.IsNull() || d.Kind != KindInt || d.Int <= 0 {
		return 0, false
	}
	return uint32(d.Int), true
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
			// M0128-P6.1: trim ctid columns — the schema returned by
			// o.Schema() (i.e. o.plan.Output()) already strips them,
			// so the row must match.
			merged = merged[:len(o.Schema())]
			ms := SlotFromRow(o.Schema(), merged)
			ms.hasCTID = true
			ms.ctidBlock = uint32(entry.newPtr.Block)
			ms.ctidOff = entry.newPtr.Offset
			return ms, nil
		}
	}
	// M0128-P6.1: trim ctid columns from the pending row so it matches
	// the stripped schema (NumCtidCols is 0 when no ctid was wired, so
	// this is a no-op for the pre-M0128 path).
	row := entry.row
	if n := o.plan.NumCtidCols; n > 0 && len(row) > n {
		row = row[:len(row)-n]
	}
	ms := SlotFromRow(o.Schema(), row)
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
		// M0128-P6.1 resjunk-ctid rowmark: preferred path — read the TID
		// from the ctid column in the row (the durable PG approach) rather
		// than reconstructing it from the plan shape. Falls through to the
		// walker/side-channel paths when no LockedRel has a wired CtidResno.
		for i := range o.plan.Locks {
			lk := &o.plan.Locks[i]
			if lk.Table != nil && lk.CtidResno >= 0 && lk.CtidResno < len(row) {
				if ptr, ok := parseRowCTID(row[lk.CtidResno]); ok {
					entry.rel = o.ctx.Catalog.RelFileNode(lk.Table)
					entry.ptr = ptr
					entry.haveTID = true
					break
				}
			}
		}
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
	// intra-grant-inplace perm 10: a pg_class rowmark recorded up front that ends
	// up locking no tuple (the relation was concurrently DELETEd and committed
	// during our wait, so the scan now yields 0 rows) holds no lock in PG —
	// retract the mark so a peer GRANT/REVOKE blocked behind it proceeds at once.
	// Design 0118-0117.
	if len(o.pending) == 0 && o.pgClassRowMarkOID != 0 {
		if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
			im.ClearPgClassRowMark(o.pgClassRowMarkOID, o.pgClassRowMarkXID)
		}
		o.pgClassRowMarkOID = 0
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
	// NO up-front tuple-tag acquire here. heap_lock_tuple takes the
	// heavyweight LOCKTAG_TUPLE only when it is about to SLEEP on a
	// conflicting holder (heap_acquire_tuplock right before
	// XactLockTableWait / MultiXactIdWait) — a request that does not
	// conflict with the tuple's current xmax must NEVER touch the tuple
	// tag, or it queues behind an unrelated waiter that is parked holding
	// it (tuplelock-upgrade-no-deadlock: s3's FOR KEY SHARE must complete
	// at once while s2's FOR UPDATE waits on s1 holding the tag).
	// stampLockInner therefore acquires the tag at its wait sites, via
	// acquireTupleLockForWait. NOWAIT / SKIP LOCKED never sleep, so they
	// never take the tag either — their fail-fast/skip arms fire off the
	// persisted-xmax conflict alone, which is the same observable outcome
	// as upstream's ConditionalLockTupleTagged/ConditionalXactLockTableWait
	// pair. Check-then-stamp atomicity needs no tag: it is guaranteed by
	// the page latch held across stampLockInner's conflict check and
	// stamp. Design 0021-0012.
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
		!o.isSelfXID(tup.Header.Xmax) {

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
					// heap_lock_updated_tuple: having combined our lock into the
					// version the updater superseded, traverse the update chain
					// forward and lock the newer version(s) too, so a later DELETE
					// or key-UPDATE on the live successor honours our row lock
					// (lock-update-traversal). Capture the forward pointer + the
					// updater's xid from the original header before releasing the
					// page; the lock-only combine above did not change either.
					succ := tup.Header.CTID
					prior := o.updaterXID(tup.Header)
					hasSucc := succ.Block != storage.InvalidBlockNumber &&
						(succ.Block != ptr.Block || succ.Offset != ptr.Offset)
					slot.Unlock()
					o.ctx.Pool.Unpin(slot)
					if hasSucc && prior != storage.InvalidTransactionID {
						// Walk the chain forward, WAITING on any conflicting
						// in-flight updater (heap_lock_updated_tuple_rec). The
						// blocked-then-woken locker (lock-update-delete s1l) must
						// re-evaluate against the latest version: if the chain ends
						// in a committed conflicting DELETE/key-UPDATE the row is
						// gone for us (EvalPlanQual returns nothing), so we drop it
						// instead of yielding the stale snapshot version.
						outcome, latest, perr := o.propagateLockForward(rel, succ, prior)
						if perr != nil {
							return storage.ItemPointer{}, false, false, perr
						}
						switch outcome {
						case chainLockDeleted:
							// Latest version deleted by a committed conflicting
							// txn — nothing to return.
							return storage.ItemPointer{}, false, true, nil // epqSkipped
						case chainLockUpdated:
							// Latest version is `latest`; re-check the WHERE
							// predicate against it (EvalPlanQualFetchRowMark). If
							// it no longer qualifies, drop the row; otherwise yield
							// the updated version.
							if o.filterPred != nil && len(o.filterCols) > 0 {
								if skip := o.epqRecheckFilter(rel, latest); skip {
									return storage.ItemPointer{}, false, true, nil // epqSkipped
								}
							}
							return latest, true, false, nil
						}
					}
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
		infomask := tup.Header.Infomask
		// MultiXact-aware updater handling (heap_lock_tuple's MultiXactIdWait):
		// when the xmax is a MultiXactId the raw value is NOT a TransactionID, so
		// it must not be fed to IsXIDActive / WaitForXID / HasAbortedXID. Resolve
		// the real row-updater member for the abort/commit/chain decision below,
		// and collect every OTHER still-active member whose lock we must also wait
		// on (e.g. a surviving FOR KEY SHARE holder recorded alongside the
		// updater). Without this an upgrading FOR UPDATE waiter ignores the
		// co-holder and proceeds out of order: in tuplelock-upgrade-no-deadlock
		// perms 2/3 the existing key-share holder s3 upgrading its own lock must
		// complete before the pure waiter s2, because s2's FOR UPDATE still
		// conflicts with s3's key-share membership even after the updater aborts.
		var multiHolders []storage.TransactionID
		if storage.IsHeapTupleXmaxMulti(infomask) {
			updater := o.updaterXID(tup.Header)
			// Wait only on co-members whose lock mode CONFLICTS with our request
			// (MultiXactIdWait semantics), not on every active member. A surviving
			// NON-conflicting locker recorded alongside the updater must NOT be
			// waited on: in multixact-no-forget, after s2's no-key UPDATE aborts the
			// row's xmax is a multi {s1 FOR KEY SHARE (active), s2 no-key-update
			// (aborted)}; s3's FOR NO KEY UPDATE is compatible with KEY SHARE, so it
			// must become grantable immediately once the aborted updater is resolved
			// rather than blocking on s1 until s1 commits. A conflicting upgrader
			// (s3's FOR UPDATE) still finds the key-share member conflicting and
			// waits — preserving tuplelock-upgrade-no-deadlock perms 2/3. The updater
			// itself is resolved as `xmax` and handled by the wait/abort logic below,
			// so exclude it from this set. M0118-0009 (docs/design/0118-0016).
			for _, hx := range o.conflictingLockHolders(xmax, infomask, tup.Header.Infomask2) {
				if hx != updater {
					multiHolders = append(multiHolders, hx)
				}
			}
			xmax = updater
		}
		slot.Unlock()
		o.ctx.Pool.Unpin(slot)

		// Wait for any still-active CONFLICTING non-updater MultiXact lock member
		// before the updater itself. An existing holder upgrading its own lock is
		// excluded here (conflictingLockHolders skips self) and so proceeds ahead
		// of a pure waiter, matching PostgreSQL's lock-release order. The wait
		// policy is honoured exactly as for the updater wait below.
		if len(multiHolders) > 0 {
			switch o.waitPolicy {
			case optimizer.LockWaitSkipLocked:
				return storage.ItemPointer{}, false, true, nil // epqSkipped
			case optimizer.LockWaitNoWait:
				return storage.ItemPointer{}, false, false, &ExecError{
					Code:    "55P03",
					Pos:     o.plan.Pos(),
					Message: fmt.Sprintf(`could not obtain lock on row in relation "%s"`, o.lockRelName),
				}
			}
			// About to sleep on conflicting members: take the tuple tag first
			// so concurrent waiters wake in arrival order (heap_acquire_tuplock
			// before MultiXactIdWait). Skipped when we are already a member of
			// the multi — an upgrader queueing behind a waiter that is itself
			// waiting on our held lock would deadlock (design 0021-0012).
			if err := o.acquireTupleLockForWait(rel, ptr, tup.Header); err != nil {
				return storage.ItemPointer{}, false, false, err
			}
			qctx := o.ctx.Ctx
			if qctx == nil {
				qctx = context.Background()
			}
			for _, hx := range multiHolders {
				if werr := o.ctx.TxnMgr.WaitForXID(qctx, hx); werr != nil {
					if ee := o.rowWaitTimeoutError(werr); ee != nil {
						return storage.ItemPointer{}, false, false, ee
					}
					// Plain client cancel — skip this row silently.
					return storage.ItemPointer{}, false, false, nil
				}
			}
			// Membership changed while we waited — re-evaluate from scratch.
			return o.stampLockInner(rel, ptr, depth+1)
		}

		// Wait for in-progress updater to commit or abort. NOWAIT / SKIP LOCKED
		// must not block here: when the chain-follow reaches a version whose xmax
		// is an in-progress *real* update (the second, uncommitted UPDATE in
		// skip-locked-4 / nowait-4), the wait policy has to be honoured exactly as
		// it is for a lock-only conflict below — PostgreSQL's EvalPlanQualFetch
		// skips (SKIP LOCKED) or ereports (NOWAIT) rather than waiting on the
		// updater. The head version is reached the same way, so this also covers a
		// plain FOR UPDATE NOWAIT/SKIP LOCKED on a directly in-progress update.
		// M0118-0003.
		isActive := o.ctx.TxnMgr != nil && o.ctx.TxnMgr.IsXIDActive(xmax)
		if isActive {
			switch o.waitPolicy {
			case optimizer.LockWaitSkipLocked:
				return storage.ItemPointer{}, false, true, nil // epqSkipped
			case optimizer.LockWaitNoWait:
				return storage.ItemPointer{}, false, false, &ExecError{
					Code:    "55P03",
					Pos:     o.plan.Pos(),
					Message: fmt.Sprintf(`could not obtain lock on row in relation "%s"`, o.lockRelName),
				}
			}
			// About to sleep on the in-progress updater: take the tuple tag
			// first (heap_acquire_tuplock before XactLockTableWait) so waiters
			// wake in arrival order; skipped when we already hold a lock
			// membership on this tuple (design 0021-0012).
			if err := o.acquireTupleLockForWait(rel, ptr, tup.Header); err != nil {
				return storage.ItemPointer{}, false, false, err
			}
			qctx := o.ctx.Ctx
			if qctx == nil {
				qctx = context.Background()
			}
			if werr := o.ctx.TxnMgr.WaitForXID(qctx, xmax); werr != nil {
				if ee := o.rowWaitTimeoutError(werr); ee != nil {
					return storage.ItemPointer{}, false, false, ee
				}
				// Plain client cancel — skip this row silently.
				return storage.ItemPointer{}, false, false, nil
			}
		}

		// "Updater rolled back" covers two abort flavours: a whole-transaction
		// ROLLBACK (HasAbortedXID, the top-level aborted set) AND a sub-transaction
		// rolled back via ROLLBACK TO SAVEPOINT (IsAborted, the pg_subtrans map).
		// A DELETE/UPDATE stamped inside a savepoint carries the sub-XID as xmax
		// (effectiveWriterXID), so when that savepoint is rolled back the deletion
		// never happened and the row is live at the original ptr — exactly the
		// top-level-abort case. Without the IsAborted arm the subxid is neither
		// active nor in the top-level aborted set, so the code below would treat the
		// dead deletion as a committed one and follow the (chain-tail) CTID to "no
		// live successor", dropping a row that is in fact still present
		// (delete-abort-savept). M0118-0009 (docs/design/0118-0013).
		if o.ctx.TxnMgr != nil && (o.ctx.TxnMgr.HasAbortedXID(xmax) || o.ctx.TxnMgr.IsAborted(xmax)) {
			// Updater rolled back: row is live at original ptr. Stamp it.
			ptr2, followed2, err2 := o.stampAtPtr(rel, ptr)
			return ptr2, followed2, false, err2
		}

		// Updater committed: under RR/SER raise a serialization error.
		// Under RC: follow CTID chain to find the live successor.
		if o.ctx.Tx.Isolation != transam.IsolationReadCommitted {
			return storage.ItemPointer{}, false, false, &ExecError{
				Code:    "40001",
				Message: "could not serialize access due to concurrent update",
			}
		}
		next := ctid
		// No live successor when CTID is a chain-tail sentinel: self-pointing
		// (the latest version), an invalid block, or a zero offset. A goopg
		// DELETE leaves the original {InvalidBlockNumber,0} initial CTID in
		// place (only UPDATE rewrites it via stampOldCtid), so a committed
		// DELETE of a never-updated row reaches here with next.Block ==
		// InvalidBlockNumber — following it would Pin a non-existent block and
		// surface ErrShortRead. isChainTailCTID is the same test used by
		// epqFollowChainFull. The committed updater deleted the row, so signal
		// epqSkipped=true: the caller (drainAndStamp) drops the row rather than
		// yielding the stale pre-delete version, matching PG's EvalPlanQual
		// returning no tuple.
		if isChainTailCTID(next, ptr.Block, ptr.Offset) {
			// Chain-tail sentinel — deleted row, no live successor.
			return storage.ItemPointer{}, false, true, nil // epqSkipped
		}
		// No tuple-tag acquire on the successor here: the recursive
		// stampLockInner takes the tag itself iff it must wait on a
		// conflicting holder of the successor version (design 0021-0012).
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
		!o.isSelfXID(tup.Header.Xmax) &&
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
		// Wait only on the holders whose mode actually CONFLICTS with our
		// request (MultiXactIdWait semantics): a multi may also name compatible
		// co-holders — e.g. the same backend's top-level FOR KEY SHARE alongside
		// a stronger savepoint sub-lock — and blocking on those would make us
		// wait for an unrelated transaction to end rather than becoming grantable
		// when the conflicting holder releases (tuplelock-upgrade-no-deadlock
		// perm 9). docs/design/0118-0012.
		conflicting := o.conflictingLockHolders(tup.Header.Xmax, tup.Header.Infomask, tup.Header.Infomask2)
		if len(conflicting) > 0 {
			slot.Unlock()
			o.ctx.Pool.Unpin(slot)
			switch o.waitPolicy {
			case optimizer.LockWaitSkipLocked:
				// SKIP LOCKED: silently drop the contended row.
				return storage.ItemPointer{}, false, true, nil // epqSkipped
			case optimizer.LockWaitNoWait:
				return storage.ItemPointer{}, false, false, &ExecError{
					Code:    "55P03",
					Pos:     o.plan.Pos(),
					Message: fmt.Sprintf(`could not obtain lock on row in relation "%s"`, o.lockRelName),
				}
			default:
				// Blocking FOR UPDATE/FOR SHARE: wait for every still-active
				// conflicting holder to finish (a multi may name several), then
				// re-evaluate the tuple from scratch. A WaitForXID on a savepoint
				// sub-lock holder returns as soon as ROLLBACK TO SAVEPOINT marks
				// that subxid aborted (xidActiveWithSubxact + the MarkSubxactAborted
				// broadcast), so the retry sees the reverted (weaker) membership.
				//
				// About to sleep: take the tuple tag first so concurrent waiters
				// on this row wake in ARRIVAL order — a single WaitForXID
				// broadcast wakes every waiter at once, and without the tag the
				// re-stamp race is decided by Go scheduling (the historical
				// TuplelockUpgradeNoDeadlock flake). Skipped when we are already
				// a lock member (upgrade): queueing behind a waiter that is
				// itself waiting on our held lock would deadlock, exactly
				// heap_lock_tuple's skip_tuple_lock case (design 0021-0012).
				if err := o.acquireTupleLockForWait(rel, ptr, tup.Header); err != nil {
					return storage.ItemPointer{}, false, false, err
				}
				qctx := o.ctx.Ctx
				if qctx == nil {
					qctx = context.Background()
				}
				for _, hx := range conflicting {
					if werr := o.ctx.TxnMgr.WaitForXID(qctx, hx); werr != nil {
						if ee := o.rowWaitTimeoutError(werr); ee != nil {
							return storage.ItemPointer{}, false, false, ee
						}
						// Plain client cancel — skip this row silently.
						return storage.ItemPointer{}, false, false, nil
					}
				}
				return o.stampLockInner(rel, ptr, depth+1)
			}
		}
		// No conflicting holder still active (committed/aborted/non-conflicting):
		// the conflicting lock(s) are released, so fall through and combine our
		// lock-only xmax with any surviving compatible holders below.
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
	//
	// The SAME backend upgrading its OWN lock inside a savepoint (s1 holds FOR
	// KEY SHARE at the top level, then takes FOR NO KEY UPDATE under sub-XID f)
	// must ALSO go through the producer so the outer-level member survives: a
	// plain single-xmax overwrite below would discard the top-level KEY SHARE,
	// and ROLLBACK TO f — which only aborts the sub-XID member — would then leave
	// the row with no surviving lock at all, letting a conflicting waiter wake
	// prematurely (delete-abort-savept-2 perms 1/2: s2's FOR UPDATE must keep
	// waiting on the restored outer KEY SHARE until s1 commits). stampMultiLock
	// keeps every active member whose xid differs from our current writerXID as a
	// survivor, so it preserves the outer self lock; for a same-level self re-lock
	// (no outer member, e.g. a plain FOR UPDATE re-acquire) it finds no survivor
	// and returns combined=false, falling through to the single-holder stamp
	// unchanged. M0118-0009 (docs/design/0118-0015).
	if o.ctx.MultiXact != nil &&
		tup.Header.Xmax != storage.InvalidTransactionID &&
		storage.IsHeapTupleLockOnly(tup.Header.Infomask) &&
		(!o.isSelfXID(tup.Header.Xmax) || o.hasOuterSelfLockMember(tup.Header)) {
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
	wxid := o.writerXID()
	if err := storage.PageSetHeapTupleLockOnly(slot.Page(), ptr.Offset, wxid, o.lockStrength); err != nil {
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
	derr := markHeapLockDirty(o.ctx.Pool, slot, rel, ptr.Block, ptr.Offset, wxid, o.lockStrength)
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
	// "Don't forget the lock": the aborted updater may have been recorded in a
	// MultiXactId that ALSO names a still-active lock-only holder (s1's FOR KEY
	// SHARE in multixact-no-forget). A plain single-locker overwrite below would
	// discard that surviving member. stampMultiLock keeps every still-active
	// member (dropping the aborted updater, which is no longer active) and adds
	// our locker, so s1's lock survives into the new multi. It returns
	// combined=false when there is no surviving holder (e.g. a single aborted
	// updater with no co-locker, as in delete-abort-savept), in which case we
	// fall through to the single-locker stamp unchanged. M0118-0009
	// (docs/design/0118-0016).
	if o.ctx.MultiXact != nil && tup.Header.Xmax != storage.InvalidTransactionID {
		multiPtr, combined, merr := o.stampMultiLock(slot, ptr, tup.Header)
		if merr != nil {
			slot.Unlock()
			o.ctx.Pool.Unpin(slot)
			return storage.ItemPointer{}, false, merr
		}
		if combined {
			// stampMultiLock already marked the page dirty.
			slot.Unlock()
			o.ctx.Pool.Unpin(slot)
			return multiPtr, false, nil
		}
	}
	wxid := o.writerXID()
	if err := storage.PageSetHeapTupleLockOnly(slot.Page(), ptr.Offset, wxid, o.lockStrength); err != nil {
		slot.Unlock()
		o.ctx.Pool.Unpin(slot)
		return storage.ItemPointer{}, false, err
	}
	if err := storage.PageSetHeapTupleLockKeysUpdated(slot.Page(), ptr.Offset, o.lockKeysUpdated); err != nil {
		slot.Unlock()
		o.ctx.Pool.Unpin(slot)
		return storage.ItemPointer{}, false, err
	}
	derr := markHeapLockDirty(o.ctx.Pool, slot, rel, ptr.Block, ptr.Offset, wxid, o.lockStrength)
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
	return !o.isSelfXID(effXmax)
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
// acquire based on the SELECT FOR clause's four-way strength — the
// hwlock column of upstream's tupleLockExtraInfo (see tupleLockHwMode).
func (o *lockRowsOp) tupleLockMode() lmgr.Mode {
	return tupleLockHwMode(o.lockMemberStatus())
}

// tupleLockHwMode is the verbatim port of upstream's
// tupleLockExtraInfo[].hwlock column (heapam.c): the heavyweight lockmgr
// mode a row-lock/DML request of the given multixact status acquires on
// the LOCKTAG_TUPLE while it sleeps on a conflicting holder. The exact
// column matters: AccessShareLock does NOT conflict with ExclusiveLock,
// so a FOR KEY SHARE never queues on the tuple tag behind a plain
// (no-key) UPDATE / FOR NO KEY UPDATE holder — collapsing the two shared
// strengths to RowShareLock made exactly that pairing block and time out
// (tuplelock-upgrade-no-deadlock, design 0021-0012).
func tupleLockHwMode(req multixact.Status) lmgr.Mode {
	switch req {
	case multixact.StatusForKeyShare:
		return lmgr.AccessShareLock
	case multixact.StatusForShare:
		return lmgr.RowShareLock
	case multixact.StatusForNoKeyUpdate, multixact.StatusNoKeyUpdate:
		return lmgr.ExclusiveLock
	default: // StatusForUpdate, StatusUpdate (DELETE / key-changing UPDATE)
		return lmgr.AccessExclusiveLock
	}
}

// selfIsLockMember reports whether this backend (top-level xact or any of
// its subxacts) already holds a row-lock or updater membership on the
// tuple — upstream's `current_is_member` out-param of
// DoesMultiXactIdConflict, plus the single-xmax self case. A waiter that
// is already a member must NOT take the heavyweight tuple tag before
// sleeping (heap_lock_tuple's skip_tuple_lock; heap_update/heap_delete's
// `if (!current_is_member) heap_acquire_tuplock(...)`): a pure waiter
// parked on the tag is waiting for OUR held lock to be released, so
// queueing behind it would deadlock — the exact scenario
// tuplelock-upgrade-no-deadlock exists to pin. Design 0021-0012.
func (o *lockRowsOp) selfIsLockMember(hdr storage.HeapTupleHeader) bool {
	if hdr.Xmax == storage.InvalidTransactionID {
		return false
	}
	if storage.IsHeapTupleXmaxMulti(hdr.Infomask) {
		if o.ctx.MultiXact == nil {
			return false
		}
		members, ok := o.ctx.MultiXact.Members(multixact.MultiXactId(hdr.Xmax))
		if !ok {
			return false
		}
		for _, m := range members {
			if o.isSelfXID(m.Xid) {
				return true
			}
		}
		return false
	}
	return o.isSelfXID(hdr.Xmax)
}

// acquireTupleLockForWait takes the heavyweight tuple tag right before
// this op sleeps on a conflicting holder — upstream's heap_acquire_tuplock
// placement — so concurrent waiters on the same row are granted in FIFO
// arrival order when the holder ends. No-op when this backend is already
// a lock member on the tuple (upgrade; see selfIsLockMember). The tag is
// held until statement end (dispatch.go's executor.ReleaseTupleLocks),
// long enough for the winner to re-stamp before the next waiter re-probes.
func (o *lockRowsOp) acquireTupleLockForWait(rel storage.RelFileNode, ptr storage.ItemPointer, hdr storage.HeapTupleHeader) error {
	if o.selfIsLockMember(hdr) {
		return nil
	}
	return o.ctx.acquireTupleLock(rel, ptr, o.tupleLockMode())
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

// writerXID returns the (sub)transaction id this lock acquisition must be
// STAMPED under. Inside an open savepoint it is the current sub-XID — the
// statement's own context still carries the top-level XID (the per-statement
// ectx.Tx is rebuilt from the session's top-level transaction each Query), so
// the live sub-XID is read from the session. Recording the lock under the
// sub-XID is what lets ROLLBACK TO SAVEPOINT revert it: the aborted sub-XID's
// member drops out of the multixact, restoring the pre-savepoint lock strength.
// Mirrors heap_lock_tuple stamping GetCurrentTransactionId(). Outside a
// savepoint EffectiveWriterXID() == the top-level XID, so this is a strict
// no-op. M0118-0004 (docs/design/0118-0012).
func (o *lockRowsOp) writerXID() storage.TransactionID {
	if sess, ok := o.ctx.Session.(*BasicSession); ok {
		if x := sess.EffectiveWriterXID(); x != storage.InvalidTransactionID {
			return x
		}
	}
	return o.ctx.Tx.XID
}

// isSelfXID reports whether xid belongs to this backend's own transaction tree
// — the top-level xact or any of its sub-xacts. A subtransaction must never
// wait on, nor clobber, a lock its own parent or a sibling savepoint holds:
// when s1 upgrades FOR KEY SHARE → FOR SHARE → FOR NO KEY UPDATE across nested
// savepoints, each strength is a separate multixact member under a distinct
// sub-XID, and they are all "self". Because the statement's own o.ctx.Tx.XID is
// the top-level XID, the plain `m.Xid == o.ctx.Tx.XID` test would miss a member
// stamped under a sub-XID; resolving both sides to their top-level ancestor
// catches the whole tree. Reduces to xid == o.ctx.Tx.XID when no savepoint is
// open. M0118-0004 (docs/design/0118-0012).
func (o *lockRowsOp) isSelfXID(xid storage.TransactionID) bool {
	if xid == storage.InvalidTransactionID {
		return false
	}
	if xid == o.ctx.Tx.XID {
		return true
	}
	if o.ctx.TxnMgr == nil {
		return false
	}
	return o.ctx.TxnMgr.TopLevelXid(xid) == o.ctx.TxnMgr.TopLevelXid(o.ctx.Tx.XID)
}

// conflictingLockHolders returns the still-active row-lock holders on a tuple
// whose lock mode CONFLICTS with this op's requested mode — the subset of
// activeLockHolders a blocking waiter must actually wait on, mirroring upstream
// MultiXactIdWait, which sleeps only on members whose status conflicts
// (StatusesConflict) with the requested status rather than on every member. A
// non-conflicting co-holder (e.g. a FOR KEY SHARE member when we request FOR NO
// KEY UPDATE) must NOT be waited on, or the waiter blocks until that unrelated
// holder's whole transaction ends instead of becoming grantable as soon as the
// conflicting holder releases. In tuplelock-upgrade-no-deadlock perm 9 the
// conflicting holders are savepoint sub-locks released by ROLLBACK TO SAVEPOINT
// while the same backend's top-level FOR KEY SHARE persists: waiting only on the
// conflicting members lets the waiter wake and re-probe at the right step.
// M0118-0004 (docs/design/0118-0012).
func (o *lockRowsOp) conflictingLockHolders(xmax storage.TransactionID, infomask, infomask2 uint16) []storage.TransactionID {
	if o.ctx.TxnMgr == nil {
		return nil
	}
	req := o.lockMemberStatus()
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
			if o.isSelfXID(m.Xid) || !o.ctx.TxnMgr.IsXIDActive(m.Xid) {
				continue
			}
			if multixact.StatusesConflict(m.Status, req) {
				out = append(out, m.Xid)
			}
		}
		return out
	}
	if o.isSelfXID(xmax) || !o.ctx.TxnMgr.IsXIDActive(xmax) {
		return nil
	}
	if multixact.StatusesConflict(lockOnlyMemberStatus(infomask, infomask2), req) {
		return []storage.TransactionID{xmax}
	}
	return nil
}

// hasOuterSelfLockMember reports whether the lock-only xmax in `hdr` carries an
// active row-lock member belonging to THIS backend's transaction tree but
// stamped under a (sub)transaction OTHER than the current writerXID — i.e. an
// outer-level self lock (the top-level XID, or a parent savepoint's sub-XID)
// that a self lock-upgrade inside a savepoint must preserve rather than
// overwrite. Used to route such an upgrade through stampMultiLock so ROLLBACK TO
// the inner savepoint reverts to the outer strength instead of leaving the row
// unlocked (delete-abort-savept-2). A same-level self re-lock (the only member
// is our own current writerXID) returns false — the caller then takes the plain
// single-xmax stamp, unchanged. M0118-0009 (docs/design/0118-0015).
func (o *lockRowsOp) hasOuterSelfLockMember(hdr storage.HeapTupleHeader) bool {
	if o.ctx.TxnMgr == nil {
		return false
	}
	wxid := o.writerXID()
	if storage.IsHeapTupleXmaxMulti(hdr.Infomask) {
		if o.ctx.MultiXact == nil {
			return false
		}
		members, ok := o.ctx.MultiXact.Members(multixact.MultiXactId(hdr.Xmax))
		if !ok {
			return false
		}
		for _, m := range members {
			if m.Xid != wxid && o.isSelfXID(m.Xid) && o.ctx.TxnMgr.IsXIDActive(m.Xid) {
				return true
			}
		}
		return false
	}
	return hdr.Xmax != wxid && o.isSelfXID(hdr.Xmax) && o.ctx.TxnMgr.IsXIDActive(hdr.Xmax)
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
	// Re-add only the member stamped under our CURRENT (sub)transaction; an
	// outer-level self member (the top-level XID, or a parent savepoint's
	// sub-XID) is a still-held weaker lock that must be carried forward as a
	// survivor, so ROLLBACK TO an inner savepoint reverts to it. Exact-match on
	// writerXID (not isSelfXID) is deliberate: isSelfXID would collapse the whole
	// transaction tree into one member and lose the outer-level locks. M0118-0004.
	wxid := o.writerXID()
	survivors := make([]multixact.Member, 0, len(existing)+1)
	for _, m := range existing {
		if m.Xid == wxid {
			continue // re-added once below with our current strength
		}
		if o.ctx.TxnMgr != nil && o.ctx.TxnMgr.IsXIDActive(m.Xid) {
			survivors = append(survivors, m)
		}
	}
	if len(survivors) == 0 {
		return storage.ItemPointer{}, false, nil
	}
	survivors = append(survivors, multixact.Member{Xid: wxid, Status: o.lockMemberStatus()})

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
	o.ctx.Pool.MarkDirtyUnlogged(slot, "multixact lock stamp: transient in-memory membership, no WAL record (0118-0002)")
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
	wxid := o.writerXID()
	survivors := make([]multixact.Member, 0, len(existing)+1)
	for _, m := range existing {
		if m.Xid == wxid {
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
	survivors = append(survivors, multixact.Member{Xid: wxid, Status: o.lockMemberStatus()})

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
	o.ctx.Pool.MarkDirtyUnlogged(slot, "multixact lock stamp: transient in-memory membership, no WAL record (0118-0002)")
	return ptr, true, nil
}

// updaterXID returns the transaction id of the *updater* recorded in a tuple's
// xmax, or InvalidTransactionID when the xmax is invalid, lock-only, or a
// MultiXactId with no update member. goopg analog of HeapTupleHeaderGetUpdateXid.
func (o *lockRowsOp) updaterXID(hdr storage.HeapTupleHeader) storage.TransactionID {
	if hdr.Xmax == storage.InvalidTransactionID || storage.IsHeapTupleLockOnly(hdr.Infomask) {
		return storage.InvalidTransactionID
	}
	if storage.IsHeapTupleXmaxMulti(hdr.Infomask) {
		return multixactUpdaterXID(o.ctx.MultiXact, hdr.Xmax)
	}
	return hdr.Xmax
}

// chainLockOutcome mirrors the relevant TM_Result outcomes of
// heap_lock_updated_tuple_rec for the forward (locker-side) chain walk.
type chainLockOutcome int

const (
	// chainLockOK: the whole chain was locked (or its end / a vacuumed gap was
	// reached). The caller keeps the originally-locked snapshot version.
	chainLockOK chainLockOutcome = iota
	// chainLockDeleted (TM_Deleted): a committed conflicting DELETE ends the
	// chain. EvalPlanQual has nothing to return — the caller drops the row.
	chainLockDeleted
	// chainLockUpdated (TM_Updated): a committed conflicting key-UPDATE relocated
	// the row; the returned ItemPointer is the next (newer) version the caller
	// must re-check the WHERE predicate against (EvalPlanQualFetchRowMark).
	chainLockUpdated
)

// chainMembers returns the lock/update holder(s) recorded in a chain version's
// xmax as multixact members: the members of a MultiXactId xmax, or the single
// synthetic member of a non-multi xmax (a lock-only locker decoded via
// lockOnlyMemberStatus, or a real updater via updaterMemberStatus). `self` is the
// version's own ItemPointer, used to recognise a DELETE. Empty when the
// membership cannot be resolved (no store, or lost across a restart) — the
// caller then treats the version as having no live conflicting holder.
func (o *lockRowsOp) chainMembers(hdr storage.HeapTupleHeader, self storage.ItemPointer) []multixact.Member {
	if storage.IsHeapTupleXmaxMulti(hdr.Infomask) {
		if o.ctx.MultiXact == nil {
			return nil
		}
		members, ok := o.ctx.MultiXact.Members(multixact.MultiXactId(hdr.Xmax))
		if !ok {
			return nil
		}
		return members
	}
	if storage.IsHeapTupleLockOnly(hdr.Infomask) {
		return []multixact.Member{{Xid: hdr.Xmax, Status: lockOnlyMemberStatus(hdr.Infomask, hdr.Infomask2)}}
	}
	keysUpdated := (hdr.Infomask2 & storage.HeapKeysUpdated) != 0
	// A DELETE leaves t_ctid pointing at the tuple itself (no successor version).
	// goopg does not stamp HEAP_KEYS_UPDATED on delete the way PG's heap_delete
	// does (compute_new_xmax_infomask with LockTupleExclusive), so recognise the
	// delete structurally: it invalidates the key for every row lock and must be
	// classified as StatusUpdate so it conflicts with FOR KEY SHARE — not the
	// StatusNoKeyUpdate the cleared bit would otherwise imply (lock-update-delete
	// blocker1). M0118-0003.
	if !keysUpdated && (hdr.CTID.Block == storage.InvalidBlockNumber ||
		(hdr.CTID.Block == self.Block && hdr.CTID.Offset == self.Offset)) {
		keysUpdated = true
	}
	return []multixact.Member{{Xid: hdr.Xmax, Status: updaterMemberStatus(keysUpdated)}}
}

// classifyChainConflict is goopg's analog of test_lockmode_for_conflict applied
// over every holder recorded in a chain version's xmax, in member order (matching
// heap_lock_updated_tuple_rec's per-member short-circuit). For our requested
// strength (o.lockMemberStatus) it returns:
//   - needWait=true, waitXID: the first still-in-flight holder whose lock mode
//     conflicts — the caller must wait on it and re-read the version.
//   - committedConflict=true: the first committed *updater* whose lock mode
//     conflicts — the caller ends the walk with TM_Updated / TM_Deleted.
//   - both false: no conflicting holder — the caller may lock this version.
//
// A holder that is our own xact (already locked by us), aborted, or a committed
// pure locker (lock gone) is skipped, exactly as upstream does. M0118-0003.
func (o *lockRowsOp) classifyChainConflict(hdr storage.HeapTupleHeader, self storage.ItemPointer) (needWait bool, waitXID storage.TransactionID, committedConflict bool) {
	if o.ctx.TxnMgr == nil {
		return false, storage.InvalidTransactionID, false
	}
	reqStatus := o.lockMemberStatus()
	for _, m := range o.chainMembers(hdr, self) {
		if o.isSelfXID(m.Xid) {
			continue // already locked by us
		}
		switch {
		case o.ctx.TxnMgr.IsXIDActive(m.Xid):
			if multixact.StatusesConflict(m.Status, reqStatus) {
				return true, m.Xid, false
			}
		case o.ctx.TxnMgr.HasAbortedXID(m.Xid):
			// Aborted: its lock/update is gone — no conflict.
		default:
			// Committed: a pure locker's lock is gone, but a committed update
			// persists and conflicts if the modes do.
			if m.Status.IsUpdate() && multixact.StatusesConflict(m.Status, reqStatus) {
				return false, storage.InvalidTransactionID, true
			}
		}
	}
	return false, storage.InvalidTransactionID, false
}

// propagateLockForward walks the update chain forward from `start` (the t_ctid
// of a just-locked version that an updater had superseded) and applies our row
// lock to every newer version, so a later DELETE / key-UPDATE on the live
// successor honours the lock. This is goopg's analog of heap_lock_updated_tuple /
// heap_lock_updated_tuple_rec (postgres/src/backend/access/heap/heapam.c): it
// does NOT touch the initial tuple (the caller already stamped it) and it does
// not check visibility — it unconditionally marks each chain member as locked.
//
// Unlike a blind walk it honours the row-lock strength matrix per version: a
// conflicting in-flight updater is WAITED on (then the version is re-read), and
// a committed conflicting DELETE / key-UPDATE ends the walk with
// chainLockDeleted / chainLockUpdated so the blocked-then-woken locker
// (lock-update-delete s1l) re-evaluates against the latest version rather than
// returning the stale snapshot tuple.
//
// `priorXmax` is the update xid of the version we came from; each successor's
// xmin must equal it for the chain to be contiguous (a mismatch means the line
// pointer was recycled for an unrelated tuple, so we stop). lock-update-traversal.
func (o *lockRowsOp) propagateLockForward(rel storage.RelFileNode, start storage.ItemPointer, priorXmax storage.TransactionID) (chainLockOutcome, storage.ItemPointer, error) {
	cur := start
	px := priorXmax
	// The backstop bounds total iterations including wait-and-re-read retries
	// (each wait re-reads the SAME version once the waited xact finishes), so it
	// must exceed the pure chain-depth backstop in stampLockInner. Mirrors
	// heap_lock_updated_tuple_rec's unbounded loop made finite for safety.
	for hops := 0; hops < 64; hops++ {
		slot, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: cur.Block})
		if err != nil {
			return chainLockOK, storage.ItemPointer{}, err
		}
		slot.Lock()
		tup, gerr := storage.PageGetHeapTuple(slot.Page(), cur.Offset)
		if gerr != nil {
			// Successor pruned/vacuumed (its creator aborted): end of chain.
			slot.Unlock()
			o.ctx.Pool.Unpin(slot)
			return chainLockOK, storage.ItemPointer{}, nil
		}
		// Chain continuity: the successor must descend from the version we just
		// locked (its xmin == that version's update xid). Otherwise the line
		// pointer was recycled for an unrelated tuple — stop (heapam.c l4 check).
		if px != storage.InvalidTransactionID && tup.Header.Xmin != px {
			slot.Unlock()
			o.ctx.Pool.Unpin(slot)
			return chainLockOK, storage.ItemPointer{}, nil
		}
		// Created by an aborted (sub)xact: the prior version was the last live one
		// in the chain, so we are done.
		if o.ctx.TxnMgr != nil && o.ctx.TxnMgr.HasAbortedXID(tup.Header.Xmin) {
			slot.Unlock()
			o.ctx.Pool.Unpin(slot)
			return chainLockOK, storage.ItemPointer{}, nil
		}
		// Conflict test on this version's xmax — the analog of
		// test_lockmode_for_conflict inside heap_lock_updated_tuple_rec. A
		// conflicting in-flight holder forces a WAIT (then a re-read of the SAME
		// version, like its `goto l4`); a committed conflicting updater ends the
		// walk with TM_Updated / TM_Deleted.
		if tup.Header.Xmax != storage.InvalidTransactionID && !o.isSelfXID(tup.Header.Xmax) {
			needWait, waitXID, committedConflict := o.classifyChainConflict(tup.Header, cur)
			if needWait {
				slot.Unlock()
				o.ctx.Pool.Unpin(slot)
				qctx := o.ctx.Ctx
				if qctx == nil {
					qctx = context.Background()
				}
				if werr := o.ctx.TxnMgr.WaitForXID(qctx, waitXID); werr != nil {
					if ee := o.rowWaitTimeoutError(werr); ee != nil {
						return chainLockOK, storage.ItemPointer{}, ee
					}
					// Plain client cancel — give up on the chain walk silently;
					// the caller keeps the originally-locked version.
					return chainLockOK, storage.ItemPointer{}, nil
				}
				continue // re-read the same version (cur/px unchanged) — goto l4
			}
			if committedConflict {
				ctid := tup.Header.CTID
				deleted := ctid.Block == storage.InvalidBlockNumber ||
					(ctid.Block == cur.Block && ctid.Offset == cur.Offset)
				slot.Unlock()
				o.ctx.Pool.Unpin(slot)
				if deleted {
					return chainLockDeleted, storage.ItemPointer{}, nil
				}
				return chainLockUpdated, ctid, nil
			}
		}
		// No conflict (TM_Ok): stamp our lock on this version and advance.
		// Decide whether the chain continues from the ORIGINAL header, before
		// lockSuccessorVersion mutates this version's xmax.
		nextPtr := tup.Header.CTID
		forwardPtr := nextPtr.Block != storage.InvalidBlockNumber &&
			(nextPtr.Block != cur.Block || nextPtr.Offset != cur.Offset)
		wasUpdated := tup.Header.Xmax != storage.InvalidTransactionID &&
			!storage.IsHeapTupleLockOnly(tup.Header.Infomask) && forwardPtr
		nextPrior := o.updaterXID(tup.Header)
		if lerr := o.lockSuccessorVersion(slot, rel, cur, tup.Header); lerr != nil {
			slot.Unlock()
			o.ctx.Pool.Unpin(slot)
			return chainLockOK, storage.ItemPointer{}, lerr
		}
		slot.Unlock()
		o.ctx.Pool.Unpin(slot)
		if !wasUpdated || nextPrior == storage.InvalidTransactionID {
			return chainLockOK, storage.ItemPointer{}, nil // reached the latest version
		}
		cur = nextPtr
		px = nextPrior
	}
	return chainLockOK, storage.ItemPointer{}, nil
}

// lockSuccessorVersion applies our row lock to one update-chain member whose page
// is already pinned and exclusively locked by the caller. A foreign holder is
// combined into a MultiXactId (lock-only via stampMultiLock, updater-bearing via
// stampMultiUpdaterLock) rather than overwritten; when no live holder survives,
// our lock-only xmax is stamped directly. A surviving foreign *updater* that
// cannot be combined is left untouched so we never clobber a real updater's xmax.
func (o *lockRowsOp) lockSuccessorVersion(slot *storage.Slot, rel storage.RelFileNode, ptr storage.ItemPointer, hdr storage.HeapTupleHeader) error {
	if hdr.Xmax != storage.InvalidTransactionID && !o.isSelfXID(hdr.Xmax) && o.ctx.MultiXact != nil {
		if storage.IsHeapTupleLockOnly(hdr.Infomask) {
			if _, combined, err := o.stampMultiLock(slot, ptr, hdr); err != nil {
				return err
			} else if combined {
				return nil
			}
			// No live lock-only holder survived: safe to stamp our own below.
		} else {
			keysUpdated := (hdr.Infomask2 & storage.HeapKeysUpdated) != 0
			if _, formed, err := o.stampMultiUpdaterLock(slot, ptr, hdr, keysUpdated); err != nil {
				return err
			} else if formed {
				return nil
			}
			// A real updater that could not be combined (e.g. aborted) must not
			// be clobbered — leave its xmax intact.
			return nil
		}
	}
	wxid := o.writerXID()
	if err := storage.PageSetHeapTupleLockOnly(slot.Page(), ptr.Offset, wxid, o.lockStrength); err != nil {
		return err
	}
	if err := storage.PageSetHeapTupleLockKeysUpdated(slot.Page(), ptr.Offset, o.lockKeysUpdated); err != nil {
		return err
	}
	return markHeapLockDirty(o.ctx.Pool, slot, rel, ptr.Block, ptr.Offset, wxid, o.lockStrength)
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
