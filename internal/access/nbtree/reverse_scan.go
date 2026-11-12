package nbtree

import (
	"fmt"
	"sync/atomic"

	"github.com/goopg/goopg/internal/storage"
)

// reverseScanLeftRecoveries counts left steps whose candidate failed the
// right-link check — the concurrent-split recoveries lockAndValidateLeft
// exists for. Tests read it to prove the recovery path ran.
var reverseScanLeftRecoveries atomic.Int64

// ReverseScanCursor walks the entries in [lo, hi] from the highest key down —
// a backward btree scan (PG's BackwardScanDirection: _bt_first /
// _bt_readpage / _bt_readnextpage, nbtsearch.c). It is ScanCursor's mirror:
// one leaf per Next call, the leaf's in-range entries delivered to fn in
// DESCENDING order (a posting list's TIDs too), under the same bound rules
// and the same fn contract (the key aliases the pinned page; do not retain
// it). M0148-0001c3a3.
//
// A leftward walk is where a btree scan meets concurrent splits head-on:
// a split moves entries RIGHT, so the left sibling a scan read off its
// current page may since have split, putting a new page — holding entries
// the scan still owes — between it and the current page. The step to the
// left is therefore _bt_lock_and_validate_left (lockAndValidateLeft): the
// candidate is accepted only when its right-link still names the page just
// read; otherwise the walk moves right from it, or recovers from the page
// just read, as nbtree/README prescribes. (nbtree.ScanFromEnd, the planner's
// endpoint probe, deliberately has no such recovery and may skip or repeat
// entries; this cursor may not.)
type ReverseScanCursor struct {
	bt                       *BTree
	lo, hi                   []byte
	loExclusive, hiExclusive bool
	// cur is the leaf the next Next reads; InvalidBlockNumber once the scan
	// is exhausted.
	cur storage.BlockNumber
	// validate is true when cur was taken from a right neighbour's left
	// link and must pass lockAndValidateLeft before it is read; last is
	// that neighbour (PG's lastcurrblkno).
	validate bool
	last     storage.BlockNumber
}

// NewReverseScanCursor positions a backward scan of [lo, hi] (either bound
// may be nil: unbounded) at the rightmost leaf that can hold an entry at or
// below hi.
func (bt *BTree) NewReverseScanCursor(lo, hi []byte, loExclusive, hiExclusive bool) (*ReverseScanCursor, error) {
	c := &ReverseScanCursor{bt: bt, lo: lo, hi: hi, loExclusive: loExclusive, hiExclusive: hiExclusive}
	var start storage.BlockNumber
	var err error
	if hi == nil {
		start, err = bt.rightmostLeaf()
	} else {
		start, err = bt.descendToLowerBound(hi, false)
	}
	if err != nil {
		return nil, err
	}
	// Move right past every leaf whose high key is at or below hi: the
	// entries equal to hi (or, for a prefix bound, sharing its prefix) can
	// continue there. Moving one page too far only costs a read — the
	// per-entry bound test skips what lies above hi — while stopping short
	// would lose entries. A tombstone (P_IGNORE) is stepped over the same
	// way, its key range having passed to its right sibling.
	for start != storage.InvalidBlockNumber {
		slot, perr := bt.pinR(start)
		if perr != nil {
			return nil, perr
		}
		op := readOpaque(slot.Page())
		next := op.Next
		moveRight := next != storage.InvalidBlockNumber &&
			(op.isUnlinked() || hi == nil || keyAtOrPastHighKey(bt.format(), slot.Page(), hi))
		bt.unpinR(slot)
		if !moveRight {
			break
		}
		start = next
	}
	c.cur = start
	return c, nil
}

// rightmostLeaf descends through each level's last downlink to the
// rightmost leaf (_bt_get_endpoint's rightmost arm), moving right first
// wherever a concurrent split has given the page a right sibling.
func (bt *BTree) rightmostLeaf() (storage.BlockNumber, error) {
	meta, err := bt.readMeta()
	if err != nil {
		return storage.InvalidBlockNumber, err
	}
	cur := meta.Root
	if cur == storage.InvalidBlockNumber || cur == 0 {
		return storage.InvalidBlockNumber, nil
	}
	for {
		slot, err := bt.pinR(cur)
		if err != nil {
			return storage.InvalidBlockNumber, err
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
			return cur, nil
		}
		count, cerr := PGDataItemCount(slot.Page())
		if cerr != nil || count < 1 {
			bt.unpinR(slot)
			if cerr == nil {
				cerr = fmt.Errorf("btree: internal page %d has no downlinks", cur)
			}
			return storage.InvalidBlockNumber, cerr
		}
		raw, rerr := pgGetItemRaw(slot.Page(), uint16(count))
		if rerr != nil {
			bt.unpinR(slot)
			return storage.InvalidBlockNumber, rerr
		}
		child := BTreeTupleGetDownLink(raw)
		bt.unpinR(slot)
		cur = child
	}
}

