package executor

import (
	"errors"
	"os"
	"strconv"

	"github.com/goopg/goopg/internal/storage"
)

// E-19 / EX5-05 slice S4 — the scan-side look-ahead window.
//
// Design: docs/design/storage-prefetch-buffer/E19-INSTALLING-PREFETCH.md.
// S1 gave the storage layer a vectored read, S2 the accounting instruments,
// S3 Pool.StartRead / ReadOp.Finish — a prefetch that installs into the buffer
// pool instead of reading into a scratch buffer and dropping it. S4 is the
// only slice with a caller, and therefore the only one that can measure
// anything.
//
// ------------------------------------------------------------------
// Two decisions stated outright, because both bound what this can show
// ------------------------------------------------------------------
//
// 1. THE WINDOW IS ON THE SERIAL PATH ONLY, so at goopg's bench settings the
//    mechanism is INERT AND THE ITEM HAS NO WITNESS THERE. E-19's obstacle 1
//    is that every TPC-H plan at bench settings is parallel, and goopg's
//    parallel bitmap scan claims pages one at a time from a shared atomic
//    allocator (parallelBitmapState): a worker cannot look ahead at its own
//    future blocks without claiming them first, so a parallel window needs a
//    BATCHED-CLAIM API — which the design itself lists as a separate S4
//    sub-item ("plus the parallel batched-claim API if the parallel path is in
//    scope"). It is not in scope here, and pretending a serial-only window has
//    a bench witness would be the manufactured win §5 forbids. The witness is
//    Probe 0's instrument, at max_parallel_workers_per_gather = 0.
//
// 2. THE DEPTH KNOB DEFAULTS TO 0 — the mechanism entirely inert — per §4.3.
//    A knob that can resurrect a harmful path is not left on by default on the
//    strength of a design document, and E-11 removed the predecessor after
//    measuring it 12.1% WORSE. The knob is an env instrument, as E-11's was.
//    It is deliberately NOT called effective_io_concurrency (§4.3a): in PG
//    18.3 that GUC is max_ios, a count of concurrent I/Os, and pinned depth is
//    derived from it — a knob meaning "D blocks of look-ahead" is a different
//    quantity and must not borrow the name.

// prefetchDepthEnv is the env instrument. 0 (and unset) means no look-ahead
// at all: StartRead is never called and the scan takes exactly the path it
// took before this file existed.
const prefetchDepthEnv = "GOOPG_HEAP_PREFETCH_DEPTH"

// maxPrefetchDepth caps the knob at upstream's MAX_IO_COMBINE_LIMIT-shaped
// ceiling. It is a sanity bound on a debug env var, not a policy.
const maxPrefetchDepth = storage.MaxIOCombineLimit

// heapPrefetchDepth reads the knob once. An unparseable or negative value is
// treated as 0 — fail closed, since every failure mode of this feature is a
// buffer-pool accounting bug rather than a wrong answer.
func heapPrefetchDepth() int {
	v := os.Getenv(prefetchDepthEnv)
	if v == "" {
		return 0
	}
	d, err := strconv.Atoi(v)
	if err != nil || d <= 0 {
		return 0
	}
	if d > maxPrefetchDepth {
		d = maxPrefetchDepth
	}
	return d
}

// heapPrefetchWindow is a small FIFO of in-flight installing reads for blocks
// the scan has not reached yet. It owns a pin on every slot in it, which is
// the whole hazard: §4.2's leak class is "a prefetch that pins and never
// unpins", and the answer is that the window has exactly one owner (the
// operator) and is drained on every lifecycle end.
type heapPrefetchWindow struct {
	pool  *storage.Pool
	rel   storage.RelFileNode
	depth int

	ops []*storage.ReadOp

	// peek is the reusable lookahead buffer, so refilling allocates nothing.
	peek []storage.BlockNumber

	// Counters, for the arm's own accounting rather than for pg_stat_*.
	started  int64
	consumed int64
	dropped  int64
}

