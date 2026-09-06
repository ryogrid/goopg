package optimizer

// parallel.go — P6 of docs/design/parallel-query/, chapter 08.
//
// The post-pass that decides whether a finished plan gets a Gather, and how
// many workers it plans for.
//
// Why a post-pass rather than partial paths in the join search: goopg's
// planner has no path abstraction to extend, and bushy.go's DP works over join
// orders, not over competing path variants. This mirrors the shape of the
// existing NLI and Memoize rewrites.
//
// TWO PROPERTIES ARE LOAD-BEARING, both discovered during the pre-implementation
// survey rather than designed in:
//
//  1. It must run AFTER the plan-cache lookup, per statement. plancache.go is
//     process-wide and cross-session, keyed on namespace-oid + normalised SQL
//     only — no session, no GUC fingerprint. A plan built under
//     max_parallel_workers_per_gather = 4 and cached would be reused by a
//     session that set it to 0, making `SET ... = 0` silently ineffective.
//     Caching serial plans and wrapping per statement is the only resolution
//     that needs no cache-key change.
//
//  2. It must be NON-MUTATING. The cached node is shared by every session that
//     runs the same SQL, concurrently. Editing any node in place would be a
//     data race that `make race-gate` would catch only under load. So the pass
//     returns a NEW root wrapping shared children, and never writes through a
//     plan pointer.

import (
	"os"
	"sync/atomic"

	"github.com/goopg/goopg/internal/catalog"
)

// parallelOn is the process-global kill switch, in the established house
// style (memoizeOn, the NLI cost-gate legacy flag). Default on; killed by
// GOOPG_PARALLEL=off at process start or by SetParallelEnabled from tests.
var parallelOn atomic.Bool

func init() {
	parallelOn.Store(parallelFromEnv(os.Getenv("GOOPG_PARALLEL")))
}

// parallelFromEnv is the kill-switch's polarity, factored out of init for the
// provenance table (flaglabels.go); see memoizeFromEnv.
func parallelFromEnv(v string) bool { return v != "off" }

// SetParallelEnabled toggles Gather insertion process-wide.
func SetParallelEnabled(on bool) { parallelOn.Store(on) }

// ParallelEnabled reports whether Gather insertion is active.
func ParallelEnabled() bool { return parallelOn.Load() }

// ParallelSettings carries the per-session GUCs the post-pass needs. They are
// passed explicitly rather than read from a global because
// max_parallel_workers_per_gather is per-session, and the existing GUC→planner
// bridge is a process-global atomic — adequate for a boolean kill switch,
// wrong for a per-session integer.
type ParallelSettings struct {
	// MaxWorkersPerGather is `max_parallel_workers_per_gather`. Zero disables.
	MaxWorkersPerGather int
	// MinTableScanBlocks is `min_parallel_table_scan_size`, in blocks.
	MinTableScanBlocks int64
	// DebugParallelQuery is `debug_parallel_query` ("off"/"on"/"regress"),
	// upstream's lever for forcing parallel plans in testing. When "on" or
	// "regress", the size gate is bypassed — but the SAFETY refusals are not.
	DebugParallelQuery string
	// IsSerializable suppresses parallelism under SERIALIZABLE.
	IsSerializable bool
	// LeaderParticipates is `parallel_leader_participation`. It reaches the
	// planner because the split cost model needs the parallel divisor, which
	// counts the leader as a worker while its contribution is positive
	// (chapter 11 §1.3). Zero value false is safe: it understates the divisor,
	// which understates the split's benefit.
	LeaderParticipates bool
	// DisableGatherMerge is `enable_gathermerge = off` (B-17c): PG's
	// cost_gather_merge flag (costsize.c:485). With no GatherMerge path in the
	// search (P5-04 open) there is no disabled_nodes count to carry, so the
	// post-pass gates the P7 arm instead: off falls back to the pre-P7 shape
	// (Gather below the Sort). Plain Gather is unaffected — upstream's
	// cost_gather has no flag either. Convert to counting when P5-04 lands
	// real Gather/GatherMerge paths.
	//
	// Opt-out on purpose: the zero value keeps the merge arm, so every
	// struct-literal constructor (production dispatch, executor tests) behaves
	// as before without setting anything; only an explicit off disables it.
	DisableGatherMerge bool
	// BlocksForTable returns a relation's size in blocks. Optional: when nil
	// the size gate falls back to the row estimate, which is an approximation
	// and is recorded as such.
	BlocksForTable func(*catalog.Table) (int64, bool)
}

