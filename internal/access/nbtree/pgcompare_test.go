package nbtree

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// M0130-S11.4 slice 3b-1 guards. Each test pins one rule of upstream's
// _bt_compare (postgres/src/backend/access/nbtree/nbtsearch.c:693) that a
// naive attribute loop gets wrong.

func int4Attr() PGKeyAttr {
	return PGKeyAttr{PGIndexAttr: PGIndexAttr{Len: 4, ByVal: true, AlignBy: 4, Storage: 'p'}}
}

func int4Val(v int32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, uint32(v))
	return b
}

// tup builds a plain (non-pivot) leaf tuple over int4 attributes. A nil entry
// in vals is a NULL.
func tup(t *testing.T, attrs []PGKeyAttr, vals [][]byte, tid storage.ItemPointer) []byte {
	t.Helper()
	phys := (&PGIndexKeyDesc{Attrs: attrs}).Physical()
	isnull := make([]bool, len(vals))
	for i, v := range vals {
		isnull[i] = v == nil
	}
	raw, err := FormPGIndexTuple(phys, vals, isnull, tid)
	if err != nil {
		t.Fatalf("FormPGIndexTuple: %v", err)
	}
	return raw
}

// pivot builds a pivot tuple that physically stores natts of the descriptor's
// attributes, with no tiebreaker heap TID — what _bt_truncate emits.
func pivot(t *testing.T, attrs []PGKeyAttr, vals [][]byte, natts uint16) []byte {
	t.Helper()
	raw := tup(t, attrs[:len(vals)], vals, storage.ItemPointer{})
	if err := BTreeTupleSetNAtts(raw, natts, false); err != nil {
		t.Fatalf("BTreeTupleSetNAtts: %v", err)
	}
	return raw
}

func cmpOrFail(t *testing.T, desc *PGIndexKeyDesc, a, b []byte) int {
	t.Helper()
	res, err := ComparePGIndexTuples(desc, a, b)
	if err != nil {
		t.Fatalf("ComparePGIndexTuples: %v", err)
	}
	return res
}

func TestComparePGIndexTuplesBytewiseDefault(t *testing.T) {
	// A nil PGKeyAttr.Compare must mean CompareKeys, so goopg's existing
	// order-preserving encodings keep working while 3b-2 migrates types one
	// at a time. EncodeInt4 is order-preserving big-endian, so use it.
	desc := &PGIndexKeyDesc{Attrs: []PGKeyAttr{{
		PGIndexAttr: PGIndexAttr{Len: 4, ByVal: true, AlignBy: 4, Storage: 'p'},
	}}}
	tid := storage.ItemPointer{Block: 1, Offset: 1}
	lo := tup(t, desc.Attrs, [][]byte{EncodeInt4(-5)}, tid)
	hi := tup(t, desc.Attrs, [][]byte{EncodeInt4(7)}, tid)
	if got := cmpOrFail(t, desc, lo, hi); got >= 0 {
		t.Fatalf("EncodeInt4(-5) vs EncodeInt4(7) = %d, want < 0", got)
	}
	if got := cmpOrFail(t, desc, hi, lo); got <= 0 {
		t.Fatalf("reverse = %d, want > 0", got)
	}
	if got := cmpOrFail(t, desc, lo, lo); got != 0 {
		t.Fatalf("self = %d, want 0", got)
	}
}

func TestComparePGIndexTuplesUsesOpclassComparator(t *testing.T) {
	// The comparator seam must actually be consulted: little-endian int4
	// bytes are NOT order-preserving, so only a real comparator gets this
	// right. This is the mutation guard for `if a.Compare != nil`.
	attr := int4Attr()
	attr.Compare = func(a, b []byte) int {
		x, y := int32(binary.LittleEndian.Uint32(a)), int32(binary.LittleEndian.Uint32(b))
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	}
	desc := &PGIndexKeyDesc{Attrs: []PGKeyAttr{attr}}
	tid := storage.ItemPointer{Block: 1, Offset: 1}
	// 1 is 01 00 00 00 and 256 is 00 01 00 00: bytewise, 256 < 1.
	one := tup(t, desc.Attrs, [][]byte{int4Val(1)}, tid)
	big := tup(t, desc.Attrs, [][]byte{int4Val(256)}, tid)
	if got := cmpOrFail(t, desc, one, big); got >= 0 {
		t.Fatalf("1 vs 256 = %d, want < 0 (the comparator, not the bytes)", got)
	}
	bytewise := &PGIndexKeyDesc{Attrs: []PGKeyAttr{int4Attr()}}
	if got := cmpOrFail(t, bytewise, one, big); got <= 0 {
		t.Fatalf("bytewise 1 vs 256 = %d, want > 0 — the fixture no longer distinguishes the two paths", got)
	}
}

