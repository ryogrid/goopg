package executor

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// routineCommandCounterIncrement is goopg's CommandCounterIncrement: it moves
// a stored routine's body one command id past its caller, so the body sees
// everything the calling statement has written so far.
//
// PostgreSQL does this for every statement of a routine that is NOT
// `readonly_func` — i.e. every VOLATILE one
// (postgres/src/backend/executor/functions.c, `postquel_getnext`: "If not
// read-only, be sure to advance the command counter for each command, so that
// all work to date in this transaction is visible", with the active snapshot's
// command id bumped to match; `readonly_func` is set from
// `provolatile != PROVOLATILE_VOLATILE` in `init_sql_fcache`). PL/pgSQL reaches
// the same place through SPI, which increments the counter per statement unless
// the plan is read-only.
//
// STABLE and IMMUTABLE routines keep the caller's command id, which is why the
// volatility test is here and not at the call sites. The only state the command
// id currently gates is the data-modifying-WITH fence and its reveal, so this is
// a no-op for every statement that has no such WITH in flight.
func routineCommandCounterIncrement(child *Context, r *catalog.Routine) {
	if child == nil || r == nil {
		return
	}
	switch strings.ToLower(r.Volatile) {
	case "s", "i":
		return // readonly_func: no CommandCounterIncrement upstream either
	}
	// M0129-S8.2: advance the child's command counter. CommandCounterIncrement
	// only actually advances when the used flag is set (a write happened in
	// the parent), matching PostgreSQL's lazy-advance scheme. Then pin the
	// new command id as the child's es_output_cid.
	child.CommandCounterIncrement()
	child.CmdID = child.GetCurrentCommandId(true)
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
	plan  *optimizer.CTEDMLPrefix
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

func newCTEDMLPrefixOp(p *optimizer.CTEDMLPrefix) *cteDMLPrefixOp {
	return &cteDMLPrefixOp{plan: p}
}

func (o *cteDMLPrefixOp) setInstrumentScope(s *instrumenter) { o.scope = s }

// buildUnderScope runs Build(n) with the package-global instrumentScope
// temporarily set to o.scope, so maybeInstrument wraps n's operator (and
// records its stats in the same nodeStatsTable the EXPLAIN renderer
// reads) exactly as if it had been Build() during the original dispatch.
//
// EX0-03b: takes instrumentScopeMu like every other global handoff. This
// path runs at Open time, which under a Gather is concurrent across
// workers — the old unguarded save/restore was safe only while the CTE
// path was serial.
func (o *cteDMLPrefixOp) buildUnderScope(n optimizer.Node) (Operator, error) {
	instrumentScopeMu.Lock()
	defer instrumentScopeMu.Unlock()
	prev := instrumentScope
	instrumentScope = o.scope
	defer func() { instrumentScope = prev }()
	return Build(n)
}

func (o *cteDMLPrefixOp) Schema() optimizer.Schema { return o.plan.Body.Output() }

func (o *cteDMLPrefixOp) Open(ctx *Context) error {
	o.ctx = ctx

	// Ensure MaterializedCTEs map exists.
	if ctx.MaterializedCTEs == nil {
		ctx.MaterializedCTEs = make(map[string][][]Datum)
	}

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
	defer func() {
		o.running[i] = false
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
	plan *optimizer.MaterializedCTEScan
	rows [][]Datum
	idx  int
}

func newMaterializedCTEScanOp(p *optimizer.MaterializedCTEScan) *materializedCTEScanOp {
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
	plan      *optimizer.CTEScan
	child     Operator
	streaming bool // true = don't cache; stream from child
	rows      []Row
	idx       int
}

func newCteScanOp(p *optimizer.CTEScan) (*cteScanOp, error) {
	child, err := Build(p.Child)
	if err != nil {
		return nil, err
	}
	return &cteScanOp{plan: p, child: child}, nil
}

func (o *cteScanOp) Schema() optimizer.Schema { return o.plan.Output() }

func (o *cteScanOp) isStreamingChild() bool {
	switch o.plan.Child.(type) {
	case *optimizer.WorkTableScan, *optimizer.RecursiveUnion:
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
func planContainsWorkTableScan(n optimizer.Node) bool {
	if n == nil {
		return false
	}
	if _, ok := n.(*optimizer.WorkTableScan); ok {
		return true
	}
	if scan, ok := n.(*optimizer.CTEScan); ok {
		return planContainsWorkTableScan(scan.Child)
	}
	if ru, ok := n.(*optimizer.RecursiveUnion); ok {
		return planContainsWorkTableScan(ru.Anchor) || planContainsWorkTableScan(ru.Recursive)
	}
	if p, ok := n.(*optimizer.Project); ok {
		return planContainsWorkTableScan(p.Child)
	}
	if f, ok := n.(*optimizer.Filter); ok {
		return planContainsWorkTableScan(f.Child)
	}
	if s, ok := n.(*optimizer.Sort); ok {
		return planContainsWorkTableScan(s.Child)
	}
	if so, ok := n.(*optimizer.SetOp); ok {
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

func (o *materializedCTEScanOp) Schema() optimizer.Schema { return o.plan.Output() }

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
