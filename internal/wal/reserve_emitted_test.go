package wal

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// reserveEmittedAndPublish closes the predict-vs-reserve race left open
// by foundation 17 (predictEmittedSize). The tests below pin:
//   - the happy path produces total/leading equal to a standalone
//     predictEmittedSize call at the resulting start LSN;
//   - the cross-segment slow path re-predicts at boundary, fires the
//     onCrossSegment hook exactly once with the correct payload, and
//     stamps prev = gap-start so xl_prev chain integrity is preserved
//     across the boundary;
//   - the stripe slot is published under posMu (concurrent readers see
//     a consistent (curr, stripe) snapshot);
//   - all nil/range-violation cases panic before any side effect.
//
// Test segSize values are always multiples of XLOGBlockSize (8192) so
// the predict-helper observes a long page header at segment boundaries
// (pageHeaderSizeAt's segment-boundary branch fires only when
// pos % XLOGBlockSize == 0). Real PG always satisfies this; production
// goopg configs default to 16 MiB segments.

func TestReserveEmittedAndPublishHappyPathMatchesStandalonePredict(t *testing.T) {
	t.Parallel()
	pos := newInsertPosTracker(1, 0, 1<<20, nil)
	tr := newInsertionTracker()

	const recordLen = 100
	start, prev, total, leading := pos.reserveEmittedAndPublish(recordLen, 2, tr)
	if start != 1 || prev != 0 {
		t.Fatalf("first reserve: got (start=%d, prev=%d), want (1, 0)", start, prev)
	}
	wantTotal, wantLeading := predictEmittedSize(recordLen, int64(start), 1<<20)
	if total != wantTotal || leading != wantLeading {
		t.Fatalf("size: got (total=%d, leading=%d), want (%d, %d)",
			total, leading, wantTotal, wantLeading)
	}
	gotCurr, gotPrev := pos.load()
	if gotCurr != start+uint64(total) || gotPrev != start {
		t.Fatalf("post-reserve load: got (curr=%d, prev=%d), want (%d, %d)",
			gotCurr, gotPrev, start+uint64(total), start)
	}
	if got := tr.insertingAt(2); got != int64(start) {
		t.Fatalf("stripe 2 insertingAt = %d, want %d", got, start)
	}
	for s := 0; s < appendLockStripes; s++ {
		if s == 2 {
			continue
		}
		if got := tr.insertingAt(s); got != lsnIdle {
			t.Fatalf("stripe %d insertingAt = %d, want lsnIdle", s, got)
		}
	}
}

func TestReserveEmittedAndPublishPageBoundaryGetsShortHeader(t *testing.T) {
	t.Parallel()
	// Start at the second page (XLOGBlockSize) inside segment 0.
	// pos % XLOGBlockSize == 0, pos % segSize != 0 → short header (24 B).
	const segSize = 1 << 20
	pos := newInsertPosTracker(XLOGBlockSize, 0, segSize, nil)
	tr := newInsertionTracker()

	const recordLen = 100
	start, _, total, leading := pos.reserveEmittedAndPublish(recordLen, 0, tr)
	if start != XLOGBlockSize {
		t.Fatalf("start=%d, want %d", start, XLOGBlockSize)
	}
	if leading != SizeOfXLogShortPHD {
		t.Fatalf("leading=%d, want SizeOfXLogShortPHD=%d", leading, SizeOfXLogShortPHD)
	}
	if total != SizeOfXLogShortPHD+recordLen {
		t.Fatalf("total=%d, want %d (short PHD + recordLen)",
			total, SizeOfXLogShortPHD+recordLen)
	}
}

func TestReserveEmittedAndPublishSegmentBoundaryGetsLongHeader(t *testing.T) {
	t.Parallel()
	const segSize = 1 << 20
	pos := newInsertPosTracker(segSize, 0, segSize, nil)
	tr := newInsertionTracker()

	const recordLen = 100
	start, _, total, leading := pos.reserveEmittedAndPublish(recordLen, 5, tr)
	if start != segSize {
		t.Fatalf("start=%d, want %d", start, segSize)
	}
	if leading != SizeOfXLogLongPHD {
		t.Fatalf("leading=%d, want SizeOfXLogLongPHD=%d", leading, SizeOfXLogLongPHD)
	}
	if total != SizeOfXLogLongPHD+recordLen {
		t.Fatalf("total=%d, want %d (long PHD + recordLen)",
			total, SizeOfXLogLongPHD+recordLen)
	}
}

