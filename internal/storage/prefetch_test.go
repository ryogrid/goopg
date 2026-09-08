package storage

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// E-19 slice S3 — StartRead / Finish / Abort.
//
// Everything here is a correctness surface no values suite can see: a leaked
// pin is not a wrong answer, a dropped checksum verification is not a wrong
// answer until the disk lies, and a deadlock is not an answer at all.

// ---------------------------------------------------------------------------
// A bounded AIO engine, built to reproduce the deadlock the design found
// ---------------------------------------------------------------------------

// boundedEngine is methodWorker's shape reduced to what the deadlock argument
// needs: a queue of fixed capacity whose Submit BLOCKS when full, drained by a
// fixed number of worker goroutines. afterOp runs on the WORKER goroutine
// after each op, which is where the design's edge (b) lives — real completion
// work wants pinMu.
type boundedEngine struct {
	queue   chan *boundedHandle
	wg      sync.WaitGroup
	stop    chan struct{}
	afterOp func()
}

type boundedHandle struct {
	op   AIOSubmitOp
	done chan struct{}
	n    int
	err  error
}

func (h *boundedHandle) Wait() (int, error) {
	<-h.done
	return h.n, h.err
}

func newBoundedEngine(capacity, workers int, afterOp func()) *boundedEngine {
	e := &boundedEngine{
		queue:   make(chan *boundedHandle, capacity),
		stop:    make(chan struct{}),
		afterOp: afterOp,
	}
	for i := 0; i < workers; i++ {
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			for {
				select {
				case <-e.stop:
					return
				case h := <-e.queue:
					h.n, h.err = h.op.File.ReadAt(h.op.Buffer, h.op.Offset)
					if h.op.OnComplete != nil {
						h.op.OnComplete()
					}
					close(h.done)
					if e.afterOp != nil {
						e.afterOp()
					}
				}
			}
		}()
	}
	return e
}

func (e *boundedEngine) Submit(op AIOSubmitOp) AIOHandle {
	h := &boundedHandle{op: op, done: make(chan struct{})}
	e.queue <- h // BLOCKS when the queue is full — methodWorker's contract
	return h
}

func (e *boundedEngine) Close() {
	close(e.stop)
	e.wg.Wait()
}

