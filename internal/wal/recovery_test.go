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
	enc := EncodeHeapDelete(rel, 13, 7, storage.TransactionID(99), nil)
	if enc[0] != RecordKindHeapDelete {
		t.Errorf("kind byte = %d, want %d", enc[0], RecordKindHeapDelete)
	}
	gotRel, gotBlk, gotSlot, gotXmax, _, err := DecodeHeapDelete(enc)
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

	rec := EncodeHeapDelete(rel, 0, 1, storage.TransactionID(42), nil)
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

// TestEncodeDecodeHeapVacuumRoundTrip pins the on-the-wire shape
// of the heap-vacuum record so a future format edit can't silently
// rearrange fields.
func TestEncodeDecodeHeapVacuumRoundTrip(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 60, RelOid: 61, Fork: storage.MainFork}
	slots := []uint16{1, 3, 5, 7}
	enc := EncodeHeapVacuum(rel, 21, slots)
	if enc[0] != RecordKindHeapVacuum {
		t.Errorf("kind byte = %d, want %d", enc[0], RecordKindHeapVacuum)
	}
	gotRel, gotBlk, gotSlots, err := DecodeHeapVacuum(enc)
	if err != nil {
		t.Fatal(err)
	}
	if gotRel != rel || gotBlk != 21 {
		t.Errorf("decoded rel/blk = %v %d", gotRel, gotBlk)
	}
	if len(gotSlots) != len(slots) {
		t.Fatalf("decoded slots len = %d, want %d", len(gotSlots), len(slots))
	}
	for i, s := range slots {
		if gotSlots[i] != s {
			t.Errorf("decoded slots[%d] = %d, want %d", i, gotSlots[i], s)
		}
	}
}

