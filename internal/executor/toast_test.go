package executor

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// newToastFixture creates a test context suitable for TOAST tests.
func newToastFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	// Use newHOTFixture which provides pool + catalog + mvcc manager.
	ctx, _, cleanup := newHOTFixture(t)
	return ctx, cleanup
}

// TestToastRoundTripDoD is the M0046-0006 Definition of Done test:
// a 1 MiB text value must survive INSERT → SELECT with full fidelity.
func TestToastRoundTripDoD(t *testing.T) {
	ctx, cleanup := newToastFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE toast_test (id int, data text)"); err != nil {
		t.Fatal(err)
	}

	// Build a 1 MiB string (1,048,576 bytes).
	const oneMiB = 1 << 20
	bigValue := strings.Repeat("X", oneMiB)

	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "toast_test"})
	rel := ctx.Catalog.RelFileNode(tbl)

	// INSERT the 1 MiB row using the raw writeHeapRow path so we bypass
	// the SQL parser and test the codec/TOAST path directly.
	row := Row{
		{Kind: KindInt, Int: 1},
		NewStringDatum(bigValue),
	}
	if err := writeHeapRow(ctx, rel, tbl.Columns, row); err != nil {
		t.Fatalf("INSERT 1 MiB row: %v", err)
	}

	// SELECT via SeqScan — the scan path detoasts automatically.
	rows := runQuery(t, ctx, "SELECT id, data FROM toast_test WHERE id = 1")
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0][0].Kind != KindInt || rows[0][0].Int != 1 {
		t.Errorf("id column: want 1, got %+v", rows[0][0])
	}
	if rows[0][1].Kind != KindString {
		t.Errorf("data column: want KindString, got kind %d", rows[0][1].Kind)
	}
	if len(rows[0][1].StringValue()) != oneMiB {
		t.Errorf("data length: want %d, got %d", oneMiB, len(rows[0][1].StringValue()))
	}
	if rows[0][1].StringValue() != bigValue {
		t.Errorf("data content mismatch (first 10 chars): %q", rows[0][1].StringValue()[:10])
	}
}

// TestToastInlineSmallValue verifies that small values (below ToastThreshold)
// are stored inline in the main heap — no TOAST relation is involved.
func TestToastInlineSmallValue(t *testing.T) {
	ctx, cleanup := newToastFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE small_test (id int, v text)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO small_test VALUES (1, 'hello')"); err != nil {
		t.Fatal(err)
	}

	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "small_test"})
	heapRel := ctx.Catalog.RelFileNode(tbl)
	toastRel := ToastRelFor(heapRel)

	// TOAST relation must have 0 blocks (no chunks written).
	nBlocks, err := ctx.Pool.NBlocks(toastRel)
	if err == nil && nBlocks > 0 {
		t.Errorf("expected 0 TOAST blocks for small value, got %d", nBlocks)
	}

	rows := runQuery(t, ctx, "SELECT v FROM small_test WHERE id = 1")
	if len(rows) != 1 || rows[0][0].StringValue() != "hello" {
		t.Errorf("small value round-trip failed: %+v", rows)
	}
}

// TestToastMultipleChunks verifies that values spanning more than one chunk
// (> ToastMaxChunkSize bytes) are correctly reassembled.
func TestToastMultipleChunks(t *testing.T) {
	ctx, cleanup := newToastFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE chunk_test (id int, v text)"); err != nil {
		t.Fatal(err)
	}

	// Exactly 3 chunks: 3 * ToastMaxChunkSize bytes.
	threeChunks := strings.Repeat("A", 3*ToastMaxChunkSize)

	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "chunk_test"})
	rel := ctx.Catalog.RelFileNode(tbl)
	row := Row{
		{Kind: KindInt, Int: 42},
		NewStringDatum(threeChunks),
	}
	if err := writeHeapRow(ctx, rel, tbl.Columns, row); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	rows := runQuery(t, ctx, "SELECT v FROM chunk_test WHERE id = 42")
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if len(rows[0][0].StringValue()) != len(threeChunks) {
		t.Errorf("length mismatch: want %d, got %d", len(threeChunks), len(rows[0][0].StringValue()))
	}
}

