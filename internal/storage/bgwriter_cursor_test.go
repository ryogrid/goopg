package storage

import "testing"

// TestWriteDirtyPagesAdvancesScanCursor is the review/260831-2 ST-4 guard. The
// bgwriter's independent scan cursor was advanced with `(start + n) % n`, which
// is `start` — a no-op. Every tick therefore restarted the sweep at the same
// origin and kept re-writing the lowest-indexed dirty buffers instead of
// sweeping the pool round-robin the way upstream BgBufferSync does with
// next_to_clean.
func TestWriteDirtyPagesAdvancesScanCursor(t *testing.T) {
	const nPages = 12
	pool, rel := newBgwriterPool(t, nPages+8)

	seedPage := make(Page, BlockSize)
	if err := InitPage(seedPage); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < nPages; i++ {
		if _, err := pool.Manager().Extend(rel, seedPage); err != nil {
			t.Fatal(err)
		}
	}
	for blk := BlockNumber(0); blk < BlockNumber(nPages); blk++ {
		slot, err := pool.Pin(BufferTag{Rel: rel, Block: blk})
		if err != nil {
			t.Fatalf("Pin %d: %v", blk, err)
		}
		pool.MarkDirty(slot)
		pool.Unpin(slot)
	}

	pool.bgwriterMu.Lock()
	before := pool.bgwriterHand
	pool.bgwriterMu.Unlock()

	const maxPages = 2
	if got := pool.WriteDirtyPages(maxPages); got != maxPages {
		t.Fatalf("WriteDirtyPages(%d) = %d, want %d dirty pages written", maxPages, got, maxPages)
	}

	pool.bgwriterMu.Lock()
	after := pool.bgwriterHand
	pool.bgwriterMu.Unlock()

	if after == before {
		t.Fatalf("bgwriterHand stayed at %d: the sweep cursor never advances, so the bgwriter rescans the same origin every tick", before)
	}
	// The tick stopped as soon as it had collected maxPages victims, so the
	// cursor must sit just past the last buffer it examined.
	if want := (before + maxPages) % len(pool.slots); after != want {
		t.Errorf("bgwriterHand = %d, want %d (start + buffers scanned)", after, want)
	}
}
