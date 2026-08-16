package nbtree

// pgdelete.go carries the page-level half of upstream's `btree_xlog_delete`
// (nbtxlog.c:651-712) — the REDO of XLOG_BTREE_DELETE, the record a primary
// writes when `_bt_delitems_delete` removes LP_DEAD-marked entries from one
// leaf page — together with `btree_xlog_updates` (nbtxlog.c:556-597), the
// posting-list half BOTH that record and xl_btree_vacuum share.
//
// M0131-S21b part 3. Two page mutations in upstream's order:
//
//	btree_xlog_updates(page, updatedoffsets, updates, nupdated)  // strip dead TIDs
//	PageIndexMultiDelete(page, deletedoffsets, ndeleted)         // drop whole items
//	opaque->btpo_flags &= ~BTP_HAS_GARBAGE
//
// The order is not cosmetic: BOTH offset arrays are page offset numbers in the
// PRE-deletion coordinate space, and an update rewrites its item in place
// without moving any line pointer, so applying the updates first leaves the
// deletion offsets meaning exactly what the primary meant. Deleting first would
// shift every later offset down and silently rewrite the wrong tuples.
//
// XLOG_BTREE_DELETE is an opcode goopg never emits — its own index cleanup
// rides RecordKindBtreeVacuum, and it has no LP_DEAD "simple deletion" pass at
// all (see the deferral ledger) — but it is ordinary traffic in a real PG's
// crash tail: every index scan that finds a killed heap tuple marks the entry,
// and the next insert that needs room runs the deletion pass.
//
// Like pgvacuum.go this is FORMAT-FREE: recovery holds a relfilenode and no
// catalog, so items move as raw bytes and are never parsed as keys.

