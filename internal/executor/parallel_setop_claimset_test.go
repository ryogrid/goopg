package executor

// M0140-0006c: the executor claim-set for a partial *setOp under Gather.
//
// gatherOp has each worker build its own FULL copy of the child subtree —
// safe only because parallel_scan.go's attachAll hands each worker a
// DISJOINT claim over the driving scan. Before this task, *setOp had no arm
// in attachAll at all: every worker would replay BOTH entire UNION ALL
// branches, and the Gather would return (workers+1) copies of every row —
// the exact defect TestParallelIdentityWithRealGather's header describes for
// a bare scan, reproduced here for the two-branch shape.
//
// Two DIFFERENT tables (deliberately different row COUNTS) drive the two
// branches, so a claim-state bug that accidentally shares one
// parallelScanState between them — letting one branch's block-count
// boundary leak into the other's (parallelScanState.initOnce publishes
// exactly once) — would show up as a wrong row count or a wrong value, not
// just as a slow query.

import (
	"fmt"
	"sort"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// parallelSetOpFixture builds two tables of DIFFERENT sizes so the two
// branches' claim state cannot be silently shared without a row count or
// value tripping.
func parallelSetOpFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	if err := runDDL(t, ctx, "CREATE TABLE pq_setop_a (id int)"); err != nil {
		cleanup()
		t.Fatalf("create a: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE pq_setop_b (id int)"); err != nil {
		cleanup()
		t.Fatalf("create b: %v", err)
	}
	for i := 0; i < 260; i++ {
		if err := runDDL(t, ctx, fmt.Sprintf("INSERT INTO pq_setop_a VALUES (%d)", i)); err != nil {
			cleanup()
			t.Fatalf("insert a %d: %v", i, err)
		}
	}
	for i := 0; i < 90; i++ {
		if err := runDDL(t, ctx, fmt.Sprintf("INSERT INTO pq_setop_b VALUES (%d)", 100000+i)); err != nil {
			cleanup()
			t.Fatalf("insert b %d: %v", i, err)
		}
	}
	// M0140-0006c-2: the build side for the join-driven branch. 40 keys
	// matching pq_setop_a ids 0..39 exactly once each, so the join branch
	// yields a deterministic 40 rows — disjoint from both pq_setop_b's
	// range and the unmatched tail of pq_setop_a.
	if err := runDDL(t, ctx, "CREATE TABLE pq_setop_c (aid int)"); err != nil {
		cleanup()
		t.Fatalf("create c: %v", err)
	}
	for i := 0; i < 40; i++ {
		if err := runDDL(t, ctx, fmt.Sprintf("INSERT INTO pq_setop_c VALUES (%d)", i)); err != nil {
			cleanup()
			t.Fatalf("insert c %d: %v", i, err)
		}
	}
	return ctx, cleanup
}

// planSetOpTestNode builds a bare optimizer.SetOp{UNION ALL} over the two
// fixture tables' plans, so the test drives the executor directly without
// depending on the optimizer ever CHOOSING a partial SetOp path itself
// (M0140-0006b-2's reachability gap means it cannot yet).
func planSetOpTestNode(t *testing.T, ctx *Context) *optimizer.SetOp {
	t.Helper()
	left := planForTest(t, ctx, "SELECT id FROM pq_setop_a")
	right := planForTest(t, ctx, "SELECT id FROM pq_setop_b")
	return &optimizer.SetOp{Left: left, Right: right, Op: parser.SetOpUnion, All: true}
}

// TestGatherOverSetOpIdentity is the M0140-0006c gate: every row from BOTH
// branches exactly once, over a Gather whose partial subtree is a streaming
// UNION ALL SetOp.
//
// Before the claim-set fix this either panicked (no driving scan attached,
// each worker fully replaying both branches) or returned (workers+1)x the
// rows, depending on the shape — the N-copies defect this file's header
// describes.
func TestGatherOverSetOpIdentity(t *testing.T) {
	ctx, cleanup := parallelSetOpFixture(t)
	defer cleanup()

	const wantTotal = 260 + 90

	for _, workers := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			advanceStmtCounter(ctx)
			so := planSetOpTestNode(t, ctx)
			gathered := optimizer.NewGather(0, so, workers)

			ctx.MaxParallelWorkers = 8
			ctx.ParallelLeaderParticipation = true
			op, err := Build(gathered)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if err := op.Open(ctx); err != nil {
				t.Fatalf("open: %v", err)
			}
			seen := map[string]int{}
			n := 0
			for {
				slot, err := op.Next()
				if err == EOF {
					break
				}
				if err != nil {
					t.Fatalf("next: %v", err)
				}
				seen[datumTestString(slot.Row()[0])]++
				n++
			}
			if err := op.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}

			if n != wantTotal {
				t.Fatalf("got %d rows, want %d — a multiple means a worker replayed a "+
					"whole branch instead of its partition (the pre-fix N-copies defect)",
					n, wantTotal)
			}
			for v, c := range seen {
				if c != 1 {
					t.Fatalf("row %s returned %d times; each branch's claim set must hand "+
						"each block to exactly one worker", v, c)
				}
			}
			// Sanity: both branches' id ranges are actually represented, so a bug
			// that dropped one branch entirely (rather than duplicating) would
			// also be caught.
			if _, ok := seen[datumTestString(NewIntDatum(5))]; !ok {
				t.Errorf("missing a row from pq_setop_a (id=5); left branch was not read")
			}
			if _, ok := seen[datumTestString(NewIntDatum(100005))]; !ok {
				t.Errorf("missing a row from pq_setop_b (id=100005); right branch was not read")
			}
		})
	}
}

