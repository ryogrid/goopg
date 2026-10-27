package storage

import (
	"math/rand"
	"testing"
)

// TestPageChecksumRowsEqualsPortable pins M0122-0012 C19: the in-place
// 32-lane rows form of pg_checksum_block equals the byte-assembling form on
// random pages, on pages with pd_checksum set (which both must ignore), and
// on an all-ones page; and an unaligned page takes the portable form.
func TestPageChecksumRowsEqualsPortable(t *testing.T) {
	rng := rand.New(rand.NewSource(19))
	page := make([]byte, BlockSize)
	for n := 0; n < 2000; n++ {
		rng.Read(page)
		if got, want := pageChecksumBlockRows(page), pageChecksumBlockPortable(page); got != want {
			t.Fatalf("page %d: rows %08x, portable %08x", n, got, want)
		}
		before := pageChecksumBlockRows(page)
		page[8], page[9] = byte(n), byte(n>>8) // pd_checksum
		if after := pageChecksumBlockRows(page); after != before {
			t.Fatalf("page %d: pd_checksum fed into the rows checksum", n)
		}
	}
	for i := range page {
		page[i] = 0xFF
	}
	if got, want := pageChecksumBlockRows(page), pageChecksumBlockPortable(page); got != want {
		t.Fatalf("all-ones page: rows %08x, portable %08x", got, want)
	}
	buf := make([]byte, BlockSize+1)
	rng.Read(buf)
	if got, want := pageChecksumBlock(buf[1:]), pageChecksumBlockPortable(buf[1:]); got != want {
		t.Fatalf("unaligned page: %08x, portable %08x", got, want)
	}
}

func BenchmarkPageChecksumBlock(b *testing.B) {
	page := make([]byte, BlockSize)
	rand.New(rand.NewSource(1)).Read(page)
	b.Run("rows", func(b *testing.B) {
		for range b.N {
			pageChecksumBlockRows(page)
		}
	})
	b.Run("portable", func(b *testing.B) {
		for range b.N {
			pageChecksumBlockPortable(page)
		}
	})
}
