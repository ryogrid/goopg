package amcheck

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/storage"
)

// blobFmt is the on-page key format every test in this package builds its
// pages in: goopg's opaque order-preserving key blobs, which is the zero
// btree.IndexFormat. The tiers take the format from their caller (it is a
// per-index catalog property the page bytes do not carry — see the note above
// KeyComparator in verify_nbtree.go), and these tests are bytes-only, so they
// state the blob choice here once rather than at ~60 call sites.
var blobFmt = btree.IndexFormat{}

// btSpecial returns the byte offset where the B-tree opaque special area
// begins, mirroring btree.go's btSpecialOffset (BlockSize - SizeOfBTPageOpaque).
func btSpecial() int { return storage.BlockSize - btree.SizeOfBTPageOpaque }

// makeMetaPage builds a metapage (block 0) carrying the given magic and
// version, on the upstream shape M0130-S11.3 flipped to: a PG-format page
// (16-byte special area, BTP_META) with BTMetaPageData at PageGetContents. The
// remaining metadata fields are left zero — the verify tier only inspects
// magic and version. It self-checks the bytes through the real decoder so a
// future layout change fails loudly here rather than silently exercising
// garbage.
func makeMetaPage(t *testing.T, magic, version uint32) storage.Page {
	t.Helper()
	p := make(storage.Page, storage.BlockSize)
	if err := btree.InitPGMetaPage(p, 0, 0, true); err != nil {
		t.Fatalf("InitPGMetaPage: %v", err)
	}
	m := btree.ReadPGMetaPage(p)
	m.Magic, m.Version = magic, version
	btree.WritePGMetaPage(p, m)
	if got := btree.ParseMeta(p); got.Magic != magic || got.Version != version {
		t.Fatalf("makeMetaPage self-check: ParseMeta=%+v, want magic=%#x version=%d", got, magic, version)
	}
	return p
}

// makeDataPage builds a non-meta B-tree page with the given opaque flags and
// level, mirroring btree.writeOpaque's layout (Prev,Next,Level,Flags,HighKeyLen
// in the special area). Self-checks through btree.ParseOpaque.
func makeDataPage(t *testing.T, flags uint16, level uint32) storage.Page {
	t.Helper()
	p := make(storage.Page, storage.BlockSize)
	if err := btree.InitPGBTPage(p); err != nil {
		t.Fatalf("InitPGBTPage: %v", err)
	}
	btree.WritePGOpaque(p, btree.PGBTPageOpaque{Level: level, Flags: pgFlagsForTest(flags)})
	op := btree.ParseOpaque(p)
	if op.Flags != flags || op.Level != level {
		t.Fatalf("makeDataPage self-check: ParseOpaque flags=%#x level=%d, want flags=%#x level=%d", op.Flags, op.Level, flags, level)
	}
	return p
}

func TestVerifyBtreePage_MetaPageClean(t *testing.T) {
	p := makeMetaPage(t, btree.BTreeMagic, btree.BTreeVersion)
	if rs := VerifyBtreePage(p, btree.MetaBlock, "ix"); len(rs) != 0 {
		t.Fatalf("clean metapage reported %d: %+v", len(rs), rs)
	}
}

func TestVerifyBtreePage_MetaPageBadMagic(t *testing.T) {
	p := makeMetaPage(t, btree.BTreeMagic^0xdead, btree.BTreeVersion)
	rs := VerifyBtreePage(p, btree.MetaBlock, "ix")
	if len(rs) != 1 {
		t.Fatalf("bad-magic metapage reported %d, want 1: %+v", len(rs), rs)
	}
	want := `index "ix" meta page is corrupt`
	if rs[0].Msg != want {
		t.Fatalf("msg = %q, want %q", rs[0].Msg, want)
	}
	if rs[0].Block != btree.MetaBlock {
		t.Fatalf("block = %d, want %d", rs[0].Block, btree.MetaBlock)
	}
}

func TestVerifyBtreePage_MetaPageBadVersion(t *testing.T) {
	bad := btree.BTreeVersion + 99
	p := makeMetaPage(t, btree.BTreeMagic, bad)
	rs := VerifyBtreePage(p, btree.MetaBlock, "ix")
	if len(rs) != 1 {
		t.Fatalf("bad-version metapage reported %d, want 1: %+v", len(rs), rs)
	}
	want := "version mismatch in index \"ix\": file version 103, current version 4, minimum supported version 4"
	if rs[0].Msg != want {
		t.Fatalf("msg = %q, want %q", rs[0].Msg, want)
	}
}

// A bad magic masks a bad version: upstream returns after the first conclusive
// metapage problem, so only the magic finding surfaces.
func TestVerifyBtreePage_MetaPageMagicMasksVersion(t *testing.T) {
	p := makeMetaPage(t, btree.BTreeMagic^1, btree.BTreeVersion+7)
	rs := VerifyBtreePage(p, btree.MetaBlock, "ix")
	if len(rs) != 1 || rs[0].Msg != `index "ix" meta page is corrupt` {
		t.Fatalf("want single meta-corrupt finding, got %+v", rs)
	}
}

func TestVerifyBtreePage_LeafLevelZeroClean(t *testing.T) {
	p := makeDataPage(t, btree.BTLeaf, 0)
	if rs := VerifyBtreePage(p, 1, "ix"); len(rs) != 0 {
		t.Fatalf("clean leaf reported %d: %+v", len(rs), rs)
	}
}