// MaybeAddGather returns root wrapped in a Gather when the plan is eligible,
// or root unchanged.
//
// It never mutates root or anything below it.
func MaybeAddGather(root Node, s ParallelSettings) Node {
	if root == nil || !parallelOn.Load() {
		return root
	}

	// C-19d COEXISTENCE RULE — the path model wins.
	//
	// The search can now produce a `PathGather` / `PathGatherMerge` priced by
	// cost_gather / cost_gather_merge (gatherpaths.go), so a plan reaching
	// this pass may ALREADY carry a Gather that add_path chose. Re-deciding
	// that with a size rule is precisely the defect Phase 5 removes, so the
	// post-pass stands down: the costed placement is kept as it is.
	//
	// This is not tidiness, it is a correctness stop. `terminatesPartial`
	// lists *Gather / *GatherMerge, and `findPartialSubtree` DESCENDS through
	// a terminating single-child node — so without this rule the post-pass
	// would nest a second Gather BELOW the path model's one: N workers each
	// launching N workers, and `gatherOp.prebuildHashJoins`'s "a Gather never
	// appears inside another Gather's partial subtree" comment silently
	// falsified.
	//
	// `EXPLAIN <query>` answers identically without a special case: an
	// *Explain root has no `parallelChildren` arm, so the scan finds nothing
	// here and the unwrap below re-enters MaybeAddGather on `ex.Child`, where
	// the check runs against the real plan. It is the check C-19h deletes
	// together with the pass.
	if subtreeHasGather(root) {
		return root
	}

	// EXPLAIN carries the real plan in Child. Descend so `EXPLAIN <query>`
	// renders the SAME plan the query would execute — otherwise EXPLAIN would
	// systematically under-report parallelism, which is worse than useless: it
	// is the tool people use to check whether parallelism happened.
	if ex, ok := root.(*Explain); ok {
		inner := MaybeAddGather(ex.Child, s)
		if inner == ex.Child {
			return root
		}
		c := *ex
		c.Child = inner
		return &c
	}
	if s.MaxWorkersPerGather <= 0 {
		return root
	}
	if s.IsSerializable {
		// SSI predicate-lock acquisition is a genuine write on the scan read
		// path, funnelling through one mutex. PG itself only allowed parallel
		// query under SERIALIZABLE from v12, and it was not cheap.
		return root
	}
	if !statementIsParallelSafe(root) {
		return root
	}

	// Find the deepest point at which the subtree below is partial-capable.
	tgt, ok := findPartialSubtree(root, s)
	if !ok {
		return root
	}

	// Worker count comes from the scan, so a wrapper target (Sort, Aggregate)
	// is sized by what it reads rather than by itself.
	sized := tgt.node
	switch {
	case tgt.mergeKeys != nil:
		sized = tgt.node.(*Sort).Child
	case tgt.splitAgg:
		sized = tgt.node.(*Aggregate).Child
	}
	workers := computeParallelWorkers(sized, s)
	if workers <= 0 {
		return root
	}

	return rebuildWithGather(root, tgt, workers)
}

// subtreeHasGather reports whether the tree already carries a Gather or a
// Gather Merge anywhere — the C-19d coexistence test.
//
// The walk is `parallelChildren`'s, the same one `findPartialSubtree` descends,
// so "already gathered" is judged over exactly the nodes the post-pass would
// have considered placing a Gather among. A node `parallelChildren` does not
// model reads as having no children, which for THIS question fails toward
// "no Gather found" — i.e. toward the post-pass acting. That is the safe
// direction only because the path model cannot put a Gather under an
// unmodelled node in the first place: `createGatherPlan` panics unless
// `drivingScan` reaches the subtree's scan through this same walk.
func subtreeHasGather(n Node) bool {
	switch n.(type) {
	case nil:
		return false
	case *Gather, *GatherMerge:
		return true
	}
	for _, c := range parallelChildren(n) {
		if subtreeHasGather(c) {
			return true
		}
	}
	return false
}

// partialTarget is where the Gather goes and what kind it must be.
type partialTarget struct {
	node Node
	// mergeKeys non-nil ⇒ the target is a Sort the workers run themselves, so
	// the leader must MERGE the streams rather than concatenate them.
	mergeKeys []SortKey
	// splitAgg ⇒ the target is an Aggregate to split into Partial (in the
	// workers) and Finalize (in the leader).
	splitAgg bool
}

// statementIsParallelSafe applies the whole-plan refusals. Each is a case
// where the substrate cannot currently guarantee correctness — not a case
// where the shape is uninteresting.
func statementIsParallelSafe(n Node) bool {
	switch n.(type) {
	case *Insert, *Update, *Delete, *Merge, *CTEDMLPrefix:
		// Workers must never assign an XID, mutate the subxact stack, touch
		// the catalog, or release locks — and lockmgr release is destructive
		// for the whole transaction (holders are a bitmask, not a refcount).
		return false
	case *DDL, *Transaction:
		return false
	}
	return !subtreeHasUnsafeNode(n)
}

