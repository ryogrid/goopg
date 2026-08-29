package executor

import (
	"sort"

	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// purgeHeapBlockBudget caps how many DISTINCT heap blocks one purge may fault
// in. The purge only runs when the alternative is a page split, so some heap
// I/O is worth it, but an unbounded scan would turn every split into a
// mini-VACUUM. Upstream bounds its equivalent the same way — bottom-up index
// deletion visits a limited number of heap blocks per pass
// (postgres/src/backend/access/nbtree/nbtinsert.c, _bt_simpledel_pass).
//
// Entries beyond the budget are simply reported live, so the only cost of the
// cap is reclaiming less on this particular split.
const purgeHeapBlockBudget = 32

// indexDeadTIDFilter builds the heap-verified oracle the btree split path uses
// to reclaim index entries whose heap tuple is dead to all
// (docs/design/not_ralph/btree-index-bloat-reclaim/DESIGN.md).
//
// This is deliberately NOT an LP_DEAD hint producer. The reverted attempt
// (bdaa325a4 -> 4998c81b9) marked entries dead as the UPDATE probe met them,
// which reclaimed nothing on uniform pgbench -N and cost ~18x MORE space on a
// re-probe-heavy workload, because hint-marking and posting-list dedup are
// mutually exclusive strategies. Verifying against the heap instead — the same
// thing _bt_simpledel_pass does — needs no hints, works on entries that are
// never re-probed, and composes with dedup rather than fighting it.
func indexDeadTIDFilter(ctx *Context, tbl *catalog.Table) nbtree.DeadTIDFilter {
	if ctx == nil || ctx.Pool == nil || ctx.TxnMgr == nil || ctx.Catalog == nil || tbl == nil {
		return nil
	}
	heapRel := ctx.Catalog.RelFileNode(tbl)
	return func(tids []storage.ItemPointer) []bool {
		if len(tids) == 0 {
			return nil
		}
		oldestXmin := ctx.TxnMgr.OldestXmin()
		if oldestXmin == storage.InvalidTransactionID {
			return nil // no horizon: nothing is provably dead
		}
		out := make([]bool, len(tids))

		// Group by heap block so each page is pinned once, and walk blocks in
		// order: one sequential pass rather than a random scatter.
		byBlock := make(map[storage.BlockNumber][]int, purgeHeapBlockBudget)
		for i, t := range tids {
			byBlock[t.Block] = append(byBlock[t.Block], i)
		}
		blocks := make([]storage.BlockNumber, 0, len(byBlock))
		for b := range byBlock {
			blocks = append(blocks, b)
		}
		sort.Slice(blocks, func(i, j int) bool { return blocks[i] < blocks[j] })
		if len(blocks) > purgeHeapBlockBudget {
			blocks = blocks[:purgeHeapBlockBudget]
		}

		for _, blk := range blocks {
			slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: heapRel, Block: blk})
			if err != nil {
				continue // unreadable: report live, reclaim nothing
			}
			slot.RLock()
			page := slot.Page()
			for _, i := range byBlock[blk] {
				// heapChainDeadToAll is the same oracle VACUUM, the
				// opportunistic prune and the read-path kill collector use: it
				// walks the whole HOT chain and demands every member be dead
				// below oldestXmin. Conservative by construction — a live or
				// merely-recent tuple anywhere in the chain vetoes the purge.
				out[i] = heapChainDeadToAll(page, tids[i].Offset, oldestXmin)
			}
			slot.RUnlock()
			ctx.Pool.Unpin(slot)
		}
		return out
	}
}
