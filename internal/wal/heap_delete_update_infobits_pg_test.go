package wal

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// --- M0131-S21h: xl_heap_delete / xl_heap_update infobits_set redo -----------
//
// goopg's own emit hardcodes infobits_set = 0 (pg_assembled_emit.go), so no
// goopg-authored record ever exercised the byte and replay simply discarded it.
// A hosted PG's stream does not have that property: fix_infomask_from_infobits
// is how upstream tells redo that the xmax it just handed over is a
// **MultiXactId** (XLHL_XMAX_IS_MULTI — an UPDATE or DELETE of a row a
// concurrent xact holds FOR KEY SHARE, the commonest real-world source being an
// FK RI check), that a lock mode survives on the tuple, or that key columns
// changed. Replaying such a record as a bare xid leaves a page whose xmax is a
// multi wearing an xid's clothes.
//
// The records are hand-built here for the same reason S21a-2's were: there is
// no goopg encoder for a shape goopg does not produce.
//
// Design: docs/design/0131-0015-pg-wal-opcode-coverage.md §S21h.

// buildHeapDeletePG assembles a real xl_heap_delete record: main data
// {TransactionId xmax; OffsetNumber offnum; uint8 infobits_set; uint8 flags}
// and one empty block reference.
func buildHeapDeletePG(t *testing.T, rel storage.RelFileNode, blk storage.BlockNumber, xid uint32,
	xmax uint32, offnum uint16, infobits, flags uint8,
) []byte {
	t.Helper()
	mainData := make([]byte, sizeOfXLogHeapDeleteData)
	binary.LittleEndian.PutUint32(mainData[0:4], xmax)
	binary.LittleEndian.PutUint16(mainData[4:6], offnum)
	mainData[6] = infobits
	mainData[7] = flags

	body, err := assembleXLogRecord(mainData, []BlockRef{{ID: 0, Rel: rel, Block: blk}})
	if err != nil {
		t.Fatalf("assembleXLogRecord: %v", err)
	}
	return framePGAssembled(RmgrHeap, xlogHeapDelete, xid, body)
}

// buildHeapUpdatePG assembles a real xl_heap_update record carrying the new
// tuple on block 0 (same-page form), with a caller-chosen old_infobits_set.
// opcode selects the HOT (0x40) or non-HOT (0x20) variant.
func buildHeapUpdatePG(t *testing.T, rel storage.RelFileNode, blk storage.BlockNumber, xid uint32,
	oldXmax uint32, oldSlot uint16, oldInfobits uint8, newSlot uint16, newTuple []byte, opcode uint8,
) []byte {
	t.Helper()
	mainData := make([]byte, sizeOfXLogHeapUpdateData)
	binary.LittleEndian.PutUint32(mainData[0:4], oldXmax)
	binary.LittleEndian.PutUint16(mainData[4:6], oldSlot)
	mainData[6] = oldInfobits
	mainData[7] = xlhUpdateContainsNewTuple
	binary.LittleEndian.PutUint16(mainData[12:14], newSlot)

	body, err := assembleXLogRecord(mainData, []BlockRef{{
		ID: 0, Rel: rel, Block: blk, Data: heapHeaderPlusData(newTuple),
	}})
	if err != nil {
		t.Fatalf("assembleXLogRecord: %v", err)
	}
	return framePGAssembled(RmgrHeap, opcode, xid, body)
}

// seedHeapTupleForRedo inserts one tuple at slot 1 of block 0 through the PG
// heap-insert redo path and returns the manager, so the page the delete/update
// redo mutates was built exactly the way a replayed PG page is.
func seedHeapTupleForRedo(t *testing.T, xmin storage.TransactionID, data string) (*storage.Manager, storage.RelFileNode) {
	t.Helper()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	t.Cleanup(func() { mgr.Close() })
	rel := storage.RelFileNode{DBOid: 1, RelOid: 921, Fork: storage.MainFork}

	tup := storage.NewHeapTuple(xmin, storage.InvalidTransactionID, []byte(data))
	tup.Header.CTID = storage.ItemPointer{Block: 0, Offset: 1}
	tupleBytes, err := tup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	framed, err := EncodeHeapInsertPG(rel, 0, 1, tupleBytes, false)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, framed, 100)
	return mgr, rel
}

func readRedoTuple(t *testing.T, mgr *storage.Manager, rel storage.RelFileNode, slot uint16) (storage.Page, storage.HeapTuple) {
	t.Helper()
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, page); err != nil {
		t.Fatal(err)
	}
	tup, err := storage.PageGetHeapTuple(page, slot)
	if err != nil {
		t.Fatal(err)
	}
	return page, tup
}

