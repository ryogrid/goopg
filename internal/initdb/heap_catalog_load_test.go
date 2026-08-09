package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestCreateTableSurvivesRestartViaCatalogHeap verifies that after CREATE TABLE,
// the table is recoverable from the pg_class/pg_attribute heap files on restart.
// This is the sole catalog recovery path — no JSON is written or read.
func TestCreateTableSurvivesRestartViaCatalogHeap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}

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

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	tbl, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: "heap_test"})
	if !ok {
		t.Fatal("heap_test not found in catalog after restart — heap recovery failed")
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
// via DDL-sync are all recovered from heap on restart.
func TestMultipleTablesLoadFromHeap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
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

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	for _, name := range []string{"alpha", "beta", "gamma"} {
		if _, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: name}); !ok {
			t.Errorf("table %q missing after restart", name)
		}
	}

	beta, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: "beta"})
	if !ok {
		t.Fatal("beta not found")
	}
	if len(beta.Columns) != 2 {
		t.Fatalf("beta has %d cols, want 2", len(beta.Columns))
	}
}

// TestHeapLoadIdempotent verifies that opening twice doesn't corrupt the catalog.
func TestHeapLoadIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
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

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatalf("second Open: %v", err)
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

// TestBpCharVarcharTypModRestoredAfterRestart verifies M0119-0010: after a
// server restart, bpchar/varchar columns restore their length (Type.Args) from
// the pg_attribute heap's atttypmod field. Before the fix, Args was always nil
// after reload, so every character column was checked against the default
// char(1) length.
func TestBpCharVarcharTypModRestoredAfterRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	// ct(a char(1), b char(25), c varchar(50))
	runDDL(t, rt1, `CREATE TABLE ct (
		a character(1),
		b character(25),
		c character varying(50)
	)`)
	if err := rt1.SaveCatalog(); err != nil {
		rt1.Close()
		t.Fatal(err)
	}
	rt1.Close()

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	tbl, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: "ct"})
	if !ok {
		t.Fatal("ct not found in catalog after restart")
	}
	if len(tbl.Columns) != 3 {
		t.Fatalf("ct has %d columns, want 3", len(tbl.Columns))
	}

	// Column "a" — char(1): Args should be [1] (default, no typmod). PG
	// stores 1 + VARHDRSZ = 5; goopg's pgTypeArgsFromTypmod subtracts 4 → 1.
	// If AttTypMod was zero (unset), Args is nil and the codec falls back to n=1,
	// which happens to match — so we also check that Args is genuinely present.
	colA := tbl.Columns[0]
	if colA.Name != "a" {
		t.Errorf("col[0].Name=%q want a", colA.Name)
	}
	if colA.Type.Name != "bpchar" {
		t.Errorf("col[0].Type.Name=%q want bpchar", colA.Type.Name)
	}
	if len(colA.Type.Args) != 1 || colA.Type.Args[0] != 1 {
		t.Errorf("col[0].Type.Args=%v want [1]", colA.Type.Args)
	}

	// Column "b" — char(25): Args should be [25].
	colB := tbl.Columns[1]
	if colB.Name != "b" {
		t.Errorf("col[1].Name=%q want b", colB.Name)
	}
	if colB.Type.Name != "bpchar" {
		t.Errorf("col[1].Type.Name=%q want bpchar", colB.Type.Name)
	}
	if len(colB.Type.Args) != 1 || colB.Type.Args[0] != 25 {
		t.Errorf("col[1].Type.Args=%v want [25]", colB.Type.Args)
	}

	// Column "c" — varchar(50): Args should be [50].
	colC := tbl.Columns[2]
	if colC.Name != "c" {
		t.Errorf("col[2].Name=%q want c", colC.Name)
	}
	if colC.Type.Name != "varchar" {
		t.Errorf("col[2].Type.Name=%q want varchar", colC.Type.Name)
	}
	if len(colC.Type.Args) != 1 || colC.Type.Args[0] != 50 {
		t.Errorf("col[2].Type.Args=%v want [50]", colC.Type.Args)
	}
}
