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

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/storage"
)

// ordinaryInnerNestedLoopPartial is the executor-side half of R94's
// admission rule (plan-parity-fix-take2): the plan shapes whose per-worker
// semantics the three attach walks model. It must agree with the
// planner-side twin `nestedLoopJoinIsPartialCapable` (optimizer/parallel.go)
// and the path classifier (`partialPathDrivingKind`'s PathNestLoop arm) —
// a shape admitted here but refused there (or vice versa) either runs
// unmodelled or never runs. Ordinary INNER and SEMI, non-nil children, never
// lateral, never parameterized (an NLI is a different plan node,
// *NestedLoopIndexJoin, and never reaches a joinOp).
//
// SEMI was added 2026-09-21 to repair a LIVE WRONG-ANSWER divergence, not to
// widen anything. M0137-0019b admitted SEMI on the planner side — both
// `nestedLoopJoinIsPartialCapable` (the node twin) and
// `partialPathDrivingKind`'s PathNestLoop arm — and this executor twin was
// left at INNER. The consequence is not a safe decline: this walk returning
// false leaves the driving scan UNATTACHED, and `attachAll`'s result is
// ignored by `gatherOp` (ledger `e10-attachall-precondition-unenforced`), so
// every worker scanned the WHOLE outer and the node emitted N copies.
//
// Measured at HEAD on the SF1 clone, `SELECT count(*) FROM customer c WHERE
// EXISTS (SELECT 1 FROM region r WHERE r.r_name > c.c_mktsegment)` — a plain
// `Nested Loop Semi Join` with a non-parameterized inner under a Gather:
// parallel returned 450000 against a serial and ground-truth 150000, exactly
// 3x with 3 workers. A semijoin can emit at most one row per outer row.
//
// Admitting SEMI is the correct direction because the planner's rationale
// already holds for the executor: each worker joins ITS partition of the
// outer against the whole inner it materialises itself, one qualifying inner
// tuple decides the outer tuple and the scan breaks (`finishOuter`,
// join_nl_stream.go), the joined row is never emitted (the join's schema is
// outer-only), and `markInner`/`fillInner` — the cross-worker reduction RIGHT
// and FULL would need — is never touched. The union over workers is therefore
// each outer row at most once, which is the semijoin's own contract.
//
// LEFT and ANTI joined the set on 2026-09-21, widened on the executor and all
// three planner gates in ONE change — the discipline this comment's own
// history argues for. PG admits {INNER, LEFT, SEMI, ANTI} at the same nestloop
// dispatch gate (`joinpath.c:1842-1846`), and both are worker-local for the
// same reason SEMI is: `fillInner` — the cross-worker inner-match reduction —
// is set only for RIGHT/FULL (`join_nl_stream.go`), and `markInner` is called
// only under it, so LEFT's null-extension and ANTI's no-match verdict are each
// decided by one outer row against the whole inner that worker materialises
// itself. Verified by measurement, not only by reading:
// TestParallelLeftAntiNestedLoopIdentity forced a Gather over both shapes and
// showed the N-copy signature before this widening, correct counts after.
//
// RIGHT and FULL stay refused, here and on every twin: they need to know which
// INNER rows went unmatched across ALL workers, which is the cross-worker
// reduction no gate in this family models.
func ordinaryInnerNestedLoopPartial(p *optimizer.Join) bool {
	if p == nil || p.Algo != optimizer.JoinAlgoNestedLoop || p.Lateral {
		return false
	}
	if p.Left == nil || p.Right == nil {
		return false
	}
	switch p.Type {
	case optimizer.JoinTypeInner, optimizer.JoinTypeLeft,
		optimizer.JoinTypeSemi, optimizer.JoinTypeAnti:
		return true
	}
	return false
}

