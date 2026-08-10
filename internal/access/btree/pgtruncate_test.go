package btree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// M0130-S11.4 slice 3b-3c guards — `_bt_truncate` suffix truncation.
//
// Two independent things are pinned here, because they fail differently:
//
//   - the SIZE half (a separator keeps only the attributes that distinguish the
//     two halves) is invisible to every row-count gate — it shows up as an index
//     several times its proper size, which is why it needs a structural guard;
//   - the CORRECTNESS half (a separator between two entries with equal key
//     attributes keeps lastleft's heap TID) is a wrong-rows bug: without it the
//     left page's own entries compare GREATER than their page's high key, so a
//     descent walks right past the page holding them.

// int4x3Desc is a three-column (a, b, c) int4 index — the smallest descriptor
// on which truncation actually shrinks the tuple, since FormPGIndexTuple
// MAXALIGNs and a 2→1 attribute cut fits inside the same 8-byte block.
func int4x3Desc() *PGIndexKeyDesc {
	a, b, c := int4Attr(), int4Attr(), int4Attr()
	a.Compare, b.Compare, c.Compare = PGCompareInt4, PGCompareInt4, PGCompareInt4
	return &PGIndexKeyDesc{Attrs: []PGKeyAttr{a, b, c}}
}

// TestTruncateSeparatorBlobFormatUnchanged is the no-op half. An opaque payload
// has no attribute boundary to cut at, so a blob-format separator must still be
// the first right key, byte for byte — the pre-slice behaviour every existing
// blob-format tree on disk was built with.
func TestTruncateSeparatorBlobFormatUnchanged(t *testing.T) {
	left := item{key: EncodeInt4(1), ptr: storage.ItemPointer{Block: 1, Offset: 1}}
	right := item{key: EncodeInt4(2), ptr: storage.ItemPointer{Block: 1, Offset: 2}}
	got := blobFormat.truncateSeparator(left, right)
	if string(got) != string(right.key) {
		t.Fatalf("blob truncateSeparator = %x, want the first right key %x", got, right.key)
	}
}

// TestTruncateSeparatorKeepsFirstDistinguishingAttr: (1,5) | (2,7) differ on
// attribute 1, so the separator is the ONE-attribute pivot "a = 2" — upstream's
// whole reason for truncating, since it is what keeps the internal levels of a
// wide composite index small.
func TestTruncateSeparatorKeepsFirstDistinguishingAttr(t *testing.T) {
	// THREE attributes on purpose: `FormPGIndexTuple` MAXALIGNs a tuple's size,
	// so dropping one int4 of two is free and the size assertion below would
	// prove nothing. Dropping two crosses an 8-byte boundary.
	desc := int4x3Desc()
	f := indexFormat{desc: desc}
	left := item{
		key: tup(t, desc.Attrs, [][]byte{int4Val(1), int4Val(5), int4Val(5)}, storage.ItemPointer{Block: 1, Offset: 1}),
		ptr: storage.ItemPointer{Block: 1, Offset: 1},
	}
	right := item{
		key: tup(t, desc.Attrs, [][]byte{int4Val(2), int4Val(7), int4Val(7)}, storage.ItemPointer{Block: 1, Offset: 2}),
		ptr: storage.ItemPointer{Block: 1, Offset: 2},
	}
	sep := f.truncateSeparator(left, right)

	if !BTreeTupleIsPivot(sep) {
		t.Fatalf("separator is not a pivot tuple (t_info=%#x)", pgTInfo(sep))
	}
	if got := BTreeTupleGetNAtts(sep, 3); got != 1 {
		t.Fatalf("separator natts = %d, want 1 (only attribute 1 distinguishes the halves)", got)
	}
	if len(sep) >= len(right.key) {
		t.Fatalf("separator is %d bytes, not smaller than the untruncated key's %d — it was not re-formed",
			len(sep), len(right.key))
	}
	vals, isnull, err := DeformPGIndexTuple(sep, desc.Physical(), 1)
	if err != nil {
		t.Fatalf("deform separator: %v", err)
	}
	if isnull[0] || decodeLEUint32(vals[0]) != 2 {
		t.Fatalf("separator attribute 1 = %v (null=%v), want 2", vals[0], isnull[0])
	}
	// The boundary property: strictly above everything left, at-or-below
	// everything right.
	if got := f.compare(left.key, sep); got >= 0 {
		t.Fatalf("compare(lastleft, sep) = %d, want <0", got)
	}
	if got := f.compare(right.key, sep); got <= 0 {
		t.Fatalf("compare(firstright, sep) = %d, want >0", got)
	}
}

