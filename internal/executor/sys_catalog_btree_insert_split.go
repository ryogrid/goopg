package executor

// M-NIGHTLY-catalog-btree-split — in-place page splits for the multi-level
// system catalog btrees (AI-20261103-004134-002).
//
// The multi-level insert used to fall back to rebuildSysBtreeWithNewEntry
// whenever the target leaf was full: it re-collected every tuple, ran the
// bulk layout (non-rightmost leaves packed 100% full) and rewrote, with a
// full-page image, every page of the index. Because the rebuilt leaves were
// full again, the next insert into any non-rightmost leaf rebuilt the whole
// index once more — CREATE TABLE cost grew with the catalog size, a bulk
// create was quadratic, and TestE2E_PGReadsThreeLevelCatalogBtree's batches
// timed out near 8000 relations.
//
// PostgreSQL never rebuilds: _bt_doinsert → _bt_split (nbtinsert.c) splits
// the full page into itself plus a new right sibling, and _bt_insert_parent
// adds the right page's downlink to the parent, splitting upward as needed
// and growing a new root (_bt_newroot) when the old root splits. A split
// touches the split pair, the old right sibling's btpo_prev, the parent
// (and, rarely, the pages above it), never the rest of the tree.
//
// This file ports that shape onto goopg's catalog btree page builders:
//
//   - _bt_findsplitloc (nbtsplitloc.c, default strategy): choose the split
//     that minimises |leftfree − rightfree|, or, on the rightmost page of a
//     level, fillfactor-weighted free space (leaf BTREE_DEFAULT_FILLFACTOR
//     90, internal BTREE_NONLEAF_FILLFACTOR 70), so ascending inserts leave
//     their left pages fillfactor full rather than half full.
//   - the left page keeps its block and btpo_prev and gets the first right
//     item as its high key; the right page takes the old high key (none if
//     the left was rightmost) and the old btpo_next; the old right sibling's
//     btpo_prev now names the right page.
//   - an internal split's right page starts with a minus-infinity downlink
//     (the separator's key moves up, nbtinsert.c "the first data item on
//     the right page becomes the minus infinity item").
//
// Write order follows nbtree's rule that a page is written before anything
// that points to it: the new right page, then the left page, then the old
// right sibling, then the parent level, and the metapage last.
//
// Catalog index writes are single-writer at this layer (the same assumption
// insertIntoExistingLeaf and the rebuild make), so no lock coupling or
// incomplete-split flag (BTP_INCOMPLETE_SPLIT) is modelled.

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

const (
	sysBtreeLeafFillfactor    = 90 // BTREE_DEFAULT_FILLFACTOR (nbtree.h)
	sysBtreeNonLeafFillfactor = 70 // BTREE_NONLEAF_FILLFACTOR (nbtree.h)
)

// sysBtreePageImage is one parsed btree page: its high key (nil on the
// rightmost page of a level), its items in slot order, and its special
// area. On an internal page items[0] is the minus-infinity downlink.
type sysBtreePageImage struct {
	highKey []byte
	items   [][]byte
	prev    uint32
	next    uint32
	level   uint32
	flags   uint16
}

func readSysBtreePageImage(ctx *Context, rel storage.RelFileNode, blk uint32) (sysBtreePageImage, error) {
	var img sysBtreePageImage
	slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: storage.BlockNumber(blk)})
	if err != nil {
		return img, fmt.Errorf("pin blk %d: %w", blk, err)
	}
	slot.Lock()
	defer func() {
		slot.Unlock()
		ctx.Pool.Unpin(slot)
	}()
	page := slot.Page()
	count, err := storage.PageLinePointerCount(page)
	if err != nil {
		return img, fmt.Errorf("line pointer count blk %d: %w", blk, err)
	}
	off := storage.BlockSize - sizeOfBTPageOpaque
	le := binary.LittleEndian
	img.prev = le.Uint32(page[off+0 : off+4])
	img.next = le.Uint32(page[off+4 : off+8])
	img.level = le.Uint32(page[off+8 : off+12])
	img.flags = le.Uint16(page[off+12 : off+14])
	for i := uint16(1); i <= uint16(count); i++ {
		raw, err := storage.PageGetItemRawNoCopy(page, i)
		if err != nil {
			return img, fmt.Errorf("read slot %d of blk %d: %w", i, blk, err)
		}
		item := append([]byte(nil), raw...)
		if i == 1 && img.next != pNoneBlock {
			img.highKey = item
			continue
		}
		img.items = append(img.items, item)
	}
	return img, nil
}

