package server

// Unit coverage for `ALTER ROLE ... [IN DATABASE ...] SET/RESET` (root-0021
// follow-up, M0119-0004-ACLHEAP): parseAlterRoleConfig's parsing plus
// applyAlterRoleConfig's error/success paths, mirroring
// database_ddl_test.go's TestParseAlterDatabaseConfig for the
// complementary setrole != 0 half of pg_db_role_setting.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/sqlstate"
)

func TestParseAlterRoleConfig(t *testing.T) {
	cases := []struct {
		sql  string
		want alterRoleConfigOp
		ok   bool
	}{
		{
			sql:  "ALTER ROLE foo SET work_mem = '64MB'",
			want: alterRoleConfigOp{roleName: "foo", configName: "work_mem", configValue: "64MB"},
			ok:   true,
		},
		{
			sql:  "alter user foo set work_mem to '64MB';",
			want: alterRoleConfigOp{roleName: "foo", configName: "work_mem", configValue: "64MB"},
			ok:   true,
		},
		{
			sql: "ALTER ROLE foo IN DATABASE postgres SET work_mem = '64MB'",
			want: alterRoleConfigOp{
				roleName: "foo", hasDatabase: true, dbName: "postgres",
				configName: "work_mem", configValue: "64MB",
			},
			ok: true,
		},
		{
			sql: `ALTER ROLE "My Role" IN DATABASE "My DB" SET search_path TO public, pg_catalog`,
			want: alterRoleConfigOp{
				roleName: "My Role", hasDatabase: true, dbName: "My DB",
				configName: "search_path", configValue: "public,pg_catalog",
			},
			ok: true,
		},
		{
			sql:  "ALTER ROLE foo SET work_mem TO DEFAULT",
			want: alterRoleConfigOp{roleName: "foo", configName: "work_mem", reset: true},
			ok:   true,
		},
		{
			sql:  "ALTER ROLE foo RESET work_mem",
			want: alterRoleConfigOp{roleName: "foo", configName: "work_mem", reset: true},
			ok:   true,
		},
		{
			sql:  "ALTER ROLE foo IN DATABASE postgres RESET work_mem",
			want: alterRoleConfigOp{roleName: "foo", hasDatabase: true, dbName: "postgres", configName: "work_mem", reset: true},
			ok:   true,
		},
		{
			sql:  "ALTER ROLE foo RESET ALL",
			want: alterRoleConfigOp{roleName: "foo", resetAll: true},
			ok:   true,
		},
		{
			sql:  "ALTER ROLE foo IN DATABASE postgres RESET ALL",
			want: alterRoleConfigOp{roleName: "foo", hasDatabase: true, dbName: "postgres", resetAll: true},
			ok:   true,
		},
		// gram.y set_rest "special syntaxes" — valid inside ALTER ROLE's
		// SetResetClause exactly like a plain SET (same grammar production).
		{
			sql:  "ALTER ROLE foo SET TIME ZONE 'UTC'",
			want: alterRoleConfigOp{roleName: "foo", configName: "timezone", configValue: "UTC"},
			ok:   true,
		},
		{
			sql:  "ALTER ROLE foo SET TIME ZONE DEFAULT",
			want: alterRoleConfigOp{roleName: "foo", configName: "timezone", reset: true},
			ok:   true,
		},
		{
			sql:  "ALTER ROLE foo SET SCHEMA 'app'",
			want: alterRoleConfigOp{roleName: "foo", configName: "search_path", configValue: "app"},
			ok:   true,
		},
		{
			sql:  "ALTER ROLE foo SET NAMES 'utf8'",
			want: alterRoleConfigOp{roleName: "foo", configName: "client_encoding", configValue: "utf8"},
			ok:   true,
		},
		{
			sql:  "ALTER ROLE foo SET NAMES",
			want: alterRoleConfigOp{roleName: "foo", configName: "client_encoding", reset: true},
			ok:   true,
		},
		{
			sql:  "ALTER ROLE foo SET ROLE 'alice'",
			want: alterRoleConfigOp{roleName: "foo", configName: "role", configValue: "alice"},
			ok:   true,
		},
		{
			sql:  "ALTER ROLE foo SET SESSION AUTHORIZATION 'alice'",
			want: alterRoleConfigOp{roleName: "foo", configName: "session_authorization", configValue: "alice"},
			ok:   true,
		},
		{
			sql:  "ALTER ROLE foo SET SESSION AUTHORIZATION DEFAULT",
			want: alterRoleConfigOp{roleName: "foo", configName: "session_authorization", reset: true},
			ok:   true,
		},
		{
			sql:  "ALTER ROLE foo SET XML OPTION DOCUMENT",
			want: alterRoleConfigOp{roleName: "foo", configName: "xmloption", configValue: "DOCUMENT"},
			ok:   true,
		},
		{
			sql:  "ALTER ROLE foo IN DATABASE postgres SET TIME ZONE 'UTC'",
			want: alterRoleConfigOp{roleName: "foo", hasDatabase: true, dbName: "postgres", configName: "timezone", configValue: "UTC"},
			ok:   true,
		},
		// "var_name FROM CURRENT" — see TestParseAlterDatabaseConfig's
		// identical case for the grammar citation; resolved at apply time.
		{
			sql:  "ALTER ROLE foo SET work_mem FROM CURRENT",
			want: alterRoleConfigOp{roleName: "foo", configName: "work_mem", fromCurrent: true},
			ok:   true,
		},
		{
			sql:  "ALTER ROLE foo IN DATABASE postgres SET search_path FROM CURRENT",
			want: alterRoleConfigOp{roleName: "foo", hasDatabase: true, dbName: "postgres", configName: "search_path", fromCurrent: true},
			ok:   true,
		},
		// "ALTER ROLE ALL ..." (role_specification = ALL, gram.y's separate
		// `ALTER ROLE ALL opt_in_database SetResetClause` production): the
		// bare unquoted keyword sets allRoles with an empty roleName.
		{
			sql:  "ALTER ROLE ALL SET work_mem = '64MB'",
			want: alterRoleConfigOp{allRoles: true, configName: "work_mem", configValue: "64MB"},
			ok:   true,
		},
		{
			sql:  "alter role all set work_mem to '64MB'",
			want: alterRoleConfigOp{allRoles: true, configName: "work_mem", configValue: "64MB"},
			ok:   true,
		},
		{
			sql:  "ALTER ROLE ALL IN DATABASE postgres SET work_mem = '64MB'",
			want: alterRoleConfigOp{allRoles: true, hasDatabase: true, dbName: "postgres", configName: "work_mem", configValue: "64MB"},
			ok:   true,
		},
		{
			sql:  "ALTER ROLE ALL RESET work_mem",
			want: alterRoleConfigOp{allRoles: true, configName: "work_mem", reset: true},
			ok:   true,
		},
		{
			sql:  "ALTER ROLE ALL RESET ALL",
			want: alterRoleConfigOp{allRoles: true, resetAll: true},
			ok:   true,
		},
		{
			sql:  "ALTER USER ALL SET work_mem = '64MB'",
			want: alterRoleConfigOp{allRoles: true, configName: "work_mem", configValue: "64MB"},
			ok:   true,
		},
		// A quoted "ALL"/"all" names a real role, not the ALL keyword — must
		// still resolve via RoleOID like any other role name.
		{
			sql:  `ALTER ROLE "ALL" SET work_mem = '64MB'`,
			want: alterRoleConfigOp{roleName: "ALL", configName: "work_mem", configValue: "64MB"},
			ok:   true,
		},
		{
			sql:  `ALTER ROLE "all" SET work_mem = '64MB'`,
			want: alterRoleConfigOp{roleName: "all", configName: "work_mem", configValue: "64MB"},
			ok:   true,
		},

		// negatives: forms handled elsewhere in tryHandleRoleDDL, or
		// unmodelled ALTER ROLE forms, must fall through unrecognised.
		{sql: "ALTER ROLE foo RENAME TO bar", ok: false},
		{sql: "ALTER ROLE foo LOGIN", ok: false},
		{sql: "ALTER ROLE foo SUPERUSER", ok: false},
		{sql: "CREATE ROLE foo", ok: false},
		{sql: "ALTER DATABASE postgres SET work_mem = '64MB'", ok: false},
		{sql: "SELECT 1", ok: false},
		{sql: "", ok: false},
	}
	for _, c := range cases {
		got, ok := parseAlterRoleConfig(c.sql)
		if ok != c.ok {
			t.Errorf("parseAlterRoleConfig(%q) ok = %v, want %v (got %+v)", c.sql, ok, c.ok, got)
			continue
		}
		if !ok {
			continue
		}
		if got != c.want {
			t.Errorf("parseAlterRoleConfig(%q) = %+v, want %+v", c.sql, got, c.want)
		}
	}
}

