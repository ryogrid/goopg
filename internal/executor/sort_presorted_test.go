package executor

// E-15 gate: contract doc + test, no behaviour change. These tests pin
// (a) the group-boundary predicate and (b) the order-equivalence property
// a future Incremental Sort must preserve, measured against CURRENT
// full-sort behaviour (the oracle E-03 implements against).

import (
	"math/rand"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

func presortedKeys2() []optimizer.SortKey {
	return []optimizer.SortKey{
		{Expr: &optimizer.ColumnRef{Index: 0}, Desc: false},
		{Expr: &optimizer.ColumnRef{Index: 1}, Desc: true, NullsFirst: true},
	}
}

func TestSortPrefixEqualTable(t *testing.T) {
	keys := presortedKeys2()
	i := func(v int64) Datum { return NewIntDatum(v) }
	n := Datum{Kind: KindNull}
	tests := []struct {
		name  string
		a, b  []Datum
		n     int
		equal bool
	}{
		{"equal prefix ignores second key", []Datum{i(1), i(9)}, []Datum{i(1), i(2)}, 1, true},
		{"full equal", []Datum{i(1), i(9)}, []Datum{i(1), i(9)}, 2, true},
		{"first differs", []Datum{i(1), i(9)}, []Datum{i(2), i(9)}, 1, false},
		{"second differs at n=2", []Datum{i(1), i(9)}, []Datum{i(1), i(2)}, 2, false},
		{"both null equal", []Datum{i(1), n}, []Datum{i(1), n}, 2, true},
		{"null vs value", []Datum{i(1), n}, []Datum{i(1), i(2)}, 2, false},
		{"value vs null", []Datum{i(1), i(2)}, []Datum{i(1), n}, 2, false},
		{"n=0 vacuous", []Datum{i(1), i(9)}, []Datum{i(2), i(3)}, 0, true},
		{"negative n vacuous", []Datum{i(1), i(9)}, []Datum{i(2), i(3)}, -1, true},
		{"n clamped to keys", []Datum{i(1), i(9)}, []Datum{i(1), i(9)}, 99, true},
		{"n clamped to row width", []Datum{i(1)}, []Datum{i(1)}, 2, true},
		// Direction plays no role in equality: DESC second key, 9 vs 2
		// differ as values regardless of order direction.
		{"desc irrelevant", []Datum{i(1), i(9)}, []Datum{i(1), i(2)}, 2, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sortPrefixEqual(tc.a, tc.b, keys, tc.n); got != tc.equal {
				t.Errorf("sortPrefixEqual(%v, %v, n=%d) = %v, want %v",
					tc.a, tc.b, tc.n, got, tc.equal)
			}
		})
	}
}

func TestSortPrefixEqualIncomparableSplits(t *testing.T) {
	keys := presortedKeys2()
	a := []Datum{NewStringDatum("x"), NewIntDatum(1)}
	b := []Datum{NewIntDatum(1), NewIntDatum(1)}
	// int key vs string value: not equal as values → split (false),
	// never merge, never error. (Mechanism note: compareDatum does NOT
	// error here — cross-kind falls back to Format-compare, so the split
	// comes from cmp != 0; the err arm in sortPrefixEqual is defensive
	// for unhandled DatumKinds only.)
	if sortPrefixEqual(a, b, keys, 1) {
		t.Error("incomparable pair must split (false), not merge")
	}
}

// drainSort collects a sortOp's full output rows in emission order.
func drainSort(t *testing.T, s *sortOp) []Row {
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

func rowsEqualKeywise(a, b []Row) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			av, bv := a[i][j], b[i][j]
			// Null-aware: datumEquals (FK helper, Format-based) reports
			// NULL as unequal to everything, which would false-fail
			// NULL-group equivalence below.
			if av.IsNull() || bv.IsNull() {
				if !(av.IsNull() && bv.IsNull()) {
					return false
				}
				continue
			}
			if !datumEquals(av, bv) {
				return false
			}
		}
	}
	return true
}

