package xlog

import (
	"encoding/binary"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"
)

// emittedBuild is a test-only helper that produces a page-headered
// emission slice of exactly `total` bytes: zero bytes for any leading
// page header (the composer's regression tests don't care what the
// header content is — only that the size matches `total` and that
// `leading` bytes were reserved for it) followed by a `recordLen`-byte
// body whose first 8 bytes stamp `prev` so callers can assert xl_prev
// linkage. Returns nil and a sentinel mismatch error if recordLen is
// inconsistent with total-leading (defensive — tests pass matched
// values).
func emittedBuild(prev uint64, total, leading, recordLen int) []byte {
	if total-leading != recordLen {
		return nil
	}
	out := make([]byte, total)
	// Leave the leading `leading` bytes as zero — page-header bytes
	// are not modelled by the composer; the slice B call-site rewrite
	// will use emitWithPageHeaders to stamp real PHD bytes there.
	if recordLen >= 8 {
		binary.LittleEndian.PutUint64(out[leading:leading+8], prev)
	}
	return out
}

func TestStripeAppendBuiltEmittedHappyPathReceivesPrevAndTotal(t *testing.T) {
	t.Parallel()
	locks, posTracker, insertTracker, walBuf, memRing, publisher := makeStripeAppendFixture(t, 4096)

	const recordLen1 = 24
	var gotStart1, gotPrev1, gotTotal1, gotLeading1 uint64
	gotStart1 = ^uint64(0)
	gotPrev1 = ^uint64(0)
	start1, prev1, total1, leading1, err := stripeAppendBuiltEmitted(
		locks, posTracker, insertTracker, walBuf, memRing,
		/*procNum*/ 0, recordLen1,
		func(s, p uint64, total, leading int) ([]byte, error) {
			gotStart1 = s
			gotPrev1 = p
			gotTotal1 = uint64(total)
			gotLeading1 = uint64(leading)
			return emittedBuild(p, total, leading, recordLen1), nil
		},
	)
	if err != nil {
		t.Fatalf("stripeAppendBuiltEmitted #1: %v", err)
	}
	// Default fixture starts at curr=0, prev=0; segSize=1<<20.
	// startCandidate=0 is page-aligned AND segment-aligned → leading=long PHD.
	if start1 != 0 || prev1 != 0 {
		t.Fatalf("reservation #1: start=%d prev=%d, want 0/0", start1, prev1)
	}
	if leading1 != SizeOfXLogLongPHD {
		t.Fatalf("leading #1 = %d, want SizeOfXLogLongPHD=%d (start=0 is segment-aligned)",
			leading1, SizeOfXLogLongPHD)
	}
	if total1 != SizeOfXLogLongPHD+recordLen1 {
		t.Fatalf("total #1 = %d, want %d", total1, SizeOfXLogLongPHD+recordLen1)
	}
	if gotStart1 != start1 || gotPrev1 != 0 || gotTotal1 != uint64(total1) || gotLeading1 != uint64(leading1) {
		t.Fatalf("build closure #1 args: start=%d prev=%d total=%d leading=%d, want %d/0/%d/%d",
			gotStart1, gotPrev1, gotTotal1, gotLeading1, start1, total1, leading1)
	}

	// Second record: posTracker.curr is now total1; mid-page so
	// leading=0, total=recordLen2.
	const recordLen2 = 16
	var gotStart2 uint64
	gotStart2 = ^uint64(0)
	start2, prev2, total2, leading2, err := stripeAppendBuiltEmitted(
		locks, posTracker, insertTracker, walBuf, memRing, 0, recordLen2,
		func(s, p uint64, total, leading int) ([]byte, error) {
			gotStart2 = s
			return emittedBuild(p, total, leading, recordLen2), nil
		},
	)
	if err != nil {
		t.Fatalf("stripeAppendBuiltEmitted #2: %v", err)
	}
	// prev2 = record-CONTENT start of #1 = start1 + leading1 (SizeOfXLogLongPHD,
	// because #1 lands at 0 which is segment-aligned).
	if start2 != uint64(total1) || prev2 != start1+uint64(SizeOfXLogLongPHD) {
		t.Fatalf("reservation #2: start=%d prev=%d, want %d/%d",
			start2, prev2, total1, start1+uint64(SizeOfXLogLongPHD))
	}
	if leading2 != 0 || total2 != recordLen2 {
		t.Fatalf("size #2: total=%d leading=%d, want %d/0", total2, leading2, recordLen2)
	}
	if gotStart2 != start2 {
		t.Fatalf("build closure #2 start arg: got=%d, want %d", gotStart2, start2)
	}

	// END marker landed for both inserts.
	if got := insertTracker.lowestActiveLSN(); got != lsnNoActive {
		t.Fatalf("insertionTracker not idle: %d", got)
	}

	// Publish and read back rec2; the stamped prev must equal start1.
	upper := int64(total1 + total2)
	if got := publishVisibility(publisher, walBuf, memRing, insertTracker, upper); got != upper {
		t.Fatalf("publishVisibility: got=%d want=%d", got, upper)
	}
	out := make([]byte, total2)
	if n := walBuf.readAt(int64(start2), out); n != total2 {
		t.Fatalf("walBuf.readAt: n=%d want=%d", n, total2)
	}
	gotStamped := binary.LittleEndian.Uint64(out[leading2 : leading2+8])
	wantXLPrev := start1 + uint64(SizeOfXLogLongPHD) // content start of #1
	if gotStamped != wantXLPrev {
		t.Fatalf("record #2 xl_prev=%d, want %d (content start of #1)", gotStamped, wantXLPrev)
	}
}

