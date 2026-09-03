package executor

// operators_gather.go — P4 of docs/design/parallel-query/, chapter 05.
//
// gatherOp fans a partial plan out across N goroutines and interleaves their
// output. It is implemented as a legacy Operator on purpose: buildRec's default
// arm wraps unmigrated nodes in an OpAdapter, so the live BuildFastIterator
// path reaches this with no slab changes at all, and each worker builds its own
// operator tree from the shared (read-only) plan.
//
// The three things that are easy to get wrong here, all in Close:
//
//   - drain BEFORE joining, or a worker blocked on a channel send never
//     observes cancellation and Close deadlocks;
//   - join on EVERY path — early LIMIT, error, cancellation — because worker
//     lifetime is strictly nested inside the statement (the statement arena is
//     released by a defer in the dispatcher and cascades to worker arenas);
//   - cancel before waiting, since Wait deliberately does not cancel.

import (
	"context"
	"errors"

	"github.com/goopg/goopg/internal/utils/mmgr"
	"github.com/goopg/goopg/internal/optimizer"
)

// gatherBatchRows is how many rows a worker accumulates before sending.
//
// A send per row would dominate a cheap scan; too large a batch adds latency to
// LIMIT-terminated queries. This is a tuning constant, not a semantic one — the
// value is a starting point to be measured, not a conclusion.
const gatherBatchRows = 256

// gatherChanDepth is the channel capacity in batches per worker. This IS the
// flow control: when the leader falls behind, sends block and workers stop
// producing. PG needs an explicit credit scheme because shm_mq is a fixed
// pre-allocated 64 KiB per worker; a Go channel of slices needs none.
const gatherChanDepth = 2

// rowBatch is one worker's unit of transfer. Rows in it are fully materialised
// — see the ownership contract in parallel_runtime.go.
type rowBatch struct {
	rows   []Row
	worker int
}

// gatherOp implements planner.Gather.
type gatherOp struct {
	plan   *optimizer.Gather
	ctx    *Context
	schema optimizer.Schema

	// buildChild constructs a fresh operator tree over the partial plan for
	// one worker. Injected so tests can drive the operator without a planner.
	buildChild func() (Operator, error)

	group   *ParallelGroup
	ch      chan rowBatch
	workers []*Context
	arenas  []*mmgr.Context

	// cur is the batch being drained into Next()'s return values.
	cur    []Row
	curIdx int
	slot   MaterializedSlot

	closed   bool
	drainErr error
	launched int

	// closerStarted records that the goroutine which closes o.ch is running.
	// Open guarantees it before returning on ANY path — see startChannelCloser.
	closerStarted bool

	// leaderRuns / leaderChild implement parallel_leader_participation: the
	// leader executes a share of the partial plan itself before falling back
	// to draining worker output.
	leaderRuns  bool
	leaderChild Operator

	// selfCancelled records that WE cancelled the group (early Close), so
	// Close can tell a self-inflicted 57014 apart from a genuine failure.
	selfCancelled bool

	// pscan is the shared block allocator every child tree's driving scan is
	// wired to. Without it each tree would scan the WHOLE relation and the
	// Gather would return N copies of every row — which is exactly what
	// happened the first time these pieces were connected, and what the
	// serial-vs-parallel identity check caught.
	pscan *parallelScanState

	// ownsSharedBuilds records that this Gather published hash tables on the
	// session context (P8) and must retract them at Close.
	ownsSharedBuilds bool

	// pbm is the shared page allocator for a parallel bitmap heap scan (S5.6).
	// The leader builds the bitmap once before fan-out and publishes it here;
	// workers claim pages via attachParallelBitmapScan.
	pbm *parallelBitmapState

	// pidx is the shared leaf-block claim set for a parallel index-only scan
	// (M0134-0189). Unlike pbm there is nothing to pre-build: the index is
	// already there, so every tree — leader and workers alike — walks it and
	// keeps only the leaf blocks it claims first.
	pidx *parallelIndexScanState
}

func newGatherOp(p *optimizer.Gather, buildChild func() (Operator, error)) *gatherOp {
	return &gatherOp{plan: p, schema: p.Output(), buildChild: buildChild}
}

func (o *gatherOp) Schema() optimizer.Schema { return o.schema }

