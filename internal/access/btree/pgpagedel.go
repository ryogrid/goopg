package btree

// PG-format page-deletion phase 2 replay helpers — the page mutations upstream's
// `btree_xlog_unlink_page` (nbtxlog.c:850-1005) performs for
// XLOG_BTREE_UNLINK_PAGE / XLOG_BTREE_UNLINK_PAGE_META. M0130-S11.5d-2.
//
// These are deliberately PG-level (PGBTPageOpaque / BTP_* flags), not the legacy
// BTPageOpaque round-trip `ReplaySetSiblingNext` and friends use: a fully
// deleted page carries BTP_HAS_FULLXID, which `legacyFlags` has no counterpart
// for and therefore drops. Reading a deleted page through the legacy translation
// and writing it back would silently clear the bit that says its contents are a
// BTDeletedPageData, turning the safexid into unreadable garbage for
// BTPageIsRecyclable.

import (
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// SizeOfBTDeletedPageData is upstream's BTDeletedPageData (nbtree.h): a single
// FullTransactionId, stored at PageGetContents of a deleted page. It is the
// entire "contents" of such a page — a deleted page has no line pointers at all.
const SizeOfBTDeletedPageData = 8

// ReplayUnlinkTargetPage rewrites the unlink target as an empty deleted page —
// block 0 of XLOG_BTREE_UNLINK_PAGE (`btree_xlog_unlink_page`,
// nbtxlog.c:894-914). M0130-S11.5d-2.
//
// As with the half-dead page in phase 1, upstream recreates the page from
// scratch rather than patching it, and for the same reason: a deleted page IS
// its shape. It has no items, its pd_lower covers exactly one
// BTDeletedPageData, and everything a reader may still ask of it — which
// siblings it used to sit between (`_bt_walk_left` follows the links of deleted
// pages), what level it was on, and from which XID it is safe to RECYCLE —
// lives in the opaque and that one 8-byte payload. So none of it needs a
// full-page image and all of it is in the record.
//
// `leftsib`/`rightsib` are in upstream's spelling (P_NONE == 0 for "none"),
// which is what the record carries.
//
// The BTP_LEAF bit is set from `level == 0` rather than preserved: the page has
// just been re-initialised, so there is nothing to preserve, and upstream
// derives it the same way. BTP_HALF_DEAD is necessarily gone — a page cannot be
// half-dead and deleted at once, and BTPageSetDeleted clears it explicitly.
func ReplayUnlinkTargetPage(page storage.Page, leftsib, rightsib storage.BlockNumber, level uint32, safexid uint64) error {
	if err := InitPGBTPage(page); err != nil {
		return fmt.Errorf("btree: replay unlink target init: %w", err)
	}
	flags := BTPDeleted | BTPHasFullXID
	if level == 0 {
		flags |= BTPLeaf
	}
	WritePGOpaque(page, PGBTPageOpaque{
		Prev:  leftsib,
		Next:  rightsib,
		Level: level,
		Flags: flags,
	})
	// BTPageSetDeleted's header surgery: the contents area holds exactly the
	// BTDeletedPageData and the free space is closed up against the special
	// area, so PageGetFreeSpace reports 0 and no item can ever be added.
	h := storage.MustHeader(page)
	h.SetLower(uint16(storage.SizeOfPageHeaderData + SizeOfBTDeletedPageData))
	h.SetUpper(h.Special())
	binary.LittleEndian.PutUint64(page[storage.SizeOfPageHeaderData:storage.SizeOfPageHeaderData+SizeOfBTDeletedPageData], safexid)
	return nil
}

// PGDeletedPageSafeXid returns the deleted page's BTPageGetDeleteXid, i.e. the
// FullTransactionId written by ReplayUnlinkTargetPage / upstream's
// BTPageSetDeleted. ok is false when the page is not a BTP_HAS_FULLXID deleted
// page (upstream's pre-v4 deleted pages carried a 32-bit btpo_xact in the
// opaque instead; goopg never writes that form).
func PGDeletedPageSafeXid(page storage.Page) (safexid uint64, ok bool) {
	op := ReadPGOpaque(page)
	if op.Flags&BTPDeleted == 0 || op.Flags&BTPHasFullXID == 0 {
		return 0, false
	}
	return binary.LittleEndian.Uint64(page[storage.SizeOfPageHeaderData : storage.SizeOfPageHeaderData+SizeOfBTDeletedPageData]), true
}

// ReplayUnlinkLeftSibling repoints the deleted page's left sibling past it —
// block 1 of XLOG_BTREE_UNLINK_PAGE (nbtxlog.c:882-893). M0130-S11.5d-2.
//
// Upstream writes `pageop->btpo_next = rightsib` and nothing else, and this must
// not go through `pgWriteNextSibling` (which also drops the high key when the
// page becomes rightmost): `rightsib` is never P_NONE in a legal unlink record,
// because upstream never deletes a rightmost page, so the left sibling stays
// non-rightmost and its existing high key stays exactly as valid as it was —
// the separator it carries still bounds the same key range, since the deleted
// page held no keys.
func ReplayUnlinkLeftSibling(page storage.Page, rightsib storage.BlockNumber) error {
	op := ReadPGOpaque(page)
	op.Next = rightsib
	WritePGOpaque(page, op)
	return nil
}

// ReplayUnlinkRightSibling repoints the deleted page's right sibling back past
// it — block 2 of XLOG_BTREE_UNLINK_PAGE (nbtxlog.c:916-925). M0130-S11.5d-2.
//
// `leftsib` is in upstream's spelling and may legitimately be P_NONE: the
// deleted page can be leftmost on its level, in which case the right sibling
// becomes the new leftmost page.
func ReplayUnlinkRightSibling(page storage.Page, leftsib storage.BlockNumber) error {
	op := ReadPGOpaque(page)
	op.Prev = leftsib
	WritePGOpaque(page, op)
	return nil
}