func TestTryHandleRoleDDLAlterRoleConfig(t *testing.T) {
	s := newTestRoleServer()
	if handled, err := s.tryHandleRoleDDL("CREATE ROLE foo LOGIN", "postgres", nil); !handled || err != nil {
		t.Fatalf("CREATE ROLE foo: handled=%v err=%v", handled, err)
	}
	im := s.cfg.Catalog.(*catalog.InMemory)
	roleOid, ok := im.RoleOID("foo")
	if !ok {
		t.Fatal("role foo not registered in catalog")
	}

	// Cluster-wide SET (no IN DATABASE): dbOid=0.
	if handled, err := s.tryHandleRoleDDL("ALTER ROLE foo SET work_mem = '64MB'", "postgres", nil); !handled || err != nil {
		t.Fatalf("ALTER ROLE foo SET work_mem: handled=%v err=%v", handled, err)
	}
	if got := im.RoleConfigEntries(roleOid, 0); len(got) != 1 || got[0] != "work_mem=64MB" {
		t.Errorf("RoleConfigEntries(foo, 0) = %v, want [work_mem=64MB]", got)
	}

	// IN DATABASE matching the connection's own live database: recorded
	// under FirstUserOID.
	if handled, err := s.tryHandleRoleDDL("ALTER ROLE foo IN DATABASE postgres SET search_path TO public", "postgres", nil); !handled || err != nil {
		t.Fatalf("ALTER ROLE foo IN DATABASE postgres SET: handled=%v err=%v", handled, err)
	}
	if got := im.RoleConfigEntries(roleOid, catalog.FirstUserOID); len(got) != 1 || got[0] != "search_path=public" {
		t.Errorf("RoleConfigEntries(foo, FirstUserOID) = %v, want [search_path=public]", got)
	}

	// IN DATABASE naming a DIFFERENT database than the live connection is a
	// silent no-op (v0 single-live-database scope, mirrors
	// applyAlterDatabaseConfig's identical restriction).
	if handled, err := s.tryHandleRoleDDL("ALTER ROLE foo IN DATABASE otherdb SET work_mem = '1MB'", "postgres", nil); !handled || err != nil {
		t.Fatalf("ALTER ROLE foo IN DATABASE otherdb SET: handled=%v err=%v", handled, err)
	}
	if got := im.RoleConfigEntries(roleOid, 0); len(got) != 1 || got[0] != "work_mem=64MB" {
		t.Errorf("cluster-wide entry mutated by an other-database ALTER ROLE: %v", got)
	}

	// RESET removes only the named entry.
	if handled, err := s.tryHandleRoleDDL("ALTER ROLE foo RESET work_mem", "postgres", nil); !handled || err != nil {
		t.Fatalf("ALTER ROLE foo RESET work_mem: handled=%v err=%v", handled, err)
	}
	if got := im.RoleConfigEntries(roleOid, 0); len(got) != 0 {
		t.Errorf("RoleConfigEntries(foo, 0) after RESET = %v, want empty", got)
	}
	if got := im.RoleConfigEntries(roleOid, catalog.FirstUserOID); len(got) != 1 {
		t.Errorf("IN DATABASE entry should survive an unrelated cluster-wide RESET: %v", got)
	}

	// RESET ALL clears the IN DATABASE scope too.
	if handled, err := s.tryHandleRoleDDL("ALTER ROLE foo IN DATABASE postgres RESET ALL", "postgres", nil); !handled || err != nil {
		t.Fatalf("ALTER ROLE foo IN DATABASE postgres RESET ALL: handled=%v err=%v", handled, err)
	}
	if got := im.RoleConfigEntries(roleOid, catalog.FirstUserOID); len(got) != 0 {
		t.Errorf("RoleConfigEntries(foo, FirstUserOID) after RESET ALL = %v, want empty", got)
	}

	// A nonexistent role errors 42704 (undefined_object), matching
	// roleDoesNotExistErr's use elsewhere in this file.
	handled, err := s.tryHandleRoleDDL("ALTER ROLE nosuchrole SET work_mem = '1MB'", "postgres", nil)
	if !handled {
		t.Fatal("ALTER ROLE nosuchrole SET: expected handled=true")
	}
	if err == nil {
		t.Fatal("ALTER ROLE nosuchrole SET: expected an error")
	}
	if got := roleErrorSQLState(err); got != sqlstate.UndefinedObject {
		t.Errorf("ALTER ROLE nosuchrole SET: SQLSTATE = %q, want %q", got, sqlstate.UndefinedObject)
	}
}

