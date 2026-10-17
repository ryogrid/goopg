package storage

import (
	"testing"
)

// TestVMSetAllVisible verifies the basic set/check/clear cycle.
func TestVMSetAllVisible(t *testing.T) {
	vm := NewVisibilityMap()
	rel := RelFileNode{DBOid: 1, RelOid: 10, Fork: MainFork}

	if vm.AllVisible(rel, 0) {
		t.Fatal("expected false before any SetAllVisible call")
	}
	vm.SetAllVisible(rel, 0)
	if !vm.AllVisible(rel, 0) {
		t.Fatal("expected true after SetAllVisible")
	}
	vm.SetAllVisible(rel, 5) // non-contiguous block
	if !vm.AllVisible(rel, 5) {
		t.Fatal("expected block 5 to be ALL_VISIBLE")
	}
	if vm.AllVisible(rel, 3) {
		t.Fatal("block 3 should not be ALL_VISIBLE (never set)")
	}
}

// TestVMClearBlock verifies that ClearBlock resets the bit.
func TestVMClearBlock(t *testing.T) {
	vm := NewVisibilityMap()
	rel := RelFileNode{DBOid: 1, RelOid: 11}
	vm.SetAllVisible(rel, 2)
	vm.ClearBlock(rel, 2)
	if vm.AllVisible(rel, 2) {
		t.Fatal("expected false after ClearBlock")
	}
	// Clear on never-set block is a no-op (must not panic).
	vm.ClearBlock(rel, 99)
}

// TestVMDropRelation verifies that DropRelation removes all entries.
func TestVMDropRelation(t *testing.T) {
	vm := NewVisibilityMap()
	rel := RelFileNode{DBOid: 1, RelOid: 12}
	vm.SetAllVisible(rel, 0)
	vm.SetAllVisible(rel, 1)
	vm.DropRelation(rel)
	if vm.AllVisible(rel, 0) || vm.AllVisible(rel, 1) {
		t.Fatal("expected all entries cleared after DropRelation")
	}
}

// TestVMNilSafe verifies that all methods are nil-safe.
func TestVMNilSafe(t *testing.T) {
	var vm *VisibilityMap
	rel := RelFileNode{DBOid: 1, RelOid: 1}
	if vm.AllVisible(rel, 0) {
		t.Fatal("nil VM must return false")
	}
	vm.SetAllVisible(rel, 0)  // must not panic
	vm.ClearBlock(rel, 0)     // must not panic
	vm.DropRelation(rel)      // must not panic
}

// TestPageAllVisible verifies the page-level ALL_VISIBLE check.
func TestPageAllVisible(t *testing.T) {
	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatal(err)
	}
	horizon := TransactionID(100)

	// Empty page → ALL_VISIBLE (nothing to invalidate it).
	if !PageAllVisible(page, horizon) {
		t.Fatal("empty page should be ALL_VISIBLE")
	}

	// Tuple with committed xmin < horizon and no xmax → visible.
	tup := NewHeapTuple(50, InvalidTransactionID, []byte("data"))
	if _, err := PageAddHeapTuple(page, tup); err != nil {
		t.Fatal(err)
	}
	if !PageAllVisible(page, horizon) {
		t.Fatal("page with committed xmin < horizon should be ALL_VISIBLE")
	}

	// Tuple with xmin >= horizon → not universally visible yet.
	tup2 := NewHeapTuple(150, InvalidTransactionID, []byte("future"))
	if _, err := PageAddHeapTuple(page, tup2); err != nil {
		t.Fatal(err)
	}
	if PageAllVisible(page, horizon) {
		t.Fatal("page with xmin >= horizon should NOT be ALL_VISIBLE")
	}
}

// TestPageAllVisibleDeadTuple verifies that a tuple with xmax set
// (deleted but not yet vacuumed) prevents ALL_VISIBLE.
func TestPageAllVisibleDeadTuple(t *testing.T) {
	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatal(err)
	}
	horizon := TransactionID(100)

	tup := NewHeapTuple(50, 80, []byte("deleted")) // xmax=80 < horizon
	if _, err := PageAddHeapTuple(page, tup); err != nil {
		t.Fatal(err)
	}
	if PageAllVisible(page, horizon) {
		t.Fatal("page with deleted (xmax set) tuple should NOT be ALL_VISIBLE")
	}
}