func TestVerifyBtreePage_InternalNonZeroClean(t *testing.T) {
	p := makeDataPage(t, 0, 2) // not leaf, level 2
	if rs := VerifyBtreePage(p, 5, "ix"); len(rs) != 0 {
		t.Fatalf("clean internal reported %d: %+v", len(rs), rs)
	}
}

func TestVerifyBtreePage_LeafBadLevel(t *testing.T) {
	p := makeDataPage(t, btree.BTLeaf, 3)
	rs := VerifyBtreePage(p, 7, "ix")
	if len(rs) != 1 {
		t.Fatalf("bad leaf level reported %d, want 1: %+v", len(rs), rs)
	}
	want := `invalid leaf page level 3 for block 7 in index "ix"`
	if rs[0].Msg != want {
		t.Fatalf("msg = %q, want %q", rs[0].Msg, want)
	}
	if rs[0].Block != 7 {
		t.Fatalf("block = %d, want 7", rs[0].Block)
	}
}

func TestVerifyBtreePage_InternalLevelZero(t *testing.T) {
	p := makeDataPage(t, 0, 0) // not leaf, level 0 == corrupt
	rs := VerifyBtreePage(p, 9, "ix")
	if len(rs) != 1 {
		t.Fatalf("internal level-0 reported %d, want 1: %+v", len(rs), rs)
	}
	want := `invalid internal page level 0 for block 9 in index "ix"`
	if rs[0].Msg != want {
		t.Fatalf("msg = %q, want %q", rs[0].Msg, want)
	}
}

// A fully deleted page type-puns its level field, so the level checks are
// suppressed even when leaf-with-nonzero-level would otherwise fire.
func TestVerifyBtreePage_DeletedPageSuppressesLevelCheck(t *testing.T) {
	p := makeDataPage(t, btree.BTLeaf|btree.BTDeleted, 42)
	if rs := VerifyBtreePage(p, 11, "ix"); len(rs) != 0 {
		t.Fatalf("deleted page reported %d, want 0: %+v", len(rs), rs)
	}
}

// A root page that is also a leaf (single-page tree) sits at level 0 and is
// clean — guards against the leaf check misfiring on the common new-tree shape.
func TestVerifyBtreePage_RootLeafClean(t *testing.T) {
	p := makeDataPage(t, btree.BTLeaf|btree.BTRoot, 0)
	if rs := VerifyBtreePage(p, 1, "ix"); len(rs) != 0 {
		t.Fatalf("root+leaf page reported %d, want 0: %+v", len(rs), rs)
	}
}

// makeCountPage builds a non-meta B-tree page whose header claims exactly
// `count` line pointers, by bumping pd_lower to header + count*itemIDSize. The
// item bodies are NOT materialised — a count above btree.MaxItemsPerPage cannot
// physically fit, so this is the only way to exercise the item-count ceiling:
// the on-disk corruption it detects is precisely a pd_lower that claims more
// line pointers than the page could ever hold. The opaque area is written as a
// clean leaf-at-0 so the level checks pass first and the count check is reached.
func makeCountPage(t *testing.T, count int) storage.Page {
	t.Helper()
	const itemIDSize = 4
	p := makeDataPage(t, btree.BTLeaf, 0)
	storage.MustHeader(p).SetLower(uint16(storage.SizeOfPageHeaderData + count*itemIDSize))
	got, err := storage.PageLinePointerCount(p)
	if err != nil {
		t.Fatalf("makeCountPage self-check PageLinePointerCount: %v", err)
	}
	if got != count {
		t.Fatalf("makeCountPage self-check: line-pointer count = %d, want %d", got, count)
	}
	return p
}

// MaxItemsPerPage is derived from goopg's per-item footprint, not PG's; assert
// the exact value so a layout change (itemPrefixSize / line-pointer size) trips
// this test rather than silently shifting the ceiling.
func TestBtreeMaxItemsPerPageValue(t *testing.T) {
	const want = (storage.BlockSize - storage.SizeOfPageHeaderData) / (4 + 8) // 8168/12 = 680
	if btree.MaxItemsPerPage != want {
		t.Fatalf("btree.MaxItemsPerPage = %d, want %d", btree.MaxItemsPerPage, want)
	}
}

// A page at exactly the ceiling is clean; one item over the ceiling is a finding.
func TestVerifyBtreePage_ItemCountAtCeilingClean(t *testing.T) {
	p := makeCountPage(t, btree.MaxItemsPerPage)
	if rs := VerifyBtreePage(p, 3, "ix"); len(rs) != 0 {
		t.Fatalf("page at ceiling reported %d, want 0: %+v", len(rs), rs)
	}
}

func TestVerifyBtreePage_ItemCountExceedsCeiling(t *testing.T) {
	p := makeCountPage(t, btree.MaxItemsPerPage+1)
	rs := VerifyBtreePage(p, 3, "ix")
	if len(rs) != 1 {
		t.Fatalf("over-ceiling page reported %d, want 1: %+v", len(rs), rs)
	}
	want := `Number of items on block 3 of index "ix" exceeds MaxIndexTuplesPerPage (680)`
	if rs[0].Msg != want {
		t.Fatalf("msg = %q, want %q", rs[0].Msg, want)
	}
	if rs[0].Block != 3 {
		t.Fatalf("block = %d, want 3", rs[0].Block)
	}
}

