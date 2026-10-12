package postmaster

import (
	"net"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/libpq"
)

// setClauseQuery runs one simple query and returns the first DataRow's
// columns and the ErrorResponse payload ("" when the query succeeded).
func setClauseQuery(t *testing.T, conn net.Conn, sql string) ([]string, string) {
	t.Helper()
	writeQuery(t, conn, sql)
	frames := readUntilReady(t, conn)
	var row []string
	for _, f := range frames {
		if f.Type == libpq.MsgDataRow && row == nil {
			row = dataRowColumns(t, f.Payload)
		}
	}
	_, errPayload := commandCompleteTag(t, frames)
	return row, errPayload
}

// TestFunctionSetClauseApplied pins M0146-0155: a routine's SET clauses
// (pg_proc.proconfig) are applied for the length of each call and restored
// afterwards, as fmgr_security_definer does (NewGUCNestLevel +
// ProcessGUCArray(GUC_ACTION_SAVE) + AtEOXact_GUC). Every expectation is
// PostgreSQL 18.3's answer to the same statements.
func TestFunctionSetClauseApplied(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, sql := range []string{
		"CREATE SCHEMA s1",
		"CREATE TABLE public.t(v text)",
		"INSERT INTO public.t VALUES ('public')",
		"CREATE TABLE s1.t(v text)",
		"INSERT INTO s1.t VALUES ('s1')",
		"SET work_mem = '4MB'",
		`CREATE FUNCTION sp() RETURNS text LANGUAGE sql SET search_path = s1 AS $$ SELECT v FROM t $$`,
		`CREATE FUNCTION pp() RETURNS text LANGUAGE plpgsql SET search_path = s1 AS $$ DECLARE r text; BEGIN SELECT v INTO r FROM t; RETURN r || ' ' || current_setting('search_path'); END $$`,
		`CREATE FUNCTION sl() RETURNS text LANGUAGE plpgsql SET work_mem = '3MB' AS $$ BEGIN SET LOCAL work_mem = '5MB'; RETURN current_setting('work_mem'); END $$`,
		`CREATE FUNCTION ps() RETURNS text LANGUAGE plpgsql SET work_mem = '3MB' AS $$ BEGIN SET work_mem = '6MB'; RETURN current_setting('work_mem'); END $$`,
		`CREATE FUNCTION inner1() RETURNS text LANGUAGE sql SET work_mem = '8MB' AS $$ SELECT current_setting('work_mem') $$`,
		`CREATE FUNCTION outer1() RETURNS text LANGUAGE plpgsql SET work_mem = '9MB' AS $$ DECLARE a text; BEGIN a := inner1(); RETURN a || ' ' || current_setting('work_mem'); END $$`,
		`CREATE FUNCTION boom() RETURNS int LANGUAGE plpgsql SET work_mem = '7MB' AS $$ BEGIN RAISE EXCEPTION 'boom %', current_setting('work_mem'); END $$`,
		`CREATE FUNCTION srf() RETURNS SETOF text LANGUAGE sql SET search_path = s1 AS $$ SELECT v FROM t $$`,
		`CREATE PROCEDURE prc() LANGUAGE plpgsql SET work_mem = '3MB' AS $$ BEGIN COMMIT; END $$`,
		`CREATE PROCEDURE pr(INOUT x text DEFAULT NULL) LANGUAGE sql SET search_path = s1 AS $$ SELECT v FROM t $$`,
		"CREATE TABLE trg(a int, b text)",
		`CREATE FUNCTION trgf() RETURNS trigger LANGUAGE plpgsql SET search_path = s1 AS $$ BEGIN NEW.b := (SELECT v FROM t); RETURN NEW; END $$`,
		"CREATE TRIGGER tg BEFORE INSERT ON trg FOR EACH ROW EXECUTE FUNCTION trgf()",
	} {
		if _, e := setClauseQuery(t, conn, sql); e != "" {
			t.Fatalf("%s: %q", sql, e)
		}
	}

	for _, c := range []struct {
		sql  string
		want []string
	}{
		// The SET clause decides the body's name resolution, then goes away.
		{"SELECT sp(), current_setting('search_path')", []string{"s1", `"$user", public`}},
		{"SELECT pp()", []string{"s1 s1"}},
		{"SELECT * FROM srf()", []string{"s1"}},
		// CREATE PROCEDURE ... SET is recorded (it used to be dropped) and applied.
		{"CALL pr()", []string{"s1"}},
		{"INSERT INTO trg VALUES (1) RETURNING b", []string{"s1"}},
		// SET LOCAL on a saved variable is undone when the function returns.
		{"SELECT sl(), current_setting('work_mem')", []string{"5MB", "4MB"}},
		// A nested SET clause wins inside, and each level restores its own.
		{"SELECT outer1(), current_setting('work_mem')", []string{"8MB 9MB", "4MB"}},
		// A plain SET overrides the clause and outlives the call.
		{"SELECT ps(), current_setting('work_mem')", []string{"6MB", "6MB"}},
	} {
		got, e := setClauseQuery(t, conn, c.sql)
		if e != "" {
			t.Errorf("%s: error %q", c.sql, e)
			continue
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s = %q, want %q", c.sql, got, c.want)
		}
	}

	if _, e := setClauseQuery(t, conn, "SET work_mem = '4MB'"); e != "" {
		t.Fatal(e)
	}
	// An error leaving the function restores the saved value.
	if _, e := setClauseQuery(t, conn, "SELECT boom()"); !strings.Contains(e, "boom 7MB") {
		t.Errorf("SELECT boom() error = %q, want boom 7MB", e)
	}
	if got, _ := setClauseQuery(t, conn, "SHOW work_mem"); strings.Join(got, "") != "4MB" {
		t.Errorf("work_mem after boom() = %q, want 4MB", got)
	}
	// A procedure with SET clauses is atomic (ExecuteCallStmt).
	if _, e := setClauseQuery(t, conn, "CALL prc()"); !strings.Contains(e, "invalid transaction termination") {
		t.Errorf("CALL prc() error = %q, want invalid transaction termination", e)
	}
}

// TestSearchPathOrderDecidesUnqualifiedName pins the lookup order the SET
// clause relies on: the first schema on the search path holding the name
// wins, even when public holds it too, and the cross-session plan cache does
// not serve a plan bound under a different search path.
func TestSearchPathOrderDecidesUnqualifiedName(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, sql := range []string{
		"CREATE SCHEMA s2",
		"CREATE TABLE public.u(v text)",
		"INSERT INTO public.u VALUES ('public')",
		"CREATE TABLE s2.u(v text)",
		"INSERT INTO s2.u VALUES ('s2')",
	} {
		if _, e := setClauseQuery(t, conn, sql); e != "" {
			t.Fatalf("%s: %q", sql, e)
		}
	}
	for _, c := range []struct{ set, want string }{
		{"RESET search_path", "public"},
		{"SET search_path = s2, public", "s2"},
		{"SET search_path = public, s2", "public"},
		{"SET search_path = s2", "s2"},
	} {
		if _, e := setClauseQuery(t, conn, c.set); e != "" {
			t.Fatalf("%s: %q", c.set, e)
		}
		got, e := setClauseQuery(t, conn, "SELECT v FROM u")
		if e != "" || strings.Join(got, "") != c.want {
			t.Errorf("after %s: SELECT v FROM u = %q (err %q), want %q", c.set, got, e, c.want)
		}
	}
}
