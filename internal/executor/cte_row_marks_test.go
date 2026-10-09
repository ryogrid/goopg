package executor

import (
	"strings"
	"testing"
)

// TestRowMarksAndWithQueries pins M0146-0007g, PG 18.3's two row-mark rules
// for WITH queries (regress subselect, "SELECT FOR UPDATE cannot be inlined"
// and "Row marks are not pushed into CTEs"):
//
//   - transformLockingClause skips RTE_CTE when a FOR UPDATE names no
//     relation, so a WITH query's rows are not locked, and naming one in the
//     OF list is "FOR UPDATE cannot be applied to a WITH query" (0A000).
//     goopg locked through the CTE's synthetic relation and failed with
//     "short read at block".
//   - contain_dml counts a row mark as DML, so a CTE whose body locks rows is
//     never inlined: it runs once, as a CTE.
func TestRowMarksAndWithQueries(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE rm_t (f1 int, f2 int)")
	runSQL(t, ctx, "INSERT INTO rm_t SELECT g, g FROM generate_series(1, 5) g")

	const bare = "WITH x AS (SELECT * FROM rm_t) SELECT * FROM x FOR UPDATE"
	rows, err := runSQLCtxErr(t, ctx, bare)
	if err != nil {
		t.Fatalf("%s: %v (PG returns the 5 rows)", bare, err)
	}
	if len(rows) != 5 {
		t.Fatalf("%s: %d rows, want PG's 5", bare, len(rows))
	}
	if plan := renderRows(runSQL(t, ctx, "EXPLAIN (COSTS OFF) "+bare)); countLinesContaining(plan, "LockRows") != 0 {
		t.Errorf("FOR UPDATE over a WITH query marks no relation, so no LockRows (PG: Seq Scan only):\n%s",
			strings.Join(plan, "\n"))
	}

	joined := "WITH x AS (SELECT * FROM rm_t) SELECT * FROM x, rm_t r WHERE x.f1 = r.f1 FOR UPDATE"
	if rows := runSQL(t, ctx, joined); len(rows) != 5 {
		t.Errorf("%s: %d rows, want 5", joined, len(rows))
	}
	if plan := renderRows(runSQL(t, ctx, "EXPLAIN (COSTS OFF) "+joined)); countLinesContaining(plan, "LockRows") != 1 {
		t.Errorf("r is still locked:\n%s", strings.Join(plan, "\n"))
	}

	for _, c := range []struct{ sql, want string }{
		{"WITH x AS (SELECT * FROM rm_t) SELECT * FROM x FOR UPDATE OF x",
			"FOR UPDATE cannot be applied to a WITH query"},
		{"WITH x AS (SELECT * FROM rm_t) SELECT * FROM x, x x2 WHERE x.f1 = x2.f1 FOR SHARE OF x",
			"FOR SHARE cannot be applied to a WITH query"},
		{"WITH x AS NOT MATERIALIZED (SELECT * FROM rm_t) SELECT * FROM x, x x2 WHERE x.f1 = x2.f1 FOR NO KEY UPDATE OF x2",
			"FOR NO KEY UPDATE cannot be applied to a WITH query"},
	} {
		if _, err := runSQLCtxErr(t, ctx, c.sql); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.sql, err, c.want)
		}
	}

	for _, q := range []string{
		"WITH x AS (SELECT f1 FROM rm_t FOR UPDATE) SELECT * FROM x WHERE f1 = 1",
		"WITH x AS (SELECT * FROM (SELECT f1 FROM rm_t FOR UPDATE) ss) SELECT * FROM x WHERE f1 = 1",
	} {
		plan := renderRows(runSQL(t, ctx, "EXPLAIN (COSTS OFF) "+q))
		if countLinesContaining(plan, "CTE Scan on x") != 1 || countLinesContaining(plan, "CTE x") != 1 {
			t.Errorf("a locking body is never inlined (PG: CTE Scan on x over CTE x):\n%s", strings.Join(plan, "\n"))
		}
		if got := strings.Join(renderRows(runSQL(t, ctx, q)), ";"); got != "1" {
			t.Errorf("%s: rows %q, want 1", q, got)
		}
	}
}
