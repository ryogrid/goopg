package xlog

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// --- M0131-S21a-2 part 4: XLOG_HEAP2_LOCK_UPDATED redo ----------------------
//
// goopg never emits this opcode — it is XLOG_HEAP_LOCK's near-sibling, written
// by heap_lock_updated_tuple_rec when a tuple-lock request (SELECT ... FOR
// UPDATE/SHARE, an FK RI check, an UPDATE about to rewrite its target)
// discovers the row it locked was already updated by a concurrent live
// transaction and re-locks the newest visible version of the update chain
// instead. The wire struct is byte-identical to xl_heap_lock's, which is why
// buildHeapLockUpdatedPG reuses sizeOfXLogHeapLockData and the seed/read
// helpers from heap_lock_confirm_pg_test.go, but the redo mutation is
// deliberately smaller: no t_ctid/HOT_UPDATED fixup, no cmax stamp (see
// storage.PageApplyHeapLockUpdatedRedo's doc comment for why).
//
// Design: docs/design/0131-0015-pg-wal-opcode-coverage.md §S21a-2 part 4.

// buildHeapLockUpdatedPG assembles a real xl_heap_lock_updated record —
// identical wire layout to xl_heap_lock, on RM_HEAP2 with opcode 0x60.
func buildHeapLockUpdatedPG(t *testing.T, rel storage.RelFileNode, blk storage.BlockNumber, xid uint32,
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
	return framePGAssembled(RmgrHeap2, xlogHeap2LockUpdated, xid, body)
}

// TestApplyRecordReplaysPGHeapLockUpdated pins the two ways this redo diverges
// from XLOG_HEAP_LOCK's: an existing forward t_ctid link and cmax must both
// survive untouched, because re-locking an older chain version can never
// legitimately claim to be the chain head or the locker's own command target.
func TestApplyRecordReplaysPGHeapLockUpdated(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4420, Fork: storage.MainFork}
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	seedHeapTuplePG(t, mgr, rel, 0, 42, "row", 100)

	// Pre-stamp a HOT forward link and a distinguishable cmax, the state a
	// re-lock of an older chain version inherits.
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, page); err != nil {
		t.Fatal(err)
	}
	if err := storage.PageStampHotOldTuple(page, 1, 7, 0, 5); err != nil {
		t.Fatal(err)
	}
	item, err := storage.PageGetItemID(page, 1)
	if err != nil {
		t.Fatal(err)
	}
	off := int(item.Offset)
	binary.LittleEndian.PutUint32(page[off+8:off+12], 321) // cmax sentinel
	if err := mgr.WriteBlock(rel, 0, page); err != nil {
		t.Fatal(err)
	}

	framed := buildHeapLockUpdatedPG(t, rel, 0, 55, 99, 1, xlhlXmaxLockOnly|xlhlXmaxExclLock, 0)
	applyPGRecord(t, mgr, framed, 200)

	page, tup := readTupleAt(t, mgr, rel, 0, 1)
	if got := storage.MustHeader(page).LSN(); got != 200 {
		t.Fatalf("pd_lsn = %d, want 200", got)
	}
	if tup.Header.Xmax != 99 {
		t.Fatalf("t_xmax = %d, want 99", tup.Header.Xmax)
	}
	if tup.Header.Infomask&storage.HeapXmaxLockOnly == 0 || tup.Header.Infomask&storage.HeapXmaxExclLock == 0 {
		t.Fatalf("t_infomask = %#x, want HEAP_XMAX_LOCK_ONLY|HEAP_XMAX_EXCL_LOCK", tup.Header.Infomask)
	}
	want := storage.ItemPointer{Block: 0, Offset: 5}
	if tup.Header.CTID != want {
		t.Fatalf("t_ctid = %+v, want the untouched forward link %+v — LOCK_UPDATED must not stamp a self-pointer", tup.Header.CTID, want)
	}
	if tup.Header.Infomask2&storage.HeapHotUpdated == 0 {
		t.Fatalf("t_infomask2 = %#x, HEAP_HOT_UPDATED must survive — LOCK_UPDATED never clears it", tup.Header.Infomask2)
	}
	if tup.Header.Xvac != 321 {
		t.Fatalf("t_cid (cmax) = %d, want the untouched sentinel 321 — LOCK_UPDATED must not stamp FirstCommandId", tup.Header.Xvac)
	}
}

// TestApplyRecordPGHeapLockUpdatedClearsStaleXmaxBits mirrors the HEAP_LOCK
// guard: HEAP_XMAX_BITS|HEAP_MOVED must still be cleared before the new
// infobits are OR'd in, or a reader would trust a stale xmax hint.
func TestApplyRecordPGHeapLockUpdatedClearsStaleXmaxBits(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4421, Fork: storage.MainFork}
	seedHeapTuplePG(t, mgr, rel, 0, 42, "row", 100)

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
		storage.HeapXmaxCommitted|storage.HeapXmaxInvalid|storage.HeapMovedOff|storage.HeapXminCommitted)
	if err := mgr.WriteBlock(rel, 0, page); err != nil {
		t.Fatal(err)
	}

	framed := buildHeapLockUpdatedPG(t, rel, 0, 55, 99, 1, xlhlXmaxLockOnly|xlhlXmaxKeyShrLock, 0)
	applyPGRecord(t, mgr, framed, 200)

	_, tup := readTupleAt(t, mgr, rel, 0, 1)
	for name, bit := range map[string]uint16{
		"HEAP_XMAX_COMMITTED": storage.HeapXmaxCommitted,
		"HEAP_XMAX_INVALID":   storage.HeapXmaxInvalid,
		"HEAP_MOVED_OFF":      storage.HeapMovedOff,
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
}

