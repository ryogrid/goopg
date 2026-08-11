package wal

import (
	"sort"
	"sync"
	"sync/atomic"
	"testing"
)

// TestInsertPosTrackerReserveContiguousMonotonic pins the
// in-segment fast path: three back-to-back reservations get
// monotonically increasing starts (0, 10, 30) and a prev pointer
// chain that walks back through them (0, 0, 10).
func TestInsertPosTrackerReserveContiguousMonotonic(t *testing.T) {
	t.Parallel()
	tr := newInsertPosTracker(0, 0, 1<<20, nil)
	type r struct{ start, prev uint64 }
	got := make([]r, 0, 3)
	for _, sz := range []uint64{10, 20, 30} {
		s, p := tr.reserve(sz)
		got = append(got, r{s, p})
	}
	want := []r{
		{0, 0},
		{10, 0},
		{30, 10},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reservation %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
	c, p := tr.load()
	if c != 60 || p != 30 {
		t.Fatalf("load after 3 reserves: curr=%d prev=%d, want curr=60 prev=30", c, p)
	}
}

// TestInsertPosTrackerReserveStartCurrPrev pins non-zero
// initialisers (recovery resume): the first reservation observes
// the configured startPrev, not 0.
func TestInsertPosTrackerReserveStartCurrPrev(t *testing.T) {
	t.Parallel()
	tr := newInsertPosTracker(0xDEAD_BEEF_00, 0xDEAD_BEEF_F0, 1<<20, nil)
	start, prev := tr.reserve(16)
	if start != 0xDEAD_BEEF_00 || prev != 0xDEAD_BEEF_F0 {
		t.Fatalf("first reserve: start=%#x prev=%#x, want start=0xDEAD_BEEF00 prev=0xDEAD_BEEFF0", start, prev)
	}
	start, prev = tr.reserve(16)
	if start != 0xDEAD_BEEF_10 || prev != 0xDEAD_BEEF_00 {
		t.Fatalf("second reserve: start=%#x prev=%#x, want start=0xDEAD_BEEF10 prev=0xDEAD_BEEF00", start, prev)
	}
}

// TestInsertPosTrackerCrossSegmentInvokesHook drives a reservation
// that straddles a segment boundary. The hook fires once with the
// pre-crossing (gapStart, boundary, gapPrev); the reservation lands
// at boundary with prev=gapStart (the pad record's start).
func TestInsertPosTrackerCrossSegmentInvokesHook(t *testing.T) {
	t.Parallel()
	type callArg struct{ start, boundary, prev uint64 }
	var calls []callArg
	tr := newInsertPosTracker(1000, 990, 1024, func(start, boundary, prev uint64) bool {
		calls = append(calls, callArg{start, boundary, prev})
		return true
	})
	start, prev := tr.reserve(50) // 1000 + 50 = 1050, crosses 1024 boundary
	if len(calls) != 1 {
		t.Fatalf("onCross fired %d times, want 1", len(calls))
	}
	if calls[0] != (callArg{1000, 1024, 990}) {
		t.Fatalf("onCross args = %+v, want {1000, 1024, 990}", calls[0])
	}
	if start != 1024 || prev != 1000 {
		t.Fatalf("crossing reservation: start=%d prev=%d, want start=1024 prev=1000", start, prev)
	}
	c, p := tr.load()
	if c != 1074 || p != 1024 {
		t.Fatalf("load after crossing: curr=%d prev=%d, want curr=1074 prev=1024", c, p)
	}
}

// TestInsertPosTrackerReserveAtExactBoundaryNoHook checks the
// off-by-one: a reservation that ends exactly at the boundary
// (oldSeg == endSeg) stays in the fast path; onCross is silent.
func TestInsertPosTrackerReserveAtExactBoundaryNoHook(t *testing.T) {
	t.Parallel()
	fired := false
	tr := newInsertPosTracker(1024-32, 1000, 1024, func(_, _, _ uint64) bool { fired = true; return true })
	start, prev := tr.reserve(32) // 1024-32 .. 1024, last byte = 1023, still seg 0
	if fired {
		t.Fatalf("onCross fired on exact-boundary reservation; should not")
	}
	if start != 1024-32 || prev != 1000 {
		t.Fatalf("exact-boundary reserve: start=%d prev=%d, want start=%d prev=1000", start, prev, 1024-32)
	}
	c, _ := tr.load()
	if c != 1024 {
		t.Fatalf("curr after exact-boundary reserve = %d, want 1024", c)
	}
}

// TestInsertPosTrackerReserveInvalidSizePanics pins the strict
// 0 < size <= segSize contract.
func TestInsertPosTrackerReserveInvalidSizePanics(t *testing.T) {
	t.Parallel()
	tr := newInsertPosTracker(0, 0, 1024, nil)
	for _, sz := range []uint64{0, 1025, 2000} {
		func(sz uint64) {
			defer func() {
				if recover() == nil {
					t.Errorf("reserve(%d): no panic; want panic", sz)
				}
			}()
			tr.reserve(sz)
		}(sz)
	}
}

// TestInsertPosTrackerNewRejectsZeroSegSize pins newInsertPosTracker's
// segSize > 0 invariant.
func TestInsertPosTrackerNewRejectsZeroSegSize(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatalf("newInsertPosTracker(_, _, 0, nil): no panic; want panic")
		}
	}()
	newInsertPosTracker(0, 0, 0, nil)
}

