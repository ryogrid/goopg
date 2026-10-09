package executor

import (
	"bytes"
	"errors"
	"sort"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// TestSysBtreeRebuildFailureLeavesOldTree pins M0146-0070. When an in-place
// insert overflows a leaf of a multi-level catalog btree,
// rebuildSysBtreeWithNewEntry rewrites the whole tree. It used to write the
// metapage and the existing internal pages before extending the file for the
// new tail blocks. A failed extension therefore left downlinks past EOF, and
// the next descent failed `short read`. The rebuild now extends the file
// first, writes children before parents, and writes the metapage last.
//
// The cases fail one pin each:
//   - A failed extension leaves the old tree unchanged and readable.
//   - A failed pin of an existing page, or of the metapage, can leave a tree
//     that mixes old and new pages (deferral ledger 2026-10-07), but no
//     block it references lies past EOF, so the tree still reads without
//     error.
//
// An unfailed rebuild then completes with every tuple in order.
func TestSysBtreeRebuildFailureLeavesOldTree(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	// A new tuple that sorts first shifts every leaf of the new layout, so a
	// half-written rebuild cannot read back as the seed by accident.
	big := "aaa" + string(bytes.Repeat([]byte{'x'}, 60))
	newTuple := buildIndexTupleProcNameArgsNsp(9, 1, big, []uint32{23, 23, 23, 23, 23, 23, 23, 23}, 11)
	seedOf := func(n int) [][]byte {
		seed := make([][]byte, 0, n)
		for i := 0; i < n; i++ {
			name := "zzfn" + string(rune('a'+i/26/26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+i%26))
			seed = append(seed, buildIndexTupleProcNameArgsNsp(0, uint16(i+1), name, []uint32{23}, 11))
		}
		sort.Slice(seed, func(i, j int) bool {
			return cmpKeyProcNameArgsNsp(seed[i][sysIndexTupleHoff:], seed[j][sysIndexTupleHoff:]) < 0
		})
		return seed
	}
	// The seed size is the first one at or above 300 whose layout grows by a
	// block when the new tuple joins it, so the rebuild must extend the file
	// and the extension case has something to fail.
	var seed [][]byte
	var image []byte
	for n := 300; n < 800 && seed == nil; n++ {
		cand := seedOf(n)
		img, err := buildBulkSysBtreeLayoutVariable(cand, 3)
		if err != nil {
			t.Fatalf("bulk layout: %v", err)
		}
		merged, _ := mergeSortedSlice(cand, newTuple, cmpKeyProcNameArgsNsp)
		grown, err := buildBulkSysBtreeLayoutVariable(merged, 3)
		if err != nil {
			t.Fatalf("bulk layout: %v", err)
		}
		if len(grown) > len(img) {
			seed, image = cand, img
		}
	}
	if seed == nil {
		t.Fatalf("no seed size makes the rebuild extend the file")
	}
	if len(image)/storage.BlockSize < 4 {
		t.Fatalf("seed produced %d pages; want a multi-level tree", len(image)/storage.BlockSize)
	}
	writeSysBtreeImage(t, ctx, pgProcPronameArgsNspIndexOID, image)
	rel := storage.RelFileNode{DBOid: catalog.DefaultDBOid, RelOid: pgProcPronameArgsNspIndexOID, Fork: storage.MainFork}
	meta, _ := keyMetaForSysBtree(pgProcPronameArgsNspIndexOID)

	collect := func() [][]byte {
		t.Helper()
		root, level, err := readSysBtreeMeta(ctx, rel)
		if err != nil {
			t.Fatalf("read meta: %v", err)
		}
		all, err := collectAllLeafTuples(ctx, rel, root, level, meta)
		if err != nil {
			t.Fatalf("collect leaves: %v", err)
		}
		return all
	}
	same := func(a, b [][]byte) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if !bytes.Equal(a[i], b[i]) {
				return false
			}
		}
		return true
	}
	if !same(collect(), seed) {
		t.Fatalf("seeded tree does not read back as the seed")
	}

	origPin, origPinNew := sysBtreeRebuildPin, sysBtreeRebuildPinNew
	defer func() { sysBtreeRebuildPin, sysBtreeRebuildPinNew = origPin, origPinNew }()
	injected := errors.New("injected pin failure")

	for _, c := range []struct {
		name       string
		failPin    int  // fail the n-th Pin of an existing block (1-based); 0 = never
		failMeta   bool // fail the Pin of the metapage (block 0, pinned last)
		failPinNew int  // fail the n-th PinNew; 0 = never
		unchanged  bool // the old tree must read back exactly
	}{
		// The extension case runs first: every later case also extends the
		// file before its injected failure, so the zeroed, unreachable tail
		// it leaves behind would give this case nothing left to extend.
		{name: "extension", failPinNew: 1, unchanged: true},
		{name: "existing block 1", failPin: 1},
		{name: "existing block 3", failPin: 3},
		{name: "metapage", failMeta: true},
	} {
		pins, pinNews := 0, 0
		sysBtreeRebuildPin = func(ctx *Context, tag storage.BufferTag) (*storage.Slot, error) {
			pins++
			if pins == c.failPin || (c.failMeta && tag.Block == 0) {
				return nil, injected
			}
			return origPin(ctx, tag)
		}
		sysBtreeRebuildPinNew = func(ctx *Context, rel storage.RelFileNode) (*storage.Slot, storage.BlockNumber, error) {
			pinNews++
			if pinNews == c.failPinNew {
				return nil, storage.InvalidBlockNumber, injected
			}
			return origPinNew(ctx, rel)
		}
		err := rebuildSysBtreeWithNewEntry(ctx, pgProcPronameArgsNspIndexOID, rel, newTuple, cmpKeyProcNameArgsNsp)
		if !errors.Is(err, injected) {
			t.Fatalf("%s: rebuild err = %v, want the injected failure", c.name, err)
		}
		if c.failPinNew != 0 && pinNews == 0 {
			t.Fatalf("%s: the rebuild never extended the file; the case tests nothing", c.name)
		}
		got := collect() // fails the test on a read past EOF
		if c.unchanged && !same(got, seed) {
			t.Fatalf("%s: a failed rebuild changed the tree", c.name)
		}
		// Restore the seed tree for the next case.
		writeSysBtreeImageOver(t, ctx, rel, image)
	}

	// Unfailed, the rebuild lands the new tuple first and keeps every seed
	// tuple.
	sysBtreeRebuildPin, sysBtreeRebuildPinNew = origPin, origPinNew
	if err := rebuildSysBtreeWithNewEntry(ctx, pgProcPronameArgsNspIndexOID, rel, newTuple, cmpKeyProcNameArgsNsp); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	all := collect()
	if len(all) != len(seed)+1 || !bytes.Equal(all[0], newTuple) || !same(all[1:], seed) {
		t.Fatalf("rebuilt tree holds %d tuples (want %d) or lost its order", len(all), len(seed)+1)
	}
}

// writeSysBtreeImageOver overwrites the leading blocks of an existing index
// relation with image.
func writeSysBtreeImageOver(t *testing.T, ctx *Context, rel storage.RelFileNode, image []byte) {
	t.Helper()
	for blk := 0; blk < len(image)/storage.BlockSize; blk++ {
		slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: storage.BlockNumber(blk)})
		if err != nil {
			t.Fatalf("pin blk %d: %v", blk, err)
		}
		slot.Lock()
		copy(slot.Page(), image[blk*storage.BlockSize:(blk+1)*storage.BlockSize])
		ctx.Pool.MarkDirty(slot)
		slot.Unlock()
		ctx.Pool.Unpin(slot)
	}
}
