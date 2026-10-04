package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestCorrelatedScalarBodyRestrictsBaseRel pins M0146-0012 impl slice 1. PG
// makes a scalar sublink's outer reference a PARAM_EXEC Param with no relids,
// so `cs_s.k = cs_o.k` in a multi-relation body is a base restriction of
// cs_s (distribute_qual_to_rels) and an index key on cs_s_k
// (is_pseudo_constant_for_index). goopg kept every correlated conjunct of a
// multi-relation scope above the join, so the body scanned cs_s whole on
// every outer row. Expected values and shapes are PG 18.3's.
//
// The guard case puts the correlated restriction on a hash join's build side:
// each SubPlan rescan must rebuild the hash table from the restriction's new
// value (the M0146-0012 prerequisite), or rows k=2,3 repeat k=1's sum.
func TestCorrelatedScalarBodyRestrictsBaseRel(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE cs_o (k int, x int)",
		"CREATE TABLE cs_s (k int, dk int, v int)",
		"CREATE TABLE cs_d (dk int PRIMARY KEY, flag int)",
		"CREATE TABLE cs_g (g int, w int)",
		"INSERT INTO cs_o SELECT g, g % 13 FROM generate_series(1, 40) g",
		"INSERT INTO cs_s SELECT g % 200, g % 50, g % 17 FROM generate_series(1, 20000) g",
		"CREATE INDEX cs_s_k ON cs_s(k)",
		"INSERT INTO cs_d SELECT g, g % 3 FROM generate_series(0, 49) g",
		"INSERT INTO cs_g SELECT g % 50, g FROM generate_series(1, 5000) g",
		"ANALYZE cs_o",
		"ANALYZE cs_s",
		"ANALYZE cs_d",
		"ANALYZE cs_g",
	} {
		runSQL(t, ctx, q)
	}

	// A target-list sublink stays a SubPlan in both engines; the WHERE form
	// below is decorrelated by goopg's unnest pass and pins rows only.
	const probe = `SELECT cs_o.k, (SELECT avg(cs_s.v)::numeric(10,4) FROM cs_s, cs_d
		WHERE cs_s.k = cs_o.k AND cs_s.dk = cs_d.dk AND cs_d.flag = 1)
		FROM cs_o WHERE cs_o.k <= 4 ORDER BY 1`
	plan := renderRows(runSQL(t, ctx, "EXPLAIN (COSTS OFF) "+probe))
	if countLinesContaining(plan, "Index Cond: (k = cs_o.k)") != 1 ||
		countLinesContaining(plan, "Recheck Cond: (k = cs_o.k)") != 1 ||
		countLinesContaining(plan, "Filter: (k = cs_o.k)") != 0 {
		t.Errorf("want the correlated equality as cs_s_k's index key and the heap scan's recheck, not a Filter (PG 18.3):\n%s", strings.Join(plan, "\n"))
	}
	if got, want := strings.Join(renderRows(runSQL(t, ctx, probe)), ";"), "1|8.0200;2|NULL;3|NULL;4|7.9600"; got != want {
		t.Errorf("rows %q, want PG's %q", got, want)
	}
	const where = `SELECT count(*) FROM cs_o WHERE cs_o.x > (SELECT avg(cs_s.v) FROM cs_s, cs_d
		WHERE cs_s.k = cs_o.k AND cs_s.dk = cs_d.dk AND cs_d.flag = 1)`
	if got := strings.Join(renderRows(runSQL(t, ctx, where)), ";"); got != "4" {
		t.Errorf("rows %q, want PG's 4", got)
	}

	ps := optimizer.DefaultPlannerSettings()
	ps.EnableNestLoop = false
	ps.EnableMergeJoin = false
	const guard = `SELECT cs_o.k, (SELECT sum(cs_g.w) FROM cs_g, cs_d
		WHERE cs_g.g = cs_d.dk AND cs_d.flag = cs_o.k % 3) FROM cs_o WHERE cs_o.k <= 6 ORDER BY 1`
	plan = renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+guard, ps))
	joined := strings.Join(plan, "\n")
	if countLinesContaining(plan, "Hash Join") != 1 || countLinesContaining(plan, "Filter: (flag = (cs_o.k % 3))") != 1 {
		t.Errorf("want PG's Hash Join with the correlated restriction on cs_d under the Hash:\n%s", joined)
	}
	const want = "1|4250000;2|3999200;3|4253300;4|4250000;5|3999200;6|4253300"
	if got := strings.Join(renderRows(runSQLWith(t, ctx, guard, ps)), ";"); got != want {
		t.Errorf("rows %q, want PG's %q\nplan:\n%s", got, want, joined)
	}
}

// TestCorrelatedOneRelBodyProbesThroughSearch pins PG 18.3's plans for TPC-H
// Q17 (the correlation alone) and Q20 (correlation plus a constant) in
// miniature, on never-analyzed tables, through the real catalog. The search
// builds both probes itself (M0146-0015a), which is what let M0146-0012 slice
// 2 delete flattenStrandedSeqScanFilters. A lost probe would also lose the
// SubPlan: the unnest pass decorrelates a body that is not probe-cheap.
func TestCorrelatedOneRelBodyProbesThroughSearch(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE ori_outer (k int4, q int4)",
		"CREATE TABLE ori_inner (k int4, v int4)",
		"CREATE UNIQUE INDEX ori_inner_k ON ori_inner(k)",
	} {
		runSQL(t, ctx, q)
	}
	for _, c := range []struct {
		q    string
		want []string
	}{
		{`select q from ori_outer where q < (select avg(v) from ori_inner where k = ori_outer.k)`,
			[]string{"Filter: ((q)::numeric < (SubPlan 1))", "Index Scan using ori_inner_k on ori_inner", "Index Cond: (k = ori_outer.k)"}},
		{`select q from ori_outer where q < (select avg(v) from ori_inner where k = ori_outer.k and v > 3)`,
			[]string{"Filter: ((q)::numeric < (SubPlan 1))", "Index Scan using ori_inner_k on ori_inner", "Index Cond: (k = ori_outer.k)", "Filter: (v > 3)"}},
	} {
		plan := renderRows(runSQL(t, ctx, "EXPLAIN (COSTS OFF) "+c.q))
		for _, w := range c.want {
			if countLinesContaining(plan, w) != 1 {
				t.Errorf("%s: want %q (PG 18.3):\n%s", c.q, w, strings.Join(plan, "\n"))
			}
		}
	}
}
