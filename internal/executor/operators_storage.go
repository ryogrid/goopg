package executor

import (
	"errors"
	"fmt"
	"sync"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

var heapExtendLocks sync.Map // map[storage.RelFileNode]*sync.Mutex

func lockHeapExtend(rel storage.RelFileNode) func() {
	v, _ := heapExtendLocks.LoadOrStore(rel, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// seqScanOp walks every block of a heap relation, yielding visible
// tuples decoded into the planner's column ordering. Visibility is
// checked against ctx.Snap; tuples whose xmin/xmax are outside the
// snapshot's horizon are skipped.
type seqScanOp struct {
	plan *planner.SeqScan
	ctx  *Context
	cols []catalog.Column

	nBlocks  storage.BlockNumber
	curBlock storage.BlockNumber
	curSlot  uint16
	slotMax  int
	pinned   *storage.Slot

	// prefetchedThru is the highest block (exclusive) we've
	// already issued a Pool.Prefetch hint for. SeqScan walks
	// blocks strictly forward, so the prefetcher just needs to
	// keep `seqScanLookahead` blocks ahead of curBlock.
	prefetchedThru storage.BlockNumber
}

// seqScanLookahead is the number of blocks ahead of the current
// scan position seqScanOp keeps prefetched. Mirrors upstream's
// `effective_io_concurrency` default scope and is enough to
// pipeline a single sequential scan against typical SSD
// latencies. A future loop turns this into a tunable GUC.
const seqScanLookahead storage.BlockNumber = 4

func newSeqScanOp(p *planner.SeqScan) *seqScanOp {
	return &seqScanOp{plan: p, cols: p.Table.Columns}
}

func (o *seqScanOp) Schema() planner.Schema { return o.plan.Output() }

func (o *seqScanOp) Open(ctx *Context) error {
	if ctx.Pool == nil || ctx.Catalog == nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "SeqScan requires storage handles in Context"}
	}
	o.ctx = ctx
	rel := ctx.Catalog.RelFileNode(o.plan.Table)
	if err := ctx.acquireRelLock(rel, lockmgr.AccessShareLock); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.plan.Pos()
		}
		return err
	}
	n, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return err
	}
	o.nBlocks = n
	o.curBlock = 0
	o.curSlot = 0
	o.slotMax = 0
	o.prefetchedThru = 0
	o.refillPrefetchWindow(rel)
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
	if o.pinned != nil {
		o.ctx.Pool.Unpin(o.pinned)
		o.pinned = nil
	}
	return nil
}

// nextVisible advances through (block, slot) pairs and returns the
// next tuple visible to the snapshot, or EOF.
func (o *seqScanOp) Next() (Row, error) {
	rel := o.ctx.Catalog.RelFileNode(o.plan.Table)
	for {
		if o.pinned == nil {
			if o.curBlock >= o.nBlocks {
				return nil, EOF
			}
			slot, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: o.curBlock})
			if err != nil {
				return nil, err
			}
			// Hold the page's read lock for the lifetime of our
			// iteration over its line pointers so writers
			// (PageAddHeapTuple / PageSetHeapTupleXmax) can't tear
			// the bytes we're decoding. RUnlock fires from
			// releasePinned.
			slot.RLock()
			o.pinned = slot
			page := slot.Page()
			if storage.IsNew(page) {
				o.releasePinned()
				o.curBlock++
				continue
			}
			count, err := storage.PageLinePointerCount(page)
			if err != nil {
				o.releasePinned()
				return nil, err
			}
			o.slotMax = count
			o.curSlot = 1
		}
		for int(o.curSlot) <= o.slotMax {
			page := o.pinned.Page()
			tuple, err := storage.PageGetHeapTuple(page, o.curSlot)
			o.curSlot++
			if err != nil {
				// Corrupt or unsupported tuples are silently
				// skipped — scanning should not fail on
				// partial page writes or WAL-replay debris.
				continue
			}
			if !mvcc.TupleVisible(tuple.Header, o.ctx.Snap, o.ctx.Tx.XID) {
				continue
			}
			row, err := DecodeRow(o.cols, tuple.Data)
			if err != nil {
				continue
			}
			return row, nil
		}
		o.releasePinned()
		o.curBlock++
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
	rel := o.ctx.Catalog.RelFileNode(o.plan.Table)
	return rel, storage.ItemPointer{Block: o.curBlock, Offset: o.curSlot - 1}, true
}