// A corrupt pd_lower whose line-pointer area is not an itemIDSize multiple is
// surfaced as a damaged-page finding rather than a Go error or a panic.
func TestVerifyBtreePage_DamagedLinePointerArea(t *testing.T) {
	p := makeDataPage(t, btree.BTLeaf, 0)
	storage.MustHeader(p).SetLower(storage.SizeOfPageHeaderData + 3) // not a multiple of 4
	rs := VerifyBtreePage(p, 6, "ix")
	if len(rs) != 1 {
		t.Fatalf("damaged line-pointer area reported %d, want 1: %+v", len(rs), rs)
	}
	if !strings.Contains(rs[0].Msg, `index "ix" has a damaged page at block 6`) {
		t.Fatalf("msg = %q, want damaged-page finding", rs[0].Msg)
	}
}

// The deleted-page early return is reached before the count check, so a deleted
// page with an over-ceiling pd_lower is still suppressed (matches the existing
// level-check suppression).
func TestVerifyBtreePage_DeletedPageSuppressesItemCount(t *testing.T) {
	p := makeDataPage(t, btree.BTLeaf|btree.BTDeleted, 0)
	storage.MustHeader(p).SetLower(uint16(storage.SizeOfPageHeaderData + (btree.MaxItemsPerPage+5)*4))
	if rs := VerifyBtreePage(p, 8, "ix"); len(rs) != 0 {
		t.Fatalf("deleted page reported %d, want 0: %+v", len(rs), rs)
	}
}

// pgInitTestPage initialises an upstream-shaped B-tree page and, when the page
// is NOT rightmost, installs the P_HIKEY placeholder that upstream requires
// there. Since M0130-S11.2b the first line pointer of a non-rightmost page IS
// the high key, so a builder that starts writing data at offset 1 silently
// loses its first item to the high-key reader.
//
// The placeholder is all-0xFF, i.e. above every key these tests use, so the
// high-key invariant tier stays quiet on pages built for the other tiers.
func pgInitTestPage(t *testing.T, p storage.Page, next storage.BlockNumber, level uint32, flags uint16) {
	t.Helper()
	if err := btree.InitPGBTPage(p); err != nil {
		t.Fatalf("InitPGBTPage: %v", err)
	}
	pgNext := next
	if next == storage.InvalidBlockNumber {
		pgNext = btree.PNone
	}
	btree.WritePGOpaque(p, btree.PGBTPageOpaque{Next: pgNext, Level: level, Flags: pgFlagsForTest(flags)})
	if pgNext != btree.PNone {
		if _, err := storage.PageAddItemRaw(p, btItemRaw([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})); err != nil {
			t.Fatalf("PageAddItemRaw P_HIKEY placeholder: %v", err)
		}
	}
}

// pgFlagsForTest mirrors btree's own legacy->BTP_* flag translation for the
// page builders here, which speak the engine's in-memory flag names. It is
// deliberately a duplicate of an unexported mapping: the point of these tests
// is to construct pages INDEPENDENTLY of the writer, so importing the writer's
// translator would hide a drift in it.
func pgFlagsForTest(legacy uint16) uint16 {
	var out uint16
	for _, m := range []struct{ legacy, pg uint16 }{
		{btree.BTLeaf, btree.BTPLeaf},
		{btree.BTRoot, btree.BTPRoot},
		{btree.BTDeleted, btree.BTPDeleted},
		{btree.BTIncompleteSplit, btree.BTPIncompleteSplit},
		{btree.BTHalfDead, btree.BTPHalfDead},
		{btree.BTHasGarbage, btree.BTPHasGarbage},
	} {
		if legacy&m.legacy != 0 {
			out |= m.pg
		}
	}
	return out
}

// btItemRaw marshals a plain (non-pivot) B-tree line-pointer item through the
// engine's own encoder. The TID is left zero — the item-order / high-key tier
// compares only keys.
func btItemRaw(key []byte) []byte {
	return btree.PGBTItemRaw(key, storage.ItemPointer{})
}

// btHighKeyRaw marshals a P_HIKEY separator, which since M0130-S11.4 slice 3a
// is a PIVOT tuple (INDEX_ALT_TID_MASK + natts) rather than a plain item.
func btHighKeyRaw(key []byte) []byte {
	return btree.PGBTPivotRaw(key, 0)
}

