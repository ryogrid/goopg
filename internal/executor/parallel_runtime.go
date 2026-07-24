package executor

// parallel_runtime.go — P3 of docs/design/parallel-query/: the tuple-ownership
// boundary and the failure machinery. Chapter 03 §3 and §4.
//
// Neither existed before. The executor propagates errors purely by return value
// up the Next() chain, ExecError has no Unwrap, and there is no errgroup or
// error-channel pattern anywhere to copy — so "first error wins" has to be made
// explicit rather than emergent.

import (
	"context"
	"fmt"
	"sync"

	"github.com/goopg/goopg/internal/mctx"
)

// ── tuple ownership ─────────────────────────────────────────────────────────

// MaterializeForTransfer returns a row that is safe to hand to another
// goroutine. It is the ONLY sanctioned way to move a row across a worker
// boundary.
//
// Two independent reasons a raw slot is not safe, either sufficient:
//
//  1. Slot aliasing. Scans, projectOp, the NLI and the slab path's reused
//     output slot all return buffers overwritten on the next Next(). Sending
//     one races the producer's own next iteration.
//
//  2. Arena lifetime. A KindString/KindBytes Datum with ArenaID != 0 is an
//     (offset, length) into an mctx arena, and seqScanOp resets its per-page
//     arena at every block boundary — exactly the cadence at which a parallel
//     worker takes new work. A row that crossed unpromoted would be read by
//     the leader after the producer recycled the bytes underneath it.
//
// What must NOT be used instead: cloneRow and Slot.CopyTo. Both are shallow
// and preserve ArenaID. They are correct within one goroutine and silently
// wrong across one, they are the obvious-looking helpers, and they pass every
// single-threaded test. This is the single easiest mistake to make in the
// whole parallel design.
func MaterializeForTransfer(row Row) Row {
	return cloneRowOwned(row)
}

// AssertTransferable reports whether every datum in row is safe to read from
// another goroutine, returning a descriptive error when one is not.
//
// The check is `ArenaID == 0 || ArenaID == PermContextID` on EVERY kind, not
// just KindString/KindBytes. That distinction is the whole point:
// cloneRowOwned promotes only those two kinds, but big-mantissa KindNumeric is
// ALSO arena-backed (its ArenaID + packed offset/length address an mctx
// payload) and falls through cloneRowOwned's else-branch with ArenaID intact.
//
// That is safe today only because every newBigNumericInCtx call site allocates
// from mctx.Perm(), the process-global permanent context, which is never reset
// — an invariant that lives elsewhere and is invisible at the point of use.
// Checking only strings would let exactly this case through if that invariant
// ever changes.
//
// Intended for tests and debug builds; it is O(width) per row and not meant
// for the production send path.
func AssertTransferable(row Row) error {
	for i, d := range row {
		if d.ArenaID == 0 || d.ArenaID == mctx.PermContextID {
			continue
		}
		return fmt.Errorf(
			"column %d (kind %v) still references arena %d: rows crossing a worker "+
				"boundary must go through MaterializeForTransfer, not cloneRow or Slot.CopyTo",
			i, d.Kind, d.ArenaID)
	}
	return nil
}

// ── failure and cancellation ────────────────────────────────────────────────

// parallelErrBox collects worker failures under a first-error-wins rule.
//
// Which error surfaces when two workers fail simultaneously is genuinely
// non-deterministic. PG has the same property, so tests must assert an error
// of the right CLASS rather than a specific one.
type parallelErrBox struct {
	once sync.Once
	err  error
}

// record keeps err if it is the first non-nil error seen, and reports whether
// this call was the one that won.
func (b *parallelErrBox) record(err error) bool {
	if err == nil {
		return false
	}
	won := false
	b.once.Do(func() {
		b.err = err
		won = true
	})
	return won
}

// err returns the winning error, or nil.
func (b *parallelErrBox) get() error { return b.err }

// ParallelGroup runs worker functions and gives the leader one error and a
// clean shutdown. It is deliberately narrower than errgroup: it owns the
// cancellation context, converts panics, and guarantees that Wait joins every
// goroutine even when the leader stops consuming early.
type ParallelGroup struct {
	cancel context.CancelFunc
	ctx    context.Context
	wg     sync.WaitGroup
	errBox parallelErrBox
}

// NewParallelGroup derives a cancellable child of parent. Cancelling the child
// stops all workers; cancelling the parent — statement timeout, client EOF via
// the MSG_PEEK watcher, an explicit cancel — propagates automatically, so
// workers keep the throttled ctx.Err() polling they already do at block and
// row-batch boundaries with no change.
func NewParallelGroup(parent context.Context) *ParallelGroup {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &ParallelGroup{ctx: ctx, cancel: cancel}
}

// Context returns the group's cancellation context, to be threaded into each
// worker's executor context.
func (g *ParallelGroup) Context() context.Context { return g.ctx }

// Go starts one worker.
//
// A panic in a goroutine the server did not start kills the PROCESS — serveConn
// has a recover(), but it only protects the connection goroutine. Converting
// the panic here to an ExecError (XX000) makes the blast radius a failed query,
// which is what PG has: a crashed worker fails the query, not the cluster.
//
// The first error, however it arose, cancels the siblings.
func (g *ParallelGroup) Go(fn func(ctx context.Context) error) {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				g.fail(&ExecError{
					Code:    "XX000",
					Message: fmt.Sprintf("parallel worker panic: %v", r),
				})
			}
		}()
		if err := fn(g.ctx); err != nil {
			g.fail(err)
		}
	}()
}

// fail records an error and stops the other workers.
func (g *ParallelGroup) fail(err error) {
	g.errBox.record(err)
	g.cancel()
}

// Wait joins every worker and returns the first error, then releases the
// group's context.
//
// It deliberately does NOT cancel up front. An earlier version did, and it was
// wrong in a way worth recording: cancelling before joining means a worker
// blocked on ctx.Done() wakes and reports context.Canceled, which can win the
// first-error race against a sibling's genuine failure. The caller would then
// see "context canceled" instead of the error that actually caused the query
// to fail. Cancellation is a CONSEQUENCE of failure here, not a peer of it.
//
// Two obligations on the caller, both load-bearing:
//
//   - Drain whatever channel the workers send on BEFORE calling Wait, or a
//     worker blocked on a send never observes cancellation and Wait deadlocks.
//     That is the classic Go shutdown bug, and it is why the Gather operator's
//     Close drains first.
//   - Call Cancel first when terminating early (LIMIT satisfied, error above
//     the Gather); Wait alone waits for workers to finish naturally.
//
// Worker lifetime is strictly nested inside the statement: the statement mctx
// is released by a defer in the dispatcher and cascades to the workers'
// arenas, and statement-end lock release runs on the statement backend ID — so
// a worker outliving Wait would read freed memory and hold locks that no
// longer exist.
func (g *ParallelGroup) Wait() error {
	g.wg.Wait()
	g.cancel() // release the context's resources; all workers have returned
	return g.errBox.get()
}

// Cancel stops the workers without waiting. Wait must still be called, and the
// caller must drain the workers' output channel first.
func (g *ParallelGroup) Cancel() { g.cancel() }
