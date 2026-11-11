package executor

import (
	"strconv"
	"testing"
)

// TestRewriteSQLNamedParamsQualified pins M0148-0001j4 item (3)'s named
// parameter rewrite: PG's sql_fn_post_column_ref resolves a two-part name
// whose FIRST part is a parameter as a field of that parameter, so `s.total`
// becomes `($1).total` (`$1.total` is not valid syntax); a parameter name as
// the SECOND part (`t.k`) is a column reference and stays.
func TestRewriteSQLNamedParamsQualified(t *testing.T) {
	for in, want := range map[string]string{
		"select s.total":         "select ($1).total",
		"select (s).total + s.n": "select ($1).total + ($1).n",
		"select s.*":             "select ($1).*",
		"select t.total + k from tab t where t.k = k": "select t.total + $2 from tab t where t.k = $2",
		"select 's.total', s":                         "select 's.total', $1",
		"select f(s) * 10":                            "select f($1) * 10",
	} {
		if got := rewriteSQLNamedParams(in, []string{"s", "k"}); got != want {
			t.Errorf("rewrite(%q) = %q, want %q", in, got, want)
		}
	}
	// `funcname.param` (regress rowtypes fcompos3: `fcompos1(fcompos3.v.*)`).
	for in, want := range map[string]string{
		"select fcompos1(fcompos3.v.*)": "select fcompos1(($1).*)",
		"select FCompos3.v":             "select $1",
		"select other.v, x.fcompos3.v":  "select other.v, x.fcompos3.v",
	} {
		if got := rewriteSQLNamedParamsFor(in, "fcompos3", []string{"v"}); got != want {
			t.Errorf("rewriteFor(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSQLFunctionCompositeParamFieldSelect pins the planner half: inside a
// SQL function body $n carries the argument's declared type
// (sql_fn_parser_setup), so `($1).f` / `(s).f` / `s.f` select from the
// composite argument instead of failing "column notation .f applied to type
// unknown". PL/pgSQL's `(v).f` takes the `v.f` path. PG 18.3 returns the
// values asserted here.
func TestSQLFunctionCompositeParamFieldSelect(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TYPE st AS (total int, n int)",
		"CREATE FUNCTION f1(s st) RETURNS int LANGUAGE sql AS 'SELECT (s).total'",
		"CREATE FUNCTION f2(s st) RETURNS int LANGUAGE sql AS 'SELECT s.total'",
		"CREATE FUNCTION f3(st) RETURNS int LANGUAGE sql AS 'SELECT ($1).total'",
		"CREATE FUNCTION f4(s st) RETURNS int LANGUAGE sql AS 'SELECT (s).total + s.n'",
		"CREATE FUNCTION p5(s st) RETURNS int LANGUAGE plpgsql AS 'BEGIN RETURN (s).total; END'",
	} {
		if err := runDDL(t, ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for q, want := range map[string]int64{
		"SELECT f1(ROW(5,2)::st)": 5,
		"SELECT f2(ROW(5,2)::st)": 5,
		"SELECT f3(ROW(5,2)::st)": 5,
		"SELECT f4(ROW(5,2)::st)": 7,
		"SELECT p5(ROW(5,2)::st)": 5,
	} {
		rows, err := runQueryErr(t, ctx, q)
		if err != nil {
			t.Errorf("%s: %v", q, err)
			continue
		}
		if len(rows) != 1 || len(rows[0]) != 1 || rows[0][0].IsNull() {
			t.Errorf("%s: rows = %v, want %d", q, rows, want)
			continue
		}
		if got := rows[0][0].Int; got != want {
			if s := rows[0][0].StringValue(); s != "" && s == strconv.FormatInt(want, 10) {
				continue
			}
			t.Errorf("%s = %v, want %d", q, rows[0][0], want)
		}
	}
}
