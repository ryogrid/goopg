package executor

import "testing"

// TestDrainSetOpInputCountsMatchRowKeys pins M0122-0012 slice 81: the set-op
// drain forms each row's key in one reused buffer and counts through an
// index (a key string per distinct key, not per row). Its counts must be
// exactly the per-row rowKeyTrimmed counts — int and numeric spellings of one
// value, a bpchar-trimmed column and NULLs canonicalise as before — and a
// drain that wants no counts returns every row and no map.
func TestDrainSetOpInputCountsMatchRowKeys(t *testing.T) {
	rows := []Row{
		{NewIntDatum(1), NewStringDatum("a  ")},
		{Datum{Kind: KindNumeric, Int: 100, Scale: 2}, NewStringDatum("a")}, // 1.00 ≡ 1, "a" ≡ "a  " trimmed
		{NewIntDatum(2), NullDatum},
		{NewIntDatum(2), NullDatum},
		{NullDatum, NewStringDatum("b")},
		{NewIntDatum(1), NewStringDatum("a")},
		{NewIntDatum(3), NewStringDatum("c")},
	}
	trims := []bool{false, true}
	want := map[string]int{}
	for _, r := range rows {
		want[rowKeyTrimmed(r, nil, trims)]++
	}
	if len(want) != 4 {
		t.Fatalf("fixture has %d distinct keys, want 4 (the canonicalisation it exercises)", len(want))
	}
	gotRows, counts, err := drainSetOpInput(&rowsOp{rows: rows}, trims, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotRows) != len(rows) || len(counts) != len(want) {
		t.Fatalf("rows %d counts %v, want %d rows and %v", len(gotRows), counts, len(rows), want)
	}
	for k, n := range want {
		if counts[k] != n {
			t.Fatalf("count[%q] = %d, want %d (all %v)", k, counts[k], n, counts)
		}
	}
	gotRows, counts, err = drainSetOpInput(&rowsOp{rows: rows}, trims, false)
	if err != nil || len(gotRows) != len(rows) || counts != nil {
		t.Fatalf("count-free drain: %d rows, counts %v, err %v", len(gotRows), counts, err)
	}
}
