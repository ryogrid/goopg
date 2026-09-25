package storage

import (
	"fmt"
	"sync/atomic"
)

// heapLinePointerLifecycle is the cluster capability "every LP_UNUSED heap
// line pointer is free of index entries" (M0145-0008v). Before it, goopg's
// prune marked dead non-HOT tuples LP_UNUSED while their index entries still
// pointed at them, and nothing removed those entries; a cluster written by
// such a binary may hold LP_UNUSED items an index still references, so no
// LP_UNUSED item there may ever be reused. initdb of a lifecycle-aware binary
// names the capability in global/pg_goopg_features, and open sets this flag
// from it. With the flag off VACUUM leaves its LP_DEAD items as they are:
// no second heap pass, no line-pointer truncation, no reuse.
var heapLinePointerLifecycle atomic.Bool

// HeapLinePointerLifecycleFeature is the capability's name in the marker file.
const HeapLinePointerLifecycleFeature = "heap_lp_lifecycle"

// SetHeapLinePointerLifecycle records whether the open cluster has the
// capability. Set once at open; tests set it explicitly.
func SetHeapLinePointerLifecycle(on bool) { heapLinePointerLifecycle.Store(on) }

// HeapLinePointerLifecycle reports whether the open cluster's LP_UNUSED heap
// items are free of index entries, so VACUUM may run its second heap pass.
func HeapLinePointerLifecycle() bool { return heapLinePointerLifecycle.Load() }

// PageVacuumDeadItems is lazy_vacuum_heap_page's page step (vacuumlazy.c):
// once VACUUM has removed the index entries for slots, each of those
// LP_DEAD items becomes LP_UNUSED, and PageTruncateLinePointerArray drops the
// trailing unused items. No tuple byte moves, so the caller needs only the
// exclusive content lock, not a cleanup lock. Every slot must be LP_DEAD with
// no storage (PG asserts the same); anything else is an error and leaves the
// page untouched. It reports the number of items it set unused.
func PageVacuumDeadItems(p Page, slots []uint16) (int, error) {
	count, err := PageLinePointerCount(p)
	if err != nil {
		return 0, err
	}
	for _, s := range slots {
		if s == 0 || int(s) > count {
			return 0, fmt.Errorf("%w: dead slot %d out of range (count=%d)", ErrInvalidSlot, s, count)
		}
		item, err := readItemID(p, int(s)-1)
		if err != nil {
			return 0, err
		}
		if item.Flags != ItemIDDead || item.Length != 0 {
			return 0, fmt.Errorf("%w: slot %d is not a storage-less LP_DEAD item (flags=%d len=%d)", ErrUnsupportedItem, s, item.Flags, item.Length)
		}
	}
	for _, s := range slots {
		if err := writeItemID(p, int(s)-1, ItemID{Flags: ItemIDUnused}); err != nil {
			return 0, err
		}
	}
	if err := PageTruncateLinePointerArray(p); err != nil {
		return 0, err
	}
	return len(slots), nil
}

// PageTruncateLinePointerArray ports bufpage.c's function of the same name:
// trailing LP_UNUSED items are cut from the line-pointer array (pd_lower moves
// down), always keeping the first item, and PD_HAS_FREE_LINES records whether
// an unused item remains below the new end.
func PageTruncateLinePointerArray(p Page) error {
	h, err := Header(p)
	if err != nil {
		return err
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return err
	}
	countDone, setHint := false, false
	unusedEnd := 0
	for i := count; i >= 1; i-- {
		item, err := readItemID(p, i-1)
		if err != nil {
			return err
		}
		used := item.Flags != ItemIDUnused
		if !countDone && i > 1 {
			if !used {
				unusedEnd++
			} else {
				countDone = true
			}
		} else if !used {
			setHint = true
			break
		}
	}
	if unusedEnd > 0 {
		lower := int(h.Lower()) - unusedEnd*itemIDSize
		for i := lower; i < int(h.Lower()); i++ {
			p[i] = 0
		}
		h.SetLower(uint16(lower))
	}
	if setHint {
		h.SetFlags(h.Flags() | PDHasFreeLines)
	} else {
		h.SetFlags(h.Flags() &^ PDHasFreeLines)
	}
	return nil
}
