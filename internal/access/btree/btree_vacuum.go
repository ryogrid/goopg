package btree

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/storage"
)

// tidKey converts a heap ItemPointer to a uint64 for use as a map key.
func tidKey(tid storage.ItemPointer) uint64 {
	return uint64(tid.Block)<<16 | uint64(tid.Offset)
}

// emptyLeafInfo records state needed to unlink and delete an empty leaf.
type emptyLeafInfo struct {
	blk      storage.BlockNumber
	firstKey []byte             // key saved before leaf was emptied (for parent descent)
	prev     storage.BlockNumber // Prev from opaque
	next     storage.BlockNumber // Next from opaque
}

// VacuumIndexPages removes B-tree leaf entries that point to dead heap
// tuples, then unlinks any leaf pages that become empty as a result.
// When the entire tree is empty after pruning, the tree is reset to a
// single empty leaf root so subsequent Inserts work correctly.
//
// deadTIDs is the list of dead heap (block, offset) pointers collected
// during the heap VACUUM pass.
//
// Returns the number of index entries removed.
func (bt *BTree) VacuumIndexPages(deadTIDs []storage.ItemPointer) (int, error) {
	if len(deadTIDs) == 0 {
		return 0, nil
	}

	// Build O(1) dead-set lookup.
	deadSet := make(map[uint64]bool, len(deadTIDs))
	for _, tid := range deadTIDs {
		deadSet[tidKey(tid)] = true
	}

	// Walk all leaf pages left-to-right via the Next chain.
	leftmostBlk, err := bt.findLeftmostLeaf()
	if err != nil {
		return 0, err
	}

	var emptyLeaves []emptyLeafInfo
	totalRemoved := 0

	for cur := leftmostBlk; cur != storage.InvalidBlockNumber; {
		slot, err := bt.pinW(cur)
		if err != nil {
			return totalRemoved, err
		}
		op := readOpaque(slot.Page())

		if !op.IsLeaf() {
			bt.unpinW(slot)
			break
		}

		items, err := pageItems(slot.Page())
		if err != nil {
			bt.unpinW(slot)
			return totalRemoved, err
		}

		// Filter dead items.
		var firstKey []byte
		if len(items) > 0 {
			firstKey = append([]byte(nil), items[0].key...)
		}

		var kept []item
		for _, it := range items {
			if deadSet[tidKey(it.ptr)] {
				totalRemoved++
			} else {
				kept = append(kept, it)
			}
		}

		next := op.Next

		if len(kept) < len(items) {
			resetPageItems(slot.Page())
			for _, it := range kept {
				if _, err := storage.PageAddItemRaw(slot.Page(), it.marshal()); err != nil {
					bt.unpinW(slot)
					return totalRemoved, err
				}
			}
			if len(kept) == 0 {
				// Leaf is now empty — mark it for unlinking.
				op.Flags |= BTDeleted
				writeOpaque(slot.Page(), op)
				emptyLeaves = append(emptyLeaves, emptyLeafInfo{
					blk:      cur,
					firstKey: firstKey,
					prev:     op.Prev,
					next:     op.Next,
				})
			}
			if err := bt.markDirtyWithPageRecord(slot, cur); err != nil {
				bt.unpinW(slot)
				return totalRemoved, err
			}
		}

		bt.unpinW(slot)
		cur = next
	}

	// Unlink empty leaves from the sibling chain and remove their
	// downlinks from parent internal pages.
	for _, leaf := range emptyLeaves {
		if err := bt.unlinkEmptyLeaf(leaf); err != nil {
			return totalRemoved, err
		}
	}

	// If the entire tree is now empty, reset it to a single empty root so
	// that subsequent Inserts work without needing a full rebuild.
	if len(emptyLeaves) > 0 {
		empty, err := bt.isTreeEmpty()
		if err != nil {
			return totalRemoved, err
		}
		if empty {
			if err := bt.resetToEmptyRoot(); err != nil {
				return totalRemoved, err
			}
		}
	}

	return totalRemoved, nil
}

