package btree

import (
	"encoding/binary"
	"fmt"

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
			keptRaw := make([][]byte, 0, len(kept))
			for _, it := range kept {
				raw := it.marshal()
				if _, err := storage.PageAddItemRaw(slot.Page(), raw); err != nil {
					bt.unpinW(slot)
					return totalRemoved, err
				}
				keptRaw = append(keptRaw, raw)
			}
			if len(kept) == 0 {
				// M0055-0005-followup-two-phase-del: PHASE 1 mark.
				// Set BOTH BTDeleted (existing flag, used by
				// readers / callers as the "this page has been
				// vacuumed empty" signal) AND BTHalfDead — the
				// new two-phase marker that says "PHASE 1 has
				// committed to disk; PHASE 2 unlink remains".
				// Crash-replay restores the half-dead state so
				// a subsequent vacuum or descent picks up where
				// we left off.
				op.Flags |= BTDeleted | BTHalfDead
				writeOpaque(slot.Page(), op)
				emptyLeaves = append(emptyLeaves, emptyLeafInfo{
					blk:      cur,
					firstKey: firstKey,
					prev:     op.Prev,
					next:     op.Next,
				})
			}
			// M0079-0002: emit a logical btree-vacuum record
			// carrying the kept-items projection + post-vacuum
			// opaque flags instead of the prior FPI path. Falls
			// back to FPI via `markDirtyWithPageRecord` when no
			// hook is wired (test harnesses without a WAL
			// writer).
			if logVac := bt.pool.LogBtreeVacuum(); logVac != nil {
				flagsAfter := readOpaque(slot.Page()).Flags
				if err := bt.pool.MarkDirtyChangeRecord(slot, func() (storage.LSN, error) {
					return logVac(bt.rel, cur, keptRaw, flagsAfter)
				}); err != nil {
					bt.unpinW(slot)
					return totalRemoved, err
				}
			} else if err := bt.markDirtyWithPageRecord(slot, cur); err != nil {
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
//
// M0079-0003: when `Pool.LogBtreeUnlinkPage` is wired, all four
// page mutations (left sibling Next, right sibling Prev, parent
// downlink removal, leaf BTHalfDead clear) are covered by a
// single atomic WAL record carrying their control fields. The
// record's end LSN is stamped onto each dirtied page's
// pd_lsn so per-page replay is independently idempotent. Falls
// back to the per-page FPI path when the hook is unset (test
// harnesses without a WAL writer).
func (bt *BTree) unlinkEmptyLeaf(leaf emptyLeafInfo) error {
	emitter := bt.pool.LogBtreeUnlinkPage()
	if emitter == nil {
		return bt.unlinkEmptyLeafFPI(leaf)
	}

	// Resolve the parent block + the slot index of leaf's
	// downlink BEFORE any mutation so the WAL record carries
	// the control fields.
	parentBlk, parentSlot, hasParent, err := bt.resolveParentDownlink(leaf)
	if err != nil {
		return err
	}

	// Compute the post-unlink leaf flags: BTDeleted (existing
	// "this page has been vacuumed empty" signal) plus clear
	// BTHalfDead (Phase 2 complete after the record applies).
	leafFlagsAfter, err := bt.readLeafFlagsAfterUnlink(leaf.blk)
	if err != nil {
		return err
	}

	req := storage.BtreeUnlinkPageRequest{
		LeafBlk:          leaf.blk,
		LeafFlagsAfter:   leafFlagsAfter,
		HasLeftSib:       leaf.prev != storage.InvalidBlockNumber,
		LeftSibBlk:       leaf.prev,
		LeftSibNewNext:   leaf.next,
		HasRightSib:      leaf.next != storage.InvalidBlockNumber,
		RightSibBlk:      leaf.next,
		RightSibNewPrev:  leaf.prev,
		HasParent:        hasParent,
		ParentBlk:        parentBlk,
		ParentRemoveSlot: parentSlot,
	}
	lsn, err := emitter(bt.rel, req)
	if err != nil {
		return fmt.Errorf("btree: emit unlink record: %w", err)
	}

	// Apply each mutation with the unlink record's end LSN as
	// pd_lsn. MarkDirtyWithLSNLocked skips the per-epoch FPI
	// path the FPI fallback would use; we rely on the unlink
	// record itself to reconstruct each page's state during
	// replay.
	if req.HasLeftSib {
		if err := bt.applyOpaqueMutation(req.LeftSibBlk, lsn, func(p storage.Page) {
			op := readOpaque(p)
			op.Next = req.LeftSibNewNext
			writeOpaque(p, op)
		}); err != nil {
			return err
		}
	}
	if req.HasRightSib {
		if err := bt.applyOpaqueMutation(req.RightSibBlk, lsn, func(p storage.Page) {
			op := readOpaque(p)
			op.Prev = req.RightSibNewPrev
			writeOpaque(p, op)
		}); err != nil {
			return err
		}
	}
	if req.HasParent {
		if err := bt.applyParentDownlinkRemoval(req.ParentBlk, req.ParentRemoveSlot, lsn); err != nil {
			return err
		}
	}
	if err := bt.applyOpaqueMutation(req.LeafBlk, lsn, func(p storage.Page) {
		op := readOpaque(p)
		op.Flags = req.LeafFlagsAfter
		writeOpaque(p, op)
	}); err != nil {
		return err
	}

	// M0055-0005 Phase D: page recycling. The unlinked leaf is
	// no longer referenced by parent or siblings; its block can
	// be reused by future allocations on this tree.
	bt.recycleBlock(leaf.blk)
	return nil
}

// resolveParentDownlink finds the parent of `leaf` and the
// 1-based pageItems-order slot index of its downlink. Returns
// `hasParent=false` for the single-page-tree case where the
// leaf is also the root. (M0079-0003.)
func (bt *BTree) resolveParentDownlink(leaf emptyLeafInfo) (storage.BlockNumber, uint16, bool, error) {
	if leaf.firstKey == nil {
		// Leftmost leaf with no keys: walk down from root
		// finding the internal page that downlinks to leaf.blk.
		parent, ok, err := bt.findParentDownlinkByBlock(leaf.blk)
		if err != nil {
			return 0, 0, false, err
		}
		if !ok {
			// Single-page tree (leaf is the root).
			return 0, 0, false, nil
		}
		slot, err := bt.findDownlinkSlotInParent(parent, leaf.blk)
		if err != nil {
			return 0, 0, false, err
		}
		return parent, slot, slot != 0, nil
	}
	_, path, err := bt.descendToLeaf(leaf.firstKey)
	if err != nil {
		return 0, 0, false, err
	}
	if len(path) == 0 {
		return 0, 0, false, nil
	}
	parent := path[len(path)-1]
	slot, err := bt.findDownlinkSlotInParent(parent, leaf.blk)
	if err != nil {
		return 0, 0, false, err
	}
	return parent, slot, slot != 0, nil
}

// findParentDownlinkByBlock walks the tree from the root and
// returns the internal page that holds a downlink to childBlk.
// Used for the leftmost-leaf case where leaf.firstKey is nil.
// (M0079-0003.)
func (bt *BTree) findParentDownlinkByBlock(childBlk storage.BlockNumber) (storage.BlockNumber, bool, error) {
	meta, err := bt.readMeta()
	if err != nil {
		return 0, false, err
	}
	cur := meta.Root
	for {
		slot, err := bt.pinR(cur)
		if err != nil {
			return 0, false, err
		}
		op := readOpaque(slot.Page())
		if op.IsLeaf() {
			bt.unpinR(slot)
			return 0, false, nil
		}
		items, perr := pageItems(slot.Page())
		bt.unpinR(slot)
		if perr != nil {
			return 0, false, perr
		}
		for _, it := range items {
			if it.ptr.Block == childBlk {
				return cur, true, nil
			}
		}
		if len(items) == 0 {
			return 0, false, nil
		}
		cur = items[0].ptr.Block
	}
}

// findDownlinkSlotInParent reads parent's items under a shared
// latch and returns the 1-based slot index whose ptr.Block ==
// childBlk. Returns 0 when not found. (M0079-0003.)
func (bt *BTree) findDownlinkSlotInParent(parentBlk, childBlk storage.BlockNumber) (uint16, error) {
	slot, err := bt.pinR(parentBlk)
	if err != nil {
		return 0, err
	}
	defer bt.unpinR(slot)
	items, err := pageItems(slot.Page())
	if err != nil {
		return 0, err
	}
	for i, it := range items {
		if it.ptr.Block == childBlk {
			return uint16(i + 1), nil
		}
	}
	return 0, nil
}

// readLeafFlagsAfterUnlink computes the post-unlink Flags value
// for the deleted leaf: clear BTHalfDead (Phase 2 complete);
// keep BTDeleted set; preserve everything else. (M0079-0003.)
func (bt *BTree) readLeafFlagsAfterUnlink(leafBlk storage.BlockNumber) (uint16, error) {
	slot, err := bt.pinR(leafBlk)
	if err != nil {
		return 0, err
	}
	op := readOpaque(slot.Page())
	bt.unpinR(slot)
	flags := op.Flags
	flags &^= BTHalfDead
	flags |= BTDeleted
	return flags, nil
}

// applyOpaqueMutation runs `mutate` on the given block under the
// page's exclusive content latch, then stamps `lsn` as pd_lsn
// via MarkDirtyWithLSNLocked. (M0079-0003.)
func (bt *BTree) applyOpaqueMutation(blk storage.BlockNumber, lsn storage.LSN, mutate func(storage.Page)) error {
	s, err := bt.pinW(blk)
	if err != nil {
		return err
	}
	mutate(s.Page())
	bt.pool.MarkDirtyWithLSNLocked(s, lsn)
	bt.unpinW(s)
	return nil
}

// applyParentDownlinkRemoval rewrites the parent's items list
// excluding the removeSlot entry, mirroring
// `removeDownlinkFromParent`'s leftmost-key adoption. (M0079-0003.)
func (bt *BTree) applyParentDownlinkRemoval(parentBlk storage.BlockNumber, removeSlot uint16, lsn storage.LSN) error {
	s, err := bt.pinW(parentBlk)
	if err != nil {
		return err
	}
	items, err := pageItems(s.Page())
	if err != nil {
		bt.unpinW(s)
		return err
	}
	if removeSlot == 0 || int(removeSlot) > len(items) {
		// Already removed; idempotent no-op.
		bt.pool.MarkDirtyWithLSNLocked(s, lsn)
		bt.unpinW(s)
		return nil
	}
	idx := int(removeSlot) - 1
	newItems := make([]item, 0, len(items)-1)
	newItems = append(newItems, items[:idx]...)
	newItems = append(newItems, items[idx+1:]...)
	if len(newItems) > 0 && len(newItems[0].key) > 0 {
		newItems[0] = item{keyLen: 0, ptr: newItems[0].ptr, key: nil}
	}
	resetPageItems(s.Page())
	for _, it := range newItems {
		if _, err := storage.PageAddItemRaw(s.Page(), it.marshal()); err != nil {
			bt.unpinW(s)
			return err
		}
	}
	bt.pool.MarkDirtyWithLSNLocked(s, lsn)
	bt.unpinW(s)
	return nil
}

// unlinkEmptyLeafFPI is the legacy per-page FPI emission path,
// kept as the fallback when LogBtreeUnlinkPage is unwired
// (test harnesses). (M0079-0003.)
func (bt *BTree) unlinkEmptyLeafFPI(leaf emptyLeafInfo) error {
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
	// M0055-0005-followup-two-phase-del: PHASE 2 complete —
	// clear the BTHalfDead marker so future vacuum passes don't
	// retry the unlink. A subsequent crash before recycle is
	// safe: the page is already orphaned from the tree
	// structure; the recycle step is local bookkeeping.
	if err := bt.clearHalfDead(leaf.blk); err != nil {
		return err
	}
	// M0055-0005 Phase D: page recycling. The unlinked leaf is
	// no longer referenced by parent or siblings; its block can
	// be reused by future allocations on this tree.
	bt.recycleBlock(leaf.blk)
	return nil
}

// clearHalfDead (M0055-0005-followup-two-phase-del) clears the
// BTHalfDead marker on a fully-unlinked page. Invoked at the
// end of a successful Phase 2 unlink. Leaving the flag set
// on a not-yet-recycled page is also safe (a later vacuum
// would just re-attempt the now-no-op unlink); but clearing
// it keeps the page-state observable to crash-replay
// inspectors.
func (bt *BTree) clearHalfDead(blk storage.BlockNumber) error {
	slot, err := bt.pinW(blk)
	if err != nil {
		return err
	}
	op := readOpaque(slot.Page())
	op.Flags &^= BTHalfDead
	writeOpaque(slot.Page(), op)
	err = bt.markDirtyWithPageRecord(slot, blk)
	bt.unpinW(slot)
	return err
}

// CompleteDeferredDeletions (M0055-0005-followup-two-phase-del)
// scans the tree for any pages still flagged BTHalfDead (Phase
// 1 committed, Phase 2 not finished — typical post-crash
// state) and finishes the unlink + recycle for each. Called by
// vacuum maintenance and by tests that simulate crashed
// deletes.
func (bt *BTree) CompleteDeferredDeletions() (int, error) {
	rel := bt.rel
	nBlocks, err := bt.pool.NBlocks(rel)
	if err != nil {
		return 0, err
	}
	completed := 0
	for blk := storage.BlockNumber(1); blk < nBlocks; blk++ {
		slot, err := bt.pinR(blk)
		if err != nil {
			continue
		}
		op := readOpaque(slot.Page())
		if !op.IsHalfDead() {
			bt.unpinR(slot)
			continue
		}
		info := emptyLeafInfo{blk: blk, prev: op.Prev, next: op.Next}
		bt.unpinR(slot)
		if err := bt.unlinkEmptyLeaf(info); err != nil {
			return completed, err
		}
		completed++
	}
	return completed, nil
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
