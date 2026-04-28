package wal

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestEncodeDecodeHeapDeleteRoundTrip pins the on-the-wire shape
// of the heap-delete record.
func TestEncodeDecodeHeapDeleteRoundTrip(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 50, RelOid: 51, Fork: storage.MainFork}
	enc := EncodeHeapDelete(rel, 13, 7, storage.TransactionID(99))
	if enc[0] != RecordKindHeapDelete {
		t.Errorf("kind byte = %d, want %d", enc[0], RecordKindHeapDelete)
	}
	gotRel, gotBlk, gotSlot, gotXmax, err := DecodeHeapDelete(enc)
	if err != nil {
		t.Fatal(err)
	}
	if gotRel != rel || gotBlk != 13 || gotSlot != 7 || gotXmax != 99 {
		t.Errorf("decoded rel/blk/slot/xmax=%v %d %d %d", gotRel, gotBlk, gotSlot, gotXmax)
	}
}

// TestReplayHeapDeleteIdempotent walks one HeapDelete record
// through replay against a page that already has a tuple at
// slot 1, verifies xmax is stamped and a second replay is a
// no-op via pd_lsn.
func TestReplayHeapDeleteIdempotent(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 905, Fork: storage.MainFork}

	// Seed block 0 with one tuple at slot 1.
	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		t.Fatal(err)
	}
	tup := storage.NewHeapTuple(7, storage.InvalidTransactionID, []byte("alive"))
	if _, err := storage.PageAddHeapTuple(page, tup); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Extend(rel, page); err != nil {
		t.Fatal(err)
	}

	rec := EncodeHeapDelete(rel, 0, 1, storage.TransactionID(42))
	stats, err := ReplayRecords(mgr, []Record{
		{StartLSN: 1, EndLSN: 100, Payload: rec},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Applied != 1 {
		t.Fatalf("first replay Applied=%d want 1", stats.Applied)
	}

	// Second replay must be a no-op.
	if _, err := ReplayRecords(mgr, []Record{
		{StartLSN: 1, EndLSN: 100, Payload: rec},
	}); err != nil {
		t.Fatalf("second replay returned err=%v (expected silent skip)", err)
	}

	// Verify xmax actually stamped.
	got := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, got); err != nil {
		t.Fatal(err)
	}
	raw, err := storage.PageGetItemRaw(got, 1)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := storage.ParseHeapTuple(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Header.Xmax != 42 {
		t.Errorf("xmax = %d want 42", parsed.Header.Xmax)
	}
}

// TestEncodeDecodeBtreeInsertRoundTrip pins the on-the-wire
// shape of the new redo record so future format edits can't
// silently rearrange fields.
func TestEncodeDecodeBtreeInsertRoundTrip(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 33, RelOid: 44, Fork: storage.MainFork}
	itemBytes := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
	enc := EncodeBtreeInsert(rel, 9, itemBytes)
	if enc[0] != RecordKindBtreeInsert {
		t.Errorf("kind byte = %d, want %d", enc[0], RecordKindBtreeInsert)
	}
	gotRel, gotBlk, gotItem, err := DecodeBtreeInsert(enc)
	if err != nil {
		t.Fatal(err)
	}
	if gotRel != rel || gotBlk != 9 {
		t.Errorf("decoded rel/blk = %v %d", gotRel, gotBlk)
	}
	if string(gotItem) != string(itemBytes) {
		t.Errorf("decoded item = %x want %x", gotItem, itemBytes)
	}
}

// TestReplayHeapInsertIdempotent pins the M0002 redo-records
// landing: a HeapInsert record applies on top of an existing
// page, and a second replay of the same record is a no-op via
// the pd_lsn idempotency guard. Without that guard, replay
// would either error (duplicate slot) or duplicate the tuple.
func TestReplayHeapInsertIdempotent(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 904, Fork: storage.MainFork}

	// Block 0 already exists with InitPage'd content.
	zero := mustPageWithByte(t, 0)
	if err := storage.InitPage(zero); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Extend(rel, zero); err != nil {
		t.Fatal(err)
	}

	tup := storage.NewHeapTuple(7, storage.InvalidTransactionID, []byte("hello"))
	tupBytes, err := tup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	rec := EncodeHeapInsert(rel, 0, 1, tupBytes)
	stats, err := ReplayRecords(mgr, []Record{
		{StartLSN: 1, EndLSN: 100, Payload: rec},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Applied != 1 {
		t.Fatalf("first replay Applied=%d want 1", stats.Applied)
	}

	// Second replay of the same record must be a no-op via the
	// pd_lsn idempotency guard.
	_, err = ReplayRecords(mgr, []Record{
		{StartLSN: 1, EndLSN: 100, Payload: rec},
	})
	if err != nil {
		t.Fatalf("second replay returned err=%v (expected silent skip)", err)
	}

	// Verify the page actually has the tuple at slot 1.
	got := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, got); err != nil {
		t.Fatal(err)
	}
	raw, err := storage.PageGetItemRaw(got, 1)
	if err != nil {
		t.Fatalf("PageGetItemRaw(1): %v", err)
	}
	parsed, err := storage.ParseHeapTuple(raw)
	if err != nil {
		t.Fatalf("ParseHeapTuple: %v", err)
	}
	if string(parsed.Data) != "hello" {
		t.Errorf("tuple body = %q want %q", parsed.Data, "hello")
	}
}

