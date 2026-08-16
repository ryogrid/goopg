package xlog

import (
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestInsertPosTrackerReserveAndPublishBasic(t *testing.T) {
	t.Parallel()
	// startCurr=1 avoids the lsnIdle=0 sentinel collision; production
	// reservations are always at non-zero LSNs.
	pos := newInsertPosTracker(1, 0, 1<<20, nil)
	tr := newInsertionTracker()

	start, prev := pos.reserveAndPublish(40, 3, tr)
	if start != 1 || prev != 0 {
		t.Fatalf("first reserve: got (start=%d, prev=%d), want (1, 0)", start, prev)
	}
	// The stripe slot must reflect the just-reserved start.
	if got := tr.insertingAt(3); got != int64(start) {
		t.Fatalf("stripe 3 insertingAt = %d, want %d", got, start)
	}
	// Other stripes untouched.
	for s := 0; s < appendLockStripes; s++ {
		if s == 3 {
			continue
		}
		if got := tr.insertingAt(s); got != lsnIdle {
			t.Fatalf("stripe %d insertingAt = %d, want lsnIdle", s, got)
		}
	}
}

func TestInsertPosTrackerReserveAndPublishMultiStripe(t *testing.T) {
	t.Parallel()
	// startCurr=1 sidesteps the lsnIdle=0 sentinel collision; LSN 0
	// is the invalid sentinel throughout PG/goopg, so production
	// reservations are always at non-zero LSNs.
	pos := newInsertPosTracker(1, 0, 1<<20, nil)
	tr := newInsertionTracker()

	// Stripes 0, 4, 7 each reserve; their slots must reflect their
	// distinct start LSNs.
	s0, _ := pos.reserveAndPublish(16, 0, tr)
	s4, _ := pos.reserveAndPublish(16, 4, tr)
	s7, _ := pos.reserveAndPublish(16, 7, tr)
	if s0 != 1 || s4 != 17 || s7 != 33 {
		t.Fatalf("starts = %d/%d/%d, want 1/17/33", s0, s4, s7)
	}
	if got := tr.insertingAt(0); got != 1 {
		t.Fatalf("stripe 0 insertingAt = %d, want 1", got)
	}
	if got := tr.insertingAt(4); got != 17 {
		t.Fatalf("stripe 4 insertingAt = %d, want 17", got)
	}
	if got := tr.insertingAt(7); got != 33 {
		t.Fatalf("stripe 7 insertingAt = %d, want 33", got)
	}
	// Untouched stripes still idle.
	for _, s := range []int{1, 2, 3, 5, 6} {
		if got := tr.insertingAt(s); got != lsnIdle {
			t.Fatalf("stripe %d insertingAt = %d, want lsnIdle", s, got)
		}
	}
	// And lowestActiveLSN sees the minimum across the three populated
	// slots — stripe 0's reservation at LSN 1.
	if got := tr.lowestActiveLSN(); got != 1 {
		t.Fatalf("lowestActiveLSN = %d, want 1", got)
	}
}

func TestInsertPosTrackerReserveAndPublishCrossSegmentPublishesNewStart(t *testing.T) {
	t.Parallel()
	const segSize = uint64(100)
	var hookCalls []struct{ start, boundary, prev uint64 }
	pos := newInsertPosTracker(80, 70, segSize, func(s, b, p uint64) bool {
		hookCalls = append(hookCalls, struct{ start, boundary, prev uint64 }{s, b, p})
		return true
	})
	tr := newInsertionTracker()

	// 40-byte reservation at curr=80, segSize=100 → straddles boundary
	// at 100. Reservation lands at 100; gap [80, 100) is the pad.
	start, prev := pos.reserveAndPublish(40, 2, tr)
	if start != 100 {
		t.Fatalf("cross-segment start = %d, want 100", start)
	}
	if prev != 80 {
		t.Fatalf("cross-segment prev = %d, want 80 (pad record start)", prev)
	}
	if len(hookCalls) != 1 {
		t.Fatalf("hook fired %d times, want 1", len(hookCalls))
	}
	if h := hookCalls[0]; h.start != 80 || h.boundary != 100 || h.prev != 70 {
		t.Fatalf("hook payload = (%d, %d, %d), want (80, 100, 70)", h.start, h.boundary, h.prev)
	}
	// Critical: the stripe slot must hold the *new* start (100), not
	// the pad's start (80). The pad record is a gap-fill emitted by
	// the caller after the hook fires; it is not the reservation
	// whose bytes the stripe will write next.
	if got := tr.insertingAt(2); got != 100 {
		t.Fatalf("stripe 2 insertingAt = %d, want 100 (the post-boundary reservation, not the pad)", got)
	}
}

func TestInsertPosTrackerReserveAndPublishInvalidSizePanics(t *testing.T) {
	t.Parallel()
	pos := newInsertPosTracker(0, 0, 100, nil)
	tr := newInsertionTracker()
	for _, sz := range []uint64{0, 101, 200} {
		sz := sz
		t.Run("", func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatalf("reserveAndPublish(%d) did not panic", sz)
				}
			}()
			_, _ = pos.reserveAndPublish(sz, 0, tr)
		})
	}
}

