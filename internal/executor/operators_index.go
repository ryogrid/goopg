package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

type indexScanOp struct {
	plan *planner.IndexScan
	ctx  *Context
	rows []Row
	idx  int
}

func newIndexScanOp(p *planner.IndexScan) *indexScanOp {
	return &indexScanOp{plan: p}
}

func (o *indexScanOp) Schema() planner.Schema { return o.plan.Output() }

func (o *indexScanOp) Open(ctx *Context) error {
	if ctx.Pool == nil || ctx.Catalog == nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "IndexScan requires storage handles in Context"}
	}
	o.ctx = ctx
	o.rows = nil
	o.idx = 0

	heapRel := ctx.Catalog.RelFileNode(o.plan.Table)
	if err := ctx.acquireRelLock(heapRel, lockmgr.AccessShareLock); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.plan.Pos()
		}
		return err
	}

	key, ok, err := o.lookupKey()
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	idxRel := ctx.Catalog.IndexRelFileNode(o.plan.Index)
	tree, err := btree.Open(ctx.Pool, idxRel)
	if err != nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}
	err = tree.RangeScan(key, key, func(_ []byte, ptr storage.ItemPointer) (bool, error) {
		slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: heapRel, Block: ptr.Block})
		if err != nil {
			return false, err
		}
		slot.RLock()
		tuple, err := storage.PageGetHeapTuple(slot.Page(), ptr.Offset)
		slot.RUnlock()
		ctx.Pool.Unpin(slot)
		if err != nil {
			return false, err
		}
		if !mvcc.TupleVisible(tuple.Header, ctx.Snap, ctx.Tx.XID) {
			return true, nil
		}
		row, err := DecodeRow(o.plan.Table.Columns, tuple.Data)
		if err != nil {
			return false, err
		}
		o.rows = append(o.rows, row)
		return true, nil
	})
	if err != nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}
	return nil
}

func (o *indexScanOp) Next() (Row, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	r := o.rows[o.idx]
	o.idx++
	return r, nil
}

func (o *indexScanOp) Close() error {
	o.rows = nil
	return nil
}

func (o *indexScanOp) lookupKey() ([]byte, bool, error) {
	v, err := evalExpr(o.plan.Key, nil, o.ctx)
	if err != nil {
		return nil, false, err
	}
	if v.IsNull() {
		return nil, false, nil
	}
	// Look up the indexed column on the underlying table so the
	// probe encoding matches what backfill stored. The index is
	// always single-column in v0 (createSingleColumnBTreeIndex
	// enforces this).
	col, ok := o.ctx.Catalog.LookupColumn(o.plan.Table, o.plan.Index.Columns[0])
	if !ok {
		return nil, false, &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: fmt.Sprintf("indexed column %q not found on table %q", o.plan.Index.Columns[0], o.plan.Table.Name)}
	}
	key, encErr := encodeBTreeKeyForColumn(v, col, o.plan.Key.Pos())
	if encErr != nil {
		return nil, false, encErr
	}
	return key, true, nil
}
