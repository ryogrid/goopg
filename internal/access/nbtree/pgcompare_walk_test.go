package nbtree

import (
	"math/rand"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// M0122-0012 slice 33 guard. comparePGIndexTuples now walks both tuples one
// attribute at a time and stops at the first that differs, and compareHigh
// answers the prefix-bound case in that same pass instead of re-comparing a
// truncated copy of the entry. Both must give exactly the answers of the
// deform-everything-first algorithm they replaced, which is reproduced here
// as the reference.

// refComparePGIndexTuples is the pre-slice-33 comparePGIndexTuples: deform
// both operands' shared attributes, compare in order, then the attribute-count
// and heap-TID rules.
func refComparePGIndexTuples(t *testing.T, desc *PGIndexKeyDesc, a, b []byte, withHeapTID bool) int {
	t.Helper()
	nkey := uint16(desc.NKeyAtts())
	nattsA, nattsB := int(BTreeTupleGetNAtts(a, nkey)), int(BTreeTupleGetNAtts(b, nkey))
	ncmp := min(nattsA, nattsB)
	av, bv := make([][]byte, ncmp), make([][]byte, ncmp)
	an, bn := make([]bool, ncmp), make([]bool, ncmp)
	if err := deformPGIndexTupleInto(a, desc.Attrs, ncmp, av, an); err != nil {
		t.Fatalf("deform left: %v", err)
	}
	if err := deformPGIndexTupleInto(b, desc.Attrs, ncmp, bv, bn); err != nil {
		t.Fatalf("deform right: %v", err)
	}
	for i := range ncmp {
		if res := desc.Attrs[i].compareAttr(av[i], an[i], bv[i], bn[i]); res != 0 {
			return res
		}
	}
	if nattsA != nattsB {
		if nattsA < nattsB {
			return -1
		}
		return 1
	}
	if !withHeapTID {
		return 0
	}
	tidA, okA := BTreeTupleGetHeapTID(a)
	tidB, okB := BTreeTupleGetHeapTID(b)
	switch {
	case !okA && !okB:
		return 0
	case !okA:
		return -1
	case !okB:
		return 1
	}
	return compareItemPointers(tidA, tidB)
}

// refCompareHigh is the pre-slice-33 compareHigh: the key-attribute compare,
// then, when entry is past a shorter bound, a second compare of entry
// restamped to the bound's attribute count.
func refCompareHigh(t *testing.T, desc *PGIndexKeyDesc, entry, hi []byte) int {
	t.Helper()
	res := refComparePGIndexTuples(t, desc, entry, hi, false)
	if res <= 0 {
		return res
	}
	nkey := uint16(desc.NKeyAtts())
	nHi := BTreeTupleGetNAtts(hi, nkey)
	if nHi == 0 || nHi >= BTreeTupleGetNAtts(entry, nkey) {
		return res
	}
	prefix := append([]byte(nil), entry...)
	if err := BTreeTupleSetNAtts(prefix, nHi, false); err != nil {
		t.Fatalf("BTreeTupleSetNAtts: %v", err)
	}
	if refComparePGIndexTuples(t, desc, prefix, hi, false) == 0 {
		return 0
	}
	return res
}

func TestComparePGIndexTuplesWalkMatchesDeformFirst(t *testing.T) {
	text := PGKeyAttr{PGIndexAttr: PGIndexAttr{Len: -1, AlignBy: 4, Storage: 'x'}, Compare: PGCompareTextC, NullsFirst: true}
	a, c := int4Attr(), int4Attr()
	a.Compare, c.Compare = PGCompareInt4, PGCompareInt4
	c.Desc = true
	desc := &PGIndexKeyDesc{Attrs: []PGKeyAttr{a, text, c}}
	f := indexFormat{desc: desc}

	rng := rand.New(rand.NewSource(33))
	words := []string{"", "a", "ab", "b", "ba"}
	val := func(i int) []byte {
		if rng.Intn(6) == 0 {
			return nil // NULL
		}
		switch i {
		case 1:
			return varlena4([]byte(words[rng.Intn(len(words))]))
		default:
			return int4Val(int32(rng.Intn(3) - 1))
		}
	}
	var tuples [][]byte
	for range 60 {
		vals := [][]byte{val(0), val(1), val(2)}
		tid := storage.ItemPointer{Block: storage.BlockNumber(rng.Intn(3)), Offset: uint16(1 + rng.Intn(3))}
		tuples = append(tuples, tup(t, desc.Attrs, vals, tid))
	}
	var bounds [][]byte
	for range 40 {
		natts := rng.Intn(4) // 0 = a bound storing no attribute
		vals := [][]byte{val(0), val(1), val(2)}[:max(natts, 1)]
		bounds = append(bounds, pivot(t, desc.Attrs, vals, uint16(natts)))
	}
	all := append(append([][]byte(nil), tuples...), bounds...)

	checks := 0
	for _, x := range all {
		for _, y := range all {
			for _, withTID := range []bool{false, true} {
				got, err := comparePGIndexTuples(desc, x, y, withTID)
				if err != nil {
					t.Fatalf("comparePGIndexTuples: %v", err)
				}
				if want := refComparePGIndexTuples(t, desc, x, y, withTID); sign(got) != sign(want) {
					t.Fatalf("comparePGIndexTuples(%x, %x, tid=%v) = %d, deform-first = %d", x, y, withTID, got, want)
				}
				checks++
			}
		}
	}
	for _, e := range tuples {
		for _, hi := range all {
			if got, want := f.compareHigh(e, hi), refCompareHigh(t, desc, e, hi); sign(got) != sign(want) {
				t.Fatalf("compareHigh(%x, %x) = %d, two-pass = %d", e, hi, got, want)
			}
			checks++
		}
	}
	t.Logf("%d comparisons agree", checks)
}
