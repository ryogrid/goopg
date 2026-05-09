package executor

import (
	"github.com/goopg/goopg/internal/planner"
)

// multiHashJoinOp replaces a chain of N binary hash joins.
// Build phase: drain N-1 small "build" tables into hash tables.
// Probe phase: stream the one "probe" table; for each probe row,
// iterate all match combinations across the chain steps lazily via
// Next(), one output row per call.
//
// M0043-0001: replaced expandChain() full-materialisation (which
// stored all rows in o.rows before yielding any, causing >19 GB heap
// + 91% GC overhead on Q9 with 1.8M lineitem rows) with a lazy
// cursor-based iterator.
//
// M0043-0002: residual filters are partitioned by the deepest chain
// step their referenced columns require, and evaluated immediately
// after that step's match is bound. A failing per-step filter aborts
// the current cursor prefix without expanding deeper steps. This
// converts O(probe × all-step Cartesian) expansion into early-exit
// search.
type multiHashJoinOp struct {
	plan     *planner.MultiHashJoin
	children []Operator
	hashTbls []map[string][]Row // one per build child; multi-value (string-key path)
	// M0043-0003: int64 fast-path hash tables. When hashTblIsInt[i] is
	// true, intHashTbls[i] is used instead of hashTbls[i]. Eliminates
	// string allocation on the probe hot path for int/numeric-scale-0
	// join keys (~22M calls for Q9 at SF=1).
	intHashTbls []map[int64][]Row // one per build child; used when keys are int64-representable
	hashTblIsInt []bool           // hashTblIsInt[i] == true means intHashTbls[i] is active
	probeOp  Operator
	keySteps []keyStep // ordered lookup chain
	filters  []planner.Expr
	nulls    []Row // null-padded rows for schema
	tableOff []int // precomputed offset of each table in output
	schema   planner.Schema
	ctx      *Context

	// M0043-0002 filter partitioning. Populated in Open() once
	// per query; consulted on the hot path.
	probeFilters []planner.Expr   // eval after advanceProbe
	stepFilters  [][]planner.Expr // stepFilters[s] = filters first eval-able after step s
	leafFilters  []planner.Expr   // catch-all (subqueries / OuterColumnRef / out-of-scope refs)

	// Lazy iterator state. Per-step cursor arrays are
	// len(keySteps); the outer loop advances from the last step
	// backwards (like an odometer).
	lazyMatches  [][]Row // lazyMatches[i] = hash-table matches for step i given the current prefix
	lazyCursors  []int   // lazyCursors[i] = index into lazyMatches[i]
	lazyInit     bool    // true once a probe row has yielded a valid leaf
	lazyProbeEOF bool    // true once probeOp is exhausted

	// M0071-0014 Stage D-2: VirtualSlot composition replaces the
	// per-Next lazyOut concat. tableSlots[i] is a persistent
	// MaterializedSlot per joined table whose .row field is
	// overwritten in place — probe slot per advanceProbe, build
	// slots per match step. virtualOut composes them in OID-sorted
	// output order via tableOff[]. Predicate / filter evaluation
	// reads virtualOut.Get(col) — zero allocation per match.
	//
	// Q5's pprof at SF=1 previously showed copyOut accounted for
	// 99.23% of allocations (2.02 TB total in 600s); virtualOut
	// eliminates that copy entirely.
	tableSlots []*MaterializedSlot
	virtualOut *VirtualSlot
}

// keyStep describes one chain-lookup: take the value at the
// srcTable's srcCol from the accumulated output row and use it as
// the lookup key into hashTbls[hashTblIndex] (whose hash is keyed on
// buildKeyCol of the build table). Tracking srcTable explicitly
// (rather than the previously-matched table) lets the chain branch
// when a probe table connects to two or more tables — TPC-H Q5's
// lineitem-supplier-…-region path and lineitem-orders-customer
// path both need to fire from the same probe.
type keyStep struct {
	hashTblIndex int // which hashTbls[] to probe
	srcTable     int // table index whose accumulated columns hold the lookup key
	srcCol       int // column within srcTable used for the lookup
	buildKeyCol  int // column in the build table that the hash is keyed on
}

func newMultiHashJoinOp(plan *planner.MultiHashJoin, children []Operator) *multiHashJoinOp {
	return &multiHashJoinOp{
		plan:     plan,
		children: children,
		schema:   plan.Output(),
	}
}

