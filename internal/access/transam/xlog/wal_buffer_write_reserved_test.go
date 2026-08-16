package xlog

import (
	"errors"
	"sync"
	"testing"
)

// TestWALBufferWriteReservedAtBaseNoWrap pins the simple in-segment
// case: write at lsn == base, fully inside the ring, no wrap. The
// bytes must land at buf[0..n] and head/tail/base must be unchanged
// (writeReserved is a pure byte-write — tail publication is a
// separate primitive).
func TestWALBufferWriteReservedAtBaseNoWrap(t *testing.T) {
	t.Parallel()
	b := newWALBuffer(64)
	b.reset(100)
	rec := []byte("hello-world")
	if err := b.writeReserved(100, rec); err != nil {
		t.Fatalf("writeReserved: %v", err)
	}
	if got := string(b.buf[0:len(rec)]); got != string(rec) {
		t.Fatalf("buf contents = %q, want %q", got, string(rec))
	}
	if b.base.Load() != 100 || b.head.Load() != 100 || b.tail.Load() != 100 {
		t.Fatalf("head/tail/base mutated: base=%d head=%d tail=%d", b.base.Load(), b.head.Load(), b.tail.Load())
	}
}

// TestWALBufferWriteReservedAtNonZeroOffset verifies the LSN→ring
// offset arithmetic for a non-zero offset inside the window.
func TestWALBufferWriteReservedAtNonZeroOffset(t *testing.T) {
	t.Parallel()
	b := newWALBuffer(64)
	b.reset(100)
	rec := []byte{0xAA, 0xBB, 0xCC, 0xDD}
	if err := b.writeReserved(120, rec); err != nil {
		t.Fatalf("writeReserved: %v", err)
	}
	// Offset = (120 - 100) % 64 = 20.
	for i, c := range rec {
		if b.buf[20+i] != c {
			t.Fatalf("buf[%d] = %02x, want %02x", 20+i, b.buf[20+i], c)
		}
	}
	// Surrounding bytes must remain zero.
	if b.buf[19] != 0 || b.buf[24] != 0 {
		t.Fatalf("neighbouring bytes mutated: buf[19]=%02x buf[24]=%02x", b.buf[19], b.buf[24])
	}
}

// TestWALBufferWriteReservedRejectsBelowBase pins the lower-edge
// boundary: lsn < base is a contract violation.
func TestWALBufferWriteReservedRejectsBelowBase(t *testing.T) {
	t.Parallel()
	b := newWALBuffer(32)
	b.reset(100)
	if err := b.writeReserved(99, []byte("x")); !errors.Is(err, errWALBufferReservedOutOfRange) {
		t.Fatalf("lsn<base: err = %v, want errWALBufferReservedOutOfRange", err)
	}
	if err := b.writeReserved(0, []byte("x")); !errors.Is(err, errWALBufferReservedOutOfRange) {
		t.Fatalf("lsn=0<base: err = %v, want errWALBufferReservedOutOfRange", err)
	}
}

// TestWALBufferWriteReservedRejectsPastEnd pins the upper-edge
// boundary: lsn + len > base + cap is a contract violation;
// lsn + len == base + cap (exactly at the right edge) is accepted.
func TestWALBufferWriteReservedRejectsPastEnd(t *testing.T) {
	t.Parallel()
	b := newWALBuffer(32)
	b.reset(100)
	if err := b.writeReserved(131, []byte("xy")); !errors.Is(err, errWALBufferReservedOutOfRange) {
		t.Fatalf("lsn+n>base+cap: err = %v, want errWALBufferReservedOutOfRange", err)
	}
	if err := b.writeReserved(132, []byte("x")); !errors.Is(err, errWALBufferReservedOutOfRange) {
		t.Fatalf("lsn=base+cap: err = %v, want errWALBufferReservedOutOfRange", err)
	}
	if err := b.writeReserved(131, []byte("z")); err != nil {
		t.Fatalf("lsn+n==base+cap: unexpected error: %v", err)
	}
}

// TestWALBufferWriteReservedEmptyIsNoop pins the empty-record
// short-circuit (matches state.appendRaw's len==0 behaviour). Even
// an "out of range" empty reservation is a no-op — the short-circuit
// runs before the range check.
func TestWALBufferWriteReservedEmptyIsNoop(t *testing.T) {
	t.Parallel()
	b := newWALBuffer(16)
	b.reset(50)
	before := append([]byte(nil), b.buf...)
	if err := b.writeReserved(60, nil); err != nil {
		t.Fatalf("writeReserved(nil): %v", err)
	}
	if err := b.writeReserved(60, []byte{}); err != nil {
		t.Fatalf("writeReserved([]): %v", err)
	}
	if err := b.writeReserved(999, nil); err != nil {
		t.Fatalf("writeReserved out-of-range nil: %v", err)
	}
	for i, c := range before {
		if b.buf[i] != c {
			t.Fatalf("buf mutated at i=%d: %02x → %02x", i, c, b.buf[i])
		}
	}
	if b.base.Load() != 50 || b.head.Load() != 50 || b.tail.Load() != 50 {
		t.Fatalf("head/tail/base mutated by empty write")
	}
}

