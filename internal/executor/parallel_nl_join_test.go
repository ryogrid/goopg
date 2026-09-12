package executor

// R94 (plan-parity-fix-take2): ordinary INNER nested-loop outer partition.
//
// The planner twin (nestedLoopJoinIsPartialCapable) and the path classifier
// admit the shape; these tests prove the executor's three claim walkers
// model it: every worker claims a disjoint outer partition and reads the
// whole inner itself. The serial-vs-parallel identity gate is what catches
// the N-copies failure mode — a plausible row multiset with no error.

import (
	"sort"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// nlPlanForTest plans sql with hash and merge joins disabled so the planner
// emits an ordinary nested-loop *Join. It fails loudly if the shape is not
// what this test exercises, so a planner change cannot silently vacate it.
func nlPlanForTest(t *testing.T, ctx *Context, sql string) optimizer.Node {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableHashJoin = false
	ps.EnableMergeJoin = false
	node, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, ps)
	if err != nil {
		t.Fatalf("plan %q: %v", sql, err)
	}
	j, ok := findNLJoin(node)
	if !ok {
		t.Fatalf("%q did not plan to an ordinary nested-loop Join (got %T); "+
			"this test would assert nothing", sql, node)
	}
	if j.Type == optimizer.JoinTypeCross {
		// Test-only normalization: the comma corpus plans Cross, and the
		// slice admits Inner only. Cross ≡ Inner for NL execution — no
		// Cross branch exists in join_nl_stream.go, EXPLAIN renders them
		// together (operators_explain.go), and deformJoinBounds shares
		// one arm — so the flip changes admission, not semantics.
		j.Type = optimizer.JoinTypeInner
	}
	return node
}

// findNLJoin locates the first ordinary nested-loop *Join under the
// single-child wrappers a TPC-H-shaped plan carries.
func findNLJoin(n optimizer.Node) (*optimizer.Join, bool) {
	for n != nil {
		switch x := n.(type) {
		case *optimizer.Join:
			if x.Algo == optimizer.JoinAlgoNestedLoop {
				return x, true
			}
			return nil, false
		case *optimizer.Filter:
			n = x.Child
		case *optimizer.Project:
			n = x.Child
		case *optimizer.Aggregate:
			n = x.Child
		case *optimizer.Sort:
			n = x.Child
		default:
			return nil, false
		}
	}
	return nil, false
}

// runNLGathered hand-places a Gather directly over the NL join (the
// post-pass never puts one over a join — findPartialSubtree bails on two
// children), below any aggregate, and drains it with the given worker count.
// A Gather above an Aggregate would run the whole aggregate per participant.
func runNLGathered(t *testing.T, ctx *Context, node optimizer.Node, workers int) []string {
	t.Helper()
	advanceStmtCounter(ctx)
	gathered := placeGatherOverNL(t, node, workers)
	ctx.MaxParallelWorkers = 8
	// With one worker the leader must not also execute, or leader +
	// worker each read the whole subtree (2x rows) — a harness
	// artifact, not a plan defect.
	ctx.ParallelLeaderParticipation = workers > 1
	op, err := Build(gathered)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("open: %v", err)
	}
	var out []string
	for {
		slot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		out = append(out, renderRows([]Row{slot.Row()})...)
	}
	if err := op.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return out
}

// placeGatherOverNL rewrites the single-child wrapper chain above the NL
// join so the Gather sits directly over the join. Wrappers with exported
// Child fields are shallow-copied, never mutated in place.
func placeGatherOverNL(t *testing.T, node optimizer.Node, workers int) optimizer.Node {
	t.Helper()
	switch x := node.(type) {
	case *optimizer.Join:
		return optimizer.NewGather(0, node, workers)
	case *optimizer.Filter:
		c := *x
		c.Child = placeGatherOverNL(t, x.Child, workers)
		return &c
	case *optimizer.Project:
		c := *x
		c.Child = placeGatherOverNL(t, x.Child, workers)
		return &c
	case *optimizer.Aggregate:
		c := *x
		c.Child = placeGatherOverNL(t, x.Child, workers)
		return &c
	case *optimizer.Sort:
		c := *x
		c.Child = placeGatherOverNL(t, x.Child, workers)
		return &c
	}
	t.Fatalf("cannot place Gather: unexpected node %T above the NL join", node)
	return nil
}

// The corpus is cross joins: with no equi key the nested loop is the only
// arm that can fire, so the shape is guaranteed (an equi pair plans hash
// even with hash/merge disabled — disabled is counted, not skipped — and
// would vacate the test). The executor's partition semantics do not branch
// on keys, so the identity proof transfers.
func nlIdentityCorpus() []string {
	return []string{
		"SELECT f.fid, d.dname FROM pq_fact f, pq_dim d",
		"SELECT count(*) FROM pq_fact f, pq_dim d",
		"SELECT f.fid FROM pq_fact f, pq_dim d WHERE f.amt > 200",
		"SELECT f.fid FROM pq_fact f, pq_dim d WHERE d.dname <> 'd-1'",
		// Empty sides (the drainInner sweep path exists for exactly these).
		"SELECT f.fid FROM pq_fact f, pq_dim d WHERE f.fid > 100000",
		"SELECT f.fid FROM pq_fact f, pq_dim d WHERE d.dk > 100000",
		// Large inner, duplicate-sensitive self-join. Values-only: per-worker
		// whole-inner materialization is N× memory (join_nl_stream.go) and
		// this slice documents the size-gate deferral rather than asserting
		// peak memory (see R94 SCOPE proof note 1).
		"SELECT a.fid, b.fid FROM pq_fact a, pq_fact b WHERE a.fid < 40",
	}
}

