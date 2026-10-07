package executor

// M0146-0002 slice 1: the Parallel Hash execution model, pinned by
// parallel-vs-serial IDENTITY — the only instrument that sees a partial build
// (the 2026-09-21 parallel SEMI lesson: the values stay plausible while rows go
// missing). The planner does not file `parallel_hash = true` yet (slice 2), so
// each parallel plan is the serial plan with its hash join marked ParallelHash,
// both scans stamped partial, and a Gather on top.

import (
	"fmt"
	"sort"
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

// TestParallelHashSpilledBuildMatchesSerial pins M0146-0090: a Parallel Hash
// build whose shares outgrow hash_mem batches (the participants' shares are
// merged under one batch count, mergeSpilledParts) and returns exactly the
// serial rows. Before, the query failed. A join that fills its build side
// still refuses a spilled share — it must fail loudly, never return a batch's
// unmatched rows from one participant's view.
func TestParallelHashSpilledBuildMatchesSerial(t *testing.T) {
	ctx, cleanup := parallelHashFixture(t)
	defer cleanup()
	buildKeys := runSQL(t, ctx, "SELECT count(*) FROM ph_build WHERE k IS NOT NULL")
	wantBuildRows := renderRows(buildKeys)[0]

	for _, tc := range []struct {
		sql       string
		mayRefuse bool
	}{
		{"SELECT p.w, b.v FROM ph_probe p JOIN ph_build b ON p.k = b.k", false},
		{"SELECT p.w, b.v FROM ph_probe p LEFT JOIN ph_build b ON p.k = b.k", false},
		{"SELECT p.w FROM ph_probe p WHERE EXISTS (SELECT 1 FROM ph_build b WHERE b.k = p.k)", false},
		{"SELECT p.w FROM ph_probe p WHERE NOT EXISTS (SELECT 1 FROM ph_build b WHERE b.k = p.k)", false},
		{"SELECT p.w FROM ph_probe p WHERE p.k NOT IN (SELECT b.k FROM ph_build b)", false},
		// RIGHT and FULL: the inner join's plan retyped, as in
		// TestParallelHashFillBuildIdentityWithSerial — ph_build is hashed and
		// the join fills it, so a spilled share must be refused.
		{"right", true},
		{"full", true},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			sql, retype := tc.sql, optimizer.JoinType(0)
			switch tc.sql {
			case "right":
				sql, retype = "SELECT p.w, b.v FROM ph_probe p JOIN ph_build b ON p.k = b.k", optimizer.JoinTypeRight
			case "full":
				sql, retype = "SELECT p.w, b.v FROM ph_probe p JOIN ph_build b ON p.k = b.k", optimizer.JoinTypeFull
			}
			plan := func() optimizer.Node {
				n := planHashOnly(t, ctx, sql)
				if retype != 0 {
					c19fFindJoin(n).Type = retype
				}
				return n
			}
			want := c19fRun(t, ctx, plan())
			par, _ := toParallelHash(t, plan())
			if par == nil {
				t.Skip("the serial plan has no hash join; nothing to parallelise")
			}
			var ph *parallelHashBuild
			var once sync.Once
			ctx.parallelHashObserver = func(got *parallelHashBuild) { once.Do(func() { ph = got }) }
			saved := ctx.WorkMem
			ctx.WorkMem = 16 << 10
			defer func() { ctx.WorkMem = saved; ctx.parallelHashObserver = nil }()

			got, err := c19fTryRun(ctx, par)
			if tc.mayRefuse {
				if err == nil || !strings.Contains(err.Error(), "fills its build side") {
					t.Fatalf("a spilled share of a build-filling join must be refused; got err=%v (%d rows, serial %d)", err, len(got), len(want))
				}
				return
			}
			if err != nil {
				t.Fatalf("spilled Parallel Hash failed: %v", err)
			}
			if len(got) != len(want) {
				t.Fatalf("parallel hash returned %d rows, serial %d", len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("row %d differs: parallel %q, serial %q", i, got[i], want[i])
				}
			}
			if ph == nil {
				t.Fatal("no participant reached the Parallel Hash build state")
			}
			if ph.table.batches == nil || ph.table.batches.nbatch < 2 {
				t.Fatal("the build did not batch; lower the fixture's work_mem — this test proved nothing")
			}
			if got := ph.rowsPublished; renderRows([]Row{{NewIntDatum(int64(got))}})[0] != wantBuildRows {
				t.Fatalf("participants published %d build rows, the inner holds %s non-NULL keys", got, wantBuildRows)
			}
			t.Logf("builders=%d rowsPublished=%d nbatch=%d", ph.builders, ph.rowsPublished, ph.table.batches.nbatch)
		})
	}
	if left := ctx.ReleaseSpillFiles(); left != 0 {
		t.Fatalf("%d spill files were still registered after every Gather closed", left)
	}
}

