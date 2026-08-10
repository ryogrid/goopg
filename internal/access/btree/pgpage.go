package btree

import (
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// This file is the PAGE SHAPE half of the upstream nbtree conversion
// (M0130-S11.2). pgformat.go (S11.1) established the 16-byte special area;
// what remains before goopg pages are shaped like PostgreSQL's is where the
// high key lives and how the line-pointer array is numbered:
//
//	upstream                          goopg (legacy)
//	--------                          --------------
//	offset 1 = P_HIKEY (an index      high key stored INSIDE the 272-byte
//	           tuple), present iff     opaque area as a length-prefixed
//	           the page is NOT         byte string, presence flagged by
//	           rightmost               BTHasHighKey
//	offset 2 = P_FIRSTKEY, the        offset 1 = first data item, always
//	           first data item on a
//	           non-rightmost page
//	btpo_next == P_NONE (0) marks     Next == InvalidBlockNumber marks
//	           the rightmost page                 the rightmost page
//
// The consequence of moving the high key onto the page is that a physical
// line-pointer slot is no longer the same thing as a data-item index: on a
// non-rightmost page every data item sits one slot further right. Rather than
// respell that arithmetic at each of the ~45 accessor call sites in this
// package (and get one of them wrong), the writers are converted by swapping
// storage.PageXxx(p, slot) for the pgXxx(p, slot) wrappers below, which take a
// 1-based DATA-item slot and add the P_FIRSTDATAKEY bias themselves. The
// algorithms above them — binary search, split, dedup, vacuum — keep counting
// data items from 1 and do not change at all.
//
// Nothing in this file is wired into the writers yet; S11.2b performs the flip
// in a single commit, because a page that has some of the upstream shape and
// some of the legacy shape is neither format and cannot be read by either
// reader. See docs/design/0130-0011-nbtree-pg-on-disk-format.md.

// Upstream offset-number landmarks from postgres/src/include/access/nbtree.h:
//
//	#define P_HIKEY     ((OffsetNumber) 1)
//	#define P_FIRSTKEY  ((OffsetNumber) 2)
//	#define P_FIRSTDATAKEY(opaque) (P_RIGHTMOST(opaque) ? P_HIKEY : P_FIRSTKEY)
//
// Note what P_FIRSTDATAKEY implies: there is no "has high key" flag bit in
// upstream's btpo_flags at all (0x0008 is BTP_META). High-key presence is
// derived purely from the sibling link, so goopg's BTHasHighKey disappears in
// the flip rather than being renumbered.
const (
	PHiKey    uint16 = 1
	PFirstKey uint16 = 2
)

// PGFirstDataKey is P_FIRSTDATAKEY: the physical offset number of the first
// data item on a page with this opaque.
func PGFirstDataKey(op PGBTPageOpaque) uint16 {
	if op.IsRightmost() {
		return PHiKey
	}
	return PFirstKey
}

// pgFirstDataSlot reads the page's own opaque to answer P_FIRSTDATAKEY. It
// costs a 4-byte load from the special area, which is why the wrappers below
// take the page rather than a pre-parsed opaque: a caller that has to thread
// an opaque through every accessor eventually threads a STALE one, and a stale
// rightmost bit silently shifts every subsequent read by one slot.
func pgFirstDataSlot(p storage.Page) uint16 {
	return PGFirstDataKey(ReadPGOpaque(p))
}

// pgDataSlot maps a 1-based data-item slot to its physical line-pointer slot.
func pgDataSlot(p storage.Page, slot uint16) uint16 {
	return slot + pgFirstDataSlot(p) - 1
}

// PGDataItemCount is the data-item count of a PG-format B-tree page: the line
// pointer count less the high key, if any. It is the wrapper counterpart of
// storage.PageLinePointerCount and is exported because the amcheck verify
// engine (internal/amcheck) walks index pages through this package's decoders.
func PGDataItemCount(p storage.Page) (int, error) {
	count, err := storage.PageLinePointerCount(p)
	if err != nil {
		return 0, err
	}
	n := count - int(pgFirstDataSlot(p)-1)
	if n < 0 {
		// A non-rightmost page with zero line pointers is corrupt (it must at
		// least carry its high key), but report it as empty rather than
		// letting a negative count propagate into a make([]T, n).
		return 0, nil
	}
	return n, nil
}

func pgGetItemRaw(p storage.Page, slot uint16) ([]byte, error) {
	return storage.PageGetItemRaw(p, pgDataSlot(p, slot))
}

func pgGetItemRawNoCopy(p storage.Page, slot uint16) ([]byte, error) {
	return storage.PageGetItemRawNoCopy(p, pgDataSlot(p, slot))
}

func pgGetItemRawAllowDead(p storage.Page, slot uint16) ([]byte, error) {
	return storage.PageGetItemRawAllowDead(p, pgDataSlot(p, slot))
}

func pgItemIsDead(p storage.Page, slot uint16) (bool, error) {
	return storage.PageItemIsDead(p, pgDataSlot(p, slot))
}

func pgSetItemIDDead(p storage.Page, slot uint16) error {
	return storage.PageSetItemIDDead(p, pgDataSlot(p, slot))
}

func pgGetItemID(p storage.Page, slot uint16) (storage.ItemID, error) {
	return storage.PageGetItemID(p, pgDataSlot(p, slot))
}

func pgReplaceItemRaw(p storage.Page, slot uint16, raw []byte) error {
	return storage.PageReplaceItemRaw(p, pgDataSlot(p, slot), raw)
}

// pgInsertItemRawAt inserts a data item at the 1-based data slot, returning
// that same data slot (not the physical one) so callers keep speaking in data
// coordinates.
func pgInsertItemRawAt(p storage.Page, slot uint16, raw []byte) (uint16, error) {
	if _, err := storage.PageInsertItemRawAt(p, pgDataSlot(p, slot), raw); err != nil {
		return 0, err
	}
	return slot, nil
}

// pgAddItemRaw appends a data item and returns its 1-based data slot.
// Appending is unaffected by the high key (it is always to the left), so this
// only has to subtract the bias back out of the physical slot storage returns.
func pgAddItemRaw(p storage.Page, raw []byte) (uint16, error) {
	phys, err := storage.PageAddItemRaw(p, raw)
	if err != nil {
		return 0, err
	}
	return phys - (pgFirstDataSlot(p) - 1), nil
}

// PGHighKeyRaw returns the raw high-key item bytes of a PG-format page, or
// ok=false when the page is rightmost and therefore has no high key. It is
// exported for the same reason ParseOpaque is: out-of-package structural
// validators must read the high key through this package's single decoder.
func PGHighKeyRaw(p storage.Page) ([]byte, bool, error) {
	if ReadPGOpaque(p).IsRightmost() {
		return nil, false, nil
	}
	count, err := storage.PageLinePointerCount(p)
	if err != nil {
		return nil, false, err
	}
	if count < int(PHiKey) {
		return nil, false, fmt.Errorf("btree: non-rightmost page has no high key line pointer (maxoff=%d)", count)
	}
	raw, err := storage.PageGetItemRawAllowDead(p, PHiKey)
	if err != nil {
		return nil, false, err
	}
	return raw, true, nil
}

// pgSetHighKeyRaw installs `raw` as the page's high key at P_HIKEY. The page
// must already be non-rightmost (btpo_next != P_NONE): high-key presence is a
// function of the sibling link, so the two are set together or the page is
// momentarily unreadable. Whether the slot has to be created or overwritten is
// decided from the current line-pointer count.
func pgSetHighKeyRaw(p storage.Page, raw []byte) error {
	if ReadPGOpaque(p).IsRightmost() {
		return fmt.Errorf("btree: refusing to set a high key on a rightmost page")
	}
	count, err := storage.PageLinePointerCount(p)
	if err != nil {
		return err
	}
	if count == 0 {
		// Fresh page: the high key is simply the first item.
		_, err := storage.PageAddItemRaw(p, raw)
		return err
	}
	// The page already reserves offset 1 for the high key (it was
	// non-rightmost before this call, or a bulk-load placeholder is sitting
	// there), so overwrite in place.
	return storage.PageReplaceItemRaw(p, PHiKey, raw)
}

// pgPromoteToNonRightmost makes room for a high key on a page that currently
// has none: every existing data item shifts one slot to the right and `raw`
// lands at P_HIKEY. The caller must have already pointed btpo_next at the new
// right sibling — pgDataSlot reads the opaque, so the shift and the sibling
// link must be committed in the same critical section.
func pgPromoteToNonRightmost(p storage.Page, raw []byte) error {
	if ReadPGOpaque(p).IsRightmost() {
		return fmt.Errorf("btree: high key insert on a page still marked rightmost")
	}
	_, err := storage.PageInsertItemRawAt(p, PHiKey, raw)
	return err
}

// pgReserveHiKeySlot reserves the P_HIKEY line pointer on a freshly
// initialised page without storing a high key behind it, so the bulk loader
// can add data items at P_FIRSTKEY before it knows whether the page will end
// up rightmost. Upstream: the pd_lower bump in _bt_blnewpage.
func pgReserveHiKeySlot(p storage.Page) error {
	count, err := storage.PageLinePointerCount(p)
	if err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("btree: P_HIKEY must be reserved on an empty page (maxoff=%d)", count)
	}
	_, err = storage.PageReserveLinePointer(p)
	return err
}