// lateralProbeJoinPartial is the executor-side half of R95's admission rule:
// R25's decomposed probe (lateral Join over a bare parameterized index
// probe). It must agree with `lateralProbeJoinIsPartialCapable` on the plan
// shape, and additionally proves what only the BUILT tree can show — that
// the probe operator takes no lateral slot binding. A wrapped probe (the
// BuildFast bridge forwards `lateralBindable` unconditionally) would
// double-bind: once through the slot, once through `ctx.OuterRows`. The
// probe binds through `ctx.OuterRows` alone, exactly as serial execution
// does, so per-worker re-opening is transparent.
//
// `right` is the join's built right operator. instrumentedOp wrappers are
// transparent (they forward Next/Open, not bindings). A memoizeOp never
// reaches this walk: the cache exists only as nestedLoopIndexJoinOp.inner
// (executor.go's *NestedLoopIndexJoin arm), whose own sibling check is
// the *nestedLoopIndexJoinOp case below (M0142-0005a).
func lateralProbeJoinPartial(p *optimizer.Join, right Operator) bool {
	if p == nil || p.Algo != optimizer.JoinAlgoNestedLoop || !p.Lateral {
		return false
	}
	if p.Type != optimizer.JoinTypeInner {
		return false
	}
	if p.Left == nil || p.Right == nil || right == nil {
		return false
	}
	inner := right
	if iw, ok := inner.(*instrumentedOp); ok {
		inner = iw.inner
	}
	switch inner.(type) {
	case *indexScanOp, *indexOnlyScanOp:
	default:
		return false
	}
	if _, bindable := inner.(lateralBindable); bindable {
		return false
	}
	return true
}

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
		// R94 (plan-parity-fix-take2). An approved ordinary INNER nested
		// loop is partial through its OUTER (left) side only: each worker
		// joins its outer partition against the WHOLE inner, which it
		// materializes and replays itself (`openNestedLoop`). The side is
		// named literally — never via probeSideIsLeft, which answers from
		// BuildLeft, a field a nested loop leaves false by construction
		// (the same trap the merge arm documents below). The inner
		// receives no claim state; a bitmap scan anywhere in it is a
		// refusal (prebuildBitmap shares nothing with >1 bitmap scan, so
		// the outer would go unpartitioned — N+1 copies, silently).
		// Seq/index inners are safe: they hold no shared state and this
		// walk never descends right, so every worker reads the whole
		// inner independently.
		if x.plan != nil && x.plan.Algo == optimizer.JoinAlgoNestedLoop {
			// R95: the lateral probe shares the literal-left rule — it
			// re-opens per worker-local outer row and takes no claim.
			if !ordinaryInnerNestedLoopPartial(x.plan) && !lateralProbeJoinPartial(x.plan, x.right) {
				return false
			}
			if optimizer.HasBitmapScan(x.plan.Right) {
				return false
			}
			return attachParallelScan(x.left, st)
		}
		// P8. Only the PROBE side is partial: the build side was drained once
		// by the leader before fan-out. Attaching the allocator to the build
		// side instead would give each worker a PARTITION of the build input,
		// so every worker's hash table would be missing most of its rows and
		// the join would silently drop matches.
		//
		// FAIL CLOSED on anything but a HASH join (2026-09-07). `joinOp` runs
		// all three algorithms, and `probeSideIsLeft` answers from `BuildLeft`
		// — a field a merge join leaves false by construction
		// (`createMergeJoinPlan`: "a merge join has no build side, so BuildLeft
		// is meaningless here and stays false"). So for a merge or nested-loop
		// join this arm used to answer "left" not because left is the partial
		// side but because the field it reads is unset, and the walk would
		// descend a subtree whose per-worker semantics nothing here models.
		//
		// It is unreachable today: the planner's own twin refuses first —
		// `drivingScan`'s `*Join` arm is gated on `hashJoinIsPartialCapable`
		// (Algo == JoinAlgoHash) and the path model's `partialPathDrivingKind`
		// has an arm for `PathHashJoin` only. That is exactly why the guard is
		// worth writing: this walk is the LAST line of defence, `runWorker`
		// IGNORES the return value, and the failure mode of a wrong answer here
		// is N copies of every row rather than an error. Declining leaves the
		// subtree serial, which is the direction an ambiguous case must fail.
		//
		// A partial merge join (`try_partial_mergejoin_path`, joinpath.c:1218)
		// is a real PG shape and goopg has no producer for it; when one is
		// written, this arm gains a `JoinAlgoMerge` case that descends the
		// OUTER (left) side explicitly, together with its own serial-vs-parallel
		// identity test.
		//
		// E-20 Cut 3 wrote that producer (`addPartialMergeJoinPath`). Each
		// worker merge-joins ITS partition of the outer against the WHOLE
		// inner, so the walk descends the OUTER (left) side explicitly —
		// never `probeSideIsLeft`, which answers from `BuildLeft`, a field
		// a merge join leaves false by construction. The inner is left
		// alone: every worker sorts and reads it whole, which is why the
		// planner prices it undivided.
		if x.plan == nil || x.plan.Algo != optimizer.JoinAlgoHash {
			if x.plan != nil && x.plan.Algo == optimizer.JoinAlgoMerge {
				return attachParallelScan(x.left, st)
			}
			return false
		}
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
	case *nestedLoopIndexJoinOp:
		// M0142-0005a: the fused NLI is partial through its OUTER only —
		// each worker joins its outer partition and re-opens the inner
		// probe per outer row (nliInner's BindOuter/Rescan), exactly like
		// R95's lateral probe and PG's try_partial_nestloop_path. The
		// inner takes NO claim: it is a parameterized probe, not a
		// partitioned scan — and a memoizeOp inner needs no shared state
		// either, since every worker already built its own
		// memoizeOp/kvcache over the read-only plan (executor.go's "each
		// worker builds its OWN operator tree"), matching real PG's
		// per-worker MemoizeState.
		//
		// Re-runs the planner's own verdict (parallel.go's
		// NestedLoopIndexJoinIsPartialCapable — literal agreement, no
		// twin to drift): INNER only, bare keyed probe, no bitmap inner
		// (a re-probed bitmap has no claim-set story — the same refusal
		// the *joinOp arm states for x.plan.Right).
		if !optimizer.NestedLoopIndexJoinIsPartialCapable(x.plan) {
			return false
		}
		return attachParallelScan(x.outer, st)
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
		// R94: an approved ordinary INNER nested loop descends the OUTER
		// (left) side literally; the inner never takes bitmap claim
		// state (same guard as the sequential arm above). Every other
		// nested loop is refused — the probeSideIsLeft fallthrough below
		// must never answer for it, since BuildLeft is meaningless here.
		if x.plan != nil && x.plan.Algo == optimizer.JoinAlgoNestedLoop {
			if !ordinaryInnerNestedLoopPartial(x.plan) && !lateralProbeJoinPartial(x.plan, x.right) {
				return false
			}
			if optimizer.HasBitmapScan(x.plan.Right) {
				return false
			}
			return attachParallelBitmapScan(x.left, st)
		}
		// E-20 Cut 3 (M0140-0006c-2 slice B): a merge join is likewise
		// partial through its OUTER (left) side — each worker merge-joins
		// its partition of the outer against the whole inner, which it
		// sorts and reads itself; the inner takes no claim. Literal left
		// like the NL arm above and attachParallelScan's own merge arm —
		// never probeSideIsLeft, which would answer "left" only because
		// BuildLeft stays false by construction (createMergeJoinPlan):
		// the right answer for the wrong reason.
		if x.plan != nil && x.plan.Algo == optimizer.JoinAlgoMerge {
			return attachParallelBitmapScan(x.left, st)
		}
		// R95: nil plan refuses rather than panicking in
		// probeSideIsLeft below — the walk is the last line of defence
		// and its failure mode must be serial, never a throw.
		if x.plan == nil {
			return false
		}
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
	case *nestedLoopIndexJoinOp:
		// M0142-0005a: mirror of the sequential arm above — the fused
		// NLI is partial through its OUTER only, so a driving bitmap
		// scan there must be reachable through it. Same guard.
		if !optimizer.NestedLoopIndexJoinIsPartialCapable(x.plan) {
			return false
		}
		return attachParallelBitmapScan(x.outer, st)
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
		// R94: same literal-left rule as the two siblings above; the
		// inner never takes index claim state.
		if x.plan != nil && x.plan.Algo == optimizer.JoinAlgoNestedLoop {
			if !ordinaryInnerNestedLoopPartial(x.plan) && !lateralProbeJoinPartial(x.plan, x.right) {
				return false
			}
			if optimizer.HasBitmapScan(x.plan.Right) {
				return false
			}
			return attachParallelIndexScan(x.left, st)
		}
		// E-20 Cut 3 (M0140-0006c-2 slice B): a merge join is likewise
		// partial through its OUTER (left) side — each worker merge-joins
		// its partition of the outer against the whole inner; the inner
		// takes no index claim. Literal left like the NL arm and the
		// sequential sibling's merge arm — never probeSideIsLeft, which
		// would answer "left" only because BuildLeft stays false by
		// construction (createMergeJoinPlan): the right answer for the
		// wrong reason.
		if x.plan != nil && x.plan.Algo == optimizer.JoinAlgoMerge {
			return attachParallelIndexScan(x.left, st)
		}
		// R95: same nil-plan refusal as the bitmap sibling above.
		if x.plan == nil {
			return false
		}
		// Probe side only, for the reason attachParallelScan's joinOp arm states.
		if probeSideIsLeft(x.plan) {
			return attachParallelIndexScan(x.left, st)
		}
		return attachParallelIndexScan(x.right, st)
	case *aggregateOp:
		return attachParallelIndexScan(x.child, st)
	case *sortOp:
		return attachParallelIndexScan(x.child, st)
	case *nestedLoopIndexJoinOp:
		// M0142-0005a: same literal-outer rule as the two siblings
		// above; the probe inner never takes index claim state.
		if !optimizer.NestedLoopIndexJoinIsPartialCapable(x.plan) {
			return false
		}
		return attachParallelIndexScan(x.outer, st)
	}
	return false
}

