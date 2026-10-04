package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestCorrelatedScalarSublinkIsJoinClause pins M0146-0012a slice A on TPC-H
// Q17 in miniature. PG counts a correlated SubPlan's parameter Vars toward its
// clause's relids, so `l.qty < (SELECT … WHERE l2.pk = p.pk)` is an {l, p}
// join clause: PG 18.3 evaluates it at the join, as the Hash Join's Join
// Filter or as the parameterised inner scan's Filter, never above the join.
// goopg read the clause as {l} only and applied it above the finished join.
//
// The inner plan's PARAM_EXEC prints as the column that supplies it
// (`pk = p.pk`), not `$0`. Values are PG 18.3's.
func TestCorrelatedScalarSublinkIsJoinClause(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE p (pk int, brand int)",
		"CREATE TABLE l (pk int, qty int, price int)",
		"INSERT INTO p SELECT g, g % 40 FROM generate_series(1, 2000) g",
		"INSERT INTO l SELECT g % 2000 + 1, g % 50, g FROM generate_series(1, 60000) g",
		"CREATE INDEX l_pk ON l(pk)",
		"ANALYZE p",
		"ANALYZE l",
	} {
		runSQL(t, ctx, q)
	}
	nl := optimizer.DefaultPlannerSettings()
	nl.EnableHashJoin = false
	nl.EnableMergeJoin = false
	for _, c := range []struct {
		name, q, rows, at string
		ps                optimizer.PlannerSettings
	}{
		{"join filter", `SELECT sum(l.price), count(*) FROM l, p WHERE p.pk = l.pk AND p.brand = 7
			AND l.qty < (SELECT 0.2 * avg(l2.qty) FROM l l2 WHERE l2.pk = p.pk)`,
			"NULL|0", "Join Filter: ((l.qty)::numeric < (SubPlan 1))", optimizer.DefaultPlannerSettings()},
		{"both relations outside the sublink", `SELECT count(*) FROM p, l WHERE p.pk = l.pk AND p.brand = 3
			AND l.qty + p.brand > (SELECT avg(l2.qty) FROM l l2 WHERE l2.pk = p.pk)`,
			"1500", "Join Filter: (((l.qty + p.brand))::numeric > (SubPlan 1))", optimizer.DefaultPlannerSettings()},
		// PG's own choice for this data: the clause filters the parameterised
		// inner scan.
		{"parameterised inner", `SELECT sum(l.price), count(*) FROM l, p WHERE p.pk = l.pk AND p.brand = 7
			AND l.qty < (SELECT 0.2 * avg(l2.qty) FROM l l2 WHERE l2.pk = p.pk)`,
			"NULL|0", "Filter: ((qty)::numeric < (SubPlan 1))", nl},
	} {
		plan := renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+c.q, c.ps))
		joined := strings.Join(plan, "\n")
		if countLinesContaining(plan, c.at) != 1 {
			t.Errorf("%s: want %q:\n%s", c.name, c.at, joined)
		}
		if countLinesContaining(plan, "$0") != 0 || countLinesContaining(plan, "Index Cond: (pk = p.pk)") == 0 {
			t.Errorf("%s: want the PARAM_EXEC printed as p.pk:\n%s", c.name, joined)
		}
		if got := strings.Join(renderRows(runSQLWith(t, ctx, c.q, c.ps)), ";"); got != c.rows {
			t.Errorf("%s: rows %q, want PG's %q\n%s", c.name, got, c.rows, joined)
		}
	}
}

// TestCorrelatedSublinkOverFlattenedSubqueryKeepsItsArgs guards the slice's
// two refusals on regress join's placeholder case. `ss.y` is the constant 42
// of a FROM subquery flattened onto `t2`, so its slot does not name a base
// relation column: the clause is not pre-lowered (sublinkArgsNameBaseRelations).
// The declined attempt must not leave anything behind either — the plan clone
// used to share the `IS NOT NULL` node with the original, which kept a `$-1`
// sentinel and failed with "SubPlan parameter $-1 read before assignment".
// Rows are PG 18.3's.
func TestCorrelatedSublinkOverFlattenedSubqueryKeepsItsArgs(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE int8_tbl (q1 int8, q2 int8)")
	runSQL(t, ctx, `INSERT INTO int8_tbl VALUES (123, 456), (123, 4567890123456789),
		(4567890123456789, 123), (4567890123456789, 4567890123456789), (4567890123456789, -4567890123456789)`)
	const q = `select * from int8_tbl t1 left join (select q1 as x, 42 as y from int8_tbl t2) ss
		on t1.q2 = ss.x where 1 = (select 1 from int8_tbl t3 where ss.y is not null limit 1) order by 1,2`
	const want = "123|4567890123456789|4567890123456789|42;123|4567890123456789|4567890123456789|42;" +
		"123|4567890123456789|4567890123456789|42;4567890123456789|123|123|42;4567890123456789|123|123|42;" +
		"4567890123456789|4567890123456789|4567890123456789|42;4567890123456789|4567890123456789|4567890123456789|42;" +
		"4567890123456789|4567890123456789|4567890123456789|42"
	if got := strings.Join(renderRows(runSQL(t, ctx, q)), ";"); got != want {
		t.Errorf("rows %q, want PG's %q", got, want)
	}
}
