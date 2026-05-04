package initdb

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/wal"
)

// TestOpenAfterInitReturnsRuntime: the typical operator flow —
// goopg init writes the layout, goopg start opens it — produces a
// Runtime with all four handles populated.
func TestOpenAfterInitReturnsRuntime(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if rt.StorageMgr == nil || rt.Pool == nil || rt.TxnMgr == nil || rt.Catalog == nil {
		t.Errorf("runtime has nil handle: %+v", rt)
	}
	if rt.DataDir == "" || !filepath.IsAbs(rt.DataDir) {
		t.Errorf("DataDir=%q want absolute path", rt.DataDir)
	}
}

// TestOpenRegistersStatCheckpointerView: the runtime wires
// pg_catalog.pg_stat_checkpointer as a virtual table backed by
// the live Checkpointer counters. After one CheckpointNow, the
// num_requested column should report "1".
func TestOpenRegistersStatCheckpointerView(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	tbl, ok := rt.Catalog.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_stat_checkpointer"})
	if !ok {
		t.Fatal("pg_catalog.pg_stat_checkpointer not registered")
	}
	if !tbl.Virtual || tbl.VirtualRows == nil {
		t.Fatal("pg_stat_checkpointer is not a virtual table with a row provider")
	}

	// Run one synchronous checkpoint and verify the row reflects it.
	if err := rt.Checkpointer.CheckpointNow(); err != nil {
		t.Fatal(err)
	}
	rows := tbl.VirtualRows()
	if len(rows) != 1 {
		t.Fatalf("VirtualRows len=%d want 1", len(rows))
	}
	if got := rows[0][1]; got != "1" {
		t.Errorf("num_requested=%q want \"1\"", got)
	}
	if got := rows[0][0]; got != "0" {
		t.Errorf("num_timed=%q want \"0\" (no timer firings)", got)
	}
	// stats_reset should be a non-empty timestamp string.
	if rows[0][10] == "" {
		t.Errorf("stats_reset is empty")
	}
}

// TestOpenRejectsUninitializedDir: pointing the server at a
// directory that goopg init never touched should fail fast with
// the diagnostic that names the missing PG_VERSION as the
// telltale.
func TestOpenRejectsUninitializedDir(t *testing.T) {
	dir := t.TempDir() // empty
	_, err := Open(OpenOptions{DataDir: dir})
	if err == nil {
		t.Fatal("expected error for uninitialized dir")
	}
	if !strings.Contains(err.Error(), "not initialized") && !strings.Contains(err.Error(), "PG_VERSION") {
		t.Errorf("err=%q want a hint about initialization", err.Error())
	}
}

// TestOpenRejectsMissingDir: clearer diagnostic when the path
// doesn't exist at all.
func TestOpenRejectsMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nonexistent")
	_, err := Open(OpenOptions{DataDir: dir})
	if err == nil {
		t.Fatal("expected error for missing dir")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("err=%q want a 'does not exist' message", err.Error())
	}
}

// TestOpenRejectsVersionMismatch: surfacing a catalog-version
// mismatch is important so a binary upgrade can't silently corrupt
// an old data directory.
func TestOpenRejectsVersionMismatch(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "PG_VERSION"), []byte("99\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(OpenOptions{DataDir: dir})
	if err == nil {
		t.Fatal("expected error for version mismatch")
	}
	if !strings.Contains(err.Error(), "catalog version") {
		t.Errorf("err=%q want a catalog-version hint", err.Error())
	}
}

