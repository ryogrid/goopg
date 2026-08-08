package executor

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// BenchmarkTBMExtractPageTuple measures tbmExtractPageTuple performance
// for pages with varying tuple densities.
func BenchmarkTBMExtractPageTuple(b *testing.B) {
	densities := []struct {
		name    string
		nTuples int // number of offsets to set on the page
	}{
		{"1tuple", 1},
		{"10tuple", 10},
		{"50tuple", 50},
		{"100tuple", 100},
	}

	for _, d := range densities {
		b.Run(d.name, func(b *testing.B) {
			e := &pageEntry{
				block:  0,
				isLossy: false,
				bitmap: make([]byte, bitmapWords),
			}
			// Distribute offsets evenly across the bitmap.
			step := MaxOffsetNumber / d.nTuples
			for off := uint16(1); off <= uint16(MaxOffsetNumber); off += uint16(step) {
				idx := (off - 1) / 8
				bit := (off - 1) % 8
				e.bitmap[idx] |= 1 << bit
			}

			buf := make([]uint16, d.nTuples)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tbmExtractPageTuple(e, buf)
			}
		})
	}
}

// BenchmarkTIDBitmapIteratorNext measures the per-TID iteration path.
func BenchmarkTIDBitmapIteratorNext(b *testing.B) {
	densities := []struct {
		name    string
		nBlocks int
		perPage int // TIDs per block
	}{
		{"1block_1tid", 1, 1},
		{"10block_1tid", 10, 1},
		{"1block_100tid", 1, 100},
		{"10block_50tid", 10, 50},
	}

	for _, d := range densities {
		b.Run(d.name, func(b *testing.B) {
			tbm := &TIDBitmap{}
			for blk := storage.BlockNumber(0); blk < storage.BlockNumber(d.nBlocks); blk++ {
				for off := uint16(1); off <= uint16(d.perPage); off++ {
					tbmAddTuples(tbm, []storage.ItemPointer{{Block: blk, Offset: off}}, false)
				}
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				it := tbmBeginIterate(tbm)
				total := 0
				for {
					_, _, _, _, ok := it.next()
					if !ok {
						break
					}
					total++
				}
				if total != d.nBlocks*d.perPage {
					b.Fatalf("expected %d TIDs, got %d", d.nBlocks*d.perPage, total)
				}
			}
		})
	}
}

// BenchmarkTIDBitmapNextPage measures the page-level iteration path.
func BenchmarkTIDBitmapNextPage(b *testing.B) {
	densities := []struct {
		name    string
		nBlocks int
		perPage int // TIDs per block
	}{
		{"1block_1tid", 1, 1},
		{"10block_1tid", 10, 1},
		{"1block_100tid", 1, 100},
		{"10block_50tid", 10, 50},
	}

	for _, d := range densities {
		b.Run(d.name, func(b *testing.B) {
			tbm := &TIDBitmap{}
			for blk := storage.BlockNumber(0); blk < storage.BlockNumber(d.nBlocks); blk++ {
				for off := uint16(1); off <= uint16(d.perPage); off++ {
					tbmAddTuples(tbm, []storage.ItemPointer{{Block: blk, Offset: off}}, false)
				}
			}

			buf := make([]uint16, d.perPage)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				it := tbmBeginIterate(tbm)
				var result BitmapPageResult
				total := 0
				for it.nextPage(&result) {
					if result.Lossy {
						continue
					}
					n := tbmExtractPageTuple(result.internalPage, buf)
					_ = n
					total += n
				}
				if total != d.nBlocks*d.perPage {
					b.Fatalf("expected %d TIDs, got %d", d.nBlocks*d.perPage, total)
				}
			}
		})
	}
}

// BenchmarkCompareNextVsNextPage compares per-TID vs page-level iteration
// for a realistic bitmap (many blocks, few TIDs each). This is the
// S5.5 microbenchmark that validates the bulk-extraction path.
func BenchmarkCompareNextVsNextPage(b *testing.B) {
	configs := []struct {
		nBlocks int
		perPage int
	}{
		{100, 5},  // many pages, sparse
		{50, 20},  // moderate pages, moderate density
		{10, 100}, // few pages, dense
	}

	for _, cfg := range configs {
		tbm := &TIDBitmap{}
		for blk := storage.BlockNumber(0); blk < storage.BlockNumber(cfg.nBlocks); blk++ {
			for off := uint16(1); off <= uint16(cfg.perPage); off++ {
				tbmAddTuples(tbm, []storage.ItemPointer{{Block: blk, Offset: off}}, false)
			}
		}
		totalTIDs := cfg.nBlocks * cfg.perPage

		b.Run(fmt.Sprintf("next/%dblocks_%dper", cfg.nBlocks, cfg.perPage), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				it := tbmBeginIterate(tbm)
				total := 0
				for {
					_, _, _, _, ok := it.next()
					if !ok {
						break
					}
					total++
				}
				if total != totalTIDs {
					b.Fatalf("expected %d TIDs, got %d", totalTIDs, total)
				}
			}
		})

		b.Run(fmt.Sprintf("nextPage/%dblocks_%dper", cfg.nBlocks, cfg.perPage), func(b *testing.B) {
			buf := make([]uint16, cfg.perPage)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				it := tbmBeginIterate(tbm)
				var result BitmapPageResult
				total := 0
				for it.nextPage(&result) {
					if result.Lossy {
						continue
					}
					n := tbmExtractPageTuple(result.internalPage, buf)
					total += n
				}
				if total != totalTIDs {
					b.Fatalf("expected %d TIDs, got %d", totalTIDs, total)
				}
			}
		})
	}
}
