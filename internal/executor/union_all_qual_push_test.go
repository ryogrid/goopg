package executor

import (
	"strings"
	"testing"
)

// TestUnionAllRestrictionPushdownValues pins M0146-0094's values: a
// restriction pushed into the members of a UNION ALL (member constants
// folded, members dropped) must return exactly what the same query returns
// with the union fenced by OFFSET 0, which keeps it out of the push.
func TestUnionAllRestrictionPushdownValues(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE ua1 (k int, v numeric, pad text)",
		"CREATE TABLE ua2 (k int, v numeric, pad text)",
		"CREATE TABLE uad (k int, m int)",
		"INSERT INTO ua1 SELECT g % 50, g, CASE WHEN g % 7 = 0 THEN NULL ELSE 'x' END FROM generate_series(1, 2000) g",
		"INSERT INTO ua2 SELECT g % 50, g * 2, 'y' FROM generate_series(1, 1500) g",
		"INSERT INTO uad SELECT g, g % 5 FROM generate_series(0, 49) g",
		"ANALYZE ua1", "ANALYZE ua2", "ANALYZE uad",
	} {
		runSQL(t, ctx, q)
	}
	const body = "select v as price, k as dk, pad, 1 as src, 'a'::text as tag from ua1 where k > 3 union all select v, k, pad, 2, 'b' from ua2"
	for _, where := range []string{
		"src > 0",
		"src = 2",
		"src = 1 and price > 100",
		"tag <> 'a' and dk < 10",
		"pad is null",
		"price between 50 and 900 or src = 2",
		"src = 3",
	} {
		pushed := "select count(*), sum(price), sum(dk) from (" + body + ") u, uad where uad.k = u.dk and " + where
		fenced := "select count(*), sum(price), sum(dk) from (" + body + " offset 0) u, uad where uad.k = u.dk and " + where
		got := strings.Join(renderRows(runSQL(t, ctx, pushed)), ";")
		want := strings.Join(renderRows(runSQL(t, ctx, fenced)), ";")
		if got != want {
			t.Errorf("where %s: pushed %q, fenced %q", where, got, want)
		}
	}
	// A LIMITed member: pushing below its LIMIT would pick other rows.
	const limited = "select v, k from ua2 union all (select v, k from ua1 order by v limit 30)"
	for _, where := range []string{"k > 10", "v < 500"} {
		pushed := "select count(*), sum(v) from (" + limited + ") c where " + where
		fenced := "select count(*), sum(v) from (" + limited + " offset 0) c where " + where
		got := strings.Join(renderRows(runSQL(t, ctx, pushed)), ";")
		want := strings.Join(renderRows(runSQL(t, ctx, fenced)), ";")
		if got != want {
			t.Errorf("limited where %s: pushed %q, fenced %q", where, got, want)
		}
	}
	// `*` members (regress union.sql's constraint-exclusion case).
	const star = "select 1 as t, * from ua1 union all select 2 as t, * from ua2"
	for _, where := range []string{"t = 2", "t = 1 and k < 5", "pad is null or t = 2"} {
		pushed := "select count(*), sum(v), sum(k) from (" + star + ") c where " + where
		fenced := "select count(*), sum(v), sum(k) from (" + star + " offset 0) c where " + where
		got := strings.Join(renderRows(runSQL(t, ctx, pushed)), ";")
		want := strings.Join(renderRows(runSQL(t, ctx, fenced)), ";")
		if got != want {
			t.Errorf("star where %s: pushed %q, fenced %q", where, got, want)
		}
	}
}
