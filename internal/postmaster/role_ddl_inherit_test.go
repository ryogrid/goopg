package postmaster

// M0134-0162 — [NO]INHERIT / pg_authid.rolinherit.
//
// roleattributes.sql's whole 28-line diff against goopg was one root cause:
// applyRoleAttrOptions never recognised INHERIT/NOINHERIT and every pg_authid
// row builder hardcoded rolinherit = 't', so `CREATE ROLE r WITH NOINHERIT`
// and `ALTER ROLE r WITH NOINHERIT` were accept-and-ignore.
//
// rolinherit is the only pg_authid boolean whose PG default is TRUE
// (postgres/src/backend/commands/user.c CreateRole, `bool inherit = true`),
// which makes the Go zero value the WRONG seed — the same trap RoleAttrs'
// ConnLimit: -1 carries. These tests pin both polarities so a future RoleAttrs
// construction site that forgets `Inherit: true` fails here rather than
// silently marking every new role NOINHERIT.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestCreateRoleInheritDefaultsTrue pins PG's CREATE ROLE default. The role
// name deliberately ends in "_inherit": applyRoleAttrOptions probes options by
// leading-space substring, and this is exactly the shape
// (regress_test_def_inherit) roleattributes.sql uses, so a probe that dropped
// the leading space would match the NAME and report NOINHERIT here.
func TestCreateRoleInheritDefaultsTrue(t *testing.T) {
	s := newTestRoleServer()
	if handled, err := s.tryHandleRoleDDL("CREATE ROLE regress_test_def_inherit", "postgres", nil); !handled || err != nil {
		t.Fatalf("CREATE ROLE: handled=%v err=%v", handled, err)
	}
	im := s.cfg.Catalog.(*catalog.InMemory)
	a, ok := im.LookupRoleAttrs("regress_test_def_inherit")
	if !ok {
		t.Fatal("expected a recorded RoleAttrs sidecar entry")
	}
	if !a.Inherit {
		t.Errorf("Inherit = false, want true (PG's CREATE ROLE default, user.c:291)")
	}
}

// TestCreateAndAlterRoleNoInherit walks roleattributes.sql's exact
// NOINHERIT → INHERIT → NOINHERIT sequence for regress_test_inherit, which is
// where the original diff's two hunks came from.
func TestCreateAndAlterRoleNoInherit(t *testing.T) {
	s := newTestRoleServer()
	im := s.cfg.Catalog.(*catalog.InMemory)
	inherit := func(step string) bool {
		a, ok := im.LookupRoleAttrs("regress_test_inherit")
		if !ok {
			t.Fatalf("%s: expected a recorded RoleAttrs sidecar entry", step)
		}
		return a.Inherit
	}
	for _, step := range []struct {
		sql  string
		want bool
	}{
		{"CREATE ROLE regress_test_inherit WITH NOINHERIT", false},
		{"ALTER ROLE regress_test_inherit WITH INHERIT", true},
		{"ALTER ROLE regress_test_inherit WITH NOINHERIT", false},
	} {
		handled, err := s.tryHandleRoleDDL(step.sql, "postgres", nil)
		if !handled || err != nil {
			t.Fatalf("%s: handled=%v err=%v", step.sql, handled, err)
		}
		if got := inherit(step.sql); got != step.want {
			t.Errorf("%s: rolinherit = %v, want %v", step.sql, got, step.want)
		}
	}
}

// TestAlterRoleUnrelatedAttrPreservesInherit pins PG's "an ALTER only changes
// what it names" rule for the one attribute whose seed default is true: a
// later ALTER that never mentions INHERIT must not resurrect it.
func TestAlterRoleUnrelatedAttrPreservesInherit(t *testing.T) {
	s := newTestRoleServer()
	if handled, err := s.tryHandleRoleDDL("CREATE ROLE erin WITH NOINHERIT", "postgres", nil); !handled || err != nil {
		t.Fatalf("CREATE ROLE erin: handled=%v err=%v", handled, err)
	}
	if handled, err := s.tryHandleRoleDDL("ALTER ROLE erin WITH CREATEDB", "postgres", nil); !handled || err != nil {
		t.Fatalf("ALTER ROLE erin: handled=%v err=%v", handled, err)
	}
	im := s.cfg.Catalog.(*catalog.InMemory)
	a, _ := im.LookupRoleAttrs("erin")
	if a.Inherit {
		t.Error("Inherit = true after an ALTER that never named INHERIT, want false (unspecified attributes keep their value)")
	}
	if !a.CreateDB {
		t.Error("CreateDB = false, want true")
	}
}

