package executor

import (
	"bytes"
	"encoding/binary"
	"log/slog"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/wal"
)

// TestApplyWorkerInsertsRowFromPgoutputStream pins the M0008 /
// 0008-0004 end-to-end contract: a publisher-side `B → R → I →
// C` pgoutput byte sequence drives the subscriber-side
// ApplyWorker to insert exactly the right row into the local
// table. Confirms encoder/decoder/apply symmetry in one shot.
func TestApplyWorkerInsertsRowFromPgoutputStream(t *testing.T) {
	_, pubCat, pubCleanup := newStorageFixture(t)
	defer pubCleanup()
	pubTbl, _ := pubCat.LookupTable(parser.ObjectName{Name: "items"})

	// Publisher: encode B → R → I → C through the existing
	// PgOutput plugin against a snapshot of the publisher's
	// schema.
	snap := wal.BuildCatalogSnapshot(pubCat.(*catalog.InMemory))
	var buf bytes.Buffer
	po := wal.NewPgOutput(snap, &buf)
	if err := po.Begin(42, 0xCAFE); err != nil {
		t.Fatal(err)
	}
	body := encodeBodyV0([]any{7, "alpha"}, []string{"int4", "text"})
	tuple := wrapAsHeapTuple(t, body, 2)
	if err := po.Change(wal.Change{
		Kind:     wal.ChangeInsert,
		Rel:      pubCat.RelFileNode(pubTbl),
		NewTuple: tuple,
	}); err != nil {
		t.Fatal(err)
	}
	if err := po.Commit(42, 0xCAFE); err != nil {
		t.Fatal(err)
	}

	// Subscriber: independent storage fixture with the same
	// schema. The publisher and subscriber both have a table
	// named "items"; in real wiring the subscriber would also
	// have to provide it — in tests the fixture pre-creates it.
	subCtx, subCat, subCleanup := newStorageFixture(t)
	defer subCleanup()
	subTbl, _ := subCat.LookupTable(parser.ObjectName{Name: "items"})

	w := NewApplyWorker(subCat, subCtx.Pool, subCtx.TxnMgr)
	defer w.SafeRollback()

	stream := buf.Bytes()
	consumed := 0
	commitLSN := uint64(0)
	for consumed < len(stream) {
		end, err := pgoutputMessageLength(stream[consumed:])
		if err != nil {
			t.Fatal(err)
		}
		m, err := wal.DecodeMessage(stream[consumed : consumed+end])
		if err != nil {
			t.Fatal(err)
		}
		lsn, err := w.ApplyMessage(m)
		if err != nil {
			t.Fatalf("ApplyMessage(kind=%q): %v", m.Kind, err)
		}
		if lsn != 0 {
			commitLSN = lsn
		}
		consumed += end
	}

	if commitLSN != 0xCAFE {
		t.Errorf("commit LSN returned by apply=%x want 0xCAFE", commitLSN)
	}

	// Read the row back via SeqScan against the subscriber's
	// catalog. The publisher used the same fixture's pre-existing
	// transaction, so the apply worker's just-committed row
	// is visible to a fresh SeqScan against the subscriber Pool.
	tx, _ := subCtx.TxnMgr.Begin(0)
	defer subCtx.TxnMgr.Rollback(tx)
	snap2, _ := subCtx.TxnMgr.SnapshotFor(tx)
	scanCtx := NewContext()
	scanCtx.Pool = subCtx.Pool
	scanCtx.Catalog = subCat
	scanCtx.TxnMgr = subCtx.TxnMgr
	scanCtx.Tx = tx
	scanCtx.Snap = snap2
	scan := newSeqScanOp(&planner.SeqScan{Table: subTbl})
	if err := scan.Open(scanCtx); err != nil {
		t.Fatal(err)
	}
	defer scan.Close()
	rows, err := drainScan(scan)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("subscriber scan returned %d rows want 1", len(rows))
	}
	if rows[0][0].Int != 7 {
		t.Errorf("col[0]=%v want 7", rows[0][0])
	}
	if rows[0][1].StringValue() != "alpha" {
		t.Errorf("col[1]=%q want alpha", rows[0][1].StringValue())
	}
}

// TestApplyWorkerAppliesInsertUnderDistinctSubscriptionDBOid exercises
// M0122-0007 4d-ii-part-2b item 1's applyworker.go corner: an ApplyWorker
// constructed over a catalog.SearchPathCatalog seeded with a distinct
// subscription dbOid (mirroring server.applyWorkerCatalog's wiring in
// DefaultLaunchApplyWorker) resolves the *subscribing database's own*
// "items" table — not the DefaultDBOid one the raw catalog.InMemory would
// always fall back to — for its un-dbOid-threaded LookupTable call at
// applyworker.go's applyRelation (~line 217). Before applyWorkerCatalog
// existed, NewApplyWorker always received the bare process-wide catalog, so
// a subscription living in a genuinely distinct database (a real CREATE
// DATABASE oid) could never find its own tables: applyRelation's LookupTable
// would either miss entirely or silently alias onto the wrong database's
// same-named table.
func TestApplyWorkerAppliesInsertUnderDistinctSubscriptionDBOid(t *testing.T) {
	_, pubCat, pubCleanup := newStorageFixture(t)
	defer pubCleanup()
	pubTbl, _ := pubCat.LookupTable(parser.ObjectName{Name: "items"})

	snap := wal.BuildCatalogSnapshot(pubCat.(*catalog.InMemory))
	var buf bytes.Buffer
	po := wal.NewPgOutput(snap, &buf)
	if err := po.Begin(42, 0xCAFE); err != nil {
		t.Fatal(err)
	}
	body := encodeBodyV0([]any{7, "alpha"}, []string{"int4", "text"})
	tuple := wrapAsHeapTuple(t, body, 2)
	if err := po.Change(wal.Change{
		Kind:     wal.ChangeInsert,
		Rel:      pubCat.RelFileNode(pubTbl),
		NewTuple: tuple,
	}); err != nil {
		t.Fatal(err)
	}
	if err := po.Commit(42, 0xCAFE); err != nil {
		t.Fatal(err)
	}

	// Subscriber: newStorageFixture pre-creates "items" under
	// DefaultDBOid; additionally register a same-named "items" under a
	// genuinely distinct dbOid — the subscription's own database, which
	// the apply worker must resolve against instead.
	const otherDBOid = 9191
	subCtx, subCat, subCleanup := newStorageFixture(t)
	defer subCleanup()
	if _, err := subCat.CreateTable(parser.ObjectName{Name: "items"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "label", Type: catalog.Type{Name: "text"}},
	}, otherDBOid); err != nil {
		t.Fatalf("CreateTable(otherDBOid): %v", err)
	}
	defaultTbl, _ := subCat.LookupTable(parser.ObjectName{Name: "items"})
	otherTbl, ok := subCat.LookupTable(parser.ObjectName{Name: "items"}, otherDBOid)
	if !ok {
		t.Fatalf("LookupTable(dbOid=%d) did not find the just-created table", otherDBOid)
	}

	w := NewApplyWorker(&catalog.SearchPathCatalog{Catalog: subCat, DBOid: otherDBOid}, subCtx.Pool, subCtx.TxnMgr)
	defer w.SafeRollback()

	stream := buf.Bytes()
	consumed := 0
	for consumed < len(stream) {
		end, err := pgoutputMessageLength(stream[consumed:])
		if err != nil {
			t.Fatal(err)
		}
		m, err := wal.DecodeMessage(stream[consumed : consumed+end])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.ApplyMessage(m); err != nil {
			t.Fatalf("ApplyMessage(kind=%q): %v", m.Kind, err)
		}
		consumed += end
	}

	countRows := func(tbl *catalog.Table) int {
		tx, _ := subCtx.TxnMgr.Begin(0)
		defer subCtx.TxnMgr.Rollback(tx)
		snap2, _ := subCtx.TxnMgr.SnapshotFor(tx)
		scanCtx := NewContext()
		scanCtx.Pool = subCtx.Pool
		scanCtx.Catalog = subCat
		scanCtx.TxnMgr = subCtx.TxnMgr
		scanCtx.Tx = tx
		scanCtx.Snap = snap2
		scan := newSeqScanOp(&planner.SeqScan{Table: tbl})
		if err := scan.Open(scanCtx); err != nil {
			t.Fatal(err)
		}
		defer scan.Close()
		rows, err := drainScan(scan)
		if err != nil {
			t.Fatal(err)
		}
		return len(rows)
	}

	if n := countRows(otherTbl); n != 1 {
		t.Errorf("otherDBOid table row count = %d, want 1 (the apply worker should have resolved this table)", n)
	}
	if n := countRows(defaultTbl); n != 0 {
		t.Errorf("DefaultDBOid table row count = %d, want 0 (the apply worker must not alias onto the wrong database's same-named table)", n)
	}
}