// findLeftmostLeaf returns the block number of the leftmost leaf page by
// descending from the root always following the first child pointer.
func (bt *BTree) findLeftmostLeaf() (storage.BlockNumber, error) {
	meta, err := bt.readMeta()
	if err != nil {
		return storage.InvalidBlockNumber, err
	}
	cur := meta.Root
	for {
		slot, err := bt.pinR(cur)
		if err != nil {
			return storage.InvalidBlockNumber, err
		}
		op := readOpaque(slot.Page())
		if op.IsLeaf() {
			bt.unpinR(slot)
			return cur, nil
		}
		// Internal page: follow leftmost child (first item's ptr).
		items, err := pageItems(slot.Page())
		bt.unpinR(slot)
		if err != nil {
			return storage.InvalidBlockNumber, err
		}
		if len(items) == 0 {
			// Empty internal page — tree might be broken; stop.
			return storage.InvalidBlockNumber, nil
		}
		cur = items[0].ptr.Block
	}
}

// unlinkEmptyLeaf updates sibling Prev/Next pointers to bypass the empty
// leaf and removes the leaf's downlink from its parent internal page.
func (bt *BTree) unlinkEmptyLeaf(leaf emptyLeafInfo) error {
	// Update left sibling's Next.
	if leaf.prev != storage.InvalidBlockNumber {
		s, err := bt.pinW(leaf.prev)
		if err != nil {
			return err
		}
		op := readOpaque(s.Page())
		op.Next = leaf.next
		writeOpaque(s.Page(), op)
		err = bt.markDirtyWithPageRecord(s, leaf.prev)
		bt.unpinW(s)
		if err != nil {
			return err
		}
	}

	// Update right sibling's Prev.
	if leaf.next != storage.InvalidBlockNumber {
		s, err := bt.pinW(leaf.next)
		if err != nil {
			return err
		}
		op := readOpaque(s.Page())
		op.Prev = leaf.prev
		writeOpaque(s.Page(), op)
		err = bt.markDirtyWithPageRecord(s, leaf.next)
		bt.unpinW(s)
		if err != nil {
			return err
		}
	}

	// Remove downlink from parent. We re-descend to find the parent; the
	// internal pages still carry the downlink to leaf.blk even though the
	// leaf is now empty, so descending with firstKey correctly routes to
	// leaf.blk's parent chain.
	if leaf.firstKey == nil {
		// Leftmost leaf with no keys — use right sibling to find parent.
		return bt.removeParentDownlinkByBlock(leaf.blk)
	}
	_, path, err := bt.descendToLeaf(leaf.firstKey)
	if err != nil {
		return err
	}
	if len(path) == 0 {
		// Single-page tree (leaf is also root) — nothing to remove.
		return nil
	}
	parentBlk := path[len(path)-1]
	if err := bt.removeDownlinkFromParent(parentBlk, leaf.blk); err != nil {
		return err
	}
	// M0055-0005 Phase D: page recycling. The unlinked leaf is
	// no longer referenced by parent or siblings; its block can
	// be reused by future allocations on this tree.
	bt.recycleBlock(leaf.blk)
	return nil
}

// removeParentDownlinkByBlock finds the parent of blk by walking the
// tree and removes the corresponding downlink. Used when the leaf has
// no saved firstKey (e.g. it was the leftmost leaf with no items).
func (bt *BTree) removeParentDownlinkByBlock(blk storage.BlockNumber) error {
	meta, err := bt.readMeta()
	if err != nil {
		return err
	}
	cur := meta.Root
	for {
		slot, err := bt.pinR(cur)
		if err != nil {
			return err
		}
		op := readOpaque(slot.Page())
		if op.IsLeaf() {
			bt.unpinR(slot)
			return nil // shouldn't reach leaf when searching for internal
		}
		items, pageErr := pageItems(slot.Page())
		bt.unpinR(slot)
		if pageErr != nil {
			return pageErr
		}
		for _, it := range items {
			if it.ptr.Block == blk {
				// Found it — remove downlink here.
				return bt.removeDownlinkFromParent(cur, blk)
			}
		}
		// Descend into any child that might contain blk.
		if len(items) > 0 {
			cur = items[0].ptr.Block
		} else {
			return nil
		}
	}
}

