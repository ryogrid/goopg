package wal

import (
	"bytes"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestStripeWriterCoreAppendHappyPath verifies that a single Append on
// a fresh core lands bytes in both rings (after PublishUpTo), updates
// the posTracker (visible via Load), and produces the expected
// (start=0, prev=0) values for the first record.
func TestStripeWriterCoreAppendHappyPath(t *testing.T) {
	walBuf := newWALBuffer(4096)
	walBuf.reset(0)
	memRing := NewMemRing(4096)

	core := newStripeWriterCore(1<<20, 0, 0, walBuf, memRing, padLayout{})

	record := []byte("hello-stripe-core")
	start, prev, err := core.Append(0, record)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if start != 0 || prev != 0 {
		t.Fatalf("first append: start=%d prev=%d, want 0/0", start, prev)
	}

	// Pre-publish: bytes are written but not visible to readers.
	out := make([]byte, len(record))
	if n := walBuf.readAt(0, out); n != 0 {
		t.Fatalf("walBuf.readAt before publish: n=%d, want 0", n)
	}
	memOut := make([]byte, len(record))
	if n, ok := memRing.ReadAt(0, memOut); ok || n != 0 {
		t.Fatalf("memRing.ReadAt before publish: n=%d ok=%t, want 0/false", n, ok)
	}

	// Drain-side publish drives the watermark up to curr.
	curr, _ := core.Load()
	publishedTail := core.PublishUpTo(int64(curr))
	if publishedTail != int64(len(record)) {
		t.Fatalf("PublishUpTo: published=%d, want %d", publishedTail, len(record))
	}

	// Post-publish: bytes visible via both rings.
	if n := walBuf.readAt(0, out); n != len(record) || !bytes.Equal(out, record) {
		t.Fatalf("walBuf.readAt after publish: n=%d bytes=%q", n, out)
	}
	if n, ok := memRing.ReadAt(0, memOut); !ok || n != len(record) || !bytes.Equal(memOut, record) {
		t.Fatalf("memRing.ReadAt after publish: n=%d ok=%t bytes=%q", n, ok, memOut)
	}

	// Load reflects post-reservation position.
	if curr != uint64(len(record)) || prev != 0 {
		t.Fatalf("Load after first append: curr=%d prev=%d", curr, prev)
	}
}

// TestStripeWriterCoreNilReceiverGuards pins the nil-safety contract:
// methods on a nil receiver return structured errors / zeros instead
// of panicking, so transitional call-site states (core unset, e.g.
// in a test fixture or during initialisation) are benign.
func TestStripeWriterCoreNilReceiverGuards(t *testing.T) {
	var c *stripeWriterCore
	if _, _, err := c.Append(0, []byte("x")); !errors.Is(err, errStripeWriterCoreNil) {
		t.Fatalf("Append on nil: err=%v, want errStripeWriterCoreNil", err)
	}
	if got := c.PublishUpTo(100); got != 0 {
		t.Fatalf("PublishUpTo on nil: %d, want 0", got)
	}
	if curr, prev := c.Load(); curr != 0 || prev != 0 {
		t.Fatalf("Load on nil: %d/%d, want 0/0", curr, prev)
	}
	if got := c.PublishedTail(); got != 0 {
		t.Fatalf("PublishedTail on nil: %d, want 0", got)
	}
}

// TestStripeWriterCoreNilRingsStillProgress verifies that
// Config.WALBuffers == 0 (walBuf nil) and wal_sender_memory_buffer == 0
// (memRing nil) are independently supported — the constructor accepts
// nil rings and Append still reserves LSN space, just skips the byte
// writes on the nil ring.
func TestStripeWriterCoreNilRingsStillProgress(t *testing.T) {
	t.Run("walBuf_nil", func(t *testing.T) {
		memRing := NewMemRing(4096)
		core := newStripeWriterCore(1<<20, 0, 0, nil, memRing, padLayout{})
		record := []byte("only-memring")
		start, _, err := core.Append(0, record)
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		core.PublishUpTo(int64(start) + int64(len(record)))
		out := make([]byte, len(record))
		if n, ok := memRing.ReadAt(int64(start), out); !ok || n != len(record) {
			t.Fatalf("memRing.ReadAt: ok=%t n=%d", ok, n)
		}
	})
	t.Run("memRing_nil", func(t *testing.T) {
		walBuf := newWALBuffer(4096)
		walBuf.reset(0)
		core := newStripeWriterCore(1<<20, 0, 0, walBuf, nil, padLayout{})
		record := []byte("only-walbuf")
		start, _, err := core.Append(0, record)
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		core.PublishUpTo(int64(start) + int64(len(record)))
		out := make([]byte, len(record))
		if n := walBuf.readAt(int64(start), out); n != len(record) {
			t.Fatalf("walBuf.readAt: n=%d", n)
		}
	})
	t.Run("both_nil", func(t *testing.T) {
		core := newStripeWriterCore(1<<20, 0, 0, nil, nil, padLayout{})
		start, _, err := core.Append(0, []byte("ghost"))
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if start != 0 {
			t.Fatalf("start=%d, want 0", start)
		}
		// PublishUpTo still returns a coherent safeTail — both rings'
		// nil receivers are no-op on publish.
		if got := core.PublishUpTo(int64(start) + 5); got != int64(start)+5 {
			t.Fatalf("PublishUpTo on both-nil: %d, want %d", got, start+5)
		}
	})
}

// TestStripeWriterCoreRejectsZeroSegSize pins the constructor
// invariant: zero segSize is illegal and surfaces immediately (via
// the newInsertPosTracker panic), not later when the first
// cross-segment reservation would silently overflow.
func TestStripeWriterCoreRejectsZeroSegSize(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic from newStripeWriterCore(segSize=0, padLayout{})")
		}
	}()
	_ = newStripeWriterCore(0, 0, 0, nil, nil, padLayout{})
}

