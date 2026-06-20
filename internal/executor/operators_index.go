package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
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
//
// ItemIDRedirect line pointers (created by opportunistic pruning when a chain
// root is freed) are followed transparently — the redirect leads to the live
// chain tip, skipping the freed slots.
func followHOTChain(page storage.Page, startSlot uint16, snap mvcc.Snapshot, xid storage.TransactionID) (storage.HeapTuple, uint16, bool) {
	const maxChain = 64
	cur := startSlot
	for i := 0; i < maxChain; i++ {
		// Check line-pointer flags before fetching tuple bytes.
		item, err := storage.PageGetItemID(page, cur)
		if err != nil {
			return storage.HeapTuple{}, 0, false
		}
		if item.Flags == storage.ItemIDRedirect {
			// Pruning converted this slot to a redirect. Follow it.
			next := item.Offset // Offset holds the redirect target slot
			if next == cur {
				return storage.HeapTuple{}, 0, false // self-reference guard
			}
			cur = next
			continue
		}
		if item.Flags != storage.ItemIDNormal {
			return storage.HeapTuple{}, 0, false // unused or dead slot
		}
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

// followHOTChainNoCopy mirrors followHOTChain but uses the no-copy
// PageGetHeapTupleNoCopy variant. Caller MUST hold the page's
// content RLock for the lifetime of the returned tuple — the
// returned tuple.Data aliases the page bytes (M0092-0006).
func followHOTChainNoCopy(page storage.Page, startSlot uint16, snap mvcc.Snapshot, xid storage.TransactionID) (storage.HeapTuple, uint16, bool) {
	const maxChain = 64
	cur := startSlot
	for i := 0; i < maxChain; i++ {
		item, err := storage.PageGetItemID(page, cur)
		if err != nil {
			return storage.HeapTuple{}, 0, false
		}
		if item.Flags == storage.ItemIDRedirect {
			next := item.Offset
			if next == cur {
				return storage.HeapTuple{}, 0, false
			}
			cur = next
			continue
		}
		if item.Flags != storage.ItemIDNormal {
			return storage.HeapTuple{}, 0, false
		}
		t, err := storage.PageGetHeapTupleNoCopy(page, cur)
		if err != nil {
			return storage.HeapTuple{}, 0, false
		}
		if mvcc.TupleVisible(t.Header, snap, xid) {
			return t, cur, true
		}
		if t.Header.Infomask&storage.HeapHotUpdated == 0 {
			return storage.HeapTuple{}, 0, false
		}
		next := t.Header.CTID.Offset
		if next == cur {
			return storage.HeapTuple{}, 0, false
		}
		cur = next
	}
	return storage.HeapTuple{}, 0, false
}

type indexScanOp struct {
	plan *planner.IndexScan
	ctx  *Context
	// M0092-0001: TID-list-eager + heap-fetch-lazy.
	// `tids[i]` holds the (block, index-pointed offset) pair for the
	// i-th match emitted by btree.RangeScan. The HOT-resolved actual
	// slot offset is computed PER Next() and recorded in lastTID for
	// currentTID() — the lockRowsOp consumer.
	//
	// Pre-M0092 the operator also kept `rows []Row` (fully materialised
	// matches via cloneRow per scanFn invocation), which dominated 34 %
	// of allocations in the post-M0091 pgbench select-only profile.
	// The new lazy model decodes one row per Next() into the reusable
	// `scanRow` and returns a slot ALIASING it — caller must consume /
	// Materialize before the next Next() call (standard
	// MaterializedSlot contract).
	tids    []storage.ItemPointer
	idx     int
	lastTID storage.ItemPointer
	hasLast bool

	// M0054-0006a: state captured at Open() time and reused across
	// Rescan() calls when the index probe is driven by an outer row
	// from a parent NestedLoopIndexJoin.
	heapRel storage.RelFileNode
	tree    *btree.BTree
	// M0072-0001: outerSlot is the slot the parent NLI bound via
	// BindOuter. The slot's Get(col) is read by lookupKey /
	// lookupRangeBounds / lookupKeys via evalExprSlot. nil when this
	// scan is run from a single-table path (the historical case):
	// then `o.plan.Key` / `LowKey` / `HighKey` must reduce to
	// constants. outerWidth is captured at BindOuter time so the
	// per-call evalExprSlot path has a consistent width hint
	// (preserves the legacy `len(o.outerRow)` bound check
	// equivalence without requiring a Width() method on SlotView).
	outerSlot  SlotView
	outerWidth int

	// scanRow is the per-Next decode buffer; reused across every
	// Next() call. The slot returned by Next() aliases this buffer.
	// Acquired in openPrep from the rowPool (M0068-0004), released
	// in Close.
	scanRow Row

	// M0092-0007: embedded slot reused across every Next() call so
	// we don't allocate a fresh MaterializedSlot per emission.
	// The returned `&o.slot` pointer is stable across calls; its
	// `row` field is overwritten each Next.
	slot MaterializedSlot
}

func newIndexScanOp(p *planner.IndexScan) *indexScanOp {
	return &indexScanOp{plan: p}
}

func (o *indexScanOp) Schema() planner.Schema { return o.plan.Output() }

// Open performs the one-time prep (lock + btree.Open) and then runs
// a single drain pass with no outer row bound (the historical
// single-table-IndexScan path). Parent operators that drive multiple
// probes (M0054-0006 NestedLoopIndexJoin) instead call Open and
// then `Rescan(outerRow)` per outer row.
//
// When o.ctx is already set (operator reused for a correlated scalar
// subquery across multiple outer rows), Open skips openPrep — the lock
// and btree handle are still valid — and just rescans with the new
// outer context. This avoids repeated lock-acquire + btree.Open
// overhead in the subqueryImpl correlated-operator cache path.
func (o *indexScanOp) Open(ctx *Context) error {
	if o.ctx != nil {
		// Reopen: lock already held, btree handle still valid.
		// Update context (new ctx.OuterRows from evalSubquery) and rescan.
		o.ctx = ctx
		return o.Rescan(nil, 0)
	}
	if err := o.openPrep(ctx); err != nil {
		return err
	}
	return o.Rescan(nil, 0)
}

// openPrep does the one-time setup that is independent of any outer
// row binding: context capture, relation lock acquisition, and
// btree.Open. NLI parents call this directly and then issue
// `Rescan(outerRow)` once per outer row without re-acquiring locks
// or re-opening the index.
func (o *indexScanOp) openPrep(ctx *Context) error {
	if ctx.Pool == nil || ctx.Catalog == nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "IndexScan requires storage handles in Context"}
	}
	o.ctx = ctx
	o.tids = nil
	o.idx = 0
	o.hasLast = false
	o.outerSlot = nil
	o.outerWidth = 0

	o.heapRel = ctx.Catalog.RelFileNode(o.plan.Table)
	if err := ctx.acquireRelLock(o.heapRel, lockmgr.AccessShareLock); err != nil {
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
	o.tree = tree
	return nil
}

// BindOuter is invoked by the M0054-0006 NestedLoopIndexJoin parent
// before each Rescan. The bound slot is the input to evalExprSlot
// when resolving Key / LowKey / HighKey expressions that reference
// outer columns. (M0072-0001: was BindOuter(row Row); reads now go
// through SlotView.Get(col) so the NLI parent passes its persistent
// outerMS slot directly without the legacy `boundRow` concat.)
func (o *indexScanOp) BindOuter(slot SlotView, outerWidth int) {
	o.outerSlot = slot
	o.outerWidth = outerWidth
}

// Rescan re-drains the underlying index after binding an outer slot.
// The historical single-table-IndexScan path calls Open which calls
// Rescan(nil, 0); the M0054-0006 NLI path calls Open once then Rescan
// per outer row.
func (o *indexScanOp) Rescan(outerSlot SlotView, outerWidth int) error {
	o.tids = o.tids[:0]
	o.idx = 0
	o.hasLast = false
	o.outerSlot = outerSlot
	o.outerWidth = outerWidth

	if o.tree == nil {
		// Defensive: openPrep must have been called.
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "indexScanOp.Rescan called before Open"}
	}

	var loBytes, hiBytes []byte
	if len(o.plan.Keys) > 0 {
		// Multi-column equality probe (M0054-0006-followup-Q9-
		// composite). Encode each leading column from
		// `Index.Columns[0..len(Keys)-1]` in order. The planner
		// guarantees `len(Keys) == len(Index.Columns)` whenever
		// `Keys` is non-empty, so no suffix padding is required —
		// we synthesise a full equality probe.
		key, ok, err := o.lookupKeys()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		loBytes = key
		hiBytes = key
	} else if o.plan.Key != nil {
		// Single-column equality scan: probe key is both lo and hi.
		key, ok, err := o.lookupKey()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		loBytes = key
		hiBytes = key
		// Composite-index leading-column probe (M0053-0001):
		// page keys carry suffix bytes for the trailing columns, so the
		// inclusive upper bound must be widened to match every key whose
		// leading bytes equal `key`. CompareKeys is byte-wise via
		// bytes.Compare; appending 0xFF padding produces an upper bound
		// that exceeds any realistic trailing-column encoding.
		if len(o.plan.Index.Columns) > 1 {
			hiBytes = appendCompositeUpperPadding(key)
		}
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
		if len(o.plan.Index.Columns) > 1 && hiBytes != nil {
			hiBytes = appendCompositeUpperPadding(hiBytes)
		}
	}

	// M0092-0001: lazy iteration. The scanFn collects only TIDs;
	// HOT-chain follow + decode + detoast happen per Next() so the
	// produced row aliases scanRow (no cloneRow per match).
	scanFn := func(_ []byte, ptr storage.ItemPointer) (bool, error) {
		o.tids = append(o.tids, ptr)
		return true, nil
	}

	if err := o.tree.RangeScan(loBytes, hiBytes, scanFn); err != nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}
	return nil
}

