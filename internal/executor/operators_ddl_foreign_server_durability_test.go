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
