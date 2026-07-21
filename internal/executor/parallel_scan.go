package executor

// parallel_scan.go — P4 of docs/design/parallel-query/, chapter 04.
//
// The shared block allocator for a parallel sequential scan. This is the only
// piece of scan state that becomes shared; everything else in seqScanOp stays
// strictly per-worker (its page pin, decode buffer, emitted slot, per-page
// arena, scan ring and prefetch watermark), which is where the actual
// engineering risk in a parallel scan lives.
//
// goopg's seqScanOp was already close to this shape: nBlocks is captured once
// at Open so every worker agrees on the scan boundary, and the block cursor is
// strictly forward with no backtracking, so "hand out the next block" is a
// complete description of the work split.

import (
	"sync/atomic"

	"github.com/goopg/goopg/internal/storage"
)

// parallelScanState is the work queue for one parallel sequential scan node.
// The leader creates it; every worker's seqScanOp holds a pointer to the same
// instance.
//
// Divergence from PostgreSQL: PG's equivalent (ParallelBlockTableScanDesc)
// lives in dynamic shared memory behind a spinlock, and carries a chunk size,
// a ramp-down schedule and the synchronised-seqscan start position. goopg
// needs none of that — the state reduces to one atomic counter, because a
// pointer is reachable without DSM, there is no sync-scan feature, and the
// per-block atomic is cheap enough that PG's chunking (which exists mainly to
// amortise the spinlock) has no motivation here.
type parallelScanState struct {
	// next is the next unallocated block number. Workers claim blocks with a
	// single atomic increment; the value may run past nBlocks, which is how
	// exhaustion is detected without a second synchronisation point.
	next atomic.Uint64

	// nBlocks is the scan boundary, captured once by the leader before any
	// worker starts. Immutable thereafter.
	nBlocks storage.BlockNumber
}

// newParallelScanState builds the allocator for a scan of nBlocks blocks.
func newParallelScanState(nBlocks storage.BlockNumber) *parallelScanState {
	return &parallelScanState{nBlocks: nBlocks}
}

// nextBlock claims the next block for the calling worker, or reports
// exhaustion.
//
// One block per call. PG allocates in shrinking chunks so that no straggler
// holds a large chunk near the end of the scan; single-block allocation makes
// that impossible by construction, and the atomic costs far less than the page
// read it precedes. If profiling ever shows the atomic mattering, chunking is
// a local change behind this same signature.
func (s *parallelScanState) nextBlock() (storage.BlockNumber, bool) {
	if s == nil {
		return 0, false
	}
	b := s.next.Add(1) - 1
	if b >= uint64(s.nBlocks) {
		return 0, false
	}
	return storage.BlockNumber(b), true
}

// claimed reports how many blocks have been handed out, for tests and
// instrumentation. It may exceed nBlocks once workers have raced past the end.
func (s *parallelScanState) claimed() uint64 {
	if s == nil {
		return 0
	}
	return s.next.Load()
}
