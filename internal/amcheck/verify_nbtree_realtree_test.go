package amcheck_test

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/amcheck"
	"github.com/goopg/goopg/internal/storage"
)

// The B-tree verification engine (verify_nbtree.go, heapallindexed*.go) was
// built and unit-tested entirely against HAND-CONSTRUCTED pages — makeItemsPage,
// makeLinkedPage, makeInternalPage, makeLeafPage and friends poke bytes directly
// because the only way to exhibit corruption is to write it by hand. That proves
// the *detection* paths fire, but it leaves the complementary risk uncovered: a
// hand-built "clean" page only models what the test author believed goopg writes.
// If the engine's decode assumptions (metapage magic/version, opaque level/flag
// layout, the inline (keyLen|block|offset|key) item format, posting-list framing,
// the high key in the opaque area, downlink child blocks, sibling links across a
// split) diverge from what the REAL btree.Create/Insert/split machinery actually
// lays on disk, the eventual verify_heapam()/bt_index_check() SQL surface would
// either report corruption on perfectly healthy user indexes, or stay silent on
// genuinely corrupt ones — the most expensive class of compatibility bug in this
// project (cf. the heap-side analogue verify_heapam_realpage_test.go, which
// surfaced a real VacuumHeapPageBySlots MAXALIGN bug exactly this way).
//
// These tests drive the REAL producer — they build live B-trees by inserting
// enough keys to force leaf splits, internal-page creation and a multi-level
// tree — then run every tier of the verification engine over the on-disk pages.
//
// They split into two outcomes by insertion order, which is itself a finding:
//
//   - Sorted (append-only) insertion only ever splits the RIGHTMOST page of a
//     level, which has no right sibling, so every tier — including the
//     cross-page sibling-link walk — must stay SILENT.
//
//   - Shuffled insertion forces splits in the MIDDLE of a level. goopg's split
//     (btree.go:1454-1466 / 1522) links the new right page's btpo_prev to the
//     left page and sets the left page's btpo_next to the new page, but it never
//     updates the OLD right sibling's btpo_prev to point at the new page. The
//     old right sibling's left-link is therefore left STALE after any non-rightmost
//     split. The sibling-link tier correctly flags this ("left link/right link
//     pair ... not in agreement"); every other tier (which does not depend on
//     btpo_prev) stays silent. This is a genuine goopg btree correctness gap —
//     btpo_prev is load-bearing (btree_vacuum.go reads op.Prev and WAL-logs
//     RightSibNewPrev to relink siblings during page deletion), so a stale
//     left-link can mislead page-deletion relinking and any backward navigation.
//     Tracked for fix as the split right-sibling prev-link maintenance task; when
//     that lands, TestVerifyBtreeEngineDetectsStaleSiblingLinkOnRealTree must flip
//     to a silence assertion (see its trailing comment).

// realPageSource returns a PageSource that reads the current (buffer-pool-visible,
// including not-yet-flushed dirty) bytes of each block of rel, copied out so the
// engine never aliases a live buffer.
func realPageSource(t *testing.T, pool *storage.Pool, rel storage.RelFileNode) amcheck.PageSource {
	t.Helper()
	return func(blk storage.BlockNumber) (storage.Page, error) {
		slot, err := pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return nil, err
		}
		defer pool.Unpin(slot)
		src := slot.Page()
		cp := make(storage.Page, len(src))
		copy(cp, src)
		return cp, nil
	}
}

// buildRealTree creates a fresh B-tree, inserts keys[i] -> a distinct heap TID,
// and returns the manager, pool, rel and a cleanup. Distinct, monotonically
// derived TIDs let the heapallindexed round-trip distinguish entries.
func buildRealTree(t *testing.T, keys [][]byte) (*storage.Manager, *storage.Pool, storage.RelFileNode, func()) {
	t.Helper()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 64})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	rel := storage.RelFileNode{DBOid: 1, RelOid: 9100, Fork: storage.MainFork}
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
	return mgr, pool, rel, cleanup
}

// leftmostByLevel descends root -> leftmost child at each level via the
// negative-infinity (slot-1) downlink, returning the leftmost block of every
// level top-down. It is the starting point the sibling-link walk needs per level.
func leftmostByLevel(t *testing.T, src amcheck.PageSource, root storage.BlockNumber) []storage.BlockNumber {
	t.Helper()
	var out []storage.BlockNumber
	for blk := root; ; {
		p, err := src(blk)
		if err != nil {
			t.Fatalf("descend read block %d: %v", blk, err)
		}
		out = append(out, blk)
		op := btree.ParseOpaque(p)
		if op.IsLeaf() {
			return out
		}
		dls, err := btree.PageDownlinks(p)
		if err != nil {
			t.Fatalf("PageDownlinks(blk %d): %v", blk, err)
		}
		if len(dls) == 0 {
			t.Fatalf("internal block %d has no downlinks", blk)
		}
		blk = dls[0].Child // leftmost (negative-infinity) downlink
	}
}