// WorkersLaunched reports how many workers actually started, which EXPLAIN
// ANALYZE renders as PG's `Workers Launched:`. It can be lower than
// WorkersPlanned once the cluster-wide cap is honoured (P6).
func (o *gatherOp) WorkersLaunched() int { return o.launched }

// prebuildBitmapScan builds the TIDBitmap once before fan-out so workers
// share the result rather than each running their own index scan. (S5.6)
//
// This mirrors the pattern of prebuildHashJoins: the leader builds the bitmap
// eagerly, publishes the sorted block list in a shared atomic allocator, and
// workers claim disjoint pages from it.
func (o *gatherOp) prebuildBitmapScan(ctx *Context) error {
	// Decide from the PLAN, before building anything.
	if !optimizer.HasBitmapScan(o.plan.Child) {
		return nil
	}
	tree, err := o.buildChild()
	if err != nil {
		return err
	}
	var bmOps []*bitmapHeapScanOp
	collectBitmapScans(tree, &bmOps)
	if len(bmOps) == 0 {
		return nil
	}
	// A partial subtree should have exactly one driving scan. If multiple
	// bitmap scans appear (unexpected), fall back rather than guessing which
	// one to share.
	if len(bmOps) > 1 {
		return nil
	}
	bm := bmOps[0]
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
	o.pbm = newParallelBitmapState()
	o.pbm.init(tbm)
	return nil
}

// collectBitmapScans walks an operator tree and collects all bitmapHeapScanOp
// nodes into dst.
// Follows the same explicit-type-switch pattern as collectShareableJoins.
func collectBitmapScans(op Operator, dst *[]*bitmapHeapScanOp) {
	switch x := op.(type) {
	case *bitmapHeapScanOp:
		*dst = append(*dst, x)
	case *filterOp:
		collectBitmapScans(x.child, dst)
	case *projectOp:
		collectBitmapScans(x.child, dst)
	case *sortOp:
		collectBitmapScans(x.child, dst)
	case *aggregateOp:
		collectBitmapScans(x.child, dst)
	case *instrumentedOp:
		collectBitmapScans(x.inner, dst)
	case *joinOp:
		if probeSideIsLeft(x.plan) {
			collectBitmapScans(x.left, dst)
		} else {
			collectBitmapScans(x.right, dst)
		}
	}
}

// prebuildHashJoins publishes shared hash tables on ctx for the duration of
// this Gather. The leader's own child tree reads them from ctx directly, which
// is why they are set on the session context rather than only on the worker
// copies. Nesting is not a concern: a Gather never appears inside another
// Gather's partial subtree (terminatesPartial refuses it).
func (o *gatherOp) prebuildHashJoins(ctx *Context) error {
	prebuilt, err := prebuildSharedHashJoins(ctx, o.plan.Child, o.buildChild)
	if err != nil {
		return err
	}
	if prebuilt == nil {
		return nil
	}
	ctx.SharedHashBuilds = prebuilt
	o.ownsSharedBuilds = true
	return nil
}

