package executor

import (
	"strings"
	"testing"
)

// TestUniqueifiedSemiInnerRows (M0146-0005dk): an IN / EXISTS whose RHS is
// unique-ified (PG's JOIN_UNIQUE_INNER over UNIQUE_PATH_HASH or _SORT) must
// return exactly the semi join's rows — every outer row once, however many
// RHS duplicates match it, and none for a NULL key. The RHS here repeats
// each key 20 times and carries NULLs; the plain-join reference counts what
// a semi join must return.
func TestUniqueifiedSemiInnerRows(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE us_outer (k int, v int)")
	runSQL(t, ctx, "CREATE TABLE us_inner (k int, w int)")
	runSQL(t, ctx, "INSERT INTO us_outer SELECT CASE WHEN g % 50 = 0 THEN NULL ELSE g % 400 END, g FROM generate_series(1, 4000) g")
	runSQL(t, ctx, "INSERT INTO us_inner SELECT CASE WHEN g % 37 = 0 THEN NULL ELSE g % 150 END, g FROM generate_series(1, 3000) g")
	want := renderRows(runSQL(t, ctx,
		"SELECT count(*), sum(v) FROM us_outer o WHERE o.k IN (SELECT DISTINCT k FROM us_inner WHERE k IS NOT NULL)"))
	for _, sql := range []string{
		"SELECT count(*), sum(v) FROM us_outer o WHERE o.k IN (SELECT i.k FROM us_inner i)",
		"SELECT count(*), sum(v) FROM us_outer o WHERE EXISTS (SELECT 1 FROM us_inner i WHERE i.k = o.k)",
	} {
		plan := explainText(t, ctx, sql)
		if !strings.Contains(plan, "HashAggregate") && !strings.Contains(plan, "Unique") && !strings.Contains(plan, "Semi Join") {
			t.Fatalf("%s: unexpected plan:\n%s", sql, plan)
		}
		got := renderRows(runSQL(t, ctx, sql))
		if strings.Join(got, ";") != strings.Join(want, ";") {
			t.Fatalf("%s: got %v, want %v\nplan:\n%s", sql, got, want, plan)
		}
		t.Logf("%s\n%s", sql, plan)
	}
}
