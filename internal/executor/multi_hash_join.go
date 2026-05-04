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
// M0043: replaced the previous expandChain() full-materialisation
// (which stored all rows in o.rows before yielding any, causing
// >19 GB heap + 91% GC overhead on Q9 with 1.8M lineitem rows)
// with a lazy cursor-based iterator.
type multiHashJoinOp struct {
	plan     *planner.MultiHashJoin
	children []Operator
	hashTbls []map[string][]Row // one per build child; multi-value
	probeOp  Operator
	keySteps []keyStep // ordered lookup chain
	filters  []planner.Expr
	nulls    []Row  // null-padded rows for schema
	tableOff []int  // precomputed offset of each table in output
	schema   planner.Schema
	ctx      *Context

	// Lazy iterator state (M0043). Per-step cursor arrays are
	// len(keySteps); the outer loop advances from the last step
	// backwards (like an odometer).
	lazyOut      Row     // current shared output buffer (reused across Next calls)
	lazyMatches  [][]Row // lazyMatches[i] = hash-table matches for step i given the current prefix
	lazyCursors  []int   // lazyCursors[i] = index into lazyMatches[i]
	lazyInit     bool    // true once the first probe row has been fetched
	lazyProbeEOF bool    // true once probeOp is exhausted
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
		rows, err := drainRows(child)
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
		ht := make(map[string][]Row, len(rows))
		for _, r := range rows {
			if keyCol >= 0 && keyCol < len(r) {
				ht[datumKey(r[keyCol])] = append(ht[datumKey(r[keyCol])], r)
			}
		}
		o.hashTbls[i] = ht
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

	// Allocate per-step cursor state.
	o.lazyMatches = make([][]Row, len(o.keySteps))
	o.lazyCursors = make([]int, len(o.keySteps))
	// Allocate the shared output buffer.
	o.lazyOut = make(Row, acc)
	return nil
}

// Next returns the next joined row by advancing the lazy cursor state.
// It streams probe rows one at a time and iterates all match
// combinations across chain steps without materialising the full
// Cartesian product into memory.
func (o *multiHashJoinOp) Next() (Row, error) {
	for {
		if o.lazyProbeEOF {
			return nil, EOF
		}

		if !o.lazyInit {
			// Fetch the first/next probe row.
			if err := o.advanceProbe(); err != nil {
				return nil, err
			}
			if o.lazyProbeEOF {
				return nil, EOF
			}
			// Init step cursors for the new probe row, starting
			// from step 0.
			if ok := o.initStepsFrom(0); !ok {
				// No match at some step — try next probe row.
				o.lazyInit = false
				continue
			}
			o.lazyInit = true
			// Cursors already point at first match per step;
			// apply all steps and check filters.
			if ok, err := o.applyAndFilter(); err != nil {
				return nil, err
			} else if ok {
				return o.copyOut(), nil
			}
			// Filter rejected — fall through to advance.
		}

		// Advance cursors (odometer from last step).
		advanced := false
		for s := len(o.keySteps) - 1; s >= 0; s-- {
			o.lazyCursors[s]++
			if o.lazyCursors[s] < len(o.lazyMatches[s]) {
				// Apply this step's new match into lazyOut.
				m := o.lazyMatches[s][o.lazyCursors[s]]
				dstOff := o.tableOff[o.keySteps[s].hashTblIndex]
				copy(o.lazyOut[dstOff:], m)
				// Re-init all deeper steps from s+1.
				if ok := o.initStepsFrom(s + 1); !ok {
					// No match at a deeper step; continue
					// incrementing s (backtrack).
					continue
				}
				advanced = true
				break
			}
			// This step is exhausted — reset and backtrack.
			o.lazyCursors[s] = 0
		}

		if !advanced {
			// All steps exhausted for this probe row — next probe.
			o.lazyInit = false
			continue
		}

		if ok, err := o.applyAndFilter(); err != nil {
			return nil, err
		} else if ok {
			return o.copyOut(), nil
		}
		// Filter rejected — loop to advance again.
	}
}

// advanceProbe fetches the next probe row and copies it into lazyOut.
func (o *multiHashJoinOp) advanceProbe() error {
	probeRow, err := o.probeOp.Next()
	if err == EOF {
		o.lazyProbeEOF = true
		return nil
	}
	if err != nil {
		return err
	}
	// Reset lazyOut to nulls, then overlay the probe row.
	nTables := len(o.plan.Tables)
	for i := 0; i < nTables; i++ {
		copy(o.lazyOut[o.tableOff[i]:], o.nulls[i])
	}
	copy(o.lazyOut[o.tableOff[o.plan.ProbeTable]:], probeRow)
	return nil
}

// initStepsFrom initialises match slices and cursors for steps
// [startStep .. len(keySteps)-1] based on the current lazyOut
// content. Returns false if any step has no matches (meaning this
// combination of probe+prefix yields no output rows at all).
func (o *multiHashJoinOp) initStepsFrom(startStep int) bool {
	for s := startStep; s < len(o.keySteps); s++ {
		step := o.keySteps[s]
		srcOff := o.tableOff[step.srcTable]
		srcLen := len(o.nulls[step.srcTable])
		if step.srcCol >= srcLen {
			return false
		}
		keyVal := o.lazyOut[srcOff+step.srcCol]
		matches := o.hashTbls[step.hashTblIndex][datumKey(keyVal)]
		if len(matches) == 0 {
			return false // INNER semantics: no match → skip
		}
		o.lazyMatches[s] = matches
		o.lazyCursors[s] = 0
		// Apply first match of this step into lazyOut.
		dstOff := o.tableOff[step.hashTblIndex]
		copy(o.lazyOut[dstOff:], matches[0])
	}
	return true
}

// applyAndFilter evaluates the residual filters on the current
// lazyOut row. Returns true when the row should be emitted.
func (o *multiHashJoinOp) applyAndFilter() (bool, error) {
	for _, f := range o.filters {
		v, err := evalExpr(f, o.lazyOut, o.ctx)
		if err != nil {
			return false, err
		}
		if v.IsNull() || !(v.Kind == KindBool && v.Bool) {
			return false, nil
		}
	}
	return true, nil
}

// copyOut returns a fresh copy of lazyOut so callers can hold a
// reference across subsequent Next() calls.
func (o *multiHashJoinOp) copyOut() Row {
	row := make(Row, len(o.lazyOut))
	copy(row, o.lazyOut)
	return row
}

func (o *multiHashJoinOp) Close() error {
	o.hashTbls = nil
	o.nulls = nil
	o.keySteps = nil
	o.lazyOut = nil
	o.lazyMatches = nil
	o.lazyCursors = nil
	o.ctx = nil
	for _, c := range o.children {
		_ = c.Close()
	}
	return nil
}

func (o *multiHashJoinOp) Schema() planner.Schema { return o.schema }