// Next reads one leaf: its in-range entries go to fn from the highest key
// down. It returns ok=true when it processed a leaf (entries delivered or
// not) and ok=false once the scan is exhausted. If fn returns false or an
// error, or the walk passes lo, the scan ends.
func (c *ReverseScanCursor) Next(fn func(key []byte, ptr storage.ItemPointer, pos ScanPos) (bool, error)) (bool, error) {
	for c.cur != storage.InvalidBlockNumber {
		var slot *storage.Slot
		var err error
		if c.validate {
			slot, c.cur, err = c.bt.lockAndValidateLeft(c.cur, c.last)
			if err != nil {
				c.cur = storage.InvalidBlockNumber
				return false, err
			}
			if slot == nil {
				c.cur = storage.InvalidBlockNumber
				return false, nil
			}
		} else if slot, err = c.bt.pinR(c.cur); err != nil {
			c.cur = storage.InvalidBlockNumber
			return false, err
		}
		op := readOpaque(slot.Page())
		blk := c.cur
		prev := op.Prev
		if op.isUnlinked() {
			// A tombstone holds no entries: step left from it again
			// (_bt_readnextpage's P_IGNORE arm).
			c.bt.unpinR(slot)
			c.stepLeft(prev, blk)
			continue
		}
		stop, ferr := c.bt.scanLeafItemsReverse(slot, blk, c.lo, c.hi, c.loExclusive, c.hiExclusive, fn)
		c.bt.unpinR(slot)
		if ferr != nil {
			c.cur = storage.InvalidBlockNumber
			return false, ferr
		}
		if stop {
			c.cur = storage.InvalidBlockNumber
		} else {
			c.stepLeft(prev, blk)
		}
		return true, nil
	}
	return false, nil
}

// stepLeft aims the cursor at prev, the left link of the page just read
// (from), to be validated before it is read.
func (c *ReverseScanCursor) stepLeft(prev, from storage.BlockNumber) {
	if prev == storage.InvalidBlockNumber {
		c.cur = storage.InvalidBlockNumber
		return
	}
	c.cur, c.last, c.validate = prev, from, true
}

// lockAndValidateLeft is _bt_lock_and_validate_left (nbtsearch.c): pin and
// share-latch blk, the left link of lastcurr (the page the scan just read),
// and return it when its right-link still points at lastcurr. Otherwise a
// concurrent split moved the boundary: walk right from blk (at most four
// hops) to the page whose right-link is lastcurr. Failing that, lastcurr
// itself changed — if it was deleted, the first live page to its right
// inherited its key space and the walk restarts from that page's left link;
// if not, its left link has moved and the walk restarts from it. It returns
// the pinned, latched slot and its block number, or a nil slot when there is
// no page to the left.
func (bt *BTree) lockAndValidateLeft(blk, lastcurr storage.BlockNumber) (*storage.Slot, storage.BlockNumber, error) {
	orig := blk // detects circular links
	for {
		slot, err := bt.pinR(blk)
		if err != nil {
			return nil, storage.InvalidBlockNumber, err
		}
		op := readOpaque(slot.Page())
		for tries := 0; ; tries++ {
			if !op.isUnlinked() && op.Next == lastcurr {
				return slot, blk, nil
			}
			if tries == 0 {
				reverseScanLeftRecoveries.Add(1)
			}
			if op.Next == storage.InvalidBlockNumber || tries >= 4 {
				break
			}
			blk = op.Next
			bt.unpinR(slot)
			if slot, err = bt.pinR(blk); err != nil {
				return nil, storage.InvalidBlockNumber, err
			}
			op = readOpaque(slot.Page())
		}
		// Back to the page most recently read to see what happened to its
		// left link.
		bt.unpinR(slot)
		if slot, err = bt.pinR(lastcurr); err != nil {
			return nil, storage.InvalidBlockNumber, err
		}
		op = readOpaque(slot.Page())
		if op.isUnlinked() {
			// Deleted: the first live page to its right took over its key
			// space; stepping left from it lands where the scan belongs.
			for op.isUnlinked() {
				if op.Next == storage.InvalidBlockNumber {
					bt.unpinR(slot)
					return nil, storage.InvalidBlockNumber, errFellOffEnd
				}
				lastcurr = op.Next
				bt.unpinR(slot)
				if slot, err = bt.pinR(lastcurr); err != nil {
					return nil, storage.InvalidBlockNumber, err
				}
				op = readOpaque(slot.Page())
			}
		} else if op.Prev == orig {
			// lastcurr's left link still names orig, so orig split more
			// than four times since it was read. PG errors here ("could
			// not find left sibling"); the page is still to the right of
			// orig, so goopg walks right without the hop limit instead.
			bt.unpinR(slot)
			return bt.walkRightToLeftOf(orig, lastcurr)
		}
		prev := op.Prev
		bt.unpinR(slot)
		if prev == storage.InvalidBlockNumber {
			// The new lastcurr is leftmost: nothing remains to the left.
			return nil, storage.InvalidBlockNumber, nil
		}
		blk, orig = prev, prev
	}
}

