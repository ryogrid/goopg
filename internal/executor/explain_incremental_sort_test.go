package executor

import (
	"strings"
	"testing"
)

// TestExplainOrderByIncrementallySortsPresortedInput pins M0146-0005bp
// against PG 18.3: create_ordered_paths sorts an input already ordered on a
// leading prefix of the ORDER BY with an Incremental Sort (enable_incremental_sort
// defaults on). PG prints
//
//	Incremental Sort  (cost=158.73..235.94 rows=2260 width=8)
//	  Sort Key: t.a, t.b
//	  Presorted Key: t.a
//	  ->  Sort  (cost=158.51..164.16 rows=2260 width=8)
//	        Sort Key: t.a
//
// goopg always stacked a full Sort (TPC-DS Q3's ORDER BY d_year, sum DESC,
// brand_id over a GroupAggregate grouped by d_year first).
func TestExplainOrderByIncrementallySortsPresortedInput(t *testing.T) {
	lines := cteExplainLines(t, `SELECT * FROM (SELECT a, b FROM t ORDER BY a OFFSET 0) s ORDER BY a, b`)
	joined := strings.Join(lines, "\n")
	if !strings.HasPrefix(lines[0], "Incremental Sort") || !strings.Contains(joined, "Presorted Key: a") {
		t.Fatalf("want PG's Incremental Sort over the presorted input:\n%s", joined)
	}
	rows := formatRows(runQueryRows(t, explainIncrementalSortCtx(t), `SELECT * FROM (SELECT a, b FROM t ORDER BY a OFFSET 0) s ORDER BY a, b`))
	if strings.Join(rows, " ") != "1,1 1,2 2,0 2,5 3,3" {
		t.Fatalf("rows = %v", rows)
	}
}

func explainIncrementalSortCtx(t *testing.T) *Context {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, sql := range []string{
		"CREATE TABLE t (a int, b int)",
		"INSERT INTO t VALUES (2, 5), (1, 2), (3, 3), (2, 0), (1, 1)",
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	return ctx
}
