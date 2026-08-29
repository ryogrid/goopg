package nbtree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// BenchmarkComparePGIndexTuples guards the btree descent's inner loop against
// re-introducing per-comparison allocation (perf-optimize-take3/05 candidate
// B). The invariant is 0 allocs/op: this runs ~276 times per single-row index
// lookup, so any allocation here is multiplied by that factor.
func BenchmarkComparePGIndexTuples(b *testing.B) {
	att := PGIndexAttr{Len: 4, ByVal: true, AlignBy: 4, Storage: 'p'}
	desc := &PGIndexKeyDesc{Attrs: []PGKeyAttr{{PGIndexAttr: att, Compare: PGCompareInt4}}}
	mk := func(v byte) []byte {
		raw, err := FormPGIndexTuple([]PGIndexAttr{att}, [][]byte{{v, 0, 0, 0}}, []bool{false},
			storage.ItemPointer{Block: 1, Offset: uint16(v) + 1})
		if err != nil {
			b.Fatal(err)
		}
		return raw
	}
	x, y := mk(1), mk(2)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := ComparePGIndexTuples(desc, x, y); err != nil {
			b.Fatal(err)
		}
	}
}
