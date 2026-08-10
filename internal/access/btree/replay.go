package btree

// WAL replay helpers for btree records. Exported so the
// `internal/wal` package can apply logical btree records during
// crash recovery without importing btree's internal mutation
// paths.

import (
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// ReplayVacuumPage rebuilds a btree page's kept-items
// projection from a `RecordKindBtreeVacuum` payload. Mirrors
// the per-page critical section in `btree_vacuum.go`'s
// VacuumIndexPages: reset the line-pointer table and data area,
// re-add each kept item via `storage.PageAddItemRaw`, then
// overwrite the opaque flags.
//
// `keptItems` carries each surviving item's raw bytes (the
// `keyLen | ptr.block | ptr.offset | key` blob produced by
// `item.marshal`). The caller is responsible for emitting
// these in the order they should appear post-replay.
//
// `opaqueFlagsAfter` is the post-vacuum BTPageOpaque.Flags
// value. The other opaque fields (Prev, Next, Level, BTreeID)
// are preserved from the on-disk page — VACUUM never changes
// them.
//
// Idempotent at the caller's level via pd_lsn (the wal package
// checks `Header(page).LSN()` against the record's end LSN
// before calling this helper).
//
// (M0079-0002.)
func ReplayVacuumPage(page storage.Page, keptItems [][]byte, opaqueFlagsAfter uint16) error {
	op := readOpaque(page)
	resetPageItems(page)
	for i, raw := range keptItems {
		if _, err := storage.PageAddItemRaw(page, raw); err != nil {
			return fmt.Errorf("btree: replay vacuum item %d: %w", i, err)
		}
	}
	op.Flags = opaqueFlagsAfter
	writeOpaque(page, op)
	return nil
}

// ReplaySetSiblingNext replays the left-sibling Next pointer
// update half of `RecordKindBtreeUnlinkPage`. Other opaque
// fields (Prev, Level, Flags) are preserved verbatim.
// (M0079-0003.)
func ReplaySetSiblingNext(page storage.Page, newNext storage.BlockNumber) error {
	// pgWriteNextSibling, not a bare writeOpaque: losing the right sibling
	// also means losing the high key (S11.2b — presence is derived from
	// btpo_next), and a page left carrying a stale separator numbers its data
	// slots one too high forever after.
	return pgWriteNextSibling(page, readOpaque(page), newNext)
}

// ReplaySetSiblingPrev replays the right-sibling Prev pointer
// update half of `RecordKindBtreeUnlinkPage`. Other opaque
// fields are preserved. (M0079-0003.)
func ReplaySetSiblingPrev(page storage.Page, newPrev storage.BlockNumber) error {
	op := readOpaque(page)
	op.Prev = newPrev
	writeOpaque(page, op)
	return nil
}

// ReplaySetOpaqueFlags replays an opaque-Flags-only mutation.
// Used by `RecordKindBtreeMarkPageHalfDead` and the leaf-flags
// limb of `RecordKindBtreeUnlinkPage`. (M0079-0003.)
func ReplaySetOpaqueFlags(page storage.Page, flagsAfter uint16) error {
	op := readOpaque(page)
	op.Flags = flagsAfter
	writeOpaque(page, op)
	return nil
}

// ReplayRemoveParentDownlink replays the parent-downlink-removal
// limb of `RecordKindBtreeUnlinkPage`. `removeSlot` is the
// 1-based pageItems-order slot index whose downlink references
// the deleted child. The replay matches `removeDownlinkFromParent`'s
// semantics including the leftmost-key adoption when slot 1 is
// removed. (M0079-0003.)
func ReplayRemoveParentDownlink(page storage.Page, removeSlot uint16) error {
	items, err := pageItems(page)
	if err != nil {
		return fmt.Errorf("btree: replay parent downlink read: %w", err)
	}
	if removeSlot == 0 || int(removeSlot) > len(items) {
		// Out-of-range slot — likely already replayed and the
		// page already has the post-removal layout. Treat as
		// a no-op to keep replay idempotent.
		return nil
	}
	idx := int(removeSlot) - 1
	newItems := make([]item, 0, len(items)-1)
	newItems = append(newItems, items[:idx]...)
	newItems = append(newItems, items[idx+1:]...)
	// Mirror removeDownlinkFromParent: when the new first item
	// adopts the leftmost slot and currently has a non-empty
	// key, blank its key to maintain the B-tree invariant.
	if len(newItems) > 0 && len(newItems[0].key) > 0 {
		newItems[0] = item{keyLen: 0, ptr: newItems[0].ptr, key: nil}
	}
	resetPageItems(page)
	for i, it := range newItems {
		if _, err := storage.PageAddItemRaw(page, it.marshal()); err != nil {
			return fmt.Errorf("btree: replay parent downlink re-add %d: %w", i, err)
		}
	}
	return nil
}

// ReplayNewRootPage rebuilds a freshly-allocated root page from
// scratch using the carried items + level. Mirrors what
// `updateRootMeta` writes after a split bubbles up + what
// `resetToEmptyRoot` writes after a full vacuum empties the
// tree. (M0079-0003.)
func ReplayNewRootPage(page storage.Page, level uint32, items [][]byte) error {
	if err := InitPGBTPage(page); err != nil {
		return fmt.Errorf("btree: replay newroot init: %w", err)
	}
	flags := uint16(BTRoot)
	if level == 0 {
		flags |= BTLeaf
	}
	op := BTPageOpaque{
		Prev:  storage.InvalidBlockNumber,
		Next:  storage.InvalidBlockNumber,
		Level: level,
		Flags: flags,
	}
	writeOpaque(page, op)
	for i, raw := range items {
		if _, err := storage.PageAddItemRaw(page, raw); err != nil {
			return fmt.Errorf("btree: replay newroot item %d: %w", i, err)
		}
	}
	return nil
}

// ReplayMetaSetRoot rewrites the metapage so it points at the
// given root + level. Used by the metapage limb of
// `RecordKindBtreeNewRoot`. (M0079-0003.)
func ReplayMetaSetRoot(page storage.Page, root storage.BlockNumber, level uint32) error {
	// Read-modify-write, not a re-init: the metapage's LastCleanupNum* and
	// allequalimage fields are owned by other writers (VACUUM, the build) and
	// must survive a new-root replay untouched.
	meta := ReadPGMetaPage(page)
	meta.Root = root
	meta.Level = level
	meta.FastRoot = root
	meta.FastLevel = level
	WritePGMetaPage(page, meta)
	return nil
}