// parallelClaimSet is the COMPLETE set of shared work-claim state a Gather (or
// Gather Merge) hands to each participant's child tree — one field per claim
// kind the executor knows about.
//
// It exists because `gatherOp` and `gatherMergeOp` are a sibling pair that must
// agree, and did not: gatherOp attached all three kinds, gatherMergeOp attached
// only the sequential-scan allocator. A Gather Merge over a partial INDEX path
// therefore had every worker walk the WHOLE index, and the merge returned
// (workers+1) copies of every row — in the correct ORDER, which is what made it
// silent. Measured on the C-19f fixture before this type existed: 5802 / 8703 /
// 14505 rows at 1 / 2 / 4 workers against a serial 2901.
//
// The planner worked around the gap by admitting seq-scan-driven subpaths only
// (C-19f, docs/design/planner-c19f-parallel-hashjoin/DESIGN.md), so the defect
// was unreachable from SQL — and unreachable also means untested. Centralising
// the state here means a future claim kind is added in ONE place and both
// consumers get it; TestParallelClaimSetAttachesEveryKind fails if a field is
// added without an arm in attachAll.
type parallelClaimSet struct {
	// pscan is the shared block allocator for a parallel sequential scan.
	pscan *parallelScanState
	// pbm is the shared page allocator for a parallel bitmap heap scan (S5.6).
	// Unlike the others it is nil until prebuildBitmap runs, because the leader
	// must build the TIDBitmap once before fan-out.
	pbm *parallelBitmapState
	// pidx is the shared leaf-block claim set for a parallel index or
	// index-only scan (M0134-0189, C-19c).
	pidx *parallelIndexScanState

	// setOpLeft / setOpRight (M0140-0006c) are the INDEPENDENT claim state
	// for a partial SetOp's two branches (operators_setop.go's *setOp,
	// built from a PathSetOp addPartialSetOpPath produced). A SetOp streams
	// BOTH branches, unlike a join which is partial through one side only,
	// so its two driving scans cannot share one pscan/pidx: parallelScanState
	// publishes its block-count boundary from whichever scan opens FIRST
	// (initOnce) and every later scan on that same state reuses it — correct
	// when both scans are on the SAME relation, silently wrong when a SetOp's
	// two branches are on different ones.
	//
	// Level one is built eagerly by newParallelClaimSet. M0145-0004a: deeper
	// levels grow LAZILY through setOpBranch, because a branch can now be a
	// nested partial SetOp itself (a right-leaning UNION ALL chain's inner
	// link — the whole-chain appendrel candidate files one PathSetOp whose
	// child is another PathSetOp). Laziness is what lets the chain be
	// arbitrary length: claim sets materialise only where attachAll actually
	// walks, in O(nesting depth) instead of the exponential 2^depth a fixed
	// eager bound would need. attachAll runs from every worker goroutine
	// concurrently, so the growth is funnelled through setOpKidsOnce — one
	// Once per claim set publishes both children, and every caller reads
	// them only through setOpBranch.
	setOpLeft  *parallelClaimSet
	setOpRight *parallelClaimSet
	// setOpKidsOnce is the growth lock for the pair above — bookkeeping,
	// NOT a claim kind: it guards setOpLeft/setOpRight creation on sets
	// whose constructor left them nil (every level below the first).
	// attachAll's *setOp arm is the only trigger; the Once's
	// happens-before makes the publish safe across worker goroutines.
	setOpKidsOnce sync.Once

	// claimedWhole (M0140-0006c-3) is PG's `pa_finished` on a non-partial
	// Append subplan (nodeAppend.c:68-80,704-832): a SetOp branch the
	// planner marked claimed-whole (SetOp.LeftNonPartial/RightNonPartial —
	// its pa_nonpartial_subpaths member) is not split by block at all.
	// Instead every participant CASes this flag once; the winner drains
	// its own private copy of the branch serially, losers treat the
	// branch as exhausted. It lives on the LEAF claim set
	// (setOpLeft/setOpRight) because the claim is per-branch, and attachAll
	// wires it into the *setOp op's claimLeft/claimRight rather than
	// attaching any scan state — the branch's scans stay unclaimed by
	// design (the claiming worker runs them serially). Unused on a claim
	// set whose SetOp branch is partial — the zero value is the
	// "unclaimed" state CAS expects, so no constructor work is needed.
	claimedWhole atomic.Bool

	// hashBuildKids (M0146-0002) is the INDEPENDENT claim state of each
	// Parallel Hash join's build side, keyed by the join. The build side is
	// a partial path over a different relation from the probe side, so it
	// cannot share this set's pscan/pidx (parallelScanState publishes the
	// block count of whichever scan opens it first — the SetOp hazard above).
	// Grown lazily under hashKidsMu: attachAll runs in every participant
	// concurrently, and every participant must reach the SAME state for a
	// given join or each would scan the whole inner.
	hashKidsMu    sync.Mutex
	hashBuildKids map[*optimizer.Join]*parallelClaimSet
}