func (o *indexScanOp) Next() (TupleSlot, error) {
	// M0092-0001: lazy iteration. Pin heap, follow HOT, decode into
	// the reusable scanRow, return slot aliasing it. Caller must
	// consume / Materialize before the next Next() call.
	// Loop instead of recursion to bound stack growth on workloads
	// that skip many invisible tuples (vacuum-pending dead rows).
	for {
		if o.idx >= len(o.tids) {
			o.hasLast = false
			return nil, EOF
		}
		ptr := o.tids[o.idx]
		o.idx++
		slot, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: o.heapRel, Block: ptr.Block})
		if err != nil {
			return nil, err
		}
		// M0092-0006: hold the RLock across decode so we can use
		// followHOTChainNoCopy → tuple.Data aliases the page bytes.
		// The RLock blocks heap writers on this page for the
		// duration of one tuple decode (~hundreds of ns for int
		// rows, ~µs for wide rows) — bounded write-starvation,
		// acceptable per the M0091-0002 audit.
		slot.RLock()
		tuple, actualSlot, found := followHOTChainNoCopy(slot.Page(), ptr.Offset, o.ctx.Snap, o.ctx.Tx.XID)
		if !found {
			slot.RUnlock()
			o.ctx.Pool.Unpin(slot)
			// Tuple invisible (deleted / not yet committed at snap);
			// skip this TID and try the next.
			continue
		}
		if o.scanRow == nil || len(o.scanRow) != len(o.plan.Table.Columns) {
			o.scanRow = acquireRow(len(o.plan.Table.Columns))
		}
		decErr := DecodeHeapTupleRowInto(o.scanRow, o.plan.Table.Columns, tuple, nil)
		slot.RUnlock()
		o.ctx.Pool.Unpin(slot)
		if decErr != nil {
			return nil, decErr
		}
		row := o.scanRow
		// Convert KindString enum column values to KindEnum (sort order) so
		// Filter predicates can compare by declaration order. M0097-0022.
		if im, ok2 := o.ctx.Catalog.(*catalog.InMemory); ok2 {
			for i, col := range o.plan.Table.Columns {
				if et, isEnum := im.LookupEnum(col.Type.Name); isEnum && i < len(row) {
					if row[i].Kind == KindString {
						label := row[i].StringValue()
						for _, ev := range et.Values {
							if ev.Label == label {
								row[i] = NewEnumDatum(ev.SortOrder, label)
								break
							}
						}
					}
				}
			}
		}
		if needsDetoast(row) {
			detoasted, err := DetoastRow(o.ctx, o.heapRel, o.plan.Table.Columns, row)
			if err != nil {
				// Skip undetoastable tuple, try the next TID.
				continue
			}
			row = detoasted
		}
		// Record the actual (HOT-resolved) live slot for
		// currentTID() — lockRowsOp stamps the live version.
		o.lastTID = storage.ItemPointer{Block: ptr.Block, Offset: actualSlot}
		o.hasLast = true
		// M0104-0007: SSI read-path hook on the HOT-resolved live slot.
		// Helper short-circuits for RC/RR; for SERIALIZABLE this installs a
		// tuple-grain predicate lock and an rw-conflict edge to the writer
		// identified by the visible tuple's xmin.
		// M0118-0001: a non-nil error means the reader closed a dangerous
		// structure to an already-committed writer and must abort the scan
		// mid-statement (40001). The heap page RLock/pin was already released
		// above (after the decode), so just propagate the error.
		if err := ssiRecordTupleRead(o.ctx, o.heapRel, ptr.Block, actualSlot, tuple.Header.Xmin, tuple.Header.Xmax); err != nil {
			return nil, err
		}
		// M0092-0007: stack-aliased slot — reuse o.slot across
		// every Next() call. Caller must consume / Materialize
		// before the next Next() invocation.
		o.slot.schema = o.Schema()
		o.slot.row = row
		return &o.slot, nil
	}
}

