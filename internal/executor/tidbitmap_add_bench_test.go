package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// BenchmarkTIDBitmapAdd measures feeding one index entry into the bitmap
// (review/260831 EO1-7). The bitmap index scan used to wrap every TID in a
// one-element slice for tbmAddTuples; it calls addOne directly now. One
// iteration is one index entry.
func BenchmarkTIDBitmapAdd(b *testing.B) {
	const npages = 512
	b.Run("slice-per-entry", func(b *testing.B) {
		tbm := &TIDBitmap{maxEntries: 1 << 20}
		i := 0
		b.ReportAllocs()
		for b.Loop() {
			ptr := storage.ItemPointer{Block: storage.BlockNumber(i % npages), Offset: uint16(i%200 + 1)}
			tbmAddTuples(tbm, []storage.ItemPointer{ptr}, false)
			i++
		}
	})
	b.Run("addOne", func(b *testing.B) {
		tbm := &TIDBitmap{maxEntries: 1 << 20}
		tbm.entries = make(map[storage.BlockNumber]*pageEntry)
		i := 0
		b.ReportAllocs()
		for b.Loop() {
			ptr := storage.ItemPointer{Block: storage.BlockNumber(i % npages), Offset: uint16(i%200 + 1)}
			tbm.addOne(ptr.Block, ptr.Offset, false)
			i++
		}
	})
}