func (o *multiHashJoinOp) Open(ctx *Context) error {
	o.ctx = ctx
	o.lazyInit = false
	o.lazyProbeEOF = false
	nTables := len(o.plan.Tables)
	o.hashTbls = make([]map[string][]Row, nTables)
	o.intHashTbls = make([]map[int64][]Row, nTables)
	o.hashTblIsInt = make([]bool, nTables)
	if o.nulls == nil || len(o.nulls) != nTables || len(o.nulls[0]) == 0 {
		o.nulls = make([]Row, nTables)
		for i := range o.nulls {
			w := len(o.plan.Tables[i].Output())
			if w == 0 {
				w = len(o.children[i].Schema())
			}
			o.nulls[i] = nullRow(w)
		}
	}

	// Build the chain-lookup steps FIRST so each step records the
	// exact (table, column) the hash table must be keyed on. Doing
	// this before the build phase means the hash table is keyed on
	// the column the chain actually probes.
	//
	// At each step, any already-visited table may serve as the
	// "source" of a new key — not just the most recently added
	// one. This branches the chain so that, e.g., TPC-H Q5's
	// lineitem-as-probe can follow both lineitem→supplier→nation→
	// region AND lineitem→orders→customer in one MHJ.
	//
	// `visited[i]` tracks tables already in the chain to prevent
	// re-visiting and to terminate when no progress is possible.
	o.keySteps = make([]keyStep, 0, len(o.plan.Keys))
	probe := o.plan.ProbeTable
	visited := make([]bool, nTables)
	visited[probe] = true
	for len(o.keySteps) < len(o.plan.Keys) {
		found := false
		for src := 0; src < nTables && !found; src++ {
			if !visited[src] {
				continue
			}
			for _, k := range o.plan.Keys {
				if k.LeftTable != src && k.RightTable != src {
					continue
				}
				buildTbl := k.LeftTable
				srcCol := k.RightCol
				buildKeyCol := k.LeftCol
				if src == k.LeftTable {
					buildTbl = k.RightTable
					srcCol = k.LeftCol
					buildKeyCol = k.RightCol
				}
				if buildTbl == probe || visited[buildTbl] {
					continue
				}
				o.keySteps = append(o.keySteps, keyStep{
					hashTblIndex: buildTbl,
					srcTable:     src,
					srcCol:       srcCol,
					buildKeyCol:  buildKeyCol,
				})
				visited[buildTbl] = true
				found = true
				break
			}
		}
		if !found {
			break
		}
	}

	// Open all build children and drain them into hash tables. Each
	// table's hash key column is taken from the matching keyStep so
	// the source-side lookup actually finds rows.
	keyColByTable := make([]int, nTables)
	for i := range keyColByTable {
		keyColByTable[i] = -1
	}
	for _, st := range o.keySteps {
		keyColByTable[st.hashTblIndex] = st.buildKeyCol
	}
	for i, child := range o.children {
		if i == o.plan.ProbeTable {
			continue // probe table is streamed
		}
		if err := child.Open(ctx); err != nil {
			return err
		}
		rows, err := drainRowsCtx(child, ctx)
		_ = child.Close()
		if err != nil {
			return err
		}
		keyCol := keyColByTable[i]
		if keyCol < 0 {
			// Fallback: pick the first key mentioning this table
			// (handles tables not reached by the chain — those
			// rows stay in a nullable hash but contribute no
			// matches).
			for _, k := range o.plan.Keys {
				if k.LeftTable == i {
					keyCol = k.LeftCol
					break
				}
				if k.RightTable == i {
					keyCol = k.RightCol
					break
				}
			}
		}
		// M0043-0003: probe whether ALL keys are int64-representable.
		// One-pass scan; if any row fails, fall back to the string map.
		allInt64 := len(rows) > 0
		if allInt64 && keyCol >= 0 {
			for _, r := range rows {
				if keyCol < len(r) {
					if _, ok := datumToInt64Key(r[keyCol]); !ok {
						allInt64 = false
						break
					}
				}
			}
		}
		if allInt64 && keyCol >= 0 {
			intHt := make(map[int64][]Row, len(rows))
			for _, r := range rows {
				if keyCol < len(r) {
					k, _ := datumToInt64Key(r[keyCol])
					intHt[k] = append(intHt[k], r)
				}
			}
			o.intHashTbls[i] = intHt
			o.hashTblIsInt[i] = true
		} else {
			ht := make(map[string][]Row, len(rows))
			for _, r := range rows {
				if keyCol >= 0 && keyCol < len(r) {
					k := datumKey(r[keyCol])
					ht[k] = append(ht[k], r)
				}
			}
			o.hashTbls[i] = ht
		}
	}

	// Open the probe child for streaming. The actual joining is
	// done lazily in Next().
	o.probeOp = o.children[probe]
	if err := o.probeOp.Open(ctx); err != nil {
		return err
	}
	o.filters = o.plan.Filters

	o.tableOff = make([]int, nTables)
	acc := 0
	for i := 0; i < nTables; i++ {
		o.tableOff[i] = acc
		acc += len(o.nulls[i])
	}

	// M0043-0002: classify each filter by the deepest chain step
	// its referenced columns require, so step-time eval can prune
	// prefixes before they expand into deeper steps.
	o.partitionFilters()

	// Allocate per-step cursor state.
	o.lazyMatches = make([][]Row, len(o.keySteps))
	o.lazyCursors = make([]int, len(o.keySteps))

	// M0071-0014 Stage D-2: persistent slot composition replacing
	// the old o.lazyOut Row. Each table gets its own
	// MaterializedSlot whose .row is overwritten in place — probe
	// per advanceProbe, builds per match step. virtualOut sources
	// these in OID-sorted output order via tableOff[] / per-table
	// width.
	o.tableSlots = make([]*MaterializedSlot, nTables)
	for i := 0; i < nTables; i++ {
		o.tableSlots[i] = SlotFromRow(nil, o.nulls[i])
	}
	sources := make([]TupleSlot, nTables)
	for i := 0; i < nTables; i++ {
		sources[i] = o.tableSlots[i]
	}
	cols := make([]virtualCol, acc)
	for tIdx := 0; tIdx < nTables; tIdx++ {
		width := len(o.nulls[tIdx])
		off := o.tableOff[tIdx]
		for col := 0; col < width; col++ {
			cols[off+col] = virtualCol{
				sourceIdx: int16(tIdx),
				sourceCol: int16(col),
			}
		}
	}
	o.virtualOut = NewVirtualSlot(o.schema, sources, cols)
	return nil
}

