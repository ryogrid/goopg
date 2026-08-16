package postmaster

// Unit coverage for CREATE/ALTER ROLE's attribute-clause options beyond
// LOGIN/PASSWORD/SUPERUSER (DU-002 slice 439 follow-up): CREATEDB/
// CREATEROLE/REPLICATION/BYPASSRLS/CONNECTION LIMIT/VALID UNTIL were
// previously accept-and-ignore — applyRoleAttrOptions never folded them into
// catalog.RoleAttrs, so pg_dump/pg_dumpall (which query pg_authid directly,
// see catalog.go's pg_authid VirtualRows) always reported the CREATE ROLE
// defaults regardless of what was actually granted.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

func TestCreateRoleAttributeOptionsRecorded(t *testing.T) {
	s := newTestRoleServer()
	handled, err := s.tryHandleRoleDDL(
		"CREATE ROLE bob LOGIN CREATEDB CREATEROLE REPLICATION BYPASSRLS CONNECTION LIMIT 5 VALID UNTIL '2030-01-01 00:00:00+00'",
		"postgres", nil)
	if !handled || err != nil {
		t.Fatalf("CREATE ROLE bob: handled=%v err=%v", handled, err)
	}
	im := s.cfg.Catalog.(*catalog.InMemory)
	a, ok := im.LookupRoleAttrs("bob")
	if !ok {
		t.Fatal("bob: expected a recorded RoleAttrs sidecar entry")
	}
	if !a.CreateDB || !a.CreateRole || !a.Replication || !a.BypassRLS {
		t.Errorf("bob: attrs = %+v, want all four bools true", a)
	}
	if a.ConnLimit != 5 {
		t.Errorf("bob: ConnLimit = %d, want 5", a.ConnLimit)
	}
	if a.ValidUntil != "2030-01-01 00:00:00+00" {
		t.Errorf("bob: ValidUntil = %q, want the literal echoed verbatim", a.ValidUntil)
	}
}

func TestCreateRoleAttributeOptionsDefaultToUnset(t *testing.T) {
	s := newTestRoleServer()
	handled, err := s.tryHandleRoleDDL("CREATE ROLE carol LOGIN", "postgres", nil)
	if !handled || err != nil {
		t.Fatalf("CREATE ROLE carol: handled=%v err=%v", handled, err)
	}
	im := s.cfg.Catalog.(*catalog.InMemory)
	a, ok := im.LookupRoleAttrs("carol")
	if !ok {
		t.Fatal("carol: expected a recorded RoleAttrs sidecar entry")
	}
	if a.CreateDB || a.CreateRole || a.Replication || a.BypassRLS {
		t.Errorf("carol: attrs = %+v, want all four bools false (PG's CREATE ROLE default)", a)
	}
	// PG's rolconnlimit default is -1 ("no limit"), not the Go zero value 0
	// ("no new connections") — see catalog.RoleAttrs' doc comment.
	if a.ConnLimit != -1 {
		t.Errorf("carol: ConnLimit = %d, want -1 (PG default)", a.ConnLimit)
	}
	if a.ValidUntil != "" {
		t.Errorf("carol: ValidUntil = %q, want empty (NULL/no expiration)", a.ValidUntil)
	}
}

func TestAlterRoleAttributeOptionsNegateAndOverride(t *testing.T) {
	s := newTestRoleServer()
	if handled, err := s.tryHandleRoleDDL("CREATE ROLE dave LOGIN CREATEDB CONNECTION LIMIT 3", "postgres", nil); !handled || err != nil {
		t.Fatalf("CREATE ROLE dave: handled=%v err=%v", handled, err)
	}
	handled, err := s.tryHandleRoleDDL("ALTER ROLE dave NOCREATEDB CREATEROLE CONNECTION LIMIT 10 VALID UNTIL NULL", "postgres", nil)
	if !handled || err != nil {
		t.Fatalf("ALTER ROLE dave: handled=%v err=%v", handled, err)
	}
	im := s.cfg.Catalog.(*catalog.InMemory)
	a, ok := im.LookupRoleAttrs("dave")
	if !ok {
		t.Fatal("dave: expected a recorded RoleAttrs sidecar entry")
	}
	if a.CreateDB {
		t.Error("dave: CreateDB should have been cleared by NOCREATEDB")
	}
	if !a.CreateRole {
		t.Error("dave: CreateRole should have been set by CREATEROLE")
	}
	if a.ConnLimit != 10 {
		t.Errorf("dave: ConnLimit = %d, want 10 (overridden by ALTER)", a.ConnLimit)
	}
	if a.ValidUntil != "" {
		t.Errorf("dave: ValidUntil = %q, want cleared by VALID UNTIL NULL", a.ValidUntil)
	}
	// LOGIN was never touched by the ALTER — must survive unchanged (PG
	// semantics: unspecified attributes keep their prior value).
	if !a.CanLogin {
		t.Error("dave: CanLogin should be unchanged (still true) — ALTER didn't mention it")
	}
}

// TestPgAuthidVirtualRowsSurfacesNewAttrs guards the pg_authid VirtualRows
// builder (catalog.go) that pg_dumpall's dumpRoles/dumpUserConfig reads
// directly: a role's CreateDB/CreateRole/Replication/BypassRLS/ConnLimit/
// ValidUntil must show up as real column values, not the old hardcoded
// 'f'/'f'/'f'/'f'/'-1'/NULL placeholders.
func TestPgAuthidVirtualRowsSurfacesNewAttrs(t *testing.T) {
	s := newTestRoleServer()
	if handled, err := s.tryHandleRoleDDL(
		"CREATE ROLE erin LOGIN CREATEDB REPLICATION CONNECTION LIMIT 7 VALID UNTIL '2031-06-01 00:00:00+00'",
		"postgres", nil); !handled || err != nil {
		t.Fatalf("CREATE ROLE erin: handled=%v err=%v", handled, err)
	}
	im := s.cfg.Catalog.(*catalog.InMemory)
	authid, ok := im.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_authid"})
	if !ok || authid.VirtualRows == nil {
		t.Fatal("pg_catalog.pg_authid: expected a virtual table with VirtualRows")
	}
	var row []string
	for _, r := range authid.VirtualRows() {
		if r[1] == "erin" {
			row = r
			break
		}
	}
	if row == nil {
		t.Fatal("pg_authid: no row for erin")
	}
	// Columns: oid, rolname, rolsuper, rolinherit, rolcreaterole, rolcreatedb,
	// rolcanlogin, rolreplication, rolbypassrls, rolconnlimit, rolpassword,
	// rolvaliduntil.
	if row[5] != "t" {
		t.Errorf("rolcreatedb = %q, want t", row[5])
	}
	if row[7] != "t" {
		t.Errorf("rolreplication = %q, want t", row[7])
	}
	if row[8] != "f" {
		t.Errorf("rolbypassrls = %q, want f", row[8])
	}
	if row[9] != "7" {
		t.Errorf("rolconnlimit = %q, want 7", row[9])
	}
	if row[11] != "2031-06-01 00:00:00+00" {
		t.Errorf("rolvaliduntil = %q, want the stored literal", row[11])
	}
}
