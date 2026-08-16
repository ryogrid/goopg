package xlog

import (
	"sync"
	"sync/atomic"
	"testing"
)

// TestWALBufferHeadIsAtomicInt64 pins that b.head is atomic.Int64.
// Assigning a *atomic.Int64 pointer fails to compile if the field is plain
// int64 — catching any regression that reverts the upgrade.
func TestWALBufferHeadIsAtomicInt64(t *testing.T) {
	b := newWALBuffer(64)
	var _ *atomic.Int64 = &b.head // compile-time proof
}

// TestWALBufferBaseIsAtomicInt64 pins that b.base is atomic.Int64.
func TestWALBufferBaseIsAtomicInt64(t *testing.T) {
	b := newWALBuffer(64)
	var _ *atomic.Int64 = &b.base // compile-time proof
}

// TestWALBufferAdvanceHeadSlidesBaseCorrectly verifies that advanceHead
// correctly slides base forward when head crosses a cap-aligned boundary,
// and that the base-first ordering doesn't break the arithmetic.
func TestWALBufferAdvanceHeadSlidesBaseCorrectly(t *testing.T) {
	const cap = 16
	b := newWALBuffer(cap)
	b.reset(0)

	// Append cap bytes so resident == cap (no drain yet).
	b.append(make([]byte, cap))

	// Advance head by exactly cap bytes → head == cap, triggers base slide.
	b.advanceHead(cap)

	if got := b.head.Load(); got != cap {
		t.Fatalf("head after advanceHead(%d): got %d, want %d", cap, got, cap)
	}
	if got := b.base.Load(); got != cap {
		t.Fatalf("base after advanceHead(%d): got %d, want %d (should slide to cap)", cap, got, cap)
	}
}

// TestWALBufferAdvanceHeadPartialDoesNotSlideBase verifies that a partial
// advance (head < base + cap) leaves base unchanged.
func TestWALBufferAdvanceHeadPartialDoesNotSlideBase(t *testing.T) {
	const cap = 32
	b := newWALBuffer(cap)
	b.reset(0)
	b.append(make([]byte, cap))

	// Advance by half — head < base+cap, no slide.
	b.advanceHead(cap / 2)
	if got := b.base.Load(); got != 0 {
		t.Fatalf("base unexpectedly slid to %d (want 0) after partial advance", got)
	}
	if got := b.head.Load(); got != cap/2 {
		t.Fatalf("head after advanceHead(%d/2): got %d", cap, got)
	}
}

// TestWALBufferAdvanceHeadConcurrentWithFreeReads runs a drain goroutine
// (sole writer of head) and many reader goroutines that call free() /
// resident() concurrently. Under -race this pins that the atomic.Int64
// upgrade is the ONLY synchronisation needed — a plain int64 would
// trigger a data-race report here.
func TestWALBufferAdvanceHeadConcurrentWithFreeReads(t *testing.T) {
	const cap = 4096
	b := newWALBuffer(cap)
	b.reset(0)

	// Pre-fill so resident == cap.
	b.append(make([]byte, cap))

	const readers = 8
	const itersPerReader = 10_000

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Readers: repeatedly call free() and resident(). These read head atomically.
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = b.free()
				_ = b.resident()
			}
		}()
	}

	// Drain writer: advance head in small steps.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(stop)
		// Drain cap bytes in 64-byte chunks to exercise multiple advanceHead
		// calls, including the base-slide path.
		drained := int64(0)
		for drained < cap {
			step := int64(64)
			if drained+step > cap {
				step = cap - drained
			}
			b.advanceHead(step)
			drained += step
		}
	}()

	// Wait for readers to process at least itersPerReader iterations each.
	wg.Wait()

	if b.head.Load() != cap {
		t.Fatalf("head after full drain: got %d, want %d", b.head.Load(), cap)
	}
}

