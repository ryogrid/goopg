package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestSubPlanPrintsAfterChildren pins M0146-0042 against PG 18.3:
// ExplainNode prints a node's initPlan list before its children and its
// subPlan list after them (explain.c: initPlan, lefttree, righttree, special
// children, subPlan). A SubPlan in a join's Join Filter therefore follows
// the join's inputs (TPC-DS Q45). The want plan is PG's.
func TestSubPlanPrintsAfterChildren(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"create table ja (k int, z text)", "create table jb (k int, v int)", "create table jc (v int, w int)",
		"insert into ja select g, case when g%2=0 then 'a' else 'b' end from generate_series(1,1000) g",
		"insert into jb select g, g%100 from generate_series(1,1000) g",
		"insert into jc select g, g%10 from generate_series(1,100) g",
		"analyze ja", "analyze jb", "analyze jc",
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	q := "explain (costs off) select count(*) from ja, jb where ja.k = jb.k and (ja.z = 'a' or jb.v in (select v from jc where w < 5))"
	var lines []string
	for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps)) {
		lines = append(lines, r[0].StringValue())
	}
	got := strings.Join(lines, "\n")
	want := strings.Join([]string{
		"Aggregate",
		"  ->  Hash Join",
		"        Hash Cond: (ja.k = jb.k)",
		"        Join Filter: ((ja.z = 'a'::text) OR (ANY (jb.v = (hashed SubPlan 1).col1)))",
		"        ->  Seq Scan on ja",
		"        ->  Hash",
		"              ->  Seq Scan on jb",
		"        SubPlan 1",
		"          ->  Seq Scan on jc",
		"                Filter: (w < 5)",
	}, "\n")
	if got != want {
		t.Errorf("got:\n%s\nwant PG's:\n%s", got, want)
	}
}