// TestEncodeDecodeHeapInsertRoundTrip pins the on-the-wire shape.
func TestEncodeDecodeHeapInsertRoundTrip(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 11, RelOid: 12, Fork: storage.MainFork}
	enc := EncodeHeapInsert(rel, 7, 3, []byte("payload"))
	if enc[0] != RecordKindHeapInsert {
		t.Errorf("kind byte = %d, want %d", enc[0], RecordKindHeapInsert)
	}
	gotRel, gotBlk, gotSlot, gotTuple, err := DecodeHeapInsert(enc)
	if err != nil {
		t.Fatal(err)
	}
	if gotRel != rel || gotBlk != 7 || gotSlot != 3 {
		t.Errorf("decoded rel/blk/slot = %v %d %d", gotRel, gotBlk, gotSlot)
	}
	if string(gotTuple) != "payload" {
		t.Errorf("decoded tuple = %q want \"payload\"", string(gotTuple))
	}
}

// TestReplayBtreeSplitAtomic pins Landing 3a of M0002: a single
// BtreeSplit record carries both pages, and replay applies the
// right page even when it does not yet exist on disk (the split's
// "Extend" half). Without this record the bare smgr.Extend image
// would be the only thing on disk for the right block, leaving a
// torn state where left's right-link points at an empty page.
func TestReplayBtreeSplitAtomic(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 902, Fork: storage.MainFork}

	// Establish block 0 (the metapage stand-in) so block 1 is the
	// "left" we'll write a post-split image to. Then the split
	// record extends block 2 as the right sibling.
	zero := mustPageWithByte(t, 0)
	if _, err := mgr.Extend(rel, zero); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Extend(rel, zero); err != nil { // block 1 reserved
		t.Fatal(err)
	}

	leftAfter := mustPageWithByte(t, 0x44)
	rightAfter := mustPageWithByte(t, 0x55)

	rec, err := EncodeBtreeSplit(rel, 1, 2, leftAfter, rightAfter)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := ReplayRecords(mgr, []Record{
		{StartLSN: 1, EndLSN: 100, Payload: rec},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Applied != 1 {
		t.Fatalf("Applied=%d want 1", stats.Applied)
	}

	got1 := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 1, got1); err != nil {
		t.Fatal(err)
	}
	if got1[100] != 0x44 {
		t.Fatalf("left byte = %#x, want 0x44", got1[100])
	}

	got2 := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 2, got2); err != nil {
		t.Fatal(err)
	}
	if got2[100] != 0x55 {
		t.Fatalf("right byte = %#x, want 0x55 (right page must have been Extend'd by replay)", got2[100])
	}
}

// TestEncodeDecodeBtreeSplitRoundTrip pins the on-the-wire shape
// of the new record so a future-format change can't silently
// rearrange fields and break replay against pre-existing WAL.
func TestEncodeDecodeBtreeSplitRoundTrip(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 7, RelOid: 8, Fork: storage.MainFork}
	left := mustPageWithByte(t, 0x10)
	right := mustPageWithByte(t, 0x20)
	enc, err := EncodeBtreeSplit(rel, 41, 42, left, right)
	if err != nil {
		t.Fatal(err)
	}
	if enc[0] != RecordKindBtreeSplit {
		t.Errorf("kind byte = %d, want %d", enc[0], RecordKindBtreeSplit)
	}
	gotRel, gotL, gotR, leftP, rightP, err := DecodeBtreeSplit(enc)
	if err != nil {
		t.Fatal(err)
	}
	if gotRel != rel || gotL != 41 || gotR != 42 {
		t.Errorf("decoded rel/blocks=%v %d %d", gotRel, gotL, gotR)
	}
	if leftP[100] != 0x10 || rightP[100] != 0x20 {
		t.Errorf("decoded page bytes mismatched")
	}
}

