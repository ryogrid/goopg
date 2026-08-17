package xlog

import (
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Pins the zero-state contract: a fresh publisher has watermark 0,
// and publishUpTo with no upper bound (0) and no active stripes
// stays at 0. A regression here would silently advance the watermark
// past LSNs that have never been reserved.
func TestTailPublisherNewIsZero(t *testing.T) {
	t.Parallel()
	p := newTailPublisher()
	if got := p.load(); got != 0 {
		t.Errorf("fresh publisher load: got %d, want 0", got)
	}
	tr := newInsertionTracker()
	if got := p.publishUpTo(0, tr); got != 0 {
		t.Errorf("publishUpTo(0, idle): got %d, want 0", got)
	}
	if got := p.load(); got != 0 {
		t.Errorf("load after no-op publish: got %d, want 0", got)
	}
}

// Idle tracker means `lowestActiveLSN == lsnNoActive == MaxInt64`,
// so safeTail collapses to upperBound. Pins that the sentinel
// composes with min as foundation 6 promised.
func TestTailPublisherIdleTrackerPublishesUpperBound(t *testing.T) {
	t.Parallel()
	p := newTailPublisher()
	tr := newInsertionTracker()
	got := p.publishUpTo(1000, tr)
	if got != 1000 {
		t.Errorf("publishUpTo(1000, idle): got %d, want 1000", got)
	}
	if cur := p.load(); cur != 1000 {
		t.Errorf("load after publish: got %d, want 1000", cur)
	}
}

// An active stripe with start LSN below upperBound caps the safe
// tail at that start LSN. The publisher must not advance past
// in-flight reservations — that is the entire reason it exists.
func TestTailPublisherActiveStripeCapsSafeTail(t *testing.T) {
	t.Parallel()
	p := newTailPublisher()
	tr := newInsertionTracker()
	tr.setInsertingAt(2, 600)
	// upperBound is 1000 but stripe 2 is mid-insert at 600.
	got := p.publishUpTo(1000, tr)
	if got != 600 {
		t.Errorf("publishUpTo with active stripe@600: got %d, want 600", got)
	}
	// After the stripe finishes, a follow-up publish advances to upperBound.
	tr.setInsertingAt(2, lsnIdle)
	got = p.publishUpTo(1000, tr)
	if got != 1000 {
		t.Errorf("publishUpTo after stripe idle: got %d, want 1000", got)
	}
}

// The lowest active LSN across all stripes wins. Without this, a
// publication walker could publish a watermark above some other
// stripe's in-flight reservation.
func TestTailPublisherTakesMinAcrossStripes(t *testing.T) {
	t.Parallel()
	p := newTailPublisher()
	tr := newInsertionTracker()
	tr.setInsertingAt(0, 500)
	tr.setInsertingAt(1, 300)
	tr.setInsertingAt(2, 700)
	got := p.publishUpTo(10000, tr)
	if got != 300 {
		t.Errorf("publishUpTo with min-stripe@300: got %d, want 300", got)
	}
}

// Pins monotonicity under the canonical "watermark wants to go
// backwards" scenario: first publish observes idle stripes and
// advances to 1000; a later publish (still with upperBound=1000)
// observes a fresh stripe@200 and would compute safeTail=200, but
// the published value MUST NOT regress. Reader-side safety depends
// on this — bytes ∈ [200, 1000) were declared safe by the first
// publish; revoking that publication while readers may be active
// would expose racing writes.
func TestTailPublisherMonotonicNeverRegresses(t *testing.T) {
	t.Parallel()
	p := newTailPublisher()
	tr := newInsertionTracker()
	// First publish: idle, advance to 1000.
	if got := p.publishUpTo(1000, tr); got != 1000 {
		t.Fatalf("first publish: got %d, want 1000", got)
	}
	// New stripe enters at 200; second publish must not regress.
	tr.setInsertingAt(4, 200)
	got := p.publishUpTo(1000, tr)
	if got != 1000 {
		t.Errorf("publishUpTo with active stripe@200 after watermark@1000: got %d, want 1000 (no regression)", got)
	}
	if cur := p.load(); cur != 1000 {
		t.Errorf("load after no-regress publish: got %d, want 1000", cur)
	}
}

// Pins the return-value contract: when the candidate does not
// advance the watermark, publishUpTo returns the CURRENT published
// value (not the candidate). Callers rely on this to use the return
// value as a safe upper bound for drain without an extra Load.
func TestTailPublisherReturnsCurrentWhenCandidateLower(t *testing.T) {
	t.Parallel()
	p := newTailPublisher()
	tr := newInsertionTracker()
	if got := p.publishUpTo(500, tr); got != 500 {
		t.Fatalf("seed publish: got %d, want 500", got)
	}
	// Active stripe at 100; candidate < current.
	tr.setInsertingAt(0, 100)
	got := p.publishUpTo(500, tr)
	if got != 500 {
		t.Errorf("publishUpTo with candidate=100, current=500: got %d, want 500", got)
	}
	// Active stripe at 500 exactly (== current); still no advance, return current.
	tr.setInsertingAt(0, 500)
	got = p.publishUpTo(500, tr)
	if got != 500 {
		t.Errorf("publishUpTo with candidate==current: got %d, want 500", got)
	}
}

// nil receiver returns 0. Pins the defensive contract used by other
// foundation primitives so a future Writer with Config.WALBuffers == 0
// can leave the publisher unset without segfaulting.
func TestTailPublisherNilReceiverReturnsZero(t *testing.T) {
	t.Parallel()
	var p *tailPublisher
	if got := p.publishUpTo(1000, newInsertionTracker()); got != 0 {
		t.Errorf("nil receiver publishUpTo: got %d, want 0", got)
	}
}

// nil tracker behaves as "all idle": safeTail = upperBound. Useful
// during call-site rewrite for transitional state when the
// publisher is wired in ahead of the tracker.
func TestTailPublisherNilTrackerActsAsAllIdle(t *testing.T) {
	t.Parallel()
	p := newTailPublisher()
	got := p.publishUpTo(2024, nil)
	if got != 2024 {
		t.Errorf("publishUpTo(_, nil): got %d, want 2024", got)
	}
}

// Pins that consecutive in-order publishes advance the watermark
// each step (this is the steady-state drain pattern: the drain
// goroutine repeatedly publishes a growing upperBound as stripes
// finish their inserts).
func TestTailPublisherAdvancesAcrossSequentialPublishes(t *testing.T) {
	t.Parallel()
	p := newTailPublisher()
	tr := newInsertionTracker()
	steps := []int64{100, 250, 250, 400, 999_999}
	want := []int64{100, 250, 250, 400, 999_999}
	for i, ub := range steps {
		got := p.publishUpTo(ub, tr)
		if got != want[i] {
			t.Errorf("step %d (upperBound=%d): got %d, want %d", i, ub, got, want[i])
		}
	}
	if got := p.load(); got != 999_999 {
		t.Errorf("final load: got %d, want 999999", got)
	}
}

// Concurrent publishUpTo callers must converge monotonically.
// Drives 16 goroutines × 1 000 iterations: each picks a random-ish
// upperBound from a monotonically growing source; the final
// watermark must equal the maximum upperBound observed across all
// calls AND every per-call return value must be ≥ all prior
// observations (monotonicity).
func TestTailPublisherConcurrentPublishesAreMonotonic(t *testing.T) {
	t.Parallel()
	p := newTailPublisher()
	tr := newInsertionTracker()
	const (
		workers = 16
		iters   = 1000
	)
	var upperSrc atomic.Int64
	var maxSeen atomic.Int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			var last int64
			for i := 0; i < iters; i++ {
				ub := upperSrc.Add(1)
				got := p.publishUpTo(ub, tr)
				if got < last {
					t.Errorf("per-worker monotonicity violated: got %d after %d", got, last)
				}
				last = got
				// Track the global max return for the final assertion.
				for {
					cur := maxSeen.Load()
					if got <= cur || maxSeen.CompareAndSwap(cur, got) {
						break
					}
				}
			}
		}()
	}
	wg.Wait()
	finalUB := upperSrc.Load()
	finalLoad := p.load()
	if finalLoad != finalUB {
		t.Errorf("final published: got %d, want %d (idle tracker means safeTail=upperBound)", finalLoad, finalUB)
	}
	if finalLoad < maxSeen.Load() {
		t.Errorf("final published %d below max return value %d", finalLoad, maxSeen.Load())
	}
}

