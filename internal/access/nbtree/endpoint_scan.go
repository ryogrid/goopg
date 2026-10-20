package nbtree

import (
	"github.com/goopg/goopg/internal/storage"
)

// ScanFromEnd invokes fn for the index's entries starting at its LAST entry
// and moving toward the first — _bt_endpoint's BackwardScanDirection arm
// (nbtsearch.c): descend through each level's rightmost downlink to the
// rightmost leaf, read its entries from the highest offset down, then follow
// the left-sibling links. It is the read get_actual_variable_endpoint needs
// for a column's actual maximum (selfuncs.c; M0146-0233), touching a handful
// of pages where a forward scan from the left would read the whole index.
//
// fn has RangeScan's contract: key aliases the pinned page and must not be
// retained or re-enter this tree; returning false stops the scan. A posting
// list yields its TIDs in descending order, as PG's backward scan returns
// them. Deleted and half-dead pages are skipped; a page split concurrently
// with the leftward walk can at worst skip or repeat a few entries, which
// the planner's endpoint probe tolerates (it stops at the first usable one).
func (bt *BTree) ScanFromEnd(fn func(key []byte, ptr storage.ItemPointer) (bool, error)) error {
	meta, err := bt.readMeta()
	if err != nil {
		return err
	}
	cur := meta.Root
	if cur == storage.InvalidBlockNumber || cur == 0 {
		return nil // empty index
	}
	// Descend: on an internal page take the last downlink; move right first
	// if a concurrent split left this page with a right sibling.
	for {
		slot, err := bt.pinR(cur)
		if err != nil {
			return err
		}
		op := readOpaque(slot.Page())
		if op.Next != storage.InvalidBlockNumber {
			next := op.Next
			bt.unpinR(slot)
			cur = next
			continue
		}
		if op.IsLeaf() {
			bt.unpinR(slot)
			break
		}
		count, cerr := PGDataItemCount(slot.Page())
		if cerr != nil || count < 1 {
			bt.unpinR(slot)
			return cerr
		}
		raw, rerr := pgGetItemRaw(slot.Page(), uint16(count))
		if rerr != nil {
			bt.unpinR(slot)
			return rerr
		}
		child := BTreeTupleGetDownLink(raw)
		bt.unpinR(slot)
		cur = child
	}
	// Walk the leaf level right to left.
	for cur != storage.InvalidBlockNumber {
		slot, err := bt.pinR(cur)
		if err != nil {
			return err
		}
		op := readOpaque(slot.Page())
		prev := op.Prev
		if op.IsDeleted() || op.IsHalfDead() {
			bt.unpinR(slot)
			cur = prev
			continue
		}
		stop, ferr := bt.scanLeafItemsBackward(slot, fn)
		bt.unpinR(slot)
		if ferr != nil {
			return ferr
		}
		if stop {
			return nil
		}
		cur = prev
	}
	return nil
}

// scanLeafItemsBackward is scanLeafItems without bounds, highest offset
// first. Dead line pointers are skipped, as the forward scan skips them.
func (bt *BTree) scanLeafItemsBackward(slot *storage.Slot, fn func(key []byte, ptr storage.ItemPointer) (bool, error)) (bool, error) {
	count, err := PGDataItemCount(slot.Page())
	if err != nil {
		return false, nil
	}
	for s := uint16(count); s >= 1; s-- {
		r, rawErr := pgGetItemRawNoCopy(slot.Page(), s)
		if rawErr != nil {
			continue
		}
		if isPostingRaw(r) {
			key, tids, perr := bt.format().parsePostingRaw(r)
			if perr != nil {
				continue
			}
			for i := len(tids) - 1; i >= 0; i-- {
				ok, ferr := fn(key, tids[i])
				if ferr != nil {
					return true, ferr
				}
				if !ok {
					return true, nil
				}
			}
			continue
		}
		it, perr := bt.format().parseNoCopy(r)
		if perr != nil {
			continue
		}
		ok, ferr := fn(it.key, it.ptr)
		if ferr != nil {
			return true, ferr
		}
		if !ok {
			return true, nil
		}
	}
	return false, nil
}
