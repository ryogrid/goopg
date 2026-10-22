package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// M0122-0012 slice 44: the sorted aggregate and the incremental sort stream.
// Each Next reads the child only up to the first row of the following group,
// as nodeAgg.c agg_retrieve_direct and nodeIncrementalSort.c do, so a LIMIT
// above stops the child early (TPC-DS Q35's LIMIT 100 over GroupAggregate →
// Incremental Sort read every row of a 16M-pair nested loop). These tests
// count the child's pulls; an operator that drains its child in Open fails
// them.

// countingSource hands out rows and counts the rows it has returned.
type countingSource struct {
	rows   []Row
	idx    int
	pulled int
}

func (o *countingSource) Open(*Context) error {
	o.idx, o.pulled = 0, 0
	return nil
}
func (o *countingSource) Schema() optimizer.Schema { return nil }
func (o *countingSource) Close() error              { return nil }
func (o *countingSource) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	r := o.rows[o.idx]
	o.idx++
	o.pulled++
	return asSlot(nil, cloneRow(r)), nil
}

func pairRows(pairs ...[2]int64) []Row {
	rows := make([]Row, len(pairs))
	for i, p := range pairs {
		rows[i] = Row{NewIntDatum(p[0]), NewIntDatum(p[1])}
	}
	return rows
}

func TestSortedAggregateStreamsGroups(t *testing.T) {
	// Keys 1,1 | 2,2,2 | 3 — already in key order (the AGG_SORTED contract).
	src := &countingSource{rows: pairRows([2]int64{1, 10}, [2]int64{1, 20}, [2]int64{2, 1}, [2]int64{2, 2}, [2]int64{2, 3}, [2]int64{3, 7})}
	op, err := Build(sortedAggPlan(optimizer.AggStrategySorted, []int{0}, 1, nil))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	agg, ok := op.(*aggregateOp)
	if !ok {
		t.Fatalf("Build returned %T, want *aggregateOp", op)
	}
	agg.child = src
	for pass := 0; pass < 2; pass++ { // the second pass re-opens, as a rescan does
		if err := agg.Open(NewContext()); err != nil {
			t.Fatalf("Open: %v", err)
		}
		if src.pulled != 0 {
			t.Fatalf("pass %d: Open pulled %d rows, want 0", pass, src.pulled)
		}
		want := []struct{ key, cnt, sum, pulled int64 }{
			{1, 2, 30, 3}, // group 1 plus the row that closes it
			{2, 3, 6, 6},  // group 2 plus the row that closes it
			{3, 1, 7, 6},  // the last group ends at EOF
		}
		for i, w := range want {
			slot, err := agg.Next()
			if err != nil {
				t.Fatalf("pass %d Next %d: %v", pass, i, err)
			}
			r := slot.Row()
			if r[0].Int != w.key || r[1].Int != w.cnt || r[2].Int != w.sum {
				t.Fatalf("pass %d row %d = %v, want key=%d count=%d sum=%d", pass, i, r, w.key, w.cnt, w.sum)
			}
			if int64(src.pulled) != w.pulled {
				t.Fatalf("pass %d: after group %d the child returned %d rows, want %d", pass, w.key, src.pulled, w.pulled)
			}
		}
		if _, err := agg.Next(); err != EOF {
			t.Fatalf("pass %d: after the last group Next = %v, want EOF", pass, err)
		}
		if err := agg.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
}

func TestIncrementalSortStreamsGroups(t *testing.T) {
	// Prefix groups 0 (three rows) | 1 (two rows) | 2 (one row); the second
	// key is DESC (presortedKeys2) and arrives ascending, so each group must
	// still be sorted.
	src := &countingSource{rows: pairRows([2]int64{0, 1}, [2]int64{0, 3}, [2]int64{0, 2}, [2]int64{1, 5}, [2]int64{1, 9}, [2]int64{2, 4})}
	s := newIncrementalSortOp(src, nil, presortedKeys2(), 1)
	if err := s.Open(&Context{}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if src.pulled != 0 {
		t.Fatalf("Open pulled %d rows, want 0", src.pulled)
	}
	want := []struct{ k, v, pulled int64 }{
		{0, 3, 4}, {0, 2, 4}, {0, 1, 4}, // group 0 plus the row that closes it
		{1, 9, 6}, {1, 5, 6}, // group 1 plus the row that closes it
		{2, 4, 6}, // the last group ends at EOF
	}
	for i, w := range want {
		slot, err := s.Next()
		if err != nil {
			t.Fatalf("Next %d: %v", i, err)
		}
		r := slot.Row()
		if r[0].Int != w.k || r[1].Int != w.v {
			t.Fatalf("row %d = %v, want (%d, %d)", i, r, w.k, w.v)
		}
		if int64(src.pulled) != w.pulled {
			t.Fatalf("row %d: the child returned %d rows, want %d", i, src.pulled, w.pulled)
		}
	}
	if _, err := s.Next(); err != EOF {
		t.Fatalf("after the last group Next = %v, want EOF", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
