package nbtree

import (
	"github.com/goopg/goopg/internal/storage"
)

// Dead-entry purge: reclaim index entries whose heap tuple is dead to all,
// WITHOUT using LP_DEAD hints and WITHOUT interfering with deduplication.
//
// See docs/design/not_ralph/btree-index-bloat-reclaim/DESIGN.md.
//
// Why not LP_DEAD. The obvious mechanism — mark dead-pointing entries
// ItemIDDead as scans/probes encounter them — was implemented and reverted
// (bdaa325a4 -> 4998c81b9). It reclaimed nothing on uniform pgbench -N (each
// aid is re-probed ~0.4x over 10M rows, so a dead entry is almost never
// revisited before its leaf splits) and made a re-probe-heavy workload ~18x
// WORSE on space, because KillItems must skip posting lists entirely
// (lpdead_kill.go: a posting has one line pointer for many TIDs and no per-TID
// dead bit) while deduplicateToRawItemsWithSpans merges runs purely on key
// equality with no dead-awareness. The two mechanisms cannot cooperate in
// either direction.
//
// What upstream does instead is ORDER them rather than mix them:
// _bt_delete_or_dedup_one_page (postgres/src/backend/access/nbtree/nbtinsert.c:2683)
// deletes known-dead items, then tries _bt_simpledel_pass (:2812) which visits
// the HEAP to discover dead TIDs with no hints at all, then _bt_dedup_pass, and
// only then splits.
//
// This file is goopg's analogue of the heap-verified step. It operates on the
// EXPANDED item list (one entry per heap TID, as pageItems produces) BEFORE any
// run is merged, so dead TIDs are gone before dedupConsolidate ever sees them
// and posting lists are formed over surviving live TIDs only. There is no dead
// bit to lose and no posting to refuse to touch — dedup is strictly helped by
// having fewer, cleaner runs to merge.

// DeadTIDFilter reports which of `tids` reference heap tuples that are dead to
// every transaction and whose index entries may therefore be removed.
//
// The result is parallel to the input. Returning nil, a short slice, or all
// false is always safe: the purge simply reclaims nothing.
//
// It is called with a leaf's exclusive latch held, so an implementation may pin
// heap pages but must not take any index latch — index-then-heap is the only
// direction goopg ever locks in (index scans pin heap pages the same way), so
// keeping to it cannot deadlock.
//
// Implementations must be conservative: report true only for a TID whose whole
// HOT chain is dead below OldestXmin. A false positive silently loses rows.
type DeadTIDFilter func(tids []storage.ItemPointer) []bool

// purgeDeadHeapPointers drops entries whose heap tuple the filter reports dead.
//
// It returns the surviving items and how many were removed. `items` is left
// untouched; survivors are written into a fresh slice, because the caller still
// needs the original if the purge does not free enough space.
//
// A nil filter (the default, and every caller that has not opted in) makes this
// a no-op returning the input unchanged, so trees behave exactly as before.
func (bt *BTree) purgeDeadHeapPointers(items []item) ([]item, int) {
	if bt.deadTIDs == nil || len(items) == 0 {
		return items, 0
	}
	tids := make([]storage.ItemPointer, len(items))
	for i := range items {
		tids[i] = items[i].ptr
	}
	dead := bt.deadTIDs(tids)
	if len(dead) != len(items) {
		// Short or nil answer: treat the whole batch as live. The filter is
		// advisory and a wrong-length reply must never be interpreted
		// positionally.
		return items, 0
	}
	ndead := 0
	for _, d := range dead {
		if d {
			ndead++
		}
	}
	if ndead == 0 {
		return items, 0
	}
	if ndead == len(items) {
		// Never empty a leaf here. An empty leaf has to go through the page
		// deletion/unlink machinery (btree_vacuum.go), which this path is not
		// prepared to drive; leaving one entry keeps the page well-formed and
		// costs a single slot.
		ndead--
		for i := len(dead) - 1; i >= 0; i-- {
			if dead[i] {
				dead[i] = false
				break
			}
		}
	}
	out := make([]item, 0, len(items)-ndead)
	for i := range items {
		if !dead[i] {
			out = append(out, items[i])
		}
	}
	return out, ndead
}