// TestPrimaryKeyOnlyRow pins the partial-key helper that applyUpdate
// falls back on when pgoutput omits OldTuple (REPLICA IDENTITY DEFAULT
// + key columns unchanged): the synthesised key row carries PK column
// values from `full`, with NullDatum in every non-PK position so
// rowMatchesKey ignores non-PK cells.
//
// Design doc: docs/design/0103-0025-m0103-0007-rung-2-pg-to-goopg-full-dml.md.
func TestPrimaryKeyOnlyRow(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "v", Type: catalog.Type{Name: "text"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// No PK yet → helper returns nil so callers fall back to
	// "cannot synthesise a key, skip the UPDATE".
	if got := primaryKeyOnlyRow(cat, tbl, Row{NewIntDatum(7), NewStringDatum("alpha")}); got != nil {
		t.Errorf("no-PK case: got %v want nil", got)
	}

	if _, err := cat.CreateIndex(parser.ObjectName{Name: "t_pkey"}, tbl,
		[]string{"id"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}

	key := primaryKeyOnlyRow(cat, tbl, Row{NewIntDatum(7), NewStringDatum("alpha")})
	if key == nil {
		t.Fatal("PK present: got nil key")
	}
	if got := key[0].Int; got != 7 {
		t.Errorf("key[0].Int = %d want 7", got)
	}
	if !key[1].IsNull() {
		t.Errorf("key[1] = %v want NullDatum (non-PK position must be NULL)", key[1])
	}
}

// TestReplicaIdentityKeyRow pins the rung-12 (M0103-0007) helper that
// supersedes `primaryKeyOnlyRow` in `applyUpdate`'s no-OldTuple branch.
// `replicaIdentityKeyRow` consults the publisher's per-column identity
// flags (`Flags & 0x01 == LOGICALREP_IS_REPLICA_IDENTITY`) rather than
// the subscriber-side catalog, so REPLICA IDENTITY USING INDEX on a
// non-PK unique index resolves to the right row-locator key regardless
// of whether the subscriber declares a PK.
//
// Design doc:
// docs/design/0103-0035-m0103-0007-rung-12-pg-to-goopg-replica-identity-index.md.
func TestReplicaIdentityKeyRow(t *testing.T) {
	localCols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "v", Type: catalog.Type{Name: "text"}},
	}
	newRow := Row{NewIntDatum(7), NewIntDatum(42), NewStringDatum("alpha")}

	t.Run("pk_columns_flagged", func(t *testing.T) {
		remoteCols := []wal.DecodedAttr{
			{Name: "id", TypeOID: 23, Flags: 0x01}, // PK column
			{Name: "a", TypeOID: 23, Flags: 0x00},
			{Name: "v", TypeOID: 25, Flags: 0x00},
		}
		key := replicaIdentityKeyRow(remoteCols, localCols, newRow)
		if key == nil {
			t.Fatal("got nil, want non-nil key (PK flag set)")
		}
		if key[0].Int != 7 {
			t.Errorf("key[0].Int = %d want 7 (id)", key[0].Int)
		}
		if !key[1].IsNull() {
			t.Errorf("key[1] = %v want NullDatum (a is non-identity)", key[1])
		}
		if !key[2].IsNull() {
			t.Errorf("key[2] = %v want NullDatum (v is non-identity)", key[2])
		}
	})

	t.Run("non_pk_unique_index_columns_flagged", func(t *testing.T) {
		// REPLICA IDENTITY USING INDEX on a composite unique (a, v).
		// `id` is NOT flagged — even if the subscriber declares it as
		// PRIMARY KEY, the key row must restrict to (a, v).
		remoteCols := []wal.DecodedAttr{
			{Name: "id", TypeOID: 23, Flags: 0x00},
			{Name: "a", TypeOID: 23, Flags: 0x01},
			{Name: "v", TypeOID: 25, Flags: 0x01},
		}
		key := replicaIdentityKeyRow(remoteCols, localCols, newRow)
		if key == nil {
			t.Fatal("got nil, want non-nil key (non-PK identity flags set)")
		}
		if !key[0].IsNull() {
			t.Errorf("key[0] = %v want NullDatum (id is non-identity in USING INDEX)", key[0])
		}
		if key[1].Int != 42 {
			t.Errorf("key[1].Int = %d want 42 (a is identity)", key[1].Int)
		}
		if key[2].StringValue() != "alpha" {
			t.Errorf("key[2] = %q want \"alpha\" (v is identity)", key[2].StringValue())
		}
	})

	t.Run("no_flags_returns_nil", func(t *testing.T) {
		remoteCols := []wal.DecodedAttr{
			{Name: "id", TypeOID: 23, Flags: 0x00},
			{Name: "a", TypeOID: 23, Flags: 0x00},
			{Name: "v", TypeOID: 25, Flags: 0x00},
		}
		// All-zero Flags is the "REPLICA IDENTITY NOTHING" / corrupt
		// stream case; applyUpdate falls back to primaryKeyOnlyRow.
		if got := replicaIdentityKeyRow(remoteCols, localCols, newRow); got != nil {
			t.Errorf("no-flags case: got %v want nil", got)
		}
	})

	t.Run("row_length_mismatch_returns_nil", func(t *testing.T) {
		remoteCols := []wal.DecodedAttr{
			{Name: "id", TypeOID: 23, Flags: 0x01},
		}
		// newRow shorter than localCols — defensive guard against
		// callers passing partially-built rows.
		short := Row{NewIntDatum(7)}
		if got := replicaIdentityKeyRow(remoteCols, localCols, short); got != nil {
			t.Errorf("row-length mismatch: got %v want nil", got)
		}
	})
}

