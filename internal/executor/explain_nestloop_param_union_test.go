package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// M0146-0106: a parameterised nested loop's Index Cond param over a UNION
// ALL subquery's column deparses as PG's get_parameter does — against the
// loop's outer plan, through the Append's first member to its kept
// "*SELECT* n" wrapper (TPC-DS Q71's `"*SELECT* 3".sold_item_sk`). The
// column's binding id names no relation, and goopg printed it bare.
//
// PG 18.3, enable_hashjoin = enable_mergejoin = off:
//
//	Nested Loop
//	  ->  Append
//	        ->  Subquery Scan on "*SELECT* 1"
//	              ->  Seq Scan on m106u1
//	                    Filter: (b > 0)
//	        ->  Subquery Scan on "*SELECT* 2" …
//	  ->  Index Scan using m106t_pkey on m106t t
//	        Index Cond: (k = "*SELECT* 1".a)
func TestNestLoopParamOverUnionAllMember(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE m106u1 (a int, b int)",
		"CREATE TABLE m106u2 (a int, b int)",
		"CREATE TABLE m106t (k int PRIMARY KEY, v int)",
		"INSERT INTO m106u1 SELECT g, g FROM generate_series(1, 20) g",
		"INSERT INTO m106u2 SELECT g, g FROM generate_series(1, 20) g",
		"INSERT INTO m106t SELECT g, g FROM generate_series(1, 5000) g",
		"ANALYZE m106u1", "ANALYZE m106u2", "ANALYZE m106t",
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableHashJoin, ps.EnableMergeJoin = false, false
	const q = "SELECT s.a, t.v FROM (SELECT a, b FROM m106u1 WHERE b > 0 UNION ALL SELECT a, b FROM m106u2 WHERE b > 0) s JOIN m106t t ON t.k = s.a"
	plan := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)), "\n")
	if !strings.Contains(plan, `Subquery Scan on "*SELECT* 1"`) || !strings.Contains(plan, "Index Scan using m106t_pkey") {
		t.Fatalf("want PG's nested loop over the kept member wrappers:\n%s", plan)
	}
	if !strings.Contains(plan, `Index Cond: (k = "*SELECT* 1".a)`) {
		t.Errorf("want the param deparsed through the first member's wrapper:\n%s", plan)
	}
}