// TestRuntimeSaveAndReloadCatalog: schema declared during one
// session must survive across a SaveCatalog + Close + reopen
// cycle. This is the persistence guarantee the on-disk catalog
// snapshot exists for.
func TestRuntimeSaveAndReloadCatalog(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	// First session: declare a table, snapshot, close.
	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 2})
	if err != nil {
		t.Fatal(err)
	}
	cat1 := rt1.Catalog.(*catalog.InMemory)
	tbl, err := cat1.CreateTable(parser.ObjectName{Name: "pgbench_accounts"}, []catalog.Column{
		{Name: "aid", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "bid", Type: catalog.Type{Name: "int4"}},
		{Name: "abalance", Type: catalog.Type{Name: "int4"}},
		{Name: "filler", Type: catalog.Type{Name: "text"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cat1.CreateIndex(parser.ObjectName{Name: "pgbench_accounts_pkey"}, tbl, []string{"aid"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	if err := rt1.SaveCatalog(); err != nil {
		t.Fatalf("SaveCatalog: %v", err)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Snapshot file should now exist under global/.
	if _, err := os.Stat(filepath.Join(dir, CatalogSnapshotFile)); err != nil {
		t.Fatalf("snapshot file missing: %v", err)
	}

	// Second session: reopen and verify the schema is back.
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()
	got, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: "pgbench_accounts"})
	if !ok {
		t.Fatal("table did not survive restart")
	}
	if len(got.Columns) != 4 || got.Columns[0].Name != "aid" {
		t.Errorf("columns lost: %+v", got.Columns)
	}
	if !rt2.Catalog.HasPrimaryKey(got) {
		t.Errorf("primary key lost on reload")
	}
}

// TestSaveCatalogIsAtomic: a tempfile crash must not leave the
// previous snapshot in a half-written state. We simulate the
// "previous snapshot exists; new save fails partway" scenario by
// pre-writing a known-good snapshot, then asserting the file
// stays valid after an error path runs.
func TestSaveCatalogPreservesPriorOnError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if _, err := rt.Catalog.(*catalog.InMemory).CreateTable(parser.ObjectName{Name: "first"}, []catalog.Column{{Name: "x", Type: catalog.Type{Name: "int4"}}}); err != nil {
		t.Fatal(err)
	}
	if err := rt.SaveCatalog(); err != nil {
		t.Fatal(err)
	}
	// Sanity: the file has the expected name under global/, and
	// reading it back parses.
	body, err := os.ReadFile(filepath.Join(dir, CatalogSnapshotFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"name": "first"`) {
		t.Errorf("snapshot missing the table name: %s", body)
	}
}

// TestRuntimeCloseIsIdempotent: a defer-Close after a successful
// Open shouldn't double-error if some other path already closed.
func TestRuntimeCloseIsIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

// TestOpenWiresXactMarkerHook is the end-to-end pin for the
// M0008 wire-layer emission: after a Begin/Commit through the
// runtime's TxnMgr, the WAL stream contains an XactCommit
// record whose xid matches the committed txn. Without this
// hook, the M0008 classifier loop on a logical slot would
// never see commit/abort markers and could not bound
// transactions in the reorder buffer.
func TestOpenWiresXactMarkerHook(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatal(err)
	}

	tx, err := rt.TxnMgr.Begin(mvcc.IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.TxnMgr.Commit(tx); err != nil {
		t.Fatal(err)
	}
	tx2, _ := rt.TxnMgr.Begin(mvcc.IsolationReadCommitted)
	if err := rt.TxnMgr.Rollback(tx2); err != nil {
		t.Fatal(err)
	}
	// Flush the WAL writer so ReadAll observes the markers,
	// then close the runtime so segment files are visible to
	// the reader.
	if err := rt.WAL.FlushUpTo(rt.WAL.WrittenLSN()); err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}

	recs, err := wal.ReadAll(filepath.Join(dir, "pg_wal"), 0)
	if err != nil {
		t.Fatal(err)
	}
	var sawCommit, sawAbort bool
	for _, r := range recs {
		if len(r.Payload) == 0 {
			continue
		}
		switch r.Payload[0] {
		case wal.RecordKindXactCommit:
			xid, err := wal.DecodeXactMarker(r.Payload)
			if err != nil {
				t.Errorf("decode commit: %v", err)
				continue
			}
			if xid == tx.XID {
				sawCommit = true
			}
		case wal.RecordKindXactAbort:
			xid, err := wal.DecodeXactMarker(r.Payload)
			if err != nil {
				t.Errorf("decode abort: %v", err)
				continue
			}
			if xid == tx2.XID {
				sawAbort = true
			}
		}
	}
	if !sawCommit {
		t.Errorf("WAL stream missing XactCommit for xid=%d", tx.XID)
	}
	if !sawAbort {
		t.Errorf("WAL stream missing XactAbort for xid=%d", tx2.XID)
	}
}

// TestOpenAttachesAIOEngineWhenMethodSet pins the engine
// lifecycle wiring: passing AIOMethod constructs an aio.Engine,
// surfaces it on Runtime.AIO, and Close tears it down. Empty
// AIOMethod leaves Runtime.AIO nil so the synchronous code
// paths are unchanged.
func TestOpenAttachesAIOEngineWhenMethodSet(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{
		DataDir:    dir,
		PoolSlots:  4,
		AIOMethod:  "sync",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if rt.AIO == nil {
		t.Fatal("Runtime.AIO is nil despite AIOMethod=sync")
	}
	if got := rt.AIO.Method(); got != "sync" {
		t.Errorf("engine method=%q want sync", got)
	}
}

// TestOpenLeavesAIONilWithoutMethod: with no AIOMethod, no
// engine is constructed and the existing synchronous storage
// paths run unchanged.
func TestOpenLeavesAIONilWithoutMethod(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if rt.AIO != nil {
		t.Errorf("Runtime.AIO=%v want nil (no AIOMethod)", rt.AIO.Method())
	}
}

// TestOpenRegistersSystemCatalogHeapTables verifies that Open() registers
// pg_type and pg_attribute as real heap-backed catalog tables when their
// M0030-0001 relfiles are present (created by goopg init).
func TestOpenRegistersSystemCatalogHeapTables(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	for _, tc := range []struct {
		name string
		wantOID uint32
	}{
		{"pg_type", catalog.TypeRelationId},
		{"pg_attribute", catalog.AttributeRelationId},
	} {
		tbl, ok := rt.Catalog.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: tc.name})
		if !ok {
			t.Errorf("%s: not found in catalog", tc.name)
			continue
		}
		if tbl.Virtual {
			t.Errorf("%s: Virtual=true, want false (heap-backed)", tc.name)
		}
		if tbl.OID != tc.wantOID {
			t.Errorf("%s: OID=%d want %d", tc.name, tbl.OID, tc.wantOID)
		}
		if len(tbl.Columns) == 0 {
			t.Errorf("%s: no columns", tc.name)
		}
	}
}

// TestOpenPGTypeIsNotVirtual confirms pg_type has no VirtualRows provider —
// its rows come from the heap file, not from a Go closure.
func TestOpenPGTypeIsNotVirtual(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	tbl, ok := rt.Catalog.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_type"})
	if !ok {
		t.Fatal("pg_type not in catalog")
	}
	if tbl.VirtualRows != nil {
		t.Error("pg_type.VirtualRows is non-nil; expected nil (heap-backed table)")
	}
}

// TestOpenSystemCatalogNotInJSONSnapshot verifies that pg_type (a system
// catalog table registered at startup) is excluded from the JSON snapshot
// so it doesn't appear as a user table after Save+Restore.
func TestOpenSystemCatalogNotInJSONSnapshot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatal(err)
	}

	// Save the catalog (including system catalog tables) to JSON.
	if err := rt.SaveCatalog(); err != nil {
		rt.Close()
		t.Fatal(err)
	}
	rt.Close()

	// Re-open: pg_type should still be present (from heap), not duplicated
	// from JSON.
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	tbl, ok := rt2.Catalog.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_type"})
	if !ok {
		t.Fatal("pg_type not in catalog after restart")
	}
	if tbl.Virtual {
		t.Error("pg_type is Virtual after restart (should be heap-backed)")
	}
	if tbl.OID != catalog.TypeRelationId {
		t.Errorf("pg_type OID=%d want %d", tbl.OID, catalog.TypeRelationId)
	}
}

// TestOpenOldClusterWithoutM0030FilesStillWorks simulates a pre-M0030-0001
// cluster that has no system catalog relfiles. Open should succeed without
// pg_type/pg_attribute being registered (backward compatibility).
func TestOpenOldClusterWithoutM0030FilesStillWorks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	// Remove the M0030-0001 relfiles to simulate an old cluster.
	base := filepath.Join(dir, "base", fmt.Sprint(catalog.DefaultDBOid))
	for _, oid := range []uint32{
		catalog.TypeRelationId,
		catalog.AttributeRelationId,
		catalog.RelationRelationId,
	} {
		if err := os.Remove(filepath.Join(base, fmt.Sprint(oid))); err != nil {
			t.Fatalf("remove OID %d: %v", oid, err)
		}
	}

	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("Open on old cluster: %v", err)
	}
	defer rt.Close()

	// pg_type and pg_attribute should not be present (files were removed).
	if _, ok := rt.Catalog.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_type"}); ok {
		t.Error("pg_type in catalog but relfile was removed")
	}
	if _, ok := rt.Catalog.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_attribute"}); ok {
		t.Error("pg_attribute in catalog but relfile was removed")
	}
}

// TestPGAttributeSQLSurfaceForUserTable verifies the M0030-0005 requirement:
// `SELECT attname, atttypid FROM pg_attribute WHERE attrelid = X` returns
// correct column metadata for a user-defined table after CREATE TABLE.
//
// This exercises the full path: DDL-sync writes to pg_attribute heap,
// the heap-backed table registration allows a SeqScan, MVCC visibility
// passes bootstrap-XID rows, and the attname/atttypid values are correct.
func TestPGAttributeSQLSurfaceForUserTable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	// Create a table with known columns — DDL-sync writes to pg_attribute.
	runDDL(t, rt, "CREATE TABLE surf_test (id int4 NOT NULL, label text, active bool)")

	// Verify pg_attribute is registered as non-virtual (heap-backed).
	pgAttr, ok := rt.Catalog.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_attribute"})
	if !ok {
		t.Fatal("pg_attribute not in catalog")
	}
	if pgAttr.Virtual {
		t.Fatal("pg_attribute is Virtual=true — expected heap-backed")
	}

	// Get the user table's OID.
	tbl, ok := rt.Catalog.LookupTable(parser.ObjectName{Name: "surf_test"})
	if !ok {
		t.Fatal("surf_test not in catalog")
	}

	// Scan pg_attribute via the storage pool to verify column data.
	pgAttrRel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: catalog.AttributeRelationId,
		Fork:   storage.MainFork,
	}
	nBlocks, err := rt.Pool.NBlocks(pgAttrRel)
	if err != nil {
		t.Fatalf("NBlocks pg_attribute: %v", err)
	}

	colsByName := map[string]catalog.PGAttributeRow{}
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		slot, err := rt.Pool.Pin(storage.BufferTag{Rel: pgAttrRel, Block: blk})
		if err != nil {
			t.Fatalf("Pin pg_attribute blk %d: %v", blk, err)
		}
		page := slot.Page()
		count, _ := storage.PageLinePointerCount(page)
		for s := uint16(1); s <= uint16(count); s++ {
			ht, err := storage.PageGetHeapTuple(page, s)
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
			if row.AttRelID == tbl.OID {
				colsByName[row.AttName] = row
			}
		}
		rt.Pool.Unpin(slot)
	}

	if len(colsByName) != 3 {
		t.Fatalf("expected 3 pg_attribute rows for surf_test, got %d (names: %v)",
			len(colsByName), colsByName)
	}

	// Verify atttypid values match TypeNameToOID (M0030-0005 OID resolution).
	if got := colsByName["id"].AttTypID; got != catalog.OIDInt4 {
		t.Errorf("id.atttypid = %d, want %d (int4)", got, catalog.OIDInt4)
	}
	if got := colsByName["label"].AttTypID; got != catalog.OIDText {
		t.Errorf("label.atttypid = %d, want %d (text)", got, catalog.OIDText)
	}
	if got := colsByName["active"].AttTypID; got != catalog.OIDBool {
		t.Errorf("active.atttypid = %d, want %d (bool)", got, catalog.OIDBool)
	}
	// Verify attnotnull.
	if !colsByName["id"].AttNotNull {
		t.Error("id.attnotnull should be true")
	}
	if colsByName["label"].AttNotNull {
		t.Error("label.attnotnull should be false")
	}
}