// subtreeHasUnsafeNode walks for shapes a worker cannot execute.
func subtreeHasUnsafeNode(n Node) bool {
	unsafe := false
	var walk func(Node)
	walk = func(cur Node) {
		if cur == nil || unsafe {
			return
		}
		switch x := cur.(type) {
		case *LockRows:
			// SELECT ... FOR UPDATE/SHARE. Not DML, but it stamps xmax and
			// needs LockMgr, and workers may not release locks. PG likewise
			// disables parallelism outright for plans carrying row marks.
			unsafe = true
			return
		case *SeqScan:
			if tableIsUnsafeForParallel(x.Table) {
				unsafe = true
				return
			}
		case *IndexScan:
			if tableIsUnsafeForParallel(x.Table) {
				unsafe = true
				return
			}
		case *IndexOnlyScan:
			if tableIsUnsafeForParallel(x.Table) {
				unsafe = true
				return
			}
		case *BitmapHeapScan:
			if tableIsUnsafeForParallel(x.Table) {
				unsafe = true
				return
			}
		}
		for _, c := range parallelChildren(cur) {
			walk(c)
		}
	}
	walk(n)
	return unsafe
}

// tableIsUnsafeForParallel refuses relations a worker cannot read.
func tableIsUnsafeForParallel(t *catalog.Table) bool {
	if t == nil {
		return true
	}
	if t.Temp {
		// PG marks temp access parallel-restricted. goopg's shared address
		// space might well make it safe — TempTableShadows is per-session
		// state, not per-process — but "might well be safe" is not an
		// argument, and no analysis establishes one. Refused pending that.
		return true
	}
	if t.Virtual {
		// Virtual catalog relations are backed by the Pg*Rows callbacks,
		// which NewWorkerContext deliberately nils so a stray call panics at
		// the call site. A Gather over pg_class would die there — that is the
		// backstop working, but the planner must not build the plan.
		return true
	}
	return false
}

// findPartialSubtree returns the node whose subtree may run in workers.
//
// The rule: push the Gather as low as possible while keeping the partial
// subtree as large as possible — i.e. immediately below the lowest node that
// terminates partial-ness. PG reaches the same placement by costing partial
// paths; here it is by construction.
func findPartialSubtree(root Node, s ParallelSettings) (partialTarget, bool) {
	cur := root
	for {
		// P9: an Aggregate over a partial-capable subtree splits rather than
		// terminating. Each worker aggregates its own share and publishes
		// per-group transition states; the leader combines them. PG's
		//
		//   Finalize HashAggregate -> Gather -> Partial HashAggregate -> Parallel Seq Scan
		//
		// This is the placement that matters most on TPC-H. Without it Q1
		// funnels ~5.9 M rows through the Gather into ONE leader-side
		// aggregate to produce four groups, and measurement shows that serial
		// tail pinning the query at ~7.1 s no matter how many workers run.
		if agg, isAgg := cur.(*Aggregate); isAgg && aggregateSplitIsSafe(agg) &&
			drivingScan(agg.Child) != nil {
			// The gate has to run HERE, not after the walk. Refusing must let
			// the loop fall through terminatesPartial(*Aggregate) and place
			// the Gather BELOW the aggregate — and that fallback is precisely
			// the "without the split" alternative the cost model costs
			// against.
			//
			// That is also why the settings are threaded in: the model needs
			// the parallel divisor, and MaybeAddGather does not compute the
			// worker count until after this walk has chosen a target. There is
			// no circularity — the sizing input (agg.Child) is in scope right
			// here — so the resolution is simply to size it now.
			//
			// C-19g (P5-07): the verdict is now a two-candidate PATH
			// tournament — Finalize->Gather->Partial against
			// Agg->Gather->input — priced by `costAgg` + `cost_gather` and
			// adjudicated by `addPath`/`setCheapest`
			// (partialaggpaths.go). `GOOPG_PARTIAL_AGG_PATHS=off`, the
			// default until the measurement in
			// docs/design/planner-c19g-partial-agg/DESIGN.md §6 says
			// otherwise, delegates to the retired size rule unchanged.
			// The construction site below is unchanged and is still the
			// ONLY one, which is what makes double-splitting structurally
			// impossible.
			if partialAggSplitPays(agg, computeParallelWorkers(agg.Child, s), s.LeaderParticipates) {
				return partialTarget{node: agg, splitAgg: true}, true
			}
		}
		// P7: a Sort directly over a partial-capable subtree does not have to
		// terminate partial-ness. Each worker sorts its own partition and the
		// leader merges the ordered streams — PG's
		//
		//   Gather Merge -> Sort -> Parallel Seq Scan
		//
		// which moves the sort, the expensive part, off the leader entirely.
		// This is reported to the caller rather than decided here because it
		// changes which Gather variant gets built, and building a plain Gather
		// over per-worker Sorts would silently return unordered rows.
		//
		// goopg's Sort carries no top-N limit, so there is no per-worker
		// truncation to reason about: every worker emits its whole partition.
		if srt, isSort := cur.(*Sort); isSort && len(srt.Keys) > 0 &&
			!s.DisableGatherMerge &&
			drivingScan(srt.Child) != nil && sortPartialRootPays(srt) {
			return partialTarget{node: srt, mergeKeys: srt.Keys}, true
		}
		if terminatesPartial(cur) {
			kids := parallelChildren(cur)
			if len(kids) != 1 {
				return partialTarget{}, false
			}
			cur = kids[0]
			continue
		}
		// cur is partial-capable if it bottoms out in an eligible seq scan.
		if drivingScan(cur) != nil {
			return partialTarget{node: cur}, true
		}
		kids := parallelChildren(cur)
		if len(kids) != 1 {
			return partialTarget{}, false
		}
		cur = kids[0]
	}
}

