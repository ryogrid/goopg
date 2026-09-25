package executor

// M0146-0002 slice 1: the Parallel Hash execution model, pinned by
// parallel-vs-serial IDENTITY — the only instrument that sees a partial build
// (the 2026-09-21 parallel SEMI lesson: the values stay plausible while rows go
// missing). The planner does not file `parallel_hash = true` yet (slice 2), so
// each parallel plan is the serial plan with its hash join marked ParallelHash,
// both scans stamped partial, and a Gather on top.

import (
	"strings"
	"sync"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// parallelHashFixture: a multi-block build relation (so several participants
// can claim a share of it) and a larger probe relation, with NULL keys on both
// sides for the NOT IN / anti arms.
func parallelHashFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, cleanup := newVMFixture(t)
	runSQL(t, ctx, "CREATE TABLE ph_build (k int, v int)")
	runSQL(t, ctx, "INSERT INTO ph_build SELECT CASE WHEN g % 97 = 0 THEN NULL ELSE g % 1500 END, g FROM generate_series(1, 6000) g")
	runSQL(t, ctx, "CREATE TABLE ph_probe (k int, w int)")
	runSQL(t, ctx, "INSERT INTO ph_probe SELECT CASE WHEN g % 89 = 0 THEN NULL ELSE g % 2000 END, g FROM generate_series(1, 20000) g")
	return ctx, cleanup
}

// planHashOnly plans sql with hash joins as the only join method, serially.
func planHashOnly(t *testing.T, ctx *Context, sql string) optimizer.Node {
	t.Helper()
	advanceStmtCounter(ctx)
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableNestLoop = false
	ps.EnableMergeJoin = false
	ps.MaxParallelWorkersPerGather = 0
	node, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, ps)
	if err != nil {
		t.Fatalf("plan %q: %v", sql, err)
	}
	return node
}

// toParallelHash rewrites a serial plan into its Parallel Hash twin and
// returns the join it marked, or nil when the plan has no hash join.
func toParallelHash(t *testing.T, root optimizer.Node) (optimizer.Node, *optimizer.Join) {
	t.Helper()
	j := c19fFindJoin(root)
	if j == nil || j.Algo != optimizer.JoinAlgoHash {
		return nil, nil
	}
	j.ParallelHash = true
	for _, side := range []optimizer.Node{j.Left, j.Right} {
		if s := c19fScanOf(side); s != nil {
			s.Parallel = true
		}
	}
	return optimizer.NewGather(0, root, 4), j
}

func TestParallelHashIdentityWithSerial(t *testing.T) {
	ctx, cleanup := parallelHashFixture(t)
	defer cleanup()
	buildKeys := runSQL(t, ctx, "SELECT count(*) FROM ph_build WHERE k IS NOT NULL")
	wantBuildRows := renderRows(buildKeys)[0]

	for _, sql := range []string{
		"SELECT p.w, b.v FROM ph_probe p JOIN ph_build b ON p.k = b.k",
		"SELECT p.w, b.v FROM ph_probe p LEFT JOIN ph_build b ON p.k = b.k",
		"SELECT p.w FROM ph_probe p WHERE EXISTS (SELECT 1 FROM ph_build b WHERE b.k = p.k)",
		"SELECT p.w FROM ph_probe p WHERE NOT EXISTS (SELECT 1 FROM ph_build b WHERE b.k = p.k)",
		"SELECT p.w FROM ph_probe p WHERE p.k NOT IN (SELECT b.k FROM ph_build b)",
	} {
		t.Run(sql, func(t *testing.T) {
			want := c19fRun(t, ctx, planHashOnly(t, ctx, sql))
			par, j := toParallelHash(t, planHashOnly(t, ctx, sql))
			if par == nil {
				t.Skip("the serial plan has no hash join; nothing to parallelise")
			}
			// The build side must be ph_build (the multi-block relation), or
			// the partial-build claim below measures nothing.
			build := j.Right
			if !probeSideIsLeft(j) {
				build = j.Left
			}
			if s := c19fScanOf(build); s == nil || s.Table == nil || s.Table.Name != "ph_build" {
				t.Skipf("the planner built over %T, not ph_build", build)
			}

			var ph *parallelHashBuild
			var once sync.Once
			ctx.parallelHashObserver = func(got *parallelHashBuild) { once.Do(func() { ph = got }) }
			defer func() { ctx.parallelHashObserver = nil }()

			got := c19fRun(t, ctx, par)
			if len(got) != len(want) {
				t.Fatalf("parallel hash returned %d rows, serial %d — a probe ran against a partial build", len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("row %d differs: parallel %q, serial %q", i, got[i], want[i])
				}
			}
			if ph == nil {
				t.Fatal("no participant reached the Parallel Hash build state; the join ran as an ordinary hash join and this test proved nothing")
			}
			// Every build row published exactly once, across however many
			// participants claimed a share (NULL keys are never inserted).
			if got := ph.rowsPublished; renderRows([]Row{{NewIntDatum(int64(got))}})[0] != wantBuildRows {
				t.Fatalf("participants published %d build rows, the inner holds %s non-NULL keys", got, wantBuildRows)
			}
			t.Logf("builders=%d rowsPublished=%d", ph.builders, ph.rowsPublished)
		})
	}
}

