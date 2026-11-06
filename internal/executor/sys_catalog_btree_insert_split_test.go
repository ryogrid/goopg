package executor

import (
	"bytes"
	"fmt"
	"math/rand"
	"sort"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// seedRelnameNspIndex installs a two-level pg_class_relname_nsp_index (2663)
// bulk image holding n "seed_NNNNN" entries and returns its relfile.
func seedRelnameNspIndex(t *testing.T, ctx *Context, n int) storage.RelFileNode {
	t.Helper()
	seed := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		seed = append(seed, buildIndexTupleNameOidKey(0, uint16(i+1), fmt.Sprintf("seed_%05d", i), 2200))
	}
	sort.Slice(seed, func(i, j int) bool {
		return cmpKeyNameOid(seed[i][sysIndexTupleHoff:], seed[j][sysIndexTupleHoff:]) < 0
	})
	meta, _ := keyMetaForSysBtree(pgClassRelnameNspIndexOID)
	image, err := buildBulkSysBtreeLayout(seed, meta.tupleSize, meta.nkeyatts)
	if err != nil {
		t.Fatalf("bulk layout: %v", err)
	}
	writeSysBtreeImage(t, ctx, pgClassRelnameNspIndexOID, image)
	return storage.RelFileNode{DBOid: catalog.DefaultDBOid, RelOid: pgClassRelnameNspIndexOID, Fork: storage.MainFork}
}

// checkSysBtreeStructure walks every level left to right and checks the
// invariants PG's nbtree readers rely on: sibling links agree in both
// directions, btpo_level matches, a non-rightmost page's high key equals the
// key of its right sibling's first data item (leaves) or the low key that
// sibling's parent downlink carries (internal pages — checked via the
// downlink), only the root carries BTP_ROOT, and every downlink of a page
// points at a page one level down. It returns the leaves' item counts.
func checkSysBtreeStructure(t *testing.T, ctx *Context, rel storage.RelFileNode) (leafCounts []int, rightmostLeaf int) {
	t.Helper()
	rootBlk, level, err := readSysBtreeMeta(ctx, rel)
	if err != nil {
		t.Fatalf("meta: %v", err)
	}
	root, err := readSysBtreePageImage(ctx, rel, rootBlk)
	if err != nil {
		t.Fatal(err)
	}
	if root.flags&btpRootFlag == 0 || root.level != level || root.prev != pNoneBlock || root.next != pNoneBlock {
		t.Fatalf("root blk %d: flags=%#x level=%d (meta %d) prev=%d next=%d", rootBlk, root.flags, root.level, level, root.prev, root.next)
	}
	leftmost := rootBlk
	for lvl := int(level); lvl >= 0; lvl-- {
		prev := uint32(pNoneBlock)
		var nextLeftmost uint32
		for cur := leftmost; cur != pNoneBlock; {
			img, err := readSysBtreePageImage(ctx, rel, cur)
			if err != nil {
				t.Fatal(err)
			}
			if int(img.level) != lvl && !(lvl == 0 && img.flags&btpLeafFlag != 0) {
				t.Fatalf("blk %d: level %d, want %d", cur, img.level, lvl)
			}
			if (img.flags&btpLeafFlag != 0) != (lvl == 0) {
				t.Fatalf("blk %d: leaf flag %v at level %d", cur, img.flags&btpLeafFlag != 0, lvl)
			}
			if img.prev != prev {
				t.Fatalf("blk %d: btpo_prev %d, want %d", cur, img.prev, prev)
			}
			if cur != rootBlk && img.flags&btpRootFlag != 0 {
				t.Fatalf("blk %d carries BTP_ROOT but the root is %d", cur, rootBlk)
			}
			if len(img.items) == 0 {
				t.Fatalf("blk %d has no items", cur)
			}
			if lvl > 0 {
				if prev == pNoneBlock {
					nextLeftmost = decodeChildFromTID(img.items[0])
				}
				if len(img.items[0]) != sizeOfIndexTupleHdr {
					t.Fatalf("internal blk %d: first item is not minus infinity (len %d)", cur, len(img.items[0]))
				}
			}
			if img.next != pNoneBlock {
				sib, err := readSysBtreePageImage(ctx, rel, img.next)
				if err != nil {
					t.Fatal(err)
				}
				if lvl == 0 && !bytes.Equal(img.highKey[sysIndexTupleHoff:], sib.items[0][sysIndexTupleHoff:]) {
					t.Fatalf("leaf %d: high key differs from right sibling %d's first key", cur, img.next)
				}
				if lvl == 0 && cmpKeyNameOid(img.items[len(img.items)-1][sysIndexTupleHoff:], img.highKey[sysIndexTupleHoff:]) >= 0 {
					t.Fatalf("leaf %d: last item not below its high key", cur)
				}
			}
			if lvl == 0 {
				leafCounts = append(leafCounts, len(img.items))
				if img.next == pNoneBlock {
					rightmostLeaf = len(leafCounts) - 1
				}
			}
			prev, cur = cur, img.next
		}
		leftmost = nextLeftmost
	}
	return leafCounts, rightmostLeaf
}