// TestApplyWorkerDecodeReturnsUnchangedMask pins the rung-5 contract:
// `decodePgoutputTupleAsRow` accepts the upstream `'u'` (unchanged
// TOAST) per-column status code, returns NullDatum for that slot, and
// reports the slot in the parallel `unchanged` mask so a downstream
// UPDATE apply can fill the cell from the matched heap row before
// insert. The earlier rungs only exercised `'t'` and `'n'`.
//
// Design doc: docs/design/0103-0028-m0103-0007-rung-5-pg-to-goopg-toast-unchanged.md.
func TestApplyWorkerDecodeReturnsUnchangedMask(t *testing.T) {
	remoteCols := []wal.DecodedAttr{
		{Name: "id", TypeOID: 23},      // int4
		{Name: "name", TypeOID: 25},    // text
		{Name: "payload", TypeOID: 25}, // text
	}
	localCols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "name", Type: catalog.Type{Name: "text"}},
		{Name: "payload", Type: catalog.Type{Name: "text"}},
	}
	tup := []wal.DecodedColumn{
		{Status: 't', Bytes: []byte("42")},
		{Status: 'n', Bytes: nil},
		{Status: 'u', Bytes: nil},
	}
	row, unchanged, missing, err := decodePgoutputTupleAsRow(remoteCols, localCols, tup)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(row) != 3 || len(unchanged) != 3 || len(missing) != 3 {
		t.Fatalf("row=%d unchanged=%d missing=%d want all 3", len(row), len(unchanged), len(missing))
	}
	if row[0].Int != 42 {
		t.Errorf("row[0].Int=%d want 42", row[0].Int)
	}
	if !row[1].IsNull() {
		t.Errorf("row[1] should be NullDatum (status 'n'), got %v", row[1])
	}
	if !row[2].IsNull() {
		t.Errorf("row[2] should be NullDatum (status 'u'), got %v", row[2])
	}
	if got := []bool{unchanged[0], unchanged[1], unchanged[2]}; got[0] || got[1] || !got[2] {
		t.Errorf("unchanged=%v want [false false true]", got)
	}
	// Every local column was claimed by a remote attribute, so
	// missing[] is all-false. The subscriber-extra case is exercised
	// by TestApplyWorkerDecodeMarksSubscriberExtraAsMissing below.
	if missing[0] || missing[1] || missing[2] {
		t.Errorf("missing=%v want [false false false]", missing)
	}

	// Unknown status still errors.
	tupBad := []wal.DecodedColumn{
		{Status: 'x', Bytes: nil},
	}
	if _, _, _, err := decodePgoutputTupleAsRow(remoteCols[:1], localCols[:1], tupBad); err == nil {
		t.Errorf("unknown status: expected error, got nil")
	}
}

// TestApplyWorkerDecodeRemapsReorderedColumns pins M0103-0007 rung 10:
// when publisher and subscriber declare the same columns in a different
// physical order, decodePgoutputTupleAsRow must look up local positions
// by name (matching PG's apply worker behaviour) rather than blindly
// copying remote ordinal i → local ordinal i. Without the fix the int4
// 'id' value would land in the local text 'v' slot and parsePgoutputText
// would parse "alice" as int4, returning an error.
func TestApplyWorkerDecodeRemapsReorderedColumns(t *testing.T) {
	remoteCols := []wal.DecodedAttr{
		{Name: "id", TypeOID: 23},
		{Name: "v", TypeOID: 25},
	}
	// Local table declares v BEFORE id — different physical order.
	localCols := []catalog.Column{
		{Name: "v", Type: catalog.Type{Name: "text"}},
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
	}
	tup := []wal.DecodedColumn{
		{Status: 't', Bytes: []byte("7")},
		{Status: 't', Bytes: []byte("alice")},
	}
	row, _, _, err := decodePgoutputTupleAsRow(remoteCols, localCols, tup)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(row) != 2 {
		t.Fatalf("row len=%d want 2", len(row))
	}
	// Row is indexed by LOCAL position: row[0] holds v (text "alice"),
	// row[1] holds id (int 7).
	if row[0].IsNull() || string(row[0].Buf) != "alice" {
		t.Errorf("row[0] (local v) = %#v, want text \"alice\"", row[0])
	}
	if row[1].Int != 7 {
		t.Errorf("row[1] (local id) = %#v, want int 7", row[1])
	}
}

// TestApplyWorkerDecodeRejectsUnmatchedRemoteCol guards the symmetric
// error path: if the publisher carries a column the subscriber's table
// doesn't have, the decoder must refuse rather than silently dropping
// the wire byte. PG's apply worker raises the same error condition.
func TestApplyWorkerDecodeRejectsUnmatchedRemoteCol(t *testing.T) {
	remoteCols := []wal.DecodedAttr{
		{Name: "id", TypeOID: 23},
		{Name: "extra_on_publisher", TypeOID: 25},
	}
	localCols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
	}
	tup := []wal.DecodedColumn{
		{Status: 't', Bytes: []byte("1")},
		{Status: 't', Bytes: []byte("x")},
	}
	_, _, _, err := decodePgoutputTupleAsRow(remoteCols, localCols, tup)
	if err == nil {
		t.Fatalf("expected error for unmatched remote col, got nil")
	}
	if !strings.Contains(err.Error(), "extra_on_publisher") {
		t.Errorf("error %q must mention the unmatched col name", err.Error())
	}
}

// TestApplyWorkerDecodeMarksSubscriberExtraAsMissing pins M0103-0007 rung 11:
// when the subscriber declares a column the publisher does not include in its
// Relation message, the decoder must mark that local position as missing[]
// (and leave the row cell NullDatum). applyUpdateByKey uses the mask to
// preserve the subscriber-only value across replicated UPDATEs.
func TestApplyWorkerDecodeMarksSubscriberExtraAsMissing(t *testing.T) {
	remoteCols := []wal.DecodedAttr{
		{Name: "id", TypeOID: 23},
		{Name: "v", TypeOID: 25},
	}
	// Subscriber declares an extra `note` column the publisher knows
	// nothing about.
	localCols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "v", Type: catalog.Type{Name: "text"}},
		{Name: "note", Type: catalog.Type{Name: "text"}},
	}
	tup := []wal.DecodedColumn{
		{Status: 't', Bytes: []byte("1")},
		{Status: 't', Bytes: []byte("hello")},
	}
	row, unchanged, missing, err := decodePgoutputTupleAsRow(remoteCols, localCols, tup)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(row) != 3 || len(unchanged) != 3 || len(missing) != 3 {
		t.Fatalf("row=%d unchanged=%d missing=%d want all 3", len(row), len(unchanged), len(missing))
	}
	if row[0].Int != 1 {
		t.Errorf("row[0].Int=%d want 1", row[0].Int)
	}
	if string(row[1].Buf) != "hello" {
		t.Errorf("row[1] = %q want \"hello\"", string(row[1].Buf))
	}
	if !row[2].IsNull() {
		t.Errorf("row[2] (subscriber-extra note) should be NullDatum, got %v", row[2])
	}
	if unchanged[0] || unchanged[1] || unchanged[2] {
		t.Errorf("unchanged=%v want all-false (no 'u' status cells)", unchanged)
	}
	if missing[0] || missing[1] {
		t.Errorf("missing[0]/[1] should be false (claimed by remote), got missing=%v", missing)
	}
	if !missing[2] {
		t.Errorf("missing[2] (subscriber-extra note) should be true, got missing=%v", missing)
	}
}

