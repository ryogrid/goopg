package executor

// operators_gather_merge.go — P7 of docs/design/parallel-query/, chapter 05 §4.
//
// gatherMergeOp is gatherOp with output ordering preserved. Each worker's
// stream is already sorted by the node's keys; the leader merges them.
//
// The merge primitive is not new: sortHeap (operators.go) already merges N
// sorted spill files for the external sort, and takes an arbitrary `less`. This
// reuses it with worker channels as the sources — the same algorithm PG's
// nodeGatherMerge.c uses, over a different transport.
//
// The one structural difference from plain Gather: the leader cannot drain a
// batch at a time. It must hold ONE row per worker at all times (the heap
// front), so it interleaves at row granularity.

import (
	"container/heap"
	"context"
	"errors"

	"github.com/goopg/goopg/internal/utils/mmgr"
	"github.com/goopg/goopg/internal/optimizer"
)

// gmSource is one ordered input to the merge: a worker's channel, or the
// leader's own child.
type gmSource struct {
	// cur is the row at this source's front, valid while live.
	cur  Row
	live bool
	// ch is non-nil for a worker source.
	ch <-chan rowBatch
	// pending holds rows already received from ch but not yet consumed.
	pending []Row
	// local is non-nil for the leader's own share.
	local Operator
}

// gatherMergeOp implements planner.GatherMerge.
type gatherMergeOp struct {
	plan       *optimizer.GatherMerge
	ctx        *Context
	schema     optimizer.Schema
	buildChild func() (Operator, error)

	group   *ParallelGroup
	chans   []chan rowBatch
	workers []*Context
	arenas  []*mmgr.Context
	pscan   *parallelScanState

	sources  []*gmSource
	h        *gmHeap
	keys     []optimizer.SortKey
	sortErr  error
	slot     MaterializedSlot
	launched int

	closed        bool
	selfCancelled bool

	// ownsSharedBuilds — see gatherOp; P8 hash tables published on ctx.
	ownsSharedBuilds bool
}

func newGatherMergeOp(p *optimizer.GatherMerge, buildChild func() (Operator, error)) *gatherMergeOp {
	return &gatherMergeOp{plan: p, schema: p.Output(), buildChild: buildChild, keys: p.Keys}
}

func (o *gatherMergeOp) Schema() optimizer.Schema { return o.schema }

// WorkersLaunched reports the worker count for EXPLAIN ANALYZE.
func (o *gatherMergeOp) WorkersLaunched() int { return o.launched }

func (o *gatherMergeOp) Open(ctx *Context) error {
	o.ctx = ctx
	o.slot = MaterializedSlot{schema: o.schema}

	n := o.plan.WorkersPlanned
	if n < 0 {
		n = 0
	}
	if n > ctx.MaxParallelWorkers {
		n = ctx.MaxParallelWorkers
	}
	if n < 0 {
		n = 0
	}

	o.group = NewParallelGroup(ctx.Ctx)
	o.pscan = newParallelScanState(0)

	// P8: build shared hash tables once, before fan-out. Same ordering
	// requirement as gatherOp — before worker contexts, before goroutines.
	prebuilt, err := prebuildSharedHashJoins(ctx, o.plan.Child, o.buildChild)
	if err != nil {
		return err
	}
	if prebuilt != nil {
		ctx.SharedHashBuilds = prebuilt
		o.ownsSharedBuilds = true
	}

	// Unlike plain Gather, each worker gets its OWN channel: the merge needs
	// to know which stream a row came from, because it must take the next row
	// from that same stream to keep the heap correct. One shared channel would
	// interleave the streams and destroy the ordering the node exists to
	// preserve.
	for i := 0; i < n; i++ {
		arena := mmgr.Acquire(ctx.Mctx, mmgr.KindStmt)
		o.arenas = append(o.arenas, arena)
		o.workers = append(o.workers, NewWorkerContext(ctx, arena, o.group.Context()))
		o.chans = append(o.chans, make(chan rowBatch, gatherChanDepth))
	}
	o.launched = n

	for i := 0; i < n; i++ {
		wctx, idx := o.workers[i], i
		o.group.Go(func(gctx context.Context) error {
			return o.runWorker(idx, wctx)
		})
	}

	// Leader participation: with zero workers it is not optional, or the node
	// returns nothing.
	if n == 0 || ctx.ParallelLeaderParticipation {
		child, err := o.buildChild()
		if err != nil {
			return err
		}
		attachParallelScan(child, o.pscan)
		if err := child.Open(ctx); err != nil {
			_ = child.Close()
			return err
		}
		o.sources = append(o.sources, &gmSource{local: child, live: true})
	}
	for i := 0; i < n; i++ {
		o.sources = append(o.sources, &gmSource{ch: o.chans[i], live: true})
	}

	// Prime every source, then build the heap. A source that yields nothing is
	// dropped before the heap is formed rather than special-cased inside it.
	live := o.sources[:0]
	for _, src := range o.sources {
		ok, err := o.advance(src)
		if err != nil {
			return err
		}
		if ok {
			live = append(live, src)
		}
	}
	o.sources = live

	o.h = &gmHeap{less: o.lessRows}
	o.h.srcs = append(o.h.srcs, o.sources...)
	heap.Init(o.h)
	return nil
}