func (o *seqScanOp) releasePinned() {
	if o.pinned != nil {
		o.pinned.RUnlock()
		o.ctx.Pool.Unpin(o.pinned)
		o.pinned = nil
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
}

// RowsAffected satisfies executor.RowCounter.
func (o *insertOp) RowsAffected() int64 { return o.rowsAffected }

func newInsertOp(p *planner.Insert, child Operator) *insertOp {
	return &insertOp{plan: p, child: child}
}

func (o *insertOp) Schema() planner.Schema { return nil }

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

// Next runs the insert as a one-shot side effect on first call; the
// wire-protocol path then issues `INSERT N` rather than streaming
// rows back. RETURNING is deferred — see fix_plan.
func (o *insertOp) Next() (Row, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true
	rel := o.ctx.Catalog.RelFileNode(o.plan.Table)
	cols := o.plan.Table.Columns
	for {
		src, err := o.child.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		// Reorder source row -> table column order via plan.ColumnIndex.
		row := make(Row, len(cols))
		for i := range cols {
			row[i] = NullDatum
		}
		for srcIdx, tgtIdx := range o.plan.ColumnIndex {
			row[tgtIdx] = src[srcIdx]
		}
		if err := writeHeapRow(o.ctx, rel, cols, row); err != nil {
			return nil, err
		}
		o.rowsAffected++
	}
	return nil, EOF
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
					Op:    "AND",
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
				Op:    "=",
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
	return pool.MarkDirtyChangeRecord(slot, func() (storage.LSN, error) {
		return logHot(rel, blk, oldLineSlot, xmax, tupleBytes)
	})
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
	body, err := EncodeRow(cols, newRow)
	if err != nil {
		return false, &ExecError{Code: "XX000", Message: err.Error()}
	}
	tup := storage.NewHeapTuple(ctx.Tx.XID, storage.InvalidTransactionID, body)
	tup.Header.Infomask |= storage.HeapOnlyTuple
	tupleBytes, err := tup.MarshalBinary()
	if err != nil {
		return false, err
	}

	s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
	if err != nil {
		return false, err
	}
	s.Lock()

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

	if err := storage.PageStampHotOldTuple(s.Page(), oldSlot, ctx.Tx.XID, blk, newSlot); err != nil {
		s.Unlock()
		ctx.Pool.Unpin(s)
		return false, err
	}

	derr := markHeapHotUpdateDirty(ctx.Pool, s, rel, blk, oldSlot, ctx.Tx.XID, tupleBytes)
	s.Unlock()
	ctx.Pool.Unpin(s)
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

func (o *updateOp) Schema() planner.Schema { return nil }

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
func (o *updateOp) updateViaIndex(rel storage.RelFileNode, cols []catalog.Column) (Row, error) {
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
	}
	pending := make([]pendingUpdate, 0, 1) // pre-alloc for common 1-row match
	heapRel := rel

	err = tree.RangeScan(keyBytes, keyBytes, func(_ []byte, ptr storage.ItemPointer) (bool, error) {
		slot, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: heapRel, Block: ptr.Block})
		if err != nil {
			return false, err
		}
		slot.RLock()
		// Follow the HOT chain: the index pointer may be stale (pointing
		// to an earlier version whose CTID leads to the live version).
		tuple, actualSlot, found := followHOTChain(slot.Page(), ptr.Offset, o.ctx.Snap, o.ctx.Tx.XID)
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
			tuple, actualSlot, found = followHOTChain(slot2.Page(), ptr.Offset, o.ctx.Snap, o.ctx.Tx.XID)
			slot2.RUnlock()
			o.ctx.Pool.Unpin(slot2)
			if !found {
				return true, nil
			}
		}
		row, err := DecodeRow(cols, tuple.Data)
		if err != nil {
			return false, err
		}

		// Build new row from SET expressions.
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
		pending = append(pending, pendingUpdate{
			blk:    ptr.Block,
			slot:   actualSlot, // use live slot, not the index-pointed slot
			newRow: newRow,
		})
		return true, nil
	})
	if err != nil {
		return nil, err
	}

	// Modification phase: HOT update when eligible, else delete+insert.
	hotEligible := hotUpdateEligible(o.plan, o.ctx)
	for _, pu := range pending {
		used := false
		if hotEligible {
			var err error
			used, err = tryApplyHOTUpdate(o.ctx, rel, cols, pu.blk, pu.slot, pu.newRow)
			if err != nil {
				return nil, err
			}
		}
		if !used {
			// HOT ineligible or page full — fall back to normal delete+insert.
			s, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: pu.blk})
			if err != nil {
				return nil, err
			}
			s.Lock()
			if err := storage.PageSetHeapTupleXmax(s.Page(), pu.slot, o.ctx.Tx.XID); err != nil {
				s.Unlock()
				o.ctx.Pool.Unpin(s)
				return nil, err
			}
			derr := markHeapDeleteDirty(o.ctx.Pool, s, rel, pu.blk, pu.slot, o.ctx.Tx.XID)
			s.Unlock()
			o.ctx.Pool.Unpin(s)
			if derr != nil {
				return nil, derr
			}
			if err := writeHeapRow(o.ctx, rel, cols, pu.newRow); err != nil {
				return nil, err
			}
		}
		o.rowsAffected++
	}
	return nil, EOF
}