func TestStripeAppendBuiltEmittedNilLocksReturnsError(t *testing.T) {
	t.Parallel()
	_, posTracker, insertTracker, walBuf, memRing, _ := makeStripeAppendFixture(t, 4096)
	_, _, _, _, err := stripeAppendBuiltEmitted(nil, posTracker, insertTracker, walBuf, memRing, 0, 16,
		func(uint64, uint64, int, int) ([]byte, error) { return make([]byte, 56), nil })
	if !errors.Is(err, errStripeAppendNilLocks) {
		t.Fatalf("err=%v, want errStripeAppendNilLocks", err)
	}
}

func TestStripeAppendBuiltEmittedNilPosTrackerReturnsError(t *testing.T) {
	t.Parallel()
	locks, _, insertTracker, walBuf, memRing, _ := makeStripeAppendFixture(t, 4096)
	_, _, _, _, err := stripeAppendBuiltEmitted(locks, nil, insertTracker, walBuf, memRing, 0, 16,
		func(uint64, uint64, int, int) ([]byte, error) { return make([]byte, 56), nil })
	if !errors.Is(err, errStripeAppendNilPosTracker) {
		t.Fatalf("err=%v, want errStripeAppendNilPosTracker", err)
	}
}

func TestStripeAppendBuiltEmittedNilInsertTrackerReturnsError(t *testing.T) {
	t.Parallel()
	locks, posTracker, _, walBuf, memRing, _ := makeStripeAppendFixture(t, 4096)
	_, _, _, _, err := stripeAppendBuiltEmitted(locks, posTracker, nil, walBuf, memRing, 0, 16,
		func(uint64, uint64, int, int) ([]byte, error) { return make([]byte, 56), nil })
	if !errors.Is(err, errStripeAppendNilInsertTracker) {
		t.Fatalf("err=%v, want errStripeAppendNilInsertTracker", err)
	}
}

func TestStripeAppendBuiltEmittedNilBuildReturnsError(t *testing.T) {
	t.Parallel()
	locks, posTracker, insertTracker, walBuf, memRing, _ := makeStripeAppendFixture(t, 4096)
	_, _, _, _, err := stripeAppendBuiltEmitted(locks, posTracker, insertTracker, walBuf, memRing, 0, 16, nil)
	if !errors.Is(err, errStripeAppendNilBuild) {
		t.Fatalf("err=%v, want errStripeAppendNilBuild", err)
	}
}

func TestStripeAppendBuiltEmittedEmptyRecordReturnsError(t *testing.T) {
	t.Parallel()
	locks, posTracker, insertTracker, walBuf, memRing, _ := makeStripeAppendFixture(t, 4096)
	for _, n := range []int{0, -1, -100} {
		_, _, _, _, err := stripeAppendBuiltEmitted(locks, posTracker, insertTracker, walBuf, memRing, 0, n,
			func(uint64, uint64, int, int) ([]byte, error) { return nil, nil })
		if !errors.Is(err, errStripeAppendEmptyRecord) {
			t.Fatalf("recordLen=%d: err=%v, want errStripeAppendEmptyRecord", n, err)
		}
	}
	// posTracker must be untouched on rejection.
	curr, prev := posTracker.load()
	if curr != 0 || prev != 0 {
		t.Fatalf("posTracker mutated after rejected calls: curr=%d prev=%d", curr, prev)
	}
}

