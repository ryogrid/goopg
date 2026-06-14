package amcheck_test

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/amcheck"
	"github.com/goopg/goopg/internal/storage"
)

// The real-producer B-tree validation in verify_nbtree_realtree_test.go drives
// the engine over trees built by Create/Insert/split. It deliberately does NOT
// exercise the OTHER load-bearing on-disk mutator: page DELETION via
// btree.VacuumIndexPages. Page deletion is where btpo_prev/btpo_next and the
// parent downlink array are rewritten in place — exactly the structure the
// cross-page tiers (VerifyBtreeLevelSiblingLinks, VerifyBtreeParentDownlinks)
// inspect. That path's split sibling, splitAndInsert, hid a real stale-prev-link
// bug that this same real-producer harness surfaced (M0110-0007); the deletion
// path (unlinkEmptyLeaf relinks the left sibling's Next, the right sibling's
// Prev, removes the parent downlink, and flags the leaf BTDeleted) is its mirror
// image and had no equivalent end-to-end check that goopg's real output is
// amcheck-clean.
//
// These tests build a live multi-level B-tree, empty one or more INTERIOR leaves
// (leaves that have both a left and a right sibling, so the unlink must relink
// neighbours on both sides and delete a non-leftmost parent downlink), run the
// real VacuumIndexPages, and assert every engine tier — per-page structure,
// item order, cross-level downlinks, the cross-page sibling-link walk, and the
// heapallindexed round-trip — stays SILENT over the post-deletion on-disk pages.
// A finding here would be a genuine page-deletion corruption (deleted page still
// reachable via a sibling link or a parent downlink, a survivor's btpo_prev left
// dangling at a recycled block, etc.), the most expensive class of bug in this
// project. Silence validates that the deletion machinery and the engine's
// deleted-page exemptions agree on goopg's real layout.

// buildRealTreeHandle is buildRealTree but also returns the live *btree.BTree so
// the caller can drive VacuumIndexPages. Keys map to distinct, monotonically
// derived heap TIDs (same scheme as buildRealTree) so the heapallindexed
// round-trip can distinguish entries and the caller can reconstruct the TID of
// any inserted key.
func buildRealTreeHandle(t *testing.T, keys [][]byte) (*storage.Manager, *storage.Pool, storage.RelFileNode, *btree.BTree, func()) {
	t.Helper()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 256})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	rel := storage.RelFileNode{DBOid: 1, RelOid: 9200, Fork: storage.MainFork}
	bt, err := btree.Create(pool, rel)
	if err != nil {
		_ = pool.Close()
		_ = mgr.Close()
		t.Fatalf("btree.Create: %v", err)
	}
	for i, k := range keys {
		ptr := storage.ItemPointer{Block: storage.BlockNumber(i/100 + 1), Offset: uint16(i%100 + 1)}
		if err := bt.Insert(k, ptr); err != nil {
			_ = pool.Close()
			_ = mgr.Close()
			t.Fatalf("Insert(#%d): %v", i, err)
		}
	}
	cleanup := func() {
		_ = pool.Close()
		_ = mgr.Close()
	}
	return mgr, pool, rel, bt, cleanup
}

// leafChainBlocks returns the leaf block numbers in left-to-right order by
// descending to the leftmost leaf and following btpo_next. Deleted leaves are
// already unlinked from this chain, so before any deletion it enumerates exactly
// the live leaves.
func leafChainBlocks(t *testing.T, src amcheck.PageSource, root storage.BlockNumber) []storage.BlockNumber {
	t.Helper()
	lm := leftmostByLevel(t, src, root)
	cur := lm[len(lm)-1] // last entry is the leftmost leaf
	var out []storage.BlockNumber
	seen := make(map[storage.BlockNumber]bool)
	for cur != storage.InvalidBlockNumber {
		if seen[cur] {
			t.Fatalf("cycle in leaf chain at block %d", cur)
		}
		seen[cur] = true
		out = append(out, cur)
		p, err := src(cur)
		if err != nil {
			t.Fatalf("read leaf %d: %v", cur, err)
		}
		op := btree.ParseOpaque(p)
		if !op.IsLeaf() {
			t.Fatalf("block %d on leaf chain is not a leaf", cur)
		}
		cur = op.Next
	}
	return out
}

// leafTIDs returns every heap TID stored on the given leaf block.
func leafTIDs(t *testing.T, src amcheck.PageSource, blk storage.BlockNumber) []storage.ItemPointer {
	t.Helper()
	p, err := src(blk)
	if err != nil {
		t.Fatalf("read leaf %d: %v", blk, err)
	}
	entries, err := btree.PageLeafEntries(p)
	if err != nil {
		t.Fatalf("PageLeafEntries(%d): %v", blk, err)
	}
	tids := make([]storage.ItemPointer, 0, len(entries))
	for _, e := range entries {
		tids = append(tids, e.TID)
	}
	return tids
}