// hashBuildBranch returns the claim set for join j's partial build side,
// creating it on first use.
func (cs *parallelClaimSet) hashBuildBranch(j *optimizer.Join) *parallelClaimSet {
	cs.hashKidsMu.Lock()
	defer cs.hashKidsMu.Unlock()
	if cs.hashBuildKids == nil {
		cs.hashBuildKids = make(map[*optimizer.Join]*parallelClaimSet)
	}
	kid, ok := cs.hashBuildKids[j]
	if !ok {
		kid = newLeafParallelClaimSet()
		cs.hashBuildKids[j] = kid
	}
	return kid
}

// attachParallelHashBuildSides wires every Parallel Hash join's build side on
// op's probe path to that join's own claim set (hashBuildBranch). It walks
// exactly what attachParallelScan walks, which never descends a hash join's
// build side itself — the leader-prebuilt build is complete, but a Parallel
// Hash build is partial and must be claimed.
func (cs *parallelClaimSet) attachParallelHashBuildSides(op Operator) bool {
	switch x := op.(type) {
	case *filterOp:
		return cs.attachParallelHashBuildSides(x.child)
	case *projectOp:
		return cs.attachParallelHashBuildSides(x.child)
	case *instrumentedOp:
		return cs.attachParallelHashBuildSides(x.inner)
	case *joinOp:
		if x.plan == nil || x.plan.Algo != optimizer.JoinAlgoHash {
			return false
		}
		probe, build := x.right, x.left
		if probeSideIsLeft(x.plan) {
			probe, build = x.left, x.right
		}
		attached := false
		if x.plan.ParallelHash {
			kid := cs.hashBuildBranch(x.plan)
			attached = kid.attachAll(build)
			// A Parallel Hash nested on this build side is built by the
			// same participants, over the build side's own claims.
			attached = kid.attachParallelHashBuildSides(build) || attached
		}
		return cs.attachParallelHashBuildSides(probe) || attached
	}
	return false
}