// TestApplyUpdateByKeyPreservesSubscriberExtraColumn pins M0103-0007 rung 11
// end-to-end against the executor's heap path: a row with a subscriber-only
// column populated, then a publisher UPDATE arrives carrying only the
// publisher-known columns. applyUpdateByKey must scan for the matched row,
// copy the subscriber-only value into newRow before delete+insert, and the
// post-update heap state must preserve the subscriber-only value.
func TestApplyUpdateByKeyPreservesSubscriberExtraColumn(t *testing.T) {
	// Build a subscriber-side storage fixture and create a table whose
	// shape includes a subscriber-only `note` column the publisher will
	// never describe in its Relation message.
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 16})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	// Cleanup order matters: Pool.Close flushes dirty pages through the
	// Manager, so it must run BEFORE Manager.Close (deferred LIFO).
	t.Cleanup(func() {
		_ = pool.Close()
		_ = mgr.Close()
	})
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true, Ordinal: 0},
		{Name: "v", Type: catalog.Type{Name: "text"}, Ordinal: 1},
		{Name: "note", Type: catalog.Type{Name: "text"}, Ordinal: 2},
	})
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	mgrMVCC := mvcc.NewManager()

	// Seed an initial row with all three columns populated (id=1, v="hello",
	// note="kept"). This is the pre-image the publisher UPDATE will replace.
	seedTx, err := mgrMVCC.Begin(mvcc.IsolationReadCommitted)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	ctx := NewContext()
	ctx.Pool = pool
	ctx.Catalog = cat
	ctx.TxnMgr = mgrMVCC
	ctx.Tx = seedTx
	if err := ctx.MaterializeWriterXID(); err != nil {
		t.Fatalf("materialize seed xid: %v", err)
	}
	rel := cat.RelFileNode(tbl)
	seed := Row{NewIntDatum(1), NewStringDatum("hello"), NewStringDatum("kept")}
	if _, err := writeHeapRowReturning(ctx, rel, tbl.Columns, seed); err != nil {
		t.Fatalf("seed writeHeapRow: %v", err)
	}
	if err := mgrMVCC.Commit(seedTx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	// Apply path: publisher knows (id, v) only. newRow has the new v at
	// position 1 and NullDatum at note's local position; newMissing[2]
	// flags note as a subscriber-extra column that must survive the UPDATE.
	applyTx, err := mgrMVCC.Begin(mvcc.IsolationReadCommitted)
	if err != nil {
		t.Fatalf("begin apply: %v", err)
	}
	ctx2 := NewContext()
	ctx2.Pool = pool
	ctx2.Catalog = cat
	ctx2.TxnMgr = mgrMVCC
	ctx2.Tx = applyTx
	if err := ctx2.MaterializeWriterXID(); err != nil {
		t.Fatalf("materialize apply xid: %v", err)
	}
	snap, _ := mgrMVCC.SnapshotFor(ctx2.Tx)
	ctx2.Snap = snap

	oldKey := Row{NewIntDatum(1), NullDatum, NullDatum}
	newRow := Row{NewIntDatum(1), NewStringDatum("updated"), NullDatum}
	newUnchanged := []bool{false, false, false}
	newMissing := []bool{false, false, true}

	if err := applyUpdateByKey(ctx2, rel, tbl, tbl.Columns, oldKey, newRow, newUnchanged, newMissing); err != nil {
		t.Fatalf("applyUpdateByKey: %v", err)
	}
	if err := mgrMVCC.Commit(applyTx); err != nil {
		t.Fatalf("commit apply: %v", err)
	}

	// Inspect via a fresh read-only transaction.
	readTx, err := mgrMVCC.Begin(mvcc.IsolationReadCommitted)
	if err != nil {
		t.Fatalf("begin read: %v", err)
	}
	defer mgrMVCC.Rollback(readTx)
	rsnap, _ := mgrMVCC.SnapshotFor(readTx)
	scanCtx := NewContext()
	scanCtx.Pool = pool
	scanCtx.Catalog = cat
	scanCtx.TxnMgr = mgrMVCC
	scanCtx.Tx = readTx
	scanCtx.Snap = rsnap
	scan := newSeqScanOp(&planner.SeqScan{Table: tbl})
	if err := scan.Open(scanCtx); err != nil {
		t.Fatalf("seqscan open: %v", err)
	}
	defer scan.Close()
	rows, err := drainScan(scan)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("visible rows=%d want 1: %#v", len(rows), rows)
	}
	r := rows[0]
	if r[0].Int != 1 {
		t.Errorf("row id=%d want 1", r[0].Int)
	}
	if r[1].StringValue() != "updated" {
		t.Errorf("row v=%q want \"updated\"", r[1].StringValue())
	}
	if r[2].StringValue() != "kept" {
		t.Errorf("row note=%q want \"kept\" (subscriber-only column must survive UPDATE)", r[2].StringValue())
	}
}

// TestApplyWorkerInsertRejectsUnchangedToast pins the defensive
// rejection at the INSERT path. pgoutput's encoder never emits 'u'
// for INSERT (there is no pre-image to inherit from); a corrupt
// stream that did so must not silently install a NULL.
//
// Design doc: docs/design/0103-0028-m0103-0007-rung-5-pg-to-goopg-toast-unchanged.md.
func TestApplyWorkerInsertRejectsUnchangedToast(t *testing.T) {
	subCtx, subCat, subCleanup := newStorageFixture(t)
	defer subCleanup()
	subTbl, _ := subCat.LookupTable(parser.ObjectName{Name: "items"})
	rel := subCat.RelFileNode(subTbl)

	w := NewApplyWorker(subCat, subCtx.Pool, subCtx.TxnMgr)
	defer w.SafeRollback()

	// Drive Begin → Relation → Insert directly via synthesized
	// DecodedMessage values; no wire-encoder helper required.
	if _, err := w.ApplyMessage(&wal.DecodedMessage{
		Kind: 'B', XID: 99, CommitLSN: 0xBEEF,
	}); err != nil {
		t.Fatalf("Begin apply: %v", err)
	}
	if _, err := w.ApplyMessage(&wal.DecodedMessage{
		Kind: 'R',
		Relation: &wal.DecodedRelation{
			OID:    rel.RelOid,
			Schema: "public",
			Name:   "items",
			Columns: []wal.DecodedAttr{
				{Name: "id", TypeOID: 23},
				{Name: "label", TypeOID: 25},
			},
		},
	}); err != nil {
		t.Fatalf("Relation apply: %v", err)
	}
	// INSERT with a 'u' cell — encoder would never emit this; we
	// build it by hand to confirm the apply-side defensive check.
	insertMsg := &wal.DecodedMessage{
		Kind:   'I',
		RelOID: rel.RelOid,
		NewTuple: []wal.DecodedColumn{
			{Status: 't', Bytes: []byte("1")},
			{Status: 'u', Bytes: nil},
		},
	}
	if _, err := w.ApplyMessage(insertMsg); err == nil {
		t.Errorf("expected INSERT-with-'u' to be rejected, got nil error")
	}
}

