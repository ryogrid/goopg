package xlog

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// --- M0131-S21a-2 part 3: XLOG_HEAP2_VISIBLE redo ---------------------------
//
// Every VACUUM emits one of these per page it marks all-visible, so a PG crash
// tail taken any time after a vacuum contains them and, before this slice,
// refused the start. It is also the first goopg redo that writes a fork other
// than `main`: block 0 is the visibility-map buffer, block 1 the heap block.
//
// Design: docs/design/0131-0015-pg-wal-opcode-coverage.md §S21a-2 part 3.

// buildHeapVisiblePG assembles a real xl_heap_visible record: main data
// {TransactionId snapshotConflictHorizon; uint8 flags}, block 0 the vm page,
// block 1 the heap page (the order upstream registers them in).
func buildHeapVisiblePG(t *testing.T, rel storage.RelFileNode, heapBlk storage.BlockNumber, xid uint32, flags uint8) []byte {
	t.Helper()
	vmRel := rel
	vmRel.Fork = storage.VisibilityMapFork

	mainData := make([]byte, sizeOfXLogHeapVisibleData)
	binary.LittleEndian.PutUint32(mainData[0:4], 7) // snapshotConflictHorizon
	mainData[4] = flags

	body, err := assembleXLogRecord(mainData, []BlockRef{
		{ID: 0, Rel: vmRel, Block: storage.VMBlockForHeapBlock(heapBlk)},
		{ID: 1, Rel: rel, Block: heapBlk},
	})
	if err != nil {
		t.Fatalf("assembleXLogRecord: %v", err)
	}
	return framePGAssembled(RmgrHeap2, xlogHeap2Visible, xid, body)
}

func vmBitsAt(t *testing.T, mgr *storage.Manager, rel storage.RelFileNode, heapBlk storage.BlockNumber) uint8 {
	t.Helper()
	vmRel := rel
	vmRel.Fork = storage.VisibilityMapFork
	vmBlk := storage.VMBlockForHeapBlock(heapBlk)
	nblocks, err := mgr.NBlocks(vmRel)
	if err != nil {
		t.Fatal(err)
	}
	if vmBlk >= nblocks {
		t.Fatalf("vm fork has %d blocks, expected it to reach block %d", nblocks, vmBlk)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(vmRel, vmBlk, page); err != nil {
		t.Fatal(err)
	}
	bits, err := storage.VMPageBits(page, heapBlk)
	if err != nil {
		t.Fatal(err)
	}
	return bits
}

// TestApplyRecordReplaysPGHeapVisible is the core guard: both halves of the
// record land. The heap page gets PD_ALL_VISIBLE and the vm fork — which does
// not exist at all when replay starts — is created and carries exactly the two
// map bits the record asked for.
func TestApplyRecordReplaysPGHeapVisible(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4410, Fork: storage.MainFork}

	t.Run("all_visible_only", func(t *testing.T) {
		mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
		defer mgr.Close()
		seedHeapTuplePG(t, mgr, rel, 0, 42, "row", 100)

		applyPGRecord(t, mgr, buildHeapVisiblePG(t, rel, 0, 55, storage.VMAllVisible), 200)

		page := make(storage.Page, storage.BlockSize)
		if err := mgr.ReadBlock(rel, 0, page); err != nil {
			t.Fatal(err)
		}
		if storage.MustHeader(page).Flags()&storage.PDAllVisible == 0 {
			t.Fatalf("heap page flags = %#x, want PD_ALL_VISIBLE set", storage.MustHeader(page).Flags())
		}
		if got := vmBitsAt(t, mgr, rel, 0); got != storage.VMAllVisible {
			t.Fatalf("vm bits = %#x, want ALL_VISIBLE (%#x)", got, storage.VMAllVisible)
		}
		// The tuple the page holds must be untouched — this record mutates a
		// page header and a map bit, nothing else.
		if _, tup := readTupleAt(t, mgr, rel, 0, 1); tup.Header.Xmin != 42 || string(tup.Data) != "row" {
			t.Fatalf("visible redo disturbed the tuple: xmin=%d data=%q", tup.Header.Xmin, tup.Data)
		}
	})

	t.Run("all_visible_and_frozen", func(t *testing.T) {
		mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
		defer mgr.Close()
		seedHeapTuplePG(t, mgr, rel, 0, 42, "row", 100)

		// VISIBILITYMAP_XLOG_CATALOG_REL rides along in the flags byte and must
		// be masked off before the bits reach the map page.
		flags := storage.VMAllVisible | storage.VMAllFrozen | xlogVisibilitymapXLogCatalogRel
		applyPGRecord(t, mgr, buildHeapVisiblePG(t, rel, 0, 55, flags), 200)

		if got := vmBitsAt(t, mgr, rel, 0); got != storage.VMAllVisible|storage.VMAllFrozen {
			t.Fatalf("vm bits = %#x, want ALL_VISIBLE|ALL_FROZEN (%#x)", got, storage.VMValidBits)
		}
	})
}

