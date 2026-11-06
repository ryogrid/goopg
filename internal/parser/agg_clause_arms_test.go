package parser

import (
	"strings"
	"testing"
)

// TestParseAggregateClauseArms pins the FILTER / WITHIN GROUP cross-product
// cells added for M0148-0001 (aggregates.sql): gram.y's func_expr is ONE rule
// `func_application within_group_clause filter_clause over_clause` with every
// clause optional, so every instantiation must parse. goopg inlines the
// cross-product as name_or_call alternatives, and the args-ORDER-BY × FILTER
// (±DISTINCT, ±OVER) and within_group × FILTER cells were missing — making
// `sum(two order by two) filter (where two > 1)` and
// `aggfns(distinct a,b,c order by a,c using ~<~,b) filter (where a > 1)`
// (aggregates.sql presort/FILTER checks) hard 42601s.
func TestParseAggregateClauseArms(t *testing.T) {
	cases := []struct {
		sql             string
		wantArgs        int // len(Args); -1 = not checked
		wantDistinct    bool
		wantOrderBy     int // len(OrderBy); -1 = not checked
		wantWithinGroup int // len(WithinGroup); -1 = not checked
		wantFilter      bool
		wantOver        bool
	}{
		// baseline cells (regression guard for the existing arms)
		{sql: "select sum(two) filter (where two > 1) from tenk1", wantArgs: 1, wantFilter: true},
		{sql: "select count(*) filter (where two > 1) from tenk1", wantArgs: 0, wantFilter: true},

		// args-ORDER-BY × FILTER (+OVER)
		{sql: "select sum(two order by two) filter (where two > 1) from tenk1", wantArgs: 1, wantOrderBy: 1, wantFilter: true},
		{sql: "select sum(two order by two) filter (where two > 1) over () from tenk1", wantArgs: 1, wantOrderBy: 1, wantFilter: true, wantOver: true},
		{sql: "select sum(two order by two) filter (where two > 1) over w from tenk1", wantArgs: 1, wantOrderBy: 1, wantFilter: true, wantOver: true},
		{sql: "select sum(two order by two) filter (where two > 1) over (partition by four) from tenk1", wantArgs: 1, wantOrderBy: 1, wantFilter: true, wantOver: true},

		// DISTINCT-args-ORDER-BY × FILTER (+OVER)
		{sql: "select aggfns(distinct a,b,c order by a,c using ~<~,b) filter (where a > 1) from (values (1,3,'foo')) v(a,b,c), generate_series(1,2) i", wantArgs: 3, wantDistinct: true, wantOrderBy: 3, wantFilter: true},
		{sql: "select aggfns(distinct a order by a) filter (where a > 1) over () from t", wantArgs: 1, wantDistinct: true, wantOrderBy: 1, wantFilter: true, wantOver: true},
		{sql: "select aggfns(distinct a order by a) filter (where a > 1) over w from t", wantArgs: 1, wantDistinct: true, wantOrderBy: 1, wantFilter: true, wantOver: true},

		// within_group × FILTER (aggordview1, aggregates.sql)
		{sql: "select percentile_disc(0.5) within group (order by thousand) filter (where hundred = 1) from tenk1", wantArgs: 1, wantWithinGroup: 1, wantFilter: true},

		// baseline within-group cell (regression guard)
		{sql: "select mode() within group (order by string4) from tenk1", wantArgs: 0, wantWithinGroup: 1},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.sql, err)
			continue
		}
		fc, ok := stmts[0].(*SelectStmt).Targets[0].Expr.(*FuncCall)
		if !ok {
			t.Errorf("%q: target is %T, want *FuncCall", tc.sql, stmts[0].(*SelectStmt).Targets[0].Expr)
			continue
		}
		if tc.wantArgs >= 0 && len(fc.Args) != tc.wantArgs {
			t.Errorf("%q: len(Args)=%d want %d", tc.sql, len(fc.Args), tc.wantArgs)
		}
		if fc.Distinct != tc.wantDistinct {
			t.Errorf("%q: Distinct=%v want %v", tc.sql, fc.Distinct, tc.wantDistinct)
		}
		if tc.wantOrderBy >= 0 && len(fc.OrderBy) != tc.wantOrderBy {
			t.Errorf("%q: len(OrderBy)=%d want %d", tc.sql, len(fc.OrderBy), tc.wantOrderBy)
		}
		if tc.wantWithinGroup >= 0 && len(fc.WithinGroup) != tc.wantWithinGroup {
			t.Errorf("%q: len(WithinGroup)=%d want %d", tc.sql, len(fc.WithinGroup), tc.wantWithinGroup)
		}
		if (fc.Filter != nil) != tc.wantFilter {
			t.Errorf("%q: Filter=%v want present=%v", tc.sql, fc.Filter != nil, tc.wantFilter)
		}
		if (fc.Over != nil) != tc.wantOver {
			t.Errorf("%q: Over=%v want present=%v", tc.sql, fc.Over != nil, tc.wantOver)
		}
	}
}

// TestParseMultipleOrderByWithinGroup pins gram.y's func_expr action, which
// raises "cannot use multiple ORDER BY clauses with WITHIN GROUP" INSIDE the
// production (the order clause for WITHIN GROUP and plain-aggregate ORDER BY
// share one field) at parser_errposition(@2) — the WITHIN token. aggregates.
// sql pins the exact wording; the caret line lands under `within`.
func TestParseMultipleOrderByWithinGroup(t *testing.T) {
	sql := "select p, percentile_cont(p order by p) within group (order by x) from generate_series(1,5) x, (values (0::float8)) v(p) group by p"
	_, err := Parse(sql)
	se, ok := err.(*SyntaxError)
	if !ok {
		t.Fatalf("Parse: err=%v, want *SyntaxError", err)
	}
	if se.Message != "cannot use multiple ORDER BY clauses with WITHIN GROUP" {
		t.Errorf("Message=%q", se.Message)
	}
	if !se.Raw {
		t.Errorf("Raw=false: PG's own wording must not get the \"syntax error at or near\" wrapper")
	}
	if se.Code != "" {
		t.Errorf("Code=%q, want \"\" (default 42601 = ERRCODE_SYNTAX_ERROR, PG's code here)", se.Code)
	}
	wantPos := strings.Index(sql, "within")
	if se.Pos != wantPos {
		t.Errorf("Pos=%d want %d (the WITHIN token, PG parser_errposition(@2))", se.Pos, wantPos)
	}
}
