package wal

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// makeStripeAppendFixture constructs a fully-wired slice B writer-side
// composer fixture. segSize defaults to 1 MiB (well past any test
// reservation so no segment crossings unless explicitly desired); the
// caller can override by passing a smaller segSize via the test-only
// builder pattern below.
func makeStripeAppendFixture(t *testing.T, cap int64) (
	locks *appendLockSet,
	posTracker *insertPosTracker,
	insertTracker *insertionTracker,
	walBuf *walBuffer,
	memRing *MemRing,
	publisher *tailPublisher,
) {
	t.Helper()
	locks = &appendLockSet{}
	walBuf = newWALBuffer(cap)
	walBuf.reset(0)
	memRing = NewMemRing(cap)
	insertTracker = newInsertionTracker()
	publisher = newTailPublisher()
	// segSize >> any record any test uses → no cross-segment in default fixture
	posTracker = newInsertPosTracker(0, 0, 1<<20, nil)
	return
}

// makeStripeAppendFixtureWithCross constructs a fixture with a small
// segSize and the cross-segment hook wired to emitSegmentPad so
// cross-segment scenarios can be exercised end-to-end.
func makeStripeAppendFixtureWithCross(t *testing.T, cap int64, segSize uint64) (
	locks *appendLockSet,
	posTracker *insertPosTracker,
	insertTracker *insertionTracker,
	walBuf *walBuffer,
	memRing *MemRing,
	publisher *tailPublisher,
) {
	t.Helper()
	locks = &appendLockSet{}
	walBuf = newWALBuffer(cap)
	walBuf.reset(0)
	memRing = NewMemRing(cap)
	insertTracker = newInsertionTracker()
	publisher = newTailPublisher()
	// Capture rings in the hook closure.
	wb := walBuf
	mr := memRing
	posTracker = newInsertPosTracker(0, 0, segSize, func(start, boundary, prev uint64) bool {
		if err := emitSegmentPadErr(wb, mr, start, boundary, prev, padLayout{}); err != nil {
			t.Errorf("emitSegmentPad in onCrossSegment: %v", err)
		}
		return true
	})
	return
}

func TestStripeAppendHappyPathWritesBothRings(t *testing.T) {
	t.Parallel()
	locks, posTracker, insertTracker, walBuf, memRing, publisher := makeStripeAppendFixture(t, 4096)

	record := []byte("hello-wal-record")
	start, prev, err := stripeAppend(locks, posTracker, insertTracker, walBuf, memRing, /*procNum*/ 3, record)
	if err != nil {
		t.Fatalf("stripeAppend: %v", err)
	}
	if start != 0 || prev != 0 {
		t.Fatalf("first reservation: start=%d prev=%d, want 0/0", start, prev)
	}

	// END marker landed: stripe slot back to idle, so lowestActiveLSN == sentinel.
	if got := insertTracker.lowestActiveLSN(); got != lsnNoActive {
		t.Fatalf("insertionTracker not idle after stripeAppend: %d", got)
	}

	// Bytes are in the rings but invisible without publication.
	out := make([]byte, len(record))
	if n := walBuf.readAt(0, out); n != 0 {
		t.Fatalf("walBuf.readAt before publish: n=%d, want 0", n)
	}
	memOut := make([]byte, len(record))
	if n, ok := memRing.ReadAt(0, memOut); ok || n != 0 {
		t.Fatalf("memRing.ReadAt before publish: n=%d ok=%t, want 0/false", n, ok)
	}

	// Drain-side publication makes them visible.
	upperBound := int64(len(record))
	if got := publishVisibility(publisher, walBuf, memRing, insertTracker, upperBound); got != upperBound {
		t.Fatalf("publishVisibility: got=%d, want %d", got, upperBound)
	}

	if n := walBuf.readAt(0, out); n != len(record) || !bytes.Equal(out, record) {
		t.Fatalf("walBuf.readAt after publish: n=%d bytes=%q", n, out)
	}
	if n, ok := memRing.ReadAt(0, memOut); !ok || n != len(record) || !bytes.Equal(memOut, record) {
		t.Fatalf("memRing.ReadAt after publish: n=%d ok=%t bytes=%q", n, ok, memOut)
	}
}

func TestStripeAppendNilLocksReturnsError(t *testing.T) {
	t.Parallel()
	_, posTracker, insertTracker, walBuf, memRing, _ := makeStripeAppendFixture(t, 4096)
	_, _, err := stripeAppend(nil, posTracker, insertTracker, walBuf, memRing, 0, []byte("x"))
	if !errors.Is(err, errStripeAppendNilLocks) {
		t.Fatalf("nil locks: err=%v, want errStripeAppendNilLocks", err)
	}
}