// newParallelClaimSet builds the claim state that needs no pre-pass. pbm is
// filled in later by prebuildBitmap, when the plan contains a bitmap scan.
func newParallelClaimSet() *parallelClaimSet {
	return &parallelClaimSet{
		pscan:      newParallelScanState(0),
		pidx:       newParallelIndexScanState(),
		setOpLeft:  newLeafParallelClaimSet(),
		setOpRight: newLeafParallelClaimSet(),
	}
}

// newLeafParallelClaimSet is newParallelClaimSet without its own nested
// setOpLeft/setOpRight — those grow lazily through setOpBranch
// (M0145-0004a). A SetOp branch CAN now be another partial SetOp:
// M0144-0003b-1's setOpBranchTag carries the inner link's SETOP rel out to
// the next link, so a right-leaning UNION ALL chain's outer partial path
// has the inner link's partial PathSetOp as a branch child, and that child
// needs claim sets of its own one level down. pbm starts nil exactly like
// the top-level claim set's: prebuildBitmap fills it when the branch's own
// plan carries a bitmap scan (M0140-0006c-2 slice C's per-branch
// publication in bitmapPrebuildTargets).
func newLeafParallelClaimSet() *parallelClaimSet {
	return &parallelClaimSet{
		pscan: newParallelScanState(0),
		pidx:  newParallelIndexScanState(),
	}
}

