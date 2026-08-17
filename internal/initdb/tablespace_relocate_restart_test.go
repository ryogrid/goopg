package initdb

// Test for M0122-0007's tablespace PHYSICAL relocation follow-up: unlike
// tablespace_restart_test.go (which only proves pg_class.reltablespace
// survives a restart), this proves the relation's actual DATA — physically
// moved by ALTER TABLE/INDEX ... SET TABLESPACE into pg_tblspc/<oid>/... —
// is still readable after a full close/reopen. Before this fix, SET
// TABLESPACE never moved the file at all, so this scenario was untestable;
// after it, a restart must resolve the table's RelFileNode (now carrying the
// new TblOid) to the file the ALTER actually wrote.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
)

// runDDLRealDataDir is runDDL (ddl_catalog_sync_test.go) plus ctx.DataDir and
// allow_in_place_tablespaces wiring — needed here because this test exercises
// real physical file relocation, which is a no-op without ctx.DataDir set
// (see relocateRelationPhysicalFile's early-return in operators_ddl.go).
func runDDLRealDataDir(t *testing.T, rt *Runtime, sql string) {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	plan, err := optimizer.Plan(stmts[0], rt.Catalog)
	if err != nil {
		t.Fatalf("plan %q: %v", sql, err)
	}
	op, err := executor.Build(plan)
	if err != nil {
		t.Fatalf("build %q: %v", sql, err)
	}
	tx, err := rt.TxnMgr.Begin(transam.IsolationReadCommitted)
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
	ctx.DataDir = rt.DataDir
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
	// DDL operators return a nil error from their first Next() (no output
	// rows); a plain INSERT with no RETURNING clause instead reports
	// completion via executor.EOF directly — both are success here.
	if _, nextErr := op.Next(); nextErr != nil && nextErr != executor.EOF {
		op.Close()
		_ = rt.TxnMgr.Commit(tx)
		t.Fatalf("DDL Next %q: %v", sql, nextErr)
	}
	op.Close()
	if err := rt.TxnMgr.Commit(tx); err != nil {
		t.Fatalf("commit after DDL %q: %v", sql, err)
	}
}

// runSelectRealDataDir runs a read-only SELECT and returns its rows, using
// the same ctx.DataDir/GetSetting wiring as runDDLRealDataDir.
func runSelectRealDataDir(t *testing.T, rt *Runtime, sql string) []executor.Row {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	plan, err := optimizer.Plan(stmts[0], rt.Catalog)
	if err != nil {
		t.Fatalf("plan %q: %v", sql, err)
	}
	op, err := executor.Build(plan)
	if err != nil {
		t.Fatalf("build %q: %v", sql, err)
	}
	tx, err := rt.TxnMgr.Begin(transam.IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = rt.TxnMgr.Commit(tx) }()
	snap, err := rt.TxnMgr.SnapshotFor(tx)
	if err != nil {
		t.Fatalf("SnapshotFor: %v", err)
	}
	ctx := executor.NewContext()
	ctx.Pool = rt.Pool
	ctx.Catalog = rt.Catalog
	ctx.TxnMgr = rt.TxnMgr
	ctx.Tx = tx
	ctx.Snap = snap
	ctx.DataDir = rt.DataDir
	if err := op.Open(ctx); err != nil {
		t.Fatalf("SELECT Open %q: %v", sql, err)
	}
	defer op.Close()
	var rows []executor.Row
	for {
		slot, err := op.Next()
		if err == executor.EOF {
			break
		}
		if err != nil {
			t.Fatalf("SELECT Next %q: %v", sql, err)
		}
		row := append(executor.Row(nil), slot.Row()...)
		slot.Release()
		rows = append(rows, row)
	}
	return rows
}

// TestAlterTableSetTablespacePhysicalRelocationSurvivesRestart proves the
// M0122-0007 physical-relocation fix end-to-end across a real close/reopen:
// a table's rows, written before ALTER TABLE ... SET TABLESPACE moves the
// file into pg_tblspc/<oid>/..., are still readable after a full restart —
// not just the reltablespace catalog value (already covered by
// TestAlterTableSetTablespaceSurvivesRestartViaCatalogHeap).
func TestAlterTableSetTablespacePhysicalRelocationSurvivesRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runCreateTablespaceInPlace(t, rt1, "ts1")
	runDDLRealDataDir(t, rt1, "CREATE TABLE ts_reloc_test (id int4)")
	runDDLRealDataDir(t, rt1, "INSERT INTO ts_reloc_test VALUES (1)")
	runDDLRealDataDir(t, rt1, "INSERT INTO ts_reloc_test VALUES (2)")
	runDDLRealDataDir(t, rt1, "INSERT INTO ts_reloc_test VALUES (3)")
	runDDLRealDataDir(t, rt1, "ALTER TABLE ts_reloc_test SET TABLESPACE ts1")

	wantOID, ok := rt1.Catalog.LookupTablespaceOID("ts1")
	if !ok {
		t.Fatal("ts1 not found in catalog before restart")
	}
	// No SaveCatalog call — must survive purely on the per-statement heap
	// resync + physical relocation the ALTER already performed, mirroring an
	// uncheckpointed crash restart (same discipline as the sibling
	// catalog-only restart test).
	rt1.Close()

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	tbl2, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: "ts_reloc_test"})
	if !ok {
		t.Fatal("ts_reloc_test not found in catalog after restart")
	}
	if tbl2.Tablespace != wantOID {
		t.Fatalf("after restart: Tablespace = %d, want %d", tbl2.Tablespace, wantOID)
	}

	// The reltablespace catalog value alone survives a restart even WITHOUT
	// physical relocation (it was already durable before this fix — see
	// TestAlterTableSetTablespaceSurvivesRestartViaCatalogHeap). The real
	// proof this fix adds is that the FILE itself lives under
	// pg_tblspc/<oid>/... post-restart, not base/<dbOid>/ (which is where a
	// pre-fix binary would still find it, since it never physically moved).
	rel2 := rt2.Catalog.RelFileNode(tbl2)
	if rel2.TblOid != wantOID {
		t.Fatalf("after restart: RelFileNode.TblOid = %d, want %d", rel2.TblOid, wantOID)
	}
	tsPath := filepath.Join(dir, rt2.Pool.Manager().RelPath(rel2))
	if _, err := os.Stat(tsPath); err != nil {
		t.Fatalf("after restart: expected relocated file %s to exist: %v", tsPath, err)
	}
	defaultRel := rel2
	defaultRel.TblOid = 0
	defaultPath := filepath.Join(dir, rt2.Pool.Manager().RelPath(defaultRel))
	if _, err := os.Stat(defaultPath); err == nil {
		t.Fatalf("after restart: expected the OLD default-tablespace path %s to be gone", defaultPath)
	}

	rows := runSelectRealDataDir(t, rt2, "SELECT id FROM ts_reloc_test ORDER BY id")
	if len(rows) != 3 {
		t.Fatalf("after restart: expected 3 rows from the relocated table, got %d (%v)", len(rows), rows)
	}
	for i, row := range rows {
		if got := row[0].Int; got != int64(i+1) {
			t.Errorf("row %d: id = %d, want %d", i, got, i+1)
		}
	}
}