// sortPartialRootPays reports whether moving the sort into the workers — P7's
// `Gather Merge -> Sort -> <partial>` — is worth it for this subtree.
//
// It answers NO when the driving scan is an index-only scan, and that is a
// MEASURED restriction, not a preference (M0134-0189). Taking the Sort as the
// partial root gives every worker its own Sort and the leader a k-way merge on
// top: the total comparison work is unchanged, a merge stage is added, and the
// scan saving has to pay for both. For a parallel SEQ scan it does — that is
// why the arm exists. For a parallel index-only scan it does not, because the
// IOS is already the cheap half of these plans: a CPU profile of TPC-H q16
// under this shape puts 34% in `sortOp.lessRows` / `sortTailWithCTIDs` and the
// scan nowhere in the top nodes, and enabling it measured q16 1.5s -> 2.3s and
// q13 4.2s -> 6.8s.
//
// Declining here does not make the subtree serial. The walk falls through to
// `terminatesPartial(*Sort)` and descends, so the Gather lands BELOW the sort
// instead: `Sort -> Gather -> <joins> -> Parallel Index Only Scan`, which
// parallelises the scan and the joins while leaving exactly one sort, in the
// leader, where the serial plan already had it.
//
// The honest shape of this rule is "per-worker sort must be cheaper than the
// merge it forces", which needs a cost model goopg's parallel post-pass does
// not have — it is a size rule (`computeParallelWorkers`), not a cost
// comparison. Until that exists this states the one case measurement has
// actually settled, and states it where the decision is made rather than by
// disabling the scan type outright.
//
// C-19c: a plain `*IndexScan` driving scan declines for the same reason.
//
// It used to decline for a SECOND, harder reason as well — the Gather Merge
// operator attached only the seq-scan block allocator to its workers, not the
// index leaf-claim set, so a per-worker Sort over an index scan under Gather
// Merge would have returned every row once per worker. **That reason is no
// longer true**: E-10 (`a22d995c8`) gave both gather operators a shared
// `parallelClaimSet` covering all three claim kinds, with an anti-drift test.
// The correctness hazard is gone; only the measured cost argument above
// remains, and it is what still carries this decline (q16 1.5 -> 2.3 s,
// q13 4.2 -> 6.8 s). Do not read the removal of the hazard as a reason to
// flip this rule — that needs the cost comparison the rule's own text says
// goopg's post-pass does not have.
func sortPartialRootPays(srt *Sort) bool {
	switch drivingScan(srt.Child).(type) {
	case *IndexOnlyScan, *IndexScan:
		return false
	}
	return true
}

// terminatesPartial reports whether a Gather must sit at or below this node.
func terminatesPartial(n Node) bool {
	switch n.(type) {
	case *Limit, *Distinct, *DistinctOn, *WindowAgg, *SetOp,
		*RecursiveUnion, *WorkTableScan, *Memoize, *NestedLoopIndexJoin,
		*Aggregate, *Sort, *Gather, *GatherMerge:
		// Aggregate reaches here only when the split was REFUSED — either the
		// node is not decomposable (aggregateSplitIsSafe) or the cost model
		// declined (partialAggSplitPays). Terminating is then correct
		// and is the fallback the model costs against: the Gather goes below
		// the aggregate, which is the pre-P9 shape.
		//
		// Sort remains listed because it terminates in the GENERAL case — a
		// Sort over something that is not a plain partial scan. The one shape
		// it does not terminate, Sort directly over a partial-capable subtree,
		// is taken by findPartialSubtree before this check runs.
		return true
	}
	return false
}

