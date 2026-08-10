package storage

import "fmt"

// Line-pointer array primitives that operate on the ItemIdData array itself
// rather than on a tuple. They exist for the nbtree PG-format conversion
// (M0130-S11.2): upstream reserves and later removes a bare line pointer for
// the high key (P_HIKEY) without ever storing tuple bytes behind it, and goopg
// has no other way to express that — PageAddItemRaw always allocates payload
// bytes and PageRemoveHeapTuple only blanks a slot in place (it never slides
// the array down), which would leave a hole at offset 1 where upstream expects
// the first data key.
//
// See docs/design/0130-0011-nbtree-pg-on-disk-format.md.

// PageReserveLinePointer appends one LP_UNUSED line pointer to the page and
// returns its 1-based slot. No tuple bytes are allocated: pd_lower advances by
// sizeof(ItemIdData) and pd_upper is untouched.
//
// This is upstream's `((PageHeader) page)->pd_lower += sizeof(ItemIdData)` in
// _bt_blnewpage (postgres/src/backend/access/nbtree/nbtsort.c) — "make the
// P_HIKEY line pointer appear allocated". The bulk loader does not know
// whether a page will end up rightmost until the level is finished, so it
// always reserves offset 1 and either fills it with the high key or removes it
// again via PageDeleteLinePointerAt (upstream's _bt_slideleft).
func PageReserveLinePointer(p Page) (uint16, error) {
	h, err := Header(p)
	if err != nil {
		return 0, err
	}
	lower := int(h.Lower())
	upper := int(h.Upper())
	if upper-lower < itemIDSize {
		return 0, ErrNoSpaceInPage
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return 0, err
	}
	if err := writeItemID(p, count, ItemID{Flags: ItemIDUnused}); err != nil {
		return 0, err
	}
	h.SetLower(uint16(lower + itemIDSize))
	return uint16(count + 1), nil
}

// PageDeleteLinePointerAt removes the 1-based line pointer `slot` entirely:
// every higher line pointer slides down one position and pd_lower shrinks by
// sizeof(ItemIdData), so the page's maximum offset number decreases by one.
// Unlike PageRemoveHeapTuple this changes the slot numbering of the items to
// the right, which is exactly what upstream's _bt_slideleft does when a page
// turns out to be rightmost and its reserved P_HIKEY pointer must disappear.
//
// The tuple bytes previously referenced by `slot` are NOT reclaimed; they
// become dead space until the page is repacked, matching upstream (which only
// ever slides away a payload-less placeholder).
func PageDeleteLinePointerAt(p Page, slot uint16) error {
	h, err := Header(p)
	if err != nil {
		return err
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return err
	}
	if int(slot) < 1 || int(slot) > count {
		return fmt.Errorf("PageDeleteLinePointerAt: slot %d out of range [1,%d]", slot, count)
	}
	for idx := int(slot) - 1; idx < count-1; idx++ {
		id, err := readItemID(p, idx+1)
		if err != nil {
			return err
		}
		if err := writeItemID(p, idx, id); err != nil {
			return err
		}
	}
	// Blank the now-unused last slot before shrinking pd_lower so a stale
	// pointer can never be resurrected by a later PageReserveLinePointer that
	// forgets to overwrite it.
	if err := writeItemID(p, count-1, ItemID{Flags: ItemIDUnused}); err != nil {
		return err
	}
	h.SetLower(uint16(SizeOfPageHeaderData + (count-1)*itemIDSize))
	return nil
}
