package executor

// P4 of docs/design/parallel-query/ — parallel sequential scan + Gather.
// Insertion is still OFF: nothing in the planner builds a Gather, so these
// tests construct the node directly. That is the point of the stage — prove
// the machinery under test before letting it near a user's plan.

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/storage"
)

// ── the block allocator ─────────────────────────────────────────────────────

// TestParallelScanStatePartitions is the core property: every block is handed
// out exactly once, and the union across workers is the whole relation. A
// duplicate would double-count rows; a gap would lose them.
func TestParallelScanStatePartitions(t *testing.T) {
	const nBlocks = 1000
	const workers = 8
	st := newParallelScanState(nBlocks)

	var mu sync.Mutex
	seen := make([]int, nBlocks)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var mine []storage.BlockNumber
			for {
				b, ok := st.nextBlock()
				if !ok {
					break
				}
				mine = append(mine, b)
			}
			mu.Lock()
			for _, b := range mine {
				seen[b]++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	for b, n := range seen {
		if n != 1 {
			t.Fatalf("block %d handed out %d times, want exactly 1", b, n)
		}
	}
}

func TestParallelScanStateEmptyRelation(t *testing.T) {
	st := newParallelScanState(0)
	if _, ok := st.nextBlock(); ok {
		t.Error("an empty relation must hand out no blocks")
	}
}

// ── Gather ──────────────────────────────────────────────────────────────────

// scriptedOp is a minimal Operator that yields a fixed set of rows. It lets
// these tests drive gatherOp without a planner or storage.
type scriptedOp struct {
	rows    []Row
	idx     int
	schema  optimizer.Schema
	onNext  func() error // optional hook: inject an error or a panic
	openErr error
	slot    MaterializedSlot
}

func (s *scriptedOp) Schema() optimizer.Schema { return s.schema }
func (s *scriptedOp) Open(ctx *Context) error {
	s.slot = MaterializedSlot{schema: s.schema}
	return s.openErr
}
func (s *scriptedOp) Next() (TupleSlot, error) {
	if s.onNext != nil {
		if err := s.onNext(); err != nil {
			return nil, err
		}
	}
	if s.idx >= len(s.rows) {
		return nil, EOF
	}
	s.slot.row = s.rows[s.idx]
	s.idx++
	return &s.slot, nil
}
func (s *scriptedOp) Close() error { return nil }

func intRows(from, to int) []Row {
	out := make([]Row, 0, to-from)
	for i := from; i < to; i++ {
		out = append(out, Row{NewIntDatum(int64(i))})
	}
	return out
}

func gatherTestCtx(t *testing.T) *Context {
	t.Helper()
	ctx := NewContext()
	ctx.Ctx = context.Background()
	ctx.MaxParallelWorkers = 8
	return ctx
}

func newTestGather(nWorkers int, mk func() (Operator, error)) (*gatherOp, *optimizer.Gather) {
	schema := optimizer.Schema{{Name: "n"}}
	child := &scriptedOp{schema: schema}
	p := optimizer.NewGather(0, &stubPlanNode{schema: schema}, nWorkers)
	_ = child
	return newGatherOp(p, mk), p
}

// stubPlanNode stands in for a partial plan subtree; gatherOp only reads its
// Output(), because the operator tree comes from the injected builder.
type stubPlanNode struct{ schema optimizer.Schema }

func (s *stubPlanNode) Pos() int               { return 0 }
func (s *stubPlanNode) Output() optimizer.Schema { return s.schema }

// TestGatherCollectsEveryWorkersRows pins the basic contract: the leader sees
// the union of what the workers produced, once each.
func TestGatherCollectsEveryWorkersRows(t *testing.T) {
	const workers = 4
	const perWorker = 500

	var next int32
	var mu sync.Mutex
	assigned := 0

	g, _ := newTestGather(workers, func() (Operator, error) {
		mu.Lock()
		id := assigned
		assigned++
		mu.Unlock()
		return &scriptedOp{
			schema: optimizer.Schema{{Name: "n"}},
			rows:   intRows(id*perWorker, (id+1)*perWorker),
		}, nil
	})
	_ = next

	ctx := gatherTestCtx(t)
	if err := g.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	var got []int
	for {
		slot, err := g.Next()
		if errors.Is(err, EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		got = append(got, int(slot.Row()[0].Int))
	}
	if err := g.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if len(got) != workers*perWorker {
		t.Fatalf("got %d rows, want %d", len(got), workers*perWorker)
	}
	// Order across workers is undefined — compare as a set, which is exactly
	// what the design says tests over unordered output must do.
	sort.Ints(got)
	for i, v := range got {
		if v != i {
			t.Fatalf("row %d = %d; the union must be each value exactly once", i, v)
		}
	}
}

// TestGatherRowsAreTransferable pins the ownership contract at the boundary
// that matters: whatever the leader receives must not reference a worker's
// arena.
func TestGatherRowsAreTransferable(t *testing.T) {
	g, _ := newTestGather(2, func() (Operator, error) {
		return &scriptedOp{
			schema: optimizer.Schema{{Name: "n"}},
			rows:   []Row{{NewStringDatum("abc")}, {NewStringDatum("def")}},
		}, nil
	})
	ctx := gatherTestCtx(t)
	if err := g.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = g.Close() }()
	for {
		slot, err := g.Next()
		if errors.Is(err, EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if err := AssertTransferable(slot.Row()); err != nil {
			t.Fatalf("row crossed the worker boundary unowned: %v", err)
		}
	}
}

// TestGatherWorkerErrorSurfaces pins that a worker failure reaches the leader
// rather than being swallowed into a short read.
func TestGatherWorkerErrorSurfaces(t *testing.T) {
	sentinel := &ExecError{Code: "XX000", Message: "worker exploded"}
	var once sync.Once
	g, _ := newTestGather(3, func() (Operator, error) {
		op := &scriptedOp{schema: optimizer.Schema{{Name: "n"}}, rows: intRows(0, 100)}
		once.Do(func() {
			op.onNext = func() error { return sentinel }
		})
		return op, nil
	})
	ctx := gatherTestCtx(t)
	if err := g.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	var lastErr error
	for {
		_, err := g.Next()
		if errors.Is(err, EOF) {
			break
		}
		if err != nil {
			lastErr = err
			break
		}
	}
	_ = g.Close()
	if lastErr == nil {
		t.Fatal("a worker error must surface at the leader")
	}
}

// TestGatherWorkerPanicBecomesError pins the blast radius: a panic in a
// goroutine the server did not start would otherwise kill the process.
func TestGatherWorkerPanicBecomesError(t *testing.T) {
	var once sync.Once
	g, _ := newTestGather(2, func() (Operator, error) {
		op := &scriptedOp{schema: optimizer.Schema{{Name: "n"}}, rows: intRows(0, 50)}
		once.Do(func() {
			op.onNext = func() error { panic("worker boom") }
		})
		return op, nil
	})
	ctx := gatherTestCtx(t)
	if err := g.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	var sawErr bool
	for {
		_, err := g.Next()
		if errors.Is(err, EOF) {
			break
		}
		if err != nil {
			sawErr = true
			if !strings.Contains(err.Error(), "panic") {
				t.Errorf("panic should be reported as such, got %v", err)
			}
			break
		}
	}
	_ = g.Close()
	if !sawErr {
		t.Error("a worker panic must surface as a query error")
	}
}

// TestGatherEarlyCloseDoesNotDeadlock is the shutdown test. Workers produce far
// more rows than the leader consumes, so several are blocked mid-send when
// Close runs. Close must cancel, DRAIN, then join — skipping the drain is the
// classic Go shutdown deadlock, and it is the specific bug this ordering
// exists to prevent.
func TestGatherEarlyCloseDoesNotDeadlock(t *testing.T) {
	g, _ := newTestGather(4, func() (Operator, error) {
		return &scriptedOp{schema: optimizer.Schema{{Name: "n"}}, rows: intRows(0, 20000)}, nil
	})
	ctx := gatherTestCtx(t)
	if err := g.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Consume a handful of rows, as a LIMIT would, then abandon the scan.
	for i := 0; i < 5; i++ {
		if _, err := g.Next(); err != nil {
			t.Fatalf("Next: %v", err)
		}
	}

	done := make(chan error, 1)
	go func() { done <- g.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close deadlocked: workers blocked on send were never drained")
	}
}

// TestGatherCancellationStopsWorkers pins that the statement's cancellation —
// timeout, client EOF, explicit cancel — reaches the workers.
func TestGatherCancellationStopsWorkers(t *testing.T) {
	g, _ := newTestGather(4, func() (Operator, error) {
		return &scriptedOp{schema: optimizer.Schema{{Name: "n"}}, rows: intRows(0, 100000)}, nil
	})
	ctx := gatherTestCtx(t)
	cctx, cancel := context.WithCancel(context.Background())
	ctx.Ctx = cctx
	if err := g.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := g.Next(); err != nil {
		t.Fatalf("Next: %v", err)
	}
	cancel()

	done := make(chan struct{})
	go func() {
		for {
			if _, err := g.Next(); err != nil {
				break
			}
		}
		_ = g.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cancellation did not stop the workers")
	}
}

// TestGatherLeaksNoGoroutines pins the lifecycle rule: worker lifetime is
// strictly nested inside the statement, because the statement arena is
// released by a defer in the dispatcher and cascades to worker arenas. An
// escaped worker would read freed memory.
func TestGatherLeaksNoGoroutines(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 5; i++ {
		g, _ := newTestGather(4, func() (Operator, error) {
			return &scriptedOp{schema: optimizer.Schema{{Name: "n"}}, rows: intRows(0, 2000)}, nil
		})
		ctx := gatherTestCtx(t)
		if err := g.Open(ctx); err != nil {
			t.Fatalf("Open: %v", err)
		}
		for j := 0; j < 3; j++ {
			if _, err := g.Next(); err != nil {
				break
			}
		}
		if err := g.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before+2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if leaked := runtime.NumGoroutine() - before; leaked > 2 {
		t.Errorf("%d goroutines leaked across 5 Gather open/close cycles", leaked)
	}
}

// TestGatherZeroWorkersRunsSerially pins the degenerate case:
// max_parallel_workers = 0 must not error and must not lose rows — the leader
// runs the partial plan itself. A partial plan is still a correct plan; it is
// just not divided.
//
// This test also pins that the cap of 0 means NO parallelism rather than "no
// cap", which an earlier version of Open got backwards and which would have
// made `SET max_parallel_workers = 0` silently ineffective.
func TestGatherZeroWorkersRunsSerially(t *testing.T) {
	g, _ := newTestGather(4, func() (Operator, error) {
		return &scriptedOp{schema: optimizer.Schema{{Name: "n"}}, rows: intRows(0, 10)}, nil
	})
	ctx := gatherTestCtx(t)
	ctx.MaxParallelWorkers = 0
	if err := g.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	n := 0
	for {
		_, err := g.Next()
		if errors.Is(err, EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		n++
	}
	if err := g.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if g.WorkersLaunched() != 0 {
		t.Errorf("launched %d workers with the cap at 0", g.WorkersLaunched())
	}
	// The load-bearing assertion: with no workers the LEADER must run the
	// child, so the result is complete rather than empty. An earlier version
	// of the operator returned zero rows here, which is a wrong-results bug
	// dressed as a degraded plan — a Gather is a transport, not a filter.
	if n != 10 {
		t.Errorf("got %d rows, want all 10: with no workers the leader must "+
			"run the child itself", n)
	}
}

var _ = fmt.Sprintf

// TestGatherCloseTerminatesAfterOpenError pins the invariant that M0127-P5.9's
// Q17 "hang" violated: once Open has created o.ch, EVERY path out of Open must
// leave a goroutine that will close it, because Close drains that channel
// unconditionally.
//
// The failure this protects against is not a slow query — it is a permanently
// wedged backend. Open returned an error (leader child build/Open failed), the
// closer goroutine at the end of Open was skipped, the workers all exited, and
// Close then sat in `for range o.ch` forever at 0 % CPU. The statement's real
// error never reached the client, so the symptom presented as "Q17 takes >20
// min at flag ON" with nothing in EXPLAIN to explain it.
//
// Each case runs under a watchdog because the regression is a deadlock: a
// plain t.Fatalf would never be reached.
func TestGatherCloseTerminatesAfterOpenError(t *testing.T) {
	buildErr := errors.New("build refused")

	cases := []struct {
		name    string
		workers int
		mk      func() (Operator, error)
	}{
		// Zero workers forces leader participation, so the leader's own
		// buildChild call is the one that fails — deterministically.
		{"zero workers, builder fails", 0, func() (Operator, error) { return nil, buildErr }},
		// With workers launched the same builder fails in every goroutine too,
		// so the group carries an error AND Open returns one.
		{"workers launched, builder fails", 2, func() (Operator, error) { return nil, buildErr }},
		// The second error return in Open: the child builds but refuses Open.
		{"child Open fails", 0, func() (Operator, error) {
			return &scriptedOp{schema: optimizer.Schema{{Name: "n"}}, openErr: buildErr}, nil
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, _ := newTestGather(tc.workers, tc.mk)
			ctx := gatherTestCtx(t)
			// PG's parallel_leader_participation default. It is what makes the
			// leader build a child of its own in the workers>0 case, and thus
			// what makes Open able to fail there at all.
			ctx.ParallelLeaderParticipation = true

			if err := g.Open(ctx); err == nil {
				t.Fatalf("Open: want error, got nil")
			}

			done := make(chan error, 1)
			go func() { done <- g.Close() }()
			select {
			case <-done:
				// Close returned; whatever it returned, it did not deadlock.
			case <-time.After(30 * time.Second):
				t.Fatalf("Close deadlocked after a failed Open — o.ch has no closer")
			}
		})
	}
}