// stampParallelScan is drivingScan's copy-on-write sibling: it performs the
// EXACT SAME traversal decision (Filter/Project pass-through, Join probe-side
// only under hashJoinIsPartialCapable, terminating on *SeqScan/*BitmapHeapScan)
// but instead of merely locating the driving scan, it returns a NEW tree with
// Parallel: true stamped on a COPY of that scan. This mirrors PostgreSQL's
// parallel_aware, which is set per-PATH-CHOICE at path-construction time
// (create_seqscan_path, pathnode.c:996; create_bitmap_heap_path,
// pathnode.c:1115) — not inferred later by walking under a Gather (a
// render-time "am I under a Gather" walk would mislabel a future single-copy
// Gather over a non-partial subtree; see docs/design's Q3 discussion).
//
// SIBLING WARNING: this traversal must stay identical to drivingScan's. If
// the two ever diverge, a scan gets labelled "Parallel " that workers never
// actually read, or vice versa — drivingScan decides ELIGIBILITY,
// stampParallelScan decides the render-time LABEL for that same decision.
//
// Respects this file's non-mutating discipline (file-level comment above):
// every node on the path down to the terminal scan is shallow-copied
// (mirroring replaceSingleChild, below), and if no eligible scan is reached,
// n is returned UNCHANGED (identical pointer) — no copies leaked.
func stampParallelScan(n Node) Node {
	switch x := n.(type) {
	case *SeqScan:
		c := *x
		c.Parallel = true
		return &c
	case *BitmapHeapScan:
		c := *x
		c.Parallel = true
		return &c
	case *IndexOnlyScan:
		c := *x
		c.Parallel = true
		return &c
	case *IndexScan:
		// C-19c. The SAME predicate drivingScan admits on, so the two
		// functions cannot disagree about a plain index scan: a node
		// drivingScan refused is returned unchanged here too.
		if !plainIndexScanIsPartialCapable(x) {
			return n
		}
		c := *x
		c.Parallel = true
		return &c
	case *Filter:
		child := stampParallelScan(x.Child)
		if child == x.Child {
			return x
		}
		c := *x
		c.Child = child
		return &c
	case *Project:
		child := stampParallelScan(x.Child)
		if child == x.Child {
			return x
		}
		c := *x
		c.Child = child
		return &c
	case *Join:
		// P8 (drivingScan): a hash join is partial through its PROBE side
		// only. Mirrored here so the same side gets labelled.
		if !hashJoinIsPartialCapable(x) {
			return n
		}
		if joinProbeSideIsLeft(x) {
			left := stampParallelScan(x.Left)
			if left == x.Left {
				return x
			}
			c := *x
			c.Left = left
			return &c
		}
		right := stampParallelScan(x.Right)
		if right == x.Right {
			return x
		}
		c := *x
		c.Right = right
		return &c
	}
	return n
}

// drivingScan finds the scan node a subtree ultimately reads from, or nil
// when the subtree is not driven by a single scannable relation.
//
// S5.6: extended from SeqScan-only to also recognize BitmapHeapScan, so that
// a parallel bitmap path satisfies the same partial-subtree walk.
func drivingScan(n Node) Node {
	switch x := n.(type) {
	case *SeqScan:
		return x
	case *BitmapHeapScan:
		return x
	case *IndexOnlyScan:
		// M0134-0189. An index-only scan is partial the same way a seq scan
		// is: workers split the relation, here by index LEAF BLOCK rather
		// than heap block. Admitted where a plain *IndexScan is not, because
		// the IOS materialises its whole range in Open — so the split is a
		// partition of that materialisation — while an index scan is also the
		// NLI probe shape, under which a Gather never sits.
		return x
	case *IndexScan:
		// C-19c (P5-03). The plain index scan is eager at Open the same way
		// the IOS is — it materialises its TID list through one leaf-chain
		// walk (operators_index.go, M0092-0001) — so the IOS's leaf-block
		// partition applies to it unchanged: attachParallelIndexScan hands
		// the worker the shared claim set and RangeScanWithPosLeafFilter
		// keeps only the leaves it owns. PG's counterpart is a Parallel
		// Index Scan, which is `amcanparallel` (btree only) and never a
		// bitmap input (build_index_paths, indxpath.c).
		//
		// Admitted only as a bare RANGE or FULL scan. A point probe (Key /
		// Keys) or a SAOP multi-descent is refused: this post-pass sizes
		// workers on the TABLE's block count (computeParallelWorkers), not
		// on what the probe fetches — PG's cost_index passes the fetched
		// heap pages to compute_parallel_worker, which is what stops a
		// 1-row probe on a large relation from gathering. Until C-19d makes
		// that price the decider, the shape rule stands in for it. A SAOP
		// scan is also the one site (rescanSAOP) that does not consult the
		// leaf filter, so refusing it here is what keeps that site serial.
		// The NLI inner probe is not reached at all: terminatesPartial
		// stops at *NestedLoopIndexJoin.
		if !plainIndexScanIsPartialCapable(x) {
			return nil
		}
		return x
	case *Filter:
		return drivingScan(x.Child)
	case *Project:
		return drivingScan(x.Child)
	case *Join:
		// P8. A hash join is partial through its PROBE side only: the build
		// side is drained once by the leader before fan-out, and the probe is
		// what the workers split. Returning the probe's scan also makes the
		// size rule measure the right relation — the probe is the big side by
		// the planner's own build-side choice.
		if !hashJoinIsPartialCapable(x) {
			return nil
		}
		if joinProbeSideIsLeft(x) {
			return drivingScan(x.Left)
		}
		return drivingScan(x.Right)
	}
	return nil
}

// plainIndexScanIsPartialCapable is the one predicate drivingScan (eligibility)
// and stampParallelScan (label) share for a plain `*IndexScan` — see the
// drivingScan arm for what each condition protects. C-19c.
func plainIndexScanIsPartialCapable(x *IndexScan) bool {
	return x != nil && x.Index != nil && isBTreeIndex(x.Index) && !x.Index.DeclaredHash &&
		x.Key == nil && len(x.Keys) == 0 && len(x.SAOPKeys) == 0
}