// planHashForced plans sql with merge and nested-loop joins disabled, so the
// planner must shape equi-joins as hash joins — the mirror of planMergeForced
// (parallel_merge_join_identity_test.go). DefaultPlannerSettings plus two
// flips; everything else identical to production planning.
func planHashForced(t *testing.T, ctx *Context, sql string) optimizer.Node {
	t.Helper()
	advanceStmtCounter(ctx)
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableMergeJoin = false
	ps.EnableNestLoop = false
	node, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, ps)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !planTreeHasHashJoin(node) {
		t.Fatalf("forced plan for %q contains no Hash Join; the identity comparison would not exercise the join branch", sql)
	}
	return node
}

func planTreeHasHashJoin(n optimizer.Node) bool {
	return findHashJoin(n) != nil
}

// findHashJoin returns the first hash Join in the tree, or nil.
func findHashJoin(n optimizer.Node) *optimizer.Join {
	var found *optimizer.Join
	var walk func(optimizer.Node)
	walk = func(cur optimizer.Node) {
		if cur == nil || found != nil {
			return
		}
		if j, ok := cur.(*optimizer.Join); ok && j.Algo == optimizer.JoinAlgoHash {
			found = j
			return
		}
		for _, c := range optimizer.ParallelChildrenForTest(cur) {
			walk(c)
		}
	}
	walk(n)
	return found
}

// planTreeHasHashJoinUnderGather reports whether a Hash Join node sits inside
// a Gather subtree — through the SetOp if needed (ParallelChildrenForTest
// descends into SetOp branches since M0140-0006c-2). Identity against a plan
// whose Gather sits BELOW the join would pass while never partitioning the
// probe — the exact vacuity this gate must not have.
func planTreeHasHashJoinUnderGather(n optimizer.Node) bool {
	found := false
	var walk func(optimizer.Node, bool)
	walk = func(cur optimizer.Node, underGather bool) {
		if cur == nil || found {
			return
		}
		switch cur.(type) {
		case *optimizer.Gather, *optimizer.GatherMerge:
			underGather = true
		}
		if j, ok := cur.(*optimizer.Join); ok && j.Algo == optimizer.JoinAlgoHash && underGather {
			found = true
			return
		}
		for _, c := range optimizer.ParallelChildrenForTest(cur) {
			walk(c, underGather)
		}
	}
	walk(n, false)
	return found
}

