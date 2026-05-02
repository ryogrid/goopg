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

// keyStep describes one chain-lookup: use col from sourceRow as key
// to look up hashTbl, then append matched row to output.
type keyStep struct {
	hashTblIndex int // which hashTbls[] to probe
	srcCol       int // column in current accumulated row for lookup key
	keyCol       int // column in hash table row that matches (for schema, unused)
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

	// Determine which tables contribute join keys.
	// Build edge map: for each MultiHashKey, record which tables
	// provide the key for linking.
	type edge struct{ srcTable, srcCol, dstTable int }
	edges := make([]edge, len(o.plan.Keys))
	for i, k := range o.plan.Keys {
		edges[i] = edge{srcTable: k.RightTable, srcCol: k.RightCol, dstTable: k.LeftTable}
	}

	// Open all build children and drain them into hash tables.
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
		// Find the key column for this table.
		keyCol := -1
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
		ht := make(map[string]Row, len(rows))
		for _, r := range rows {
			if keyCol >= 0 && keyCol < len(r) {
				ht[datumKey(r[keyCol])] = r
			}
		}
		o.hashTbls[i] = ht
	}

	// Build the chain-lookup steps.
	// Walk the keys in order to determine the lookup chain.
	o.keySteps = make([]keyStep, 0, len(o.plan.Keys))
	probe := o.plan.ProbeTable
	nextSrc := probe
	// Find the key where probe table is involved.
	for len(o.keySteps) < len(o.plan.Keys) {
		found := false
		for _, k := range o.plan.Keys {
			if k.LeftTable == nextSrc || k.RightTable == nextSrc {
				buildTbl := k.LeftTable
				srcCol := k.RightCol
				if nextSrc == k.LeftTable {
					buildTbl = k.RightTable
					srcCol = k.LeftCol
				}
				if buildTbl == probe || o.hashTbls[buildTbl] == nil {
					continue // already processed or is probe
				}
				o.keySteps = append(o.keySteps, keyStep{
					hashTblIndex: buildTbl,
					srcCol:       srcCol,
				})
				nextSrc = buildTbl
				found = true
				break
			}
		}
		if !found {
			break
		}
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
		// Copy probe row into position.
		probeOff := 0
		for i := 0; i < o.plan.ProbeTable; i++ {
			probeOff += len(o.nulls[i])
		}
		copy(out[probeOff:], probeRow)

		matched := true
		currentOff := probeOff
		currentLen := len(probeRow)

		// Chain lookup through hash tables.
		for _, step := range o.keySteps {
			if step.srcCol >= currentLen {
				matched = false
				break
			}
			keyVal := out[currentOff+step.srcCol]
			match, ok := o.hashTbls[step.hashTblIndex][datumKey(keyVal)]
			if !ok {
				matched = false
				break
			}
			// Copy matched row into output.
			destOff := 0
			for i := 0; i < step.hashTblIndex; i++ {
				destOff += len(o.nulls[i])
			}
			copy(out[destOff:], match)
			currentOff = 0 // now the full accumulated output
			currentLen = len(out)
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
