package btree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// M0130-S11.4 slice 3b-2c-ii-B2-c-i guards — the prefix UPPER bound.
//
// The rule under test is stated once in pgkeycmp.go: a range-scan upper bound
// that names only the first k key attributes is PLUS infinity in the ones it
// drops, whereas every other ordering decision (descent, insert slot, the low
// bound) wants the truncation rule's minus infinity. Getting it wrong is not a
// crash and not a mis-ordered tree — it is a scan that silently returns ZERO
// rows for `WHERE a = ?` on a two-column index, which is exactly the shape of
// failure the 608 historical row-count anchors exist to catch.

// int4x2Desc is a two-column (a, b) int4 index — the smallest descriptor for
// which "bound names a prefix" is a distinct case from "bound names the key".
func int4x2Desc() *PGIndexKeyDesc {
	a, b := int4Attr(), int4Attr()
	a.Compare = PGCompareInt4
	b.Compare = PGCompareInt4
	return &PGIndexKeyDesc{Attrs: []PGKeyAttr{a, b}}
}

// TestCompareHighIsCompareForBlobFormat pins the no-op half. The blob format
// has no attribute count to interpret, so the new bound comparison must be
// CompareKeys byte for byte — that equivalence is the whole reason this slice
// could land ahead of the flip without moving any on-disk behaviour.
func TestCompareHighIsCompareForBlobFormat(t *testing.T) {
	pairs := [][2][]byte{
		{EncodeInt4(1), EncodeInt4(2)},
		{EncodeInt4(2), EncodeInt4(2)},
		{EncodeInt4(3), EncodeInt4(2)},
		{EncodeInt4(-1), EncodeInt4(1)},
		{nil, EncodeInt4(1)},
		{append(EncodeInt4(2), EncodeInt4(9)...), EncodeInt4(2)},
	}
	for _, p := range pairs {
		if got, want := blobFormat.compareHigh(p[0], p[1]), blobFormat.compare(p[0], p[1]); got != want {
			t.Fatalf("blob compareHigh(%x, %x) = %d, want compare = %d", p[0], p[1], got, want)
		}
	}
}

// TestCompareHighPrefixBoundIsPlusInfinity is the asymmetry itself, at the
// comparison level: the same (entry, bound) pair must read as "past the bound"
// for the LOW end and "inside the bound" for the HIGH end.
func TestCompareHighPrefixBoundIsPlusInfinity(t *testing.T) {
	desc := int4x2Desc()
	f := indexFormat{desc: desc}
	bound := pivot(t, desc.Attrs, [][]byte{int4Val(5)}, 1) // "a = 5", b unnamed

	for _, b := range []int32{-100, 0, 7, 1 << 30} {
		entry := tup(t, desc.Attrs, [][]byte{int4Val(5), int4Val(b)},
			storage.ItemPointer{Block: 1, Offset: 1})
		if got := f.compare(entry, bound); got <= 0 {
			t.Fatalf("compare((5,%d), pivot(5)) = %d, want >0 — the truncation rule makes the shorter operand minus infinity", b, got)
		}
		if got := f.compareHigh(entry, bound); got != 0 {
			t.Fatalf("compareHigh((5,%d), pivot(5)) = %d, want 0 — a prefix upper bound is plus infinity in the attributes it drops", b, got)
		}
	}

	// A genuine difference on a named attribute must still be reported, or the
	// bound would never end the scan at all.
	for _, a := range []int32{6, 1 << 20} {
		entry := tup(t, desc.Attrs, [][]byte{int4Val(a), int4Val(0)},
			storage.ItemPointer{Block: 1, Offset: 1})
		if got := f.compareHigh(entry, bound); got <= 0 {
			t.Fatalf("compareHigh((%d,0), pivot(5)) = %d, want >0", a, got)
		}
	}
	for _, a := range []int32{4, -7} {
		entry := tup(t, desc.Attrs, [][]byte{int4Val(a), int4Val(0)},
			storage.ItemPointer{Block: 1, Offset: 1})
		if got := f.compareHigh(entry, bound); got >= 0 {
			t.Fatalf("compareHigh((%d,0), pivot(5)) = %d, want <0", a, got)
		}
	}
}