// rootOf reads the metapage and returns the current root block.
func rootOf(t *testing.T, src amcheck.PageSource) storage.BlockNumber {
	t.Helper()
	p, err := src(btree.MetaBlock)
	if err != nil {
		t.Fatalf("read metapage: %v", err)
	}
	return btree.ParseMeta(p).Root
}

// assertLeafDeleted asserts the given leaf block is now flagged deleted (unlinked
// from the tree by VacuumIndexPages) — proving the test actually exercised the
// deletion path rather than a no-op vacuum.
func assertLeafDeleted(t *testing.T, src amcheck.PageSource, blk storage.BlockNumber) {
	t.Helper()
	p, err := src(blk)
	if err != nil {
		t.Fatalf("read deleted leaf %d: %v", blk, err)
	}
	if !btree.ParseOpaque(p).IsDeleted() {
		t.Fatalf("leaf %d was expected to be deleted after VacuumIndexPages but is still live", blk)
	}
}

// TestVerifyBtreeEngineSilentAfterInteriorLeafDeletion empties a single INTERIOR
// leaf (one with both a left and a right sibling) and asserts the whole engine
// stays silent over the post-deletion tree. This exercises unlinkEmptyLeaf's
// three-way relink: left sibling Next -> right sibling, right sibling Prev ->
// left sibling, and removal of the leaf's (non-leftmost) parent downlink.
func TestVerifyBtreeEngineSilentAfterInteriorLeafDeletion(t *testing.T) {
	const n = 3000
	keys := make([][]byte, n)
	for i := range n {
		keys[i] = btree.EncodeInt4(int32(i))
	}
	mgr, pool, rel, bt, cleanup := buildRealTreeHandle(t, keys)
	defer cleanup()
	src := realPageSource(t, pool, rel)

	leaves := leafChainBlocks(t, src, rootOf(t, src))
	if len(leaves) < 3 {
		t.Fatalf("need >=3 leaves to delete an interior one, got %d", len(leaves))
	}
	target := leaves[1] // second leaf: has left sibling leaves[0] and right sibling leaves[2]
	deadTIDs := leafTIDs(t, src, target)
	if len(deadTIDs) == 0 {
		t.Fatalf("target interior leaf %d had no entries", target)
	}

	removed, err := bt.VacuumIndexPages(deadTIDs)
	if err != nil {
		t.Fatalf("VacuumIndexPages: %v", err)
	}
	if removed != len(deadTIDs) {
		t.Fatalf("VacuumIndexPages removed %d entries, expected %d", removed, len(deadTIDs))
	}
	assertLeafDeleted(t, src, target)

	// Every tier — including the cross-page sibling-link walk — must be silent
	// over the post-deletion tree. The surviving leaf-entry count is n minus the
	// entries on the emptied leaf.
	want := n - len(deadTIDs)
	for _, lm := range assertNonSiblingTiersSilent(t, mgr, pool, rel, want) {
		if r := amcheck.VerifyBtreeLevelSiblingLinks(src, lm, "ix_real"); r != nil {
			t.Fatalf("VerifyBtreeLevelSiblingLinks(leftmost %d) false positive after "+
				"interior leaf deletion — a deleted page is still reachable via a "+
				"sibling link, or a survivor's btpo_prev was left dangling: %+v", lm, r)
		}
	}
}

// TestVerifyBtreeEngineSilentAfterMultiLeafDeletion empties several NON-ADJACENT
// interior leaves in a single VacuumIndexPages pass and asserts the engine stays
// silent. Non-adjacent targets keep each unlink independent (no relink touches a
// concurrently-deleted neighbour), validating that multiple downlink removals
// from the same parent page and multiple sibling relinks in one pass all leave a
// structurally consistent tree.
func TestVerifyBtreeEngineSilentAfterMultiLeafDeletion(t *testing.T) {
	const n = 3000
	keys := make([][]byte, n)
	for i := range n {
		keys[i] = btree.EncodeInt4(int32(i))
	}
	mgr, pool, rel, bt, cleanup := buildRealTreeHandle(t, keys)
	defer cleanup()
	src := realPageSource(t, pool, rel)

	leaves := leafChainBlocks(t, src, rootOf(t, src))
	if len(leaves) < 6 {
		t.Fatalf("need >=6 leaves to delete several non-adjacent interior ones, got %d", len(leaves))
	}
	// Target every other interior leaf (1,3,5) — all interior, none adjacent to
	// another target.
	targets := []storage.BlockNumber{leaves[1], leaves[3], leaves[5]}
	var deadTIDs []storage.ItemPointer
	for _, blk := range targets {
		tids := leafTIDs(t, src, blk)
		if len(tids) == 0 {
			t.Fatalf("target interior leaf %d had no entries", blk)
		}
		deadTIDs = append(deadTIDs, tids...)
	}

	removed, err := bt.VacuumIndexPages(deadTIDs)
	if err != nil {
		t.Fatalf("VacuumIndexPages: %v", err)
	}
	if removed != len(deadTIDs) {
		t.Fatalf("VacuumIndexPages removed %d entries, expected %d", removed, len(deadTIDs))
	}
	for _, blk := range targets {
		assertLeafDeleted(t, src, blk)
	}

	want := n - len(deadTIDs)
	for _, lm := range assertNonSiblingTiersSilent(t, mgr, pool, rel, want) {
		if r := amcheck.VerifyBtreeLevelSiblingLinks(src, lm, "ix_real"); r != nil {
			t.Fatalf("VerifyBtreeLevelSiblingLinks(leftmost %d) false positive after "+
				"multi-leaf deletion: %+v", lm, r)
		}
	}
}

