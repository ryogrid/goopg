package wal

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// --- M0131-S21a-2: XLOG_HEAP_LOCK / XLOG_HEAP_CONFIRM redo -------------------
//
// goopg emits neither opcode in PG form — its own row locks are the native
// RecordKindHeapLock record, and its upsert path locks before inserting rather
// than speculating — so before S21a-2 a PG crash tail refused the start at the
// first SELECT ... FOR UPDATE, foreign-key check, or INSERT ... ON CONFLICT.
// The record bytes are hand-built here for the same reason: there is no goopg
// encoder for a record goopg does not produce.
//
// Design: docs/design/0131-0015-pg-wal-opcode-coverage.md §S21a-2.

// buildHeapLockPG assembles a real xl_heap_lock record: main data
// {TransactionId xmax; OffsetNumber offnum; uint8 infobits_set; uint8 flags}
// and one empty block reference (upstream registers the buffer with no data).
func buildHeapLockPG(t *testing.T, rel storage.RelFileNode, blk storage.BlockNumber, xid uint32,
	xmax uint32, offnum uint16, infobits, flags uint8,
) []byte {
	t.Helper()
	mainData := make([]byte, sizeOfXLogHeapLockData)
	binary.LittleEndian.PutUint32(mainData[0:4], xmax)
	binary.LittleEndian.PutUint16(mainData[4:6], offnum)
	mainData[6] = infobits
	mainData[7] = flags

	body, err := assembleXLogRecord(mainData, []BlockRef{{ID: 0, Rel: rel, Block: blk}})
	if err != nil {
		t.Fatalf("assembleXLogRecord: %v", err)
	}
	return framePGAssembled(RmgrHeap, xlogHeapLock, xid, body)
}

// buildHeapConfirmPG assembles a real xl_heap_confirm record: main data is the
// single OffsetNumber of the speculatively inserted tuple.
func buildHeapConfirmPG(t *testing.T, rel storage.RelFileNode, blk storage.BlockNumber, xid uint32, offnum uint16) []byte {
	t.Helper()
	mainData := make([]byte, sizeOfXLogHeapConfirmData)
	binary.LittleEndian.PutUint16(mainData[0:2], offnum)

	body, err := assembleXLogRecord(mainData, []BlockRef{{ID: 0, Rel: rel, Block: blk}})
	if err != nil {
		t.Fatalf("assembleXLogRecord: %v", err)
	}
	return framePGAssembled(RmgrHeap, xlogHeapConfirm, xid, body)
}

// seedHeapTuplePG writes one tuple at slot 1 of block `blk` through the PG
// heap-insert redo path, so the page reaching the lock/confirm redo was built
// exactly the way a replayed PG page is.
func seedHeapTuplePG(t *testing.T, mgr *storage.Manager, rel storage.RelFileNode, blk storage.BlockNumber, xmin storage.TransactionID, data string, endLSN uint64) {
	t.Helper()
	tup := storage.NewHeapTuple(xmin, storage.InvalidTransactionID, []byte(data))
	tup.Header.CTID = storage.ItemPointer{Block: blk, Offset: 1}
	raw, err := tup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	framed, err := EncodeHeapInsertPG(rel, blk, 1, raw, false)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, framed, endLSN)
}

func readTupleAt(t *testing.T, mgr *storage.Manager, rel storage.RelFileNode, blk storage.BlockNumber, slot uint16) (storage.Page, storage.HeapTuple) {
	t.Helper()
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, blk, page); err != nil {
		t.Fatal(err)
	}
	tup, err := storage.PageGetHeapTuple(page, slot)
	if err != nil {
		t.Fatal(err)
	}
	return page, tup
}

