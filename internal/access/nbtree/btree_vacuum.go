package nbtree

import (
	"errors"
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
		items, itemDead, err := bt.format().pageItemsWithDead(slot.Page())
		if err != nil {
			bt.unpinW(slot)
			return totalRemoved, err
		}

		// Filter dead items.
		var firstKey []byte
		if len(items) > 0 {
			firstKey = append([]byte(nil), items[0].key...)
		}

		// M0130-S11.5c: PG's xl_btree_vacuum names the doomed items by OFFSET
		// NUMBER, which is only possible while the item list maps one-to-one
		// onto line pointers. A posting list expands into several items here
		// and its survivors are re-marshalled individually below, so the
		// rewrite changes the page's item count in a way offset numbers cannot
		// describe — in that case collect nothing and let the encoder log a
		// full-page image. The pre-vacuum page goes to the encoder as well: it
		// verifies that replaying these offsets reproduces the page written
		// here (see btree.CheckVacuumDelete) rather than trusting this loop.
		lineCount, err := PGDataItemCount(slot.Page())
		if err != nil {
			bt.unpinW(slot)
			return totalRemoved, err
		}
		byOffset := lineCount == len(items)
		var prePage storage.Page
		var deletedOffs []uint16
		if byOffset {
			prePage = make(storage.Page, storage.BlockSize)
			copy(prePage, slot.Page())
		}

		var kept []item
		for i, it := range items {
			if itemDead[i] || deadSet[tidKey(it.ptr)] {
				totalRemoved++
				if byOffset {
					deletedOffs = append(deletedOffs, pgPhysOffnum(slot.Page(), i))
				}
			} else {
				kept = append(kept, it)
			}
		}

		next := op.Next

		var justEmptied *emptyLeafInfo
		if len(kept) < len(items) {
			resetPageItems(slot.Page())
			for _, it := range kept {
				raw := bt.format().marshal(it)
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
				// M0130-S11.5c: hand the encoder both pages plus the deleted
				// offsets; it picks the incremental xl_btree_vacuum form when
				// they reproduce this page and a full-page image when they do
				// not (posting lists above, or the page that just went empty —
				// its BTDeleted|BTHalfDead stamp is no part of any vacuum redo).
				if err := bt.pool.MarkDirtyChangeRecord(slot, func() (storage.LSN, error) {
					return logVac(bt.rel, cur, prePage, slot.Page(), deletedOffs)
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
		items, err := bt.format().pageItems(slot.Page())
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

// errUnlinkChainUnstable reports that the page-deletion protocol could not
// obtain a coherent, stable set of latches around the page it wanted to
// delete: a concurrent split kept splicing a live page into the sibling
// chain between the (unlatched) neighbour walk and the latch acquisition, or
// the chain itself is malformed. Upstream's `_bt_unlink_halfdead_page` has
// the same retry loop around `_bt_lock_and_validate_left`, and gives up on
// the deletion rather than proceeding on stale links; so do we — the callers
// abandon, leaving an empty-but-live page in the tree. M0130-S11.5d-3b.
var errUnlinkChainUnstable = errors.New("btree: sibling chain unstable under the page-deletion protocol")

// unlinkPinAttempts bounds the acquire/validate retry loop. A racing split has
// to win the same race this many times in a row to make us give up.
const unlinkPinAttempts = 10

// unlinkPins is the set of exclusive content latches the page-deletion
// protocol holds for the WHOLE compute→emit→write window (M0130-S11.5d-3b).
//
// Why the window has to be that wide: before S11.5d-3b, goopg computed the
// nearest live neighbours unlatched, emitted the unlink record with those
// values, and then RE-DERIVED each sibling link under the write latch that
// performed the write — because a split on another connection's *BTree can
// splice a live page into the chain in between (AI-20260709-010336-082).
// That made the record's link fields advisory: the primary deliberately wrote
// something other than what it had logged, and redo therefore could not
// reproduce the primary's page. No PG-shaped record may carry an advisory
// field, so the fix is upstream's: hold the latches instead of re-deriving
// under them, and the values computed are the values logged are the values
// written.
type unlinkPins struct {
	// parent is latched FIRST and, unlike the three level-siblings, is one
	// level UP. Order matters: the internal-split path pins a lower-level
	// child while holding this level's latches (S11.5b-3's
	// BTP_INCOMPLETE_SPLIT clear), i.e. goopg latches strictly top-down, so
	// a page deletion that took the parent last could deadlock against it.
	parent *storage.Slot
	// left, target, right are latched in that order — strictly left→right
	// along the sibling chain, the same direction `_bt_split` takes
	// (blk → rightBlk → oldNext), so the two cannot deadlock either.
	left, target, right *storage.Slot
	leftBlk, rightBlk   storage.BlockNumber
}

// releaseSiblings drops just the three level-siblings (reverse acquisition
// order), keeping the parent latch for another attempt.
func (u *unlinkPins) releaseSiblings(bt *BTree) {
	if u.right != nil {
		bt.unpinW(u.right)
		u.right = nil
	}
	if u.target != nil {
		bt.unpinW(u.target)
		u.target = nil
	}
	if u.left != nil {
		bt.unpinW(u.left)
		u.left = nil
	}
	u.leftBlk, u.rightBlk = storage.InvalidBlockNumber, storage.InvalidBlockNumber
}

// release drops every latch the protocol holds. Callers MUST release before
// doing anything that pins a page again (abandonHalfDeadLeaf,
// maybeCascadeEmptyInternal) — the content latch is not reentrant.
func (u *unlinkPins) release(bt *BTree) {
	u.releaseSiblings(bt)
	if u.parent != nil {
		bt.unpinW(u.parent)
		u.parent = nil
	}
}

// acquireUnlinkPins implements the acquisition half of upstream's
// page-deletion protocol (`_bt_unlink_halfdead_page`, nbtpage.c): resolve the
// nearest LIVE neighbours of `target`, latch parent → left → target → right,
// then re-validate the links UNDER those latches. If anything moved, drop the
// sibling latches and recompute — a split that spliced a live page next to
// our left neighbour simply makes the next attempt pick that page as the new
// left neighbour, which is the same self-correction the old re-derive-under-
// the-write did, except it now happens BEFORE the record is emitted.
//
// Returns errUnlinkChainUnstable when the retries are exhausted or the walk
// produces a set of blocks that cannot all be latched at once (a cyclic dead
// run); the caller abandons the deletion. M0130-S11.5d-3b.
func (bt *BTree) acquireUnlinkPins(target, parentBlk storage.BlockNumber, hasParent bool) (*unlinkPins, error) {
	pins := &unlinkPins{
		leftBlk:  storage.InvalidBlockNumber,
		rightBlk: storage.InvalidBlockNumber,
	}
	if hasParent {
		s, err := bt.pinW(parentBlk)
		if err != nil {
			return nil, err
		}
		pins.parent = s
	}
	for attempt := 0; attempt < unlinkPinAttempts; attempt++ {
		// Snapshot the target's own links unlatched, then walk outward
		// past the run of pages being deleted in this same pass. The
		// walk takes shared latches on pages we do not hold, and the
		// parent is a level up, so it cannot self-deadlock.
		ts, err := bt.pinR(target)
		if err != nil {
			pins.release(bt)
			return nil, err
		}
		targetOp := readOpaque(ts.Page())
		bt.unpinR(ts)
		prevAt, nextAt := targetOp.Prev, targetOp.Next

		leftLive, leftAdj, err := bt.liveSiblingLinked(target, prevAt, false)
		if err != nil {
			pins.release(bt)
			return nil, err
		}
		rightLive, rightAdj, err := bt.liveSiblingLinked(target, nextAt, true)
		if err != nil {
			pins.release(bt)
			return nil, err
		}
		// A block can only be latched once: a dead run that loops back
		// onto the target or onto the other neighbour is malformed, and
		// latching it twice would deadlock this goroutine against
		// itself. Refuse the deletion instead.
		if leftLive == target || rightLive == target ||
			(leftLive != storage.InvalidBlockNumber && leftLive == rightLive) {
			pins.release(bt)
			return nil, errUnlinkChainUnstable
		}

		if leftLive != storage.InvalidBlockNumber {
			if pins.left, err = bt.pinW(leftLive); err != nil {
				pins.release(bt)
				return nil, err
			}
		}
		if pins.target, err = bt.pinW(target); err != nil {
			pins.release(bt)
			return nil, err
		}
		if rightLive != storage.InvalidBlockNumber {
			if pins.right, err = bt.pinW(rightLive); err != nil {
				pins.release(bt)
				return nil, err
			}
		}
		pins.leftBlk, pins.rightBlk = leftLive, rightLive

		// Validate under the latches. The walk is only trustworthy if
		// every link it traversed still holds; checking the two ends
		// plus the target is sufficient, because the pages in between
		// are dead (half-dead/deleted) and no split ever splices next
		// to a dead page — only next to the live page it is splitting,
		// which is exactly `leftLive` (its btpo_next changes) or a page
		// outside the run.
		held := readOpaque(pins.target.Page())
		stable := held.Prev == prevAt && held.Next == nextAt
		if stable && pins.left != nil {
			stable = readOpaque(pins.left.Page()).Next == leftAdj
		}
		if stable && pins.right != nil {
			stable = readOpaque(pins.right.Page()).Prev == rightAdj
		}
		if stable {
			return pins, nil
		}
		pins.releaseSiblings(bt)
	}
	pins.release(bt)
	return nil, errUnlinkChainUnstable
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

	// Resolve the parent block (and the ancestor path the cascade needs)
	// BEFORE any mutation. The slot index it also returns is used only by the
	// FPI fallback now: since S11.5d-3b-2 the WAL path re-finds the downlink
	// by child identity on the LATCHED parent, and the record carries a
	// poffset read from that page rather than a value captured earlier.
	parentBlk, _, hasParent, ancestorPath, err := bt.resolveParentDownlink(leaf)
	if err != nil {
		return err
	}

	// M0130-S11.5d-3a: upstream cannot delete a page whose downlink is
	// its parent's rightmost item — the retarget mutation the parent
	// limb now performs has no right neighbour to absorb the key range
	// (`_bt_lock_subtree_parent`). Upstream abandons the deletion and
	// leaves the empty page linked in the tree; so do we. This is
	// tested BEFORE the emitter branch so both the WAL path and the FPI
	// fallback below refuse identically, and before ANY mutation so the
	// refusal never leaves a half-relinked page behind.
	//
	// It is also what keeps the empty-internal-page hazard
	// (AI-20260706-201855-001) structurally unreachable from this path:
	// the last item of an internal page is by definition its rightmost
	// child, so a downlink removal can no longer take a page to zero
	// items.
	emitter := bt.pool.LogBtreeUnlinkPage()
	halfDeadEmitter := bt.pool.LogBtreeMarkPageHalfDead()
	if emitter == nil || halfDeadEmitter == nil {
		// The FPI fallback keeps the pre-S11.5d-3b shape deliberately: it
		// logs whole page images, so it has no record control fields that
		// could go stale, and its re-derive-under-the-write is still the
		// right answer there. It makes the same refusal, off an unlatched
		// read of the parent.
		//
		// S11.5d-3b-2: the deletion now needs BOTH emitters — the two
		// upstream phases are two records — so either one missing sends the
		// whole deletion down this path rather than logging half of it.
		if hasParent {
			isRightmost, found, rerr := bt.parentDownlinkIsRightmostChild(parentBlk, leaf.blk)
			if rerr != nil {
				return rerr
			}
			if found && isRightmost {
				return bt.abandonHalfDeadLeaf(leaf.blk)
			}
		}
		return bt.unlinkEmptyLeafFPI(leaf)
	}

	// M0130-S11.5d-3b: upstream's protocol — pin left/target/right (plus the
	// parent), THEN compute, THEN emit, THEN write, all under one set of
	// latches. Everything below this line reads and writes pinned pages
	// only; nothing is re-derived after the record is emitted, which is what
	// makes the record's link fields authoritative rather than advisory.
	// M0110-0010's "relink the nearest LIVE sibling, not the captured
	// neighbour" rule lives inside acquireUnlinkPins now: an adjacent run of
	// leaves deleted in the same pass is walked past, so a survivor never
	// ends up pointing at a deleted block.
	pins, err := bt.acquireUnlinkPins(leaf.blk, parentBlk, hasParent)
	if err != nil {
		if errors.Is(err, errUnlinkChainUnstable) {
			// Could not get a stable view of the chain; leave the page
			// empty-but-live exactly as the rightmost-child refusal does.
			return bt.abandonHalfDeadLeaf(leaf.blk)
		}
		return err
	}

	// M0130-S11.5d-3a: upstream cannot delete a page whose downlink is
	// its parent's rightmost item — the retarget mutation the parent
	// limb performs has no right neighbour to absorb the key range
	// (`_bt_lock_subtree_parent`). Upstream abandons the deletion and
	// leaves the empty page linked in the tree; so do we. Since S11.5d-3b
	// this is read off the LATCHED parent, and still before ANY mutation,
	// so the refusal never leaves a half-relinked page behind.
	//
	// It is also what keeps the empty-internal-page hazard
	// (AI-20260706-201855-001) structurally unreachable from this path:
	// the last item of an internal page is by definition its rightmost
	// child, so a downlink removal can no longer take a page to zero
	// items.
	//
	// S11.5d-3b-2 adds two more refusals of the same kind — shapes that
	// upstream never produces and that its two records therefore cannot
	// express, so emitting them would leave a standby unable to replay:
	//
	//   * NO PARENT (or no downlink found in it): `_bt_mark_page_halfdead`
	//     exists to take the downlink out of a subtree parent, and
	//     `btree_xlog_mark_page_halfdead` reads block 1 unconditionally. A
	//     leaf nothing points at is not deletable through this protocol.
	//   * RIGHTMOST TARGET: `btree_xlog_unlink_page` reads block 2 (the right
	//     sibling) unconditionally, so "no right sibling" has no
	//     representation in the record at all — consistent with upstream,
	//     which never deletes a rightmost page because that would have to
	//     update the parent's high key.
	//
	// Refusing leaves the page empty-but-live, exactly as the rightmost-child
	// refusal does.
	var poffset uint16
	if pins.parent != nil {
		off, isRightmost, found, ferr := PGFindDownlinkOffset(pins.parent.Page(), leaf.blk)
		if ferr != nil {
			pins.release(bt)
			return ferr
		}
		if !found || isRightmost {
			pins.release(bt)
			return bt.abandonHalfDeadLeaf(leaf.blk)
		}
		poffset = off
	}
	if pins.parent == nil || pins.rightBlk == storage.InvalidBlockNumber {
		pins.release(bt)
		return bt.abandonHalfDeadLeaf(leaf.blk)
	}

	// ---- PHASE 1: xl_btree_mark_page_halfdead (M0130-S11.5d-3b-2) ----
	//
	// Upstream's `_bt_mark_page_halfdead`: retarget-and-delete the downlink in
	// the subtree parent, and rewrite the leaf as a half-dead page whose only
	// item is a dummy high key carrying the top parent of the deleted subtree.
	// goopg deletes one page at a time, so the leaf IS the top parent and the
	// link is InvalidBlockNumber — upstream writes exactly that in the same
	// case.
	//
	// Both mutations are applied by the SAME functions the record's redo runs
	// (ReplayMarkHalfDeadLeaf / ReplayParentRetargetByChild), which is the
	// point: a primary that computed its own version of "what half-dead looks
	// like" could drift from the standby that rebuilds the page from the
	// record alone.
	targetOp := readOpaque(pins.target.Page())
	lsn1, err := halfDeadEmitter(bt.rel, storage.BtreeMarkPageHalfDeadRequest{
		LeafBlk:   leaf.blk,
		LeafPage:  pins.target.Page(),
		ParentBlk: parentBlk,
		POffset:   poffset,
		TopParent: storage.InvalidBlockNumber,
	})
	if err != nil {
		pins.release(bt)
		return fmt.Errorf("btree: emit mark-halfdead record: %w", err)
	}
	if err := ReplayMarkHalfDeadLeaf(pins.target.Page(), targetOp.Prev, targetOp.Next, storage.InvalidBlockNumber); err != nil {
		pins.release(bt)
		return fmt.Errorf("btree: mark block %d half-dead: %w", leaf.blk, err)
	}
	bt.pool.MarkDirtyWithLSNLocked(pins.target, lsn1)
	// Upstream's RETARGET-and-delete, via the very same
	// ReplayParentRetargetByChild the record's redo runs (S11.5d-3a); it
	// locates the item by CHILD BLOCK identity rather than by `poffset`, so
	// primary and redo cannot disagree about which item moved.
	if err := ReplayParentRetargetByChild(pins.parent.Page(), leaf.blk); err != nil {
		pins.release(bt)
		return fmt.Errorf("btree: parent retarget on block %d for child %d: %w", parentBlk, leaf.blk, err)
	}
	// M0131-S26b: the leaf (block 0) is registered WILL_INIT — redo rebuilds it
	// from the record — but the PARENT is a bare block reference carrying
	// neither image nor data, so it still owes its first-touch FPI for the
	// epoch. MarkDirtyCoveredByRecordLocked emits that image when needed and
	// raises pd_lsn without advancing the native-image watermark.
	bt.pool.MarkDirtyCoveredByRecordLocked(pins.parent, lsn1)

	// ---- PHASE 2: xl_btree_unlink_page (M0130-S11.5d-3b-2) ----
	//
	// The record's leftsib/rightsib/level are read off the TARGET IMAGE, and
	// the image handed to the encoder is the post-mutation one, produced by
	// the redo function itself. That indirection is not ceremony: goopg
	// relinks the nearest LIVE siblings (a vacuum pass marks a whole adjacent
	// run dead before unlinking any of it), so the target's own
	// btpo_prev/btpo_next are NOT the blocks being relinked and an encoder
	// reading the pre-mutation page would describe a different mutation from
	// the one performed.
	//
	// The safexid is read from the XID counter here, exactly where upstream
	// reads it (`_bt_unlink_halfdead_page`, nbtpage.c:2646 —
	// `safexid = ReadNextFullTransactionId(); BTPageSetDeleted(page, safexid)`).
	// It is the tombstone horizon: any scan that already descended to this
	// block started before this XID, so the block cannot be handed to a new
	// allocation until no snapshot reaches back that far (M0130-S11.5d-3c;
	// enforced by pinNewOrRecycled via PGPageIsRecyclable). 0 when no horizon
	// source is wired, which reads as "recyclable immediately" and reproduces
	// the pre-S11.5d-3c behaviour.
	leftsib, rightsib := pgSibling(pins.leftBlk), pgSibling(pins.rightBlk)
	safexid := bt.nextSafeXid()
	targetAfter := make(storage.Page, storage.BlockSize)
	copy(targetAfter, pins.target.Page())
	if err := ReplayUnlinkTargetPage(targetAfter, leftsib, rightsib, targetOp.Level, safexid); err != nil {
		pins.release(bt)
		return fmt.Errorf("btree: unlink target image for block %d: %w", leaf.blk, err)
	}
	lsn, err := emitter(bt.rel, storage.BtreeUnlinkPageRequest{
		TargetBlk:  leaf.blk,
		TargetPage: targetAfter,
		SafeXid:    safexid,
		LeafBlk:    leaf.blk,
		MetaBlk:    storage.InvalidBlockNumber,
	})
	if err != nil {
		pins.release(bt)
		return fmt.Errorf("btree: emit unlink record: %w", err)
	}

	// Apply each mutation on the page we already hold, with the unlink
	// record's end LSN as pd_lsn. The TARGET (block 0, and the leaf as block
	// 3) is registered WILL_INIT: redo rebuilds it wholesale from the record,
	// so its pd_lsn may advance the native-image watermark — the record IS the
	// image. The two SIBLINGS (blocks 1 and 2) are bare block references
	// carrying neither image nor data; redo re-derives their single link
	// rewrite from the page it finds, which presupposes an untorn page.
	// M0131-S26b: they therefore keep the per-epoch first-touch FPI that
	// MarkDirtyCoveredByRecordLocked emits — exactly upstream's ordinary
	// buffer registration — instead of suppressing it via
	// MarkDirtyWithLSNLocked. Either way the replayed values are byte-for-byte
	// the values the record carries, which is what makes the stamp sound.
	//
	// Historical note (M-NIGHTLY AI-20260709-010336-082, 3rd pgbench
	// reopen): this used to re-derive each live neighbour from the block's
	// CURRENT on-disk link under the write pin, because a concurrent
	// Insert-driven split on a DIFFERENT connection's *BTree instance can
	// splice a brand-new live page into this chain (bt.splitMu serialises
	// only within one instance), and stamping the stale walk result stomped
	// that split's relink — the mechanism behind block 678's persistent
	// "left link/right link pair not in agreement" corruption. S11.5d-3b
	// closes the same race at the front instead of the back: the split
	// cannot splice anything in while we hold these latches, and if it won
	// the race before we took them, acquireUnlinkPins saw the changed link
	// and recomputed. Do NOT reintroduce a post-emit re-derivation here —
	// it would make the record advisory again.
	if pins.left != nil {
		if err := ReplayUnlinkLeftSibling(pins.left.Page(), rightsib); err != nil {
			pins.release(bt)
			return fmt.Errorf("btree: unlink left sibling %d: %w", pins.leftBlk, err)
		}
		bt.pool.MarkDirtyCoveredByRecordLocked(pins.left, lsn)
	}
	if pins.right != nil {
		if err := ReplayUnlinkRightSibling(pins.right.Page(), leftsib); err != nil {
			pins.release(bt)
			return fmt.Errorf("btree: unlink right sibling %d: %w", pins.rightBlk, err)
		}
		bt.pool.MarkDirtyCoveredByRecordLocked(pins.right, lsn)
	}
	// The image the record already carries, byte for byte — not a second,
	// independently computed rewrite of the same page.
	copy(pins.target.Page(), targetAfter)
	bt.pool.MarkDirtyWithLSNLocked(pins.target, lsn)
	pins.release(bt)

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
	if hasParent {
		if err := bt.maybeCascadeEmptyInternal(parentBlk, ancestorPath[:len(ancestorPath)-1]); err != nil {
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
	live, _, err := bt.liveSiblingLinked(storage.InvalidBlockNumber, start, forward)
	return live, err
}

// liveSiblingLinked is liveSibling plus the one extra fact the S11.5d-3b
// acquisition protocol needs to validate its walk after the fact: the block
// the live page links BACK to along the walk (`adjacent`) — i.e. the value
// the live page's own facing link (btpo_next when walking left, btpo_prev
// when walking right) must still hold for the chain `live … from` to be
// intact. `from` is the page the walk started next to (the deletion target);
// pass InvalidBlockNumber when only the live block is wanted.
func (bt *BTree) liveSiblingLinked(from, start storage.BlockNumber, forward bool) (live, adjacent storage.BlockNumber, err error) {
	adjacent = from
	cur := start
	for steps := 0; cur != storage.InvalidBlockNumber; steps++ {
		s, err := bt.pinR(cur)
		if err != nil {
			return storage.InvalidBlockNumber, storage.InvalidBlockNumber, err
		}
		op := readOpaque(s.Page())
		bt.unpinR(s)
		if !op.IsDeleted() && !op.IsHalfDead() {
			return cur, adjacent, nil // nearest live page
		}
		adjacent = cur
		if forward {
			cur = op.Next
		} else {
			cur = op.Prev
		}
		// Guard against a malformed/cyclic chain of dead pages.
		if steps > 1<<24 {
			return storage.InvalidBlockNumber, storage.InvalidBlockNumber, fmt.Errorf(
				"btree: sibling chain walk exceeded bound from block %d", start)
		}
	}
	return storage.InvalidBlockNumber, storage.InvalidBlockNumber, nil
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
		items, perr := bt.format().pageItems(slot.Page())
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
	items, err := bt.format().pageItems(slot.Page())
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

// abandonHalfDeadLeaf reverts VacuumIndexPages' phase-1 marking on a
// leaf whose deletion turned out to be structurally impossible
// (ErrParentRightmostChild): the page is empty but still live and
// linked, exactly as upstream leaves it when `_bt_pagedel` gives up.
// Leaving BTHalfDead|BTDeleted set instead would make the page
// invisible to liveSibling and eligible for recycleBlock while its
// parent downlink is still intact. M0130-S11.5d-3a.
func (bt *BTree) abandonHalfDeadLeaf(blk storage.BlockNumber) error {
	s, err := bt.pinW(blk)
	if err != nil {
		return err
	}
	op := readOpaque(s.Page())
	if op.Flags&(BTDeleted|BTHalfDead) == 0 {
		bt.unpinW(s)
		return nil
	}
	op.Flags &^= BTDeleted | BTHalfDead
	writeOpaque(s.Page(), op)
	err = bt.markDirtyWithPageRecord(s, blk)
	bt.unpinW(s)
	return err
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
	emitter := bt.pool.LogBtreeUnlinkPage()
	if emitter != nil {
		// M0130-S11.5d-3b-2: a STANDALONE internal-page deletion has no
		// PostgreSQL record shape. Upstream never deletes an internal page on
		// its own — it deletes a leaf-rooted SUBTREE, and every
		// xl_btree_unlink_page whose target is internal registers the
		// subtree's half-dead leaf as block 3, which
		// `btree_xlog_unlink_page` reads unconditionally (level > 0) and
		// whose dummy high key supplies `leaftopparent`, the next page down.
		// goopg has no such leaf here: this cascade starts at an internal
		// page that reached zero items after a downlink removal, with no
		// phase-1 marker of its own. Emitting the record with LeafBlk ==
		// TargetBlk would omit block 3 and PANIC a standby in
		// XLogReadBufferForRedoExtended.
		//
		// Refusing is not a behaviour regression, because S11.5d-3a made the
		// case structurally unreachable: the parent mutation RETARGETS the
		// downlink at its right neighbour's child and deletes the neighbour's
		// item, and it refuses outright when the downlink is the parent's
		// last item — so a downlink removal always leaves at least one item
		// behind and can no longer take a page to zero. The cascade survives
		// only as a defence for pages emptied some other way (a pre-S11.5d-3a
		// index), and for those we leave the page linked rather than log a
		// record no standby can replay.
		return nil
	}
	// See unlinkEmptyLeaf: the FPI fallback keeps the pre-S11.5d-3b
	// shape (whole page images, no control fields, re-derive under the
	// write), including the unlatched refusal.
	isRightmost, found, rerr := bt.parentDownlinkIsRightmostChild(parentBlk, blk)
	if rerr != nil {
		return rerr
	}
	if !found || isRightmost {
		return nil
	}
	leftLive, lerr := bt.liveSibling(prev, false)
	if lerr != nil {
		return lerr
	}
	rightLive, rerr2 := bt.liveSibling(next, true)
	if rerr2 != nil {
		return rerr2
	}
	return bt.unlinkEmptyInternalPageFPI(blk, parentBlk, leftLive, rightLive)
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

// removeDownlinkFromParent applies the parent limb of a page deletion
// on the FPI fallback paths (no WAL emitter wired). M0130-S11.5d-3a
// routes it through the same `ReplayParentRetargetByChild` as the
// WAL path and redo — the mutation must not depend on which emission
// path the caller took, or a tree vacuumed under a test harness would
// have a different shape from one vacuumed under a real server.
func (bt *BTree) removeDownlinkFromParent(parentBlk, childBlk storage.BlockNumber) error {
	s, err := bt.pinW(parentBlk)
	if err != nil {
		return err
	}
	if err := ReplayParentRetargetByChild(s.Page(), childBlk); err != nil {
		bt.unpinW(s)
		return fmt.Errorf("btree: parent retarget on block %d for child %d: %w", parentBlk, childBlk, err)
	}
	err = bt.markDirtyWithPageRecord(s, parentBlk)
	bt.unpinW(s)
	return err
}

// parentDownlinkIsRightmostChild reports whether childBlk's downlink is
// the LAST item on parentBlk — upstream's one structural refusal in page
// deletion (`_bt_lock_subtree_parent`; see ErrParentRightmostChild).
// `found` is false when the downlink is not on the page at all, which the
// callers treat as "someone else already unlinked it".
// M0130-S11.5d-3a.
func (bt *BTree) parentDownlinkIsRightmostChild(parentBlk, childBlk storage.BlockNumber) (isRightmost, found bool, err error) {
	s, err := bt.pinR(parentBlk)
	if err != nil {
		return false, false, err
	}
	defer bt.unpinR(s)
	_, isLast, ok, err := PGFindDownlinkOffset(s.Page(), childBlk)
	if err != nil {
		return false, false, err
	}
	return isLast, ok, nil
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
		// No left child: this root is a level-0 (leaf) root, so upstream's
		// backup block 1 does not exist and redo does not look for it.
		lsn, err := emitter(bt.rel, rootBlk, rootSlot.Page(), storage.InvalidBlockNumber, MetaBlock, metaSlot.Page())
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
	// The downlink is a t_tid: bi_hi at [0:2], bi_lo at [2:4] (bytes [4:6] are
	// the offset/status half, not part of the block number). Reading a bare
	// little-endian uint32 at [2:6] therefore dropped the high 16 bits and
	// mixed in status bits — wrong for any block, and wrong even at block 0
	// once the status half is non-zero. Share the one decoder
	// (review/260831-2 NB-3).
	return BTreeTupleGetDownLink(raw)
}
