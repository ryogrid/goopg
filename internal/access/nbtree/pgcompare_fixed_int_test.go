package nbtree

import (
	"encoding/binary"
	"math"
	"math/rand"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestCompareFixedIntMatchesComparator pins M0122-0012 C37: an attribute
// whose descriptor carries IntWidth is compared inline by compareFixedPrefix,
// and must order every pair exactly as its Compare (btint2cmp / btint4cmp /
// btint8cmp) does through compareAttr — sign, deciding attribute and the
// DESC negation alike. Values are drawn near zero, at the type extremes and
// across the sign boundary, where a wrong-width or unsigned read would flip
// the answer.
func TestCompareFixedIntMatchesComparator(t *testing.T) {
	int2 := PGKeyAttr{PGIndexAttr: PGIndexAttr{Len: 2, ByVal: true, AlignBy: 2, Storage: 'p'}, Compare: PGCompareInt2, IntWidth: 2}
	int4 := PGKeyAttr{PGIndexAttr: PGIndexAttr{Len: 4, ByVal: true, AlignBy: 4, Storage: 'p'}, Compare: PGCompareInt4, IntWidth: 4}
	int8 := PGKeyAttr{PGIndexAttr: PGIndexAttr{Len: 8, ByVal: true, AlignBy: 8, Storage: 'p'}, Compare: PGCompareInt8, IntWidth: 8}
	descInt4 := int4
	descInt4.Desc = true
	descInt8 := int8
	descInt8.Desc = true
	rng := rand.New(rand.NewSource(37))
	pick := func(w int) int64 {
		lim := map[int][2]int64{2: {math.MinInt16, math.MaxInt16}, 4: {math.MinInt32, math.MaxInt32}, 8: {math.MinInt64, math.MaxInt64}}[w]
		switch rng.Intn(4) {
		case 0:
			return int64(rng.Intn(5) - 2)
		case 1:
			return lim[rng.Intn(2)]
		case 2:
			return lim[0] + int64(rng.Intn(3))
		default:
			return rng.Int63n(1<<(8*w-2)) - 1<<(8*w-3)
		}
	}
	img := func(w int, v int64) []byte {
		b := make([]byte, w)
		switch w {
		case 2:
			binary.LittleEndian.PutUint16(b, uint16(v))
		case 4:
			binary.LittleEndian.PutUint32(b, uint32(v))
		default:
			binary.LittleEndian.PutUint64(b, uint64(v))
		}
		return b
	}
	for _, attrs := range [][]PGKeyAttr{
		{int4, int4, int8},
		{int2, descInt4, int8},
		{descInt8, int2, int4},
		{int8, descInt4},
	} {
		desc := &PGIndexKeyDesc{Attrs: attrs}
		var tuples [][]byte
		for range 80 {
			vals := make([][]byte, len(attrs))
			for i, a := range attrs {
				vals[i] = img(int(a.Len), pick(int(a.Len)))
			}
			tuples = append(tuples, tup(t, attrs, vals, storage.ItemPointer{Block: 1, Offset: 1}))
		}
		for _, x := range tuples {
			for _, y := range tuples {
				n := len(attrs)
				gotRes, gotAt, gotDone := compareFixedPrefix(desc, x, y, n)
				compareFixedIntDisabled = true
				wantRes, wantAt, wantDone := compareFixedPrefix(desc, x, y, n)
				compareFixedIntDisabled = false
				if !gotDone || !wantDone || sign(gotRes) != sign(wantRes) || gotAt != wantAt {
					t.Fatalf("attrs %v: inline (%d, at %d, %v) vs comparator (%d, at %d, %v) for %x / %x",
						attrs, gotRes, gotAt, gotDone, wantRes, wantAt, wantDone, x, y)
				}
			}
		}
	}
}