func newHeapPrefetchWindow(pool *storage.Pool, rel storage.RelFileNode, depth int) *heapPrefetchWindow {
	if depth <= 0 || pool == nil {
		return nil
	}
	// §4.2 hazard 2: a deep window IS eviction pressure, because StartRead
	// calls claimVictim. Upstream's read stream does not decline — it SHRINKS
	// the distance to what the pin budget allows and floors it at one block to
	// guarantee progress (read_stream.c:320-341). Transcribe the shape: cap
	// the window at a small fraction of the pool so a look-ahead can never
	// evict a meaningful part of it, and floor at 1.
	if cap := pool.Capacity() / 8; depth > cap {
		depth = cap
	}
	if depth < 1 {
		depth = 1
	}
	return &heapPrefetchWindow{
		pool:  pool,
		rel:   rel,
		depth: depth,
		ops:   make([]*storage.ReadOp, 0, depth),
		peek:  make([]storage.BlockNumber, depth),
	}
}

// refill tops the window up from the upcoming blocks, which arrive in
// ascending block order because the TID bitmap is iterated in that order —
// exactly the adjacency upstream's io_combine_limit exists to exploit.
func (w *heapPrefetchWindow) refill(upcoming []storage.BlockNumber) {
	if w == nil {
		return
	}
	for _, blk := range upcoming {
		if len(w.ops) >= w.depth {
			return
		}
		if w.holds(blk) {
			continue
		}
		op, err := w.pool.StartRead(storage.BufferTag{Rel: w.rel, Block: blk})
		switch {
		case errors.Is(err, storage.ErrReadInFlight):
			// Another backend is already reading it; the consumer's own Pin
			// will park on the slot. Skip, do not wait, do not re-read.
			continue
		case errors.Is(err, storage.ErrNoBuffer):
			// No victim available: stop GROWING, keep what we have. The
			// window is discretionary; the consumer's Pin is not.
			return
		case err != nil || op == nil:
			// Anything else is the window's problem, not the scan's: stop
			// looking ahead and let the ordinary path surface any real error.
			return
		}
		w.ops = append(w.ops, op)
		w.started++
	}
}

func (w *heapPrefetchWindow) holds(blk storage.BlockNumber) bool {
	for _, op := range w.ops {
		if op.Tag().Block == blk {
			return true
		}
	}
	return false
}

// take hands over the window's slot for blk, already pinned and valid, or
// returns nil if the window does not have it. A nil return is always safe:
// the caller falls back to Pool.Pin.
//
// Blocks the scan has passed are dropped here rather than at a page boundary.
// That is deliberate and is the point of §4.2's correction (i): releasePinned
// is a PER-PAGE operation called on every page transition, so draining the
// window there would unwind the look-ahead at every page and the mechanism
// would never be a window at all.
func (w *heapPrefetchWindow) take(blk storage.BlockNumber) *storage.Slot {
	if w == nil {
		return nil
	}
	for len(w.ops) > 0 {
		op := w.ops[0]
		got := op.Tag().Block
		if got > blk {
			return nil // the window ran ahead of a block it never queued
		}
		w.ops = w.ops[1:]
		if got < blk {
			op.Abort() // stale: the scan skipped past it
			w.dropped++
			continue
		}
		if err := op.Finish(); err != nil {
			// Let the caller's own Pin reproduce and report the error, so the
			// prefetch path can never turn a read failure into a DIFFERENT
			// error than the one the scan would have got without it.
			w.dropped++
			return nil
		}
		w.consumed++
		return op.Slot()
	}
	return nil
}

// drain releases every outstanding op. This is the NEW drain point §4.2
// correction (i) says the operator needs — separate from releasePinned, which
// is per-page — and it must be reached from every lifecycle end: Close,
// Rescan, EOF and the error return of the page-transition path.
func (w *heapPrefetchWindow) drain() {
	if w == nil {
		return
	}
	for _, op := range w.ops {
		op.Abort()
		w.dropped++
	}
	w.ops = w.ops[:0]
}