// makeItemsPage builds a non-meta B-tree page carrying keys as line pointers in
// data-slot order, with the given opaque flags/level/next-sibling and optional
// high key.
//
// Since M0130-S11.2b the page is built the way the engine builds one: an
// upstream 16-byte special area (btree.InitPGBTPage + btree.WritePGOpaque, with
// the P_NONE sentinel translation), the high key as an ordinary item at P_HIKEY
// so data starts at P_FIRSTKEY, and no "has high key" flag — presence is
// derived from btpo_next. `next` is given in the engine's in-memory spelling
// (storage.InvalidBlockNumber for "rightmost"), and a non-nil highKey therefore
// requires a real sibling. Self-checks the decoded opaque and key sequence
// through the real readers so a layout change fails loudly here rather than
// silently exercising garbage.
func makeItemsPage(t *testing.T, flags uint16, level uint32, next storage.BlockNumber, highKey []byte, keys ...[]byte) storage.Page {
	t.Helper()
	p := make(storage.Page, storage.BlockSize)
	if highKey != nil && next == storage.InvalidBlockNumber {
		t.Fatalf("makeItemsPage: a high key requires a right sibling (upstream derives presence from btpo_next)")
	}
	if highKey != nil {
		if err := btree.InitPGBTPage(p); err != nil {
			t.Fatalf("InitPGBTPage: %v", err)
		}
		btree.WritePGOpaque(p, btree.PGBTPageOpaque{Next: next, Level: level, Flags: pgFlagsForTest(flags)})
		if _, err := storage.PageAddItemRaw(p, btHighKeyRaw(highKey)); err != nil {
			t.Fatalf("PageAddItemRaw high key: %v", err)
		}
	} else {
		// No separator asked for: pgInitTestPage still has to install the
		// P_HIKEY placeholder when the page is non-rightmost, since data may
		// not start at offset 1 there.
		pgInitTestPage(t, p, next, level, flags)
	}
	for i, k := range keys {
		if _, err := storage.PageAddItemRaw(p, btItemRaw(k)); err != nil {
			t.Fatalf("PageAddItemRaw[%d]: %v", i, err)
		}
	}

	op := btree.ParseOpaque(p)
	if op.Flags != flags || op.Level != level || op.Next != next {
		t.Fatalf("makeItemsPage opaque self-check: got %+v, want flags=%#x level=%d next=%d", op, flags, level, next)
	}
	got, err := blobFmt.PageItemKeys(p)
	if err != nil {
		t.Fatalf("makeItemsPage PageItemKeys self-check: %v", err)
	}
	if len(got) != len(keys) {
		t.Fatalf("makeItemsPage PageItemKeys self-check: got %d keys, want %d", len(got), len(keys))
	}
	for i := range keys {
		if !bytes.Equal(got[i], keys[i]) {
			t.Fatalf("makeItemsPage PageItemKeys self-check slot %d: got %x, want %x", i, got[i], keys[i])
		}
	}
	return p
}

// k builds a single-byte key — keys compare via bytes.Compare, so {1} < {2}.
func k(b byte) []byte { return []byte{b} }

func TestVerifyBtreeItemOrder_LeafAscendingClean(t *testing.T) {
	p := makeItemsPage(t, btree.BTLeaf, 0, 5, nil, k(1), k(2), k(3))
	if rs := VerifyBtreeItemOrder(p, 1, "ix"); len(rs) != 0 {
		t.Fatalf("ascending leaf reported %d: %+v", len(rs), rs)
	}
}

func TestVerifyBtreeItemOrder_ItemOrderViolation(t *testing.T) {
	p := makeItemsPage(t, btree.BTLeaf, 0, 5, nil, k(1), k(3), k(2))
	rs := VerifyBtreeItemOrder(p, 4, "ix")
	if len(rs) != 1 {
		t.Fatalf("out-of-order leaf reported %d, want 1: %+v", len(rs), rs)
	}
	want := `item order invariant violated for index "ix"`
	if rs[0].Msg != want {
		t.Fatalf("msg = %q, want %q", rs[0].Msg, want)
	}
	if rs[0].Block != 4 {
		t.Fatalf("block = %d, want 4", rs[0].Block)
	}
}

// Equal adjacent keys are NOT an item-order violation: goopg's insert/split
// path (btree.CompareKeys, used everywhere the engine orders items) has no
// TID tiebreak, and dedupConsolidate's posting-list promotion never runs
// outside BulkCreate (.ralph/deferral_ledger.md 2026-07-07), so a healthy
// page routinely carries several plain line pointers sharing one key — e.g.
// every non-HOT UPDATE leaves the old index entry in place until VACUUM.
// (Was TestVerifyBtreeItemOrder_DuplicateKeysViolation — the strict-less
// assumption this test used to encode was itself the bug behind the
// AI-20260708-064334-001 nightly false-positive corruption report; see
// .ralph/deferral_ledger.md 2026-07-08.)
func TestVerifyBtreeItemOrder_DuplicateKeysAllowed(t *testing.T) {
	p := makeItemsPage(t, btree.BTLeaf, 0, 5, nil, k(1), k(2), k(2))
	if rs := VerifyBtreeItemOrder(p, 4, "ix"); len(rs) != 0 {
		t.Fatalf("duplicate keys: want no findings, got %+v", rs)
	}
}

// A genuine decrease among duplicate-key runs (not just a tie) is still a
// violation.
func TestVerifyBtreeItemOrder_DecreaseAfterDuplicateViolation(t *testing.T) {
	p := makeItemsPage(t, btree.BTLeaf, 0, 5, nil, k(2), k(2), k(1))
	rs := VerifyBtreeItemOrder(p, 4, "ix")
	if len(rs) != 1 || rs[0].Msg != `item order invariant violated for index "ix"` {
		t.Fatalf("decrease after duplicate: want single item-order finding, got %+v", rs)
	}
}

// Leaf high-key check is <=: a key equal to the high key is allowed (suffix
// truncation can make a leaf high key an untruncated copy of the last item).
func TestVerifyBtreeItemOrder_LeafHighKeyEqualOK(t *testing.T) {
	p := makeItemsPage(t, btree.BTLeaf, 0, 5, k(2), k(1), k(2))
	if rs := VerifyBtreeItemOrder(p, 2, "ix"); len(rs) != 0 {
		t.Fatalf("leaf key == high key reported %d, want 0: %+v", len(rs), rs)
	}
}

