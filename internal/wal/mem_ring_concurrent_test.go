package wal

import (
	"bytes"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestMemRingWriteReservedAtHeadNoWrap: write at pos == head == 0 on
// a fresh ring lands the bytes in the leading slots and leaves head /
// tail untouched. Pins the "publication is a separate step" contract.
func TestMemRingWriteReservedAtHeadNoWrap(t *testing.T) {
	r := NewMemRing(64)
	if err := r.WriteReserved(0, []byte("hello")); err != nil {
		t.Fatalf("WriteReserved: %v", err)
	}
	if h, ta := r.Range(); h != 0 || ta != 0 {
		t.Errorf("Range = (%d, %d), want (0, 0) — WriteReserved must not advance tail", h, ta)
	}
	// Bytes landed at buf[0..5].
	if string(r.buf[:5]) != "hello" {
		t.Errorf("buf[:5] = %q, want %q", r.buf[:5], "hello")
	}
}

// TestMemRingWriteReservedAtNonZeroOffset: LSN→ring-slot arithmetic
// at a non-zero offset; neighbouring slots untouched.
func TestMemRingWriteReservedAtNonZeroOffset(t *testing.T) {
	r := NewMemRing(64)
	// Pre-fill the ring buffer with a marker so we can detect any
	// accidental writes outside the target slot range.
	for i := range r.buf {
		r.buf[i] = 0xAB
	}
	if err := r.WriteReserved(10, []byte("hello")); err != nil {
		t.Fatalf("WriteReserved: %v", err)
	}
	// Bytes land at buf[10..15].
	if string(r.buf[10:15]) != "hello" {
		t.Errorf("buf[10:15] = %q, want %q", r.buf[10:15], "hello")
	}
	for i, b := range r.buf {
		if i >= 10 && i < 15 {
			continue
		}
		if b != 0xAB {
			t.Errorf("buf[%d] = 0x%02x, want 0xAB (write leaked outside target range)", i, b)
		}
	}
}

// TestMemRingWriteReservedWrapsAcrossRingBoundary: a write that
// straddles the cap boundary splits across [pos % cap, cap) and
// [0, n-first). Pins the wrap-aware copy.
func TestMemRingWriteReservedWrapsAcrossRingBoundary(t *testing.T) {
	r := NewMemRing(16)
	// Advance head/tail to 16 (one cap's worth published) so the
	// valid window is [16, 32). A write at pos=24 with len=12 occupies
	// LSN range [24, 36) → ring slots [8, 16) and [0, 4).
	//
	// We need pos+n <= head+cap, i.e., 24+12 <= 16+16 — exactly the
	// boundary case. Bump head so the window admits it.
	r.PublishUpTo(20) // head=4 because tail-head ≤ cap: 20-4=16; tail=20
	// Re-shape window: head=4 lets pos=4..20 with n bounded. We need
	// the wrap at pos%cap+n > cap → pos=12 gives slot 12, n=8 spans
	// slots [12, 16) and [0, 4). Window: pos>=4 and pos+n<=20: 12+8==20. ✓
	for i := range r.buf {
		r.buf[i] = 0
	}
	payload := []byte("ABCDEFGH") // 8 bytes
	if err := r.WriteReserved(12, payload); err != nil {
		t.Fatalf("WriteReserved: %v", err)
	}
	// Slot 12..15 holds payload[:4]; slot 0..3 holds payload[4:].
	if !bytes.Equal(r.buf[12:16], payload[:4]) {
		t.Errorf("buf[12:16] = %v, want %v", r.buf[12:16], payload[:4])
	}
	if !bytes.Equal(r.buf[0:4], payload[4:]) {
		t.Errorf("buf[0:4] = %v, want %v", r.buf[0:4], payload[4:])
	}
}

// TestMemRingWriteReservedRejectsBelowHead: after PublishUpTo
// advances head past LSN X, a WriteReserved at X is rejected with the
// sentinel error.
func TestMemRingWriteReservedRejectsBelowHead(t *testing.T) {
	r := NewMemRing(16)
	// Publish past cap so head advances.
	r.PublishUpTo(20) // head=4, tail=20
	err := r.WriteReserved(3, []byte("x"))
	if !errors.Is(err, errMemRingReservedOutOfRange) {
		t.Errorf("WriteReserved(below head) = %v, want errMemRingReservedOutOfRange", err)
	}
}

// TestMemRingWriteReservedRejectsPastWindow: pos+n > head+cap is
// rejected; pos+n == head+cap (exact boundary) is accepted.
func TestMemRingWriteReservedRejectsPastWindow(t *testing.T) {
	r := NewMemRing(16)
	// Window is [0, 16).
	if err := r.WriteReserved(8, []byte("12345678")); err != nil { // ends at 16, exact boundary
		t.Errorf("WriteReserved exact boundary should accept, got %v", err)
	}
	if err := r.WriteReserved(9, []byte("12345678")); !errors.Is(err, errMemRingReservedOutOfRange) {
		t.Errorf("WriteReserved past window = %v, want errMemRingReservedOutOfRange", err)
	}
	// pos itself past the window.
	if err := r.WriteReserved(16, []byte("x")); !errors.Is(err, errMemRingReservedOutOfRange) {
		t.Errorf("WriteReserved at head+cap = %v, want errMemRingReservedOutOfRange", err)
	}
}

// TestMemRingWriteReservedEmptyIsNoop: nil and zero-length data
// short-circuit before the range check; head/tail untouched.
// Critically, an empty write with an out-of-window pos is still nil
// (no spurious error).
func TestMemRingWriteReservedEmptyIsNoop(t *testing.T) {
	r := NewMemRing(16)
	r.PublishUpTo(20) // head=4, tail=20
	if err := r.WriteReserved(0, nil); err != nil {
		t.Errorf("WriteReserved(nil) = %v, want nil even with pos<head", err)
	}
	if err := r.WriteReserved(0, []byte{}); err != nil {
		t.Errorf("WriteReserved([]) = %v, want nil even with pos<head", err)
	}
	if err := r.WriteReserved(1000, nil); err != nil {
		t.Errorf("WriteReserved(nil) past window = %v, want nil", err)
	}
	if h, ta := r.Range(); h != 4 || ta != 20 {
		t.Errorf("Range mutated by empty WriteReserved = (%d, %d), want (4, 20)", h, ta)
	}
}

// TestMemRingWriteReservedNilReceiver: nil receiver is a no-op
// returning nil. Matches MemRing.Append nil-safe convention so the
// slice B call-site rewrite can call this unconditionally under
// `wal_sender_memory_buffer == 0`.
func TestMemRingWriteReservedNilReceiver(t *testing.T) {
	var r *MemRing
	if err := r.WriteReserved(0, []byte("x")); err != nil {
		t.Errorf("nil receiver WriteReserved = %v, want nil", err)
	}
	if err := r.WriteReserved(0, nil); err != nil {
		t.Errorf("nil receiver WriteReserved(nil) = %v, want nil", err)
	}
	// Also pins PublishUpTo nil-safety alongside.
	r.PublishUpTo(100)
}

// TestMemRingWriteReservedDoesNotMutateHeadTail: a series of
// WriteReserveds leaves head and tail exactly as they were. Pins the
// "publication is a separate step" contract end-to-end across many
// writes.
func TestMemRingWriteReservedDoesNotMutateHeadTail(t *testing.T) {
	r := NewMemRing(64)
	hBefore, tBefore := r.Range()
	for i := 0; i < 8; i++ {
		if err := r.WriteReserved(int64(i*4), []byte("ABCD")); err != nil {
			t.Fatalf("WriteReserved[%d]: %v", i, err)
		}
	}
	hAfter, tAfter := r.Range()
	if hAfter != hBefore || tAfter != tBefore {
		t.Errorf("head/tail mutated by 8 WriteReserveds: before=(%d,%d) after=(%d,%d)",
			hBefore, tBefore, hAfter, tAfter)
	}
}

// TestMemRingPublishUpToAdvancesTail: tail tracks safeTail; head
// stays at 0 when residency fits in cap.
func TestMemRingPublishUpToAdvancesTail(t *testing.T) {
	r := NewMemRing(64)
	r.PublishUpTo(10)
	if h, ta := r.Range(); h != 0 || ta != 10 {
		t.Errorf("after PublishUpTo(10): Range = (%d, %d), want (0, 10)", h, ta)
	}
	r.PublishUpTo(40)
	if h, ta := r.Range(); h != 0 || ta != 40 {
		t.Errorf("after PublishUpTo(40): Range = (%d, %d), want (0, 40)", h, ta)
	}
}

// TestMemRingPublishUpToMonotonic: regressing safeTail ≤ current tail
// is a no-op.
func TestMemRingPublishUpToMonotonic(t *testing.T) {
	r := NewMemRing(64)
	r.PublishUpTo(40)
	r.PublishUpTo(20) // regression
	if h, ta := r.Range(); h != 0 || ta != 40 {
		t.Errorf("regressing PublishUpTo modified state: Range = (%d, %d), want (0, 40)", h, ta)
	}
	r.PublishUpTo(40) // equal
	if h, ta := r.Range(); h != 0 || ta != 40 {
		t.Errorf("equal PublishUpTo modified state: Range = (%d, %d), want (0, 40)", h, ta)
	}
}

// TestMemRingPublishUpToEvictsWhenOverCap: safeTail - head > cap
// advances head to maintain the residency invariant.
func TestMemRingPublishUpToEvictsWhenOverCap(t *testing.T) {
	r := NewMemRing(16)
	r.PublishUpTo(8)
	if h, ta := r.Range(); h != 0 || ta != 8 {
		t.Errorf("Range = (%d, %d), want (0, 8)", h, ta)
	}
	r.PublishUpTo(24) // 24 - 0 = 24 > cap=16 → head = 24 - 16 = 8
	if h, ta := r.Range(); h != 8 || ta != 24 {
		t.Errorf("Range = (%d, %d), want (8, 24)", h, ta)
	}
	r.PublishUpTo(100) // 100 - 8 = 92 > 16 → head = 100 - 16 = 84
	if h, ta := r.Range(); h != 84 || ta != 100 {
		t.Errorf("Range = (%d, %d), want (84, 100)", h, ta)
	}
}

// TestMemRingPublishUpToNilReceiver: defensive nil-safety on the
// publisher side too.
func TestMemRingPublishUpToNilReceiver(t *testing.T) {
	var r *MemRing
	r.PublishUpTo(100) // must not panic
}

// TestMemRingWriteReservedReadbackViaReadAt: end-to-end. Write bytes
// at LSN X via WriteReserved, advance tail past X+n via PublishUpTo,
// then ReadAt(X) hits with the same bytes.
func TestMemRingWriteReservedReadbackViaReadAt(t *testing.T) {
	r := NewMemRing(64)
	payload := []byte("hello world!")
	if err := r.WriteReserved(0, payload); err != nil {
		t.Fatalf("WriteReserved: %v", err)
	}
	// Before publication, ReadAt must miss — tail is still 0.
	out := make([]byte, len(payload))
	if n, ok := r.ReadAt(0, out); ok {
		t.Errorf("ReadAt before PublishUpTo hit (n=%d); want miss", n)
	}
	r.PublishUpTo(int64(len(payload)))
	if n, ok := r.ReadAt(0, out); !ok || n != len(payload) {
		t.Fatalf("ReadAt after PublishUpTo = (%d, %v); want (%d, true)", n, ok, len(payload))
	}
	if !bytes.Equal(out, payload) {
		t.Errorf("ReadAt bytes = %q, want %q", out, payload)
	}
}

// TestMemRingWriteReservedConcurrentDisjoint: 8 goroutines × 50
// records × 16 bytes in disjoint LSN ranges; race-clean under -race;
// after a final PublishUpTo every stripe's marker bytes are present
// in the right slot. The disjoint-range contract is the slice B call-
// site's responsibility; here we verify that, given a disjoint
// schedule, no data races occur and no bytes get corrupted.
func TestMemRingWriteReservedConcurrentDisjoint(t *testing.T) {
	const stripes = 8
	const perStripe = 50
	const recSize = 16
	// Total = 6400 bytes. Cap must be >= total because we don't run
	// the publisher concurrently in this test (we want every byte
	// resident at the end for verification).
	total := int64(stripes * perStripe * recSize)
	r := NewMemRing(total + 64)

	var wg sync.WaitGroup
	wg.Add(stripes)
	for s := 0; s < stripes; s++ {
		go func(stripe int) {
			defer wg.Done()
			for i := 0; i < perStripe; i++ {
				// Stripe s's reservations occupy
				// [s*perStripe*recSize + i*recSize, +recSize).
				pos := int64((stripe*perStripe + i) * recSize)
				rec := make([]byte, recSize)
				for k := range rec {
					rec[k] = byte('A' + stripe)
				}
				if err := r.WriteReserved(pos, rec); err != nil {
					t.Errorf("stripe %d rec %d WriteReserved: %v", stripe, i, err)
					return
				}
			}
		}(s)
	}
	wg.Wait()

	// Publish all writes, then verify every stripe's marker bytes
	// land in the right slot.
	r.PublishUpTo(total)
	out := make([]byte, recSize)
	for s := 0; s < stripes; s++ {
		for i := 0; i < perStripe; i++ {
			pos := int64((s*perStripe + i) * recSize)
			n, ok := r.ReadAt(pos, out)
			if !ok || n != recSize {
				t.Fatalf("ReadAt(stripe=%d rec=%d pos=%d) = (%d, %v)", s, i, pos, n, ok)
			}
			for k := range out {
				if out[k] != byte('A'+s) {
					t.Fatalf("stripe %d rec %d byte %d = 0x%02x, want 0x%02x",
						s, i, k, out[k], byte('A'+s))
				}
			}
		}
	}
}

// TestMemRingPublishUpToAndWriteReservedSerialise: a publisher
// goroutine periodically advances tail while writers race to fill in
// the reserved LSN range. The contract relies on the call site
// (tailPublisher) not advancing past any active reservation; here we
// simulate that by computing safeTail as the minimum of (writer
// progress, writer's reserved position). At the end every written LSN
// range is readable via ReadAt and contains the right bytes.
func TestMemRingPublishUpToAndWriteReservedSerialise(t *testing.T) {
	const writers = 8
	const recsPerWriter = 100
	const recSize = 16
	total := int64(writers * recsPerWriter * recSize) // 12800
	r := NewMemRing(total + 64)

	// progress[w] is the highest LSN end (pos+recSize) that writer w
	// has finished. Updated AFTER WriteReserved returns so the
	// minimum across writers is always ≤ any in-flight reservation
	// pos (mimics the insertionTracker discipline).
	progress := make([]int64, writers)
	stop := atomic.Bool{}
	var wg sync.WaitGroup

	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func(writer int) {
			defer wg.Done()
			for i := 0; i < recsPerWriter; i++ {
				pos := int64((writer*recsPerWriter + i) * recSize)
				rec := make([]byte, recSize)
				for k := range rec {
					rec[k] = byte('A' + writer)
				}
				if err := r.WriteReserved(pos, rec); err != nil {
					t.Errorf("writer %d rec %d WriteReserved: %v", writer, i, err)
					return
				}
				atomic.StoreInt64(&progress[writer], pos+recSize)
			}
		}(w)
	}

	// Publisher goroutine: continuously computes the safe tail as
	// min across writers' published progress and advances the ring.
	pubDone := make(chan struct{})
	go func() {
		defer close(pubDone)
		for !stop.Load() {
			var safe int64 = total // sentinel: if all done, full publish
			for w := 0; w < writers; w++ {
				p := atomic.LoadInt64(&progress[w])
				if p < safe {
					safe = p
				}
			}
			if safe > 0 {
				r.PublishUpTo(safe)
			}
			time.Sleep(50 * time.Microsecond)
		}
	}()

	wg.Wait()
	stop.Store(true)
	<-pubDone
	// Final publish to ensure every byte is resident.
	r.PublishUpTo(total)

	// Verify all writers' bytes are readable.
	out := make([]byte, recSize)
	for w := 0; w < writers; w++ {
		for i := 0; i < recsPerWriter; i++ {
			pos := int64((w*recsPerWriter + i) * recSize)
			n, ok := r.ReadAt(pos, out)
			if !ok || n != recSize {
				t.Fatalf("ReadAt(w=%d rec=%d pos=%d) = (%d, %v)", w, i, pos, n, ok)
			}
			for k := range out {
				if out[k] != byte('A'+w) {
					t.Fatalf("writer %d rec %d byte %d = 0x%02x, want 0x%02x",
						w, i, k, out[k], byte('A'+w))
				}
			}
		}
	}
}