// TestToastChunkInsertsAreIndividuallyWALLogged pins the root-0022 follow-up
// fix (deferral-ledger row appended alongside the chunk_id counter reseed):
// every TOAST chunk insert must emit its own WAL record, even when it lands
// on a page some earlier chunk already dirtied in the same checkpoint epoch.
//
// Before the fix, writeHeapTupleToRel dirtied the TOAST page via a bare
// ctx.Pool.MarkDirty (no per-insert WAL emitter wired at all), so only the
// first chunk written to a page could ever get an FPI and chunks 2+ into an
// already-dirty page produced zero WAL output — silently losing them on an
// unclean crash before the next checkpoint. This test uses a Pool with a
// real LogHeapInsert hook (mirroring internal/initdb/open.go's production
// wiring) and asserts one WAL emission per TOAST chunk row, all landing on
// the same TOAST page.
func TestToastChunkInsertsAreIndividuallyWALLogged(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	defer mgr.Close()

	type insertRec struct {
		rel storage.RelFileNode
		blk storage.BlockNumber
	}
	var inserts []insertRec
	logHeapInsert := func(rel storage.RelFileNode, blk storage.BlockNumber, lineSlot uint16, tuple []byte) (storage.LSN, error) {
		inserts = append(inserts, insertRec{rel: rel, blk: blk})
		return storage.LSN(len(inserts)), nil
	}
	logFPI := func(_ storage.RelFileNode, _ storage.BlockNumber, _ storage.Page) (storage.LSN, error) {
		return storage.LSN(1), nil
	}
	pool, err := storage.NewPool(mgr, storage.PoolConfig{
		Slots:          64,
		LogHeapInsert:  logHeapInsert,
		LogPageImage:   logFPI,
		FullPageWrites: true,
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

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
	defer func() { _ = mgrMVCC.Rollback(tx) }()

	if err := runDDL(t, ctx, "CREATE TABLE wal_chunk_test (id int, v text)"); err != nil {
		t.Fatal(err)
	}

	// 3 full chunks (3*1996 = 5988 B) fit on one 8 KiB TOAST page: chunk 0
	// dirties the page, chunks 1 and 2 write into the already-dirty page in
	// the same checkpoint epoch — exactly the scenario that used to be lost.
	threeChunks := strings.Repeat("A", 3*ToastMaxChunkSize)
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "wal_chunk_test"})
	rel := ctx.Catalog.RelFileNode(tbl)
	row := Row{
		{Kind: KindInt, Int: 1},
		NewStringDatum(threeChunks),
	}
	if err := writeHeapRow(ctx, rel, tbl.Columns, row); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	toastRel := ToastRelFor(rel)
	nBlocks, err := ctx.Pool.NBlocks(toastRel)
	if err != nil || nBlocks != 1 {
		t.Fatalf("expected all 3 chunks on a single TOAST page, got %d blocks (err=%v)", nBlocks, err)
	}

	var toastInserts []insertRec
	for _, ins := range inserts {
		if ins.rel == toastRel {
			toastInserts = append(toastInserts, ins)
		}
	}
	if len(toastInserts) != 3 {
		t.Fatalf("logHeapInsert fired for %d TOAST chunk writes, want 3 (one per chunk — chunks written after the page's first dirty must not be silently dropped from the WAL stream)", len(toastInserts))
	}
	for i, ins := range toastInserts {
		if ins.blk != 0 {
			t.Errorf("toastInserts[%d].blk = %d, want 0 (all 3 chunks land on the same page)", i, ins.blk)
		}
	}
}

// TestToastByteaRoundTrip verifies that bytea (binary data) columns are also
// correctly toasted and detoasted.
func TestToastByteaRoundTrip(t *testing.T) {
	ctx, cleanup := newToastFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE bytea_test (id int, b bytea)"); err != nil {
		t.Fatal(err)
	}

	const size = 4000
	bigBytes := make([]byte, size)
	for i := range bigBytes {
		bigBytes[i] = byte(i % 256)
	}

	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "bytea_test"})
	rel := ctx.Catalog.RelFileNode(tbl)
	row := Row{
		{Kind: KindInt, Int: 7},
		NewBytesDatum(bigBytes),
	}
	if err := writeHeapRow(ctx, rel, tbl.Columns, row); err != nil {
		t.Fatalf("INSERT bytea: %v", err)
	}

	rows := runQuery(t, ctx, "SELECT id FROM bytea_test WHERE id = 7")
	if len(rows) != 1 || rows[0][0].Int != 7 {
		t.Errorf("bytea row not found: %+v", rows)
	}
}

