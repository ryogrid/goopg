package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// correlatedLeafFixture loads two tables whose correlated scalar sublink
// keeps 993 of the 2000 joined rows.
func correlatedLeafFixture(t *testing.T) (*Context, optimizer.PlannerSettings) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE ca (k int, v int, s int)")
	runSQL(t, ctx, "CREATE TABLE cb (k int, w int)")
	runSQL(t, ctx, "INSERT INTO ca SELECT i, i % 97, i % 7 FROM generate_series(1,2000) i")
	runSQL(t, ctx, "INSERT INTO cb SELECT i, i FROM generate_series(1,2000) i")
	runSQL(t, ctx, "ANALYZE ca")
	runSQL(t, ctx, "ANALYZE cb")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	return ctx, ps
}

// TestCorrelatedSublinkIsBaseRestriction pins M0146-0005bu against PG 18.3:
// a correlated scalar sublink whose testexpr and outer Vars all name r1 is
// r1's base restriction (distribute_qual_to_rels over pull_varnos), and PG
// plans
//
//	->  CTE Scan on r r1
//	      Filter: ((t)::numeric > (SubPlan 2))
//
// with count 801 — TPC-DS Q1/Q30/Q81's shape, avg()*1.2 included. goopg held
// the qual above the whole join. PG never decorrelates a scalar sublink, so
// goopg's scalar-aggregate unnest post-pass is switched off to read the
// placement; TestUnnestedLeafSublinkKeepsJoinWidth covers it switched on.
func TestCorrelatedSublinkIsBaseRestriction(t *testing.T) {
	optimizer.SetSubqueryUnnestEnabled(false)
	defer optimizer.SetSubqueryUnnestEnabled(true)
	ctx, ps := correlatedLeafFixture(t)
	const q = "WITH r AS (SELECT k, s, sum(v) t FROM ca GROUP BY k, s) " +
		"SELECT count(*) FROM r r1, cb WHERE r1.k = cb.k AND r1.t > (SELECT avg(t) * 1.2 FROM r r2 WHERE r2.s = r1.s)"
	var lines []string
	for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)) {
		if len(r) > 0 && r[0].Kind == KindString {
			lines = append(lines, strings.TrimSpace(r[0].StringValue()))
		}
	}
	placed := false
	for i := 0; i+1 < len(lines); i++ {
		if strings.Contains(lines[i], "CTE Scan on r r1") && strings.HasPrefix(lines[i+1], "Filter:") &&
			strings.Contains(lines[i+1], "SubPlan") {
			placed = true
		}
	}
	if !placed {
		t.Fatalf("want the SubPlan qual on the r1 CTE Scan:\n%s", strings.Join(lines, "\n"))
	}
	rows := formatRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps)))
	if len(rows) != 1 || rows[0] != "801" {
		t.Fatalf("rows = %v, want [801]", rows)
	}
}

// TestUnnestedLeafSublinkKeepsJoinWidth pins the join-input half of
// M0146-0005bu: a base-restriction sublink on a plain table is still
// decorrelated by goopg's scalar-aggregate unnest, which appends the
// aggregate's columns to the host. Under a join that widened input shifted
// cb's coordinates and the count read 0; PG 18.3 answers 993 and 801.
func TestUnnestedLeafSublinkKeepsJoinWidth(t *testing.T) {
	ctx, ps := correlatedLeafFixture(t)
	const cte = "WITH r AS (SELECT k, s, sum(v) t FROM ca GROUP BY k, s) "
	for _, c := range []struct{ q, want string }{
		{"SELECT count(*) FROM ca, cb WHERE ca.k = cb.k AND ca.v > (SELECT avg(v) FROM ca c2 WHERE c2.s = ca.s)", "993"},
		{cte + "SELECT count(*) FROM r r1, cb WHERE r1.k = cb.k AND r1.t > (SELECT avg(t) FROM r r2 WHERE r2.s = r1.s)", "993"},
		{cte + "SELECT count(*) FROM r r1, cb WHERE r1.k = cb.k AND r1.t > (SELECT avg(t) * 1.2 FROM r r2 WHERE r2.s = r1.s)", "801"},
	} {
		rows := formatRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, c.q, ps)))
		if len(rows) != 1 || rows[0] != c.want {
			t.Fatalf("%s\nrows = %v, want [%s]", c.q, rows, c.want)
		}
	}
}