// The `*MultiHashJoin` arm and its `multiHashJoinIsPartialCapable` approval
// point were deleted with the node by M0127-P6.2. An MHJ was partial through
// its PROBE side only, and needed no shared build: each worker rebuilt the
// (small, by the probe-selection rule) dimension tables itself. What survives
// is the discipline the approval point existed to enforce — every arm of
// `drivingScan` names its partial side explicitly and refuses toward serial
// rather than deriving a side that could disagree across call sites.

// ParallelChildrenForTest exposes the post-pass's child walk to tests in other
// packages, which need to traverse a rebuilt plan to find the Gather.
func ParallelChildrenForTest(n Node) []Node { return parallelChildren(n) }

// HasBitmapScan reports whether a partial subtree contains a BitmapHeapScan.
// The executor asks this BEFORE constructing anything, matching the pattern of
// HasShareableHashJoin. (S5.6)
func HasBitmapScan(n Node) bool {
	switch n.(type) {
	case *BitmapHeapScan:
		return true
	}
	for _, c := range parallelChildren(n) {
		if HasBitmapScan(c) {
			return true
		}
	}
	return false
}

// HasShareableHashJoin reports whether a partial subtree contains a hash join
// whose build side the leader must pre-build before fanning out (P8).
//
// The executor asks this BEFORE constructing anything. Building a throwaway
// operator tree to find out would call the Gather's child-builder an extra
// time, and a child-builder is not required to be side-effect-free.
func HasShareableHashJoin(n Node) bool {
	switch x := n.(type) {
	case nil:
		return false
	case *Join:
		if !hashJoinIsPartialCapable(x) {
			return false
		}
		return true
	}
	for _, c := range parallelChildren(n) {
		if HasShareableHashJoin(c) {
			return true
		}
	}
	return false
}

// joinProbeSideIsLeft mirrors the executor's probeSideIsLeft. The two must
// agree: if they disagree the parallel scan lands on the BUILD side, every
// worker hashes a partition of the build input, and the join silently drops
// matches.
func joinProbeSideIsLeft(p *Join) bool {
	buildLeft := p.BuildLeft
	if p.Type == JoinTypeSemi || p.Type == JoinTypeAnti {
		buildLeft = false
	}
	return !buildLeft
}

// hashJoinIsPartialCapable states which hash joins may run with a partial
// probe side.
//
// The rule is not "hash join" but "hash join whose per-probe-row verdict is
// worker-local". Everything below turns on that:
//
//   - INNER, SEMI and ANTI decide each probe row against the frozen build
//     table alone, so partitioning the probe is transparent.
//   - LEFT qualifies only with the outer on the PROBE side (!BuildLeft), which
//     is the only LEFT shape the lazy-hash runtime implements anyway: its
//     null-padding is per-probe-row and needs no cross-worker state.
//   - FULL and RIGHT would require knowing which BUILD rows went unmatched
//     across ALL workers — a cross-worker reduction that does not exist here.
//     Refused rather than approximated.
//
// LATERAL is excluded because its right side is re-planned per outer row and
// never takes the hash path at all.
func hashJoinIsPartialCapable(p *Join) bool {
	if p == nil || p.Algo != JoinAlgoHash || p.Lateral {
		return false
	}
	switch p.Type {
	case JoinTypeInner, JoinTypeSemi, JoinTypeAnti:
		return true
	case JoinTypeLeft:
		return !p.BuildLeft
	}
	return false
}

// scanTable extracts the *catalog.Table from a scan node (SeqScan,
// BitmapHeapScan, IndexOnlyScan or — C-19c — a plain IndexScan). Returns nil
// for any other node kind.
//
// A plain index scan is sized on its TABLE's blocks below, the heap arm of
// compute_parallel_worker. PG sizes it on min(heap ladder over the heap pages
// the scan fetches, index ladder over the index pages it reads); the path
// model's twin (costPartialIndexScan) does exactly that, while this post-pass
// has only the live block counts. The heap ladder is the binding one for the
// shapes drivingScan admits (a range or full scan reads most of the heap, and
// an index is far smaller than its heap against a 16x smaller threshold), so
// until C-19h retires the post-pass the two agree in practice.
func scanTable(n Node) *catalog.Table {
	switch x := n.(type) {
	case *SeqScan:
		return x.Table
	case *BitmapHeapScan:
		return x.Table
	case *IndexOnlyScan:
		return x.Table
	case *IndexScan:
		return x.Table
	}
	return nil
}