// TestApplyRecordReplaysPGHeapLock covers the two shapes a real xl_heap_lock
// comes in, which differ in far more than a bit value:
//
//   - a plain FOR UPDATE lock (LOCK_ONLY | EXCL_LOCK): xmax is a lock, so redo
//     must ALSO clear HEAP_HOT_UPDATED and re-point t_ctid at the tuple itself,
//     because a locker must not leave a forward chain link behind;
//   - a multixact lock with a key update (IS_MULTI | KEYS_UPDATED): xmax is NOT
//     locked-only, so t_ctid and the HOT bit must be left exactly as they were.
//
// Reading the wrong branch is silent corruption (a chain follower chasing a
// stale link, or a lost successor pointer), not a decode error, so each shape
// gets its own assertions.
func TestApplyRecordReplaysPGHeapLock(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4310, Fork: storage.MainFork}

	t.Run("lock_only_self_ctid", func(t *testing.T) {
		mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
		defer mgr.Close()
		seedHeapTuplePG(t, mgr, rel, 0, 42, "row", 100)

		// Pre-stamp a HOT forward link, the state a locker inherits when it
		// locks the latest version of a chain that was HOT-updated earlier.
		page := make(storage.Page, storage.BlockSize)
		if err := mgr.ReadBlock(rel, 0, page); err != nil {
			t.Fatal(err)
		}
		if err := storage.PageStampHotOldTuple(page, 1, 7, 0, 5); err != nil {
			t.Fatal(err)
		}
		if err := mgr.WriteBlock(rel, 0, page); err != nil {
			t.Fatal(err)
		}

		framed := buildHeapLockPG(t, rel, 0, 55, 99, 1, xlhlXmaxLockOnly|xlhlXmaxExclLock, 0)
		applyPGRecord(t, mgr, framed, 200)

		page, tup := readTupleAt(t, mgr, rel, 0, 1)
		if got := storage.MustHeader(page).LSN(); got != 200 {
			t.Fatalf("pd_lsn = %d, want 200", got)
		}
		if tup.Header.Xmax != 99 {
			t.Fatalf("t_xmax = %d, want 99", tup.Header.Xmax)
		}
		if tup.Header.Xmin != 42 || string(tup.Data) != "row" {
			t.Fatalf("lock redo disturbed xmin/data: xmin=%d data=%q", tup.Header.Xmin, tup.Data)
		}
		if tup.Header.Infomask&storage.HeapXmaxLockOnly == 0 || tup.Header.Infomask&storage.HeapXmaxExclLock == 0 {
			t.Fatalf("t_infomask = %#x, want HEAP_XMAX_LOCK_ONLY|HEAP_XMAX_EXCL_LOCK", tup.Header.Infomask)
		}
		if tup.Header.Infomask&storage.HeapXmaxIsMulti != 0 {
			t.Fatalf("t_infomask = %#x, HEAP_XMAX_IS_MULTI must not be set", tup.Header.Infomask)
		}
		if tup.Header.Infomask2&storage.HeapHotUpdated != 0 {
			t.Fatalf("t_infomask2 = %#x, a locked-only xmax must clear HEAP_HOT_UPDATED", tup.Header.Infomask2)
		}
		want := storage.ItemPointer{Block: 0, Offset: 1}
		if tup.Header.CTID != want {
			t.Fatalf("t_ctid = %+v, want the self-pointer %+v (heap_xlog_lock's ItemPointerSet)", tup.Header.CTID, want)
		}
		if got := tup.Header.Xvac; got != storage.TransactionID(storage.FirstCommandId) {
			t.Fatalf("t_cid = %d, want FirstCommandId (%d)", got, storage.FirstCommandId)
		}
	})

	t.Run("multi_keys_updated_keeps_chain_link", func(t *testing.T) {
		mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
		defer mgr.Close()
		seedHeapTuplePG(t, mgr, rel, 0, 42, "row", 100)

		page := make(storage.Page, storage.BlockSize)
		if err := mgr.ReadBlock(rel, 0, page); err != nil {
			t.Fatal(err)
		}
		if err := storage.PageStampHotOldTuple(page, 1, 7, 0, 5); err != nil {
			t.Fatal(err)
		}
		if err := mgr.WriteBlock(rel, 0, page); err != nil {
			t.Fatal(err)
		}

		framed := buildHeapLockPG(t, rel, 0, 55, 1234, 1, xlhlXmaxIsMulti|xlhlKeysUpdated, 0)
		applyPGRecord(t, mgr, framed, 200)

		_, tup := readTupleAt(t, mgr, rel, 0, 1)
		if tup.Header.Xmax != 1234 {
			t.Fatalf("t_xmax = %d, want the MultiXactId 1234", tup.Header.Xmax)
		}
		if tup.Header.Infomask&storage.HeapXmaxIsMulti == 0 {
			t.Fatalf("t_infomask = %#x, want HEAP_XMAX_IS_MULTI", tup.Header.Infomask)
		}
		if tup.Header.Infomask&storage.HeapXmaxLockOnly != 0 {
			t.Fatalf("t_infomask = %#x, HEAP_XMAX_LOCK_ONLY must not be invented", tup.Header.Infomask)
		}
		if tup.Header.Infomask2&storage.HeapKeysUpdated == 0 {
			t.Fatalf("t_infomask2 = %#x, want HEAP_KEYS_UPDATED", tup.Header.Infomask2)
		}
		if tup.Header.Infomask2&storage.HeapHotUpdated == 0 {
			t.Fatalf("t_infomask2 = %#x, an updating xmax must NOT clear HEAP_HOT_UPDATED", tup.Header.Infomask2)
		}
		want := storage.ItemPointer{Block: 0, Offset: 5}
		if tup.Header.CTID != want {
			t.Fatalf("t_ctid = %+v, want the preserved forward link %+v", tup.Header.CTID, want)
		}
	})
}

