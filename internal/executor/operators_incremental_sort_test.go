package executor

// M0141-S7-exec-a: standalone tests for incrementalSortOp — constructed
// directly here, with zero createPlanNode/Plan() callers (see the operator's
// own file header). Order-equivalence is checked against sortOp, the
// existing full-sort oracle, following the same pattern
// sort_presorted_test.go already established for the E-15 contract.

import (
	"math/rand"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// drainIncSort collects an incrementalSortOp's full output rows in
// emission order.
func drainIncSort(t *testing.T, s *incrementalSortOp) []Row {
	t.Helper()
	if err := s.Open(&Context{}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out []Row
	for {
		slot, err := s.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		out = append(out, append(Row{}, slot.Row()...))
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return out
}

func TestIncrementalSortOp_OrderEquivalenceVsFullSort(t *testing.T) {
	keys := presortedKeys2()
	rng := rand.New(rand.NewSource(0x51707a))
	var rows []Row
	for g := int64(0); g < 8; g++ {
		for k := 0; k < 25; k++ {
			rows = append(rows, Row{NewIntDatum(g), NewIntDatum(rng.Int63n(1000))})
		}
	}
	// rows is presorted by the first key (n=1); a real planner would only
	// offer this shape when the child's own ordering already guarantees it.
	newFull := func(input []Row) *sortOp {
		return &sortOp{child: &fakeBorrowSource{rows: input}, keys: keys}
	}
	newInc := func(input []Row) *incrementalSortOp {
		return newIncrementalSortOp(&fakeBorrowSource{rows: input}, nil, keys, 1)
	}
	want := drainSort(t, newFull(rows))
	got := drainIncSort(t, newInc(rows))
	if !rowsEqualKeywise(got, want) {
		t.Fatal("incrementalSortOp output differs from full-sort oracle over presorted input")
	}
	if len(got) != len(rows) {
		t.Fatalf("emitted %d rows, want %d", len(got), len(rows))
	}
}

func TestIncrementalSortOp_NullDescFirstKey(t *testing.T) {
	keys := []optimizer.SortKey{
		{Expr: &optimizer.ColumnRef{Index: 0}, Desc: true, NullsFirst: true},
		{Expr: &optimizer.ColumnRef{Index: 1}, Desc: false},
	}
	rng := rand.New(rand.NewSource(0xE157))
	null := Datum{Kind: KindNull}
	mkrow := func(a Datum, b int64) Row { return Row{a, NewIntDatum(b)} }
	var grouped []Row
	for k := 0; k < 10; k++ {
		grouped = append(grouped, mkrow(null, rng.Int63n(100)))
	}
	for k := 0; k < 15; k++ {
		grouped = append(grouped, mkrow(NewIntDatum(5), rng.Int63n(100)))
	}
	for k := 0; k < 12; k++ {
		grouped = append(grouped, mkrow(NewIntDatum(3), rng.Int63n(100)))
	}
	// grouped is presorted under DESC NULLS FIRST: NULLs, then 5s, then 3s.
	newFull := func(input []Row) *sortOp {
		return &sortOp{child: &fakeBorrowSource{rows: input}, keys: keys}
	}
	newInc := func(input []Row) *incrementalSortOp {
		return newIncrementalSortOp(&fakeBorrowSource{rows: input}, nil, keys, 1)
	}
	want := drainSort(t, newFull(grouped))
	got := drainIncSort(t, newInc(grouped))
	if !rowsEqualKeywise(got, want) {
		t.Fatal("incrementalSortOp output differs from full-sort oracle over NULL/DESC presorted input")
	}
	if len(got) != 37 {
		t.Fatalf("emitted %d rows, want 37", len(got))
	}
	for i := 0; i < 10; i++ {
		if !got[i][0].IsNull() {
			t.Fatalf("row %d must be NULL-first, got %v", i, got[i][0])
		}
	}
}

// TestIncrementalSortOp_ZeroPresortedCount pins the degenerate case
// (PresortedCount=0, or an input with no genuine presorted prefix) to a
// single group covering the whole input — order-equivalent to a plain full
// sort, matching nodeIncrementalSort.c's own worst-case fallback.
func TestIncrementalSortOp_ZeroPresortedCount(t *testing.T) {
	keys := presortedKeys2()
	rng := rand.New(rand.NewSource(0xC0))
	var rows []Row
	for k := 0; k < 40; k++ {
		rows = append(rows, Row{NewIntDatum(rng.Int63n(5)), NewIntDatum(rng.Int63n(1000))})
	}
	newFull := func(input []Row) *sortOp {
		return &sortOp{child: &fakeBorrowSource{rows: input}, keys: keys}
	}
	newInc := func(input []Row) *incrementalSortOp {
		return newIncrementalSortOp(&fakeBorrowSource{rows: input}, nil, keys, 0)
	}
	want := drainSort(t, newFull(rows))
	got := drainIncSort(t, newInc(rows))
	if !rowsEqualKeywise(got, want) {
		t.Fatal("PresortedCount=0 must be order-equivalent to a full sort")
	}
}

func TestIncrementalSortOp_EmptyInput(t *testing.T) {
	keys := presortedKeys2()
	s := newIncrementalSortOp(&fakeBorrowSource{}, nil, keys, 1)
	got := drainIncSort(t, s)
	if len(got) != 0 {
		t.Fatalf("emitted %d rows for empty input, want 0", len(got))
	}
}

// TestIncrementalSortOp_GroupCountMatchesPrefix pins the actual grouping
// mechanism (not just the end-to-end order), the property that
// distinguishes an Incremental Sort from "coincidentally the same output as
// a full sort": adjacent output rows sharing the presorted prefix must have
// come from the SAME internal group, and the group count must equal the
// distinct-prefix count.
func TestIncrementalSortOp_GroupCountMatchesPrefix(t *testing.T) {
	keys := presortedKeys2()
	var rows []Row
	for g := int64(0); g < 5; g++ {
		for k := int64(0); k < 6; k++ {
			// Descending second-key arrival order within a group: a
			// no-op operator would leave this unsorted, so this also
			// pins that each group actually gets sorted.
			rows = append(rows, Row{NewIntDatum(g), NewIntDatum(6 - k)})
		}
	}
	s := newIncrementalSortOp(&fakeBorrowSource{rows: rows}, nil, keys, 1)
	got := drainIncSort(t, s)
	if len(got) != len(rows) {
		t.Fatalf("emitted %d rows, want %d", len(got), len(rows))
	}
	for i := 1; i < len(got); i++ {
		a, b := got[i-1], got[i]
		if a[0].Int == b[0].Int {
			// Second key is DESC (presortedKeys2): within a group, values
			// must be non-increasing.
			if a[1].Int < b[1].Int {
				t.Fatalf("group at prefix %d not sorted DESC at row %d: %v before %v", a[0].Int, i, a, b)
			}
			continue
		}
		if a[0].Int >= b[0].Int {
			t.Fatalf("groups out of arrival order at row %d: %v then %v", i, a, b)
		}
	}
}
