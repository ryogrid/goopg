package postmaster

import (
	"net"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/libpq"
)

// allRowsQuery runs sql and returns every DataRow's cells, flattened in
// order, and the error payload (empty on success).
func allRowsQuery(t *testing.T, conn net.Conn, sql string) ([]string, string) {
	t.Helper()
	writeQuery(t, conn, sql)
	frames := readUntilReady(t, conn)
	var cells []string
	for _, f := range frames {
		if f.Type == libpq.MsgDataRow {
			cells = append(cells, dataRowColumns(t, f.Payload)...)
		}
	}
	_, errPayload := commandCompleteTag(t, frames)
	return cells, errPayload
}

// TestOutParameterFunctions pins M0146-0165, the OUT-parameter function model
// (functioncmds.c interpret_function_parameter_list, funcapi.c
// build_function_result_tupdesc_d, pl_comp.c / pl_exec.c out-parameter
// rows). Expectations are PostgreSQL 18.3's.
func TestOutParameterFunctions(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, c := range []struct {
		sql  string
		want []string
		err  string
	}{
		// RETURNS may be omitted: the result type is derived from the outputs.
		{sql: "CREATE FUNCTION f4(a int, OUT b int, OUT c text) AS $$ SELECT $1, 'x' $$ LANGUAGE sql"},
		{sql: "SELECT f4(3)", want: []string{"(3,x)"}},
		{sql: "SELECT * FROM f4(3)", want: []string{"3", "x"}},
		{sql: "SELECT c, b FROM f4(3)", want: []string{"x", "3"}},
		{sql: "SELECT (f4(3)).c", want: []string{"x"}},
		{sql: "CREATE FUNCTION e1(a int) AS $$ SELECT 1 $$ LANGUAGE sql",
			err: "function result type must be specified"},
		{sql: "CREATE FUNCTION e2(OUT a int) RETURNS record AS $$ SELECT 1 $$ LANGUAGE sql",
			err: "function result type must be integer because of OUT parameters"},
		// PL/pgSQL output parameters are variables the function returns.
		{sql: "CREATE FUNCTION f5(x int, OUT y int) LANGUAGE plpgsql AS $$ BEGIN y := x * 10; END $$"},
		{sql: "SELECT f5(4)", want: []string{"40"}},
		{sql: "CREATE FUNCTION f8(x int, OUT y int, OUT z text) LANGUAGE plpgsql AS $$ BEGIN y := x * 2; z := 'z' || x; RETURN; END $$"},
		{sql: "SELECT f8(3), (f8(3)).z", want: []string{"(6,z3)", "z3"}},
		{sql: "CREATE FUNCTION f9(INOUT x int, OUT y int) LANGUAGE plpgsql AS $$ BEGIN y := x + 1; x := x * 2; END $$"},
		{sql: "SELECT * FROM f9(5)", want: []string{"10", "6"}},
		{sql: "CREATE FUNCTION f13(x int, OUT y int) LANGUAGE plpgsql AS $$ BEGIN END $$"},
		{sql: "SELECT f13(1) IS NULL", want: []string{"t"}},
		// RETURN NEXT without an expression emits the output row; named
		// columns of a FROM-clause function resolve.
		{sql: "CREATE FUNCTION t1(n int) RETURNS TABLE(a int, b text) LANGUAGE plpgsql AS $$ BEGIN FOR i IN 1..n LOOP a := i; b := 'v' || i; RETURN NEXT; END LOOP; END $$"},
		{sql: "SELECT a, b FROM t1(2)", want: []string{"1", "v1", "2", "v2"}},
		{sql: "SELECT t1(2)", want: []string{"(1,v1)", "(2,v2)"}},
		{sql: "CREATE FUNCTION s1(n int, OUT a int, OUT b text) RETURNS SETOF record AS $$ SELECT g, 's' || g FROM generate_series(1, $1) g $$ LANGUAGE sql"},
		{sql: "SELECT b FROM s1(2)", want: []string{"s1", "s2"}},
		// IN is not printed for a function.
		{sql: "SELECT pg_get_function_arguments('f8'::regproc)", want: []string{"x integer, OUT y integer, OUT z text"}},
		// An OUT parameter before VARIADIC does not shift the variadic
		// position.
		{sql: "CREATE FUNCTION vf(OUT n int, VARIADIC xs int[]) AS $$ SELECT array_length($1, 1) $$ LANGUAGE sql"},
		{sql: "SELECT vf(1, 2, 3)", want: []string{"3"}},
		// record_out doubles an embedded quote.
		{sql: "CREATE FUNCTION q1(OUT a int, OUT b text) AS $$ SELECT 1, 'q\"r' $$ LANGUAGE sql"},
		{sql: "SELECT q1(), (SELECT b FROM q1())", want: []string{`(1,"q""r")`, `q"r`}},
	} {
		got, e := allRowsQuery(t, conn, c.sql)
		if c.err != "" {
			if !strings.Contains(e, c.err) {
				t.Errorf("%s: error %q, want %q", c.sql, e, c.err)
			}
			continue
		}
		if e != "" || strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s = %q (err %q), want %q", c.sql, got, e, c.want)
		}
	}
}