func TestReserveEmittedAndPublishCrossSegmentEmitsPadAndRePredicts(t *testing.T) {
	t.Parallel()
	// segSize=2 pages (16 KiB) so the boundary is reachable in a few
	// hundred bytes; both segSize and the boundary are XLOGBlockSize-
	// aligned so predictEmittedSize observes the long header at the
	// boundary.
	const segSize uint64 = 2 * XLOGBlockSize
	// Start 6 pages into segment 1, near the segment-2 boundary.
	// Specifically: 50 bytes before the (segment-2) boundary 2*segSize.
	// Pick mid-page so leading at startPos is 0.
	startPos := 2*segSize - 50 // = 32718
	pos := newInsertPosTracker(startPos, 999, segSize, nil)
	tr := newInsertionTracker()

	type gap struct {
		start, boundary, prev uint64
	}
	var gaps []gap
	pos.onCrossSegment = func(s, b, p uint64) bool {
		gaps = append(gaps, gap{s, b, p})
		return true
	}

	const recordLen = 100
	start, prev, total, leading := pos.reserveEmittedAndPublish(recordLen, 1, tr)

	expectedBoundary := 2 * segSize
	if len(gaps) != 1 {
		t.Fatalf("onCrossSegment fired %d times, want 1: %+v", len(gaps), gaps)
	}
	if gaps[0] != (gap{startPos, expectedBoundary, 999}) {
		t.Fatalf("gap payload = %+v, want {%d %d 999}", gaps[0], startPos, expectedBoundary)
	}
	if start != expectedBoundary {
		t.Fatalf("start=%d, want %d", start, expectedBoundary)
	}
	if prev != startPos {
		t.Fatalf("prev=%d, want %d (pad record's start)", prev, startPos)
	}
	if leading != SizeOfXLogLongPHD {
		t.Fatalf("leading=%d, want %d", leading, SizeOfXLogLongPHD)
	}
	if total != SizeOfXLogLongPHD+recordLen {
		t.Fatalf("total=%d, want %d (long PHD + recordLen)",
			total, SizeOfXLogLongPHD+recordLen)
	}
	gotCurr, gotPrev := pos.load()
	wantStoredPrev := expectedBoundary + uint64(SizeOfXLogLongPHD) // content start of the triggering record
	if gotCurr != expectedBoundary+uint64(total) || gotPrev != wantStoredPrev {
		t.Fatalf("post-cross load: got (curr=%d, prev=%d), want (%d, %d)",
			gotCurr, gotPrev, expectedBoundary+uint64(total), wantStoredPrev)
	}
	if got := tr.insertingAt(1); got != int64(expectedBoundary) {
		t.Fatalf("stripe 1 insertingAt = %d, want %d (post-boundary start)",
			got, expectedBoundary)
	}
}

func TestReserveEmittedAndPublishCrossSegmentNoHookSkipsNotify(t *testing.T) {
	t.Parallel()
	const segSize uint64 = 2 * XLOGBlockSize
	startPos := 2*segSize - 50
	pos := newInsertPosTracker(startPos, 999, segSize, nil)
	tr := newInsertionTracker()
	start, prev, total, leading := pos.reserveEmittedAndPublish(100, 0, tr)
	if start != 2*segSize || prev != startPos {
		t.Fatalf("got (start=%d, prev=%d), want (%d, %d)",
			start, prev, 2*segSize, startPos)
	}
	if leading != SizeOfXLogLongPHD || total != SizeOfXLogLongPHD+100 {
		t.Fatalf("size: got (total=%d, leading=%d), want (%d, %d)",
			total, leading, SizeOfXLogLongPHD+100, SizeOfXLogLongPHD)
	}
}

func TestReserveEmittedAndPublishInvalidRecordLenPanics(t *testing.T) {
	t.Parallel()
	cases := []int{0, -1, -100}
	for _, recordLen := range cases {
		recordLen := recordLen
		t.Run("", func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("recordLen=%d: expected panic", recordLen)
				}
			}()
			pos := newInsertPosTracker(1, 0, 1<<20, nil)
			tr := newInsertionTracker()
			pos.reserveEmittedAndPublish(recordLen, 0, tr)
		})
	}
}

func TestReserveEmittedAndPublishNilTrackerPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatalf("expected panic on nil tracker")
		}
	}()
	pos := newInsertPosTracker(1, 0, 1<<20, nil)
	pos.reserveEmittedAndPublish(100, 0, nil)
}

