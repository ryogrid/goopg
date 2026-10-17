package postmaster

import (
	"strings"
	"testing"
)

// TestExplainKeyThroughSplitAggregatePassthrough pins the M0146-0042 slice for
// TPC-DS Q66 against PG 18.3 (lines from a scratch PG on this fixture). Each
// UNION ALL member groups by d_year under a WHERE that pins it to a constant,
// so the planner prunes it from the group key and carries it as a passthrough
// column; the members are parallel (Finalize over Partial). A key above the
// Append deparses through the first member's target list to the Var PG keeps
// there, `dd.d_year`, not the subquery output name `year`.
func TestExplainKeyThroughSplitAggregatePassthrough(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, q := range []string{
		"create table wh (w_sk int primary key, w_name text, w_city text)",
		"create table dd (d_sk int primary key, d_year int)",
		"create table ws (ws_wh int, ws_dt int, ws_amt numeric(7,2))",
		"create table cs (cs_wh int, cs_dt int, cs_amt numeric(7,2))",
		"insert into wh select g, 'w' || g, 'c' || (g % 3) from generate_series(1, 10) g",
		"insert into dd select g, 1998 + g % 5 from generate_series(1, 2000) g",
		"insert into ws select g % 10 + 1, g % 2000 + 1, g % 100 from generate_series(1, 50000) g",
		"insert into cs select g % 10 + 1, g % 2000 + 1, g % 100 from generate_series(1, 50000) g",
		"analyze wh",
		"analyze dd",
		"analyze ws",
		"analyze cs",
		"set parallel_setup_cost = 0",
		"set parallel_tuple_cost = 0",
		"set min_parallel_table_scan_size = 0",
	} {
		if _, e := allRowsQuery(t, conn, q); e != "" {
			t.Fatalf("%s: %s", q, e)
		}
	}
	got, e := allRowsQuery(t, conn, `EXPLAIN (COSTS OFF) select w_name, w_city, year, sum(amt) from ( select w_name, w_city, d_year as year, sum(ws_amt) as amt from ws, wh, dd where ws_wh = w_sk and ws_dt = d_sk and d_year = 2001 group by w_name, w_city, d_year union all select w_name, w_city, d_year as year, sum(cs_amt) as amt from cs, wh, dd where cs_wh = w_sk and cs_dt = d_sk and d_year = 2001 group by w_name, w_city, d_year) x group by w_name, w_city, year order by w_name limit 10`)
	plan := strings.Join(got, "\n")
	if e != "" || !strings.Contains(plan, "Finalize GroupAggregate") {
		t.Fatalf("fixture did not plan PG's parallel member aggregates (err %q):\n%s", e, plan)
	}
	for _, want := range []string{
		"Group Key: wh.w_name, wh.w_city, dd.d_year",
		"Sort Key: wh.w_name, wh.w_city, dd.d_year",
	} {
		if !strings.Contains(plan, want) {
			t.Errorf("missing PG's line %s:\n%s", want, plan)
		}
	}
}
