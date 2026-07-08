package initdb

// Tests for M0122-0007's tablespace-restart-durability follow-up:
// pg_class.reltablespace (Table.Tablespace / Index.Tablespace) previously
// reset to 0 (database default) across every restart, because
// loadUserTablesFromHeap / loadUserIndexesFromHeap never decoded the
// heap-persisted reltablespace column back into the catalog. See the
// deferral ledger row appended alongside the CREATE INDEX / ALTER
// TABLE/INDEX ... SET TABLESPACE follow-up for the exact resume point this
// closes.

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// runCreateTablespaceInPlace issues `CREATE TABLESPACE name LOCATION ''`
// with allow_in_place_tablespaces effectively "on" (the shared runDDL test
// helper never wires ctx.GetSetting, so execCreateTablespace's GUC check
// otherwise always sees it off and rejects the empty in-place location as
// "not an absolute path" — this is the only DDL statement in this file that
// needs the GUC, so it gets its own minimal pipeline invocation rather than
// changing the shared helper other tests in this package rely on).
func runCreateTablespaceInPlace(t *testing.T, rt *Runtime, name string) {
	t.Helper()
	sql := "CREATE TABLESPACE " + name + " LOCATION ''"
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	plan, err := planner.Plan(stmts[0], rt.Catalog)
	if err != nil {
		t.Fatalf("plan %q: %v", sql, err)
	}
	op, err := executor.Build(plan)
	if err != nil {
		t.Fatalf("build %q: %v", sql, err)
	}
	tx, err := rt.TxnMgr.Begin(mvcc.IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	snap, err := rt.TxnMgr.SnapshotFor(tx)
	if err != nil {
		_ = rt.TxnMgr.Commit(tx)
		t.Fatalf("SnapshotFor: %v", err)
	}
	ctx := executor.NewContext()
	ctx.Pool = rt.Pool
	ctx.Catalog = rt.Catalog
	ctx.TxnMgr = rt.TxnMgr
	ctx.Tx = tx
	ctx.Snap = snap
	ctx.GetSetting = func(setting string) (string, bool) {
		if setting == "allow_in_place_tablespaces" {
			return "on", true
		}
		return "", false
	}
	if err := op.Open(ctx); err != nil {
		_ = rt.TxnMgr.Commit(tx)
		t.Fatalf("DDL Open %q: %v", sql, err)
	}
	if _, nextErr := op.Next(); nextErr != nil {
		op.Close()
		_ = rt.TxnMgr.Commit(tx)
		t.Fatalf("DDL Next %q: %v", sql, nextErr)
	}
	op.Close()
	if err := rt.TxnMgr.Commit(tx); err != nil {
		t.Fatalf("commit after DDL %q: %v", sql, err)
	}
}

// TestTableTablespaceSurvivesRestartViaCatalogHeap verifies that a table
// created with an explicit TABLESPACE clause keeps reporting the correct
// catalog.Table.Tablespace OID after a full close/reopen (heap-driven
// recovery, no JSON snapshot).
func TestTableTablespaceSurvivesRestartViaCatalogHeap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runCreateTablespaceInPlace(t, rt1, "ts1")
	runDDL(t, rt1, "CREATE TABLE ts_test (id int4) TABLESPACE ts1")

	wantOID, ok := rt1.Catalog.LookupTablespaceOID("ts1")
	if !ok {
		t.Fatal("ts1 not found in catalog before restart")
	}
	tbl1, ok := rt1.Catalog.LookupTable(parser.ObjectName{Name: "ts_test"})
	if !ok || tbl1.Tablespace != wantOID {
		t.Fatalf("before restart: Tablespace = %v (ok=%v), want %d", tbl1, ok, wantOID)
	}
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

	tbl2, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: "ts_test"})
	if !ok {
		t.Fatal("ts_test not found in catalog after restart")
	}
	if tbl2.Tablespace != wantOID {
		t.Errorf("after restart: Tablespace = %d, want %d (reverted to database default)", tbl2.Tablespace, wantOID)
	}
}

// TestIndexTablespaceSurvivesRestartViaCatalogHeap is the index sibling of
// TestTableTablespaceSurvivesRestartViaCatalogHeap: a btree index created
// with an explicit TABLESPACE clause must keep reporting the correct
// catalog.Index.Tablespace OID after a full close/reopen.
func TestIndexTablespaceSurvivesRestartViaCatalogHeap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runCreateTablespaceInPlace(t, rt1, "ts1")
	runDDL(t, rt1, "CREATE TABLE ts_idx_test (id int4)")
	runDDL(t, rt1, "CREATE INDEX ts_idx ON ts_idx_test (id) TABLESPACE ts1")

	wantOID, ok := rt1.Catalog.LookupTablespaceOID("ts1")
	if !ok {
		t.Fatal("ts1 not found in catalog before restart")
	}
	idx1, ok := rt1.Catalog.LookupIndex(parser.ObjectName{Name: "ts_idx"})
	if !ok || idx1.Tablespace != wantOID {
		t.Fatalf("before restart: Tablespace = %v (ok=%v), want %d", idx1, ok, wantOID)
	}
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

	idx2, ok := rt2.Catalog.LookupIndex(parser.ObjectName{Name: "ts_idx"})
	if !ok {
		t.Fatal("ts_idx not found in catalog after restart")
	}
	if idx2.Tablespace != wantOID {
		t.Errorf("after restart: Tablespace = %d, want %d (reverted to database default)", idx2.Tablespace, wantOID)
	}
}

// TestAlterTableSetTablespaceSurvivesRestartViaCatalogHeap verifies that
// ALTER TABLE ... SET TABLESPACE — not just CREATE TABLE ... TABLESPACE —
// resyncs the heap-persisted reltablespace column immediately, so an
// uncheckpointed restart right after the ALTER doesn't lose the change.
func TestAlterTableSetTablespaceSurvivesRestartViaCatalogHeap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runCreateTablespaceInPlace(t, rt1, "ts1")
	runDDL(t, rt1, "CREATE TABLE ts_alter_test (id int4)")
	runDDL(t, rt1, "ALTER TABLE ts_alter_test SET TABLESPACE ts1")

	wantOID, ok := rt1.Catalog.LookupTablespaceOID("ts1")
	if !ok {
		t.Fatal("ts1 not found in catalog before restart")
	}
	// No SaveCatalog call here — this must survive purely on the per-statement
	// heap resync the ALTER action performs, mirroring an uncheckpointed
	// crash restart.
	rt1.Close()

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	tbl2, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: "ts_alter_test"})
	if !ok {
		t.Fatal("ts_alter_test not found in catalog after restart")
	}
	if tbl2.Tablespace != wantOID {
		t.Errorf("after restart: Tablespace = %d, want %d (ALTER TABLE SET TABLESPACE not durable)", tbl2.Tablespace, wantOID)
	}
}
