package postmaster

import (
	"strings"
	"testing"
)

// TestExplainRollupKeyThroughHavingInitPlan pins the M0146-0042 slice for
// TPC-DS Q14 against PG 18.3 (lines from a scratch PG on this fixture). A
// ROLLUP over a UNION ALL whose first member has a HAVING that compares
// against an InitPlan: the Sort Key above the rollup deparses through the
// first member's Filter to its group key, `it.i_brand`. A sublink in the
// HAVING walls off an aggregate result (TPC-DS Q44), not a plain group key,
// which the Agg's target list keeps as a Var.
func TestExplainRollupKeyThroughHavingInitPlan(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, q := range []string{
		"create table it (i_sk int primary key, i_brand int, i_class int)",
		"create table s1 (s_item int, s_q int)",
		"create table s2 (w_item int, w_q int)",
		"insert into it select g, g % 7, g % 5 from generate_series(1, 500) g",
		"insert into s1 select g % 500 + 1, g % 10 from generate_series(1, 10000) g",
		"insert into s2 select g % 500 + 1, g % 10 from generate_series(1, 10000) g",
		"analyze it",
		"analyze s1",
		"analyze s2",
		"set enable_hashagg = off",
		"set max_parallel_workers_per_gather = 0",
	} {
		if _, e := allRowsQuery(t, conn, q); e != "" {
			t.Fatalf("%s: %s", q, e)
		}
	}
	got, e := allRowsQuery(t, conn, `EXPLAIN (COSTS OFF) select channel, i_brand, i_class, sum(q) from ( select 'store' channel, i_brand, i_class, sum(s_q) q from s1, it where s_item = i_sk group by i_brand, i_class having sum(s_q) > (select avg(s_q) from s1) union all select 'web' channel, i_brand, i_class, sum(w_q) q from s2, it where w_item = i_sk group by i_brand, i_class) y group by rollup(channel, i_brand, i_class) order by channel, i_brand, i_class limit 10`)
	plan := strings.Join(got, "\n")
	if e != "" || !strings.Contains(plan, "InitPlan") {
		t.Fatalf("fixture did not plan the HAVING InitPlan (err %q):\n%s", e, plan)
	}
	want := "Sort Key: ('store'::text), it.i_brand, it.i_class"
	if strings.Count(plan, want) != 2 {
		t.Errorf("want PG's %q on both Sort lines:\n%s", want, plan)
	}
}
