package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestAggregatesWrittenOnlyInsideCaseOrWindowSpecArePlanned pins M0146-0143.
// PG registers an aggregate at its query level wherever it is written
// (transformAggregateCall, parse_agg.c) — inside a CASE, a window's
// PARTITION BY / ORDER BY, a named WINDOW clause or an IN list. goopg's
// collector (collectAggregateCalls via walkExpr) stopped at those nodes, so a
// query whose aggregate appeared ONLY there failed "aggregate call could not
// be resolved" — TPC-DS Q70's `rank() over (partition by s_state order by
// sum(ss_net_profit) desc)` inside its IN subquery.
func TestAggregatesWrittenOnlyInsideCaseOrWindowSpecArePlanned(t *testing.T) {
	cat := presortedAggCatalog(t)
	for _, sql := range []string{
		"select ten, rank() over (order by sum(unique1)) from tenk1 group by ten",
		"select ten, rank() over (partition by sum(unique1) > 10 order by ten) from tenk1 group by ten",
		"select ten, rank() over w from tenk1 group by ten window w as (order by sum(unique1) desc)",
		"select ten, case when sum(unique1) > 2 then 1 else 0 end from tenk1 group by ten",
		"select case when count(*) > 2 then 'big' end from tenk1",
		"select ten from tenk1 group by ten having case when sum(unique1) > 2 then true else false end",
		"select ten, sum(unique1) in (3, 4) from tenk1 group by ten",
		"select ten from tenk1 group by ten having sum(unique1) in (3)",
		"select ten, ten in (1, 3) from tenk1 group by ten",
		// A subquery IN's left operand resolves through the aggregate
		// surface; only its body is planned in the HAVING-parent context.
		"select ten from tenk1 group by ten having sum(unique1) in (select 3)",
		"select sum(unique1) in (select 8) from tenk1",
		"select (1 = any(array_agg(ten))) = any (select false) from tenk1",
	} {
		if _, err := PlanWithSettings(parseOne(t, sql), cat, DefaultPlannerSettings()); err != nil {
			t.Errorf("%s: %v", sql, err)
		}
	}
}

// TestWalkExprStaysAtItsQueryLevel pins the other half of walkExpr's
// contract: calls inside a subquery belong to the inner query level and are
// not visited, and neither are an aggregate's own FILTER or ORDER BY (an
// aggregate there is an error PG raises on its own).
func TestWalkExprStaysAtItsQueryLevel(t *testing.T) {
	for _, tc := range []struct {
		sql  string
		want int // aggregates visited at the outer level
	}{
		{"select case when sum(a) > 0 then max(b) else min(c) end from t", 3},
		{"select rank() over (partition by sum(a) order by max(b)) from t", 2},
		{"select (select sum(a) from u) from t", 0},
		{"select a in (select count(*) from u) from t", 0},
		{"select count(*) filter (where a > 0) from t", 1},
	} {
		stmts, err := parser.Parse(tc.sql)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.sql, err)
		}
		s := stmts[0].(*parser.SelectStmt)
		n := 0
		_ = walkExpr(s.Targets[0].Expr, func(fc *parser.FuncCall) error {
			if isAggregateFunc(fc) {
				n++
			}
			return nil
		})
		if n != tc.want {
			t.Errorf("%s: visited %d aggregates, want %d", tc.sql, n, tc.want)
		}
	}
}