func TestReplayRecordsAppliesPageImages(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 900, Fork: storage.MainFork}

	p0 := mustPageWithByte(t, 0x22)
	p1 := mustPageWithByte(t, 0x33)

	rec0, err := EncodePageImage(rel, 0, p0)
	if err != nil {
		t.Fatal(err)
	}
	rec1, err := EncodePageImage(rel, 1, p1)
	if err != nil {
		t.Fatal(err)
	}

	stats, err := ReplayRecords(mgr, []Record{
		{StartLSN: 1, EndLSN: 100, Payload: rec0},
		{StartLSN: 101, EndLSN: 200, Payload: rec1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Records != 2 || stats.Applied != 2 || stats.CheckpointLSN != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}

	got0 := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, got0); err != nil {
		t.Fatal(err)
	}
	if got0[100] != 0x22 {
		t.Fatalf("block0 byte = %#x, want 0x22", got0[100])
	}

	got1 := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 1, got1); err != nil {
		t.Fatal(err)
	}
	if got1[100] != 0x33 {
		t.Fatalf("block1 byte = %#x, want 0x33", got1[100])
	}
}

func TestReplayRecordsStopsAtLastCheckpoint(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 901, Fork: storage.MainFork}

	before := mustPageWithByte(t, 0x11)
	after := mustPageWithByte(t, 0x44)

	beforePayload, err := EncodePageImage(rel, 0, before)
	if err != nil {
		t.Fatal(err)
	}
	afterPayload, err := EncodePageImage(rel, 0, after)
	if err != nil {
		t.Fatal(err)
	}

	stats, err := ReplayRecords(mgr, []Record{
		{StartLSN: 1, EndLSN: 100, Payload: beforePayload},
		{StartLSN: 101, EndLSN: 110, Payload: EncodeCheckpoint()},
		{StartLSN: 111, EndLSN: 210, Payload: afterPayload},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Applied != 1 {
		t.Fatalf("applied = %d, want 1", stats.Applied)
	}
	if stats.CheckpointLSN != 110 {
		t.Fatalf("checkpoint lsn = %d, want 110", stats.CheckpointLSN)
	}

	got := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, got); err != nil {
		t.Fatal(err)
	}
	if got[100] != 0x11 {
		t.Fatalf("block0 byte = %#x, want 0x11", got[100])
	}
}

func TestReplayFromDirEndToEnd(t *testing.T) {
	dataDir := t.TempDir()
	walDir := filepath.Join(dataDir, "pg_wal")

	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 256})
	if err != nil {
		t.Fatal(err)
	}

	rel := storage.RelFileNode{DBOid: 1, RelOid: 902, Fork: storage.MainFork}
	pBefore := mustPageWithByte(t, 0x55)
	pAfter := mustPageWithByte(t, 0x77)

	beforePayload, err := EncodePageImage(rel, 0, pBefore)
	if err != nil {
		t.Fatal(err)
	}
	afterPayload, err := EncodePageImage(rel, 0, pAfter)
	if err != nil {
		t.Fatal(err)
	}

	_, end1, err := w.Append(beforePayload)
	if err != nil {
		t.Fatal(err)
	}
	_, end2, err := w.Append(EncodeCheckpoint())
	if err != nil {
		t.Fatal(err)
	}
	_, end3, err := w.Append(afterPayload)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.FlushUpTo(end3); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if end2 <= end1 {
		t.Fatalf("checkpoint end lsn ordering invalid: end1=%d end2=%d", end1, end2)
	}

	stats, err := ReplayFromDir(dataDir, 256)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Records != 3 || stats.Applied != 1 || stats.CheckpointLSN != end2 {
		t.Fatalf("unexpected stats: %+v (want records=3 applied=1 checkpoint=%d)", stats, end2)
	}

	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()
	got := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, got); err != nil {
		t.Fatal(err)
	}
	if got[100] != 0x55 {
		t.Fatalf("replayed byte = %#x, want 0x55", got[100])
	}
}

func mustPageWithByte(t *testing.T, v byte) storage.Page {
	t.Helper()
	p := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(p); err != nil {
		t.Fatal(err)
	}
	p[100] = v
	return p
}
