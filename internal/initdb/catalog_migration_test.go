package initdb

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// createLegacyCluster creates an initdb'd data directory where a user table
// is in pg_catalog.json but NOT in the pg_class heap (simulating a cluster
// created before M0030-0001 DDL-sync). The table is added via the raw catalog
// API (bypassing the executor's syncTableToCatalogHeap path) so it goes
// to JSON but not to the heap.
func createLegacyCluster(t *testing.T) (dir string, tableOID uint32) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	// Open briefly to get the runtime state.
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 16})
	if err != nil {
		t.Fatal(err)
	}

	// Add a table via the raw catalog API — this does NOT go through the
	// executor's DDL-sync path and therefore never writes to pg_class heap.
	inMem := rt.Catalog.(*catalog.InMemory)
	tbl, err := inMem.CreateTable(
		parser.ObjectName{Name: "legacy_tbl"},
		[]catalog.Column{
			{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
			{Name: "val", Type: catalog.Type{Name: "text"}},
		},
	)
	if err != nil {
		rt.Close()
		t.Fatal(err)
	}
	tableOID = tbl.OID

	// Save catalog to JSON only — not to pg_class heap.
	if err := rt.SaveCatalog(); err != nil {
		rt.Close()
		t.Fatal(err)
	}
	rt.Close()
	return dir, tableOID
}

// scanPGClassUserRows reads pg_class and returns all rows with OID >= FirstUserOID.
func scanPGClassUserRows(t *testing.T, dir string) []catalog.PGClassRow {
	t.Helper()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	defer mgr.Close()

	rel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: catalog.RelationRelationId,
		Fork:   storage.MainFork,
	}
	nBlocks, err := mgr.NBlocks(rel)
	if err != nil {
		t.Fatalf("NBlocks pg_class: %v", err)
	}

	page := make(storage.Page, storage.BlockSize)
	var rows []catalog.PGClassRow
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		if err := mgr.ReadBlock(rel, blk, page); err != nil {
			t.Fatalf("ReadBlock pg_class blk %d: %v", blk, err)
		}
		count, _ := storage.PageLinePointerCount(page)
		for slot := uint16(1); slot <= uint16(count); slot++ {
			ht, err := storage.PageGetHeapTuple(page, slot)
			if err != nil {
				continue
			}
			if ht.Header.Xmin == storage.InvalidTransactionID || ht.Header.Xmax != storage.InvalidTransactionID {
				continue
			}
			row, err := catalog.DecodePGClassRow(ht.Data)
			if err != nil {
				continue
			}
			if row.OID >= catalog.FirstUserOID {
				rows = append(rows, row)
			}
		}
	}
	return rows
}

// TestMigrationFromLegacyJSONCluster verifies that maybeMigrateCatalogToHeap
// writes the in-memory catalog (loaded from JSON) into pg_class/pg_attribute
// when those heap tables contain no user-table rows.
func TestMigrationFromLegacyJSONCluster(t *testing.T) {
	dir, tableOID := createLegacyCluster(t)

	// Confirm pg_class has no user-table rows before migration.
	rowsBefore := scanPGClassUserRows(t, dir)
	if len(rowsBefore) != 0 {
		t.Fatalf("expected 0 user rows in pg_class before migration, got %d", len(rowsBefore))
	}

	// Open — maybeMigrateCatalogToHeap should fire.
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 16})
	if err != nil {
		t.Fatal(err)
	}
	rt.Close()

	// pg_class should now have a user-table row for legacy_tbl.
	rowsAfter := scanPGClassUserRows(t, dir)
	if len(rowsAfter) == 0 {
		t.Fatal("migration did not write any user rows to pg_class")
	}

	found := false
	for _, r := range rowsAfter {
		if r.OID == tableOID && r.RelName == "legacy_tbl" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("legacy_tbl (OID %d) not found in pg_class after migration; rows: %v",
			tableOID, rowsAfter)
	}
}