func TestComparePGIndexTuplesDescInverts(t *testing.T) {
	tid := storage.ItemPointer{Block: 1, Offset: 1}
	asc := &PGIndexKeyDesc{Attrs: []PGKeyAttr{int4Attr()}}
	descAttr := int4Attr()
	descAttr.Desc = true
	dsc := &PGIndexKeyDesc{Attrs: []PGKeyAttr{descAttr}}
	lo := tup(t, asc.Attrs, [][]byte{EncodeInt4(1)}, tid)
	hi := tup(t, asc.Attrs, [][]byte{EncodeInt4(2)}, tid)
	if got := cmpOrFail(t, asc, lo, hi); got >= 0 {
		t.Fatalf("ASC = %d, want < 0", got)
	}
	if got := cmpOrFail(t, dsc, lo, hi); got <= 0 {
		t.Fatalf("DESC = %d, want > 0", got)
	}
	// DESC must not disturb the heap-TID tiebreak, which is never reversed:
	// upstream applies SK_BT_DESC per key attribute only.
	eqLo := tup(t, dsc.Attrs, [][]byte{EncodeInt4(1)}, storage.ItemPointer{Block: 1, Offset: 1})
	eqHi := tup(t, dsc.Attrs, [][]byte{EncodeInt4(1)}, storage.ItemPointer{Block: 1, Offset: 2})
	if got := cmpOrFail(t, dsc, eqLo, eqHi); got >= 0 {
		t.Fatalf("DESC heap-TID tiebreak = %d, want < 0 (TID order is never inverted)", got)
	}
}

func TestComparePGIndexTuplesNullOrdering(t *testing.T) {
	tid := storage.ItemPointer{Block: 1, Offset: 1}
	for _, tc := range []struct {
		name       string
		nullsFirst bool
		want       int // sign of compare(NULL, value)
	}{
		{"nulls last (PG default for ASC)", false, 1},
		{"nulls first", true, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attr := int4Attr()
			attr.NullsFirst = tc.nullsFirst
			desc := &PGIndexKeyDesc{Attrs: []PGKeyAttr{attr}}
			null := tup(t, desc.Attrs, [][]byte{nil}, tid)
			val := tup(t, desc.Attrs, [][]byte{EncodeInt4(1)}, tid)
			if got := cmpOrFail(t, desc, null, val); got != tc.want {
				t.Fatalf("NULL vs value = %d, want %d", got, tc.want)
			}
			if got := cmpOrFail(t, desc, val, null); got != -tc.want {
				t.Fatalf("value vs NULL = %d, want %d", got, -tc.want)
			}
			// NULL "=" NULL, then the heap TID breaks the tie.
			null2 := tup(t, desc.Attrs, [][]byte{nil}, storage.ItemPointer{Block: 1, Offset: 2})
			if got := cmpOrFail(t, desc, null, null2); got != -1 {
				t.Fatalf("NULL vs NULL (differing TIDs) = %d, want -1", got)
			}
		})
	}
}

func TestComparePGIndexTuplesTruncatedIsMinusInfinity(t *testing.T) {
	// Upstream: `if (key->keysz > ntupatts) return 1` — the side with fewer
	// physically stored attributes sorts FIRST.
	attrs := []PGKeyAttr{int4Attr(), int4Attr()}
	desc := &PGIndexKeyDesc{Attrs: attrs}
	tid := storage.ItemPointer{Block: 3, Offset: 4}
	full := tup(t, attrs, [][]byte{EncodeInt4(10), EncodeInt4(10)}, tid)
	trunc := pivot(t, attrs, [][]byte{EncodeInt4(10)}, 1)
	if got := cmpOrFail(t, desc, trunc, full); got != -1 {
		t.Fatalf("truncated vs full = %d, want -1", got)
	}
	if got := cmpOrFail(t, desc, full, trunc); got != 1 {
		t.Fatalf("full vs truncated = %d, want 1", got)
	}
	// Minus infinity only applies AFTER the shared prefix: a truncated pivot
	// whose stored attribute is larger still sorts last.
	bigTrunc := pivot(t, attrs, [][]byte{EncodeInt4(11)}, 1)
	if got := cmpOrFail(t, desc, bigTrunc, full); got != 1 {
		t.Fatalf("truncated-but-greater vs full = %d, want 1", got)
	}
	// The zero-attribute pivot (the minus-infinity downlink) sorts before
	// everything.
	minusInf := pivot(t, attrs, nil, 0)
	if got := cmpOrFail(t, desc, minusInf, trunc); got != -1 {
		t.Fatalf("-inf vs truncated = %d, want -1", got)
	}
	if got := cmpOrFail(t, desc, minusInf, minusInf); got != 0 {
		t.Fatalf("-inf vs -inf = %d, want 0", got)
	}
}

