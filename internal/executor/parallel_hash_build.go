package executor

// parallel_hash_build.go — P8 + M0129-S4.1 (cooperative parallel hash build).
//
// PostgreSQL offers two parallel hash joins: a non-shared one where every
// worker builds its own complete copy of the table, and `Parallel Hash`, where
// workers cooperatively build ONE table in dynamic shared memory behind a
// barrier protocol with explicit phases. Both exist because shared memory is
// expensive to manage, so neither option is free.
//
// goopg needs neither. Workers are goroutines in one address space, so the
// table can be built once and shared by pointer. The correctness argument is
// the ordinary Go one: a map is safe for unlimited concurrent reads provided
// no writer runs concurrently, and the goroutine-start edge supplies the
// happens-before that publishes it. That replaces PG's whole DSA + barrier
// apparatus with a struct and a map lookup.
//
// P8 keeps the build serial in the leader. M0129-S4.1 (= P2.1a) adds a
// cooperative parallel build: N goroutines scan+filter the build table
// (producers), one goroutine owns the hash map and inserts (consumer). The
// design is a producer/consumer split over a buffered channel — single writer
// on the map, no lock, no concurrent map. See
// docs/design/parallel-query/13-cooperative-parallel-hash-build.md.

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/goopg/goopg/internal/utils/mmgr"
	"github.com/goopg/goopg/internal/optimizer"
)

// sharedHashBuild is one hash join's build-side result, frozen and shared by
// every worker running that plan node.
//
// The map is the obvious part. The scalars are the part that is easy to miss:
// they are per-INSTANCE fields on joinOp, not entries in the map, so sharing
// the table does not carry them along. antiBuildHasNull in particular decides
// NOT IN's three-valued-NULL result, and a worker that defaulted it to false
// would silently return wrong rows for NOT IN over a NULL-containing subquery.
type sharedHashBuild struct {
	hash     map[string][]Row
	hashCTID map[string][]joinRowCTID

	// int64 fast-path (INNER only): when the build side's keys were all
	// int64-representable, buildLazyHashTable/lazyHashFinalize sets
	// lazyHashIsInt and frees lazyHash, leaving the table in lazyIntHash. These
	// must ride along, or a worker probes an empty string map and drops every
	// match (the symptom: parallel INNER joins return 0 rows).
	intHash   map[int64][]Row
	hashIsInt bool

	// probeIsLeft records which side the build consumed, so a worker opens the
	// other one without re-deriving the rule.
	probeIsLeft bool

	// EX3-02 Cut 1 (stratum B): the builder's buildBytes arena, parented to
	// the statement context. Adopted by workers (applySharedBuild) so
	// chunk-backed rows stay valid after the prebuild throwaway tree is
	// gone; reclaimed at statement end (Cut 3 owns the explicit teardown).
	buildBytes *mmgr.Context

	preserveBuildSide bool
	antiBuildRows     int
	antiBuildHasNull  bool
	leftWidth         int
	rightWidth        int

	// E-09a (docs/design/executor-e09a-shared-spilling-build/DESIGN.md §4):
	// the batch descriptor of a build that batched. The maps above hold
	// BATCH 0 ONLY; every other batch is an inner file the leader wrote and
	// froze, and each participant reloads it privately (join_batch.go,
	// newParticipantBatchState). nil when the build had no batch state —
	// then, as before, the maps are the whole table.
	batches *sharedBatchDesc
}

// captureSharedBuild snapshots the build-phase results of a joinOp whose
// buildLazyHashTable has just completed, freezing its batch files for
// shared, read-only reloading. The joinOp's own batch state is detached in
// the process: the descriptor owns the files from here on.
func (o *joinOp) captureSharedBuild(probeIsLeft bool) (*sharedHashBuild, error) {
	sb := &sharedHashBuild{
		hash:              o.lazyHash,
		hashCTID:          o.lazyHashCTID,
		intHash:           o.lazyIntHash,
		hashIsInt:         o.lazyHashIsInt,
		probeIsLeft:       probeIsLeft,
		preserveBuildSide: o.preserveBuildSide,
		antiBuildRows:     o.antiBuildRows,
		antiBuildHasNull:  o.antiBuildHasNull,
		leftWidth:         o.lazyLW,
		rightWidth:        o.lazyRW,
		buildBytes:        o.buildBytes,
	}
	if o.batches != nil {
		d, err := o.batches.freezeForSharing()
		if err != nil {
			return nil, err
		}
		sb.batches = d
		o.batches = nil
	}
	return sb, nil
}