// Concurrent publishers under an oscillating tracker: writer
// stripes alternate active/idle while publication walkers run. The
// invariant under test is the safety one — the published watermark
// must NEVER exceed the lowest active LSN at the moment of any
// observed return value (because the publisher uses a snapshot of
// the tracker just before computing the candidate). We can't prove
// this directly in a race-clean test (the tracker can advance
// between read and CAS), but we CAN assert the per-call return
// value is bounded by the upperBound passed in AND the watermark
// never decreases between sequential per-worker observations.
func TestTailPublisherConcurrentWithActiveStripes(t *testing.T) {
	t.Parallel()
	p := newTailPublisher()
	tr := newInsertionTracker()
	const (
		stripeWorkers  = appendLockStripes
		publishWorkers = 4
		ops            = 500
	)
	stop := make(chan struct{})
	var stripeWG, pubWG sync.WaitGroup
	// Stripe workers: oscillate stripe i between active(i*100) and idle.
	stripeWG.Add(stripeWorkers)
	for i := 0; i < stripeWorkers; i++ {
		go func(stripe int) {
			defer stripeWG.Done()
			start := int64((stripe + 1) * 100) // 100..800
			for {
				select {
				case <-stop:
					return
				default:
				}
				tr.setInsertingAt(stripe, start)
				tr.setInsertingAt(stripe, lsnIdle)
			}
		}(i)
	}
	// Publish workers: drive publishUpTo with a growing upperBound.
	pubWG.Add(publishWorkers)
	for w := 0; w < publishWorkers; w++ {
		go func() {
			defer pubWG.Done()
			var last int64
			for i := 0; i < ops; i++ {
				ub := int64(1000 + i)
				got := p.publishUpTo(ub, tr)
				if got > ub {
					t.Errorf("publishUpTo return %d exceeds upperBound %d", got, ub)
				}
				if got < last {
					t.Errorf("per-worker watermark regressed: got %d after %d", got, last)
				}
				last = got
			}
		}()
	}
	pubWG.Wait()
	close(stop)
	stripeWG.Wait()
	// After stripe workers stop, drain to confirm a final publish
	// reaches the final upperBound.
	final := p.publishUpTo(1_000_000, tr)
	if final != 1_000_000 {
		t.Errorf("post-stop final publishUpTo: got %d, want 1_000_000", final)
	}
}