// partitionFilters splits o.filters into probeFilters,
// stepFilters[s], and leafFilters according to the deepest chain
// step each filter's referenced columns require. Called once per
// Open(); off the hot path.
func (o *multiHashJoinOp) partitionFilters() {
	nTables := len(o.plan.Tables)
	nSteps := len(o.keySteps)
	o.probeFilters = nil
	o.stepFilters = make([][]planner.Expr, nSteps)
	o.leafFilters = nil

	if len(o.filters) == 0 {
		return
	}

	// bindStepOfTable[t] = -1 if t is the probe (bound by
	// advanceProbe), s if t is bound by chain step s, -2 if t
	// is unreachable from the chain (defensive — should not
	// occur in practice).
	bindStepOfTable := make([]int, nTables)
	for i := range bindStepOfTable {
		bindStepOfTable[i] = -2
	}
	bindStepOfTable[o.plan.ProbeTable] = -1
	for s, k := range o.keySteps {
		bindStepOfTable[k.hashTblIndex] = s
	}

	// tableEnd[t] = exclusive upper bound of table t's column
	// range in lazyOut. tableEnd is len(nulls[t]) past tableOff.
	tableEnd := make([]int, nTables)
	for i := 0; i < nTables; i++ {
		tableEnd[i] = o.tableOff[i] + len(o.nulls[i])
	}

	tableOf := func(idx int) int {
		// Linear scan is fine: nTables ≤ ~10 in practice.
		for t := 0; t < nTables; t++ {
			if idx >= o.tableOff[t] && idx < tableEnd[t] {
				return t
			}
		}
		return -1 // out of range — should never happen
	}

	for _, f := range o.filters {
		var (
			outOfScope bool
			anyTable   bool
			maxStep    = -1 // pessimistic (probe-only); raised by per-table joins
		)
		// Walk the expression tree once, accumulating the
		// deepest binding step encountered along the way.
		walkColumnRefs(f, func(idx int) {
			t := tableOf(idx)
			if t < 0 || bindStepOfTable[t] == -2 {
				outOfScope = true
				return
			}
			anyTable = true
			if bs := bindStepOfTable[t]; bs > maxStep {
				maxStep = bs
			}
		}, func() {
			outOfScope = true
		})

		if outOfScope {
			o.leafFilters = append(o.leafFilters, f)
			continue
		}
		if !anyTable || maxStep == -1 {
			// Constant or probe-only; evaluate at probe time.
			o.probeFilters = append(o.probeFilters, f)
			continue
		}
		o.stepFilters[maxStep] = append(o.stepFilters[maxStep], f)
	}
}