// buildSysBtreePage renders img with the leaf or internal builder.
func buildSysBtreePage(img sysBtreePageImage) ([]byte, error) {
	isRoot := img.flags&btpRootFlag != 0
	if img.flags&btpLeafFlag != 0 {
		return buildSysBtreeLeafPage(img.items, img.highKey, img.prev, img.next, isRoot)
	}
	return buildSysBtreeInternalPage(img.items, img.highKey, img.prev, img.next, img.level, isRoot)
}

// writeSysBtreePage overwrites block blk with page and logs a full-page
// image, as every other catalog btree write does.
func writeSysBtreePage(ctx *Context, rel storage.RelFileNode, blk uint32, page []byte) error {
	slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: storage.BlockNumber(blk)})
	if err != nil {
		return fmt.Errorf("pin blk %d: %w", blk, err)
	}
	slot.Lock()
	copy(slot.Page(), page)
	ctx.Pool.MarkDirtyForceFPI(slot)
	slot.Unlock()
	ctx.Pool.Unpin(slot)
	return nil
}

// appendSysBtreePage extends rel by one block holding page and returns it.
func appendSysBtreePage(ctx *Context, rel storage.RelFileNode, page []byte) (uint32, error) {
	slot, blk, err := ctx.Pool.PinNew(rel)
	if err != nil {
		return 0, fmt.Errorf("extend: %w", err)
	}
	slot.Lock()
	copy(slot.Page(), page)
	ctx.Pool.MarkDirtyForceFPI(slot)
	slot.Unlock()
	ctx.Pool.Unpin(slot)
	return uint32(blk), nil
}

// descendSysBtreeWithPath is descendSysBtreeToLeaf that also returns the
// internal pages visited, root first (_bt_search's BTStack).
func descendSysBtreeWithPath(ctx *Context, rel storage.RelFileNode, rootBlk uint32, level uint32, newKey []byte, cmp keyCompareFn) (uint32, []uint32, error) {
	var path []uint32
	cur := rootBlk
	for lvl := int(level); lvl >= 1; lvl-- {
		path = append(path, cur)
		img, err := readSysBtreePageImage(ctx, rel, cur)
		if err != nil {
			return 0, nil, err
		}
		if len(img.items) == 0 {
			return 0, nil, fmt.Errorf("internal blk %d has no downlinks", cur)
		}
		next := decodeChildFromTID(img.items[0])
		for _, it := range img.items[1:] {
			if len(it) <= sysIndexTupleHoff {
				continue
			}
			if cmp(newKey, it[sysIndexTupleHoff:]) < 0 {
				break
			}
			next = decodeChildFromTID(it)
		}
		cur = next
	}
	return cur, path, nil
}

// insertSysBtreeMultiLevel inserts indexTuple into a multi-level catalog
// btree, splitting the target leaf (and its ancestors) when it is full.
func insertSysBtreeMultiLevel(ctx *Context, indexOID uint32, rel storage.RelFileNode, rootBlk, level uint32, indexTuple []byte, cmp keyCompareFn) error {
	meta, ok := keyMetaForSysBtree(indexOID)
	if !ok {
		return fmt.Errorf("split: unsupported system btree OID %d", indexOID)
	}
	leafBlk, path, err := descendSysBtreeWithPath(ctx, rel, rootBlk, level, indexTuple[sysIndexTupleHoff:], cmp)
	if err != nil {
		return fmt.Errorf("descend: %w", err)
	}
	err = insertIntoExistingLeaf(ctx, indexOID, rel, storage.BlockNumber(leafBlk), indexTuple, cmp)
	if err == nil || !errors.Is(err, storage.ErrNoSpaceInPage) {
		return err
	}
	leaf, err := readSysBtreePageImage(ctx, rel, leafBlk)
	if err != nil {
		return err
	}
	merged, _ := mergeSortedSlice(leaf.items, indexTuple, cmp)
	leaf.items = merged
	rightBlk, sepTuple, err := splitSysBtreePage(ctx, rel, leafBlk, leaf, meta.nkeyatts)
	if err != nil {
		return fmt.Errorf("split leaf blk %d: %w", leafBlk, err)
	}
	return insertSysBtreeDownlink(ctx, rel, path, leafBlk, rightBlk, sepTuple, meta.nkeyatts, level)
}

