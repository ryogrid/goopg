package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestNestLoopParamNamesItsOwnBranch pins M0146-0042 against PG 18.3: a
// parameterised index scan's outer reference is the loop's NestLoop param,
// which get_parameter deparses against the loop's outer plan. In the second
// UNION ALL branch that plan holds two `it` scans exposing `ik` (the
// branch's own and the IN subquery's pulled-up one), so the name alone is
// ambiguous; the binding id picks the branch's own `it_2`, where goopg
// printed the first branch's `it` (TPC-DS Q56). The want lines are PG's.
func TestNestLoopParamNamesItsOwnBranch(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"create table it (ik int primary key, iid text, c text)",
		"create table s1 (k int primary key, ik int, v int)",
		"create table s2 (k int primary key, ik int, v int)",
		"create index on s1 (ik)", "create index on s2 (ik)",
		"insert into it select g, 'i'||(g%500), case when g%50=0 then 'x' else 'y' end from generate_series(1,5000) g",
		"insert into s1 select g, g%5000+1, g from generate_series(1,50000) g",
		"insert into s2 select g, g%5000+1, g from generate_series(1,50000) g",
		"analyze it", "analyze s1", "analyze s2",
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	ps.EnableHashJoin = false
	ps.EnableMergeJoin = false
	q := "explain (costs off) select id, sum(v) t from (select it.iid id, sum(s1.v) v from s1, it " +
		"where s1.ik = it.ik and it.iid in (select iid from it where c = 'x') group by it.iid union all " +
		"select it.iid, sum(s2.v) from s2, it where s2.ik = it.ik and it.iid in (select iid from it where c = 'x') " +
		"group by it.iid) u group by id"
	var lines []string
	for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps)) {
		lines = append(lines, strings.TrimSpace(r[0].StringValue()))
	}
	got := strings.Join(lines, "\n")
	for _, w := range []string{"Index Cond: (ik = it.ik)", "Index Cond: (ik = it_2.ik)"} {
		if !strings.Contains(got, w) {
			t.Errorf("want PG's %q in:\n%s", w, got)
		}
	}
}