// computeParallelWorkers reproduces upstream's compute_parallel_worker()
// (postgres/src/backend/optimizer/path/allpaths.c): a SIZE RULE, not a cost
// comparison — which is exactly why it is reproducible here despite goopg
// having no absolute node costs to add parallel_setup_cost to.
func computeParallelWorkers(subtree Node, s ParallelSettings) int {
	scan := drivingScan(subtree)
	tbl := scanTable(scan)
	if tbl == nil {
		return 0
	}

	forced := s.DebugParallelQuery == "on" || s.DebugParallelQuery == "regress"

	// The table's parallel_workers reloption wins outright, as in PG.
	if n := tableParallelWorkersReloption(tbl); n > 0 {
		return min(n, s.MaxWorkersPerGather)
	}

	blocks, known := parallelRelationBlocks(tbl, s)
	// An index-only scan never reads the heap, so the heap's size is not what
	// bounds its parallelism — the INDEX's is. PG sizes exactly this way:
	// `create_index_paths` passes `index->pages` to `compute_parallel_worker`
	// (allpaths.c) rather than the relation's, so a covering scan of a small
	// index does not get the worker count its big table would earn.
	//
	// Measured, and it is what separates the two TPC-H index-only queries
	// (M0134-0189): q16 scans `partsupp_pk`, 2770 real blocks, which clears
	// the threshold and gains from the split (1.5s -> 1.2s). q13 scans
	// `customer_pk`, a few hundred blocks over a 3822-block table — sized by
	// the heap it was granted workers and the Gather cost more than the scan
	// saved (4.2s -> 7.1s); sized by the index it is below the threshold and
	// stays serial, which is the right answer and PG's own rule.
	if ios, isIOS := scan.(*IndexOnlyScan); isIOS && ios.Index != nil {
		if pages, ok := catalog.IndexRealPages(ios.Index); ok && pages > 0 {
			blocks, known = pages, true
		}
	}
	if !known {
		// Size unknown — refuse. Note this is NOT "no ANALYZE statistics":
		// the size input is a live block count, which needs no ANALYZE (see
		// parallelRelationBlocks). Reaching here means the storage manager
		// could not answer at all, which is a real anomaly rather than a
		// normal cold-start state.
		if !forced {
			return 0
		}
		blocks = s.MinTableScanBlocks
	}

	threshold := s.MinTableScanBlocks
	if threshold < 1 {
		threshold = 1
	}
	if blocks < threshold && !forced {
		return 0
	}

	// Start at one worker and add one every time the size passes another
	// factor of three. PG's own comment calls this "probably needs to be a
	// good deal more sophisticated"; reproducing it keeps worker counts
	// PG-comparable, which is what EXPLAIN comparisons depend on.
	workers := 1
	for blocks >= threshold*3 {
		workers++
		threshold *= 3
		if threshold > (1<<31-1)/3 { // upstream's INT_MAX/3 overflow break
			break
		}
	}
	return min(workers, s.MaxWorkersPerGather)
}

// parallelRelationBlocks returns the relation size in blocks.
//
// This is a SIZE query, not a statistics lookup, and the distinction is the
// whole reason the gate works on a freshly started server.
//
// PG does the same thing: compute_parallel_worker() takes `rel->pages`, and
// estimate_rel_size() fills that from a live RelationGetNumberOfBlocks() call
// (postgres/src/backend/optimizer/util/plancat.c:1097-1100). `pg_class.relpages`
// — the ANALYZE/VACUUM-maintained catalog value — is consulted only afterwards,
// to scale the TUPLE estimate. So PG chooses a worker count without needing
// ANALYZE at all, and so does this.
//
// An earlier cut of this function used `Stats.RowCount / 60` as a block proxy.
// That was wrong twice over: the divisor was invented, and RowCount is never
// restored at startup even though the column statistics are
// (internal/initdb/open.go builds TableStats{Columns: ...} with RowCount left
// zero — see the deferral ledger). The result was a gate that refused every
// query on any server that had not been ANALYZEd since boot, while looking
// like a deliberate policy.
func parallelRelationBlocks(t *catalog.Table, s ParallelSettings) (int64, bool) {
	if s.BlocksForTable == nil {
		return 0, false
	}
	return s.BlocksForTable(t)
}

// tableParallelWorkersReloption reads the per-table parallel_workers setting.
// Returns 0 when unset.
func tableParallelWorkersReloption(t *catalog.Table) int {
	if t == nil || !t.ParallelWorkersSet {
		// The reloption's default is -1 = unset, so ParallelWorkersSet — not
		// a zero check — is what says whether the user specified it. goopg
		// has parsed and stored this since M0110-0001 and never read it; P6
		// is where it becomes load-bearing.
		return 0
	}
	return t.ParallelWorkers
}

// rebuildWithGather returns a copy of root's spine with target replaced by
// Gather{target}. Nodes not on the path are shared by pointer; nothing is
// mutated.
func rebuildWithGather(root Node, tgt partialTarget, workers int) Node {
	if root == tgt.node {
		switch {
		case tgt.mergeKeys != nil:
			stamped := stampParallelScan(root)
			return NewGatherMerge(stamped.Pos(), stamped, workers, tgt.mergeKeys)
		case tgt.splitAgg:
			return splitAggregate(root.(*Aggregate), workers)
		}
		stamped := stampParallelScan(root)
		return NewGather(stamped.Pos(), stamped, workers)
	}
	kids := parallelChildren(root)
	if len(kids) != 1 {
		return root
	}
	rebuilt := rebuildWithGather(kids[0], tgt, workers)
	if rebuilt == kids[0] {
		return root
	}
	return replaceSingleChild(root, rebuilt)
}

