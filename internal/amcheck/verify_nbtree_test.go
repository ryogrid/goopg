package amcheck

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/storage"
)

// btSpecial returns the byte offset where the B-tree opaque special area
// begins, mirroring btree.go's btSpecialOffset (BlockSize - SizeOfBTPageOpaque).
func btSpecial() int { return storage.BlockSize - btree.SizeOfBTPageOpaque }

// makeMetaPage builds a metapage (block 0) carrying the given magic and
// version, mirroring btree.writeMeta's layout (payload at SizeOfPageHeaderData:
// magic, version, ...). The remaining metadata fields are left zero — the
// verify tier only inspects magic and version. It self-checks the bytes through
// the real decoder so a future layout change fails loudly here rather than
// silently exercising garbage.
func makeMetaPage(t *testing.T, magic, version uint32) storage.Page {
	t.Helper()
	p := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(p); err != nil {
		t.Fatalf("InitPage: %v", err)
	}
	off := storage.SizeOfPageHeaderData
	binary.LittleEndian.PutUint32(p[off:off+4], magic)
	binary.LittleEndian.PutUint32(p[off+4:off+8], version)
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
	if err := storage.InitPage(p); err != nil {
		t.Fatalf("InitPage: %v", err)
	}
	off := btSpecial()
	binary.LittleEndian.PutUint32(p[off+8:off+12], level) // Level
	binary.LittleEndian.PutUint16(p[off+12:off+14], flags)
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

// btItemRaw marshals a B-tree line-pointer item in the on-disk layout parseItem
// expects: keyLen(2) | block(4) | offset(2) | key. The TID is left zero — the
// item-order / high-key tier compares only keys.
func btItemRaw(key []byte) []byte {
	raw := make([]byte, 8+len(key))
	binary.LittleEndian.PutUint16(raw[0:2], uint16(len(key)))
	copy(raw[8:], key)
	return raw
}

// makeItemsPage builds a non-meta B-tree page carrying keys as line pointers in
// slot order, with the given opaque flags/level/next-sibling and optional high
// key. It sets pd_special/pd_upper to the B-tree special offset before adding
// items (mirroring btree.initPage) so item data grows above the opaque area
// instead of clobbering it, then writes the opaque bytes (mirroring
// btree.writeOpaque). A non-nil highKey sets BTHasHighKey. Self-checks the
// decoded opaque and key sequence through the real readers so a layout change
// fails loudly here rather than silently exercising garbage.
func makeItemsPage(t *testing.T, flags uint16, level uint32, next storage.BlockNumber, highKey []byte, keys ...[]byte) storage.Page {
	t.Helper()
	p := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(p); err != nil {
		t.Fatalf("InitPage: %v", err)
	}
	h := storage.MustHeader(p)
	h.SetSpecial(uint16(btSpecial()))
	h.SetUpper(uint16(btSpecial()))
	for i, k := range keys {
		if _, err := storage.PageAddItemRaw(p, btItemRaw(k)); err != nil {
			t.Fatalf("PageAddItemRaw[%d]: %v", i, err)
		}
	}
	if highKey != nil {
		flags |= btree.BTHasHighKey
	}
	off := btSpecial()
	binary.LittleEndian.PutUint32(p[off+4:off+8], uint32(next)) // Next
	binary.LittleEndian.PutUint32(p[off+8:off+12], level)       // Level
	binary.LittleEndian.PutUint16(p[off+12:off+14], flags)      // Flags
	binary.LittleEndian.PutUint16(p[off+14:off+16], uint16(len(highKey)))
	copy(p[off+16:off+16+len(highKey)], highKey)

	op := btree.ParseOpaque(p)
	if op.Flags != flags || op.Level != level || op.Next != next {
		t.Fatalf("makeItemsPage opaque self-check: got %+v, want flags=%#x level=%d next=%d", op, flags, level, next)
	}
	got, err := btree.PageItemKeys(p)
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

// Equal adjacent keys violate the strict-less item-order invariant: goopg dedups
// equal keys into a single posting item, so two physical slots can never share a
// separator key on a healthy page.
func TestVerifyBtreeItemOrder_DuplicateKeysViolation(t *testing.T) {
	p := makeItemsPage(t, btree.BTLeaf, 0, 5, nil, k(1), k(2), k(2))
	rs := VerifyBtreeItemOrder(p, 4, "ix")
	if len(rs) != 1 || rs[0].Msg != `item order invariant violated for index "ix"` {
		t.Fatalf("duplicate keys: want single item-order finding, got %+v", rs)
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

// A rightmost page (Next == InvalidBlockNumber) has no high key to honour even
// if a stale high key value lingers in its opaque area.
func TestVerifyBtreeItemOrder_RightmostNoHighKeyCheck(t *testing.T) {
	p := makeItemsPage(t, btree.BTLeaf, 0, storage.InvalidBlockNumber, k(3), k(1), k(5))
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
