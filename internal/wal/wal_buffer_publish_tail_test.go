package wal

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestWALBufferPublishTailAdvancesFromBase verifies that the first
// publishTail on a freshly-reset buffer advances tail from base to
// the requested value, and returns the new tail. Head and base remain
// unchanged — publishTail's contract is "tail only".
func TestWALBufferPublishTailAdvancesFromBase(t *testing.T) {
	t.Parallel()
	b := newWALBuffer(64)
	b.reset(100)

	got := b.publishTail(140)
	if got != 140 {
		t.Fatalf("publishTail return = %d, want 140", got)
	}
	if b.tail.Load() != 140 {
		t.Fatalf("b.tail = %d, want 140", b.tail.Load())
	}
	if b.head.Load() != 100 || b.base.Load() != 100 {
		t.Fatalf("head/base mutated: head=%d base=%d, want both 100", b.head.Load(), b.base.Load())
	}
}

// TestWALBufferPublishTailMonotonicIgnoresRegression pins the
// monotonic-store contract. A second publishTail with a value below
// the current tail must be a no-op and the return value must be the
// existing tail.
func TestWALBufferPublishTailMonotonicIgnoresRegression(t *testing.T) {
	t.Parallel()
	b := newWALBuffer(128)
	b.reset(0)
	b.publishTail(64)

	got := b.publishTail(32)
	if got != 64 {
		t.Fatalf("regressing publishTail return = %d, want 64", got)
	}
	if b.tail.Load() != 64 {
		t.Fatalf("b.tail = %d, want 64 (must not regress)", b.tail.Load())
	}
}

// TestWALBufferPublishTailEqualIsNoop covers the boundary case
// safeTail == current tail. The monotonic check uses `<=` so equal
// is a no-op (matches MemRing.PublishUpTo).
func TestWALBufferPublishTailEqualIsNoop(t *testing.T) {
	t.Parallel()
	b := newWALBuffer(64)
	b.reset(0)
	b.publishTail(50)

	got := b.publishTail(50)
	if got != 50 {
		t.Fatalf("equal publishTail return = %d, want 50", got)
	}
	if b.tail.Load() != 50 || b.head.Load() != 0 || b.base.Load() != 0 {
		t.Fatalf("state changed under equal publish: tail=%d head=%d base=%d", b.tail.Load(), b.head.Load(), b.base.Load())
	}
}

// TestWALBufferPublishTailDoesNotMutateHeadBase confirms a series of
// monotonic publications leaves head and base untouched. Head can
// only advance via advanceHead (drain after I/O); publishTail must
// never reclaim ring slots.
func TestWALBufferPublishTailDoesNotMutateHeadBase(t *testing.T) {
	t.Parallel()
	b := newWALBuffer(256)
	b.reset(1000)

	for _, v := range []int64{1010, 1050, 1100, 1200, 1255} {
		b.publishTail(v)
		if b.head.Load() != 1000 || b.base.Load() != 1000 {
			t.Fatalf("publishTail(%d) mutated head=%d base=%d", v, b.head.Load(), b.base.Load())
		}
	}
	if b.tail.Load() != 1255 {
		t.Fatalf("final tail = %d, want 1255", b.tail.Load())
	}
}

// TestWALBufferPublishTailNilReceiver pins the nil-safe convention
// shared with writeReserved / MemRing.PublishUpTo. Important because
// the call-site rewrite at `Config.WALBuffers == 0` leaves walBuf as
// nil; the drain goroutine should still be able to call publishTail
// without an extra nil check.
func TestWALBufferPublishTailNilReceiver(t *testing.T) {
	t.Parallel()
	var b *walBuffer
	if got := b.publishTail(100); got != 0 {
		t.Fatalf("nil-receiver publishTail return = %d, want 0", got)
	}
}