// TestReplayHeapVacuumIdempotent walks one HeapVacuum record
// through replay against a page seeded with three live tuples.
// One slot is reclaimed; the prune is observed after replay and
// a second replay is a no-op via pd_lsn.
func TestReplayHeapVacuumIdempotent(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 906, Fork: storage.MainFork}

	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"alpha", "beta", "gamma"} {
		tup := storage.NewHeapTuple(7, storage.InvalidTransactionID, []byte(body))
		if _, err := storage.PageAddHeapTuple(page, tup); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mgr.Extend(rel, page); err != nil {
		t.Fatal(err)
	}

	rec := EncodeHeapVacuum(rel, 0, []uint16{2})
	stats, err := ReplayRecords(mgr, []Record{
		{StartLSN: 1, EndLSN: 100, Payload: rec},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Applied != 1 {
		t.Fatalf("first replay Applied=%d want 1", stats.Applied)
	}

	// Second replay must be a no-op via pd_lsn.
	if _, err := ReplayRecords(mgr, []Record{
		{StartLSN: 1, EndLSN: 100, Payload: rec},
	}); err != nil {
		t.Fatalf("second replay returned err=%v (expected silent skip)", err)
	}

	// Slot 2 should be LP_UNUSED; 1 and 3 still readable.
	got := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, got); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.PageGetHeapTuple(got, 2); err == nil {
		t.Errorf("slot 2 still readable after vacuum replay; want LP_UNUSED")
	}
	t1, err := storage.PageGetHeapTuple(got, 1)
	if err != nil {
		t.Fatalf("slot 1: %v", err)
	}
	if string(t1.Data) != "alpha" {
		t.Errorf("slot 1 body = %q want alpha", t1.Data)
	}
	t3, err := storage.PageGetHeapTuple(got, 3)
	if err != nil {
		t.Fatalf("slot 3: %v", err)
	}
	if string(t3.Data) != "gamma" {
		t.Errorf("slot 3 body = %q want gamma", t3.Data)
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

// TestReplayRecordsStartsFromLastCheckpoint pins M0045-0002 behavior:
// crash recovery replays records FROM the last checkpoint (inclusive),
// NOT up to it. Records before the checkpoint are already on disk and
// are skipped. Post-checkpoint records are the ones that may need
// recovery after a crash.
func TestReplayRecordsStartsFromLastCheckpoint(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 901, Fork: storage.MainFork}

	// before_image writes 0x11; after_image writes 0x44.
	// Checkpoint sits between them.
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
		{StartLSN: 1, EndLSN: 100, Payload: beforePayload},  // pre-checkpoint, skipped
		{StartLSN: 101, EndLSN: 110, Payload: EncodeCheckpoint()},
		{StartLSN: 111, EndLSN: 210, Payload: afterPayload}, // post-checkpoint, applied
	})
	if err != nil {
		t.Fatal(err)
	}
	// The after_image (post-checkpoint) is applied; the before_image
	// (pre-checkpoint) is skipped. Checkpoint itself is a no-op marker.
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
	// Page reflects after_image (0x44), not before_image (0x11), because
	// crash recovery starts from the checkpoint.
	if got[100] != 0x44 {
		t.Fatalf("block0 byte = %#x, want 0x44 (post-checkpoint image)", got[100])
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
	// M0045-0002: replay starts FROM the checkpoint, so the post-checkpoint
	// page image (0x77) is applied, not the pre-checkpoint one (0x55).
	if got[100] != 0x77 {
		t.Fatalf("replayed byte = %#x, want 0x77 (post-checkpoint image)", got[100])
	}
}

func TestReplayFromDirEndToEndPageHeaders(t *testing.T) {
	dataDir := t.TempDir()
	walDir := filepath.Join(dataDir, "pg_wal")

	w, err := NewWriter(Config{
		WALDir:      walDir,
		SegmentSize: 16 * 1024,
		PageHeaders: true,
		SystemID:    0x1111222233334444,
		TimelineID:  1,
	})
	if err != nil {
		t.Fatal(err)
	}

	rel := storage.RelFileNode{DBOid: 1, RelOid: 902, Fork: storage.MainFork}
	pBefore := mustPageWithByte(t, 0x66)
	pAfter := mustPageWithByte(t, 0x99)

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

	stats, err := ReplayFromDir(dataDir, 16*1024)
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
	// M0045-0002: post-checkpoint image (0x99) replayed, not pre-checkpoint (0x66).
	if got[100] != 0x99 {
		t.Fatalf("replayed byte = %#x, want 0x99 (post-checkpoint image)", got[100])
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

// TestEncodeDecodeHeapLockRoundTrip pins the on-the-wire shape
// of the row-lock record (M0021 tuple-level locking step 3).
func TestEncodeDecodeHeapLockRoundTrip(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 70, RelOid: 71, Fork: storage.MainFork}
	enc := EncodeHeapLock(rel, 17, 9, storage.TransactionID(123), storage.HeapXmaxExclLock)
	if enc[0] != RecordKindHeapLock {
		t.Errorf("kind byte = %d, want %d", enc[0], RecordKindHeapLock)
	}
	if len(enc) != heapLockSize {
		t.Errorf("len = %d, want %d", len(enc), heapLockSize)
	}
	gotRel, gotBlk, gotSlot, gotXmax, gotStrength, err := DecodeHeapLock(enc)
	if err != nil {
		t.Fatal(err)
	}
	if gotRel != rel || gotBlk != 17 || gotSlot != 9 || gotXmax != 123 || gotStrength != storage.HeapXmaxExclLock {
		t.Errorf("decoded rel/blk/slot/xmax/strength = %+v %d %d %d %#x",
			gotRel, gotBlk, gotSlot, gotXmax, gotStrength)
	}
}

// TestDecodeHeapLockRejectsWrongKind — guards against a future
// caller that hands a non-heap-lock payload to DecodeHeapLock.
// Mirrors the shape-guard tests on the other heap-record decoders.
func TestDecodeHeapLockRejectsWrongKind(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 70, RelOid: 71, Fork: storage.MainFork}
	enc := EncodeHeapDelete(rel, 17, 9, storage.TransactionID(123), nil)
	if _, _, _, _, _, err := DecodeHeapLock(enc); err == nil {
		t.Error("expected error decoding heap-delete bytes as heap-lock")
	}
}

// TestReplayHeapLockIdempotent walks one HeapLock record through
// replay against a page seeded with one live tuple; verifies xmax
// + the HeapXmaxLockOnly + lock-strength infomask bits land, and
// a second replay is a no-op via pd_lsn. The lock-only stamp
// must NOT make the tuple invisible — that's the whole point of
// HEAP_XMAX_LOCK_ONLY versus HEAP_XMAX (delete) — but visibility
// is a mvcc package concern; this test only pins the on-page
// bytes and the redo-path idempotency.
func TestReplayHeapLockIdempotent(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 906, Fork: storage.MainFork}

	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		t.Fatal(err)
	}
	tup := storage.NewHeapTuple(7, storage.InvalidTransactionID, []byte("locked-target"))
	if _, err := storage.PageAddHeapTuple(page, tup); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Extend(rel, page); err != nil {
		t.Fatal(err)
	}

	rec := EncodeHeapLock(rel, 0, 1, storage.TransactionID(42), storage.HeapXmaxExclLock)
	stats, err := ReplayRecords(mgr, []Record{
		{StartLSN: 1, EndLSN: 100, Payload: rec},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Applied != 1 {
		t.Fatalf("first replay Applied=%d want 1", stats.Applied)
	}

	// Second replay must be a no-op via pd_lsn.
	if _, err := ReplayRecords(mgr, []Record{
		{StartLSN: 1, EndLSN: 100, Payload: rec},
	}); err != nil {
		t.Fatalf("second replay returned err=%v (expected silent skip)", err)
	}

	// Verify the on-page bytes: xmax stamped, LockOnly + ExclLock
	// bits set in infomask, HeapXmaxInvalid cleared.
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
	if parsed.Header.Infomask&storage.HeapXmaxLockOnly == 0 {
		t.Errorf("Infomask = %#x missing HeapXmaxLockOnly", parsed.Header.Infomask)
	}
	if parsed.Header.Infomask&storage.HeapXmaxExclLock == 0 {
		t.Errorf("Infomask = %#x missing HeapXmaxExclLock", parsed.Header.Infomask)
	}
}

// TestReplayHeapLockMissingBlock — replay against a relation
// whose target block doesn't yet exist surfaces an error rather
// than silently extending — locking a non-existent tuple is
// always a producer bug.
func TestReplayHeapLockMissingBlock(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 907, Fork: storage.MainFork}
	rec := EncodeHeapLock(rel, 5, 1, storage.TransactionID(42), storage.HeapXmaxExclLock)
	_, err := ReplayRecords(mgr, []Record{
		{StartLSN: 1, EndLSN: 100, Payload: rec},
	})
	if err == nil {
		t.Error("expected error replaying lock against non-existent block")
	}
}