// walkColumnRefs visits every ColumnRef.Index in e via onIdx.
// Triggers onOuter for any reference that disqualifies pushdown
// (OuterColumnRef, SubqueryExpr, ExistsExpr, InExpr). Mirrors the
// planner's pushdown.go::walkColumnRefs walker so the executor does
// not depend on internal/planner's package layout.
func walkColumnRefs(e planner.Expr, onIdx func(int), onOuter func()) {
	switch x := e.(type) {
	case nil:
	case *planner.ColumnRef:
		onIdx(x.Index)
	case *planner.OuterColumnRef:
		onOuter()
	case *planner.SubqueryExpr, *planner.ExistsExpr, *planner.InExpr:
		onOuter()
	case *planner.BinaryOp:
		walkColumnRefs(x.Left, onIdx, onOuter)
		walkColumnRefs(x.Right, onIdx, onOuter)
	case *planner.UnaryOp:
		walkColumnRefs(x.Operand, onIdx, onOuter)
	case *planner.FuncCall:
		for _, a := range x.Args {
			walkColumnRefs(a, onIdx, onOuter)
		}
	case *planner.CaseExpr:
		walkColumnRefs(x.Operand, onIdx, onOuter)
		for _, w := range x.Whens {
			walkColumnRefs(w.When, onIdx, onOuter)
			walkColumnRefs(w.Then, onIdx, onOuter)
		}
		walkColumnRefs(x.Else, onIdx, onOuter)
	case *planner.ExtractExpr:
		walkColumnRefs(x.Source, onIdx, onOuter)
	}
}

// Next returns the next joined row. The state machine alternates
// between "advance to next probe + initialise chain" and "advance
// the cursor odometer". Filters are evaluated at the earliest step
// where their referenced columns are bound.
//
// Cancellation: ctx.Err() is checked once per Next() call so a
// CancelRequest interrupts the probe phase promptly.  Q5 ran 60+
// minutes without responding to cancel before this check existed.
// (M0058-0005.)
func (o *multiHashJoinOp) Next() (TupleSlot, error) {
	if o.ctx != nil && o.ctx.Ctx != nil {
		if err := o.ctx.Ctx.Err(); err != nil {
			return nil, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
		}
	}
	for {
		if o.lazyProbeEOF {
			return nil, EOF
		}

		if !o.lazyInit {
			// Fetch the next probe row.
			if err := o.advanceProbe(); err != nil {
				return nil, err
			}
			if o.lazyProbeEOF {
				return nil, EOF
			}
			// Probe-only filters (and constants) — evaluated
			// once per probe row; failure skips the row.
			if ok, err := o.evalFilters(o.probeFilters); err != nil {
				return nil, err
			} else if !ok {
				continue
			}
			// Find the first valid combination across all
			// chain steps. Failure means this probe row has
			// no valid join match; advance to the next probe.
			ok, err := o.initStepHelper(0)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			o.lazyInit = true
			return o.virtualOut, nil
		}

		// Already on a valid combination; advance to the next.
		ok, err := o.advanceFrom(len(o.keySteps) - 1)
		if err != nil {
			return nil, err
		}
		if !ok {
			// All combinations exhausted for this probe row.
			o.lazyInit = false
			continue
		}
		return o.virtualOut, nil
	}
}

// advanceProbe fetches the next probe row and binds it into the
// probe table's slot. Other tables' slots are reset to their
// per-table null padding so a subsequent step's match either
// overwrites that slot's row or leaves it as null padding (the
// initStepHelper / advanceFrom paths always overwrite before
// emit, so the reset is defensive).
func (o *multiHashJoinOp) advanceProbe() error {
	probeSlot, err := o.probeOp.Next()
	if err == EOF {
		o.lazyProbeEOF = true
		return nil
	}
	if err != nil {
		return err
	}
	probeRow := slotRow(probeSlot)
	nTables := len(o.plan.Tables)
	for i := 0; i < nTables; i++ {
		o.tableSlots[i].row = o.nulls[i]
	}
	o.tableSlots[o.plan.ProbeTable].row = probeRow
	return nil
}