func TestReserveEmittedAndPublishInvalidStripePanics(t *testing.T) {
	t.Parallel()
	cases := []int{-1, appendLockStripes, appendLockStripes + 1, 1 << 30}
	for _, stripe := range cases {
		stripe := stripe
		t.Run("", func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("stripe=%d: expected panic", stripe)
				}
			}()
			pos := newInsertPosTracker(1, 0, 1<<20, nil)
			tr := newInsertionTracker()
			pos.reserveEmittedAndPublish(100, stripe, tr)
		})
	}
}

func TestReserveEmittedAndPublishConcurrentNoRaceMatchesPredictAtStart(t *testing.T) {
	t.Parallel()
	// 8 stripes × 200 records × 16-byte records → 25 600 bytes of
	// payload; segSize big enough so no segment crossings. Each call
	// must return (total, leading) that exactly equals a standalone
	// predict at the returned start; this pins the race-closure
	// property — peer reservations can advance curr arbitrarily
	// between calls, but never within a single call.
	const (
		segSize    uint64 = 1 << 24 // 16 MiB
		workers           = 8
		perWorker         = 200
		recordLen         = 16
	)
	pos := newInsertPosTracker(1, 0, segSize, nil)
	tr := newInsertionTracker()

	type result struct {
		start, prev    uint64
		total, leading int
	}
	results := make(chan result, workers*perWorker)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				s, p, tot, lead := pos.reserveEmittedAndPublish(recordLen, w, tr)
				results <- result{s, p, tot, lead}
				tr.setInsertingAt(w, lsnIdle)
			}
		}()
	}
	wg.Wait()
	close(results)

	starts := make(map[uint64]struct{}, workers*perWorker)
	for r := range results {
		wantTotal, wantLeading := predictEmittedSize(recordLen, int64(r.start), int64(segSize))
		if r.total != wantTotal || r.leading != wantLeading {
			t.Fatalf("start=%d: got (total=%d, leading=%d), want (%d, %d)",
				r.start, r.total, r.leading, wantTotal, wantLeading)
		}
		if _, dup := starts[r.start]; dup {
			t.Fatalf("duplicate start LSN %d across reservations", r.start)
		}
		starts[r.start] = struct{}{}
	}
}

func TestReserveEmittedAndPublishConcurrentChainAndStripePublishConsistent(t *testing.T) {
	t.Parallel()
	// Race-closure assertion: a reader that takes posMu (via direct
	// Lock) while writers interleave reserveEmittedAndPublish calls
	// must see, for every non-idle stripe slot v, v < curr. The old
	// two-step (reserve then setInsertingAt) admitted a window where
	// curr was advanced but the stripe slot still read lsnIdle — the
	// new joint-atomic primitive forbids that window.
	//
	// Non-vacuity (AI-20260810-011258-001). The assertion only does real
	// work on a snapshot that catches a stripe mid-flight (v != lsnIdle),
	// so that — not "the reader ran at all" — is what has to be proven to
	// have happened. Two scheduling hazards left it vacuous, and the old
	// `observed > 0` guard covered only the first:
	//  1. the reader goroutine could stay unscheduled until after
	//     close(stop) and take zero snapshots. This is what reddened the
	//     nightly race-gate; it reproduces on 100% of runs under
	//     GOMAXPROCS=1 -race, and intermittently at full package load.
	//  2. even once scheduled, the reader could be starved across the
	//     entire (sub-millisecond) write burst and take only snapshots of
	//     idle stripes — `observed > 0` holds, yet nothing was asserted.
	// Both are now closed by construction rather than left to chance: the
	// writers do not start until the reader is confirmed live, and the
	// burst repeats until the reader has actually witnessed a stripe in
	// flight.
	const (
		segSize    uint64 = 1 << 24
		workers           = 8
		perWorker         = 500
		recordLen         = 16
	)
	pos := newInsertPosTracker(1, 0, segSize, nil)
	tr := newInsertionTracker()

	stop := make(chan struct{})
	// witnessed counts only snapshots that caught at least one stripe
	// mid-flight, i.e. snapshots on which the assertion below was
	// evaluated non-trivially.
	var witnessed int64
	readerLive := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		live := readerLive
		for {
			select {
			case <-stop:
				return
			default:
			}
			pos.posMu.Lock()
			curr := pos.curr
			inFlight := false
			for s := 0; s < appendLockStripes; s++ {
				v := tr.insertingAt(s)
				if v != lsnIdle {
					inFlight = true
					if uint64(v) >= curr {
						t.Errorf("snapshot violation: stripe %d insertingAt=%d, curr=%d",
							s, v, curr)
					}
				}
			}
			pos.posMu.Unlock()
			if inFlight {
				atomic.AddInt64(&witnessed, 1)
			}
			if live != nil {
				close(live)
				live = nil
			}
		}
	}()

	// Hazard 1: block until the reader has completed a snapshot, so it is
	// known to be running rather than merely spawned.
	<-readerLive

	// Hazard 2: repeat the burst until the reader has caught a writer in
	// flight. Each round is the original workers x perWorker workload; the
	// round bound exists only so a wedged reader fails the test rather
	// than hanging it.
	const maxRounds = 200
	rounds := 0
	for ; rounds < maxRounds && atomic.LoadInt64(&witnessed) == 0; rounds++ {
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			w := w
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < perWorker; i++ {
					_, _, _, _ = pos.reserveEmittedAndPublish(recordLen, w, tr)
					tr.setInsertingAt(w, lsnIdle)
				}
			}()
		}
		wg.Wait()
	}

	close(stop)
	<-readerDone

	if atomic.LoadInt64(&witnessed) == 0 {
		t.Fatalf("reader never caught a stripe in flight across %d rounds of %dx%d "+
			"reservations — race-closure assertion vacuously true",
			rounds, workers, perWorker)
	}
}

