package wal

import (
	"sort"
	"sync"
	"sync/atomic"
	"testing"
)

func TestLSNAllocatorReserveContiguousMonotonic(t *testing.T) {
	a := newLSNAllocator(0, 1024, nil)
	got := []uint64{a.reserve(10), a.reserve(20), a.reserve(30)}
	want := []uint64{0, 10, 30}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reserve %d: got %d want %d", i, got[i], want[i])
		}
	}
	if a.load() != 60 {
		t.Fatalf("load after 3 reserves: got %d want 60", a.load())
	}
}

func TestLSNAllocatorReserveStartLSN(t *testing.T) {
	// Non-zero starting LSN exercises the offset arithmetic used by
	// recovery (writer resumes at last detectWritePos, not 0).
	a := newLSNAllocator(1000, 4096, nil)
	if got := a.reserve(50); got != 1000 {
		t.Fatalf("first reserve: got %d want 1000", got)
	}
	if got := a.load(); got != 1050 {
		t.Fatalf("load: got %d want 1050", got)
	}
}

func TestLSNAllocatorCrossSegmentInvokesHook(t *testing.T) {
	type hookCall struct{ start, boundary uint64 }
	var hooks []hookCall
	a := newLSNAllocator(0, 1024, func(s, b uint64) {
		hooks = append(hooks, hookCall{s, b})
	})

	// Fill segment 0 to byte 1000 (24 bytes free) — single CAS path.
	if got := a.reserve(1000); got != 0 {
		t.Fatalf("first reserve: got %d want 0", got)
	}
	if len(hooks) != 0 {
		t.Fatalf("hook called on non-crossing reserve: %v", hooks)
	}

	// Next reserve of 50 bytes would land at [1000, 1050), crossing
	// into segment 1. Allocator pads [1000, 1024) via hook, reserves
	// [1024, 1074) in segment 1.
	got := a.reserve(50)
	if got != 1024 {
		t.Fatalf("crossing reserve: got %d want 1024 (segment 1 start)", got)
	}
	if len(hooks) != 1 || hooks[0] != (hookCall{start: 1000, boundary: 1024}) {
		t.Fatalf("hook calls: got %v want one call {1000,1024}", hooks)
	}
	if a.load() != 1024+50 {
		t.Fatalf("load after crossing: got %d want %d", a.load(), 1024+50)
	}
}

func TestLSNAllocatorReserveAtExactBoundaryNoHook(t *testing.T) {
	// next == segment boundary exactly + reservation fits in next
	// segment: this is the "oldSeg == endSeg" fast path (both indices
	// equal segment 1), so no rotation hook should fire.
	var hookCalls int
	a := newLSNAllocator(1024, 1024, func(s, b uint64) { hookCalls++ })

	if got := a.reserve(100); got != 1024 {
		t.Fatalf("reserve at boundary: got %d want 1024", got)
	}
	if hookCalls != 0 {
		t.Fatalf("hook invoked on at-boundary reserve: %d", hookCalls)
	}
}

func TestLSNAllocatorReserveInvalidSizePanics(t *testing.T) {
	a := newLSNAllocator(0, 1024, nil)
	for _, sz := range []uint64{0, 1025, 2048} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("reserve(%d): expected panic", sz)
				}
			}()
			a.reserve(sz)
		}()
	}
}

func TestLSNAllocatorNewRejectsZeroSegSize(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("newLSNAllocator(_,0,_): expected panic")
		}
	}()
	_ = newLSNAllocator(0, 0, nil)
}

func TestLSNAllocatorConcurrentReservesDisjoint(t *testing.T) {
	// 32 goroutines each reserve 100 ranges of 16 bytes inside a
	// large single segment. All 3200 ranges must be disjoint and
	// their union must be a permutation of [0, 51200) in 16-byte
	// chunks. Exercises the CAS retry loop under contention; no
	// segment boundary is involved, so rotateMu and the hook stay
	// idle.
	const goroutines = 32
	const perG = 100
	const sz = 16
	a := newLSNAllocator(0, 1<<20, func(_, _ uint64) {
		t.Errorf("rotation hook invoked in single-segment test")
	})

	var wg sync.WaitGroup
	starts := make([]uint64, goroutines*perG)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				starts[base+i] = a.reserve(sz)
			}
		}(g * perG)
	}
	wg.Wait()

	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	for i, s := range starts {
		want := uint64(i) * sz
		if s != want {
			t.Fatalf("starts[%d] = %d want %d", i, s, want)
		}
	}
	if a.load() != uint64(goroutines*perG)*sz {
		t.Fatalf("load: got %d want %d", a.load(), uint64(goroutines*perG)*sz)
	}
}

