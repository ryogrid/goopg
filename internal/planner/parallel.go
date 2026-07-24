package planner

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
	parallelOn.Store(os.Getenv("GOOPG_PARALLEL") != "off")
}

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
		case *Aggregate:
			// An order-sensitive aggregate above a Gather returns its elements
			// in worker-arrival order — a different order on every run. It is
			// not decomposable either, so parallelising below it buys only the
			// scan and costs determinism. Refused outright.
			for _, call := range x.Aggs {
				if AggregateIsOrderSensitive(call) {
					unsafe = true
					return
				}
			}
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
			drivingSeqScan(agg.Child) != nil {
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
			if splitAggregateIsProfitable(agg, computeParallelWorkers(agg.Child, s), s.LeaderParticipates) {
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
			drivingSeqScan(srt.Child) != nil {
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
		if drivingSeqScan(cur) != nil {
			return partialTarget{node: cur}, true
		}
		kids := parallelChildren(cur)
		if len(kids) != 1 {
			return partialTarget{}, false
		}
		cur = kids[0]
	}
}

// terminatesPartial reports whether a Gather must sit at or below this node.
func terminatesPartial(n Node) bool {
	switch n.(type) {
	case *Limit, *Distinct, *DistinctOn, *WindowAgg, *SetOp,
		*RecursiveUnion, *WorkTableScan, *Memoize, *NestedLoopIndexJoin,
		*Aggregate, *Sort, *Gather, *GatherMerge:
		// Aggregate reaches here only when the split was REFUSED — either the
		// node is not decomposable (aggregateSplitIsSafe) or the cost model
		// declined (splitAggregateIsProfitable). Terminating is then correct
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

// drivingSeqScan finds the SeqScan a subtree ultimately reads from, or nil
// when the subtree is not driven by a single sequential scan.
func drivingSeqScan(n Node) *SeqScan {
	switch x := n.(type) {
	case *SeqScan:
		return x
	case *Filter:
		return drivingSeqScan(x.Child)
	case *Project:
		return drivingSeqScan(x.Child)
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
			return drivingSeqScan(x.Left)
		}
		return drivingSeqScan(x.Right)
	case *MultiHashJoin:
		// Chapter 12. An MHJ is partial through its PROBE side only. Unlike a
		// two-way Join it needs no shared build: attachParallelScan partitions
		// only this probe scan, so each worker rebuilds the (small, by the
		// probe-selection rule) dimension tables itself and the result is
		// exactly correct. The probe is named explicitly, so there is no
		// side-derivation that could disagree across call sites.
		if !multiHashJoinIsPartialCapable(x) {
			return nil
		}
		return drivingSeqScan(x.Tables[x.ProbeTable])
	}
	return nil
}

// multiHashJoinIsPartialCapable is the explicit approval point drivingSeqScan's
// "refuse toward serial" discipline requires. An MHJ is INNER by construction
// (collectMultiHashTables refuses any other join type, and the node has no Type
// field), and its residual Filters are per-probe-row WHERE conjuncts over the
// concatenated output row, so the predicate is nearly trivial — it exists so a
// malformed node fails toward serial rather than panicking.
func multiHashJoinIsPartialCapable(m *MultiHashJoin) bool {
	if m == nil {
		return false
	}
	if m.ProbeTable < 0 || m.ProbeTable >= len(m.Tables) {
		return false
	}
	return m.Tables[m.ProbeTable] != nil
}

// ParallelChildrenForTest exposes the post-pass's child walk to tests in other
// packages, which need to traverse a rebuilt plan to find the Gather.
func ParallelChildrenForTest(n Node) []Node { return parallelChildren(n) }

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

// computeParallelWorkers reproduces upstream's compute_parallel_worker()
// (postgres/src/backend/optimizer/path/allpaths.c): a SIZE RULE, not a cost
// comparison — which is exactly why it is reproducible here despite goopg
// having no absolute node costs to add parallel_setup_cost to.
func computeParallelWorkers(subtree Node, s ParallelSettings) int {
	scan := drivingSeqScan(subtree)
	if scan == nil || scan.Table == nil {
		return 0
	}

	forced := s.DebugParallelQuery == "on" || s.DebugParallelQuery == "regress"

	// The table's parallel_workers reloption wins outright, as in PG.
	if n := tableParallelWorkersReloption(scan.Table); n > 0 {
		return min(n, s.MaxWorkersPerGather)
	}

	blocks, known := parallelRelationBlocks(scan.Table, s)
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
			return NewGatherMerge(root.Pos(), root, workers, tgt.mergeKeys)
		case tgt.splitAgg:
			return splitAggregate(root.(*Aggregate), workers)
		}
		return NewGather(root.Pos(), root, workers)
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

	gather := NewGather(a.Pos(), &partial, workers)

	final := *a
	final.Mode = AggModeFinal
	final.Child = gather
	final.PartialSource = &partial
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
		// rebuildWithGather refuse any join drivingSeqScan has not explicitly
		// approved, since both bail on len(kids) != 1.
		return []Node{x.Left, x.Right}
	case *MultiHashJoin:
		// Chapter 12 §7 — the safety fix, and the one dangerous item in this
		// change. Without it subtreeHasUnsafeNode stops dead at an MHJ and
		// treats "no children" as "nothing unsafe below" (the opposite of
		// conservative for the SAFETY walk), so a temp / virtual / LockRows
		// relation under the probe side would go unseen the moment
		// drivingSeqScan learned about MHJ. Must land with that change, never
		// after. Returning all Tables does not break the placement walks:
		// findPartialSubtree takes the MHJ via drivingSeqScan before its
		// len(kids)!=1 check, and rebuildWithGather matches root==target
		// before walking children.
		return x.Tables
	case *NestedLoopIndexJoin:
		return []Node{x.Outer, x.Inner}
	}
	return nil
}
