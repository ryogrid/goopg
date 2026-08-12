package btree

// WAL replay helpers for btree records. Exported so the
// `internal/wal` package can apply logical btree records during
// crash recovery without importing btree's internal mutation
// paths.

import (
	"encoding/binary"
	"errors"
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

// ReplayMarkHalfDeadLeaf rebuilds a leaf page as the HALF-DEAD page phase 1 of
// btree page deletion leaves behind — block 0 of upstream's
// XLOG_BTREE_MARK_PAGE_HALFDEAD (`btree_xlog_mark_page_halfdead`,
// nbtxlog.c:807-848). M0130-S11.5d-1.
//
// Upstream recreates the page from scratch rather than patching it, and that is
// not an optimisation: a half-dead page is DEFINED by its contents (empty item
// area plus one dummy high key whose downlink field carries the top parent of
// the subtree being deleted), so every field of it is derivable from the record
// and none of it is worth a full-page image. The dummy high key is what makes
// the second phase possible at all — `_bt_unlink_halfdead_page` reads
// BTreeTupleGetTopParent off it to find the next page down to unlink.
//
// `topparent` is InvalidBlockNumber when the leaf IS the top parent of the
// subtree (the ordinary single-page deletion goopg performs), which is exactly
// what upstream writes in that case too.
func ReplayMarkHalfDeadLeaf(page storage.Page, leftblk, rightblk, topparent storage.BlockNumber) error {
	if err := InitPGBTPage(page); err != nil {
		return fmt.Errorf("btree: replay mark-halfdead init: %w", err)
	}
	writeOpaque(page, BTPageOpaque{
		Prev:  leftblk,
		Next:  rightblk,
		Level: 0,
		Flags: BTHalfDead | BTLeaf,
	})
	// PGBTPivotRaw(nil, topparent) IS upstream's `trunctuple`: SizeOfIndexTupleData
	// bytes, t_info = 8, zero key attributes, and the block half of t_tid holding
	// the top-parent link (BTreeTupleSetTopParent and BTreeTupleSetDownLink write
	// the same field — upstream's two names for it differ only by which page kind
	// is reading).
	if _, err := storage.PageAddItemRaw(page, PGBTPivotRaw(nil, topparent)); err != nil {
		return fmt.Errorf("btree: replay mark-halfdead high key: %w", err)
	}
	return nil
}

// ReplayHalfDeadParent applies upstream's parent-downlink removal — block 1 of
// XLOG_BTREE_MARK_PAGE_HALFDEAD (`btree_xlog_mark_page_halfdead`,
// nbtxlog.c:775-800). M0130-S11.5d-1.
//
// `poffset` is a PHYSICAL OffsetNumber (the coordinate the record carries), not
// a data-slot index: on an internal page P_FIRSTDATAKEY is 2 when the page has a
// high key and 1 when it is rightmost, and the record cannot be re-derived
// through that distinction on the standby.
//
// The algorithm is deliberately NOT "delete the item at poffset", which is what
// goopg's own `ReplayRemoveParentDownlink` does. Upstream retargets poffset's
// downlink at the RIGHT neighbour's child and deletes the neighbour's item
// instead, so the deleted subtree's key range is absorbed by the page to its
// RIGHT, matching the direction the sibling chain was relinked in. Removing
// poffset outright absorbs the range LEFTWARD. Both are self-consistent for an
// empty page, but they produce different parent pages, so a record shaped for
// upstream's redo must be produced by a primary that did upstream's mutation —
// see the deferral-ledger row for the emit-side half (S11.5d-3).
//
// A deleted page always has a right sibling on its own level (upstream never
// deletes a rightmost page), so poffset is never the last item.
func ReplayHalfDeadParent(page storage.Page, poffset uint16) error {
	count, err := storage.PageLinePointerCount(page)
	if err != nil {
		return fmt.Errorf("btree: replay halfdead parent read: %w", err)
	}
	if poffset == 0 || int(poffset)+1 > count {
		// Out of range: either a malformed record or a page that already has
		// the post-removal layout. Idempotency is the caller's contract via
		// pd_lsn, so treat it as a no-op rather than corrupting the page.
		return nil
	}
	// Collect from the first DATA offset only: resetPageItems below re-installs
	// the high key by itself (its whole reason for existing — pd_lower must
	// already account for P_HIKEY before the first data item is refilled), so
	// carrying the high key through this list would duplicate it.
	first := PGFirstDataKey(ReadPGOpaque(page))
	if poffset < first {
		return nil
	}
	raws := make([][]byte, 0, count)
	for slot := first; int(slot) <= count; slot++ {
		raw, err := storage.PageGetItemRaw(page, slot)
		if err != nil {
			return fmt.Errorf("btree: replay halfdead parent slot %d: %w", slot, err)
		}
		raws = append(raws, raw)
	}
	target := int(poffset - first)
	rightsib := BTreeTupleGetDownLink(raws[target+1])
	BTreeTupleSetDownLink(raws[target], rightsib)
	raws = append(raws[:target+1], raws[target+2:]...)
	resetPageItems(page)
	for i, raw := range raws {
		if _, err := storage.PageAddItemRaw(page, raw); err != nil {
			return fmt.Errorf("btree: replay halfdead parent re-add %d: %w", i, err)
		}
	}
	return nil
}

// ErrParentRightmostChild reports upstream's one structural refusal in page
// deletion: a page whose downlink is its parent's LAST item cannot be deleted,
// because the retarget mutation has no right neighbour to absorb the key range
// (`_bt_lock_subtree_parent`, nbtpage.c: "Cannot delete a page that is the
// rightmost child of its immediate parent, unless it is the only child --- in
// which case the parent has to be deleted too"). Upstream abandons the deletion
// and leaves the empty page in the tree; so does goopg. M0130-S11.5d-3a.
var ErrParentRightmostChild = errors.New("btree: downlink is the parent's rightmost child")

// PGFindDownlinkOffset locates the PHYSICAL OffsetNumber of the item on an
// internal page whose downlink references childBlk, and reports whether that
// item is the page's last one. M0130-S11.5d-3a.
//
// Physical, not data-slot: this is the coordinate `poffset` that
// XLOG_BTREE_MARK_PAGE_HALFDEAD carries and ReplayHalfDeadParent consumes, and
// the two differ by whether the page has a high key.
//
// The lookup is by block IDENTITY rather than by a previously captured index,
// on both the primary and in redo, for the reason M0122-0010 documented: a
// concurrent split on another connection's *BTree can insert a downlink ahead
// of the target and shift every later index right, so an index captured before
// the mutation is advisory — which no PG-shaped record may be.
func PGFindDownlinkOffset(page storage.Page, childBlk storage.BlockNumber) (poffset uint16, isLast bool, ok bool, err error) {
	count, err := storage.PageLinePointerCount(page)
	if err != nil {
		return 0, false, false, fmt.Errorf("btree: find downlink offset: %w", err)
	}
	first := PGFirstDataKey(ReadPGOpaque(page))
	for slot := first; int(slot) <= count; slot++ {
		raw, rerr := storage.PageGetItemRaw(page, slot)
		if rerr != nil {
			return 0, false, false, fmt.Errorf("btree: find downlink offset slot %d: %w", slot, rerr)
		}
		if BTreeTupleGetDownLink(raw) == childBlk {
			return slot, int(slot) == count, true, nil
		}
	}
	return 0, false, false, nil
}

// ReplayParentRetargetByChild is the parent limb of page deletion as BOTH the
// primary and redo perform it: find childBlk's downlink by identity and apply
// upstream's retarget-and-delete (ReplayHalfDeadParent). M0130-S11.5d-3a.
//
// A missing downlink is a no-op, not an error — that is the already-applied
// case, which redo reaches through a replayed record and the primary reaches
// through a racing unlink of the same child.
//
// Returns ErrParentRightmostChild when the downlink is the parent's last item.
// The primary tests for this BEFORE it emits anything and abandons the deletion;
// redo can only see it from a record no current primary would write.
func ReplayParentRetargetByChild(page storage.Page, childBlk storage.BlockNumber) error {
	poffset, isLast, ok, err := PGFindDownlinkOffset(page, childBlk)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if isLast {
		return ErrParentRightmostChild
	}
	return ReplayHalfDeadParent(page, poffset)
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

// ApplyInsertPostingRecordAt re-runs the `posting` limb of upstream's
// btree_xlog_insert (nbtxlog.c:186-224) — the redo of XLOG_BTREE_INSERT_POST,
// a leaf insert whose heap TID fell INSIDE an existing posting list, so the
// primary had to split that posting to make room.
//
// `data` is block 0's data run in the record's own layout: a uint16 posting
// offset followed by `orignewitem` — the new item as it looked BEFORE the
// split. Redo must re-run the split rather than trust the record, because the
// item actually placed on the primary's page is not the one logged: the split
// evicts the old posting's rightmost heap TID into the new item (see
// SwapPosting). Inserting `orignewitem` verbatim would put the wrong TID on the
// page and leave the posting's TIDs non-ascending.
//
// `offnum` is the physical 1-based offset the new item goes at; the posting
// being split is its immediate predecessor, upstream's
// OffsetNumberPrev(xlrec->offnum).
//
// M0131-S21b part 2. Idempotency is the caller's (pd_lsn vs record end-LSN),
// as for ApplyInsertRecordAt.
func ApplyInsertPostingRecordAt(page storage.Page, data []byte, offnum uint16) error {
	if len(data) < 2 {
		return fmt.Errorf("btree: replay of posting-split insert: block data len %d (want >= 2)", len(data))
	}
	postingoff := int(binary.LittleEndian.Uint16(data[0:2]))
	orignewitem := data[2:]
	if offnum < 2 {
		// P_HIKEY is offset 1, so a posting split can never target offset 1 or
		// 0: there would be no posting to the left to split.
		return fmt.Errorf("btree: replay of posting-split insert: offnum %d has no predecessor item", offnum)
	}
	oposting, err := storage.PageGetItemRaw(page, offnum-1)
	if err != nil {
		return fmt.Errorf("btree: replay of posting-split insert: read posting at offnum %d: %w", offnum-1, err)
	}
	nposting, newitem, err := SwapPosting(orignewitem, oposting, postingoff)
	if err != nil {
		return fmt.Errorf("btree: replay of posting-split insert at offnum %d: %w", offnum, err)
	}
	// Same byte length as oposting by construction, so this is upstream's
	// in-place memcpy over the existing item and cannot need page space.
	if err := storage.PageReplaceItemRaw(page, offnum-1, nposting); err != nil {
		return fmt.Errorf("btree: replay of posting-split insert: rewrite posting at offnum %d: %w", offnum-1, err)
	}
	if _, err := storage.PageInsertItemRawAt(page, offnum, newitem); err != nil {
		return fmt.Errorf("btree: replay of posting-split insert at offnum %d: %w", offnum, err)
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