// TestVMTruncateRel pins the in-memory half of M0146-0206: bits for heap
// blocks at or beyond the truncation point are forgotten, kept blocks
// retain theirs, and a nil receiver stays a no-op.
func TestVMTruncateRel(t *testing.T) {
	vm := NewVisibilityMap()
	rel := RelFileNode{DBOid: 1, RelOid: 13}
	for b := BlockNumber(0); b < 10; b++ {
		vm.SetAllVisible(rel, b)
	}
	vm.TruncateRel(rel, 4)
	for b := BlockNumber(0); b < 4; b++ {
		if !vm.AllVisible(rel, b) {
			t.Fatalf("kept block %d lost ALL_VISIBLE", b)
		}
	}
	for b := BlockNumber(4); b < 10; b++ {
		if vm.AllVisible(rel, b) {
			t.Fatalf("truncated block %d still ALL_VISIBLE", b)
		}
	}
	// Truncating beyond the recorded end and truncating a never-seen
	// relation are no-ops.
	vm.TruncateRel(rel, 64)
	vm.TruncateRel(RelFileNode{DBOid: 1, RelOid: 9999}, 3)

	var nilVM *VisibilityMap
	nilVM.TruncateRel(rel, 1) // must not panic
}

// TestVMTruncateForkForHeap exercises the smgr_redo SMGR_TRUNCATE_VM arm:
// the _vm fork shrinks to the pages covering the kept heap blocks and the
// surviving map page's tail bits are cleared — visibilitymap_prepare_truncate.
func TestVMTruncateForkForHeap(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(ManagerConfig{DataDir: dir})
	defer m.Close()
	heapRel := RelFileNode{DBOid: 1, RelOid: 7000, Fork: MainFork}
	vmRel := RelFileNode{DBOid: 1, RelOid: 7000, Fork: VisibilityMapFork}

	// Two map pages fully set.
	page := make([]byte, BlockSize)
	for i := vmDataOffset; i < len(page); i++ {
		page[i] = 0xFF
	}
	for blk := 0; blk < 2; blk++ {
		if _, err := m.Extend(vmRel, page); err != nil {
			t.Fatalf("Extend: %v", err)
		}
	}

	// Keep heap blocks [0, VMHeapBlocksPerPage+5): the truncation lands in
	// map page 1, byte 1, offset 1.
	heapN := BlockNumber(VMHeapBlocksPerPage + 5)
	if err := VMTruncateForkForHeap(m, heapRel, heapN); err != nil {
		t.Fatalf("VMTruncateForkForHeap: %v", err)
	}
	if n, err := m.NBlocks(vmRel); err != nil || n != 2 {
		t.Fatalf("vm fork nblocks=%d (err=%v), want 2", n, err)
	}
	got := make([]byte, BlockSize)
	if err := m.ReadBlock(vmRel, 1, got); err != nil {
		t.Fatalf("ReadBlock: %v", err)
	}
	data := got[vmDataOffset:]
	if data[0] != 0xFF {
		t.Fatalf("map byte 0 = %#02x, want 0xff (fully kept)", data[0])
	}
	if data[1] != 0x01 {
		t.Fatalf("map byte 1 = %#02x, want 0x01 (only bit 0 survives)", data[1])
	}
	for i := 2; i < len(data); i++ {
		if data[i] != 0 {
			t.Fatalf("map byte %d = %#02x, want 0", i, data[i])
		}
	}

	// Idempotent replay: a second call must not fail.
	if err := VMTruncateForkForHeap(m, heapRel, heapN); err != nil {
		t.Fatalf("second VMTruncateForkForHeap: %v", err)
	}

	// A heap truncation aligned to a map-page boundary drops the page
	// outright — no tail masking needed.
	if err := VMTruncateForkForHeap(m, heapRel, VMHeapBlocksPerPage); err != nil {
		t.Fatalf("boundary VMTruncateForkForHeap: %v", err)
	}
	if n, _ := m.NBlocks(vmRel); n != 1 {
		t.Fatalf("boundary-truncated vm fork nblocks=%d, want 1", n)
	}

	// Missing fork is a no-op.
	miss := RelFileNode{DBOid: 1, RelOid: 4242, Fork: MainFork}
	if err := VMTruncateForkForHeap(m, miss, 3); err != nil {
		t.Fatalf("missing-fork VMTruncateForkForHeap: %v", err)
	}
}