// setOpBranch returns this claim set's per-branch set for a partial SetOp
// attach — setOpLeft for the left branch, setOpRight for the right —
// growing the pair on first use. The top-level claim set has them built
// eagerly (newParallelClaimSet); leaf sets create them here under
// setOpKidsOnce, so an N-deep UNION ALL chain materialises exactly N-1
// levels of branch state and no more. The Once is what makes the lazy
// growth safe: attachAll calls this from every worker goroutine
// concurrently, and the Once's happens-before edge publishes both fields
// to every caller.
func (cs *parallelClaimSet) setOpBranch(right bool) *parallelClaimSet {
	cs.setOpKidsOnce.Do(func() {
		if cs.setOpLeft == nil {
			cs.setOpLeft = newLeafParallelClaimSet()
		}
		if cs.setOpRight == nil {
			cs.setOpRight = newLeafParallelClaimSet()
		}
	})
	if right {
		return cs.setOpRight
	}
	return cs.setOpLeft
}

// unwrapToSetOp walks the SAME wrapper kinds attachParallelScan's own
// recursion sees (Filter/Project/instrumentedOp/Sort/Aggregate) looking for
// a *setOp, so attachAll finds one wrapped by EXPLAIN instrumentation or a
// residual predicate/projection the same way a bare one is found.
//
// M0145-0004: also descends the PARTIAL side of a join — the one
// multi-child kind a partial spine may contain. The appendrel hoist lets
// a PathSetOp sit as a join's probe input (`Gather → HashJoin → probe
// SetOp`), where before M0140-0006c a setOp could only sit at/near the
// subtree root. Without this arm attachAll fell through to the flat
// walk, which has no *setOp arm: the member scans stayed unclaimed and
// every worker returned the whole union — N+1 copies of every row.
// The side rules mirror attachParallelScan's *joinOp arm exactly
// (nested loop / merge / lateral → literal left, hash → probeSideIsLeft);
// a shape the planner refused partial-capability for is not descended.
func unwrapToSetOp(op Operator) (*setOp, bool) {
	switch x := op.(type) {
	case *setOp:
		return x, true
	case *filterOp:
		return unwrapToSetOp(x.child)
	case *projectOp:
		return unwrapToSetOp(x.child)
	case *instrumentedOp:
		return unwrapToSetOp(x.inner)
	case *sortOp:
		return unwrapToSetOp(x.child)
	case *aggregateOp:
		return unwrapToSetOp(x.child)
	case *joinOp:
		if x.plan == nil {
			return nil, false
		}
		switch x.plan.Algo {
		case optimizer.JoinAlgoNestedLoop:
			if !ordinaryInnerNestedLoopPartial(x.plan) && !lateralProbeJoinPartial(x.plan, x.right) {
				return nil, false
			}
			return unwrapToSetOp(x.left)
		case optimizer.JoinAlgoMerge:
			return unwrapToSetOp(x.left)
		case optimizer.JoinAlgoHash:
			if probeSideIsLeft(x.plan) {
				return unwrapToSetOp(x.left)
			}
			return unwrapToSetOp(x.right)
		}
		return nil, false
	case *nestedLoopIndexJoinOp:
		if !optimizer.NestedLoopIndexJoinIsPartialCapable(x.plan) {
			return nil, false
		}
		return unwrapToSetOp(x.outer)
	}
	return nil, false
}