// TestTruncateSeparatorKeepsThroughFirstDifference: (1,5) | (1,7) agree on
// attribute 1, so truncation cannot cut before attribute 2.
func TestTruncateSeparatorKeepsThroughFirstDifference(t *testing.T) {
	desc := int4x2Desc()
	f := indexFormat{desc: desc}
	left := item{
		key: tup(t, desc.Attrs, [][]byte{int4Val(1), int4Val(5)}, storage.ItemPointer{Block: 1, Offset: 1}),
		ptr: storage.ItemPointer{Block: 1, Offset: 1},
	}
	right := item{
		key: tup(t, desc.Attrs, [][]byte{int4Val(1), int4Val(7)}, storage.ItemPointer{Block: 1, Offset: 2}),
		ptr: storage.ItemPointer{Block: 1, Offset: 2},
	}
	sep := f.truncateSeparator(left, right)

	if got := BTreeTupleGetNAtts(sep, 2); got != 2 {
		t.Fatalf("separator natts = %d, want 2 (attribute 1 is equal on both sides)", got)
	}
	if _, ok := BTreeTupleGetHeapTID(sep); ok {
		t.Fatalf("separator kept a tiebreaker heap TID although attribute 2 distinguishes the halves")
	}
	if got := f.compare(left.key, sep); got >= 0 {
		t.Fatalf("compare(lastleft, sep) = %d, want <0", got)
	}
	if got := f.compare(right.key, sep); got <= 0 {
		t.Fatalf("compare(firstright, sep) = %d, want >0", got)
	}
}

// TestTruncateSeparatorHeapTIDTiebreak is the correctness half: every key
// attribute is equal, so the separator keeps lastleft's heap TID as the
// implicit final key attribute (BT_PIVOT_HEAP_TID_ATTR).
//
// The last assertion is the bug this branch fixes, stated as the untruncated
// separator goopg wrote before this slice: a pivot with no TID is MINUS infinity
// in the tiebreak, so the left page's own last entry compares above its own
// page's high key.
func TestTruncateSeparatorHeapTIDTiebreak(t *testing.T) {
	desc := int4x2Desc()
	f := indexFormat{desc: desc}
	leftTID := storage.ItemPointer{Block: 4, Offset: 9}
	rightTID := storage.ItemPointer{Block: 4, Offset: 10}
	left := item{key: tup(t, desc.Attrs, [][]byte{int4Val(7), int4Val(3)}, leftTID), ptr: leftTID}
	right := item{key: tup(t, desc.Attrs, [][]byte{int4Val(7), int4Val(3)}, rightTID), ptr: rightTID}

	sep := f.truncateSeparator(left, right)
	if got := BTreeTupleGetNAtts(sep, 2); got != 2 {
		t.Fatalf("separator natts = %d, want 2", got)
	}
	tid, ok := BTreeTupleGetHeapTID(sep)
	if !ok {
		t.Fatalf("separator has no tiebreaker heap TID although every key attribute is equal")
	}
	if tid != leftTID {
		t.Fatalf("tiebreaker heap TID = %+v, want lastleft's %+v", tid, leftTID)
	}
	if len(sep)%8 != 0 {
		t.Fatalf("tiebreaker separator is %d bytes, not the MAXALIGNed size _bt_truncate produces", len(sep))
	}
	// leaf invariant: key <= HighKey on the left page, key > HighKey on the right.
	if got := f.compare(left.key, sep); got != 0 {
		t.Fatalf("compare(lastleft, sep) = %d, want 0 — the separator IS lastleft's key+TID", got)
	}
	if got := f.compare(right.key, sep); got <= 0 {
		t.Fatalf("compare(firstright, sep) = %d, want >0", got)
	}
	// Mutation reference: the pre-3b-3c separator (the whole right key, stamped
	// as a full-attribute pivot with no TID) breaks the left page's invariant.
	old := pivot(t, desc.Attrs, [][]byte{int4Val(7), int4Val(3)}, 2)
	if got := f.compare(left.key, old); got <= 0 {
		t.Fatalf("untruncated separator compares %d against lastleft; the test no longer demonstrates the bug", got)
	}
}