// TestInsertPosTrackerConcurrentReservesFormChain races 32
// goroutines × 100 reservations of 16 bytes each in a 1 MiB segment
// and pins three invariants:
//  1. The 3200 starts are a permutation of {0, 16, 32, ..., 51184}
//     (no duplicates, no gaps, complete cover).
//  2. Each reservation's prev pointer equals start-16 of some other
//     reservation (or 0 for the very first), i.e. the chain is
//     complete and self-referential.
//  3. The "prev → start" chain, when walked from the first record
//     forward, visits every reservation exactly once in start order.
func TestInsertPosTrackerConcurrentReservesFormChain(t *testing.T) {
	t.Parallel()
	const (
		goroutines = 32
		perG       = 100
		size       = uint64(16)
		segSize    = uint64(1 << 20)
	)
	tr := newInsertPosTracker(0, 0, segSize, func(_, _, _ uint64) bool {
		t.Errorf("onCross fired in single-segment scenario")
		return true
	})
	type pair struct{ start, prev uint64 }
	results := make([]pair, goroutines*perG)
	var idx atomic.Int64
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				s, p := tr.reserve(size)
				results[idx.Add(1)-1] = pair{s, p}
			}
		}()
	}
	wg.Wait()

	// Invariant 1: starts form a permutation of 16-stride sequence.
	starts := make([]uint64, len(results))
	for i, r := range results {
		starts[i] = r.start
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	for i, s := range starts {
		want := uint64(i) * size
		if s != want {
			t.Fatalf("sorted starts[%d] = %d, want %d (chain is not a contiguous permutation)", i, s, want)
		}
	}

	// Invariants 2 + 3: build a (start → prev) map; walking back
	// from any start through prev pointers eventually reaches 0,
	// visiting every start exactly once.
	prevOf := make(map[uint64]uint64, len(results))
	for _, r := range results {
		if existing, ok := prevOf[r.start]; ok && existing != r.prev {
			t.Fatalf("start=%d has two different prev values: %d and %d", r.start, existing, r.prev)
		}
		prevOf[r.start] = r.prev
	}
	// Walk from the largest start back to 0 through prev pointers.
	visited := make(map[uint64]bool, len(results))
	cur := starts[len(starts)-1]
	for {
		if visited[cur] {
			t.Fatalf("chain cycle at start=%d", cur)
		}
		visited[cur] = true
		prev, ok := prevOf[cur]
		if !ok {
			t.Fatalf("chain referenced unknown start=%d", cur)
		}
		if cur == 0 {
			// Reached the root (start=0, prev=0). Chain complete.
			break
		}
		if prev >= cur {
			t.Fatalf("prev=%d is not strictly less than start=%d", prev, cur)
		}
		cur = prev
	}
	if len(visited) != len(results) {
		t.Fatalf("chain walk visited %d records, want %d (chain is incomplete)", len(visited), len(results))
	}
}

