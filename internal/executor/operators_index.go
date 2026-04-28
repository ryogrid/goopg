package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/access/btree"
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
	heapRel := ctx.Catalog.RelFileNode(o.plan.Table)
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
	if v.Kind != KindInt {
		return nil, false, &ExecError{Code: "42804", Pos: o.plan.Key.Pos(), Message: "index lookup key must be integer"}
	}
	const minInt32 = -1 << 31
	const maxInt32 = 1<<31 - 1
	if v.Int < minInt32 || v.Int > maxInt32 {
		return nil, false, &ExecError{Code: "22003", Pos: o.plan.Key.Pos(), Message: fmt.Sprintf("integer %d out of int4 range", v.Int)}
	}
	return btree.EncodeInt4(int32(v.Int)), true, nil
}
