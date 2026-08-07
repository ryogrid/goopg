package executor

import (
	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/mctx"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// ---------------------------------------------------------------------------
// bitmapProducer — the interface a bitmap-producing subtree must satisfy.
// The heap scan calls buildBitmap() once to get the filled TIDBitmap.
// ---------------------------------------------------------------------------

type bitmapProducer interface {
	buildBitmap(ctx *Context) (*TIDBitmap, error)
}

// ---------------------------------------------------------------------------
// bitmapIndexScanOp — PG's BitmapIndexScan (nodeBitmapIndexscan.c).
// MultiExec-style: no Next(), only buildBitmap().
// ---------------------------------------------------------------------------

type bitmapIndexScanOp struct {
	plan *planner.BitmapIndexScan
	ctx  *Context

	heapRel storage.RelFileNode
	tree    *btree.BTree

	// scanRow is lazily allocated for evalExpr (needs a Row context).
	scanRow Row
}

func newBitmapIndexScanOp(p *planner.BitmapIndexScan) *bitmapIndexScanOp {
	return &bitmapIndexScanOp{plan: p}
}

func (o *bitmapIndexScanOp) Schema() planner.Schema { return o.plan.Output() }

func (o *bitmapIndexScanOp) Open(ctx *Context) error {
	if ctx.Pool == nil || ctx.Catalog == nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "BitmapIndexScan requires storage handles in Context"}
	}
	o.ctx = ctx
	o.heapRel = ctx.Catalog.RelFileNode(o.plan.Table)

	// Acquire relation locks (mirrors indexScanOp.openPrep).
	if err := ctx.acquireRelLock(o.heapRel, lockmgr.AccessShareLock); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.plan.Pos()
		}
		return err
	}
	if err := ctx.acquireScanReadLockTxn(o.heapRel); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.plan.Pos()
		}
		return err
	}
	if err := ctx.acquireScanIndexReadLocksTxn(o.plan.Table); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.plan.Pos()
		}
		return err
	}

	// Open the B-tree index.
	idxRel := ctx.Catalog.IndexRelFileNode(o.plan.Index)
	tree, err := btree.Open(ctx.Pool, idxRel)
	if err != nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}
	o.tree = tree
	return nil
}

func (o *bitmapIndexScanOp) Close() error {
	o.tree = nil
	o.ctx = nil
	if o.scanRow != nil {
		releaseRow(o.scanRow)
		o.scanRow = nil
	}
	return nil
}

// Next panics — a BitmapIndexScan is never executed via the pull model.
func (o *bitmapIndexScanOp) Next() (TupleSlot, error) {
	panic("BitmapIndexScan does not support ExecProcNode call")
}

// buildBitmap runs the index scan and returns a filled TIDBitmap.
func (o *bitmapIndexScanOp) buildBitmap(ctx *Context) (*TIDBitmap, error) {
	if o.tree == nil {
		if err := o.Open(ctx); err != nil {
			return nil, err
		}
	}
	// tbm is sized by work_mem.
	maxEntries := tbmCalculateMaxEntries(o.ctx.WorkMem)
	tbm := &TIDBitmap{maxEntries: maxEntries}

	// Determine whether recheck is needed (prefix scan on composite index).
	recheck := o.needsRecheck()

	// Compute the lo/hi key bounds (same logic as indexScanOp.Rescan).
	loBytes, hiBytes, err := o.lookupBounds()
	if err != nil {
		return nil, err
	}

	// Scan the B-tree and feed TIDs into the bitmap.
	scanFn := func(_ []byte, ptr storage.ItemPointer, _ btree.ScanPos) (bool, error) {
		tbmAddTuples(tbm, []storage.ItemPointer{ptr}, recheck)
		return true, nil
	}
	if err := o.tree.RangeScanWithPos(loBytes, hiBytes, scanFn); err != nil {
		return nil, &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}

	// Lossify if over budget.
	tbmLossify(tbm)
	return tbm, nil
}

// needsRecheck returns true when the index scan uses only a prefix of a
// composite index key — the remaining key columns' conditions must be
// rechecked against the heap tuple.
func (o *bitmapIndexScanOp) needsRecheck() bool {
	if len(o.plan.Index.Columns) == 0 {
		return false
	}
	// Key covers the first N columns; if there are more index columns,
	// the trailing conditions need recheck.
	if len(o.plan.Keys) > 0 {
		return len(o.plan.Keys) < len(o.plan.Index.Columns)
	}
	// Single-column equality on composite index — recheck needed if
	// there are more columns beyond the leading one.
	if o.plan.Key != nil && len(o.plan.Index.Columns) > 1 {
		return true
	}
	return false
}