// removeDownlinkFromParent removes the item with ptr.Block == childBlk
// from the internal page at parentBlk and rewrites the page.
func (bt *BTree) removeDownlinkFromParent(parentBlk, childBlk storage.BlockNumber) error {
	s, err := bt.pinW(parentBlk)
	if err != nil {
		return err
	}
	items, err := pageItems(s.Page())
	if err != nil {
		bt.unpinW(s)
		return err
	}

	var newItems []item
	for _, it := range items {
		if it.ptr.Block != childBlk {
			newItems = append(newItems, it)
		}
	}

	if len(newItems) == len(items) {
		// childBlk not found; already removed or was never here.
		bt.unpinW(s)
		return nil
	}

	// If the removed item was the leftmost (nil key), the new first item
	// must adopt nil key to maintain the B-tree invariant.
	if len(newItems) > 0 && len(newItems[0].key) > 0 {
		newItems[0] = item{keyLen: 0, ptr: newItems[0].ptr, key: nil}
	}

	resetPageItems(s.Page())
	for _, it := range newItems {
		if _, addErr := storage.PageAddItemRaw(s.Page(), it.marshal()); addErr != nil {
			bt.unpinW(s)
			return addErr
		}
	}

	err = bt.markDirtyWithPageRecord(s, parentBlk)
	bt.unpinW(s)
	return err
}

// isTreeEmpty returns true when the B-tree has no leaf entries (i.e. all
// leaves are empty or deleted). It walks the leaf chain from leftmost.
func (bt *BTree) isTreeEmpty() (bool, error) {
	leftmost, err := bt.findLeftmostLeaf()
	if err != nil || leftmost == storage.InvalidBlockNumber {
		return true, err
	}
	for cur := leftmost; cur != storage.InvalidBlockNumber; {
		slot, err := bt.pinR(cur)
		if err != nil {
			return false, err
		}
		op := readOpaque(slot.Page())
		if !op.IsLeaf() {
			bt.unpinR(slot)
			break
		}
		count, countErr := storage.PageLinePointerCount(slot.Page())
		next := op.Next
		bt.unpinR(slot)
		if countErr != nil {
			return false, countErr
		}
		if count > 0 {
			return false, nil // found a non-empty leaf
		}
		cur = next
	}
	return true, nil
}

// resetToEmptyRoot reinitialises the B-tree as a single empty leaf root.
// Block 0 (metapage) is preserved; block 1 is overwritten as a fresh
// empty leaf+root page. All other blocks become unreferenced orphans —
// the pool may evict them and the file space is recovered at next VACUUM
// or by simply dropping and recreating the index.
func (bt *BTree) resetToEmptyRoot() error {
	// Pin block 1 (original root or first data page) and reinitialise it.
	// If it doesn't exist yet (relation has only the metapage), allocate.
	nBlocks, err := bt.pool.NBlocks(bt.rel)
	if err != nil {
		return err
	}

	var rootSlot *storage.Slot
	var rootBlk storage.BlockNumber = 1

	if nBlocks > 1 {
		// Block 1 exists — reuse it.
		rootSlot, err = bt.pinW(1)
		if err != nil {
			return err
		}
	} else {
		// Allocate a fresh block.
		var blk storage.BlockNumber
		rootSlot, blk, err = bt.pool.PinNew(bt.rel)
		if err != nil {
			return err
		}
		rootSlot.Lock()
		rootBlk = blk
	}

	initPage(rootSlot.Page(), BTPageOpaque{
		Prev:  storage.InvalidBlockNumber,
		Next:  storage.InvalidBlockNumber,
		Level: 0,
		Flags: BTLeaf | BTRoot,
	})
	if err := bt.markDirtyWithPageRecord(rootSlot, rootBlk); err != nil {
		bt.unpinW(rootSlot)
		return err
	}
	bt.unpinW(rootSlot)

	return bt.updateRootMeta(rootBlk, 0)
}

// readInternalFirstChildBlock reads the first child block from an
// internal B-tree page without fully decoding all items. Returns
// InvalidBlockNumber if the page is empty or not an internal page.
func readInternalFirstChildBlock(p storage.Page) storage.BlockNumber {
	op := readOpaque(p)
	if op.IsLeaf() {
		return storage.InvalidBlockNumber
	}
	count, err := storage.PageLinePointerCount(p)
	if err != nil || count == 0 {
		return storage.InvalidBlockNumber
	}
	raw, err := storage.PageGetItemRaw(p, 1)
	if err != nil || len(raw) < itemPrefixSize {
		return storage.InvalidBlockNumber
	}
	return storage.BlockNumber(binary.LittleEndian.Uint32(raw[2:6]))
}