// TestApplyRecordRoutesHeapLock — the per-record kernel
// `ApplyRecord` must dispatch `RecordKindHeapLock` to
// replayHeapLock; missing the case would silently drop the
// lock at recovery time. Verifies via the public `applied`
// flag the dispatcher returns.
func TestApplyRecordRoutesHeapLock(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 908, Fork: storage.MainFork}
	page := make(storage.Page, storage.BlockSize)
	_ = storage.InitPage(page)
	tup := storage.NewHeapTuple(1, storage.InvalidTransactionID, []byte("x"))
	_, _ = storage.PageAddHeapTuple(page, tup)
	_, _ = mgr.Extend(rel, page)

	rec := Record{
		StartLSN: 1, EndLSN: 50,
		Payload: EncodeHeapLock(rel, 0, 1, storage.TransactionID(7), storage.HeapXmaxExclLock),
	}
	applied, err := ApplyRecord(mgr, rec)
	if err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Errorf("ApplyRecord applied=false, want true")
	}
}

// TestReplayRecordsPostCheckpointAfterRetention verifies M0045-0002:
// after WAL retention removes pre-checkpoint segments, crash recovery
// replays post-checkpoint records to recover dirty pages. This is the
// end-to-end scenario from run-007.
func TestReplayRecordsPostCheckpointAfterRetention(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 999, Fork: storage.MainFork}

	postPayload, err := EncodePageImage(rel, 0, mustPageWithByte(t, 0xAB))
	if err != nil {
		t.Fatal(err)
	}
	postPayload2, err := EncodePageImage(rel, 1, mustPageWithByte(t, 0xCD))
	if err != nil {
		t.Fatal(err)
	}

	// Simulate post-retention WAL: only checkpoint + post-checkpoint records.
	stats, err := ReplayRecords(mgr, []Record{
		{StartLSN: 500, EndLSN: 510, Payload: EncodeCheckpoint()},
		{StartLSN: 511, EndLSN: 600, Payload: postPayload},
		{StartLSN: 601, EndLSN: 700, Payload: postPayload2},
	})
	if err != nil {
		t.Fatalf("ReplayRecords: %v", err)
	}
	if stats.Applied != 2 {
		t.Fatalf("applied = %d, want 2", stats.Applied)
	}
	if stats.CheckpointLSN != 510 {
		t.Fatalf("checkpointLSN = %d, want 510", stats.CheckpointLSN)
	}

	got0 := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, got0); err != nil {
		t.Fatalf("ReadBlock(0): %v", err)
	}
	if got0[100] != 0xAB {
		t.Fatalf("block0[100] = %#x, want 0xAB", got0[100])
	}
	got1 := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 1, got1); err != nil {
		t.Fatalf("ReadBlock(1): %v", err)
	}
	if got1[100] != 0xCD {
		t.Fatalf("block1[100] = %#x, want 0xCD", got1[100])
	}
}

