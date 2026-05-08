package executor

import (
	"fmt"
	"time"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// indexOnlyScanOp implements IndexOnlyScan (M0046-0004): when the Visibility
// Map marks a heap page ALL_VISIBLE, the operator decodes projected column
// values from the B-tree key bytes and returns them without fetching the heap.
// For pages not yet marked ALL_VISIBLE it falls back to a full heap fetch
// so MVCC visibility is always respected.
type indexOnlyScanOp struct {
	plan *planner.IndexOnlyScan
	ctx  *Context
	rows []Row
	idx  int
	outSlot MaterializedSlot
}

func newIndexOnlyScanOp(p *planner.IndexOnlyScan) *indexOnlyScanOp {
	return &indexOnlyScanOp{plan: p}
}

func (o *indexOnlyScanOp) Schema() planner.Schema { return o.plan.Output() }

func (o *indexOnlyScanOp) Open(ctx *Context) error {
	if ctx.Pool == nil || ctx.Catalog == nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(),
			Message: "IndexOnlyScan requires storage handles"}
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

	idxRel := ctx.Catalog.IndexRelFileNode(o.plan.Index)
	tree, err := btree.Open(ctx.Pool, idxRel)
	if err != nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}

	var loBytes, hiBytes []byte
	if o.plan.Key != nil {
		key, ok, err := o.lookupKey()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		loBytes = key
		hiBytes = key
		if len(o.plan.Index.Columns) > 1 {
			hiBytes = appendCompositeUpperPadding(key)
		}
	} else {
		lo, hi, ok, err := o.lookupRangeBounds()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		loBytes = lo
		hiBytes = hi
		if len(o.plan.Index.Columns) > 1 && hiBytes != nil {
			hiBytes = appendCompositeUpperPadding(hiBytes)
		}
	}

	scanFn := func(key []byte, ptr storage.ItemPointer) (bool, error) {
		// Fast path: ALL_VISIBLE → decode from key, zero heap reads.
		if ctx.VM != nil && ctx.VM.AllVisible(heapRel, ptr.Block) {
			row, err := o.decodeRowFromKey(key)
			if err == nil {
				o.rows = append(o.rows, row)
			}
			return true, nil
		}

		// Fallback: heap fetch + HOT chain + MVCC.
		slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: heapRel, Block: ptr.Block})
		if err != nil {
			return false, err
		}
		slot.RLock()
		tuple, _, found := followHOTChain(slot.Page(), ptr.Offset, ctx.Snap, ctx.Tx.XID)
		slot.RUnlock()
		ctx.Pool.Unpin(slot)
		if !found {
			return true, nil
		}
		row, err := o.decodeRowFromHeap(tuple)
		if err != nil {
			return false, err
		}
		o.rows = append(o.rows, row)
		return true, nil
	}

	if err := tree.RangeScan(loBytes, hiBytes, scanFn); err != nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}
	return nil
}

func (o *indexOnlyScanOp) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	r := o.rows[o.idx]
	o.idx++
	return o.outSlot.set(r), nil
}

func (o *indexOnlyScanOp) Close() error {
	o.rows = nil
	return nil
}

// decodeRowFromKey extracts covered column values from a B-tree key.
// Only single-column indexes are fully supported (v0); multi-column returns
// an error so the caller falls back gracefully.
func (o *indexOnlyScanOp) decodeRowFromKey(key []byte) (Row, error) {
	if len(o.plan.Index.Columns) != 1 || len(o.plan.Covered) != 1 {
		return nil, fmt.Errorf("index-only scan: multi-column key decode not supported yet")
	}
	d, err := decodeBTreeKeyToDatum(key, o.plan.Covered[0])
	if err != nil {
		return nil, err
	}
	return Row{d}, nil
}