// attachAll wires every claim kind into op's driving scan. It reports whether
// ANY kind attached.
//
// The return value is NOT a safe-fallback signal, and no caller treats it as
// one. A tree with an unattached driving scan does not "stay serial": each
// participant runs a complete scan and the node returns N copies of every row.
// What actually keeps that from happening is the planner's producer, which
// refuses to build a partial subtree whose driving scan it cannot model
// (createGatherPlan). The executor cannot re-derive that here, because an
// injected child tree may legitimately be a non-scan source. So: precondition
// owned by the planner, reported (not enforced) here.
func (cs *parallelClaimSet) attachAll(op Operator) bool {
	if cs == nil {
		return false
	}
	// M0140-0006c: a SetOp streams two branches, each needing its OWN claim
	// state (see the type's setOpLeft/setOpRight comment) — dispatched
	// before the flat single-state walk below, which has no *setOp arm of
	// its own by design (one recursion here instead of three, one per
	// attachParallel* function).
	if so, ok := unwrapToSetOp(op); ok {
		// M0140-0006c-3: a branch the plan stamped claimed-whole
		// (SetOp.LeftNonPartial/RightNonPartial — PG's
		// pa_nonpartial_subpaths) gets NO scan attach at all. Instead the
		// leaf's claimedWhole flag is wired into the op so every
		// participant CAS-claims the branch before draining it
		// (nextStreaming); the winner runs its private copy serially, the
		// losers skip it. Wiring counts as attached — the flag IS the
		// claim state for that branch. A planless *setOp (synthetic
		// trees) takes the partial path on both sides, as before.
		left, right := false, false
		// M0145-0004a: both sides go through setOpBranch — the accessor is
		// what grows the next claim level when the branch op is itself a
		// nested partial *setOp (a multi-link UNION ALL chain). Reading the
		// fields directly would still work at level one but would hand a
		// nil child set to a deeper link — an unclaimed inner member is the
		// N-copies defect this type exists to prevent.
		if so.plan != nil && so.plan.LeftNonPartial {
			so.claimLeft = &cs.setOpBranch(false).claimedWhole
			left = true
		} else {
			left = cs.setOpBranch(false).attachAll(so.left)
		}
		if so.plan != nil && so.plan.RightNonPartial {
			so.claimRight = &cs.setOpBranch(true).claimedWhole
			right = true
		} else {
			right = cs.setOpBranch(true).attachAll(so.right)
		}
		return left || right
	}
	attached := attachParallelScan(op, cs.pscan)
	attached = attachParallelBitmapScan(op, cs.pbm) || attached
	attached = attachParallelIndexScan(op, cs.pidx) || attached
	attached = cs.attachParallelHashBuildSides(op) || attached
	return attached
}

// prebuildBitmap builds the TIDBitmap once before fan-out so workers
// share the result rather than each running their own index scan. (S5.6)
//
// C-19f/E-10: hoisted off gatherOp so gatherMergeOp runs the same pre-pass.
// This mirrors the pattern of prebuildSharedHashJoins: the leader builds the bitmap
// eagerly, publishes the sorted block list in a shared atomic allocator, and
// workers claim disjoint pages from it.
func (cs *parallelClaimSet) prebuildBitmap(ctx *Context, planChild optimizer.Node, buildChild func(scope *instrumenter) (Operator, error)) error {
	// Decide from the PLAN, before building anything.
	if !optimizer.HasBitmapScan(planChild) {
		return nil
	}
	// EX0-03b: prebuild throwaway tree — scope explicitly NIL
	// (uninstrumented, exactly today's behavior). Its drains would
	// double-count the same plan keys into a worker/leader table, and
	// the bitmap tree is never even closed, so its loops would leak.
	tree, err := buildChild(nil)
	if err != nil {
		return err
	}
	for _, tgt := range cs.bitmapPrebuildTargets(tree) {
		bm := tgt.bm
		if err := bm.Open(ctx); err != nil {
			return err
		}
		// Build the bitmap.
		tbm, err := bm.outerBitmap.buildBitmap(ctx)
		if err != nil {
			bm.Close()
			return err
		}
		bm.tbm = tbm
		bm.iter = tbmBeginIterate(tbm)
		bm.ownBitmap = true

		// Publish the sorted block list for workers.
		tgt.cs.pbm = newParallelBitmapState()
		tgt.cs.pbm.init(tbm)
	}
	return nil
}

// bitmapPrebuildTarget pairs a claim set with the throwaway-tree bitmap
// scan whose prebuilt bitmap it must publish (M0140-0006c-2 slice C).
type bitmapPrebuildTarget struct {
	cs *parallelClaimSet
	bm *bitmapHeapScanOp
}

