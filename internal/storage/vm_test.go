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