// release retires the files a spilling build published. The maps and the
// arena are garbage-collected / statement-reclaimed as before.
func (sb *sharedHashBuild) release(ctx *Context) {
	if sb != nil && sb.batches != nil {
		sb.batches.release(ctx)
	}
}

// releaseSharedHashBuilds retracts a Gather's publication from ctx and
// unlinks the batch files it carried. Called from Gather / GatherMerge Close
// AFTER the fan-out has joined — no participant may still be reading.
func releaseSharedHashBuilds(ctx *Context) {
	if ctx == nil {
		return
	}
	for _, sb := range ctx.SharedHashBuilds {
		sb.release(ctx)
	}
	ctx.SharedHashBuilds = nil
}

// applySharedBuild adopts a published table instead of building one.
//
// Every field buildLazyHashTable would have set must be set here too. Leaving
// one out does not fail loudly — it produces a join that runs and returns the
// wrong rows.
//
// ctx is THIS participant's context (a worker's, or the leader's own): the
// private batch state installed for a spilling build writes its outer files
// and its EXPLAIN counters through it.
func (o *joinOp) applySharedBuild(ctx *Context, sb *sharedHashBuild) {
	// A re-Open that skipped Close must not keep the previous run's private
	// batch state (its outer files, its curBatch).
	o.releaseBatches()
	o.lazyHash = sb.hash
	o.lazyHashCTID = sb.hashCTID
	o.lazyIntHash = sb.intHash
	o.lazyHashIsInt = sb.hashIsInt
	o.preserveBuildSide = sb.preserveBuildSide
	o.antiBuildRows = sb.antiBuildRows
	o.antiBuildHasNull = sb.antiBuildHasNull
	o.lazyLW = sb.leftWidth
	o.lazyRW = sb.rightWidth
	// EX3-02 Cut 1: adopt the shared stratum-B arena. Marked shared so
	// Close only dereferences it — the builder (never Closed on this path)
	// and the statement-end Release own its lifetime.
	o.buildBytes = sb.buildBytes
	o.buildBytesShared = true
	// E-09a §4 part 3: a spilling build gets a private batch state whose
	// inner files are the shared, frozen ones. Without it the probe loop
	// would never route a probe row (routeProbeRow is guarded on
	// `bs != nil`) and the join would silently return batch 0's partition.
	if sb.batches != nil {
		o.batches = newParticipantBatchState(ctx, o.plan, sb.batches)
	}
}

// lookupSharedHashBuild returns the published table for a plan node, or nil
// when this execution is not under a Gather that pre-built one.
func lookupSharedHashBuild(ctx *Context, p *optimizer.Join) *sharedHashBuild {
	if ctx == nil || ctx.SharedHashBuilds == nil || p == nil {
		return nil
	}
	return ctx.SharedHashBuilds[p]
}

// probeSideIsLeft reports which side of a hash join is probed.
//
// This rule is duplicated in three places by necessity — the build loop, the
// parallel-scan attach walk, and the planner's partial-subtree search — and
// they must agree, because a disagreement puts the parallel scan on the BUILD
// side, where each worker would build a partition of the table and the join
// would silently lose rows. Hence one exported-ish helper rather than three
// copies of `if Semi/Anti { false }`.
func probeSideIsLeft(p *optimizer.Join) bool {
	buildLeft := p.BuildLeft
	if p.Type == optimizer.JoinTypeSemi || p.Type == optimizer.JoinTypeAnti {
		buildLeft = false
	}
	// The build consumed the left side ⇒ the probe is the right side.
	return !buildLeft
}

