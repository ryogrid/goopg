package btree

import (
	"github.com/goopg/goopg/internal/storage"
)

// KillItem is one pending kill handed to KillItems: the entry's physical
// position as captured at scan time (D7 re-verify token included) and its
// TID (cheap pre-filter — never sufficient alone).
type KillItem struct {
	Pos ScanPos
	Ptr storage.ItemPointer
}

// KillItems is the deferred exclusive-latched marking pass (C3-S3; PG's
// _bt_killitems analog, design §4b). For each pending kill, grouped by
// leaf: take the leaf's exclusive latch, re-verify identity by PAGE-LSN
// EQUALITY against the value captured at scan time (D7 — any WAL-logged
// change, split, vacuum rewrite, or recycle bumps pd_lsn and fails the
// check; the pending kill is then dropped: hint loss, never corruption),
// pre-filter by TID match at the recorded slot, set ItemIDDead + the
// BTHasGarbage opaque hint, and dirty the page WITHOUT an FPI or pd_lsn
// bump (Pool.MarkDirtyHint — the mark itself must not self-invalidate D7
// and is an unlogged hint, D2). Single-leaf latches only, no descent —
// cannot deadlock with insert/split paths (I3). Posting-list slots are
// skipped entirely for now (O-C3-1: PG marks a posting tuple only when ALL
// its TIDs are dead; goopg defers posting kills to a later slice).
//
// Errors are not returned: marking is best-effort by contract; any
// pin/parse failure just drops the pending kill.
func (bt *BTree) KillItems(kills []KillItem) {
	if len(kills) == 0 {
		return
	}
	// Without the full WAL wiring, item-moving writers cannot bump
	// pd_lsn, so the D7 LSN token is VACUOUS — a stale kill could mark a
	// live entry that re-used its coordinates (S3-review: a vacuum-only
	// harness let an insert shift slots LSN-silently). Require every
	// leaf-writer hook plus the hint flush-barrier source (async-commit
	// durability); production wires all of them together in initdb.Open.
	if bt.pool.LogBtreeVacuum() == nil || bt.pool.LogPageImage() == nil || !bt.pool.HasWALFrontier() {
		return
	}
	byLeaf := make(map[storage.BlockNumber][]KillItem, 4)
	for _, k := range kills {
		byLeaf[k.Pos.Blk] = append(byLeaf[k.Pos.Blk], k)
	}
	for blk, ks := range byLeaf {
		slot, err := bt.pinW(blk)
		if err != nil {
			continue
		}
		p := slot.Page()
		op := readOpaque(p)
		// Leaf-only invariant (S1 review finding 3): never mark internal
		// pages; deleted/half-dead pages belong to VACUUM's machinery.
		if !op.IsLeaf() || op.IsDeleted() || op.IsHalfDead() {
			bt.unpinW(slot)
			continue
		}
		// D7: the page must be byte-for-byte the one the scan saw.
		if storage.MustHeader(p).LSN() != ks[0].Pos.PageLSN {
			bt.unpinW(slot)
			continue
		}
		marked := false
		for _, k := range ks {
			if k.Pos.PageLSN != ks[0].Pos.PageLSN {
				continue // mixed captures on one leaf: only the verified LSN wins
			}
			raw, rerr := pgGetItemRawAllowDead(p, k.Pos.Slot)
			if rerr != nil {
				continue
			}
			if isPostingRaw(raw) {
				continue // O-C3-1: posting kills deferred
			}
			it, perr := parseItem(raw)
			if perr != nil || it.ptr != k.Ptr {
				continue // TID pre-filter (paranoia under an equal LSN)
			}
			if pgSetItemIDDead(p, k.Pos.Slot) == nil {
				marked = true
			}
		}
		if marked {
			if !op.HasGarbage() {
				op.Flags |= BTHasGarbage
				writeOpaque(p, op)
			}
			// Unlogged hint: dirty WITHOUT FPI/pd_lsn (D2/D7).
			bt.pool.MarkDirtyHint(slot)
		}
		bt.unpinW(slot)
	}
}
