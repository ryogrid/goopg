package wal

import (
	"sync"
	"sync/atomic"
)

// lsnAllocator hands out contiguous LSN byte-ranges atomically while
// serialising segment-boundary crossings via rotateMu. Foundation
// for M0107-0007 slice B (Phase D4 — 8-stripe WAL insert locks per
// `docs/design/perf-optimize/07-wal-fsm-insert.md` §2). The common
// case (record fits within the current segment) is a single atomic
// CAS; segment boundaries serialise on rotateMu so a callback can
// pad the segment tail or open the next segment file exactly once
// per crossing.
//
// Not wired into Writer.Append in this loop. Lands ahead of its
// consumer per the slice C foundation pattern (0107-0007b/c/d landed
// before the executor consumer in 0107-0007e/f/g).
type lsnAllocator struct {
	next     atomic.Uint64
	rotateMu sync.Mutex
	segSize  uint64

	// onCrossSegment, when non-nil, runs under rotateMu before each
	// segment-boundary advance. start is the next LSN at the moment
	// the crossing reservation took rotateMu; boundary is the first
	// LSN of the new segment. The hook can pad the current segment's
	// tail with a NOOP WAL record, open the next segment file, or
	// fsync-recycle the previous segment — whatever the caller needs.
	// Its work is bounded by the rotation rate (~1 per segSize bytes),
	// not the insert rate.
	onCrossSegment func(start, boundary uint64)
}

// newLSNAllocator initialises an allocator that starts handing out
// LSNs from startLSN. segSize must be > 0 (callers normally pass
// Config.SegmentSize, default DefaultSegmentSize = 16 MiB).
// onCross may be nil (the allocator still serialises crossings; the
// caller just has no per-crossing hook).
func newLSNAllocator(startLSN, segSize uint64, onCross func(start, boundary uint64)) *lsnAllocator {
	if segSize == 0 {
		panic("wal: lsnAllocator requires non-zero segSize")
	}
	a := &lsnAllocator{segSize: segSize, onCrossSegment: onCross}
	a.next.Store(startLSN)
	return a
}

// load returns the current next-LSN without reserving anything. Used
// by observability surfaces; not for hot-path serialisation.
func (a *lsnAllocator) load() uint64 { return a.next.Load() }

// reserve atomically claims `size` bytes of LSN space and returns
// the starting LSN. Callers must materialise the range
// `[start, start+size)` before any later reserve reads beyond
// `start+size`.
//
// In the common case (the reservation fits within the current
// segment) reserve is a single CAS. When the reservation would
// cross a segment boundary the call takes rotateMu, invokes
// onCrossSegment (if set), advances next past the boundary, and
// reserves from the new segment. Multiple goroutines racing across
// the same boundary serialise on rotateMu; onCrossSegment runs
// exactly once per crossing (a peer that loses the race observes
// next already past the boundary and drops back to the CAS path).
//
// size must satisfy 0 < size <= segSize; segment-spanning records
// must be split or padded by the caller before reserve is invoked.
func (a *lsnAllocator) reserve(size uint64) uint64 {
	if size == 0 || size > a.segSize {
		panic("wal: lsnAllocator.reserve: size out of range")
	}
	for {
		old := a.next.Load()
		end := old + size
		oldSeg := old / a.segSize
		endSeg := (end - 1) / a.segSize
		if oldSeg == endSeg {
			if a.next.CompareAndSwap(old, end) {
				return old
			}
			continue
		}

		// Cross-segment slow path.
		a.rotateMu.Lock()
		old = a.next.Load()
		oldSeg = old / a.segSize
		end = old + size
		endSeg = (end - 1) / a.segSize
		if oldSeg == endSeg {
			// Another writer already rotated past us; drop to fast path.
			a.rotateMu.Unlock()
			continue
		}
		nextStart := (oldSeg + 1) * a.segSize
		if a.onCrossSegment != nil {
			a.onCrossSegment(old, nextStart)
		}
		// Place the reservation at the start of the new segment.
		a.next.Store(nextStart + size)
		a.rotateMu.Unlock()
		return nextStart
	}
}
