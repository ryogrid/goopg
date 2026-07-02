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
	if handled, err := s.tryHandleRoleDDL("CREATE ROLE foo LOGIN", "postgres"); !handled || err != nil {
		t.Fatalf("CREATE ROLE foo: handled=%v err=%v", handled, err)
	}
	im := s.cfg.Catalog.(*catalog.InMemory)
	roleOid, ok := im.RoleOID("foo")
	if !ok {
		t.Fatal("role foo not registered in catalog")
	}

	// Cluster-wide SET (no IN DATABASE): dbOid=0.
	if handled, err := s.tryHandleRoleDDL("ALTER ROLE foo SET work_mem = '64MB'", "postgres"); !handled || err != nil {
		t.Fatalf("ALTER ROLE foo SET work_mem: handled=%v err=%v", handled, err)
	}
	if got := im.RoleConfigEntries(roleOid, 0); len(got) != 1 || got[0] != "work_mem=64MB" {
		t.Errorf("RoleConfigEntries(foo, 0) = %v, want [work_mem=64MB]", got)
	}

	// IN DATABASE matching the connection's own live database: recorded
	// under FirstUserOID.
	if handled, err := s.tryHandleRoleDDL("ALTER ROLE foo IN DATABASE postgres SET search_path TO public", "postgres"); !handled || err != nil {
		t.Fatalf("ALTER ROLE foo IN DATABASE postgres SET: handled=%v err=%v", handled, err)
	}
	if got := im.RoleConfigEntries(roleOid, catalog.FirstUserOID); len(got) != 1 || got[0] != "search_path=public" {
		t.Errorf("RoleConfigEntries(foo, FirstUserOID) = %v, want [search_path=public]", got)
	}

	// IN DATABASE naming a DIFFERENT database than the live connection is a
	// silent no-op (v0 single-live-database scope, mirrors
	// applyAlterDatabaseConfig's identical restriction).
	if handled, err := s.tryHandleRoleDDL("ALTER ROLE foo IN DATABASE otherdb SET work_mem = '1MB'", "postgres"); !handled || err != nil {
		t.Fatalf("ALTER ROLE foo IN DATABASE otherdb SET: handled=%v err=%v", handled, err)
	}
	if got := im.RoleConfigEntries(roleOid, 0); len(got) != 1 || got[0] != "work_mem=64MB" {
		t.Errorf("cluster-wide entry mutated by an other-database ALTER ROLE: %v", got)
	}

	// RESET removes only the named entry.
	if handled, err := s.tryHandleRoleDDL("ALTER ROLE foo RESET work_mem", "postgres"); !handled || err != nil {
		t.Fatalf("ALTER ROLE foo RESET work_mem: handled=%v err=%v", handled, err)
	}
	if got := im.RoleConfigEntries(roleOid, 0); len(got) != 0 {
		t.Errorf("RoleConfigEntries(foo, 0) after RESET = %v, want empty", got)
	}
	if got := im.RoleConfigEntries(roleOid, catalog.FirstUserOID); len(got) != 1 {
		t.Errorf("IN DATABASE entry should survive an unrelated cluster-wide RESET: %v", got)
	}

	// RESET ALL clears the IN DATABASE scope too.
	if handled, err := s.tryHandleRoleDDL("ALTER ROLE foo IN DATABASE postgres RESET ALL", "postgres"); !handled || err != nil {
		t.Fatalf("ALTER ROLE foo IN DATABASE postgres RESET ALL: handled=%v err=%v", handled, err)
	}
	if got := im.RoleConfigEntries(roleOid, catalog.FirstUserOID); len(got) != 0 {
		t.Errorf("RoleConfigEntries(foo, FirstUserOID) after RESET ALL = %v, want empty", got)
	}

	// A nonexistent role errors 42704 (undefined_object), matching
	// roleDoesNotExistErr's use elsewhere in this file.
	handled, err := s.tryHandleRoleDDL("ALTER ROLE nosuchrole SET work_mem = '1MB'", "postgres")
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