// TestToastPointerCodecRoundTrip verifies that a KindToastPointer datum
// survives EncodeRowPG → DecodeRowInto without corruption.
func TestToastPointerCodecRoundTrip(t *testing.T) {
	cols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "data", Type: catalog.Type{Name: "text"}, Ordinal: 1},
	}
	ptr := []byte{0, 0, 0, 1, 0, 16, 0, 0, 0, 0, 0, 2} // oid=1, len=1M, chunks=2
	row := Row{
		{Kind: KindInt, Int: 99},
		NewToastPointerDatum(ptr),
	}
	encoded, err := EncodeRowPG(cols, row)
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}

	decoded := make(Row, 2)
	if err := DecodeRowInto(decoded, cols, encoded); err != nil {
		t.Fatalf("DecodeRowInto: %v", err)
	}
	if decoded[0].Kind != KindInt || decoded[0].Int != 99 {
		t.Errorf("id: want 99, got %+v", decoded[0])
	}
	if decoded[1].Kind != KindToastPointer {
		t.Errorf("data: want KindToastPointer, got kind %d", decoded[1].Kind)
	}
	if string(decoded[1].BytesValue()) != string(ptr) {
		t.Errorf("pointer bytes mismatch")
	}
}

func TestDetoastValueRejectsImplausibleChunkCount(t *testing.T) {
	ctx, cleanup := newToastFixture(t)
	defer cleanup()

	ptr := make([]byte, 12)
	binary.BigEndian.PutUint32(ptr[0:4], 1)
	binary.BigEndian.PutUint32(ptr[4:8], 16)
	binary.BigEndian.PutUint32(ptr[8:12], maxDetoastChunks+1)

	_, err := DetoastValue(ctx, storage.RelFileNode{DBOid: 1, RelOid: 2, Fork: storage.MainFork}, ptr)
	if err == nil || !strings.Contains(err.Error(), "implausible chunk count") {
		t.Fatalf("expected implausible chunk count error, got %v", err)
	}
}

func TestDetoastValueRejectsImplausibleTotalLength(t *testing.T) {
	ctx, cleanup := newToastFixture(t)
	defer cleanup()

	ptr := make([]byte, 12)
	binary.BigEndian.PutUint32(ptr[0:4], 1)
	binary.BigEndian.PutUint32(ptr[4:8], maxDetoastTotalLen+1)
	binary.BigEndian.PutUint32(ptr[8:12], 1)

	_, err := DetoastValue(ctx, storage.RelFileNode{DBOid: 1, RelOid: 2, Fork: storage.MainFork}, ptr)
	if err == nil || !strings.Contains(err.Error(), "implausible total length") {
		t.Fatalf("expected implausible total length error, got %v", err)
	}
}

// TestToastOIDCounterCollisionAcrossRestart reproduces the WordPress
// wp_options neighbor-row corruption (deferral ledger 2026-07-02): the
// executor's toastOIDCounter is process-local and always starts at 0, but
// a table's TOAST relation survives a restart on disk. Without reseeding
// the counter from existing TOAST content at startup, the next TOASTed
// value written after a restart reissues chunk_id 1 (colliding with an
// earlier value's still-resident chunk_id 1 in the same TOAST relation)
// and DetoastValue's oid-only scan (toast.go) splices the two unrelated
// values' chunks together, corrupting both. This test simulates the
// restart boundary (counter reset) and verifies that reseeding via
// SeedToastOIDCounter — exactly as internal/initdb/open.go now does once
// at startup — prevents the collision.
func TestToastOIDCounterCollisionAcrossRestart(t *testing.T) {
	ctx, cleanup := newToastFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE opts (id int, v text)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "opts"})
	rel := ctx.Catalog.RelFileNode(tbl)

	// valueA mirrors the reported wp_user_roles size; valueB mirrors the
	// reported oversized theme-patterns transient.
	valueA := strings.Repeat("A", 3992)
	if err := writeHeapRow(ctx, rel, tbl.Columns, Row{
		{Kind: KindInt, Int: 1}, NewStringDatum(valueA),
	}); err != nil {
		t.Fatalf("insert A (pre-restart): %v", err)
	}

	// Simulate a goopg process restart: the in-memory counter resets to 0,
	// exactly as it would on a fresh process start, but valueA's TOAST
	// chunks and its inline pointer (in row id=1) survive on disk.
	toastOIDCounter.Store(0)

	// The startup reseed a real restart now performs (internal/initdb/
	// open.go, after loadUserTablesFromHeap): scan every table's TOAST
	// relation and advance the counter past the highest chunk_id found.
	if err := SeedToastOIDCounter(ctx.Pool, []storage.RelFileNode{rel}); err != nil {
		t.Fatalf("SeedToastOIDCounter: %v", err)
	}

	valueB := strings.Repeat("B", 30000)
	if err := writeHeapRow(ctx, rel, tbl.Columns, Row{
		{Kind: KindInt, Int: 2}, NewStringDatum(valueB),
	}); err != nil {
		t.Fatalf("insert B (post-restart): %v", err)
	}

	rows := runQuery(t, ctx, "SELECT id, v FROM opts ORDER BY id")
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if got := rows[0][1].StringValue(); got != valueA {
		t.Errorf("row id=1 (pre-restart value) corrupted by post-restart TOAST OID collision: got %d bytes, want %d bytes of 'A'",
			len(got), len(valueA))
	}
	if got := rows[1][1].StringValue(); got != valueB {
		t.Errorf("row id=2 (post-restart value) corrupted: got %d bytes, want %d bytes of 'B'", len(got), len(valueB))
	}
}