func TestStripeAppendNilPosTrackerReturnsError(t *testing.T) {
	t.Parallel()
	locks, _, insertTracker, walBuf, memRing, _ := makeStripeAppendFixture(t, 4096)
	_, _, err := stripeAppend(locks, nil, insertTracker, walBuf, memRing, 0, []byte("x"))
	if !errors.Is(err, errStripeAppendNilPosTracker) {
		t.Fatalf("nil posTracker: err=%v, want errStripeAppendNilPosTracker", err)
	}
}

func TestStripeAppendNilInsertTrackerReturnsError(t *testing.T) {
	t.Parallel()
	locks, posTracker, _, walBuf, memRing, _ := makeStripeAppendFixture(t, 4096)
	_, _, err := stripeAppend(locks, posTracker, nil, walBuf, memRing, 0, []byte("x"))
	if !errors.Is(err, errStripeAppendNilInsertTracker) {
		t.Fatalf("nil insertTracker: err=%v, want errStripeAppendNilInsertTracker", err)
	}
}

func TestStripeAppendEmptyRecordReturnsError(t *testing.T) {
	t.Parallel()
	locks, posTracker, insertTracker, walBuf, memRing, _ := makeStripeAppendFixture(t, 4096)
	for _, rec := range [][]byte{nil, {}} {
		_, _, err := stripeAppend(locks, posTracker, insertTracker, walBuf, memRing, 0, rec)
		if !errors.Is(err, errStripeAppendEmptyRecord) {
			t.Fatalf("empty record (%v): err=%v, want errStripeAppendEmptyRecord", rec, err)
		}
	}
	// And nothing was reserved or published.
	curr, _ := posTracker.load()
	if curr != 0 {
		t.Fatalf("posTracker.curr advanced on empty record: %d", curr)
	}
	if got := insertTracker.lowestActiveLSN(); got != lsnNoActive {
		t.Fatalf("insertionTracker not idle after empty: %d", got)
	}
}

func TestStripeAppendNilWalBufStillWritesMemRing(t *testing.T) {
	t.Parallel()
	locks, posTracker, insertTracker, _, memRing, _ := makeStripeAppendFixture(t, 4096)
	record := []byte("only-memring-please")
	start, _, err := stripeAppend(locks, posTracker, insertTracker, nil, memRing, 0, record)
	if err != nil {
		t.Fatalf("stripeAppend nil walBuf: %v", err)
	}
	memRing.PublishUpTo(int64(start) + int64(len(record)))
	out := make([]byte, len(record))
	if n, ok := memRing.ReadAt(int64(start), out); !ok || n != len(record) || !bytes.Equal(out, record) {
		t.Fatalf("memRing.ReadAt: n=%d ok=%t bytes=%q", n, ok, out)
	}
}

func TestStripeAppendNilMemRingStillWritesWalBuf(t *testing.T) {
	t.Parallel()
	locks, posTracker, insertTracker, walBuf, _, _ := makeStripeAppendFixture(t, 4096)
	record := []byte("only-walbuf-please")
	start, _, err := stripeAppend(locks, posTracker, insertTracker, walBuf, nil, 0, record)
	if err != nil {
		t.Fatalf("stripeAppend nil memRing: %v", err)
	}
	walBuf.publishTail(int64(start) + int64(len(record)))
	out := make([]byte, len(record))
	if n := walBuf.readAt(int64(start), out); n != len(record) || !bytes.Equal(out, record) {
		t.Fatalf("walBuf.readAt: n=%d bytes=%q", n, out)
	}
}

func TestStripeAppendWalBufOutOfWindowReturnsErrorAndClearsStripe(t *testing.T) {
	t.Parallel()
	locks, posTracker, insertTracker, walBuf, memRing, _ := makeStripeAppendFixture(t, 64)
	// Shrink walBuf cap to 64 but leave memRing at 64 too; reserve 80 bytes
	// → reserveAndPublish allows it (segSize=1MiB), but walBuf.writeReserved
	// rejects because 0+80 > 0+64.
	record := bytes.Repeat([]byte{0x55}, 80)
	_, _, err := stripeAppend(locks, posTracker, insertTracker, walBuf, memRing, 0, record)
	if !errors.Is(err, errWALBufferReservedOutOfRange) {
		t.Fatalf("walBuf overflow: err=%v, want errWALBufferReservedOutOfRange", err)
	}
	// Despite the error, the END marker must have fired so the drain
	// publication is not frozen.
	if got := insertTracker.lowestActiveLSN(); got != lsnNoActive {
		t.Fatalf("insertionTracker not idle after walBuf error: %d", got)
	}
}

