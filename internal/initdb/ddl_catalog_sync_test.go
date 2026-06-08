package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// runDDL executes a single DDL statement through the full planner+executor
// pipeline using a real Runtime.
func runDDL(t *testing.T, rt *Runtime, sql string) {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	if len(stmts) != 1 {
		t.Fatalf("expected 1 statement, got %d", len(stmts))
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

	if err := op.Open(ctx); err != nil {
		_ = rt.TxnMgr.Commit(tx)
		t.Fatalf("DDL Open %q: %v", sql, err)
	}
	for {
		_, nextErr := op.Next()
		if nextErr != nil {
			op.Close()
			_ = rt.TxnMgr.Commit(tx)
			t.Fatalf("DDL Next %q: %v", sql, nextErr)
		}
		break
	}
	op.Close()
	if err := rt.TxnMgr.Commit(tx); err != nil {
		t.Fatalf("commit after DDL %q: %v", sql, err)
	}
}

// scanPGClassByOID reads all pg_class rows and returns the row matching oid.
func scanPGClassByOID(t *testing.T, rt *Runtime, oid uint32) *catalog.PGClassRow {
	t.Helper()
	rel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: catalog.RelationRelationId,
		Fork:   storage.MainFork,
	}
	nBlocks, err := rt.Pool.NBlocks(rel)
	if err != nil {
		t.Fatalf("NBlocks pg_class: %v", err)
	}
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		slot, err := rt.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			t.Fatalf("Pin pg_class block %d: %v", blk, err)
		}
		page := slot.Page()
		count, _ := storage.PageLinePointerCount(page)
		for s := uint16(1); s <= uint16(count); s++ {
			ht, err := storage.PageGetHeapTuple(page, s)
			if err != nil {
				continue
			}
			if ht.Header.Xmax != storage.InvalidTransactionID {
				continue
			}
			row, err := catalog.DecodePGClassRow(ht.Data)
			if err != nil {
				var err2 error
				row, err2 = catalog.DecodePGClassPhysicalRow(ht.Data)
				if err2 != nil {
					continue
				}
			}
			if row.OID == oid {
				rt.Pool.Unpin(slot)
				return &row
			}
		}
		rt.Pool.Unpin(slot)
	}
	return nil
}

// scanPGAttributeByRelID reads all pg_attribute rows for relOID.
func scanPGAttributeByRelID(t *testing.T, rt *Runtime, relOID uint32) []catalog.PGAttributeRow {
	t.Helper()
	rel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: catalog.AttributeRelationId,
		Fork:   storage.MainFork,
	}
	nBlocks, err := rt.Pool.NBlocks(rel)
	if err != nil {
		t.Fatalf("NBlocks pg_attribute: %v", err)
	}
	var out []catalog.PGAttributeRow
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		slot, err := rt.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			t.Fatalf("Pin pg_attribute block %d: %v", blk, err)
		}
		page := slot.Page()
		count, _ := storage.PageLinePointerCount(page)
		for s := uint16(1); s <= uint16(count); s++ {
			ht, err := storage.PageGetHeapTuple(page, s)
			if err != nil {
				continue
			}
			if ht.Header.Xmax != storage.InvalidTransactionID {
				continue
			}
			row, err := catalog.DecodePGAttributeRow(ht.Data)
			if err != nil {
				var err2 error
				row, err2 = catalog.DecodePGAttributePhysicalRow(ht.Data)
				if err2 != nil {
					continue
				}
			}
			if row.AttRelID == relOID {
				out = append(out, row)
			}
		}
		rt.Pool.Unpin(slot)
	}
	return out
}

// TestCreateTableSyncsToPGClass verifies that CREATE TABLE writes a pg_class
// row with the correct OID, name, relkind='r', and column count.
func TestCreateTableSyncsToPGClass(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	runDDL(t, rt, "CREATE TABLE sync_test (id int4 NOT NULL, name text)")

	tbl, ok := rt.Catalog.LookupTable(parser.ObjectName{Name: "sync_test"})
	if !ok {
		t.Fatal("sync_test not in catalog")
	}

	row := scanPGClassByOID(t, rt, tbl.OID)
	if row == nil {
		t.Fatalf("pg_class: no row for OID %d (sync_test)", tbl.OID)
	}
	if row.RelName != "sync_test" {
		t.Errorf("relname=%q want sync_test", row.RelName)
	}
	if row.RelKind != "r" {
		t.Errorf("relkind=%q want 'r'", row.RelKind)
	}
	if row.RelNAtts != 2 {
		t.Errorf("relnatts=%d want 2", row.RelNAtts)
	}
	if row.RelPersistence != "p" {
		t.Errorf("relpersistence=%q want 'p'", row.RelPersistence)
	}
}

// TestCreateTableSyncsToPGAttribute verifies that CREATE TABLE writes
// pg_attribute rows for every column with correct names, type OIDs, and ordinals.
func TestCreateTableSyncsToPGAttribute(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	runDDL(t, rt, "CREATE TABLE attr_test (id int4 NOT NULL, label text, flag bool)")

	tbl, ok := rt.Catalog.LookupTable(parser.ObjectName{Name: "attr_test"})
	if !ok {
		t.Fatal("attr_test not in catalog")
	}

	rows := scanPGAttributeByRelID(t, rt, tbl.OID)
	if len(rows) != 3 {
		t.Fatalf("pg_attribute: got %d rows for attr_test, want 3", len(rows))
	}

	byName := map[string]catalog.PGAttributeRow{}
	for _, r := range rows {
		byName[r.AttName] = r
	}

	id := byName["id"]
	if id.AttTypID != catalog.OIDInt4 {
		t.Errorf("id.atttypid=%d want %d (int4)", id.AttTypID, catalog.OIDInt4)
	}
	if !id.AttNotNull {
		t.Error("id.attnotnull should be true")
	}
	if id.AttNum != 1 {
		t.Errorf("id.attnum=%d want 1", id.AttNum)
	}

	lbl := byName["label"]
	if lbl.AttTypID != catalog.OIDText {
		t.Errorf("label.atttypid=%d want %d (text)", lbl.AttTypID, catalog.OIDText)
	}
	if lbl.AttNum != 2 {
		t.Errorf("label.attnum=%d want 2", lbl.AttNum)
	}

	fl := byName["flag"]
	if fl.AttTypID != catalog.OIDBool {
		t.Errorf("flag.atttypid=%d want %d (bool)", fl.AttTypID, catalog.OIDBool)
	}
}

// TestCreateIndexSyncsToPGClass verifies that CREATE INDEX writes a pg_class
// row for the index with relkind='i'.
func TestCreateIndexSyncsToPGClass(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	runDDL(t, rt, "CREATE TABLE idx_host (id int4 NOT NULL, val int4)")
	runDDL(t, rt, "CREATE INDEX idx_host_id ON idx_host (id)")

	idx, ok := rt.Catalog.LookupIndex(parser.ObjectName{Name: "idx_host_id"})
	if !ok {
		t.Fatal("idx_host_id not in catalog")
	}

	row := scanPGClassByOID(t, rt, idx.OID)
	if row == nil {
		t.Fatalf("pg_class: no row for index OID %d (idx_host_id)", idx.OID)
	}
	if row.RelName != "idx_host_id" {
		t.Errorf("relname=%q want idx_host_id", row.RelName)
	}
	if row.RelKind != "i" {
		t.Errorf("relkind=%q want 'i'", row.RelKind)
	}
}
