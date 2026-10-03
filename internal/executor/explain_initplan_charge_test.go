package executor

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestExplainChargesInitPlansToTopNode pins M0146-0005dr against PG 18.3's
// SS_charge_for_initplans: the top node of a query level carries the cost of
// the initPlans it runs — every kept CTE body and every uncorrelated
// sublink — on both its startup and its total cost. PG prints
// `Limit (cost=38427.43..)` over `Sort (cost=20729.93..)` with
// `CTE c -> HashAggregate (cost=..17697.50)`: 20729.93 + 17697.50.
func TestExplainChargesInitPlansToTopNode(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE e1 (a int, p int)")
	runSQL(t, ctx, "CREATE TABLE e2 (b int, q int)")
	runSQL(t, ctx, "INSERT INTO e1 SELECT g, g FROM generate_series(1,2000) g")
	runSQL(t, ctx, "INSERT INTO e2 SELECT g, g FROM generate_series(1,2000) g")

	costRe := regexp.MustCompile(`^\s*(?:->\s+)?(.*?)\s+\(cost=([0-9.]+)\.\.([0-9.]+) `)
	type line struct {
		label          string
		startup, total float64
	}
	explain := func(sql string) []line {
		var out []line
		for _, r := range renderRows(runSQL(t, ctx, "EXPLAIN "+sql)) {
			m := costRe.FindStringSubmatch(r)
			if m == nil {
				continue
			}
			s, _ := strconv.ParseFloat(m[2], 64)
			tt, _ := strconv.ParseFloat(m[3], 64)
			out = append(out, line{m[1], s, tt})
		}
		return out
	}
	near := func(a, b float64) bool { return math.Abs(a-b) < 0.02 }

	// A kept CTE: Limit = Sort + CTE body total.
	ls := explain("WITH c AS MATERIALIZED (SELECT a, count(*) n FROM e1 GROUP BY a) SELECT * FROM c x JOIN c y ON x.a = y.a + 1 ORDER BY 1 LIMIT 5")
	find := func(ls []line, prefix string) line {
		for _, l := range ls {
			if strings.HasPrefix(l.label, prefix) {
				return l
			}
		}
		t.Fatalf("no %q line in %+v", prefix, ls)
		return line{}
	}
	if len(ls) == 0 || !strings.HasPrefix(ls[0].label, "Limit") {
		t.Fatalf("unexpected plan shape: %+v", ls)
	}
	body, sort := find(ls, "HashAggregate"), find(ls, "Sort")
	if !near(ls[0].startup, sort.startup+body.total) {
		t.Errorf("Limit startup %.2f, want Sort startup %.2f + CTE body %.2f", ls[0].startup, sort.startup, body.total)
	}

	// An uncorrelated scalar sublink in a WHERE: the scan carries the
	// InitPlan's total on both costs.
	ls = explain("SELECT b FROM e2 WHERE q > (SELECT avg(p) FROM e1)")
	if len(ls) < 2 || !strings.HasPrefix(ls[0].label, "Seq Scan on e2") {
		t.Fatalf("unexpected plan shape: %+v", ls)
	}
	ip := ls[1]
	if !near(ls[0].startup, ip.total) {
		t.Errorf("Seq Scan startup %.2f, want the InitPlan's total %.2f", ls[0].startup, ip.total)
	}
	if ls[0].total <= ip.total {
		t.Errorf("Seq Scan total %.2f does not include the InitPlan's %.2f", ls[0].total, ip.total)
	}
}
