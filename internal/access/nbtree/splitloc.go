package nbtree

import (
	"github.com/goopg/goopg/internal/utils/pgqsort"
)

// The rest of _bt_findsplitloc (postgres/src/backend/access/nbtree/
// nbtsplitloc.c) for a LEAF split, M0146-0216. Upstream does not take the
// single best-balanced cut. It sorts every legal split by its distance from the
// space-optimal one (_bt_deltasortsplits), takes an interval of nearly as good
// splits (_bt_defaultinterval), and within it picks the cut whose new high key
// keeps the fewest key attributes (_bt_bestsplitloc, _bt_split_penalty =
// _bt_keep_natts_fast). Suffix truncation then makes the pivot as short, and
// the tree as discriminating, as it can be. A composite-key index built by
// inserts therefore lands its splits a few items away from the exact middle.
// goopg's cut-at-best-delta left insert-built TPC-DS indexes 1-3% off PG's
// page counts.
//
// The duplicate strategies are ported too: "many duplicates" widens the
// interval to every split, and "single value" re-weights by
// BTREE_SINGLEVAL_FILLFACTOR with an interval of one.

const (
	// leafSplitDistance is LEAF_SPLIT_DISTANCE: the free-space tolerance
	// (as a share of the page's data) that bounds the split interval.
	leafSplitDistance = 0.050
	// internalSplitDistance is INTERNAL_SPLIT_DISTANCE: "a bit more
	// aggressive when splitting internal pages".
	internalSplitDistance = 0.075
	// bTreeSingleValFillFactor is BTREE_SINGLEVAL_FILLFACTOR (nbtree.h).
	bTreeSingleValFillFactor = 96
)

type findSplitStrat int

const (
	splitDefault findSplitStrat = iota
	splitManyDuplicates
	splitSingleValue
)

// pgFindSplitLoc chooses the split point of a leaf page as _bt_findsplitloc
// does after its fillfactor decision. items is the page's data items with the
// new item merged in at newIdx (-1 when it was folded into a posting list),
// budgets are leftspace and rightspace, and hikey is the original page's high
// key (nil on the rightmost page). fillFactorMult/useMult are the caller's
// choice: the leaf fillfactor on a rightmost page or for "split after new
// item", else 0.50 unweighted. It returns the 0-based cut, or false when no
// legal split exists. The format must have a key descriptor (penalties deform
// the key attributes).
func (f indexFormat) pgFindSplitLoc(items []item, newIdx, leftBudget, rightBudget int, hikey []byte,
	rightmost bool, fillFactorMult float64, useMult bool) (m int, ok bool) {
	cands := f.splitCandidates(items, leftBudget, rightBudget)
	if len(cands) == 0 {
		return 0, false
	}
	nkey := f.desc.NKeyAtts()
	keep := func(a, b []byte) int {
		n, err := f.keepNAttsFast(a, b)
		if err != nil {
			return nkey + 1
		}
		return n
	}
	lastLeft := func(c splitCand) []byte { return items[c.m-1].key }
	firstRight := func(c splitCand) []byte { return items[c.m].key }

	// olddataitemstotal: the old data items' space, without the new item.
	oldTotal := f.compactFootprint(items)
	if newIdx >= 0 {
		const itemIDSize = 4 // matches storage.itemIDSize
		oldTotal -= itemIDSize + MaxAlign(f.bodySize(items[newIdx].key))
	}

	leftPage, rightPage := cands[0], cands[len(cands)-1]
	deltaSort(cands, fillFactorMult, useMult)
	interval := defaultInterval(cands, oldTotal)

	// _bt_strategy.
	strategy := splitDefault
	leftInt, rightInt := intervalEdges(cands, interval)
	perfect := keep(lastLeft(leftInt), firstRight(rightInt))
	if perfect > nkey {
		perfect = keep(lastLeft(leftPage), firstRight(rightPage))
		switch {
		case perfect <= nkey:
			// Return indnkeyatts, not the true perfect penalty, so the split
			// lands on either side of the duplicate group.
			strategy, perfect = splitManyDuplicates, nkey
		case rightmost:
			strategy = splitSingleValue
		case hikey != nil && newIdx >= 0:
			perfect = keep(hikey, items[newIdx].key)
			if perfect <= nkey {
				strategy = splitSingleValue
			}
		}
	}
	switch strategy {
	case splitManyDuplicates:
		interval = len(cands)
	case splitSingleValue:
		deltaSort(cands, bTreeSingleValFillFactor/100.0, true)
		interval = 1
	}

	// _bt_bestsplitloc.
	high := min(interval, len(cands))
	best, bestPenalty := 0, int(^uint(0)>>1)
	for i := 0; i < high; i++ {
		p := keep(lastLeft(cands[i]), firstRight(cands[i]))
		if p < bestPenalty {
			best, bestPenalty = i, p
		}
		if p <= perfect {
			break
		}
	}
	final := cands[best]
	// The "many duplicates" guard against repeated splits ahead of
	// monotonically decreasing insertions: a would-be split just to the right
	// of the new item becomes a 50:50 split. With the new item not on the left
	// and firstrightoff in [newitemoff, newitemoff+9), the only such cut in the
	// merged item list is the one that starts the right half at the new item.
	if strategy == splitManyDuplicates && !rightmost && newIdx >= 0 && final.m == newIdx {
		final = cands[0]
	}
	return final.m, true
}

