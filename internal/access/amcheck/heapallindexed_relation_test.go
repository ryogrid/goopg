package amcheck

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/storage"
)

// btLeafRaw marshals a leaf-page line pointer in parseItem's on-disk layout
// (keyLen(2) | block(4) | offset(2) | key), setting BOTH the heap TID block and
// offset — btItemRaw leaves them zero and btDownlinkRaw sets only the block.
func btLeafRaw(key []byte, block storage.BlockNumber, offset uint16) []byte {
	return nbtree.PGBTItemRaw(key, storage.ItemPointer{Block: block, Offset: offset})
}

// le is a (key, heap TID) leaf entry for makeLeafPage.
type le struct {
	key    []byte
	block  storage.BlockNumber
	offset uint16
}

// makeLeafPage builds a leaf B-tree page carrying (key, TID) entries in slot
// order, with the given next-sibling link. Self-checks the decoded entries
// through btree.PageLeafEntries so a layout change fails loudly here.
func makeLeafPage(t *testing.T, next storage.BlockNumber, entries ...le) storage.Page {
	t.Helper()
	p := make(storage.Page, storage.BlockSize)
	pgInitTestPage(t, p, next, 0, nbtree.BTLeaf)
	for i, e := range entries {
		if _, err := storage.PageAddItemRaw(p, btLeafRaw(e.key, e.block, e.offset)); err != nil {
			t.Fatalf("PageAddItemRaw[%d]: %v", i, err)
		}
	}
	got, err := blobFmt.PageLeafEntries(p)
	if err != nil {
		t.Fatalf("makeLeafPage PageLeafEntries self-check: %v", err)
	}
	if len(got) != len(entries) {
		t.Fatalf("makeLeafPage self-check: got %d entries, want %d", len(got), len(entries))
	}
	return p
}

// makeMetaWithRoot builds a valid metapage (correct magic/version) whose Root
// points at the given block — makeMetaPage leaves Root zero, which the leaf walk
// reads as "no key level".
func makeMetaWithRoot(t *testing.T, root storage.BlockNumber) storage.Page {
	t.Helper()
	p := makeMetaPage(t, nbtree.BTreeMagic, nbtree.BTreeVersion)
	off := storage.SizeOfPageHeaderData
	binary.LittleEndian.PutUint32(p[off+8:off+12], uint32(root)) // Root
	if got := nbtree.ParseMeta(p); got.Root != root {
		t.Fatalf("makeMetaWithRoot self-check: Root=%d, want %d", got.Root, root)
	}
	return p
}

// makeDeletedLeaf builds a leaf page flagged deleted (no live items).
func makeDeletedLeaf(t *testing.T, next storage.BlockNumber) storage.Page {
	t.Helper()
	p := makeLinkedPage(t, none, next, 0, nbtree.BTLeaf|nbtree.BTDeleted)
	return p
}

// --- CollectBtreeLeafEntries -------------------------------------------------

// A single root-leaf tree (root == leaf at block 1) yields exactly its entries.
func TestCollectBtreeLeafEntries_SingleRootLeaf(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		nbtree.MetaBlock: makeMetaWithRoot(t, 1),
		1: makeLeafPage(t, none,
			le{k(1), 10, 1},
			le{k(2), 10, 2},
			le{k(3), 11, 5}),
	}
	got, err := CollectBtreeLeafEntries(mapSource(pages), blobFmt)
	if err != nil {
		t.Fatalf("CollectBtreeLeafEntries: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(got), got)
	}
	if got[0].TID.Block != 10 || got[0].TID.Offset != 1 {
		t.Fatalf("entry 0 TID = (%d,%d), want (10,1)", got[0].TID.Block, got[0].TID.Offset)
	}
	if got[2].TID.Block != 11 || got[2].TID.Offset != 5 {
		t.Fatalf("entry 2 TID = (%d,%d), want (11,5)", got[2].TID.Block, got[2].TID.Offset)
	}
}

// A two-level tree: meta → internal root (block 1) → leaves 2,3. The walk
// descends the leftmost downlink to leaf 2 then follows btpo_next to leaf 3,
// collecting all entries in left-to-right order.
func TestCollectBtreeLeafEntries_MultiLevel(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		nbtree.MetaBlock: makeMetaWithRoot(t, 1),
		1: makeInternalPage(t, 1, none,
			dl{nil, 2}, // leftmost (negative infinity) → leaf 2
			dl{k(5), 3}),
		2: makeLeafPage(t, 3, le{k(1), 20, 1}, le{k(2), 20, 2}),
		3: makeLeafPage(t, none, le{k(5), 21, 1}, le{k(7), 21, 2}),
	}
	got, err := CollectBtreeLeafEntries(mapSource(pages), blobFmt)
	if err != nil {
		t.Fatalf("CollectBtreeLeafEntries: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d entries, want 4: %+v", len(got), got)
	}
	wantTID := []storage.ItemPointer{{Block: 20, Offset: 1}, {Block: 20, Offset: 2}, {Block: 21, Offset: 1}, {Block: 21, Offset: 2}}
	for i, w := range wantTID {
		if got[i].TID != w {
			t.Fatalf("entry %d TID = %+v, want %+v", i, got[i].TID, w)
		}
	}
}

// An empty/absent key level (meta Root == 0 / metapage) yields no entries and no
// error.
func TestCollectBtreeLeafEntries_NoKeyLevel(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		nbtree.MetaBlock: makeMetaPage(t, nbtree.BTreeMagic, nbtree.BTreeVersion), // Root left 0
	}
	got, err := CollectBtreeLeafEntries(mapSource(pages), blobFmt)
	if err != nil {
		t.Fatalf("CollectBtreeLeafEntries: %v", err)
	}
	if got != nil {
		t.Fatalf("got %+v, want nil", got)
	}
}