// TestApplyRecordPGHeapVisibleHighBlockExtendsVMFork covers the vm-side
// RBM_ZERO_ON_ERROR rule. A heap block far enough in maps to vm block 1, a page
// no vm fork of a young relation has; upstream initialises it on the spot
// ("initialize the page if it was read as zeros"), which for goopg means
// zero-extending the fork. Getting this wrong is a refused start, not a wrong
// bit.
func TestApplyRecordPGHeapVisibleHighBlockExtendsVMFork(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4411, Fork: storage.MainFork}
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	heapBlk := storage.BlockNumber(storage.VMHeapBlocksPerPage + 3)
	if got := storage.VMBlockForHeapBlock(heapBlk); got != 1 {
		t.Fatalf("test setup: heap block %d maps to vm block %d, want 1", heapBlk, got)
	}
	// The heap fork stays short on purpose: the heap half must skip (the block
	// does not exist) while the vm half still runs — upstream's "we don't need
	// to update the page, but we'd better still update the visibility map".
	applyPGRecord(t, mgr, buildHeapVisiblePG(t, rel, heapBlk, 55, storage.VMAllVisible), 200)

	vmRel := rel
	vmRel.Fork = storage.VisibilityMapFork
	if n, err := mgr.NBlocks(vmRel); err != nil || n != 2 {
		t.Fatalf("vm fork nblocks = %d (err %v), want 2 (zero-extended through vm block 1)", n, err)
	}
	if got := vmBitsAt(t, mgr, rel, heapBlk); got != storage.VMAllVisible {
		t.Fatalf("vm bits = %#x, want ALL_VISIBLE", got)
	}
	// vm block 0 must have been created empty, not filled with the same bits.
	if got := vmBitsAt(t, mgr, rel, 0); got != 0 {
		t.Fatalf("vm block 0 bits for heap block 0 = %#x, want 0", got)
	}
	if n, err := mgr.NBlocks(rel); err != nil || n != 0 {
		t.Fatalf("main fork nblocks = %d (err %v), want 0 — the absent heap page must not be extended", n, err)
	}
}

// TestApplyRecordPGHeapVisibleIdempotent replays the same record twice with a
// stale second LSN. The vm bits must survive: the second apply is a no-op
// because the bits already match, and it must not clear or re-stamp anything.
func TestApplyRecordPGHeapVisibleIdempotent(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4412, Fork: storage.MainFork}
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	seedHeapTuplePG(t, mgr, rel, 0, 42, "row", 100)

	framed := buildHeapVisiblePG(t, rel, 0, 55, storage.VMAllVisible)
	applyPGRecord(t, mgr, framed, 200)
	applyPGRecord(t, mgr, framed, 150)

	if got := vmBitsAt(t, mgr, rel, 0); got != storage.VMAllVisible {
		t.Fatalf("vm bits after re-apply = %#x, want ALL_VISIBLE", got)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, page); err != nil {
		t.Fatal(err)
	}
	if got := storage.MustHeader(page).LSN(); got != 200 {
		t.Fatalf("heap pd_lsn = %d, want 200 (the stale re-apply must not roll it back)", got)
	}
}

// TestApplyRecordPGHeapVisibleRefusals: a record whose flags carry a bit
// outside VISIBILITYMAP_XLOG_VALID_BITS was written by a PG whose map semantics
// goopg does not know, and a truncated main data cannot be trusted at all.
// Both must refuse rather than guess.
func TestApplyRecordPGHeapVisibleRefusals(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4413, Fork: storage.MainFork}

	t.Run("unknown_flag_bit", func(t *testing.T) {
		mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
		defer mgr.Close()
		seedHeapTuplePG(t, mgr, rel, 0, 42, "row", 100)

		err := applyPGRecordErr(t, mgr, buildHeapVisiblePG(t, rel, 0, 55, storage.VMAllVisible|0x40), 200)
		if err == nil || !strings.Contains(err.Error(), "unknown bits") {
			t.Fatalf("apply error = %v, want an unknown-flag-bit refusal", err)
		}
	})

	t.Run("truncated_main_data", func(t *testing.T) {
		mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
		defer mgr.Close()
		vmRel := rel
		vmRel.Fork = storage.VisibilityMapFork
		body, err := assembleXLogRecord(make([]byte, sizeOfXLogHeapVisibleData-1), []BlockRef{
			{ID: 0, Rel: vmRel, Block: 0},
			{ID: 1, Rel: rel, Block: 0},
		})
		if err != nil {
			t.Fatal(err)
		}
		err = applyPGRecordErr(t, mgr, framePGAssembled(RmgrHeap2, xlogHeap2Visible, 55, body), 200)
		if err == nil || !strings.Contains(err.Error(), "heap-visible main-data len") {
			t.Fatalf("apply error = %v, want a truncated-main-data refusal", err)
		}
	})

	t.Run("missing_heap_block_ref", func(t *testing.T) {
		mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
		defer mgr.Close()
		vmRel := rel
		vmRel.Fork = storage.VisibilityMapFork
		mainData := make([]byte, sizeOfXLogHeapVisibleData)
		mainData[4] = storage.VMAllVisible
		body, err := assembleXLogRecord(mainData, []BlockRef{{ID: 0, Rel: vmRel, Block: 0}})
		if err != nil {
			t.Fatal(err)
		}
		err = applyPGRecordErr(t, mgr, framePGAssembled(RmgrHeap2, xlogHeap2Visible, 55, body), 200)
		if err == nil || !strings.Contains(err.Error(), "missing block 1") {
			t.Fatalf("apply error = %v, want a missing-heap-block refusal", err)
		}
	})
}

