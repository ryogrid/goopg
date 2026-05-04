package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestCreateTableSurvivesRestartViaCatalogHeap is the definitive test for
// M0030-0003: after CREATE TABLE, even if the JSON snapshot is deleted,
// the table is recoverable from the pg_class/pg_attribute heap files.
//
// Flow:
//  1. Init + Open + CREATE TABLE + SaveCatalog + Close (writes both JSON and heap)
//  2. Delete pg_catalog.json
//  3. Re-Open: JSON absent → loadCatalogSnapshot is a no-op
//             heap scan → loadUserTablesFromHeap finds the table
//  4. Assert table is in the catalog with correct columns
func TestCreateTableSurvivesRestartViaCatalogHeap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	// Phase 1: create table, save catalog, close.
	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt1, "CREATE TABLE heap_test (id int4 NOT NULL, val text)")
	if err := rt1.SaveCatalog(); err != nil {
		rt1.Close()
		t.Fatal(err)
	}
	rt1.Close()

	// Phase 2: remove the JSON snapshot.
	jsonPath := filepath.Join(dir, CatalogSnapshotFile)
	if err := os.Remove(jsonPath); err != nil {
		t.Fatalf("remove JSON snapshot: %v", err)
	}

	// Phase 3: re-open — table must load from heap only.
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	// Phase 4: verify.
	tbl, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: "heap_test"})
	if !ok {
		t.Fatal("heap_test not found in catalog after JSON deletion — heap recovery failed")
	}
	if tbl.OID == 0 {
		t.Error("heap_test has OID=0 (zero value)")
	}
	if len(tbl.Columns) != 2 {
		t.Fatalf("heap_test has %d columns, want 2", len(tbl.Columns))
	}
	if got := tbl.Columns[0].Name; got != "id" {
		t.Errorf("col[0].name=%q want id", got)
	}
	if got := tbl.Columns[1].Name; got != "val" {
		t.Errorf("col[1].name=%q want val", got)
	}
}

// TestMultipleTablesLoadFromHeap verifies that multiple user tables created
// via DDL-sync are all recovered from heap when the JSON is absent.
func TestMultipleTablesLoadFromHeap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt1, "CREATE TABLE alpha (a int4, b int4)")
	runDDL(t, rt1, "CREATE TABLE beta (x text NOT NULL, y bool)")
	runDDL(t, rt1, "CREATE TABLE gamma (n int8)")
	if err := rt1.SaveCatalog(); err != nil {
		rt1.Close()
		t.Fatal(err)
	}
	rt1.Close()

	// Delete JSON.
	_ = os.Remove(filepath.Join(dir, CatalogSnapshotFile))

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	for _, name := range []string{"alpha", "beta", "gamma"} {
		if _, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: name}); !ok {
			t.Errorf("table %q missing after JSON deletion", name)
		}
	}

	// Column spot-check for beta.
	beta, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: "beta"})
	if !ok {
		t.Fatal("beta not found")
	}
	if len(beta.Columns) != 2 {
		t.Fatalf("beta has %d cols, want 2", len(beta.Columns))
	}
}

// TestHeapLoadIdempotentWithJSON verifies that when both JSON and heap have
// a table, the result is still correct (no duplicate, no panic).
func TestHeapLoadIdempotentWithJSON(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt1, "CREATE TABLE shared (id int4)")
	if err := rt1.SaveCatalog(); err != nil {
		rt1.Close()
		t.Fatal(err)
	}
	rt1.Close()

	// Re-open with BOTH JSON and heap present.
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatalf("Open with both JSON and heap: %v", err)
	}
	defer rt2.Close()

	tbl, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: "shared"})
	if !ok {
		t.Fatal("shared not found")
	}
	if len(tbl.Columns) != 1 {
		t.Errorf("shared has %d cols, want 1", len(tbl.Columns))
	}
}
