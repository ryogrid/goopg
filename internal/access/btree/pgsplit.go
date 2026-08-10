package btree

// pgsplit.go carries the page-level primitives upstream's `btree_xlog_split`
// (nbtxlog.c:180-352) needs — the halves of the split REDO that rebuild a page
// from record content rather than from a full-page image.
//
// M0130-S11.5b. Two of the record's three blocks are handled here:
//
//   - block 1, the new right sibling: upstream reconstructs it FROM SCRATCH on
//     every replay (`XLogInitBufferForRedo` + `_bt_pageinit` + `_bt_restore_page`)
//     and never relies on a backup image, so the block data is mandatory —
//     `ReplaySplitRightPage` is that reconstruction.
//   - block 2, the page that used to be the left page's right sibling: the only
//     change is its back-link, so upstream patches `btpo_prev` in place —
//     `ReplaySplitSiblingPrev`.
//
// Block 0 (the left half) is deliberately absent: goopg logs it as a full-page
// image, which upstream's own redo handles by taking the `BLK_RESTORED` arm and
// skipping the incremental rebuild entirely. See `wal.EncodeBtreeSplitPG`.
//
// Everything here is FORMAT-FREE for `PGRestorePageData`'s reason: recovery
// holds a relfilenode and has no catalog to resolve a `PGIndexKeyDesc` from, so
// items move as raw bytes and are never parsed as keys.

import (
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// SplitRightPageOpaque is the opaque header upstream's split REDO gives the new
// right sibling (nbtxlog.c:216-222): the back-link to the page that was split,
// the forward link to whatever used to be its right sibling (`P_NONE` on a
// rightmost split), the level, and NOTHING ELSE — `btpo_flags` is exactly
// `BTP_LEAF` or zero and `btpo_cycleid` is zero.
//
// Upstream's runtime `_bt_split` is slightly more generous: it copies the split
// page's flags minus `BTP_ROOT|BTP_SPLIT_END|BTP_HAS_GARBAGE` and inherits the
// vacuum cycle id (nbtinsert.c:1741-1746), so a real primary and a real standby
// can disagree about `BTP_SPLIT_END`/`btpo_cycleid` after replaying the same
// record. Both are VACUUM hints that `btree_mask` excludes from WAL consistency
// checking. goopg has no equivalent of either bit, so pinning the runtime to
// this same function makes the primary and the replayed page byte-identical —
// which is why `wal.EncodeBtreeSplitPG` can refuse a right page that does not
// match it, rather than silently logging a record whose redo builds a different
// page than the one the primary wrote.
func SplitRightPageOpaque(level uint32, leftBlk, sibBlk storage.BlockNumber) BTPageOpaque {
	var flags uint16
	if level == 0 {
		flags = BTLeaf
	}
	return BTPageOpaque{
		Prev:  leftBlk,
		Next:  sibBlk,
		Level: level,
		Flags: flags,
	}
}

// CheckSplitRightPageOpaque validates a freshly-split right page against
// SplitRightPageOpaque and returns its level for the record's main data.
//
// It lives here rather than in the WAL package because the comparison has to
// run on goopg's IN-MEMORY flag set: the on-disk bits are upstream's BTP_*, and
// readOpaque/writeOpaque are the translation. A caller outside this package
// would compare the wrong vocabulary.
func CheckSplitRightPageOpaque(page storage.Page, leftBlk, sibBlk storage.BlockNumber) (uint32, error) {
	got := readOpaque(page)
	want := SplitRightPageOpaque(got.Level, leftBlk, sibBlk)
	if got != want {
		return 0, fmt.Errorf("btree: split right page opaque %+v is not the redo-derived %+v", got, want)
	}
	return got.Level, nil
}

// ReplaySplitRightPage is upstream's right-sibling reconstruction
// (nbtxlog.c:213-228): initialise the page, stamp the opaque header from the
// record's `xl_btree_split.level` and the record's own block tags, then restore
// the item area with `_bt_restore_page`.
//
// `items` come from PGParseRestorePageData and are already in ASCENDING offset
// order, so appending them reproduces the primary's line pointers one for one —
// including the inherited high key at P_HIKEY when the page is not rightmost.
// The high key is not special-cased: it is simply the first item in the run,
// exactly as it is the first line pointer on the page it was read from.
func ReplaySplitRightPage(page storage.Page, level uint32, leftBlk, sibBlk storage.BlockNumber, items [][]byte) error {
	if err := InitPGBTPage(page); err != nil {
		return fmt.Errorf("btree: replay split right init: %w", err)
	}
	writeOpaque(page, SplitRightPageOpaque(level, leftBlk, sibBlk))
	for i, raw := range items {
		if _, err := storage.PageAddItemRaw(page, raw); err != nil {
			return fmt.Errorf("btree: replay split right item %d: %w", i, err)
		}
	}
	return nil
}

// ReplaySplitSiblingPrev is upstream's block-2 arm (nbtxlog.c:305-311): the page
// that was the split page's right sibling gains the new right page as its
// back-link. Nothing else on that page changes, which is why the record carries
// no block data for it.
func ReplaySplitSiblingPrev(page storage.Page, rightBlk storage.BlockNumber) error {
	op := readOpaque(page)
	op.Prev = rightBlk
	writeOpaque(page, op)
	return nil
}