func TestVerifyBtreeItemOrder_LeafHighKeyExceeded(t *testing.T) {
	p := makeItemsPage(t, btree.BTLeaf, 0, 5, k(3), k(1), k(5))
	rs := VerifyBtreeItemOrder(p, 2, "ix")
	if len(rs) != 1 {
		t.Fatalf("leaf key > high key reported %d, want 1: %+v", len(rs), rs)
	}
	want := `high key invariant violated for index "ix"`
	if rs[0].Msg != want {
		t.Fatalf("msg = %q, want %q", rs[0].Msg, want)
	}
}

// Internal high-key check is strict <: a key equal to the high key is a
// violation (an internal high key is "just another separator", unique on its
// level).
func TestVerifyBtreeItemOrder_InternalHighKeyEqualViolation(t *testing.T) {
	// Internal page: leftmost negative-infinity downlink (empty key), then 1, 2.
	p := makeItemsPage(t, 0, 1, 5, k(2), nil, k(1), k(2))
	rs := VerifyBtreeItemOrder(p, 3, "ix")
	if len(rs) != 1 || rs[0].Msg != `high key invariant violated for index "ix"` {
		t.Fatalf("internal key == high key: want single high-key finding, got %+v", rs)
	}
}

// The leftmost negative-infinity downlink (empty key) on an internal page
// satisfies both invariants without a special case — empty compares strictly
// less than any real separator and strictly less than the high key.
func TestVerifyBtreeItemOrder_InternalNegInfinityClean(t *testing.T) {
	p := makeItemsPage(t, 0, 1, 5, k(3), nil, k(1), k(2))
	if rs := VerifyBtreeItemOrder(p, 3, "ix"); len(rs) != 0 {
		t.Fatalf("internal neg-infinity page reported %d, want 0: %+v", len(rs), rs)
	}
}

// A rightmost page has NO high key at all — since M0130-S11.2b presence is
// derived from btpo_next, so there is no stale separator to mistake for one and
// item 1 is real data. k(5) would violate a k(3) separator; on a rightmost page
// nothing is reported.
func TestVerifyBtreeItemOrder_RightmostNoHighKeyCheck(t *testing.T) {
	p := makeItemsPage(t, btree.BTLeaf, 0, storage.InvalidBlockNumber, nil, k(1), k(5))
	if rs := VerifyBtreeItemOrder(p, 9, "ix"); len(rs) != 0 {
		t.Fatalf("rightmost page reported %d, want 0 (high key not enforced): %+v", len(rs), rs)
	}
}

func TestVerifyBtreeItemOrder_MetaPageNil(t *testing.T) {
	p := makeMetaPage(t, btree.BTreeMagic, btree.BTreeVersion)
	if rs := VerifyBtreeItemOrder(p, btree.MetaBlock, "ix"); len(rs) != 0 {
		t.Fatalf("metapage reported %d, want 0: %+v", len(rs), rs)
	}
}

// Deleted pages hold no live items; out-of-order bytes on one are suppressed.
func TestVerifyBtreeItemOrder_DeletedPageNil(t *testing.T) {
	p := makeItemsPage(t, btree.BTLeaf|btree.BTDeleted, 0, 5, nil, k(3), k(1))
	if rs := VerifyBtreeItemOrder(p, 11, "ix"); len(rs) != 0 {
		t.Fatalf("deleted page reported %d, want 0: %+v", len(rs), rs)
	}
}

// --- Cross-page sibling-link tier (VerifyBtreeLevelSiblingLinks) -------------

// makeLinkedPage builds a non-meta page carrying explicit prev/next sibling
// links plus a level and flags, so a horizontal level walk can be assembled from
// a map. Self-checks the links through btree.ParseOpaque.
func makeLinkedPage(t *testing.T, prev, next storage.BlockNumber, level uint32, flags uint16) storage.Page {
	t.Helper()
	p := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(p); err != nil {
		t.Fatalf("InitPage: %v", err)
	}
	off := btSpecial()
	binary.LittleEndian.PutUint32(p[off:off+4], uint32(prev))   // Prev
	binary.LittleEndian.PutUint32(p[off+4:off+8], uint32(next)) // Next
	binary.LittleEndian.PutUint32(p[off+8:off+12], level)       // Level
	binary.LittleEndian.PutUint16(p[off+12:off+14], flags)      // Flags
	op := btree.ParseOpaque(p)
	if op.Prev != prev || op.Next != next || op.Level != level || op.Flags != flags {
		t.Fatalf("makeLinkedPage self-check: got %+v, want prev=%d next=%d level=%d flags=%#x", op, prev, next, level, flags)
	}
	return p
}

// mapSource turns a block→page map into a PageSource; an unknown block errors
// (exercises the damaged-page path).
func mapSource(pages map[storage.BlockNumber]storage.Page) PageSource {
	return func(b storage.BlockNumber) (storage.Page, error) {
		p, ok := pages[b]
		if !ok {
			return nil, fmt.Errorf("no such block %d", b)
		}
		return p, nil
	}
}

const none = storage.InvalidBlockNumber

