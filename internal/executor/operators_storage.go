package executor

import (
	"errors"
	"sync"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

var heapExtendLocks sync.Map // map[storage.RelFileNode]*sync.Mutex
var scanMatchLocks sync.Map  // map[storage.RelFileNode]*sync.Mutex

func lockHeapExtend(rel storage.RelFileNode) func() {
	v, _ := heapExtendLocks.LoadOrStore(rel, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func lockScanMatch(rel storage.RelFileNode) func() {
	v, _ := scanMatchLocks.LoadOrStore(rel, &sync.Mutex{})
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
				if errors.Is(err, storage.ErrUnsupportedItem) {
					continue
				}
				return nil, err
			}
			if !mvcc.TupleVisible(tuple.Header, o.ctx.Snap, o.ctx.Tx.XID) {
				continue
			}
			row, err := DecodeRow(o.cols, tuple.Data)
			if err != nil {
				return nil, err
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
// out the underlying SeqScan plus an optional predicate. v0 only
// emits Filter(SeqScan) or bare SeqScan, so anything else is a
// planner bug we surface explicitly.
func extractScanAndPredicate(child planner.Node) (*planner.SeqScan, planner.Expr, error) {
	switch c := child.(type) {
	case *planner.SeqScan:
		return c, nil, nil
	case *planner.Filter:
		scan, ok := c.Child.(*planner.SeqScan)
		if !ok {
			return nil, nil, &ExecError{Code: "XX000", Pos: c.Pos(), Message: "Update/Delete: Filter child is not SeqScan"}
		}
		return scan, c.Predicate, nil
	}
	return nil, nil, &ExecError{Code: "XX000", Pos: child.Pos(), Message: "Update/Delete: unsupported child plan"}
}

// updateOp scans the target relation and rewrites visible matching
// tuples. v0 uses upstream's "delete + insert" pattern: stamp xmax on
// the old tuple, write the new image as a fresh heap tuple. HOT
// chains and same-page updates are deferred.
type updateOp struct {
	plan         *planner.Update
	scan         *planner.SeqScan
	pred         planner.Expr
	ctx          *Context
	rowsAffected int64
	done         bool
}

// RowsAffected satisfies executor.RowCounter.
func (o *updateOp) RowsAffected() int64 { return o.rowsAffected }

func newUpdateOp(p *planner.Update) (*updateOp, error) {
	scan, pred, err := extractScanAndPredicate(p.Child)
	if err != nil {
		return nil, err
	}
	return &updateOp{plan: p, scan: scan, pred: pred}, nil
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

func (o *updateOp) Next() (Row, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true
	tbl := o.plan.Table
	cols := tbl.Columns
	rel := o.ctx.Catalog.RelFileNode(tbl)

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
	var pending []pendingUpdate

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
	for _, pu := range pending {
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
}

// RowsAffected satisfies executor.RowCounter.
func (o *deleteOp) RowsAffected() int64 { return o.rowsAffected }

func newDeleteOp(p *planner.Delete) (*deleteOp, error) {
	scan, pred, err := extractScanAndPredicate(p.Child)
	if err != nil {
		return nil, err
	}
	return &deleteOp{plan: p, scan: scan, pred: pred}, nil
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

func scanMatching(ctx *Context, rel storage.RelFileNode, cols []catalog.Column, pred planner.Expr, fn func(blk storage.BlockNumber, slot uint16, row Row) error) error {
	unlock := lockScanMatch(rel)
	defer unlock()

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
		var matches []struct {
			slot uint16
			row  Row
		}
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
			row, err := DecodeRow(cols, tuple.Data)
			if err != nil {
				s.RUnlock()
				ctx.Pool.Unpin(s)
				return err
			}
			if pred != nil {
				v, err := evalExpr(pred, row, ctx)
				if err != nil {
					s.RUnlock()
					ctx.Pool.Unpin(s)
					return err
				}
				if v.IsNull() || v.Kind != KindBool || !v.Bool {
					continue
				}
			}
			matches = append(matches, struct {
				slot uint16
				row  Row
			}{slot: slot, row: row})
		}
		s.RUnlock()
		ctx.Pool.Unpin(s)
		for _, m := range matches {
			if err := fn(blk, m.slot, m.row); err != nil {
				return err
			}
		}
	}
	return nil
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
	body, err := EncodeRow(cols, row)
	if err != nil {
		return &ExecError{Code: "XX000", Message: err.Error()}
	}
	tuple := storage.NewHeapTuple(ctx.Tx.XID, storage.InvalidTransactionID, body)
	tupleBytes, err := tuple.MarshalBinary()
	if err != nil {
		return err
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
		return err
	}
	if nBlocks > 0 {
		appended, err := tryAppendToBlock(nBlocks - 1)
		if err != nil {
			return err
		}
		if appended {
			return nil
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
		return err
	}
	if nBlocks > 0 {
		appended, err := tryAppendToBlock(nBlocks - 1)
		if err != nil {
			return err
		}
		if appended {
			return nil
		}
	}

	slot, blk, err := ctx.Pool.PinNew(rel)
	if err != nil {
		return err
	}
	slot.Lock()
	lineSlot, err := storage.PageAddHeapTuple(slot.Page(), tuple)
	if err != nil {
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		return err
	}
	derr := markHeapInsertDirty(ctx.Pool, slot, logHeap, rel, blk, lineSlot, tupleBytes)
	slot.Unlock()
	ctx.Pool.Unpin(slot)
	return derr
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
