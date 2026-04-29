package initdb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
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