// TestCompareHighFullBoundIsHeapTIDBlind pins what a bound naming EVERY key
// attribute means. Slice 3b-2c-ii-B2-c-i asserted that such a bound agrees with
// `compare` outright, heap-TID tiebreak included; the flip
// (3b-2c-ii-B2-c) disproved that, and this is the corrected contract.
//
// A range bound is a bound on the KEY. `indexProbeKey` builds one from
// expressions, so it always carries the zero ItemPointer — and every real entry
// carries a real one, which is ABOVE zero in the tiebreak. Had compareHigh kept
// weighing the TID, an equality scan on a full-key probe would have found its
// first matching entry already "past" the bound and stopped with zero rows;
// that is precisely the failure the flip hit across the executor suite.
//
// So: agreement with `compare` on every key-attribute difference (the bound
// still has to end the scan), and TID-blindness where the key attributes are
// equal (every duplicate of the bound's key is inside it, whatever its heap
// position — including the last one).
func TestCompareHighFullBoundIsHeapTIDBlind(t *testing.T) {
	desc := int4x2Desc()
	f := indexFormat{desc: desc}
	// A bound with a non-zero TID as well as the zero one the executor actually
	// produces: neither may influence the answer.
	for _, boundTID := range []storage.ItemPointer{{}, {Block: 10, Offset: 2}} {
		bound := tup(t, desc.Attrs, [][]byte{int4Val(5), int4Val(5)}, boundTID)

		for _, e := range [][2]int32{{5, 5}, {5, 4}, {5, 6}, {4, 9}, {6, 1}} {
			// Blocks below, at and above the bound's own: the sign must not move.
			for _, blk := range []storage.BlockNumber{1, 10, 99} {
				entry := tup(t, desc.Attrs, [][]byte{int4Val(e[0]), int4Val(e[1])},
					storage.ItemPointer{Block: blk, Offset: 2})
				got := f.compareHigh(entry, bound)
				want := 0
				switch {
				case e[0] != 5:
					want = sign(int(e[0]) - 5)
				case e[1] != 5:
					want = sign(int(e[1]) - 5)
				}
				if sign(got) != want {
					t.Fatalf("compareHigh((%d,%d)@%d, full bound tid=%v) = %d, want sign %d",
						e[0], e[1], blk, boundTID, got, want)
				}
			}
		}
	}
}

// TestTupleFormatPrefixRangeScan is the end-to-end case: a real two-column
// tuple-format tree, big enough to span several leaf pages, scanned for one
// leading-column value with a prefix pivot as BOTH bounds — the shape the
// executor's composite equality probe takes once the flip lands (today it fakes
// the upper bound with 64 0xFF bytes, which a tuple cannot use).
//
// Both the completeness of the group and its exclusivity are asserted: a bound
// that is too generous is as wrong as one that stops early, and only the
// exclusivity half distinguishes "plus infinity in the dropped attribute" from
// "no upper bound at all".
func TestTupleFormatPrefixRangeScan(t *testing.T) {
	desc := int4x2Desc()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 64})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer func() { _ = pool.Close(); _ = mgr.Close() }()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 9130, Fork: storage.MainFork}
	bt, err := CreateWithOptions(pool, rel, Options{KeyDesc: desc})
	if err != nil {
		t.Fatalf("CreateWithOptions: %v", err)
	}

	// 40 leading values × 30 trailing values = 1200 entries, several leaf
	// pages deep, so the scan crosses page boundaries and the bound is tested
	// against a right-link walk rather than a single page's slot loop. The
	// trailing value deliberately runs through zero and negative numbers: under
	// bytewise order -1 (0xffffffff) sorts ABOVE every positive b, so a scan
	// that fell back to CompareKeys would report a truncated group.
	const nA, nB = 40, 30
	for a := int32(0); a < nA; a++ {
		for i := int32(0); i < nB; i++ {
			b := i - nB/2
			tid := storage.ItemPointer{Block: storage.BlockNumber(a), Offset: uint16(i + 1)}
			raw := tup(t, desc.Attrs, [][]byte{int4Val(a), int4Val(b)}, tid)
			if err := bt.Insert(raw, tid); err != nil {
				t.Fatalf("Insert(%d,%d): %v", a, b, err)
			}
		}
	}

	for _, a := range []int32{0, 17, nA - 1} {
		bound := pivot(t, desc.Attrs, [][]byte{int4Val(a)}, 1)
		var seen []int32
		if err := bt.RangeScan(bound, bound, func(key []byte, _ storage.ItemPointer) (bool, error) {
			vals, isnull, derr := DeformPGIndexTuple(key, desc.Physical(), 2)
			if derr != nil {
				return false, derr
			}
			if isnull[0] || isnull[1] {
				t.Fatalf("unexpected NULL key")
			}
			if got := int32(decodeLEUint32(vals[0])); got != a {
				t.Fatalf("scan for a=%d returned a row with a=%d — the upper bound is not exclusive of the next group", a, got)
			}
			seen = append(seen, int32(decodeLEUint32(vals[1])))
			return true, nil
		}); err != nil {
			t.Fatalf("RangeScan(a=%d): %v", a, err)
		}
		if len(seen) != nB {
			t.Fatalf("scan for a=%d returned %d rows, want %d (%v)", a, len(seen), nB, seen)
		}
		for i, b := range seen {
			if want := int32(i) - nB/2; b != want {
				t.Fatalf("scan for a=%d position %d has b=%d, want %d — not in int4 order", a, i, b, want)
			}
		}
	}
}
