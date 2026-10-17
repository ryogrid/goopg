package postmaster

import (
	"strings"
	"testing"
)

// TestSecurityDefinerRunsAsOwner pins M0146-0157: a SECURITY DEFINER
// routine runs as its owner (fmgr_security_definer's
// SetUserIdAndSecContext(proowner, SECURITY_LOCAL_USERID_CHANGE)) — for
// current_user and for privilege checks — and the caller's identity comes
// back when it returns, also after an error. CREATE FUNCTION / PROCEDURE
// record the creating role as proowner, which the switch depends on. Every
// expectation is PostgreSQL 18.3's answer to the same statements.
func TestSecurityDefinerRunsAsOwner(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, sql := range []string{
		"CREATE ROLE sd_owner",
		"CREATE ROLE sd_caller",
		"CREATE TABLE secret(v text)",
		"INSERT INTO secret VALUES ('hidden')",
		"GRANT SELECT ON secret TO sd_owner",
		"GRANT CREATE ON SCHEMA public TO sd_owner",
		"SET ROLE sd_owner",
		`CREATE FUNCTION who_def() RETURNS text LANGUAGE sql SECURITY DEFINER AS $$ SELECT current_user::text || '/' || session_user::text $$`,
		`CREATE FUNCTION peek() RETURNS text LANGUAGE sql SECURITY DEFINER AS $$ SELECT v FROM secret $$`,
		`CREATE FUNCTION peek_inv() RETURNS text LANGUAGE sql AS $$ SELECT v FROM secret $$`,
		`CREATE FUNCTION boom() RETURNS int LANGUAGE plpgsql SECURITY DEFINER AS $$ BEGIN RAISE EXCEPTION 'as %', current_user; END $$`,
		`CREATE FUNCTION trysetrole() RETURNS text LANGUAGE plpgsql SECURITY DEFINER AS $$ BEGIN SET ROLE sd_caller; RETURN 'x'; END $$`,
		`CREATE PROCEDURE pdef(INOUT r text DEFAULT NULL) LANGUAGE sql SECURITY DEFINER AS $$ SELECT current_user::text $$`,
		"RESET ROLE",
		"SET ROLE sd_caller",
	} {
		if _, e := setClauseQuery(t, conn, sql); e != "" {
			t.Fatalf("%s: %q", sql, e)
		}
	}
	for _, c := range []struct {
		sql  string
		want []string
	}{
		{"SELECT who_def(), current_user", []string{"sd_owner/postgres", "sd_caller"}},
		{"SELECT peek()", []string{"hidden"}},
		{"CALL pdef()", []string{"sd_owner"}},
	} {
		got, e := setClauseQuery(t, conn, c.sql)
		if e != "" || strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s = %q (err %q), want %q", c.sql, got, e, c.want)
		}
	}
	if _, e := setClauseQuery(t, conn, "SELECT peek_inv()"); !strings.Contains(e, "permission denied for table secret") {
		t.Errorf("SELECT peek_inv() error = %q, want permission denied (an invoker function runs as the caller)", e)
	}
	if _, e := setClauseQuery(t, conn, "SELECT boom()"); !strings.Contains(e, "as sd_owner") {
		t.Errorf("SELECT boom() error = %q, want as sd_owner", e)
	}
	if got, _ := setClauseQuery(t, conn, "SELECT current_user"); strings.Join(got, "") != "sd_caller" {
		t.Errorf("current_user after a failed definer call = %q, want sd_caller", got)
	}
	if _, e := setClauseQuery(t, conn, "SELECT trysetrole()"); !strings.Contains(e, `cannot set parameter "role" within security-definer function`) {
		t.Errorf("SET ROLE inside a definer: error = %q", e)
	}
}
