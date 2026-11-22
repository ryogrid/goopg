package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestSelectListSRFInsideExpression pins M0118-0008: a set-returning function
// nested inside a larger SELECT-list scalar expression (e.g.
// `generate_series(1,n) % 4`) must expand to one row per series element with the
// enclosing expression applied to each, not collapse to a single row holding the
// SRF's start value. The latter silently dropped rows — `INSERT INTO t SELECT
// generate_series(1,1000) % 4` inserted exactly one row instead of 1000.
func TestSelectListSRFInsideExpression(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	collect := func(sql string) []int64 {
		t.Helper()
		rows := runQuery(t, ctx, sql)
		out := make([]int64, 0, len(rows))
		for _, r := range rows {
			v, _ := datumInt64(r[0])
			out = append(out, v)
		}
		return out
	}

	eq := func(name string, got, want []int64) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: got %d rows %v, want %d rows %v", name, len(got), got, len(want), want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: row %d = %d, want %d (got=%v)", name, i, got[i], want[i], got)
			}
		}
	}

	// Nested in a binary expression: one row per element, modulo applied.
	eq("gs % 4",
		collect("SELECT generate_series(1,10) % 4"),
		[]int64{1, 2, 3, 0, 1, 2, 3, 0, 1, 2})

	// Nested with addition.
	eq("gs + 100",
		collect("SELECT generate_series(1,5) + 100"),
		[]int64{101, 102, 103, 104, 105})

	// Three-arg generate_series with a step, wrapped in multiplication.
	eq("gs(2,10,2) * 10",
		collect("SELECT generate_series(2,10,2) * 10"),
		[]int64{20, 40, 60, 80, 100})

	// Regression: a bare SRF in the target list still expands (unchanged path).
	eq("bare gs",
		collect("SELECT generate_series(1,4)"),
		[]int64{1, 2, 3, 4})
}

// TestSelectListNestedUnnestAndZippedSRFs pins M0149-0001h1a78: unnest nested
// in an expression expands like generate_series (it used to fail with
// "function unnest does not exist", or return one NULL row under abs()), and
// every SRF in one expression runs in lockstep with the shorter NULL-padded,
// as PG's ProjectSet does (`unnest(array[1,2]) * generate_series(1,3)` is
// 1, 4, NULL).
func TestSelectListNestedUnnestAndZippedSRFs(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	check := func(sql string, want []string) {
		t.Helper()
		rows := runQuery(t, ctx, sql)
		if len(rows) != len(want) {
			t.Fatalf("%s: got %d rows, want %d", sql, len(rows), len(want))
		}
		for i, w := range want {
			got := "NULL"
			if !rows[i][0].IsNull() {
				got = datumToString(rows[i][0])
			}
			if got != w {
				t.Fatalf("%s: row %d = %s, want %s", sql, i, got, w)
			}
		}
	}
	check("SELECT unnest(array[1,2]) + 1", []string{"2", "3"})
	check("SELECT abs(unnest(array[-1,2]))", []string{"1", "2"})
	check("SELECT upper(unnest(array['a','b']))", []string{"A", "B"})
	check("SELECT unnest(array[1,2]) * generate_series(1,3)", []string{"1", "4", "NULL"})
	check("SELECT (generate_series(1,2) + generate_series(1,3)) * 10", []string{"20", "40", "NULL"})
	check("SELECT (regexp_matches('abab', '(b)', 'g'))[1] || '!'", []string{"b!", "b!"})
	if err := runDDL(t, ctx, "CREATE FUNCTION h78f() RETURNS SETOF int LANGUAGE sql AS 'SELECT 1 UNION ALL SELECT 2'"); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}
	check("SELECT h78f() + 10", []string{"11", "12"})

	// FigureColname names a subscript by the subscripted expression.
	for sql, want := range map[string]string{
		"SELECT (regexp_matches('ab', 'a'))[1]":        "regexp_matches",
		"SELECT (array[1,2])[1], generate_series(1,2)": "array",
		"SELECT (array[1,2])[1]":                       "array",
		"SELECT (regexp_match('ab', 'a'))[1]":          "regexp_match",
	} {
		stmts, err := parser.Parse(sql)
		if err != nil {
			t.Fatalf("Parse(%q): %v", sql, err)
		}
		plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
		if err != nil {
			t.Fatalf("Plan(%q): %v", sql, err)
		}
		if got := plan.Output()[0].Name; got != want {
			t.Errorf("%s: column 1 is %q, want %q", sql, got, want)
		}
	}
}