// TestApplyRecordPGHeapLockClearsStaleXmaxBits pins the "turn these all off when
// Xmax is to change" clause (HEAP_XMAX_BITS | HEAP_MOVED, htup_details.h:284).
// A tuple whose previous xmax was a committed deleter must not keep
// HEAP_XMAX_COMMITTED after being restamped with a live locker's xid — a reader
// would take the hint and treat the row as deleted by transaction 99.
func TestApplyRecordPGHeapLockClearsStaleXmaxBits(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4311, Fork: storage.MainFork}
	seedHeapTuplePG(t, mgr, rel, 0, 42, "row", 100)

	// Hand-set the stale bits directly: no goopg writer produces HEAP_MOVED_*,
	// which is exactly why redo must still clear them for a pg_upgrade'd tuple.
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, page); err != nil {
		t.Fatal(err)
	}
	item, err := storage.PageGetItemID(page, 1)
	if err != nil {
		t.Fatal(err)
	}
	off := int(item.Offset)
	binary.LittleEndian.PutUint16(page[off+20:off+22],
		storage.HeapXmaxCommitted|storage.HeapXmaxInvalid|storage.HeapMovedOff|storage.HeapComboCID|storage.HeapXminCommitted)
	binary.LittleEndian.PutUint16(page[off+18:off+20], binary.LittleEndian.Uint16(page[off+18:off+20])|storage.HeapKeysUpdated)
	if err := mgr.WriteBlock(rel, 0, page); err != nil {
		t.Fatal(err)
	}

	framed := buildHeapLockPG(t, rel, 0, 55, 99, 1, xlhlXmaxLockOnly|xlhlXmaxKeyShrLock, 0)
	applyPGRecord(t, mgr, framed, 200)

	_, tup := readTupleAt(t, mgr, rel, 0, 1)
	for name, bit := range map[string]uint16{
		"HEAP_XMAX_COMMITTED": storage.HeapXmaxCommitted,
		"HEAP_XMAX_INVALID":   storage.HeapXmaxInvalid,
		"HEAP_MOVED_OFF":      storage.HeapMovedOff,
		"HEAP_COMBOCID":       storage.HeapComboCID,
		"HEAP_XMAX_EXCL_LOCK": storage.HeapXmaxExclLock,
	} {
		if tup.Header.Infomask&bit != 0 {
			t.Errorf("t_infomask = %#x, %s must have been cleared", tup.Header.Infomask, name)
		}
	}
	if tup.Header.Infomask&storage.HeapXminCommitted == 0 {
		t.Errorf("t_infomask = %#x, the xmin hint is not an xmax bit and must survive", tup.Header.Infomask)
	}
	if tup.Header.Infomask&storage.HeapXmaxKeyShrLock == 0 {
		t.Errorf("t_infomask = %#x, want HEAP_XMAX_KEYSHR_LOCK", tup.Header.Infomask)
	}
	if tup.Header.Infomask2&storage.HeapKeysUpdated != 0 {
		t.Errorf("t_infomask2 = %#x, HEAP_KEYS_UPDATED must be cleared when the record does not set it", tup.Header.Infomask2)
	}
}

// TestApplyRecordPGHeapLockIsIdempotent pins the pd_lsn interlock: replaying a
// record the page already reflects must not restamp it. The second record
// carries a LOWER EndLSN, standing in for the re-read of a tail that was
// already applied before the crash.
func TestApplyRecordPGHeapLockIsIdempotent(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4312, Fork: storage.MainFork}
	seedHeapTuplePG(t, mgr, rel, 0, 42, "row", 100)

	applyPGRecord(t, mgr, buildHeapLockPG(t, rel, 0, 55, 99, 1, xlhlXmaxLockOnly|xlhlXmaxExclLock, 0), 300)
	applyPGRecord(t, mgr, buildHeapLockPG(t, rel, 0, 55, 777, 1, xlhlXmaxLockOnly|xlhlXmaxExclLock, 0), 200)

	page, tup := readTupleAt(t, mgr, rel, 0, 1)
	if tup.Header.Xmax != 99 {
		t.Fatalf("t_xmax = %d, want 99 — the older record must have been skipped", tup.Header.Xmax)
	}
	if got := storage.MustHeader(page).LSN(); got != 300 {
		t.Fatalf("pd_lsn = %d, want 300", got)
	}
}