// TestGatherOverSetOpHashJoinBranchIdentity is M0140-0006c-2's gate for the
// first widened branch kind: the left UNION ALL branch is a hash join (probe
// over pq_setop_a, build over pq_setop_c — 40 matching keys, so the branch
// yields a deterministic 40 rows), the right a bare scan over pq_setop_b
// (90 rows).
//
// Two properties, each with its own witness:
//   - row identity (serial vs parallel at workers 1/2/4): the probe
//     partition attaches on the probe side, so no worker replays a whole
//     branch (the N-copies defect returns MORE rows);
//   - build-once sharing: the leader prebuilds the branch join's build side
//     and publishes it for every participant (lookupSharedHashBuild hits on
//     the branch join after Open). Without the M0140-0006c-2 collector
//     descent the prebuild collects nothing, publishes nothing, and every
//     worker rebuilds the table privately — correct rows, N+1x the build
//     work and memory, which is what the widened admission would otherwise
//     silently buy on every newly-admitted plan.
func TestGatherOverSetOpHashJoinBranchIdentity(t *testing.T) {
	ctx, cleanup := parallelSetOpFixture(t)
	defer cleanup()

	ctx.MaxParallelWorkers = 8
	ctx.ParallelLeaderParticipation = true

	for _, workers := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			joinBranch := planHashForced(t, ctx, "SELECT a.id FROM pq_setop_a a JOIN pq_setop_c c ON a.id = c.aid")
			advanceStmtCounter(ctx)
			right := planForTest(t, ctx, "SELECT id FROM pq_setop_b")
			so := &optimizer.SetOp{Left: joinBranch, Right: right, Op: parser.SetOpUnion, All: true}

			advanceStmtCounter(ctx)
			want := drainPlan(t, ctx, so)
			if len(want) != 40+90 {
				t.Fatalf("serial baseline = %d rows, want 130 (40 join + 90 scan); the fixture planner shape moved", len(want))
			}

			gathered := optimizer.NewGather(0, so, workers)
			if !planTreeHasHashJoinUnderGather(gathered) {
				t.Fatal("no Hash Join under the Gather; the identity comparison would not exercise the join branch")
			}
			branchJoin := findHashJoin(joinBranch)
			if branchJoin == nil {
				t.Fatal("no Hash Join in the branch plan; nothing to prebuild")
			}
			if !optimizer.HasShareableHashJoin(so) {
				t.Fatal("HasShareableHashJoin misses the join under the SetOp; the build side would never be prebuilt")
			}
			advanceStmtCounter(ctx)
			op, err := Build(gathered)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if err := op.Open(ctx); err != nil {
				t.Fatalf("open: %v", err)
			}
			if lookupSharedHashBuild(ctx, branchJoin) == nil {
				op.Close()
				t.Fatal("leader published no shared build for the join under the SetOp branch; every worker rebuilds the table privately (the collector descent is what prevents this)")
			}
			var got []string
			for {
				slot, err := op.Next()
				if err == EOF {
					break
				}
				if err != nil {
					op.Close()
					t.Fatalf("next: %v", err)
				}
				got = append(got, renderRows([]Row{slot.Row()})...)
			}
			if err := op.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}

			sort.Strings(want)
			sort.Strings(got)
			if len(got) != len(want) {
				t.Fatalf("got %d rows, want %d — more means a worker replayed a whole branch instead of its partition (the N-copies defect)",
					len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("row %d differs: got %q want %q — a worker dropped or duplicated a branch row", i, got[i], want[i])
				}
			}
		})
	}
}

// planTreeHasNestedLoopUnderGather is the Nested Loop twin of
// planTreeHasHashJoinUnderGather: the identity comparison is only
// meaningful if an NL Join node actually sits inside the Gather subtree —
// through the SetOp if needed (ParallelChildrenForTest descends into SetOp
// branches since M0140-0006c-2).
func planTreeHasNestedLoopUnderGather(n optimizer.Node) bool {
	found := false
	var walk func(optimizer.Node, bool)
	walk = func(cur optimizer.Node, underGather bool) {
		if cur == nil || found {
			return
		}
		switch cur.(type) {
		case *optimizer.Gather, *optimizer.GatherMerge:
			underGather = true
		}
		if j, ok := cur.(*optimizer.Join); ok && j.Algo == optimizer.JoinAlgoNestedLoop && underGather {
			found = true
			return
		}
		for _, c := range optimizer.ParallelChildrenForTest(cur) {
			walk(c, underGather)
		}
	}
	walk(n, false)
	return found
}