// TestStripeWriterCoreRecoveryResume verifies that startCurr /
// startPrev parameters are propagated into the posTracker so a
// freshly-constructed core can resume at an arbitrary LSN pair (the
// recovery-time segment scan result).
func TestStripeWriterCoreRecoveryResume(t *testing.T) {
	const (
		startCurr = uint64(0x100)
		startPrev = uint64(0x80)
	)
	walBuf := newWALBuffer(4096)
	walBuf.reset(int64(startCurr))
	// MemRing is initially anchored at [head=0, tail=0]; a fresh resume
	// scenario starts in-window because startCurr=256 < cap=4096.
	memRing := NewMemRing(4096)

	core := newStripeWriterCore(1<<20, startCurr, startPrev, walBuf, memRing, padLayout{})

	curr, prev := core.Load()
	if curr != startCurr || prev != startPrev {
		t.Fatalf("Load post-construct: curr=%#x prev=%#x, want %#x/%#x",
			curr, prev, startCurr, startPrev)
	}

	// First append lands at startCurr; its prev field carries startPrev.
	record := []byte("resume-record")
	start, p, err := core.Append(0, record)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if start != startCurr {
		t.Fatalf("first resume append: start=%#x, want %#x", start, startCurr)
	}
	if p != startPrev {
		t.Fatalf("first resume append: prev=%#x, want %#x", p, startPrev)
	}
}

