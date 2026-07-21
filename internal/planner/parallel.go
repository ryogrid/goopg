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
	target, ok := findPartialSubtree(root)
	if !ok {
		return root
	}

	workers := computeParallelWorkers(target, s)
	if workers <= 0 {
		return root
	}

	return rebuildWithGather(root, target, workers)
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
func findPartialSubtree(root Node) (Node, bool) {
	cur := root
	for {
		if terminatesPartial(cur) {
			kids := parallelChildren(cur)
			if len(kids) != 1 {
				return nil, false
			}
			cur = kids[0]
			continue
		}
		// cur is partial-capable if it bottoms out in an eligible seq scan.
		if drivingSeqScan(cur) != nil {
			return cur, true
		}
		kids := parallelChildren(cur)
		if len(kids) != 1 {
			return nil, false
		}
		cur = kids[0]
	}
}

// terminatesPartial reports whether a Gather must sit at or below this node.
func terminatesPartial(n Node) bool {
	switch n.(type) {
	case *Limit, *Distinct, *DistinctOn, *WindowAgg, *SetOp,
		*RecursiveUnion, *WorkTableScan, *Memoize, *NestedLoopIndexJoin,
		*Aggregate, *Sort, *Gather:
		// Aggregate and Sort terminate partial-ness in P6 because the
		// partial/finalize split (P5's combine rules) and Gather Merge are
		// not wired into the planner yet — the machinery exists, the
		// placement does not. Lifting these is the next increment, not a
		// gap in the refusal set.
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
	}
	return nil
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
		// No statistics → no Gather. This is the OPPOSITE default from the
		// semi/anti NLI gate, which optimistically accepts without stats, and
		// the asymmetry is deliberate: accepting without evidence risks
		// spawning workers for a tiny relation plus N× the per-operator
		// memory, while declining merely keeps today's behaviour. goopg's
		// ANALYZE statistics are in-memory and lost on restart, so "no stats"
		// is a COMMON production state, not an edge case.
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
func parallelRelationBlocks(t *catalog.Table, s ParallelSettings) (int64, bool) {
	if s.BlocksForTable != nil {
		if b, ok := s.BlocksForTable(t); ok {
			return b, true
		}
	}
	// Fall back to the row estimate. This IS an approximation — it ignores
	// row width and page fill — and choosing it changes which relations cross
	// the threshold, so it is recorded rather than silently assumed.
	if t.Stats != nil && t.Stats.RowCount > 0 {
		const assumedRowsPerBlock = 60
		return t.Stats.RowCount / assumedRowsPerBlock, true
	}
	return 0, false
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
func rebuildWithGather(root, target Node, workers int) Node {
	if root == target {
		return NewGather(root.Pos(), root, workers)
	}
	kids := parallelChildren(root)
	if len(kids) != 1 {
		return root
	}
	rebuilt := rebuildWithGather(kids[0], target, workers)
	if rebuilt == kids[0] {
		return root
	}
	return replaceSingleChild(root, rebuilt)
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
		return []Node{x.Left, x.Right}
	case *NestedLoopIndexJoin:
		return []Node{x.Outer, x.Inner}
	}
	return nil
}
