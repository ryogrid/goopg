package executor

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// newTablespaceRelocateFixture is like newHOTFixture, but — unlike
// tablespaceFixture (operators_tablespace_test.go), which deliberately points
// ctx.DataDir at a SEPARATE temp dir from the storage.Manager's own (those
// tests only exercise pg_tblspc/<oid> directory bookkeeping, never real
// relation files) — wires ctx.DataDir to the SAME directory backing
// ctx.Pool's Manager. Physical relocation (M0122-0007) needs both to agree:
// relocateRelationPhysicalFile builds absolute paths via
// filepath.Join(ctx.DataDir, mgr.RelPath(rel)), the same convention
// execCreateTablespace already uses for pg_tblspc/<oid> itself.
func newTablespaceRelocateFixture(t *testing.T) (*Context, string, func()) {
	t.Helper()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 64})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	cat := catalog.NewInMemory()
	mgrMVCC := mvcc.NewManager()
	tx, err := mgrMVCC.Begin(mvcc.IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := mgrMVCC.SnapshotFor(tx)
	if err != nil {
		t.Fatal(err)
	}
	ctx := NewContext()
	ctx.Pool = pool
	ctx.Catalog = cat
	ctx.TxnMgr = mgrMVCC
	ctx.Tx = tx
	ctx.Snap = snap
	ctx.DataDir = dir
	ctx.GetSetting = func(name string) (string, bool) {
		if name == "allow_in_place_tablespaces" {
			return "on", true
		}
		return "", false
	}
	cleanup := func() {
		_ = mgrMVCC.Rollback(tx)
		_ = pool.Close()
		_ = mgr.Close()
	}
	return ctx, dir, cleanup
}

// TestAlterTableSetTablespacePhysicallyRelocatesFile pins the M0122-0007
// physical-relocation fix: ALTER TABLE ... SET TABLESPACE previously only
// updated pg_class.reltablespace, leaving the table's real data file sitting
// under base/<dbOid>/ regardless of the declared tablespace. It must now
// actually move the file under pg_tblspc/<oid>/..., preserve every row, and
// clean up the old file.
func TestAlterTableSetTablespacePhysicallyRelocatesFile(t *testing.T) {
	ctx, dataDir, cleanup := newTablespaceRelocateFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLESPACE ts1 LOCATION ''"); err != nil {
		t.Fatalf("CREATE TABLESPACE: %v", err)
	}
	tsOID, ok := ctx.Catalog.LookupTablespaceOID("ts1")
	if !ok {
		t.Fatalf("LookupTablespaceOID(ts1): not found")
	}
	if err := runDDL(t, ctx, "CREATE TABLE t1 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	for i := 1; i <= 5; i++ {
		if err := runDDL(t, ctx, "INSERT INTO t1 VALUES ("+strconv.Itoa(i)+")"); err != nil {
			t.Fatalf("INSERT %d: %v", i, err)
		}
	}
	if err := ctx.Pool.FlushAll(); err != nil {
		t.Fatalf("FlushAll before move: %v", err)
	}

	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Schema: "public", Name: "t1"})
	if !ok {
		t.Fatalf("t1 not found")
	}
	oldRel := ctx.Catalog.RelFileNode(tbl)
	oldPath := filepath.Join(dataDir, ctx.Pool.Manager().RelPath(oldRel))
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("expected pre-move file %s to exist: %v", oldPath, err)
	}

	if err := runDDL(t, ctx, "ALTER TABLE t1 SET TABLESPACE ts1"); err != nil {
		t.Fatalf("ALTER TABLE SET TABLESPACE: %v", err)
	}

	tbl, ok = ctx.Catalog.LookupTable(parser.ObjectName{Schema: "public", Name: "t1"})
	if !ok {
		t.Fatalf("t1 not found after ALTER")
	}
	if tbl.Tablespace != tsOID {
		t.Fatalf("t1.Tablespace = %d, want %d", tbl.Tablespace, tsOID)
	}
	newRel := ctx.Catalog.RelFileNode(tbl)
	if newRel.TblOid != tsOID {
		t.Fatalf("RelFileNode(t1).TblOid = %d, want %d", newRel.TblOid, tsOID)
	}
	newPath := filepath.Join(dataDir, ctx.Pool.Manager().RelPath(newRel))
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("expected post-move file %s to exist: %v", newPath, err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("expected pre-move file %s to be gone, stat err = %v", oldPath, err)
	}

	// Every row survived the move — read straight off the smgr (below the
	// buffer pool, which was invalidated as part of the move) to prove the
	// bytes on disk at the NEW location are correct, not just cached.
	ctx.Pool.InvalidateRel(newRel)
	rows := runQuery(t, ctx, "SELECT a FROM t1 ORDER BY a")
	if len(rows) != 5 {
		t.Fatalf("expected 5 rows after relocation, got %d", len(rows))
	}
	for i, row := range rows {
		if got := row[0].Int; got != int64(i+1) {
			t.Errorf("row %d: a = %d, want %d", i, got, i+1)
		}
	}
}