// TestPgAuthidVirtualRowReportsRolinherit pins the read path the regress case
// actually asserts on: `SELECT rolinherit FROM pg_authid` is served by the
// catalog's pg_authid VirtualRows builder, not the on-disk global/1260 heap.
func TestPgAuthidVirtualRowReportsRolinherit(t *testing.T) {
	s := newTestRoleServer()
	if handled, err := s.tryHandleRoleDDL("CREATE ROLE frank WITH NOINHERIT", "postgres", nil); !handled || err != nil {
		t.Fatalf("CREATE ROLE frank: handled=%v err=%v", handled, err)
	}
	if handled, err := s.tryHandleRoleDDL("CREATE ROLE grace", "postgres", nil); !handled || err != nil {
		t.Fatalf("CREATE ROLE grace: handled=%v err=%v", handled, err)
	}
	im := s.cfg.Catalog.(*catalog.InMemory)
	tbl, ok := im.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_authid"})
	if !ok || tbl.VirtualRows == nil {
		t.Fatal("pg_catalog.pg_authid: expected a virtual table with a row builder")
	}
	// rolinherit is pg_authid's 4th column (ordinal 3), per pg_authid.h.
	const rolnameIdx, rolinheritIdx = 1, 3
	want := map[string]string{"frank": "f", "grace": "t", "postgres": "t"}
	seen := map[string]string{}
	for _, row := range tbl.VirtualRows() {
		if w, ok := want[row[rolnameIdx]]; ok {
			seen[row[rolnameIdx]] = row[rolinheritIdx]
			if row[rolinheritIdx] != w {
				t.Errorf("pg_authid.rolinherit for %q = %q, want %q", row[rolnameIdx], row[rolinheritIdx], w)
			}
		}
	}
	for name := range want {
		if _, ok := seen[name]; !ok {
			t.Errorf("pg_authid: no row for %q", name)
		}
	}
}

// TestGrantRoleMembershipInheritOptionDefaultsToGranteeRolinherit pins the
// engine-wide half of M0134-0162 — the reason modelling rolinherit is not
// cosmetic. AddRoleMems (user.c:1924-1939) defaults a fresh pg_auth_members
// row's inherit_option to the GRANTEE's rolinherit when the GRANT names no
// INHERIT option; goopg hardcoded true, which was only correct while no role
// could ever be NOINHERIT. Since HasPrivsOfRole traverses inherit-marked rows
// exclusively, the old default would have handed a NOINHERIT member every
// privilege of every role granted to it.
func TestGrantRoleMembershipInheritOptionDefaultsToGranteeRolinherit(t *testing.T) {
	s := newTestRoleServer()
	for _, sql := range []string{
		"CREATE ROLE privileged_role",
		"CREATE ROLE inheriting_member",
		"CREATE ROLE noinherit_member WITH NOINHERIT",
	} {
		if handled, err := s.tryHandleRoleDDL(sql, "postgres", nil); !handled || err != nil {
			t.Fatalf("%s: handled=%v err=%v", sql, handled, err)
		}
	}
	im := s.cfg.Catalog.(*catalog.InMemory)
	roleOID, _ := im.RoleOID("privileged_role")
	inheritOID, _ := im.RoleOID("inheriting_member")
	noInheritOID, _ := im.RoleOID("noinherit_member")

	// Plain GRANT: no INHERIT option named, so each row's default is read off
	// the grantee's own rolinherit.
	im.GrantRoleMembership(roleOID, inheritOID, 10, nil, nil, nil)
	im.GrantRoleMembership(roleOID, noInheritOID, 10, nil, nil, nil)

	if m, ok := im.LookupRoleMembership(roleOID, inheritOID, 10); !ok || !m.InheritOption {
		t.Errorf("inheriting_member: InheritOption = %v (found=%v), want true", ok && m.InheritOption, ok)
	}
	if m, ok := im.LookupRoleMembership(roleOID, noInheritOID, 10); !ok || m.InheritOption {
		t.Errorf("noinherit_member: InheritOption = %v (found=%v), want false — a NOINHERIT grantee must not inherit by default", ok && m.InheritOption, ok)
	}
	// The privilege traversal is the thing that actually matters.
	if !im.HasPrivsOfRole(inheritOID, roleOID) {
		t.Error("HasPrivsOfRole(inheriting_member, privileged_role) = false, want true")
	}
	if im.HasPrivsOfRole(noInheritOID, roleOID) {
		t.Error("HasPrivsOfRole(noinherit_member, privileged_role) = true, want false — NOINHERIT means privileges need an explicit SET ROLE")
	}
}

// TestGrantRoleMembershipExplicitInheritOverridesRolinherit pins the other
// arm of the same branch: an explicit `WITH INHERIT TRUE` always wins over
// the role-level property (user.c's GRANT_ROLE_SPECIFIED_INHERIT test).
func TestGrantRoleMembershipExplicitInheritOverridesRolinherit(t *testing.T) {
	s := newTestRoleServer()
	for _, sql := range []string{"CREATE ROLE target_role", "CREATE ROLE ni_member WITH NOINHERIT"} {
		if handled, err := s.tryHandleRoleDDL(sql, "postgres", nil); !handled || err != nil {
			t.Fatalf("%s: handled=%v err=%v", sql, handled, err)
		}
	}
	im := s.cfg.Catalog.(*catalog.InMemory)
	roleOID, _ := im.RoleOID("target_role")
	memberOID, _ := im.RoleOID("ni_member")

	yes := true
	im.GrantRoleMembership(roleOID, memberOID, 10, nil, &yes, nil)
	if m, ok := im.LookupRoleMembership(roleOID, memberOID, 10); !ok || !m.InheritOption {
		t.Errorf("explicit WITH INHERIT TRUE on a NOINHERIT grantee: InheritOption = %v (found=%v), want true", ok && m.InheritOption, ok)
	}
}
