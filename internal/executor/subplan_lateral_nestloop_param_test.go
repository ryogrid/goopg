package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestCorrelatedSubPlanKeepsInnerNestloopParam pins M0146-0170 against PG
// 18.3. A correlated SubPlan whose body is a parameterised nested loop (the
// R25 lateral Join over an index probe) carries two kinds of Level-1
// OuterColumnRef: the correlation to the enclosing query (`cs_1.item =
// it.id`) and the nestloop param of its own inner probe (`dd.sk =
// cs_1.sold`), which reads the frame the lateral join pushes per outer row.
// The sublink pull-up post-pass (rebaseOne) re-based every Level-1 ref as
// escaping and bound the nestloop param to the host query's row — here the
// outer `cs` row — so every average was computed over the wrong dates and the
// whole sum came back NULL. TPC-DS Q32 hit it once M0146-0159 retired the x2
// probe calibration and its SubPlan took this shape.
//
// Nested loops are forced (hash and merge join off) so the SubPlan keeps the
// probe shape on this small fixture; PG 18.3 answers 26457 at default and at
// these settings alike.
//
// M0146-0176: the join-qual case also pins WHERE the qual runs. Its relids
// are {cs, it} (the SubPlan's argument is it.id), so PG evaluates it as the
// {cs, it} join's level: at these settings it moves into the parameterised
// inner scan (PG 18.3 files it on `Index Scan using it_pkey on it` as
// `Filter: ((m = 2) AND (cs.amt > (SubPlan 1)))`), and with hash joins on,
// TPC-DS Q32/Q92 show it as the hash join's Join Filter. M0146-0170's first
// fix refused to re-base any SubPlan holding a lateral join, which left the
// qual as a Filter on the finished join above both scans — unpriced, so Q32
// and Q92 lost PG's hash join. The re-base now walks a lateral join's right
// child one frame deeper instead.
func TestCorrelatedSubPlanKeepsInnerNestloopParam(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, q := range []string{
		"CREATE TABLE cs (item int, ord int, sold int, amt numeric, PRIMARY KEY (item, ord))",
		"CREATE TABLE it (id int PRIMARY KEY, m int)",
		"CREATE TABLE dd (sk int PRIMARY KEY, d int)",
		"INSERT INTO it SELECT g, g % 5 FROM generate_series(1, 50) g",
		"INSERT INTO dd SELECT g, g FROM generate_series(1, 100) g",
		"INSERT INTO cs SELECT g % 50 + 1, g, g % 100 + 1, (g % 97)::numeric FROM generate_series(1, 5000) g",
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableHashJoin = false
	ps.EnableMergeJoin = false
	ps.MaxParallelWorkersPerGather = 0

	const sub = "(SELECT 1.3 * avg(amt) FROM cs, dd WHERE cs.item = it.id AND dd.sk = cs.sold AND dd.d <= 60)"
	for _, c := range []struct{ name, sql, want, planWant string }{
		// The failing shape: the SubPlan in a qual over a join whose other
		// side (it) supplies the correlation.
		{"join-qual", "SELECT sum(amt) FROM cs, it WHERE it.id = cs.item AND it.m = 2 AND amt > " + sub, "26457",
			"amt > (SubPlan 1))"},
		// The same subquery where it was already right (controls).
		{"single-rel-qual", "SELECT count(*) FROM it WHERE it.m = 2 AND 30 < " + sub, "10", ""},
		// PG: sum(x) = 641.277; asserted as an integer because this
		// fixture's row renderer prints a numeric's unscaled digits.
		{"target-list", "SELECT (round(sum(x), 3) * 1000)::int FROM (SELECT " + sub + " x FROM it WHERE it.m = 2) s", "641277", ""},
	} {
		var plan []string
		for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN (COSTS OFF) "+c.sql, ps)) {
			plan = append(plan, r[0].StringValue())
		}
		got := strings.Join(formatRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, c.sql, ps))), ";")
		if got != c.want {
			t.Errorf("%s = %q, want %q (PG 18.3)\nplan:\n%s", c.name, got, c.want, strings.Join(plan, "\n"))
		}
		if c.planWant != "" {
			if owner := qualOwner(plan, c.planWant); !strings.Contains(owner, "Scan") && !strings.Contains(owner, "Join Filter") {
				t.Errorf("%s: %q is evaluated by %q, not at the {cs, it} join or a parameterised scan below it (PG 18.3)\nplan:\n%s",
					c.name, c.planWant, owner, strings.Join(plan, "\n"))
			}
		}
	}
}

// qualOwner returns the EXPLAIN line that owns the first qual line containing
// want: the qual line itself when it is a Join Filter, otherwise the nearest
// node line ("->  …" or the root) above it.
func qualOwner(plan []string, want string) string {
	for i, l := range plan {
		if !strings.Contains(l, want) {
			continue
		}
		if strings.Contains(l, "Join Filter:") {
			return strings.TrimSpace(l)
		}
		for j := i - 1; j >= 0; j-- {
			if strings.Contains(plan[j], "->  ") || j == 0 {
				return strings.TrimSpace(plan[j])
			}
		}
	}
	return "(qual not in plan)"
}
