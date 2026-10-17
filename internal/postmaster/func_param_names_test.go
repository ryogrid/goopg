package postmaster

import (
	"strings"
	"testing"
)

// TestFunctionParameterNamesAndOutArgs pins M0146-0084. polymorphism.sql's
// "named parameters are not unique" definitions must fail (functioncmds.c
// interpret_function_parameter_list: two inputs, or two outputs, may not share
// a name) — goopg created them, and the later testpolym(37) call then resolved
// against leftovers, so the case passed or failed with session history. A
// call supplies the input parameters only, so a function with an OUT
// parameter is callable with its IN arguments, and $n numbers inputs only.
// Expectations are PostgreSQL 18.3's.
func TestFunctionParameterNamesAndOutArgs(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, sql := range []string{
		`CREATE FUNCTION testpolym(a int, a int) RETURNS int AS $$ SELECT 1 $$ LANGUAGE sql`,
		`CREATE FUNCTION testpolym(int, out a int, out a int) RETURNS int AS $$ SELECT 1 $$ LANGUAGE sql`,
		`CREATE FUNCTION testpolym(out a int, inout a int) RETURNS int AS $$ SELECT 1 $$ LANGUAGE sql`,
		`CREATE FUNCTION testpolym(a int, inout a int) RETURNS int AS $$ SELECT 1 $$ LANGUAGE sql`,
	} {
		if _, e := setClauseQuery(t, conn, sql); !strings.Contains(e, `parameter name "a" used more than once`) || !strings.Contains(e, "C42P13") {
			t.Errorf("%s: error = %q, want 42P13 parameter name \"a\" used more than once", sql, e)
		}
	}
	for _, c := range []struct {
		sql  string
		want []string
	}{
		{`CREATE FUNCTION testpolym(a int, out a int) RETURNS int AS $$ SELECT $1 $$ LANGUAGE sql`, nil},
		{"SELECT testpolym(37)", []string{"37"}},
		{"DROP FUNCTION testpolym(int)", nil},
		{`CREATE FUNCTION testpolym(a int) RETURNS TABLE(a int) AS $$ SELECT $1 $$ LANGUAGE sql`, nil},
		{"SELECT * FROM testpolym(37)", []string{"37"}},
		{`CREATE FUNCTION outfirst(out b int, a int) RETURNS int AS $$ SELECT $1 * 2 $$ LANGUAGE sql`, nil},
		{"SELECT outfirst(5)", []string{"10"}},
	} {
		got, e := setClauseQuery(t, conn, c.sql)
		if e != "" || strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s = %q (err %q), want %q", c.sql, got, e, c.want)
		}
	}
}
