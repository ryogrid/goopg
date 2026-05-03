package executor

import (
	"github.com/goopg/goopg/internal/planner"
)

// multiHashJoinOp replaces a chain of N binary hash joins.
// Build phase: drain N-1 small "build" tables into hash tables.
// Probe phase: stream the one "probe" table, chain-lookup through
// hash tables, and emit joined rows lazily via Next().
type multiHashJoinOp struct {
	plan     *planner.MultiHashJoin
	children []Operator
	hashTbls []map[string]Row // one per build child
	probeOp  Operator
	keySteps []keyStep // ordered lookup chain
	filters  []planner.Expr
	nulls    []Row // null-padded rows for schema
	schema   planner.Schema
	ctx      *Context
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
	nTables := len(o.plan.Tables)
	o.hashTbls = make([]map[string]Row, nTables)
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
		ht := make(map[string]Row, len(rows))
		for _, r := range rows {
			if keyCol >= 0 && keyCol < len(r) {
				ht[datumKey(r[keyCol])] = r
			}
		}
		o.hashTbls[i] = ht
	}

	// Open probe child for streaming.
	o.probeOp = o.children[probe]
	if err := o.probeOp.Open(ctx); err != nil {
		return err
	}

	o.filters = o.plan.Filters
	return nil
}

func (o *multiHashJoinOp) Next() (Row, error) {
	for {
		probeRow, err := o.probeOp.Next()
		if err == EOF {
			return nil, EOF
		}
		if err != nil {
			return nil, err
		}

		// Accumulate: start with probe table's columns plus null padding
		// for all other tables.
		out := make(Row, 0, len(o.schema))
		// Fill with nulls, then overwrite the probe table's position.
		nTables := len(o.plan.Tables)
		for i := 0; i < nTables; i++ {
			out = append(out, o.nulls[i]...)
		}
		// Precompute each table's offset in the accumulated output.
		tableOff := make([]int, nTables)
		acc := 0
		for i := 0; i < nTables; i++ {
			tableOff[i] = acc
			acc += len(o.nulls[i])
		}
		// Copy probe row into position.
		copy(out[tableOff[o.plan.ProbeTable]:], probeRow)

		matched := true

		// Chain lookup through hash tables. Each step takes the
		// lookup key from out[srcTable_off + srcCol] — independent
		// of step order — so the chain can branch from the probe
		// to multiple subtrees.
		for _, step := range o.keySteps {
			srcOff := tableOff[step.srcTable]
			srcLen := len(o.nulls[step.srcTable])
			if step.srcCol >= srcLen {
				matched = false
				break
			}
			keyVal := out[srcOff+step.srcCol]
			match, ok := o.hashTbls[step.hashTblIndex][datumKey(keyVal)]
			if !ok {
				matched = false
				break
			}
			copy(out[tableOff[step.hashTblIndex]:], match)
		}

		if !matched {
			continue // INNER semantics: skip unmatched probe rows
		}

		// Apply residual filters.
		ok := true
		for _, f := range o.filters {
			v, err := evalExpr(f, out, o.ctx)
			if err != nil {
				return nil, err
			}
			if v.IsNull() || !(v.Kind == KindBool && v.Bool) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}

		return out, nil
	}
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