// TestApplyRecordPGHeapLockUpdatedIsIdempotent pins the pd_lsn interlock.
func TestApplyRecordPGHeapLockUpdatedIsIdempotent(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4422, Fork: storage.MainFork}
	seedHeapTuplePG(t, mgr, rel, 0, 42, "row", 100)

	applyPGRecord(t, mgr, buildHeapLockUpdatedPG(t, rel, 0, 55, 99, 1, xlhlXmaxLockOnly|xlhlXmaxExclLock, 0), 300)
	applyPGRecord(t, mgr, buildHeapLockUpdatedPG(t, rel, 0, 55, 777, 1, xlhlXmaxLockOnly|xlhlXmaxExclLock, 0), 200)

	page, tup := readTupleAt(t, mgr, rel, 0, 1)
	if tup.Header.Xmax != 99 {
		t.Fatalf("t_xmax = %d, want 99 — the older record must have been skipped", tup.Header.Xmax)
	}
	if got := storage.MustHeader(page).LSN(); got != 300 {
		t.Fatalf("pd_lsn = %d, want 300", got)
	}
}

// TestApplyRecordPGHeapLockUpdatedSkipsAbsentPage mirrors HEAP_LOCK's
// RBM_NORMAL guard: a missing page is skipped, never zero-extended.
func TestApplyRecordPGHeapLockUpdatedSkipsAbsentPage(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4423, Fork: storage.MainFork}
	seedHeapTuplePG(t, mgr, rel, 0, 42, "row", 100)

	if err := applyPGRecordErr(t, mgr, buildHeapLockUpdatedPG(t, rel, 9, 55, 99, 1, xlhlXmaxLockOnly|xlhlXmaxExclLock, 0), 200); err != nil {
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

// TestApplyRecordPGHeapLockUpdatedRefusesInvalidOffset pins upstream's
// elog(PANIC, "invalid lp").
func TestApplyRecordPGHeapLockUpdatedRefusesInvalidOffset(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4424, Fork: storage.MainFork}
	seedHeapTuplePG(t, mgr, rel, 0, 42, "row", 100)

	err := applyPGRecordErr(t, mgr, buildHeapLockUpdatedPG(t, rel, 0, 55, 99, 9, xlhlXmaxLockOnly|xlhlXmaxExclLock, 0), 200)
	if err == nil || !strings.Contains(err.Error(), "heap2-lock-updated apply") {
		t.Fatalf("ApplyRecord error = %v, want a heap2-lock-updated apply refusal", err)
	}
}

// TestApplyRecordPGHeapLockUpdatedRefusesShortMainData guards the decode
// bound, reusing xl_heap_lock's fixed size (the wire structs are identical).
func TestApplyRecordPGHeapLockUpdatedRefusesShortMainData(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4425, Fork: storage.MainFork}
	seedHeapTuplePG(t, mgr, rel, 0, 42, "row", 100)

	body, err := assembleXLogRecord(make([]byte, sizeOfXLogHeapLockData-1), []BlockRef{{ID: 0, Rel: rel, Block: 0}})
	if err != nil {
		t.Fatal(err)
	}
	err = applyPGRecordErr(t, mgr, framePGAssembled(RmgrHeap2, xlogHeap2LockUpdated, 55, body), 200)
	if err == nil || !strings.Contains(err.Error(), "heap-lock main-data len") {
		t.Fatalf("ApplyRecord error = %v, want a short-main-data refusal", err)
	}
}

// TestApplyRecordPGHeapLockUpdatedClearsVMAllFrozen pins that
// XLH_LOCK_ALL_FROZEN_CLEARED is honoured on this opcode too — the flags byte
// and redoClearVMBitsForHeapBlock call are shared with XLOG_HEAP_LOCK, but the
// dispatch wiring is new and untested without this.
func TestApplyRecordPGHeapLockUpdatedClearsVMAllFrozen(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4426, Fork: storage.MainFork}
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	seedHeapTuplePG(t, mgr, rel, 0, 42, "row", 100)
	applyPGRecord(t, mgr, buildHeapVisiblePG(t, rel, 0, 55, storage.VMAllVisible|storage.VMAllFrozen), 150)

	framed := buildHeapLockUpdatedPG(t, rel, 0, 56, 99, 1, xlhlXmaxLockOnly|xlhlXmaxExclLock, xlhLockAllFrozenCleared)
	applyPGRecord(t, mgr, framed, 300)

	if got := vmBitsAt(t, mgr, rel, 0); got != storage.VMAllVisible {
		t.Fatalf("vm bits = %#x, want ALL_VISIBLE only (ALL_FROZEN cleared, ALL_VISIBLE kept)", got)
	}
}
