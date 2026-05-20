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


// TestFSMGetCandidatesBasic verifies that GetCandidates returns the
// top-N pages by free space descending, restricted to entries that
// meet the minFreeBytes floor. (M0107-0007 slice C foundation.)
func TestFSMGetCandidatesBasic(t *testing.T) {
	fsm := NewFSM()
	rel := RelFileNode{DBOid: 1, RelOid: 42, Fork: MainFork}

	fsm.RecordFreeSpace(rel, 0, 4000)
	fsm.RecordFreeSpace(rel, 1, 200)
	fsm.RecordFreeSpace(rel, 2, 7000)
	fsm.RecordFreeSpace(rel, 3, 5500)
	fsm.RecordFreeSpace(rel, 4, 100)

	// Ask for top-3 with floor 1000: blocks 2 (7000) > 3 (5500) > 0 (4000).
	got := fsm.GetCandidates(rel, 1000, 3)
	want := []BlockNumber{2, 3, 0}
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, want %d (got=%v)", len(got), len(want), got)
	}
	for i, b := range want {
		if got[i] != b {
			t.Errorf("got[%d] = %d, want %d (full=%v)", i, got[i], b, got)
		}
	}

	// Asking for top-10 returns only the qualifying entries (3 of them).
	got = fsm.GetCandidates(rel, 1000, 10)
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3 (got=%v)", len(got), got)
	}

	// Raising the floor above all entries → nil.
	got = fsm.GetCandidates(rel, 8000, 4)
	if got != nil {
		t.Fatalf("expected nil for impossibly-high floor, got %v", got)
	}
}

// TestFSMGetCandidatesEdgeCases covers nil receiver, zero arguments,
// empty relation, and the tie-breaking contract (lowest block number
// wins among equal free-space estimates).
func TestFSMGetCandidatesEdgeCases(t *testing.T) {
	var nilFSM *FSM
	rel := RelFileNode{DBOid: 1, RelOid: 1}
	if got := nilFSM.GetCandidates(rel, 100, 4); got != nil {
		t.Fatalf("nil FSM should return nil, got %v", got)
	}

	fsm := NewFSM()
	// Empty relation → nil regardless of arguments.
	if got := fsm.GetCandidates(rel, 100, 4); got != nil {
		t.Fatalf("empty FSM should return nil, got %v", got)
	}

	fsm.RecordFreeSpace(rel, 0, 4000)
	// n <= 0 → nil.
	if got := fsm.GetCandidates(rel, 100, 0); got != nil {
		t.Fatalf("n=0 should return nil, got %v", got)
	}
	if got := fsm.GetCandidates(rel, 100, -1); got != nil {
		t.Fatalf("n<0 should return nil, got %v", got)
	}
	// minFreeBytes == 0 → nil (mirrors GetPageWithFreeSpace contract).
	if got := fsm.GetCandidates(rel, 0, 4); got != nil {
		t.Fatalf("minFreeBytes=0 should return nil, got %v", got)
	}

	// Tie-breaking: equal free-space estimates → ascending block number.
	rel2 := RelFileNode{DBOid: 1, RelOid: 2}
	fsm.RecordFreeSpace(rel2, 0, 3000)
	fsm.RecordFreeSpace(rel2, 1, 3000)
	fsm.RecordFreeSpace(rel2, 2, 3000)
	got := fsm.GetCandidates(rel2, 1000, 3)
	want := []BlockNumber{0, 1, 2}
	for i, b := range want {
		if i >= len(got) || got[i] != b {
			t.Errorf("tie-break got[%d] = %v, want %v (full=%v)", i, got, want, got)
		}
	}
}

// TestFSMGetCandidatesLargeRelation exercises the insertion-sort window
// on a relation big enough to stress the O(N) scan with a small N.
// Confirms top-K extraction is correct over 1000 randomized free-space
// values when the kept set is constrained to 4.
func TestFSMGetCandidatesLargeRelation(t *testing.T) {
	fsm := NewFSM()
	rel := RelFileNode{DBOid: 1, RelOid: 99}

	// Deterministic pseudo-random distribution: block i gets free-space
	// (i * 37) % 8000. Block 217 yields the maximum (8029 % 8000 = 29
	// — actually 217*37 = 8029, % 8000 = 29; tiny. Let's pick a series
	// that has known top entries.) Simpler: assign by formula such that
	// the maximum is at a known block.
	const N = 1000
	for i := 0; i < N; i++ {
		// (i * 7919) % 7000 is uniformly distributed across [0, 7000)
		// with a peak at i=0 (val 0), so add an explicit big-value
		// outlier so we can assert top-1.
		free := uint16((i * 7919) % 7000)
		fsm.RecordFreeSpace(rel, BlockNumber(i), free)
	}
	// Inject known top-4: blocks 950, 951, 952, 953 with 7990, 7980, 7970, 7960.
	fsm.RecordFreeSpace(rel, 950, 7990)
	fsm.RecordFreeSpace(rel, 951, 7980)
	fsm.RecordFreeSpace(rel, 952, 7970)
	fsm.RecordFreeSpace(rel, 953, 7960)

	got := fsm.GetCandidates(rel, 5000, 4)
	want := []BlockNumber{950, 951, 952, 953}
	if len(got) != 4 {
		t.Fatalf("expected top-4, got %v (len=%d)", got, len(got))
	}
	for i, b := range want {
		if got[i] != b {
			t.Errorf("got[%d] = %d, want %d (full=%v)", i, got[i], b, got)
		}
	}

	// minFreeBytes filter: top-2 above 7975 should be {950, 951}.
	got = fsm.GetCandidates(rel, 7975, 8)
	if len(got) != 2 {
		t.Fatalf("expected 2 above-7975 candidates, got %v", got)
	}
	if got[0] != 950 || got[1] != 951 {
		t.Errorf("expected [950 951], got %v", got)
	}
}

// TestFSMGetCandidatesDoesNotMutateState confirms the read path leaves
// the FSM map unchanged — important because the method runs under
// RLock and a concurrent writer should not observe any side effects.
func TestFSMGetCandidatesDoesNotMutateState(t *testing.T) {
	fsm := NewFSM()
	rel := RelFileNode{DBOid: 1, RelOid: 7}
	fsm.RecordFreeSpace(rel, 0, 4000)
	fsm.RecordFreeSpace(rel, 1, 6000)

	_ = fsm.GetCandidates(rel, 1000, 4)

	// Original entries should still be there.
	if blk, ok := fsm.GetPageWithFreeSpace(rel, 5500); !ok || blk != 1 {
		t.Fatalf("expected block 1 still recorded after GetCandidates, got (%d, %v)", blk, ok)
	}
}
