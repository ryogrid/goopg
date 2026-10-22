package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestCTEScanReusesOneSlot pins M0122-0012 slice 38: a CTE scan returns one
// slot re-pointed at each cached row, as PG's CteScan stores every tuple into
// its single scan slot, instead of allocating a slot per row (21 % of TPC-DS
// Q81's correlated SubPlan, which rescans its CTE once per outer row). The
// rows are the cache's own and must come back unchanged and in order, for
// both CTE scan kinds.
func TestCTEScanReusesOneSlot(t *testing.T) {
	rows := []Row{
		{NewIntDatum(1), NewStringDatum("a")},
		{NewIntDatum(2), NewStringDatum("b")},
		{NewIntDatum(3), NewStringDatum("c")},
	}
	drain := func(t *testing.T, next func() (TupleSlot, error)) {
		t.Helper()
		var first TupleSlot
		for i := range rows {
			slot, err := next()
			if err != nil {
				t.Fatalf("row %d: %v", i, err)
			}
			if first == nil {
				first = slot
			} else if slot != first {
				t.Fatalf("row %d: a new slot per row", i)
			}
			got := slotRow(slot)
			if &got[0] != &rows[i][0] {
				t.Fatalf("row %d: the slot does not carry the cached row itself", i)
			}
			if got[0].Int != int64(i+1) || got[1].StringValue() != rows[i][1].StringValue() {
				t.Fatalf("row %d = %v, want %v", i, got, rows[i])
			}
		}
		if _, err := next(); err != EOF {
			t.Fatalf("after the last row: %v, want EOF", err)
		}
	}
	t.Run("cte-scan", func(t *testing.T) {
		op := &cteScanOp{plan: &optimizer.CTEScan{}, rows: rows}
		drain(t, op.Next)
	})
	t.Run("materialized-cte-scan", func(t *testing.T) {
		mrows := make([][]Datum, len(rows))
		for i, r := range rows {
			mrows[i] = r
		}
		op := &materializedCTEScanOp{plan: &optimizer.MaterializedCTEScan{}, rows: mrows}
		drain(t, op.Next)
	})
}
