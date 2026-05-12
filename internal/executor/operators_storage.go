package executor

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// maxEPQRetries is the maximum number of EvalPlanQual re-checks before
// escalating to SQLSTATE 40001. M0098-0004.
const maxEPQRetries = 3

// maxWFGHops is the maximum chain length walked during WFG cycle detection.
// Limits the O(N) scan to a constant bound under adversarial workloads.
const maxWFGHops = 64

// Process-global wait-for graph for EPQ deadlock detection.
// Maps waitingXID → blockingXID. Protected by wfgMu. M0099-0004.
var (
	wfgMu        sync.Mutex
	waitForGraph = make(map[storage.TransactionID]storage.TransactionID)
)

// registerWFGAndCheckCycle adds the edge myXID→blockingXID and walks the
// graph up to maxWFGHops looking for a cycle (deadlock). Returns true when
// a cycle is detected; the edge is removed before returning (caller must NOT
// call deregisterWFG). Returns false when no cycle is found; the caller must
// call deregisterWFG after the wait completes.
func registerWFGAndCheckCycle(myXID, blockingXID storage.TransactionID) bool {
	wfgMu.Lock()
	defer wfgMu.Unlock()
	waitForGraph[myXID] = blockingXID
	cur := blockingXID
	for i := 0; i < maxWFGHops; i++ {
		if cur == myXID {
			delete(waitForGraph, myXID)
			return true
		}
		next, ok := waitForGraph[cur]
		if !ok {
			return false
		}
		cur = next
	}
	return false
}

// deregisterWFG removes myXID from the wait-for graph after a snapshot
// refresh completes.
func deregisterWFG(myXID storage.TransactionID) {
	wfgMu.Lock()
	delete(waitForGraph, myXID)
	wfgMu.Unlock()
}

// epqWait detects deadlock cycles via the wait-for graph (WFG) and refreshes
// the snapshot. Returns true if a deadlock cycle is confirmed — caller must
// immediately escalate to SQLSTATE 40001. Returns false otherwise.
//
// WFG cycle detection (M0099-0004) provides earlier deadlock identification
// than the M0098-0004 retry-exhaustion approach: a confirmed 2-node cycle
// (TX1→TX2, TX2→TX1) yields 40001 immediately for one participant instead
// of after maxEPQRetries snapshot-refresh rounds.
//
// Non-deadlock conflicts fall back to snapshot-refresh-only (same as
// M0098-0004) — blocking via WaitForXID was removed because it caused
// pgbench client goroutines to hang past the 180 s measurement window.
// M0098-0004, M0099-0004.
func epqWait(ctx *Context, xmax storage.TransactionID) (deadlock bool) {
	if ctx.TxnMgr == nil {
		return false
	}
	if ctx.Tx.XID != storage.InvalidTransactionID {
		if registerWFGAndCheckCycle(ctx.Tx.XID, xmax) {
			return true
		}
		defer deregisterWFG(ctx.Tx.XID)
	}
	// Refresh the snapshot so the next epqRecheckVisible call sees any
	// committed changes from the conflicting transaction.
	if snap, serr := ctx.TxnMgr.SnapshotFor(ctx.Tx); serr == nil {
		ctx.Snap = snap.Clone()
	}
	return false
}