// TestStripeWriterCoreCrossSegmentEmitsPad verifies that the
// constructor's onCrossSegment closure fires emitSegmentPad when a
// reservation straddles a segment boundary. The pad record fills
// the gap with a well-formed XLOG_NOOP record; the triggering
// reservation lands at the post-boundary LSN with prev = pad's
// start LSN.
//
// Setup mirrors stripe_append_test.go's cross-segment scenario:
// segSize=200, two 80-byte reservations fit (starts 0 + 80), the
// third 80-byte reservation crosses → pad fills [160, 200) and
// lands at 200 with prev=160.
func TestStripeWriterCoreCrossSegmentEmitsPad(t *testing.T) {
	const segSize uint64 = 200
	walBuf := newWALBuffer(int64(segSize) * 2)
	walBuf.reset(0)
	memRing := NewMemRing(int64(segSize) * 2)

	core := newStripeWriterCore(segSize, 0, 0, walBuf, memRing, padLayout{})

	r1 := bytes.Repeat([]byte{0xAA}, 80)
	r2 := bytes.Repeat([]byte{0xBB}, 80)
	r3 := bytes.Repeat([]byte{0xCC}, 80)

	s1, _, err := core.Append(0, r1)
	if err != nil || s1 != 0 {
		t.Fatalf("r1: s=%d err=%v", s1, err)
	}
	s2, _, err := core.Append(0, r2)
	if err != nil || s2 != 80 {
		t.Fatalf("r2: s=%d err=%v", s2, err)
	}
	// r3 would land at 160..240 — crosses boundary 200.
	s3, p3, err := core.Append(0, r3)
	if err != nil {
		t.Fatalf("r3: %v", err)
	}
	if s3 != segSize {
		t.Fatalf("r3: start=%d, want %d", s3, segSize)
	}
	if p3 != 160 {
		t.Fatalf("r3: prev=%d, want 160 (pad start)", p3)
	}

	// Publish past r3.
	core.PublishUpTo(int64(segSize) + 80)

	// Pad bytes occupy [160, 200) — verify a non-zero header.
	pad := make([]byte, 40)
	if n := walBuf.readAt(160, pad); n != 40 {
		t.Fatalf("walBuf.readAt pad: n=%d", n)
	}
	// First byte of an XLogRecord is the low byte of xl_tot_len; 40 (0x28)
	// is non-zero, confirming a record header is present.
	if pad[0] == 0 && pad[1] == 0 && pad[2] == 0 && pad[3] == 0 {
		t.Fatalf("pad header all-zero: %x", pad[:8])
	}

	// r3 bytes byte-identical in both rings.
	r3Out := make([]byte, 80)
	if n := walBuf.readAt(int64(segSize), r3Out); n != 80 || !bytes.Equal(r3Out, r3) {
		t.Fatalf("walBuf.readAt r3: n=%d match=%t", n, bytes.Equal(r3Out, r3))
	}
	memOut := make([]byte, 80)
	if n, ok := memRing.ReadAt(int64(segSize), memOut); !ok || n != 80 || !bytes.Equal(memOut, r3) {
		t.Fatalf("memRing.ReadAt r3: n=%d ok=%t match=%t", n, ok, bytes.Equal(memOut, r3))
	}
}

// TestStripeWriterCorePublishUpToCapsAtActiveStripe pins the
// drain-side composition: while a stripe is mid-insert (slot still
// holds the start LSN), PublishUpTo's tailPublisher caps the safe
// tail at that stripe's start; after the stripe completes,
// PublishUpTo advances both rings to the requested upperBound.
//
// This synthesises an active stripe by manually setting an
// insertion slot — the natural way an in-flight reservation
// reaches the slot is via reserveAndPublish, which also writes
// bytes; for this isolation test we set the slot directly and
// verify the publication-watermark cap.
func TestStripeWriterCorePublishUpToCapsAtActiveStripe(t *testing.T) {
	walBuf := newWALBuffer(4096)
	walBuf.reset(0)
	memRing := NewMemRing(4096)
	core := newStripeWriterCore(1<<20, 0, 0, walBuf, memRing, padLayout{})

	// Synthesise an active stripe at LSN 600 (no bytes written —
	// PublishUpTo only consults the insertion tracker for the cap).
	core.inserting.setInsertingAt(2, 600)

	published := core.PublishUpTo(1000)
	if published != 600 {
		t.Fatalf("PublishUpTo with active stripe: %d, want 600", published)
	}
	if walBuf.tail.Load() != 600 {
		t.Fatalf("walBuf.tail = %d, want 600", walBuf.tail.Load())
	}

	// Mark stripe idle; the next publish should advance to upperBound.
	core.inserting.setInsertingAt(2, lsnIdle)
	published = core.PublishUpTo(1000)
	if published != 1000 {
		t.Fatalf("PublishUpTo after idle: %d, want 1000", published)
	}
	if walBuf.tail.Load() != 1000 {
		t.Fatalf("walBuf.tail after idle = %d, want 1000", walBuf.tail.Load())
	}
}

// TestStripeWriterCorePublishedTailReflectsInternalState verifies the
// PublishedTail accessor: it returns the publisher's raw watermark
// (not capped at any upperBound), so callers can introspect the
// publisher independently of a PublishUpTo call.
func TestStripeWriterCorePublishedTailReflectsInternalState(t *testing.T) {
	core := newStripeWriterCore(1<<20, 0, 0, nil, nil, padLayout{})
	if got := core.PublishedTail(); got != 0 {
		t.Fatalf("fresh PublishedTail: %d, want 0", got)
	}
	// PublishUpTo with no rings + idle tracker advances internal
	// watermark.
	core.PublishUpTo(500)
	if got := core.PublishedTail(); got != 500 {
		t.Fatalf("after PublishUpTo(500): %d, want 500", got)
	}
	// Regressing upperBound does NOT roll the internal watermark back.
	core.PublishUpTo(200)
	if got := core.PublishedTail(); got != 500 {
		t.Fatalf("after PublishUpTo(200): %d, want 500 (monotonic)", got)
	}
}

