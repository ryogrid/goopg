package executor

import (
	"fmt"
	"strconv"
	"strings"
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
	// M0092-0007: embedded slot reused across every Next() call
	// so we don't allocate a fresh MaterializedSlot per emission.
	slot MaterializedSlot
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
	// M0092-0007: stack-aliased slot reused across Next() calls.
	o.slot.schema = o.Schema()
	o.slot.row = r
	return &o.slot, nil
}

func (o *indexOnlyScanOp) Close() error {
	o.rows = nil
	return nil
}

// decodeRowFromKey extracts covered column values from a B-tree key.
func (o *indexOnlyScanOp) decodeRowFromKey(key []byte) (Row, error) {
	if len(o.plan.Index.Columns) == 1 && len(o.plan.Covered) == 1 {
		d, err := decodeBTreeKeyToDatum(key, o.plan.Covered[0])
		if err != nil {
			return nil, err
		}
		return Row{d}, nil
	}
	// Multi-column: decode all key columns in declaration order, then project.
	decoded := make(map[string]Datum, len(o.plan.Index.Columns))
	off := 0
	for _, colName := range o.plan.Index.Columns {
		col, ok := o.ctx.Catalog.LookupColumn(o.plan.Table, colName)
		if !ok {
			return nil, fmt.Errorf("IOS: index column %q not in catalog", colName)
		}
		d, n, err := decodeIndexKeyColumn(key[off:], *col)
		if err != nil {
			return nil, fmt.Errorf("IOS key col %q: %w", colName, err)
		}
		decoded[colName] = d
		off += n
	}
	row := make(Row, len(o.plan.Covered))
	for i, col := range o.plan.Covered {
		d, ok := decoded[col.Name]
		if !ok {
			return nil, fmt.Errorf("IOS: covered column %q not decoded", col.Name)
		}
		row[i] = d
	}
	return row, nil
}

// decodeIndexKeyColumn decodes one column from a B-tree key slice and returns
// the Datum plus the number of bytes consumed. Used by the multi-column path.
func decodeIndexKeyColumn(key []byte, col catalog.Column) (Datum, int, error) {
	typeName := col.Type.Name
	switch {
	case isInt4Type(typeName):
		v, err := btree.DecodeInt4(key)
		return Datum{Kind: KindInt, Int: int64(v)}, 4, err
	case isInt8Type(typeName):
		v, err := btree.DecodeInt8(key)
		return Datum{Kind: KindInt, Int: v}, 8, err
	case isFloat8Type(typeName):
		v, err := btree.DecodeFloat8(key)
		return NewStringDatum(strconv.FormatFloat(v, 'g', -1, 64)), 8, err
	case isTimestampType(typeName):
		v, err := btree.DecodeTimestamp(key)
		ts := pgEpoch.Add(time.Duration(v) * time.Microsecond)
		return NewTimeDatum(ts), 8, err
	case isVarcharType(typeName), isCharType(typeName), isTextType(typeName), isNameType(typeName),
		strings.ToLower(typeName) == "uuid":
		raw, n, err := btree.DecodeVarcharLen(key)
		return NewStringDatum(string(raw)), n, err
	default:
		return NullDatum, 0, fmt.Errorf("IOS: unsupported key type %q", typeName)
	}
}

// decodeRowFromHeap projects only the covered columns from a full heap tuple.
func (o *indexOnlyScanOp) decodeRowFromHeap(t storage.HeapTuple) (Row, error) {
	fullRow, err := DecodeHeapTupleRow(o.plan.Table.Columns, t, nil)
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

	case isFloat8Type(typeName):
		v, err := btree.DecodeFloat8(key)
		if err != nil {
			return NullDatum, err
		}
		return NewStringDatum(strconv.FormatFloat(v, 'g', -1, 64)), nil

	case isVarcharType(typeName), isCharType(typeName), isTextType(typeName), isNameType(typeName):
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