func TestStripeAppendMemRingOutOfWindowReturnsErrorAndClearsStripe(t *testing.T) {
	t.Parallel()
	locks, posTracker, insertTracker, _, memRing, _ := makeStripeAppendFixture(t, 64)
	// nil walBuf so we exercise the memRing-error branch in isolation
	// (walBuf would reject first otherwise).
	record := bytes.Repeat([]byte{0xAA}, 80)
	_, _, err := stripeAppend(locks, posTracker, insertTracker, nil, memRing, 0, record)
	if !errors.Is(err, errMemRingReservedOutOfRange) {
		t.Fatalf("memRing overflow: err=%v, want errMemRingReservedOutOfRange", err)
	}
	if got := insertTracker.lowestActiveLSN(); got != lsnNoActive {
		t.Fatalf("insertionTracker not idle after memRing error: %d", got)
	}
}

func TestStripeAppendSelectsStripeByProcNum(t *testing.T) {
	t.Parallel()
	locks, posTracker, insertTracker, walBuf, memRing, _ := makeStripeAppendFixture(t, 4096)

	// Drive 8 procNums (0..7) sequentially with a 16-byte payload each.
	// Each call must publish its stripe (briefly), and after the END
	// marker the stripe must be idle. The pre-reserve race is closed
	// by reserveAndPublish, so the publication is sealed under posMu —
	// but here we only check post-call idleness because the per-stripe
	// observability while the call is running is exercised by the
	// concurrent test below.
	for procNum := int32(0); procNum < 8; procNum++ {
		record := bytes.Repeat([]byte{byte(procNum)}, 16)
		if _, _, err := stripeAppend(locks, posTracker, insertTracker, walBuf, memRing, procNum, record); err != nil {
			t.Fatalf("procNum=%d: %v", procNum, err)
		}
		if got := insertTracker.insertingAt(int(procNum & 7)); got != lsnIdle {
			t.Fatalf("procNum=%d stripe %d not idle: %d", procNum, procNum&7, got)
		}
	}

	// Final reservation count: 8 records × 16 bytes = 128 bytes.
	curr, _ := posTracker.load()
	if curr != 128 {
		t.Fatalf("posTracker.curr=%d, want 128", curr)
	}
}

func TestStripeAppendCrossSegmentEmitsPadAndChainsPrev(t *testing.T) {
	t.Parallel()
	// segSize=200 + 80B records → r1[0,80), r2[80,160), r3 would straddle
	// [160, 240) (crosses boundary 200). Pad fills [160, 200) = 40 bytes
	// (well above the 24-byte minimum), r3 lands at 200 with prev=160
	// (the pad record's start LSN).
	const segSize uint64 = 200
	locks, posTracker, insertTracker, walBuf, memRing, _ := makeStripeAppendFixtureWithCross(t, /*cap*/ 1024, segSize)

	r1 := bytes.Repeat([]byte{0x11}, 80)
	s1, p1, err := stripeAppend(locks, posTracker, insertTracker, walBuf, memRing, 0, r1)
	if err != nil || s1 != 0 || p1 != 0 {
		t.Fatalf("r1: start=%d prev=%d err=%v", s1, p1, err)
	}
	r2 := bytes.Repeat([]byte{0x22}, 80)
	s2, p2, err := stripeAppend(locks, posTracker, insertTracker, walBuf, memRing, 0, r2)
	if err != nil || s2 != 80 || p2 != 0 {
		t.Fatalf("r2: start=%d prev=%d err=%v", s2, p2, err)
	}
	r3 := bytes.Repeat([]byte{0x33}, 80)
	s3, p3, err := stripeAppend(locks, posTracker, insertTracker, walBuf, memRing, 0, r3)
	if err != nil {
		t.Fatalf("r3: %v", err)
	}
	if s3 != segSize || p3 != 160 {
		t.Fatalf("r3 cross-segment: start=%d prev=%d, want %d/160", s3, p3, segSize)
	}

	// Both rings hold all bytes — including the 40-byte pad at [160, 200).
	walBuf.publishTail(int64(segSize) + 80)
	memRing.PublishUpTo(int64(segSize) + 80)

	// Pad bytes form an XLOG_NOOP record at LSN 160 with TotLen=40 and
	// xl_prev=80 (the prev pointer at the moment of crossing — equal to
	// r2's start, since r2 was the most-recent reservation that
	// completed before r3 attempted to cross).
	pad := make([]byte, 40)
	if n := walBuf.readAt(160, pad); n != 40 {
		t.Fatalf("walBuf.readAt pad: n=%d", n)
	}
	h, derr := DecodeXLogRecordHeader(pad[:SizeOfXLogRecord])
	if derr != nil {
		t.Fatalf("DecodeXLogRecordHeader: %v", derr)
	}
	if h.TotLen != 40 || h.Rmid != RmgrXLog || h.Info != xlogInfoNoop {
		t.Fatalf("pad header: TotLen=%d Rmid=%d Info=%#x", h.TotLen, h.Rmid, h.Info)
	}
	if h.Prev != 80 {
		t.Fatalf("pad prev=%d, want 80 (= r2.start, the prev pointer at crossing time)", h.Prev)
	}

	// r3's bytes land at [200, 280).
	r3Out := make([]byte, 80)
	if n := walBuf.readAt(int64(segSize), r3Out); n != 80 || !bytes.Equal(r3Out, r3) {
		t.Fatalf("walBuf.readAt r3: n=%d match=%t", n, bytes.Equal(r3Out, r3))
	}
}