// TestMemRingAdvanceWindowMakesRoomForNextWrite verifies the fix for the
// "WriteReserved range outside ring window" failure that occurred once total
// WAL written exceeded ring capacity. After cap bytes of PublishUpTo calls,
// head == tail - cap, leaving the window [tail-cap, tail). The next write at
// pos = tail fails because pos >= head+cap. AdvanceWindow(pos+len) slides head
// to max(head, pos+len-cap), making [pos+len-cap, pos+len) the new window, so
// the write at pos succeeds.
func TestMemRingAdvanceWindowMakesRoomForNextWrite(t *testing.T) {
	t.Parallel()
	const cap = 128
	r := NewMemRing(cap)

	// Fill the ring: write cap bytes in two chunks and publish.
	data1 := make([]byte, cap/2)
	data2 := make([]byte, cap/2)
	r.WriteReserved(0, data1)
	r.WriteReserved(int64(cap/2), data2)
	r.PublishUpTo(int64(cap))
	// Now head == 0, tail == cap (ring exactly full).

	// WITHOUT AdvanceWindow, writing at pos=cap would fail (pos >= head+cap=cap).
	if err := r.WriteReserved(int64(cap), make([]byte, 1)); err == nil {
		t.Fatal("expected WriteReserved to fail before AdvanceWindow, but it succeeded")
	}

	// WITH AdvanceWindow(cap+1), head advances to 1, head+cap=cap+1 > cap.
	r.AdvanceWindow(int64(cap) + 1)
	if err := r.WriteReserved(int64(cap), make([]byte, 1)); err != nil {
		t.Fatalf("WriteReserved after AdvanceWindow: %v", err)
	}
}

// TestMemRingAdvanceWindowIsNilSafe ensures AdvanceWindow on nil receiver is a no-op.
func TestMemRingAdvanceWindowIsNilSafe(t *testing.T) {
	t.Parallel()
	var r *MemRing
	r.AdvanceWindow(1000) // must not panic
}

// TestMemRingAdvanceWindowDoesNotRollBackHead confirms that AdvanceWindow
// with a value smaller than the current head+cap leaves head unchanged.
func TestMemRingAdvanceWindowDoesNotRollBackHead(t *testing.T) {
	t.Parallel()
	const cap = 64
	r := NewMemRing(cap)
	r.AdvanceWindow(64)  // head → 0 (64-64=0); no change since 0 ≤ head=0
	r.AdvanceWindow(128) // head → 64
	r.AdvanceWindow(80)  // 80-64=16 < 64; no change
	r.WriteReserved(64, make([]byte, 32))
	// If head were rolled back to 16, pos+len=96 > 80=head+cap would fail.
	if err := r.WriteReserved(64, make([]byte, 32)); err != nil {
		t.Fatalf("WriteReserved after non-advancing AdvanceWindow: %v", err)
	}
}