// TestParallelHashSpillFailsLoudly: a participant whose share exceeds hash_mem
// must fail the query, never publish its batch 0 alone.
func TestParallelHashSpillFailsLoudly(t *testing.T) {
	ctx, cleanup := parallelHashFixture(t)
	defer cleanup()
	sql := "SELECT p.w, b.v FROM ph_probe p JOIN ph_build b ON p.k = b.k"
	par, _ := toParallelHash(t, planHashOnly(t, ctx, sql))
	if par == nil {
		t.Skip("no hash join")
	}
	saved := ctx.WorkMem
	ctx.WorkMem = 8 << 10
	defer func() { ctx.WorkMem = saved }()
	ctx.MaxParallelWorkers = 8
	ctx.ParallelLeaderParticipation = true
	op, err := Build(par)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	err = op.Open(ctx)
	for err == nil {
		_, err = op.Next()
	}
	_ = op.Close()
	if err == EOF {
		t.Fatal("a spilling Parallel Hash build returned rows; it must fail")
	}
	if !strings.Contains(err.Error(), "parallel hash") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestParallelHashBarrier pins the barrier protocol itself: no waiter returns
// before the last attached participant finished, a late participant builds
// nothing, and one failure reaches every waiter.
func TestParallelHashBarrier(t *testing.T) {
	ph := newParallelHashBuild(nil)
	if !ph.attach() || !ph.attach() {
		t.Fatal("participants attaching before completion must build")
	}
	ph.finish(&sharedHashBuild{intHash: map[int64][]Row{1: {{NewIntDatum(1)}}}}, nil)
	select {
	case <-ph.done:
		t.Fatal("the barrier released with one of two participants still building")
	default:
	}
	ph.finish(&sharedHashBuild{intHash: map[int64][]Row{1: {{NewIntDatum(2)}}, 2: {{NewIntDatum(3)}}}}, nil)
	if err := ph.wait(nil); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if ph.attach() {
		t.Fatal("a participant arriving after completion must adopt, not build")
	}
	if n := ph.table.rowCount(); n != 3 || len(ph.table.intHash[1]) != 2 {
		t.Fatalf("merged table has %d rows (key 1: %d); want 3 (2)", n, len(ph.table.intHash[1]))
	}

	failed := newParallelHashBuild(nil)
	failed.attach()
	failed.attach()
	failed.finish(nil, errParallelHashSpilled)
	failed.finish(&sharedHashBuild{}, nil)
	if err := failed.wait(nil); err != errParallelHashSpilled {
		t.Fatalf("a participant's failure must reach every waiter; got %v", err)
	}
}

// TestParallelHashPathModelWinner is slice 2's consumer check: on plans the
// PATH MODEL chose with enable_parallel_hash on, the gathered hash join is the
// `parallel_hash = true` variant — both scans stamped partial, labelled
// `Parallel Hash Join` — and it returns exactly the serial rows. "An unwinnable
// path is an untested path": the planner arm is only proven once a plan it
// won has executed.
func TestParallelHashPathModelWinner(t *testing.T) {
	ctx, cleanup := pqJoinFixture(t)
	defer cleanup()

	chosen := 0
	for _, sql := range c19fCorpus() {
		t.Run(sql, func(t *testing.T) {
			restoreOff := optimizer.SetGatherPathsMode("off")
			serialPlan := c19fPlan(t, ctx, sql)
			restoreOff()

			restoreAll := optimizer.SetGatherPathsMode("all")
			advanceStmtCounter(ctx)
			stmts, err := parser.Parse(sql)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			ps := c19fSettings()
			ps.EnableParallelHash = true
			parPlan, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, ps)
			restoreAll()
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			gather := c19fFindGather(parPlan)
			if gather == nil {
				t.Skip("the path model chose no Gather for this shape")
			}
			j := c19fFindJoin(gather.Child)
			if j == nil || j.Algo != optimizer.JoinAlgoHash || !j.ParallelHash {
				t.Skipf("the gathered join is not a Parallel Hash (%+v)", j)
			}
			chosen++
			for side, n := range map[string]optimizer.Node{"left": j.Left, "right": j.Right} {
				if s := c19fScanOf(n); s == nil || !s.Parallel {
					t.Errorf("the %s scan of a Parallel Hash join is not stamped Parallel; both sides are partial", side)
				}
			}
			if got := describePlan(j, nil); !strings.HasPrefix(got, "Parallel Hash") {
				t.Errorf("EXPLAIN label = %q, want the Parallel Hash Join label", got)
			}
			want := c19fRun(t, ctx, serialPlan)
			got := c19fRun(t, ctx, parPlan)
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("Parallel Hash plan returned %d rows, serial %d (or rows differ)", len(got), len(want))
			}
		})
	}
	if chosen == 0 {
		t.Fatal("no shape produced a planner-chosen Parallel Hash; the arm was never exercised")
	}
}
