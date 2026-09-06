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
	case *joinOp:
		// P8. Only the PROBE side is partial: the build side was drained once
		// by the leader before fan-out. Attaching the allocator to the build
		// side instead would give each worker a PARTITION of the build input,
		// so every worker's hash table would be missing most of its rows and
		// the join would silently drop matches.
		if probeSideIsLeft(x.plan) {
			return attachParallelScan(x.left, st)
		}
		return attachParallelScan(x.right, st)
	case *aggregateOp:
		// P9. A Partial aggregate inside the partial subtree must read only
		// its worker's PARTITION. Without this the walk stops at the aggregate,
		// every worker aggregates the WHOLE relation, and the Finalize node
		// combines N full results into an N-times overcount — arithmetically
		// plausible output with nothing to flag it.
		return attachParallelScan(x.child, st)
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

// attachParallelBitmapScan wires op's driving bitmap heap scan to the shared
// bitmap state, so each worker claims disjoint pages from the pre-built
// TIDBitmap. (S5.6)
//
// The walk mirrors attachParallelScan: it descends through row-wise wrappers
// and stops at the first bitmapHeapScanOp. An unmodelled node is left alone,
// which declines to parallelise rather than risk a wrong result.
func attachParallelBitmapScan(op Operator, st *parallelBitmapState) bool {
	if st == nil {
		return false
	}
	switch x := op.(type) {
	case *bitmapHeapScanOp:
		x.pbm = st
		return true
	case *filterOp:
		return attachParallelBitmapScan(x.child, st)
	case *projectOp:
		return attachParallelBitmapScan(x.child, st)
	case *instrumentedOp:
		return attachParallelBitmapScan(x.inner, st)
	case *joinOp:
		// P8: only the PROBE side is partial.
		if probeSideIsLeft(x.plan) {
			return attachParallelBitmapScan(x.left, st)
		}
		return attachParallelBitmapScan(x.right, st)
	case *aggregateOp:
		// P9: Partial aggregate must read only its worker's partition.
		return attachParallelBitmapScan(x.child, st)
	case *sortOp:
		// P7: per-worker Sort under Gather Merge.
		return attachParallelBitmapScan(x.child, st)
	}
	return false
}

// parallelIndexScanState is the work queue for one parallel index-only scan.
//
// It partitions by index LEAF BLOCK rather than heap block, because that is the
// unit an index scan walks: `nbtree.ScanPos.Blk` names the leaf every entry
// came from, so a worker decides "mine or not" per page with no extra I/O.
//
// Divergence from PostgreSQL, stated because it is real: PG's parallel btree
// scan (`_bt_parallel_seize`) hands each worker the NEXT leaf page, so a worker
// walks only its own. goopg's workers each walk the whole leaf chain and skip
// blocks another worker claimed — the descent is duplicated N times, while the
// per-entry work (visibility, decode, materialisation) is partitioned once.
//
// The claim is first-come, and that is what makes it CORRECT rather than merely
// fast: `LoadOrStore` reports loaded=false to exactly one caller per block, so
// every leaf is processed by exactly one worker. None can be dropped (some
// worker always reaches it) and none duplicated (only the first claimer
// proceeds). The seq-scan allocator gets that from an atomic counter; this gets
// it from the map.
type parallelIndexScanState struct {
	claimed sync.Map // storage.BlockNumber -> struct{}
	blocks  atomic.Uint64
}

func newParallelIndexScanState() *parallelIndexScanState { return &parallelIndexScanState{} }

// claimLeaf reports whether the calling worker owns leaf block blk. Exactly one
// worker is told yes for any given block.
//
// A nil receiver answers YES for every block — the serial case, which is what
// leaves every non-parallel caller of the scan loop unchanged.
func (s *parallelIndexScanState) claimLeaf(blk storage.BlockNumber) bool {
	if s == nil {
		return true
	}
	if _, loaded := s.claimed.LoadOrStore(blk, struct{}{}); loaded {
		return false
	}
	s.blocks.Add(1)
	return true
}

// claimedBlocks reports how many leaf blocks have been handed out, for tests
// and instrumentation.
func (s *parallelIndexScanState) claimedBlocks() uint64 {
	if s == nil {
		return 0
	}
	return s.blocks.Load()
}

// leafClaimMemo is one worker's cached view of the shared leaf-claim set: the
// verdict per leaf, and the most recent one. The shared claim is a sync.Map
// operation and a range scan visits ~300 entries per leaf, so consulting it
// per ENTRY made the parallel index-only scan 3.5x SLOWER than serial (q16
// 1.6s -> 5.7s). A btree scan walks leaves in key order, so the common case
// is "same block as the last entry" and costs one comparison. The map is kept
// so revisiting a block reuses this worker's OWN verdict rather than
// re-asking the shared set, which would answer "already claimed" about our
// own claim and silently drop rows.
//
// C-19c: extracted from indexOnlyScanOp's fields so the plain index scan
// (indexScanOp) partitions by the same memo rather than a re-typed sibling.
type leafClaimMemo struct {
	owned     map[storage.BlockNumber]bool
	last      storage.BlockNumber
	lastOwned bool
	lastValid bool
}

// owns reports whether this worker processes entries from leaf block blk,
// memoising the shared claim. A nil st answers YES for every block (serial).
func (m *leafClaimMemo) owns(st *parallelIndexScanState, blk storage.BlockNumber) bool {
	if m.lastValid && m.last == blk {
		return m.lastOwned
	}
	owned, seen := m.owned[blk]
	if !seen {
		owned = st.claimLeaf(blk)
		if m.owned == nil {
			m.owned = make(map[storage.BlockNumber]bool, 64)
		}
		m.owned[blk] = owned
	}
	m.last, m.lastOwned, m.lastValid = blk, owned, true
	return owned
}

// attachParallelIndexScan wires op's driving index scan — index-only or, since
// C-19c, a plain index scan — to the shared leaf-block claim set. The walk
// mirrors attachParallelScan's — row-wise wrappers only, stopping at the first
// index scan — and an unmodelled node is left alone, leaving the tree serial.
// Declining to parallelise is a missed optimisation; attaching in the wrong
// place is duplicated or dropped rows.
//
// The plain index scan partitions the SAME way the index-only scan does: both
// are eager at Open/Rescan (the IOS materialises rows, the plain scan its TID
// list — operators_index.go's M0092-0001 note), both walk the leaf chain
// through nbtree.RangeScanWithPosLeafFilter, and the leaf filter decides per
// leaf block which worker's list an entry lands in. Exactly one worker owns
// each leaf, so the union over workers is the whole scan exactly once, and the
// per-Next heap fetch then runs only over that worker's TIDs.
func attachParallelIndexScan(op Operator, st *parallelIndexScanState) bool {
	if st == nil {
		return false
	}
	switch x := op.(type) {
	case *indexOnlyScanOp:
		x.pidx = st
		return true
	case *indexScanOp:
		x.pidx = st
		return true
	case *filterOp:
		return attachParallelIndexScan(x.child, st)
	case *projectOp:
		return attachParallelIndexScan(x.child, st)
	case *instrumentedOp:
		return attachParallelIndexScan(x.inner, st)
	case *joinOp:
		// Probe side only, for the reason attachParallelScan's joinOp arm states.
		if probeSideIsLeft(x.plan) {
			return attachParallelIndexScan(x.left, st)
		}
		return attachParallelIndexScan(x.right, st)
	case *aggregateOp:
		return attachParallelIndexScan(x.child, st)
	case *sortOp:
		return attachParallelIndexScan(x.child, st)
	}
	return false
}
