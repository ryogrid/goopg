package xlog

import (
	"bytes"
	"encoding/binary"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestStripeAppendBuildHappyPathReceivesPrev(t *testing.T) {
	t.Parallel()
	locks, posTracker, insertTracker, walBuf, memRing, publisher := makeStripeAppendFixture(t, 4096)

	// First record: prev is 0 by initial state.
	const sz1 = 24
	var gotPrev1 uint64
	gotPrev1 = ^uint64(0) // sentinel: ensure closure was actually called
	start1, prev1, err := stripeAppendBuild(locks, posTracker, insertTracker, walBuf, memRing, /*procNum*/ 0, sz1, func(p uint64) ([]byte, error) {
		gotPrev1 = p
		rec := make([]byte, sz1)
		binary.LittleEndian.PutUint64(rec[0:8], p)
		return rec, nil
	})
	if err != nil {
		t.Fatalf("stripeAppendBuild #1: %v", err)
	}
	if start1 != 0 || prev1 != 0 {
		t.Fatalf("reservation #1: start=%d prev=%d, want 0/0", start1, prev1)
	}
	if gotPrev1 != 0 {
		t.Fatalf("build closure #1 got prev=%d, want 0", gotPrev1)
	}

	// Second record: prev must equal start1 (the immediately preceding
	// record's start LSN, per insertPosTracker's joint-atomic contract).
	const sz2 = 32
	var gotPrev2 uint64
	gotPrev2 = ^uint64(0)
	start2, prev2, err := stripeAppendBuild(locks, posTracker, insertTracker, walBuf, memRing, 0, sz2, func(p uint64) ([]byte, error) {
		gotPrev2 = p
		rec := make([]byte, sz2)
		binary.LittleEndian.PutUint64(rec[0:8], p)
		return rec, nil
	})
	if err != nil {
		t.Fatalf("stripeAppendBuild #2: %v", err)
	}
	if start2 != sz1 || prev2 != start1 {
		t.Fatalf("reservation #2: start=%d prev=%d, want %d/%d", start2, prev2, sz1, start1)
	}
	if gotPrev2 != start1 {
		t.Fatalf("build closure #2 got prev=%d, want %d", gotPrev2, start1)
	}

	// Publication makes the encoded bytes visible; verify the prev
	// stamped into the record matches what the closure was passed.
	upper := int64(sz1 + sz2)
	if got := publishVisibility(publisher, walBuf, memRing, insertTracker, upper); got != upper {
		t.Fatalf("publishVisibility: got=%d want=%d", got, upper)
	}
	out := make([]byte, sz2)
	if n := walBuf.readAt(int64(start2), out); n != sz2 {
		t.Fatalf("walBuf.readAt: n=%d want=%d", n, sz2)
	}
	gotStamped := binary.LittleEndian.Uint64(out[0:8])
	if gotStamped != start1 {
		t.Fatalf("record #2 xl_prev=%d, want %d", gotStamped, start1)
	}
}

func TestStripeAppendBuildNilLocksReturnsError(t *testing.T) {
	t.Parallel()
	_, posTracker, insertTracker, walBuf, memRing, _ := makeStripeAppendFixture(t, 4096)
	_, _, err := stripeAppendBuild(nil, posTracker, insertTracker, walBuf, memRing, 0, 16, func(uint64) ([]byte, error) { return make([]byte, 16), nil })
	if !errors.Is(err, errStripeAppendNilLocks) {
		t.Fatalf("nil locks: err=%v, want errStripeAppendNilLocks", err)
	}
}

func TestStripeAppendBuildNilPosTrackerReturnsError(t *testing.T) {
	t.Parallel()
	locks, _, insertTracker, walBuf, memRing, _ := makeStripeAppendFixture(t, 4096)
	_, _, err := stripeAppendBuild(locks, nil, insertTracker, walBuf, memRing, 0, 16, func(uint64) ([]byte, error) { return make([]byte, 16), nil })
	if !errors.Is(err, errStripeAppendNilPosTracker) {
		t.Fatalf("nil posTracker: err=%v, want errStripeAppendNilPosTracker", err)
	}
}

func TestStripeAppendBuildNilInsertTrackerReturnsError(t *testing.T) {
	t.Parallel()
	locks, posTracker, _, walBuf, memRing, _ := makeStripeAppendFixture(t, 4096)
	_, _, err := stripeAppendBuild(locks, posTracker, nil, walBuf, memRing, 0, 16, func(uint64) ([]byte, error) { return make([]byte, 16), nil })
	if !errors.Is(err, errStripeAppendNilInsertTracker) {
		t.Fatalf("nil insertTracker: err=%v, want errStripeAppendNilInsertTracker", err)
	}
}

func TestStripeAppendBuildNilBuildReturnsError(t *testing.T) {
	t.Parallel()
	locks, posTracker, insertTracker, walBuf, memRing, _ := makeStripeAppendFixture(t, 4096)
	_, _, err := stripeAppendBuild(locks, posTracker, insertTracker, walBuf, memRing, 0, 16, nil)
	if !errors.Is(err, errStripeAppendNilBuild) {
		t.Fatalf("nil build: err=%v, want errStripeAppendNilBuild", err)
	}
}

func TestStripeAppendBuildZeroSizeReturnsError(t *testing.T) {
	t.Parallel()
	locks, posTracker, insertTracker, walBuf, memRing, _ := makeStripeAppendFixture(t, 4096)
	for _, sz := range []int{0, -1} {
		_, _, err := stripeAppendBuild(locks, posTracker, insertTracker, walBuf, memRing, 0, sz, func(uint64) ([]byte, error) { return nil, nil })
		if !errors.Is(err, errStripeAppendEmptyRecord) {
			t.Fatalf("size=%d: err=%v, want errStripeAppendEmptyRecord", sz, err)
		}
	}
}

func TestStripeAppendBuildBuildErrorPropagatesAndClearsStripe(t *testing.T) {
	t.Parallel()
	locks, posTracker, insertTracker, walBuf, memRing, _ := makeStripeAppendFixture(t, 4096)
	sentinel := errors.New("encoder boom")
	_, _, err := stripeAppendBuild(locks, posTracker, insertTracker, walBuf, memRing, 0, 16, func(uint64) ([]byte, error) {
		return nil, sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("build error: err=%v, want sentinel", err)
	}
	// END marker fires regardless: stripe slot back to idle.
	if got := insertTracker.lowestActiveLSN(); got != lsnNoActive {
		t.Fatalf("insertionTracker not idle after build error: %d", got)
	}
}

func TestStripeAppendBuildSizeMismatchReturnsError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		got  int
	}{
		{"under", 15},
		{"over", 17},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			locks, posTracker, insertTracker, walBuf, memRing, _ := makeStripeAppendFixture(t, 4096)
			_, _, err := stripeAppendBuild(locks, posTracker, insertTracker, walBuf, memRing, 0, 16, func(uint64) ([]byte, error) {
				return make([]byte, c.got), nil
			})
			if !errors.Is(err, errStripeAppendBuildSizeMismatch) {
				t.Fatalf("size mismatch: err=%v, want errStripeAppendBuildSizeMismatch", err)
			}
			if got := insertTracker.lowestActiveLSN(); got != lsnNoActive {
				t.Fatalf("insertionTracker not idle after size mismatch: %d", got)
			}
		})
	}
}

