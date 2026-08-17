package executor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// writeSyntheticPgDatabaseFile writes a minimal one-row pg_database heap page
// to <dataDir>/global/1262, mirroring internal/initdb's
// bootstrapPostgresDatabase closely enough for persistDatFrozenXID to locate
// and rewrite the row.
func writeSyntheticPgDatabaseFile(t *testing.T, dataDir string, oid uint32, name string, datfrozenxid int64) {
	t.Helper()
	cols := catalog.PgDatabaseColumnsPG18()
	row := Row{
		NewIntDatum(int64(oid)),   // oid
		NewStringDatum(name),      // datname
		NewIntDatum(10),           // datdba
		NewIntDatum(6),            // encoding
		NewStringDatum("c"),       // datlocprovider
		NewBoolDatum(false),       // datistemplate
		NewBoolDatum(true),        // datallowconn
		NewBoolDatum(false),       // dathasloginevt
		NewIntDatum(-1),           // datconnlimit
		NewIntDatum(datfrozenxid), // datfrozenxid
		NewIntDatum(1),            // datminmxid
		NewIntDatum(1663),         // dattablespace
		NewStringDatum("C"),       // datcollate
		NewStringDatum("C"),       // datctype
		NullDatum,                 // datlocale
		NullDatum,                 // daticurules
		NullDatum,                 // datcollversion
		NullDatum,                 // datacl
	}
	if len(row) != len(cols) {
		t.Fatalf("row/cols length mismatch: %d vs %d", len(row), len(cols))
	}

	payload, err := EncodeRowPG(cols, row)
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}
	bitmap := NullBitmapPG(row)
	var tup storage.HeapTuple
	if bitmap != nil {
		tup = storage.NewHeapTupleWithNulls(storage.TransactionID(1), storage.InvalidTransactionID, bitmap, payload)
	} else {
		tup = storage.NewHeapTuple(storage.TransactionID(1), storage.InvalidTransactionID, payload)
	}
	tup.Header.SetNatts(len(cols))
	tup.Header.Infomask |= storage.HeapHasVarWidth

	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		t.Fatalf("InitPage: %v", err)
	}
	if _, err := storage.PageAddHeapTuple(page, tup); err != nil {
		t.Fatalf("PageAddHeapTuple: %v", err)
	}

	globalDir := filepath.Join(dataDir, "global")
	if err := os.MkdirAll(globalDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "1262"), page, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// readPgDatabaseDatFrozenXID reads back pg_database's single row through the
// buffer pool (so it observes a MarkDirty'd-but-not-yet-flushed page, exactly
// as a later reader in the same running server would) and returns its
// datfrozenxid column.
func readPgDatabaseDatFrozenXID(t *testing.T, pool *storage.Pool) int64 {
	t.Helper()
	rel := catalog.SharedCatalogRelFileNode(catalog.PgDatabaseRelationOID)
	slot, err := pool.Pin(storage.BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatalf("Pin: %v", err)
	}
	slot.RLock()
	page := make(storage.Page, storage.BlockSize)
	copy(page, slot.Page())
	slot.RUnlock()
	pool.Unpin(slot)

	tup, err := storage.PageGetHeapTuple(page, 1)
	if err != nil {
		t.Fatalf("PageGetHeapTuple: %v", err)
	}
	cols := catalog.PgDatabaseColumnsPG18()
	row := make(Row, len(cols))
	natts := int(tup.Header.Infomask2 & 0x07FF)
	if err := DecodeRowIntoMctxPGTuple(row, cols, tup.Data, tup.Bitmap, natts, nil); err != nil {
		t.Fatalf("DecodeRowIntoMctxPGTuple: %v", err)
	}
	return row[catalog.PgDatabaseDatFrozenXIDOrdinal].Int
}

func newDatFrozenXIDFixture(t *testing.T) (*Context, string, func()) {
	t.Helper()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 64})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	cat := catalog.NewInMemory()
	cat.SetDBOID(5)
	mgrMVCC := transam.NewManager()
	tx, err := mgrMVCC.Begin(transam.IsolationReadCommitted)
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
	cleanup := func() {
		_ = mgrMVCC.Rollback(tx)
		_ = pool.Close()
		_ = mgr.Close()
	}
	return ctx, dir, cleanup
}

// TestPersistDatFrozenXIDAdvances is the DoD test for M0117-0008 Part B: a
// user table's advanced RelFrozenXID must propagate to the on-disk
// pg_database.datfrozenxid tuple as an in-place overwrite (same tuple
// identity, no new MVCC version), mirroring vac_update_datfrozenxid.
func TestPersistDatFrozenXIDAdvances(t *testing.T) {
	ctx, dir, cleanup := newDatFrozenXIDFixture(t)
	defer cleanup()

	writeSyntheticPgDatabaseFile(t, dir, 5, "postgres", 3)

	if _, err := ctx.Catalog.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	if !ok {
		t.Fatal("LookupTable: not found")
	}
	tbl.RelFrozenXID = storage.TransactionID(500)

	if err := persistDatFrozenXID(ctx); err != nil {
		t.Fatalf("persistDatFrozenXID: %v", err)
	}

	if got := readPgDatabaseDatFrozenXID(t, ctx.Pool); got != 500 {
		t.Fatalf("datfrozenxid = %d, want 500", got)
	}

	// Idempotent: calling again with the same horizon must not error and
	// must leave the value unchanged (PG's dirty-guard: no write when the
	// on-disk value already equals/precedes-not the new horizon).
	if err := persistDatFrozenXID(ctx); err != nil {
		t.Fatalf("persistDatFrozenXID (second call): %v", err)
	}
	if got := readPgDatabaseDatFrozenXID(t, ctx.Pool); got != 500 {
		t.Fatalf("datfrozenxid after second call = %d, want unchanged 500", got)
	}
}

// TestPersistDatFrozenXIDEmitsCanonicalWAL verifies the in-place overwrite is
// WAL-logged via the XLOG_HEAP_INPLACE canonical record when LogCanonical is
// wired, so a crash or standby replays the same advance (M0117-0008 Part B;
// cf. feedback_m0106_continuous_pg_compat).
func TestPersistDatFrozenXIDEmitsCanonicalWAL(t *testing.T) {
	t.Skip("canonical WAL emission removed 2026-07-15 (native\u2192PG (rmid,info) dispatch); intentional, not a regression \u2014 see docs/design/wal-native-pg-format/04 + .ralph/deferral_ledger.md")
}
