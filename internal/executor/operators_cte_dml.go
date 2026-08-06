package executor

import (
	"strings"

	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

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
	ctx.CTEWriteFence = make(map[storage.ItemPointer]struct{})
	ctx.CTENewToOld = make(map[storage.ItemPointer]storage.ItemPointer)
	ctx.CTESelfModifiedErrors = make(map[storage.ItemPointer]struct{})
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