func (o *updateOp) Next() (Row, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true
	tbl := o.plan.Table
	cols := tbl.Columns
	rel := o.ctx.Catalog.RelFileNode(tbl)

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
		blk    storage.BlockNumber
		slot   uint16
		newRow Row
	}
	pending := make([]pendingUpdate, 0, 1) // pre-alloc for common 1-row match

	if err := o.scanForMatches(rel, cols, func(blk storage.BlockNumber, slot uint16, row Row) error {
		newRow := make(Row, len(cols))
		for i := range cols {
			if o.plan.Set[i] == nil {
				newRow[i] = row[i]
				continue
			}
			v, err := evalExpr(o.plan.Set[i], row, o.ctx)
			if err != nil {
				return err
			}
			newRow[i] = v
		}
		pending = append(pending, pendingUpdate{blk: blk, slot: slot, newRow: newRow})
		return nil
	}); err != nil {
		return nil, err
	}
	hotEligibleSeq := hotUpdateEligible(o.plan, o.ctx)
	for _, pu := range pending {
		used := false
		if hotEligibleSeq {
			var err error
			used, err = tryApplyHOTUpdate(o.ctx, rel, cols, pu.blk, pu.slot, pu.newRow)
			if err != nil {
				return nil, err
			}
		}
		if !used {
			s, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: pu.blk})
			if err != nil {
				return nil, err
			}
			s.Lock()
			if err := storage.PageSetHeapTupleXmax(s.Page(), pu.slot, o.ctx.Tx.XID); err != nil {
				s.Unlock()
				o.ctx.Pool.Unpin(s)
				return nil, err
			}
			derr := markHeapDeleteDirty(o.ctx.Pool, s, rel, pu.blk, pu.slot, o.ctx.Tx.XID)
			s.Unlock()
			o.ctx.Pool.Unpin(s)
			if derr != nil {
				return nil, derr
			}
			if err := writeHeapRow(o.ctx, rel, cols, pu.newRow); err != nil {
				return nil, err
			}
		}
		o.rowsAffected++
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

func (o *deleteOp) Schema() planner.Schema { return nil }

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

func (o *deleteOp) Next() (Row, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true
	tbl := o.plan.Table
	cols := tbl.Columns
	rel := o.ctx.Catalog.RelFileNode(tbl)

	type victim struct {
		blk  storage.BlockNumber
		slot uint16
	}
	var victims []victim
	if err := o.scanForMatches(rel, cols, func(blk storage.BlockNumber, slot uint16, _ Row) error {
		victims = append(victims, victim{blk: blk, slot: slot})
		return nil
	}); err != nil {
		return nil, err
	}
	for _, v := range victims {
		s, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: v.blk})
		if err != nil {
			return nil, err
		}
		s.Lock()
		if err := storage.PageSetHeapTupleXmax(s.Page(), v.slot, o.ctx.Tx.XID); err != nil {
			s.Unlock()
			o.ctx.Pool.Unpin(s)
			return nil, err
		}
		derr := markHeapDeleteDirty(o.ctx.Pool, s, rel, v.blk, v.slot, o.ctx.Tx.XID)
		s.Unlock()
		o.ctx.Pool.Unpin(s)
		if derr != nil {
			return nil, derr
		}
		o.rowsAffected++
	}
	return nil, EOF
}

// scanForMatches walks every block/slot of rel, decodes visible
// tuples, evaluates the operator's predicate, and invokes fn for
// each match. Blocks are unpinned before the next iteration so
// pendingUpdate's downstream Pin doesn't deadlock against itself.
func (o *updateOp) scanForMatches(rel storage.RelFileNode, cols []catalog.Column, fn func(blk storage.BlockNumber, slot uint16, row Row) error) error {
	return scanMatching(o.ctx, rel, cols, o.pred, fn)
}