// scanLeafItemsReverse is scanLeafItems walked from the last data item to
// the first: an entry above hi is skipped (the leaf's higher entries come
// first), and the first entry below lo ends the scan, everything after it
// being lower still. The bound comparisons are scanLeafItems' own, so a
// forward and a backward scan of one range admit exactly the same entries.
// Dead (LP_DEAD) entries are invisible, as there. A posting list's TIDs go
// out in descending order, as PG's backward scan returns them.
func (bt *BTree) scanLeafItemsReverse(slot *storage.Slot, cur storage.BlockNumber, lo, hi []byte, loExclusive, hiExclusive bool, fn func(key []byte, ptr storage.ItemPointer, pos ScanPos) (bool, error)) (stop bool, err error) {
	count, countErr := PGDataItemCount(slot.Page())
	if countErr != nil {
		return false, nil
	}
	f := bt.format()
	pageLSN := storage.MustHeader(slot.Page()).LSN()
	aboveHi := func(key []byte) bool {
		if hi == nil {
			return false
		}
		if hiExclusive {
			return f.compareHigh(key, hi) >= 0
		}
		return f.compareHigh(key, hi) > 0
	}
	belowLo := func(key []byte) bool {
		if lo == nil {
			return false
		}
		if loExclusive {
			return f.compareHigh(key, lo) <= 0
		}
		return f.compare(key, lo) < 0
	}
	for s := uint16(count); s >= 1; s-- {
		r, rawErr := pgGetItemRawNoCopy(slot.Page(), s)
		if rawErr != nil {
			continue // dead entries are invisible to scans
		}
		pos := ScanPos{Blk: cur, Slot: s, PageLSN: pageLSN}
		if isPostingRaw(r) {
			key, tids, perr := f.parsePostingRaw(r)
			if perr != nil {
				continue
			}
			if aboveHi(key) {
				continue
			}
			if belowLo(key) {
				return true, nil
			}
			for i := len(tids) - 1; i >= 0; i-- {
				ok, ferr := fn(key, tids[i], pos)
				if ferr != nil {
					return true, ferr
				}
				if !ok {
					return true, nil
				}
			}
			continue
		}
		it, perr := f.parseNoCopy(r)
		if perr != nil {
			continue
		}
		if aboveHi(it.key) {
			continue
		}
		if belowLo(it.key) {
			return true, nil
		}
		ok, ferr := fn(it.key, it.ptr, pos)
		if ferr != nil {
			return true, ferr
		}
		if !ok {
			return true, nil
		}
	}
	return false, nil
}

// walkRightToLeftOf follows right-links from blk to the page whose
// right-link is target — the left neighbour of target — with no hop limit,
// erroring only if it runs off the level's end.
func (bt *BTree) walkRightToLeftOf(blk, target storage.BlockNumber) (*storage.Slot, storage.BlockNumber, error) {
	for {
		slot, err := bt.pinR(blk)
		if err != nil {
			return nil, storage.InvalidBlockNumber, err
		}
		op := readOpaque(slot.Page())
		if !op.isUnlinked() && op.Next == target {
			return slot, blk, nil
		}
		next := op.Next
		bt.unpinR(slot)
		if next == storage.InvalidBlockNumber {
			return nil, storage.InvalidBlockNumber, fmt.Errorf("btree: could not find left sibling of block %d", target)
		}
		blk = next
	}
}