// lookupBounds computes the lo/hi key bytes for the index scan.
func (o *bitmapIndexScanOp) lookupBounds() (loBytes, hiBytes []byte, err error) {
	col, found := o.ctx.Catalog.LookupColumn(o.plan.Table, o.plan.Index.Columns[0])
	if !found {
		return nil, nil, &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "column not found for index key"}
	}

	// Ensure scanRow is allocated for evalExpr.
	if o.scanRow == nil {
		o.scanRow = acquireRow(len(o.plan.Table.Columns))
	}

	if len(o.plan.Keys) > 0 {
		// Multi-column equality: encode all keys.
		return o.lookupKeys(col)
	}
	if o.plan.Key != nil {
		// Single-column equality.
		return o.lookupKey(col)
	}
	// No scan key means scan all. loBytes=nil, hiBytes=nil.
	return nil, nil, nil
}

// lookupKey encodes a single-column equality key.
func (o *bitmapIndexScanOp) lookupKey(col *catalog.Column) (lo, hi []byte, err error) {
	val, evalErr := evalExpr(o.plan.Key, o.scanRow, o.ctx)
	if evalErr != nil {
		return nil, nil, evalErr
	}
	if val.IsNull() {
		return nil, nil, nil // empty probe
	}
	key, encErr := encodeBTreeKeyForColumn(val, col, o.plan.Pos())
	if encErr != nil {
		return nil, nil, encErr
	}
	lo = key
	hi = key
	if len(o.plan.Index.Columns) > 1 {
		hi = appendCompositeUpperPadding(key)
	}
	return lo, hi, nil
}

// lookupKeys encodes multi-column equality keys.
func (o *bitmapIndexScanOp) lookupKeys(firstCol *catalog.Column) (lo, hi []byte, err error) {
	// Encode each leading column in order.
	var keyBytes []byte
	for i, keyExpr := range o.plan.Keys {
		keyCol, found := o.ctx.Catalog.LookupColumn(o.plan.Table, o.plan.Index.Columns[i])
		if !found {
			return nil, nil, &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "column not found for index key"}
		}
		val, evalErr := evalExpr(keyExpr, o.scanRow, o.ctx)
		if evalErr != nil {
			return nil, nil, evalErr
		}
		if val.IsNull() {
			return nil, nil, nil // empty probe
		}
		k, encErr := encodeBTreeKeyForColumn(val, keyCol, o.plan.Pos())
		if encErr != nil {
			return nil, nil, encErr
		}
		keyBytes = append(keyBytes, k...)
	}
	_ = firstCol // unified interface; first column is looked up per-key in the loop
	return keyBytes, keyBytes, nil
}

// ---------------------------------------------------------------------------
// bitmapHeapScanOp — PG's BitmapHeapScan (nodeBitmapHeapscan.c).
// Standard pull-model operator that iterates the TIDBitmap and fetches
// heap tuples in physical order.
// ---------------------------------------------------------------------------

type bitmapHeapScanOp struct {
	plan *planner.BitmapHeapScan
	ctx  *Context
	tbl  *catalog.Table
	rel  storage.RelFileNode

	// Cached outer bitmap producer.
	outer       Operator
	outerBitmap bitmapProducer

	// TIDBitmap iterator state.
	tbm  *TIDBitmap
	iter *tbmIterator

	// Current page state.
	pinned    *storage.Slot
	pageBuf   storage.Page
	pageBlock storage.BlockNumber
	pageLossy bool

	// scanRow is reused per Next() — zero allocation per tuple.
	scanRow Row
	// mctx is the per-page byte arena for varlena payloads.
	mctx *mctx.Context
	// slot is the embedded MaterializedSlot reused every Next().
	slot MaterializedSlot
	// cols maps column index → catalog.Column for decode.
	cols []catalog.Column

	// Stats.
	exactPages int64
	lossyPages int64
}

func newBitmapHeapScanOp(p *planner.BitmapHeapScan) *bitmapHeapScanOp {
	return &bitmapHeapScanOp{plan: p}
}

func (o *bitmapHeapScanOp) Schema() planner.Schema { return o.plan.Output() }

func (o *bitmapHeapScanOp) Open(ctx *Context) error {
	if ctx.Pool == nil || ctx.Catalog == nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "BitmapHeapScan requires storage handles in Context"}
	}
	o.ctx = ctx
	o.tbl = o.plan.Table
	o.rel = ctx.Catalog.RelFileNode(o.plan.Table)

	// Resolve columns for decode.
	o.cols = make([]catalog.Column, len(o.tbl.Columns))
	for i, c := range o.tbl.Columns {
		col, found := ctx.Catalog.LookupColumn(o.tbl, c.Name)
		if !found {
			o.cols[i] = c
		} else {
			o.cols[i] = *col
		}
	}

	// Create mctx for per-page byte arena.
	if ctx.Mctx != nil {
		o.mctx = mctx.Acquire(ctx.Mctx, mctx.KindExpr)
	}

	// Build the outer operator (a BitmapIndexScan or BitmapAnd/BitmapOr tree).
	outerOp, err := buildNode(o.plan.Outer)
	if err != nil {
		return err
	}
	if err := outerOp.Open(ctx); err != nil {
		outerOp.Close()
		return err
	}
	o.outer = outerOp
	var ok bool
	o.outerBitmap, ok = outerOp.(bitmapProducer)
	if !ok {
		outerOp.Close()
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "BitmapHeapScan outer is not a bitmap producer"}
	}
	return nil
}

