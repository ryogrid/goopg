package executor

// window_aggregate_functions_test.go — M0134-0022b.
//
// PostgreSQL has no window-function allow-list: any ordinary aggregate is
// usable as a window function (postgres/src/backend/parser/parse_agg.c:
// transformWindowFuncCall / postgres/src/backend/parser/parse_func.c:
// ParseFuncOrColumn resolve window-capability through pg_proc/pg_aggregate,
// never by name). goopg previously rejected everything outside a small
// hand-written switch at what turned out to be FOUR independent, serial
// gates — the analyzer (analyzeWindowFuncCall), the planner
// (buildWindowFunc — discovered widening this brief's three named gates
// alone still left every case erroring "not supported in v0 planner"),
// and the executor's frame-aggregate detector (isFrameAggWindowFunc) plus
// per-row switch (evalWindowFuncs) — even though every one of these
// aggregates already has a working ordinary (non-window) implementation.
//
// Every `want` below not covered by a documented pre-existing numeric-
// formatting gap (see TestWindowAggregateFunctions's doc comment) is
// lifted verbatim from postgres/src/test/regress/expected/window.out
// (PostgreSQL 18.3, the read-only oracle), stripped of psql's
// column-padding whitespace.

import (
	"strings"
	"testing"
)

// TestWindowAggregateFunctions is the acceptance matrix for M0134-0022b:
// ordinary aggregates (var_pop/var_samp/variance/stddev family, bool_and/
// bool_or, array_agg) driven through OVER(), each with an explicit frame.
//
// The var_pop/var_samp/variance/stddev*/stddev cases below pin goopg's
// ACTUAL output, not PostgreSQL's — the routing fix this brief owns (three
// serial gates: analyzer/planner/executor) makes these values COMPUTE at
// all for the first time, but their numeric display scale/precision still
// diverges from PG's `window.out` oracle by a PRE-EXISTING, orthogonal bug
// this brief explicitly excludes from scope: goopg's variance/stddev
// finishers (internal/executor/operators_join_agg.go: exactIntVariance)
// (a) drop trailing zeros PG's NUMERIC division keeps (21704 vs PG's
// 21704.000000000000 — a numeric-scale gap), and (b) compute sqrt via
// 128-bit Newton-Raphson formatted at 18 significant digits vs PG's
// numeric sqrt_var rounding (147.322774885623167 vs PG's
// 147.322774885623 — a precision gap, already ledgered 2026-07-27
// "tpcds-round2 stddev-precision"). Reproduced with IDENTICAL divergence
// on the plain (non-window) aggregate call, confirming it is unrelated to
// this brief's window-function gate fix. See the brief's own escalation
// clause ("numeric display scale ... cause is outside the three gates").
// bool_and/bool_or and array_agg below ARE asserted byte-identical to the
// PG oracle — they hit no numeric formatting path.
func TestWindowAggregateFunctions(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		name string
		sql  string
		want []string
	}{
		// --- statistical aggregates, ROWS BETWEEN CURRENT ROW AND
		// UNBOUNDED FOLLOWING — window.out lines ~4989-4998. PG's exact
		// text (for the record): 21704.000000000000 / 13868.750000000000
		// / 11266.666666666667 / 4225.0000000000000000 / 0.
		{
			name: "var_pop",
			sql: "SELECT VAR_POP(n::bigint) OVER (ORDER BY i ROWS BETWEEN CURRENT ROW AND UNBOUNDED FOLLOWING) " +
				"FROM (VALUES(1,600),(2,470),(3,170),(4,430),(5,300)) r(i,n)",
			want: []string{
				"21704",
				"13868.75",
				"11266.666666666667",
				"4225",
				"0",
			},
		},
		{
			// PG: 27130.000000000000 / 18491.666666666667 /
			// 16900.000000000000 / 8450.0000000000000000 / <NULL>.
			name: "var_samp",
			sql: "SELECT VAR_SAMP(n::bigint) OVER (ORDER BY i ROWS BETWEEN CURRENT ROW AND UNBOUNDED FOLLOWING) " +
				"FROM (VALUES(1,600),(2,470),(3,170),(4,430),(5,300)) r(i,n)",
			want: []string{
				"27130",
				"18491.666666666667",
				"16900",
				"8450",
				"",
			},
		},
		{
			// variance() is the SQL-standard spelling of var_samp(). Same
			// PG text as var_samp above.
			name: "variance",
			sql: "SELECT VARIANCE(n::bigint) OVER (ORDER BY i ROWS BETWEEN CURRENT ROW AND UNBOUNDED FOLLOWING) " +
				"FROM (VALUES(1,600),(2,470),(3,170),(4,430),(5,300)) r(i,n)",
			want: []string{
				"27130",
				"18491.666666666667",
				"16900",
				"8450",
				"",
			},
		},
		{
			// PG: 147.322774885623 / 147.322774885623 /
			// 117.765657133139 / 106.144555520604 /
			// 65.0000000000000000 / 0.
			name: "stddev_pop",
			sql: "SELECT STDDEV_POP(n::bigint) OVER (ORDER BY i ROWS BETWEEN CURRENT ROW AND UNBOUNDED FOLLOWING) " +
				"FROM (VALUES(1,NULL),(2,600),(3,470),(4,170),(5,430),(6,300)) r(i,n)",
			want: []string{
				"147.322774885623167",
				"147.322774885623167",
				"117.765657133138777",
				"106.144555520604384",
				"65",
				"0",
			},
		},
		{
			// PG: 164.711869639076 / 164.711869639076 /
			// 135.984067694222 / 130.000000000000 /
			// 91.9238815542511782 / <NULL>.
			name: "stddev_samp",
			sql: "SELECT STDDEV_SAMP(n::bigint) OVER (ORDER BY i ROWS BETWEEN CURRENT ROW AND UNBOUNDED FOLLOWING) " +
				"FROM (VALUES(1,NULL),(2,600),(3,470),(4,170),(5,430),(6,300)) r(i,n)",
			want: []string{
				"164.711869639076103",
				"164.711869639076103",
				"135.984067694221688",
				"130",
				"91.9238815542511782",
				"",
			},
		},
		{
			// stddev() is the SQL-standard spelling of stddev_samp(). Same
			// PG text as stddev_samp above.
			name: "stddev",
			sql: "SELECT STDDEV(n::bigint) OVER (ORDER BY i ROWS BETWEEN CURRENT ROW AND UNBOUNDED FOLLOWING) " +
				"FROM (VALUES(1,NULL),(2,600),(3,470),(4,170),(5,430),(6,300)) r(i,n)",
			want: []string{
				"164.711869639076103",
				"164.711869639076103",
				"135.984067694221688",
				"130",
				"91.9238815542511782",
				"",
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows := runQuery(t, ctx, c.sql)
			if len(rows) != len(c.want) {
				t.Fatalf("%s: rows=%d want %d", c.sql, len(rows), len(c.want))
			}
			for i, w := range c.want {
				got := rows[i][0].Format()
				if got != w {
					t.Errorf("%s\n  row[%d] got  %q\n  row[%d] want %q (PostgreSQL 18.3)", c.sql, i, got, i, w)
				}
			}
		})
	}
}

