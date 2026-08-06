package executor

import (
	"strings"

	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// CTEFencePtr identifies one heap tuple for the DML-CTE write fence.
//
// The relation is part of the key because an ItemPointer alone is not unique
// across relations: {block 0, offset 1} exists in every table, so a
// relation-blind fence hides an unrelated table's row from the rest of the
// statement. That collision was already known — the EvalPlanQual site in
// operators_storage.go worked around it by re-reading the tuple and checking
// xmin — and M0125-0052 made it reachable in the common case by registering
// plain INSERTs, which land at low block/offset numbers in every table.
type CTEFencePtr struct {
	Rel storage.RelFileNode
	Ptr storage.ItemPointer
}

// cteFenceInsert registers a tuple this DML CTE just inserted, so every later
// scan in the same statement skips it. No-op outside a DML CTE.
//
// PostgreSQL needs no such set: all sub-statements of a data-modifying WITH
// share `estate->es_snapshot` AND `estate->es_output_cid`, so a sibling's
// tuple is filtered by the cmin test in HeapTupleSatisfiesMVCC
// (postgres/src/backend/access/heap/heapam_visibility.c). goopg's heap has no
// per-tuple command id, so the fence stands in for the cmin test.
func cteFenceInsert(ctx *Context, rel storage.RelFileNode, ptr storage.ItemPointer) {
	if !ctx.InDMLCTE || ctx.CTEWriteFence == nil {
		return
	}
	ctx.CTEWriteFence[CTEFencePtr{Rel: rel, Ptr: ptr}] = struct{}{}
}

// cteFenceUpdate registers the new version of a tuple a DML CTE just updated
// and remembers which tuple it replaced. When the replaced tuple was itself
// written by an earlier DML CTE, the original (pre-CTE) tuple is recorded in
// CTESelfModifiedErrors so the outer UPDATE/DELETE raises
// ERRCODE_TRIGGERED_DATA_CHANGE_VIOLATION. No-op outside a DML CTE.
// oldRel and newRel differ when the update moved the row across partitions.
func cteFenceUpdate(ctx *Context, oldRel storage.RelFileNode, oldPtr storage.ItemPointer,
	newRel storage.RelFileNode, newPtr storage.ItemPointer) {
	if !ctx.InDMLCTE || ctx.CTEWriteFence == nil {
		return
	}
	oldKey := CTEFencePtr{Rel: oldRel, Ptr: oldPtr}
	newKey := CTEFencePtr{Rel: newRel, Ptr: newPtr}
	if _, inFence := ctx.CTEWriteFence[oldKey]; inFence {
		if ctx.CTENewToOld != nil {
			if orig, ok := ctx.CTENewToOld[oldKey]; ok {
				if ctx.CTESelfModifiedErrors == nil {
					ctx.CTESelfModifiedErrors = make(map[CTEFencePtr]struct{})
				}
				ctx.CTESelfModifiedErrors[orig] = struct{}{}
			}
		}
	}
	ctx.CTEWriteFence[newKey] = struct{}{}
	if ctx.CTENewToOld != nil {
		ctx.CTENewToOld[newKey] = oldKey
	}
	// The old version now carries our own xmax, which would hide it from the
	// rest of the statement — but PG still shows its PRE-IMAGE there. Reveal
	// it. M0125-0053.
	cteFenceDelete(ctx, oldRel, oldPtr)
}

// cteFenceDelete registers a tuple whose xmax this DML CTE just stamped, so
// read scans in the rest of the statement still see its pre-image. No-op
// outside a DML CTE. See Context.CTEXmaxReveal for why writes must not
// consult the set this fills.
func cteFenceDelete(ctx *Context, rel storage.RelFileNode, ptr storage.ItemPointer) {
	if !ctx.InDMLCTE || ctx.CTEXmaxReveal == nil {
		return
	}
	ctx.CTEXmaxReveal[CTEFencePtr{Rel: rel, Ptr: ptr}] = struct{}{}
}

// cteRevealFn answers "is this slot of the page being scanned a pre-image a
// DML CTE of this statement removed?". nil means no reveal is in effect —
// which is the case for every statement that has no data-modifying WITH, and
// for every DML target scan regardless.
type cteRevealFn func(slot uint16) bool

// cteRevealed is the direct (non-closure) form of cteRevealFn, for read scans
// that test one slot at a time rather than walking a HOT chain.
func cteRevealed(ctx *Context, rel storage.RelFileNode, blk storage.BlockNumber, slot uint16) bool {
	if len(ctx.CTEXmaxReveal) == 0 {
		return false
	}
	_, ok := ctx.CTEXmaxReveal[CTEFencePtr{Rel: rel, Ptr: storage.ItemPointer{Block: blk, Offset: slot}}]
	return ok
}

// cteRevealHeader returns h with its xmax cleared, so the ordinary visibility
// test judges the tuple as if this statement's DML CTE had never stamped it.
//
// Being in CTEXmaxReveal is NOT on its own a licence to show the tuple: PG
// relaxes only the cmax arm of HeapTupleSatisfiesMVCC, and the xmin snapshot
// test still has to pass. The difference is not academic — INSERT … ON
// CONFLICT DO UPDATE carries a documented MVCC violation that lets it update a
// tuple *not visible to the command's snapshot* (see the header comment of
// postgres/src/test/isolation/specs/insert-conflict-do-update-3.spec), so a
// CTE upsert can stamp a version this statement was never allowed to see.
// Revealing that version unconditionally produced a duplicate key in
// TestPort_IsolationInsertConflictDoUpdate3: the snapshot-visible version of
// the row was returned alongside it.
//
// Clearing Xmax alone is sufficient — TupleVisible/TupleVisibleSubxact reach
// no xmax infomask bit once Xmax is invalid — and the header is passed by
// value, so the caller's tuple is untouched.
func cteRevealHeader(h storage.HeapTupleHeader) storage.HeapTupleHeader {
	h.Xmax = storage.InvalidTransactionID
	return h
}

// cteRevealFor builds the reveal predicate for a read scan of one heap block,
// or returns nil when there is nothing to reveal. Returning nil rather than an
// always-false closure keeps the ordinary scan path allocation-free.
func cteRevealFor(ctx *Context, rel storage.RelFileNode, blk storage.BlockNumber) cteRevealFn {
	if ctx == nil || len(ctx.CTEXmaxReveal) == 0 {
		return nil
	}
	return func(slot uint16) bool {
		_, ok := ctx.CTEXmaxReveal[CTEFencePtr{Rel: rel, Ptr: storage.ItemPointer{Block: blk, Offset: slot}}]
		return ok
	}
}

// cteFenced reports whether a tuple was written by a DML CTE of this
// statement and must therefore be skipped by the rest of it.
func cteFenced(ctx *Context, rel storage.RelFileNode, blk storage.BlockNumber, slot uint16) bool {
	if ctx.CTEWriteFence == nil {
		return false
	}
	_, ok := ctx.CTEWriteFence[CTEFencePtr{Rel: rel, Ptr: storage.ItemPointer{Block: blk, Offset: slot}}]
	return ok
}

// cteDMLPrefixOp executes data-modifying CTEs (INSERT/UPDATE/DELETE/MERGE)
// before handing control to the outer query plan. Each DML plan's RETURNING
// rows are materialized into ctx.MaterializedCTEs so that MaterializedCTEScan
// operators in the outer query can read them.
type cteDMLPrefixOp struct {
	plan  *planner.CTEDMLPrefix
	ctx   *Context
	inner Operator // outer query operator

	// scope is the instrumenter active on this op's own Build() call,
	// handed over by maybeInstrument (instrumentScopeCarrier). The DML
	// plans and the outer body below are only Build() at Open() time —
	// after the top-level withInstrumentation() call has already
	// restored the package-global instrumentScope — so Open()
	// reinstates it around each nested Build() to keep those nodes
	// under EXPLAIN ANALYZE's instrumentation.
	scope *instrumenter
}

func newCTEDMLPrefixOp(p *planner.CTEDMLPrefix) *cteDMLPrefixOp {
	return &cteDMLPrefixOp{plan: p}
}

func (o *cteDMLPrefixOp) setInstrumentScope(s *instrumenter) { o.scope = s }

// buildUnderScope runs Build(n) with the package-global instrumentScope
// temporarily set to o.scope, so maybeInstrument wraps n's operator (and
// records its stats in the same nodeStatsTable the EXPLAIN renderer
// reads) exactly as if it had been Build() during the original dispatch.
func (o *cteDMLPrefixOp) buildUnderScope(n planner.Node) (Operator, error) {
	prev := instrumentScope
	instrumentScope = o.scope
	defer func() { instrumentScope = prev }()
	return Build(n)
}

func (o *cteDMLPrefixOp) Schema() planner.Schema { return o.plan.Body.Output() }

func (o *cteDMLPrefixOp) Open(ctx *Context) error {
	o.ctx = ctx

	// Ensure MaterializedCTEs map exists.
	if ctx.MaterializedCTEs == nil {
		ctx.MaterializedCTEs = make(map[string][][]Datum)
	}

	// CTE snapshot isolation: save the statement-start snapshot and
	// initialise the write fence. The outer query will restore the
	// snapshot and skip any rows written by the DML CTEs so that
	// PostgreSQL CTE semantics hold (outer SELECT sees pre-CTE state).
	savedSnap := ctx.Snap
	ctx.CTEWriteFence = make(map[CTEFencePtr]struct{})
	ctx.CTEXmaxReveal = make(map[CTEFencePtr]struct{})
	ctx.CTENewToOld = make(map[CTEFencePtr]CTEFencePtr)
	ctx.CTESelfModifiedErrors = make(map[CTEFencePtr]struct{})
	ctx.InDMLCTE = true

	// Execute each DML CTE in order, collecting RETURNING rows.
	for i, dml := range o.plan.DMls {
		op, err := o.buildUnderScope(dml)
		if err != nil {
			ctx.InDMLCTE = false
			ctx.Snap = savedSnap
			return err
		}
		if err := op.Open(ctx); err != nil {
			ctx.InDMLCTE = false
			ctx.Snap = savedSnap
			return err
		}
		var rows [][]Datum
		for {
			slot, err := op.Next()
			if err == EOF {
				break
			}
			if err != nil {
				op.Close()
				ctx.InDMLCTE = false
				ctx.Snap = savedSnap
				return err
			}
			// Materialize the row so it survives after op.Close().
			r := slotRow(slot)
			owned := make([]Datum, len(r))
			copy(owned, r)
			rows = append(rows, owned)
		}
		op.Close()
		key := strings.ToLower(o.plan.Names[i])
		ctx.MaterializedCTEs[key] = rows
	}

	// Restore snapshot and clear InDMLCTE before running the outer query.
	// The outer SELECT uses the statement-start snapshot (pre-CTE state)
	// and the CTEWriteFence skips any rows written by the DML CTEs above.
	ctx.InDMLCTE = false
	ctx.Snap = savedSnap

	// Now build and open the outer query plan.
	inner, err := o.buildUnderScope(o.plan.Body)
	if err != nil {
		return err
	}
	if err := inner.Open(ctx); err != nil {
		return err
	}
	o.inner = inner
	return nil
}

func (o *cteDMLPrefixOp) Close() error {
	if o.inner != nil {
		return o.inner.Close()
	}
	return nil
}

func (o *cteDMLPrefixOp) Next() (TupleSlot, error) {
	return o.inner.Next()
}

// materializedCTEScanOp reads rows from ctx.MaterializedCTEs[name].
// Used when the outer SELECT references a data-modifying CTE by name.
type materializedCTEScanOp struct {
	plan *planner.MaterializedCTEScan
	rows [][]Datum
	idx  int
}

func newMaterializedCTEScanOp(p *planner.MaterializedCTEScan) *materializedCTEScanOp {
	return &materializedCTEScanOp{plan: p}
}

// cteScanOp executes a regular (SELECT) CTE with two modes:
//
//   - Streaming mode (recursive CTEs): passes rows from child directly.
//     Used when Child is *planner.WorkTableScan (recursive self-reference)
//     or *planner.RecursiveUnion (outer reference). LIMIT must be able to
//     stop a recursive CTE before it buffers infinitely. M0097-0099.
//
//   - Materializing mode (non-recursive CTEs): buffers all rows on first
//     Open(), replays from ctx.CTERowCache on subsequent Open()s. This
//     implements PostgreSQL's CTE optimization-fence: volatile CTEs
//     (e.g. random()) produce the same rows every reference. M0097-0099.
type cteScanOp struct {
	plan      *planner.CTEScan
	child     Operator
	streaming bool // true = don't cache; stream from child
	rows      []Row
	idx       int
}

func newCteScanOp(p *planner.CTEScan) (*cteScanOp, error) {
	child, err := Build(p.Child)
	if err != nil {
		return nil, err
	}
	return &cteScanOp{plan: p, child: child}, nil
}

func (o *cteScanOp) Schema() planner.Schema { return o.plan.Output() }

func (o *cteScanOp) isStreamingChild() bool {
	switch o.plan.Child.(type) {
	case *planner.WorkTableScan, *planner.RecursiveUnion:
		return true
	}
	// If the child plan subtree contains a WorkTableScan (e.g. a non-recursive
	// CTE that wraps another CTE which is the recursive work table reference),
	// we must stream to avoid caching stale rows from the first iteration.
	return planContainsWorkTableScan(o.plan.Child)
}

// planContainsWorkTableScan walks the plan tree looking for a WorkTableScan node.
// This is needed to detect CTEs whose body (even indirectly) reads from a recursive
// CTE's work table — those CTEs must be streamed, not materialized.
func planContainsWorkTableScan(n planner.Node) bool {
	if n == nil {
		return false
	}
	if _, ok := n.(*planner.WorkTableScan); ok {
		return true
	}
	if scan, ok := n.(*planner.CTEScan); ok {
		return planContainsWorkTableScan(scan.Child)
	}
	if ru, ok := n.(*planner.RecursiveUnion); ok {
		return planContainsWorkTableScan(ru.Anchor) || planContainsWorkTableScan(ru.Recursive)
	}
	if p, ok := n.(*planner.Project); ok {
		return planContainsWorkTableScan(p.Child)
	}
	if f, ok := n.(*planner.Filter); ok {
		return planContainsWorkTableScan(f.Child)
	}
	if s, ok := n.(*planner.Sort); ok {
		return planContainsWorkTableScan(s.Child)
	}
	if so, ok := n.(*planner.SetOp); ok {
		return planContainsWorkTableScan(so.Left) || planContainsWorkTableScan(so.Right)
	}
	return false
}

func (o *cteScanOp) Open(ctx *Context) error {
	// Streaming mode: WorkTableScan (recursive self-reference) and RecursiveUnion
	// (outer reference to recursive CTE) must NOT be cached. Both need lazy row
	// delivery so LIMIT can stop them before the full sequence is produced.
	if o.isStreamingChild() {
		o.streaming = true
		return o.child.Open(ctx)
	}

	// Key by DECLARATION, not by name: `WITH x` in two disjoint scopes is two
	// declarations that must materialize separately, and keying by "x" made
	// the second replay the first's rows (M0125-0050 — goopg answered 1,1
	// where PG answers 1,2). See planner.CTEScan.DeclKey for why the key is
	// the declaration site rather than the plannedCTE pointer.
	key := o.plan.DeclKey()
	if ctx.CTERowCache != nil {
		if cached, ok := ctx.CTERowCache[key]; ok {
			// Replay from cache (second or later reference to this CTE).
			o.rows = cached
			o.idx = 0
			return nil
		}
	}
	// First reference: run the child plan and buffer all rows.
	if err := o.child.Open(ctx); err != nil {
		return err
	}
	var rows []Row
	for {
		slot, err := o.child.Next()
		if err == EOF {
			break
		}
		if err != nil {
			o.child.Close()
			return err
		}
		// Clone the row so it survives after child is closed.
		r := slotRow(slot)
		owned := make(Row, len(r))
		copy(owned, r)
		rows = append(rows, owned)
	}
	o.child.Close()
	// Store in cache so subsequent scans can replay.
	if ctx.CTERowCache == nil {
		ctx.CTERowCache = make(map[string][]Row)
	}
	ctx.CTERowCache[key] = rows
	o.rows = rows
	o.idx = 0
	return nil
}

func (o *cteScanOp) Close() error {
	if o.streaming {
		return o.child.Close()
	}
	return nil
}

func (o *cteScanOp) Next() (TupleSlot, error) {
	if o.streaming {
		return o.child.Next()
	}
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.idx]
	o.idx++
	return SlotFromRow(o.plan.Output(), row), nil
}

func (o *materializedCTEScanOp) Schema() planner.Schema { return o.plan.Output() }

func (o *materializedCTEScanOp) Open(ctx *Context) error {
	key := strings.ToLower(o.plan.Name)
	if ctx.MaterializedCTEs != nil {
		o.rows = ctx.MaterializedCTEs[key]
	}
	o.idx = 0
	return nil
}

func (o *materializedCTEScanOp) Close() error { return nil }

func (o *materializedCTEScanOp) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.idx]
	o.idx++
	return SlotFromRow(o.plan.Output(), row), nil
}