// initStepHelper recursively descends from step `s` to the leaf,
// binding the first match at each step that yields a leaf where
// every step-filter (at every level) and every leaf-filter passes.
// Returns (true, nil) on success with lazyOut populated; (false,
// nil) when no valid configuration exists from this state.
func (o *multiHashJoinOp) initStepHelper(s int) (bool, error) {
	if s >= len(o.keySteps) {
		// Base case: leaf. Evaluate any filters that did not
		// classify into per-step filters (subqueries, etc).
		return o.evalFilters(o.leafFilters)
	}
	step := o.keySteps[s]
	srcLen := len(o.nulls[step.srcTable])
	if step.srcCol >= srcLen {
		return false, nil
	}
	// Read the lookup key directly from the source table's slot
	// (the chain-level prefix of bound rows lives in tableSlots).
	keyVal := o.tableSlots[step.srcTable].Get(step.srcCol)
	// M0043-0003: zero-allocation probe for int64-keyed hash tables.
	var matches []Row
	if o.hashTblIsInt[step.hashTblIndex] {
		if k, ok := datumToInt64Key(keyVal); ok {
			matches = o.intHashTbls[step.hashTblIndex][k]
		}
		// else: key not int64-representable (e.g. NULL) → no matches
	} else {
		matches = o.hashTbls[step.hashTblIndex][datumKey(keyVal)]
	}
	if len(matches) == 0 {
		return false, nil
	}
	o.lazyMatches[s] = matches
	stepFs := o.stepFilters[s]
	for c := 0; c < len(matches); c++ {
		// M0062-0001: ctx check inside the per-match loop.
		// Next()'s outer check fires once per emitted row; for
		// Q5-shape queries where many matches survive the chain
		// but downstream filters reject most, the inner loop can
		// run for seconds without yielding to the caller. Cadence
		// 4096 mirrors runNestedLoop's inner-loop guard.
		if c&0xFFF == 0 && o.ctx != nil && o.ctx.Ctx != nil {
			if err := o.ctx.Ctx.Err(); err != nil {
				return false, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		o.tableSlots[step.hashTblIndex].row = matches[c]
		o.lazyCursors[s] = c
		// Evaluate filters whose deepest table is this step.
		// If any reject, skip this match without descending.
		if ok, err := o.evalFilters(stepFs); err != nil {
			return false, err
		} else if !ok {
			continue
		}
		// Try to bind deeper steps.
		ok, err := o.initStepHelper(s + 1)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
		// Deeper steps had no valid combination for this
		// match; try the next match at this step.
	}
	return false, nil
}

// advanceFrom moves the chain cursor from a valid leaf to the next
// valid leaf, starting from step `s`. Returns (true, nil) when a
// next configuration exists; (false, nil) when this probe row is
// exhausted.
func (o *multiHashJoinOp) advanceFrom(s int) (bool, error) {
	if s < 0 {
		return false, nil
	}
	step := o.keySteps[s]
	matches := o.lazyMatches[s]
	stepFs := o.stepFilters[s]
	for {
		// M0062-0001: ctx check inside the advance loop (cadence
		// every 4096 cursor advances). Same rationale as
		// initStepHelper: a single Next() may iterate many matches
		// here when downstream filters reject most.
		if o.lazyCursors[s]&0xFFF == 0 && o.ctx != nil && o.ctx.Ctx != nil {
			if err := o.ctx.Ctx.Err(); err != nil {
				return false, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		o.lazyCursors[s]++
		if o.lazyCursors[s] >= len(matches) {
			// Exhausted at this level — recursive backtrack.
			// Parent's advance will re-init this level (and
			// everything deeper) via initStepHelper, so we
			// don't need to manually reset cursor[s] here.
			return o.advanceFrom(s - 1)
		}
		o.tableSlots[step.hashTblIndex].row = matches[o.lazyCursors[s]]
		// Re-evaluate this step's filters with the new match.
		if ok, err := o.evalFilters(stepFs); err != nil {
			return false, err
		} else if !ok {
			continue
		}
		// Find the first valid configuration for deeper steps.
		ok, err := o.initStepHelper(s + 1)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
		// Deeper init failed; try the next match at this level.
	}
}

// evalFilters returns true iff every filter in `fs` evaluates to
// boolean-true on the current virtualOut composition. NULL or
// non-bool results reject the row, matching the M0043-0001
// leaf-eval semantics.
func (o *multiHashJoinOp) evalFilters(fs []planner.Expr) (bool, error) {
	for _, f := range fs {
		v, err := evalExprSlot(f, o.virtualOut, o.ctx)
		if err != nil {
			return false, err
		}
		if v.IsNull() || !(v.Kind == KindBool && v.BoolValue()) {
			return false, nil
		}
	}
	return true, nil
}

func (o *multiHashJoinOp) Close() error {
	o.hashTbls = nil
	o.intHashTbls = nil
	o.hashTblIsInt = nil
	o.nulls = nil
	o.keySteps = nil
	o.tableSlots = nil
	o.virtualOut = nil
	o.lazyMatches = nil
	o.lazyCursors = nil
	o.probeFilters = nil
	o.stepFilters = nil
	o.leafFilters = nil
	o.ctx = nil
	for _, c := range o.children {
		_ = c.Close()
	}
	return nil
}

func (o *multiHashJoinOp) Schema() planner.Schema { return o.schema }