// epqRecheckVisible re-reads the tuple at (rel, blk, slot) and reports
// whether it is still visible under the current snapshot. Returns false if
// the row was committed by the conflicting transaction (skip the row),
// true if the conflicting transaction aborted (row is still live, retry).
// M0098-0004.
func epqRecheckVisible(ctx *Context, rel storage.RelFileNode, blk storage.BlockNumber, slot uint16) (bool, error) {
	s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
	if err != nil {
		return false, err
	}
	s.RLock()
	tup, gerr := storage.PageGetHeapTuple(s.Page(), slot)
	s.RUnlock()
	ctx.Pool.Unpin(s)
	if gerr != nil {
		return false, nil // page read error → treat as not visible
	}
	return mvcc.TupleVisible(tup.Header, ctx.Snap, ctx.Tx.XID), nil
}

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

	// activePage holds the current page bytes regardless of source
	// (pool slot or ring buffer). Set alongside pinned (for pool) or
	// independently (for ring). Readers use this instead of
	// o.pinned.Page() so ring-buffered pages work transparently.
	activePage storage.Page

	// ring is the SeqScan strategy ring (M0048-0002). When non-nil,
	// cache misses are served from private ring buffers instead of
	// evicting pool pages.  Activated when nBlocks > pool.Capacity()/4.
	ring *storage.ScanRing

	// prefetchedThru is the highest block (exclusive) we've
	// already issued a Pool.Prefetch hint for. SeqScan walks
	// blocks strictly forward, so the prefetcher just needs to
	// keep `seqScanLookahead` blocks ahead of curBlock.
	prefetchedThru storage.BlockNumber

	// scanRow is the per-Next() decode buffer (M0054-0005a). The
	// pre-fix path called `DecodeRow` on every visible tuple,
	// allocating a fresh `Row` slice each time. We now allocate
	// `scanRow` once on first use and decode in place via
	// `DecodeRowInto`, returning a defensive `cloneRow` so callers
	// that retain the row across `Next()` calls (sortOp, hash-join
	// build, etc.) keep their own copy. This drops the
	// per-row leaf-allocation cost the M0054-0004 pprof survey
	// flagged as `runtime.findObject` flat 29.30 % under Q9.
	scanRow Row

	// arena is the per-page byte allocator backing varchar / char
	// / text / bytea Datums emitted by DecodeRowIntoArena.
	// Reset() at the per-block boundary (when curBlock advances)
	// frees all variable-length payload allocated for the
	// previous page's tuples; consumers that retain rows past
	// the boundary must call slot.Materialize() to deep-copy.
	// (M0073-0004.)
	arena *Arena

	// M0092-0007: embedded slot reused across every Next() call.
	// The returned `&o.slot` pointer is stable across calls; its
	// `row` field is overwritten per emission. Caller must
	// consume / Materialize before the next Next() invocation.
	slot MaterializedSlot
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
	// M0073-0004: per-page byte arena for varchar / char / text /
	// bytea payload. Reset on block-advance below. Lifetime tied
	// to the operator; Close drops the pages.
	o.arena = NewArena(0)
	// Activate the ring strategy when the relation is large enough that a
	// full sequential scan would evict most hot pages from the shared pool.
	// Threshold: pool capacity / 4, matching upstream's heuristic.
	if ctx.Pool != nil && int(n) > ctx.Pool.Capacity()/4 {
		o.ring = storage.NewScanRing(ctx.Pool, rel)
	}
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
	if o.ring != nil {
		o.ring.Close()
		o.ring = nil
		o.activePage = nil
	} else if o.pinned != nil {
		o.ctx.Pool.Unpin(o.pinned)
		o.pinned = nil
		o.activePage = nil
	}
	if o.scanRow != nil {
		releaseRow(o.scanRow)
		o.scanRow = nil
	}
	if o.arena != nil {
		o.arena.Drop()
		o.arena = nil
	}
	return nil
}