// c19fTryRun is c19fRun returning the execution error instead of failing.
func c19fTryRun(ctx *Context, node optimizer.Node) ([]string, error) {
	ctx.MaxParallelWorkers = 8
	ctx.ParallelLeaderParticipation = true
	op, err := Build(node)
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

// TestParallelHashBarrier pins the barrier protocol itself: no waiter returns
// before the last attached participant finished, a late participant builds
// nothing, and one failure reaches every waiter.
func TestParallelHashBarrier(t *testing.T) {
	ph := newParallelHashBuild(nil)
	if !ph.attach() || !ph.attach() {
		t.Fatal("participants attaching before completion must build")
	}
	ph.finish(&sharedHashBuild{intHash: map[int64][]Row{1: {{NewIntDatum(1)}}}}, nil, nil)
	select {
	case <-ph.done:
		t.Fatal("the barrier released with one of two participants still building")
	default:
	}
	ph.finish(&sharedHashBuild{intHash: map[int64][]Row{1: {{NewIntDatum(2)}}, 2: {{NewIntDatum(3)}}}}, nil, nil)
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
	failed.finish(nil, nil, errParallelHashSpilled)
	failed.finish(&sharedHashBuild{}, nil, nil)
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

// TestParallelHashFillBuildIdentityWithSerial (M0146-0005dj slice 3): the
// joins that emit unmatched BUILD rows — RIGHT, FULL, RIGHT ANTI — run as a
// Parallel Hash only because the participants merge their match bits and the
// last one to finish probing sweeps the shared table once. A participant that
// swept with only its own bits would emit build rows another participant
// matched (duplicates against serial); two sweepers would emit every
// unmatched row twice. Parallel-vs-serial identity sees both.
func TestParallelHashFillBuildIdentityWithSerial(t *testing.T) {
	ctx, cleanup := parallelHashFixture(t)
	defer cleanup()
	// RIGHT ANTI is the planner's own election (M0146-0005dj slice 2): a
	// distinct-keyed probe table much larger than the anti join's LHS.
	runSQL(t, ctx, "CREATE TABLE ph_big (k int, w int)")
	runSQL(t, ctx, "INSERT INTO ph_big SELECT g, g FROM generate_series(1, 200000) g")
	runSQL(t, ctx, "ANALYZE ph_big")
	runSQL(t, ctx, "ANALYZE ph_build")
	runSQL(t, ctx, "ANALYZE ph_probe")
	// RIGHT and FULL reuse the inner join's hash plan with the join retyped:
	// the column layout is the same, ph_probe probes and ph_build is hashed,
	// and the serial executor's RIGHT/FULL hash path is the reference.
	const inner = "SELECT p.w, b.v FROM ph_probe p JOIN ph_build b ON p.k = b.k"
	for _, c := range []struct {
		name   string
		sql    string
		retype optimizer.JoinType
		typ    optimizer.JoinType
	}{
		{"right", inner, optimizer.JoinTypeRight, optimizer.JoinTypeRight},
		{"full", inner, optimizer.JoinTypeFull, optimizer.JoinTypeFull},
		{"right anti", "SELECT b.v FROM ph_build b WHERE NOT EXISTS (SELECT 1 FROM ph_big p WHERE p.k = b.k AND p.w > 1000)",
			0, optimizer.JoinTypeRightAnti},
	} {
		t.Run(c.name, func(t *testing.T) {
			plan := func() optimizer.Node {
				n := planHashOnly(t, ctx, c.sql)
				if c.retype != 0 {
					c19fFindJoin(n).Type = c.retype
				}
				return n
			}
			want := c19fRun(t, ctx, plan())
			par, j := toParallelHash(t, plan())
			if par == nil {
				t.Fatal("the serial plan has no hash join")
			}
			if j.Type != c.typ {
				t.Fatalf("planned a %v hash join, want %v", j.Type, c.typ)
			}
			if s := c19fScanOf(j.Right); s == nil || s.Table == nil || s.Table.Name != "ph_build" {
				t.Fatalf("the build side is %T, not ph_build", j.Right)
			}
			var ph *parallelHashBuild
			var once sync.Once
			ctx.parallelHashObserver = func(got *parallelHashBuild) { once.Do(func() { ph = got }) }
			defer func() { ctx.parallelHashObserver = nil }()

			got := c19fRun(t, ctx, par)
			if len(got) != len(want) {
				t.Fatalf("parallel hash returned %d rows, serial %d", len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("row %d differs: parallel %q, serial %q", i, got[i], want[i])
				}
			}
			if ph == nil {
				t.Fatal("no participant reached the Parallel Hash build state")
			}
			if !ph.sweepClaimed || ph.probers != 0 {
				t.Fatalf("sweepClaimed=%v probers=%d: the shared sweep did not run exactly once", ph.sweepClaimed, ph.probers)
			}
			t.Logf("builders=%d rows=%d", ph.builders, len(got))
		})
	}
}

// TestParallelHashProbeDetachMerges pins the detach protocol: the bits of every
// prober reach the one that sweeps, the sweep is claimed once, and a
// participant arriving after the claim does not probe.
func TestParallelHashProbeDetachMerges(t *testing.T) {
	ph := newParallelHashBuild(nil)
	if !ph.probeAttach() || !ph.probeAttach() {
		t.Fatal("attach refused before any detach")
	}
	_, _, last := ph.probeDetach(map[string][]bool{"a": {true, false}}, map[int64][]bool{7: {false, true}})
	if last {
		t.Fatal("the first of two probers claimed the sweep")
	}
	ms, mi, last := ph.probeDetach(map[string][]bool{"a": {false, true}, "b": {true}}, nil)
	if !last {
		t.Fatal("the last prober did not claim the sweep")
	}
	if fmt.Sprint(ms["a"], ms["b"], mi[7]) != "[true true] [true] [false true]" {
		t.Fatalf("merged bits %v %v %v", ms["a"], ms["b"], mi[7])
	}
	if ph.probeAttach() {
		t.Fatal("a participant attached after the sweep was claimed")
	}
}