// assertNonSiblingTiersSilent runs every B-tree engine tier EXCEPT the cross-page
// sibling-link walk over a real on-disk index and asserts zero findings. None of
// these tiers reads btpo_prev, so they must be clean regardless of insertion
// order. It returns the per-level leftmost blocks so the caller can drive the
// sibling-link tier separately (silent for sorted trees, a detection for shuffled
// trees). wantLeaves pins the collected leaf-entry count (one per inserted key —
// proves the level walk reached them all, not merely that nothing was flagged).
func assertNonSiblingTiersSilent(t *testing.T, mgr *storage.Manager, pool *storage.Pool, rel storage.RelFileNode, wantLeaves int) []storage.BlockNumber {
	t.Helper()
	const name = "ix_real"
	const tbl = "tbl_real"
	src := realPageSource(t, pool, rel)

	nblocks, err := mgr.NBlocks(rel)
	if err != nil {
		t.Fatalf("NBlocks: %v", err)
	}
	if nblocks < 2 {
		t.Fatalf("expected a split tree (>=2 blocks incl. metapage), got %d", nblocks)
	}

	// Per-page tiers over every block, including the metapage (block 0).
	for blk := range nblocks {
		p, err := src(blk)
		if err != nil {
			t.Fatalf("read block %d: %v", blk, err)
		}
		if r := amcheck.VerifyBtreePage(p, blk, name); r != nil {
			t.Fatalf("VerifyBtreePage(blk %d) false positive: %+v", blk, r)
		}
		if r := amcheck.VerifyBtreeItemOrder(p, blk, name); r != nil {
			t.Fatalf("VerifyBtreeItemOrder(blk %d) false positive: %+v", blk, r)
		}
	}

	// Cross-level downlink tier on every internal (non-leaf, live) page.
	for blk := range nblocks {
		if blk == btree.MetaBlock {
			continue
		}
		p, err := src(blk)
		if err != nil {
			t.Fatalf("read block %d: %v", blk, err)
		}
		op := btree.ParseOpaque(p)
		if op.IsLeaf() || op.IsDeleted() {
			continue
		}
		if r := amcheck.VerifyBtreeParentDownlinks(src, blk, name); r != nil {
			t.Fatalf("VerifyBtreeParentDownlinks(internal blk %d) false positive: %+v", blk, r)
		}
	}

	// heapallindexed round-trip: the leaf entries collected by the engine's own
	// relation walk, fed back as both the index set and the heap set, must report
	// zero "lacks matching index tuple" findings — every entry fingerprints and
	// probes to the same value through fingerprintLeafEntry (sibling-path
	// invariant on real leaf bytes).
	leaves, err := amcheck.CollectBtreeLeafEntries(src)
	if err != nil {
		t.Fatalf("CollectBtreeLeafEntries: %v", err)
	}
	if len(leaves) != wantLeaves {
		t.Fatalf("leaf walk collected %d entries, want %d (level walk missed entries)", len(leaves), wantLeaves)
	}
	const seed = 0x9e3779b97f4a7c15
	if r := amcheck.VerifyBtreeHeapAllIndexed(leaves, leaves, name, tbl, seed); r != nil {
		t.Fatalf("VerifyBtreeHeapAllIndexed round-trip false positive: %+v", r)
	}
	if rr, err := amcheck.VerifyBtreeHeapAllIndexedRelation(src, leaves, name, tbl, seed); err != nil {
		t.Fatalf("VerifyBtreeHeapAllIndexedRelation: %v", err)
	} else if rr != nil {
		t.Fatalf("VerifyBtreeHeapAllIndexedRelation false positive: %+v", rr)
	}

	meta := func() btree.BTreeMeta {
		p, err := src(btree.MetaBlock)
		if err != nil {
			t.Fatalf("read metapage: %v", err)
		}
		return btree.ParseMeta(p)
	}()
	return leftmostByLevel(t, src, meta.Root)
}