// TestTryHandleRoleDDLAlterRoleConfigFromCurrent covers "SET <name> FROM
// CURRENT" (VAR_SET_CURRENT): the stored value must be whatever the live
// session's resolver reports for that name RIGHT NOW, not a literal parsed
// out of the SQL text (there is none). Mirrors PG's ExtractSetVariableArgs
// VAR_SET_CURRENT case (GetConfigOptionByName).
func TestTryHandleRoleDDLAlterRoleConfigFromCurrent(t *testing.T) {
	s := newTestRoleServer()
	if handled, err := s.tryHandleRoleDDL("CREATE ROLE foo LOGIN", "postgres", nil); !handled || err != nil {
		t.Fatalf("CREATE ROLE foo: handled=%v err=%v", handled, err)
	}
	im := s.cfg.Catalog.(*catalog.InMemory)
	roleOid, _ := im.RoleOID("foo")

	live := map[string]string{"work_mem": "128MB"}
	resolver := currentGUCResolver(func(name string) (string, bool) {
		v, ok := live[name]
		return v, ok
	})

	if handled, err := s.tryHandleRoleDDL("ALTER ROLE foo SET work_mem FROM CURRENT", "postgres", resolver); !handled || err != nil {
		t.Fatalf("ALTER ROLE foo SET work_mem FROM CURRENT: handled=%v err=%v", handled, err)
	}
	if got := im.RoleConfigEntries(roleOid, 0); len(got) != 1 || got[0] != "work_mem=128MB" {
		t.Errorf("RoleConfigEntries(foo, 0) = %v, want [work_mem=128MB]", got)
	}

	// An unrecognised GUC name (resolver reports ok=false) errors 42704,
	// matching GetConfigOptionByName's "unrecognized configuration
	// parameter" ereport.
	handled, err := s.tryHandleRoleDDL("ALTER ROLE foo SET no_such_guc FROM CURRENT", "postgres", resolver)
	if !handled {
		t.Fatal("ALTER ROLE foo SET no_such_guc FROM CURRENT: expected handled=true")
	}
	if err == nil {
		t.Fatal("ALTER ROLE foo SET no_such_guc FROM CURRENT: expected an error")
	}
	if got := roleErrorSQLState(err); got != sqlstate.UndefinedObject {
		t.Errorf("SQLSTATE = %q, want %q", got, sqlstate.UndefinedObject)
	}

	// A nil resolver (no live session) behaves the same as ok=false.
	handled, err = s.tryHandleRoleDDL("ALTER ROLE foo SET work_mem FROM CURRENT", "postgres", nil)
	if !handled || err == nil {
		t.Fatalf("ALTER ROLE foo SET work_mem FROM CURRENT (nil resolver): handled=%v err=%v", handled, err)
	}
}

