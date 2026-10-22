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

	// group is the presorted-prefix group being emitted, sorted by every key;
	// ri is the next row of it to return. groupKeys holds its key values while
	// it is collected.
	group     []Row
	groupKeys [][]Datum
	ri        int
	// next / nextKV is the first row of the following group, read to find
	// where the current group ends and held until that group is collected.
	next    Row
	nextKV  []Datum
	hasNext bool
	// eof records that the child returned EOF.
	eof    bool
	pulled int
}

func newIncrementalSortOp(child Operator, plan *optimizer.IncrementalSort, keys []optimizer.SortKey, presortedCount int) *incrementalSortOp {
	return &incrementalSortOp{child: child, plan: plan, keys: keys, presortedCount: presortedCount}
}

func (o *incrementalSortOp) Schema() optimizer.Schema { return o.child.Schema() }

// Open opens the child and reads nothing from it. Groups are read on demand
// by Next, one at a time, as PG's ExecIncrementalSort does: a group is
// collected, sorted and emitted before the next one is read, so a LIMIT above
// stops the child after the group that completes it (M0122-0012 slice 44 —
// TPC-DS Q35's LIMIT 100 read 50 000 outer rows of a nested loop where PG read
// 20 300). Draining the child in Open, as this operator first did, returned
// the same rows in the same order but always paid for the whole input.
func (o *incrementalSortOp) Open(ctx *Context) error {
	o.ctx = ctx
	o.group, o.groupKeys = o.group[:0], o.groupKeys[:0]
	o.ri = 0
	o.next, o.nextKV, o.hasNext = nil, nil, false
	o.eof = false
	o.pulled = 0
	// A `ctid` key reads the child's carried tid (M0146-0052).
	if sortKeysUseCTID(o.keys) {
		markSortWantCTIDs(o.child)
	}
	return o.child.Open(ctx)
}

// readGroup collects the next presorted-prefix group into o.group and sorts
// it. A group closes the moment a row's prefix no longer matches the
// previous row's (sortPrefixEqual's "groups are contiguous" contract); that
// row is held as the first of the following group. Peak memory is bounded by
// the largest group, not the input (E-15's EXECUTOR GUARANTEE). It reports
// false when the child is exhausted and nothing remains.
func (o *incrementalSortOp) readGroup() (bool, error) {
	for i := range o.group {
		o.group[i] = nil
	}
	o.group, o.groupKeys = o.group[:0], o.groupKeys[:0]
	o.ri = 0
	prevKV := []Datum(nil)
	if o.hasNext {
		o.group = append(o.group, o.next)
		o.groupKeys = append(o.groupKeys, o.nextKV)
		prevKV = o.nextKV
		o.next, o.nextKV, o.hasNext = nil, nil, false
	}
	for !o.eof {
		// M0062-followup precedent (sortOp.Open): give a cancel opportunity
		// every 4096 rows.
		if o.pulled&0xFFF == 0 && o.ctx != nil && o.ctx.Ctx != nil {
			if err := o.ctx.Ctx.Err(); err != nil {
				return false, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		slot, err := o.child.Next()
		if err == EOF {
			o.eof = true
			break
		}
		if err != nil {
			return false, err
		}
		o.pulled++
		ms := slot.Materialize()
		row := ms.Row()
		// On the slot, so a `ctid` key sees the carried tid (M0146-0052).
		kv, kerr := o.sortKeyVals(ms)
		if kerr != nil {
			return false, kerr
		}
		if prevKV != nil && !sortPrefixEqual(prevKV, kv, o.keys, o.presortedCount) {
			o.next, o.nextKV, o.hasNext = row, kv, true
			break
		}
		o.group = append(o.group, row)
		o.groupKeys = append(o.groupKeys, kv)
		prevKV = kv
	}
	if len(o.group) == 0 {
		return false, nil
	}
	if err := o.sortGroup(o.group, o.groupKeys); err != nil {
		return false, err
	}
	return true, nil
}

// sortKeyVals evaluates every ORDER BY key for one row, once — same shape as
// sortOp.sortKeyVals (operators.go), duplicated rather than shared because
// exec-a is deliberately standalone (no sortOp dependency).
func (o *incrementalSortOp) sortKeyVals(view SlotView) ([]Datum, error) {
	if len(o.keys) == 0 {
		return nil, nil
	}
	kv := make([]Datum, len(o.keys))
	for i, k := range o.keys {
		v, err := evalSortKeyValueSlot(k.Expr, view, o.ctx)
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
		cmp, err := compareDatumTyped(av, bv, pos, k.Expr)
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
	if o.ri >= len(o.group) {
		ok, err := o.readGroup()
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, EOF
		}
	}
	row := o.group[o.ri]
	o.ri++
	return SlotFromRow(o.Schema(), row), nil
}

func (o *incrementalSortOp) Close() error {
	o.group, o.groupKeys = nil, nil
	o.ri = 0
	o.next, o.nextKV, o.hasNext = nil, nil, false
	o.ctx = nil
	return o.child.Close()
}