func TestReserveEmittedAndPublishCrossSegmentChainIntegrity(t *testing.T) {
	t.Parallel()
	// Walk a multi-record sequence that hits a segment boundary
	// mid-record. Chain must be: rec1.prev=999, rec1.start=startPos1;
	// (cross-segment fires for rec2) pad.prev=rec1.start, rec2.prev=pad.start,
	// rec2.start=boundary.
	const segSize uint64 = 2 * XLOGBlockSize
	// Start rec1 with room for exactly 100 emitted bytes before the
	// boundary. mid-page → leading 0; total=100; end=boundary exactly.
	startPos1 := 2*segSize - 100 // mid-page (16284 % 8192 != 0)
	pos := newInsertPosTracker(startPos1, 999, segSize, nil)
	tr := newInsertionTracker()

	type gap struct {
		start, boundary, prev uint64
	}
	var gaps []gap
	pos.onCrossSegment = func(s, b, p uint64) bool {
		gaps = append(gaps, gap{s, b, p})
		return true
	}

	// Rec1: 100 bytes at startPos1 → no leading, no page crossings
	// (next page at startPos1+100=boundary which is also a page
	// boundary, so consumed reaches recordLen at pos==boundary and
	// the loop exits). Stays in segment.
	s1, p1, t1, _ := pos.reserveEmittedAndPublish(100, 0, tr)
	if s1 != startPos1 || p1 != 999 || t1 != 100 {
		t.Fatalf("rec1: got (start=%d, prev=%d, total=%d), want (%d, 999, 100)",
			s1, p1, t1, startPos1)
	}
	if len(gaps) != 0 {
		t.Fatalf("rec1 should not cross: gaps=%+v", gaps)
	}
	tr.setInsertingAt(0, lsnIdle)

	// Rec2: 100 bytes at boundary (now t.curr == 2*segSize, page+segment
	// aligned). Leading=long(40); total=140; lands at boundary; prev=
	// startPos1 (rec1's start). No cross-segment because we're at the
	// boundary precisely (not crossing it).
	s2, p2, t2, l2 := pos.reserveEmittedAndPublish(100, 0, tr)
	if s2 != 2*segSize || p2 != startPos1 {
		t.Fatalf("rec2: got (start=%d, prev=%d), want (%d, %d)",
			s2, p2, 2*segSize, startPos1)
	}
	if t2 != SizeOfXLogLongPHD+100 || l2 != SizeOfXLogLongPHD {
		t.Fatalf("rec2: got (total=%d, leading=%d), want (%d, %d)",
			t2, l2, SizeOfXLogLongPHD+100, SizeOfXLogLongPHD)
	}
	if len(gaps) != 0 {
		t.Fatalf("rec2 should not cross either (curr==boundary, not >): gaps=%+v", gaps)
	}
}