// TestMarshalKeepsPivotHeapTIDFlag pins the codec half. `marshal` re-stamps
// every pivot's natts on the way to the page (it is the one place that knows
// the item is a pivot at all), and stamping it WITHOUT the heap-TID bit would
// leave the trailing ItemPointerData on the tuple while telling every reader
// those six bytes are key data.
func TestMarshalKeepsPivotHeapTIDFlag(t *testing.T) {
	desc := int4x2Desc()
	f := indexFormat{desc: desc}
	tid := storage.ItemPointer{Block: 4, Offset: 9}
	left := item{key: tup(t, desc.Attrs, [][]byte{int4Val(7), int4Val(3)}, tid), ptr: tid}
	right := item{key: tup(t, desc.Attrs, [][]byte{int4Val(7), int4Val(3)},
		storage.ItemPointer{Block: 4, Offset: 10}), ptr: storage.ItemPointer{Block: 4, Offset: 10}}
	sep := f.truncateSeparator(left, right)

	raw := f.marshal(downlinkItem(sep, 77))
	if got := BTreeTupleGetNAtts(raw, 2); got != 2 {
		t.Fatalf("marshalled downlink natts = %d, want 2", got)
	}
	got, ok := BTreeTupleGetHeapTID(raw)
	if !ok || got != tid {
		t.Fatalf("marshalled downlink heap TID = %+v (ok=%v), want %+v", got, ok, tid)
	}
	if BTreeTupleGetDownLink(raw) != 77 {
		t.Fatalf("marshalled downlink block = %d, want 77", BTreeTupleGetDownLink(raw))
	}
	// The tiebreaker TID is not key data: parseItemBody must hand the comparison
	// layer a body without it.
	_, body, isPivot, err := parseItemBody(raw)
	if err != nil || !isPivot {
		t.Fatalf("parseItemBody(pivot) = pivot %v, err %v", isPivot, err)
	}
	if len(body) != len(raw)-SizeOfIndexTupleData-SizeOfItemPointerData {
		t.Fatalf("pivot body is %d bytes, want the tuple minus header and tiebreaker TID", len(body))
	}
}

// TestTupleFormatDuplicateKeySplitsStayReachable is the end-to-end case, and
// the one that fails outright without the tiebreaker branch: enough entries
// sharing ONE key value to fill several leaf pages, so every split falls
// between two entries whose key attributes are all equal.
//
// Before 3b-3c each such split installed a high key that was minus infinity in
// the heap-TID attribute, which put every entry of the left page ABOVE its own
// page's high key — the tree still built, and the scan silently lost rows.
func TestTupleFormatDuplicateKeySplitsStayReachable(t *testing.T) {
	desc := int4x2Desc()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 64})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer func() { _ = pool.Close(); _ = mgr.Close() }()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 9131, Fork: storage.MainFork}
	bt, err := CreateWithOptions(pool, rel, Options{KeyDesc: desc})
	if err != nil {
		t.Fatalf("CreateWithOptions: %v", err)
	}

	const n = 1500
	for i := 0; i < n; i++ {
		tid := storage.ItemPointer{Block: storage.BlockNumber(i / 100), Offset: uint16(i%100 + 1)}
		raw := tup(t, desc.Attrs, [][]byte{int4Val(7), int4Val(3)}, tid)
		if err := bt.Insert(raw, tid); err != nil {
			t.Fatalf("Insert #%d: %v", i, err)
		}
	}

	// A point descent for an EARLY duplicate is where the missing tiebreaker
	// shows as wrong rows: without it the first page's high key is minus
	// infinity in the heap-TID attribute, so `keyExceedsHighKey` sends the
	// search right off the page that actually holds the entry.
	for _, i := range []int{0, 3, 700, n - 1} {
		tid := storage.ItemPointer{Block: storage.BlockNumber(i / 100), Offset: uint16(i%100 + 1)}
		got, ok, err := bt.Search(tup(t, desc.Attrs, [][]byte{int4Val(7), int4Val(3)}, tid))
		if err != nil {
			t.Fatalf("Search #%d: %v", i, err)
		}
		if !ok || got != tid {
			t.Fatalf("Search #%d = %+v (found=%v), want %+v — the descent left the page holding it", i, got, ok, tid)
		}
	}

	bound := pivot(t, desc.Attrs, [][]byte{int4Val(7)}, 1)
	seen := 0
	if err := bt.RangeScan(bound, bound, func(_ []byte, _ storage.ItemPointer) (bool, error) {
		seen++
		return true, nil
	}); err != nil {
		t.Fatalf("RangeScan: %v", err)
	}
	if seen != n {
		t.Fatalf("scan returned %d of %d duplicate-key entries — the split separators do not bound their own pages", seen, n)
	}

	// The structural half: at least one page really did get a tiebreaker high
	// key, so the scan above exercised the branch rather than avoiding it.
	nblocks, err := pool.NBlocks(rel)
	if err != nil {
		t.Fatalf("NBlocks: %v", err)
	}
	tiebreakers := 0
	for blk := rootStart; blk < nblocks; blk++ {
		slot, err := pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			t.Fatalf("pin %d: %v", blk, err)
		}
		slot.RLock()
		p := make(storage.Page, storage.BlockSize)
		copy(p, slot.Page())
		slot.RUnlock()
		pool.Unpin(slot)
		raw, ok, err := PGHighKeyRaw(p)
		if err != nil {
			t.Fatalf("PGHighKeyRaw(%d): %v", blk, err)
		}
		if ok && PGIndexTupleTID(raw).Offset&BTPivotHeapTIDAttr != 0 {
			tiebreakers++
		}
	}
	if tiebreakers == 0 {
		t.Fatalf("no page carries a tiebreaker high key; the duplicate-split branch was never taken")
	}
}