// deltaSort is _bt_deltasortsplits: each split's distance from the optimal
// one, as upstream computes it (int16 truncation of the weighted difference),
// then upstream's qsort. Ties keep pg_qsort's order, which is not stable, and
// _bt_bestsplitloc takes the first of equal candidates.
func deltaSort(cands []splitCand, fillFactorMult float64, useMult bool) {
	for i := range cands {
		var d int16
		if useMult {
			d = int16(fillFactorMult*float64(cands[i].leftfree) - (1.0-fillFactorMult)*float64(cands[i].rightfree))
		} else {
			d = int16(cands[i].leftfree - cands[i].rightfree)
		}
		if d < 0 {
			d = -d
		}
		cands[i].curdelta = d
	}
	pgqsort.Sort(cands, func(a, b *splitCand) int {
		switch {
		case a.curdelta < b.curdelta:
			return -1
		case a.curdelta > b.curdelta:
			return 1
		}
		return 0
	})
}

// pgFindSplitLocInternal is _bt_findsplitloc for an INTERNAL page
// (nbtsplitloc.c, M0146-0216). items is the page's data items (pivots) with the
// new downlink merged in at newIdx; budgets are leftspace and rightspace. Each
// candidate is priced as _bt_recsplitloc prices a non-leaf split: the left half
// pays for its new high key with the firstright tuple as it stands (no heap-TID
// allowance — that is the leaf's worst case), and the right half gets back the
// firstright tuple's key data, because its first item becomes a key-less
// minus-infinity pivot. The fillfactor is BTREE_NONLEAF_FILLFACTOR on a
// rightmost page and an unweighted balance elsewhere; the interval uses
// INTERNAL_SPLIT_DISTANCE; and within it _bt_bestsplitloc takes the split with
// the smallest firstright tuple (_bt_split_penalty's internal arm), stopping at
// the page-wide minimum (_bt_strategy's perfect penalty for internal pages).
func (f indexFormat) pgFindSplitLocInternal(items []item, newIdx, leftBudget, rightBudget int, rightmost bool) (int, bool) {
	n := len(items)
	if n < 2 {
		return 0, false
	}
	const itemIDSize = 4 // matches storage.itemIDSize
	// minusInfSize is MAXALIGN(sizeof(IndexTupleData)) + sizeof(ItemIdData):
	// what the right page's first item shrinks to.
	const minusInfSize = SizeOfIndexTupleData + itemIDSize
	sz := make([]int, n)
	total := 0
	for i := range items {
		sz[i] = itemIDSize + MaxAlign(f.bodySize(items[i].key))
		total += sz[i]
	}
	oldTotal := total
	if newIdx >= 0 && newIdx < n {
		oldTotal -= sz[newIdx]
	}
	var cands []splitCand
	minFirstRight := int(^uint(0) >> 1)
	left := 0
	for m := 1; m < n; m++ {
		left += sz[m-1]
		fr := sz[m]
		leftfree := int(int16(leftBudget - left - fr))
		rightfree := int(int16(rightBudget - (total - left) + fr - minusInfSize))
		if leftfree < 0 || rightfree < 0 {
			continue
		}
		minFirstRight = min(minFirstRight, fr)
		cands = append(cands, splitCand{m: m, left: left, right: total - left, leftfree: leftfree, rightfree: rightfree})
	}
	if len(cands) == 0 {
		return 0, false
	}
	if rightmost {
		deltaSort(cands, BTreeNonLeafFillFactor/100.0, true)
	} else {
		deltaSort(cands, 0.50, false)
	}
	interval := splitInterval(cands, oldTotal, internalSplitDistance)
	high := min(interval, len(cands))
	best, bestPenalty := 0, int(^uint(0)>>1)
	for i := 0; i < high; i++ {
		p := sz[cands[i].m]
		if p < bestPenalty {
			best, bestPenalty = i, p
		}
		if p <= minFirstRight {
			break
		}
	}
	return cands[best].m, true
}

// defaultInterval is _bt_defaultinterval for a leaf: the count of leading
// (delta-sorted) splits whose free space stays within LEAF_SPLIT_DISTANCE of
// the space-optimal split's on both sides.
func defaultInterval(cands []splitCand, oldTotal int) int {
	return splitInterval(cands, oldTotal, leafSplitDistance)
}

// splitInterval is _bt_defaultinterval at a given split distance
// (LEAF_SPLIT_DISTANCE or INTERNAL_SPLIT_DISTANCE).
func splitInterval(cands []splitCand, oldTotal int, distance float64) int {
	tolerance := int(int16(float64(oldTotal) * distance))
	opt := cands[0]
	lowLeft, lowRight := opt.leftfree-tolerance, opt.rightfree-tolerance
	highLeft, highRight := opt.leftfree+tolerance, opt.rightfree+tolerance
	for i := 1; i < len(cands); i++ {
		c := cands[i]
		if c.leftfree < lowLeft || c.rightfree < lowRight || c.leftfree > highLeft || c.rightfree > highRight {
			return i
		}
	}
	return len(cands)
}

// intervalEdges is _bt_interval_edges: the splits in the interval furthest to
// the left and to the right of the delta-optimal one, by position on the page.
// In the merged item list a cut's position is m itself, so the two
// newitemoff-tied cases upstream distinguishes reduce to m comparisons.
func intervalEdges(cands []splitCand, interval int) (left, right splitCand) {
	high := min(interval, len(cands))
	opt := cands[0]
	var haveLeft, haveRight bool
	for i := high - 1; i >= 0; i-- {
		d := cands[i]
		switch {
		case d.m < opt.m:
			if !haveLeft {
				left, haveLeft = d, true
			}
		case d.m > opt.m:
			if !haveRight {
				right, haveRight = d, true
			}
		default:
			if !haveLeft {
				left, haveLeft = d, true
			}
			if !haveRight {
				right, haveRight = d, true
			}
		}
		if haveLeft && haveRight {
			return left, right
		}
	}
	if !haveLeft {
		left = opt
	}
	if !haveRight {
		right = opt
	}
	return left, right
}