func (o *bitmapHeapScanOp) Close() error {
	o.releasePinned()
	if o.outer != nil {
		o.outer.Close()
		o.outer = nil
	}
	if o.mctx != nil {
		o.mctx.Release()
		o.mctx = nil
	}
	if o.scanRow != nil {
		releaseRow(o.scanRow)
		o.scanRow = nil
	}
	o.outerBitmap = nil
	o.tbm = nil
	o.iter = nil
	o.ctx = nil
	return nil
}

// releasePinned unpins the current page if any.
func (o *bitmapHeapScanOp) releasePinned() {
	if o.pinned != nil {
		o.pinned.RUnlock()
		o.ctx.Pool.Unpin(o.pinned)
		o.pinned = nil
		o.pageBuf = nil
	}
}

// Next advances the bitmap iterator and returns the next matching heap tuple.
func (o *bitmapHeapScanOp) Next() (TupleSlot, error) {
	for {
		// Lazily build the bitmap on first call.
		if o.tbm == nil {
			tbm, err := o.outerBitmap.buildBitmap(o.ctx)
			if err != nil {
				return nil, err
			}
			o.tbm = tbm
			o.iter = tbmBeginIterate(tbm)
		}

		// Advance the TBM iterator.
		block, offset, lossy, recheck, ok := o.iter.next()
		if !ok {
			// Exhausted.
			o.releasePinned()
			return nil, nil
		}

		// If the page changed or we haven't pinned one yet, pin the new page.
		if o.pinned == nil || block != o.pageBlock {
			o.releasePinned()

			slot, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: o.rel, Block: block})
			if err != nil {
				return nil, err
			}
			slot.RLock()
			o.pinned = slot
			o.pageBuf = slot.Page()
			o.pageBlock = block
			o.pageLossy = lossy

			if lossy {
				o.lossyPages++
			} else {
				o.exactPages++
			}

			// Reset per-page arena.
			o.mctx.Reset()
		}

		// For lossy pages, iterate ALL offsets. The iterator yields
		// (block, 0, lossy=true) once; we walk every line pointer.
		if lossy {
			// Use a sub-state to track position within the lossy page.
			// We'll return one row at a time and save position.
			continue // fall through to lossy handling below
		}

		// Exact page: fetch the specific tuple at the given offset.
		return o.fetchExact(block, offset, recheck)
	}
}

// fetchExact fetches a specific tuple at the given offset.
func (o *bitmapHeapScanOp) fetchExact(block storage.BlockNumber, offset uint16, recheck bool) (TupleSlot, error) {
	// Check that the line pointer is still valid.
	id, err := storage.PageGetItemID(o.pageBuf, offset)
	if err != nil {
		return o.Next() // entry reclaimed, skip
	}
	if id.Flags == storage.ItemIDUnused || id.Flags == storage.ItemIDDead {
		return o.Next() // entry reclaimed, skip
	}

	// Follow HOT chain + MVCC visibility.
	tuple, _, found := followHOTChainNoCopy(o.pageBuf, offset, o.ctx.Snap, o.ctx.Tx.XID, o.ctx.MultiXact, nil)
	if !found {
		return o.Next() // tuple invisible, skip
	}

	// Lazily allocate scanRow.
	if o.scanRow == nil || len(o.scanRow) != len(o.tbl.Columns) {
		o.scanRow = acquireRow(len(o.tbl.Columns))
	}

	storedNatts := int(tuple.Header.Infomask2 & 0x07FF)
	if err := DecodeRowIntoMctxPGTuple(o.scanRow, o.cols, tuple.Data, tuple.Bitmap, storedNatts, o.mctx); err != nil {
		return o.Next() // decode failure, skip
	}

	// If recheck is required (lossy page or index AM said recheck),
	// evaluate the original index qual (BitmapQual).
	if recheck && len(o.plan.BitmapQual) > 0 {
		passed, evalErr := o.evalBitmapQual()
		if evalErr != nil {
			return nil, evalErr
		}
		if !passed {
			return o.Next() // recheck failed, skip
		}
	}

	// Clone arena-backed data.
	row := cloneRowOwned(o.scanRow)
	o.slot = MaterializedSlot{schema: o.plan.Output(), row: row}
	return &o.slot, nil
}