// TestApplyWorkerTruncate pins M0103-0007 rung 9: the apply worker
// handles pgoutput 'T' (TRUNCATE) frames by stamping xmax on every
// visible tuple in each named relation, transactional with the
// surrounding apply xact. After Begin → Relation → Insert(x2) →
// Truncate → Commit, a fresh-snapshot SeqScan must observe zero
// rows.
//
// The 'T' message is synthesised directly because goopg's PgOutput
// encoder does not (yet) emit TRUNCATE; we only consume the wire
// shape on the apply side.
func TestApplyWorkerTruncate(t *testing.T) {
	subCtx, subCat, subCleanup := newStorageFixture(t)
	defer subCleanup()
	subTbl, _ := subCat.LookupTable(parser.ObjectName{Name: "items"})
	rel := subCat.RelFileNode(subTbl)

	w := NewApplyWorker(subCat, subCtx.Pool, subCtx.TxnMgr)
	defer w.SafeRollback()

	if _, err := w.ApplyMessage(&wal.DecodedMessage{
		Kind: 'B', XID: 100, CommitLSN: 0xD00D,
	}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := w.ApplyMessage(&wal.DecodedMessage{
		Kind: 'R',
		Relation: &wal.DecodedRelation{
			// Schema left empty to match the fixture's unqualified
			// table — LookupTable falls back to the default schema
			// when Schema is "".
			OID: rel.RelOid, Name: "items",
			Columns: []wal.DecodedAttr{
				{Name: "id", TypeOID: 23},
				{Name: "label", TypeOID: 25},
			},
		},
	}); err != nil {
		t.Fatalf("Relation: %v", err)
	}
	for _, pair := range [][2]string{{"1", "alpha"}, {"2", "beta"}} {
		if _, err := w.ApplyMessage(&wal.DecodedMessage{
			Kind: 'I', RelOID: rel.RelOid,
			NewTuple: []wal.DecodedColumn{
				{Status: 't', Bytes: []byte(pair[0])},
				{Status: 't', Bytes: []byte(pair[1])},
			},
		}); err != nil {
			t.Fatalf("Insert(%s): %v", pair[0], err)
		}
	}

	// TRUNCATE message naming the same relation. CASCADE bit set
	// to exercise the option-byte plumbing (the apply worker
	// records the option but takes no extra action — CASCADE
	// resolution happens publisher-side).
	if _, err := w.ApplyMessage(&wal.DecodedMessage{
		Kind:           'T',
		TruncateRels:   []uint32{rel.RelOid},
		TruncateOption: 0x01,
	}); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	if _, err := w.ApplyMessage(&wal.DecodedMessage{
		Kind: 'C', CommitLSN: 0xD00D, EndLSN: 0xD00D,
	}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Fresh snapshot — the just-committed xact's xmax stamps
	// must mark both rows dead.
	tx, _ := subCtx.TxnMgr.Begin(0)
	defer subCtx.TxnMgr.Rollback(tx)
	snap2, _ := subCtx.TxnMgr.SnapshotFor(tx)
	scanCtx := NewContext()
	scanCtx.Pool = subCtx.Pool
	scanCtx.Catalog = subCat
	scanCtx.TxnMgr = subCtx.TxnMgr
	scanCtx.Tx = tx
	scanCtx.Snap = snap2
	scan := newSeqScanOp(&planner.SeqScan{Table: subTbl})
	if err := scan.Open(scanCtx); err != nil {
		t.Fatalf("scan open: %v", err)
	}
	defer scan.Close()
	rows, err := drainScan(scan)
	if err != nil {
		t.Fatalf("drainScan: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("post-TRUNCATE scan returned %d rows want 0", len(rows))
	}
}

// TestApplyWorkerTruncateUnknownRelOid pins the apply-time rejection
// path: a 'T' message naming an OID for which no prior 'R' was seen
// must error instead of silently no-oping. This is the same policy
// applyDelete / applyUpdate take and protects against
// publisher/subscriber catalog drift hiding a data-loss outcome.
func TestApplyWorkerTruncateUnknownRelOid(t *testing.T) {
	subCtx, subCat, subCleanup := newStorageFixture(t)
	defer subCleanup()

	w := NewApplyWorker(subCat, subCtx.Pool, subCtx.TxnMgr)
	defer w.SafeRollback()

	if _, err := w.ApplyMessage(&wal.DecodedMessage{
		Kind: 'B', XID: 101, CommitLSN: 0xBEEF,
	}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	// No 'R' for OID 99999 — the TRUNCATE handler must reject.
	_, err := w.ApplyMessage(&wal.DecodedMessage{
		Kind:         'T',
		TruncateRels: []uint32{99999},
	})
	if err == nil {
		t.Errorf("expected error for unknown rel_oid, got nil")
	}
}

// TestApplyWorkerCommitOutsideXactIsNoop pins the tolerant
// behaviour: a `C` with no preceding `B` doesn't crash. v0
// pgoutput emits commit-only sequences only when every change
// was filtered.
func TestApplyWorkerCommitOutsideXactIsNoop(t *testing.T) {
	subCtx, subCat, subCleanup := newStorageFixture(t)
	defer subCleanup()
	w := NewApplyWorker(subCat, subCtx.Pool, subCtx.TxnMgr)
	lsn, err := w.ApplyMessage(&wal.DecodedMessage{Kind: 'C', CommitLSN: 0xDEAD})
	if err != nil {
		t.Errorf("commit-only err=%v want nil", err)
	}
	if lsn != 0xDEAD {
		t.Errorf("lsn=%x want 0xDEAD", lsn)
	}
}

// TestApplyWorkerInsertWithoutRelationFails: an `I` for a
// rel_oid the worker hasn't seen `R` for surfaces an apply
// error rather than silently dropping the row.
func TestApplyWorkerInsertWithoutRelationFails(t *testing.T) {
	subCtx, subCat, subCleanup := newStorageFixture(t)
	defer subCleanup()
	w := NewApplyWorker(subCat, subCtx.Pool, subCtx.TxnMgr)
	if _, err := w.ApplyMessage(&wal.DecodedMessage{Kind: 'B', XID: 42}); err != nil {
		t.Fatal(err)
	}
	defer w.SafeRollback()
	_, err := w.ApplyMessage(&wal.DecodedMessage{
		Kind:   'I',
		RelOID: 99999,
	})
	if err == nil {
		t.Errorf("INSERT for unknown rel_oid accepted")
	}
}

// encodeBodyV0 mirrors the executor codec's null-flag-then-value
// frame so tests can construct on-disk bytes the encoder will
// embed in HeapInsert tuples. Duplicated here from
// internal/wal/pgoutput_test.go because Go-test packages can't
// share helpers across packages.
// PG-physical column-data body (M0111-0002: single on-disk format).
// Non-NULL int4/int8/text, alignment mirroring executor.physicalPGTypeAlign
// (int4/text=4, int8=8); pair with wrapAsHeapTuple to stamp natts.
func encodeBodyV0(values []any, types []string) []byte {
	var out []byte
	alignTo := func(a int) {
		for len(out)%a != 0 {
			out = append(out, 0)
		}
	}
	for i, v := range values {
		switch types[i] {
		case "int4":
			alignTo(4)
			var tmp [4]byte
			binary.LittleEndian.PutUint32(tmp[:], uint32(int32(v.(int))))
			out = append(out, tmp[:]...)
		case "int8":
			alignTo(8)
			var tmp [8]byte
			binary.LittleEndian.PutUint64(tmp[:], uint64(v.(int64)))
			out = append(out, tmp[:]...)
		case "text":
			alignTo(4)
			s := v.(string)
			total := len(s) + 1 // PG short varlena: header byte included in len
			out = append(out, byte((total<<1)|0x01))
			out = append(out, []byte(s)...)
		}
	}
	return out
}

func wrapAsHeapTuple(t *testing.T, body []byte, natts int) []byte {
	t.Helper()
	tup := storage.NewHeapTuple(42, 0, body)
	tup.Header.SetNatts(natts)
	raw, err := tup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// pgoutputMessageLength is the test-side helper to chunk the
// concatenated encoder output into individual messages. Mirrors
// the upstream framing the apply worker will get from CopyData
// frames in production.
func pgoutputMessageLength(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	switch buf[0] {
	case 'B':
		return 21, nil
	case 'C':
		return 26, nil
	case 'R':
		// kind | oid(4) | nspname\0 | relname\0 | replident
		// | natts(2) | per-attr.
		off := 1 + 4
		i := off
		for i < len(buf) && buf[i] != 0 {
			i++
		}
		off = i + 1
		i = off
		for i < len(buf) && buf[i] != 0 {
			i++
		}
		off = i + 1 + 1
		natts := int(buf[off])<<8 | int(buf[off+1])
		off += 2
		for j := 0; j < natts; j++ {
			off++
			i = off
			for i < len(buf) && buf[i] != 0 {
				i++
			}
			off = i + 1 + 4 + 4
		}
		return off, nil
	case 'I', 'D':
		off := 1 + 4 + 1
		natts := int(buf[off])<<8 | int(buf[off+1])
		off += 2
		for j := 0; j < natts; j++ {
			st := buf[off]
			off++
			if st == 't' {
				ln := int(buf[off])<<24 | int(buf[off+1])<<16 | int(buf[off+2])<<8 | int(buf[off+3])
				off += 4 + ln
			}
		}
		return off, nil
	}
	return 0, nil
}

// pgoutputBIRC builds the B → R → I → C byte stream for one
// (relfile, [datums]) pair, mirroring what the publisher emits
// over CopyData. Returned bytes are framed back-to-back; the
// caller chunks them with pgoutputMessageLength. Reused by
// the tablesync gating tests below.
func pgoutputBIRC(t *testing.T, snap *wal.CatalogSnapshot, rel storage.RelFileNode,
	id int, label string, commitLSN uint64) []byte {
	t.Helper()
	var buf bytes.Buffer
	po := wal.NewPgOutput(snap, &buf)
	if err := po.Begin(42, commitLSN); err != nil {
		t.Fatal(err)
	}
	body := encodeBodyV0([]any{id, label}, []string{"int4", "text"})
	tuple := wrapAsHeapTuple(t, body, 2)
	if err := po.Change(wal.Change{
		Kind:     wal.ChangeInsert,
		Rel:      rel,
		NewTuple: tuple,
	}); err != nil {
		t.Fatal(err)
	}
	if err := po.Commit(42, commitLSN); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// driveStream chunks the encoder output and feeds each message
// through the apply worker. Returns the last commit LSN observed.
// Test helper.
func driveStream(t *testing.T, w *ApplyWorker, stream []byte) uint64 {
	t.Helper()
	consumed := 0
	var commitLSN uint64
	for consumed < len(stream) {
		end, err := pgoutputMessageLength(stream[consumed:])
		if err != nil {
			t.Fatal(err)
		}
		m, err := wal.DecodeMessage(stream[consumed : consumed+end])
		if err != nil {
			t.Fatal(err)
		}
		lsn, err := w.ApplyMessage(m)
		if err != nil {
			t.Fatalf("ApplyMessage(kind=%q): %v", m.Kind, err)
		}
		if lsn != 0 {
			commitLSN = lsn
		}
		consumed += end
	}
	return commitLSN
}

// TestApplyWorkerSkipsChangesForRelInTablesync pins the gating
// rule: when a subscription_rel is at state 'd' (still copying),
// the apply worker drops INSERT events for that rel rather than
// double-applying — the tablesync worker is responsible for
// getting that data into the table. Mirrors the upstream
// `should_apply_changes_for_rel` early-out.
func TestApplyWorkerSkipsChangesForRelInTablesync(t *testing.T) {
	_, pubCat, pubCleanup := newStorageFixture(t)
	defer pubCleanup()
	pubTbl, _ := pubCat.LookupTable(parser.ObjectName{Name: "items"})
	snap := wal.BuildCatalogSnapshot(pubCat.(*catalog.InMemory))
	rel := pubCat.RelFileNode(pubTbl)

	subCtx, subCat, subCleanup := newStorageFixture(t)
	defer subCleanup()
	subTbl, _ := subCat.LookupTable(parser.ObjectName{Name: "items"})
	subRel := subCat.RelFileNode(subTbl)

	// Wire the apply worker to a subscription whose 'items'
	// row is at state 'd' (data copy in progress).
	ps := catalog.NewPubSub()
	if _, err := ps.CreateSubscription("sub1", "x", []string{"p"}, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AddSubscriptionRel("sub1", subRel.RelOid); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AdvanceSubscriptionRel("sub1", subRel.RelOid,
		catalog.SubRelStateDataCopy, 0); err != nil {
		t.Fatal(err)
	}

	w := NewApplyWorker(subCat, subCtx.Pool, subCtx.TxnMgr)
	w.SetSubscriptionContext(ps, "sub1")
	defer w.SafeRollback()

	stream := pgoutputBIRC(t, snap, rel, 99, "skipped", 0xFEED)
	if lsn := driveStream(t, w, stream); lsn != 0xFEED {
		t.Errorf("commit LSN=%x want 0xFEED", lsn)
	}

	// SeqScan should see zero rows — the INSERT was filtered.
	tx, _ := subCtx.TxnMgr.Begin(0)
	defer subCtx.TxnMgr.Rollback(tx)
	snap2, _ := subCtx.TxnMgr.SnapshotFor(tx)
	scanCtx := NewContext()
	scanCtx.Pool = subCtx.Pool
	scanCtx.Catalog = subCat
	scanCtx.TxnMgr = subCtx.TxnMgr
	scanCtx.Tx = tx
	scanCtx.Snap = snap2
	scan := newSeqScanOp(&planner.SeqScan{Table: subTbl})
	if err := scan.Open(scanCtx); err != nil {
		t.Fatal(err)
	}
	defer scan.Close()
	rows, err := drainScan(scan)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("subscriber scan returned %d rows; gating should have dropped the INSERT", len(rows))
	}

	// Rel state must still be 'd' — the apply worker's promotion
	// logic only advances 's' rows, never resets the table.
	got, _ := ps.LookupSubscriptionRel("sub1", subRel.RelOid)
	if got.State != catalog.SubRelStateDataCopy {
		t.Errorf("state=%q want d (gating should not advance state)", got.State)
	}
}

// TestApplyWorkerPromotesSyncDoneToReadyOnCommit pins the
// `s` → `r` advance: when a rel is at state 's' with a recorded
// sync-end LSN, the first commit at-or-after that LSN promotes
// it to 'r' so subsequent INSERTs apply directly. This mirrors
// upstream worker.c's process_syncing_tables_for_apply.
func TestApplyWorkerPromotesSyncDoneToReadyOnCommit(t *testing.T) {
	_, pubCat, pubCleanup := newStorageFixture(t)
	defer pubCleanup()
	pubTbl, _ := pubCat.LookupTable(parser.ObjectName{Name: "items"})
	snap := wal.BuildCatalogSnapshot(pubCat.(*catalog.InMemory))
	rel := pubCat.RelFileNode(pubTbl)

	subCtx, subCat, subCleanup := newStorageFixture(t)
	defer subCleanup()
	subTbl, _ := subCat.LookupTable(parser.ObjectName{Name: "items"})
	subRel := subCat.RelFileNode(subTbl)

	ps := catalog.NewPubSub()
	if _, err := ps.CreateSubscription("sub1", "x", []string{"p"}, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AddSubscriptionRel("sub1", subRel.RelOid); err != nil {
		t.Fatal(err)
	}
	// Walk i → d → s with a recorded end-LSN of 0xCAFE; the
	// commit below at 0xFEED is past it so promotion fires.
	if _, err := ps.AdvanceSubscriptionRel("sub1", subRel.RelOid,
		catalog.SubRelStateDataCopy, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AdvanceSubscriptionRel("sub1", subRel.RelOid,
		catalog.SubRelStateSyncDone, 0xCAFE); err != nil {
		t.Fatal(err)
	}

	w := NewApplyWorker(subCat, subCtx.Pool, subCtx.TxnMgr)
	w.SetSubscriptionContext(ps, "sub1")
	defer w.SafeRollback()

	// First commit at 0xFEED — past the sync-end LSN. The INSERT
	// itself is gated (state is still 's' when it arrives), but
	// the commit promotes 's' → 'r'.
	stream := pgoutputBIRC(t, snap, rel, 1, "first", 0xFEED)
	driveStream(t, w, stream)
	got, _ := ps.LookupSubscriptionRel("sub1", subRel.RelOid)
	if got.State != catalog.SubRelStateReady {
		t.Errorf("after first commit: state=%q want r", got.State)
	}

	// A second B/R/I/C now applies directly because the rel is
	// 'r'. Use a fresh worker so no relation cache leaks state
	// between phases (the existing one would still work).
	w2 := NewApplyWorker(subCat, subCtx.Pool, subCtx.TxnMgr)
	w2.SetSubscriptionContext(ps, "sub1")
	defer w2.SafeRollback()
	stream2 := pgoutputBIRC(t, snap, rel, 2, "second", 0xFFFF)
	driveStream(t, w2, stream2)

	tx, _ := subCtx.TxnMgr.Begin(0)
	defer subCtx.TxnMgr.Rollback(tx)
	snap2, _ := subCtx.TxnMgr.SnapshotFor(tx)
	scanCtx := NewContext()
	scanCtx.Pool = subCtx.Pool
	scanCtx.Catalog = subCat
	scanCtx.TxnMgr = subCtx.TxnMgr
	scanCtx.Tx = tx
	scanCtx.Snap = snap2
	scan := newSeqScanOp(&planner.SeqScan{Table: subTbl})
	if err := scan.Open(scanCtx); err != nil {
		t.Fatal(err)
	}
	defer scan.Close()
	rows, err := drainScan(scan)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0][0].Int != 2 {
		t.Errorf("scan rows=%v want one row with id=2", rows)
	}
}

// TestApplyWorkerLogsCommitAndPromotion pins the structured-log
// contract: a commit produces an `event=apply_commit` line with
// the commit LSN; promoting an `s` rel to `r` produces an
// `event=tablesync_state_change` line with `from=s to=r`. Both
// land via the configured slog.Logger so dashboards can alert on
// either keyword.
func TestApplyWorkerLogsCommitAndPromotion(t *testing.T) {
	_, pubCat, pubCleanup := newStorageFixture(t)
	defer pubCleanup()
	pubTbl, _ := pubCat.LookupTable(parser.ObjectName{Name: "items"})
	snap := wal.BuildCatalogSnapshot(pubCat.(*catalog.InMemory))
	rel := pubCat.RelFileNode(pubTbl)

	subCtx, subCat, subCleanup := newStorageFixture(t)
	defer subCleanup()
	subTbl, _ := subCat.LookupTable(parser.ObjectName{Name: "items"})
	subRel := subCat.RelFileNode(subTbl)

	ps := catalog.NewPubSub()
	if _, err := ps.CreateSubscription("sub_log", "x", nil, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AddSubscriptionRel("sub_log", subRel.RelOid); err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{
		catalog.SubRelStateDataCopy,
		catalog.SubRelStateSyncDone,
	} {
		if _, err := ps.AdvanceSubscriptionRel("sub_log", subRel.RelOid, st, 0); err != nil {
			t.Fatal(err)
		}
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	w := NewApplyWorker(subCat, subCtx.Pool, subCtx.TxnMgr)
	w.SetSubscriptionContext(ps, "sub_log")
	w.SetLogger(logger)
	defer w.SafeRollback()

	stream := pgoutputBIRC(t, snap, rel, 1, "row", 0xC0FFEE)
	driveStream(t, w, stream)

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte{'\n'})
	var sawCommit, sawPromotion bool
	for _, line := range lines {
		if bytes.Contains(line, []byte(`"event":"apply_commit"`)) &&
			bytes.Contains(line, []byte(`"lsn":12648430`)) {
			sawCommit = true
		}
		if bytes.Contains(line, []byte(`"event":"tablesync_state_change"`)) &&
			bytes.Contains(line, []byte(`"from":"s"`)) &&
			bytes.Contains(line, []byte(`"to":"r"`)) {
			sawPromotion = true
		}
	}
	if !sawCommit {
		t.Errorf("missing apply_commit log line; saw: %s", buf.String())
	}
	if !sawPromotion {
		t.Errorf("missing tablesync_state_change s→r log line; saw: %s", buf.String())
	}
}

// TestApplyWorkerStatHandleAdvancesOnCommit pins the
// pg_stat_subscription wiring: every ApplyMessage stamps the
// Subscriber's last_msg_*_time via MarkMessage; every commit
// advances received_lsn to the commit LSN. Confirms the
// observability seam reflects the apply loop's actual progress.
func TestApplyWorkerStatHandleAdvancesOnCommit(t *testing.T) {
	_, pubCat, pubCleanup := newStorageFixture(t)
	defer pubCleanup()
	pubTbl, _ := pubCat.LookupTable(parser.ObjectName{Name: "items"})
	snap := wal.BuildCatalogSnapshot(pubCat.(*catalog.InMemory))
	rel := pubCat.RelFileNode(pubTbl)

	subCtx, subCat, subCleanup := newStorageFixture(t)
	defer subCleanup()

	subs := wal.NewSubscribers()
	statHandle := subs.Register(wal.SubscriberState{
		SubID:      99,
		SubName:    "sub_stat",
		WorkerType: wal.SubscriberWorkerLeader,
		PID:        7777,
	})
	defer subs.Unregister(statHandle)

	w := NewApplyWorker(subCat, subCtx.Pool, subCtx.TxnMgr)
	w.SetStatHandle(statHandle)
	defer w.SafeRollback()

	stream := pgoutputBIRC(t, snap, rel, 1, "obs", 0xC0FFEE)
	driveStream(t, w, stream)

	got := subs.Snapshot()[0]
	if got.ReceivedLSN != 0xC0FFEE {
		t.Errorf("received_lsn=%x want 0xC0FFEE (commit LSN must flow into stat handle)", got.ReceivedLSN)
	}
	// MarkMessage stamps last_msg_*_time on every frame; a
	// post-Begin/post-Commit timestamp must be after the
	// pre-Apply zero default.
	if got.LastMsgReceiptTime.IsZero() {
		t.Errorf("LastMsgReceiptTime is zero; ApplyMessage should have stamped it via MarkMessage")
	}
	// The B/C messages carry an EndLSN of 0xC0FFEE; that should
	// have flowed into latest_end_lsn.
	if got.LatestEndLSN != 0xC0FFEE {
		t.Errorf("latest_end_lsn=%x want 0xC0FFEE", got.LatestEndLSN)
	}
}

// TestApplyWorkerCommitWithoutPromotionLeavesUncrossedRelAtS:
// when a commit's LSN is below a subscription_rel's recorded
// end-LSN, the rel stays at 's' (still waiting for the apply
// stream to catch up).
func TestApplyWorkerCommitWithoutPromotionLeavesUncrossedRelAtS(t *testing.T) {
	_, pubCat, pubCleanup := newStorageFixture(t)
	defer pubCleanup()
	pubTbl, _ := pubCat.LookupTable(parser.ObjectName{Name: "items"})
	snap := wal.BuildCatalogSnapshot(pubCat.(*catalog.InMemory))
	rel := pubCat.RelFileNode(pubTbl)

	subCtx, subCat, subCleanup := newStorageFixture(t)
	defer subCleanup()
	subTbl, _ := subCat.LookupTable(parser.ObjectName{Name: "items"})
	subRel := subCat.RelFileNode(subTbl)

	ps := catalog.NewPubSub()
	if _, err := ps.CreateSubscription("sub1", "x", nil, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AddSubscriptionRel("sub1", subRel.RelOid); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AdvanceSubscriptionRel("sub1", subRel.RelOid,
		catalog.SubRelStateDataCopy, 0); err != nil {
		t.Fatal(err)
	}
	// Sync end LSN is 0xFFFFFFFF; commit below is at 0x100. The
	// commit must NOT promote.
	if _, err := ps.AdvanceSubscriptionRel("sub1", subRel.RelOid,
		catalog.SubRelStateSyncDone, 0xFFFFFFFF); err != nil {
		t.Fatal(err)
	}

	w := NewApplyWorker(subCat, subCtx.Pool, subCtx.TxnMgr)
	w.SetSubscriptionContext(ps, "sub1")
	defer w.SafeRollback()

	stream := pgoutputBIRC(t, snap, rel, 1, "x", 0x100)
	driveStream(t, w, stream)
	got, _ := ps.LookupSubscriptionRel("sub1", subRel.RelOid)
	if got.State != catalog.SubRelStateSyncDone {
		t.Errorf("state=%q want s (commit below sync-end LSN must not promote)", got.State)
	}
	_ = subTbl // silence unused if scan removed
}

// TestApplyDefaultsForMissingFillsSlots pins M0103-0007 rung 13's helper.
// Subscriber-extra columns flagged missing[i]=true should be filled by
// evaluating the column's DefaultExpr; columns without a DEFAULT stay at
// their incoming value (typically NullDatum from the pgoutput decoder).
func TestApplyDefaultsForMissingFillsSlots(t *testing.T) {
	cols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "v", Type: catalog.Type{Name: "text"}},
		// Subscriber-extra: has a DEFAULT we'll evaluate.
		{Name: "note", Type: catalog.Type{Name: "text"},
			DefaultExpr: &parser.StringConst{Value: "kept"}},
		// Subscriber-extra: NO DEFAULT — stays NullDatum.
		{Name: "n", Type: catalog.Type{Name: "int4"}},
	}
	row := Row{NewIntDatum(1), NewStringDatum("hello"), NullDatum, NullDatum}
	missing := []bool{false, false, true, true}

	applyDefaultsForMissing(cols, row, missing)

	if row[0].Int != 1 {
		t.Errorf("row[0] mutated: got %v want 1", row[0])
	}
	if string(row[1].Buf) != "hello" {
		t.Errorf("row[1] mutated: got %q want \"hello\"", row[1].Buf)
	}
	if string(row[2].Buf) != "kept" {
		t.Errorf("row[2] not filled: got %v want \"kept\"", row[2])
	}
	if !row[3].IsNull() {
		t.Errorf("row[3] (no DEFAULT) should stay NullDatum, got %v", row[3])
	}
}

// TestApplyDefaultsForMissingIgnoresFalseMask: a slot that is NOT missing
// is NEVER overwritten, even when the column carries a DEFAULT (the
// publisher's value wins).
func TestApplyDefaultsForMissingIgnoresFalseMask(t *testing.T) {
	cols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}},
		{Name: "v", Type: catalog.Type{Name: "text"},
			DefaultExpr: &parser.StringConst{Value: "DEFAULT-VALUE"}},
	}
	row := Row{NewIntDatum(1), NewStringDatum("publisher-value")}
	missing := []bool{false, false}

	applyDefaultsForMissing(cols, row, missing)

	if string(row[1].Buf) != "publisher-value" {
		t.Errorf("row[1] overwritten despite missing[1]=false: got %q", row[1].Buf)
	}
}