func TestLSNAllocatorConcurrentCrossSegmentHookOncePerBoundary(t *testing.T) {
	// All 16 goroutines race to reserve across the same segment
	// boundary. After the dust settles, onCrossSegment must have
	// fired exactly once per boundary actually crossed (a peer that
	// loses the rotateMu race re-observes next past the boundary
	// and drops to the fast path — no duplicate hook calls).
	const segSize uint64 = 256
	const goroutines = 16
	const sz uint64 = 40 // 40-byte records inside a 256-byte segment.

	var crossings atomic.Int64
	// Start at byte 230 of segment 0 so the first reserve from any
	// goroutine crosses into segment 1.
	a := newLSNAllocator(230, segSize, func(_, _ uint64) {
		crossings.Add(1)
	})

	var wg sync.WaitGroup
	out := make([]uint64, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			out[idx] = a.reserve(sz)
		}(g)
	}
	wg.Wait()

	// Count distinct segment boundaries actually crossed across all
	// reservations. After the first goroutine pads [230, 256) and
	// places its reserve at 256, the next goroutines reserve at
	// 296, 336, 376, …; one of them will land at the next boundary
	// (segSize boundaries at multiples of 256). Crossings must equal
	// the number of segment boundaries between min(start) and the
	// last reservation's end LSN.
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	// All reservations are in segment 1 or later (none stays in
	// segment 0 since 230+40 > 256).
	lastEnd := out[len(out)-1] + sz
	firstSeg := uint64(230) / segSize       // segment 0 (where we started)
	lastSeg := (lastEnd - 1) / segSize      // segment index of final byte
	wantCrossings := int64(lastSeg - firstSeg)
	if crossings.Load() != wantCrossings {
		t.Fatalf("crossings: got %d want %d (segments %d..%d, ends @%d)",
			crossings.Load(), wantCrossings, firstSeg, lastSeg, lastEnd)
	}

	// All reservations must be disjoint and lie within segment 1+.
	for i := 1; i < len(out); i++ {
		if out[i] < out[i-1]+sz {
			t.Fatalf("overlap: out[%d]=%d out[%d]=%d sz=%d",
				i-1, out[i-1], i, out[i], sz)
		}
		// Each reservation occupies a contiguous range inside a
		// single segment (no record straddles a boundary).
		startSeg := out[i] / segSize
		endSeg := (out[i] + sz - 1) / segSize
		if startSeg != endSeg {
			t.Fatalf("out[%d]=%d straddles segment boundary (seg %d -> %d)",
				i, out[i], startSeg, endSeg)
		}
	}
}

func TestLSNAllocatorReserveAcrossTwoBoundaries(t *testing.T) {
	const segSize uint64 = 100
	type call struct{ start, boundary uint64 }
	var got []call
	a := newLSNAllocator(0, segSize, func(s, b uint64) {
		got = append(got, call{s, b})
	})

	// Use size <= segSize per the primitive's contract.
	if g := a.reserve(80); g != 0 {
		t.Fatalf("reserve 80@0: got %d want 0", g)
	}
	if len(got) != 0 {
		t.Fatalf("no crossing yet, hook fired: %v", got)
	}
	// Cross into seg 1.
	if g := a.reserve(50); g != 100 {
		t.Fatalf("reserve crossing into seg 1: got %d want 100", g)
	}
	if len(got) != 1 || got[0] != (call{80, 100}) {
		t.Fatalf("first crossing hook: %v want [{80,100}]", got)
	}
	// Fill segment 1 to 195; no crossing.
	if g := a.reserve(45); g != 150 {
		t.Fatalf("reserve 45@150: got %d want 150", g)
	}
	if len(got) != 1 {
		t.Fatalf("hook fired again unexpectedly: %v", got)
	}
	// Cross into seg 2.
	if g := a.reserve(20); g != 200 {
		t.Fatalf("reserve crossing into seg 2: got %d want 200", g)
	}
	if len(got) != 2 || got[1] != (call{195, 200}) {
		t.Fatalf("second crossing hook: %v want [...,{195,200}]", got)
	}
}
