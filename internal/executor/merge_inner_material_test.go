package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestMergeJoinInnerJoinMaterialized pins M0146-0005dv against PG 18.3: a merge
// join whose inner is itself a merge join ordered by another member of the
// merge key's equivalence class needs no Sort — PG's pathkeys are the class,
// so `vl ⋈ v0` ordered by vl.k satisfies a merge on v0.k — and, since a merge
// join cannot mark/restore, final_cost_mergejoin puts a Material on it
// (TPC-DS Q47 / Q57). goopg compared pathkeys by expression, priced an
// explicit Sort over the inner join and started the outer merge at the inner's
// total.
//
// PG 18.3, enable_hashjoin = enable_nestloop = off:
//
//	Merge Join  (cost=0.00..337.90 rows=1 width=0)
//	  Merge Cond: (vd.k = v0.k)
//	  ->  CTE Scan on v vd
//	  ->  Materialize  (cost=0.00..225.20 rows=1 width=24)
//	        ->  Merge Join  (cost=0.00..225.20 rows=1 width=24)
//	              Merge Cond: (vl.k = v0.k)
func TestMergeJoinInnerJoinMaterialized(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE mji (k int, v int)",
		"INSERT INTO mji SELECT g % 500, g FROM generate_series(1, 5000) g",
		"ANALYZE mji",
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableHashJoin, ps.EnableNestLoop = false, false
	const q = "WITH v AS MATERIALIZED (SELECT k, v, rank() OVER (PARTITION BY k ORDER BY v) rn FROM mji ORDER BY k) " +
		"SELECT count(*) FROM v v0, v vl, v vd WHERE v0.k = vl.k AND v0.rn = vl.rn + 1 AND v0.k = vd.k AND v0.rn = vd.rn - 1 AND v0.v = 77"
	lines := renderRows(runSQLWith(t, ctx, "EXPLAIN "+q, ps))
	plan := strings.Join(lines, "\n")
	top, mat := -1, -1
	for i, l := range lines {
		if top < 0 && strings.Contains(l, "Merge Join") {
			top = i
		}
		if top >= 0 && mat < 0 && strings.Contains(l, "Materialize") {
			mat = i
		}
	}
	if top < 0 || !strings.Contains(lines[top], "(cost=0.00..") {
		t.Fatalf("want PG's top Merge Join starting at 0 (cost=0.00..337.90):\n%s", plan)
	}
	if mat < 0 || mat+1 >= len(lines) || !strings.Contains(lines[mat+1], "Merge Join") {
		t.Fatalf("want a Materialize over the inner Merge Join, as PG:\n%s", plan)
	}
	if got := strings.Join(renderRows(runSQLWith(t, ctx, q, ps)), ";"); got != strings.Join(renderRows(runSQL(t, ctx, q)), ";") {
		t.Errorf("merge plan count %q differs from the default plan's", got)
	}
}
