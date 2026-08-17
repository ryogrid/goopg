package nbtree

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// M0130-S11.4 slice 3b-2c-ii-B2-b guard.
//
// The exported page readers used to decode every page as blob-formatted. That
// is not a missing plumbing detail but a wrong CLAIM once a tree is opened with
// a key descriptor: the item's key is then the whole nbtree tuple, and a blob
// parse strips the very header (t_info's null bitmap, t_tid's attribute count
// and heap TID) the comparison and amcheck depend on. Since B2-b the format is
// the caller's to supply, so this test pins BOTH answers on the SAME bytes —
// the tuple format returns the tuple, the blob format returns the payload after
// its header — because a reader that silently agreed with either would make the
// parameter decorative.
func TestPageReadersFollowTheIndexFormat(t *testing.T) {
	desc := int4Desc()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 64})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer func() { _ = pool.Close(); _ = mgr.Close() }()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 9102, Fork: storage.MainFork}
	bt, err := CreateWithOptions(pool, rel, Options{KeyDesc: desc})
	if err != nil {
		t.Fatalf("CreateWithOptions: %v", err)
	}
	// A tree's own format is what an out-of-package reader of its pages must
	// use; the two can never be resolved independently.
	if bt.Format().KeyDesc() != desc {
		t.Fatalf("(*BTree).Format did not carry the tree's descriptor")
	}
	if (IndexFormat{}).KeyDesc() != nil {
		t.Fatalf("the zero IndexFormat must be the blob format")
	}

	// Enough inserts to force splits, so the tree has an internal level with
	// downlinks and non-rightmost pages with high keys.
	const n = 600
	for i := 0; i < n; i++ {
		v := int32((i*397)%n) - n/2
		tid := storage.ItemPointer{Block: storage.BlockNumber(i/50 + 1), Offset: uint16(i%50 + 1)}
		if err := bt.Insert(tup(t, desc.Attrs, [][]byte{int4Val(v)}, tid), tid); err != nil {
			t.Fatalf("Insert(%d): %v", v, err)
		}
	}

	tupleFmt := IndexFormatFor(desc)
	blobFmt := IndexFormat{}

	page := func(blk storage.BlockNumber) storage.Page {
		t.Helper()
		slot, perr := pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if perr != nil {
			t.Fatalf("Pin(%d): %v", blk, perr)
		}
		p := make(storage.Page, len(slot.Page()))
		copy(p, slot.Page())
		pool.Unpin(slot)
		return p
	}

	root := ParseMeta(page(MetaBlock)).Root
	rootPage := page(root)
	if ParseOpaque(rootPage).IsLeaf() {
		t.Fatalf("%d inserts did not split the root — the downlink half of this test would be vacuous", n)
	}

	// Internal page: the downlinks' keys are pivot TUPLES under the real
	// format, and the same bytes minus their header under the blob format.
	dls, err := tupleFmt.PageDownlinks(rootPage)
	if err != nil {
		t.Fatalf("PageDownlinks(tuple): %v", err)
	}
	blobDls, err := blobFmt.PageDownlinks(rootPage)
	if err != nil {
		t.Fatalf("PageDownlinks(blob): %v", err)
	}
	if len(dls) != len(blobDls) || len(dls) < 2 {
		t.Fatalf("got %d/%d downlinks, want the same count and at least 2", len(dls), len(blobDls))
	}
	// dls[0] is the negative-infinity downlink (a bare header, no payload), so
	// the discriminating comparison is on a real separator.
	sep := dls[1]
	if PGIndexTupleSize(sep.Key) != len(sep.Key) {
		t.Fatalf("separator %x is not a whole tuple: t_info size %d, len %d",
			sep.Key, PGIndexTupleSize(sep.Key), len(sep.Key))
	}
	if !BTreeTupleIsPivot(sep.Key) {
		t.Fatalf("separator %x is not a pivot tuple", sep.Key)
	}
	if got := BTreeTupleGetDownLink(sep.Key); got != sep.Child {
		t.Fatalf("separator downlink %d disagrees with the decoded child %d", got, sep.Child)
	}
	if want := sep.Key[SizeOfIndexTupleData:]; !bytes.Equal(blobDls[1].Key, want) {
		t.Fatalf("blob reader returned %x, want the header-stripped payload %x", blobDls[1].Key, want)
	}
	if bytes.Equal(blobDls[1].Key, sep.Key) {
		t.Fatalf("blob and tuple readers agreed on %x — the format argument is not reaching the decoder", sep.Key)
	}

	// Leaf page: the same split, plus the heap TID, which the tuple format must
	// report from the item's t_tid and the descriptor's arity.
	leafBlk := dls[0].Child
	leafPage := page(leafBlk)
	if !ParseOpaque(leafPage).IsLeaf() {
		t.Fatalf("block %d is not a leaf", leafBlk)
	}
	items, err := tupleFmt.PageLeafItems(leafPage)
	if err != nil {
		t.Fatalf("PageLeafItems(tuple): %v", err)
	}
	if len(items) == 0 {
		t.Fatalf("leaf %d yielded no items", leafBlk)
	}
	for _, it := range items {
		if PGIndexTupleSize(it.Key) != len(it.Key) {
			t.Fatalf("leaf key %x is not a whole tuple", it.Key)
		}
		if got := PGIndexTupleTID(it.Key); got != it.TID {
			t.Fatalf("leaf key t_tid %+v disagrees with the reported TID %+v", got, it.TID)
		}
		vals, isnull, derr := DeformPGIndexTuple(it.Key, desc.Physical(), 1)
		if derr != nil || isnull[0] {
			t.Fatalf("leaf key %x does not deform: %v (null=%v)", it.Key, derr, isnull)
		}
		_ = vals
	}

	keys, err := tupleFmt.PageItemKeys(leafPage)
	if err != nil {
		t.Fatalf("PageItemKeys(tuple): %v", err)
	}
	if len(keys) != len(items) {
		t.Fatalf("PageItemKeys returned %d keys, PageLeafItems %d entries", len(keys), len(items))
	}
	blobKeys, err := blobFmt.PageItemKeys(leafPage)
	if err != nil {
		t.Fatalf("PageItemKeys(blob): %v", err)
	}
	if want := keys[0][SizeOfIndexTupleData:]; !bytes.Equal(blobKeys[0], want) {
		t.Fatalf("blob PageItemKeys returned %x, want the header-stripped payload %x", blobKeys[0], want)
	}

	// High key: a non-rightmost leaf carries one, and it is a pivot tuple.
	hk, ok, err := tupleFmt.PageHighKey(leafPage)
	if err != nil {
		t.Fatalf("PageHighKey(tuple): %v", err)
	}
	if !ok {
		t.Fatalf("leaf %d (the leftmost of a split tree) has no high key", leafBlk)
	}
	if PGIndexTupleSize(hk) != len(hk) || !BTreeTupleIsPivot(hk) {
		t.Fatalf("high key %x is not a whole pivot tuple", hk)
	}
	blobHK, _, err := blobFmt.PageHighKey(leafPage)
	if err != nil {
		t.Fatalf("PageHighKey(blob): %v", err)
	}
	if want := hk[SizeOfIndexTupleData:]; !bytes.Equal(blobHK, want) {
		t.Fatalf("blob PageHighKey returned %x, want the header-stripped payload %x", blobHK, want)
	}
}
