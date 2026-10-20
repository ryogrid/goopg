package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestStrippedCTELeafQualsDeparseSubplanTargets pins M0146-0224 slice 4.
// When setrefs removes the trivial SubqueryScan over a grouped CTE leg, PG's
// set_subqueryscan_references replaces the upper Vars by the subplan's
// target-list expressions, so EXPLAIN deparses a join key as the base
// column the CTE renames and a WHERE conjunct as the aggregate call (TPC-DS
// Q78: `web_sales.ws_bill_customer_sk`,
// `COALESCE((sum(web_sales.ws_quantity)), '0'::bigint)`). goopg printed the
// CTE output names, `ws_customer_sk` and `COALESCE(ws_qty, 0)`: the merge
// keys over an anti join's output and the top Filter over the joins both
// stopped at the stripped leg, and so did the top Sort's keys
// (`ss_qty DESC` for `(sum(store_sales.ss_quantity)) DESC`).
func TestStrippedCTELeafQualsDeparseSubplanTargets(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE sc_dd (d_sk int, d_year int)",
		"CREATE TABLE sc_ss (ss_item int, ss_cust int, ss_qty int, ss_tick int, ss_date int)",
		"CREATE TABLE sc_sr (sr_item int, sr_tick int, PRIMARY KEY (sr_item, sr_tick))",
		"CREATE TABLE sc_ws (ws_item int, ws_bill_cust int, ws_qty int, ws_order int, ws_date int)",
		"CREATE TABLE sc_wr (wr_item int, wr_order int, PRIMARY KEY (wr_item, wr_order))",
		"CREATE TABLE sc_cs (cs_item int, cs_bill_cust int, cs_qty int, cs_order int, cs_date int)",
		"CREATE TABLE sc_cr (cr_item int, cr_order int, PRIMARY KEY (cr_item, cr_order))",
		"INSERT INTO sc_dd SELECT g, 1990 + g % 20 FROM generate_series(1, 2000) g",
		"INSERT INTO sc_ss SELECT g % 3000, g % 700, g, g, g % 2000 FROM generate_series(1, 60000) g",
		"INSERT INTO sc_sr SELECT g % 3000, g FROM generate_series(1, 6000) g",
		"INSERT INTO sc_ws SELECT g % 3000, g % 700, g, g, g % 2000 FROM generate_series(1, 30000) g",
		"INSERT INTO sc_wr SELECT g % 3000, g FROM generate_series(1, 3000) g",
		"INSERT INTO sc_cs SELECT g % 3000, g % 700, g, g, g % 2000 FROM generate_series(1, 40000) g",
		"INSERT INTO sc_cr SELECT g % 3000, g FROM generate_series(1, 4000) g",
		"ANALYZE sc_dd", "ANALYZE sc_ss", "ANALYZE sc_sr", "ANALYZE sc_ws",
		"ANALYZE sc_wr", "ANALYZE sc_cs", "ANALYZE sc_cr",
	} {
		runSQL(t, ctx, q)
	}
	// Q78's shape: three grouped legs, each over an anti join against its
	// returns table, two outer joins, and a WHERE disjunction only the top
	// joinrel can evaluate.
	leg := func(name, p, r, cust, qty, key string) string {
		return name + " AS (SELECT d_year AS " + p + "_year, " + p + "_item, " + p + "_" + cust + " AS " + name + "leg_cust, " +
			"sum(" + p + "_qty) AS " + name + "leg_qty FROM sc_" + p + " LEFT JOIN sc_" + r + " ON " +
			r + "_" + key + " = " + p + "_" + key + " AND " + p + "_item = " + r + "_item " +
			"JOIN sc_dd ON " + p + "_date = d_sk WHERE " + r + "_" + key + " IS NULL " +
			"GROUP BY d_year, " + p + "_item, " + p + "_" + cust + ")"
	}
	q := "EXPLAIN (COSTS OFF) WITH " +
		leg("w", "ws", "wr", "bill_cust", "qty", "order") + ", " +
		leg("c", "cs", "cr", "bill_cust", "qty", "order") + ", " +
		leg("s", "ss", "sr", "cust", "qty", "tick") + " " +
		"SELECT ss_item, sleg_qty, wleg_qty, cleg_qty FROM s " +
		"LEFT JOIN w ON (ws_year = ss_year AND ws_item = ss_item AND wleg_cust = sleg_cust) " +
		"LEFT JOIN c ON (cs_year = ss_year AND cs_item = ss_item AND cleg_cust = sleg_cust) " +
		"WHERE (coalesce(wleg_qty, 0) > 0 OR coalesce(cleg_qty, 0) > 0) AND ss_year = 2000 ORDER BY sleg_cust, sleg_qty DESC LIMIT 100"
	// GroupAggregate legs feed the merges presorted, so no Sort keeps a
	// Subquery Scan above them (TestMergeJoinOverPresortedSubqueryLeaf).
	defer hashAggSeed(false)()
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	ps.EnableHashJoin = false
	plan := strings.Join(renderRows(runSQLWith(t, ctx, q, ps)), "\n")
	if !strings.Contains(plan, "Merge Left Join") || strings.Contains(plan, "CTE Scan") ||
		strings.Contains(plan, "Subquery Scan") {
		t.Fatalf("want merge left joins over stripped CTE legs:\n%s", plan)
	}
	if strings.Contains(plan, "leg_") {
		t.Errorf("plan still prints a CTE output name:\n%s", plan)
	}
	for _, want := range []string{
		"Merge Cond: ((sc_ss.ss_item = sc_ws.ws_item) AND (sc_ss.ss_cust = sc_ws.ws_bill_cust))",
		"Merge Cond: ((sc_ss.ss_item = sc_cs.cs_item) AND (sc_ss.ss_cust = sc_cs.cs_bill_cust))",
		"Filter: ((COALESCE((sum(sc_ws.ws_qty)), ",
		"OR (COALESCE((sum(sc_cs.cs_qty)), ",
		"Sort Key: sc_ss.ss_cust, (sum(sc_ss.ss_qty)) DESC",
	} {
		if !strings.Contains(plan, want) {
			t.Errorf("want %q, as PG deparses the stripped leg's target:\n%s", want, plan)
		}
	}
}
