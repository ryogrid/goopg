package executor

import (
	"sort"

	"github.com/goopg/goopg/internal/optimizer"
)

// operators_incremental_sort.go — M0141-S7-exec-a: the executor operator for
// optimizer.IncrementalSort (PG oracle: nodeIncrementalSort.c). Groups the
// child's rows by the leading PresortedCount keys via sortPrefixEqual (E-15's
// contract, sort_presorted.go), fully sorts each group by ALL keys (this is
// equivalent to sorting by the trailing keys alone, since every row's prefix
// is already equal within a group — see sortPrefixEqual), and streams groups
// in arrival order.
//
// STANDALONE per M0141-S7-exec-a's scope: constructed directly in tests,
// zero createPlanNode/Plan() callers (that wiring is M0141-S7-exec-b).
// All-in-memory []Row, no spill-to-disk / packed-tuple retention / ctid
// passthrough (sortOp's harder features) — deferred to M0141-S7-exec-d; none
// of the 14 TPC-DS witnesses this task exists for need them (see fix_plan's
// M0141-S7-exec-d entry).
type incrementalSortOp struct {
	child          Operator
	plan           *optimizer.IncrementalSort
	keys           []optimizer.SortKey
	presortedCount int
	ctx            *Context

	groups [][]Row
	gi, ri int
}

func newIncrementalSortOp(child Operator, plan *optimizer.IncrementalSort, keys []optimizer.SortKey, presortedCount int) *incrementalSortOp {
	return &incrementalSortOp{child: child, plan: plan, keys: keys, presortedCount: presortedCount}
}

func (o *incrementalSortOp) Schema() optimizer.Schema { return o.child.Schema() }

// Open drains the child fully, splitting it into presorted-prefix groups as
// they arrive (a group closes the moment a row's prefix no longer matches
// the previous row's — sortPrefixEqual's "groups are contiguous" contract),
// sorting each group as it closes. Peak memory is bounded by the largest
// group, not the input (E-15's EXECUTOR GUARANTEE).
func (o *incrementalSortOp) Open(ctx *Context) error {
	o.ctx = ctx
	o.groups = nil
	o.gi, o.ri = 0, 0
	if err := o.child.Open(ctx); err != nil {
		return err
	}

	var curRows []Row
	var curKeys [][]Datum
	var prevKV []Datum
	haveGroup := false

	flush := func() error {
		if len(curRows) == 0 {
			return nil
		}
		if err := o.sortGroup(curRows, curKeys); err != nil {
			return err
		}
		o.groups = append(o.groups, curRows)
		curRows, curKeys = nil, nil
		return nil
	}

	pulled := 0
	for {
		// M0062-followup precedent (sortOp.Open): give a cancel opportunity
		// every 4096 rows rather than only draining fully unattended.
		if pulled&0xFFF == 0 && ctx != nil && ctx.Ctx != nil {
			if err := ctx.Ctx.Err(); err != nil {
				return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		slot, err := o.child.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return err
		}
		row := slot.Materialize().Row()
		kv, kerr := o.sortKeyVals(row)
		if kerr != nil {
			return kerr
		}
		if haveGroup && !sortPrefixEqual(prevKV, kv, o.keys, o.presortedCount) {
			if err := flush(); err != nil {
				return err
			}
		}
		curRows = append(curRows, row)
		curKeys = append(curKeys, kv)
		prevKV = kv
		haveGroup = true
		pulled++
	}
	return flush()
}

// sortKeyVals evaluates every ORDER BY key for one row, once — same shape as
// sortOp.sortKeyVals (operators.go), duplicated rather than shared because
// exec-a is deliberately standalone (no sortOp dependency).
func (o *incrementalSortOp) sortKeyVals(row Row) ([]Datum, error) {
	if len(o.keys) == 0 {
		return nil, nil
	}
	kv := make([]Datum, len(o.keys))
	for i, k := range o.keys {
		v, err := evalSortKeyValue(k.Expr, row, o.ctx)
		if err != nil {
			return nil, err
		}
		kv[i] = v
	}
	return kv, nil
}

// lessKeyVals mirrors sortOp.lessKeyVals (operators.go): NULL placement by
// NullsFirst, then compareDatum, then Desc — over precomputed key values.
// Unlike sortOp it returns its error instead of latching it on the
// receiver, since exec-a's caller (sortGroup) needs it per-group, not
// per-Open.
func (o *incrementalSortOp) lessKeyVals(a, b []Datum) (bool, error) {
	for i, k := range o.keys {
		av, bv := a[i], b[i]
		if av.IsNull() && !bv.IsNull() {
			return k.NullsFirst, nil
		}
		if !av.IsNull() && bv.IsNull() {
			return !k.NullsFirst, nil
		}
		if av.IsNull() && bv.IsNull() {
			continue
		}
		pos := 0
		if k.Expr != nil {
			pos = k.Expr.Pos()
		}
		cmp, err := compareDatum(av, bv, pos)
		if err != nil {
			return false, err
		}
		if cmp == 0 {
			continue
		}
		if k.Desc {
			return cmp > 0, nil
		}
		return cmp < 0, nil
	}
	return false, nil
}

// sortGroup fully sorts one presorted-prefix group in place, permutation-
// based (rows and keyvals move together) exactly as sortOp.sortChunk does.
func (o *incrementalSortOp) sortGroup(rows []Row, keyvals [][]Datum) error {
	perm := make([]int, len(rows))
	for i := range perm {
		perm[i] = i
	}
	var sortErr error
	sort.SliceStable(perm, func(i, j int) bool {
		if sortErr != nil {
			return false
		}
		less, err := o.lessKeyVals(keyvals[perm[i]], keyvals[perm[j]])
		if err != nil {
			sortErr = err
			return false
		}
		return less
	})
	if sortErr != nil {
		return sortErr
	}
	newRows := make([]Row, len(perm))
	for i, p := range perm {
		newRows[i] = rows[p]
	}
	copy(rows, newRows)
	return nil
}

func (o *incrementalSortOp) Next() (TupleSlot, error) {
	for o.gi < len(o.groups) {
		g := o.groups[o.gi]
		if o.ri >= len(g) {
			o.gi++
			o.ri = 0
			continue
		}
		row := g[o.ri]
		o.ri++
		return SlotFromRow(o.Schema(), row), nil
	}
	return nil, EOF
}

func (o *incrementalSortOp) Close() error {
	o.groups = nil
	o.gi, o.ri = 0, 0
	o.ctx = nil
	return o.child.Close()
}