// TestStripeWriterCoreConcurrentAppendsAndPublish drives multiple
// stripe writers in parallel while a drain goroutine continuously
// calls PublishUpTo. Pins the full slice B writer + drain
// composition (the call-site rewrite's intended use shape) under
// `-race`.
func TestStripeWriterCoreConcurrentAppendsAndPublish(t *testing.T) {
	walBuf := newWALBuffer(1 << 20)
	walBuf.reset(0)
	memRing := NewMemRing(1 << 20)
	core := newStripeWriterCore(1<<20, 0, 0, walBuf, memRing, padLayout{})

	const (
		writers      = 8
		recordsEach  = 200
		recordSize   = 16
		expectedTail = writers * recordsEach * recordSize
	)

	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		procNum := int32(i)
		go func() {
			defer wg.Done()
			rec := bytes.Repeat([]byte{byte(procNum + 1)}, recordSize)
			for j := 0; j < recordsEach; j++ {
				if _, _, err := core.Append(procNum, rec); err != nil {
					t.Errorf("Append procNum=%d j=%d: %v", procNum, j, err)
					return
				}
			}
		}()
	}

	// Drain goroutine continuously publishes up to current curr.
	var stop atomic.Bool
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for !stop.Load() {
			curr, _ := core.Load()
			core.PublishUpTo(int64(curr))
		}
	}()

	wg.Wait()

	// Final publish to ensure the drain has caught up to the last
	// reservation (the drain may have caught a snapshot mid-insert).
	curr, _ := core.Load()
	for i := 0; i < 100; i++ {
		core.PublishUpTo(int64(curr))
		if core.PublishedTail() == int64(curr) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	stop.Store(true)
	<-drainDone

	if curr != expectedTail {
		t.Fatalf("final curr=%d, want %d", curr, expectedTail)
	}
	if pub := core.PublishedTail(); pub != int64(expectedTail) {
		t.Fatalf("final PublishedTail=%d, want %d", pub, expectedTail)
	}
	if walBuf.tail.Load() != int64(expectedTail) {
		t.Fatalf("walBuf.tail=%d, want %d", walBuf.tail.Load(), expectedTail)
	}
}

// TestStripeWriterCoreConcurrentCompletesUnderWatchdog wraps the
// concurrent scenario in a 5-second watchdog so deadlock regressions
// surface inside the package-level test timeout rather than the
// CI-level one. Mirrors foundations 7 / 9 / 11.
func TestStripeWriterCoreConcurrentCompletesUnderWatchdog(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Re-run the concurrent scenario body by invoking the public
		// test (re-entering t.Run is the simplest way to share the
		// fixture without externalising a helper).
		t.Run("inner", TestStripeWriterCoreConcurrentAppendsAndPublish)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stripeWriterCore concurrent scenario exceeded 5-second watchdog")
	}
}

// TestStripeWriterCoreAppendEmptyRecord pins the stripeAppend
// contract pass-through: an empty record is rejected before any
// side effect, so the posTracker / insertionTracker remain
// untouched (visible via Load) and the call-site rewrite cannot
// hide an empty-record bug.
func TestStripeWriterCoreAppendEmptyRecord(t *testing.T) {
	core := newStripeWriterCore(1<<20, 0, 0, nil, nil, padLayout{})
	if _, _, err := core.Append(0, nil); !errors.Is(err, errStripeAppendEmptyRecord) {
		t.Fatalf("Append(nil): err=%v, want errStripeAppendEmptyRecord", err)
	}
	if _, _, err := core.Append(0, []byte{}); !errors.Is(err, errStripeAppendEmptyRecord) {
		t.Fatalf("Append([]byte{}): err=%v, want errStripeAppendEmptyRecord", err)
	}
	if curr, prev := core.Load(); curr != 0 || prev != 0 {
		t.Fatalf("Load after empty Append: curr=%d prev=%d, want 0/0", curr, prev)
	}
	for i := 0; i < appendLockStripes; i++ {
		if v := core.inserting.insertingAt(i); v != lsnIdle {
			t.Fatalf("stripe %d slot=%d after empty Append, want lsnIdle", i, v)
		}
	}
}
