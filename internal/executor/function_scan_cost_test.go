package executor

import "testing"

// TestFunctionScanCostsMatchPG pins M0149-0001h1a80 against PG 18.3's
// cost_functionscan: the call's evaluation is startup cost, each returned
// tuple costs cpu_tuple_cost plus the scan's quals, unnest estimates the
// array's length (array_unnest_support), and a function scan inside a join is
// priced the same way rather than as a sub-plan.
func TestFunctionScanCostsMatchPG(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for sql, want := range map[string]string{
		"EXPLAIN SELECT * FROM generate_series(1, 10)":                                       "Function Scan on generate_series  (cost=0.00..0.10 rows=10 width=4)",
		"EXPLAIN SELECT * FROM unnest(array[1,2,3])":                                         "Function Scan on unnest  (cost=0.00..0.03 rows=3 width=4)",
		"EXPLAIN SELECT * FROM generate_series(1,100) g WHERE g > 50":                        "Function Scan on generate_series g  (cost=0.00..1.25 rows=33 width=4)",
		"EXPLAIN SELECT * FROM string_to_table('a,b', ',') s WHERE s <> 'a'":                 "Function Scan on string_to_table s  (cost=0.00..12.50 rows=995 width=32)",
		"EXPLAIN SELECT * FROM generate_series(1,10) a JOIN generate_series(1,5) b ON a = b": "Hash Join  (cost=0.12..0.30 rows=5 width=8)",
	} {
		rows := runQuery(t, ctx, sql)
		if len(rows) == 0 {
			t.Fatalf("%s: no plan", sql)
		}
		if got := datumToString(rows[0][0]); got != want {
			t.Errorf("%s:\n got %q\nwant %q", sql, got, want)
		}
	}
}
