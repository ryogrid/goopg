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