// prebuildSharedHashJoins runs the build phase of every shareable hash join in
// a partial subtree, in the LEADER, before any worker starts.
//
// It builds one throwaway operator tree for the sole purpose of running those
// build phases. That is cheaper than it sounds: the build child is opened,
// drained and closed inside the build phase, and nothing else in the tree is
// ever opened. What the tree costs is construction; what it buys is that the
// build runs through the SAME code path a serial execution uses, rather than a
// reimplementation that could drift from it.
//
// Returns nil when the subtree has no shareable hash join, which is the common
// case and costs nothing — the check is on the plan, not on a built tree.
func prebuildSharedHashJoins(ctx *Context, plan optimizer.Node, buildChild func() (Operator, error)) (map[*optimizer.Join]*sharedHashBuild, error) {
	// Decide from the PLAN, before building anything. An earlier cut built the
	// tree unconditionally and then looked for joins in it, which called the
	// Gather's child-builder one extra time — harmless for the production
	// builder (a pure Build(p.Child)) but not for any builder that counts its
	// invocations, as several tests legitimately do.
	if !optimizer.HasShareableHashJoin(plan) {
		return nil, nil
	}
	// EX0-03b: prebuild throwaway tree — scope explicitly NIL
	// (uninstrumented, exactly today's behavior; covers both Gather and
	// GatherMerge prebuild call sites). Its drains would double-count the
	// same plan keys into a worker/leader table.
	tree, err := buildUnderNilScope(buildChild)
	if err != nil {
		return nil, err
	}
	var joins []*joinOp
	collectShareableJoins(tree, &joins)
	if len(joins) == 0 {
		return nil, nil
	}

	out := make(map[*optimizer.Join]*sharedHashBuild, len(joins))
	fail := func(err error) (map[*optimizer.Join]*sharedHashBuild, error) {
		for _, sb := range out {
			sb.release(ctx)
		}
		return nil, err
	}
	for _, j := range joins {
		j.ctx = ctx
		// E-09a: a spilling build is published, batch files and all.
		//
		// M0127-P3.4 used to decline the SHARE here whenever the geometry (or,
		// after the build, the measurement) said nbatch > 1, because the
		// batch files and the per-batch probe replay lived on THIS operator,
		// which no worker runs — a worker handed the batch-0 maps alone would
		// have returned one partition's rows. On TPC-H Q9 that meant all
		// five participants built the 1.5 M-row `orders` table privately
		// (DESIGN.md §1). captureSharedBuild now freezes the batch state into
		// a descriptor and every participant derives a private state from it
		// (join_batch.go), so the work_mem bound and the sharing coexist.
		// Growth is still free to fire during THIS build; it is frozen the
		// moment the descriptor is cut.
		probeIsLeft, err := j.buildLazyHashTable(ctx)
		if err != nil {
			return fail(err)
		}
		sb, err := j.captureSharedBuild(probeIsLeft)
		if err != nil {
			j.releaseBatches()
			return fail(err)
		}
		out[j.plan] = sb
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// collectShareableJoins finds the hash joins in a tree whose build side can be
// shared.
//
// The walk descends the PROBE side only. A hash join nested on another join's
// build side is built as part of that build, serially, and must not be
// pre-built separately — doing so would run its build twice.
func collectShareableJoins(op Operator, out *[]*joinOp) {
	switch x := op.(type) {
	case *joinOp:
		// R95 (plan-parity-fix-take2): descend the outer (left) of an
		// approved nested loop — ordinary (R94) or lateral probe — so
		// hashes below it are leader-prebuilt exactly as standalone
		// hashes are. Without this, `HasShareableHashJoin` (which does
		// descend) promises a prebuild the collection never performs,
		// and every worker builds a PARTIAL hash table from its scan
		// partition — silently dropped matches. The probe/lateral inner
		// itself is never descended: it is re-opened per outer row, not
		// prebuilt.
		if x.plan != nil && x.plan.Algo == optimizer.JoinAlgoNestedLoop {
			if ordinaryInnerNestedLoopPartial(x.plan) || lateralProbeJoinPartial(x.plan, x.right) {
				collectShareableJoins(x.left, out)
			}
			return
		}
		if x.plan == nil || x.plan.Algo != optimizer.JoinAlgoHash || x.plan.Lateral {
			return
		}
		*out = append(*out, x)
		if probeSideIsLeft(x.plan) {
			collectShareableJoins(x.left, out)
		} else {
			collectShareableJoins(x.right, out)
		}
	case *filterOp:
		collectShareableJoins(x.child, out)
	case *projectOp:
		collectShareableJoins(x.child, out)
	case *sortOp:
		collectShareableJoins(x.child, out)
	case *aggregateOp:
		collectShareableJoins(x.child, out)
	case *instrumentedOp:
		collectShareableJoins(x.inner, out)
	}
}

// ── M0129-S4.1: cooperative parallel hash build ──────────────────────────
//
// Design: docs/design/parallel-query/13-cooperative-parallel-hash-build.md.

// channelSource is a synthetic Operator that feeds rows received from a
// channel into the build loop. It exists so the parallel build can reuse the
// EXACT same buildLoopRight/buildLoopLeft code the serial build uses — the
// only difference is the row source (channel vs. child operator tree).
type channelSource struct {
	ch     <-chan []Row
	schema optimizer.Schema
	batch  []Row
	idx    int
}

func (s *channelSource) Open(_ *Context) error { return nil }
func (s *channelSource) Close() error {
	// Drain remaining batches so producers don't block on send.
	for range s.ch {
	}
	return nil
}
func (s *channelSource) Schema() optimizer.Schema { return s.schema }

func (s *channelSource) Next() (TupleSlot, error) {
	for s.idx >= len(s.batch) {
		b, ok := <-s.ch
		if !ok {
			return nil, EOF
		}
		s.batch = b
		s.idx = 0
	}
	row := s.batch[s.idx]
	s.idx++
	return &MaterializedSlot{schema: s.schema, row: row}, nil
}

// extractSeqScanFromPlan finds the scan that DRIVES a build subtree — the leaf
// a parallel block allocator can be attached to — descending the pass-through
// nodes Filter and Project to any depth. It returns nil for every other shape.
//
// It is deliberately NARROWER than attachParallelScan (parallel_scan.go), and
// the asymmetry is a SAFETY PROPERTY, not an oversight. Do not "fix" it by
// making the two match.
//
// attachParallelScan also descends aggregateOp, sortOp and a joinOp's probe
// side. Those are safe THERE because the node it serves is a Gather, and the
// planner has split the partial subtree accordingly: a Partial aggregate under
// the Gather with a Finalize above it (optimizer/parallel.go, splitAgg), and
// Gather Merge for the sorted case. The cooperative hash build has neither.
// Its producers each rebuild buildPlan and get the WHOLE aggregate,
// then attachParallelScan partitions the scan beneath it — so N producers would
// each aggregate their own partition and the consumer would union the partial
// results into the hash table with no Finalize. For a HAVING sum(...) predicate
// — TPC-H Q18's semi-join build side is exactly that — the result is silently
// WRONG ROWS.
//
// Refusing to descend Aggregate (and Sort, and Join, which can reach an
// Aggregate) is what prevents that today. Widening this walker is only safe for
// a node kind that is 1:1 and order-independent, which Filter and Project are.
//
// Q18 also measures the cost side of descending joins: every producer redoes
// each nested build, 35.7s -> 42.9-44.1s over two alternating rounds. But cost
// is the SECOND reason to decline; correctness is the first. See
// docs/design/not_ralph/parallel-hash-build-coverage/DESIGN.md §4.
func extractSeqScanFromPlan(node optimizer.Node) *optimizer.SeqScan {
	for {
		switch n := node.(type) {
		case *optimizer.SeqScan:
			return n
		case *optimizer.Filter:
			node = n.Child
		case *optimizer.Project:
			node = n.Child
		default:
			return nil
		}
	}
}

// coopJoinBuild is E-18 slice 2's knob: OFF by default.
//
// It widens the cooperative build's Rule 3 so a build side that is a JOIN TREE
// can be built cooperatively, which is the shape TPC-H Q9 needs and the one
// `extractSeqScanFromPlan` refuses. It is a knob, not a default, because the
// two objections recorded on that walker are both real and only one of them is
// answered here:
//
//   - CORRECTNESS (answered): the widened walk descends a HASH join's PROBE
//     side only, and refuses every other node kind including Aggregate and
//     Sort. That is the property `extractSeqScanFromPlan`'s comment protects —
//     N producers each aggregating their own partition, with no Finalize
//     above, is a silent wrong answer for a `HAVING sum(...)` build side.
//   - COST (answered here, but not yet across a corpus): each producer used to
//     redo every nested build (Q18 35.7 -> 42.9-44.1 s). This path prebuilds
//     the nested shareable joins ONCE in the leader and publishes them to the
//     producers by pointer, reusing the same sharedHashBuild machinery a
//     Gather uses, so a producer does the driving scan and the probes only.
//
// Default ON (GOOPG_COOP_JOIN_BUILD=off to disable). Measured on TPC-H SF=1
// in PARALLEL mode (4 workers), fresh capped server per arm per rep, 5
// alternating reps, every result set md5-identical between the arms:
// Q20 1.91 -> 0.65 s (-66%, ranges disjoint), Q21 14.63 -> 13.75 s (-6.0%),
// Q7 4.07 -> 3.79 s, Q9 10.75 -> 10.50 s; Q2/Q5/Q8/Q17/Q18 unchanged. No
// query regressed in any rep.
var coopJoinBuildOn = os.Getenv("GOOPG_COOP_JOIN_BUILD") != "off"

// coopDrivingScan finds the scan a cooperative build's producers can partition.
//
// It is extractSeqScanFromPlan widened by exactly one node kind: a HASH join's
// PROBE side. Everything else is refused, and the refusal is the safety
// property — see extractSeqScanFromPlan's comment for why Aggregate and Sort
// must never be descended, and attachParallelScan's joinOp arm for why the
// side must be the probe side and the algorithm must be hash.
//
// It must agree with attachParallelScan, which does the same walk over the
// BUILT tree: if this walker names a leaf attachParallelScan would not reach,
// the producers all scan the whole relation and the build gets N copies of
// every row. Both call probeSideIsLeft, which is the single shared rule.
func coopDrivingScan(node optimizer.Node) *optimizer.SeqScan {
	for {
		switch n := node.(type) {
		case *optimizer.SeqScan:
			return n
		case *optimizer.Filter:
			node = n.Child
		case *optimizer.Project:
			node = n.Child
		case *optimizer.Join:
			if !coopJoinBuildOn || n.Algo != optimizer.JoinAlgoHash || n.Lateral {
				return nil
			}
			if probeSideIsLeft(n) {
				node = n.Left
			} else {
				node = n.Right
			}
		default:
			return nil
		}
	}
}

// parallelBuildEligible reports whether this hash join's build side can be
// parallelised. The design (§1.3) filed four rules; two of them have since
// been retired against the source, and the numbering is kept so the retirement
// notes below stay findable:
//
//  1. The join type permits shared probe (INNER/SEMI/ANTI, or LEFT with
//     probe on the outer side).
//  2. RETIRED (E-18 slice 1) — "the build fits in one batch". Spilling is
//     consumer-side; see the note below.
//  2b. RETIRED (E-18 slice 3) — "single-column key only". The composite lane
//     is consumer-side too; see the note below.
//  3. The build child exposes a partitionable driving scan: a SeqScan under
//     Filter/Project, and (slice 2) under a hash join's probe side.
//  4. The relation has enough blocks (≥ MinParallelTableScanBlocks).
//
// The function never mutates join state.
func (o *joinOp) parallelBuildEligible(ctx *Context, buildLeft bool) bool {
	// Rule 1: must be shareable (P8 eligibility).
	if o.plan.Type == optimizer.JoinTypeFull || o.plan.Type == optimizer.JoinTypeRight {
		return false
	}
	if o.plan.Type == optimizer.JoinTypeLeft && buildLeft {
		// LEFT join with build on the left side: the probe (right) would
		// carry the outer — wrong shape, declined.
		return false
	}
	// FOR UPDATE on the build side uses a CTID-preserving build that
	// cannot be parallelised (it captures per-tuple heap TIDs via a
	// dedicated scan leaf).
	if o.preserveCTIDRel != nil {
		return false
	}
	// Composite (multi-column) keys: RETIRED as a decline in E-18 slice 3.
	//
	// The rule's stated ground was that "composite keys have a different
	// insertion path (fileCompositeBuildRow) that the channel-source pattern
	// doesn't reach". Reading the source refutes it: fileCompositeBuildRow is
	// called from inside buildLoopRight / buildLoopLeft
	// (operators_join_agg.go), the very loops the cooperative build runs in
	// its single consumer goroutine, from a channelSource instead of from the
	// child operator tree. The composite lane is therefore entirely
	// CONSUMER-side, exactly as batching is (slice 1's Rule 2), and the
	// producer/consumer split cannot observe it: producers only scan, filter
	// and hand materialised rows over a channel; key encoding
	// (encodeBuildCompositeKey, o.execKeyBuf) and filing happen after the
	// channel, on one goroutine.
	//
	// Why it matters: TPC-H Q9 spends 12.86 s of its 13.6 s in ONE build —
	// the (ps_suppkey, ps_partkey) composite join, whose driving scan is the
	// 6 M-row lineitem seq scan. That build was the item's whole remaining
	// gap, and this rule was the only thing declining it.

	// Rule 2 (E-18 slice 1): RETIRED. It used to decline a build whose
	// geometry predicted more than one batch, on the stated ground that
	// "spilling builds can't be shared". Two things make that obsolete:
	//
	//  1. E-09a/E-09b made a spilling build shareable — captureSharedBuild
	//     freezes the batch descriptor and every participant reloads the
	//     inner files privately (sharedBatchDesc, join_batch.go). Sharing is
	//     no longer conditional on fitting in one batch.
	//  2. Spilling is entirely CONSUMER-side here. The cooperative build is a
	//     producer/consumer split: the producers only scan+filter and hand
	//     rows over a channel; the single consumer goroutine (the leader)
	//     evaluates the key and calls insertBuildRow, which is what routes a
	//     row to a batch file. buildLoopRight/buildLoopLeft make ONE pass
	//     over the row source and never rescan the child, so a channelSource
	//     serves a batching build exactly as a child operator tree does.
	//
	// This is the point where goopg's producer/consumer shape beats PG's:
	// PG needs SHARED batch files for `Parallel Hash` because every backend
	// inserts, so writes to a batch come from N backends at once. goopg has
	// exactly one writer, so the batch files stay per-operator and
	// single-writer, and the parallel BUILD SCAN is obtained without any of
	// the shared-file machinery E-18's design doc scoped as "Phase 2".
	//
	// Witnesses at bench work_mem (64MB), TPC-H SF=1: Q5, Q7 and Q21 each
	// declined an `orders` build at NBatch=4 under the old rule.
	buildPlan := o.plan.Right
	if buildLeft {
		buildPlan = o.plan.Left
	}

	// Rule 3: the build child must expose a partitionable driving scan —
	// a SeqScan under Filter/Project, and (E-18 slice 2, knob) under a hash
	// join's probe side.
	scan := coopDrivingScan(buildPlan)
	if scan == nil {
		return false
	}

	// Rule 4: enough blocks.
	nBlocks, err := ctx.Pool.NBlocks(ctx.Catalog.RelFileNode(scan.Table))
	if err != nil || int64(nBlocks) < ctx.MinParallelTableScanBlocks {
		return false
	}

	return true
}

// coopSpillingBuilds counts cooperative parallel hash builds that ACTUALLY
// spilled (ended with a live batch descriptor). E-18 slice 1 retired the
// eligibility rule that declined those, and the design's own discipline is
// that a newly-reachable path which never fires is an untested one — so the
// path is counted, and TestCoopParallelHashBuildSpills asserts the count moves.
var coopSpillingBuilds atomic.Int64

// CoopSpillingBuildCount reports the process-wide number of cooperative
// parallel hash builds that spilled to batch files. Test/diagnostic use.
func CoopSpillingBuildCount() int64 { return coopSpillingBuilds.Load() }

// coopCompositeBuilds counts cooperative parallel hash builds on a COMPOSITE
// (multi-column) key. E-18 slice 3 retired the eligibility rule that declined
// those; same discipline as coopSpillingBuilds — a newly-reachable path that
// never fires is an untested one, so the path is counted and
// TestCoopParallelHashBuildComposite asserts the count moves.
var coopCompositeBuilds atomic.Int64

// CoopCompositeBuildCount reports the process-wide number of cooperative
// parallel hash builds that used the composite-key lane. Test/diagnostic use.
func CoopCompositeBuildCount() int64 { return coopCompositeBuilds.Load() }

// parallelBuildLazyHashTable runs a cooperative parallel hash build.
//
// N goroutines scan+filter the build table (producers), each claiming blocks
// atomically from a shared ParallelScanState. Rows are sent in batches through
// a buffered channel to the calling goroutine (consumer), which evaluates hash
// keys and inserts into the hash map — exactly as the serial build loops do,
// but from a channelSource instead of from the child operator tree.
//
// The consumer runs the SAME buildLoopRight or buildLoopLeft as the serial
// path. A synthetic channelSource operator replaces o.right or o.left, so the
// existing build loop needs zero changes.
func (o *joinOp) parallelBuildLazyHashTable(ctx *Context, buildLeft bool) (bool, error) {
	buildPlan := o.plan.Right
	buildSchema := o.right.Schema()
	otherWidth := o.lazyLW
	// EX1-01/EX1-02 deform bound. The producers below REBUILD this subtree
	// from the plan, so they must build it at the bound the serial builder
	// gave the same side (joinOp.deformLeftBound / deformRightBound). Using
	// the root bound instead — which is what BuildWorker does — restarts the
	// walk with no recorded consumer, and a build side such as
	// `Filter(dk > 3) -> SeqScan(dk, dname)` then narrows to [0,1): the hash
	// table is loaded with rows whose `dname` was never deformed, and the
	// join returns the RIGHT NUMBER OF ROWS with a NULL payload. No error,
	// no row-count change; see TestCoopParallelHashBuildValuesAcrossWorkMem.
	buildBound := o.deformRightBound
	if buildLeft {
		buildPlan = o.plan.Left
		buildSchema = o.left.Schema()
		otherWidth = o.lazyRW
		buildBound = o.deformLeftBound
	}

	scan := coopDrivingScan(buildPlan)
	if scan == nil {
		return false, fmt.Errorf("parallel build: no driving scan in build child")
	}

	// Determine worker count. At least 2 producers, capped by
	// MaxParallelWorkers. One goroutine is the consumer (the leader); the
	// rest are producers.
	maxProducers := ctx.MaxParallelWorkers
	if maxProducers < 2 {
		maxProducers = 2
	}

	group := NewParallelGroup(ctx.Ctx)
	pscan := newParallelScanState(0)
	ch := make(chan []Row, gatherChanDepth*(maxProducers+1))

	// E-18 slice 2: when the build side is a JOIN TREE, every producer would
	// otherwise redo each nested build — the cost objection recorded on
	// extractSeqScanFromPlan (Q18 35.7 -> 42.9-44.1 s). Prebuild those nested
	// hash joins ONCE here, in the leader, and hand the producers the tables
	// by pointer, exactly as a Gather does for its own subtree. A producer
	// then does the driving scan and the probes and nothing else.
	//
	// The publication is put on the WORKER contexts, never on ctx: ctx's own
	// SharedHashBuilds map may already be published to a surrounding Gather's
	// participants, and mutating it here would be a write to a map those
	// goroutines are reading.
	nested, err := prebuildSharedHashJoins(ctx, buildPlan, func() (Operator, error) {
		return buildNode(buildPlan, buildBound)
	})
	if err != nil {
		return false, err
	}
	defer func() {
		for _, sb := range nested {
			sb.release(ctx)
		}
	}()

	// Pre-allocate arenas and worker contexts. mctx.Acquire is NOT
	// goroutine-safe (appends to parent.children without synchronisation).
	var workerCtxs []*Context
	var arenas []*mmgr.Context
	for i := 0; i < maxProducers; i++ {
		arena := mmgr.Acquire(ctx.Mctx, mmgr.KindStmt)
		arenas = append(arenas, arena)
		wctx := NewWorkerContext(ctx, arena, group.Context())
		// EX0-03c: stamp the fan-out slot so MergeWorkerContext can tag
		// this producer's sort entries with an explicit index.
		wctx.workerSlot = i
		if len(nested) > 0 {
			// Merge, do not overwrite: a coop build nested under a Gather
			// must keep seeing the Gather's publication too. The merged map
			// is fresh per worker and never written after this point.
			merged := make(map[*optimizer.Join]*sharedHashBuild, len(ctx.SharedHashBuilds)+len(nested))
			for k, v := range ctx.SharedHashBuilds {
				merged[k] = v
			}
			for k, v := range nested {
				merged[k] = v
			}
			wctx.SharedHashBuilds = merged
		}
		workerCtxs = append(workerCtxs, wctx)
	}

	// Launch producers.
	for i := 0; i < maxProducers; i++ {
		wctx := workerCtxs[i]
		group.Go(func(workerCtx context.Context) error {
			// EX0-03b: coop throwaway tree — scope explicitly NIL
			// (uninstrumented, exactly today's behavior). Producer
			// goroutines build concurrently, so the mutex-serialized
			// NIL handoff also keeps a concurrent Gather site's fresh
			// table out of this tree.
			tree, err := buildUnderNilScope(func() (Operator, error) {
				return buildNode(buildPlan, buildBound)
			})
			if err != nil {
				return err
			}
			// attachParallelScan wires the shared block allocator into
			// the driving seqScanOp so each producer claims a disjoint
			// subset of blocks.
			if !attachParallelScan(tree, pscan) {
				return fmt.Errorf("parallel build: no scan in built tree")
			}
			if err := tree.Open(wctx); err != nil {
				return err
			}
			defer tree.Close()

			var batch []Row
			for {
				slot, err := tree.Next()
				if err == EOF {
					break
				}
				if err != nil {
					return err
				}
				batch = append(batch, transferRowForQueue(slot))
				if len(batch) >= gatherBatchRows {
					select {
					case ch <- batch:
					case <-workerCtx.Done():
						return workerCtx.Err()
					}
					batch = nil
				}
			}
			if len(batch) > 0 {
				select {
				case ch <- batch:
				case <-workerCtx.Done():
					return workerCtx.Err()
				}
			}
			return nil
		})
	}

	// Close channel after all producers exit so the consumer's build loop
	// receives EOF through the channelSource.
	go func() {
		group.Wait()
		close(ch)
	}()

	// Replace the build child with a synthetic operator that reads from the
	// channel. The old operator was never opened — it is harmless to drop.
	source := &channelSource{ch: ch, schema: buildSchema}

	// Run the SAME build loop as the serial path, but from the channel.
	buildStart := time.Now()
	var loopErr error
	var probeIsLeft bool
	if buildLeft {
		o.left = source
		if err := o.left.Open(ctx); err != nil {
			loopErr = err
		} else {
			o.presizeLazyHash(ctx, o.plan.Left, o.lazyLW, true)
			loopErr = o.buildLoopLeft(ctx, otherWidth)
			_ = o.left.Close()
		}
		probeIsLeft = false
	} else {
		o.right = source
		if err := o.right.Open(ctx); err != nil {
			loopErr = err
		} else {
			o.presizeLazyHash(ctx, o.plan.Right, o.lazyRW, false)
			loopErr = o.buildLoopRight(ctx, otherWidth)
			_ = o.right.Close()
		}
		probeIsLeft = true
	}

	// Cleanup: same discipline as gatherOp.Close — cancel before draining
	// so no producer is stuck on send, then drain, then join.
	//
	// In the normal path (loopErr == nil) the channel is already closed by
	// the closer goroutine, so the drain is instant. In the error path the
	// channel may still be open; Cancel unblocks producers, they exit, the
	// closer closes the channel, and the drain completes.
	group.Cancel()
	for range ch {
	}
	group.Wait()

	// Merge per-worker notices/warnings and release arenas.
	for i := range maxProducers {
		MergeWorkerContext(ctx, workerCtxs[i])
		arenas[i].Release()
	}

	if loopErr != nil {
		o.releaseBatches()
		return false, loopErr
	}

	if o.batches != nil {
		coopSpillingBuilds.Add(1)
	}
	if o.multiKey() {
		coopCompositeBuilds.Add(1)
	}
	o.recordBuildTime(ctx, buildStart)
	return probeIsLeft, nil
}