// TestReplayPGHeapDeleteRestoresMultiXactInfobits is the slice's headline case:
// a PG DELETE of a row also held FOR KEY SHARE carries a MultiXactId in
// xl_heap_delete.xmax and says so through XLHL_XMAX_IS_MULTI. Redo must land
// HEAP_XMAX_IS_MULTI on the page, or every reader mis-reads the multi as a
// transaction id.
func TestReplayPGHeapDeleteRestoresMultiXactInfobits(t *testing.T) {
	mgr, rel := seedHeapTupleForRedo(t, 42, "row")

	const multi = uint32(7)
	applyPGRecord(t, mgr, buildHeapDeletePG(t, rel, 0, 99, multi, 1,
		xlhlXmaxIsMulti|xlhlXmaxKeyShrLock|xlhlKeysUpdated, 0), 200)

	_, after := readRedoTuple(t, mgr, rel, 1)
	if after.Header.Xmax != storage.TransactionID(multi) {
		t.Fatalf("t_xmax = %d, want %d", after.Header.Xmax, multi)
	}
	if !storage.IsHeapTupleXmaxMulti(after.Header.Infomask) {
		t.Fatalf("HEAP_XMAX_IS_MULTI not restored: infomask=%#x", after.Header.Infomask)
	}
	if after.Header.Infomask&storage.HeapXmaxKeyShrLock == 0 {
		t.Fatalf("HEAP_XMAX_KEYSHR_LOCK not restored: infomask=%#x", after.Header.Infomask)
	}
	if after.Header.Infomask2&storage.HeapKeysUpdated == 0 {
		t.Fatalf("HEAP_KEYS_UPDATED not restored: infomask2=%#x", after.Header.Infomask2)
	}
	// heap_xlog_delete's remaining tuple mutations.
	if after.Header.Infomask2&storage.HeapHotUpdated != 0 {
		t.Fatalf("HEAP_HOT_UPDATED not cleared: infomask2=%#x", after.Header.Infomask2)
	}
	if after.Header.CTID != (storage.ItemPointer{Block: 0, Offset: 1}) {
		t.Fatalf("t_ctid = %+v, want self-pointing {0,1}", after.Header.CTID)
	}
	if after.Header.Xmin != 42 || string(after.Data) != "row" {
		t.Fatalf("delete touched xmin/data: xmin=%d data=%q", after.Header.Xmin, after.Data)
	}
}

// TestReplayPGHeapDeleteInfobitsZeroKeepsPlainXmax pins the goopg-authored
// shape: infobits_set = 0 must still produce exactly the pre-S21h page — a
// plain deleter xid with no multi/lock bits — so the fidelity work did not
// change goopg↔goopg replay.
func TestReplayPGHeapDeleteInfobitsZeroKeepsPlainXmax(t *testing.T) {
	mgr, rel := seedHeapTupleForRedo(t, 42, "row")

	framed, err := EncodeHeapDeletePG(rel, 0, 1, 99, nil)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, framed, 200)

	_, after := readRedoTuple(t, mgr, rel, 1)
	if after.Header.Xmax != 99 {
		t.Fatalf("t_xmax = %d, want 99", after.Header.Xmax)
	}
	if after.Header.Infomask&(storage.HeapXmaxIsMulti|storage.HeapXmaxLockOnly|storage.HeapXmaxLockMask) != 0 {
		t.Fatalf("xmax classification bits set from a zero infobits_set: infomask=%#x", after.Header.Infomask)
	}
	if after.Header.Infomask2&storage.HeapKeysUpdated != 0 {
		t.Fatalf("HEAP_KEYS_UPDATED set from a zero infobits_set: infomask2=%#x", after.Header.Infomask2)
	}
}

// TestReplayPGHeapDeleteIsSuperClearsXmin: XLH_DELETE_IS_SUPER is upstream's
// speculative-insert cleanup — it kills the tuple by clearing xmin, NOT by
// stamping xmax (heap_xlog_delete). goopg used to stamp xmax unconditionally,
// leaving the aborted speculative tuple alive to any snapshot that ignores the
// deleter.
func TestReplayPGHeapDeleteIsSuperClearsXmin(t *testing.T) {
	mgr, rel := seedHeapTupleForRedo(t, 42, "spec")

	applyPGRecord(t, mgr, buildHeapDeletePG(t, rel, 0, 99, 99, 1, 0, xlhDeleteIsSuper), 200)

	_, after := readRedoTuple(t, mgr, rel, 1)
	if after.Header.Xmin != storage.InvalidTransactionID {
		t.Fatalf("t_xmin = %d, want InvalidTransactionID (IS_SUPER)", after.Header.Xmin)
	}
	if after.Header.Xmax != storage.InvalidTransactionID {
		t.Fatalf("t_xmax = %d, want untouched under IS_SUPER", after.Header.Xmax)
	}
}

