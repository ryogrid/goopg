package nbtree

import (
	"encoding/binary"
	"math/rand"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestCompareFixedPrefixMatchesWalk pins M0122-0012 slice 55: the
// cached-offset fast path (compareFixedPrefix) must give exactly the answers
// of the attribute walk it short-circuits. The descriptor mixes widths and
// alignments — bool (1, 'c'), int2 (2, 's'), int8 (8, 'd'), a DESC int4 — so
// a wrong alignment step reads the wrong bytes. Tuples with NULLs (the fast
// path declines on a null bitmap), truncated pivots (fewer stored attributes)
// and a fixed-width prefix followed by a varlena (it declines at the varlena)
// all go through comparePGIndexTuples and compareHigh against the deform-first
// reference; the fixture must reach the fast path's deciding branch.
func TestCompareFixedPrefixMatchesWalk(t *testing.T) {
	boolAttr := PGKeyAttr{PGIndexAttr: PGIndexAttr{Len: 1, ByVal: true, AlignBy: 1, Storage: 'p'}, Compare: PGCompareBool}
	int2Attr := PGKeyAttr{PGIndexAttr: PGIndexAttr{Len: 2, ByVal: true, AlignBy: 2, Storage: 'p'}, Compare: PGCompareInt2}
	int8Attr := PGKeyAttr{PGIndexAttr: PGIndexAttr{Len: 8, ByVal: true, AlignBy: 8, Storage: 'p'}, Compare: PGCompareInt8}
	descInt4 := int4Attr()
	descInt4.Compare, descInt4.Desc = PGCompareInt4, true
	text := PGKeyAttr{PGIndexAttr: PGIndexAttr{Len: -1, AlignBy: 4, Storage: 'x'}, Compare: PGCompareTextC}

	rng := rand.New(rand.NewSource(55))
	fixedVal := func(att PGKeyAttr) []byte {
		if rng.Intn(8) == 0 {
			return nil // NULL
		}
		b := make([]byte, att.Len)
		v := uint64(rng.Intn(3)) - 1
		switch att.Len {
		case 1:
			b[0] = byte(v & 1)
		case 2:
			binary.LittleEndian.PutUint16(b, uint16(v))
		case 4:
			binary.LittleEndian.PutUint32(b, uint32(v))
		case 8:
			binary.LittleEndian.PutUint64(b, v)
		}
		return b
	}
	for _, tc := range []struct {
		name  string
		attrs []PGKeyAttr
	}{
		{"fixed", []PGKeyAttr{boolAttr, int2Attr, int8Attr, descInt4}},
		{"fixed-then-varlena", []PGKeyAttr{int2Attr, int8Attr, text}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			desc := &PGIndexKeyDesc{Attrs: tc.attrs}
			f := indexFormat{desc: desc}
			val := func(i int) []byte {
				if tc.attrs[i].Len < 0 {
					if rng.Intn(8) == 0 {
						return nil
					}
					return varlena4([]byte(strings.Repeat("x", rng.Intn(3))))
				}
				return fixedVal(tc.attrs[i])
			}
			vals := func(n int) [][]byte {
				out := make([][]byte, n)
				for i := range out {
					out[i] = val(i)
				}
				return out
			}
			var tuples, all [][]byte
			for range 60 {
				tid := storage.ItemPointer{Block: storage.BlockNumber(rng.Intn(3)), Offset: uint16(1 + rng.Intn(3))}
				tuples = append(tuples, tup(t, tc.attrs, vals(len(tc.attrs)), tid))
			}
			all = append(all, tuples...)
			for range 30 {
				natts := 1 + rng.Intn(len(tc.attrs))
				all = append(all, pivot(t, tc.attrs, vals(natts), uint16(natts)))
			}
			decided := 0
			for _, x := range all {
				for _, y := range all {
					nkey := uint16(desc.NKeyAtts())
					ncmp := min(int(BTreeTupleGetNAtts(x, nkey)), int(BTreeTupleGetNAtts(y, nkey)))
					if _, _, done := compareFixedPrefix(desc, x, y, ncmp); done {
						decided++
					}
					for _, withTID := range []bool{false, true} {
						got, err := comparePGIndexTuples(desc, x, y, withTID)
						if err != nil {
							t.Fatalf("comparePGIndexTuples: %v", err)
						}
						if want := refComparePGIndexTuples(t, desc, x, y, withTID); sign(got) != sign(want) {
							t.Fatalf("comparePGIndexTuples(%x, %x, tid=%v) = %d, deform-first = %d", x, y, withTID, got, want)
						}
					}
				}
			}
			for _, e := range tuples {
				for _, hi := range all {
					if got, want := f.compareHigh(e, hi), refCompareHigh(t, desc, e, hi); sign(got) != sign(want) {
						t.Fatalf("compareHigh(%x, %x) = %d, two-pass = %d", e, hi, got, want)
					}
				}
			}
			if decided < len(all) {
				t.Fatalf("the fast path decided only %d of %d pairs — the fixture never reaches it", decided, len(all)*len(all))
			}
		})
	}
}

// TestCompareFixedPrefixOverrunKeepsWalkError: a tuple whose t_info size
// cuts a fixed-width attribute short makes the fast path decline, so the
// comparison reports the walk's overrun error instead of reading past the
// tuple or answering.
func TestCompareFixedPrefixOverrunKeepsWalkError(t *testing.T) {
	a, b := int4Attr(), int4Attr()
	a.Compare, b.Compare = PGCompareInt4, PGCompareInt4
	desc := &PGIndexKeyDesc{Attrs: []PGKeyAttr{a, b}}
	good := tup(t, desc.Attrs, [][]byte{int4Val(1), int4Val(2)}, storage.ItemPointer{Block: 1, Offset: 1})
	short := append([]byte(nil), good...)
	pgPutIndexTupleSize(short, len(short)-4) // second attribute now overruns
	if _, _, done := compareFixedPrefix(desc, good, short, 2); done {
		t.Fatal("compareFixedPrefix answered for a tuple whose attribute overruns its t_info size")
	}
	if _, err := ComparePGIndexTuples(desc, good, short); err == nil {
		t.Fatal("ComparePGIndexTuples accepted a tuple whose attribute overruns its t_info size")
	}
}
