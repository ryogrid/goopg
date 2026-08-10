package btree

// pgvacuum.go carries the page-level half of upstream's `btree_xlog_vacuum`
// (nbtxlog.c:479-528) — the REDO that deletes a set of index tuples from one
// leaf page by OFFSET NUMBER instead of restoring a full-page image.
//
// M0130-S11.5c. Upstream's redo is three steps:
//
//	btree_xlog_updates(page, updatedoffsets, updates, nupdated)   // posting splits
//	PageIndexMultiDelete(page, deletedoffsets, ndeleted)
//	opaque->btpo_cycleid = 0; opaque->btpo_flags &= ~BTP_HAS_GARBAGE
//
// goopg implements the second and third; the first (`xl_btree_update`, the
// partial rewrite of a posting-list tuple whose TIDs died one at a time) has no
// counterpart because goopg's VACUUM does not rewrite posting tuples in place —
// it re-marshals every surviving TID as its own item, which is a change of page
// SHAPE that offset numbers cannot describe. `wal.EncodeBtreeVacuumPG` detects
// that case and logs a full-page image instead, so a record with nupdated > 0
// is one only a real PG primary can produce.
//
// Like pgsplit.go / pgnewroot.go this is FORMAT-FREE: recovery holds a
// relfilenode and no catalog, so items move as raw bytes and are never parsed
// as keys.

import (
	"bytes"
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// ReplayVacuumDelete is upstream's `PageIndexMultiDelete` + opaque-hint clear
// for a B-tree leaf: the items at the given PHYSICAL offset numbers are dropped
// and the survivors keep their relative order, so every remaining item's offset
// number shifts down by the number of deletions before it.
//
// `deleted` must be strictly ascending (upstream's callers build it that way and
// `PageIndexMultiDelete` requires it) and must name data items only — the high
// key at P_HIKEY is never vacuumed away, and a non-rightmost page that lost it
// would be structurally invalid (see resetPageItems).
//
// One documented difference from `PageIndexMultiDelete`: goopg rebuilds the item
// area rather than compacting line pointers in place, so a SURVIVING item that
// carried an LP_DEAD mark comes back unmarked. That bit is a hint (it says "this
// entry is known dead to all"), it is never WAL-logged in the first place, and
// goopg's own VACUUM deletes every marked item anyway — the loss can only cost a
// later re-check, never correctness.
func ReplayVacuumDelete(page storage.Page, deleted []uint16) error {
	count, err := PGDataItemCount(page)
	if err != nil {
		return err
	}
	first := pgFirstDataSlot(page)
	drop := make(map[uint16]bool, len(deleted))
	prev := uint16(0)
	for _, off := range deleted {
		if off <= prev {
			return fmt.Errorf("btree: vacuum deleted offsets not strictly ascending at %d", off)
		}
		prev = off
		if off < first || int(off-first) >= count {
			return fmt.Errorf("btree: vacuum deleted offset %d outside data range [%d,%d]", off, first, int(first)+count-1)
		}
		drop[off-first+1] = true
	}

	kept := make([][]byte, 0, count-len(deleted))
	for slot := uint16(1); slot <= uint16(count); slot++ {
		if drop[slot] {
			continue
		}
		raw, err := pgGetItemRawAllowDead(page, slot)
		if err != nil {
			return err
		}
		kept = append(kept, raw)
	}
	resetPageItems(page)
	for i, raw := range kept {
		if _, err := storage.PageAddItemRaw(page, raw); err != nil {
			return fmt.Errorf("btree: replay vacuum item %d: %w", i, err)
		}
	}
	// Upstream clears btpo_cycleid too; goopg's opaque has no such field (the
	// vacuum cycle id is a concurrency hint for _bt_vacuum_needs_cleanup, which
	// goopg does not implement).
	if op := readOpaque(page); op.HasGarbage() {
		op.Flags &^= BTHasGarbage
		writeOpaque(page, op)
	}
	return nil
}

// CheckVacuumDelete reports whether describing the primary's page rewrite as
// "delete these offset numbers" actually REPRODUCES it: it replays `deleted`
// against a copy of the pre-vacuum page and compares the result with the page
// the primary wrote, item for item plus the opaque header.
//
// This exists because goopg's VACUUM does not delete items in place — it filters
// a parsed item list and refills the page (`resetPageItems` + re-marshal). That
// happens to equal `PageIndexMultiDelete` whenever every item is an ordinary
// tuple, and NOT to equal it when the page carries posting lists (surviving TIDs
// come back as separate tuples) or when the page went empty (VACUUM additionally
// stamps BTDeleted|BTHalfDead, which no redo would set). Rather than enumerate
// those cases at the emit site and hope the list stays complete, the encoder
// asks this question of the two pages it actually has and falls back to a
// full-page image when the answer is no.
func CheckVacuumDelete(prePage, postPage storage.Page, deleted []uint16) error {
	if len(prePage) != storage.BlockSize || len(postPage) != storage.BlockSize {
		return fmt.Errorf("btree: vacuum check needs two %d-byte pages", storage.BlockSize)
	}
	replayed := make(storage.Page, storage.BlockSize)
	copy(replayed, prePage)
	if err := ReplayVacuumDelete(replayed, deleted); err != nil {
		return err
	}
	if got, want := readOpaque(replayed), readOpaque(postPage); got != want {
		return fmt.Errorf("btree: vacuum replay opaque %+v != written %+v", got, want)
	}
	gotN, err := PGDataItemCount(replayed)
	if err != nil {
		return err
	}
	wantN, err := PGDataItemCount(postPage)
	if err != nil {
		return err
	}
	if gotN != wantN {
		return fmt.Errorf("btree: vacuum replay left %d items, written page has %d", gotN, wantN)
	}
	// The high key is part of the comparison: slot 0 in this loop is
	// P_FIRSTDATAKEY, so compare it separately.
	gotHK, gotHas, err := PGHighKeyRaw(replayed)
	if err != nil {
		return err
	}
	wantHK, wantHas, err := PGHighKeyRaw(postPage)
	if err != nil {
		return err
	}
	if gotHas != wantHas || !bytes.Equal(gotHK, wantHK) {
		return fmt.Errorf("btree: vacuum replay high key differs")
	}
	for slot := uint16(1); slot <= uint16(gotN); slot++ {
		a, err := pgGetItemRawAllowDead(replayed, slot)
		if err != nil {
			return err
		}
		b, err := pgGetItemRawAllowDead(postPage, slot)
		if err != nil {
			return err
		}
		if !bytes.Equal(a, b) {
			return fmt.Errorf("btree: vacuum replay item %d differs from the written page", slot)
		}
	}
	return nil
}