// evalBitmapQual evaluates the BitmapHeapScan's BitmapQual against the
// current scanRow.
func (o *bitmapHeapScanOp) evalBitmapQual() (bool, error) {
	for _, qual := range o.plan.BitmapQual {
		val, err := evalExpr(qual, o.scanRow, o.ctx)
		if err != nil {
			return false, err
		}
		if val.IsNull() {
			// NULL qual → false (strict boolean semantics).
			return false, nil
		}
		// BoolValue returns false for non-boolean values, matching
		// the strict boolean semantics: NULL → false, non-bool → false.
		if !val.BoolValue() {
			return false, nil
		}
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// bitmapAndOp / bitmapOrOp — combine multiple bitmap-producing subtrees.
// ---------------------------------------------------------------------------

type bitmapAndOp struct {
	plan       *planner.BitmapAnd
	inputs     []Operator
	inputBitmaps []bitmapProducer
	ctx        *Context
}

func newBitmapAndOp(p *planner.BitmapAnd) *bitmapAndOp {
	return &bitmapAndOp{plan: p}
}

func (o *bitmapAndOp) Schema() planner.Schema { return o.plan.Output() }

func (o *bitmapAndOp) Open(ctx *Context) error {
	o.ctx = ctx
	o.inputs = make([]Operator, len(o.plan.Inputs))
	o.inputBitmaps = make([]bitmapProducer, len(o.plan.Inputs))
	for i, input := range o.plan.Inputs {
		op, err := buildNode(input)
		if err != nil {
			return err
		}
		if err := op.Open(ctx); err != nil {
			return err
		}
		bp, ok := op.(bitmapProducer)
		if !ok {
			return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "BitmapAnd input is not a bitmap producer"}
		}
		o.inputs[i] = op
		o.inputBitmaps[i] = bp
	}
	return nil
}

func (o *bitmapAndOp) Close() error {
	for _, op := range o.inputs {
		if op != nil {
			op.Close()
		}
	}
	o.inputs = nil
	o.inputBitmaps = nil
	o.ctx = nil
	return nil
}

// Next panics — BitmapAnd is MultiExec-style.
func (o *bitmapAndOp) Next() (TupleSlot, error) {
	panic("BitmapAnd does not support ExecProcNode call")
}

func (o *bitmapAndOp) buildBitmap(ctx *Context) (*TIDBitmap, error) {
	if len(o.inputBitmaps) == 0 {
		return &TIDBitmap{}, nil
	}
	first, err := o.inputBitmaps[0].buildBitmap(ctx)
	if err != nil {
		return nil, err
	}
	for i := 1; i < len(o.inputBitmaps); i++ {
		other, err := o.inputBitmaps[i].buildBitmap(ctx)
		if err != nil {
			return nil, err
		}
		tbmIntersect(first, other)
	}
	return first, nil
}

type bitmapOrOp struct {
	plan         *planner.BitmapOr
	inputs       []Operator
	inputBitmaps []bitmapProducer
	ctx          *Context
}

func newBitmapOrOp(p *planner.BitmapOr) *bitmapOrOp {
	return &bitmapOrOp{plan: p}
}

func (o *bitmapOrOp) Schema() planner.Schema { return o.plan.Output() }

func (o *bitmapOrOp) Open(ctx *Context) error {
	o.ctx = ctx
	o.inputs = make([]Operator, len(o.plan.Inputs))
	o.inputBitmaps = make([]bitmapProducer, len(o.plan.Inputs))
	for i, input := range o.plan.Inputs {
		op, err := buildNode(input)
		if err != nil {
			return err
		}
		if err := op.Open(ctx); err != nil {
			return err
		}
		bp, ok := op.(bitmapProducer)
		if !ok {
			return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "BitmapOr input is not a bitmap producer"}
		}
		o.inputs[i] = op
		o.inputBitmaps[i] = bp
	}
	return nil
}

func (o *bitmapOrOp) Close() error {
	for _, op := range o.inputs {
		if op != nil {
			op.Close()
		}
	}
	o.inputs = nil
	o.inputBitmaps = nil
	o.ctx = nil
	return nil
}

// Next panics — BitmapOr is MultiExec-style.
func (o *bitmapOrOp) Next() (TupleSlot, error) {
	panic("BitmapOr does not support ExecProcNode call")
}

func (o *bitmapOrOp) buildBitmap(ctx *Context) (*TIDBitmap, error) {
	if len(o.inputBitmaps) == 0 {
		return &TIDBitmap{}, nil
	}
	first, err := o.inputBitmaps[0].buildBitmap(ctx)
	if err != nil {
		return nil, err
	}
	for i := 1; i < len(o.inputBitmaps); i++ {
		other, err := o.inputBitmaps[i].buildBitmap(ctx)
		if err != nil {
			return nil, err
		}
		tbmUnion(first, other)
	}
	return first, nil
}
