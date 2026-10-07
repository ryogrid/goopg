package executor

import (
	"strings"
	"testing"
)

// TestExplainJoinCondsThroughGroupedSubqueries pins the M0146-0042 slice for
// TPC-DS Q65's shape: join conditions over GroupAggregate'd derived tables
// deparse through the aggregate that produced each column, as PG's
// resolve_special_varno does. A group key prints its grouped source column,
// an aggregate result prints the call, parenthesised as get_variable prints
// a non-Var target, and an aggregate over another aggregate's result nests
// (`avg((sum(ss.price)))`). goopg printed `store`, `revenue` and `ave`.
// Expected lines are PG 18.3's for the same schema and query with
// enable_hashagg off.
func TestExplainJoinCondsThroughGroupedSubqueries(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, d := range []string{
		"CREATE TABLE ss (store int, item int, price numeric, d int)",
		"CREATE TABLE st (s_sk int, s_name text)",
	} {
		if err := runDDL(t, ctx, d); err != nil {
			t.Fatalf("%s: %v", d, err)
		}
	}
	defer hashAggSeed(false)()
	const q = "EXPLAIN (COSTS OFF) SELECT s_name, sc.revenue FROM st, " +
		"(SELECT store, avg(revenue) AS ave FROM (SELECT store, item, sum(price) AS revenue FROM ss GROUP BY store, item) sa GROUP BY store) sb, " +
		"(SELECT store, item, sum(price) AS revenue FROM ss GROUP BY store, item) sc " +
		"WHERE sb.store = sc.store AND sc.revenue <= 0.1 * sb.ave AND s_sk = sc.store"
	plan := strings.Join(runExplainRows(t, ctx, q), "\n")
	for _, want := range []string{
		"Hash Cond: (st.s_sk = ss.store)",
		"Merge Cond: (ss.store = ss_1.store)",
		"Join Filter: ((sum(ss_1.price)) <= (0.1 * (avg((sum(ss.price))))))",
	} {
		if !strings.Contains(plan, want) {
			t.Errorf("missing PG's %q in:\n%s", want, plan)
		}
	}
}

// TestExplainSortKeyAggregateOverUnionAllMembers pins the TPC-DS Q56 shape:
// an outer aggregate over a UNION ALL of two inlined, grouped CTEs. PG
// deparses the Sort Key's `sum(total)` through the Append's first branch to
// that member's own aggregate, nested and parenthesised:
// `(sum((sum(s1.v))))`. goopg printed the CTE's label column,
// `(sum(a.total))`. Expected text is PG 18.3's for the same schema and query.
func TestExplainSortKeyAggregateOverUnionAllMembers(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, d := range []string{
		"CREATE TABLE s1 (id text, v numeric)",
		"CREATE TABLE s2 (id text, v numeric)",
	} {
		if err := runDDL(t, ctx, d); err != nil {
			t.Fatalf("%s: %v", d, err)
		}
	}
	const q = "EXPLAIN (COSTS OFF) WITH a AS (SELECT id, sum(v) total FROM s1 GROUP BY id), " +
		"b AS (SELECT id, sum(v) total FROM s2 GROUP BY id) " +
		"SELECT id, sum(total) t FROM (SELECT * FROM a UNION ALL SELECT * FROM b) x GROUP BY id ORDER BY t, id"
	plan := strings.Join(runExplainRows(t, ctx, q), "\n")
	if want := "Sort Key: (sum((sum(s1.v)))), s1.id"; !strings.Contains(plan, want) {
		t.Errorf("missing PG's %q in:\n%s", want, plan)
	}
}