// pgSlideLeft removes the reserved-but-unused P_HIKEY line pointer from a page
// that turned out to be rightmost, sliding the data items back down one slot.
// Upstream: _bt_slideleft (nbtsort.c).
func pgSlideLeft(p storage.Page) error {
	count, err := storage.PageLinePointerCount(p)
	if err != nil {
		return err
	}
	if count < 1 {
		return fmt.Errorf("btree: slide-left on an empty page")
	}
	return storage.PageDeleteLinePointerAt(p, PHiKey)
}

// pgSibling translates a legacy sibling link to the upstream sentinel:
// storage.InvalidBlockNumber means "no sibling" in goopg's legacy opaque,
// upstream spells the same thing P_NONE (0). Block 0 is the metapage and can
// never be a sibling, which is what makes 0 free to carry the meaning.
func pgSibling(legacy storage.BlockNumber) storage.BlockNumber {
	if legacy == storage.InvalidBlockNumber {
		return PNone
	}
	return legacy
}

// legacySibling is pgSibling's inverse, for readers that still speak the
// legacy sentinel while the flip is in progress.
func legacySibling(pg storage.BlockNumber) storage.BlockNumber {
	if pg == PNone {
		return storage.InvalidBlockNumber
	}
	return pg
}

// pgFlags translates a legacy btpo_flags word to the upstream bit assignment.
// Three bits move: BTHasHighKey (0x0008) is DROPPED — upstream derives high-key
// presence from the sibling link and uses 0x0008 for BTP_META — while
// BTIncompleteSplit (0x0010 -> 0x0080) and BTHalfDead (0x0020 -> 0x0010) swap
// into upstream's positions. BTLeaf/BTRoot/BTDeleted/BTHasGarbage already agree.
func pgFlags(legacy uint16) uint16 {
	var out uint16
	for _, m := range []struct{ from, to uint16 }{
		{BTLeaf, BTPLeaf},
		{BTRoot, BTPRoot},
		{BTDeleted, BTPDeleted},
		{BTIncompleteSplit, BTPIncompleteSplit},
		{BTHalfDead, BTPHalfDead},
		{BTHasGarbage, BTPHasGarbage},
	} {
		if legacy&m.from != 0 {
			out |= m.to
		}
	}
	return out
}