func TestReserveEmittedAndPublishCrossSegmentLeadingDiffersFromPredictAtCurr(t *testing.T) {
	t.Parallel()
	// Pins the load-bearing property: when a reservation would
	// straddle, the re-predict at boundary gives a DIFFERENT leading
	// header size than predict-at-curr. At a mid-page startPos,
	// predict-at-curr has leading=0; at the boundary, predict-at-
	// boundary has leading=long(40). The total may coincide (both
	// pay the 40 B long header — just at different positions within
	// the byte stream), but `leading` deterministically differs.
	const segSize uint64 = 2 * XLOGBlockSize
	startPos := 2*segSize - 50
	pos := newInsertPosTracker(startPos, 0, segSize, nil)
	tr := newInsertionTracker()

	_, currLeadingAtCurr := predictEmittedSize(100, int64(startPos), int64(segSize))
	if currLeadingAtCurr != 0 {
		t.Fatalf("test invariant: predict at mid-page must yield leading=0 (got %d)",
			currLeadingAtCurr)
	}
	_, boundaryLeading := predictEmittedSize(100, int64(2*segSize), int64(segSize))
	if boundaryLeading != SizeOfXLogLongPHD {
		t.Fatalf("test invariant: predict at segment boundary must yield long PHD (got %d)",
			boundaryLeading)
	}

	_, _, _, leading := pos.reserveEmittedAndPublish(100, 0, tr)
	if leading != boundaryLeading {
		t.Fatalf("leading=%d, want %d (foundation 18 re-predicts at boundary)",
			leading, boundaryLeading)
	}
}

func TestReserveEmittedAndPublishWatchdog(t *testing.T) {
	t.Parallel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		const (
			segSize    uint64 = 1 << 20
			workers           = 4
			perWorker         = 100
			recordLen         = 16
		)
		pos := newInsertPosTracker(1, 0, segSize, nil)
		tr := newInsertionTracker()
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			w := w
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < perWorker; i++ {
					_, _, _, _ = pos.reserveEmittedAndPublish(recordLen, w, tr)
					tr.setInsertingAt(w, lsnIdle)
				}
			}()
		}
		wg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("reserveEmittedAndPublish hung — possible deadlock under contention")
	}
}

// TestReserveEmittedAndPublishSmallGapSkipsNoop verifies the fix for the
// "pad record padLen below minimum 24" panic. When a record would create a
// gap of < 24 bytes before a segment boundary, reserveEmittedAndPublish must
// advance curr to the boundary and the new record's xl_prev must point to the
// last valid record before the gap.
//
// M0131-S30.6 changed WHO decides: the hook is now called for every crossing
// (it zero-fills a gap too small to hold a record, so the segment file has no
// hole) and reports back whether it wrote a real pad RECORD. Only a real pad
// advances prev. This test therefore pins "hook returns false ⇒ prev does not
// advance onto the gap" rather than "hook is not called".
func TestReserveEmittedAndPublishSmallGapSkipsNoop(t *testing.T) {
	t.Parallel()
	// Use segSize = 2 * XLOGBlockSize (16 KiB) so boundary is small enough
	// to position curr near it without large data. Place curr 8 bytes before
	// the boundary (gap = 8, below minimum 24).
	segSize := uint64(2 * XLOGBlockSize)
	boundary := segSize // first boundary
	gapStart := boundary - 8

	hookFired := false
	pos := newInsertPosTracker(gapStart, 0, segSize, func(start, bound, prev uint64) bool {
		hookFired = true
		// The composer zero-fills a sub-24-byte gap and reports padded=false.
		return false
	})
	tr := newInsertionTracker()

	// A record of size 32 starting at gapStart would straddle boundary:
	// gapStart + 32 > boundary (boundary - 8 + 32 = boundary + 24 > boundary).
	// The gap is 8 bytes — onCrossSegment must NOT be called (panic avoidance).
	start, prev, total, leading := pos.reserveEmittedAndPublish(32, 0, tr)
	tr.setInsertingAt(0, lsnIdle)

	if !hookFired {
		t.Fatal("onCrossSegment was not called for a sub-24-byte gap — the gap would be left unwritten, leaving a hole in the segment file (M0131-S30.6)")
	}
	if start < boundary {
		t.Fatalf("record should start at boundary (%d) or later, got start=%d", boundary, start)
	}
	// prev points to the record before the gap (= 0 in this test), NOT to
	// gapStart: the zero-filled gap carries no record to link to.
	if prev != 0 {
		t.Fatalf("prev=%d, want 0 — xl_prev must skip a gap that holds no record", prev)
	}
	_ = total
	_ = leading
}
