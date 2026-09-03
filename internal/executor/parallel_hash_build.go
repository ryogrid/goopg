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

	preserveBuildSide bool
	antiBuildRows     int
	antiBuildHasNull  bool
	leftWidth         int
	rightWidth        int
}

// captureSharedBuild snapshots the build-phase results of a joinOp whose
// buildLazyHashTable has just completed.
func (o *joinOp) captureSharedBuild(probeIsLeft bool) *sharedHashBuild {
	return &sharedHashBuild{
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
	}
}

// applySharedBuild adopts a published table instead of building one.
//
// Every field buildLazyHashTable would have set must be set here too. Leaving
// one out does not fail loudly — it produces a join that runs and returns the
// wrong rows.
func (o *joinOp) applySharedBuild(sb *sharedHashBuild) {
	o.lazyHash = sb.hash
	o.lazyHashCTID = sb.hashCTID
	o.lazyIntHash = sb.intHash
	o.lazyHashIsInt = sb.hashIsInt
	o.preserveBuildSide = sb.preserveBuildSide
	o.antiBuildRows = sb.antiBuildRows
	o.antiBuildHasNull = sb.antiBuildHasNull
	o.lazyLW = sb.leftWidth
	o.lazyRW = sb.rightWidth
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
	for _, j := range joins {
		j.ctx = ctx
		// M0127-P3.4: decline the SHARE, not the SPILL.
		//
		// A spilling build cannot be published: captureSharedBuild freezes the
		// in-memory table alone, while the batch files and the per-batch probe
		// replay live on THIS operator, which no worker ever runs — a worker
		// handed that table would probe batch 0 and silently return the rows
		// of one partition. P3.2 avoided the problem by forcing `noBatch`,
		// which made the shared build the one hash build in the executor with
		// no work_mem bound at all. The honest form is the opposite: keep the
		// bound, give up the sharing, and let each worker build privately (and
		// batch privately) — 06 §6, which defers real parallel hash outright.
		//
		// The check runs twice on purpose. Before the build, the ESTIMATE is
		// consulted so the common case costs no wasted pass; after it, the
		// MEASUREMENT is, because goopg's estimates are absent often enough
		// that growth-on-overrun is the only bound worth relying on.
		if sharedBuildWouldSpill(ctx, j) {
			continue
		}
		probeIsLeft, err := j.buildLazyHashTable(ctx)
		if err != nil {
			return nil, err
		}
		if j.batches != nil && j.batches.nbatch > 1 {
			// The estimate said it fit and it did not. Throw the leader's
			// build away — files included — rather than publish a partition.
			j.releaseBatches()
			j.lazyHash, j.lazyIntHash, j.lazyHashCTID = nil, nil, nil
			continue
		}
		out[j.plan] = j.captureSharedBuild(probeIsLeft)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// sharedBuildWouldSpill asks the shared geometry — the same one presizeLazyHash
// and the batch state use — whether this build is projected to need more than
// one batch.
//
// It answers false for a join that cannot batch anyway (a composite key, the
// FOR-UPDATE ctid build): declining to share such a build would not bound
// anything, it would just replace one unbounded build with one per worker.
func sharedBuildWouldSpill(ctx *Context, j *joinOp) bool {
	j.initExecKeys()
	if !j.joinBatchEligible() {
		return false
	}
	buildNode, buildWidth := j.plan.Right, len(j.right.Schema())
	if !probeSideIsLeft(j.plan) {
		buildNode, buildWidth = j.plan.Left, len(j.left.Schema())
	}
	return j.buildGeometry(ctx, buildNode, buildWidth, !probeSideIsLeft(j.plan)).NBatch > 1
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
// Its producers each call BuildWorker(buildPlan) and get the WHOLE aggregate,
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

// parallelBuildEligible reports whether this hash join's build side can be
// parallelised. Four conditions must all hold (design §1.3):
//
//  1. The join type permits shared probe (INNER/SEMI/ANTI, or LEFT with
//     probe on the outer side).
//  2. The build fits in one batch (spilling builds can't be shared).
//  3. The build child is a parallel-scannable SeqScan (possibly under
//     Filter).
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
	// Multi-key joins force the string map but are otherwise fine.
	// Composite keys, however, have a different insertion path
	// (fileCompositeBuildRow) that the channel-source pattern doesn't
	// reach — declined for now.
	if o.multiKey() {
		return false
	}

	// Rule 2: must not be projected to spill.
	buildPlan := o.plan.Right
	buildWidth := o.lazyRW
	if buildLeft {
		buildPlan = o.plan.Left
		buildWidth = o.lazyLW
	}
	if o.joinBatchEligible() {
		if o.buildGeometry(ctx, buildPlan, buildWidth, buildLeft).NBatch > 1 {
			return false
		}
	}

	// Rule 3: build child must be a SeqScan (possibly under Filter).
	scan := extractSeqScanFromPlan(buildPlan)
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
	if buildLeft {
		buildPlan = o.plan.Left
		buildSchema = o.left.Schema()
		otherWidth = o.lazyRW
	}

	scan := extractSeqScanFromPlan(buildPlan)
	if scan == nil {
		return false, fmt.Errorf("parallel build: no SeqScan in build child")
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
			tree, err := buildUnderNilScope(func() (Operator, error) { return BuildWorker(buildPlan) })
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
				batch = append(batch, MaterializeForTransfer(slot.Row()))
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

	o.recordBuildTime(ctx, buildStart)
	return probeIsLeft, nil
}