// A clean three-page level (1 → 2 → 3) with mutually-agreeing links and a
// uniform level walks without findings.
func TestVerifyBtreeSiblingLinks_CleanLevel(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		1: makeLinkedPage(t, none, 2, 0, btree.BTLeaf),
		2: makeLinkedPage(t, 1, 3, 0, btree.BTLeaf),
		3: makeLinkedPage(t, 2, none, 0, btree.BTLeaf),
	}
	if rs := VerifyBtreeLevelSiblingLinks(mapSource(pages), 1, "ix"); len(rs) != 0 {
		t.Fatalf("clean level reported %d, want 0: %+v", len(rs), rs)
	}
}

// Block 3's back-link points at 9 instead of 2 — the sibling links disagree.
func TestVerifyBtreeSiblingLinks_BackLinkMismatch(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		1: makeLinkedPage(t, none, 2, 0, btree.BTLeaf),
		2: makeLinkedPage(t, 1, 3, 0, btree.BTLeaf),
		3: makeLinkedPage(t, 9, none, 0, btree.BTLeaf),
	}
	rs := VerifyBtreeLevelSiblingLinks(mapSource(pages), 1, "ix")
	if len(rs) != 1 {
		t.Fatalf("mismatch reported %d, want 1: %+v", len(rs), rs)
	}
	want := `left link/right link pair in index "ix" not in agreement`
	if rs[0].Msg != want {
		t.Fatalf("msg = %q, want %q", rs[0].Msg, want)
	}
	if rs[0].Block != 3 {
		t.Fatalf("block = %d, want 3", rs[0].Block)
	}
}

// The leftmost page is exempt from the back-link check: a non-P_NONE Prev on the
// first page (left sibling half-dead) is tolerated, matching upstream's
// leftcurrent != P_NONE gate.
func TestVerifyBtreeSiblingLinks_LeftmostPrevExempt(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		1: makeLinkedPage(t, 7, 2, 0, btree.BTLeaf), // Prev=7 but it's leftmost
		2: makeLinkedPage(t, 1, none, 0, btree.BTLeaf),
	}
	if rs := VerifyBtreeLevelSiblingLinks(mapSource(pages), 1, "ix"); len(rs) != 0 {
		t.Fatalf("leftmost-prev-exempt reported %d, want 0: %+v", len(rs), rs)
	}
}

// A page on the level whose btpo_level differs from the leftmost page's level
// trips the per-level uniformity check.
func TestVerifyBtreeSiblingLinks_LevelMismatch(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		1: makeLinkedPage(t, none, 2, 1, 0),
		2: makeLinkedPage(t, 1, none, 2, 0), // level 2, expected 1
	}
	rs := VerifyBtreeLevelSiblingLinks(mapSource(pages), 1, "ix")
	if len(rs) != 1 {
		t.Fatalf("level mismatch reported %d, want 1: %+v", len(rs), rs)
	}
	want := `leftmost down link for level points to block in index "ix" whose level is not one level down`
	if rs[0].Msg != want {
		t.Fatalf("msg = %q, want %q", rs[0].Msg, want)
	}
}

// A two-page cycle (1 → 2 → 1) is caught when block 1 is revisited.
func TestVerifyBtreeSiblingLinks_CircularChain(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		1: makeLinkedPage(t, none, 2, 0, btree.BTLeaf),
		2: makeLinkedPage(t, 1, 1, 0, btree.BTLeaf), // Next loops back to 1
	}
	rs := VerifyBtreeLevelSiblingLinks(mapSource(pages), 1, "ix")
	if len(rs) != 1 {
		t.Fatalf("cycle reported %d, want 1: %+v", len(rs), rs)
	}
	want := `circular link chain found in block 1 of index "ix"`
	if rs[0].Msg != want {
		t.Fatalf("msg = %q, want %q", rs[0].Msg, want)
	}
}

// A self-loop (1 → 1) is the degenerate cycle and is caught on the second visit.
func TestVerifyBtreeSiblingLinks_SelfLoop(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		1: makeLinkedPage(t, none, 1, 0, btree.BTLeaf),
	}
	rs := VerifyBtreeLevelSiblingLinks(mapSource(pages), 1, "ix")
	if len(rs) != 1 || rs[0].Msg != `circular link chain found in block 1 of index "ix"` {
		t.Fatalf("self-loop want single circular finding, got %+v", rs)
	}
}

// Reaching a fully deleted page through a sibling link is corruption in
// readonly mode.
func TestVerifyBtreeSiblingLinks_DeletedReachable(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		1: makeLinkedPage(t, none, 2, 0, btree.BTLeaf),
		2: makeLinkedPage(t, 1, none, 0, btree.BTLeaf|btree.BTDeleted),
	}
	rs := VerifyBtreeLevelSiblingLinks(mapSource(pages), 1, "ix")
	if len(rs) != 1 {
		t.Fatalf("deleted-reachable reported %d, want 1: %+v", len(rs), rs)
	}
	want := `downlink or sibling link points to deleted block in index "ix"`
	if rs[0].Msg != want {
		t.Fatalf("msg = %q, want %q", rs[0].Msg, want)
	}
}

