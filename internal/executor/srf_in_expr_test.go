package executor

import (
	"strings"
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

// TestExplainSelectListProjectSetEstimates pins M0149-0001h1a78a against PG
// 18.3's EXPLAIN: a SELECT-list ProjectSet's rows are its input's times the
// largest SRF estimate (create_set_projection_path), and an SRF inside an
// expression gets the Result PG plans above the ProjectSet
// (split_pathtarget_at_srfs, create_projection_path) — at the top level as
// well as under a parent. goopg used to print rows=1 and no Result.
func TestExplainSelectListProjectSetEstimates(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	cases := map[string][]string{
		"EXPLAIN SELECT generate_series(1,2)": {
			"ProjectSet  (cost=0.00..0.03 rows=2 width=4)",
			"  ->  Result  (cost=0.00..0.01 rows=1 width=0)"},
		// M0149-0001h1a78d: VERBOSE Output lists of the split targetlist.
		"EXPLAIN VERBOSE SELECT generate_series(1,2) + 1": {
			"Result  (cost=0.00..0.06 rows=2 width=4)",
			"  Output: ((generate_series(1, 2)) + 1)",
			"  ->  ProjectSet  (cost=0.00..0.03 rows=2 width=4)",
			"        Output: generate_series(1, 2)"},
		"EXPLAIN VERBOSE SELECT 5, generate_series(1,3) % 2, generate_series(1,2)": {
			"Result  (cost=0.00..0.09 rows=3 width=12)",
			"  Output: 5, ((generate_series(1, 3)) % 2), (generate_series(1, 2))",
			"  ->  ProjectSet  (cost=0.00..0.04 rows=3 width=8)",
			"        Output: generate_series(1, 3), generate_series(1, 2)"},
		"EXPLAIN VERBOSE SELECT (regexp_matches('abab', 'b', 'g'))[1]": {
			"Result  (cost=0.00..0.19 rows=10 width=32)",
			"  Output: (regexp_matches('abab'::text, 'b'::text, 'g'::text))[1]"},
		"EXPLAIN VERBOSE SELECT generate_series(1,3), unnest(array['a','b'])": {
			"ProjectSet  (cost=0.00..0.04 rows=3 width=36)",
			"  Output: generate_series(1, 3), unnest('{a,b}'::text[])"},
		"EXPLAIN VERBOSE SELECT generate_series(1,3) g, 7 ORDER BY 1 LIMIT 2": {
			"Limit  (cost=0.06..0.06 rows=2 width=8)",
			"  Output: (generate_series(1, 3)), 7"},
		"EXPLAIN SELECT generate_series(1,2) + 1": {
			"Result  (cost=0.00..0.06 rows=2 width=4)",
			"  ->  ProjectSet  (cost=0.00..0.03 rows=2 width=4)"},
		"EXPLAIN SELECT 5, generate_series(1,3) % 2, generate_series(1,2)": {
			"Result  (cost=0.00..0.09 rows=3 width=12)",
			"  ->  ProjectSet  (cost=0.00..0.04 rows=3 width=8)"},
		"EXPLAIN SELECT generate_series(1,2) % 2 ORDER BY 1": {
			"Sort  (cost=0.07..0.07 rows=2 width=4)",
			"  Sort Key: (((generate_series(1, 2)) % 2))",
			"  ->  Result  (cost=0.00..0.06 rows=2 width=4)",
			"        ->  ProjectSet  (cost=0.00..0.03 rows=2 width=4)"},
		"EXPLAIN SELECT (regexp_matches('abab', 'b', 'g'))[1]": {
			"Result  (cost=0.00..0.19 rows=10 width=32)",
			"  ->  ProjectSet  (cost=0.00..0.07 rows=10 width=32)"},
		"EXPLAIN SELECT count(*) FROM (SELECT unnest('{1,2,3}'::int[]) + 1) s": {
			"Aggregate  (cost=0.11..0.12 rows=1 width=8)",
			"  ->  Result  (cost=0.00..0.08 rows=3 width=4)",
			"        ->  ProjectSet  (cost=0.00..0.03 rows=3 width=4)"},
	}
	for sql, want := range cases {
		rows := runQuery(t, ctx, sql)
		if len(rows) < len(want) {
			t.Fatalf("%s: %d lines, want at least %d", sql, len(rows), len(want))
		}
		for i, w := range want {
			if got := datumToString(rows[i][0]); got != w {
				t.Errorf("%s: line %d = %q, want %q", sql, i, got, w)
			}
		}
	}
}

// TestArraySubscriptArithmetic pins M0149-0001h1a78b: a subscript over an
// ARRAY constructor or a date[] yields its element type's datum, so
// arithmetic on it is PG's (int4pl, date_pli), not "requires integer
// operands".
func TestArraySubscriptArithmetic(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for sql, want := range map[string]string{
		"SELECT (ARRAY[1,2])[1] + 1":                  "2",
		"SELECT (ARRAY[1,2])[2] * 3":                  "6",
		"SELECT ('{2020-01-31}'::date[])[1] + 1":      "2020-02-01",
		"SELECT (ARRAY['2020-03-01'::date])[1] - 1":   "2020-02-29",
		"SELECT ('{2020-01-01,infinity}'::date[])[2]": "infinity",
	} {
		rows := runQuery(t, ctx, sql)
		if len(rows) != 1 {
			t.Fatalf("%s: %d rows", sql, len(rows))
		}
		if got := formatDatumDateStyle(rows[0][0], ctx); got != want {
			t.Errorf("%s = %s, want %s", sql, got, want)
		}
	}
}

// TestStringToArray pins M0149-0001h1a78c against PG 18.3's text_to_array
// (varlena.c split_text): the function is not strict, a NULL delimiter splits
// into characters, an empty delimiter keeps the input whole, an empty input
// is {}, and elements equal to null_string become NULL. An element spelling
// NULL is quoted, as array_out does.
func TestStringToArray(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for sql, want := range map[string]string{
		"SELECT string_to_array('a,b,,c', ',')":     "{a,b,\"\",c}",
		"SELECT string_to_array('abc', null)":       "{a,b,c}",
		"SELECT string_to_array('abc', '')":         "{abc}",
		"SELECT string_to_array('', ',')":           "{}",
		"SELECT string_to_array('a,*,b', ',', '*')": "{a,NULL,b}",
		"SELECT string_to_array('aXbXXc', 'XX')":    "{aXb,c}",
		"SELECT string_to_array('NULL,x', ',')":     "{\"NULL\",x}",
		"SELECT string_to_array('x y,{z}', ',')":    "{\"x y\",\"{z}\"}",
		"SELECT (string_to_array('a,b', ','))[2]":   "b",
		"SELECT string_to_array(null, ',') IS NULL": "t",
	} {
		rows := runQuery(t, ctx, sql)
		if len(rows) != 1 {
			t.Fatalf("%s: %d rows", sql, len(rows))
		}
		if got := rows[0][0].Format(); got != want {
			t.Errorf("%s = %s, want %s", sql, got, want)
		}
	}
}

// TestStringToTable pins M0149-0001h1a78f against PG 18.3's text_to_table:
// the FROM form and the target-list form split like string_to_array (NULL
// delimiter per character, null_string → NULL, no rows for NULL or ”), and
// EXPLAIN names the function with its prorows of 1000.
func TestStringToTable(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	col := func(sql string) []string {
		t.Helper()
		var out []string
		for _, r := range runQuery(t, ctx, sql) {
			if r[0].IsNull() {
				out = append(out, "NULL")
				continue
			}
			out = append(out, r[0].Format())
		}
		return out
	}
	for sql, want := range map[string]string{
		"SELECT * FROM string_to_table('a,b,,c', ',')":       "a||b||||c",
		"SELECT * FROM string_to_table('1,NA,3', ',', 'NA')": "1||NULL||3",
		"SELECT * FROM string_to_table('abc', null)":         "a||b||c",
		"SELECT count(*) FROM string_to_table('', ',')":      "0",
		"SELECT count(*) FROM string_to_table(null, ',')":    "0",
		"SELECT string_to_table('p,q', ',')":                 "p||q",
		"SELECT upper(string_to_table('p,q', ','))":          "P||Q",
		"EXPLAIN SELECT * FROM string_to_table('a,b', ',')":  "Function Scan on string_to_table  (cost=0.00..10.00 rows=1000 width=32)",
	} {
		if got := strings.Join(col(sql), "||"); got != want {
			t.Errorf("%s = %q, want %q", sql, got, want)
		}
	}
}