// TestAlterIndexSetTablespacePhysicallyRelocatesFile mirrors the table test
// for the index form (ALTER INDEX ... SET TABLESPACE), and additionally
// verifies the index is still fully usable (an index-covered query returns
// the right row) after its btree file moves.
func TestAlterIndexSetTablespacePhysicallyRelocatesFile(t *testing.T) {
	ctx, dataDir, cleanup := newTablespaceRelocateFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLESPACE ts1 LOCATION ''"); err != nil {
		t.Fatalf("CREATE TABLESPACE: %v", err)
	}
	tsOID, _ := ctx.Catalog.LookupTablespaceOID("ts1")
	if err := runDDL(t, ctx, "CREATE TABLE t1 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	for i := 1; i <= 5; i++ {
		if err := runDDL(t, ctx, "INSERT INTO t1 VALUES ("+strconv.Itoa(i)+")"); err != nil {
			t.Fatalf("INSERT %d: %v", i, err)
		}
	}
	if err := runDDL(t, ctx, "CREATE INDEX t1_a_idx ON t1 (a)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	if err := ctx.Pool.FlushAll(); err != nil {
		t.Fatalf("FlushAll before move: %v", err)
	}

	idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Schema: "public", Name: "t1_a_idx"})
	if !ok {
		t.Fatalf("t1_a_idx not found")
	}
	oldRel := ctx.Catalog.IndexRelFileNode(idx)
	oldPath := filepath.Join(dataDir, ctx.Pool.Manager().RelPath(oldRel))
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("expected pre-move index file %s to exist: %v", oldPath, err)
	}

	if err := runDDL(t, ctx, "ALTER INDEX t1_a_idx SET TABLESPACE ts1"); err != nil {
		t.Fatalf("ALTER INDEX SET TABLESPACE: %v", err)
	}

	idx, ok = ctx.Catalog.LookupIndex(parser.ObjectName{Schema: "public", Name: "t1_a_idx"})
	if !ok {
		t.Fatalf("t1_a_idx not found after ALTER")
	}
	if idx.Tablespace != tsOID {
		t.Fatalf("t1_a_idx.Tablespace = %d, want %d", idx.Tablespace, tsOID)
	}
	newRel := ctx.Catalog.IndexRelFileNode(idx)
	newPath := filepath.Join(dataDir, ctx.Pool.Manager().RelPath(newRel))
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("expected post-move index file %s to exist: %v", newPath, err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("expected pre-move index file %s to be gone, stat err = %v", oldPath, err)
	}

	ctx.Pool.InvalidateRel(newRel)
	rows := runQuery(t, ctx, "SELECT a FROM t1 WHERE a = 3")
	if len(rows) != 1 || rows[0][0].Int != 3 {
		t.Fatalf("index scan after relocation: got %+v, want one row a=3", rows)
	}
}
