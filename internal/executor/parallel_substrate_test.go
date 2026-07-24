package executor

// P3 of docs/design/parallel-query/ — the concurrency substrate, with no
// parallel execution attached. race-gate is the point of this stage: these
// tests spin N goroutines through the machinery so the detector has something
// to observe.

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/mctx"
)

// ── tuple ownership ─────────────────────────────────────────────────────────

// TestAssertTransferableRejectsArenaBackedRow is the load-bearing test of the
// stage. It pins the distinction that makes the ownership contract correct:
// the check is on EVERY kind, not just the two that cloneRowOwned promotes.
func TestAssertTransferableRejectsArenaBackedRow(t *testing.T) {
	arena := mctx.Acquire(nil, mctx.KindStmt)
	defer arena.Release()

	off, length := arena.AllocString("hello")
	arenaStr := Datum{
		Kind:    KindString,
		ArenaID: arena.ID(),
		Int:     int64(off)<<32 | int64(length),
	}

	if err := AssertTransferable(Row{arenaStr}); err == nil {
		t.Fatal("an arena-backed string must be rejected before crossing a worker boundary")
	}

	// The sanctioned helper promotes it.
	owned := MaterializeForTransfer(Row{arenaStr})
	if err := AssertTransferable(owned); err != nil {
		t.Errorf("MaterializeForTransfer must produce a transferable row: %v", err)
	}
	if got := owned[0].StringValue(); got != "hello" {
		t.Errorf("value lost in promotion: %q", got)
	}

	// cloneRow is the trap: it is the obvious-looking helper, it is correct
	// within one goroutine, and it leaves ArenaID intact. If this ever starts
	// passing, the contract has silently weakened.
	if err := AssertTransferable(cloneRow(Row{arenaStr})); err == nil {
		t.Error("cloneRow is shallow and must NOT satisfy the transfer contract")
	}
}

// TestAssertTransferableAllowsPermanentArena pins the other half: the
// permanent context is never reset, so datums pointing into it are safe to
// share. This is what makes big-mantissa numerics transferable even though
// cloneRowOwned does not promote them.
func TestAssertTransferableAllowsPermanentArena(t *testing.T) {
	// A numeric too large for an int64 mantissa lands in the big lane, which
	// stores its payload in mctx.Perm().
	big1, _ := new(big.Int).SetString("123456789012345678901234567890", 10)
	d := NewNumericBigDatum(big1, 0)
	if d.ArenaID != mctx.PermContextID {
		t.Skipf("big numeric did not land in the permanent arena (ArenaID=%d); "+
			"if this changed, AssertTransferable and the design's 03 §3.1 need revisiting", d.ArenaID)
	}
	if err := AssertTransferable(Row{d}); err != nil {
		t.Errorf("permanent-arena datum must be transferable: %v", err)
	}
	// And note what this proves about cloneRowOwned: it does NOT promote this
	// datum (it only handles KindString/KindBytes), yet the row is still safe.
	// The safety comes from the allocation site, not from the copy.
	promoted := MaterializeForTransfer(Row{d})
	if promoted[0].ArenaID != mctx.PermContextID {
		t.Log("note: cloneRowOwned now promotes big numerics; 03 §3.1 can be simplified")
	}
}

// ── worker context derivation ───────────────────────────────────────────────

func TestNewWorkerContextSharesAndSeparates(t *testing.T) {
	leader := NewContext()
	leader.WorkMem = 1234
	leader.MaxParallelWorkersPerGather = 4
	leader.MinParallelTableScanBlocks = 2048
	leader.Notices = []string{"leader notice"}
	leader.ParamExec = []Datum{NewIntDatum(7), NewIntDatum(9)}
	leader.ParamSet = []bool{true, true}
	leader.ParamDirty = []bool{false, false}
	leader.OuterRows = []Row{{NewIntDatum(42)}}
	leader.GetSetting = func(string) (string, bool) { return "", false }

	arena := mctx.Acquire(nil, mctx.KindStmt)
	defer arena.Release()
	wctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWorkerContext(leader, arena, wctx)

	// Shared settings ride along.
	if w.WorkMem != 1234 || w.MaxParallelWorkersPerGather != 4 || w.MinParallelTableScanBlocks != 2048 {
		t.Error("shared settings did not reach the worker context")
	}

	// Params are COPIED BY VALUE, not cloned empty. An empty ParamExec would
	// make ExecParamRef raise XX000 for any Gather below a param-consuming
	// node — the failure this category exists to prevent.
	if len(w.ParamExec) != 2 || w.ParamExec[0].Int != 7 {
		t.Errorf("ParamExec must be copied by value, got %v", w.ParamExec)
	}
	if len(w.OuterRows) != 1 {
		t.Errorf("OuterRows must be copied by value, got %v", w.OuterRows)
	}
	// Copied, not aliased: SetParamExec grows these lazily, so a shared
	// backing array would race on append.
	w.ParamExec[0] = NewIntDatum(99)
	if leader.ParamExec[0].Int != 7 {
		t.Error("worker ParamExec aliases the leader's backing array")
	}

	// Per-statement mutable state starts empty and is NOT shared.
	if len(w.Notices) != 0 {
		t.Errorf("Notices must start empty in a worker, got %v", w.Notices)
	}
	if w.Mctx != arena {
		t.Error("worker must use the leader-allocated arena")
	}
	if w.Ctx != wctx {
		t.Error("worker must use the group cancellation context")
	}

	// Connection callbacks must be nil so a worker that reaches one panics at
	// the call site instead of mutating session state off-goroutine.
	if w.GetSetting != nil || w.SetSetting != nil || w.CancelBackend != nil ||
		w.QueueNotify != nil || w.PgClassRows != nil {
		t.Error("connection callbacks must be nil in a worker context")
	}
}