// TestTryHandleRoleDDLAlterRoleAll covers "ALTER ROLE ALL ...": real PG's
// AlterRoleSet (user.c) leaves roleid=InvalidOid (0) when the parsed
// role_specification is the ALL keyword (stmt->role == NULL), so the
// cluster-wide default lands in the same setrole=0 pg_db_role_setting row
// that any other roleOid keys off of — no RoleOID lookup, and no
// "role does not exist" error, unlike a named role.
func TestTryHandleRoleDDLAlterRoleAll(t *testing.T) {
	s := newTestRoleServer()
	im := s.cfg.Catalog.(*catalog.InMemory)

	if handled, err := s.tryHandleRoleDDL("ALTER ROLE ALL SET work_mem = '64MB'", "postgres", nil); !handled || err != nil {
		t.Fatalf("ALTER ROLE ALL SET work_mem: handled=%v err=%v", handled, err)
	}
	if got := im.RoleConfigEntries(0, 0); len(got) != 1 || got[0] != "work_mem=64MB" {
		t.Errorf("RoleConfigEntries(0, 0) = %v, want [work_mem=64MB]", got)
	}

	// IN DATABASE matching the connection's own live database: recorded
	// under (roleOid=0, FirstUserOID) — the "setdatabase=<db>, setrole=0"
	// row, distinct from the plain cluster-wide (0,0) row above.
	if handled, err := s.tryHandleRoleDDL("ALTER ROLE ALL IN DATABASE postgres SET search_path TO public", "postgres", nil); !handled || err != nil {
		t.Fatalf("ALTER ROLE ALL IN DATABASE postgres SET: handled=%v err=%v", handled, err)
	}
	if got := im.RoleConfigEntries(0, catalog.FirstUserOID); len(got) != 1 || got[0] != "search_path=public" {
		t.Errorf("RoleConfigEntries(0, FirstUserOID) = %v, want [search_path=public]", got)
	}
	if got := im.RoleConfigEntries(0, 0); len(got) != 1 || got[0] != "work_mem=64MB" {
		t.Errorf("cluster-wide (0,0) entry mutated by an IN DATABASE ALTER ROLE ALL: %v", got)
	}

	// RESET removes only the named entry from the (0,0) row.
	if handled, err := s.tryHandleRoleDDL("ALTER ROLE ALL RESET work_mem", "postgres", nil); !handled || err != nil {
		t.Fatalf("ALTER ROLE ALL RESET work_mem: handled=%v err=%v", handled, err)
	}
	if got := im.RoleConfigEntries(0, 0); len(got) != 0 {
		t.Errorf("RoleConfigEntries(0, 0) after RESET = %v, want empty", got)
	}

	// A quoted "ALL" role name is a real (nonexistent here) role, not the
	// ALL keyword — must still error like any other undefined role.
	handled, err := s.tryHandleRoleDDL(`ALTER ROLE "ALL" SET work_mem = '1MB'`, "postgres", nil)
	if !handled {
		t.Fatal(`ALTER ROLE "ALL" SET: expected handled=true`)
	}
	if err == nil {
		t.Fatal(`ALTER ROLE "ALL" SET: expected an error (no role literally named ALL)`)
	}
	if got := roleErrorSQLState(err); got != sqlstate.UndefinedObject {
		t.Errorf(`ALTER ROLE "ALL" SET: SQLSTATE = %q, want %q`, got, sqlstate.UndefinedObject)
	}
}