// TestWALBufferPublishTailExposesWriteReservedBytesToReadAt is the
// end-to-end pairing check. Bytes landed via writeReserved are NOT
// visible to readAt before publishTail (readAt's `pos >= b.tail`
// guard returns 0); after publishTail covers them they become
// readable. Pins the publication-is-the-visibility-edge invariant.
func TestWALBufferPublishTailExposesWriteReservedBytesToReadAt(t *testing.T) {
	t.Parallel()
	b := newWALBuffer(128)
	b.reset(0)

	rec := []byte("publish-tail-test")
	if err := b.writeReserved(40, rec); err != nil {
		t.Fatalf("writeReserved: %v", err)
	}

	out := make([]byte, len(rec))
	if n := b.readAt(40, out); n != 0 {
		t.Fatalf("readAt before publish returned n=%d, want 0 (bytes not yet visible)", n)
	}

	b.publishTail(40 + int64(len(rec)))

	if n := b.readAt(40, out); n != len(rec) {
		t.Fatalf("readAt after publish: n=%d, want %d", n, len(rec))
	}
	if string(out) != string(rec) {
		t.Fatalf("readAt bytes = %q, want %q", string(out), string(rec))
	}
}

// TestWALBufferPublishTailMakesResidentTrackTailMinusHead pins the
// resident()-tracks-publication invariant: under the stripe-concurrent
// writer model, drain consumes resident() to decide how much to
// readForDrain — and resident() must reflect ONLY published bytes,
// never bytes still in-flight via writeReserved. Since publishTail
// is the only path to advance tail in the stripe-concurrent model,
// resident() == tail - head naturally satisfies this.
func TestWALBufferPublishTailMakesResidentTrackTailMinusHead(t *testing.T) {
	t.Parallel()
	b := newWALBuffer(256)
	b.reset(500)

	if got := b.resident(); got != 0 {
		t.Fatalf("fresh resident = %d, want 0", got)
	}

	// Stripe writes bytes at LSN 530 but doesn't publish yet.
	rec := make([]byte, 32)
	for i := range rec {
		rec[i] = 0xAB
	}
	if err := b.writeReserved(530, rec); err != nil {
		t.Fatalf("writeReserved: %v", err)
	}
	if got := b.resident(); got != 0 {
		t.Fatalf("resident after unpublished writeReserved = %d, want 0", got)
	}

	// Drain publishes up to 530 (a prior write at 500..530 only —
	// the 530..562 record is still in-flight from publisher's view).
	b.publishTail(530)
	if got := b.resident(); got != 30 {
		t.Fatalf("resident after publishTail(530) = %d, want 30", got)
	}

	// Now publish past the new record.
	b.publishTail(562)
	if got := b.resident(); got != 62 {
		t.Fatalf("resident after publishTail(562) = %d, want 62", got)
	}
}

// TestWALBufferPublishTailComposesWithAdvanceHead pins that drain's
// advanceHead and publication interleave correctly. Drain pattern:
// publishTail(X) → readForDrain → writeAt → advanceHead. resident()
// must shrink after advanceHead, and a follow-up publishTail must
// keep advancing tail relative to the *new* head, not the old one.
func TestWALBufferPublishTailComposesWithAdvanceHead(t *testing.T) {
	t.Parallel()
	b := newWALBuffer(256)
	b.reset(0)

	// Land bytes via writeReserved, publish, then drain via
	// advanceHead. publishTail must not perturb the advanceHead
	// arithmetic.
	rec := make([]byte, 40)
	for i := range rec {
		rec[i] = byte(i)
	}
	if err := b.writeReserved(0, rec); err != nil {
		t.Fatalf("writeReserved: %v", err)
	}
	b.publishTail(40)

	first, second := b.readForDrain(40)
	if int64(len(first)+len(second)) != 40 {
		t.Fatalf("readForDrain returned len=%d, want 40", len(first)+len(second))
	}
	b.advanceHead(40)
	if got := b.resident(); got != 0 {
		t.Fatalf("resident after drain = %d, want 0", got)
	}
	if b.head.Load() != 40 || b.tail.Load() != 40 {
		t.Fatalf("head/tail after drain: head=%d tail=%d, want 40/40", b.head.Load(), b.tail.Load())
	}

	// Another write+publish cycle confirms publishTail extends tail
	// from its post-advance value, not from a stale snapshot.
	if err := b.writeReserved(40, rec); err != nil {
		t.Fatalf("writeReserved second: %v", err)
	}
	got := b.publishTail(80)
	if got != 80 {
		t.Fatalf("publishTail second return = %d, want 80", got)
	}
	if b.tail.Load() != 80 || b.head.Load() != 40 {
		t.Fatalf("after second publish: tail=%d head=%d, want 80/40", b.tail.Load(), b.head.Load())
	}
	if got := b.resident(); got != 40 {
		t.Fatalf("resident after second publish = %d, want 40", got)
	}
}