// splitSysBtreePage writes img (already holding the new item) as the pair
// (blk, new right block). It returns the right block and the separator: the
// right page's low key in data-tuple form, to be posted to the parent.
func splitSysBtreePage(ctx *Context, rel storage.RelFileNode, blk uint32, img sysBtreePageImage, nkeyatts uint16) (uint32, []byte, error) {
	isLeaf := img.flags&btpLeafFlag != 0
	k, err := sysBtreeFindSplitLoc(img, isLeaf)
	if err != nil {
		return 0, nil, err
	}
	sep := img.items[k]
	left := sysBtreePageImage{
		highKey: buildSysBtreeLeafHighKey(sep, nkeyatts),
		items:   img.items[:k],
		prev:    img.prev,
		level:   img.level,
		flags:   img.flags &^ btpRootFlag,
	}
	right := sysBtreePageImage{
		highKey: img.highKey,
		items:   append([][]byte(nil), img.items[k:]...),
		prev:    blk,
		next:    img.next,
		level:   img.level,
		flags:   img.flags &^ btpRootFlag,
	}
	if !isLeaf {
		right.items[0] = buildSysBtreeMinusInfDownlink(decodeChildFromTID(sep))
	}
	// The right page goes first: nothing points to it until the left page's
	// btpo_next does.
	rightPage, err := buildSysBtreePage(right)
	if err != nil {
		return 0, nil, fmt.Errorf("build right page: %w", err)
	}
	rightBlk, err := appendSysBtreePage(ctx, rel, rightPage)
	if err != nil {
		return 0, nil, fmt.Errorf("right page: %w", err)
	}
	left.next = rightBlk
	leftPage, err := buildSysBtreePage(left)
	if err != nil {
		return 0, nil, fmt.Errorf("build left page: %w", err)
	}
	if err := writeSysBtreePage(ctx, rel, blk, leftPage); err != nil {
		return 0, nil, fmt.Errorf("left page: %w", err)
	}
	if img.next != pNoneBlock {
		sib, err := readSysBtreePageImage(ctx, rel, img.next)
		if err != nil {
			return 0, nil, fmt.Errorf("right sibling: %w", err)
		}
		sib.prev = rightBlk
		sibPage, err := buildSysBtreePage(sib)
		if err != nil {
			return 0, nil, fmt.Errorf("build right sibling: %w", err)
		}
		if err := writeSysBtreePage(ctx, rel, img.next, sibPage); err != nil {
			return 0, nil, fmt.Errorf("right sibling: %w", err)
		}
	}
	return rightBlk, sep, nil
}

