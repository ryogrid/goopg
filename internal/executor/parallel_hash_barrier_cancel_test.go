package executor

// A Gather's group context is cancelled by failure, by an early Close, AND by
// ParallelGroup.Wait after every worker returned normally. Only the first two
// abandon a Parallel Hash build. These tests pin that difference at the build
// barrier (AI-20261115-011812-001).

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// A leader that reaches the barrier after the workers finished the whole query
// sees both the completed build and the group's end-of-life cancellation. Go's
// select picks among ready cases at random, so the completed build has to be
// checked first; the repetition makes the old coin flip impossible to pass.
func TestParallelHashWaitCompletedBuildWinsOverGroupEnd(t *testing.T) {
	groupDone := make(chan struct{})
	close(groupDone)
	ph := newParallelHashBuild(groupDone)
	if !ph.attach() {
		t.Fatal("the first participant must build")
	}
	ph.finish(&sharedHashBuild{intHash: map[int64][]Row{1: {{NewIntDatum(1)}}}}, nil, nil)
	if ph.attach() {
		t.Fatal("a participant arriving after completion must adopt, not build")
	}
	for i := 0; i < 1000; i++ {
		if err := ph.wait(nil); err != nil {
			t.Fatalf("wait %d on a completed build: %v", i, err)
		}
	}
}

// The other half of the contract: a build that cannot complete still releases
// its waiter when the group ends, or the leader would hang on a dead worker.
func TestParallelHashWaitIncompleteBuildIsAbandoned(t *testing.T) {
	groupDone := make(chan struct{})
	close(groupDone)
	ph := newParallelHashBuild(groupDone)
	ph.attach()
	ph.attach()
	ph.finish(&sharedHashBuild{}, nil, nil)
	if err := ph.wait(nil); !errors.Is(err, errParallelHashAbandoned) {
		t.Fatalf("wait on an incomplete build of a finished group = %v, want the abandoned error", err)
	}
}

// With no worker launched the channel closer's Wait cancels the group at once,
// before the leader has built anything, so every zero-worker Parallel Hash
// join reaches the barrier with the group already ended.
func TestParallelHashZeroWorkersRunsInLeader(t *testing.T) {
	ctx, cleanup := pqJoinFixture(t)
	defer cleanup()
	savedMax, savedLeader := ctx.MaxParallelWorkers, ctx.ParallelLeaderParticipation
	defer func() { ctx.MaxParallelWorkers, ctx.ParallelLeaderParticipation = savedMax, savedLeader }()

	sql := c19fCorpus()[0]
	want := c19fRun(t, ctx, c19fSerialPlan(t, ctx, sql))

	advanceStmtCounter(ctx)
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ps := c19fSettings()
	ps.EnableParallelHash = true
	plan, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, ps)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	gather := c19fFindGather(plan)
	if gather == nil {
		t.Fatal("the path model chose no Gather; the zero-worker case was not exercised")
	}
	if j := c19fFindJoin(gather.Child); j == nil || !j.ParallelHash {
		t.Fatalf("the gathered join is not a Parallel Hash (%+v)", j)
	}

	ctx.MaxParallelWorkers = 0
	ctx.ParallelLeaderParticipation = false // the leader runs anyway when n == 0
	for i := 0; i < 64; i++ {
		got, err := runSortedRows(ctx, plan)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("run %d returned %d rows, serial %d (or rows differ)", i, len(got), len(want))
		}
	}
}

// runSortedRows executes plan under ctx's current parallel settings.
func runSortedRows(ctx *Context, plan optimizer.Node) ([]string, error) {
	op, err := Build(plan)
	if err != nil {
		return nil, err
	}
	err = op.Open(ctx)
	var out []string
	for err == nil {
		var slot TupleSlot
		slot, err = op.Next()
		if err == nil {
			out = append(out, renderRows([]Row{slot.Row()})...)
		}
	}
	cerr := op.Close()
	if err != EOF {
		return nil, err
	}
	if cerr != nil {
		return nil, cerr
	}
	sort.Strings(out)
	return out, nil
}

// The abandoned error only stops a waiter. When a worker's failure caused it,
// the Gather must report that failure.
func TestParallelLeaderOpenErrorReportsWorkerFailure(t *testing.T) {
	errWorker := errors.New("a worker's scan failed")
	g := NewParallelGroup(nil)
	g.Go(func(context.Context) error { return errWorker })
	if err := parallelLeaderOpenError(g, errParallelHashAbandoned); err != errWorker {
		t.Fatalf("leader open error = %v, want the worker's failure", err)
	}
	if err := g.Wait(); err != errWorker {
		t.Fatalf("group error = %v", err)
	}

	errLeader := errors.New("the leader's own failure")
	g = NewParallelGroup(nil)
	if err := parallelLeaderOpenError(g, errLeader); err != errLeader {
		t.Fatalf("an unrelated leader error must pass through; got %v", err)
	}
	_ = g.Wait()
}
