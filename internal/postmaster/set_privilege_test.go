package postmaster

import (
	"strings"
	"testing"
)

// TestSusetParameterNeedsPrivilege pins M0146-0158: a non-superuser role
// cannot SET, SET LOCAL, RESET or set_config() a PGC_SUSET parameter, nor
// attach one to a routine, unless it holds SET on the parameter; a
// superuser-owned SECURITY DEFINER routine may apply one in its SET clause.
// PostgreSQL 18.3's answers to the same statements.
func TestSusetParameterNeedsPrivilege(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, sql := range []string{
		"CREATE ROLE sp_u",
		"GRANT CREATE ON SCHEMA public TO sp_u",
		`CREATE FUNCTION sudef() RETURNS text LANGUAGE sql SECURITY DEFINER SET log_min_messages = 'debug1' AS $$ SELECT current_setting('log_min_messages') $$`,
		"SET ROLE sp_u",
	} {
		if _, e := setClauseQuery(t, conn, sql); e != "" {
			t.Fatalf("%s: %q", sql, e)
		}
	}
	const denied = `permission denied to set parameter "log_min_messages"`
	for _, sql := range []string{
		"SET log_min_messages = debug1",
		"BEGIN; SET LOCAL log_min_messages = debug1; COMMIT",
		"RESET log_min_messages",
		"SELECT set_config('log_min_messages', 'debug1', false)",
		`CREATE FUNCTION cf() RETURNS int LANGUAGE sql SET log_min_messages = 'debug1' AS $$ SELECT 1 $$`,
	} {
		_, e := setClauseQuery(t, conn, sql)
		if !strings.Contains(e, denied) || !strings.Contains(e, "C42501") {
			t.Errorf("%s: error = %q, want 42501 %s", sql, e, denied)
		}
		_, _ = setClauseQuery(t, conn, "ROLLBACK")
	}
	if _, e := setClauseQuery(t, conn, "SET work_mem = '5MB'"); e != "" {
		t.Errorf("user-settable parameter: %q", e)
	}
	if got, e := setClauseQuery(t, conn, "SELECT sudef()"); e != "" || strings.Join(got, "") != "debug1" {
		t.Errorf("superuser-owned definer clause = %q (err %q), want debug1", got, e)
	}
	if got, _ := setClauseQuery(t, conn, "SHOW log_min_messages"); strings.Join(got, "") != "warning" {
		t.Errorf("log_min_messages after the calls = %q, want warning", got)
	}
}