func TestComparePGIndexTuplesHeapTIDTiebreak(t *testing.T) {
	desc := &PGIndexKeyDesc{Attrs: []PGKeyAttr{int4Attr()}}
	key := [][]byte{EncodeInt4(42)}
	a := tup(t, desc.Attrs, key, storage.ItemPointer{Block: 1, Offset: 9})
	b := tup(t, desc.Attrs, key, storage.ItemPointer{Block: 2, Offset: 1})
	if got := cmpOrFail(t, desc, a, b); got != -1 {
		t.Fatalf("block tiebreak = %d, want -1", got)
	}
	c := tup(t, desc.Attrs, key, storage.ItemPointer{Block: 1, Offset: 10})
	if got := cmpOrFail(t, desc, a, c); got != -1 {
		t.Fatalf("offset tiebreak = %d, want -1", got)
	}
	// A pivot that kept every attribute but no tiebreaker TID is minus
	// infinity in that final position.
	p := pivot(t, desc.Attrs, key, 1)
	if got := cmpOrFail(t, desc, p, a); got != -1 {
		t.Fatalf("TID-less pivot vs leaf tuple = %d, want -1", got)
	}
	if got := cmpOrFail(t, desc, a, p); got != 1 {
		t.Fatalf("leaf tuple vs TID-less pivot = %d, want 1", got)
	}
}

func TestComparePGIndexTuplesMultiAttrFirstDifferenceWins(t *testing.T) {
	attrs := []PGKeyAttr{int4Attr(), int4Attr()}
	desc := &PGIndexKeyDesc{Attrs: attrs}
	tid := storage.ItemPointer{Block: 1, Offset: 1}
	a := tup(t, attrs, [][]byte{EncodeInt4(1), EncodeInt4(99)}, tid)
	b := tup(t, attrs, [][]byte{EncodeInt4(2), EncodeInt4(0)}, tid)
	if got := cmpOrFail(t, desc, a, b); got >= 0 {
		t.Fatalf("leading attribute must decide: got %d, want < 0", got)
	}
	// Second attribute decides only when the first ties, and carries its own
	// DESC flag independently.
	attrs2 := []PGKeyAttr{int4Attr(), int4Attr()}
	attrs2[1].Desc = true
	desc2 := &PGIndexKeyDesc{Attrs: attrs2}
	c := tup(t, attrs2, [][]byte{EncodeInt4(1), EncodeInt4(0)}, tid)
	d := tup(t, attrs2, [][]byte{EncodeInt4(1), EncodeInt4(99)}, tid)
	if got := cmpOrFail(t, desc2, c, d); got <= 0 {
		t.Fatalf("per-attribute DESC = %d, want > 0", got)
	}
}

func TestComparePGIndexTuplesRejectsUnsupportedInput(t *testing.T) {
	desc := &PGIndexKeyDesc{Attrs: []PGKeyAttr{int4Attr()}}
	tid := storage.ItemPointer{Block: 1, Offset: 1}
	plain := tup(t, desc.Attrs, [][]byte{EncodeInt4(1)}, tid)

	if _, err := ComparePGIndexTuples(nil, plain, plain); err == nil {
		t.Fatal("nil descriptor must be rejected")
	}
	if _, err := ComparePGIndexTuples(&PGIndexKeyDesc{}, plain, plain); err == nil {
		t.Fatal("empty descriptor must be rejected")
	}

	posting := tup(t, desc.Attrs, [][]byte{EncodeInt4(1)}, tid)
	if err := BTreeTupleSetPosting(posting, 2, len(posting)); err != nil {
		t.Fatalf("BTreeTupleSetPosting: %v", err)
	}
	if _, err := ComparePGIndexTuples(desc, posting, plain); err == nil {
		t.Fatal("posting-list tuple must be rejected (which of its TIDs would break the tie?)")
	}
	if _, err := ComparePGIndexTuples(desc, plain, posting); err == nil {
		t.Fatal("posting-list tuple must be rejected on the right too")
	}

	// A pivot claiming more attributes than the descriptor describes is
	// corruption, not a comparison.
	wide := pivot(t, []PGKeyAttr{int4Attr(), int4Attr()},
		[][]byte{EncodeInt4(1), EncodeInt4(2)}, 2)
	if _, err := ComparePGIndexTuples(desc, wide, plain); err == nil {
		t.Fatal("natts beyond the descriptor must be rejected")
	}
}

func TestPGIndexKeyDescPhysicalProjection(t *testing.T) {
	attr := int4Attr()
	attr.Desc = true
	attr.Compare = func(a, b []byte) int { return 0 }
	desc := &PGIndexKeyDesc{Attrs: []PGKeyAttr{attr, {PGIndexAttr: PGIndexAttr{Len: -1, AlignBy: 4, Storage: 'x'}}}}
	phys := desc.Physical()
	if len(phys) != 2 || phys[0].Len != 4 || phys[1].Len != -1 || phys[1].Storage != 'x' {
		t.Fatalf("Physical() lost layout fields: %+v", phys)
	}
	if desc.NKeyAtts() != 2 {
		t.Fatalf("NKeyAtts = %d, want 2", desc.NKeyAtts())
	}
	if (*PGIndexKeyDesc)(nil).NKeyAtts() != 0 || (*PGIndexKeyDesc)(nil).Physical() != nil {
		t.Fatal("nil descriptor must degrade cleanly")
	}
}