func (o *indexScanOp) Close() error {
	o.tids = nil
	o.hasLast = false
	if o.scanRow != nil {
		releaseRow(o.scanRow)
		o.scanRow = nil
	}
	return nil
}

// currentTID returns the (rel, ItemPointer) of the most recently
// emitted row, or ok=false before the first Next() call / past
// EOF. Mirrors seqScanOp.currentTID for the index-scan path so
// lockRowsOp can stamp per-row lock-only xmax (M0021 step 2c).
//
// M0092-0001: returns the HOT-resolved actual slot (lastTID),
// recorded by Next() during the HOT-chain follow. Before M0092
// the operator pre-collected `tids[]` of HOT-resolved offsets
// during scanFn; the lazy refactor moves HOT-follow to Next()
// and stashes the result in lastTID.
func (o *indexScanOp) currentTID() (storage.RelFileNode, storage.ItemPointer, bool) {
	if !o.hasLast {
		return storage.RelFileNode{}, storage.ItemPointer{}, false
	}
	rel := o.ctx.Catalog.RelFileNode(o.plan.Table)
	return rel, o.lastTID, true
}

// lookupKeys evaluates each `Keys[i]` against the bound outer row
// and concatenates the per-column B-tree encodings to form the
// multi-column equality probe key. Returns ok=false when any
// component evaluates to NULL — equality on NULL is unknown, so
// the probe correctly produces zero rows. (M0054-0006-followup-
// Q9-composite.)
func (o *indexScanOp) lookupKeys() ([]byte, bool, error) {
	if len(o.plan.Keys) != len(o.plan.Index.Columns) {
		// Defensive: the planner is contractually required to
		// supply one Key per index column. A mismatch here is a
		// planner bug, surfaced as runtime XX000 with the index
		// name so the bug is named at the failure site.
		return nil, false, &ExecError{
			Code: "XX000", Pos: o.plan.Pos(),
			Message: fmt.Sprintf("indexScanOp.lookupKeys: planner supplied %d keys for index %q with %d columns", len(o.plan.Keys), o.plan.Index.Name, len(o.plan.Index.Columns)),
		}
	}
	var probe []byte
	for i, ke := range o.plan.Keys {
		v, err := evalExprSlot(ke, o.outerSlot, o.ctx)
		if err != nil {
			return nil, false, err
		}
		if v.IsNull() {
			return nil, false, nil
		}
		colName := o.plan.Index.Columns[i]
		col, found := o.ctx.Catalog.LookupColumn(o.plan.Table, colName)
		if !found {
			return nil, false, &ExecError{
				Code: "XX000", Pos: o.plan.Pos(),
				Message: fmt.Sprintf("indexed column %q not found on table %q", colName, o.plan.Table.Name),
			}
		}
		segment, encErr := encodeBTreeKeyForColumn(v, col, ke.Pos())
		if encErr != nil {
			return nil, false, encErr
		}
		probe = append(probe, segment...)
	}
	return probe, true, nil
}

