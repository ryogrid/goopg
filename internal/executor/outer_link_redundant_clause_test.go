package executor

import (
	"strings"
	"testing"
)

// TestOuterJoinClauseRedundantAfterConstantDerivation (M0146-0005dz): in
// `ss LEFT JOIN ws ON ws.y = ss.y AND ws.i = ss.i … WHERE ss.y = 2000`, PG's
// reconsider_outer_join_clauses pushes `ws.y = 2000` into the nullable side
// and removes `ws.y = ss.y` from the join (TPC-DS Q78 merges on item and
// customer only). The join must still return what the query returns with the
// clause kept — `ws.y + 0 = ss.y` is not a bare column equality, so nothing
// is derived from it — and its join condition must no longer mention y. Both
// routes are covered: grouped CTE bodies (the AST push, Q78's route) and base
// relations (the join-search seam's deriveOuterLinkConstants).
func TestOuterJoinClauseRedundantAfterConstantDerivation(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE ol_ss (y int, i int, c int, q int)",
		"CREATE TABLE ol_ws (y int, i int, c int, q int)",
		"INSERT INTO ol_ss SELECT 1998 + g % 4, g % 50, g % 70, g FROM generate_series(1, 6000) g",
		"INSERT INTO ol_ws SELECT 1998 + g % 3, g % 60, g % 50, g FROM generate_series(1, 5000) g",
		"ANALYZE ol_ss", "ANALYZE ol_ws",
	} {
		runSQL(t, ctx, q)
	}
	// Base relations take the join-search seam's deriveOuterLinkConstants
	// instead of the AST push into grouped bodies; same rule.
	base := "SELECT count(*), sum(s.q), sum(w.q), count(w.y) FROM ol_ss s LEFT JOIN ol_ws w ON "
	bsql := base + "w.y = s.y AND w.i = s.i AND w.c = s.c WHERE s.y = 2000"
	bref := base + "w.y + 0 = s.y AND w.i = s.i AND w.c = s.c WHERE s.y = 2000"
	bwant := strings.Join(renderRows(runSQL(t, ctx, bref)), ";")
	bplan := explainText(t, ctx, bsql)
	if got := strings.Join(renderRows(runSQL(t, ctx, bsql)), ";"); got != bwant {
		t.Fatalf("base: got %s, want %s\nplan:\n%s", got, bwant, bplan)
	}
	assertNoYJoinCond(t, bplan)
	const body = "WITH ss AS (SELECT y, i, c, sum(q) sq FROM ol_ss GROUP BY y, i, c), " +
		"ws AS (SELECT y, i, c, sum(q) wq FROM ol_ws GROUP BY y, i, c) " +
		"SELECT count(*), sum(ss.sq), sum(ws.wq), count(ws.y) FROM ss LEFT JOIN ws ON "
	sql := body + "ws.y = ss.y AND ws.i = ss.i AND ws.c = ss.c WHERE ss.y = 2000"
	ref := body + "ws.y + 0 = ss.y AND ws.i = ss.i AND ws.c = ss.c WHERE ss.y = 2000"
	want := strings.Join(renderRows(runSQL(t, ctx, ref)), ";")
	plan := explainText(t, ctx, sql)
	if got := strings.Join(renderRows(runSQL(t, ctx, sql)), ";"); got != want {
		t.Fatalf("got %s, want %s\nplan:\n%s", got, want, plan)
	}
	assertNoYJoinCond(t, plan)
}

// assertNoYJoinCond checks PG 18.3's shape on the same data: the join keys
// on i and c only, and both inputs carry `y = 2000`.
func assertNoYJoinCond(t *testing.T, plan string) {
	t.Helper()
	for _, l := range strings.Split(plan, "\n") {
		if (strings.Contains(l, " Cond:") || strings.Contains(l, "Join Filter:")) && strings.Contains(l, "y = ") {
			t.Fatalf("the outer-join clause on y should be redundant, found %q\nplan:\n%s", strings.TrimSpace(l), plan)
		}
	}
	if strings.Count(plan, "(y = 2000)") != 2 {
		t.Fatalf("want y = 2000 on both inputs\nplan:\n%s", plan)
	}
}