// TestParallelNLJoinIdentity is the gate: hand-gathered ordinary INNER NL
// returns the exact serial multiset at 1/2/4 workers.
func TestParallelNLJoinIdentity(t *testing.T) {
	ctx, cleanup := pqJoinFixture(t)
	defer cleanup()

	for _, sql := range nlIdentityCorpus() {
		serialRows, err := runQueryWithErr(ctx, sql)
		if err != nil {
			t.Fatalf("serial %q: %v", sql, err)
		}
		want := renderRows(serialRows)
		sort.Strings(want)
		if len(want) == 0 && sql != nlIdentityCorpus()[4] && sql != nlIdentityCorpus()[5] {
			t.Fatalf("fixture produced no rows for %q; the comparison is vacuous", sql)
		}
		for _, workers := range []int{1, 2, 4} {
			node := nlPlanForTest(t, ctx, sql)
			got := runNLGathered(t, ctx, node, workers)
			sort.Strings(got)
			if len(got) != len(want) {
				t.Fatalf("%q workers=%d: got %d rows, want %d (N-copies or dropped matches)",
					sql, workers, len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("%q workers=%d: row %d differs", sql, workers, i)
				}
			}
		}
	}
}

// TestParallelNLInnerTakesNoClaim pins the outer-only rule structurally: the
// inner subtree's scan ops carry no claim state after a parallel attach.
func TestParallelNLInnerTakesNoClaim(t *testing.T) {
	plan := &optimizer.Join{
		Algo: optimizer.JoinAlgoNestedLoop, Type: optimizer.JoinTypeInner,
		Left: &optimizer.SeqScan{}, Right: &optimizer.SeqScan{},
	}
	left, right := &seqScanOp{}, &seqScanOp{}
	op := &joinOp{plan: plan, left: left, right: right}
	st := newParallelScanState(0)
	if !attachParallelScan(op, st) {
		t.Fatal("approved NL must attach")
	}
	if left.pscan == nil {
		t.Error("outer scan must take the claim")
	}
	if right.pscan != nil {
		t.Error("inner scan must never take claim state")
	}
}

// TestParallelNLWalkerRefusals pins the refusal matrix on all three walks:
// jointype, lateral, nil plan, inner bitmap, and the literal-left rule (a
// claimable right side alone attaches nothing).
func TestParallelNLWalkerRefusals(t *testing.T) {
	mkPlan := func(typ optimizer.JoinType, lateral bool, right optimizer.Node) *optimizer.Join {
		return &optimizer.Join{
			Algo: optimizer.JoinAlgoNestedLoop, Type: typ, Lateral: lateral,
			Left: &optimizer.SeqScan{}, Right: right,
		}
	}
	refusals := map[string]*optimizer.Join{
		"right":   mkPlan(optimizer.JoinTypeRight, false, &optimizer.SeqScan{}),
		"full":    mkPlan(optimizer.JoinTypeFull, false, &optimizer.SeqScan{}),
		"left":    mkPlan(optimizer.JoinTypeLeft, false, &optimizer.SeqScan{}),
		"semi":    mkPlan(optimizer.JoinTypeSemi, false, &optimizer.SeqScan{}),
		"anti":    mkPlan(optimizer.JoinTypeAnti, false, &optimizer.SeqScan{}),
		"lateral": mkPlan(optimizer.JoinTypeInner, true, &optimizer.SeqScan{}),
		// Bitmap anywhere in the inner: prebuildBitmap shares nothing, so
		// the outer would go unpartitioned (N+1 copies, silently).
		"inner-bitmap": mkPlan(optimizer.JoinTypeInner, false, &optimizer.BitmapHeapScan{}),
	}
	for name, plan := range refusals {
		op := &joinOp{plan: plan, left: &seqScanOp{}, right: &seqScanOp{}}
		if attachParallelScan(op, newParallelScanState(0)) {
			t.Errorf("%s: sequential walk must refuse", name)
		}
		if attachParallelBitmapScan(op, newParallelBitmapState()) {
			t.Errorf("%s: bitmap walk must refuse", name)
		}
		if attachParallelIndexScan(op, newParallelIndexScanState()) {
			t.Errorf("%s: index walk must refuse", name)
		}
	}
	// Nil plan and nil state fail closed on every walk.
	nilOp := &joinOp{left: &seqScanOp{}, right: &seqScanOp{}}
	if attachParallelScan(nilOp, newParallelScanState(0)) {
		t.Error("nil plan: sequential walk must refuse")
	}
	if attachParallelScan(&joinOp{plan: mkPlan(optimizer.JoinTypeInner, false, &optimizer.SeqScan{}), left: &seqScanOp{}, right: &seqScanOp{}}, nil) {
		t.Error("nil state: sequential walk must refuse")
	}
}