// gmHeap is the merge front: one entry per live source, ordered by the node's
// sort keys.
//
// The design said to reuse sortOp's sortHeap, and the ALGORITHM is exactly
// that — but sortHeap is typed on *sortSource, the external sort's spill-file
// cursor. Reusing the type would have meant adding a parallel-query field to a
// struct that has nothing to do with parallelism, so every future reader of the
// sort operator would meet it. Fifteen lines of heap boilerplate is the cheaper
// trade.
type gmHeap struct {
	srcs []*gmSource
	less func(a, b Row) bool
}

func (h *gmHeap) Len() int           { return len(h.srcs) }
func (h *gmHeap) Less(i, j int) bool { return h.less(h.srcs[i].cur, h.srcs[j].cur) }
func (h *gmHeap) Swap(i, j int)      { h.srcs[i], h.srcs[j] = h.srcs[j], h.srcs[i] }
func (h *gmHeap) Push(x any)         { h.srcs = append(h.srcs, x.(*gmSource)) }
func (h *gmHeap) Pop() any {
	n := len(h.srcs)
	x := h.srcs[n-1]
	h.srcs = h.srcs[:n-1]
	return x
}

func (o *gatherMergeOp) runWorker(idx int, wctx *Context) error {
	child, err := o.buildChild()
	if err != nil {
		return err
	}
	attachParallelScan(child, o.pscan)
	defer func() { _ = child.Close() }()
	if err := child.Open(wctx); err != nil {
		return err
	}
	defer close(o.chans[idx])

	batch := make([]Row, 0, gatherBatchRows)
	flush := func() bool {
		if len(batch) == 0 {
			return true
		}
		select {
		case o.chans[idx] <- rowBatch{rows: batch, worker: idx}:
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

// advance pulls the next row for src, returning false when it is exhausted.
func (o *gatherMergeOp) advance(src *gmSource) (bool, error) {
	if src.local != nil {
		slot, err := src.local.Next()
		if errors.Is(err, EOF) {
			_ = src.local.Close()
			src.local, src.live = nil, false
			return false, nil
		}
		if err != nil {
			return false, err
		}
		// The leader's own rows do not cross a goroutine boundary, but they DO
		// have to survive until the heap pops them — several Next() calls
		// later — so they must be materialised like any retained row.
		src.cur = MaterializeForTransfer(slot.Row())
		return true, nil
	}
	for len(src.pending) == 0 {
		batch, ok := <-src.ch
		if !ok {
			src.live = false
			return false, nil
		}
		src.pending = batch.rows
	}
	src.cur = src.pending[0]
	src.pending = src.pending[1:]
	return true, nil
}

// lessRows orders two rows by the node's sort keys. Evaluation errors are
// captured rather than returned so the comparator stays a strict weak
// ordering, matching sortOp's own discipline.
func (o *gatherMergeOp) lessRows(a, b Row) bool {
	for _, k := range o.keys {
		av, err := evalSortKeyValue(k.Expr, a, o.ctx)
		if err != nil {
			if o.sortErr == nil {
				o.sortErr = err
			}
			return false
		}
		bv, err := evalSortKeyValue(k.Expr, b, o.ctx)
		if err != nil {
			if o.sortErr == nil {
				o.sortErr = err
			}
			return false
		}
		// NULL placement is `k.NullsFirst`, NOT `k.Desc`. The two coincide
		// only for PG's DEFAULTS (NULLS LAST for ASC, NULLS FIRST for DESC,
		// which is how `sortByNullsFirst` resolves an omitted clause), so the
		// old `k.Desc` form agreed with `sortOp` by accident and disagreed the
		// moment a query wrote NULLS FIRST/LAST explicitly.
		//
		// That is a WRONG-RESULTS bug, not a cosmetic one, and it was live:
		// each worker's Sort ordered NULLs one way and this merge re-ordered
		// them the other, so the leader interleaved them as soon as one worker
		// exhausted its NULLs. Measured on HEAD before the fix, over a
		// Gather Merge -> Sort -> Seq Scan plan:
		//
		//   select nullif(l_linenumber,1) from lineitem
		//     order by 1 asc nulls first
		//   -> a NULL surfaced at row 1183498, AFTER non-NULLs (PG: correct)
		//
		// This comparator and `sortOp.lessRows` are one rule read twice: the
		// merge orders the streams the worker sorts produced, so any
		// disagreement between them is unordered output by construction.
		if av.IsNull() && !bv.IsNull() {
			return k.NullsFirst
		}
		if !av.IsNull() && bv.IsNull() {
			return !k.NullsFirst
		}
		if av.IsNull() && bv.IsNull() {
			continue
		}
		cmp, err := compareDatum(av, bv, 0)
		if err != nil {
			if o.sortErr == nil {
				o.sortErr = err
			}
			return false
		}
		if cmp == 0 {
			continue
		}
		if k.Desc {
			return cmp > 0
		}
		return cmp < 0
	}
	return false
}

func (o *gatherMergeOp) Next() (TupleSlot, error) {
	if o.sortErr != nil {
		return nil, o.sortErr
	}
	if o.h == nil || o.h.Len() == 0 {
		if err := o.group.Wait(); err != nil && !o.selfCancelled {
			return nil, err
		}
		return nil, EOF
	}

	src := o.h.srcs[0]
	row := src.cur

	ok, err := o.advance(src)
	if err != nil {
		return nil, err
	}
	if ok {
		heap.Fix(o.h, 0)
	} else {
		// Exhausted stream leaves the heap, exactly as nodeGatherMerge.c
		// removes a finished reader.
		heap.Pop(o.h)
	}

	if o.sortErr != nil {
		return nil, o.sortErr
	}
	o.slot.row = row
	return &o.slot, nil
}

func (o *gatherMergeOp) Close() error {
	if o.closed {
		return nil
	}
	o.closed = true

	for _, src := range o.sources {
		if src.local != nil {
			_ = src.local.Close()
			src.local = nil
		}
	}

	// Same ordering as gatherOp.Close, and for the same reason: cancel, then
	// DRAIN so a worker blocked mid-send can observe it, then join.
	o.selfCancelled = true
	o.group.Cancel()
	for _, ch := range o.chans {
		for range ch { //nolint:revive // draining for shutdown
		}
	}
	err := o.group.Wait()

	for _, w := range o.workers {
		MergeWorkerContext(o.ctx, w)
	}
	for _, a := range o.arenas {
		a.Release()
	}
	o.workers, o.arenas = nil, nil
	if o.ownsSharedBuilds && o.ctx != nil {
		o.ctx.SharedHashBuilds = nil
		o.ownsSharedBuilds = false
	}

	if o.sortErr != nil {
		return o.sortErr
	}
	if err != nil && !o.selfCancelled && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