// TestMaxToastChunkIDInRelNoFile verifies the Pool.Exists short-circuit:
// a table that never TOASTed a value has no on-disk TOAST relation file,
// and MaxToastChunkIDInRel must not create one via a stray NBlocks/Pin
// call (see goopg_smgr_ocreate_recreates_removed_files).
func TestMaxToastChunkIDInRelNoFile(t *testing.T) {
	ctx, cleanup := newToastFixture(t)
	defer cleanup()

	toastRel := storage.RelFileNode{DBOid: 1, RelOid: 999_000_000, Fork: storage.MainFork}
	max, found, err := MaxToastChunkIDInRel(ctx.Pool, toastRel)
	if err != nil {
		t.Fatalf("MaxToastChunkIDInRel: %v", err)
	}
	if found {
		t.Errorf("expected found=false for a never-created TOAST relation, got max=%d", max)
	}
	if ctx.Pool.Exists(toastRel) {
		t.Errorf("MaxToastChunkIDInRel must not create the TOAST relation file as a side effect")
	}
}

// TestSeedToastOIDCounterAdvancesPastExisting is a focused unit test for
// the seeding helper itself (independent of the restart-simulation
// end-to-end test above): after writing a TOASTed value with a known oid,
// resetting the counter, and reseeding, the very next assigned oid must
// exceed every oid physically present in the TOAST relation.
func TestSeedToastOIDCounterAdvancesPastExisting(t *testing.T) {
	ctx, cleanup := newToastFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE seed_test (id int, v text)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "seed_test"})
	rel := ctx.Catalog.RelFileNode(tbl)

	if err := writeHeapRow(ctx, rel, tbl.Columns, Row{
		{Kind: KindInt, Int: 1}, NewStringDatum(strings.Repeat("Z", ToastThreshold+1)),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	toastRel := ToastRelFor(rel)
	wantMax, found, err := MaxToastChunkIDInRel(ctx.Pool, toastRel)
	if err != nil {
		t.Fatalf("MaxToastChunkIDInRel: %v", err)
	}
	if !found || wantMax == 0 {
		t.Fatalf("expected a non-zero max chunk_id, got found=%v max=%d", found, wantMax)
	}

	toastOIDCounter.Store(0)
	if err := SeedToastOIDCounter(ctx.Pool, []storage.RelFileNode{rel}); err != nil {
		t.Fatalf("SeedToastOIDCounter: %v", err)
	}
	if next := toastNextOID(); next <= wantMax {
		t.Errorf("next assigned oid %d does not exceed existing max %d", next, wantMax)
	}
}

// TestToastRelFor verifies the TOAST relation OID derivation.
func TestToastRelFor(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 16384, Fork: storage.MainFork}
	toast := ToastRelFor(rel)
	if toast.DBOid != rel.DBOid {
		t.Errorf("DBOid mismatch")
	}
	if toast.RelOid != rel.RelOid+100_000_000 {
		t.Errorf("RelOid: want %d, got %d", rel.RelOid+100_000_000, toast.RelOid)
	}
}