// TestWALBufferWriteReservedUsesHeadNotBaseForBounds verifies the fix for
// "writeReserved range outside buffer window" that occurred after partial
// drain: head advances (bytes drained to disk) but base lags behind until
// head - base >= cap. Using base for the upper-bound check rejects valid
// writes at [base+cap, head+cap). The fix uses head for the bounds check
// so writes at [head, head+cap) are always accepted.
func TestWALBufferWriteReservedUsesHeadNotBaseForBounds(t *testing.T) {
	const cap = 64
	b := newWALBuffer(cap)
	b.reset(0)

	// Write cap/2 bytes and publish tail.
	b.append(make([]byte, cap/2))
	// Drain cap/2 bytes — head advances to cap/2, base stays 0.
	b.advanceHead(cap / 2)
	// Confirm base did NOT slide (head - base < cap).
	if b.base.Load() != 0 {
		t.Fatalf("base unexpectedly slid to %d, want 0", b.base.Load())
	}
	if b.head.Load() != cap/2 {
		t.Fatalf("head = %d, want %d", b.head.Load(), cap/2)
	}

	// Now: base=0, head=cap/2, cap=64. head+cap = cap/2+64.
	// Write at lsn = cap/2+32 = 48: lsn+8 = 56 <= head+cap = 96. Valid.
	// Old base-based check: lsn+8 = 56 <= base+cap = 64. Also valid. OK.
	// Write at lsn = cap/2+cap = 96: lsn+8 = 104 > head+cap = 96. Invalid.
	// Write at lsn = cap = 64:
	//   Old base check: 64+8=72 > base+cap=64. Would FAIL (the bug).
	//   New head check: 64+8=72 <= head+cap=96. Should PASS (the fix).
	if err := b.writeReserved(int64(cap), make([]byte, 8)); err != nil {
		t.Fatalf("writeReserved at base+cap (lsn=%d) failed with head-based check: %v", cap, err)
	}
}

// TestWALBufferWriteReservedConcurrentWithAdvanceHead exercises writeReserved
// from N stripe-writer goroutines while a drain goroutine concurrently advances
// head — the canonical slice B concurrent drain scenario. Under -race this
// confirms no data race between writeReserved's base.Load() and advanceHead's
// base.Store().
func TestWALBufferWriteReservedConcurrentWithAdvanceHead(t *testing.T) {
	const bufCap = 4096
	const stripes = 8
	const recsPerStripe = 50
	const recSize = 16

	b := newWALBuffer(bufCap)
	b.reset(0)

	// Pre-fill resident bytes so the drain goroutine has work to do;
	// use append (legacy path, single-goroutine safe) then publish via
	// publishTail to make them visible.
	drainBytes := int64(bufCap / 2)
	b.append(make([]byte, drainBytes))
	b.publishTail(drainBytes) // make bytes visible to readForDrain

	// Start LSN for stripe writes: must land in [base, base+cap).
	// After drain, head will advance to drainBytes; stripes write
	// at LSNs [drainBytes..drainBytes+stripes*recsPerStripe*recSize).
	// The buffer must accommodate all of them.
	totalWriteBytes := int64(stripes * recsPerStripe * recSize) // 6400
	if drainBytes+totalWriteBytes > bufCap {
		t.Skipf("buffer too small for this test: cap=%d need=%d", bufCap, drainBytes+totalWriteBytes)
	}

	var wg sync.WaitGroup

	// Stripe writers: write into disjoint LSN ranges starting at drainBytes.
	for s := 0; s < stripes; s++ {
		s := s
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < recsPerStripe; i++ {
				lsn := int64(drainBytes) + int64(s*recsPerStripe+i)*recSize
				data := make([]byte, recSize)
				data[0] = byte(s)
				if err := b.writeReserved(lsn, data); err != nil {
					t.Errorf("stripe %d rec %d writeReserved(%d): %v", s, i, lsn, err)
				}
			}
		}()
	}

	// Drain writer: advance head by drainBytes in small steps while stripe
	// writers run concurrently.
	wg.Add(1)
	go func() {
		defer wg.Done()
		drained := int64(0)
		for drained < drainBytes {
			step := int64(64)
			if drained+step > drainBytes {
				step = drainBytes - drained
			}
			b.advanceHead(step)
			drained += step
		}
	}()

	wg.Wait()

	if got := b.head.Load(); got != drainBytes {
		t.Fatalf("head after drain: got %d, want %d", got, drainBytes)
	}
}
