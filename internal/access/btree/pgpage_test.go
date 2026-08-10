package btree

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// newPGPage returns an upstream-shaped B-tree page whose btpo_next is `next`,
// so the P_FIRSTDATAKEY bias under test is the real one (derived from the
// sibling link, never from a flag).
func newPGPage(t *testing.T, next storage.BlockNumber) storage.Page {
	t.Helper()
	p := make(storage.Page, storage.BlockSize)
	if err := InitPGBTPage(p); err != nil {
		t.Fatalf("InitPGBTPage: %v", err)
	}
	WritePGOpaque(p, PGBTPageOpaque{Next: next, Flags: BTPLeaf})
	return p
}

func TestPGFirstDataKeyFollowsSiblingLink(t *testing.T) {
	if got := PGFirstDataKey(PGBTPageOpaque{Next: PNone}); got != PHiKey {
		t.Fatalf("rightmost P_FIRSTDATAKEY = %d, want P_HIKEY (%d)", got, PHiKey)
	}
	if got := PGFirstDataKey(PGBTPageOpaque{Next: 7}); got != PFirstKey {
		t.Fatalf("non-rightmost P_FIRSTDATAKEY = %d, want P_FIRSTKEY (%d)", got, PFirstKey)
	}
}

// The wrappers must hide the high key completely: a caller asking for data
// item 1 of a non-rightmost page gets the item at physical offset 2.
func TestPGDataAccessorsSkipHighKey(t *testing.T) {
	p := newPGPage(t, 9)
	hk := []byte("high-key")
	data := [][]byte{[]byte("d1"), []byte("d2"), []byte("d3")}
	if _, err := storage.PageAddItemRaw(p, hk); err != nil {
		t.Fatalf("add high key: %v", err)
	}
	for _, d := range data {
		if _, err := pgAddItemRaw(p, d); err != nil {
			t.Fatalf("pgAddItemRaw: %v", err)
		}
	}

	n, err := PGDataItemCount(p)
	if err != nil {
		t.Fatalf("PGDataItemCount: %v", err)
	}
	if n != len(data) {
		t.Fatalf("PGDataItemCount = %d, want %d (high key must not be counted)", n, len(data))
	}
	for i, want := range data {
		got, err := pgGetItemRaw(p, uint16(i+1))
		if err != nil {
			t.Fatalf("pgGetItemRaw(%d): %v", i+1, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("data slot %d = %q, want %q", i+1, got, want)
		}
		noCopy, err := pgGetItemRawNoCopy(p, uint16(i+1))
		if err != nil {
			t.Fatalf("pgGetItemRawNoCopy(%d): %v", i+1, err)
		}
		if !bytes.Equal(noCopy, want) {
			t.Fatalf("no-copy data slot %d = %q, want %q", i+1, noCopy, want)
		}
	}

	// And the high key itself is reachable only through the high-key reader.
	raw, ok, err := PGHighKeyRaw(p)
	if err != nil || !ok {
		t.Fatalf("PGHighKeyRaw: raw=%q ok=%v err=%v", raw, ok, err)
	}
	if !bytes.Equal(raw, hk) {
		t.Fatalf("PGHighKeyRaw = %q, want %q", raw, hk)
	}
}

// On a rightmost page there is no bias at all — data item 1 IS offset 1 — and
// PGHighKeyRaw reports absence rather than handing back the first data item.
func TestPGRightmostPageHasNoHighKey(t *testing.T) {
	p := newPGPage(t, PNone)
	if _, err := pgAddItemRaw(p, []byte("d1")); err != nil {
		t.Fatalf("pgAddItemRaw: %v", err)
	}
	slot, err := pgAddItemRaw(p, []byte("d2"))
	if err != nil {
		t.Fatalf("pgAddItemRaw: %v", err)
	}
	if slot != 2 {
		t.Fatalf("second item data slot = %d, want 2", slot)
	}
	if _, ok, err := PGHighKeyRaw(p); ok || err != nil {
		t.Fatalf("PGHighKeyRaw on rightmost page: ok=%v err=%v, want false/nil", ok, err)
	}
	got, err := pgGetItemRaw(p, 1)
	if err != nil {
		t.Fatalf("pgGetItemRaw: %v", err)
	}
	if !bytes.Equal(got, []byte("d1")) {
		t.Fatalf("rightmost data slot 1 = %q, want %q", got, "d1")
	}
	if err := pgSetHighKeyRaw(p, []byte("nope")); err == nil {
		t.Fatal("pgSetHighKeyRaw on a rightmost page: want error, got nil")
	}
}

// The split transition: a rightmost page acquires a right sibling and must
// grow a high key at P_HIKEY without disturbing the data it already holds.
func TestPGPromoteToNonRightmostShiftsDataItems(t *testing.T) {
	p := newPGPage(t, PNone)
	data := [][]byte{[]byte("aa"), []byte("bbb"), []byte("cccc")}
	for _, d := range data {
		if _, err := pgAddItemRaw(p, d); err != nil {
			t.Fatalf("pgAddItemRaw: %v", err)
		}
	}

	// Sibling link first: pgPromoteToNonRightmost refuses to run against a
	// page still advertising itself as rightmost, because the data-slot bias
	// it applies comes from that very field.
	if err := pgPromoteToNonRightmost(p, []byte("hk")); err == nil {
		t.Fatal("promote before the sibling link was set: want error, got nil")
	}
	op := ReadPGOpaque(p)
	op.Next = 42
	WritePGOpaque(p, op)
	if err := pgPromoteToNonRightmost(p, []byte("hk")); err != nil {
		t.Fatalf("pgPromoteToNonRightmost: %v", err)
	}

	n, err := PGDataItemCount(p)
	if err != nil {
		t.Fatalf("PGDataItemCount: %v", err)
	}
	if n != len(data) {
		t.Fatalf("data count after promote = %d, want %d", n, len(data))
	}
	for i, want := range data {
		got, err := pgGetItemRaw(p, uint16(i+1))
		if err != nil {
			t.Fatalf("pgGetItemRaw(%d): %v", i+1, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("data slot %d after promote = %q, want %q", i+1, got, want)
		}
	}
	raw, ok, err := PGHighKeyRaw(p)
	if err != nil || !ok || !bytes.Equal(raw, []byte("hk")) {
		t.Fatalf("high key after promote: raw=%q ok=%v err=%v", raw, ok, err)
	}

	// Replacing the high key with a different-length key must keep the data
	// items exactly where they are (no second shift).
	if err := pgSetHighKeyRaw(p, []byte("longer-hk")); err != nil {
		t.Fatalf("pgSetHighKeyRaw: %v", err)
	}
	raw, _, err = PGHighKeyRaw(p)
	if err != nil {
		t.Fatalf("PGHighKeyRaw: %v", err)
	}
	if !bytes.Equal(raw, []byte("longer-hk")) {
		t.Fatalf("replaced high key = %q, want %q", raw, "longer-hk")
	}
	if got, err := pgGetItemRaw(p, 1); err != nil || !bytes.Equal(got, data[0]) {
		t.Fatalf("data slot 1 after high-key replace = %q (err=%v), want %q", got, err, data[0])
	}
}

// The bulk-load shape: reserve P_HIKEY up front, fill data from P_FIRSTKEY,
// then either keep the reservation (fill it with a high key) or slide it away
// because the page turned out to be rightmost.
func TestPGReserveThenSlideLeftReproducesBulkLoadShape(t *testing.T) {
	p := newPGPage(t, PNone)
	if err := pgReserveHiKeySlot(p); err != nil {
		t.Fatalf("pgReserveHiKeySlot: %v", err)
	}
	if err := pgReserveHiKeySlot(p); err == nil {
		t.Fatal("second pgReserveHiKeySlot: want error (page no longer empty), got nil")
	}
	data := [][]byte{[]byte("k1"), []byte("k2")}
	for _, d := range data {
		if _, err := storage.PageAddItemRaw(p, d); err != nil {
			t.Fatalf("PageAddItemRaw: %v", err)
		}
	}
	// While the placeholder is present the page is physically shaped like a
	// non-rightmost page even though btpo_next still says rightmost — that is
	// precisely the window _bt_slideleft closes.
	if err := pgSlideLeft(p); err != nil {
		t.Fatalf("pgSlideLeft: %v", err)
	}
	n, err := PGDataItemCount(p)
	if err != nil {
		t.Fatalf("PGDataItemCount: %v", err)
	}
	if n != len(data) {
		t.Fatalf("data count after slide-left = %d, want %d", n, len(data))
	}
	for i, want := range data {
		got, err := pgGetItemRaw(p, uint16(i+1))
		if err != nil {
			t.Fatalf("pgGetItemRaw(%d): %v", i+1, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("data slot %d after slide-left = %q, want %q", i+1, got, want)
		}
	}
}

// Dead-marking and insert-at both have to speak data coordinates, or the
// LP_DEAD kill pass would eventually mark the high key dead.
func TestPGDataSlotMutatorsAddressDataItems(t *testing.T) {
	p := newPGPage(t, 5)
	if _, err := storage.PageAddItemRaw(p, []byte("hk")); err != nil {
		t.Fatalf("add high key: %v", err)
	}
	for _, d := range [][]byte{[]byte("d1"), []byte("d3")} {
		if _, err := pgAddItemRaw(p, d); err != nil {
			t.Fatalf("pgAddItemRaw: %v", err)
		}
	}
	if _, err := pgInsertItemRawAt(p, 2, []byte("d2")); err != nil {
		t.Fatalf("pgInsertItemRawAt: %v", err)
	}
	for i, want := range [][]byte{[]byte("d1"), []byte("d2"), []byte("d3")} {
		got, err := pgGetItemRaw(p, uint16(i+1))
		if err != nil {
			t.Fatalf("pgGetItemRaw(%d): %v", i+1, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("data slot %d = %q, want %q", i+1, got, want)
		}
	}

	if err := pgSetItemIDDead(p, 1); err != nil {
		t.Fatalf("pgSetItemIDDead: %v", err)
	}
	dead, err := pgItemIsDead(p, 1)
	if err != nil || !dead {
		t.Fatalf("pgItemIsDead(1) = %v (err=%v), want true", dead, err)
	}
	// The high key must be untouched by that kill.
	if hkDead, err := storage.PageItemIsDead(p, PHiKey); err != nil || hkDead {
		t.Fatalf("high key marked dead by a data-slot kill (dead=%v err=%v)", hkDead, err)
	}
	if raw, err := pgGetItemRawAllowDead(p, 1); err != nil || !bytes.Equal(raw, []byte("d1")) {
		t.Fatalf("pgGetItemRawAllowDead(1) = %q (err=%v)", raw, err)
	}
	if id, err := pgGetItemID(p, 2); err != nil || id.Flags != storage.ItemIDNormal {
		t.Fatalf("pgGetItemID(2) flags = %v (err=%v), want LP_NORMAL", id.Flags, err)
	}
	if err := pgReplaceItemRaw(p, 2, []byte("D2")); err != nil {
		t.Fatalf("pgReplaceItemRaw: %v", err)
	}
	if raw, err := pgGetItemRaw(p, 2); err != nil || !bytes.Equal(raw, []byte("D2")) {
		t.Fatalf("pgReplaceItemRaw did not address data slot 2: %q (err=%v)", raw, err)
	}
}

func TestPGSiblingSentinelTranslation(t *testing.T) {
	if got := pgSibling(storage.InvalidBlockNumber); got != PNone {
		t.Fatalf("pgSibling(InvalidBlockNumber) = %d, want P_NONE (%d)", got, PNone)
	}
	if got := pgSibling(3); got != 3 {
		t.Fatalf("pgSibling(3) = %d, want 3", got)
	}
	if got := legacySibling(PNone); got != storage.InvalidBlockNumber {
		t.Fatalf("legacySibling(P_NONE) = %d, want InvalidBlockNumber", got)
	}
	if got := legacySibling(3); got != 3 {
		t.Fatalf("legacySibling(3) = %d, want 3", got)
	}
}

// The three bits that MOVE are the whole point of this translation: a
// mechanical copy of the legacy flags word would tell a real PG that every
// non-rightmost goopg page is a metapage.
func TestPGFlagsTranslationMovesTheThreeDivergentBits(t *testing.T) {
	if got := pgFlags(0x0008); got != 0 {
		t.Fatalf("pgFlags(0x0008) = 0x%x, want 0 (goopg's old BTHasHighKey bit is unclaimed; upstream uses it for BTP_META)", got)
	}
	if got := pgFlags(BTIncompleteSplit); got != BTPIncompleteSplit {
		t.Fatalf("pgFlags(BTIncompleteSplit) = 0x%x, want 0x%x", got, BTPIncompleteSplit)
	}
	if got := pgFlags(BTHalfDead); got != BTPHalfDead {
		t.Fatalf("pgFlags(BTHalfDead) = 0x%x, want 0x%x", got, BTPHalfDead)
	}
	all := BTLeaf | BTRoot | BTDeleted | 0x0008 | BTIncompleteSplit | BTHalfDead | BTHasGarbage
	want := BTPLeaf | BTPRoot | BTPDeleted | BTPIncompleteSplit | BTPHalfDead | BTPHasGarbage
	if got := pgFlags(all); got != want {
		t.Fatalf("pgFlags(all legacy bits) = 0x%x, want 0x%x", got, want)
	}
	if pgFlags(all)&BTPMeta != 0 {
		t.Fatal("translated flags claim BTP_META — a real PG would treat the page as the metapage")
	}
}

// --- M0130-S11.2b flip guards -------------------------------------------
//
// These three cover the invariants the flip introduced that no pre-existing
// test could express, because before it the high key lived in the opaque area
// and was therefore immune to every page-level operation below.

// A page's on-disk bytes must round-trip through the engine's in-memory
// spelling without losing the sentinel or a flag. This is the encode/decode
// sibling-pair rule: readOpaque and writeOpaque are the only translators, and a
// disagreement between them corrupts silently rather than loudly.
func TestOpaqueRoundTripsThroughUpstreamBytes(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   BTPageOpaque
	}{
		{"rightmost leaf", BTPageOpaque{Prev: storage.InvalidBlockNumber, Next: storage.InvalidBlockNumber, Flags: BTLeaf | BTRoot}},
		{"interior leaf", BTPageOpaque{Prev: 4, Next: 9, Level: 0, Flags: BTLeaf | BTHasGarbage}},
		{"internal mid-split", BTPageOpaque{Prev: storage.InvalidBlockNumber, Next: 12, Level: 3, Flags: BTIncompleteSplit}},
		{"half-dead", BTPageOpaque{Prev: 2, Next: storage.InvalidBlockNumber, Level: 1, Flags: BTDeleted | BTHalfDead}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := make(storage.Page, storage.BlockSize)
			initPage(p, tc.op)
			if got := readOpaque(p); got != tc.op {
				t.Fatalf("round trip: got %+v, want %+v", got, tc.op)
			}
			// The bytes a real PG reads must be the upstream ones.
			if err := CheckPGBTPage(p, 1); err != nil {
				t.Fatalf("_bt_checkpage equivalent rejected the page: %v", err)
			}
			pg := ReadPGOpaque(p)
			if tc.op.Next == storage.InvalidBlockNumber && pg.Next != PNone {
				t.Fatalf("rightmost page wrote btpo_next=%d, want P_NONE", pg.Next)
			}
			if pg.Flags&BTPMeta != 0 {
				t.Fatal("a data page claims BTP_META to a real PG")
			}
		})
	}
}

// resetPageItems is used by every whole-page rewrite (split, dedup recovery,
// VACUUM kept-items, WAL replay). If it dropped the separator, the rewritten
// non-rightmost page would say its data starts at offset 2 while holding it at
// offset 1 — every read off by one, silently.
func TestResetPageItemsKeepsTheHighKey(t *testing.T) {
	p := make(storage.Page, storage.BlockSize)
	initPage(p, BTPageOpaque{Prev: storage.InvalidBlockNumber, Next: 6, Level: 0, Flags: BTLeaf})
	if err := pgSetHighKeyRaw(p, highKeyItem([]byte("sep")).marshal()); err != nil {
		t.Fatalf("pgSetHighKeyRaw: %v", err)
	}
	if _, err := pgAddItemRaw(p, item{key: []byte("k1")}.marshal()); err != nil {
		t.Fatalf("add data: %v", err)
	}

	resetPageItems(p)

	hk, ok, err := pageHighKey(p)
	if err != nil || !ok {
		t.Fatalf("high key after reset: ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(hk, []byte("sep")) {
		t.Fatalf("high key after reset = %q, want %q", hk, "sep")
	}
	if n, err := PGDataItemCount(p); err != nil || n != 0 {
		t.Fatalf("data items after reset = %d (err %v), want 0", n, err)
	}
	// And the refill must land at P_FIRSTKEY, not on top of the separator.
	if _, err := pgAddItemRaw(p, item{key: []byte("k2")}.marshal()); err != nil {
		t.Fatalf("refill: %v", err)
	}
	raw, err := pgGetItemRaw(p, 1)
	if err != nil {
		t.Fatalf("read refilled data item 1: %v", err)
	}
	it, err := parseItem(raw)
	if err != nil || !bytes.Equal(it.key, []byte("k2")) {
		t.Fatalf("data item 1 = %q (err %v), want %q", it.key, err, "k2")
	}
}

// Page deletion hands a left sibling the rightmost position. Because high-key
// presence is derived from btpo_next, the separator has to go at the same
// moment — otherwise P_FIRSTDATAKEY moves down onto the stale separator and the
// page reports it as its first data item forever after.
func TestWriteNextSiblingSlidesTheHighKeyAway(t *testing.T) {
	p := make(storage.Page, storage.BlockSize)
	initPage(p, BTPageOpaque{Prev: storage.InvalidBlockNumber, Next: 6, Level: 0, Flags: BTLeaf})
	if err := pgSetHighKeyRaw(p, highKeyItem([]byte("sep")).marshal()); err != nil {
		t.Fatalf("pgSetHighKeyRaw: %v", err)
	}
	for _, k := range []string{"k1", "k2"} {
		if _, err := pgAddItemRaw(p, item{key: []byte(k)}.marshal()); err != nil {
			t.Fatalf("add %s: %v", k, err)
		}
	}

	if err := pgWriteNextSibling(p, readOpaque(p), storage.InvalidBlockNumber); err != nil {
		t.Fatalf("pgWriteNextSibling: %v", err)
	}

	if _, ok, _ := pageHighKey(p); ok {
		t.Fatal("rightmost page still reports a high key")
	}
	n, err := PGDataItemCount(p)
	if err != nil || n != 2 {
		t.Fatalf("data items after slide-left = %d (err %v), want 2", n, err)
	}
	for i, want := range []string{"k1", "k2"} {
		raw, err := pgGetItemRaw(p, uint16(i+1))
		if err != nil {
			t.Fatalf("data item %d: %v", i+1, err)
		}
		it, perr := parseItem(raw)
		if perr != nil || !bytes.Equal(it.key, []byte(want)) {
			t.Fatalf("data item %d = %q (err %v), want %q", i+1, it.key, perr, want)
		}
	}

	// The reverse transition is a split's job and must be refused here.
	if err := pgWriteNextSibling(p, readOpaque(p), 8); err == nil {
		t.Fatal("gaining a right sibling outside a split was accepted; no separator exists to install")
	}
}
