package btree

// pgnewroot.go carries the two page-level primitives an upstream RM_BTREE
// record needs when it rebuilds a whole page's item area from block data
// instead of from a full-page image: upstream's `_bt_restore_page`
// (nbtxlog.c:50-97) and the metapage rebuild `_bt_restore_meta`
// (nbtxlog.c:99-146), plus the EMIT-side producer of the byte range
// `_bt_restore_page` consumes.
//
// M0130-S11.5a. Producer and consumer live in one file on purpose: the payload
// is an untagged run of MAXALIGNed IndexTupleData images whose only framing is
// each tuple's own `t_info` size, so a disagreement between the two halves is a
// silently mis-built page rather than a parse error (the sibling-path rule —
// see .ralph/fix_plan.md's M0106 note and CLAUDE.md's Hard-won Rule #2).
//
// Everything here is FORMAT-FREE: items are moved as raw bytes and never
// parsed as keys, so recovery — which holds a relfilenode and has no catalog to
// resolve a `PGIndexKeyDesc` from — can apply these records against a
// descriptor-ordered tree. Same constraint, same reason as
// `ApplyInsertRecordAt`.

import (
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// PGRestorePageData builds the block-data payload upstream registers when redo
// must rebuild a page's entire item area — `XLogRegisterBufData(0, rootpage +
// pd_upper, pd_special - pd_upper)` in `_bt_newroot` (nbtinsert.c:2588-2591).
//
// Upstream registers the raw tuple-storage region and relies on PageAddItem
// having allocated downward from pd_upper in increasing offset-number order, so
// that a FORWARD walk of the region visits the items in DECREASING offset
// number. This builds the same byte sequence explicitly, from the line pointers
// in reverse offset order, rather than slicing the page. The two agree byte for
// byte on any page whose items were appended in offset order (which is what
// `createNewRoot` does: the minus-infinity downlink first, then the separator),
// but the explicit form is also correct on a page whose data area was allocated
// in some other order — and goopg does insert index items at a computed
// physical offset (`storage.PageInsertItemRawAt`), which shifts LINE POINTERS
// while leaving the data area in allocation order.
//
// Each item is padded to MAXALIGN, matching `_bt_restore_page`'s
// `itemsz = MAXALIGN(IndexTupleSize(...))` stride and the page layer's own
// allocation granularity (M0130-S11.4 slice 3b-3a).
//
// ALL line pointers are emitted, including a high key at P_HIKEY if the page
// has one: the payload is a physical image of the item area, and offset numbers
// must survive the round trip unshifted.
func PGRestorePageData(page storage.Page) ([]byte, error) {
	count, err := storage.PageLinePointerCount(page)
	if err != nil {
		return nil, fmt.Errorf("btree: restore-page data: %w", err)
	}
	out := make([]byte, 0, storage.BlockSize)
	for slot := count; slot >= 1; slot-- {
		raw, err := storage.PageGetItemRaw(page, uint16(slot))
		if err != nil {
			return nil, fmt.Errorf("btree: restore-page data slot %d: %w", slot, err)
		}
		if len(raw) < SizeOfIndexTupleData {
			return nil, fmt.Errorf("btree: restore-page data slot %d: item %d bytes < header %d", slot, len(raw), SizeOfIndexTupleData)
		}
		out = append(out, raw...)
		out = append(out, make([]byte, MaxAlign(len(raw))-len(raw))...)
	}
	return out, nil
}

// PGParseRestorePageData is the consumer half of PGRestorePageData: upstream's
// `_bt_restore_page` scan (nbtxlog.c:63-84). It returns the items in ASCENDING
// offset-number order — upstream achieves the same by adding them to the page in
// reverse (`PageAddItem(..., nitems - i)`) — so the caller can simply append
// them to a freshly initialised page.
//
// The run is self-framing: each item's length is its own `t_info` size field,
// and the next item starts MAXALIGN bytes later. A truncated or over-long size
// is rejected rather than clamped; a malformed record must not silently produce
// a short page.
func PGParseRestorePageData(data []byte) ([][]byte, error) {
	var reversed [][]byte
	for off := 0; off < len(data); {
		if off+SizeOfIndexTupleData > len(data) {
			return nil, fmt.Errorf("btree: restore-page parse: %d trailing bytes < header %d", len(data)-off, SizeOfIndexTupleData)
		}
		size := PGIndexTupleSize(data[off:])
		if size < SizeOfIndexTupleData || off+size > len(data) {
			return nil, fmt.Errorf("btree: restore-page parse: item at %d claims %d bytes (remaining %d)", off, size, len(data)-off)
		}
		reversed = append(reversed, append([]byte(nil), data[off:off+size]...))
		off += MaxAlign(size)
	}
	items := make([][]byte, len(reversed))
	for i, raw := range reversed {
		items[len(reversed)-1-i] = raw
	}
	return items, nil
}

// ReplayRestoreMetaPage is upstream's `_bt_restore_meta` (nbtxlog.c:99-146):
// the metapage is re-initialised from scratch out of the record's
// `xl_btree_metadata`, NOT read-modify-written. btm_magic is re-asserted,
// btm_last_cleanup_num_heap_tuples is reset to -1.0 (upstream does not log it),
// and pd_lower is advanced past the struct so a later full-page image cannot
// compress the metadata away.
//
// The read-modify-write variant is `ReplayMetaSetRoot`, which the goopg-native
// RecordKindBtreeNewRoot record still needs because that record carries only
// (root, level). Once every metapage-touching record carries the full
// xl_btree_metadata, ReplayMetaSetRoot goes away with them.
func ReplayRestoreMetaPage(page storage.Page, m PGBTMetaPage) error {
	if err := InitPGBTPage(page); err != nil {
		return fmt.Errorf("btree: replay restore meta init: %w", err)
	}
	m.Magic = BTreeMagicPG
	m.LastCleanupNumHeapTuples = -1.0
	if m.Version < BTreeNoVacVersionPG || m.Version > BTreeVersionPG {
		return fmt.Errorf("btree: replay restore meta: btm_version %d out of range [%d,%d]", m.Version, BTreeNoVacVersionPG, BTreeVersionPG)
	}
	WritePGMetaPage(page, m)
	WritePGOpaque(page, PGBTPageOpaque{Flags: BTPMeta})
	storage.MustHeader(page).SetLower(uint16(storage.SizeOfPageHeaderData + SizeOfBTMetaPageDataPG))
	return nil
}

// ReplayClearIncompleteSplit is upstream's `_bt_clear_incomplete_split`
// (nbtxlog.c:34-48), applied to the left child registered as backup block 1 of
// a split-completing record (XLOG_BTREE_INSERT_UPPER / XLOG_BTREE_NEWROOT).
//
// goopg's runtime clears the flag in a separate step (`clearIncompleteSplit`),
// so on a goopg primary the flag is usually already clear by the time this
// runs; clearing it again is a no-op. It is applied anyway because a real PG
// standby replaying the same record WILL clear it here, and the two engines
// must not disagree about the flag's value at any LSN.
func ReplayClearIncompleteSplit(page storage.Page) error {
	op := readOpaque(page)
	if op.Flags&BTIncompleteSplit == 0 {
		return nil
	}
	op.Flags &^= BTIncompleteSplit
	writeOpaque(page, op)
	return nil
}