// TestApplyRecordPGHeapLockSkipsAbsentPage pins the RBM_NORMAL half of
// upstream's buffer acquisition: a lock/confirm record whose page does not
// exist is skipped (BLK_NOTFOUND), NOT zero-extended the way an insert-like
// record's block is. Getting this wrong would materialise an empty page for a
// relation the stream is about to drop, and then fail "invalid lp" on it.
func TestApplyRecordPGHeapLockSkipsAbsentPage(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4313, Fork: storage.MainFork}
	seedHeapTuplePG(t, mgr, rel, 0, 42, "row", 100)

	if err := applyPGRecordErr(t, mgr, buildHeapLockPG(t, rel, 9, 55, 99, 1, xlhlXmaxLockOnly|xlhlXmaxExclLock, 0), 200); err != nil {
		t.Fatalf("ApplyRecord on an absent block: %v, want a silent skip", err)
	}
	nblocks, err := mgr.NBlocks(rel)
	if err != nil {
		t.Fatal(err)
	}
	if nblocks != 1 {
		t.Fatalf("nblocks = %d, want 1 — the fork must not have been extended to reach block 9", nblocks)
	}
}

// TestApplyRecordPGHeapLockRefusesInvalidOffset pins upstream's
// elog(PANIC, "invalid lp"): an offset that is not an LP_NORMAL line pointer is
// a corrupt record, and replay must stop rather than write somewhere arbitrary.
func TestApplyRecordPGHeapLockRefusesInvalidOffset(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4314, Fork: storage.MainFork}
	seedHeapTuplePG(t, mgr, rel, 0, 42, "row", 100)

	err := applyPGRecordErr(t, mgr, buildHeapLockPG(t, rel, 0, 55, 99, 9, xlhlXmaxLockOnly|xlhlXmaxExclLock, 0), 200)
	if err == nil || !strings.Contains(err.Error(), "heap-lock apply") {
		t.Fatalf("ApplyRecord error = %v, want a heap-lock apply refusal", err)
	}
}

// TestApplyRecordPGHeapLockRefusesShortMainData guards the decode bound: a
// truncated xl_heap_lock must be reported, never read past.
func TestApplyRecordPGHeapLockRefusesShortMainData(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4315, Fork: storage.MainFork}
	seedHeapTuplePG(t, mgr, rel, 0, 42, "row", 100)

	body, err := assembleXLogRecord(make([]byte, sizeOfXLogHeapLockData-1), []BlockRef{{ID: 0, Rel: rel, Block: 0}})
	if err != nil {
		t.Fatal(err)
	}
	err = applyPGRecordErr(t, mgr, framePGAssembled(RmgrHeap, xlogHeapLock, 55, body), 200)
	if err == nil || !strings.Contains(err.Error(), "heap-lock main-data len") {
		t.Fatalf("ApplyRecord error = %v, want a short-main-data refusal", err)
	}
}

// TestApplyRecordReplaysPGHeapConfirm pins the second record of every
// INSERT ... ON CONFLICT. The speculative insert leaves a speculative TOKEN in
// t_ctid, not a self-pointer; without the confirm redo the replayed tuple keeps
// it and a chain follower chases a location that does not exist.
func TestApplyRecordReplaysPGHeapConfirm(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4316, Fork: storage.MainFork}

	// Speculative insert: t_ctid holds the token, here (0xDEAD, 0xBEEF)-ish.
	tup := storage.NewHeapTuple(42, storage.InvalidTransactionID, []byte("spec"))
	tup.Header.CTID = storage.ItemPointer{Block: 57005, Offset: 4660}
	raw, err := tup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	framed, err := EncodeHeapInsertPG(rel, 0, 1, raw, false)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, framed, 100)

	applyPGRecord(t, mgr, buildHeapConfirmPG(t, rel, 0, 42, 1), 200)

	page, after := readTupleAt(t, mgr, rel, 0, 1)
	want := storage.ItemPointer{Block: 0, Offset: 1}
	if after.Header.CTID != want {
		t.Fatalf("t_ctid = %+v, want the self-pointer %+v", after.Header.CTID, want)
	}
	if after.Header.Xmin != 42 || string(after.Data) != "spec" {
		t.Fatalf("confirm redo disturbed the tuple: xmin=%d data=%q", after.Header.Xmin, after.Data)
	}
	if got := storage.MustHeader(page).LSN(); got != 200 {
		t.Fatalf("pd_lsn = %d, want 200", got)
	}
}

// TestApplyRecordPGHeapConfirmRefusesShortMainData is the confirm twin of the
// heap-lock decode-bound guard.
func TestApplyRecordPGHeapConfirmRefusesShortMainData(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4317, Fork: storage.MainFork}
	seedHeapTuplePG(t, mgr, rel, 0, 42, "row", 100)

	body, err := assembleXLogRecord([]byte{0}, []BlockRef{{ID: 0, Rel: rel, Block: 0}})
	if err != nil {
		t.Fatal(err)
	}
	err = applyPGRecordErr(t, mgr, framePGAssembled(RmgrHeap, xlogHeapConfirm, 42, body), 200)
	if err == nil || !strings.Contains(err.Error(), "heap-confirm main-data len") {
		t.Fatalf("ApplyRecord error = %v, want a short-main-data refusal", err)
	}
}