func TestStripeAppendBuildNilWalBufStillWritesMemRing(t *testing.T) {
	t.Parallel()
	locks, posTracker, insertTracker, _, memRing, publisher := makeStripeAppendFixture(t, 4096)
	const sz = 24
	start, _, err := stripeAppendBuild(locks, posTracker, insertTracker, nil, memRing, 0, sz, func(prev uint64) ([]byte, error) {
		rec := make([]byte, sz)
		binary.LittleEndian.PutUint64(rec[0:8], prev)
		copy(rec[8:], []byte("only-memring-build!!"))
		return rec, nil
	})
	if err != nil {
		t.Fatalf("stripeAppendBuild: %v", err)
	}
	if got := publishVisibility(publisher, nil, memRing, insertTracker, int64(sz)); got != int64(sz) {
		t.Fatalf("publishVisibility: got=%d want=%d", got, sz)
	}
	out := make([]byte, sz)
	if n, ok := memRing.ReadAt(int64(start), out); !ok || n != sz {
		t.Fatalf("memRing.ReadAt: n=%d ok=%t", n, ok)
	}
}

func TestStripeAppendBuildNilMemRingStillWritesWalBuf(t *testing.T) {
	t.Parallel()
	locks, posTracker, insertTracker, walBuf, _, publisher := makeStripeAppendFixture(t, 4096)
	const sz = 24
	start, _, err := stripeAppendBuild(locks, posTracker, insertTracker, walBuf, nil, 0, sz, func(prev uint64) ([]byte, error) {
		rec := make([]byte, sz)
		binary.LittleEndian.PutUint64(rec[0:8], prev)
		copy(rec[8:], []byte("only-walbuf-build!!!"))
		return rec, nil
	})
	if err != nil {
		t.Fatalf("stripeAppendBuild: %v", err)
	}
	if got := publishVisibility(publisher, walBuf, nil, insertTracker, int64(sz)); got != int64(sz) {
		t.Fatalf("publishVisibility: got=%d want=%d", got, sz)
	}
	out := make([]byte, sz)
	if n := walBuf.readAt(int64(start), out); n != sz {
		t.Fatalf("walBuf.readAt: n=%d want=%d", n, sz)
	}
}