// TestReplayPGHeapDeletePartitionMoveSetsSentinel: a DELETE that is really the
// old half of a cross-partition UPDATE points t_ctid at the moved-partitions
// sentinel instead of at itself, which is how a reader raises the
// "tuple to be locked was already moved to another partition" error.
func TestReplayPGHeapDeletePartitionMoveSetsSentinel(t *testing.T) {
	mgr, rel := seedHeapTupleForRedo(t, 42, "moved")

	applyPGRecord(t, mgr, buildHeapDeletePG(t, rel, 0, 99, 99, 1, 0, xlhDeleteIsPartitionMove), 200)

	_, after := readRedoTuple(t, mgr, rel, 1)
	if !storage.IsMovedToAnotherPartition(after.Header.CTID) {
		t.Fatalf("t_ctid = %+v, want the moved-partitions sentinel", after.Header.CTID)
	}
}

// TestReplayPGHeapUpdateRestoresOldInfobits is the update-side twin: the old
// version's xmax is a MultiXactId whenever the updated row was held FOR KEY
// SHARE, and old_infobits_set is the only place the record says so. Both
// opcodes are checked because HOT and non-HOT differ only in the HOT_UPDATED
// branch — a sibling pair that must move together.
func TestReplayPGHeapUpdateRestoresOldInfobits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		opcode uint8
		hot    bool
	}{
		{"hot", xlogHeapHotUpdate, true},
		{"non-hot", xlogHeapUpdate, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, rel := seedHeapTupleForRedo(t, 42, "old")

			newTup := storage.NewHeapTuple(99, storage.InvalidTransactionID, []byte("new"))
			newTup.Header.CTID = storage.ItemPointer{Block: 0, Offset: 2}
			newBytes, err := newTup.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			const multi = uint32(11)
			applyPGRecord(t, mgr, buildHeapUpdatePG(t, rel, 0, 99, multi, 1,
				xlhlXmaxIsMulti|xlhlXmaxKeyShrLock, 2, newBytes, tc.opcode), 200)

			_, old := readRedoTuple(t, mgr, rel, 1)
			if old.Header.Xmax != storage.TransactionID(multi) {
				t.Fatalf("old t_xmax = %d, want %d", old.Header.Xmax, multi)
			}
			if !storage.IsHeapTupleXmaxMulti(old.Header.Infomask) {
				t.Fatalf("HEAP_XMAX_IS_MULTI not restored: infomask=%#x", old.Header.Infomask)
			}
			if old.Header.Infomask&storage.HeapXmaxKeyShrLock == 0 {
				t.Fatalf("HEAP_XMAX_KEYSHR_LOCK not restored: infomask=%#x", old.Header.Infomask)
			}
			gotHot := old.Header.Infomask2&storage.HeapHotUpdated != 0
			if gotHot != tc.hot {
				t.Fatalf("HEAP_HOT_UPDATED = %v, want %v", gotHot, tc.hot)
			}
			if old.Header.CTID != (storage.ItemPointer{Block: 0, Offset: 2}) {
				t.Fatalf("old t_ctid = %+v, want the forward link {0,2}", old.Header.CTID)
			}
			if _, err := storage.PageGetHeapTuple(mustReadPage(t, mgr, rel), 2); err != nil {
				t.Fatalf("new version missing at slot 2: %v", err)
			}
		})
	}
}

// TestReplayPGHeapDeleteAdvancesPrunableFromRecordXID pins upstream's
// PageSetPrunable input: the RECORD's xid, not the stamped xmax. The two differ
// exactly when xmax is a MultiXactId — feeding a multi into pd_prune_xid would
// pin an arbitrary value into the page header.
func TestReplayPGHeapDeleteAdvancesPrunableFromRecordXID(t *testing.T) {
	mgr, rel := seedHeapTupleForRedo(t, 42, "row")

	const recordXID = uint32(77)
	const multi = uint32(7)
	applyPGRecord(t, mgr, buildHeapDeletePG(t, rel, 0, recordXID, multi, 1, xlhlXmaxIsMulti, 0), 200)

	page, _ := readRedoTuple(t, mgr, rel, 1)
	if got := storage.MustHeader(page).PruneXID(); got != recordXID {
		t.Fatalf("pd_prune_xid = %d, want the record xid %d", got, recordXID)
	}
}

func mustReadPage(t *testing.T, mgr *storage.Manager, rel storage.RelFileNode) storage.Page {
	t.Helper()
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, page); err != nil {
		t.Fatal(err)
	}
	return page
}