func TestInsertPosTrackerReserveAndPublishNilTrackerPanics(t *testing.T) {
	t.Parallel()
	pos := newInsertPosTracker(0, 0, 1<<20, nil)
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("reserveAndPublish(nil tracker) did not panic")
		}
	}()
	_, _ = pos.reserveAndPublish(16, 0, nil)
}

func TestInsertPosTrackerReserveAndPublishInvalidStripePanics(t *testing.T) {
	t.Parallel()
	pos := newInsertPosTracker(0, 0, 1<<20, nil)
	tr := newInsertionTracker()
	for _, s := range []int{-1, appendLockStripes, appendLockStripes + 1, math.MaxInt32} {
		s := s
		t.Run("", func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatalf("reserveAndPublish(stripe=%d) did not panic", s)
				}
			}()
			_, _ = pos.reserveAndPublish(16, s, tr)
		})
	}
}

func TestInsertPosTrackerReserveAndPublishInteropWithReserve(t *testing.T) {
	t.Parallel()
	// Mixing plain reserve and reserveAndPublish on the same tracker
	// must keep (curr, prev) chain coherent — the reserveLocked helper
	// is the single source of truth.
	pos := newInsertPosTracker(0, 0, 1<<20, nil)
	tr := newInsertionTracker()

	// reserve(16): no tracker publish.
	s0, p0 := pos.reserve(16)
	if s0 != 0 || p0 != 0 {
		t.Fatalf("reserve#1 = (%d, %d), want (0, 0)", s0, p0)
	}
	// reserveAndPublish(16): publishes stripe.
	s1, p1 := pos.reserveAndPublish(16, 5, tr)
	if s1 != 16 || p1 != 0 {
		t.Fatalf("reserveAndPublish#2 = (%d, %d), want (16, 0)", s1, p1)
	}
	// reserve(16): no publish but prev pointer matches the previous
	// reserveAndPublish's start.
	s2, p2 := pos.reserve(16)
	if s2 != 32 || p2 != 16 {
		t.Fatalf("reserve#3 = (%d, %d), want (32, 16)", s2, p2)
	}
	// Only the stripe written by reserveAndPublish is populated.
	if got := tr.insertingAt(5); got != 16 {
		t.Fatalf("stripe 5 insertingAt = %d, want 16", got)
	}
	for s := 0; s < appendLockStripes; s++ {
		if s == 5 {
			continue
		}
		if got := tr.insertingAt(s); got != lsnIdle {
			t.Fatalf("stripe %d insertingAt = %d, want lsnIdle", s, got)
		}
	}
}

// TestInsertPosTrackerReserveAndPublishConcurrentChain pins that
// concurrent reserveAndPublish calls produce a coherent prev chain
// AND every stripe's published slot reflects a real reservation.
// The chain assertion mirrors TestInsertPosTrackerConcurrentReservesFormChain;
// the tracker assertion is the new race-closure invariant.
func TestInsertPosTrackerReserveAndPublishConcurrentChain(t *testing.T) {
	t.Parallel()
	const (
		goroutines = 32
		perG       = 100
		size       = uint64(16)
		segSize    = uint64(1 << 20)
	)
	pos := newInsertPosTracker(0, 0, segSize, func(_, _, _ uint64) bool {
		t.Errorf("onCross fired in single-segment scenario")
		return true
	})
	tr := newInsertionTracker()

	type pair struct{ start, prev uint64 }
	results := make([]pair, goroutines*perG)
	var idx atomic.Int64
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			stripe := g & (appendLockStripes - 1)
			for i := 0; i < perG; i++ {
				s, p := pos.reserveAndPublish(size, stripe, tr)
				results[idx.Add(1)-1] = pair{s, p}
				// Match the per-stripe contract: publish idle after
				// the (notional) byte write completes. Without this
				// the last writer per stripe leaves a stale active
				// LSN that lowestActiveLSN would still see.
				tr.setInsertingAt(stripe, lsnIdle)
			}
		}()
	}
	wg.Wait()

	// Chain integrity: starts form a contiguous 16-stride permutation.
	starts := make([]uint64, len(results))
	for i, r := range results {
		starts[i] = r.start
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	for i, s := range starts {
		want := uint64(i) * size
		if s != want {
			t.Fatalf("sorted starts[%d] = %d, want %d", i, s, want)
		}
	}
	// All stripes are idle at the end (each goroutine cleared its slot).
	if got := tr.lowestActiveLSN(); got != lsnNoActive {
		t.Fatalf("post-run lowestActiveLSN = %d, want lsnNoActive", got)
	}
}

