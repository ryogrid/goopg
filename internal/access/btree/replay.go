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
// `IndexTupleData header | key` blob produced by
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
// 1-based data-item slot index whose downlink references
// the deleted child. The replay matches `removeDownlinkFromParent`'s
// semantics including the leftmost-key adoption when slot 1 is
// removed. (M0079-0003.)
//
// M0130-S11.4 slice 3b-2c-ii-B2-b-ii: this works on RAW item bytes and needs no
// IndexFormat, which is what lets recovery — which holds a relfilenode and has
// no catalog to resolve a key descriptor from — replay a descriptor-ordered tree
// correctly. Two properties of a pivot tuple make that possible, and both are
// format-independent because they live in the IndexTupleData HEADER:
//
//   - a minus-infinity pivot is exactly SizeOfIndexTupleData bytes (no key
//     attributes survive truncation), so "does the new first item still carry a
//     key?" is a length test, not a decode;
//   - the downlink is t_tid's block half (upstream BTreeTupleGetDownLink), so
//     rebuilding the leftmost pivot only needs the child block number.
//
// The surviving items are re-added VERBATIM: their key bytes are never parsed,
// so it does not matter whether they are goopg blobs or nbtree tuples.
func ReplayRemoveParentDownlink(page storage.Page, removeSlot uint16) error {
	count, err := PGDataItemCount(page)
	if err != nil {
		return fmt.Errorf("btree: replay parent downlink read: %w", err)
	}
	if removeSlot == 0 || int(removeSlot) > count {
		// Out-of-range slot — likely already replayed and the
		// page already has the post-removal layout. Treat as
		// a no-op to keep replay idempotent.
		return nil
	}
	raws := make([][]byte, 0, count-1)
	for slot := 1; slot <= count; slot++ {
		if slot == int(removeSlot) {
			continue
		}
		raw, err := pgGetItemRaw(page, uint16(slot))
		if err != nil {
			return fmt.Errorf("btree: replay parent downlink read slot %d: %w", slot, err)
		}
		raws = append(raws, raw)
	}
	// Mirror removeDownlinkFromParent: when the new first item adopts the
	// leftmost slot and still carries key attributes, blank them to maintain
	// the B-tree invariant. PGBTPivotRaw with a nil key is the zero-attribute
	// minus-infinity PIVOT tuple (M0130-S11.4 slice 3a) that the tuple format's
	// marshal produces for the same item, byte for byte — a bare literal would
	// replay the page into plain tuples.
	if len(raws) > 0 && len(raws[0]) > SizeOfIndexTupleData {
		raws[0] = PGBTPivotRaw(nil, BTreeTupleGetDownLink(raws[0]))
	}
	resetPageItems(page)
	for i, raw := range raws {
		if _, err := storage.PageAddItemRaw(page, raw); err != nil {
			return fmt.Errorf("btree: replay parent downlink re-add %d: %w", i, err)
		}
	}
	return nil
}

// ApplyInsertRecordAt re-runs one B-tree non-split insert against the given
// page bytes during WAL replay, placing the raw item at the RECORDED offset
// number — upstream's btree_xlog_insert, which does exactly one PageAddItem at
// `xlrec->offnum` (postgres/src/backend/access/nbtree/nbtxlog.c:56-70).
//
// M0130-S11.4 slice 3b-2c-ii-B2-b-ii. This replaces an insert-BY-KEY replay,
// and the offset is not merely cheaper: re-deriving the slot needs the index's
// comparison semantics, and recovery cannot know them. It holds a relfilenode,
// and the catalog that would turn one into a key descriptor is itself being
// replayed. Under the descriptor-ordered format (3b-2c-ii-B2-c) a by-key replay
// would both PARSE the item wrong (a tuple-format key is the whole tuple,
// header included) and, ordering the header-less bytes, file it at the wrong
// slot — silently, on the standby only. Placing recorded bytes at a recorded
// offset needs neither the parse nor the comparison.
//
// `offnum` is a PHYSICAL 1-based offset number (P_HIKEY = 1), the same
// coordinate xl_btree_insert carries, so the high key is already accounted for
// and this does not go through the pgXxx data-slot wrappers.
//
// The raw item is the bytes the writer put on its page. The page must already
// be a valid initialised B-tree page; replay never creates a fresh btree page
// from a logical insert (a split record handles that case).
//
// Idempotency is the caller's responsibility: WAL recovery compares page pd_lsn
// against the record's end-LSN before invoking this. The function is "apply
// unconditionally".
func ApplyInsertRecordAt(page storage.Page, raw []byte, offnum uint16) error {
	if offnum == 0 {
		// Not a legal OffsetNumber. Records emitted before
		// M0130-S11.4 slice 3b-2c-ii-B2-b-ii carried a placeholder 0 because
		// replay re-derived the slot by key; there is no way to honour one now,
		// and guessing would corrupt the page silently.
		return fmt.Errorf("btree: replay of insert: offnum 0 (pre-B2-b-ii record; re-initdb the cluster)")
	}
	if _, err := storage.PageInsertItemRawAt(page, offnum, raw); err != nil {
		return fmt.Errorf("btree: replay of insert at offnum %d: %w", offnum, err)
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