func TestMergeWorkerContextFoldsNotices(t *testing.T) {
	leader := NewContext()
	leader.Notices = []string{"a"}
	w := NewContext()
	w.Notices = []string{"b", "c"}
	w.Warnings = []string{"w"}
	MergeWorkerContext(leader, w)
	if strings.Join(leader.Notices, ",") != "a,b,c" {
		t.Errorf("notices = %v, want a,b,c in worker order", leader.Notices)
	}
	if len(leader.Warnings) != 1 {
		t.Errorf("warnings = %v", leader.Warnings)
	}
}

// ── failure, cancellation, panics ───────────────────────────────────────────

func TestParallelGroupFirstErrorWins(t *testing.T) {
	g := NewParallelGroup(context.Background())
	sentinel := errors.New("worker failed")
	g.Go(func(ctx context.Context) error { return sentinel })
	g.Go(func(ctx context.Context) error {
		<-ctx.Done() // must be cancelled by the sibling's failure
		return ctx.Err()
	})
	err := g.Wait()
	if err == nil {
		t.Fatal("Wait must return an error")
	}
	// Which error wins is non-deterministic when workers fail simultaneously;
	// here only one fails spontaneously, so it must be that one.
	if !errors.Is(err, sentinel) {
		t.Errorf("got %v, want the spontaneous failure", err)
	}
}

// TestParallelGroupRecoversPanic pins the difference between a failed query
// and a dead process. A panic in a goroutine the server did not start is
// fatal; serveConn's recover only covers the connection goroutine.
func TestParallelGroupRecoversPanic(t *testing.T) {
	g := NewParallelGroup(context.Background())
	g.Go(func(ctx context.Context) error { panic("boom") })
	err := g.Wait()
	if err == nil {
		t.Fatal("a worker panic must surface as an error, not kill the process")
	}
	var ee *ExecError
	if !errors.As(err, &ee) {
		// ExecError is returned by value in places; accept either shape.
		if !strings.Contains(err.Error(), "panic") {
			t.Fatalf("panic was not converted to an ExecError: %v (%T)", err, err)
		}
	} else if ee.Code != "XX000" {
		t.Errorf("panic SQLSTATE = %q, want XX000", ee.Code)
	}
}

func TestParallelGroupParentCancellationPropagates(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	g := NewParallelGroup(parent)

	started := make(chan struct{})
	g.Go(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	<-started
	cancelParent() // statement timeout / client EOF / user cancel
	if err := g.Wait(); err == nil {
		t.Error("parent cancellation must reach workers")
	}
}

// TestParallelGroupJoinsEveryWorker pins the lifecycle rule: worker lifetime is
// strictly nested inside the statement, because the statement arena is released
// by a defer in the dispatcher and cascades to worker arenas. A worker that
// outlives Wait would read freed memory.
func TestParallelGroupJoinsEveryWorker(t *testing.T) {
	before := runtime.NumGoroutine()
	g := NewParallelGroup(context.Background())
	var ran sync.WaitGroup
	for i := 0; i < 8; i++ {
		ran.Add(1)
		g.Go(func(ctx context.Context) error {
			defer ran.Done()
			<-ctx.Done()
			return nil
		})
	}
	// Workers here block until cancelled, so this exercises the documented
	// early-termination contract: Cancel first, then Wait. Wait alone would
	// hang — deliberately, since Wait no longer cancels (see its doc comment).
	g.Cancel()
	if err := g.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	ran.Wait()

	// Goroutine counts settle asynchronously; poll rather than assert once.
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before+2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if leaked := runtime.NumGoroutine() - before; leaked > 2 {
		t.Errorf("%d goroutines leaked after Wait", leaked)
	}
}

// TestParallelGroupConcurrentFailuresAreSafe is a race-detector target: many
// workers failing at once must not corrupt the error box or double-cancel.
func TestParallelGroupConcurrentFailuresAreSafe(t *testing.T) {
	g := NewParallelGroup(context.Background())
	for i := 0; i < 16; i++ {
		i := i
		g.Go(func(ctx context.Context) error {
			return fmt.Errorf("worker %d", i)
		})
	}
	if err := g.Wait(); err == nil {
		t.Error("expected one of the concurrent failures to surface")
	}
	// Deliberately NOT asserting WHICH error: with simultaneous failures that
	// is genuinely non-deterministic, and PG has the same property.
}