// Sentinel composition: a published watermark at MaxInt64-1 plus
// an all-idle tracker plus an upperBound at MaxInt64-1 should be a
// no-op (already at upperBound). The test pins that the sentinel
// MaxInt64 used by lowestActiveLSN doesn't accidentally become the
// published value via the min().
func TestTailPublisherSentinelDoesNotLeakIntoPublishedValue(t *testing.T) {
	t.Parallel()
	p := newTailPublisher()
	tr := newInsertionTracker() // all idle → lowestActiveLSN == math.MaxInt64
	ub := int64(math.MaxInt64) - 1
	got := p.publishUpTo(ub, tr)
	if got != ub {
		t.Errorf("publishUpTo at MaxInt64-1: got %d, want %d", got, ub)
	}
	if cur := p.load(); cur == math.MaxInt64 {
		t.Errorf("published reached sentinel MaxInt64: got %d", cur)
	}
	if cur := p.load(); cur != ub {
		t.Errorf("load: got %d, want %d", cur, ub)
	}
}

// Watchdog the concurrent test — if the publication chain deadlocks
// or live-locks (it shouldn't, the publisher is lock-free), surface
// it here rather than letting `go test` hit its default timeout.
func TestTailPublisherConcurrentCompletesUnderWatchdog(t *testing.T) {
	t.Parallel()
	done := make(chan struct{})
	go func() {
		p := newTailPublisher()
		tr := newInsertionTracker()
		var wg sync.WaitGroup
		const (
			workers = 8
			iters   = 200
		)
		wg.Add(workers)
		for w := 0; w < workers; w++ {
			go func(id int) {
				defer wg.Done()
				stripe := id % appendLockStripes
				for i := 0; i < iters; i++ {
					tr.setInsertingAt(stripe, int64(10*(i+1)))
					_ = p.publishUpTo(int64(20*(i+1)), tr)
					tr.setInsertingAt(stripe, lsnIdle)
					_ = p.publishUpTo(int64(20*(i+1)), tr)
				}
			}(w)
		}
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("tailPublisher concurrent stress did not complete within 5s — possible live-lock")
	}
}
