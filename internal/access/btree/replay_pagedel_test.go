package btree

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

func pagedelTestPage(t *testing.T, prev, next storage.BlockNumber, level uint32, flags uint16) storage.Page {
	t.Helper()
	p := make(storage.Page, storage.BlockSize)
	if err := InitPGBTPage(p); err != nil {
		t.Fatal(err)
	}
	WritePGOpaque(p, PGBTPageOpaque{Prev: prev, Next: next, Level: level, Flags: flags})
	return p
}

// TestReplayUnlinkTargetPageMatchesBTPageSetDeleted pins the deleted page's
// SHAPE against upstream BTPageSetDeleted (nbtree.h) + _bt_pageinit: flags
// BTP_DELETED|BTP_HAS_FULLXID (plus BTP_LEAF at level 0 and never
// BTP_HALF_DEAD), the links and level preserved from the record, pd_lower
// covering exactly one BTDeletedPageData, pd_upper closed against pd_special,
// and the safexid readable back out of the contents area. M0130-S11.5d-2.
func TestReplayUnlinkTargetPageMatchesBTPageSetDeleted(t *testing.T) {
	for _, tc := range []struct {
		name      string
		level     uint32
		wantFlags uint16
	}{
		{"leaf target", 0, BTPDeleted | BTPHasFullXID | BTPLeaf},
		{"internal target", 3, BTPDeleted | BTPHasFullXID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Start from a half-dead page with an item on it, as phase 1 left
			// it: the rewrite must drop both the item and BTP_HALF_DEAD.
			page := pagedelTestPage(t, 4, 6, tc.level, BTPHalfDead|BTPLeaf)
			if _, err := storage.PageAddItemRaw(page, PGBTPivotRaw(nil, 12)); err != nil {
				t.Fatal(err)
			}
			const safexid = uint64(0x3_0000_0007)
			if err := ReplayUnlinkTargetPage(page, 4, 6, tc.level, safexid); err != nil {
				t.Fatal(err)
			}
			op := ReadPGOpaque(page)
			if op.Flags != tc.wantFlags {
				t.Errorf("flags = %#x, want %#x", op.Flags, tc.wantFlags)
			}
			if op.Prev != 4 || op.Next != 6 || op.Level != tc.level || op.CycleID != 0 {
				t.Errorf("opaque = %+v, want prev 4 next 6 level %d cycleid 0", op, tc.level)
			}
			h := storage.MustHeader(page)
			if got := h.Lower(); got != storage.SizeOfPageHeaderData+SizeOfBTDeletedPageData {
				t.Errorf("pd_lower = %d, want %d", got, storage.SizeOfPageHeaderData+SizeOfBTDeletedPageData)
			}
			if h.Upper() != h.Special() {
				t.Errorf("pd_upper = %d, want pd_special %d", h.Upper(), h.Special())
			}
			got, ok := PGDeletedPageSafeXid(page)
			if !ok || got != safexid {
				t.Errorf("safexid = %#x (ok=%v), want %#x", got, ok, safexid)
			}

			// Idempotent: replay may re-run the record after a crash between
			// blocks, and the page it produces the second time must be
			// byte-identical (pd_lsn is stamped by the caller).
			again := bytes.Clone(page)
			if err := ReplayUnlinkTargetPage(again, 4, 6, tc.level, safexid); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(again, page) {
				t.Error("second replay produced different bytes")
			}
		})
	}
}

// TestReplayUnlinkSiblingsAreFlagLossless is the reason ReplayUnlinkLeftSibling
// / ReplayUnlinkRightSibling exist instead of the older ReplaySetSiblingNext /
// ReplaySetSiblingPrev: those round-trip the opaque through the legacy BT* flag
// word, which has no counterpart for BTP_HAS_FULLXID (or BTP_META) and silently
// drops it. Dropping it on a deleted page turns its BTDeletedPageData into
// unreadable garbage for BTPageIsRecyclable. The link fix upstream performs is a
// single field write, so nothing else on the page may move. M0130-S11.5d-2.
func TestReplayUnlinkSiblingsAreFlagLossless(t *testing.T) {
	const exotic = BTPDeleted | BTPHasFullXID | BTPIncompleteSplit
	t.Run("left sibling keeps flags and high key", func(t *testing.T) {
		page := pagedelTestPage(t, 3, 5, 1, exotic)
		hikey := PGBTPivotRaw([]byte("sep"), PNone)
		if _, err := storage.PageAddItemRaw(page, hikey); err != nil {
			t.Fatal(err)
		}
		if err := ReplayUnlinkLeftSibling(page, 6); err != nil {
			t.Fatal(err)
		}
		op := ReadPGOpaque(page)
		if op.Next != 6 {
			t.Errorf("btpo_next = %d, want 6", op.Next)
		}
		if op.Flags != exotic || op.Prev != 3 || op.Level != 1 {
			t.Errorf("opaque = %+v, want flags %#x prev 3 level 1 untouched", op, exotic)
		}
		got, ok, err := PGHighKeyRaw(page)
		if err != nil || !ok {
			t.Fatalf("high key gone after the link fix (ok=%v, err=%v)", ok, err)
		}
		if !bytes.Equal(got, hikey) {
			t.Errorf("high key = %x, want %x", got, hikey)
		}
	})
	t.Run("right sibling keeps flags", func(t *testing.T) {
		page := pagedelTestPage(t, 5, 7, 1, exotic)
		if err := ReplayUnlinkRightSibling(page, PNone); err != nil {
			t.Fatal(err)
		}
		op := ReadPGOpaque(page)
		if op.Prev != PNone {
			t.Errorf("btpo_prev = %d, want P_NONE", op.Prev)
		}
		if op.Flags != exotic || op.Next != 7 {
			t.Errorf("opaque = %+v, want flags %#x next 7 untouched", op, exotic)
		}
	})
	t.Run("the legacy helper is the lossy one", func(t *testing.T) {
		// Documents WHY the pair above exists, so a later loop cannot
		// "simplify" them back onto the legacy round-trip.
		page := pagedelTestPage(t, 5, 7, 1, exotic)
		if err := ReplaySetSiblingPrev(page, PNone); err != nil {
			t.Fatal(err)
		}
		if got := ReadPGOpaque(page).Flags; got&BTPHasFullXID != 0 {
			t.Skip("legacy flag translation now carries BTP_HAS_FULLXID; fold the helpers back together")
		}
	})
}

// TestPGDeletedPageSafeXidRejectsNonDeletedPages guards the reader's gate: a
// page without BTP_DELETED|BTP_HAS_FULLXID has no BTDeletedPageData, and its
// first 8 content bytes are a line pointer or a metapage field. Upstream's
// BTPageGetDeleteXid asserts the same two bits.
func TestPGDeletedPageSafeXidRejectsNonDeletedPages(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags uint16
	}{
		{"live leaf", BTPLeaf},
		{"half-dead leaf", BTPHalfDead | BTPLeaf},
		{"deleted without the fullxid bit", BTPDeleted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := PGDeletedPageSafeXid(pagedelTestPage(t, 4, 6, 0, tc.flags)); ok {
				t.Fatalf("%s reported a safexid", tc.name)
			}
		})
	}
}