// TestStartReadDoesNotHoldPinMuAcrossSubmit is the deadlock gate, and it is
// written so that it HANGS (and then fails on the watchdog) against the naive
// implementation the design's §4.1a finding 2 describes.
//
// The cycle it builds is the real one, scaled down so it is deterministic:
//   - the engine's queue holds 2 ops and Submit blocks when it is full;
//   - there is ONE worker, and after every op it takes pinMu — standing in for
//     the completion work that must read slotWaiters under pinMu;
//   - the scan opens a window of 8 blocks, i.e. deeper than the queue.
//
// If StartRead submitted with pinMu held, the third StartRead would block in
// Submit holding pinMu while the only worker that could drain the queue is
// blocked wanting pinMu. Neither ever moves. Because submission happens with
// pinMu released, it completes.
func TestStartReadDoesNotHoldPinMuAcrossSubmit(t *testing.T) {
	mgr, pool, rel := prefetchTestPool(t, 64)
	const n = 8
	extendN(t, mgr, rel, n)
	// Start cold: the blocks were just written, so they are resident.
	pool.InvalidateRel(rel)

	eng := newBoundedEngine(2, 1, func() {
		// Edge (b): completion work that wants pinMu.
		pool.pinMu.Lock()
		pool.pinMu.Unlock() //nolint:staticcheck // deliberate: model the acquire
	})
	defer eng.Close()
	mgr.SetAIO(eng)
	defer mgr.SetAIO(nil)

	done := make(chan error, 1)
	go func() {
		ops := make([]*ReadOp, 0, n)
		for i := 0; i < n; i++ {
			op, err := pool.StartRead(BufferTag{Rel: rel, Block: BlockNumber(i)})
			if err != nil {
				done <- err
				return
			}
			ops = append(ops, op)
		}
		for _, op := range ops {
			if err := op.Finish(); err != nil {
				done <- err
				return
			}
			pool.Unpin(op.Slot())
		}
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("window of %d over a 2-deep queue: %v", n, err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("DEADLOCK: a window deeper than the AIO queue did not complete. " +
			"Submission must happen with pinMu released (E-19 design §4.1a finding 2)")
	}

	if total, _ := pool.TotalPinCount(); total != 0 {
		t.Fatalf("window leaked %d pins", total)
	}
}

// ---------------------------------------------------------------------------
// The thing E-19 exists for
// ---------------------------------------------------------------------------

// TestStartReadInstallsIntoThePool is the entire item in one assertion. The
// deleted Pool.Prefetch read into a scratch buffer and dropped it, so the
// following Pin did the whole read again; StartRead reads into the SLOT'S page
// and publishes it, so the following Pin is a hit and issues no read at all.
func TestStartReadInstallsIntoThePool(t *testing.T) {
	mgr, pool, rel := prefetchTestPool(t, 32)
	const n = 6
	extendN(t, mgr, rel, n)
	// Reference bytes AS THEY ARE ON DISK (the checksum is stamped at write
	// time, so the in-memory page handed to Extend is not what a read returns).
	want := blockBufs(n)
	for i := 0; i < n; i++ {
		if err := mgr.ReadBlock(rel, BlockNumber(i), want[i]); err != nil {
			t.Fatal(err)
		}
	}
	pool.InvalidateRel(rel)

	readsBefore := pool.sharedReadCount.Load()

	ops := make([]*ReadOp, n)
	for i := 0; i < n; i++ {
		op, err := pool.StartRead(BufferTag{Rel: rel, Block: BlockNumber(i)})
		if err != nil {
			t.Fatalf("StartRead %d: %v", i, err)
		}
		if op.AlreadyValid() {
			t.Fatalf("block %d reported resident after InvalidateRel", i)
		}
		ops[i] = op
	}
	for i, op := range ops {
		if err := op.Finish(); err != nil {
			t.Fatalf("Finish %d: %v", i, err)
		}
		// The page must be the real page, in the slot, not a scratch copy.
		if got := op.Slot().page; string(got) != string(want[i]) {
			t.Fatalf("block %d: slot page does not match what was written", i)
		}
		pool.Unpin(op.Slot())
	}

	readsAfterPrefetch := pool.sharedReadCount.Load()
	if readsAfterPrefetch-readsBefore != n {
		t.Fatalf("prefetching %d blocks counted %d reads, want %d",
			n, readsAfterPrefetch-readsBefore, n)
	}

	// The payoff: consuming the same blocks now costs ZERO further reads.
	for i := 0; i < n; i++ {
		s, err := pool.Pin(BufferTag{Rel: rel, Block: BlockNumber(i)})
		if err != nil {
			t.Fatalf("Pin %d: %v", i, err)
		}
		pool.Unpin(s)
	}
	if got := pool.sharedReadCount.Load(); got != readsAfterPrefetch {
		t.Fatalf("consuming the prefetched blocks issued %d more reads — "+
			"the prefetch did not install into the pool", got-readsAfterPrefetch)
	}
	if total, _ := pool.TotalPinCount(); total != 0 {
		t.Fatalf("leaked %d pins", total)
	}
}

// TestStartReadVerifiesChecksums is requirement 2 of the slice: a prefetch
// path that silently skipped verification would be a correctness regression no
// values suite could catch, because the wrong bytes would simply be believed.
// Verification lives one layer down (relFile.ReadAt / readBlock), which is
// what makes it unbypassable — this test is what proves the claim rather than
// asserting it.
func TestStartReadVerifiesChecksums(t *testing.T) {
	mgr, pool, rel := prefetchTestPool(t, 16)
	extendN(t, mgr, rel, 3)
	pool.InvalidateRel(rel)

	// Corrupt block 1 on disk, underneath the pool.
	f, err := mgr.relFile(rel)
	if err != nil {
		t.Fatal(err)
	}
	bad := make([]byte, BlockSize)
	if _, err := f.f.ReadAt(bad, BlockSize); err != nil {
		t.Fatal(err)
	}
	bad[BlockSize-1] ^= 0xFF // flips a byte the checksum covers
	if _, err := f.f.WriteAt(bad, BlockSize); err != nil {
		t.Fatal(err)
	}

	op, err := pool.StartRead(BufferTag{Rel: rel, Block: 1})
	if err != nil {
		t.Fatalf("StartRead: %v", err)
	}
	if ferr := op.Finish(); ferr == nil {
		pool.Unpin(op.Slot())
		t.Fatal("Finish accepted a page that fails its checksum — verification " +
			"has been dropped from the prefetch path")
	}
	// A failed Finish must leave nothing behind.
	if total, _ := pool.TotalPinCount(); total != 0 {
		t.Fatalf("failed Finish leaked %d pins", total)
	}
	if idx, _ := pool.bm.Lookup(BufferTag{Rel: rel, Block: 1}); idx >= 0 {
		t.Fatalf("failed Finish left the tag mapped to slot %d", idx)
	}
}

// TestConcurrentPinOfInFlightBlockWaitsForValidity is requirement 3: while a
// prefetch of a block is in flight, a concurrent Pin of the SAME block must
// block on the slot becoming VALID, and must not issue a second read of it.
func TestConcurrentPinOfInFlightBlockWaitsForValidity(t *testing.T) {
	mgr, pool, rel := prefetchTestPool(t, 32)
	extendN(t, mgr, rel, 4)
	pool.InvalidateRel(rel)

	// A single-worker engine we can hold: the op sits in the queue until we
	// let the worker run, so the read is genuinely in flight for a while.
	release := make(chan struct{})
	eng := newBoundedEngine(4, 1, nil)
	defer eng.Close()
	mgr.SetAIO(&gatedEngine{inner: eng, gate: release})
	defer mgr.SetAIO(nil)

	tag := BufferTag{Rel: rel, Block: 2}
	op, err := pool.StartRead(tag)
	if err != nil {
		t.Fatalf("StartRead: %v", err)
	}
	if op.AlreadyValid() {
		t.Fatal("block reported resident after InvalidateRel")
	}

	readsBefore := pool.sharedReadCount.Load()
	pinned := make(chan *Slot, 1)
	waited := make(chan struct{})
	pool.OnBufferIOWait = func() { close(waited) }
	go func() {
		s, err := pool.Pin(tag)
		if err != nil {
			t.Errorf("concurrent Pin: %v", err)
			pinned <- nil
			return
		}
		pinned <- s
	}()

	// The concurrent Pin must PARK, not proceed and not re-read.
	select {
	case <-waited:
	case s := <-pinned:
		if s != nil {
			pool.Unpin(s)
		}
		t.Fatal("a concurrent Pin of an in-flight block completed before the " +
			"read did — it either re-read the block or read an invalid slot")
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent Pin neither parked nor completed")
	}

	// Let the read land and publish it.
	close(release)
	if err := op.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	var s *Slot
	select {
	case s = <-pinned:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent Pin never woke after the prefetch published")
	}
	if s == nil {
		t.Fatal("concurrent Pin failed")
	}
	if got := pool.sharedReadCount.Load(); got != readsBefore+1 {
		t.Fatalf("read count moved by %d; the waiter issued its own read of an "+
			"in-flight block", got-readsBefore-1)
	}
	pool.Unpin(s)
	pool.Unpin(op.Slot())
	if total, _ := pool.TotalPinCount(); total != 0 {
		t.Fatalf("leaked %d pins", total)
	}
}

// gatedEngine defers submission to inner until gate is closed, so a test can
// hold a read "in flight" for as long as it likes.
type gatedEngine struct {
	inner AIOEngine
	gate  chan struct{}
}

func (g *gatedEngine) Submit(op AIOSubmitOp) AIOHandle {
	h := &gatedHandle{done: make(chan struct{})}
	go func() {
		<-g.gate
		inner := g.inner.Submit(op)
		h.n, h.err = inner.Wait()
		close(h.done)
	}()
	return h
}

type gatedHandle struct {
	done chan struct{}
	n    int
	err  error
}

func (h *gatedHandle) Wait() (int, error) {
	<-h.done
	return h.n, h.err
}

// TestAbortReadReturnsTheSlotWithoutRecyclingIt covers §4.1a finding 5, which
// the design flags as the one a pin-BALANCE test would pass while broken:
// releaseVictimSlot does state.Store(0), wiping the generation out from under
// a *Slot the caller still holds, so the slot can be re-claimed for a
// different tag while the caller believes it owns it. Abort must therefore
// keep the pin until it is done, and the test checks ownership, not counts.
func TestAbortReadReturnsTheSlotWithoutRecyclingIt(t *testing.T) {
	mgr, pool, rel := prefetchTestPool(t, 16)
	extendN(t, mgr, rel, 4)
	pool.InvalidateRel(rel)

	tag := BufferTag{Rel: rel, Block: 3}
	op, err := pool.StartRead(tag)
	if err != nil {
		t.Fatalf("StartRead: %v", err)
	}
	s := op.Slot()

	// While the op is live the slot is pinned, so no clock sweep can take it.
	if got := statePin(s.state.Load()); got != 1 {
		t.Fatalf("in-flight slot has pin %d, want 1 — StartRead must pin before "+
			"it submits (findings 3 and 4)", got)
	}
	if !stateIO(s.state.Load()) {
		t.Fatal("in-flight slot does not have the IO bit set")
	}

	op.Abort()

	if total, pinned := pool.TotalPinCount(); total != 0 || pinned != 0 {
		t.Fatalf("Abort left %d pins over %d slots", total, pinned)
	}
	if st := s.state.Load(); stateIO(st) || stateValid(st) {
		t.Fatalf("aborted slot state %#x still claims IO or validity", st)
	}
	if idx, _ := pool.bm.Lookup(tag); idx >= 0 {
		t.Fatalf("Abort left tag %v mapped to slot %d", tag, idx)
	}
	// A second Abort must be a no-op, not an unpin underflow panic.
	op.Abort()

	// And the block must still be readable the ordinary way.
	got, err := pool.Pin(tag)
	if err != nil {
		t.Fatalf("Pin after Abort: %v", err)
	}
	pool.Unpin(got)
}

// TestInvalidateTreatsInFlightSlotAsPinned pins the reasoning in this file's
// header: an in-flight prefetch slot is skipped by the invalidators for the
// same reason an ordinary pinned buffer is, so S3 introduces no new
// truncate/drop exposure and needs no new mechanism for one.
func TestInvalidateTreatsInFlightSlotAsPinned(t *testing.T) {
	mgr, pool, rel := prefetchTestPool(t, 16)
	extendN(t, mgr, rel, 4)
	pool.InvalidateRel(rel)

	// Baseline: an ordinary PINNED valid buffer survives InvalidateRel.
	ref := BufferTag{Rel: rel, Block: 0}
	refSlot, err := pool.Pin(ref)
	if err != nil {
		t.Fatal(err)
	}
	pool.InvalidateRel(rel)
	if idx, _ := pool.bm.Lookup(ref); idx < 0 {
		t.Skip("InvalidateRel evicts pinned buffers on this build; the header's " +
			"equivalence argument would need revisiting")
	}

	tag := BufferTag{Rel: rel, Block: 1}
	op, err := pool.StartRead(tag)
	if err != nil {
		t.Fatalf("StartRead: %v", err)
	}
	pool.InvalidateRel(rel)
	if idx, _ := pool.bm.Lookup(tag); idx < 0 {
		t.Fatal("InvalidateRel removed an in-flight slot's mapping — the two " +
			"cases are NOT equivalent and the header is wrong")
	}
	if err := op.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	pool.Unpin(op.Slot())
	pool.Unpin(refSlot)
}

// TestStartReadConcurrentWindowsAreBalanced is §4.2 hazard 3's arm: N
// goroutines running overlapping look-ahead windows on one pool, gated under
// -race, with the pin sum asserted back to zero at the end. Overlap is the
// point — it drives the ErrReadInFlight decline, the semaphore park and the
// re-resolve loop against each other.
func TestStartReadConcurrentWindowsAreBalanced(t *testing.T) {
	mgr, pool, rel := prefetchTestPool(t, 24)
	const nblocks = 40
	extendN(t, mgr, rel, nblocks)
	pool.InvalidateRel(rel)

	eng := newBoundedEngine(4, 3, nil)
	defer eng.Close()
	mgr.SetAIO(eng)
	defer mgr.SetAIO(nil)

	const scanners = 6
	const depth = 5
	var wg sync.WaitGroup
	for g := 0; g < scanners; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for pass := 0; pass < 3; pass++ {
				window := make([]*ReadOp, 0, depth)
				for b := 0; b < nblocks; b++ {
					tag := BufferTag{Rel: rel, Block: BlockNumber((b + g*7) % nblocks)}
					op, err := pool.StartRead(tag)
					switch {
					case errors.Is(err, ErrReadInFlight), errors.Is(err, ErrNoBuffer):
						continue // both are legitimate "do not grow the window"
					case err != nil:
						t.Errorf("StartRead: %v", err)
						return
					case op == nil:
						continue
					}
					window = append(window, op)
					if len(window) == depth {
						for _, w := range window {
							if err := w.Finish(); err != nil {
								t.Errorf("Finish: %v", err)
								w.Abort()
								continue
							}
							pool.Unpin(w.Slot())
						}
						window = window[:0]
					}
				}
				for _, w := range window {
					w.Abort() // the drain path, exercised on every pass
				}
			}
		}(g)
	}
	wg.Wait()

	if total, pinned := pool.TotalPinCount(); total != 0 || pinned != 0 {
		t.Fatalf("%d concurrent windows leaked: %d pins over %d slots", scanners, total, pinned)
	}
}