// TestPresortedInputOrderEquivalence pins the executor half of the E-15
// contract against CURRENT behaviour: a presorted-prefix input sorts to
// the identical sequence as the same rows shuffled. A future Incremental
// Sort must reproduce `want` exactly — this test is its oracle.
func TestPresortedInputOrderEquivalence(t *testing.T) {
	keys := presortedKeys2()
	rng := rand.New(rand.NewSource(0xE15))
	var rows []Row
	for g := int64(0); g < 8; g++ {
		for k := 0; k < 25; k++ {
			rows = append(rows, Row{NewIntDatum(g), NewIntDatum(rng.Int63n(1000))})
		}
	}
	// rows is already grouped by the first key (presorted prefix, n=1).
	shuffled := append([]Row{}, rows...)
	rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

	newSort := func(input []Row) *sortOp {
		return &sortOp{child: &fakeBorrowSource{rows: input}, keys: keys}
	}
	want := drainSort(t, newSort(shuffled))
	got := drainSort(t, newSort(rows))
	if !rowsEqualKeywise(got, want) {
		t.Fatal("presorted-prefix input sorts to a different sequence than shuffled input")
	}
	// And the sequence is fully ordered over both keys.
	for i := 1; i < len(got); i++ {
		a, b := got[i-1], got[i]
		less := a[0].Int < b[0].Int || (a[0].Int == b[0].Int && a[1].Int >= b[1].Int)
		if !less {
			t.Fatalf("output not fully ordered at %d: %v before %v (keys ASC,DESC)", i, a, b)
		}
	}
	// Group framing agrees with the emitted order: every adjacent pair
	// inside a prefix group satisfies sortPrefixEqual, and group count
	// matches the distinct first-key count.
	groups := 1
	for i := 1; i < len(got); i++ {
		eq := sortPrefixEqual([]Datum{got[i-1][0], got[i-1][1]}, []Datum{got[i][0], got[i][1]}, keys, 1)
		if got[i][0].Int == got[i-1][0].Int && !eq {
			t.Fatalf("same-prefix pair %d splits under n=1", i)
		}
		if got[i][0].Int != got[i-1][0].Int {
			if eq {
				t.Fatalf("cross-prefix pair %d merges under n=1", i)
			}
			groups++
		}
	}
	if groups != 8 {
		t.Errorf("groups = %d, want 8", groups)
	}
}

// TestPresortedInputNullAndDescFirstKey extends the equivalence oracle to
// NULL groups under a DESC NULLS-FIRST first key: NULL framing, group
// contiguity, and full DESC ordering all pinned for E-03.
func TestPresortedInputNullAndDescFirstKey(t *testing.T) {
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
	shuffled := append([]Row{}, grouped...)
	rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

	newSort := func(input []Row) *sortOp {
		return &sortOp{child: &fakeBorrowSource{rows: input}, keys: keys}
	}
	want := drainSort(t, newSort(shuffled))
	got := drainSort(t, newSort(grouped))
	if !rowsEqualKeywise(got, want) {
		t.Fatal("NULL/DESC presorted input sorts differently than shuffled input")
	}
	if len(got) != 37 {
		t.Fatalf("emitted %d rows, want 37", len(got))
	}
	// NULL group first and contiguous (NULLS FIRST).
	for i := 0; i < 10; i++ {
		if !got[i][0].IsNull() {
			t.Fatalf("row %d must be NULL-first, got %v", i, got[i][0])
		}
	}
	for i := 10; i < 37; i++ {
		if got[i][0].IsNull() {
			t.Fatalf("NULL leaks past the first group at row %d", i)
		}
	}
	// DESC on the non-null tail: 5s before 3s; ASC within each group.
	for i := 11; i < 37; i++ {
		a, b := got[i-1], got[i]
		if a[0].Int < b[0].Int {
			t.Fatalf("DESC violated at %d: %v before %v", i, a, b)
		}
		if a[0].Int == b[0].Int && a[1].Int > b[1].Int {
			t.Fatalf("ASC second key violated at %d: %v before %v", i, a, b)
		}
	}
	// Framing: NULL==NULL merges; NULL vs value splits.
	if !sortPrefixEqual([]Datum{null, NewIntDatum(1)}, []Datum{null, NewIntDatum(2)}, keys, 1) {
		t.Error("NULL pair must merge under n=1")
	}
	if sortPrefixEqual([]Datum{null, NewIntDatum(1)}, []Datum{NewIntDatum(5), NewIntDatum(1)}, keys, 1) {
		t.Error("NULL vs value must split under n=1")
	}
}