func TestVerifyBtreeEngineSilentOnRealInt4Sequential(t *testing.T) {
	const n = 3000 // forces multiple leaf splits and at least one internal level
	keys := make([][]byte, n)
	for i := range n {
		keys[i] = btree.EncodeInt4(int32(i))
	}
	mgr, pool, rel, cleanup := buildRealTree(t, keys)
	defer cleanup()
	src := realPageSource(t, pool, rel)
	for _, lm := range assertNonSiblingTiersSilent(t, mgr, pool, rel, n) {
		// Append-only inserts split only the rightmost page (no old right
		// sibling), so the sibling-link tier must be silent here too.
		if r := amcheck.VerifyBtreeLevelSiblingLinks(src, lm, "ix_real"); r != nil {
			t.Fatalf("VerifyBtreeLevelSiblingLinks(leftmost %d) false positive on sorted tree: %+v", lm, r)
		}
	}
}

func TestVerifyBtreeEngineSilentOnRealVarchar(t *testing.T) {
	// Variable-length keys put a real, non-trivial separator in each page's
	// opaque high-key area — the path the fixed-width int cases barely exercise
	// (the high-key invariant tier reads BTPageOpaque.HighKey directly). Sorted
	// order keeps splits rightmost, so all tiers (incl. sibling links) stay silent.
	const n = 2500
	keys := make([][]byte, n)
	for i := range n {
		keys[i] = btree.EncodeVarchar(fmt.Appendf(nil, "user-%010d-row", i))
	}
	mgr, pool, rel, cleanup := buildRealTree(t, keys)
	defer cleanup()
	src := realPageSource(t, pool, rel)
	for _, lm := range assertNonSiblingTiersSilent(t, mgr, pool, rel, n) {
		if r := amcheck.VerifyBtreeLevelSiblingLinks(src, lm, "ix_real"); r != nil {
			t.Fatalf("VerifyBtreeLevelSiblingLinks(leftmost %d) false positive on sorted varchar tree: %+v", lm, r)
		}
	}
}

func TestVerifyBtreeEngineSilentOnRealInt8(t *testing.T) {
	const n = 2500
	keys := make([][]byte, n)
	for i := range n {
		keys[i] = btree.EncodeInt8(int64(i) * 1_000_003)
	}
	mgr, pool, rel, cleanup := buildRealTree(t, keys)
	defer cleanup()
	src := realPageSource(t, pool, rel)
	for _, lm := range assertNonSiblingTiersSilent(t, mgr, pool, rel, n) {
		if r := amcheck.VerifyBtreeLevelSiblingLinks(src, lm, "ix_real"); r != nil {
			t.Fatalf("VerifyBtreeLevelSiblingLinks(leftmost %d) false positive on sorted int8 tree: %+v", lm, r)
		}
	}
}

// TestVerifyBtreeEngineSilentOnRealShuffledInt4 builds a B-tree with SHUFFLED
// inserts (forcing MIDDLE-of-level splits, not just rightmost ones) and asserts
// every tier — including the cross-page sibling-link walk — stays silent.
//
// This test was previously a DETECTION assertion: the real-producer harness
// uncovered that goopg's splitAndInsert never updated the OLD right sibling's
// btpo_prev on a non-rightmost split (it set the new right page's prev and the
// left page's next, but the page that used to be left's right sibling kept its
// btpo_prev pointing at the left block — a stale left-link on any non-append
// insert pattern). btpo_prev is load-bearing (btree_vacuum.go reads op.Prev and
// WAL-logs RightSibNewPrev to relink siblings on page deletion), so the stale
// link was a genuine on-disk correctness gap.
//
// M0110-0007 fixed the split path to relink the old right sibling's btpo_prev to
// the new right page, atomically with the split (third page in the
// BtreeSplit WAL record; mirrors PostgreSQL _bt_split). The shuffled tree is now
// internally consistent, so the sibling-link tier — which a healthy PG tree
// never trips — must stay silent here, exactly as it does for the sorted cases.
func TestVerifyBtreeEngineSilentOnRealShuffledInt4(t *testing.T) {
	const n = 3000
	rng := rand.New(rand.NewSource(20260614))
	perm := rng.Perm(n)
	keys := make([][]byte, n)
	for i, v := range perm {
		keys[i] = btree.EncodeInt4(int32(v))
	}
	mgr, pool, rel, cleanup := buildRealTree(t, keys)
	defer cleanup()
	src := realPageSource(t, pool, rel)

	for _, lm := range assertNonSiblingTiersSilent(t, mgr, pool, rel, n) {
		// Shuffled inserts split middle pages, so each split must relink the
		// old right sibling's btpo_prev — the sibling-link tier proves it did.
		if r := amcheck.VerifyBtreeLevelSiblingLinks(src, lm, "ix_real"); r != nil {
			t.Fatalf("VerifyBtreeLevelSiblingLinks(leftmost %d) false positive on "+
				"shuffled tree — a stale old-right-sibling prev-link survived a "+
				"middle-of-level split (M0110-0007 regression?): %+v", lm, r)
		}
	}
}
