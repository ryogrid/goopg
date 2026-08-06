package executor

import (
	"fmt"
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

// cteFenceInsert registers a tuple a sub-statement of a data-modifying WITH
// just inserted, so every later scan in the same statement skips it. No-op
// outside such a statement.
//
// PostgreSQL needs no such set: all sub-statements of a data-modifying WITH
// share `estate->es_snapshot` AND `estate->es_output_cid`, so a sibling's
// tuple is filtered by the cmin test in HeapTupleSatisfiesMVCC
// (postgres/src/backend/access/heap/heapam_visibility.c). goopg's heap has no
// per-tuple command id, so the fence stands in for the cmin test.
//
// The OUTER statement's own writes belong in the fence too (M0125-0054): once
// the outer body runs first, a CTE deferred to the post-body phase would
// otherwise see rows the outer statement inserted, where PG's cmin test hides
// them. That is why the gate is the fence's existence, not InDMLCTE.
func cteFenceInsert(ctx *Context, rel storage.RelFileNode, ptr storage.ItemPointer) {
	if ctx.CTEWriteFence == nil {
		return
	}
	ctx.CTEWriteFence[CTEFencePtr{Rel: rel, Ptr: ptr}] = struct{}{}
}

// cteFenceUpdate registers the new version of a tuple a DML CTE just updated
// and remembers which tuple it replaced. When the replaced tuple was itself
// written by an earlier DML CTE, the original (pre-CTE) tuple is recorded in
// CTESelfModifiedErrors so the outer UPDATE/DELETE raises
// ERRCODE_TRIGGERED_DATA_CHANGE_VIOLATION. No-op outside a data-modifying
// WITH. oldRel and newRel differ when the update moved the row across
// partitions.
//
// The fence and the reveal cover the outer statement's writes as well
// (M0125-0054), but the self-modified bookkeeping stays gated on InDMLCTE:
// it exists to catch a sub-command *inside* a CTE re-modifying a CTE-written
// row, and the outer body's own writes are the normal case, not that one.
func cteFenceUpdate(ctx *Context, oldRel storage.RelFileNode, oldPtr storage.ItemPointer,
	newRel storage.RelFileNode, newPtr storage.ItemPointer) {
	if ctx.CTEWriteFence == nil {
		return
	}
	oldKey := CTEFencePtr{Rel: oldRel, Ptr: oldPtr}
	newKey := CTEFencePtr{Rel: newRel, Ptr: newPtr}
	if _, inFence := ctx.CTEWriteFence[oldKey]; inFence && ctx.InDMLCTE {
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

// cteFenceDelete registers a tuple whose xmax a sub-statement of this
// data-modifying WITH just stamped, so read scans in the rest of the statement
// still see its pre-image. No-op outside such a statement. See
// Context.CTEXmaxReveal for why writes must not consult the set this fills.
//
// As with the fence, the outer statement's own victims belong here too
// (M0125-0054): PG's cmax-vs-es_output_cid test is symmetric between the CTEs
// and the main plan, so a CTE deferred to the post-body phase must still see
// the pre-image of a row the outer statement removed.
func cteFenceDelete(ctx *Context, rel storage.RelFileNode, ptr storage.ItemPointer) {
	if ctx.CTEXmaxReveal == nil {
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

// cteDMLPrefixOp drives the data-modifying CTEs (INSERT/UPDATE/DELETE/MERGE)
// of one statement. Each DML plan's RETURNING rows are materialized into
// ctx.MaterializedCTEs so that MaterializedCTEScan operators in the outer
// query can read them.
//
// The name is now a misnomer: the CTEs are NOT a prefix. PostgreSQL runs the
// main plan first and only afterwards the data-modifying CTEs nothing pulled
// from, in ExecPostprocessPlan over estate->es_auxmodifytables
// (postgres/src/backend/executor/execMain.c); a CTE the main plan DOES read is
// run by the CteScan that pulls from it, i.e. at the moment of demand. Both
// halves are reproduced here (M0125-0054): runDMLCTE is called from
// materializedCTEScanOp.Open for the referenced CTEs, and from runPending —
// the ExecPostprocessPlan analogue — once the body reaches EOF.
//
// Running them up front, as this operator did until M0125-0054, is observable
// whenever the outer statement writes a row a CTE also writes: whichever
// sub-statement runs SECOND finds the row already stamped by this same command
// and declines it. `WITH x AS (INSERT INTO t VALUES ('cte') RETURNING tag)
// INSERT INTO t SELECT 'outer' RETURNING tag` puts 'outer' at ctid (0,1) and
// 'cte' at (0,2) on live PG 18.3 — the reverse of the old prefix order.
type cteDMLPrefixOp struct {
	plan  *planner.CTEDMLPrefix
	ctx   *Context
	inner Operator // outer query operator

	// ran[i] is set once DMls[i] has been executed to completion, running[i]
	// while it is executing. running guards against a CTE reachable from its
	// own body; references may only point backwards, so it should be
	// unreachable, but a demand-driven runner must not recurse forever if it
	// ever is.
	ran     []bool
	running []bool

	// prevPending restores ctx.pendingDMLCTEs at Close, so a nested driver
	// (currently rejected by the analyzer — a data-modifying WITH below the
	// top level is an error, M0125-0051) could not strand the outer one.
	prevPending *cteDMLPrefixOp

	// drained records that the post-body phase has already been attempted, so
	// Close does not repeat what Next already did. failed suppresses that
	// phase after the body raised: PG never reaches ExecutorFinish on the
	// error path either.
	drained bool
	failed  bool

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

	// CTE snapshot isolation: initialise the write fence and its mirror.
	// Every sub-statement of this WITH — the CTEs and the outer body alike —
	// registers what it writes, and every scan of the others skips it, so
	// PostgreSQL's "the sub-statements cannot see one another's effects on the
	// target tables" holds regardless of the order they run in.
	ctx.CTEWriteFence = make(map[CTEFencePtr]struct{})
	ctx.CTEXmaxReveal = make(map[CTEFencePtr]struct{})
	ctx.CTENewToOld = make(map[CTEFencePtr]CTEFencePtr)
	ctx.CTESelfModifiedErrors = make(map[CTEFencePtr]struct{})
	ctx.InDMLCTE = false

	o.ran = make([]bool, len(o.plan.DMls))
	o.running = make([]bool, len(o.plan.DMls))
	o.prevPending = ctx.pendingDMLCTEs
	ctx.pendingDMLCTEs = o

	// The main plan goes first. A MaterializedCTEScan inside it reaches back
	// through ctx.pendingDMLCTEs and runs the CTE it reads before returning a
	// row, so a referenced CTE still completes before its consumer sees
	// anything — which is what PG's CteScan-driven ModifyTable does.
	inner, err := o.buildUnderScope(o.plan.Body)
	if err != nil {
		ctx.pendingDMLCTEs = o.prevPending
		return err
	}
	if err := inner.Open(ctx); err != nil {
		ctx.pendingDMLCTEs = o.prevPending
		return err
	}
	o.inner = inner
	return nil
}

// runDMLCTE executes DMls[i] to completion and files its RETURNING rows under
// Names[i]. Idempotent: the second call for an already-run CTE is a no-op,
// which is what makes demand-driving and the post-body sweep composable.
func (o *cteDMLPrefixOp) runDMLCTE(i int) error {
	if o.ran[i] {
		return nil
	}
	if o.running[i] {
		return &ExecError{
			Code:    "42P19",
			Pos:     o.plan.Pos(),
			Message: fmt.Sprintf("data-modifying WITH query %q refers to itself", o.plan.Names[i]),
		}
	}
	o.running[i] = true
	ctx := o.ctx
	savedSnap := ctx.Snap
	savedInCTE := ctx.InDMLCTE
	ctx.InDMLCTE = true
	defer func() {
		o.running[i] = false
		ctx.InDMLCTE = savedInCTE
		// The sub-statements all read the statement-start snapshot; a DML
		// plan that took its own must not leak it back to the caller.
		ctx.Snap = savedSnap
	}()

	op, err := o.buildUnderScope(o.plan.DMls[i])
	if err != nil {
		return err
	}
	if err := op.Open(ctx); err != nil {
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
			return err
		}
		// Materialize the row so it survives after op.Close().
		r := slotRow(slot)
		owned := make([]Datum, len(r))
		copy(owned, r)
		rows = append(rows, owned)
	}
	op.Close()
	o.ran[i] = true
	ctx.MaterializedCTEs[strings.ToLower(o.plan.Names[i])] = rows
	return nil
}

// ensureCTE runs the named DML CTE if it has not run yet. Called by
// materializedCTEScanOp.Open — the demand half of PG's model.
func (o *cteDMLPrefixOp) ensureCTE(name string) error {
	for i, n := range o.plan.Names {
		if strings.EqualFold(n, name) {
			return o.runDMLCTE(i)
		}
	}
	return nil
}

// runPending is the ExecPostprocessPlan analogue: after the main plan is done,
// every data-modifying CTE nothing pulled from is run to completion.
//
// The order is REVERSE declaration order, which is not an arbitrary choice.
// ExecInitModifyTable files each non-canSetTag ModifyTable with `lcons`, not
// lappend (postgres/src/backend/executor/nodeModifyTable.c), so
// es_auxmodifytables is in reverse initialization order and
// ExecPostprocessPlan walks it head-first. Upstream's own comment gives the
// reason: a later CTE may read an earlier one, and running the later one first
// lets its CteScan drive the earlier one rather than finding its RETURNING
// rows already thrown away. Confirmed on live PG 18.3 (2026-08-06): three
// unreferenced INSERT CTEs a, b, c land at ctid (0,1)=c, (0,2)=b, (0,3)=a.
//
// goopg reaches the same place from the other side — a deferred CTE that reads
// an earlier one demands it through ensureCTE — so the traversal order only
// has to match PG's observable heap order, and this is it.
func (o *cteDMLPrefixOp) runPending() error {
	for i := len(o.plan.DMls) - 1; i >= 0; i-- {
		if err := o.runDMLCTE(i); err != nil {
			return err
		}
	}
	return nil
}

func (o *cteDMLPrefixOp) Close() error {
	// A cursor closed before its body was exhausted never reached the EOF in
	// Next; PG still runs the leftover CTEs (ExecutorFinish precedes
	// ExecutorEnd), so do it here, with the body still open as it is there.
	var err error
	if !o.drained && !o.failed && o.ctx != nil {
		o.drained = true
		err = o.runPending()
	}
	if o.ctx != nil {
		o.ctx.pendingDMLCTEs = o.prevPending
	}
	if o.inner != nil {
		if cerr := o.inner.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}
	return err
}

func (o *cteDMLPrefixOp) Next() (TupleSlot, error) {
	slot, err := o.inner.Next()
	switch {
	case err == EOF:
		if !o.drained {
			o.drained = true
			if perr := o.runPending(); perr != nil {
				o.failed = true
				return nil, perr
			}
		}
		return nil, EOF
	case err != nil:
		o.failed = true
	}
	return slot, err
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
	// Demand half of PG's model: a data-modifying CTE runs when something
	// pulls from it. Reaching the driver here rather than deciding
	// referenced-ness at plan time keeps the split fail-safe — a CTE the
	// planner would have judged unreferenced still runs before its first row
	// is read. M0125-0054.
	if ctx.pendingDMLCTEs != nil {
		if err := ctx.pendingDMLCTEs.ensureCTE(key); err != nil {
			return err
		}
	}
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