// TestInsertPosTrackerConcurrentCrossSegmentHookOncePerBoundary
// races 16 goroutines across the same segment boundary at curr=200
// of a 256-byte segment. Each does a 40-byte reservation, so most
// land within the segment but several cross. The hook must fire
// exactly once per crossing; no two reservations may straddle the
// same boundary.
func TestInsertPosTrackerConcurrentCrossSegmentHookOncePerBoundary(t *testing.T) {
	t.Parallel()
	const (
		goroutines = 16
		size       = uint64(40)
		segSize    = uint64(256)
		startCurr  = uint64(200)
	)
	var (
		hookMu sync.Mutex
		hooks  []uint64 // boundary values
	)
	tr := newInsertPosTracker(startCurr, 0, segSize, func(_, boundary, _ uint64) bool {
		hookMu.Lock()
		hooks = append(hooks, boundary)
		hookMu.Unlock()
		return true
	})
	type pair struct{ start, prev uint64 }
	results := make([]pair, goroutines)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, p := tr.reserve(size)
			results[g] = pair{s, p}
		}()
	}
	wg.Wait()

	// No two reservations may straddle a segment boundary (the
	// crossing path always lands the reservation at boundary).
	for _, r := range results {
		if r.start/segSize != (r.start+size-1)/segSize {
			t.Fatalf("reservation start=%d straddles a boundary (size=%d, segSize=%d)", r.start, size, segSize)
		}
	}

	// Each unique boundary in the hook list must equal the
	// difference between firstSeg and lastSeg across the 16
	// reservations.
	starts := make([]uint64, len(results))
	for i, r := range results {
		starts[i] = r.start
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	firstSeg := starts[0] / segSize
	lastSeg := starts[len(starts)-1] / segSize
	hookMu.Lock()
	defer hookMu.Unlock()
	if uint64(len(hooks)) != lastSeg-firstSeg {
		t.Fatalf("hook fired %d times, want %d (segments crossed)", len(hooks), lastSeg-firstSeg)
	}
}

// TestInsertPosTrackerCrossSegmentPrevIsCrossingStart pins the
// prev-chain integrity across a segment crossing. The pad record
// at `[old, boundary)` inherits the cumulative prev so far; the
// reservation that triggered the crossing receives prev=old (the
// pad record's start), preserving the chain.
func TestInsertPosTrackerCrossSegmentPrevIsCrossingStart(t *testing.T) {
	t.Parallel()
	type hookCall struct{ start, boundary, prev uint64 }
	var calls []hookCall
	tr := newInsertPosTracker(0, 0, 100, func(start, boundary, prev uint64) bool {
		calls = append(calls, hookCall{start, boundary, prev})
		return true
	})

	// Three reservations: 40, 40, 40 — third one crosses the
	// 100-byte boundary because 80+40 = 120 > 100.
	starts := make([]uint64, 3)
	prevs := make([]uint64, 3)
	for i, sz := range []uint64{40, 40, 40} {
		starts[i], prevs[i] = tr.reserve(sz)
	}

	if calls := len(calls); calls != 1 {
		t.Fatalf("onCross fired %d times, want 1", calls)
	}
	// Pre-crossing curr = 80, prev = 40. Crossing emits the pad
	// record at [80, 100) with xl_prev = 40 (the cumulative prev),
	// then the third reservation lands at 100 with prev = 80
	// (the pad record's start).
	if calls[0] != (hookCall{80, 100, 40}) {
		t.Fatalf("onCross args = %+v, want {80, 100, 40}", calls[0])
	}
	if starts[0] != 0 || starts[1] != 40 || starts[2] != 100 {
		t.Fatalf("starts = %v, want [0 40 100]", starts)
	}
	if prevs[0] != 0 || prevs[1] != 0 || prevs[2] != 80 {
		t.Fatalf("prevs = %v, want [0 0 80] (third prev is pad record's start)", prevs)
	}
}

// TestInsertPosTrackerLoadSnapshotConsistent races a reader against
// a writer and pins that the (curr, prev) snapshot read under
// posMu is internally consistent — prev is the start of the record
// whose end equals curr (or curr > prev+size for the very first
// reservation case).
func TestInsertPosTrackerLoadSnapshotConsistent(t *testing.T) {
	t.Parallel()
	const (
		segSize = uint64(1 << 20)
		size    = uint64(16)
		iters   = 5000
	)
	tr := newInsertPosTracker(0, 0, segSize, nil)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			tr.reserve(size)
		}
		close(stop)
	}()
	for {
		c, p := tr.load()
		if p+size > c && c > 0 {
			t.Fatalf("inconsistent snapshot: curr=%d prev=%d (prev+size > curr)", c, p)
		}
		select {
		case <-stop:
			wg.Wait()
			return
		default:
		}
	}
}