// bitmapPrebuildTargets decides which (claim set, bitmap scan) pairs the
// prebuild must publish. A partial SetOp child streams TWO branches —
// each branch's driving bitmap is claimed through its own leaf claim set
// (cs.setOpLeft/setOpRight, the state attachAll's *setOp arm hands the
// branch's attach walk), so the prebuilt bitmap must publish per branch.
// Publishing only to the top-level cs.pbm would leave every worker's
// branch bitmap unattached — and attachAll's return is ignored by design,
// so a miss means each worker scans its own whole bitmap: the N-copies
// defect the claim sets exist to prevent.
func (cs *parallelClaimSet) bitmapPrebuildTargets(tree Operator) []bitmapPrebuildTarget {
	if so, ok := unwrapToSetOp(tree); ok {
		// The per-branch gate reads the op's own plan, not planChild —
		// robust to a plan wrapper above the SetOp. A planless *setOp
		// (synthetic trees only — the builder always sets plan) publishes
		// nothing rather than guessing.
		if so.plan == nil {
			return nil
		}
		// M0140-0006c-3: a branch stamped claimed-whole
		// (LeftNonPartial/RightNonPartial) is skipped — it gets no claim
		// attach (attachAll wires only the claimedWhole CAS flag), so its
		// leaf pbm must stay nil and the claiming worker's bitmap op builds
		// a private serial bitmap, exactly like a bitmap in any serial
		// subtree. Publishing a shared bitmap nobody attaches would be
		// wasted leader work at best.
		var out []bitmapPrebuildTarget
		if !so.plan.LeftNonPartial {
			out = cs.setOpBranch(false).appendBitmapPrebuildTarget(out, so.plan.Left, so.left)
		}
		if !so.plan.RightNonPartial {
			out = cs.setOpBranch(true).appendBitmapPrebuildTarget(out, so.plan.Right, so.right)
		}
		return out
	}
	var bmOps []*bitmapHeapScanOp
	collectBitmapScans(tree, &bmOps)
	// A partial subtree should have exactly one driving scan. If multiple
	// bitmap scans appear (unexpected), fall back rather than guessing
	// which one to share.
	if len(bmOps) != 1 {
		return nil
	}
	return []bitmapPrebuildTarget{{cs: cs, bm: bmOps[0]}}
}

// appendBitmapPrebuildTarget applies the flat rule to one SetOp branch:
// decide from the branch's PLAN (the same agreement rule the caller's
// HasBitmapScan gate applies at top level), then collect from the
// branch's own subtree. Zero collected means the bitmap the plan reported
// sits somewhere the collection walk does not reach (e.g. under a join
// build side, where no claim is needed — it is read whole); more than one
// is the same ambiguity the flat path refuses. Either way the branch
// publishes nothing and its attach fails closed.
func (cs *parallelClaimSet) appendBitmapPrebuildTarget(dst []bitmapPrebuildTarget, planBranch optimizer.Node, branch Operator) []bitmapPrebuildTarget {
	// M0145-0004a: the branch can itself be a partial UNION ALL link — a
	// nested *setOp whose members claim through THIS leaf set's own branch
	// sets, one level down. Recurse per side exactly as attachAll does so a
	// member bitmap publishes into the leaf claim set its workers will
	// actually attach (a flat collectBitmapScans here would find 2+ scans
	// and publish nothing, leaving every member bitmap unattached).
	if inner, ok := unwrapToSetOp(branch); ok {
		if inner.plan == nil {
			return dst
		}
		if !inner.plan.LeftNonPartial {
			dst = cs.setOpBranch(false).appendBitmapPrebuildTarget(dst, inner.plan.Left, inner.left)
		}
		if !inner.plan.RightNonPartial {
			dst = cs.setOpBranch(true).appendBitmapPrebuildTarget(dst, inner.plan.Right, inner.right)
		}
		return dst
	}
	if !optimizer.HasBitmapScan(planBranch) {
		return dst
	}
	var bmOps []*bitmapHeapScanOp
	collectBitmapScans(branch, &bmOps)
	if len(bmOps) != 1 {
		return dst
	}
	return append(dst, bitmapPrebuildTarget{cs: cs, bm: bmOps[0]})
}
