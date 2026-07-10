package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestCreateUserMappingSameNameAcrossDistinctDBOidDoesNotCollide pins the
// M0122-0007 4e follow-up 37 fix: RegisterUserMapping used to key the
// userMappings registry by bare (user, server) only, so two distinct
// databases' CREATE USER MAPPING of the same (user, server) pair silently
// collapsed onto one entry (last-writer-wins) instead of coexisting as
// PostgreSQL's own per-database pg_user_mappings does. Mirrors
// TestCreateServerSameNameAcrossDistinctDBOidDoesNotCollide
// (operators_ddl_foreign_server_durability_test.go).
func TestCreateUserMappingSameNameAcrossDistinctDBOidDoesNotCollide(t *testing.T) {
	const otherDBOid = 8802
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	if err := runDDL(t, ctx, `CREATE SERVER shared_srv FOREIGN DATA WRAPPER goopg_fdw`); err != nil {
		t.Fatalf("CREATE SERVER shared_srv (DefaultDBOid): %v", err)
	}
	if err := runDDL(t, ctx, `CREATE USER MAPPING FOR shared_user SERVER shared_srv OPTIONS (username 'a')`); err != nil {
		t.Fatalf("CREATE USER MAPPING (DefaultDBOid): %v", err)
	}
	ctx.CurrentDatabaseOid = otherDBOid
	if err := runDDL(t, ctx, `CREATE SERVER shared_srv FOREIGN DATA WRAPPER goopg_fdw`); err != nil {
		t.Fatalf("CREATE SERVER shared_srv (otherDBOid): %v", err)
	}
	if err := runDDL(t, ctx, `CREATE USER MAPPING FOR shared_user SERVER shared_srv OPTIONS (username 'b')`); err != nil {
		t.Fatalf("CREATE USER MAPPING (otherDBOid): %v", err)
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("ctx.Catalog is %T, want *catalog.InMemory", ctx.Catalog)
	}

	defaultList := im.ListUserMappings(catalog.DefaultDBOid)
	if len(defaultList) != 1 || len(defaultList[0].Options) == 0 || defaultList[0].Options[0] != "username=a" {
		t.Fatalf("DefaultDBOid's ListUserMappings = %+v, want exactly 1 entry with Options=[username=a]", defaultList)
	}
	otherList := im.ListUserMappings(otherDBOid)
	if len(otherList) != 1 || len(otherList[0].Options) == 0 || otherList[0].Options[0] != "username=b" {
		t.Fatalf("otherDBOid's ListUserMappings = %+v, want exactly 1 entry with Options=[username=b]", otherList)
	}
	if defaultList[0].OID == otherList[0].OID {
		t.Fatalf("DefaultDBOid and otherDBOid's same-named mapping share OID %d — they collided into one entry", defaultList[0].OID)
	}

	// DROP USER MAPPING in one database must not remove the other's
	// same-named mapping (mirrors the drop half of the fix).
	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	if err := runDDL(t, ctx, `DROP USER MAPPING FOR shared_user SERVER shared_srv`); err != nil {
		t.Fatalf("DROP USER MAPPING (DefaultDBOid): %v", err)
	}
	if list := im.ListUserMappings(catalog.DefaultDBOid); len(list) != 0 {
		t.Fatalf("after DROP USER MAPPING, DefaultDBOid's ListUserMappings = %+v, want empty", list)
	}
	if list := im.ListUserMappings(otherDBOid); len(list) != 1 {
		t.Fatalf("after DefaultDBOid's DROP USER MAPPING, otherDBOid's ListUserMappings = %+v, want unchanged 1 entry", list)
	}
}
