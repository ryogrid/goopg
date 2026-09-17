package postmaster

// M0143-0002g (items 2 of 2 — the pg_range write-site portion is carved out
// to M0143-0002h, see the deferral-ledger row dated 2026-09-17: fixing
// pgRangeRel's DBOid alone would regress restart durability because
// reloadUserRangeTypesFromHeap/RegisterRangeTypeDuringRecovery only ever
// scan/registers at cat.DBOID(), so it needs a paired read-side fix landed
// together, not a lone write-side swap).
//
// execDropDomain's two deleteTypeFromCatalogHeap calls and the GRANT/REVOKE
// ACL resync functions (resyncTypeACLHeapRow, resyncAttrACLHeapRow) still
// hardcoded catalog.DefaultDBOid even after M0143-0002f routed every other
// type-catalog write site through tableCatalogHeapDBOid(ctx). Both bugs are
// silent-corruption, not silent-loss:
//   - resyncTypeACLHeapRow's stale-row xmax stamp targeted the WRONG heap
//     (DefaultDBOid) while writeTypeHeapRowWithIndexes (already fixed by
//     M0143-0002f) appended the NEW row to the RIGHT heap (the DB's own) —
//     so a GRANT/REVOKE ON TYPE in a non-default database left the type's
//     pg_type row DUPLICATED (old un-stamped row + new row, same OID) in
//     that database's own heap.
//   - execDropDomain's xmax stamp on DROP DOMAIN made the same wrong-heap
//     mistake with no compensating write, so a DROP DOMAIN in a non-default
//     database silently failed to remove the physical row: the "dropped"
//     domain reappeared in pg_type after a restart.
import (
	"testing"

	"github.com/goopg/goopg/internal/initdb"
)

// TestDatabaseDDLTypeGrantOnTypeNonDefaultDBReload drives a GRANT ON TYPE
// against a domain declared in a non-default database through a genuine
// restart and checks the domain's pg_type row is not duplicated and its
// typacl reflects the grant.
func TestDatabaseDDLTypeGrantOnTypeNonDefaultDBReload(t *testing.T) {
	dir := t.TempDir() + "/data"
	if err := initdb.Init(initdb.Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatalf("initdb.Init: %v", err)
	}

	rt1, err := initdb.Open(initdb.OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatalf("initdb.Open: %v", err)
	}
	s1 := New(Config{Catalog: rt1.Catalog, Pool: rt1.Pool, TxnMgr: rt1.TxnMgr})

	handled, _, err := s1.tryHandleDatabaseDDL("CREATE DATABASE r1", "postgres", "", nil)
	if !handled || err != nil {
		t.Fatalf("CREATE DATABASE r1: handled=%v err=%v", handled, err)
	}

	runChainDDLDurable(t, rt1, s1, "r1", "CREATE DOMAIN acldomain AS int4")
	runChainDDLDurable(t, rt1, s1, "r1", "GRANT USAGE ON TYPE acldomain TO alice")

	if err := rt1.SaveCatalog(); err != nil {
		t.Fatalf("SaveCatalog: %v", err)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("rt1.Close: %v", err)
	}

	rt2, err := initdb.Open(initdb.OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatalf("initdb.Open (restart): %v", err)
	}
	defer rt2.Close()
	s2 := New(Config{Catalog: rt2.Catalog, Pool: rt2.Pool, TxnMgr: rt2.TxnMgr})

	rows, err := queryUnderDBReload(t, rt2, s2, "r1",
		"SELECT typacl FROM pg_type WHERE typname = 'acldomain'")
	if err != nil {
		t.Fatalf("db r1 post-restart: pg_type query: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("db r1 post-restart: acldomain pg_type rows = %d, want 1 (a stray un-stamped stale row means the ACL "+
			"resync's xmax stamp targeted the wrong database's heap)", len(rows))
	}
	if rows[0][0].IsNull() {
		t.Errorf("db r1 post-restart: acldomain typacl is NULL, want the GRANT USAGE reflected (resync wrote the new "+
			"row to the right heap but never invalidated in the right heap, or wrote to the wrong heap entirely)")
	}
}

// TestDatabaseDDLTypeDropDomainNonDefaultDBReload drives a DROP DOMAIN in a
// non-default database through a genuine restart and checks the domain's
// pg_type row is actually gone, not merely dropped from the in-memory
// registry.
func TestDatabaseDDLTypeDropDomainNonDefaultDBReload(t *testing.T) {
	dir := t.TempDir() + "/data"
	if err := initdb.Init(initdb.Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatalf("initdb.Init: %v", err)
	}

	rt1, err := initdb.Open(initdb.OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatalf("initdb.Open: %v", err)
	}
	s1 := New(Config{Catalog: rt1.Catalog, Pool: rt1.Pool, TxnMgr: rt1.TxnMgr})

	handled, _, err := s1.tryHandleDatabaseDDL("CREATE DATABASE r1", "postgres", "", nil)
	if !handled || err != nil {
		t.Fatalf("CREATE DATABASE r1: handled=%v err=%v", handled, err)
	}

	runChainDDLDurable(t, rt1, s1, "r1", "CREATE DOMAIN dropdomain AS int4")
	runChainDDLDurable(t, rt1, s1, "r1", "DROP DOMAIN dropdomain")

	if err := rt1.SaveCatalog(); err != nil {
		t.Fatalf("SaveCatalog: %v", err)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("rt1.Close: %v", err)
	}

	rt2, err := initdb.Open(initdb.OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatalf("initdb.Open (restart): %v", err)
	}
	defer rt2.Close()
	s2 := New(Config{Catalog: rt2.Catalog, Pool: rt2.Pool, TxnMgr: rt2.TxnMgr})

	rows, err := queryUnderDBReload(t, rt2, s2, "r1",
		"SELECT typname FROM pg_type WHERE typname = 'dropdomain'")
	if err != nil {
		t.Fatalf("db r1 post-restart: pg_type query: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("db r1 post-restart: dropdomain pg_type rows = %d, want 0 (DROP DOMAIN's xmax stamp targeted the "+
			"wrong database's heap, so the row survives)", len(rows))
	}
}
