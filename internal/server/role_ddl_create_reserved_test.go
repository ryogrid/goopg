package server

// Unit coverage for CREATE ROLE/USER/GROUP's reserved-"pg_"-prefix rejection
// (M0119-0004-ACLHEAP follow-up): CreateRole (postgres/src/backend/commands/
// user.c) calls IsReservedName before it even opens pg_authid, so `CREATE
// ROLE pg_x` fails with 42939 regardless of whether pg_x already exists.
// Until this loop, tryHandleRoleDDL's "create role "/"create user "/
// "create group " branch only ever accepted names — the RENAME TO path
// (role_ddl_rename_test.go) already had this check via isReservedRoleName,
// but CREATE never called it.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/sqlstate"
)

func TestCreateRoleRejectsReservedPgPrefix(t *testing.T) {
	cases := []struct {
		sql string
	}{
		{"CREATE ROLE pg_custom"},
		{"CREATE USER pg_custom"},
		{"CREATE GROUP pg_custom"},
		{"CREATE ROLE PG_CUSTOM"}, // case-insensitive, matches IsReservedName's pg_strncasecmp
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			s := newTestRoleServer()
			handled, err := s.tryHandleRoleDDL(tc.sql, "postgres")
			if !handled {
				t.Fatalf("%s: expected handled=true", tc.sql)
			}
			if err == nil {
				t.Fatalf("%s: expected a reserved-name error", tc.sql)
			}
			if got := roleErrorSQLState(err); got != sqlstate.ReservedName {
				t.Errorf("%s: SQLSTATE = %q, want %q (err=%v)", tc.sql, got, sqlstate.ReservedName, err)
			}
			if s.roleExists("pg_custom") {
				t.Errorf("%s: role must not be registered after rejection", tc.sql)
			}
		})
	}
}

func TestCreateRoleAllowsNonReservedName(t *testing.T) {
	s := newTestRoleServer()
	handled, err := s.tryHandleRoleDDL("CREATE ROLE alice LOGIN", "postgres")
	if !handled || err != nil {
		t.Fatalf("CREATE ROLE alice: handled=%v err=%v", handled, err)
	}
	if !s.roleExists("alice") {
		t.Fatal("alice should be registered after a successful CREATE ROLE")
	}
	im := s.cfg.Catalog.(*catalog.InMemory)
	if !im.RoleExists("alice") {
		t.Fatal("alice should be registered in the catalog too")
	}
}
