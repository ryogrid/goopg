package server

// Unit coverage for ALTER ROLE/USER ... RENAME TO (root-0021 follow-up,
// M0119-0004): the parsing helper plus tryHandleRoleDDL's error/success
// paths, exercised directly against an in-process *Server (no TCP/wire
// handshake needed — renameRole only touches Server fields + the catalog).

import (
	"testing"

	"github.com/goopg/goopg/internal/auth"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/sqlstate"
)

func TestRoleRenameFromAlterParsing(t *testing.T) {
	cases := []struct {
		sql         string
		wantName    string
		wantNewName string
		wantOK      bool
	}{
		{"alter role foo rename to bar", "foo", "bar", true},
		{"alter user foo rename to bar", "foo", "bar", true},
		{"alter role foo login", "", "", false},
		{"alter role foo set search_path = 'x'", "", "", false},
		{"alter role foo reset search_path", "", "", false},
		{"create role foo", "", "", false},
	}
	for _, tc := range cases {
		name, newName, ok := roleRenameFromAlter(normalizeCompatSQL(tc.sql))
		if ok != tc.wantOK || name != tc.wantName || newName != tc.wantNewName {
			t.Errorf("roleRenameFromAlter(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.sql, name, newName, ok, tc.wantName, tc.wantNewName, tc.wantOK)
		}
	}
}

func newTestRoleServer() *Server {
	return New(Config{
		Catalog:   catalog.NewInMemory(),
		UserStore: auth.NewMapUserStore(),
	})
}

func TestAlterRoleRenameSuccess(t *testing.T) {
	s := newTestRoleServer()
	if handled, err := s.tryHandleRoleDDL("CREATE ROLE foo LOGIN PASSWORD 'secret'", "postgres"); !handled || err != nil {
		t.Fatalf("CREATE ROLE: handled=%v err=%v", handled, err)
	}
	im := s.cfg.Catalog.(*catalog.InMemory)
	oid, ok := im.RoleOID("foo")
	if !ok {
		t.Fatal("role foo not registered in catalog")
	}

	handled, err := s.tryHandleRoleDDL("ALTER ROLE foo RENAME TO bar", "postgres")
	if !handled || err != nil {
		t.Fatalf("ALTER ROLE RENAME TO: handled=%v err=%v", handled, err)
	}

	if s.roleExists("foo") {
		t.Error("old name foo still registered after rename")
	}
	if !s.roleExists("bar") {
		t.Fatal("new name bar not registered after rename")
	}
	newOID, ok := im.RoleOID("bar")
	if !ok || newOID != oid {
		t.Errorf("RoleOID(bar) = (%d, %v), want (%d, true) — OID must be preserved across rename", newOID, ok, oid)
	}
	if _, ok := im.RoleOID("foo"); ok {
		t.Error("catalog still resolves old name foo after rename")
	}
	store := s.cfg.UserStore.(*auth.MapUserStore)
	if _, found := store.Lookup("bar"); !found {
		t.Error("credential did not follow the rename to the new name")
	}
	if _, found := store.Lookup("foo"); found {
		t.Error("credential still present under the old name after rename")
	}
}

func TestAlterRoleRenameErrors(t *testing.T) {
	s := newTestRoleServer()
	if handled, err := s.tryHandleRoleDDL("CREATE ROLE foo LOGIN", "postgres"); !handled || err != nil {
		t.Fatalf("CREATE ROLE foo: handled=%v err=%v", handled, err)
	}
	if handled, err := s.tryHandleRoleDDL("CREATE ROLE existing LOGIN", "postgres"); !handled || err != nil {
		t.Fatalf("CREATE ROLE existing: handled=%v err=%v", handled, err)
	}

	cases := []struct {
		name     string
		sql      string
		wantCode sqlstate.Code
	}{
		{"role does not exist", "ALTER ROLE nosuchrole RENAME TO whatever", sqlstate.UndefinedObject},
		{"new name already exists", "ALTER ROLE foo RENAME TO existing", sqlstate.DuplicateObject},
		{"reserved pg_ prefix", "ALTER ROLE foo RENAME TO pg_reserved", sqlstate.ReservedName},
		{"bootstrap superuser", "ALTER ROLE postgres RENAME TO newname", sqlstate.FeatureNotSupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handled, err := s.tryHandleRoleDDL(tc.sql, "postgres")
			if !handled {
				t.Fatalf("%s: expected handled=true", tc.sql)
			}
			if err == nil {
				t.Fatalf("%s: expected an error", tc.sql)
			}
			if got := roleErrorSQLState(err); got != tc.wantCode {
				t.Errorf("%s: SQLSTATE = %q, want %q (err=%v)", tc.sql, got, tc.wantCode, err)
			}
		})
	}
	// foo must still be registered under its original name — none of the
	// error cases above should have mutated the registry.
	if !s.roleExists("foo") {
		t.Error("foo should still exist after all rename error cases")
	}
}

func TestCatalogRenameRolePreservesOID(t *testing.T) {
	im := catalog.NewInMemory()
	im.RegisterRole("foo")
	im.SetRoleAttrs("foo", catalog.RoleAttrs{CanLogin: true, Superuser: true})
	oid, ok := im.RoleOID("foo")
	if !ok {
		t.Fatal("RegisterRole did not register foo")
	}

	if !im.RenameRole("foo", "bar") {
		t.Fatal("RenameRole(foo, bar) = false, want true")
	}
	if im.RoleExists("foo") {
		t.Error("old name foo still exists after RenameRole")
	}
	newOID, ok := im.RoleOID("bar")
	if !ok || newOID != oid {
		t.Errorf("RoleOID(bar) = (%d, %v), want (%d, true)", newOID, ok, oid)
	}
	attrs, ok := im.LookupRoleAttrs("bar")
	if !ok || !attrs.CanLogin || !attrs.Superuser {
		t.Errorf("LookupRoleAttrs(bar) = (%+v, %v), want CanLogin+Superuser attrs to follow the rename", attrs, ok)
	}

	if im.RenameRole("nosuchrole", "whatever") {
		t.Error("RenameRole on an unregistered name should return false")
	}
}
