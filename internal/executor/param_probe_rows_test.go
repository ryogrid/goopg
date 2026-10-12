package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestParameterizedProbeRowsApplyMovableJoinClauses (M0146-0009p): a
// parameterised index probe returns the rows get_parameterized_baserel_size
// counts — every join clause movable into the path, not only the equalities
// the index binds. `i.qty < c.qty` binds no key but filters each probe (PG's
// default 1/3 for a comparison of two variables). PG 18.3 on the same data,
// hash and merge joins off:
//
//	Index Scan using pr_inv_pkey on pr_inv i  (cost=0.29..5.95 rows=10)
//	  Index Cond: (item = c.item)
//	  Filter: (qty < c.qty)
//
// goopg reported rows=30 (the 30 rows per item the equality alone gives).
func TestParameterizedProbeRowsApplyMovableJoinClauses(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE pr_inv (d int, item int, qty int, PRIMARY KEY (item, d))",
		"CREATE TABLE pr_cs (id int, item int, qty int, flag int)",
		"INSERT INTO pr_inv SELECT d, item, (d * 7 + item) % 100 FROM generate_series(1, 30) d, generate_series(1, 2000) item",
		"INSERT INTO pr_cs SELECT g, (g * 13) % 2000 + 1, g % 100, g % 50 FROM generate_series(1, 20000) g",
		"ANALYZE pr_inv", "ANALYZE pr_cs",
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableHashJoin, ps.EnableMergeJoin = false, false
	const q = "SELECT count(*) FROM pr_cs c JOIN pr_inv i ON i.item = c.item AND i.qty < c.qty WHERE c.flag = 7"
	lines := renderRows(runSQLWith(t, ctx, "EXPLAIN "+q, ps))
	probe := ""
	for _, l := range lines {
		if strings.Contains(l, "Index Scan using pr_inv_pkey") {
			probe = l
		}
	}
	if !strings.Contains(probe, " rows=10 ") {
		t.Fatalf("want PG's probe rows=10, got %q\nplan:\n%s", strings.TrimSpace(probe), strings.Join(lines, "\n"))
	}
	if got, want := strings.Join(renderRows(runSQLWith(t, ctx, q, ps)), ";"), strings.Join(renderRows(runSQL(t, ctx, q)), ";"); got != want {
		t.Fatalf("forced probe plan returned %s, want %s", got, want)
	}
}