func (o *gatherOp) Open(ctx *Context) error {
	o.ctx = ctx
	o.slot = MaterializedSlot{schema: o.schema}

	n := o.plan.WorkersPlanned
	if n < 0 {
		n = 0
	}
	// The cluster-wide cap bounds what a single Gather may launch. Zero means
	// parallelism is disabled outright — NOT "no cap", which is how an earlier
	// version read it and which would have made `SET max_parallel_workers = 0`
	// silently ineffective. This matches the P1 readers, whose fallback for an
	// unreadable GUC is 0 precisely because that is the safe direction.
	if n > ctx.MaxParallelWorkers {
		n = ctx.MaxParallelWorkers
	}
	if n < 0 {
		n = 0
	}

	o.group = NewParallelGroup(ctx.Ctx)
	o.ch = make(chan rowBatch, gatherChanDepth*(n+1))
	// One allocator shared by every child tree, including the leader's.
	o.pscan = newParallelScanState(0)
	o.pidx = newParallelIndexScanState()

	// P8: hash-join build sides run ONCE, here, before anything fans out.
	// This must precede both NewWorkerContext (which copies the reference)
	// and the goroutine launches (which is the publication edge that makes
	// unlocked reads of the tables safe).
	if err := o.prebuildHashJoins(ctx); err != nil {
		// No worker has been launched yet, so Wait returns at once — but the
		// closer still has to run, or Close's drain has nothing to end it.
		o.startChannelCloser()
		return err
	}

	// S5.6: pre-build the bitmap scan (if any) so workers share the result
	// rather than each running their own index scan.
	if err := o.prebuildBitmapScan(ctx); err != nil {
		o.startChannelCloser()
		return err
	}

	// Worker arenas are allocated HERE, by the leader, before any goroutine
	// starts: mctx.Acquire appends to parent.children without synchronisation,
	// so concurrent acquisition is itself a slice-append race.
	for i := 0; i < n; i++ {
		arena := mmgr.Acquire(ctx.Mctx, mmgr.KindStmt)
		o.arenas = append(o.arenas, arena)
		o.workers = append(o.workers, NewWorkerContext(ctx, arena, o.group.Context()))
	}
	o.launched = n
	ctx.recordGatherLaunched(o.plan, n)

	// Leader participation. PG's parallel_leader_participation has the leader
	// execute a share as well as drain; goopg honours it — and with ZERO
	// workers it is not optional but load-bearing, because a Gather whose
	// workers were all refused would otherwise return no rows at all. A
	// silently empty result is a wrong-results bug, not a degraded plan, so
	// the leader always runs the child when nothing else will.
	o.leaderRuns = n == 0 || ctx.ParallelLeaderParticipation

	for i := 0; i < n; i++ {
		wctx := o.workers[i]
		idx := i
		o.group.Go(func(gctx context.Context) error {
			return o.runWorker(idx, wctx)
		})
	}

	// Close the channel once every worker has finished, so Next() sees a clean
	// end-of-stream rather than having to count workers.
	//
	// This starts HERE — after the last group.Go, before anything that can
	// still fail — and not at the end of Open, because Close's drain loop
	// (`for range o.ch`) terminates only when someone closes o.ch. When the
	// closer was started last, every error return below skipped it and left a
	// live channel with no closer: the statement then hung forever INSIDE
	// Close, at 0 % CPU, with the real error never reaching the client. That
	// is what M0127-P5.9's Q17 "hang" was — >20 min parked in gatherOp.Close
	// while the workers had all exited. The invariant is now: once o.ch
	// exists, a closer for it exists on every path out of Open.
	o.startChannelCloser()

	if o.leaderRuns {
		child, err := o.buildChild()
		if err != nil {
			return err
		}
		// The leader takes blocks from the same allocator as the workers —
		// it is a peer, not an extra full scan.
		attachParallelScan(child, o.pscan)
		attachParallelBitmapScan(child, o.pbm) // S5.6
		attachParallelIndexScan(child, o.pidx) // M0134-0189
		if err := child.Open(ctx); err != nil {
			_ = child.Close()
			return err
		}
		o.leaderChild = child
	}

	return nil
}

// startChannelCloser closes o.ch once every launched worker has finished, so
// Next() sees a clean end-of-stream rather than having to count workers, and so
// Close's drain always terminates.
//
// Idempotent, and it MUST be called after the last group.Go: an empty group's
// Wait returns immediately, so starting it earlier would close the channel out
// from under a worker that has not sent yet (a send on a closed channel panics
// the process, since the panic happens in a goroutine the connection's
// recover() does not cover).
func (o *gatherOp) startChannelCloser() {
	if o.closerStarted {
		return
	}
	o.closerStarted = true
	go func() {
		_ = o.group.Wait()
		close(o.ch)
	}()
}

// runWorker builds this worker's own operator tree and streams materialised
// batches to the leader.
func (o *gatherOp) runWorker(idx int, wctx *Context) error {
	child, err := o.buildChild()
	if err != nil {
		return err
	}
	attachParallelScan(child, o.pscan)
	attachParallelBitmapScan(child, o.pbm) // S5.6
	attachParallelIndexScan(child, o.pidx) // M0134-0189
	defer func() { _ = child.Close() }()
	if err := child.Open(wctx); err != nil {
		return err
	}

	batch := make([]Row, 0, gatherBatchRows)
	flush := func() bool {
		if len(batch) == 0 {
			return true
		}
		select {
		case o.ch <- rowBatch{rows: batch, worker: idx}:
			batch = make([]Row, 0, gatherBatchRows)
			return true
		case <-wctx.Ctx.Done():
			return false
		}
	}

	for {
		if err := wctx.Ctx.Err(); err != nil {
			return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
		}
		slot, err := child.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return err
		}
		if slot == nil {
			continue
		}
		// The ownership boundary. Materialize — never cloneRow, never
		// Slot.CopyTo — because both are shallow, preserve ArenaID, and are
		// silently wrong exactly here while passing every serial test.
		batch = append(batch, MaterializeForTransfer(slot.Row()))
		if len(batch) >= gatherBatchRows && !flush() {
			return wctx.Ctx.Err()
		}
	}
	if !flush() {
		return wctx.Ctx.Err()
	}
	return nil
}