// splitAggregate turns one Aggregate into Finalize -> Gather -> Partial.
//
// Both new nodes are SHALLOW COPIES of the original: the post-pass runs on a
// plan held by a process-wide cache that other sessions may be executing right
// now, so setting Mode on the original would be a data race and would also
// corrupt the serial plan every other session sees.
//
// The schema is unchanged at every level. The Partial node emits no ROWS at
// all — it publishes per-group states through a side channel and the Finalize
// reads them from there — so nothing downstream sees a different shape, and
// the Gather in between needs no knowledge of aggregation whatsoever.
func splitAggregate(a *Aggregate, workers int) Node {
	partial := *a
	partial.Mode = AggModePartial
	// Stamp the "Parallel " label on the partial side's driving scan — the
	// split-aggregate shape puts a real Gather here too (rebuildWithGather's
	// sibling call site).
	partial.Child = stampParallelScan(partial.Child)

	gather := NewGather(a.Pos(), &partial, workers)

	final := *a
	final.Mode = AggModeFinal
	final.Child = gather
	final.PartialSource = &partial
	// B-01c second cut: the Final's child is now the Gather over the
	// Partial's output row, not the original input row, so the copied
	// group-input keep (positions into the input schema) no longer
	// addresses this node's child — decline to unknown, the safe
	// direction. The Partial keeps the original's stamp: it reads the
	// same input row (stampParallelScan only labels the scan).
	// Payload-only, no plan change.
	final.InputTarget, final.InputTargetKnown = nil, false
	return &final
}

// replaceSingleChild returns a SHALLOW COPY of n with its single child
// replaced. Copying is what keeps the pass non-mutating: the original node is
// still referenced by the plan cache and by any session executing it
// concurrently.
func replaceSingleChild(n Node, child Node) Node {
	switch x := n.(type) {
	case *Project:
		c := *x
		c.Child = child
		return &c
	case *Filter:
		c := *x
		c.Child = child
		return &c
	case *Limit:
		c := *x
		c.Child = child
		return &c
	case *Sort:
		c := *x
		c.Child = child
		return &c
	case *Aggregate:
		c := *x
		c.Child = child
		return &c
	case *Distinct:
		c := *x
		c.Child = child
		return &c
	case *DistinctOn:
		// C-16b: the unique winner is a DistinctOn; without this arm
		// rebuildWithGather silently drops Gather insertion across it
		// (safe direction — correct plan, missed parallelization — but
		// a prior review's requirement must not evaporate silently).
		c := *x
		c.Child = child
		return &c
	}
	// Unknown wrapper: refuse rather than guess. Returning n unchanged means
	// no Gather is inserted, which is always safe.
	return n
}

// parallelChildren returns a node's children for the walks above. It is
// deliberately conservative: an unlisted node kind reports no children, which
// makes the enclosing walks refuse rather than descend into something they do
// not model.
func parallelChildren(n Node) []Node {
	switch x := n.(type) {
	case *Project:
		return []Node{x.Child}
	case *Filter:
		return []Node{x.Child}
	case *Limit:
		return []Node{x.Child}
	case *Sort:
		return []Node{x.Child}
	case *Aggregate:
		return []Node{x.Child}
	case *Distinct:
		return []Node{x.Child}
	case *DistinctOn:
		return []Node{x.Child}
	case *Gather:
		return []Node{x.Child}
	case *LockRows:
		return []Node{x.Child}
	case *Join:
		// Both sides. This is what lets the SAFETY walk
		// (subtreeHasUnsafeNode) see temp tables, virtual catalog relations
		// and LockRows sitting under a join once P8 made joins
		// partial-capable. Two children also make findPartialSubtree and
		// rebuildWithGather refuse any join drivingScan has not explicitly
		// approved, since both bail on len(kids) != 1.
		return []Node{x.Left, x.Right}
	// The `*MultiHashJoin` arm here (chapter 12 §7) went with the node in
	// M0127-P6.2. Its rule binds every arm added after it: a node missing from
	// this switch makes `subtreeHasUnsafeNode` stop dead and read "no children"
	// as "nothing unsafe below" — the opposite of conservative for the SAFETY
	// walk — so a temp / virtual / LockRows relation underneath would go unseen
	// the moment `drivingScan` learned to descend through it.
	case *NestedLoopIndexJoin:
		return []Node{x.Outer, x.Inner}
	case *BitmapHeapScan:
		// S5.6: the Outer bitmap-producing subtree is the child.
		return []Node{x.Outer}
	}
	return nil
}