// _ avoids an unused-import lint when this file ends up alone.
var _ = filepath.Base

// TestEncodeDecodeSmgrCreateRoundTrip verifies that EncodeSmgrCreate +
// DecodeSmgrCreate preserves the RelFileNode fields exactly.
func TestEncodeDecodeSmgrCreateRoundTrip(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 16384, Fork: storage.MainFork}
	enc := EncodeSmgrCreate(rel)
	if enc[0] != RecordKindSmgrCreate {
		t.Errorf("kind byte = %d, want %d", enc[0], RecordKindSmgrCreate)
	}
	if len(enc) != smgrRecordSize {
		t.Errorf("encoded len = %d, want %d", len(enc), smgrRecordSize)
	}
	got, err := DecodeSmgrCreate(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got != rel {
		t.Errorf("decoded rel = %+v, want %+v", got, rel)
	}
}

// TestEncodeDecodeSmgrTruncateRoundTrip verifies SmgrTruncate round-trip.
func TestEncodeDecodeSmgrTruncateRoundTrip(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 99999, Fork: storage.MainFork}
	enc := EncodeSmgrTruncate(rel)
	if enc[0] != RecordKindSmgrTruncate {
		t.Errorf("kind byte = %d, want %d", enc[0], RecordKindSmgrTruncate)
	}
	got, err := DecodeSmgrTruncate(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got != rel {
		t.Errorf("decoded rel = %+v, want %+v", got, rel)
	}
}

// TestReplaySmgrCreateCreatesRelfile verifies that replaying a SmgrCreate
// record creates the relfile with one block when it doesn't already exist.
func TestReplaySmgrCreateCreatesRelfile(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 55555, Fork: storage.MainFork}

	// File must not exist before replay.
	n, _ := mgr.NBlocks(rel)
	if n != 0 {
		t.Fatalf("expected 0 blocks before replay, got %d", n)
	}

	payload := EncodeSmgrCreate(rel)
	applied, err := ApplyRecord(mgr, Record{Payload: payload})
	if err != nil {
		t.Fatalf("ApplyRecord SmgrCreate: %v", err)
	}
	if !applied {
		t.Error("ApplyRecord returned applied=false for SmgrCreate")
	}

	n, err = mgr.NBlocks(rel)
	if err != nil {
		t.Fatalf("NBlocks after replay: %v", err)
	}
	if n != 1 {
		t.Errorf("NBlocks after SmgrCreate replay = %d, want 1", n)
	}
}

// TestReplaySmgrCreateIdempotent verifies that replaying SmgrCreate on an
// already-present relfile is a no-op (no error, NBlocks unchanged).
func TestReplaySmgrCreateIdempotent(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 66666, Fork: storage.MainFork}

	// Pre-create the relfile with 3 blocks.
	page := make(storage.Page, storage.BlockSize)
	_ = storage.InitPage(page)
	for i := 0; i < 3; i++ {
		if _, err := mgr.Extend(rel, page); err != nil {
			t.Fatal(err)
		}
	}

	payload := EncodeSmgrCreate(rel)
	if _, err := ApplyRecord(mgr, Record{Payload: payload}); err != nil {
		t.Fatalf("ApplyRecord SmgrCreate idempotent: %v", err)
	}
	n, _ := mgr.NBlocks(rel)
	if n != 3 {
		t.Errorf("NBlocks after idempotent replay = %d, want 3", n)
	}
}

// TestReplaySmgrTruncateZerosRelfile verifies that SmgrTruncate replay
// reduces the relfile to 0 blocks.
func TestReplaySmgrTruncateZerosRelfile(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 77777, Fork: storage.MainFork}

	// Pre-create with 2 blocks.
	page := make(storage.Page, storage.BlockSize)
	_ = storage.InitPage(page)
	for i := 0; i < 2; i++ {
		if _, err := mgr.Extend(rel, page); err != nil {
			t.Fatal(err)
		}
	}

	payload := EncodeSmgrTruncate(rel)
	applied, err := ApplyRecord(mgr, Record{Payload: payload})
	if err != nil {
		t.Fatalf("ApplyRecord SmgrTruncate: %v", err)
	}
	if !applied {
		t.Error("ApplyRecord returned applied=false for SmgrTruncate")
	}
	n, _ := mgr.NBlocks(rel)
	if n != 0 {
		t.Errorf("NBlocks after SmgrTruncate = %d, want 0", n)
	}
}
