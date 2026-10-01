package executor

import (
	"github.com/goopg/goopg/internal/optimizer"
	"strings"
	"testing"
)

func explainLine(t *testing.T, ctx *Context, sql, prefix string) string {
	t.Helper()
	for _, l := range runExplainRows(t, ctx, sql) {
		if s := strings.TrimSpace(l); strings.HasPrefix(s, prefix) {
			return s
		}
	}
	t.Fatalf("no %q line in EXPLAIN of %s", prefix, sql)
	return ""
}

// TestSortKeyChaseCrossesJoins pins M0146-0005bz against PG 18.3: an upper
// Sort key is an OUTER_VAR that PG deparses through the join producing it,
// down to the aggregate or relation that computes it. TPC-DS Q73's shape
// prints `Sort Key: (count(*)) DESC, customer.c_last_name`; goopg printed
// the output aliases `cnt DESC, c_last_name`. PG 18.3 on this fixture:
//
//	Sort Key: (count(*)) DESC, sk_c.ln
func TestSortKeyChaseCrossesJoins(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE sk_f (t int, c int, v int)")
	runSQL(t, ctx, "CREATE TABLE sk_c (c int PRIMARY KEY, ln text, fn text)")
	runSQL(t, ctx, "INSERT INTO sk_f SELECT i % 500, i % 100, i FROM generate_series(1,5000) i")
	runSQL(t, ctx, "INSERT INTO sk_c SELECT i, 'l' || i, 'f' || i FROM generate_series(0,99) i")
	runSQL(t, ctx, "ANALYZE sk_f")
	runSQL(t, ctx, "ANALYZE sk_c")
	got := explainLine(t, ctx, "EXPLAIN (COSTS OFF) SELECT ln, fn, cnt FROM (SELECT t, c, count(*) cnt FROM sk_f GROUP BY t, c) dj, sk_c "+
		"WHERE dj.c = sk_c.c AND cnt BETWEEN 1 AND 50 ORDER BY cnt DESC, ln", "Sort Key:")
	if got != "Sort Key: (count(*)) DESC, sk_c.ln" {
		t.Fatalf("got %q, want PG's %q", got, "Sort Key: (count(*)) DESC, sk_c.ln")
	}
}

// TestSortKeyChaseNamesTheEvaluatingLevel pins the naming half: past a join
// the chased expression belongs to a subquery whose relation ids can collide
// with the printed node's, so its columns render as named where they are
// evaluated. Regress join.sql, PG 18.3:
//
//	Group Key: t2.a, (COALESCE(t2.a))
//
// The chase first printed `coalesce(t1.a)` — the outer t1 shares t2's id.
// (goopg spells the function lower-case; that rendering is not pinned here.)
func TestSortKeyChaseNamesTheEvaluatingLevel(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE group_tbl (a int, b int)")
	runSQL(t, ctx, "INSERT INTO group_tbl SELECT 1, 1")
	runSQL(t, ctx, "ANALYZE group_tbl")
	got := explainLine(t, ctx, "EXPLAIN (COSTS OFF) SELECT 1 FROM group_tbl t1 LEFT JOIN (SELECT a c1, COALESCE(a) c2 FROM group_tbl t2) s ON TRUE "+
		"GROUP BY s.c1, s.c2", "Group Key:")
	if !strings.EqualFold(got, "Group Key: t2.a, (COALESCE(t2.a))") {
		t.Fatalf("got %q, want PG's %q", got, "Group Key: t2.a, (COALESCE(t2.a))")
	}
}

// TestSortKeyChaseKeepsTheKeysRelation pins the fail-closed rule: a key that
// names its relation (t2.b) is never re-labelled with another relation's
// same-named column found at its position (regress partition_join, PG 18.3
// `Sort Key: t1.a, t2.b, ((t3.a + t3.b))`).
func TestSortKeyChaseKeepsTheKeysRelation(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE prt1 (a int, b int, c varchar) PARTITION BY RANGE(a)",
		"CREATE TABLE prt1_p1 PARTITION OF prt1 FOR VALUES FROM (0) TO (250)",
		"CREATE TABLE prt1_p2 PARTITION OF prt1 FOR VALUES FROM (250) TO (600)",
		"INSERT INTO prt1 SELECT i, i % 25, to_char(i, 'FM0000') FROM generate_series(0, 599) i WHERE i % 2 = 0",
		"CREATE TABLE prt2 (a int, b int, c varchar) PARTITION BY RANGE(b)",
		"CREATE TABLE prt2_p1 PARTITION OF prt2 FOR VALUES FROM (0) TO (250)",
		"CREATE TABLE prt2_p2 PARTITION OF prt2 FOR VALUES FROM (250) TO (600)",
		"INSERT INTO prt2 SELECT i % 25, i, to_char(i, 'FM0000') FROM generate_series(0, 599) i WHERE i % 3 = 0",
		"CREATE TABLE prt1_e (a int, b int, c int) PARTITION BY RANGE(((a + b)/2))",
		"CREATE TABLE prt1_e_p1 PARTITION OF prt1_e FOR VALUES FROM (0) TO (250)",
		"CREATE TABLE prt1_e_p2 PARTITION OF prt1_e FOR VALUES FROM (250) TO (600)",
		"INSERT INTO prt1_e SELECT i, i, i % 25 FROM generate_series(0, 599, 2) i",
		"ANALYZE prt1", "ANALYZE prt2", "ANALYZE prt1_e",
	} {
		runSQL(t, ctx, q)
	}
	got := explainLine(t, ctx, "EXPLAIN (COSTS OFF) SELECT t1.a, t1.c, t2.b, t2.c, t3.a + t3.b, t3.c FROM (prt1 t1 LEFT JOIN prt2 t2 ON t1.a = t2.b) "+
		"LEFT JOIN prt1_e t3 ON (t1.a = (t3.a + t3.b)/2) WHERE t1.b = 0 ORDER BY t1.a, t2.b, t3.a + t3.b", "Sort Key:")
	if !strings.HasPrefix(got, "Sort Key: t1.a, t2.b, ") {
		t.Fatalf("got %q, want PG's key relations `t1.a, t2.b, …`", got)
	}
}