// TestMigrationIdempotent verifies that running Open twice on a legacy cluster
// doesn't duplicate the migrated rows in pg_class.
func TestMigrationIdempotent(t *testing.T) {
	dir, tableOID := createLegacyCluster(t)

	// First open: triggers migration.
	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 16})
	if err != nil {
		t.Fatal(err)
	}
	rt1.Close()

	rowsAfterFirst := scanPGClassUserRows(t, dir)
	countFirst := 0
	for _, r := range rowsAfterFirst {
		if r.OID == tableOID {
			countFirst++
		}
	}
	if countFirst != 1 {
		t.Fatalf("expected 1 row for legacy_tbl after first open, got %d", countFirst)
	}

	// Second open: migration must NOT run again (pg_class already has user rows).
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 16})
	if err != nil {
		t.Fatal(err)
	}
	rt2.Close()

	rowsAfterSecond := scanPGClassUserRows(t, dir)
	countSecond := 0
	for _, r := range rowsAfterSecond {
		if r.OID == tableOID {
			countSecond++
		}
	}
	if countSecond != 1 {
		t.Fatalf("expected 1 row for legacy_tbl after second open (idempotent), got %d", countSecond)
	}
}

// TestMigrationNewClusterIsNoOp verifies that a fresh cluster (empty catalog)
// does not trigger migration — maybeMigrateCatalogToHeap is a no-op when
// there are no user tables in JSON.
func TestMigrationNewClusterIsNoOp(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	// Open a fresh cluster — no user tables → no migration expected.
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 16})
	if err != nil {
		t.Fatal(err)
	}
	rt.Close()

	// pg_class should still have 0 user-table rows.
	rows := scanPGClassUserRows(t, dir)
	if len(rows) != 0 {
		t.Errorf("fresh cluster should have 0 user rows in pg_class, got %d", len(rows))
	}
}

// TestMigrationPGAttributeRowsWritten verifies that column definitions are
// also migrated to pg_attribute when user tables are migrated from JSON.
func TestMigrationPGAttributeRowsWritten(t *testing.T) {
	dir, tableOID := createLegacyCluster(t)

	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 16})
	if err != nil {
		t.Fatal(err)
	}
	rt.Close()

	// Scan pg_attribute for our table's columns via the storage manager.
	attrRows := scanAttrViaManager(t, dir, tableOID)
	if len(attrRows) != 2 {
		t.Fatalf("pg_attribute: expected 2 columns for legacy_tbl, got %d", len(attrRows))
	}
	names := map[string]bool{}
	for _, r := range attrRows {
		names[r.AttName] = true
	}
	if !names["id"] || !names["val"] {
		t.Errorf("pg_attribute: missing columns, got names: %v", names)
	}
}

func scanAttrViaManager(t *testing.T, dir string, relOID uint32) []catalog.PGAttributeRow {
	t.Helper()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	defer mgr.Close()

	rel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: catalog.AttributeRelationId,
		Fork:   storage.MainFork,
	}
	nBlocks, err := mgr.NBlocks(rel)
	if err != nil {
		t.Fatalf("NBlocks pg_attribute: %v", err)
	}

	page := make(storage.Page, storage.BlockSize)
	var out []catalog.PGAttributeRow
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		if err := mgr.ReadBlock(rel, blk, page); err != nil {
			t.Fatalf("ReadBlock pg_attribute blk %d: %v", blk, err)
		}
		count, _ := storage.PageLinePointerCount(page)
		for slot := uint16(1); slot <= uint16(count); slot++ {
			ht, err := storage.PageGetHeapTuple(page, slot)
			if err != nil {
				continue
			}
			if ht.Header.Xmin == storage.InvalidTransactionID || ht.Header.Xmax != storage.InvalidTransactionID {
				continue
			}
			row, err := catalog.DecodePGAttributeRow(ht.Data)
			if err != nil {
				continue
			}
			if row.AttRelID == relOID {
				out = append(out, row)
			}
		}
	}
	return out
}

// suppress unused import warning
var _ = fmt.Sprint
