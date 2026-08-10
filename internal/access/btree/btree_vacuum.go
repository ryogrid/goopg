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
	firstKey []byte              // key saved before leaf was emptied (for parent descent)
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
		// Safe to skip even with C3 ItemIDDead marks outstanding: the
		// resurrection hazard (review MUST-FIX 1) requires the HEAP side
		// to reclaim a marked entry's TID, which only happens when heap
		// vacuum found dead tuples — i.e. deadTIDs is non-empty. Marked
		// entries wait for the next real vacuum or the S4 pre-split purge.
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

	unlinkedAny := false
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

		// C3-S1 (review MUST-FIX 1): enumerate INCLUDING ItemIDDead-marked
		// entries — VACUUM must physically drop them inside its logged
		// kept-items rewrite (D3: the mark was verified dead-to-all at mark
		// time, so trusting it is exactly PG's LP_DEAD model). Skipping
		// them here would leave marked entries out of the rewrite while
		// the heap reclaims their TIDs; a crash replays them back as
		// Normal pointing at recycled heap slots.
		items, itemDead, err := pageItemsWithDead(slot.Page())
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
		for i, it := range items {
			if itemDead[i] || deadSet[tidKey(it.ptr)] {
				totalRemoved++
			} else {
				kept = append(kept, it)
			}
		}

		next := op.Next

		var justEmptied *emptyLeafInfo
		if len(kept) < len(items) {
			resetPageItems(slot.Page())
			for _, it := range kept {
				raw := it.marshal()
				if _, err := storage.PageAddItemRaw(slot.Page(), raw); err != nil {
					bt.unpinW(slot)
					return totalRemoved, err
				}
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
				justEmptied = &emptyLeafInfo{
					blk:      cur,
					firstKey: firstKey,
					prev:     op.Prev,
					next:     op.Next,
				}
			}
			// M0079-0002: emit a logical btree-vacuum record
			// carrying the kept-items projection + post-vacuum
			// opaque flags instead of the prior FPI path. Falls
			// back to FPI via `markDirtyWithPageRecord` when no
			// hook is wired (test harnesses without a WAL
			// writer).
			// O-C3-5: the rewrite dropped every dead-marked item, so the
			// garbage hint clears unconditionally (stale-set is harmless
			// but pointless to persist through a logged rewrite).
			if opAfter := readOpaque(slot.Page()); opAfter.HasGarbage() {
				opAfter.Flags &^= BTHasGarbage
				writeOpaque(slot.Page(), opAfter)
			}
			if logVac := bt.pool.LogBtreeVacuum(); logVac != nil {
				// A8: the record carries the post-vacuum page as a full-page
				// image, so pass the mutated page rather than the kept items.
				if err := bt.pool.MarkDirtyChangeRecord(slot, func() (storage.LSN, error) {
					return logVac(bt.rel, cur, slot.Page())
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

		// M-NIGHTLY (AI-20260706-201855-001, loop 9): unlink the
		// now-empty leaf IMMEDIATELY, in this same iteration,
		// rather than deferring it to a second pass over a batch
		// collected across the WHOLE leftmost-to-rightmost scan.
		// The old batched design left a leaf flagged
		// BTHalfDead|BTDeleted — with its parent downlink still
		// live — for as long as the rest of the (possibly
		// thousand-leaf) scan took, wide open for a concurrent
		// fast-path insert (tryInsertNoSplit never checks
		// BTHalfDead/BTDeleted) to land on it before the deferred
		// unlink discarded it. Unlinking here shrinks that window
		// to the handful of instructions inside unlinkEmptyLeaf
		// itself, which now also re-verifies emptiness under
		// splitMu before doing anything destructive.
		if justEmptied != nil {
			if err := bt.unlinkEmptyLeaf(*justEmptied); err != nil {
				return totalRemoved, err
			}
			unlinkedAny = true
		}

		cur = next
	}

	// If the entire tree is now empty, reset it to a single empty root so
	// that subsequent Inserts work without needing a full rebuild.
	if unlinkedAny {
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
	// M-NIGHTLY (AI-20260706-201855-001): resolveParentDownlink
	// below captures the parent's downlink slot index for the WAL
	// record, well before applyParentDownlinkRemoval actually
	// mutates the page. Between the two, a concurrent Insert-driven
	// split (Insert/finishSplit, both under splitMu) can insert a
	// new downlink into the SAME parent page ahead of the captured
	// slot, shifting every later index right. Splits already
	// serialise every OTHER structural mutation through splitMu;
	// taking it here too closes the gap for splits originating from
	// THIS *BTree Go instance. (M0122-0010: a split from a DIFFERENT
	// connection's instance is NOT covered by splitMu — see
	// applyParentDownlinkRemoval's own doc comment for how that
	// cross-connection case is closed independently, by re-locating
	// the downlink by block identity instead of trusting the index.)
	bt.splitMu.Lock()
	defer bt.splitMu.Unlock()

	// M-NIGHTLY (AI-20260706-201855-001, loop 9): the caller
	// (VacuumIndexPages) marks a leaf BTHalfDead|BTDeleted while
	// scanning it, then unpins it and moves on — it does NOT hold
	// splitMu or any lock spanning the marking and this unlink
	// call. The fast, non-split Insert path (tryInsertNoSplit)
	// never checks BTHalfDead/BTDeleted before writing: it only
	// checks keyExceedsHighKey/pageHasSpaceFor, and the leaf's
	// parent downlink is still intact at this point (removed only
	// below), so a concurrent insert can land on this exact leaf
	// and add a live tuple before we get here. Blindly proceeding
	// would silently discard that tuple (unlinkEmptyLeaf's steps
	// below never re-read the item area) and then hand the block
	// to recycleBlock for a completely unrelated split to reuse —
	// this is a real, independently confirmed corruption source,
	// not merely a defensive check. Re-verify the leaf is still
	// physically empty before doing anything destructive; if a
	// racing insert repopulated it, revert the phase-1 marking and
	// leave the (now live again) page alone instead of unlinking it.
	recheckSlot, err := bt.pinW(leaf.blk)
	if err != nil {
		return err
	}
	recheckOp := readOpaque(recheckSlot.Page())
	count, cerr := PGDataItemCount(recheckSlot.Page())
	if cerr != nil {
		bt.unpinW(recheckSlot)
		return cerr
	}
	if count > 0 {
		recheckOp.Flags &^= BTDeleted | BTHalfDead
		writeOpaque(recheckSlot.Page(), recheckOp)
		if err := bt.markDirtyWithPageRecord(recheckSlot, leaf.blk); err != nil {
			bt.unpinW(recheckSlot)
			return err
		}
		bt.unpinW(recheckSlot)
		return nil
	}
	bt.unpinW(recheckSlot)

	emitter := bt.pool.LogBtreeUnlinkPage()
	if emitter == nil {
		return bt.unlinkEmptyLeafFPI(leaf)
	}

	// Resolve the parent block + the slot index of leaf's
	// downlink BEFORE any mutation so the WAL record carries
	// the control fields.
	parentBlk, parentSlot, hasParent, ancestorPath, err := bt.resolveParentDownlink(leaf)
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

	// M0110-0010: relink the nearest *live* siblings, not the
	// captured neighbours. When an adjacent run of leaves is
	// deleted in one pass, leaf.prev/leaf.next may themselves be
	// deleted-in-this-pass blocks; trusting them leaves a
	// survivor's btpo_prev/btpo_next pointing at a deleted block.
	// Walk past any deleted/half-dead page (their original links
	// remain navigable — recycleBlock does not wipe the page) to
	// the live page just outside the run. Order-independent, so it
	// is correct for both the in-pass batch and CompleteDeferredDeletions.
	leftLive, err := bt.liveSibling(leaf.prev, false)
	if err != nil {
		return err
	}
	rightLive, err := bt.liveSibling(leaf.next, true)
	if err != nil {
		return err
	}

	req := storage.BtreeUnlinkPageRequest{
		LeafBlk:          leaf.blk,
		LeafFlagsAfter:   leafFlagsAfter,
		HasLeftSib:       leftLive != storage.InvalidBlockNumber,
		LeftSibBlk:       leftLive,
		LeftSibNewNext:   rightLive,
		HasRightSib:      rightLive != storage.InvalidBlockNumber,
		RightSibBlk:      rightLive,
		RightSibNewPrev:  leftLive,
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
	//
	// M-NIGHTLY (AI-20260709-010336-082, 3rd pgbench reopen): do NOT
	// blindly stamp the leftLive/rightLive values captured by the
	// liveSibling walk above. bt.splitMu (held for this whole
	// function) only serialises against OTHER structural mutations on
	// THIS *BTree Go instance -- it does NOT serialise across
	// connections, and each backend opens its own *BTree per
	// statement (see btree.go's splitMu doc comment). A concurrent
	// Insert-driven split on a DIFFERENT connection's *BTree instance
	// for the SAME relation can splice a brand-new live page into the
	// chain between the walk above and the writes below (its own
	// sibling relink is safe -- it re-reads fresh under its own pinW
	// immediately before writing). Blindly applying the stale
	// leftLive/rightLive here would stomp that split's correct relink
	// right back to the pre-split (now wrong) neighbour -- this is
	// the exact mechanism that produced block 678's persistent
	// "left link/right link pair not in agreement" corruption
	// (confirmed on-disk: true chain 677->15798->678, but 678's
	// btpo_prev stayed 677). Fix: re-derive the live neighbour from
	// this block's CURRENT on-disk link, under the same pinW that
	// performs the write -- a no-op if nothing raced, self-correcting
	// if it did.
	if req.HasLeftSib {
		var walkErr error
		if err := bt.applyOpaqueMutation(req.LeftSibBlk, lsn, func(p storage.Page) {
			op := readOpaque(p)
			newNext, werr := bt.liveSibling(op.Next, true)
			if werr != nil {
				walkErr = werr
				return
			}
			op.Next = newNext
			writeOpaque(p, op)
		}); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
	}
	if req.HasRightSib {
		var walkErr error
		if err := bt.applyOpaqueMutation(req.RightSibBlk, lsn, func(p storage.Page) {
			op := readOpaque(p)
			newPrev, werr := bt.liveSibling(op.Prev, false)
			if werr != nil {
				walkErr = werr
				return
			}
			op.Prev = newPrev
			writeOpaque(p, op)
		}); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
	}
	if req.HasParent {
		if err := bt.applyParentDownlinkRemoval(req.ParentBlk, leaf.blk, lsn); err != nil {
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

	// M-NIGHTLY (AI-20260706-201855-001): removing leaf.blk's
	// downlink above may have dropped req.ParentBlk's item count
	// to 0. An empty non-root internal page left in the tree is a
	// live, linked, contentless node — the next descent that
	// routes through it (its separator range in ITS OWN parent is
	// untouched) hits findChildBlockDirect's `count == 0` guard
	// and raises "btree: empty internal page", independent of any
	// race. Cascade the same unlink one level up for as long as
	// removing a downlink keeps emptying the next ancestor.
	if req.HasParent {
		if err := bt.maybeCascadeEmptyInternal(req.ParentBlk, ancestorPath[:len(ancestorPath)-1]); err != nil {
			return err
		}
	}
	return nil
}

// liveSibling walks the leaf sibling chain starting from `start`,
// skipping any page that is itself BTDeleted or BTHalfDead (i.e.
// a leaf being unlinked in the same vacuum pass), and returns the
// nearest LIVE leaf block. `forward` true walks via btpo_next
// (rightward), false via btpo_prev (leftward). Returns
// InvalidBlockNumber when the chain end is reached without finding
// a live page (the run extends to the edge of the level).
//
// M0110-0010: deleted/half-dead pages retain their original
// btpo_prev/btpo_next (recycleBlock does not wipe the page), so the
// chain through them stays navigable until the block is reused.
// PHASE 1 of VacuumIndexPages stamps BTDeleted|BTHalfDead on every
// target leaf BEFORE any unlink runs, so this walk recognises the
// whole adjacent run from the very first unlink — making the result
// independent of the order the run's leaves are processed in.
func (bt *BTree) liveSibling(start storage.BlockNumber, forward bool) (storage.BlockNumber, error) {
	cur := start
	for steps := 0; cur != storage.InvalidBlockNumber; steps++ {
		s, err := bt.pinR(cur)
		if err != nil {
			return storage.InvalidBlockNumber, err
		}
		op := readOpaque(s.Page())
		bt.unpinR(s)
		if !op.IsDeleted() && !op.IsHalfDead() {
			return cur, nil // nearest live page
		}
		if forward {
			cur = op.Next
		} else {
			cur = op.Prev
		}
		// Guard against a malformed/cyclic chain of dead pages.
		if steps > 1<<24 {
			return storage.InvalidBlockNumber, fmt.Errorf(
				"btree: sibling chain walk exceeded bound from block %d", start)
		}
	}
	return storage.InvalidBlockNumber, nil
}

// resolveParentDownlink finds the parent of `leaf` and the
// 1-based pageItems-order slot index of its downlink. Returns
// `hasParent=false` for the single-page-tree case where the
// leaf is also the root. (M0079-0003.)
//
// The returned ancestorPath is the full root..parent chain
// (inclusive of the parent, i.e. ancestorPath[len-1] == the
// returned parent block) — M-NIGHTLY (AI-20260706-201855-001)
// threads this through so a caller whose downlink removal empties
// the parent can keep cascading upward (maybeCascadeEmptyInternal)
// without re-deriving the chain, which would no longer be possible
// once the parent itself holds zero items.
func (bt *BTree) resolveParentDownlink(leaf emptyLeafInfo) (storage.BlockNumber, uint16, bool, []storage.BlockNumber, error) {
	if leaf.firstKey == nil {
		// Leftmost leaf with no keys: walk down from root
		// finding the internal page that downlinks to leaf.blk.
		parent, ancestorPath, ok, err := bt.findParentDownlinkByBlock(leaf.blk)
		if err != nil {
			return 0, 0, false, nil, err
		}
		if !ok {
			// Single-page tree (leaf is the root).
			return 0, 0, false, nil, nil
		}
		slot, err := bt.findDownlinkSlotInParent(parent, leaf.blk)
		if err != nil {
			return 0, 0, false, nil, err
		}
		return parent, slot, slot != 0, ancestorPath, nil
	}
	_, path, err := bt.descendToLeaf(leaf.firstKey)
	if err != nil {
		return 0, 0, false, nil, err
	}
	if len(path) == 0 {
		return 0, 0, false, nil, nil
	}
	parent := path[len(path)-1]
	slot, err := bt.findDownlinkSlotInParent(parent, leaf.blk)
	if err != nil {
		return 0, 0, false, nil, err
	}
	return parent, slot, slot != 0, path, nil
}

// findParentDownlinkByBlock walks the tree from the root and
// returns the internal page that holds a downlink to childBlk,
// together with the full root..parent ancestor chain (inclusive
// of the returned parent — M-NIGHTLY cascade support, see
// resolveParentDownlink). Used for the leftmost-leaf case where
// leaf.firstKey is nil. (M0079-0003.)
func (bt *BTree) findParentDownlinkByBlock(childBlk storage.BlockNumber) (storage.BlockNumber, []storage.BlockNumber, bool, error) {
	meta, err := bt.readMeta()
	if err != nil {
		return 0, nil, false, err
	}
	var path []storage.BlockNumber
	cur := meta.Root
	for {
		slot, err := bt.pinR(cur)
		if err != nil {
			return 0, nil, false, err
		}
		op := readOpaque(slot.Page())
		if op.IsLeaf() {
			bt.unpinR(slot)
			return 0, nil, false, nil
		}
		items, perr := pageItems(slot.Page())
		bt.unpinR(slot)
		if perr != nil {
			return 0, nil, false, perr
		}
		for _, it := range items {
			if it.ptr.Block == childBlk {
				return cur, append(path, cur), true, nil
			}
		}
		if len(items) == 0 {
			return 0, nil, false, nil
		}
		path = append(path, cur)
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

// applyParentDownlinkRemoval rewrites the parent's items list,
// removing the downlink to childBlk, mirroring
// `removeDownlinkFromParent`'s leftmost-key adoption. (M0079-0003.)
//
// M0122-0010 (AI-20260709-010336-082 follow-up): the caller resolves
// a slot INDEX well before this runs (WAL record emission plus the
// sibling-relink writes above it in unlinkEmptyLeaf/
// unlinkEmptyInternalPage both happen in between). bt.splitMu only
// serialises structural writes within THIS *BTree Go instance, not
// across connections (each backend opens its own instance per
// statement — see btree.go's splitMu doc comment), so a concurrent
// Insert-driven split on a DIFFERENT connection's instance can splice
// a new downlink into parentBlk ahead of the captured slot, shifting
// every later index right. Removing by trusted index would then
// delete an unrelated LIVE child's downlink instead of childBlk's.
// Re-locate the target by block identity under this same pinW —
// mirrors findParentDownlinkByBlock's matching, self-correcting if a
// split raced, a no-op if the downlink was already removed by a
// racing unlink (findParentDownlinkByBlock's twin, WAL-replay
// idempotency case).
func (bt *BTree) applyParentDownlinkRemoval(parentBlk, childBlk storage.BlockNumber, lsn storage.LSN) error {
	s, err := bt.pinW(parentBlk)
	if err != nil {
		return err
	}
	items, err := pageItems(s.Page())
	if err != nil {
		bt.unpinW(s)
		return err
	}
	idx := -1
	for i, it := range items {
		if it.ptr.Block == childBlk {
			idx = i
			break
		}
	}
	if idx < 0 {
		// Already removed; idempotent no-op.
		bt.pool.MarkDirtyWithLSNLocked(s, lsn)
		bt.unpinW(s)
		return nil
	}
	newItems := make([]item, 0, len(items)-1)
	newItems = append(newItems, items[:idx]...)
	newItems = append(newItems, items[idx+1:]...)
	if len(newItems) > 0 && len(newItems[0].key) > 0 {
		newItems[0] = item{ptr: newItems[0].ptr, key: nil}
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
	// M0110-0010: relink the nearest *live* siblings (see the
	// sibling-path twin unlinkEmptyLeaf for the rationale) so an
	// adjacent deleted run never leaves a survivor pointing at a
	// deleted block.
	leftLive, err := bt.liveSibling(leaf.prev, false)
	if err != nil {
		return err
	}
	rightLive, err := bt.liveSibling(leaf.next, true)
	if err != nil {
		return err
	}

	// Update left sibling's Next. M-NIGHTLY (AI-20260709-010336-082):
	// re-derive the live neighbour from the sibling's CURRENT on-disk
	// link under this pinW, not the stale leftLive/rightLive captured
	// above — see the WAL-path twin unlinkEmptyLeaf for the full
	// cross-connection race rationale.
	if leftLive != storage.InvalidBlockNumber {
		s, err := bt.pinW(leftLive)
		if err != nil {
			return err
		}
		op := readOpaque(s.Page())
		newNext, werr := bt.liveSibling(op.Next, true)
		if werr != nil {
			bt.unpinW(s)
			return werr
		}
		if err := pgWriteNextSibling(s.Page(), op, newNext); err != nil {
			bt.unpinW(s)
			return err
		}
		err = bt.markDirtyWithPageRecord(s, leftLive)
		bt.unpinW(s)
		if err != nil {
			return err
		}
	}

	// Update right sibling's Prev.
	if rightLive != storage.InvalidBlockNumber {
		s, err := bt.pinW(rightLive)
		if err != nil {
			return err
		}
		op := readOpaque(s.Page())
		newPrev, werr := bt.liveSibling(op.Prev, false)
		if werr != nil {
			bt.unpinW(s)
			return werr
		}
		op.Prev = newPrev
		writeOpaque(s.Page(), op)
		err = bt.markDirtyWithPageRecord(s, rightLive)
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
		// M-NIGHTLY (AI-20260706-201855-001): pre-existing gap,
		// left as-is — this branch has never called clearHalfDead
		// / recycleBlock (only removeParentDownlinkByBlock runs),
		// so it is bug-compatible with prior behaviour; only the
		// cascade attempt is new.
		parentBlk, ancestorPath, hasParent, err := bt.removeParentDownlinkByBlock(leaf.blk)
		if err != nil || !hasParent {
			return err
		}
		return bt.maybeCascadeEmptyInternal(parentBlk, ancestorPath[:len(ancestorPath)-1])
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
	// M-NIGHTLY (AI-20260706-201855-001): see the WAL-path twin in
	// unlinkEmptyLeaf for the full rationale.
	return bt.maybeCascadeEmptyInternal(parentBlk, path[:len(path)-1])
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

// maybeCascadeEmptyInternal is invoked after a downlink removal from
// blk potentially dropped its item count to 0. When that happens and
// blk is not the tree root, blk itself must be unlinked from ITS OWN
// parent the same way an empty leaf is unlinked — otherwise a live
// routing key whose separator range still points through blk hits a
// linked-but-contentless internal page and findChildBlockDirect
// raises "btree: empty internal page". This is a real bug surfaced
// by nightly pgbench churn on tiny, heavily-vacuumed indexes
// (M-NIGHTLY AI-20260706-201855-001): repeated single-row
// insert/delete/vacuum cycles on a small table build up enough
// b-tree levels that an entire non-root internal page's leaf
// children can all be vacuum-unlinked in one pass, emptying it.
//
// ancestorPath is the root..parent-of-blk chain (blk itself already
// popped off) captured by the caller before blk's own downlink was
// removed — once blk holds zero items it no longer has a key to
// re-derive its parent chain by descent, so this must be threaded
// through rather than recomputed.
//
// Loops upward: a single VacuumIndexPages pass that empties an
// entire multi-level subtree cascades level by level until it hits a
// still-populated ancestor or the root.
//
// NOT crash-safe across cascade steps: unlike leaf unlinking (which
// marks BTHalfDead in a separate PHASE 1 pass before any structural
// mutation, so CompleteDeferredDeletions can finish an interrupted
// unlink after a crash), this cascade detects-and-unlinks an internal
// page synchronously with no phase-1 marker of its own. A crash
// between cascading level N and N+1 leaves level N's now-empty,
// still-linked parent exposed to the same "empty internal page" bug
// one level higher. Each individual cascade step is still atomically
// WAL-logged (same emitter/replay path as a leaf unlink), so this is
// a narrower gap than the original bug, not a new corruption source —
// deferred to `.ralph/deferral_ledger.md` (M-NIGHTLY) rather than
// solved in this pass: extend CompleteDeferredDeletions to also scan
// for and cascade non-root internal pages left with zero items.
func (bt *BTree) maybeCascadeEmptyInternal(blk storage.BlockNumber, ancestorPath []storage.BlockNumber) error {
	for {
		slot, err := bt.pinR(blk)
		if err != nil {
			return err
		}
		op := readOpaque(slot.Page())
		if op.IsRoot() || op.IsDeleted() {
			bt.unpinR(slot)
			return nil
		}
		count, cerr := PGDataItemCount(slot.Page())
		prev, next := op.Prev, op.Next
		bt.unpinR(slot)
		if cerr != nil {
			return cerr
		}
		if count > 0 {
			return nil
		}
		if len(ancestorPath) == 0 {
			return fmt.Errorf("btree: cascade found empty non-root internal page %d with no recorded ancestor", blk)
		}
		parentBlk := ancestorPath[len(ancestorPath)-1]
		if err := bt.unlinkEmptyInternalPage(blk, prev, next, parentBlk); err != nil {
			return err
		}
		blk = parentBlk
		ancestorPath = ancestorPath[:len(ancestorPath)-1]
	}
}

// unlinkEmptyInternalPage unlinks a non-root internal page that has
// just reached zero items: it relinks the nearest live level
// siblings around blk, removes blk's downlink from parentBlk, flags
// blk BTDeleted, and recycles its block. Mirrors unlinkEmptyLeaf /
// unlinkEmptyLeafFPI's structural-mutation shape (M-NIGHTLY,
// AI-20260706-201855-001); kept as a separate, page-agnostic
// implementation rather than folded into the leaf path because the
// leaf path's PHASE 1 (BTHalfDead) marking has no analogue here (see
// maybeCascadeEmptyInternal's crash-safety note).
func (bt *BTree) unlinkEmptyInternalPage(blk, prev, next, parentBlk storage.BlockNumber) error {
	parentSlot, err := bt.findDownlinkSlotInParent(parentBlk, blk)
	if err != nil {
		return err
	}
	if parentSlot == 0 {
		// Already removed — idempotent no-op.
		return nil
	}

	leftLive, err := bt.liveSibling(prev, false)
	if err != nil {
		return err
	}
	rightLive, err := bt.liveSibling(next, true)
	if err != nil {
		return err
	}

	emitter := bt.pool.LogBtreeUnlinkPage()
	if emitter == nil {
		return bt.unlinkEmptyInternalPageFPI(blk, parentBlk, leftLive, rightLive)
	}

	flagsAfter, err := bt.readInternalFlagsAfterUnlink(blk)
	if err != nil {
		return err
	}
	req := storage.BtreeUnlinkPageRequest{
		LeafBlk:          blk,
		LeafFlagsAfter:   flagsAfter,
		HasLeftSib:       leftLive != storage.InvalidBlockNumber,
		LeftSibBlk:       leftLive,
		LeftSibNewNext:   rightLive,
		HasRightSib:      rightLive != storage.InvalidBlockNumber,
		RightSibBlk:      rightLive,
		RightSibNewPrev:  leftLive,
		HasParent:        true,
		ParentBlk:        parentBlk,
		ParentRemoveSlot: parentSlot,
	}
	lsn, err := emitter(bt.rel, req)
	if err != nil {
		return fmt.Errorf("btree: emit internal-page unlink record: %w", err)
	}
	// M-NIGHTLY (AI-20260709-010336-082 follow-up): do NOT blindly
	// stamp the leftLive/rightLive values captured by the liveSibling
	// walk above — mirrors unlinkEmptyLeaf's fix for the identical
	// stale-sibling-relink race, just at the internal-page level.
	// bt.splitMu only serialises structural mutations within THIS
	// *BTree Go instance; each backend opens its own instance per
	// statement, so a concurrent Insert-driven split on a DIFFERENT
	// connection's instance for the SAME relation can splice a new
	// live page into this exact chain segment between the walk above
	// and the writes below. Re-derive the live neighbour from the
	// sibling's CURRENT on-disk link, under the same pinW that
	// performs the write — a no-op if nothing raced, self-correcting
	// if it did.
	if req.HasLeftSib {
		var walkErr error
		if err := bt.applyOpaqueMutation(req.LeftSibBlk, lsn, func(p storage.Page) {
			op := readOpaque(p)
			newNext, werr := bt.liveSibling(op.Next, true)
			if werr != nil {
				walkErr = werr
				return
			}
			op.Next = newNext
			writeOpaque(p, op)
		}); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
	}
	if req.HasRightSib {
		var walkErr error
		if err := bt.applyOpaqueMutation(req.RightSibBlk, lsn, func(p storage.Page) {
			op := readOpaque(p)
			newPrev, werr := bt.liveSibling(op.Prev, false)
			if werr != nil {
				walkErr = werr
				return
			}
			op.Prev = newPrev
			writeOpaque(p, op)
		}); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
	}
	if err := bt.applyParentDownlinkRemoval(req.ParentBlk, blk, lsn); err != nil {
		return err
	}
	if err := bt.applyOpaqueMutation(req.LeafBlk, lsn, func(p storage.Page) {
		op := readOpaque(p)
		op.Flags = req.LeafFlagsAfter
		writeOpaque(p, op)
	}); err != nil {
		return err
	}
	bt.recycleBlock(blk)
	return nil
}

// unlinkEmptyInternalPageFPI is the legacy per-page FPI fallback for
// unlinkEmptyInternalPage, used when LogBtreeUnlinkPage is unwired
// (test harnesses without a WAL writer) — mirrors
// unlinkEmptyLeafFPI's shape.
func (bt *BTree) unlinkEmptyInternalPageFPI(blk, parentBlk, leftLive, rightLive storage.BlockNumber) error {
	// M-NIGHTLY (AI-20260709-010336-082 follow-up): re-derive the live
	// neighbour from the sibling's CURRENT on-disk link under this
	// pinW, not the stale leftLive/rightLive captured by the caller —
	// see unlinkEmptyInternalPage's WAL-path twin and
	// unlinkEmptyLeafFPI for the full cross-connection race rationale.
	if leftLive != storage.InvalidBlockNumber {
		s, err := bt.pinW(leftLive)
		if err != nil {
			return err
		}
		op := readOpaque(s.Page())
		newNext, werr := bt.liveSibling(op.Next, true)
		if werr != nil {
			bt.unpinW(s)
			return werr
		}
		if err := pgWriteNextSibling(s.Page(), op, newNext); err != nil {
			bt.unpinW(s)
			return err
		}
		err = bt.markDirtyWithPageRecord(s, leftLive)
		bt.unpinW(s)
		if err != nil {
			return err
		}
	}
	if rightLive != storage.InvalidBlockNumber {
		s, err := bt.pinW(rightLive)
		if err != nil {
			return err
		}
		op := readOpaque(s.Page())
		newPrev, werr := bt.liveSibling(op.Prev, false)
		if werr != nil {
			bt.unpinW(s)
			return werr
		}
		op.Prev = newPrev
		writeOpaque(s.Page(), op)
		err = bt.markDirtyWithPageRecord(s, rightLive)
		bt.unpinW(s)
		if err != nil {
			return err
		}
	}
	if err := bt.removeDownlinkFromParent(parentBlk, blk); err != nil {
		return err
	}
	s, err := bt.pinW(blk)
	if err != nil {
		return err
	}
	op := readOpaque(s.Page())
	op.Flags |= BTDeleted
	writeOpaque(s.Page(), op)
	err = bt.markDirtyWithPageRecord(s, blk)
	bt.unpinW(s)
	if err != nil {
		return err
	}
	bt.recycleBlock(blk)
	return nil
}

// readInternalFlagsAfterUnlink computes the post-unlink Flags value
// for a cascaded internal page: existing flags plus BTDeleted. Unlike
// readLeafFlagsAfterUnlink, there is no BTHalfDead to clear — the
// cascade has no phase-1 marker (see maybeCascadeEmptyInternal).
func (bt *BTree) readInternalFlagsAfterUnlink(blk storage.BlockNumber) (uint16, error) {
	slot, err := bt.pinR(blk)
	if err != nil {
		return 0, err
	}
	op := readOpaque(slot.Page())
	bt.unpinR(slot)
	return op.Flags | BTDeleted, nil
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
// Returns hasParent=false when blk is the root (nothing to remove).
// The returned ancestorPath is root..parent inclusive, for cascading
// (M-NIGHTLY, see resolveParentDownlink).
func (bt *BTree) removeParentDownlinkByBlock(blk storage.BlockNumber) (parentBlk storage.BlockNumber, ancestorPath []storage.BlockNumber, hasParent bool, err error) {
	parent, path, ok, err := bt.findParentDownlinkByBlock(blk)
	if err != nil || !ok {
		return 0, nil, false, err
	}
	if err := bt.removeDownlinkFromParent(parent, blk); err != nil {
		return 0, nil, false, err
	}
	return parent, path, true, nil
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
		newItems[0] = item{ptr: newItems[0].ptr, key: nil}
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
		count, countErr := PGDataItemCount(slot.Page())
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

	// M0079-0004 / A8: emit a single PG RM_BTREE new-root record covering
	// both the empty-leaf-root page (backup block 0) and the updated metapage
	// (backup block 2) as full-page images. The metapage is mutated in memory
	// HERE — before the emit — so its post-op bytes ride the same record; both
	// pages' pd_lsn is then stamped to the record LSN. Each newroot holds its
	// own private root slot then the shared metapage, so co-locking is
	// deadlock-free even across connections. Falls back to per-page FPI when
	// the hook is unset.
	if emitter := bt.pool.LogBtreeNewRoot(); emitter != nil {
		metaSlot, err := bt.pinW(MetaBlock)
		if err != nil {
			bt.unpinW(rootSlot)
			return err
		}
		m := ReadPGMetaPage(metaSlot.Page())
		m.Root = rootBlk
		m.Level = 0
		m.FastRoot = rootBlk
		m.FastLevel = 0
		WritePGMetaPage(metaSlot.Page(), m)
		lsn, err := emitter(bt.rel, rootBlk, rootSlot.Page(), MetaBlock, metaSlot.Page())
		if err != nil {
			bt.unpinW(metaSlot)
			bt.unpinW(rootSlot)
			return err
		}
		bt.pool.MarkDirtyWithLSNLocked(rootSlot, lsn)
		bt.pool.MarkDirtyWithLSNLocked(metaSlot, lsn)
		bt.unpinW(metaSlot)
		bt.unpinW(rootSlot)
		return nil
	}
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
	count, err := PGDataItemCount(p)
	if err != nil || count == 0 {
		return storage.InvalidBlockNumber
	}
	raw, err := pgGetItemRaw(p, 1)
	if err != nil || len(raw) < SizeOfIndexTupleData {
		return storage.InvalidBlockNumber
	}
	return storage.BlockNumber(binary.LittleEndian.Uint32(raw[2:6]))
}