// TestSysBtreeSplitsInPlace drives thousands of random-order inserts into a
// multi-level catalog index. Every key must stay reachable by descent, the
// tree must stay structurally valid while it grows past a third level, and
// a full leaf must be split, not rebuilt: the rebuild re-collected every
// tuple (collectAllLeafTuples) per overflow, which made catalog DDL cost
// grow with the catalog (AI-20261103-004134-002).
func TestSysBtreeSplitsInPlace(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	rel := seedRelnameNspIndex(t, ctx, 300)

	rebuilds := 0
	origPin := sysBtreeRebuildPinNew
	sysBtreeRebuildPinNew = func(ctx *Context, rel storage.RelFileNode) (*storage.Slot, storage.BlockNumber, error) {
		rebuilds++
		return origPin(ctx, rel)
	}
	defer func() { sysBtreeRebuildPinNew = origPin }()

	const n = 12000
	rng := rand.New(rand.NewSource(7))
	names := make([]string, n)
	for i, p := range rng.Perm(n) {
		names[i] = fmt.Sprintf("rel_%05d", p)
	}
	for i, name := range names {
		if err := insertPgClassRelnameNspIndexEntry(ctx, name, 2200, storage.ItemPointer{Block: 9, Offset: uint16(i%60000 + 1)}); err != nil {
			t.Fatalf("insert %d (%s): %v", i, name, err)
		}
	}
	if rebuilds != 0 {
		t.Fatalf("whole-index rebuild ran %d times; a full leaf must be split in place", rebuilds)
	}
	rootBlk, level, err := readSysBtreeMeta(ctx, rel)
	if err != nil {
		t.Fatal(err)
	}
	if level < 2 {
		t.Fatalf("tree level %d after %d inserts; want the root to have split at least once (>= 2)", level, n)
	}
	checkSysBtreeStructure(t, ctx, rel)

	meta, _ := keyMetaForSysBtree(pgClassRelnameNspIndexOID)
	all, err := collectAllLeafTuples(ctx, rel, rootBlk, level, meta)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 300+n {
		t.Fatalf("tree holds %d tuples, want %d", len(all), 300+n)
	}
	for i := 1; i < len(all); i++ {
		if cmpKeyNameOid(all[i-1][sysIndexTupleHoff:], all[i][sysIndexTupleHoff:]) > 0 {
			t.Fatalf("tuples %d/%d out of order", i-1, i)
		}
	}
	for i := 0; i < n; i += 97 {
		want := buildIndexTupleNameOidKey(9, uint16(i%60000+1), names[i], 2200)
		leafBlk, err := descendSysBtreeToLeaf(ctx, rel, rootBlk, level, want[sysIndexTupleHoff:], cmpKeyNameOid)
		if err != nil {
			t.Fatal(err)
		}
		if !leafContainsTuple(t, ctx, rel, leafBlk, want) {
			t.Fatalf("%s not on its descent leaf %d", names[i], leafBlk)
		}
	}
}

// TestSysBtreeAscendingSplitsLeaveFillfactorPages: ascending inserts always
// split the rightmost leaf, where _bt_findsplitloc weighs free space by the
// leaf fillfactor (90), so the pages left behind are ~90% full — a 50/50
// split would leave them half empty and double the index.
func TestSysBtreeAscendingSplitsLeaveFillfactorPages(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	rel := seedRelnameNspIndex(t, ctx, 300)
	for i := 0; i < 3000; i++ {
		if err := insertPgClassRelnameNspIndexEntry(ctx, fmt.Sprintf("zz_%05d", i), 2200, storage.ItemPointer{Block: 9, Offset: uint16(i + 1)}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	counts, rightmost := checkSysBtreeStructure(t, ctx, rel)
	meta, _ := keyMetaForSysBtree(pgClassRelnameNspIndexOID)
	payload := storage.BlockSize - storage.SizeOfPageHeaderData - sizeOfBTPageOpaque
	full := payload / (meta.tupleSize + 4)
	// The seed's bulk-built leaves sort first ("seed_" < "zz_"); the leaves
	// the ascending inserts split off follow them.
	seedLeaves := 0
	for sum := 0; sum < 300; seedLeaves++ {
		sum += counts[seedLeaves]
	}
	var split []int
	for i := seedLeaves; i < len(counts); i++ {
		if i != rightmost {
			split = append(split, counts[i])
		}
	}
	if len(split) < 10 {
		t.Fatalf("only %d split-off leaves", len(split))
	}
	for i, c := range split {
		if c*100 < full*85 || c*100 > full*95 {
			t.Fatalf("split-off leaf %d holds %d of %d items (%.0f%%), want ~90%% (leaf fillfactor)", i, c, full, 100*float64(c)/float64(full))
		}
	}
}