// nextVisible advances through (block, slot) pairs and returns the
// next tuple visible to the snapshot, or EOF.
func (o *seqScanOp) Next() (TupleSlot, error) {
	rel := o.ctx.Catalog.RelFileNode(o.plan.Table)
	for {
		if o.pinned == nil && o.activePage == nil {
			if o.curBlock >= o.nBlocks {
				return nil, EOF
			}
			// Poll for query cancellation at each new block boundary.
			if o.ctx.Ctx != nil {
				if err := o.ctx.Ctx.Err(); err != nil {
					return nil, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
				}
			}
			if o.ring != nil {
				// Ring strategy: cache hit → pool slot (with RLock);
				// cache miss → private ring buffer (no pool eviction).
				page, err := o.ring.AcquirePage(o.curBlock)
				if err != nil {
					return nil, err
				}
				o.activePage = page
			} else {
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
				o.activePage = slot.Page()
			}
			page := o.activePage
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
			page := o.activePage
			tuple, err := storage.PageGetHeapTuple(page, o.curSlot)
			o.curSlot++
			if err != nil {
				// Corrupt or unsupported tuples are silently
				// skipped — scanning should not fail on
				// partial page writes or WAL-replay debris.
				continue
			}
			if !mvcc.TupleVisibleSubxact(tuple.Header, o.ctx.Snap, o.ctx.Tx.XID, o.ctx.TxnMgr) {
				continue
			}
			// M0054-0005a: decode into the reusable o.scanRow
			// buffer. M0073-0004: route varchar / char / text /
			// bytea payload through the per-page arena so per-
			// tuple `make([]byte)` allocs are amortised across
			// the page (one alloc per ~64 KiB). Reset is bound
			// to the curBlock++ boundary below; consumers that
			// retain rows past the boundary call slot.Materialize
			// which deep-copies arena bytes via cloneRowOwned.
			if o.scanRow == nil || len(o.scanRow) != len(o.cols) {
				o.scanRow = acquireRow(len(o.cols))
			}
			if err := DecodeRowIntoArena(o.scanRow, o.cols, tuple.Data, o.arena); err != nil {
				continue
			}
			row := o.scanRow
			// Detoast any out-of-line column values (M0046-0006).
			// DetoastRow may return a fresh row when it allocates
			// large detoasted strings; either way the result is
			// safe to clone.
			if needsDetoast(row) {
				detoasted, err := DetoastRow(o.ctx, rel, o.cols, row)
				if err != nil {
					continue // skip undetoastable tuple
				}
				row = detoasted
			}
			// M0092-0007: stack-aliased slot reused across
			// Next() calls; matches the M0092-0002 contract
			// (consumers materialize at retention boundaries).
			// scanRow is reused across the per-page tuple
			// loop; rows that need retention go through
			// slot.Materialize().
			o.slot.schema = o.Schema()
			o.slot.row = row
			return &o.slot, nil
		}
		o.releasePinned()
		o.curBlock++
		// M0073-0004: rewind the per-page byte arena. All slots
		// emitted from the just-finished page have either been
		// consumed by the parent or had their arena Datums
		// promoted to owned []byte via slot.Materialize() at the
		// retention boundary (sortOp.Open / windowOp.Open /
		// lockRowsOp.drainAndStamp / executor.Run; aggregateOp's
		// targeted MaterializeArena in evalGroupKey + applyAgg).
		// Reset rewinds page len to 0 but keeps capacity, so the
		// next page's decode reuses the same backing bytes — no
		// per-page allocation in steady state.
		if o.arena != nil {
			o.arena.Reset()
		}
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
	if o.ring != nil {
		o.ring.ReleasePage()
		o.activePage = nil
	} else if o.pinned != nil {
		o.pinned.RUnlock()
		o.ctx.Pool.Unpin(o.pinned)
		o.pinned = nil
		o.activePage = nil
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
func (o *insertOp) Next() (TupleSlot, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true
	rel := o.ctx.Catalog.RelFileNode(o.plan.Table)
	cols := o.plan.Table.Columns
	isPartitioned := len(o.plan.Table.PartitionKey) > 0
	for {
		srcSlot, err := o.child.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		src := slotRow(srcSlot)
		// Reorder source row -> table column order via plan.ColumnIndex.
		row := make(Row, len(cols))
		for i := range cols {
			row[i] = NullDatum
		}
		for srcIdx, tgtIdx := range o.plan.ColumnIndex {
			row[tgtIdx] = src[srcIdx]
		}

		// Auto-generate values for SERIAL / BIGSERIAL / SMALLSERIAL columns.
		// M0097-0009: if a serial column's slot is still NullDatum (not provided
		// in the INSERT), call nextval on the implicit sequence.
		for i, col := range cols {
			if !row[i].IsNull() {
				continue
			}
			seqName := ""
			switch strings.ToLower(col.Type.Name) {
			case "serial":
				seqName = strings.ToLower(o.plan.Table.Name) + "_" + strings.ToLower(col.Name) + "_seq"
			case "bigserial":
				seqName = strings.ToLower(o.plan.Table.Name) + "_" + strings.ToLower(col.Name) + "_seq"
			case "smallserial":
				seqName = strings.ToLower(o.plan.Table.Name) + "_" + strings.ToLower(col.Name) + "_seq"
			}
			if seqName == "" {
				continue
			}
			seqArgs := []Datum{NewStringDatum(seqName)}
			v, err := evalNextval(seqArgs, o.ctx)
			if err == nil && !v.IsNull() {
				row[i] = v
			}
		}

		// BEFORE INSERT triggers (M0096-0012).
		if len(o.plan.Table.Triggers) > 0 {
			newRow, ok := fireTriggers(o.ctx, o.plan.Table, "before", "insert", nil, row)
			if !ok {
				continue // trigger returned NULL — skip this row
			}
			row = newRow
		}

		// CHECK constraint enforcement (M0097-0014).
		if len(o.plan.Table.CheckConstraints) > 0 {
			if err := checkConstraints(o.ctx, o.plan.Table, row); err != nil {
				return nil, err
			}
		}

		// FK referential integrity check (M0096-0011): verify parent rows exist
		// before writing.  Uses the plan table's ForeignKeys (parent partition's
		// FKs apply to routed child inserts too).
		if len(o.plan.Table.ForeignKeys) > 0 {
			if err := checkFKInsert(o.ctx, o.plan.Table, row); err != nil {
				return nil, err
			}
		}

		// Partition routing (M0096-0007): if the target table is partitioned,
		// route the row to the appropriate partition child.
		targetRel := rel
		// Compute generated columns (GENERATED ALWAYS AS … STORED) before writing.
		// M0096-0008.
		_ = computeGeneratedColumns(cols, row)

		if isPartitioned {
			if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
				partTable := routeToPartition(o.plan.Table, row, im)
				if partTable != nil {
					targetRel = o.ctx.Catalog.RelFileNode(partTable)
					// Remap row from parent column order to partition child column order.
					// Partition children may have columns in a different order (ATTACH
					// PARTITION allows mismatched column order). M0096-0013.
					partRow := remapRowForPartition(o.plan.Table.Columns, partTable.Columns, row)
					// Recompute generated columns using partition child's schema.
					_ = computeGeneratedColumns(partTable.Columns, partRow)
					if err := writeHeapRow(o.ctx, targetRel, partTable.Columns, partRow); err != nil {
						return nil, err
					}
					o.rowsAffected++
					continue
				}
			}
			// No matching partition found — write to parent anyway (best effort)
		}
		if err := writeHeapRow(o.ctx, targetRel, cols, row); err != nil {
			return nil, err
		}
		o.rowsAffected++
	}
	return nil, EOF
}

// routeToPartition finds the partition child table that matches the given row
// remapRowForPartition reorders a row from the parent's column layout to the
// partition child's column layout. PostgreSQL's ATTACH PARTITION allows the
// child to have columns in a different order (as long as names and types match).
// We remap by matching column names. M0096-0013.
func remapRowForPartition(parentCols, childCols []catalog.Column, row Row) Row {
	if len(parentCols) == len(childCols) {
		same := true
		for i := range childCols {
			if childCols[i].Name != parentCols[i].Name {
				same = false
				break
			}
		}
		if same {
			return row // fast path: same ordering
		}
	}
	// Build name→value map from parent row.
	byName := make(map[string]Datum, len(parentCols))
	for i, c := range parentCols {
		if i < len(row) {
			byName[strings.ToLower(c.Name)] = row[i]
		}
	}
	out := make(Row, len(childCols))
	for i, c := range childCols {
		if v, ok := byName[strings.ToLower(c.Name)]; ok {
			out[i] = v
		} else {
			out[i] = NullDatum
		}
	}
	return out
}

// based on the parent's partition key. Returns nil if no partition matches.
// M0096-0007.
func routeToPartition(parent *catalog.Table, row Row, im *catalog.InMemory) *catalog.Table {
	if len(parent.PartitionKey) == 0 {
		return nil
	}
	// Find the column index for the partition key
	keyColName := parent.PartitionKey[0]
	keyIdx := -1
	for i, col := range parent.Columns {
		if strings.EqualFold(col.Name, keyColName) {
			keyIdx = i
			break
		}
	}
	if keyIdx < 0 || keyIdx >= len(row) {
		return nil
	}
	keyDatum := row[keyIdx]

	switch parent.PartitionMethod {
	case "LIST":
		keyStr := ""
		if keyDatum.Kind == KindInt {
			keyStr = fmt.Sprintf("%d", keyDatum.Int)
		} else if keyDatum.Kind == KindString {
			keyStr = keyDatum.StringValue()
		}
		return im.FindPartitionForValue(parent.OID, keyStr)
	case "RANGE":
		if keyDatum.Kind == KindInt {
			return im.FindRangePartitionForValue(parent.OID, keyDatum.Int)
		}
	case "HASH":
		// Use string representation of key for hash. M0097-0015.
		keyStr := ""
		if keyDatum.Kind == KindInt {
			keyStr = fmt.Sprintf("%d", keyDatum.Int)
		} else if keyDatum.Kind == KindString {
			keyStr = keyDatum.StringValue()
		} else {
			keyStr = keyDatum.Format()
		}
		return im.FindHashPartitionForValue(parent.OID, keyStr)
	}
	return nil
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
					Op:    parser.OpAnd,
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
				Op:    parser.OpEq,
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

// isConcurrentlyUpdated reports whether the tuple has been updated or
// deleted by a transaction OTHER than myXID. Used under the page's
// exclusive Lock by the UPDATE / DELETE / HOT-update paths to detect
// the concurrent-xmax-stamp race that produces orphan visible tuples
// in MVCC (M0090-0002).
//
// snap is the current statement snapshot; it is passed for context but
// the aborted-xmax disambiguation is handled in the EPQ retry loops
// (see updateOp, deleteOp): when epqRecheckVisible finds the row still
// visible AND snap.HasInProgress(xmax) is false, the xmax was aborted,
// so the loop breaks instead of retrying → avoids permanent 40001 on
// rolled-back HOT update chains (M0099 fix).
//
// A lock-only xmax (SELECT FOR UPDATE) is NOT treated as a concurrent
// update — the lock holder does not own the row's write intent.
func isConcurrentlyUpdated(h storage.HeapTupleHeader, myXID storage.TransactionID, _ *mvcc.Snapshot) bool {
	// "Our own xmax stamp" — re-update in the same transaction is
	// always legal, regardless of HeapHotUpdated or other bits set
	// by our prior write.
	if h.Xmax != storage.InvalidTransactionID && h.Xmax == myXID {
		return false
	}
	// Beyond this point, any xmax/HOT marker is from a DIFFERENT
	// transaction.
	if h.Infomask&storage.HeapHotUpdated != 0 {
		return true
	}
	if h.Xmax == storage.InvalidTransactionID {
		return false
	}
	if h.Infomask&storage.HeapXmaxInvalid != 0 {
		// Xmax is hinted as not-a-deleter; matches the
		// HeapXmaxInvalid semantics defined in heap.go:64.
		return false
	}
	if storage.IsHeapTupleLockOnly(h.Infomask) {
		return false
	}
	return true
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
	// M0093: materialise the transaction's XID BEFORE the
	// isConcurrentlyUpdated race check (line 646). Calling it
	// after would feed XID=0 into the check, letting a foreign
	// xmax stamp slip through as a false negative (orphan visible
	// tuples — the M0090 invariant we explicitly guard).
	if err := ctx.MaterializeWriterXID(); err != nil {
		return false, err
	}
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

	// Race check: between the scan-time RLock release and this Lock
	// acquire, a concurrent UPDATE/DELETE or an opportunistic prune
	// in another session may have flipped the old slot out of
	// LP_NORMAL. Detect that here, before adding the new tuple, so
	// we don't leave an orphan tuple body that a later
	// PageStampHotOldTuple would have abandoned. Caller treats
	// (false, nil) as "skip this row" — same fall-through as the
	// page-full case.
	if oldItem, ierr := storage.PageGetItemID(s.Page(), oldSlot); ierr == nil &&
		oldItem.Flags != storage.ItemIDNormal {
		s.Unlock()
		ctx.Pool.Unpin(s)
		return false, nil
	}

	// M0090-0002: under the exclusive Lock the page is frozen.
	// Re-read the old tuple and detect whether ANOTHER transaction
	// has already stamped xmax / set HeapHotUpdated. Without this
	// check, two concurrent HOT-updates of the same row both call
	// PageStampHotOldTuple — the second one OVERWRITES the first's
	// xmax + CTID, orphaning the first's new tuple in a state
	// where it remains visible under MVCC. The accumulated orphans
	// are the cause of the pgbench scale-100 1,610-visible-rows
	// symptom in pgbench_branches.
	//
	// EvalPlanQual (M0098-0004, M0099-0004): on concurrent xmax conflict,
	// wait for the conflicting transaction (with deadlock detection) and
	// fall back to the delete+insert path so it can re-check visibility.
	if oldTuple, gerr := storage.PageGetHeapTuple(s.Page(), oldSlot); gerr == nil &&
		isConcurrentlyUpdated(oldTuple.Header, ctx.Tx.XID, &ctx.Snap) {
		xmax := oldTuple.Header.Xmax
		s.Unlock()
		ctx.Pool.Unpin(s)
		if epqWait(ctx, xmax) {
			// Deadlock detected — surface 40001 immediately rather than
			// looping into the delete+insert EPQ path.
			return false, &ExecError{
				Code:    "40001",
				Message: "could not serialize access due to concurrent update (deadlock)",
			}
		}
		return false, nil // fall back to delete+insert; caller re-checks
	}

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
		if errors.Is(err, storage.ErrUnsupportedItem) {
			// PagePruneOpt above (page-full fallback) can invalidate
			// the old slot in a tight window between our pre-check
			// and this stamp. Caller treats (false, nil) as "skip
			// this row" — same fall-through as the page-full case.
			return false, nil
		}
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
func (o *updateOp) updateViaIndex(rel storage.RelFileNode, cols []catalog.Column) (TupleSlot, error) {
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
		// Recompute GENERATED ALWAYS AS … STORED columns after SET. M0096-0008.
		_ = computeGeneratedColumns(cols, newRow)
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
		epqSkip := false
		if !used {
			// HOT ineligible or page full — fall back to normal delete+insert.
			// EvalPlanQual retry loop (M0098-0004): retry up to maxEPQRetries
			// times when a concurrent xmax conflict is detected.
			for epqRetry := 0; ; epqRetry++ {
				s, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: pu.blk})
				if err != nil {
					return nil, err
				}
				s.Lock()
				// M0090-0002: detect concurrent xmax-stamp under the
				// exclusive Lock before our own stamp. Capture old tuple
				// bytes for WAL logical record (M0094-0002).
				oldTup, oldGerr := storage.PageGetHeapTuple(s.Page(), pu.slot)
				if oldGerr == nil && isConcurrentlyUpdated(oldTup.Header, o.ctx.Tx.XID, &o.ctx.Snap) {
					xmax := oldTup.Header.Xmax
					s.Unlock()
					o.ctx.Pool.Unpin(s)
					if epqRetry >= maxEPQRetries {
						return nil, &ExecError{
							Code:    "40001",
							Pos:     o.plan.Pos(),
							Message: "could not serialize access due to concurrent update",
						}
					}
					if epqWait(o.ctx, xmax) {
						return nil, &ExecError{
							Code:    "40001",
							Pos:     o.plan.Pos(),
							Message: "could not serialize access due to concurrent update (deadlock)",
						}
					}
					visible, _ := epqRecheckVisible(o.ctx, rel, pu.blk, pu.slot)
					if !visible {
						epqSkip = true // row gone — xmax committed, deleted the row
						break
					}
					// Row still visible. If xmax is no longer in InProgress the
					// conflicting transaction aborted — break so the outer code
					// can proceed with the update instead of retrying to 40001.
					// M0099: fixes permanent 40001 on rolled-back HOT updates.
					if !o.ctx.Snap.HasInProgress(xmax) {
						break // xmax aborted; proceed with update
					}
					continue // xmax still in-progress; retry
				}
				var oldTupleBytes []byte
				if oldGerr == nil {
					oldTupleBytes, _ = oldTup.MarshalBinary()
				}
				if err := storage.PageSetHeapTupleXmax(s.Page(), pu.slot, o.ctx.Tx.XID); err != nil {
					s.Unlock()
					o.ctx.Pool.Unpin(s)
					if errors.Is(err, storage.ErrUnsupportedItem) {
						// Concurrent UPDATE/DELETE or opportunistic
						// prune flipped this slot out of LP_NORMAL
						// after scan-time. Skip the row.
						epqSkip = true
						break
					}
					return nil, err
				}
				derr := markHeapDeleteDirtyAndClearVM(o.ctx, s, rel, pu.blk, pu.slot, o.ctx.Tx.XID, oldTupleBytes)
				s.Unlock()
				o.ctx.Pool.Unpin(s)
				if derr != nil {
					return nil, derr
				}
				if err := writeHeapRow(o.ctx, rel, cols, pu.newRow); err != nil {
					return nil, err
				}
				break
			}
		}
		if !epqSkip {
			o.rowsAffected++
		}
	}
	return nil, EOF
}

func (o *updateOp) Next() (TupleSlot, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true
	// M0093: UPDATE is unconditionally a write — materialise the
	// transaction's XID before the scan so foreignLockOnly /
	// isConcurrentlyUpdated / tuple-lock acquisition see the real
	// XID (zero would cause false-negative race detection and
	// would mis-classify our own locks as foreign).
	if err := o.ctx.MaterializeWriterXID(); err != nil {
		return nil, err
	}
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
		rel    storage.RelFileNode
		blk    storage.BlockNumber
		slot   uint16
		cols   []catalog.Column // columns of the source relation
		newRow Row
	}
	pending := make([]pendingUpdate, 0, 1)

	// Scan parent + partition/inheritance children. M0096-0013.
	updateScanTables := []*catalog.Table{tbl}
	if imU, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
		updateScanTables = append(updateScanTables, imU.PartitionChildren(tbl.OID)...)
		updateScanTables = append(updateScanTables, imU.InheritanceChildren(tbl.OID)...)
	}
	for _, scanTbl := range updateScanTables {
		scanRel := o.ctx.Catalog.RelFileNode(scanTbl)
		scanCols := scanTbl.Columns
		if scanTbl != tbl {
			if err := o.ctx.acquireRelLock(scanRel, lockmgr.RowExclusiveLock); err != nil {
				return nil, err
			}
		}
		captureRel := scanRel
		captureCols := scanCols
		if err := scanMatching(o.ctx, scanRel, scanCols, o.pred, func(blk storage.BlockNumber, slot uint16, row Row) error {
			nCols := len(captureCols)
			newRow := make(Row, nCols)
			for i := range captureCols {
				setIdx := i
				if setIdx < len(o.plan.Set) && o.plan.Set[setIdx] != nil {
					v, err := evalExpr(o.plan.Set[setIdx], row, o.ctx)
					if err != nil {
						return err
					}
					newRow[i] = v
				} else {
					if i < len(row) {
						newRow[i] = row[i]
					}
				}
			}
			_ = computeGeneratedColumns(captureCols, newRow)
			pending = append(pending, pendingUpdate{rel: captureRel, blk: blk, slot: slot, cols: captureCols, newRow: newRow})
			return nil
		}); err != nil {
			return nil, err
		}
	}
	hotEligibleSeq := hotUpdateEligible(o.plan, o.ctx)
	for _, pu := range pending {
		puRel := pu.rel
		if puRel == (storage.RelFileNode{}) {
			puRel = rel
		}
		puCols := pu.cols
		if puCols == nil {
			puCols = cols
		}
		used := false
		if hotEligibleSeq && puRel == rel {
			var err error
			used, err = tryApplyHOTUpdate(o.ctx, rel, cols, pu.blk, pu.slot, pu.newRow)
			if err != nil {
				return nil, err
			}
		}
		epqSkipSeq := false
		if !used {
			// EvalPlanQual retry loop (M0098-0004).
			for epqRetry := 0; ; epqRetry++ {
			s, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: puRel, Block: pu.blk})
			if err != nil {
				return nil, err
			}
			s.Lock()
			oldTup, oldGerr := storage.PageGetHeapTuple(s.Page(), pu.slot)
			if oldGerr == nil && isConcurrentlyUpdated(oldTup.Header, o.ctx.Tx.XID, &o.ctx.Snap) {
				xmax := oldTup.Header.Xmax
				s.Unlock()
				o.ctx.Pool.Unpin(s)
				if epqRetry >= maxEPQRetries {
					return nil, &ExecError{
						Code:    "40001",
						Pos:     o.plan.Pos(),
						Message: "could not serialize access due to concurrent update",
					}
				}
				if epqWait(o.ctx, xmax) {
					return nil, &ExecError{
						Code:    "40001",
						Pos:     o.plan.Pos(),
						Message: "could not serialize access due to concurrent update (deadlock)",
					}
				}
				visible, _ := epqRecheckVisible(o.ctx, puRel, pu.blk, pu.slot)
				if !visible {
					epqSkipSeq = true
					break
				}
				// Row visible; if xmax no longer in-progress → it aborted → proceed.
				if !o.ctx.Snap.HasInProgress(xmax) {
					break // xmax aborted; proceed with update
				}
				continue // xmax still running; retry
			}
			var oldTupleBytes []byte
			if oldGerr == nil {
				oldTupleBytes, _ = oldTup.MarshalBinary()
			}
			if err := storage.PageSetHeapTupleXmax(s.Page(), pu.slot, o.ctx.Tx.XID); err != nil {
				s.Unlock()
				o.ctx.Pool.Unpin(s)
				if errors.Is(err, storage.ErrUnsupportedItem) {
					continue
				}
				return nil, err
			}
			derr := markHeapDeleteDirtyAndClearVM(o.ctx, s, puRel, pu.blk, pu.slot, o.ctx.Tx.XID, oldTupleBytes)
			s.Unlock()
			o.ctx.Pool.Unpin(s)
			if derr != nil {
				return nil, derr
			}
			// For partition key UPDATE: route new row to correct partition.
			targetWriteRel := puRel
			targetWriteCols := puCols
			if imW, ok := o.ctx.Catalog.(*catalog.InMemory); ok && len(tbl.PartitionKey) > 0 {
				destPart := routeToPartition(tbl, pu.newRow, imW)
				if destPart != nil {
					targetWriteRel = o.ctx.Catalog.RelFileNode(destPart)
					targetWriteCols = destPart.Columns
					_ = computeGeneratedColumns(destPart.Columns, pu.newRow)
				}
			}
			if err := writeHeapRow(o.ctx, targetWriteRel, targetWriteCols, pu.newRow); err != nil {
				return nil, err
			}
			break // success — exit epq retry loop
			} // end epq retry loop
		} // end if !used
		if !epqSkipSeq {
			o.rowsAffected++
		}
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

