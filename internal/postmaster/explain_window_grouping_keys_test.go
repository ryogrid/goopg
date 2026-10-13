package postmaster

import (
	"strings"
	"testing"
)

// TestExplainWindowKeysOverGroupingSetsAggregate pins the M0146-0042 slice for
// TPC-DS Q36/Q86 against PG 18.3 (lines from a scratch PG on this fixture). A
// WindowAgg over a grouping-sets aggregate partitions and orders by computed
// expressions of the aggregate's outputs; PG deparses each output column
// through the Agg's target list (get_variable on the OUTER_VAR), so the
// Window clause and the Sort below it print GROUPING(...) and sum(...) calls,
// not the output names `grouping` / `sum`.
func TestExplainWindowKeysOverGroupingSetsAggregate(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, q := range []string{
		"create table it (i_item_sk int primary key, i_category char(50), i_class char(50))",
		"create table ss (ss_item_sk int, ss_net_profit numeric(7,2), ss_ext_sales_price numeric(7,2))",
		"insert into it select g, 'cat' || (g % 5), 'cls' || (g % 7) from generate_series(1, 1000) g",
		"insert into ss select (g % 1000) + 1, g % 50, g % 70 + 1 from generate_series(1, 20000) g",
		"analyze it",
		"analyze ss",
		"set max_parallel_workers_per_gather = 0",
	} {
		if _, e := allRowsQuery(t, conn, q); e != "" {
			t.Fatalf("%s: %s", q, e)
		}
	}
	got, e := allRowsQuery(t, conn, `EXPLAIN (COSTS OFF) select * from (
 select sum(ss_net_profit)/sum(ss_ext_sales_price) as gross_margin, i_category, i_class,
        grouping(i_category)+grouping(i_class) as lochierarchy,
        rank() over (partition by grouping(i_category)+grouping(i_class),
                     case when grouping(i_class) = 0 then i_category end
                     order by sum(ss_net_profit)/sum(ss_ext_sales_price) asc) as rank_within_parent
 from ss, it where i_item_sk = ss_item_sk
 group by rollup(i_category, i_class)) sub
order by lochierarchy desc, case when lochierarchy = 0 then i_category end, rank_within_parent limit 100`)
	plan := strings.Join(got, "\n")
	if e != "" {
		t.Fatalf("explain: %s", e)
	}
	for _, want := range []string{
		"Window: w1 AS (PARTITION BY ((GROUPING(it.i_category) + GROUPING(it.i_class))), (CASE WHEN (GROUPING(it.i_class) = 0) THEN it.i_category ELSE NULL::bpchar END) ORDER BY ((sum(ss.ss_net_profit) / sum(ss.ss_ext_sales_price))) ROWS UNBOUNDED PRECEDING)",
		"Sort Key: ((GROUPING(it.i_category) + GROUPING(it.i_class))), (CASE WHEN (GROUPING(it.i_class) = 0) THEN it.i_category ELSE NULL::bpchar END), ((sum(ss.ss_net_profit) / sum(ss.ss_ext_sales_price)))",
	} {
		if !strings.Contains(plan, want) {
			t.Errorf("missing PG's line %s:\n%s", want, plan)
		}
	}
}