// An empty leaf (root-leaf with zero items) yields zero entries, not an error.
func TestCollectBtreeLeafEntries_EmptyLeaf(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		nbtree.MetaBlock: makeMetaWithRoot(t, 1),
		1:               makeLeafPage(t, none),
	}
	got, err := CollectBtreeLeafEntries(mapSource(pages), blobFmt)
	if err != nil {
		t.Fatalf("CollectBtreeLeafEntries: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d entries, want 0", len(got))
	}
}

// A deleted leaf page reached on the sibling walk contributes no entries (its
// items are not live) but does not abort the walk.
func TestCollectBtreeLeafEntries_DeletedLeafSkipped(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		nbtree.MetaBlock: makeMetaWithRoot(t, 1),
		1:               makeLeafPage(t, 2, le{k(1), 30, 1}),
		2:               makeDeletedLeaf(t, 3),
		3:               makeLeafPage(t, none, le{k(9), 31, 1}),
	}
	got, err := CollectBtreeLeafEntries(mapSource(pages), blobFmt)
	if err != nil {
		t.Fatalf("CollectBtreeLeafEntries: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2 (deleted leaf skipped): %+v", len(got), got)
	}
}

// A sibling cycle (leaf 1 → leaf 2 → leaf 1) terminates with an error rather
// than looping forever — a truncated entry set would manufacture spurious
// heapallindexed reports, so this must surface, not silently truncate.
func TestCollectBtreeLeafEntries_SiblingCycle(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		nbtree.MetaBlock: makeMetaWithRoot(t, 1),
		1:               makeLeafPage(t, 2, le{k(1), 40, 1}),
		2:               makeLeafPage(t, 1, le{k(2), 40, 2}), // points back to 1
	}
	_, err := CollectBtreeLeafEntries(mapSource(pages), blobFmt)
	if err == nil {
		t.Fatal("expected error on sibling cycle, got nil")
	}
	if !strings.Contains(err.Error(), "circular leaf sibling chain") {
		t.Fatalf("error = %q, want circular leaf sibling chain", err)
	}
}

// A read error during the descent surfaces as an error.
func TestCollectBtreeLeafEntries_DescentReadError(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		nbtree.MetaBlock: makeMetaWithRoot(t, 1),
		1:               makeInternalPage(t, 1, none, dl{nil, 2}), // child 2 absent from map
	}
	_, err := CollectBtreeLeafEntries(mapSource(pages), blobFmt)
	if err == nil {
		t.Fatal("expected error on missing child block, got nil")
	}
}

// --- VerifyBtreeHeapAllIndexedRelation ---------------------------------------

// Every heap-formed entry is present in the index → no findings.
func TestVerifyBtreeHeapAllIndexedRelation_Clean(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		nbtree.MetaBlock: makeMetaWithRoot(t, 1),
		1: makeLeafPage(t, none,
			le{k(1), 10, 1},
			le{k(2), 10, 2}),
	}
	heap := []nbtree.LeafEntry{
		{Key: k(1), TID: storage.ItemPointer{Block: 10, Offset: 1}},
		{Key: k(2), TID: storage.ItemPointer{Block: 10, Offset: 2}},
	}
	reports, err := VerifyBtreeHeapAllIndexedRelation(mapSource(pages), blobFmt, heap, "ix", "tbl", 0x9e3779b97f4a7c15)
	if err != nil {
		t.Fatalf("VerifyBtreeHeapAllIndexedRelation: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("got %d reports, want 0: %+v", len(reports), reports)
	}
}

// A heap tuple whose index entry is missing from the index → one finding with
// the upstream-verbatim message and the heap block.
func TestVerifyBtreeHeapAllIndexedRelation_MissingEntry(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		nbtree.MetaBlock: makeMetaWithRoot(t, 1),
		1:               makeLeafPage(t, none, le{k(1), 10, 1}),
	}
	heap := []nbtree.LeafEntry{
		{Key: k(1), TID: storage.ItemPointer{Block: 10, Offset: 1}}, // present
		{Key: k(2), TID: storage.ItemPointer{Block: 12, Offset: 7}}, // absent
	}
	reports, err := VerifyBtreeHeapAllIndexedRelation(mapSource(pages), blobFmt, heap, "ix", "tbl", 0x9e3779b97f4a7c15)
	if err != nil {
		t.Fatalf("VerifyBtreeHeapAllIndexedRelation: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("got %d reports, want 1: %+v", len(reports), reports)
	}
	want := "heap tuple (12,7) from table \"tbl\" lacks matching index tuple within index \"ix\""
	if reports[0].Msg != want {
		t.Fatalf("msg = %q, want %q", reports[0].Msg, want)
	}
	if reports[0].Block != 12 {
		t.Fatalf("block = %d, want 12", reports[0].Block)
	}
}

// An index-walk error (unreadable index) propagates rather than yielding a
// truncated fingerprint set (which would manufacture spurious reports).
func TestVerifyBtreeHeapAllIndexedRelation_IndexWalkError(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		nbtree.MetaBlock: makeMetaWithRoot(t, 1), // block 1 absent → read error
	}
	heap := []nbtree.LeafEntry{{Key: k(1), TID: storage.ItemPointer{Block: 10, Offset: 1}}}
	_, err := VerifyBtreeHeapAllIndexedRelation(mapSource(pages), blobFmt, heap, "ix", "tbl", 1)
	if err == nil {
		t.Fatal("expected error on unreadable index, got nil")
	}
}
