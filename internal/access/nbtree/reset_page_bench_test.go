package nbtree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// BenchmarkResetPageItems measures clearing a btree page's data area
// (review/260831 NB-8): it used to zero ~8 KB one byte at a time, on every
// whole-page rewrite — every split, every dedup recovery, every VACUUM
// compaction and every WAL replay of those.
func BenchmarkResetPageItems(b *testing.B) {
	p := make(storage.Page, storage.BlockSize)
	initPage(p, BTPageOpaque{Prev: storage.InvalidBlockNumber, Next: 6, Level: 0, Flags: BTLeaf})
	if err := pgSetHighKeyRaw(p, highKeyItem([]byte("sep")).marshal()); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		resetPageItems(p)
	}
}

// TestPGFirstDataSlotMatchesOpaqueDecode pins review/260831 NB-4: the narrow
// read must answer exactly what decoding the whole opaque answered.
func TestPGFirstDataSlotMatchesOpaqueDecode(t *testing.T) {
	for _, next := range []storage.BlockNumber{PNone, 0, 1, 12345} {
		p := make(storage.Page, storage.BlockSize)
		initPage(p, BTPageOpaque{Prev: storage.InvalidBlockNumber, Next: next, Level: 0, Flags: BTLeaf})
		want := PGFirstDataKey(ReadPGOpaque(p))
		if got := pgFirstDataSlot(p); got != want {
			t.Errorf("btpo_next=%d: pgFirstDataSlot = %d, PGFirstDataKey(ReadPGOpaque) = %d", next, got, want)
		}
	}
}

// BenchmarkPGFirstDataSlot measures the per-item accessor helper
// (review/260831 NB-4).
func BenchmarkPGFirstDataSlot(b *testing.B) {
	p := make(storage.Page, storage.BlockSize)
	initPage(p, BTPageOpaque{Prev: storage.InvalidBlockNumber, Next: 6, Level: 0, Flags: BTLeaf})
	b.ReportAllocs()
	for b.Loop() {
		if pgFirstDataSlot(p) == 0 {
			b.Fatal("zero slot")
		}
	}
}
