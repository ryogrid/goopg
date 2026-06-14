package amcheck

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// The unit tests in verify_heapam_test.go validate the corruption *detection*
// paths by hand-injecting damage (via setItemID and direct byte pokes) — the
// only way to produce corruption, since the clean storage API refuses to write
// it. But that leaves a complementary risk uncovered: does the engine stay
// SILENT on a page produced by goopg's *real* on-disk mutators? If the engine's
// page-byte assumptions (CTID layout at off+12, HOT/heap-only bits in
// t_infomask, natts in t_infomask2, LP_UNUSED/LP_REDIRECT handling) diverge
// from what storage.PageStampHotOldTuple / PageSetItemIDRedirect / VacuumHeapPage
// / NewHeapTupleWithNulls actually write, the eventual verify_heapam() SRF would
// report corruption on perfectly healthy user tables — the most expensive class
// of compatibility bug in this project.
//
// These tests therefore drive the *real* producers (mirroring the executor's
// HOT-update path in operators_storage.go:tryApplyHOTUpdate and the vacuum
// kernel) and assert the engine finds nothing. They are the false-positive
// guard that the per-page corruption-injection tests cannot be.

// addRealHotSuccessor inserts a HOT-only successor tuple exactly as the executor
// does in tryApplyHOTUpdate: a fresh tuple with xmin = the updating xid, the
// HeapOnlyTuple bit set, natts stamped, and xmax marked invalid. Returns the
// 1-based slot.
func addRealHotSuccessor(t *testing.T, p storage.Page, xid storage.TransactionID, dataLen int) uint16 {
	t.Helper()
	tup := storage.NewHeapTuple(xid, storage.InvalidTransactionID, make([]byte, dataLen))
	tup.Header.Infomask |= storage.HeapOnlyTuple
	tup.Header.SetNatts(1)
	tup.Header.Infomask |= storage.HeapXmaxInvalid
	slot, err := storage.PageAddHeapTuple(p, tup)
	if err != nil {
		t.Fatalf("PageAddHeapTuple(successor): %v", err)
	}
	return slot
}

// buildRealHotChainPage builds a healthy same-page HOT update chain through the
// real storage mutators: a root tuple (with a live index entry) stamped to point
// at a heap-only successor, exactly as a same-page UPDATE produces on disk.
// Returns the page plus the root and successor slots.
func buildRealHotChainPage(t *testing.T) (storage.Page, uint16, uint16) {
	t.Helper()
	const updXID = storage.TransactionID(200)
	p := newPage(t)
	rootSlot := addCleanTuple(t, p, 16)               // root: xmin=100, xmax=0, LP_NORMAL
	succSlot := addRealHotSuccessor(t, p, updXID, 16) // heap-only successor: xmin=200
	// Stamp the root: xmax=updXID, CTID=(blk 0, succSlot), HEAP_HOT_UPDATED.
	// blk 0 because the chain is same-page; the engine reads CTID.block at
	// off+12 and only links within the page being checked.
	if err := storage.PageStampHotOldTuple(p, rootSlot, updXID, 0, succSlot); err != nil {
		t.Fatalf("PageStampHotOldTuple: %v", err)
	}
	return p, rootSlot, succSlot
}

// A HOT update chain produced by goopg's real mutators is structurally clean:
// root.xmax == successor.xmin forms the chain link, the root is HOT-updated and
// non-heap-only, and the successor is heap-only — so the update-chain checker
// must find nothing. A false positive here would flag every HOT-updated row.
func TestVerifyHeapPage_RealHotChain_NoFalsePositive(t *testing.T) {
	p, _, _ := buildRealHotChainPage(t)

	reports, err := VerifyHeapPage(p, 0)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("real HOT chain falsely reported %d corruptions: %+v", len(reports), reports)
	}

	// Same page, now with the relation-natts tier engaged (natts=1 matches the
	// tuples) — still clean.
	reports, err = VerifyHeapPageWithRel(p, 0, RelDesc{Natts: 1})
	if err != nil {
		t.Fatalf("VerifyHeapPageWithRel: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("real HOT chain (with RelDesc) falsely reported %d corruptions: %+v", len(reports), reports)
	}
}

