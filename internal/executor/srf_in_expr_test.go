package executor

import "testing"

// TestSelectListSRFInsideExpression pins M0118-0008: a set-returning function
// nested inside a larger SELECT-list scalar expression (e.g.
// `generate_series(1,n) % 4`) must expand to one row per series element with the
// enclosing expression applied to each, not collapse to a single row holding the
// SRF's start value. The latter silently dropped rows — `INSERT INTO t SELECT
// generate_series(1,1000) % 4` inserted exactly one row instead of 1000.
func TestSelectListSRFInsideExpression(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	collect := func(sql string) []int64 {
		t.Helper()
		rows := runQuery(t, ctx, sql)
		out := make([]int64, 0, len(rows))
		for _, r := range rows {
			v, _ := datumInt64(r[0])
			out = append(out, v)
		}
		return out
	}

	eq := func(name string, got, want []int64) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: got %d rows %v, want %d rows %v", name, len(got), got, len(want), want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: row %d = %d, want %d (got=%v)", name, i, got[i], want[i], got)
			}
		}
	}

	// Nested in a binary expression: one row per element, modulo applied.
	eq("gs % 4",
		collect("SELECT generate_series(1,10) % 4"),
		[]int64{1, 2, 3, 0, 1, 2, 3, 0, 1, 2})

	// Nested with addition.
	eq("gs + 100",
		collect("SELECT generate_series(1,5) + 100"),
		[]int64{101, 102, 103, 104, 105})

	// Three-arg generate_series with a step, wrapped in multiplication.
	eq("gs(2,10,2) * 10",
		collect("SELECT generate_series(2,10,2) * 10"),
		[]int64{20, 40, 60, 80, 100})

	// Regression: a bare SRF in the target list still expands (unchanged path).
	eq("bare gs",
		collect("SELECT generate_series(1,4)"),
		[]int64{1, 2, 3, 4})
}
