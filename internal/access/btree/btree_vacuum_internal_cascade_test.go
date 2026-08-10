package btree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestVacuumIndexPagesCascadesEmptyInternalPage is the regression test for
// M-NIGHTLY (AI-20260706-201855-001)'s second, independent root cause: when
// every leaf child under a NON-ROOT internal page is vacuumed empty in a
// single VacuumIndexPages pass, the internal page's own item count drops to
// 0, but (pre-fix) nothing ever removed IT from ITS OWN parent — it stayed
// linked in the tree as a contentless internal page. Any later descent whose
// separator range still routed through it hit findChildBlockDirect's
// `count == 0` guard and raised "btree: empty internal page", independent of
// any concurrency race (this is what a tiny, heavily split/vacuumed
// pgbench_branches-style table eventually triggers via repeated churn).
//
// This test builds a genuine 3-level tree (root -> internal -> leaf) via
// BulkCreate, deletes every entry under one whole non-root internal page's
// leaf subtree in one VacuumIndexPages call, and asserts the tree remains
// fully readable afterward — including a RangeScan whose key range used to
// route through the now-empty subtree.
func TestVacuumIndexPagesCascadesEmptyInternalPage(t *testing.T) {
	pool, rel := newVacuumTestPool(t)

	// n=900000 sequential int4 keys reliably produces meta.Level==2 (root
	// at level 2, internal pages at level 1, leaves at level 0) with the
	// root holding >=4 downlinks — probed empirically: n=250000 first
	// reaches level 2 but with only 2 root downlinks (no non-edge child
	// to pick); n=600000 reaches 3; n=900000 reaches 4.
	const n = 900000
	entries := make([]BulkEntry, n)
	for i := 0; i < n; i++ {
		entries[i] = BulkEntry{
			Key: EncodeInt4(int32(i)),
			Ptr: storage.ItemPointer{Block: storage.BlockNumber(i / 100), Offset: uint16(i%100 + 1)},
		}
	}
	tree, err := BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}

	meta, err := tree.readMeta()
	if err != nil {
		t.Fatalf("readMeta: %v", err)
	}
	if meta.Level < 2 {
		t.Fatalf("need a 3-level tree (root level>=2) to exercise a non-root internal cascade, got level=%d — adjust n", meta.Level)
	}

	// Read the root's downlinks (level-1 children).
	rootItems := readItemsForTest(t, tree, meta.Root)
	if len(rootItems) < 3 {
		t.Fatalf("need >=3 root downlinks to pick a non-edge internal child, got %d", len(rootItems))
	}
	// Pick a middle child so it has live siblings on both sides at level 1.
	targetInternal := rootItems[len(rootItems)/2].ptr.Block

	// Read that internal page's own downlinks (leaf children) and collect
	// every heap TID stored across all of its leaves.
	leafItems := readItemsForTest(t, tree, targetInternal)
	if len(leafItems) == 0 {
		t.Fatalf("target internal page %d has no downlinks", targetInternal)
	}
	var deadTIDs []storage.ItemPointer
	for _, li := range leafItems {
		leafBlk := li.ptr.Block
		for _, it := range readItemsForTest(t, tree, leafBlk) {
			deadTIDs = append(deadTIDs, it.ptr)
		}
	}
	if len(deadTIDs) == 0 {
		t.Fatalf("target internal page %d's leaves have no entries", targetInternal)
	}

	removed, err := tree.VacuumIndexPages(deadTIDs)
	if err != nil {
		t.Fatalf("VacuumIndexPages: %v", err)
	}
	if removed != len(deadTIDs) {
		t.Fatalf("expected %d removed, got %d", len(deadTIDs), removed)
	}

	// The cascaded-away internal page must now be flagged deleted.
	if !vacOpaque(t, tree, targetInternal).IsDeleted() {
		t.Fatalf("cascaded internal page %d was expected deleted (BTDeleted) but is still live", targetInternal)
	}

	// The root must no longer downlink to it (this is the actual fix:
	// pre-fix, the root retained a stale downlink to a now-empty page).
	for _, it := range readItemsForTest(t, tree, meta.Root) {
		if it.ptr.Block == targetInternal {
			t.Fatalf("root still downlinks to cascaded-away internal page %d", targetInternal)
		}
	}

	// A full-range scan must succeed with the exact expected remaining
	// count and must not surface "btree: empty internal page" — this is
	// the actual pre-fix failure mode (any key whose separator range used
	// to route through targetInternal would hit it during descent).
	want := n - len(deadTIDs)
	var count int
	if err := tree.RangeScan(nil, nil, func(_ []byte, _ storage.ItemPointer) (bool, error) {
		count++
		return true, nil
	}); err != nil {
		t.Fatalf("RangeScan after internal-page cascade: %v", err)
	}
	if count != want {
		t.Fatalf("expected %d surviving entries, got %d", want, count)
	}

	// A fresh Insert must also work (exercises descendToLeaf routing
	// through the gap left by the cascaded-away subtree).
	newKey := EncodeInt4(int32(n + 1))
	if err := tree.Insert(newKey, storage.ItemPointer{Block: 0, Offset: 1}); err != nil {
		t.Fatalf("Insert after internal-page cascade: %v", err)
	}
}

// readItemsForTest reads and decodes every item on blk under a shared latch.
func readItemsForTest(t *testing.T, bt *BTree, blk storage.BlockNumber) []item {
	t.Helper()
	slot, err := bt.pinR(blk)
	if err != nil {
		t.Fatalf("pinR(%d): %v", blk, err)
	}
	items, err := blobFormat.pageItems(slot.Page())
	bt.unpinR(slot)
	if err != nil {
		t.Fatalf("blobFormat.pageItems(%d): %v", blk, err)
	}
	return items
}
