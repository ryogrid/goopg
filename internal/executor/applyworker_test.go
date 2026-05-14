package executor

import (
	"bytes"
	"encoding/binary"
	"log/slog"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
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
	tuple := wrapAsHeapTuple(t, body)
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
	row, unchanged, err := decodePgoutputTupleAsRow(remoteCols, localCols, tup)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(row) != 3 || len(unchanged) != 3 {
		t.Fatalf("row=%d unchanged=%d want both 3", len(row), len(unchanged))
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

	// Unknown status still errors.
	tupBad := []wal.DecodedColumn{
		{Status: 'x', Bytes: nil},
	}
	if _, _, err := decodePgoutputTupleAsRow(remoteCols[:1], localCols[:1], tupBad); err == nil {
		t.Errorf("unknown status: expected error, got nil")
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
func encodeBodyV0(values []any, types []string) []byte {
	var out []byte
	for i, v := range values {
		if v == nil {
			out = append(out, 1)
			continue
		}
		out = append(out, 0)
		switch types[i] {
		case "int4":
			var tmp [4]byte
			binary.BigEndian.PutUint32(tmp[:], uint32(int32(v.(int))))
			out = append(out, tmp[:]...)
		case "int8":
			var tmp [8]byte
			binary.BigEndian.PutUint64(tmp[:], uint64(v.(int64)))
			out = append(out, tmp[:]...)
		case "text":
			s := v.(string)
			var ln [4]byte
			binary.BigEndian.PutUint32(ln[:], uint32(len(s)))
			out = append(out, ln[:]...)
			out = append(out, []byte(s)...)
		}
	}
	return out
}

func wrapAsHeapTuple(t *testing.T, body []byte) []byte {
	t.Helper()
	tup, err := storage.NewHeapTuple(42, 0, body).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return tup
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
	tuple := wrapAsHeapTuple(t, body)
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