// TestSortKeyStopsAtKeptSubqueryScan pins M0146-0005ca against PG 18.3: a
// Subquery Scan carrying quals is non-trivial to setrefs.c, so PG keeps it
// and an upper key deparses to the scan's own column (TPC-DS Q53/Q63's
// tmp1, regress union's ss.x). On this fixture PG prints
//
//	Sort Key: tmp.av, tmp.s
//	->  Subquery Scan on tmp
//	      Filter: …
func TestSortKeyStopsAtKeptSubqueryScan(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE sqw (a int, b int, v int)")
	runSQL(t, ctx, "INSERT INTO sqw SELECT i % 20, i % 7, i FROM generate_series(1,5000) i")
	runSQL(t, ctx, "ANALYZE sqw")
	const q = "EXPLAIN (COSTS OFF) SELECT * FROM (SELECT a, b, sum(v) s, avg(sum(v)) OVER (PARTITION BY a) av FROM sqw GROUP BY a, b) tmp " +
		"WHERE CASE WHEN av > 0 THEN s / av ELSE NULL END > 0.1 ORDER BY av, s"
	plan := strings.Join(runExplainRows(t, ctx, q), "\n")
	if !strings.Contains(plan, "Subquery Scan on tmp") {
		t.Fatalf("fixture lost its Subquery Scan:\n%s", plan)
	}
	if got := explainLine(t, ctx, q, "Sort Key:"); got != "Sort Key: tmp.av, tmp.s" {
		t.Fatalf("got %q, want PG's %q", got, "Sort Key: tmp.av, tmp.s")
	}
}

// TestSortKeyDeparsesThroughFirstUnionArm pins M0146-0005cb against PG 18.3:
// set_deparse_plan makes an Append's first child the outer plan, so a key
// over a UNION ALL prints the leftmost arm's expression (TPC-DS Q5/Q77's
// `('store channel'::text)` where goopg printed the label `channel`). PG on
// this fixture (goopg omits the `::text` literal cast, which is not pinned):
//
//	Group Key: ('a chan'::text), ua.id
//	Sort Key: ('a chan'::text), ua.id
//	Sort Key: ua.v, ua.id
func TestSortKeyDeparsesThroughFirstUnionArm(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{"CREATE TABLE ua (id int, v int)", "CREATE TABLE ub (id int, v int)",
		"INSERT INTO ua SELECT i, i FROM generate_series(1,100) i", "INSERT INTO ub SELECT i, i FROM generate_series(1,100) i",
		"ANALYZE ua", "ANALYZE ub"} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	ps.EnableHashAgg = false
	lines := func(q string) string {
		var out []string
		for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)) {
			if len(r) > 0 && r[0].Kind == KindString {
				out = append(out, strings.TrimSpace(r[0].StringValue()))
			}
		}
		return strings.Join(out, "\n")
	}
	grouped := lines("SELECT channel, id, sum(v) FROM (SELECT 'a chan' AS channel, id, v FROM ua UNION ALL SELECT 'b chan', id, v FROM ub) x GROUP BY channel, id ORDER BY channel, id")
	for _, want := range []string{"Group Key: ('a chan'", "Sort Key: ('a chan'"} {
		if !strings.Contains(grouped, want) || !strings.Contains(grouped, "), ua.id") {
			t.Fatalf("want %q … ua.id (PG's first-arm deparse):\n%s", want, grouped)
		}
	}
	if plain := lines("SELECT * FROM (SELECT id, v FROM ua UNION ALL SELECT id, v FROM ub) x ORDER BY v, id"); !strings.Contains(plain, "Sort Key: ua.v, ua.id") {
		t.Fatalf("want PG's `Sort Key: ua.v, ua.id`:\n%s", plain)
	}
}