// TestWALBufferWriteReservedNilReceiver pins the nil-buffer guard —
// matches the existing `s.walBuf != nil` pattern in state.append,
// makes the 8-stripe slice B call-site rewrite safe against
// Config.WALBuffers == 0 deployments.
func TestWALBufferWriteReservedNilReceiver(t *testing.T) {
	t.Parallel()
	var b *walBuffer
	if err := b.writeReserved(0, []byte("x")); !errors.Is(err, errWALBufferNil) {
		t.Fatalf("nil receiver: err = %v, want errWALBufferNil", err)
	}
}

// TestWALBufferWriteReservedConcurrentDisjoint exercises the
// stripe-style use case: N goroutines each own a disjoint LSN range
// and writeReserved concurrently. Under -race this catches any
// accidental mutation of head/tail/base or unsynchronized state.
// After all writers complete, the buffer contents must match the
// per-stripe writes (verified via readAt after manually publishing
// tail).
func TestWALBufferWriteReservedConcurrentDisjoint(t *testing.T) {
	t.Parallel()
	const (
		stripes       = 8
		recordsPerStr = 50
		recordLen     = 16
	)
	totalBytes := int64(stripes * recordsPerStr * recordLen)
	b := newWALBuffer(totalBytes)
	b.reset(0)

	var wg sync.WaitGroup
	for s := 0; s < stripes; s++ {
		s := s
		wg.Add(1)
		go func() {
			defer wg.Done()
			base := int64(s * recordsPerStr * recordLen)
			for r := 0; r < recordsPerStr; r++ {
				rec := make([]byte, recordLen)
				marker := byte((s*recordsPerStr + r) & 0xFF)
				for i := range rec {
					rec[i] = marker
				}
				lsn := base + int64(r*recordLen)
				if err := b.writeReserved(lsn, rec); err != nil {
					t.Errorf("stripe=%d r=%d: %v", s, r, err)
					return
				}
			}
		}()
	}
	wg.Wait()

	// Manually publish tail (the publication primitive is a
	// separate slice B foundation; for this test we promote tail
	// directly to expose readAt over the written bytes).
	b.tail.Store(totalBytes)

	out := make([]byte, recordLen)
	for s := 0; s < stripes; s++ {
		for r := 0; r < recordsPerStr; r++ {
			lsn := int64(s*recordsPerStr*recordLen + r*recordLen)
			n := b.readAt(lsn, out)
			if n != recordLen {
				t.Fatalf("readAt stripe=%d r=%d: n=%d, want %d", s, r, n, recordLen)
			}
			want := byte((s*recordsPerStr + r) & 0xFF)
			for i := 0; i < recordLen; i++ {
				if out[i] != want {
					t.Fatalf("stripe=%d r=%d off=%d: got %02x, want %02x", s, r, i, out[i], want)
				}
			}
		}
	}
}

// TestWALBufferWriteReservedReadbackViaReadAt confirms the bytes
// land at exactly the LSN that readAt subsequently retrieves. This
// pins the LSN→ring-offset arithmetic against readAt's mirror
// arithmetic.
func TestWALBufferWriteReservedReadbackViaReadAt(t *testing.T) {
	t.Parallel()
	b := newWALBuffer(64)
	b.reset(200)
	rec := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE}
	if err := b.writeReserved(210, rec); err != nil {
		t.Fatalf("writeReserved: %v", err)
	}
	b.tail.Store(216)
	out := make([]byte, len(rec))
	n := b.readAt(210, out)
	if n != len(rec) {
		t.Fatalf("readAt: n=%d, want %d", n, len(rec))
	}
	for i := range rec {
		if out[i] != rec[i] {
			t.Fatalf("readAt[%d] = %02x, want %02x", i, out[i], rec[i])
		}
	}
}

// TestWALBufferWriteReservedDoesNotMutateTailHeadBase guards the
// publication-is-separate contract — writeReserved is a pure byte
// landing and must never touch the position pointers.
func TestWALBufferWriteReservedDoesNotMutateTailHeadBase(t *testing.T) {
	t.Parallel()
	b := newWALBuffer(64)
	b.reset(1000)
	// Set tail and head to non-base values to verify writeReserved does not
	// mutate them. head must not exceed base+cap (1064) so the valid write
	// window [head, head+cap) starts inside the ring.
	b.tail.Store(1010)
	b.head.Store(1005) // head > base: 5 bytes already drained
	rec := []byte{1, 2, 3, 4, 5}
	// Write within [head, head+cap) = [1005, 1069) to avoid writing at
	// positions before head (those have been drained and are no longer valid).
	for lsn := int64(1005); lsn+int64(len(rec)) <= 1005+64; lsn += 7 {
		if err := b.writeReserved(lsn, rec); err != nil {
			t.Fatalf("writeReserved lsn=%d: %v", lsn, err)
		}
		if b.base.Load() != 1000 || b.head.Load() != 1005 || b.tail.Load() != 1010 {
			t.Fatalf("position mutated after lsn=%d: base=%d head=%d tail=%d",
				lsn, b.base.Load(), b.head.Load(), b.tail.Load())
		}
	}
}
