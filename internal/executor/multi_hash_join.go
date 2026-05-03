package executor

import (
	"github.com/goopg/goopg/internal/planner"
)

// multiHashJoinOp replaces a chain of N binary hash joins.
// Build phase: drain N-1 small "build" tables into hash tables.
// Probe phase: stream the one "probe" table, chain-lookup through
// hash tables, and emit joined rows lazily via Next().
type multiHashJoinOp struct {
	plan       *planner.MultiHashJoin
	children   []Operator
	hashTbls   []map[string][]Row // one per build child; multi-value
	probeOp    Operator
	keySteps   []keyStep // ordered lookup chain
	filters    []planner.Expr
	nulls      []Row // null-padded rows for schema
	tableOff   []int // precomputed offset of each table in output
	schema     planner.Schema
	ctx        *Context

	// Materialised join output state. The chain may produce
	// multiple rows per probe row when any build table has multi-
	// row matches per key (e.g. TPC-H Q9 partsupp where two
	// partsupps share each ps_suppkey). Open() drives the chain
	// once and stores all output rows; Next() yields them one at
	// a time. (Lazy expansion across multiple multi-match steps
	// would require nested iterators — the synthetic dataset is
	// small and this materialisation has negligible cost.)
	rows []Row
	idx  int
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
	o.rows = nil
	o.idx = 0
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

	// Open probe child and drive the entire join — produce the
	// Cartesian expansion across multi-match chain steps up front.
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
	for {
		probeRow, err := o.probeOp.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return err
		}
		out := make(Row, 0, len(o.schema))
		for i := 0; i < nTables; i++ {
			out = append(out, o.nulls[i]...)
		}
		copy(out[o.tableOff[o.plan.ProbeTable]:], probeRow)
		if err := o.expandChain(out, 0); err != nil {
			return err
		}
	}
	return nil
}

// expandChain drives the chain of hash lookups depth-first, emitting
// one output row per Cartesian combination of multi-row matches at
// each step. Materialising all output rows up front avoids holding
// per-step iterator state across Next() calls; the synthetic dataset
// is small and TPC-H scales fit comfortably in memory.
func (o *multiHashJoinOp) expandChain(out Row, stepIdx int) error {
	if stepIdx >= len(o.keySteps) {
		// Leaf: apply residual filters, emit if all pass.
		for _, f := range o.filters {
			v, err := evalExpr(f, out, o.ctx)
			if err != nil {
				return err
			}
			if v.IsNull() || !(v.Kind == KindBool && v.Bool) {
				return nil
			}
		}
		// Copy out (caller may overwrite slots in subsequent
		// recursion levels for sibling matches).
		row := make(Row, len(out))
		copy(row, out)
		o.rows = append(o.rows, row)
		return nil
	}
	step := o.keySteps[stepIdx]
	srcOff := o.tableOff[step.srcTable]
	srcLen := len(o.nulls[step.srcTable])
	if step.srcCol >= srcLen {
		return nil // no source value
	}
	keyVal := out[srcOff+step.srcCol]
	matches := o.hashTbls[step.hashTblIndex][datumKey(keyVal)]
	if len(matches) == 0 {
		return nil // INNER semantics: no matches → drop branch
	}
	dstOff := o.tableOff[step.hashTblIndex]
	for _, m := range matches {
		copy(out[dstOff:], m)
		if err := o.expandChain(out, stepIdx+1); err != nil {
			return err
		}
	}
	// Restore null padding so sibling branches don't see leftover
	// values from this match.
	copy(out[dstOff:], o.nulls[step.hashTblIndex])
	return nil
}

func (o *multiHashJoinOp) Next() (Row, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.idx]
	o.idx++
	return row, nil
}

func (o *multiHashJoinOp) Close() error {
	o.hashTbls = nil
	o.nulls = nil
	o.keySteps = nil
	o.ctx = nil
	for _, c := range o.children {
		_ = c.Close()
	}
	return nil
}

func (o *multiHashJoinOp) Schema() planner.Schema { return o.schema }
