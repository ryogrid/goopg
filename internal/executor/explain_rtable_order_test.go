package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestRelationSuffixesFollowFlatRtableOrder pins M0146-0005df against PG
// 18.3: set_rtable_names hands out `_N` suffixes in glob->finalrtable order,
// where the top query's own range table comes first and a CTE body — a
// subplan, flattened only after the top plan — comes after it. So the main
// query's `zrt` keeps the bare name and the CTE body's scan is `zrt_1`,
// although the CTE body is planned (and its relation allocated) first.
// Every want line is PG's output.
func TestRelationSuffixesFollowFlatRtableOrder(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE zrt (a int, b int)")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	q := "WITH c AS (SELECT a, count(*) n FROM zrt GROUP BY a) SELECT * FROM c, c c2, zrt WHERE c.a = c2.a AND zrt.a = c.a"
	var lines []string
	for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)) {
		if len(r) > 0 && r[0].Kind == KindString {
			lines = append(lines, strings.TrimSpace(r[0].StringValue()))
		}
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{
		"Group Key: zrt_1.a",
		"->  Seq Scan on zrt zrt_1",
		"->  Seq Scan on zrt",
		"Hash Cond: (zrt.a = c.a)",
	} {
		found := false
		for _, l := range lines {
			if l == want {
				found = true
			}
		}
		if !found {
			t.Errorf("want line %q in:\n%s", want, got)
		}
	}
}

// TestSetOpBranchSuffixesFollowPlanWalk pins M0146-0042 against PG 18.3: a
// genuine set operation (here an INTERSECT) is a query whose range table
// holds one subquery RTE per branch, and each branch's relations enter the
// flat range table as setrefs.c reaches that branch — so the first branch's
// nested grouped subquery takes `st_1` before the second branch's `st_2`.
// goopg had numbered the second branch with the outer level. The want plan
// is PG's.
func TestSetOpBranchSuffixesFollowPlanWalk(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "create table st (sk int primary key, city varchar(60))")
	runSQL(t, ctx, "insert into st select g, 'c' || g from generate_series(0,9) g")
	runSQL(t, ctx, "analyze st")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	q := "explain (costs off) select st.sk from st, (select z from (select city z, count(*) c from st where sk < 5 " +
		"group by city having count(*) > 0) a1 intersect select city from st where sk > 2) v1 " +
		"where substr(st.city,1,2) < substr(v1.z,1,2)"
	var lines []string
	for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps)) {
		lines = append(lines, strings.TrimSpace(r[0].StringValue()))
	}
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "Group Key: st_1.city") {
		t.Errorf("want PG's %q in:\n%s", "Group Key: st_1.city", got)
	}
	// The second branch's scan carries `st_2` whichever scan method it uses.
	if !strings.Contains(got, "on st st_2\n") {
		t.Errorf("second INTERSECT branch should be st_2 (PG):\n%s", got)
	}
}
