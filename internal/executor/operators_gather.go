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

	"github.com/goopg/goopg/internal/mctx"
	"github.com/goopg/goopg/internal/planner"
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
	plan   *planner.Gather
	ctx    *Context
	schema planner.Schema

	// buildChild constructs a fresh operator tree over the partial plan for
	// one worker. Injected so tests can drive the operator without a planner.
	buildChild func() (Operator, error)

	group   *ParallelGroup
	ch      chan rowBatch
	workers []*Context
	arenas  []*mctx.Context

	// cur is the batch being drained into Next()'s return values.
	cur    []Row
	curIdx int
	slot   MaterializedSlot

	closed   bool
	drainErr error
	launched int

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
}

func newGatherOp(p *planner.Gather, buildChild func() (Operator, error)) *gatherOp {
	return &gatherOp{plan: p, schema: p.Output(), buildChild: buildChild}
}

func (o *gatherOp) Schema() planner.Schema { return o.schema }

// WorkersLaunched reports how many workers actually started, which EXPLAIN
// ANALYZE renders as PG's `Workers Launched:`. It can be lower than
// WorkersPlanned once the cluster-wide cap is honoured (P6).
func (o *gatherOp) WorkersLaunched() int { return o.launched }

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

	// Worker arenas are allocated HERE, by the leader, before any goroutine
	// starts: mctx.Acquire appends to parent.children without synchronisation,
	// so concurrent acquisition is itself a slice-append race.
	for i := 0; i < n; i++ {
		arena := mctx.Acquire(ctx.Mctx, mctx.KindStmt)
		o.arenas = append(o.arenas, arena)
		o.workers = append(o.workers, NewWorkerContext(ctx, arena, o.group.Context()))
	}
	o.launched = n

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

	if o.leaderRuns {
		child, err := o.buildChild()
		if err != nil {
			return err
		}
		// The leader takes blocks from the same allocator as the workers —
		// it is a peer, not an extra full scan.
		attachParallelScan(child, o.pscan)
		if err := child.Open(ctx); err != nil {
			_ = child.Close()
			return err
		}
		o.leaderChild = child
	}

	// Close the channel once every worker has finished, so Next() sees a
	// clean end-of-stream rather than having to count workers.
	go func() {
		_ = o.group.Wait()
		close(o.ch)
	}()

	return nil
}

// runWorker builds this worker's own operator tree and streams materialised
// batches to the leader.
func (o *gatherOp) runWorker(idx int, wctx *Context) error {
	child, err := o.buildChild()
	if err != nil {
		return err
	}
	attachParallelScan(child, o.pscan)
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
		// Leader participation: execute our own share before blocking on the
		// channel. Note the cost PG has too — while the leader is running the
		// child it is NOT draining, so workers can block on send. That is what
		// parallel_leader_participation = off exists to avoid.
		if o.leaderChild != nil {
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