func TestStripeAppendBuildCrossSegmentChainsPrevAcrossPad(t *testing.T) {
	t.Parallel()
	// segSize=128, two records of 80 bytes each → second reservation
	// crosses the 128-byte boundary and triggers emitSegmentPad.
	locks, posTracker, insertTracker, walBuf, memRing, publisher := makeStripeAppendFixtureWithCross(t, 4096, 128)

	const sz = 80
	// Record #1 at LSN 0.
	s1, p1, err := stripeAppendBuild(locks, posTracker, insertTracker, walBuf, memRing, 0, sz, func(prev uint64) ([]byte, error) {
		rec := make([]byte, sz)
		binary.LittleEndian.PutUint64(rec[0:8], prev)
		return rec, nil
	})
	if err != nil {
		t.Fatalf("rec#1: %v", err)
	}
	if s1 != 0 || p1 != 0 {
		t.Fatalf("rec#1: start=%d prev=%d, want 0/0", s1, p1)
	}

	// Record #2: 80 + 80 = 160 > segSize 128 → cross-segment hop. The
	// build closure must receive the post-reservation prev, which is
	// the *pad record's* start LSN (the pad is the immediately
	// preceding record once it lands at the gap).
	var capturedPrev uint64
	s2, p2, err := stripeAppendBuild(locks, posTracker, insertTracker, walBuf, memRing, 0, sz, func(prev uint64) ([]byte, error) {
		capturedPrev = prev
		rec := make([]byte, sz)
		binary.LittleEndian.PutUint64(rec[0:8], prev)
		return rec, nil
	})
	if err != nil {
		t.Fatalf("rec#2: %v", err)
	}
	// Boundary is 128; the pad fills [80, 128) and rec#2 lands at 128.
	if s2 != 128 {
		t.Fatalf("rec#2 start=%d, want 128 (post-boundary)", s2)
	}
	// The pad record's start is 80; rec#2's xl_prev should chain to
	// the pad, so prev2 == 80 (the pad's start LSN — current behaviour
	// of insertPosTracker's cross-segment slow path).
	if p2 != 80 {
		t.Fatalf("rec#2 prev=%d, want 80 (chains to pad)", p2)
	}
	if capturedPrev != 80 {
		t.Fatalf("build closure rec#2 received prev=%d, want 80", capturedPrev)
	}

	// Publish to the highest written byte (sz + pad + sz = 80+48+80=208).
	upper := int64(s2 + sz)
	if got := publishVisibility(publisher, walBuf, memRing, insertTracker, upper); got != upper {
		t.Fatalf("publishVisibility: got=%d want=%d", got, upper)
	}
	// Confirm rec#2 was stamped with prev=80.
	out := make([]byte, sz)
	if n := walBuf.readAt(int64(s2), out); n != sz {
		t.Fatalf("walBuf.readAt rec#2: n=%d", n)
	}
	if gotStamped := binary.LittleEndian.Uint64(out[0:8]); gotStamped != 80 {
		t.Fatalf("rec#2 stamped xl_prev=%d, want 80", gotStamped)
	}
}