func (o *deleteOp) Next() (TupleSlot, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true
	// M0093: DELETE is unconditionally a write — materialise the
	// transaction's XID before the scan so foreign-lock checks see
	// the real XID.
	if err := o.ctx.MaterializeWriterXID(); err != nil {
		return nil, err
	}
	tbl := o.plan.Table
	rel := o.ctx.Catalog.RelFileNode(tbl)

	type victim struct {
		rel  storage.RelFileNode
		blk  storage.BlockNumber
		slot uint16
		row  Row
	}
	// Collect victims from parent + partition/inheritance children. M0096-0013.
	var victims []victim
	scanTables := []*catalog.Table{tbl}
	if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
		scanTables = append(scanTables, im.PartitionChildren(tbl.OID)...)
		scanTables = append(scanTables, im.InheritanceChildren(tbl.OID)...)
	}
	for _, scanTbl := range scanTables {
		scanRel := o.ctx.Catalog.RelFileNode(scanTbl)
		if scanTbl != tbl {
			if err := o.ctx.acquireRelLock(scanRel, lockmgr.RowExclusiveLock); err != nil {
				return nil, err
			}
		}
		captureRel := scanRel // capture for closure
		if err := scanMatching(o.ctx, scanRel, scanTbl.Columns, o.pred, func(blk storage.BlockNumber, slot uint16, row Row) error {
			victims = append(victims, victim{rel: captureRel, blk: blk, slot: slot, row: cloneRow(row)})
			return nil
		}); err != nil {
			return nil, err
		}
	}
	// Fire BEFORE DELETE triggers and enforce FK constraints. M0096-0011/0012.
	filtered := victims[:0]
	for _, v := range victims {
		if len(tbl.Triggers) > 0 {
			_, ok := fireTriggers(o.ctx, tbl, "before", "delete", v.row, nil)
			if !ok {
				continue // trigger returned NULL — skip deletion
			}
		}
		if err := enforceFKOnDelete(o.ctx, tbl, v.row); err != nil {
			return nil, err
		}
		filtered = append(filtered, v)
	}
	victims = filtered
	for _, v := range victims {
		victimRel := v.rel
		if victimRel == (storage.RelFileNode{}) {
			victimRel = rel // fallback to parent rel
		}
		// EvalPlanQual retry loop (M0098-0004): on concurrent xmax conflict,
		// wait for the conflicting transaction and re-check visibility.
		epqSkipDel := false
		for epqRetry := 0; ; epqRetry++ {
		s, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: victimRel, Block: v.blk})
		if err != nil {
			return nil, err
		}
		s.Lock()
		// M0090-0002: detect concurrent xmax-stamp under the
		// exclusive Lock before our own stamp.
		oldTup, oldGerr := storage.PageGetHeapTuple(s.Page(), v.slot)
		if oldGerr == nil && isConcurrentlyUpdated(oldTup.Header, o.ctx.Tx.XID, &o.ctx.Snap) {
			xmax := oldTup.Header.Xmax
			s.Unlock()
			o.ctx.Pool.Unpin(s)
			if epqRetry >= maxEPQRetries {
				return nil, &ExecError{
					Code:    "40001",
					Pos:     o.plan.Pos(),
					Message: "could not serialize access due to concurrent update",
				}
			}
			if epqWait(o.ctx, xmax) {
				return nil, &ExecError{
					Code:    "40001",
					Pos:     o.plan.Pos(),
					Message: "could not serialize access due to concurrent update (deadlock)",
				}
			}
			visible, _ := epqRecheckVisible(o.ctx, victimRel, v.blk, v.slot)
			if !visible {
				epqSkipDel = true
				break
			}
			// Row visible; if xmax no longer in-progress → aborted → proceed.
			if !o.ctx.Snap.HasInProgress(xmax) {
				break // xmax aborted; proceed with delete
			}
			continue // xmax still running; retry
		}
		var oldTupleBytes []byte
		if oldGerr == nil {
			oldTupleBytes, _ = oldTup.MarshalBinary()
		}
		if err := storage.PageSetHeapTupleXmax(s.Page(), v.slot, o.ctx.Tx.XID); err != nil {
			s.Unlock()
			o.ctx.Pool.Unpin(s)
			if errors.Is(err, storage.ErrUnsupportedItem) {
				epqSkipDel = true
				break
			}
			return nil, err
		}
		derr := markHeapDeleteDirtyAndClearVM(o.ctx, s, victimRel, v.blk, v.slot, o.ctx.Tx.XID, oldTupleBytes)
		s.Unlock()
		o.ctx.Pool.Unpin(s)
		if derr != nil {
			return nil, derr
		}
		break // success — exit epq retry loop
		} // end epq retry loop
		if !epqSkipDel {
			o.rowsAffected++
		}
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
			if !mvcc.TupleVisibleSubxact(tuple.Header, ctx.Snap, ctx.Tx.XID, ctx.TxnMgr) {
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
				if v.IsNull() || v.Kind != KindBool || !v.BoolValue() {
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

	// M0093: lazily materialise the transaction's XID before any
	// xmin stamp. ToastLargeColumnsIfNeeded may itself call
	// NewHeapTuple for the TOAST chunk relation; doing this at the
	// top covers both the TOAST writes and the main-heap NewHeapTuple
	// below.
	if err := ctx.MaterializeWriterXID(); err != nil {
		return ptr, err
	}

	// TOAST oversized column values before encoding (M0046-0006).
	var toastErr error
	row, toastErr = ToastLargeColumnsIfNeeded(ctx, rel, cols, row)
	if toastErr != nil {
		return ptr, &ExecError{Code: "XX000", Message: toastErr.Error()}
	}

	body, err := EncodeRow(cols, row)
	if err != nil {
		// Preserve ExecError (e.g. 22P02 for invalid input syntax) so the
		// SQLSTATE and message reach the client unchanged. M0097-0003.
		var ee *ExecError
		if errors.As(err, &ee) {
			return ptr, ee
		}
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
			// Update FSM with remaining free space (M0046-0003).
			if ctx.FSM != nil {
				ctx.FSM.RecordFreeSpaceForPage(rel, blk, slot.Page())
			}
			// Clear VM: page was modified, no longer ALL_VISIBLE (M0046-0004).
			if ctx.VM != nil {
				ctx.VM.ClearBlock(rel, blk)
			}
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
		// Page full: invalidate FSM entry so future lookups skip it.
		if ctx.FSM != nil {
			ctx.FSM.RecordFreeSpace(rel, blk, 0)
		}
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		return false, nil
	}

	// FSM consultation (M0046-0003): if a page freed by a previous
	// VACUUM has enough room, use it before trying the last block or
	// extending the relation.
	minFreeBytes := uint16(len(tupleBytes) + 4) // 4 = itemIDSize (line pointer size)
	if ctx.FSM != nil {
		if fsmBlk, ok := ctx.FSM.GetPageWithFreeSpace(rel, minFreeBytes); ok {
			appended, err := tryAppendToBlock(fsmBlk)
			if err != nil {
				return ptr, err
			}
			if appended {
				return ptr, nil
			}
			// Stale FSM entry — invalidation was already done in
			// tryAppendToBlock above; fall through to normal path.
		}
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
	// New page: record its free space in the FSM (M0046-0003).
	if ctx.FSM != nil {
		ctx.FSM.RecordFreeSpaceForPage(rel, blk, slot.Page())
	}
	// New page starts dirty — not ALL_VISIBLE (M0046-0004).
	if ctx.VM != nil {
		ctx.VM.ClearBlock(rel, blk)
	}
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
// stamp paths (UPDATE old image + DELETE). oldTuple carries the
// pre-delete heap-tuple bytes for logical replication; pass nil
// when not needed (DDL, UPSERT). When the pool has a LogHeapDelete
// hook configured, subsequent dirties of the same page in an epoch
// emit a logical record instead of a full FPI.
func markHeapDeleteDirty(
	pool *storage.Pool, slot *storage.Slot,
	rel storage.RelFileNode, blk storage.BlockNumber,
	lineSlot uint16, xmax storage.TransactionID,
	oldTuple []byte,
) error {
	logDel := pool.LogHeapDelete()
	if logDel == nil {
		pool.MarkDirty(slot)
		return nil
	}
	return pool.MarkDirtyChangeRecord(slot, func() (storage.LSN, error) {
		return logDel(rel, blk, lineSlot, xmax, oldTuple)
	})
}

// markHeapDeleteDirtyAndClearVM is markHeapDeleteDirty + VM clear (M0046-0004).
// Any page that has a tuple deleted is no longer ALL_VISIBLE.
func markHeapDeleteDirtyAndClearVM(
	ctx *Context, slot *storage.Slot,
	rel storage.RelFileNode, blk storage.BlockNumber,
	lineSlot uint16, xmax storage.TransactionID,
	oldTuple []byte,
) error {
	if err := markHeapDeleteDirty(ctx.Pool, slot, rel, blk, lineSlot, xmax, oldTuple); err != nil {
		return err
	}
	if ctx.VM != nil {
		ctx.VM.ClearBlock(rel, blk)
	}
	return nil
}
