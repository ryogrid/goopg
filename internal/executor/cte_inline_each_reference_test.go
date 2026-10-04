package executor

import (
	"strings"
	"testing"
)

// cteInlineFixture builds the two tables both tests below read. The values
// they assert were taken from PG 18.3 on the same data.
func cteInlineFixture(t *testing.T) *Context {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE ci_t (a int, b int)",
		"INSERT INTO ci_t SELECT g, g % 10 FROM generate_series(1, 1000) g",
		"CREATE TABLE ci_u (a int, b int)",
		"INSERT INTO ci_u SELECT g, g % 7 FROM generate_series(1, 500) g",
		"ANALYZE ci_t",
		"ANALYZE ci_u",
	} {
		runSQL(t, ctx, q)
	}
	return ctx
}

// TestSublinkOverInlinedCTEKeepsBodyWhere pins M0146-0057: a CTE reference
// inside an IN / EXISTS sublink reads as a plain relation to the sublink
// pull-up, and the FROM walk then pulls the CTE's body up (M0146-0007e). The
// body's own WHERE came back in the scope's pulled quals, which the sublink
// splice never read, so every row of the CTE's table reached the semi join.
// PG 18.3 returns 82 for each query; goopg returned 166.
func TestSublinkOverInlinedCTEKeepsBodyWhere(t *testing.T) {
	ctx := cteInlineFixture(t)
	for _, q := range []string{
		`WITH x AS (SELECT a, b FROM ci_t WHERE b < 5)
		 SELECT count(*) FROM ci_u WHERE ci_u.a IN (SELECT a FROM x x2 WHERE x2.a % 3 = 0)`,
		`WITH x AS (SELECT a, b FROM ci_t WHERE b < 5)
		 SELECT count(*) FROM ci_u WHERE EXISTS (SELECT 1 FROM x x2 WHERE x2.a = ci_u.a AND x2.a % 3 = 0)`,
		`WITH x AS NOT MATERIALIZED (SELECT a, b FROM ci_t WHERE b < 5)
		 SELECT count(*) FROM ci_u WHERE ci_u.a IN (SELECT a FROM x x2 WHERE x2.a % 3 = 0)
		   AND ci_u.a IN (SELECT a FROM x)`,
	} {
		if got := strings.Join(renderRows(runSQL(t, ctx, q)), ";"); got != "82" {
			t.Errorf("count = %s, want PG's 82\nquery: %s\nplan:\n%s", got, q,
				strings.Join(renderRows(runSQL(t, ctx, "EXPLAIN "+q)), "\n"))
		}
	}
}

// TestNotMaterializedCTEInlinesEachReference pins M0146-0007f: PG's
// inline_cte copies a NOT MATERIALIZED CTE into every reference, however many
// there are, so no `CTE x` section or CTE Scan remains. Each copy is an
// ordinary subquery: a simple body is pulled up (two scans of ci_t, the
// second qual on the second copy only), a grouped body takes the outer qual
// below its aggregate. A CTE written without the keyword and referenced twice
// stays one shared CTE.
//
// Every case gets its own fixture: the executor keys a kept CTE's rows by its
// declaration (offset and name), and that cache outlives the statement on a
// reused Context (M0146-0059), so two statements declaring `x` at the same
// offset must not share one.
func TestNotMaterializedCTEInlinesEachReference(t *testing.T) {
	explain := func(ctx *Context, q string) []string {
		return renderRows(runSQL(t, ctx, "EXPLAIN "+q))
	}
	cases := []struct {
		name, q, rows string
		check         func(plan []string) string
	}{
		{
			name: "simple body",
			q: `WITH x AS NOT MATERIALIZED (SELECT a, b FROM ci_t WHERE b < 5)
			    SELECT count(*), sum(x.a) FROM x, x x2 WHERE x.a = x2.a AND x2.b = 1`,
			rows: "100|49600",
			check: func(plan []string) string {
				if countLinesContaining(plan, "Seq Scan on ci_t") != 2 {
					return "want both references pulled up into two scans of ci_t"
				}
				if countLinesContaining(plan, "(b < 5) AND (b = 1)") != 1 {
					return "want the second copy to carry its body's qual and the outer one"
				}
				return ""
			},
		},
		{
			name: "grouped body",
			q: `WITH x AS NOT MATERIALIZED (SELECT b, count(*) c FROM ci_t GROUP BY b)
			    SELECT * FROM x, x x2 WHERE x.b = x2.b + 1 AND x2.b = 3`,
			rows: "4|100|3|100",
			check: func(plan []string) string {
				if countLinesContaining(plan, "Aggregate") != 2 {
					return "want one aggregate per reference"
				}
				if countLinesContaining(plan, "Filter: (b = 3)") != 1 {
					return "want x2.b = 3 pushed below x2's aggregate onto its scan"
				}
				return ""
			},
		},
		{
			name: "join chain",
			q: `WITH x AS NOT MATERIALIZED (SELECT a, b FROM ci_t WHERE b < 5)
			    SELECT count(*) FROM x JOIN ci_u ON x.a = ci_u.a JOIN x x2 ON x2.a = ci_u.a WHERE x2.b = 1`,
			rows: "50",
			check: func(plan []string) string {
				if countLinesContaining(plan, "Seq Scan on ci_t") != 2 {
					return "want both references in the JOIN chain pulled up"
				}
				return ""
			},
		},
		{
			name: "body names resolve at the WITH",
			q: `WITH ci_t AS NOT MATERIALIZED (SELECT a + 1000 AS a, b FROM ci_t)
			    SELECT count(*), min(ci_t.a) FROM ci_t, ci_t t2 WHERE ci_t.a = t2.a`,
			rows: "1000|1001",
		},
	}
	for _, c := range cases {
		ctx := cteInlineFixture(t)
		plan := explain(ctx, c.q)
		joined := strings.Join(plan, "\n")
		if countLinesContaining(plan, "CTE ") != 0 {
			t.Errorf("%s: NOT MATERIALIZED must inline every reference, got a CTE:\n%s", c.name, joined)
		}
		if c.check != nil {
			if why := c.check(plan); why != "" {
				t.Errorf("%s: %s:\n%s", c.name, why, joined)
			}
		}
		if got := strings.Join(renderRows(runSQL(t, ctx, c.q)), ";"); got != c.rows {
			t.Errorf("%s: rows %q, want PG's %q\nplan:\n%s", c.name, got, c.rows, joined)
		}
	}
	shared := explain(cteInlineFixture(t), `WITH x AS (SELECT a, b FROM ci_t WHERE b < 5)
		SELECT * FROM x, x x2 WHERE x.a = x2.a AND x2.b = 1`)
	if countLinesContaining(shared, "CTE x") != 1 {
		t.Errorf("a CTE referenced twice without NOT MATERIALIZED stays one CTE:\n%s", strings.Join(shared, "\n"))
	}
}