// TestGroupKeyDeparsesThroughInlinedCTE pins M0146-0005cc against PG 18.3:
// a single-reference CTE is inlined (no CTE Scan in PG's plan), so an upper
// key over a UNION of such CTEs deparses into the first CTE body — TPC-DS
// Q33/Q60's `Group Key: item.i_manufact_id` where goopg printed
// `ss.i_manufact_id`. PG on this fixture prints the outer
//
//	GroupAggregate
//	  Group Key: it.m
func TestGroupKeyDeparsesThroughInlinedCTE(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{"CREATE TABLE it (i int, m int)", "CREATE TABLE sa (i int, v int)", "CREATE TABLE ca2 (i int, v int)",
		"INSERT INTO it SELECT g, g % 10 FROM generate_series(1,200) g",
		"INSERT INTO sa SELECT g % 200 + 1, g FROM generate_series(1,2000) g",
		"INSERT INTO ca2 SELECT g % 200 + 1, g FROM generate_series(1,2000) g",
		"ANALYZE it", "ANALYZE sa", "ANALYZE ca2"} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	ps.EnableHashAgg = false
	var first string
	for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN (COSTS OFF) WITH ss AS (SELECT it.m, sum(sa.v) tot FROM sa, it WHERE sa.i = it.i GROUP BY it.m), "+
		"cs AS (SELECT it.m, sum(ca2.v) tot FROM ca2, it WHERE ca2.i = it.i GROUP BY it.m) "+
		"SELECT m, sum(tot) FROM (SELECT * FROM ss UNION ALL SELECT * FROM cs) tmp1 GROUP BY m ORDER BY m", ps)) {
		if len(r) > 0 && r[0].Kind == KindString {
			if s := strings.TrimSpace(r[0].StringValue()); first == "" && strings.HasPrefix(s, "Group Key:") {
				first = s
			}
		}
	}
	if first != "Group Key: it.m" {
		t.Fatalf("outer Group Key = %q, want PG's %q", first, "Group Key: it.m")
	}
}

// TestSortKeyOverGroupingSetsDeparsesGroupExprs pins M0146-0005cd against PG
// 18.3: a grouping-set aggregate keeps the group-prefix output layout, so a
// Sort key above a MixedAggregate deparses to the grouping expression like
// any other group key (TPC-DS Q5/Q77, where goopg printed `channel, id`). PG
// on this fixture (goopg omits the literals' `::text` casts, not pinned):
//
//	Sort Key: ('a chan'::text), (('x'::text || (ra.id)::text))
//	->  MixedAggregate
//	      Hash Key: ('a chan'::text), (('x'::text || (ra.id)::text))
func TestSortKeyOverGroupingSetsDeparsesGroupExprs(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{"CREATE TABLE ra (id int, v int)", "CREATE TABLE rb (id int, v int)",
		"INSERT INTO ra SELECT i, i FROM generate_series(1,100) i", "INSERT INTO rb SELECT i, i FROM generate_series(1,100) i",
		"ANALYZE ra", "ANALYZE rb"} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	var sortKey string
	for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN (COSTS OFF) SELECT channel, id, sum(v) FROM (SELECT 'a chan' AS channel, 'x' || id AS id, v FROM ra "+
		"UNION ALL SELECT 'b chan', 'y' || id, v FROM rb) x GROUP BY ROLLUP (channel, id) ORDER BY channel, id", ps)) {
		if len(r) > 0 && r[0].Kind == KindString {
			if s := strings.TrimSpace(r[0].StringValue()); sortKey == "" && strings.HasPrefix(s, "Sort Key:") {
				sortKey = s
			}
		}
	}
	if !strings.HasPrefix(sortKey, "Sort Key: ('a chan'") || !strings.Contains(sortKey, "ra.id") {
		t.Fatalf("got %q, want PG's grouping-expression deparse `('a chan'…), (('x' || ra.id))`", sortKey)
	}
}

// TestSortKeyOverCrossJoinOfAggregates pins M0146-0005ce against PG 18.3: a
// key naming a scalar aggregate subquery's output reaches the aggregate
// through the cross join even though the join's output column carries no
// relation (it is an aggregate result) — TPC-DS Q61. PG on this fixture:
//
//	Sort Key: (sum(sp.v)), (sum(sp_1.v))
//
// goopg does not yet suffix the second reference `_1` (ledgered); both
// aggregate expansions are what this pins.
func TestSortKeyOverCrossJoinOfAggregates(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE sp (p int, v int)")
	runSQL(t, ctx, "INSERT INTO sp SELECT i % 3, i FROM generate_series(1,300) i")
	runSQL(t, ctx, "ANALYZE sp")
	got := explainLine(t, ctx, "EXPLAIN (COSTS OFF) SELECT promotions, total FROM (SELECT sum(v) promotions FROM sp WHERE p = 1) a, "+
		"(SELECT sum(v) total FROM sp) b ORDER BY promotions, total", "Sort Key:")
	if !strings.HasPrefix(got, "Sort Key: (sum(sp.v)), (sum(sp") {
		t.Fatalf("got %q, want PG's `(sum(sp.v)), (sum(sp_1.v))`", got)
	}
}