// A right link to a block the source cannot supply surfaces as a damaged-page
// finding, not a panic.
func TestVerifyBtreeSiblingLinks_DanglingRightLink(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		1: makeLinkedPage(t, none, 2, 0, btree.BTLeaf),
		// block 2 absent
	}
	rs := VerifyBtreeLevelSiblingLinks(mapSource(pages), 1, "ix")
	if len(rs) != 1 {
		t.Fatalf("dangling right link reported %d, want 1: %+v", len(rs), rs)
	}
	if !strings.Contains(rs[0].Msg, `has a damaged page at block 2`) {
		t.Fatalf("msg = %q, want a damaged-page finding for block 2", rs[0].Msg)
	}
}

// A leftmost of the metapage is a damaged starting point (the metapage carries
// no sibling links).
func TestVerifyBtreeSiblingLinks_MetaLeftmost(t *testing.T) {
	rs := VerifyBtreeLevelSiblingLinks(mapSource(nil), btree.MetaBlock, "ix")
	if len(rs) != 1 || !strings.Contains(rs[0].Msg, "metapage is not part of a level") {
		t.Fatalf("meta-leftmost want single damaged finding, got %+v", rs)
	}
}

// A single rightmost leaf (the new-tree shape: one page, Prev=None, Next=None)
// is a clean level of length one.
func TestVerifyBtreeSiblingLinks_SinglePageLevel(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		1: makeLinkedPage(t, none, none, 0, btree.BTLeaf|btree.BTRoot),
	}
	if rs := VerifyBtreeLevelSiblingLinks(mapSource(pages), 1, "ix"); len(rs) != 0 {
		t.Fatalf("single-page level reported %d, want 0: %+v", len(rs), rs)
	}
}

// --- Cross-level downlink tier (VerifyBtreeParentDownlinks) ------------------

// btDownlinkRaw marshals an internal-page downlink through the engine's own
// encoder. Since M0130-S11.4 slice 3a that is a PIVOT tuple: the child block
// lives in t_tid's block half and the offset half carries natts, not a line
// pointer.
func btDownlinkRaw(key []byte, child storage.BlockNumber) []byte {
	return btree.PGBTPivotRaw(key, child)
}

// dl is a (separator key, child block) downlink for makeInternalPage.
type dl struct {
	key   []byte
	child storage.BlockNumber
}

// makeInternalPage builds an internal B-tree page carrying (key, child)
// downlinks in slot order, with the given level and next-sibling link. By v0
// convention the leftmost downlink's key should be empty (negative infinity);
// callers pass that explicitly. Self-checks the decoded downlinks through
// btree.PageDownlinks so a layout change fails loudly here.
func makeInternalPage(t *testing.T, level uint32, next storage.BlockNumber, downlinks ...dl) storage.Page {
	t.Helper()
	p := make(storage.Page, storage.BlockSize)
	// Flags = 0 → internal (not leaf), not deleted.
	pgInitTestPage(t, p, next, level, 0)
	for i, d := range downlinks {
		if _, err := storage.PageAddItemRaw(p, btDownlinkRaw(d.key, d.child)); err != nil {
			t.Fatalf("PageAddItemRaw[%d]: %v", i, err)
		}
	}
	got, err := blobFmt.PageDownlinks(p)
	if err != nil {
		t.Fatalf("makeInternalPage PageDownlinks self-check: %v", err)
	}
	if len(got) != len(downlinks) {
		t.Fatalf("makeInternalPage self-check: got %d downlinks, want %d", len(got), len(downlinks))
	}
	for i, d := range downlinks {
		if got[i].Child != d.child || !bytes.Equal(got[i].Key, d.key) {
			t.Fatalf("makeInternalPage self-check slot %d: got {key=%x child=%d}, want {key=%x child=%d}",
				i, got[i].Key, got[i].Child, d.key, d.child)
		}
	}
	op := btree.ParseOpaque(p)
	if op.IsLeaf() || op.IsDeleted() || op.Level != level || op.Next != next {
		t.Fatalf("makeInternalPage opaque self-check: got %+v, want internal level=%d next=%d", op, level, next)
	}
	return p
}

// A clean internal parent (level 1) with two downlinks: a negative-infinity
// child (block 2, keys all >= empty) and a separator-keyed child (block 3, keys
// all >= k(5)). No findings.
func TestVerifyBtreeParentDownlinks_Clean(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		1: makeInternalPage(t, 1, none, dl{nil, 2}, dl{k(5), 3}),
		2: makeItemsPage(t, btree.BTLeaf, 0, 3, k(5), k(1), k(3)),
		3: makeItemsPage(t, btree.BTLeaf, 0, none, nil, k(5), k(7)),
	}
	if rs := VerifyBtreeParentDownlinks(mapSource(pages), 1, "ix", blobFmt); len(rs) != 0 {
		t.Fatalf("clean parent reported %d, want 0: %+v", len(rs), rs)
	}
}

// A child key below the parent's separator violates the down-link lower bound.
func TestVerifyBtreeParentDownlinks_LowerBoundViolation(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		1: makeInternalPage(t, 1, none, dl{nil, 2}, dl{k(5), 3}),
		2: makeItemsPage(t, btree.BTLeaf, 0, 3, k(5), k(1), k(3)),
		3: makeItemsPage(t, btree.BTLeaf, 0, none, nil, k(4), k(7)), // k(4) < k(5)
	}
	rs := VerifyBtreeParentDownlinks(mapSource(pages), 1, "ix", blobFmt)
	want := `down-link lower bound invariant violated for index "ix"`
	if len(rs) != 1 || rs[0].Msg != want {
		t.Fatalf("lower-bound case = %+v, want single %q", rs, want)
	}
	if rs[0].Block != 3 {
		t.Fatalf("block = %d, want child 3", rs[0].Block)
	}
}