// TestWALBufferPublishTailDoesNotEvictPendingWrites pins the
// no-auto-eviction contract. Unlike MemRing.PublishUpTo, walBuffer's
// publishTail must NOT auto-advance head when resident exceeds cap —
// pending writes are not yet on disk, so silently evicting them
// would lose data. The contract instead requires the caller (drain)
// to keep resident ≤ cap by advanceHead-after-writeAt.
//
// This test deliberately over-publishes (tail-head > cap) and
// verifies head stays put — the violation is the caller's problem,
// but the primitive must not paper over it by data-losing eviction.
func TestWALBufferPublishTailDoesNotEvictPendingWrites(t *testing.T) {
	t.Parallel()
	b := newWALBuffer(64)
	b.reset(0)

	b.publishTail(96) // 96 > cap=64 — contract violation by caller
	if b.head.Load() != 0 {
		t.Fatalf("publishTail evicted head: head=%d, want 0 (no auto-eviction)", b.head.Load())
	}
	if b.tail.Load() != 96 {
		t.Fatalf("publishTail did not advance tail: tail=%d, want 96", b.tail.Load())
	}
	// resident now reports 96 (caller's responsibility to drain).
	if got := b.resident(); got != 96 {
		t.Fatalf("resident = %d, want 96 (no auto-shrink)", got)
	}
}

// TestWALBufferPublishTailMonotonicUnderSerialisedAdvances pins
// that a sequence of publishTail calls forms a monotonic non-
// decreasing sequence in b.tail. Models the drain goroutine
// repeatedly receiving safeTail from tailPublisher (already monotonic
// per [[0107-0007n]]) and forwarding to publishTail. Race conditions
// across drains do not apply here (single drain), but a stale
// snapshot from a slow tailPublisher reader could yield a regression
// — silent no-op is correct.
func TestWALBufferPublishTailMonotonicUnderSerialisedAdvances(t *testing.T) {
	t.Parallel()
	b := newWALBuffer(4096)
	b.reset(0)

	vals := []int64{100, 200, 150, 300, 200, 400, 400, 401}
	want := []int64{100, 200, 200, 300, 300, 400, 400, 401}
	for i, v := range vals {
		b.publishTail(v)
		if b.tail.Load() != want[i] {
			t.Fatalf("step %d: after publishTail(%d) tail=%d, want %d", i, v, b.tail.Load(), want[i])
		}
	}
}

// TestWALBufferPublishTailRaceFreeWithDisjointWriters drives the
// canonical stripe-concurrent scenario: N writer goroutines call
// writeReserved at disjoint LSN ranges, a publisher goroutine
// monotonically calls publishTail. Under -race the test surfaces any
// data race the primitive may have introduced (it must not — the
// primitive only mutates b.tail; writeReserved touches only b.buf at
// disjoint offsets). After all writers join and a final publishTail
// covers the entire written range, readAt confirms every record
// landed in the right place.
//
// walBuffer.tail is atomic.Int64 (see
// docs/design/0107-0007r-wal-buffer-tail-atomic.md); the publisher
// goroutine and writer goroutines coexist without a data race
// because every read uses Load and every write uses Store.
func TestWALBufferPublishTailRaceFreeWithDisjointWriters(t *testing.T) {
	t.Parallel()
	runPublishTailDisjointWritersScenario(t)
}