// TestWindowAggregateFunctionsBoolAndOrArrayAgg covers bool_and/bool_or with
// a 2-row lookahead frame and array_agg with a 1-row lookbehind frame —
// window.out lines ~5321-5330 and ~5416-5424 (array_agg's VALUES source
// mirrors the oracle's generate_series+boolean-cast-offset semantics: the
// offset there evaluates to a plain 1, so the frame is equivalent).
func TestWindowAggregateFunctionsBoolAndOrArrayAgg(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	t.Run("bool_and_bool_or", func(t *testing.T) {
		rows := runQuery(t, ctx,
			"SELECT i, b, bool_and(b) OVER w, bool_or(b) OVER w "+
				"FROM (VALUES (1,true), (2,true), (3,false), (4,false), (5,true)) v(i,b) "+
				"WINDOW w AS (ORDER BY i ROWS BETWEEN CURRENT ROW AND 1 FOLLOWING) "+
				"ORDER BY i")
		want := [][4]string{
			{"1", "t", "t", "t"},
			{"2", "t", "f", "t"},
			{"3", "f", "f", "f"},
			{"4", "f", "f", "t"},
			{"5", "t", "t", "t"},
		}
		if len(rows) != len(want) {
			t.Fatalf("rows=%d want %d", len(rows), len(want))
		}
		for i, w := range want {
			for col := range w {
				got := rows[i][col].Format()
				if got != w[col] {
					t.Errorf("row[%d] col[%d] got %q want %q", i, col, got, w[col])
				}
			}
		}
	})

	t.Run("array_agg", func(t *testing.T) {
		rows := runQuery(t, ctx,
			"SELECT (array_agg(i) OVER (ORDER BY i ROWS BETWEEN 1 PRECEDING AND CURRENT ROW))::text "+
				"FROM (VALUES(1),(2),(3),(4),(5)) v(i)")
		want := []string{"{1}", "{1,2}", "{2,3}", "{3,4}", "{4,5}"}
		if len(rows) != len(want) {
			t.Fatalf("rows=%d want %d", len(rows), len(want))
		}
		for i, w := range want {
			got := rows[i][0].Format()
			if got != w {
				t.Errorf("row[%d] got %q want %q", i, got, w)
			}
		}
	})
}

// TestWindowNonAggregateNonWindowFuncStillErrors is the negative twin: a
// scalar function (not a window function, not an aggregate) with OVER()
// still raises 0A000 — PostgreSQL's ParseFuncOrColumn/transformWindowFuncCall
// reject exactly this class of name, and the M0134-0022b widening of
// analyzeWindowFuncCall's default arm must not blanket-accept everything.
func TestWindowNonAggregateNonWindowFuncStillErrors(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	_, err := runQueryWithErr(ctx, "SELECT abs(x) OVER () FROM (VALUES(1)) v(x)")
	if err == nil {
		t.Fatalf("abs(x) OVER () did not error, want 0A000")
	}
	if !strings.Contains(err.Error(), "0A000") {
		t.Fatalf("err = %q, want it to carry SQLSTATE 0A000", err.Error())
	}
}