// decodeRowFromHeap projects only the covered columns from a full heap tuple.
func (o *indexOnlyScanOp) decodeRowFromHeap(t storage.HeapTuple) (Row, error) {
	fullRow, err := DecodeRow(o.plan.Table.Columns, t.Data)
	if err != nil {
		return nil, err
	}
	row := make(Row, len(o.plan.Covered))
	for i, col := range o.plan.Covered {
		for j, tc := range o.plan.Table.Columns {
			if tc.Name == col.Name && j < len(fullRow) {
				row[i] = fullRow[j]
				break
			}
		}
	}
	return row, nil
}

// decodeBTreeKeyToDatum inverts the B-tree key encoding for a single column
// back to an executor Datum.
func decodeBTreeKeyToDatum(key []byte, col catalog.Column) (Datum, error) {
	typeName := col.Type.Name
	switch {
	case isInt4Type(typeName):
		v, err := btree.DecodeInt4(key)
		if err != nil {
			return NullDatum, err
		}
		return Datum{Kind: KindInt, Int: int64(v)}, nil

	case isInt8Type(typeName):
		v, err := btree.DecodeInt8(key)
		if err != nil {
			return NullDatum, err
		}
		return Datum{Kind: KindInt, Int: v}, nil

	case isVarcharType(typeName), isCharType(typeName):
		b, err := btree.DecodeVarchar(key)
		if err != nil {
			return NullDatum, err
		}
		return NewStringDatum(string(b)), nil

	case isTimestampType(typeName):
		v, err := btree.DecodeTimestamp(key)
		if err != nil {
			return NullDatum, err
		}
		ts := pgEpoch.Add(time.Duration(v) * time.Microsecond)
		return NewTimeDatum(ts), nil

	default:
		return NullDatum, fmt.Errorf("index-only scan: unsupported key type %q for key decode", typeName)
	}
}

// lookupKey evaluates the equality probe key expression.
// Note: encodeBTreeKeyForColumn returns *ExecError (not error), so we
// must guard against the nil-pointer-in-interface issue explicitly.
func (o *indexOnlyScanOp) lookupKey() ([]byte, bool, error) {
	v, err := evalExpr(o.plan.Key, nil, o.ctx)
	if err != nil {
		return nil, false, err
	}
	if v.IsNull() {
		return nil, false, nil
	}
	col, ok := o.ctx.Catalog.LookupColumn(o.plan.Table, o.plan.Index.Columns[0])
	if !ok {
		return nil, false, &ExecError{Code: "XX000", Pos: o.plan.Pos(),
			Message: fmt.Sprintf("indexed column %q not found", o.plan.Index.Columns[0])}
	}
	k, encErr := encodeBTreeKeyForColumn(v, col, o.plan.Key.Pos())
	if encErr != nil {
		return nil, false, encErr
	}
	return k, true, nil
}

func (o *indexOnlyScanOp) lookupRangeBounds() (lo, hi []byte, ok bool, err error) {
	col, found := o.ctx.Catalog.LookupColumn(o.plan.Table, o.plan.Index.Columns[0])
	if !found {
		return nil, nil, false, &ExecError{Code: "XX000", Pos: o.plan.Pos(),
			Message: fmt.Sprintf("indexed column %q not found", o.plan.Index.Columns[0])}
	}
	if o.plan.LowKey != nil {
		v, evalE := evalExpr(o.plan.LowKey, nil, o.ctx)
		if evalE != nil {
			return nil, nil, false, evalE
		}
		if v.IsNull() {
			return nil, nil, false, nil
		}
		k, encE := encodeBTreeKeyForColumn(v, col, o.plan.LowKey.Pos())
		if encE != nil {
			return nil, nil, false, encE
		}
		lo = k
	}
	if o.plan.HighKey != nil {
		v, evalE := evalExpr(o.plan.HighKey, nil, o.ctx)
		if evalE != nil {
			return nil, nil, false, evalE
		}
		if v.IsNull() {
			return nil, nil, false, nil
		}
		k, encE := encodeBTreeKeyForColumn(v, col, o.plan.HighKey.Pos())
		if encE != nil {
			return nil, nil, false, encE
		}
		hi = k
	}
	return lo, hi, true, nil
}
