package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestReversePathColdStartOpensWithoutCache verifies the M0130-S2 reverse
// path: Open() succeeds when the goopg catalog cache is absent (simulating
// a PG-created data dir). The system catalog is populated by
// registerSystemTables, and any user tables in the pg_class heap are
// loaded by loadUserTablesFromHeap.
//
// The pg_class heap written by goopg Init uses PG-compatible physical
// format (pgClassColDefs / pgClassColumnsPG18), so the same
// DecodePGClassPhysicalRow fallback that reads PG-created rows also reads
// goopg-created rows — the cold-start path works for both.
func TestReversePathColdStartOpensWithoutCache(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	// Remove the goopg catalog cache before the first Open to simulate a
	// PG-created data dir (no goopg-specific files). The first Open
	// normally writes this cache after a successful heap scan, but we
	// remove it ahead of time to force the cold-start path.
	for _, dbOid := range []uint32{1, 5} {
		cachePath := catalogCachePath(dir, dbOid)
		_ = os.Remove(cachePath) // may not exist (first start)
	}

	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatalf("Open without cache (reverse path): %v", err)
	}
	defer rt.Close()

	cat := rt.Catalog.(*catalog.InMemory)

	// System catalogs must be present (from registerSystemTables).
	if tbl, ok := cat.LookupTableByOID(catalog.RelationRelationId); !ok || tbl == nil {
		t.Fatal("pg_class (OID 1259) not found in catalog after cold start")
	}

	// pg_type must be present.
	if tbl, ok := cat.LookupTableByOID(catalog.TypeRelationId); !ok || tbl == nil {
		t.Fatal("pg_type (OID 1247) not found in catalog after cold start")
	}

	// pg_attribute must be present.
	if tbl, ok := cat.LookupTableByOID(catalog.AttributeRelationId); !ok || tbl == nil {
		t.Fatal("pg_attribute (OID 1249) not found in catalog after cold start")
	}

	t.Logf("Cold-start catalog OK: %d tables loaded", len(cat.AllTables()))
}
