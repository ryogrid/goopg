package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// followHOTChain walks the HOT chain starting at startSlot on the given
// page and returns the first visible tuple along with its actual slot.
// Returns (HeapTuple{}, 0, false) when no visible tuple exists in the chain.
//
// HOT invariant: all versions in a chain reside on the same page, so no
// additional I/O is needed. The caller must hold at least a read lock on
// the page for the duration of this call.
func followHOTChain(page storage.Page, startSlot uint16, snap mvcc.Snapshot, xid storage.TransactionID) (storage.HeapTuple, uint16, bool) {
	const maxChain = 64
	cur := startSlot
	for i := 0; i < maxChain; i++ {
		t, err := storage.PageGetHeapTuple(page, cur)
		if err != nil {
			return storage.HeapTuple{}, 0, false
		}
		if mvcc.TupleVisible(t.Header, snap, xid) {
			return t, cur, true
		}
		if t.Header.Infomask&storage.HeapHotUpdated == 0 {
			// Chain end: tuple is not visible and has no successor.
			return storage.HeapTuple{}, 0, false
		}
		next := t.Header.CTID.Offset
		if next == cur {
			return storage.HeapTuple{}, 0, false // self-reference guard
		}
		cur = next
	}
	return storage.HeapTuple{}, 0, false
}

type indexScanOp struct {
	plan *planner.IndexScan
	ctx  *Context
	rows []Row
	// tids parallels rows: tids[i] is the heap (block, slot) the
	// i-th matched row was decoded from. Captured during the
	// btree.RangeScan callback so SELECT FOR UPDATE / FOR SHARE
	// (M0021 step 2c) can stamp per-row lock-only xmax via
	// lockRowsOp.currentTID after Next emits the row.
	tids []storage.ItemPointer
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
	o.tids = nil
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
		// Equality scan: probe key is both lo and hi.
		key, ok, err := o.lookupKey()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		loBytes = key
		hiBytes = key
	} else {
		// Range scan: evaluate lo/hi bounds independently.
		lo, hiB, ok, err := o.lookupRangeBounds()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		loBytes = lo
		hiBytes = hiB
	}

	scanFn := func(_ []byte, ptr storage.ItemPointer) (bool, error) {
		slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: heapRel, Block: ptr.Block})
		if err != nil {
			return false, err
		}
		slot.RLock()
		// Follow the HOT chain: the index still points to the original
		// tuple location even after HOT updates. followHOTChain walks
		// CTID links (same page) until it finds the live version.
		tuple, actualSlot, found := followHOTChain(slot.Page(), ptr.Offset, ctx.Snap, ctx.Tx.XID)
		slot.RUnlock()
		ctx.Pool.Unpin(slot)
		if !found {
			return true, nil
		}
		row, err := DecodeRow(o.plan.Table.Columns, tuple.Data)
		if err != nil {
			return false, err
		}
		o.rows = append(o.rows, row)
		// Record the actual live slot (not the index-pointed slot) so
		// lockRowsOp stamps the current version for SELECT FOR UPDATE.
		o.tids = append(o.tids, storage.ItemPointer{Block: ptr.Block, Offset: actualSlot})
		return true, nil
	}

	if err := tree.RangeScan(loBytes, hiBytes, scanFn); err != nil {
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
	o.tids = nil
	return nil
}

// currentTID returns the (rel, ItemPointer) of the most recently
// emitted row, or ok=false before the first Next() call / past
// EOF. Mirrors seqScanOp.currentTID for the index-scan path so
// lockRowsOp can stamp per-row lock-only xmax (M0021 step 2c).
// idx points one past the last-returned row (Next increments
// post-fetch), so the just-returned row's TID is at idx-1.
func (o *indexScanOp) currentTID() (storage.RelFileNode, storage.ItemPointer, bool) {
	if o.idx == 0 || o.idx > len(o.tids) {
		return storage.RelFileNode{}, storage.ItemPointer{}, false
	}
	rel := o.ctx.Catalog.RelFileNode(o.plan.Table)
	return rel, o.tids[o.idx-1], true
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

// lookupRangeBounds evaluates LowKey and HighKey for a range scan.
// Returns (loKey, hiKey, ok, err). ok=false when a non-nil bound
// evaluates to NULL (the scan should produce no rows). Either loKey
// or hiKey may be nil for an open-ended range.
func (o *indexScanOp) lookupRangeBounds() (loKey []byte, hiKey []byte, ok bool, err error) {
	col, found := o.ctx.Catalog.LookupColumn(o.plan.Table, o.plan.Index.Columns[0])
	if !found {
		return nil, nil, false, &ExecError{
			Code:    "XX000",
			Pos:     o.plan.Pos(),
			Message: fmt.Sprintf("indexed column %q not found on table %q", o.plan.Index.Columns[0], o.plan.Table.Name),
		}
	}

	if o.plan.LowKey != nil {
		v, evalErr := evalExpr(o.plan.LowKey, nil, o.ctx)
		if evalErr != nil {
			return nil, nil, false, evalErr
		}
		if v.IsNull() {
			// NULL lower bound → skip entire scan (no row can satisfy >= NULL)
			return nil, nil, false, nil
		}
		k, encErr := encodeBTreeKeyForColumn(v, col, o.plan.LowKey.Pos())
		if encErr != nil {
			return nil, nil, false, encErr
		}
		loKey = k
	}

	if o.plan.HighKey != nil {
		v, evalErr := evalExpr(o.plan.HighKey, nil, o.ctx)
		if evalErr != nil {
			return nil, nil, false, evalErr
		}
		if v.IsNull() {
			// NULL upper bound → skip entire scan (no row can satisfy <= NULL)
			return nil, nil, false, nil
		}
		k, encErr := encodeBTreeKeyForColumn(v, col, o.plan.HighKey.Pos())
		if encErr != nil {
			return nil, nil, false, encErr
		}
		hiKey = k
	}

	// ok = true as long as at least one bound is specified (the scan is valid)
	return loKey, hiKey, true, nil
}
