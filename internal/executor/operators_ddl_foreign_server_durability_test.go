package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestDropServerGatesOnForeignServerRegistryNotCompatObjects pins the
// M0122-0007 foreign-server registry restart-durability fix. Before this fix,
// DROP SERVER's existence check gated on the non-durable
// compatObjects["server"] bookkeeping, which never survived a restart even
// though the durable foreignServers registry (now WAL-persisted) did — so a
// server recovered by WAL replay after a crash could never be dropped again
// ("does not exist"). Simulate that post-restart shape directly: populate
// foreignServers via the *DuringRecovery path (what WAL replay calls) without
// ever touching compatObjects (what a live CREATE SERVER would also set), and
// confirm DROP SERVER still succeeds.
func TestDropServerGatesOnForeignServerRegistryNotCompatObjects(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatalf("Catalog is %T, expected *catalog.InMemory", cat)
	}
	im.RegisterForeignServerDuringRecovery("recovered_srv", "postgres_fdw", "", "", nil, 50123)
	if im.ForeignServerOID("recovered_srv") == 0 {
		t.Fatalf("setup: recovered_srv not registered")
	}

	if err := runDDL(t, ctx, `DROP SERVER recovered_srv`); err != nil {
		t.Fatalf("DROP SERVER recovered_srv (post-restart shape): %v", err)
	}
	if oid := im.ForeignServerOID("recovered_srv"); oid != 0 {
		t.Fatalf("after DROP SERVER, ForeignServerOID(recovered_srv) = %d, want 0", oid)
	}
}

// TestDropForeignDataWrapperCascadeUsesForeignServerRegistry pins the CASCADE
// half of the same fix: DROP FOREIGN DATA WRAPPER ... CASCADE must discover
// dependent servers via the durable foreignServers registry (filtered by
// FdwName), not the non-durable "fdw-server:fdwname:servername" compatObjects
// association, so a server recovered by WAL replay after a crash is still
// cascade-dropped instead of silently surviving while the statement reports
// success.
func TestDropForeignDataWrapperCascadeUsesForeignServerRegistry(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatalf("Catalog is %T, expected *catalog.InMemory", cat)
	}
	im.RegisterForeignDataWrapper("recovered_fdw", nil)
	im.RegisterForeignServerDuringRecovery("cascaded_srv", "recovered_fdw", "", "", nil, 50124)
	if im.ForeignServerOID("cascaded_srv") == 0 {
		t.Fatalf("setup: cascaded_srv not registered")
	}

	if err := runDDL(t, ctx, `DROP FOREIGN DATA WRAPPER recovered_fdw CASCADE`); err != nil {
		t.Fatalf("DROP FOREIGN DATA WRAPPER ... CASCADE (post-restart shape): %v", err)
	}
	if oid := im.ForeignServerOID("cascaded_srv"); oid != 0 {
		t.Fatalf("after CASCADE drop, ForeignServerOID(cascaded_srv) = %d, want 0 (should have cascaded)", oid)
	}
}

// TestCreateServerRegistersForeignServerWithCapturedOID confirms the CREATE
// SERVER call site now threads RegisterForeignServer's actual return value
// (name/fdw/type/version/options/OID) instead of discarding it, which the
// M0122-0007 WAL-emit fix depends on to persist the real assigned OID.
func TestCreateServerRegistersForeignServerWithCapturedOID(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE SERVER srv1 TYPE 'prod' VERSION '9.1' FOREIGN DATA WRAPPER goopg_fdw OPTIONS (host 'localhost')`); err != nil {
		t.Fatalf("CREATE SERVER: %v", err)
	}
	im := cat.(*catalog.InMemory)
	list := im.ListForeignServers()
	if len(list) != 1 {
		t.Fatalf("ListForeignServers = %+v, want exactly 1 entry", list)
	}
	srv := list[0]
	if srv.Name != "srv1" || srv.FdwName != "goopg_fdw" || srv.Type != "prod" || srv.Version != "9.1" {
		t.Fatalf("registered server = %+v, want name=srv1 fdw=goopg_fdw type=prod version=9.1", srv)
	}
	if srv.OID == 0 {
		t.Fatalf("registered server OID = 0, want a minted OID")
	}
}

// TestCreateServerSameNameAcrossDistinctDBOidDoesNotCollide pins the
// M0122-0007 4e follow-up 36 fix: RegisterForeignServer used to key the
// foreignServers registry by bare name only, so two distinct databases'
// CREATE SERVER of the same name silently collapsed onto one entry
// (last-writer-wins) instead of erroring or coexisting as PostgreSQL's own
// per-database pg_foreign_server does. Confirmed to fail against a revert of
// just foreignServerKey's dbOid component (a same-signature neutered key
// that folds dbOid to a no-op).
func TestCreateServerSameNameAcrossDistinctDBOidDoesNotCollide(t *testing.T) {
	const otherDBOid = 8801
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	if err := runDDL(t, ctx, `CREATE SERVER shared TYPE 'a' FOREIGN DATA WRAPPER goopg_fdw`); err != nil {
		t.Fatalf("CREATE SERVER shared (DefaultDBOid): %v", err)
	}
	ctx.CurrentDatabaseOid = otherDBOid
	if err := runDDL(t, ctx, `CREATE SERVER shared TYPE 'b' FOREIGN DATA WRAPPER goopg_fdw`); err != nil {
		t.Fatalf("CREATE SERVER shared (otherDBOid): %v", err)
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("ctx.Catalog is %T, want *catalog.InMemory", ctx.Catalog)
	}

	defaultOID := im.ForeignServerOID("shared", catalog.DefaultDBOid)
	otherOID := im.ForeignServerOID("shared", otherDBOid)
	if defaultOID == 0 || otherOID == 0 {
		t.Fatalf("ForeignServerOID: default=%d other=%d, want both non-zero", defaultOID, otherOID)
	}
	if defaultOID == otherOID {
		t.Fatalf("DefaultDBOid and otherDBOid's same-named \"shared\" server share OID %d — they collided into one entry", defaultOID)
	}

	defaultList := im.ListForeignServers(catalog.DefaultDBOid)
	if len(defaultList) != 1 || defaultList[0].Type != "a" {
		t.Fatalf("DefaultDBOid's ListForeignServers = %+v, want exactly 1 entry with Type=a", defaultList)
	}
	otherList := im.ListForeignServers(otherDBOid)
	if len(otherList) != 1 || otherList[0].Type != "b" {
		t.Fatalf("otherDBOid's ListForeignServers = %+v, want exactly 1 entry with Type=b", otherList)
	}

	// DROP SERVER in one database must not remove the other's same-named
	// server (mirrors the drop half of the fix).
	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	if err := runDDL(t, ctx, `DROP SERVER shared`); err != nil {
		t.Fatalf("DROP SERVER shared (DefaultDBOid): %v", err)
	}
	if oid := im.ForeignServerOID("shared", catalog.DefaultDBOid); oid != 0 {
		t.Fatalf("after DROP SERVER, DefaultDBOid's ForeignServerOID(shared) = %d, want 0", oid)
	}
	if oid := im.ForeignServerOID("shared", otherDBOid); oid != otherOID {
		t.Fatalf("after DefaultDBOid's DROP SERVER, otherDBOid's ForeignServerOID(shared) = %d, want unchanged %d", oid, otherOID)
	}
}