func (o *gatherOp) Next() (TupleSlot, error) {
	for {
		if o.curIdx < len(o.cur) {
			row := o.cur[o.curIdx]
			o.curIdx++
			o.slot.row = row
			return &o.slot, nil
		}
		// Leader participation, INTERLEAVED with draining — this ordering is
		// load-bearing, not stylistic.
		//
		// An earlier version ran the leader's own child to exhaustion before
		// ever reading the channel. Workers then filled their buffer
		// (gatherChanDepth batches), blocked on send, and contributed nothing
		// further while the leader claimed every remaining block from the
		// shared allocator. Measured effect: parallel and serial were within
		// 1% of each other — the feature was structurally inert, and only an
		// uncontrolled warm-cache comparison made it look like a 1.68x win.
		//
		// PG's gather_getnext has the shape below: try the readers first
		// WITHOUT blocking, and otherwise take ONE tuple from the local plan
		// (nodeGather.c). The leader stays a peer that also drains, rather
		// than a producer that happens to drain afterwards.
		if o.leaderChild != nil {
			select {
			case batch, ok := <-o.ch:
				if ok {
					o.cur, o.curIdx = batch.rows, 0
					continue
				}
				// Channel closed: keep taking local rows until exhausted.
			default:
				// Nothing ready from workers; fall through to one local row.
			}
			slot, err := o.leaderChild.Next()
			if err == nil && slot != nil {
				// The leader's own rows never cross a goroutine boundary, so
				// no materialisation is required here.
				return slot, nil
			}
			if err != nil && !errors.Is(err, EOF) {
				return nil, err
			}
			// Leader share exhausted; fall through to draining workers.
			_ = o.leaderChild.Close()
			o.leaderChild = nil
			continue
		}
		batch, ok := <-o.ch
		if !ok {
			// Every worker has finished and the closer has run, so the
			// group's error is settled.
			if err := o.group.Wait(); err != nil {
				return nil, err
			}
			return nil, EOF
		}
		o.cur, o.curIdx = batch.rows, 0
	}
}

func (o *gatherOp) Close() error {
	if o.closed {
		return nil
	}
	o.closed = true

	if o.leaderChild != nil {
		_ = o.leaderChild.Close()
		o.leaderChild = nil
	}

	// Order matters and is the whole reason this method is interesting.
	// 1. Cancel, so workers stop producing.
	o.selfCancelled = true
	o.group.Cancel()
	// 2. Drain, so a worker blocked mid-send can observe the cancellation.
	//    Skipping this is the classic Go shutdown deadlock.
	for range o.ch { //nolint:revive // draining for shutdown, values discarded
	}
	// 3. Join. Worker lifetime is strictly nested inside the statement.
	err := o.group.Wait()

	// 4. Fold per-worker notices back, then release the arenas the leader
	//    allocated. Both happen after the join, which supplies the
	//    happens-before edge.
	for _, w := range o.workers {
		MergeWorkerContext(o.ctx, w)
	}
	for _, a := range o.arenas {
		a.Release()
	}
	o.workers, o.arenas = nil, nil
	if o.ownsSharedBuilds && o.ctx != nil {
		// Retract the published tables so a later serial statement on this
		// session does not adopt a stale build for the same plan node.
		o.ctx.SharedHashBuilds = nil
		o.ownsSharedBuilds = false
	}

	if o.drainErr != nil {
		return o.drainErr
	}
	// A self-inflicted cancellation is not a failure. Closing early — a
	// satisfied LIMIT, an error above the Gather — makes every in-flight
	// worker report 57014, and returning that would turn a normal early exit
	// into a query error. Only surface a worker error when we did not cause
	// it. (Genuine statement cancellation still reaches the client: the
	// statement's own context is already cancelled, and the layers above
	// report it.)
	if err != nil && !o.selfCancelled && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