func (o *deleteOp) scanForMatches(rel storage.RelFileNode, cols []catalog.Column, fn func(blk storage.BlockNumber, slot uint16, row Row) error) error {
	return scanMatching(o.ctx, rel, cols, o.pred, fn)
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
		matches := make([]struct {
			slot     uint16
			row      Row
			lockedBy storage.TransactionID
		}, 0, 1)
		// Reusable row buffer — DecodeRowInto fills it without
		// allocating (M0027-0001).  Copy into matches only for
		// tuples that pass the predicate.
		scanRow := make(Row, len(cols))
		for slot := uint16(1); slot <= uint16(count); slot++ {
			tuple, err := storage.PageGetHeapTuple(page, slot)
			if err != nil {
				if errors.Is(err, storage.ErrUnsupportedItem) {
					continue
				}
				s.RUnlock()
				ctx.Pool.Unpin(s)
				return err
			}
			if !mvcc.TupleVisible(tuple.Header, ctx.Snap, ctx.Tx.XID) {
				continue
			}
			if err := DecodeRowInto(scanRow, cols, tuple.Data); err != nil {
				s.RUnlock()
				ctx.Pool.Unpin(s)
				return err
			}
			if pred != nil {
				v, err := evalExpr(pred, scanRow, ctx)
				if err != nil {
					s.RUnlock()
					ctx.Pool.Unpin(s)
					return err
				}
				if v.IsNull() || v.Kind != KindBool || !v.Bool {
					continue
				}
			}
			// Matching tuple — copy the row (scanRow is reused).
			matchedRow := make(Row, len(cols))
			copy(matchedRow, scanRow)
			matches = append(matches, struct {
				slot     uint16
				row      Row
				lockedBy storage.TransactionID
			}{slot: slot, row: matchedRow, lockedBy: lockedByForeign(tuple.Header, ctx.Tx.XID)})
		}
		s.RUnlock()
		ctx.Pool.Unpin(s)
		for _, m := range matches {
			// M0021 step 2b: if the tuple is row-locked by
			// another live xact (HEAP_XMAX_LOCK_ONLY +
			// xmax != ours), block on the locker's tuple-tag
			// in the lockmgr. ReleaseAll on the locker's
			// commit/abort wakes us up; we then proceed
			// with the UPDATE / DELETE atomic stamp.
			if m.lockedBy != storage.InvalidTransactionID {
				ptr := storage.ItemPointer{Block: blk, Offset: m.slot}
				if err := ctx.acquireTupleLock(rel, ptr, lockmgr.ExclusiveLock); err != nil {
					return err
				}
			}
			if err := fn(blk, m.slot, m.row); err != nil {
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
	body, err := EncodeRow(cols, row)
	if err != nil {
		return ptr, &ExecError{Code: "XX000", Message: err.Error()}
	}
	tuple := storage.NewHeapTuple(ctx.Tx.XID, storage.InvalidTransactionID, body)
	tupleBytes, err := tuple.MarshalBinary()
	if err != nil {
		return ptr, err
	}

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
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		return false, nil
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
	// tail block under heavy insert workloads.
	unlock := lockHeapExtend(rel)
	defer unlock()

	// Re-check after taking the extension lock; another writer may
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
	return pool.MarkDirtyChangeRecord(slot, func() (storage.LSN, error) {
		return logHeap(rel, blk, lineSlot, tupleBytes)
	})
}

// markHeapDeleteDirty mirrors markHeapInsertDirty for the xmax
// stamp paths (UPDATE old image + DELETE). When the pool has a
// LogHeapDelete hook configured, subsequent dirties of the same
// page in an epoch emit a fixed-size 20-byte logical record
// instead of a full FPI.
func markHeapDeleteDirty(
	pool *storage.Pool, slot *storage.Slot,
	rel storage.RelFileNode, blk storage.BlockNumber,
	lineSlot uint16, xmax storage.TransactionID,
) error {
	logDel := pool.LogHeapDelete()
	if logDel == nil {
		pool.MarkDirty(slot)
		return nil
	}
	return pool.MarkDirtyChangeRecord(slot, func() (storage.LSN, error) {
		return logDel(rel, blk, lineSlot, xmax)
	})
}