// TestGatherOverSetOpNestLoopBranchIdentity is M0140-0006c-2 slice A's gate:
// the left UNION ALL branch is a nested loop (outer = a 3-row filtered
// scan of pq_setop_a, whole inner = pq_setop_c — a deterministic 120 rows),
// the right a bare scan over pq_setop_b (90 rows).
//
// Serial-vs-parallel identity at 1/2/4 workers is the witness: each
// participant claims a disjoint partition of the NL's OUTER through the
// branch's own leaf claim set and materialises the whole inner itself
// (attachParallelScan's JoinAlgoNestedLoop arm — unchanged by this slice,
// already correct under any subtree). A worker replaying the whole branch
// (the N-copies defect) returns MORE rows; a claim that never attaches
// silently multiplies every branch row by the participant count.
func TestGatherOverSetOpNestLoopBranchIdentity(t *testing.T) {
	ctx, cleanup := parallelSetOpFixture(t)
	defer cleanup()

	ctx.MaxParallelWorkers = 8
	ctx.ParallelLeaderParticipation = true

	for _, workers := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			advanceStmtCounter(ctx)
			// Cross join guarantees the NL shape — no equi key exists for
			// a hash/merge arm to fire on (parallel_nl_join_test.go's own
			// corpus uses comma joins for the same reason). a.id < 3 keeps
			// the branch small and deterministic: 3 outer x 40 inner.
			nlBranch := nlPlanForTest(t, ctx, "SELECT a.id FROM pq_setop_a a, pq_setop_c c WHERE a.id < 3")
			advanceStmtCounter(ctx)
			right := planForTest(t, ctx, "SELECT id FROM pq_setop_b")
			so := &optimizer.SetOp{Left: nlBranch, Right: right, Op: parser.SetOpUnion, All: true}

			advanceStmtCounter(ctx)
			want := drainPlan(t, ctx, so)
			if len(want) != 120+90 {
				t.Fatalf("serial baseline = %d rows, want 210 (120 join + 90 scan); the fixture planner shape moved", len(want))
			}

			gathered := optimizer.NewGather(0, so, workers)
			if !planTreeHasNestedLoopUnderGather(gathered) {
				t.Fatal("no Nested Loop under the Gather; the identity comparison would not exercise the join branch")
			}
			advanceStmtCounter(ctx)
			op, err := Build(gathered)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if err := op.Open(ctx); err != nil {
				t.Fatalf("open: %v", err)
			}
			var got []string
			for {
				slot, err := op.Next()
				if err == EOF {
					break
				}
				if err != nil {
					op.Close()
					t.Fatalf("next: %v", err)
				}
				got = append(got, renderRows([]Row{slot.Row()})...)
			}
			if err := op.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}

			sort.Strings(want)
			sort.Strings(got)
			if len(got) != len(want) {
				t.Fatalf("got %d rows, want %d — more means a worker replayed a whole branch instead of its partition (the N-copies defect)",
					len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("row %d differs: got %q want %q — a worker dropped or duplicated a branch row", i, got[i], want[i])
				}
			}
		})
	}
}

// TestCollectShareableJoinsDescendsSetOp pins the prebuild agreement for the
// widened branch: a hash join on either SetOp branch is collected for the
// leader prebuild; a scan-only SetOp collects nothing.
func TestCollectShareableJoinsDescendsSetOp(t *testing.T) {
	hashPlan := &optimizer.Join{Algo: optimizer.JoinAlgoHash, Type: optimizer.JoinTypeInner}
	hashOp := func() *joinOp {
		return &joinOp{plan: hashPlan, left: &seqScanOp{}, right: &seqScanOp{}}
	}
	for _, tc := range []struct {
		name string
		op   *setOp
		want int
	}{
		{"hash-left", &setOp{left: hashOp(), right: &seqScanOp{}}, 1},
		{"hash-right", &setOp{left: &seqScanOp{}, right: hashOp()}, 1},
		{"hash-both", &setOp{left: hashOp(), right: hashOp()}, 2},
		{"scans-only", &setOp{left: &seqScanOp{}, right: &seqScanOp{}}, 0},
	} {
		var out []*joinOp
		collectShareableJoins(tc.op, &out)
		if len(out) != tc.want {
			t.Errorf("%s: collected %d joins, want %d", tc.name, len(out), tc.want)
		}
	}
}

// TestCollectBitmapScansDescendsSetOp is the bitmap twin: the collection and
// the plan-side gate (HasBitmapScan) must agree on what sits under a SetOp,
// even though a bitmap-DRIVEN branch is still refused at admission.
func TestCollectBitmapScansDescendsSetOp(t *testing.T) {
	so := &setOp{left: &seqScanOp{}, right: &bitmapHeapScanOp{}}
	var out []*bitmapHeapScanOp
	collectBitmapScans(so, &out)
	if len(out) != 1 {
		t.Fatalf("collected %d bitmap scans under a SetOp, want 1", len(out))
	}
	plain := &setOp{left: &seqScanOp{}, right: &seqScanOp{}}
	out = nil
	collectBitmapScans(plain, &out)
	if len(out) != 0 {
		t.Fatalf("collected %d bitmap scans under a scan-only SetOp, want 0", len(out))
	}
}