// TestVerifyBtreeEngineDetectsStaleSiblingLinkAfterAdjacentLeafDeletion is a
// DETECTION test (mirroring the original shuffled-tree test before M0110-0007):
// the real-producer harness uncovered that VacuumIndexPages mishandles the
// deletion of ADJACENT empty leaves in a single pass.
//
// unlinkEmptyLeaf relinks neighbours from pointers captured at PHASE-1 scan time
// (emptyLeafInfo.prev/next), BEFORE any leaf is unlinked. When a neighbour is
// itself one of the leaves being deleted in the same pass, those captured
// pointers go stale: for an adjacent run L0->L1->L2->L3->L4 with L1,L2,L3 all
// emptied, unlink(L1) sets L0.next=L2, unlink(L2) sets L3.prev=L1 / L1.next=L3,
// unlink(L3) sets L2.next=L4 / L4.prev=L2 — so the surviving left edge L0.next
// ends up pointing at the DELETED block L2, and L4.prev at the DELETED block L2.
// The leaf sibling chain is left structurally broken; the engine's sibling-link
// tier correctly flags "downlink or sibling link points to deleted block".
//
// btpo_prev/btpo_next are load-bearing (backward scans + future page-deletion
// relinking read them), and an adjacent dead-tuple run is the common case for a
// range DELETE + VACUUM, so this is a genuine on-disk correctness gap. Upstream
// avoids it: _bt_unlink_halfdead_page re-reads the CURRENT left/right siblings at
// unlink time and defers when a sibling is itself half-dead, rather than trusting
// pointers captured earlier in the pass.
//
// Tracked for fix as M0110-0010 (B-tree vacuum: relink the live siblings when
// deleting adjacent empty leaves). When that lands, this test must FLIP to a
// silence assertion exactly like TestVerifyBtreeEngineSilentAfterMultiLeafDeletion.
func TestVerifyBtreeEngineDetectsStaleSiblingLinkAfterAdjacentLeafDeletion(t *testing.T) {
	const n = 3000
	keys := make([][]byte, n)
	for i := range n {
		keys[i] = btree.EncodeInt4(int32(i))
	}
	mgr, pool, rel, bt, cleanup := buildRealTreeHandle(t, keys)
	defer cleanup()
	src := realPageSource(t, pool, rel)

	leaves := leafChainBlocks(t, src, rootOf(t, src))
	if len(leaves) < 5 {
		t.Fatalf("need >=5 leaves to delete an adjacent interior run, got %d", len(leaves))
	}
	// A contiguous run of interior leaves (each adjacent to the next target).
	targets := []storage.BlockNumber{leaves[1], leaves[2], leaves[3]}
	var deadTIDs []storage.ItemPointer
	for _, blk := range targets {
		deadTIDs = append(deadTIDs, leafTIDs(t, src, blk)...)
	}
	if removed, err := bt.VacuumIndexPages(deadTIDs); err != nil {
		t.Fatalf("VacuumIndexPages: %v", err)
	} else if removed != len(deadTIDs) {
		t.Fatalf("VacuumIndexPages removed %d entries, expected %d", removed, len(deadTIDs))
	}
	for _, blk := range targets {
		assertLeafDeleted(t, src, blk)
	}

	// Every NON-sibling tier remains clean (the survivors' contents, the parent
	// downlinks, and the heapallindexed round-trip are unaffected — only the
	// inter-leaf links are corrupt). CollectBtreeLeafEntries tolerates the broken
	// chain by skipping deleted pages, so the survivor count is still exact.
	want := n - len(deadTIDs)
	lms := assertNonSiblingTiersSilent(t, mgr, pool, rel, want)

	// The sibling-link tier MUST fire, naming a deleted block — the bug signature.
	found := false
	for _, lm := range lms {
		for _, r := range amcheck.VerifyBtreeLevelSiblingLinks(src, lm, "ix_real") {
			if strings.Contains(r.Msg, "deleted block") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("expected the sibling-link tier to flag a deleted-block reference after " +
			"adjacent-leaf deletion (the known VacuumIndexPages stale-relink bug); got none " +
			"— if VacuumIndexPages was fixed (M0110-0010), flip this test to a silence assertion")
	}
}
