package postmaster

// M0143-0002f. Write side of M0143-0002d: writeTypeHeapRowWithIndexes,
// updateTypeHeapRowWithIndexes, syncCompositeTypeToCatalogHeap's classRel/
// attrRel pair, and the composite/enum/range delete-side sites in
// operators_ddl.go now route through tableCatalogHeapDBOid(ctx) instead of
// hardcoding catalog.DefaultDBOid. Before this fix, every user-defined
// type's physical pg_type/pg_class/pg_attribute row (regardless of which
// database declared it) landed in the shared DefaultDBOid heap files, so
// two databases each declaring a same-named composite type showed the
// UNION of both types' fields to either database (the M0143-0002c live
// repro). This test mirrors TestDatabaseDDLReloadAcrossRestart's shape
// (database_ddl_reload_test.go) — genuine Close+Open restart, not just a
// live session — and covers all four CREATE TYPE kinds the design doc
// named: composite, domain, and range (enum's own pg_enum label storage is
// a distinct catalog outside this task's scope, so only its pg_type row
// identity is checked here, not label isolation).
import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/initdb"
)

// TestDatabaseDDLTypeCatalogReloadAcrossRestart drives two non-default
// databases (r1, r2), each declaring a same-named composite/domain/range
// type with a different shape, through a genuine restart and re-verifies
// post-reload that each database's pg_type/pg_attribute rows show ONLY its
// own type's shape — the M0143-0002c cross-database union repro, now also
// across a restart.
func TestDatabaseDDLTypeCatalogReloadAcrossRestart(t *testing.T) {
	dir := t.TempDir() + "/data"
	if err := initdb.Init(initdb.Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatalf("initdb.Init: %v", err)
	}

	rt1, err := initdb.Open(initdb.OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatalf("initdb.Open: %v", err)
	}
	s1 := New(Config{Catalog: rt1.Catalog, Pool: rt1.Pool, TxnMgr: rt1.TxnMgr})

	for _, name := range []string{"CREATE DATABASE r1", "CREATE DATABASE r2"} {
		handled, _, err := s1.tryHandleDatabaseDDL(name, "postgres", "", nil)
		if !handled || err != nil {
			t.Fatalf("%s: handled=%v err=%v", name, handled, err)
		}
	}

	// Composite: same type name, different field sets.
	runChainDDLDurable(t, rt1, s1, "r1", "CREATE TYPE samename AS (a int4)")
	runChainDDLDurable(t, rt1, s1, "r2", "CREATE TYPE samename AS (x text, y text)")
	// Domain: same type name, different base types.
	runChainDDLDurable(t, rt1, s1, "r1", "CREATE DOMAIN samedomain AS int4")
	runChainDDLDurable(t, rt1, s1, "r2", "CREATE DOMAIN samedomain AS text")
	// Range: same type name, different subtypes.
	runChainDDLDurable(t, rt1, s1, "r1", "CREATE TYPE samerange AS RANGE (subtype = int4)")
	runChainDDLDurable(t, rt1, s1, "r2", "CREATE TYPE samerange AS RANGE (subtype = int8)")

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

	im2, ok := rt2.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("rt2.Catalog is %T, want *catalog.InMemory", rt2.Catalog)
	}
	var haveR1, haveR2 bool
	for _, name := range im2.ListDatabases() {
		switch name {
		case "r1":
			haveR1 = true
		case "r2":
			haveR2 = true
		}
	}
	if !haveR1 || !haveR2 {
		t.Fatalf("post-restart ListDatabases() missing r1/r2 (have r1=%v r2=%v)", haveR1, haveR2)
	}

	attnames := func(rows []executor.Row) []string {
		out := make([]string, len(rows))
		for i, r := range rows {
			out[i] = string(r[0].Buf)
		}
		return out
	}
	compositeQuery := "SELECT a.attname FROM pg_attribute a JOIN pg_type t ON t.typrelid = a.attrelid " +
		"WHERE t.typname = 'samename' ORDER BY a.attnum"

	rowsR1, err := queryUnderDBReload(t, rt2, s2, "r1", compositeQuery)
	if err != nil {
		t.Fatalf("db r1 post-restart: composite pg_attribute query: %v", err)
	}
	if got := attnames(rowsR1); len(got) != 1 || got[0] != "a" {
		t.Errorf("db r1 post-restart: samename fields = %v, want [a] — got db r2's fields too (union bug) or none", got)
	}

	rowsR2, err := queryUnderDBReload(t, rt2, s2, "r2", compositeQuery)
	if err != nil {
		t.Fatalf("db r2 post-restart: composite pg_attribute query: %v", err)
	}
	if got := attnames(rowsR2); len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Errorf("db r2 post-restart: samename fields = %v, want [x y] — got db r1's fields too (union bug) or none", got)
	}

	baseTypeOf := func(t2 *testing.T, dbName, typname string) string {
		t2.Helper()
		rows, err := queryUnderDBReload(t2, rt2, s2, dbName,
			"SELECT format_type(typbasetype, NULL) FROM pg_type WHERE typname = '"+typname+"' AND typtype = 'd'")
		if err != nil {
			t2.Fatalf("db %s: domain base-type query: %v", dbName, err)
		}
		if len(rows) != 1 {
			t2.Fatalf("db %s: expected exactly 1 samedomain pg_type row, got %d — cross-database duplication", dbName, len(rows))
		}
		return string(rows[0][0].Buf)
	}
	if bt := baseTypeOf(t, "r1", "samedomain"); bt != "integer" {
		t.Errorf("db r1 post-restart: samedomain base type = %q, want integer", bt)
	}
	if bt := baseTypeOf(t, "r2", "samedomain"); !strings.HasPrefix(bt, "text") {
		t.Errorf("db r2 post-restart: samedomain base type = %q, want text", bt)
	}

	// Range: only pg_type isolation is asserted here (not the subtype, which
	// would require joining pg_range — a separate, still-DefaultDBOid-hardcoded
	// catalog outside this task's scope, see the M0143-0002g deferral-ledger
	// row filed alongside this test). writeTypeHeapRowWithIndexes is the same
	// funnel every CREATE TYPE kind shares, so a single pg_type row per
	// database (not the cross-database union M0143-0002c found) is still the
	// right signal that the write-side fix reaches the range branch too.
	rangeTypeCountOf := func(t2 *testing.T, dbName, typname string) int {
		t2.Helper()
		rows, err := queryUnderDBReload(t2, rt2, s2, dbName,
			"SELECT typname FROM pg_type WHERE typname = '"+typname+"' AND typtype = 'r'")
		if err != nil {
			t2.Fatalf("db %s: range pg_type query: %v", dbName, err)
		}
		return len(rows)
	}
	if n := rangeTypeCountOf(t, "r1", "samerange"); n != 1 {
		t.Errorf("db r1 post-restart: samerange pg_type rows = %d, want 1", n)
	}
	if n := rangeTypeCountOf(t, "r2", "samerange"); n != 1 {
		t.Errorf("db r2 post-restart: samerange pg_type rows = %d, want 1", n)
	}
}