func TestStripeAppendBuiltEmittedBuildErrorPropagatesAndClearsStripe(t *testing.T) {
	t.Parallel()
	locks, posTracker, insertTracker, walBuf, memRing, _ := makeStripeAppendFixture(t, 4096)
	sentinel := errors.New("encoder explosion")
	_, _, _, _, err := stripeAppendBuiltEmitted(locks, posTracker, insertTracker, walBuf, memRing, 3, 16,
		func(uint64, uint64, int, int) ([]byte, error) { return nil, sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("err=%v, want sentinel", err)
	}
	// END marker fired — drain must not be frozen.
	if got := insertTracker.insertingAt(stripeForProcNum(3)); got != lsnIdle {
		t.Fatalf("stripe slot not idle after build error: %d", got)
	}
	// Reservation still consumed (peer stripes may have advanced past).
	curr, _ := posTracker.load()
	if curr == 0 {
		t.Fatalf("curr did not advance after build error: %d", curr)
	}
}

func TestStripeAppendBuiltEmittedSizeMismatchReturnsError(t *testing.T) {
	t.Parallel()
	locks, posTracker, insertTracker, walBuf, memRing, _ := makeStripeAppendFixture(t, 4096)
	const recordLen = 16
	// First call: under-size.
	_, _, _, _, err := stripeAppendBuiltEmitted(locks, posTracker, insertTracker, walBuf, memRing, 0, recordLen,
		func(_, _ uint64, total, _ int) ([]byte, error) { return make([]byte, total-1), nil })
	if !errors.Is(err, errStripeAppendBuildSizeMismatch) {
		t.Fatalf("under-size: err=%v, want errStripeAppendBuildSizeMismatch", err)
	}
	if got := insertTracker.insertingAt(stripeForProcNum(0)); got != lsnIdle {
		t.Fatalf("stripe slot not idle after under-size: %d", got)
	}
	// Second call: over-size on a different stripe (so the first
	// reservation's published curr does not interfere).
	_, _, _, _, err = stripeAppendBuiltEmitted(locks, posTracker, insertTracker, walBuf, memRing, 1, recordLen,
		func(_, _ uint64, total, _ int) ([]byte, error) { return make([]byte, total+1), nil })
	if !errors.Is(err, errStripeAppendBuildSizeMismatch) {
		t.Fatalf("over-size: err=%v, want errStripeAppendBuildSizeMismatch", err)
	}
}

func TestStripeAppendBuiltEmittedNilWalBufStillWritesMemRing(t *testing.T) {
	t.Parallel()
	_, posTracker, insertTracker, _, memRing, publisher := makeStripeAppendFixture(t, 4096)
	locks := &appendLockSet{}
	const recordLen = 24
	start, _, total, leading, err := stripeAppendBuiltEmitted(locks, posTracker, insertTracker, /*walBuf*/ nil, memRing, 0, recordLen,
		func(_, p uint64, total, leading int) ([]byte, error) {
			return emittedBuild(p, total, leading, recordLen), nil
		})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	// Publish via memRing only.
	publisher.publishUpTo(int64(start)+int64(total), insertTracker)
	memRing.PublishUpTo(int64(start) + int64(total))
	out := make([]byte, total)
	if n, ok := memRing.ReadAt(int64(start), out); !ok || n != total {
		t.Fatalf("memRing.ReadAt n=%d ok=%v want=%d/true", n, ok, total)
	}
	// First leading bytes are page header (zero in test); body bytes
	// stamp prev=0 in the first 8 bytes after leading.
	body := out[leading:]
	gotPrev := binary.LittleEndian.Uint64(body[0:8])
	if gotPrev != 0 {
		t.Fatalf("stamped prev=%d, want 0", gotPrev)
	}
}

func TestStripeAppendBuiltEmittedNilMemRingStillWritesWalBuf(t *testing.T) {
	t.Parallel()
	locks, posTracker, insertTracker, walBuf, _, publisher := makeStripeAppendFixture(t, 4096)
	const recordLen = 24
	start, _, total, leading, err := stripeAppendBuiltEmitted(locks, posTracker, insertTracker, walBuf, /*memRing*/ nil, 0, recordLen,
		func(_, p uint64, total, leading int) ([]byte, error) {
			return emittedBuild(p, total, leading, recordLen), nil
		})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	publisher.publishUpTo(int64(start)+int64(total), insertTracker)
	walBuf.publishTail(int64(start) + int64(total))
	out := make([]byte, total)
	if n := walBuf.readAt(int64(start), out); n != total {
		t.Fatalf("walBuf.readAt n=%d want=%d", n, total)
	}
	body := out[leading:]
	gotPrev := binary.LittleEndian.Uint64(body[0:8])
	if gotPrev != 0 {
		t.Fatalf("stamped prev=%d, want 0", gotPrev)
	}
}

func TestStripeAppendBuiltEmittedCrossSegmentEmitsPadAndRePredicts(t *testing.T) {
	t.Parallel()
	// segSize = 2 pages = 16 KiB. Set curr to one page in so the next
	// boundary is XLOGBlockSize away; a 100-byte record fits in
	// segment-0 mid-page but at curr=segSize-50 it will cross.
	const segSize = uint64(2 * XLOGBlockSize)
	locks, posTracker, insertTracker, walBuf, memRing, publisher := makeStripeAppendFixtureWithCross(t, int64(segSize)*2, segSize)

	// Burn down posTracker.curr to just before the boundary so that a
	// 100-byte record + page-header(s) will cross into segment 1.
	// At pos=segSize-50, the record body straddles boundary; predict
	// will see total > boundary-curr and trigger cross-segment.
	posTracker.posMu.Lock()
	posTracker.curr = segSize - 50
	posTracker.prev = segSize - 200
	posTracker.posMu.Unlock()
	prevBeforeCross := uint64(segSize - 50)

	const recordLen = 100
	gotClosureStart := ^uint64(0)
	start, prev, total, leading, err := stripeAppendBuiltEmitted(
		locks, posTracker, insertTracker, walBuf, memRing, 0, recordLen,
		func(s, p uint64, total, leading int) ([]byte, error) {
			gotClosureStart = s
			return emittedBuild(p, total, leading, recordLen), nil
		})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	// After cross-segment the reservation lands at segSize (boundary)
	// with leading=long PHD.
	if start != segSize {
		t.Fatalf("post-cross start=%d, want %d", start, segSize)
	}
	// Build closure must observe the POST-boundary start (so
	// emitWithPageHeaders stamps the long PHD at the boundary), NOT the
	// pre-shift candidate start that triggered the crossing.
	if gotClosureStart != start {
		t.Fatalf("build closure start arg: got=%d, want %d (post-boundary)", gotClosureStart, start)
	}
	if prev != prevBeforeCross {
		t.Fatalf("prev=%d, want %d (pad's start LSN)", prev, prevBeforeCross)
	}
	if leading != SizeOfXLogLongPHD {
		t.Fatalf("leading=%d, want long PHD %d (boundary)", leading, SizeOfXLogLongPHD)
	}
	if total != SizeOfXLogLongPHD+recordLen {
		t.Fatalf("total=%d, want %d", total, SizeOfXLogLongPHD+recordLen)
	}

	// Publish and verify the pad record's start LSN can be read.
	upper := int64(start) + int64(total)
	publisher.publishUpTo(upper, insertTracker)
	walBuf.publishTail(upper)
	memRing.PublishUpTo(upper)
	// The pad lives at [segSize-50, segSize); it's a real XLOG_NOOP
	// record whose decoding we don't re-test here (foundation 12 has
	// that coverage). We do verify our record landed at start with
	// stamped prev=segSize-50 (the pad's start LSN).
	body := make([]byte, recordLen)
	if n := walBuf.readAt(int64(start)+int64(leading), body); n != recordLen {
		t.Fatalf("walBuf.readAt body: n=%d want=%d", n, recordLen)
	}
	gotPrev := binary.LittleEndian.Uint64(body[0:8])
	if gotPrev != prevBeforeCross {
		t.Fatalf("stamped prev=%d, want %d", gotPrev, prevBeforeCross)
	}
}

func TestStripeAppendBuiltEmittedConcurrentDisjointStripesProgressInParallel(t *testing.T) {
	t.Parallel()
	locks := &appendLockSet{}
	walBuf := newWALBuffer(1 << 20)
	walBuf.reset(0)
	memRing := NewMemRing(1 << 20)
	insertTracker := newInsertionTracker()
	publisher := newTailPublisher()
	posTracker := newInsertPosTracker(0, 0, 1<<24, nil)

	const stripes = 8
	const perStripe = 50
	const recordLen = 16

	var wg sync.WaitGroup
	starts := make([][]uint64, stripes)
	for s := 0; s < stripes; s++ {
		starts[s] = make([]uint64, perStripe)
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			for i := 0; i < perStripe; i++ {
				start, _, _, _, err := stripeAppendBuiltEmitted(
					locks, posTracker, insertTracker, walBuf, memRing,
					int32(s), recordLen,
					func(_, p uint64, total, leading int) ([]byte, error) {
						return emittedBuild(p, total, leading, recordLen), nil
					})
				if err != nil {
					t.Errorf("stripe %d iter %d: err=%v", s, i, err)
					return
				}
				starts[s][i] = start
			}
		}(s)
	}
	wg.Wait()

	// Every reservation must be distinct.
	seen := make(map[uint64]struct{}, stripes*perStripe)
	for s := 0; s < stripes; s++ {
		for i := 0; i < perStripe; i++ {
			if _, dup := seen[starts[s][i]]; dup {
				t.Fatalf("duplicate start LSN %d (stripe %d iter %d)", starts[s][i], s, i)
			}
			seen[starts[s][i]] = struct{}{}
		}
	}

	// Tracker must be idle.
	if got := insertTracker.lowestActiveLSN(); got != lsnNoActive {
		t.Fatalf("lowestActiveLSN=%d, want sentinel", got)
	}

	// Sort starts and assert disjoint by at least recordLen (no overlap).
	all := make([]uint64, 0, stripes*perStripe)
	for s := 0; s < stripes; s++ {
		all = append(all, starts[s]...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	for i := 1; i < len(all); i++ {
		if all[i]-all[i-1] < recordLen {
			t.Fatalf("overlapping reservations: all[%d]=%d all[%d]=%d", i-1, all[i-1], i, all[i])
		}
	}
	_ = publisher
}

func TestStripeWriterCoreAppendBuiltEmittedDelegatesToStripeAppendBuiltEmitted(t *testing.T) {
	t.Parallel()
	walBuf := newWALBuffer(4096)
	walBuf.reset(0)
	memRing := NewMemRing(4096)
	core := newStripeWriterCore(1<<20, 0, 0, walBuf, memRing, padLayout{})

	const recordLen = 24
	start, prev, total, leading, err := core.AppendBuiltEmitted(0, recordLen,
		func(_, p uint64, total, leading int) ([]byte, error) {
			return emittedBuild(p, total, leading, recordLen), nil
		})
	if err != nil {
		t.Fatalf("AppendBuiltEmitted: %v", err)
	}
	if start != 0 || prev != 0 {
		t.Fatalf("start=%d prev=%d, want 0/0", start, prev)
	}
	if leading != SizeOfXLogLongPHD || total != SizeOfXLogLongPHD+recordLen {
		t.Fatalf("size: total=%d leading=%d", total, leading)
	}
	curr, _ := core.Load()
	if curr != uint64(total) {
		t.Fatalf("core.Load curr=%d, want %d", curr, total)
	}
	// End-to-end: publication makes bytes readable.
	upper := int64(total)
	if got := core.PublishUpTo(upper); got != upper {
		t.Fatalf("PublishUpTo=%d want=%d", got, upper)
	}
	out := make([]byte, total)
	if n := walBuf.readAt(0, out); n != total {
		t.Fatalf("walBuf.readAt n=%d want=%d", n, total)
	}
	gotPrev := binary.LittleEndian.Uint64(out[leading : leading+8])
	if gotPrev != 0 {
		t.Fatalf("stamped prev=%d want 0", gotPrev)
	}
}

func TestStripeWriterCoreAppendBuiltEmittedNilReceiverReturnsError(t *testing.T) {
	t.Parallel()
	var core *stripeWriterCore
	_, _, _, _, err := core.AppendBuiltEmitted(0, 16,
		func(uint64, uint64, int, int) ([]byte, error) { return nil, nil })
	if !errors.Is(err, errStripeWriterCoreNil) {
		t.Fatalf("err=%v, want errStripeWriterCoreNil", err)
	}
}

func TestStripeAppendBuiltEmittedWatchdog(t *testing.T) {
	t.Parallel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		locks := &appendLockSet{}
		walBuf := newWALBuffer(1 << 20)
		walBuf.reset(0)
		memRing := NewMemRing(1 << 20)
		insertTracker := newInsertionTracker()
		posTracker := newInsertPosTracker(0, 0, 1<<24, nil)
		const recordLen = 16
		for i := 0; i < 200; i++ {
			_, _, _, _, err := stripeAppendBuiltEmitted(
				locks, posTracker, insertTracker, walBuf, memRing,
				int32(i%appendLockStripes), recordLen,
				func(_, p uint64, total, leading int) ([]byte, error) {
					return emittedBuild(p, total, leading, recordLen), nil
				})
			if err != nil {
				t.Errorf("err=%v at iter %d", err, i)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watchdog: stripeAppendBuiltEmitted serial loop exceeded 5s")
	}
}
