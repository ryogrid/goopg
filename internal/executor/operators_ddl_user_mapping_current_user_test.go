package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestCreateUserMappingCurrentUserResolvesToConnectingRoleName pins the
// M0122-0007 4e follow-up 38 fix: follow-up 37's live E2E check surfaced that
// `CREATE USER MAPPING FOR CURRENT_USER SERVER srv` stored the literal string
// "current_user" as UmUser instead of resolving it to the connecting role's
// actual name, unlike every OWNER TO site in this file (which all honour the
// same sentinel) and unlike real PostgreSQL's CreateUserMapping
// (foreigncmds.c), which resolves the RoleSpec via get_rolespec_oid
// (GetUserId()) at CREATE time. Covers all four PG role-spec spellings that
// resolve identically (CURRENT_USER/SESSION_USER/CURRENT_ROLE/USER) plus the
// bootstrap-superuser fallback and the PUBLIC/plain-name pass-through cases.
func TestCreateUserMappingCurrentUserResolvesToConnectingRoleName(t *testing.T) {
	for _, spec := range []string{"CURRENT_USER", "SESSION_USER", "CURRENT_ROLE", "USER"} {
		t.Run(spec, func(t *testing.T) {
			ctx, cleanup := newVMFixture(t)
			defer cleanup()
			ctx.NonSuperuserRole = "alice"
			im, ok := ctx.Catalog.(*catalog.InMemory)
			if !ok {
				t.Fatalf("ctx.Catalog is %T, want *catalog.InMemory", ctx.Catalog)
			}
			im.RegisterRole("alice")

			if err := runDDL(t, ctx, `CREATE SERVER srv FOREIGN DATA WRAPPER goopg_fdw`); err != nil {
				t.Fatalf("CREATE SERVER: %v", err)
			}
			if err := runDDL(t, ctx, `CREATE USER MAPPING FOR `+spec+` SERVER srv`); err != nil {
				t.Fatalf("CREATE USER MAPPING FOR %s: %v", spec, err)
			}

			list := im.ListUserMappings(catalog.DefaultDBOid)
			if len(list) != 1 {
				t.Fatalf("ListUserMappings = %+v, want exactly 1 entry", list)
			}
			if list[0].UmUser != "alice" {
				t.Fatalf("UmUser = %q, want resolved connecting role name %q (not the literal keyword text)", list[0].UmUser, "alice")
			}
		})
	}
}

// TestCreateUserMappingCurrentUserFallsBackToBootstrapSuperuser covers the
// no-SET-ROLE-active case: CURRENT_USER must resolve to the bootstrap
// superuser's name ("postgres"), mirroring currentDDLOwnerOID's OID-10
// fallback used by every other "current_user" sentinel site.
func TestCreateUserMappingCurrentUserFallsBackToBootstrapSuperuser(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE SERVER srv FOREIGN DATA WRAPPER goopg_fdw`); err != nil {
		t.Fatalf("CREATE SERVER: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE USER MAPPING FOR CURRENT_USER SERVER srv`); err != nil {
		t.Fatalf("CREATE USER MAPPING FOR CURRENT_USER: %v", err)
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("ctx.Catalog is %T, want *catalog.InMemory", ctx.Catalog)
	}
	list := im.ListUserMappings(catalog.DefaultDBOid)
	if len(list) != 1 || list[0].UmUser != "postgres" {
		t.Fatalf("ListUserMappings = %+v, want exactly 1 entry with UmUser=postgres", list)
	}
}

// TestDropUserMappingCurrentUserResolvesSameAsCreate ensures a mapping
// created FOR CURRENT_USER can be dropped again FOR CURRENT_USER — real
// PostgreSQL's RemoveUserMapping (foreigncmds.c) resolves the RoleSpec the
// same way CreateUserMapping does, so both sides of the DDL must agree.
func TestDropUserMappingCurrentUserResolvesSameAsCreate(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.NonSuperuserRole = "bob"
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("ctx.Catalog is %T, want *catalog.InMemory", ctx.Catalog)
	}
	im.RegisterRole("bob")

	if err := runDDL(t, ctx, `CREATE SERVER srv FOREIGN DATA WRAPPER goopg_fdw`); err != nil {
		t.Fatalf("CREATE SERVER: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE USER MAPPING FOR CURRENT_USER SERVER srv`); err != nil {
		t.Fatalf("CREATE USER MAPPING FOR CURRENT_USER: %v", err)
	}
	if err := runDDL(t, ctx, `DROP USER MAPPING FOR CURRENT_USER SERVER srv`); err != nil {
		t.Fatalf("DROP USER MAPPING FOR CURRENT_USER: %v", err)
	}
	if list := im.ListUserMappings(catalog.DefaultDBOid); len(list) != 0 {
		t.Fatalf("after DROP USER MAPPING FOR CURRENT_USER, ListUserMappings = %+v, want empty", list)
	}
}

// TestCreateUserMappingPlainRoleNamePassesThrough guards against an
// over-broad sentinel match: a genuine role literally named e.g. "myuser"
// must round-trip unchanged, and PUBLIC must still resolve to the PUBLIC
// pseudo-role, not the connecting role.
func TestCreateUserMappingPlainRoleNamePassesThrough(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.NonSuperuserRole = "alice"
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("ctx.Catalog is %T, want *catalog.InMemory", ctx.Catalog)
	}
	im.RegisterRole("alice")
	im.RegisterRole("myuser")

	if err := runDDL(t, ctx, `CREATE SERVER srv FOREIGN DATA WRAPPER goopg_fdw`); err != nil {
		t.Fatalf("CREATE SERVER: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE USER MAPPING FOR myuser SERVER srv`); err != nil {
		t.Fatalf("CREATE USER MAPPING FOR myuser: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE USER MAPPING FOR PUBLIC SERVER srv`); err != nil {
		t.Fatalf("CREATE USER MAPPING FOR PUBLIC: %v", err)
	}

	list := im.ListUserMappings(catalog.DefaultDBOid)
	if len(list) != 2 {
		t.Fatalf("ListUserMappings = %+v, want exactly 2 entries", list)
	}
	var gotMyuser, gotPublic bool
	for _, m := range list {
		switch m.UmUser {
		case "myuser":
			gotMyuser = true
		case "public", "":
			gotPublic = true
		default:
			t.Fatalf("unexpected UmUser %q in %+v", m.UmUser, list)
		}
	}
	if !gotMyuser || !gotPublic {
		t.Fatalf("ListUserMappings = %+v, want one myuser entry and one public entry", list)
	}
}
