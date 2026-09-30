package executor

import (
	"strings"
	"testing"
)

// TestOneElementInIsEquality pins M0146-0005by against PG 18.3:
// transformAExprIn builds a ScalarArrayOpExpr only for two or more non-Var
// list items, so a one-element IN list is a plain comparison — `=` for IN,
// `<>` for NOT IN. `= ANY (ARRAY[7])` is not IN syntax and keeps its array.
// PG prints (fixture below):
//
//	a IN (7)            Filter: (a = 7)            rows=100
//	a NOT IN (7)        Filter: (a <> 7)           rows=9900
//	a = ANY (ARRAY[7])  Filter: (a = ANY ('{7}'::integer[]))
//	b IN ('3')          Filter: (b = '3'::text)    rows=1000
//
// TPC-DS Q89's `d_year IN (2001)` printed `(d_year = ANY (2001))` in goopg.
func TestOneElementInIsEquality(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE e (a int, b text)")
	runSQL(t, ctx, "INSERT INTO e SELECT i % 100, (i % 10)::text FROM generate_series(1,10000) i")
	runSQL(t, ctx, "ANALYZE e")
	for _, c := range []struct{ where, filter, rows, count string }{
		{"a IN (7)", "Filter: (a = 7)", " rows=100 ", "100"},
		{"a NOT IN (7)", "Filter: (a <> 7)", " rows=9900 ", "9900"},
		{"b IN ('3')", "Filter: (b = '3')", " rows=1000 ", "1000"},
		{"a = ANY (ARRAY[7])", "ANY", "", "100"},
	} {
		plan := strings.Join(runExplainRows(t, ctx, "EXPLAIN SELECT * FROM e WHERE "+c.where), "\n")
		if !strings.Contains(plan, c.filter) || !strings.Contains(plan, c.rows) {
			t.Errorf("%s: want %q and%q in:\n%s", c.where, c.filter, c.rows, plan)
		}
		rows := formatRows(runQueryRows(t, ctx, "SELECT count(*) FROM e WHERE "+c.where))
		if len(rows) != 1 || rows[0] != c.count {
			t.Errorf("%s: count = %v, want %s", c.where, rows, c.count)
		}
	}
}