// insertSysBtreeDownlink is _bt_insert_parent: post (sep → rightBlk) to the
// parent of leftBlk, right after leftBlk's own downlink, splitting the
// parent when it is full and growing a new root when the root splits.
// path holds the internal pages from the root down to leftBlk's parent;
// treeLevel is the metapage's btm_level.
func insertSysBtreeDownlink(ctx *Context, rel storage.RelFileNode, path []uint32, leftBlk, rightBlk uint32, sep []byte, nkeyatts uint16, treeLevel uint32) error {
	downlink := buildSysBtreeInternalDownlink(sep, rightBlk, nkeyatts)
	if len(path) == 0 {
		// leftBlk was the root (_bt_newroot).
		root := sysBtreePageImage{
			items: [][]byte{buildSysBtreeMinusInfDownlink(leftBlk), downlink},
			prev:  pNoneBlock,
			next:  pNoneBlock,
			level: treeLevel + 1,
			flags: btpRootFlag,
		}
		rootPage, err := buildSysBtreePage(root)
		if err != nil {
			return fmt.Errorf("build new root: %w", err)
		}
		rootBlk, err := appendSysBtreePage(ctx, rel, rootPage)
		if err != nil {
			return fmt.Errorf("new root: %w", err)
		}
		metaSlot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: 0})
		if err != nil {
			return fmt.Errorf("pin metapage: %w", err)
		}
		metaSlot.Lock()
		err = writeSysBtreeMetapageInPlace(metaSlot.Page(), rootBlk, treeLevel+1)
		if err == nil {
			ctx.Pool.MarkDirtyForceFPI(metaSlot)
		}
		metaSlot.Unlock()
		ctx.Pool.Unpin(metaSlot)
		return err
	}
	parentBlk := path[len(path)-1]
	parent, err := readSysBtreePageImage(ctx, rel, parentBlk)
	if err != nil {
		return err
	}
	pos := -1
	for i, it := range parent.items {
		if decodeChildFromTID(it) == leftBlk {
			pos = i
			break
		}
	}
	if pos < 0 {
		return fmt.Errorf("parent blk %d has no downlink to blk %d", parentBlk, leftBlk)
	}
	items := make([][]byte, 0, len(parent.items)+1)
	items = append(items, parent.items[:pos+1]...)
	items = append(items, downlink)
	items = append(items, parent.items[pos+1:]...)
	parent.items = items
	if page, err := buildSysBtreePage(parent); err == nil {
		return writeSysBtreePage(ctx, rel, parentBlk, page)
	}
	// The parent is full: split it and post its separator one level up.
	newRight, upSep, err := splitSysBtreePage(ctx, rel, parentBlk, parent, nkeyatts)
	if err != nil {
		return fmt.Errorf("split internal blk %d: %w", parentBlk, err)
	}
	return insertSysBtreeDownlink(ctx, rel, path[:len(path)-1], parentBlk, newRight, upSep, nkeyatts, treeLevel)
}

// sysBtreeFindSplitLoc is _bt_findsplitloc's default strategy: the first
// right item k (1 ≤ k < len(items)) minimising the free-space delta, where
// the left page gains items[k] as its high key and keeps the old one only
// on the right. On the rightmost page of a level the delta weighs the left
// page's free space by the fillfactor (nbtsplitloc.c usemult).
func sysBtreeFindSplitLoc(img sysBtreePageImage, isLeaf bool) (int, error) {
	n := len(img.items)
	if n < 2 {
		return 0, fmt.Errorf("cannot split a page with %d items", n)
	}
	payload := storage.BlockSize - storage.SizeOfPageHeaderData - sizeOfBTPageOpaque
	sizes := make([]int, n)
	total := 0
	for i, it := range img.items {
		sizes[i] = len(it) + 4
		total += sizes[i]
	}
	oldHigh := 0
	if img.highKey != nil {
		oldHigh = len(img.highKey) + 4
	}
	rightmost := img.next == pNoneBlock
	mult := 0.5
	if rightmost {
		ff := sysBtreeLeafFillfactor
		if !isLeaf {
			ff = sysBtreeNonLeafFillfactor
		}
		mult = float64(ff) / 100
	}
	best, bestDelta := -1, 0.0
	leftUsed := 0
	for k := 1; k < n; k++ {
		leftUsed += sizes[k-1]
		rightUsed := total - leftUsed + oldHigh
		firstRight := sizes[k]
		if !isLeaf {
			// The right page's first item becomes a bare minus-infinity
			// downlink; its key moves up to the parent.
			rightUsed += sizeOfIndexTupleHdr + 4 - firstRight
		}
		leftfree := payload - (leftUsed + firstRight) // + new high key
		rightfree := payload - rightUsed
		if leftfree < 0 || rightfree < 0 {
			continue
		}
		delta := mult*float64(leftfree) - (1-mult)*float64(rightfree)
		if delta < 0 {
			delta = -delta
		}
		if best < 0 || delta < bestDelta {
			best, bestDelta = k, delta
		}
	}
	if best < 0 {
		return 0, fmt.Errorf("no legal split point for %d items", n)
	}
	return best, nil
}
