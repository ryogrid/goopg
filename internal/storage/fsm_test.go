package storage

import (
	"testing"
)

// TestFSMBasic verifies that RecordFreeSpace and GetPageWithFreeSpace
// round-trip correctly.
func TestFSMBasic(t *testing.T) {
	fsm := NewFSM()
	rel := RelFileNode{DBOid: 1, RelOid: 42, Fork: MainFork}

	// No entry yet → not found.
	if _, ok := fsm.GetPageWithFreeSpace(rel, 100); ok {
		t.Fatal("expected no page before any record")
	}

	fsm.RecordFreeSpace(rel, 0, 4000)
	fsm.RecordFreeSpace(rel, 1, 200)
	fsm.RecordFreeSpace(rel, 2, 7000)

	// Ask for ≥ 5000 free bytes: only block 2 qualifies.
	blk, ok := fsm.GetPageWithFreeSpace(rel, 5000)
	if !ok {
		t.Fatal("expected block 2 with 7000 free bytes")
	}
	if blk != 2 {
		t.Errorf("expected block 2, got %d", blk)
	}

	// Ask for ≥ 3000: block 0 (4000) qualifies first.
	blk, ok = fsm.GetPageWithFreeSpace(rel, 3000)
	if !ok {
		t.Fatal("expected a block with >= 3000 free bytes")
	}
	if blk != 0 {
		t.Errorf("expected block 0, got %d", blk)
	}

	// Mark block 0 as full.
	fsm.RecordFreeSpace(rel, 0, 0)
	blk, ok = fsm.GetPageWithFreeSpace(rel, 3000)
	if !ok {
		t.Fatal("expected block 2 after block 0 invalidated")
	}
	if blk != 2 {
		t.Errorf("expected block 2, got %d", blk)
	}
}

// TestFSMDropRelation verifies that DropRelation clears all entries for a
// relation so stale pages are never returned after TRUNCATE / DROP TABLE.
func TestFSMDropRelation(t *testing.T) {
	fsm := NewFSM()
	rel := RelFileNode{DBOid: 1, RelOid: 99, Fork: MainFork}
	fsm.RecordFreeSpace(rel, 0, 8000)
	fsm.DropRelation(rel)
	if _, ok := fsm.GetPageWithFreeSpace(rel, 100); ok {
		t.Fatal("expected no pages after DropRelation")
	}
}

// TestFSMRecordFreeSpaceForPage exercises the convenience method that reads
// free space directly from a page's header.
func TestFSMRecordFreeSpaceForPage(t *testing.T) {
	fsm := NewFSM()
	rel := RelFileNode{DBOid: 1, RelOid: 7, Fork: MainFork}

	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatal(err)
	}
	// Fresh page has ~8168 bytes free.
	fsm.RecordFreeSpaceForPage(rel, 0, page)
	blk, ok := fsm.GetPageWithFreeSpace(rel, 8000)
	if !ok {
		t.Fatal("expected large free space on a fresh page")
	}
	if blk != 0 {
		t.Errorf("expected block 0, got %d", blk)
	}
}

// TestFSMNilSafe verifies that a nil FSM silently no-ops on all methods.
func TestFSMNilSafe(t *testing.T) {
	var fsm *FSM
	rel := RelFileNode{DBOid: 1, RelOid: 1}
	fsm.RecordFreeSpace(rel, 0, 1000) // must not panic
	if _, ok := fsm.GetPageWithFreeSpace(rel, 100); ok {
		t.Fatal("nil FSM should never return a page")
	}
	fsm.DropRelation(rel) // must not panic
}
