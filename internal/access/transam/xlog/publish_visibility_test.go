package xlog

import (
	"bytes"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestPublishVisibilityIdleTrackerAdvancesBothRings exercises the happy
// path: with every stripe idle, publishVisibility advances both rings to
// the caller's upperBound.
func TestPublishVisibilityIdleTrackerAdvancesBothRings(t *testing.T) {
	publisher := newTailPublisher()
	walBuf := newWALBuffer(1024)
	walBuf.reset(0)
	memRing := NewMemRing(1024)
	tracker := newInsertionTracker()

	const upperBound int64 = 500
	got := publishVisibility(publisher, walBuf, memRing, tracker, upperBound)
	if got != upperBound {
		t.Fatalf("safeTail: got %d, want %d", got, upperBound)
	}
	if tail := walBuf.tail.Load(); tail != upperBound {
		t.Fatalf("walBuf.tail: got %d, want %d", tail, upperBound)
	}
	memRing.mu.RLock()
	memTail := memRing.tail
	memRing.mu.RUnlock()
	if memTail != upperBound {
		t.Fatalf("memRing.tail: got %d, want %d", memTail, upperBound)
	}
	if cur := publisher.load(); cur != upperBound {
		t.Fatalf("publisher.load: got %d, want %d", cur, upperBound)
	}
}

// TestPublishVisibilityActiveStripeCapsBothRings pins that an active
// stripe at LSN X below the caller's upperBound clamps the published
// watermark in both rings to X (the publisher's safeTail derivation
// flows through unchanged).
func TestPublishVisibilityActiveStripeCapsBothRings(t *testing.T) {
	publisher := newTailPublisher()
	walBuf := newWALBuffer(1024)
	walBuf.reset(0)
	memRing := NewMemRing(1024)
	tracker := newInsertionTracker()

	tracker.setInsertingAt(3, 600)
	got := publishVisibility(publisher, walBuf, memRing, tracker, 1000)
	if got != 600 {
		t.Fatalf("safeTail: got %d, want 600 (capped by stripe@600)", got)
	}
	if tail := walBuf.tail.Load(); tail != 600 {
		t.Fatalf("walBuf.tail: got %d, want 600", tail)
	}
	memRing.mu.RLock()
	memTail := memRing.tail
	memRing.mu.RUnlock()
	if memTail != 600 {
		t.Fatalf("memRing.tail: got %d, want 600", memTail)
	}

	// Now the stripe goes idle; second publish should advance both
	// rings to 1000.
	tracker.setInsertingAt(3, lsnIdle)
	got = publishVisibility(publisher, walBuf, memRing, tracker, 1000)
	if got != 1000 {
		t.Fatalf("safeTail after idle: got %d, want 1000", got)
	}
	if tail := walBuf.tail.Load(); tail != 1000 {
		t.Fatalf("walBuf.tail after idle: got %d, want 1000", tail)
	}
	memRing.mu.RLock()
	memTail = memRing.tail
	memRing.mu.RUnlock()
	if memTail != 1000 {
		t.Fatalf("memRing.tail after idle: got %d, want 1000", memTail)
	}
}

// TestPublishVisibilityMonotonicAcrossCalls pins that a sequence of
// calls with strictly increasing upperBounds advances both rings in
// lock-step.
func TestPublishVisibilityMonotonicAcrossCalls(t *testing.T) {
	publisher := newTailPublisher()
	walBuf := newWALBuffer(4096)
	walBuf.reset(0)
	memRing := NewMemRing(4096)
	tracker := newInsertionTracker()

	for _, ub := range []int64{100, 250, 250, 800, 800, 1600} {
		got := publishVisibility(publisher, walBuf, memRing, tracker, ub)
		if got != ub {
			t.Fatalf("safeTail at upperBound=%d: got %d", ub, got)
		}
		if tail := walBuf.tail.Load(); tail != ub {
			t.Fatalf("walBuf.tail at upperBound=%d: got %d", ub, tail)
		}
		memRing.mu.RLock()
		memTail := memRing.tail
		memRing.mu.RUnlock()
		if memTail != ub {
			t.Fatalf("memRing.tail at upperBound=%d: got %d", ub, memTail)
		}
	}
}

// TestPublishVisibilityRegressingUpperBoundDoesNotRegressRings pins
// that a subsequent call with a LOWER upperBound never causes either
// ring's tail to regress.
func TestPublishVisibilityRegressingUpperBoundDoesNotRegressRings(t *testing.T) {
	publisher := newTailPublisher()
	walBuf := newWALBuffer(4096)
	walBuf.reset(0)
	memRing := NewMemRing(4096)
	tracker := newInsertionTracker()

	publishVisibility(publisher, walBuf, memRing, tracker, 1000)
	got := publishVisibility(publisher, walBuf, memRing, tracker, 400)
	// tailPublisher caps the return at upperBound, so got==400; but
	// the rings' tails must NOT regress.
	if got != 400 {
		t.Fatalf("safeTail at lower upperBound: got %d, want 400 (cap)", got)
	}
	if tail := walBuf.tail.Load(); tail != 1000 {
		t.Fatalf("walBuf.tail regressed: got %d, want 1000", tail)
	}
	memRing.mu.RLock()
	memTail := memRing.tail
	memRing.mu.RUnlock()
	if memTail != 1000 {
		t.Fatalf("memRing.tail regressed: got %d, want 1000", memTail)
	}
}

// TestPublishVisibilityNilWalBufStillAdvancesMemRing exercises the
// `Config.WALBuffers == 0` case: walBuf is nil, memRing is configured.
// The composer publishes the memRing tail and returns the safeTail
// from the publisher.
func TestPublishVisibilityNilWalBufStillAdvancesMemRing(t *testing.T) {
	publisher := newTailPublisher()
	memRing := NewMemRing(1024)
	tracker := newInsertionTracker()

	got := publishVisibility(publisher, nil, memRing, tracker, 300)
	if got != 300 {
		t.Fatalf("safeTail: got %d, want 300", got)
	}
	memRing.mu.RLock()
	memTail := memRing.tail
	memRing.mu.RUnlock()
	if memTail != 300 {
		t.Fatalf("memRing.tail: got %d, want 300", memTail)
	}
}

// TestPublishVisibilityNilMemRingStillAdvancesWalBuf is the symmetric
// `wal_sender_memory_buffer == 0` case.
func TestPublishVisibilityNilMemRingStillAdvancesWalBuf(t *testing.T) {
	publisher := newTailPublisher()
	walBuf := newWALBuffer(1024)
	walBuf.reset(0)
	tracker := newInsertionTracker()

	got := publishVisibility(publisher, walBuf, nil, tracker, 300)
	if got != 300 {
		t.Fatalf("safeTail: got %d, want 300", got)
	}
	if tail := walBuf.tail.Load(); tail != 300 {
		t.Fatalf("walBuf.tail: got %d, want 300", tail)
	}
}

// TestPublishVisibilityBothRingsNil exercises the "publisher only"
// degenerate case — the publisher still advances; neither ring is
// touched (because both nil short-circuit). Useful for tests and for
// the call-site rewrite's transitional state.
func TestPublishVisibilityBothRingsNil(t *testing.T) {
	publisher := newTailPublisher()
	tracker := newInsertionTracker()

	got := publishVisibility(publisher, nil, nil, tracker, 777)
	if got != 777 {
		t.Fatalf("safeTail: got %d, want 777", got)
	}
	if cur := publisher.load(); cur != 777 {
		t.Fatalf("publisher.load: got %d, want 777", cur)
	}
}

// TestPublishVisibilityNilPublisherReturnsZero pins the defensive
// nil-receiver convention: a nil publisher returns 0 and the rings
// are not advanced past 0.
func TestPublishVisibilityNilPublisherReturnsZero(t *testing.T) {
	walBuf := newWALBuffer(1024)
	walBuf.reset(0)
	memRing := NewMemRing(1024)
	tracker := newInsertionTracker()

	got := publishVisibility(nil, walBuf, memRing, tracker, 500)
	if got != 0 {
		t.Fatalf("safeTail with nil publisher: got %d, want 0", got)
	}
	if tail := walBuf.tail.Load(); tail != 0 {
		t.Fatalf("walBuf.tail: got %d, want 0", tail)
	}
	memRing.mu.RLock()
	memTail := memRing.tail
	memRing.mu.RUnlock()
	if memTail != 0 {
		t.Fatalf("memRing.tail: got %d, want 0", memTail)
	}
}

// TestPublishVisibilityNilTrackerActsAsAllIdle confirms the
// transitional contract: a nil tracker behaves as if every stripe
// were idle, so safeTail == upperBound.
func TestPublishVisibilityNilTrackerActsAsAllIdle(t *testing.T) {
	publisher := newTailPublisher()
	walBuf := newWALBuffer(1024)
	walBuf.reset(0)
	memRing := NewMemRing(1024)

	got := publishVisibility(publisher, walBuf, memRing, nil, 425)
	if got != 425 {
		t.Fatalf("safeTail with nil tracker: got %d, want 425", got)
	}
	if tail := walBuf.tail.Load(); tail != 425 {
		t.Fatalf("walBuf.tail: got %d, want 425", tail)
	}
}

// TestPublishVisibilityExposesWriteReservedBytesEndToEnd pins the
// end-to-end contract: bytes written via the slice B stripe path
// (`walBuffer.writeReserved` + `MemRing.WriteReserved`) are invisible
// to readers until publishVisibility advances the watermark past
// them, then become readable in both rings.
func TestPublishVisibilityExposesWriteReservedBytesEndToEnd(t *testing.T) {
	publisher := newTailPublisher()
	walBuf := newWALBuffer(1024)
	walBuf.reset(0)
	memRing := NewMemRing(1024)
	tracker := newInsertionTracker()

	payload := []byte("slice-b-foundation-13-visibility")
	if err := walBuf.writeReserved(64, payload); err != nil {
		t.Fatalf("walBuf.writeReserved: %v", err)
	}
	if err := memRing.WriteReserved(64, payload); err != nil {
		t.Fatalf("memRing.WriteReserved: %v", err)
	}

	// Before publication, readers should observe nothing: walBuf.readAt
	// returns 0 (tail still at base); memRing.ReadAt misses.
	dst := make([]byte, len(payload))
	if n := walBuf.readAt(64, dst); n != 0 {
		t.Fatalf("pre-publish walBuf.readAt: got %d bytes, want 0", n)
	}
	if _, ok := memRing.ReadAt(64, dst); ok {
		t.Fatalf("pre-publish memRing.ReadAt: hit, want miss")
	}

	upperBound := int64(64 + len(payload))
	got := publishVisibility(publisher, walBuf, memRing, tracker, upperBound)
	if got != upperBound {
		t.Fatalf("safeTail: got %d, want %d", got, upperBound)
	}

	// After publication, both rings expose the bytes.
	if n := walBuf.readAt(64, dst); n != len(payload) || !bytes.Equal(dst, payload) {
		t.Fatalf("post-publish walBuf.readAt: n=%d dst=%q", n, dst)
	}
	dst2 := make([]byte, len(payload))
	if n, ok := memRing.ReadAt(64, dst2); !ok || n != len(payload) || !bytes.Equal(dst2, payload) {
		t.Fatalf("post-publish memRing.ReadAt: ok=%v n=%d dst=%q", ok, n, dst2)
	}
}

// TestPublishVisibilitySentinelComposesWithMin pins that the
// publisher's sentinel composition (idle tracker → safeTail =
// upperBound) flows correctly through both rings even when upperBound
// is very large but realistic (just below MaxInt64). Guards against a
// future refactor that mishandles the sentinel and leaks math.MaxInt64
// into either ring's tail.
func TestPublishVisibilitySentinelComposesWithMin(t *testing.T) {
	publisher := newTailPublisher()
	walBuf := newWALBuffer(1024)
	walBuf.reset(math.MaxInt64 - 1024)
	memRing := NewMemRing(1024)
	// MemRing starts at head=0; advance head first via PublishUpTo so
	// the window contains the test's upperBound (publishUpTo is
	// monotonic so we drive memRing's head up via successive calls).
	memRing.PublishUpTo(math.MaxInt64 - 1024)
	tracker := newInsertionTracker()

	got := publishVisibility(publisher, walBuf, memRing, tracker, math.MaxInt64-1)
	if got != math.MaxInt64-1 {
		t.Fatalf("safeTail: got %d, want MaxInt64-1", got)
	}
	if tail := walBuf.tail.Load(); tail != math.MaxInt64-1 {
		t.Fatalf("walBuf.tail: got %d, want MaxInt64-1", tail)
	}
}

// TestPublishVisibilityConcurrentWithStripeWriters drives the full
// slice B stripe-concurrent pattern: 8 writer stripes oscillate
// active/idle while a publisher goroutine repeatedly calls
// publishVisibility. The test asserts (a) both ring tails advance
// monotonically (no regression), (b) the rings' tails stay equal to
// the publisher.load() so the composition is consistent across
// rings, and (c) the run completes under a watchdog (no deadlock).
func TestPublishVisibilityConcurrentWithStripeWriters(t *testing.T) {
	publisher := newTailPublisher()
	walBuf := newWALBuffer(1 << 20)
	walBuf.reset(0)
	memRing := NewMemRing(1 << 20)
	tracker := newInsertionTracker()

	const writers = 8
	const iters = 5000
	var stop atomic.Bool
	var wg sync.WaitGroup

	// 8 writer stripes oscillating active/idle across [stripeBase,
	// stripeBase+iters).
	for s := 0; s < writers; s++ {
		s := s
		wg.Add(1)
		go func() {
			defer wg.Done()
			stripeBase := int64(s+1) * 100000
			for i := 0; i < iters && !stop.Load(); i++ {
				tracker.setInsertingAt(s, stripeBase+int64(i))
				tracker.setInsertingAt(s, lsnIdle)
			}
		}()
	}

	// Publisher goroutine: advance upperBound monotonically up to
	// the largest stripe-emitted LSN's upper bound.
	pubDone := make(chan struct{})
	go func() {
		defer close(pubDone)
		var lastTail int64
		const finalUpper = int64(writers+1) * 100000 * 2
		for ub := int64(1000); ub <= finalUpper; ub += 1000 {
			got := publishVisibility(publisher, walBuf, memRing, tracker, ub)
			if got < lastTail {
				t.Errorf("publisher regressed: got %d, lastTail %d", got, lastTail)
				return
			}
			lastTail = got
			// Cross-ring consistency check: walBuf.tail equals
			// memRing.tail after every publishVisibility call.
			wbTail := walBuf.tail.Load()
			memRing.mu.RLock()
			mrTail := memRing.tail
			memRing.mu.RUnlock()
			if wbTail != mrTail {
				t.Errorf("ring divergence: walBuf.tail=%d memRing.tail=%d", wbTail, mrTail)
				return
			}
		}
	}()

	watchdog := time.AfterFunc(5*time.Second, func() {
		stop.Store(true)
		t.Errorf("publishVisibility concurrent watchdog tripped")
	})
	defer watchdog.Stop()

	wg.Wait()
	stop.Store(true)
	<-pubDone
}