// TestInsertPosTrackerReserveAndPublishConsistentSnapshot is the main
// correctness test for foundation 9. The race-closure guarantee: any
// observer that synchronises with posMu (e.g. via insertPosTracker.load)
// sees the (curr, tracker[stripe]) pair as a joint snapshot — no
// "curr advanced past LSN X but tracker[stripe] still lsnIdle" tear
// is possible during the writer's posMu critical section.
//
// Without this foundation (old contract: reserve, drop posMu, then
// setInsertingAt outside posMu), the invariant fails: a reader holding
// posMu would observe curr advanced but the stripe slot still idle
// for the in-flight reservation. With reserveAndPublish, both updates
// happen under one posMu critical section.
//
// The reader takes posMu directly (legal because the test sits in
// the `wal` package and posMu is package-private). Inside that
// critical section, it reads curr AND every stripe slot. The proxy
// invariants it asserts on each non-idle slot value v:
//
//   - v < curr (the slot's published LSN is strictly below the
//     latest reserved LSN — any in-flight reservation has already
//     advanced curr past its own start).
//   - (v - startCurr) % size == 0 (the slot holds a real reservation
//     start, not a torn value).
//
// Note that the *converse* invariant — "if curr advanced, some slot
// is non-idle" — does NOT hold, because the END setInsertingAt(idle)
// is deliberately not under posMu (only the BEGIN publish is); a
// reservation that has completed its full lifetime leaves all stripe
// slots idle even though curr remains advanced. That's the expected
// boundary of foundation 9: it seals the BEGIN edge, not the END.
func TestInsertPosTrackerReserveAndPublishConsistentSnapshot(t *testing.T) {
	t.Parallel()
	const (
		writers   = 8
		perWriter = 2000
		size      = uint64(16)
		segSize   = uint64(1 << 24)
		startCurr = uint64(1)
	)
	pos := newInsertPosTracker(startCurr, 0, segSize, nil)
	tr := newInsertionTracker()

	var stop atomic.Bool
	var writersWG, readerWG sync.WaitGroup
	for w := 0; w < writers; w++ {
		w := w
		writersWG.Add(1)
		go func() {
			defer writersWG.Done()
			stripe := w & (appendLockStripes - 1)
			for i := 0; i < perWriter; i++ {
				_, _ = pos.reserveAndPublish(size, stripe, tr)
				// END setInsertingAt happens immediately — no
				// sleep. The natural race window is between
				// posMu release inside reserveAndPublish and
				// the next reserveAndPublish call.
				tr.setInsertingAt(stripe, lsnIdle)
			}
		}()
	}

	violations := atomic.Int64{}
	readerWG.Add(1)
	go func() {
		defer readerWG.Done()
		for !stop.Load() {
			pos.posMu.Lock()
			curr := pos.curr
			for s := 0; s < appendLockStripes; s++ {
				v := tr.insertingAt(s)
				if v == lsnIdle {
					continue
				}
				if uint64(v) >= curr {
					violations.Add(1)
				}
				if (uint64(v)-startCurr)%size != 0 {
					violations.Add(1)
				}
			}
			pos.posMu.Unlock()
		}
	}()

	writersWG.Wait()
	stop.Store(true)
	readerWG.Wait()

	if v := violations.Load(); v != 0 {
		t.Fatalf("consistent-snapshot invariant violated %d times", v)
	}
}

// TestInsertPosTrackerReserveAndPublishWatchdog ensures the
// concurrent race-closure scenario completes promptly under -race.
// A regression that deadlocks (e.g. by inverting the lock order or
// nesting posMu inside the stripe mutex incorrectly) would manifest
// as the test timing out at the package level; a per-test watchdog
// surfaces it sooner.
func TestInsertPosTrackerReserveAndPublishWatchdog(t *testing.T) {
	t.Parallel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		pos := newInsertPosTracker(0, 0, 1<<24, nil)
		tr := newInsertionTracker()
		var wg sync.WaitGroup
		for w := 0; w < 8; w++ {
			w := w
			wg.Add(1)
			go func() {
				defer wg.Done()
				stripe := w & (appendLockStripes - 1)
				for i := 0; i < 5000; i++ {
					_, _ = pos.reserveAndPublish(16, stripe, tr)
					tr.setInsertingAt(stripe, lsnIdle)
				}
			}()
		}
		wg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("reserveAndPublish concurrent run did not complete within 5s — possible deadlock")
	}
}
