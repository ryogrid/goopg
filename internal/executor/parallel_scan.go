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
	"sync"
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

	// nBlocks is the scan boundary. It is not known when the Gather creates
	// this state — the relation size is read in seqScanOp.Open — so the first
	// scan to open publishes it under initOnce and the rest observe it.
	// Immutable thereafter.
	nBlocks  storage.BlockNumber
	initOnce sync.Once
}

// newParallelScanState builds an allocator whose boundary is not yet known.
func newParallelScanState(nBlocks storage.BlockNumber) *parallelScanState {
	s := &parallelScanState{}
	if nBlocks > 0 {
		s.setBoundary(nBlocks)
	}
	return s
}

// setBoundary publishes the relation size. Idempotent: every worker's scan
// calls it during Open with the same value, and only the first takes effect.
// sync.Once supplies the happens-before edge that makes nBlocks safe to read
// without further synchronisation.
func (s *parallelScanState) setBoundary(n storage.BlockNumber) {
	if s == nil {
		return
	}
	s.initOnce.Do(func() { s.nBlocks = n })
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

// attachParallelScan wires op's driving sequential scan to the shared block
// allocator, making that tree scan a PARTITION of the relation rather than all
// of it.
//
// This is the step whose absence produced N copies of every row the first time
// the Gather and the allocator were connected: each worker built an ordinary
// serial seqScanOp, and every one of them read the whole table. The
// serial-vs-parallel identity check caught it immediately — 240298 rows where
// serial returned 120149 — which is precisely why that check exists.
//
// The walk is deliberately narrow. It descends only through the row-wise
// wrappers a partial subtree may contain, and stops at the first scan. A node
// it does not model is left alone, which means the tree simply stays serial —
// duplicating rows is a wrong-results bug, whereas declining to parallelise is
// merely a missed optimisation, so the ambiguous case must fail toward serial.
func attachParallelScan(op Operator, st *parallelScanState) bool {
	if st == nil {
		return false
	}
	switch x := op.(type) {
	case *seqScanOp:
		x.pscan = st
		return true
	case *filterOp:
		return attachParallelScan(x.child, st)
	case *projectOp:
		return attachParallelScan(x.child, st)
	case *instrumentedOp:
		return attachParallelScan(x.inner, st)
	case *sortOp:
		// P7. A Sort inside the partial subtree is legal ONLY under Gather
		// Merge: each worker sorts its own partition and the leader merges the
		// already-ordered streams. Plain Gather over per-worker Sorts would
		// interleave them and lose the ordering, so the planner never builds
		// that shape — see findPartialSubtree, which returns a Sort as the
		// partial root only together with the GatherMerge it requires.
		return attachParallelScan(x.child, st)
	}
	return false
}