// TestApplyRecordPGHeapLockClearsVMAllFrozen is part 2's deferral, discharged.
// A locker on an all-frozen page reports XLH_LOCK_ALL_FROZEN_CLEARED; redo has
// to clear ALL_FROZEN while LEAVING ALL_VISIBLE set, because the page is still
// all-visible — clearing both would only cost heap fetches, but clearing
// ALL_VISIBLE alone (leaving ALL_FROZEN) is the corrupt state upstream asserts
// against.
func TestApplyRecordPGHeapLockClearsVMAllFrozen(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4414, Fork: storage.MainFork}
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	seedHeapTuplePG(t, mgr, rel, 0, 42, "row", 100)

	applyPGRecord(t, mgr, buildHeapVisiblePG(t, rel, 0, 55, storage.VMAllVisible|storage.VMAllFrozen), 200)
	if got := vmBitsAt(t, mgr, rel, 0); got != storage.VMValidBits {
		t.Fatalf("setup: vm bits = %#x, want both bits", got)
	}

	framed := buildHeapLockPG(t, rel, 0, 56, 99, 1, xlhlXmaxLockOnly|xlhlXmaxExclLock, xlhLockAllFrozenCleared)
	applyPGRecord(t, mgr, framed, 300)

	if got := vmBitsAt(t, mgr, rel, 0); got != storage.VMAllVisible {
		t.Fatalf("vm bits = %#x, want ALL_VISIBLE only (ALL_FROZEN cleared, ALL_VISIBLE kept)", got)
	}
}

// TestApplyRecordPGHeapLockVMClearRunsDespiteLSNInterlock pins upstream's
// ordering: the vm clear happens BEFORE and independently of the heap-page
// redo, "even if the heap page is already up-to-date". A record whose heap page
// is already past its LSN still has to fix the map, or an index-only scan keeps
// trusting an all-frozen bit for a page that now carries a live locker.
func TestApplyRecordPGHeapLockVMClearRunsDespiteLSNInterlock(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4415, Fork: storage.MainFork}
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	seedHeapTuplePG(t, mgr, rel, 0, 42, "row", 100)
	applyPGRecord(t, mgr, buildHeapVisiblePG(t, rel, 0, 55, storage.VMAllVisible|storage.VMAllFrozen), 400)

	// endLSN 300 < the page's 400, so the tuple stamp is skipped.
	framed := buildHeapLockPG(t, rel, 0, 56, 99, 1, xlhlXmaxLockOnly|xlhlXmaxExclLock, xlhLockAllFrozenCleared)
	applyPGRecord(t, mgr, framed, 300)

	if _, tup := readTupleAt(t, mgr, rel, 0, 1); tup.Header.Xmax != 0 {
		t.Fatalf("t_xmax = %d, want 0 — the pd_lsn interlock should have skipped the tuple stamp", tup.Header.Xmax)
	}
	if got := vmBitsAt(t, mgr, rel, 0); got != storage.VMAllVisible {
		t.Fatalf("vm bits = %#x, want ALL_FROZEN cleared even though the heap page was skipped", got)
	}
}

// TestApplyRecordPGHeapLockVMClearSkipsAbsentFork guards the one deliberate
// deviation from visibilitymap_pin: a relation with no vm fork at all must not
// grow one just because a lock record says it cleared a bit. The bits there are
// already zero, and materialising an empty map page would invent content the
// primary may never have had.
func TestApplyRecordPGHeapLockVMClearSkipsAbsentFork(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4416, Fork: storage.MainFork}
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	seedHeapTuplePG(t, mgr, rel, 0, 42, "row", 100)

	framed := buildHeapLockPG(t, rel, 0, 56, 99, 1, xlhlXmaxLockOnly|xlhlXmaxExclLock, xlhLockAllFrozenCleared)
	applyPGRecord(t, mgr, framed, 300)

	vmRel := rel
	vmRel.Fork = storage.VisibilityMapFork
	if n, err := mgr.NBlocks(vmRel); err != nil || n != 0 {
		t.Fatalf("vm fork nblocks = %d (err %v), want 0 — a clear must not create the fork", n, err)
	}
	if _, tup := readTupleAt(t, mgr, rel, 0, 1); tup.Header.Xmax != 99 {
		t.Fatalf("t_xmax = %d, want 99 — the lock itself must still have applied", tup.Header.Xmax)
	}
}
