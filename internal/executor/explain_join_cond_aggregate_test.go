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

// TestExplainUnionDedupeComputedKeyParens pins the TPC-DS Q75 shape: a
// UNION's dedupe groups on the Append's output, whose target entries are
// Vars of the first branch. show_agg_keys deparses that Var and get_variable
// wraps the branch's computed target in parentheses of its own, so PG 18.3
// prints `((u1.q - COALESCE(u1.r, 0)))`. A DISTINCT over a plain scan reads
// the expression from its input's own target list and keeps one pair.
func TestExplainUnionDedupeComputedKeyParens(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, d := range []string{
		"CREATE TABLE u1 (a int, q int, r int)",
		"CREATE TABLE u2 (a int, q int, r int)",
	} {
		if err := runDDL(t, ctx, d); err != nil {
			t.Fatalf("%s: %v", d, err)
		}
	}
	for _, tc := range []struct{ sql, want string }{
		{"EXPLAIN (COSTS OFF) SELECT a, sum(x) FROM (SELECT a, q - COALESCE(r, 0) AS x FROM u1 UNION SELECT a, q - COALESCE(r, 0) AS x FROM u2) s GROUP BY a",
			"Group Key: u1.a, ((u1.q - COALESCE(u1.r, 0)))"},
		{"EXPLAIN (COSTS OFF) SELECT DISTINCT a, q - COALESCE(r, 0) FROM u1",
			"Group Key: a, (q - COALESCE(r, 0))"},
	} {
		plan := strings.Join(runExplainRows(t, ctx, tc.sql), "\n")
		if !strings.Contains(plan, tc.want) {
			t.Errorf("missing PG's %q in:\n%s", tc.want, plan)
		}
	}
}

// TestExplainKeyThroughPrunedGroupColumn pins the TPC-DS Q66 shape: a
// grouped UNION ALL member whose `d_year` key is pinned by `d_year = 2001`
// and pruned from its group keys still publishes d_year — goopg as a
// Passthrough column, PG as a plain Var in the Agg's target list. PG 18.3
// deparses the outer key through it to `f1.d_year`; goopg printed the
// subquery alias `yr`.
func TestExplainKeyThroughPrunedGroupColumn(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, d := range []string{
		"CREATE TABLE f1 (w text, d_year int, v int)",
		"CREATE TABLE f2 (w text, d_year int, v int)",
	} {
		if err := runDDL(t, ctx, d); err != nil {
			t.Fatalf("%s: %v", d, err)
		}
	}
	defer hashAggSeed(false)()
	const q = "EXPLAIN (COSTS OFF) SELECT w, yr, sum(s) FROM (" +
		"SELECT w, d_year AS yr, sum(v) s FROM f1 WHERE d_year = 2001 GROUP BY w, d_year UNION ALL " +
		"SELECT w, d_year AS yr, sum(v) s FROM f2 WHERE d_year = 2001 GROUP BY w, d_year) x GROUP BY w, yr"
	plan := strings.Join(runExplainRows(t, ctx, q), "\n")
	for _, want := range []string{"Group Key: f1.w, f1.d_year", "Sort Key: f1.w, f1.d_year"} {
		if !strings.Contains(plan, want) {
			t.Errorf("missing PG's %q in:\n%s", want, plan)
		}
	}
}

// TestExplainAggregateArgThroughUnionMemberWrapper pins the TPC-DS Q71
// shape: an aggregate over a join with a UNION ALL whose join members keep
// their "*SELECT* n" Subquery Scans. PG 18.3 deparses the aggregate's
// argument through the Append's first branch and stops at that kept scan:
// `Sort Key: (sum("*SELECT* 1".v)) DESC, it.n`. goopg printed `sum(v)`: the
// key chase stepped through the wrapper, and the join's relation-id check
// compared ids from two different numberings.
func TestExplainAggregateArgThroughUnionMemberWrapper(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, d := range []string{
		"CREATE TABLE m1 (a int, v int, x int)",
		"CREATE TABLE m2 (a int, v int, x int)",
		"CREATE TABLE d (k int, y int)",
		"CREATE TABLE it (k int, n int)",
	} {
		if err := runDDL(t, ctx, d); err != nil {
			t.Fatalf("%s: %v", d, err)
		}
	}
	const q = "EXPLAIN (COSTS OFF) SELECT it.n, sum(v) AS s FROM it, (" +
		"SELECT m1.a AS b, m1.v FROM m1, d WHERE m1.x = d.k AND d.y = 2002 UNION ALL " +
		"SELECT m2.a, m2.v FROM m2, d WHERE m2.x = d.k AND d.y = 2002) u " +
		"WHERE u.b = it.k GROUP BY it.n ORDER BY s DESC, it.n"
	plan := strings.Join(runExplainRows(t, ctx, q), "\n")
	if !strings.Contains(plan, `Subquery Scan on "*SELECT* 1"`) {
		t.Fatalf("the shape under test is gone (want the member wrappers kept):\n%s", plan)
	}
	if want := `Sort Key: (sum("*SELECT* 1".v)) DESC, it.n`; !strings.Contains(plan, want) {
		t.Errorf("missing PG's %q in:\n%s", want, plan)
	}
}

// TestExplainComputedGroupKeyThroughKeptSubqueryScan pins the TPC-DS Q54
// shape: an outer GROUP BY on a computed expression over a grouped derived
// table that keeps its Subquery Scan. The Sort above the outer aggregate
// names the computed group key, whose columns index the aggregate's input;
// PG 18.3 deparses them to the kept scan's column:
// `Sort Key: (((my_revenue.rev / '50'::numeric))::integer), (count(*))`.
// goopg printed the column bare (`rev`). (PG elects an Incremental Sort
// there; the test pins only the key text.)
func TestExplainComputedGroupKeyThroughKeptSubqueryScan(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, d := range []string{
		"CREATE TABLE rv (c int, v numeric)",
		"CREATE TABLE rw (c int, k int)",
	} {
		if err := runDDL(t, ctx, d); err != nil {
			t.Fatalf("%s: %v", d, err)
		}
	}
	defer hashAggSeed(false)()
	const q = "EXPLAIN (COSTS OFF) SELECT (rev/50)::int AS seg, count(*) FROM (" +
		"SELECT rv.c, sum(v) AS rev FROM rv, rw WHERE rv.c = rw.c GROUP BY rv.c) my_revenue " +
		"GROUP BY seg ORDER BY seg, count(*)"
	plan := strings.Join(runExplainRows(t, ctx, q), "\n")
	for _, want := range []string{
		"Sort Key: (((my_revenue.rev / '50'::numeric))::integer), (count(*))",
		"Group Key: (((my_revenue.rev / '50'::numeric))::integer)",
	} {
		if !strings.Contains(plan, want) {
			t.Errorf("missing PG's %q in:\n%s", want, plan)
		}
	}
}