// A downlink to a deleted child is corruption (readonly mode).
func TestVerifyBtreeParentDownlinks_DownlinkToDeleted(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		1: makeInternalPage(t, 1, none, dl{nil, 2}, dl{k(5), 3}),
		2: makeItemsPage(t, btree.BTLeaf, 0, 3, k(5), k(1), k(3)),
		3: makeItemsPage(t, btree.BTLeaf|btree.BTDeleted, 0, none, nil),
	}
	rs := VerifyBtreeParentDownlinks(mapSource(pages), 1, "ix", blobFmt)
	want := `downlink to deleted page found in index "ix"`
	if len(rs) != 1 || rs[0].Msg != want {
		t.Fatalf("deleted-child case = %+v, want single %q", rs, want)
	}
	if rs[0].Block != 3 {
		t.Fatalf("block = %d, want child 3", rs[0].Block)
	}
}

// A child whose level is not exactly one below the parent's is flagged before
// the lower-bound loop.
func TestVerifyBtreeParentDownlinks_ChildLevelNotOneDown(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		1: makeInternalPage(t, 1, none, dl{nil, 2}, dl{k(5), 3}),
		2: makeItemsPage(t, btree.BTLeaf, 0, 3, k(5), k(1), k(3)),
		3: makeItemsPage(t, btree.BTLeaf, 2, none, nil, k(5)), // level 2, expected 0
	}
	rs := VerifyBtreeParentDownlinks(mapSource(pages), 1, "ix", blobFmt)
	want := `downlink points to block in index "ix" whose level is not one level down`
	if len(rs) != 1 || rs[0].Msg != want {
		t.Fatalf("level case = %+v, want single %q", rs, want)
	}
}

// The child's own negative-infinity item (the first item of an INTERNAL child,
// stored with the empty key) is below the parent separator but must be skipped,
// so an otherwise-clean internal child yields no finding.
func TestVerifyBtreeParentDownlinks_NegInfChildItemSkipped(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		1: makeInternalPage(t, 2, none, dl{k(5), 3}),
		// Internal child at level 1: leftmost neg-inf (empty) downlink, then
		// real separators k(6), k(8) — all real keys >= the parent's k(5).
		3: makeInternalPage(t, 1, none, dl{nil, 10}, dl{k(6), 11}, dl{k(8), 12}),
	}
	if rs := VerifyBtreeParentDownlinks(mapSource(pages), 1, "ix", blobFmt); len(rs) != 0 {
		t.Fatalf("internal child with neg-inf item reported %d, want 0: %+v", len(rs), rs)
	}
}

// A real (non-neg-inf) key below the parent separator on an internal child still
// trips the lower bound — the skip is only for the first item.
func TestVerifyBtreeParentDownlinks_InternalChildRealKeyBelowBound(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		1: makeInternalPage(t, 2, none, dl{k(5), 3}),
		3: makeInternalPage(t, 1, none, dl{nil, 10}, dl{k(4), 11}), // k(4) < k(5)
	}
	rs := VerifyBtreeParentDownlinks(mapSource(pages), 1, "ix", blobFmt)
	want := `down-link lower bound invariant violated for index "ix"`
	if len(rs) != 1 || rs[0].Msg != want {
		t.Fatalf("internal-child real-key case = %+v, want single %q", rs, want)
	}
}

// A leaf parentBlk has no downlinks to descend; nil.
func TestVerifyBtreeParentDownlinks_LeafParentNoFindings(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		1: makeItemsPage(t, btree.BTLeaf, 0, none, nil, k(1), k(2)),
	}
	if rs := VerifyBtreeParentDownlinks(mapSource(pages), 1, "ix", blobFmt); rs != nil {
		t.Fatalf("leaf parent reported %+v, want nil", rs)
	}
}

// The metapage carries no downlinks; nil (no read attempted).
func TestVerifyBtreeParentDownlinks_MetaPageNil(t *testing.T) {
	if rs := VerifyBtreeParentDownlinks(mapSource(nil), btree.MetaBlock, "ix", blobFmt); rs != nil {
		t.Fatalf("metapage reported %+v, want nil", rs)
	}
}

// An unreadable parent surfaces as a damaged-page finding, not a panic.
func TestVerifyBtreeParentDownlinks_DamagedParent(t *testing.T) {
	rs := VerifyBtreeParentDownlinks(mapSource(nil), 7, "ix", blobFmt)
	if len(rs) != 1 || !strings.Contains(rs[0].Msg, "has a damaged page at block 7") {
		t.Fatalf("damaged parent = %+v, want single damaged-page finding", rs)
	}
}

// An unreadable child (dangling downlink) surfaces as a damaged-page finding for
// the child block, not a panic.
func TestVerifyBtreeParentDownlinks_DanglingChild(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		1: makeInternalPage(t, 1, none, dl{nil, 99}), // child 99 absent
	}
	rs := VerifyBtreeParentDownlinks(mapSource(pages), 1, "ix", blobFmt)
	if len(rs) != 1 || !strings.Contains(rs[0].Msg, "has a damaged page at block 99") {
		t.Fatalf("dangling child = %+v, want single damaged-page finding for block 99", rs)
	}
}
