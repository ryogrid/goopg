package postmaster

import (
	"strings"
	"testing"
)

// TestExplainKeyAboveAppendRelMemberSubqueryScan pins the M0146-0042 slice for
// TPC-DS Q5's wsr arm against PG 18.3. A UNION ALL in FROM is an appendrel;
// its join member stays a subquery RTE, scanned by `Subquery Scan on "*SELECT*
// 2"`, which a Parallel Append puts first (non-partial children lead). A join
// key above the Append deparses through that first child (ruleutils.c
// set_deparse_plan) and prints with the member's alias, `"*SELECT*
// 2".wsr_site`, not the column of a relation inside the member.
func TestExplainKeyAboveAppendRelMemberSubqueryScan(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, q := range []string{
		"CREATE TABLE ws (ws_item int, ws_ord int, ws_site int, ws_dt int)",
		"CREATE TABLE wr (wr_item int, wr_ord int, wr_dt int)",
		"CREATE TABLE site (s_sk int PRIMARY KEY, s_id text)",
		"CREATE TABLE dd (d_sk int PRIMARY KEY, d_y int)",
		"INSERT INTO ws SELECT g % 500, g, g % 10, g % 300 FROM generate_series(1, 20000) g",
		"INSERT INTO wr SELECT g % 500, g, g % 300 FROM generate_series(1, 2000) g",
		"INSERT INTO site SELECT g, 'site' || g FROM generate_series(0, 9) g",
		"INSERT INTO dd SELECT g, 2000 + g % 3 FROM generate_series(0, 299) g",
		"ANALYZE ws", "ANALYZE wr", "ANALYZE site", "ANALYZE dd",
		"SET parallel_setup_cost = 0",
		"SET parallel_tuple_cost = 0",
		"SET min_parallel_table_scan_size = 0",
		"SET max_parallel_workers_per_gather = 2",
	} {
		if _, e := allRowsQuery(t, conn, q); e != "" {
			t.Fatalf("%s: %s", q, e)
		}
	}
	got, e := allRowsQuery(t, conn, `EXPLAIN (COSTS OFF) SELECT s_id, count(*) FROM
  (SELECT ws_site AS wsr_site, ws_dt AS date_sk FROM ws
   UNION ALL
   SELECT ws.ws_site AS wsr_site, wr.wr_dt AS date_sk FROM wr LEFT JOIN ws ON wr.wr_item = ws.ws_item AND wr.wr_ord = ws.ws_ord) sr,
  dd, site
 WHERE sr.date_sk = dd.d_sk AND sr.wsr_site = site.s_sk AND dd.d_y = 2001
 GROUP BY s_id`)
	plan := strings.Join(got, "\n")
	if e != "" || !strings.Contains(plan, "Parallel Append") || !strings.Contains(plan, `Subquery Scan on "*SELECT* 2"`) {
		t.Fatalf("fixture did not plan PG's Parallel Append over the kept member (err %q):\n%s", e, plan)
	}
	for _, want := range []string{
		`Hash Cond: ("*SELECT* 2".date_sk = dd.d_sk)`,
		`Hash Cond: ("*SELECT* 2".wsr_site = site.s_sk)`,
	} {
		if !strings.Contains(plan, want) {
			t.Errorf("missing PG's key deparse %s:\n%s", want, plan)
		}
	}
}
