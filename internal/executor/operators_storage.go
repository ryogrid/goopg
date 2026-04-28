package executor

import (
	"errors"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

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
}

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
	n, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return err
	}
	o.nBlocks = n
	o.curBlock = 0
	o.curSlot = 0
	o.slotMax = 0
	return nil
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
	}
}

func (o *seqScanOp) releasePinned() {
	if o.pinned != nil {
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
	RowsAffected int64
	done         bool
}

func newInsertOp(p *planner.Insert, child Operator) *insertOp {
	return &insertOp{plan: p, child: child}
}

func (o *insertOp) Schema() planner.Schema { return nil }

func (o *insertOp) Open(ctx *Context) error {
	if ctx.Pool == nil || ctx.Catalog == nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "Insert requires storage handles in Context"}
	}
	o.ctx = ctx
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
		o.RowsAffected++
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
	RowsAffected int64
	done         bool
}

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
		if err := storage.PageSetHeapTupleXmax(s.Page(), pu.slot, o.ctx.Tx.XID); err != nil {
			o.ctx.Pool.Unpin(s)
			return nil, err
		}
		o.ctx.Pool.MarkDirty(s)
		o.ctx.Pool.Unpin(s)
		if err := writeHeapRow(o.ctx, rel, cols, pu.newRow); err != nil {
			return nil, err
		}
		o.RowsAffected++
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
	RowsAffected int64
	done         bool
}

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
		if err := storage.PageSetHeapTupleXmax(s.Page(), v.slot, o.ctx.Tx.XID); err != nil {
			o.ctx.Pool.Unpin(s)
			return nil, err
		}
		o.ctx.Pool.MarkDirty(s)
		o.ctx.Pool.Unpin(s)
		o.RowsAffected++
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
	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return err
	}
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return err
		}
		page := s.Page()
		if storage.IsNew(page) {
			ctx.Pool.Unpin(s)
			continue
		}
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
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
				ctx.Pool.Unpin(s)
				return err
			}
			if !mvcc.TupleVisible(tuple.Header, ctx.Snap, ctx.Tx.XID) {
				continue
			}
			row, err := DecodeRow(cols, tuple.Data)
			if err != nil {
				ctx.Pool.Unpin(s)
				return err
			}
			if pred != nil {
				v, err := evalExpr(pred, row, ctx)
				if err != nil {
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
func writeHeapRow(ctx *Context, rel storage.RelFileNode, cols []catalog.Column, row Row) error {
	body, err := EncodeRow(cols, row)
	if err != nil {
		return &ExecError{Code: "XX000", Message: err.Error()}
	}
	tuple := storage.NewHeapTuple(ctx.Tx.XID, storage.InvalidTransactionID, body)

	// Try the last existing block first.
	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return err
	}
	if nBlocks > 0 {
		blk := nBlocks - 1
		slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return err
		}
		if storage.IsNew(slot.Page()) {
			if err := storage.InitPage(slot.Page()); err != nil {
				ctx.Pool.Unpin(slot)
				return err
			}
		}
		if _, err := storage.PageAddHeapTuple(slot.Page(), tuple); err == nil {
			ctx.Pool.MarkDirty(slot)
			ctx.Pool.Unpin(slot)
			return nil
		} else if !errors.Is(err, storage.ErrNoSpaceInPage) {
			ctx.Pool.Unpin(slot)
			return err
		}
		ctx.Pool.Unpin(slot)
	}
	// Extend.
	slot, _, err := ctx.Pool.PinNew(rel)
	if err != nil {
		return err
	}
	if _, err := storage.PageAddHeapTuple(slot.Page(), tuple); err != nil {
		ctx.Pool.Unpin(slot)
		return err
	}
	ctx.Pool.MarkDirty(slot)
	ctx.Pool.Unpin(slot)
	return nil
}