func TestStripeAppendBuildConcurrentDisjointStripesProgressInParallel(t *testing.T) {
	t.Parallel()
	locks, posTracker, insertTracker, walBuf, memRing, _ := makeStripeAppendFixture(t, 1<<16)

	const procs = 8
	const recordsPerProc = 50
	const sz = 16

	var wg sync.WaitGroup
	var totalSucc atomic.Int64
	starts := make([][]uint64, procs)
	for i := range starts {
		starts[i] = make([]uint64, 0, recordsPerProc)
	}
	var startsMu sync.Mutex

	for p := 0; p < procs; p++ {
		wg.Add(1)
		go func(pn int32) {
			defer wg.Done()
			for i := 0; i < recordsPerProc; i++ {
				start, _, err := stripeAppendBuild(locks, posTracker, insertTracker, walBuf, memRing, pn, sz, func(prev uint64) ([]byte, error) {
					rec := make([]byte, sz)
					binary.LittleEndian.PutUint64(rec[0:8], prev)
					binary.LittleEndian.PutUint64(rec[8:16], uint64(pn))
					return rec, nil
				})
				if err != nil {
					t.Errorf("pn=%d i=%d: %v", pn, i, err)
					return
				}
				startsMu.Lock()
				starts[pn] = append(starts[pn], start)
				startsMu.Unlock()
				totalSucc.Add(1)
			}
		}(int32(p))
	}
	wg.Wait()

	if int(totalSucc.Load()) != procs*recordsPerProc {
		t.Fatalf("succ count: got=%d want=%d", totalSucc.Load(), procs*recordsPerProc)
	}
	// All reservations should be disjoint and fill [0, procs*recordsPerProc*sz).
	all := make([]uint64, 0, procs*recordsPerProc)
	for _, s := range starts {
		all = append(all, s...)
	}
	seen := make(map[uint64]bool, len(all))
	for _, s := range all {
		if seen[s] {
			t.Fatalf("duplicate start LSN %d", s)
		}
		seen[s] = true
	}
	if got := insertTracker.lowestActiveLSN(); got != lsnNoActive {
		t.Fatalf("insertionTracker not idle after concurrent runs: %d", got)
	}
}

func TestStripeWriterCoreAppendBuiltDelegatesToStripeAppendBuild(t *testing.T) {
	t.Parallel()
	walBuf := newWALBuffer(4096)
	walBuf.reset(0)
	memRing := NewMemRing(4096)
	core := newStripeWriterCore(1<<20, 0, 0, walBuf, memRing, padLayout{})

	const sz = 24
	var receivedPrev uint64
	receivedPrev = ^uint64(0)
	start, prev, err := core.AppendBuilt(/*procNum*/ 2, sz, func(p uint64) ([]byte, error) {
		receivedPrev = p
		rec := make([]byte, sz)
		binary.LittleEndian.PutUint64(rec[0:8], p)
		copy(rec[8:], []byte("core-appendbuilt"))
		return rec, nil
	})
	if err != nil {
		t.Fatalf("core.AppendBuilt: %v", err)
	}
	if start != 0 || prev != 0 {
		t.Fatalf("first reservation: start=%d prev=%d", start, prev)
	}
	if receivedPrev != 0 {
		t.Fatalf("build closure got prev=%d, want 0", receivedPrev)
	}
	// Publication via core.PublishUpTo makes the bytes visible.
	if got := core.PublishUpTo(sz); got != sz {
		t.Fatalf("core.PublishUpTo: got=%d want=%d", got, sz)
	}
	out := make([]byte, sz)
	if n := walBuf.readAt(int64(start), out); n != sz {
		t.Fatalf("walBuf.readAt: n=%d", n)
	}
	if !bytes.HasSuffix(out, []byte("core-appendbuilt")) {
		t.Fatalf("record body mismatch: %q", out)
	}
}

func TestStripeWriterCoreAppendBuiltNilReceiverReturnsError(t *testing.T) {
	t.Parallel()
	var core *stripeWriterCore
	_, _, err := core.AppendBuilt(0, 16, func(uint64) ([]byte, error) { return make([]byte, 16), nil })
	if !errors.Is(err, errStripeWriterCoreNil) {
		t.Fatalf("nil receiver: err=%v, want errStripeWriterCoreNil", err)
	}
}