// runPublishTailDisjointWritersScenario is the scenario body extracted
// so the watchdog test can re-run it with its own *testing.T sized for
// timeout enforcement. Calling t.Parallel() inside a manually-
// constructed *testing.T panics — keeping the parallel-marker on the
// public test and the body separate lets the watchdog reuse the
// scenario without that constraint.
func runPublishTailDisjointWritersScenario(t *testing.T) {
	const (
		stripes       = 8
		recordsPerStr = 50
		recordLen     = 16
	)
	totalBytes := int64(stripes * recordsPerStr * recordLen)
	b := newWALBuffer(totalBytes)
	b.reset(0)

	var writersDone sync.WaitGroup
	var maxPublished atomic.Int64
	publishReq := make(chan int64, stripes*recordsPerStr)
	publisherDone := make(chan struct{})
	go func() {
		defer close(publisherDone)
		for req := range publishReq {
			cur := maxPublished.Load()
			if req > cur {
				maxPublished.Store(req)
				b.publishTail(req)
			}
		}
	}()

	for s := 0; s < stripes; s++ {
		s := s
		writersDone.Add(1)
		go func() {
			defer writersDone.Done()
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
				publishReq <- lsn + recordLen
			}
		}()
	}
	writersDone.Wait()
	close(publishReq)
	<-publisherDone

	b.publishTail(totalBytes)
	if b.tail.Load() != totalBytes {
		t.Fatalf("final tail = %d, want %d", b.tail.Load(), totalBytes)
	}

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

// TestWALBufferPublishTailWatchdog is a deadlock-detection net for
// the concurrent scenario above. A regression that introduced a
// blocking pattern in publishTail (it currently takes no locks)
// would deadlock the disjoint-writers test; the watchdog surfaces it
// at 5 s rather than the package-level 10 m timeout, mirroring the
// pattern from foundation 7 / foundation 9.
func TestWALBufferPublishTailWatchdog(t *testing.T) {
	t.Parallel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runPublishTailDisjointWritersScenario(t)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publishTail concurrent scenario exceeded 5 s — possible deadlock")
	}
}

// TestWALBufferTailIsAtomicInt64 is a compile-time pin on the type
// of walBuffer.tail. The slice B call-site rewrite (8-stripe writers
// + dedicated drain goroutine) requires tail to be atomic.Int64 so
// concurrent readers (resident / readForDrain / readAt) and the
// single drain publisher observe a race-free monotonic watermark.
// Anyone shrinking this back to a plain int64 trips the type
// assertion at compile time.
func TestWALBufferTailIsAtomicInt64(t *testing.T) {
	t.Parallel()
	var b walBuffer
	// Take the address — atomic.Int64 has noCopy, so a value
	// assignment trips vet. The pointer form is the canonical
	// way to assert a field's atomic type.
	var _ *atomic.Int64 = &b.tail
}

// TestWALBufferPublishTailObservedByConcurrentReader pins that a
// reader goroutine observing b.tail via .Load() during a writer
// goroutine's Store loop never sees a value that exceeds the
// writer's highest stored value, and never observes a regression
// across two successive Loads — i.e. atomic semantics hold under
// concurrent access. The previous int64 field could in principle
// be sliced into torn reads on 32-bit; the atomic.Int64 upgrade
// guarantees this never happens regardless of platform.
//
// Race-detector also catches the data race on a plain int64 field
// when this test runs under `go test -race`; the explicit
// monotonicity assertion is defence in depth so a CI run without
// -race still catches a regression that re-introduces a torn-read
// hazard.
func TestWALBufferPublishTailObservedByConcurrentReader(t *testing.T) {
	t.Parallel()
	const iters = 100_000
	b := newWALBuffer(int64(iters) + 64)
	b.reset(0)

	var done atomic.Bool
	var stored atomic.Int64

	go func() {
		for i := int64(1); i <= iters; i++ {
			b.publishTail(i)
			stored.Store(i)
		}
		done.Store(true)
	}()

	var lastSeen int64
	for !done.Load() {
		v := b.tail.Load()
		if v < lastSeen {
			t.Fatalf("tail regressed: lastSeen=%d, current=%d", lastSeen, v)
		}
		if v > stored.Load()+1 {
			// stored is set AFTER publishTail returns; v may be
			// the very latest store that hasn't yet propagated to
			// `stored`. Allow a +1 slack.
			t.Fatalf("tail %d exceeded stored ceiling %d", v, stored.Load()+1)
		}
		lastSeen = v
	}
	if got := b.tail.Load(); got != iters {
		t.Fatalf("final tail = %d, want %d", got, iters)
	}
}