func TestStripeAppendConcurrentDisjointStripesProgressInParallel(t *testing.T) {
	t.Parallel()
	locks, posTracker, insertTracker, walBuf, memRing, publisher := makeStripeAppendFixture(t, 1<<20)

	const (
		numStripes     = appendLockStripes
		recordsPerProc = 200
		recordSize     = 16
	)
	total := int64(numStripes * recordsPerProc * recordSize)

	var wg sync.WaitGroup
	starts := make([][]uint64, numStripes)
	for i := range starts {
		starts[i] = make([]uint64, 0, recordsPerProc)
	}
	var startMu sync.Mutex
	for procNum := int32(0); procNum < numStripes; procNum++ {
		wg.Add(1)
		go func(pn int32) {
			defer wg.Done()
			local := make([]uint64, 0, recordsPerProc)
			payload := bytes.Repeat([]byte{byte(pn + 1)}, recordSize)
			for i := 0; i < recordsPerProc; i++ {
				start, _, err := stripeAppend(locks, posTracker, insertTracker, walBuf, memRing, pn, payload)
				if err != nil {
					t.Errorf("procNum=%d i=%d: %v", pn, i, err)
					return
				}
				local = append(local, start)
			}
			startMu.Lock()
			starts[pn] = local
			startMu.Unlock()
		}(procNum)
	}
	wg.Wait()

	// All stripes must be idle.
	for s := 0; s < numStripes; s++ {
		if got := insertTracker.insertingAt(s); got != lsnIdle {
			t.Fatalf("stripe %d not idle: %d", s, got)
		}
	}

	// Collect every reservation start; expect a perfect permutation of
	// {0, 16, 32, …, total-16}.
	all := make([]uint64, 0, numStripes*recordsPerProc)
	for _, ss := range starts {
		all = append(all, ss...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	if int64(len(all))*recordSize != total {
		t.Fatalf("len(all)=%d × %d ≠ total %d", len(all), recordSize, total)
	}
	for i, v := range all {
		want := uint64(i) * recordSize
		if v != want {
			t.Fatalf("starts[%d]=%d, want %d", i, v, want)
		}
	}

	// Drain publication releases all bytes; the per-stripe payload byte
	// landed at every per-stripe start LSN.
	publishVisibility(publisher, walBuf, memRing, insertTracker, int64(total))
	for procNum := int32(0); procNum < numStripes; procNum++ {
		payload := byte(procNum + 1)
		for _, start := range starts[procNum] {
			out := make([]byte, recordSize)
			if n := walBuf.readAt(int64(start), out); n != recordSize {
				t.Fatalf("walBuf.readAt start=%d: n=%d", start, n)
			}
			for j, b := range out {
				if b != payload {
					t.Fatalf("walBuf at start=%d byte %d: %#x, want %#x", start, j, b, payload)
				}
			}
		}
	}
}

func TestStripeAppendConcurrentSameStripeSerialise(t *testing.T) {
	t.Parallel()
	locks, posTracker, insertTracker, walBuf, memRing, _ := makeStripeAppendFixture(t, 1<<20)

	// procNum 3 and procNum 11 hash to the same stripe (3 & 7 == 11 & 7
	// == 3). They must serialise: peak in-CS concurrency is 1, observed
	// via an atomic gate that increments on stripe-lock acquire and
	// decrements on release.
	const sameStripeProc1, sameStripeProc2 int32 = 3, 11
	if stripeForProcNum(sameStripeProc1) != stripeForProcNum(sameStripeProc2) {
		t.Fatalf("test assumes procNum 3 and 11 hash to same stripe")
	}

	// Peak observation is at the user level (Outside the composer). We
	// can't easily probe in-CS concurrency without invasive hooks, but
	// we can still pin (a) all records land correctly and (b) every
	// reservation chains via the xl_prev field. The latter is the
	// definitional same-stripe-serialisation contract (concurrent
	// writers on different stripes do NOT chain).
	const iters = 500
	var wg sync.WaitGroup
	for _, pn := range []int32{sameStripeProc1, sameStripeProc2} {
		wg.Add(1)
		go func(p int32) {
			defer wg.Done()
			payload := bytes.Repeat([]byte{byte(p)}, 16)
			for i := 0; i < iters; i++ {
				if _, _, err := stripeAppend(locks, posTracker, insertTracker, walBuf, memRing, p, payload); err != nil {
					t.Errorf("procNum=%d i=%d: %v", p, i, err)
					return
				}
			}
		}(pn)
	}
	wg.Wait()

	curr, _ := posTracker.load()
	if curr != uint64(2*iters*16) {
		t.Fatalf("posTracker.curr=%d, want %d", curr, 2*iters*16)
	}
	if got := insertTracker.lowestActiveLSN(); got != lsnNoActive {
		t.Fatalf("insertionTracker not idle after parallel same-stripe: %d", got)
	}
}

func TestStripeAppendConcurrentDrainConsistency(t *testing.T) {
	t.Parallel()
	locks, posTracker, insertTracker, walBuf, memRing, publisher := makeStripeAppendFixture(t, 1<<20)

	const (
		numProcs    = 16 // 16 procs → 8 stripes × 2 producers each
		recordsEach = 200
		recordSize  = 16
	)
	total := int64(numProcs * recordsEach * recordSize)

	done := make(chan struct{})
	ready := make(chan struct{})
	var drainHits atomic.Int64

	// Drain-style goroutine continuously advances publication via
	// publishVisibility — must never let the safe tail leak past an
	// in-flight reservation (pre-reserve race closure validation).
	go func() {
		first := true
		for {
			select {
			case <-done:
				return
			default:
			}
			curr, _ := posTracker.load()
			publishVisibility(publisher, walBuf, memRing, insertTracker, int64(curr))
			drainHits.Add(1)
			if first {
				// Signal readiness only once the goroutine has actually been
				// scheduled and completed an iteration — under host CPU
				// contention (nightly batch's parallel race/testport/pgbench
				// lanes) this goroutine can otherwise starve entirely before
				// wg.Wait() returns, making drainHits==0 a scheduling flake
				// rather than a real drain-safety failure.
				first = false
				close(ready)
			}
		}
	}()
	<-ready

	var wg sync.WaitGroup
	for procNum := int32(0); procNum < numProcs; procNum++ {
		wg.Add(1)
		go func(pn int32) {
			defer wg.Done()
			payload := bytes.Repeat([]byte{byte(pn + 1)}, recordSize)
			for i := 0; i < recordsEach; i++ {
				if _, _, err := stripeAppend(locks, posTracker, insertTracker, walBuf, memRing, pn, payload); err != nil {
					t.Errorf("procNum=%d i=%d: %v", pn, i, err)
					return
				}
			}
		}(procNum)
	}
	wg.Wait()
	close(done)

	// Final publication brings safe tail to total.
	published := publishVisibility(publisher, walBuf, memRing, insertTracker, total)
	if published != total {
		t.Fatalf("final publish: %d, want %d", published, total)
	}
	if walBuf.tail.Load() != total {
		t.Fatalf("walBuf.tail=%d, want %d", walBuf.tail.Load(), total)
	}
	if drainHits.Load() == 0 {
		t.Fatalf("drain goroutine never ran")
	}
}

func TestStripeAppendWatchdog(t *testing.T) {
	t.Parallel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		t.Run("inner", TestStripeAppendConcurrentDrainConsistency)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("TestStripeAppendConcurrentDrainConsistency exceeded 5-second watchdog")
	}
}

// _ guards against unused-package warnings if the helpers are
// trimmed.
var _ = fmt.Sprintf