func (o *indexScanOp) lookupKey() ([]byte, bool, error) {
	v, err := evalExprSlot(o.plan.Key, o.outerSlot, o.ctx)
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
		v, evalErr := evalExprSlot(o.plan.LowKey, o.outerSlot, o.ctx)
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
		v, evalErr := evalExprSlot(o.plan.HighKey, o.outerSlot, o.ctx)
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

// compositeUpperPaddingLen is how many 0xFF bytes are appended to a
// leading-column key to form an inclusive upper bound for a composite
// index probe (M0053-0001). It must exceed the maximum suffix-column
// encoding for any plausible composite key. 64 bytes covers up to
// ~8 trailing int4/int8 columns, ~3 NUMERIC(38) columns, or 1 varchar(60).
// PostgreSQL's MaxHighKeyLen on goopg is 32, but leaf keys are not
// truncated, so a generous bound is required.
const compositeUpperPaddingLen = 64

// appendCompositeUpperPadding returns key with `compositeUpperPaddingLen`
// trailing 0xFF bytes. Caller-owned slice; the input is not aliased.
func appendCompositeUpperPadding(key []byte) []byte {
	out := make([]byte, len(key)+compositeUpperPaddingLen)
	copy(out, key)
	for i := len(key); i < len(out); i++ {
		out[i] = 0xFF
	}
	return out
}