// The clog-dependent HOT-chain tier (root xmin committed, link xmin commit
// status) must also stay silent on the real chain: a verifier that says the
// updating transaction committed should not turn a healthy chain into a finding.
func TestVerifyHeapPage_RealHotChain_WithXidStatus_NoFalsePositive(t *testing.T) {
	p, _, _ := buildRealHotChainPage(t)
	// Both the root's xmin (100) and the successor's xmin / link xid (200) are
	// committed in this healthy table.
	xidStatus := func(uint32) XidCommitStatus { return XidStatusCommitted }

	reports, err := VerifyHeapPageWithXminStatus(p, 0, RelDesc{Natts: 1}, xidStatus)
	if err != nil {
		t.Fatalf("VerifyHeapPageWithXminStatus: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("real HOT chain (clog tier) falsely reported %d corruptions: %+v", len(reports), reports)
	}
}

// After opportunistic pruning frees a dead HOT-chain root, storage rewrites the
// root line pointer as an LP_REDIRECT to the live tip via PageSetItemIDRedirect.
// The engine requires a redirect to target a heap-only tuple; the real pruner
// always redirects to the heap-only successor, so this must stay clean.
func TestVerifyHeapPage_RealRedirectAfterPrune_NoFalsePositive(t *testing.T) {
	p, rootSlot, succSlot := buildRealHotChainPage(t)
	if err := storage.PageSetItemIDRedirect(p, rootSlot, succSlot); err != nil {
		t.Fatalf("PageSetItemIDRedirect: %v", err)
	}

	reports, err := VerifyHeapPage(p, 0)
	if err != nil {
		t.Fatalf("VerifyHeapPage: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("real post-prune redirect falsely reported %d corruptions: %+v", len(reports), reports)
	}
}

// A page pruned by goopg's real vacuum kernel (VacuumHeapPage) carries LP_UNUSED
// line pointers for the reclaimed slots and repacked live tuples. The engine
// must skip the unused slots and accept the repacked survivors, reporting
// nothing on a healthy vacuumed page.
func TestVerifyHeapPage_RealVacuumedPage_NoFalsePositive(t *testing.T) {
	p := newPage(t)
	// Lay down five plain tuples; mark the even-xmin ones dead so vacuum
	// reclaims a non-trivial, non-contiguous subset.
	for i := 0; i < 5; i++ {
		tup := storage.NewHeapTuple(storage.TransactionID(100+i), 0, make([]byte, 8+i))
		tup.Header.SetNatts(1)
		if _, err := storage.PageAddHeapTuple(p, tup); err != nil {
			t.Fatalf("PageAddHeapTuple(%d): %v", i, err)
		}
	}
	isDead := func(h storage.HeapTupleHeader) bool { return h.Xmin%2 == 0 }
	stats, err := storage.VacuumHeapPage(p, isDead)
	if err != nil {
		t.Fatalf("VacuumHeapPage: %v", err)
	}
	if stats.Dead == 0 {
		t.Fatalf("vacuum reclaimed nothing; test would not exercise LP_UNUSED")
	}

	reports, err := VerifyHeapPageWithRel(p, 0, RelDesc{Natts: 1})
	if err != nil {
		t.Fatalf("VerifyHeapPageWithRel: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("real vacuumed page falsely reported %d corruptions: %+v", len(reports), reports)
	}
}

// A nullable multi-attribute tuple written via NewHeapTupleWithNulls carries a
// null bitmap embedded in t_hoff and HEAP_HASNULL set — the on-disk shape the
// executor produces for rows with NULLs. The engine decodes t_hoff and natts
// page-structurally; a divergence in either would false-positive here. natts in
// RelDesc matches the writer's SetNatts so the relation-natts tier is exercised.
func TestVerifyHeapPage_RealNullableMultiAttr_NoFalsePositive(t *testing.T) {
	const natts = 5
	p := newPage(t)
	// Bitmap covers 5 attributes (1 byte); mark attribute 3 NULL (bit 2 clear),
	// all others present — a realistic mixed-null row.
	bitmap := []byte{0b00011011} // attrs 1,2,4,5 present; attr 3 null
	tup := storage.NewHeapTupleWithNulls(300, storage.InvalidTransactionID, bitmap, make([]byte, 24))
	tup.Header.SetNatts(natts)
	tup.Header.Infomask |= storage.HeapHasNull
	tup.Header.Infomask |= storage.HeapXmaxInvalid
	if _, err := storage.PageAddHeapTuple(p, tup); err != nil {
		t.Fatalf("PageAddHeapTuple(nullable): %v", err)
	}

	reports, err := VerifyHeapPageWithRel(p, 0, RelDesc{Natts: natts})
	if err != nil {
		t.Fatalf("VerifyHeapPageWithRel: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("real nullable multi-attr tuple falsely reported %d corruptions: %+v", len(reports), reports)
	}
}

// The whole-relation driver over a mix of real pages — a clean page, a real HOT
// chain, and a real vacuumed page — must likewise yield zero findings, proving
// the SRF block-walk stays silent end-to-end on healthy goopg storage.
func TestVerifyHeapRelation_RealPages_NoFalsePositive(t *testing.T) {
	clean := newPage(t)
	addCleanTuple(t, clean, 16)

	hot, _, _ := buildRealHotChainPage(t)

	vac := newPage(t)
	for i := 0; i < 4; i++ {
		tup := storage.NewHeapTuple(storage.TransactionID(100+i), 0, make([]byte, 8))
		tup.Header.SetNatts(1)
		if _, err := storage.PageAddHeapTuple(vac, tup); err != nil {
			t.Fatalf("PageAddHeapTuple(vac %d): %v", i, err)
		}
	}
	if _, err := storage.VacuumHeapPage(vac, func(h storage.HeapTupleHeader) bool { return h.Xmin == 101 }); err != nil {
		t.Fatalf("VacuumHeapPage: %v", err)
	}

	pages := map[storage.BlockNumber]storage.Page{0: clean, 1: hot, 2: vac}
	reports, err := VerifyHeapRelation(heapMapSource(pages), 3, HeapRelOptions{Rel: RelDesc{Natts: 1}})
	if err != nil {
		t.Fatalf("VerifyHeapRelation: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("real-pages relation walk falsely reported %d corruptions: %+v", len(reports), reports)
	}
}