import (
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// SizeOfBtreeUpdate is SizeOfBtreeUpdate (nbtxlog.h:271): the fixed head of one
// xl_btree_update, a single uint16 ndeletedtids, followed by that many uint16
// posting-list offsets.
const SizeOfBtreeUpdate = 2

// PostingUpdate is one xl_btree_update paired with the page offset number it
// applies to (upstream keeps the two in separate arrays inside the record's
// block data; nothing else ever needs them apart).
//
// DeleteTIDs are indexes INTO the posting list's own TID array — 0-based, as
// nbtxlog.h:258-263 is at pains to say — NOT page offset numbers.
type PostingUpdate struct {
	Offset     uint16
	DeleteTIDs []uint16
}

// ReplayDeletePage is upstream's btree_xlog_delete page work: apply the
// posting-list updates, drop the deleted items, and clear the page's
// BTP_HAS_GARBAGE hint.
//
// Upstream's XLOG_BTREE_DELETE deliberately does NOT clear btpo_cycleid (its
// own comment: "Do *not* clear the vacuum cycle ID"), unlike XLOG_BTREE_VACUUM
// which does. goopg's opaque has no such field either way, so the two redos
// differ in nothing here and share this function.
//
// Idempotency is the caller's (pd_lsn vs the record's end-LSN), as for
// ReplayDedupPage.
func ReplayDeletePage(page storage.Page, deleted []uint16, updates []PostingUpdate) error {
	if err := ReplayPostingUpdates(page, updates); err != nil {
		return err
	}
	if len(deleted) > 0 {
		// ReplayVacuumDelete is PageIndexMultiDelete + the same hint clear.
		return ReplayVacuumDelete(page, deleted)
	}
	// nupdated-only record: no item goes away, so skip the rebuild and clear
	// the hint directly (upstream reaches the same two lines with ndeleted 0).
	if op := readOpaque(page); op.HasGarbage() {
		op.Flags &^= BTHasGarbage
		writeOpaque(page, op)
	}
	return nil
}

// ReplayPostingUpdates is upstream's `btree_xlog_updates` (nbtxlog.c:556-597):
// each named posting-list tuple is rewritten WITHOUT the TIDs the primary
// found dead, and overwritten in place at the same offset number.
//
// A posting list is how a deduplicated index stores N heap TIDs under one key,
// and its TIDs die one at a time — so index cleanup cannot express the work as
// "delete these items" alone. That is the whole reason this second array
// exists, and the reason goopg refused such records until this slice: goopg's
// own VACUUM re-marshals the surviving TIDs as separate items, a change of page
// SHAPE that offset numbers cannot describe, so it logs a full-page image
// instead (wal.EncodeBtreeVacuumPG). Replaying only the deletions from a real
// PG's record would leave dead TIDs on the page — index entries pointing at
// heap slots that have been vacuumed away and reused.
//
// Every update is validated before the first byte of the page changes: a
// partially-applied update array would leave a page no primary ever wrote.
func ReplayPostingUpdates(page storage.Page, updates []PostingUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	count, err := storage.PageLinePointerCount(page)
	if err != nil {
		return fmt.Errorf("btree: replay posting updates: read line pointers: %w", err)
	}
	minoff := pgFirstDataSlot(page)

	rewritten := make([][]byte, len(updates))
	for i, u := range updates {
		if u.Offset < minoff || int(u.Offset) > count {
			return fmt.Errorf("btree: replay posting update %d: offset %d outside data range [%d,%d]",
				i, u.Offset, minoff, count)
		}
		// AllowDead: an LP_DEAD line pointer's bytes are still the tuple the
		// record describes, and the flag is an unlogged hint — refusing to read
		// through it would make redo depend on hint state the WAL never carried.
		raw, err := storage.PageGetItemRawAllowDead(page, u.Offset)
		if err != nil {
			return fmt.Errorf("btree: replay posting update %d: read item %d: %w", i, u.Offset, err)
		}
		updated, err := updatePostingRaw(raw, u.DeleteTIDs)
		if err != nil {
			return fmt.Errorf("btree: replay posting update %d at offset %d: %w", i, u.Offset, err)
		}
		rewritten[i] = updated
	}
	for i, u := range updates {
		// The rewrite is never longer than the original (TIDs only leave), so
		// PageReplaceItemRaw takes its in-place branch and no line pointer
		// moves — which is what keeps the deletion offsets valid afterwards.
		if err := storage.PageReplaceItemRaw(page, u.Offset, rewritten[i]); err != nil {
			return fmt.Errorf("btree: replay posting update %d: overwrite item %d: %w", i, u.Offset, err)
		}
	}
	return nil
}

// updatePostingRaw is upstream's `_bt_update_posting` (nbtdedup.c): rebuild a
// posting-list tuple over the TIDs that survive, where `deleteTIDs` indexes the
// original TID array.
//
// It reproduces upstream's two outcomes rather than always re-forming a
// posting, because the tuple's SHAPE depends on the survivor count:
//
//   - more than one survivor: a posting again, sized exactly as
//     `_bt_form_posting` would (PGBTPostingRaw), so the byte image matches what
//     the primary wrote;
//   - exactly one: a PLAIN non-pivot tuple whose t_tid is the surviving heap
//     TID and whose declared size is the key material's length — the alt-TID
//     bit goes off, the array disappears, and the tuple keeps `keysize` bytes
//     (upstream's `newsize = keysize`, padding included).
//
// Deleting every TID is upstream's `Assert(nhtids > 0)`: the primary emits a
// whole-item deletion for that case instead, so a record claiming it is
// malformed.
func updatePostingRaw(raw []byte, deleteTIDs []uint16) ([]byte, error) {
	if !isPostingRaw(raw) {
		return nil, fmt.Errorf("item is not a posting list")
	}
	postingOffset, n, err := postingBounds(raw)
	if err != nil {
		return nil, err
	}
	if len(deleteTIDs) == 0 {
		return nil, fmt.Errorf("update deletes no TIDs from a %d-TID posting list", n)
	}
	if len(deleteTIDs) >= n {
		return nil, fmt.Errorf("update deletes %d of %d TIDs, leaving none (the primary deletes the whole item instead)",
			len(deleteTIDs), n)
	}
	// Strictly ascending and in range is what the primary builds
	// (_bt_delitems_delete walks each posting once, in order) and what
	// upstream's redo loop ASSUMES — its single `d` cursor advances only on a
	// match, so a repeated or out-of-order index would silently keep a TID the
	// record says is dead. Validate rather than inherit the assumption.
	for i, off := range deleteTIDs {
		if i > 0 && off <= deleteTIDs[i-1] {
			return nil, fmt.Errorf("deleted TID offsets not strictly ascending at %d", off)
		}
		if int(off) >= n {
			return nil, fmt.Errorf("deleted TID offset %d outside the posting list's %d entries", off, n)
		}
	}
	tidAt := func(i int) storage.ItemPointer {
		off := postingOffset + i*SizeOfItemPointerData
		return PGItemPointerAt(raw[off : off+SizeOfItemPointerData])
	}
	keep := make([]storage.ItemPointer, 0, n-len(deleteTIDs))
	d := 0
	for i := range n {
		if d < len(deleteTIDs) && int(deleteTIDs[d]) == i {
			d++
			continue
		}
		keep = append(keep, tidAt(i))
	}
	base := raw[:postingOffset]
	if len(keep) > 1 {
		return PGBTPostingRaw(base, keep), nil
	}
	// Single survivor: undo BTreeTupleSetPosting, exactly as parsePostingRaw's
	// tuple branch does when it hands a posting's base back to a caller.
	out := append([]byte(nil), base...)
	binary.LittleEndian.PutUint16(out[6:8], pgTInfo(out)&^IndexAltTIDMask)
	pgPutIndexTupleSize(out, postingOffset)
	PutPGItemPointer(out, keep[0])
	return out, nil
}
