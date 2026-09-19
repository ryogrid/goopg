package executor

// M0142-0005a: fused NestedLoopIndexJoin worker semantics — the shape PG's
// try_partial_nestloop_path builds when its inner is the cheapest
// parameterized path, INCLUDING the get_memoize_path variant
// (joinpath.c:2194-2199). goopg's memoized NLI is a fused
// *NestedLoopIndexJoin carrying the cache as the InnerMemo FIELD (never a
// *Join{Right: *Memoize} — createPlan panics on a free-standing
// PathMemoize), so the claim walks need their own sibling arms that
// descend the OUTER literally and never touch the re-probed inner.
//
// These tests pin the arm guard-for-guard with the planner's
// NestedLoopIndexJoinIsPartialCapable: INNER only, bare keyed equality
// probe (no SAOP, no range bounds, no bitmap), nil-plan/nil-state fail
// closed. The serial-vs-parallel identity gate lives with the plan-flip
// verification (TPC-DS Q34/Q73 on the SF0.25 sweep); what these tests
// protect is the claim topology: one partitioned outer, one re-probed
// inner per worker, no claim state crossing the boundary.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// nliProbePlan builds the bare parameterized equality probe
// lateralProbeIsPartialProbe admits — the same shape
// parallel_lateral_probe_test.go uses for the decomposed twin.
func nliProbePlan() *optimizer.IndexScan {
	return &optimizer.IndexScan{Index: &catalog.Index{},
		Key: &optimizer.OuterColumnRef{Level: 1, Index: 1}}
}

// nliPlan builds a fused NLI plan: INNER over a SeqScan outer and a bare
// keyed probe inner. memo=true adds the InnerMemo wrapper — the field
// aliases the same *IndexScan, exactly as createNestLoopPlan's fused arm
// emits it.
func nliPlan(typ optimizer.JoinType, inner optimizer.Node, memo bool) *optimizer.NestedLoopIndexJoin {
	nli := &optimizer.NestedLoopIndexJoin{
		Type: typ, Outer: &optimizer.SeqScan{}, Inner: inner,
	}
	if memo {
		is, ok := inner.(*optimizer.IndexScan)
		if !ok {
			panic("nliPlan: InnerMemo aliases an *IndexScan only")
		}
		nli.InnerMemo = &optimizer.Memoize{Child: is}
	}
	return nli
}

// TestParallelNLIWalkerAdmission pins the approval: a capable fused NLI
// attaches on all three claim walks, the outer's scan takes the claim,
// and the probe inner carries none — memoized or not.
func TestParallelNLIWalkerAdmission(t *testing.T) {
	for _, memo := range []bool{false, true} {
		name := "bare"
		if memo {
			name = "memoized"
		}
		t.Run(name, func(t *testing.T) {
			op := &nestedLoopIndexJoinOp{
				plan:  nliPlan(optimizer.JoinTypeInner, nliProbePlan(), memo),
				outer: &seqScanOp{},
			}
			if !attachParallelScan(op, newParallelScanState(0)) {
				t.Fatal("approved NLI must attach (sequential)")
			}
			if op.outer.(*seqScanOp).pscan == nil {
				t.Error("outer scan must take the claim")
			}

			op = &nestedLoopIndexJoinOp{
				plan:  nliPlan(optimizer.JoinTypeInner, nliProbePlan(), memo),
				outer: &bitmapHeapScanOp{},
			}
			if !attachParallelBitmapScan(op, newParallelBitmapState()) {
				t.Fatal("approved NLI must attach (bitmap walk reaches outer)")
			}
			if op.outer.(*bitmapHeapScanOp).pbm == nil {
				t.Error("outer bitmap must take the claim")
			}

			op = &nestedLoopIndexJoinOp{
				plan:  nliPlan(optimizer.JoinTypeInner, nliProbePlan(), memo),
				outer: &indexOnlyScanOp{},
			}
			if !attachParallelIndexScan(op, newParallelIndexScanState()) {
				t.Fatal("approved NLI must attach (index walk reaches outer)")
			}
			if op.outer.(*indexOnlyScanOp).pidx == nil {
				t.Error("outer index scan must take the claim")
			}
		})
	}
}

// TestParallelNLIWalkerRefusals pins the refusal matrix on all three
// walks: every non-INNER jointype, a bitmap inner, an unkeyed inner, an
// SAOP probe, a range-bounded probe, nil plan, nil state.
func TestParallelNLIWalkerRefusals(t *testing.T) {
	bitmapInner := &optimizer.BitmapHeapScan{}
	saopInner := &optimizer.IndexScan{Index: &catalog.Index{},
		Key:      &optimizer.OuterColumnRef{Level: 1, Index: 1},
		SAOPKeys: []optimizer.Expr{&optimizer.OuterColumnRef{Level: 1, Index: 2}}}
	unkeyedInner := &optimizer.IndexScan{Index: &catalog.Index{}}
	rangedInner := &optimizer.IndexScan{Index: &catalog.Index{},
		Key:    &optimizer.OuterColumnRef{Level: 1, Index: 1},
		LowKey: &optimizer.NumericConst{Value: "1"}}

	refusals := map[string]*optimizer.NestedLoopIndexJoin{
		"left":    nliPlan(optimizer.JoinTypeLeft, nliProbePlan(), false),
		"right":   nliPlan(optimizer.JoinTypeRight, nliProbePlan(), false),
		"full":    nliPlan(optimizer.JoinTypeFull, nliProbePlan(), false),
		"semi":    nliPlan(optimizer.JoinTypeSemi, nliProbePlan(), false),
		"anti":    nliPlan(optimizer.JoinTypeAnti, nliProbePlan(), false),
		"cross":   nliPlan(optimizer.JoinTypeCross, nliProbePlan(), false),
		"bitmap":  nliPlan(optimizer.JoinTypeInner, bitmapInner, false),
		"saop":    nliPlan(optimizer.JoinTypeInner, saopInner, false),
		"unkeyed": nliPlan(optimizer.JoinTypeInner, unkeyedInner, false),
		"ranged":  nliPlan(optimizer.JoinTypeInner, rangedInner, false),
	}
	for name, plan := range refusals {
		op := &nestedLoopIndexJoinOp{plan: plan, outer: &seqScanOp{}}
		if attachParallelScan(op, newParallelScanState(0)) {
			t.Errorf("%s: sequential walk must refuse", name)
		}
		op = &nestedLoopIndexJoinOp{plan: plan, outer: &bitmapHeapScanOp{}}
		if attachParallelBitmapScan(op, newParallelBitmapState()) {
			t.Errorf("%s: bitmap walk must refuse", name)
		}
		op = &nestedLoopIndexJoinOp{plan: plan, outer: &indexOnlyScanOp{}}
		if attachParallelIndexScan(op, newParallelIndexScanState()) {
			t.Errorf("%s: index walk must refuse", name)
		}
	}
	// Nil plan fails closed on every walk.
	nilOp := &nestedLoopIndexJoinOp{outer: &seqScanOp{}}
	if attachParallelScan(nilOp, newParallelScanState(0)) {
		t.Error("nil plan: sequential walk must refuse")
	}
	if attachParallelBitmapScan(nilOp, newParallelBitmapState()) {
		t.Error("nil plan: bitmap walk must refuse")
	}
	if attachParallelIndexScan(nilOp, newParallelIndexScanState()) {
		t.Error("nil plan: index walk must refuse")
	}
	// Nil state fails closed on the approved shape.
	ok := &nestedLoopIndexJoinOp{
		plan:  nliPlan(optimizer.JoinTypeInner, nliProbePlan(), false),
		outer: &seqScanOp{},
	}
	if attachParallelScan(ok, nil) {
		t.Error("nil state must refuse")
	}
}

// TestCollectShareableJoinsDescendsNLI pins the prebuild agreement: a hash
// join below an approved NLI's outer is COLLECTED for leader prebuild;
// under a refused shape nothing is — matching HasShareableHashJoin's
// plan-side verdict so the promise never exceeds the collection.
func TestCollectShareableJoinsDescendsNLI(t *testing.T) {
	hash := &joinOp{plan: &optimizer.Join{Algo: optimizer.JoinAlgoHash, Type: optimizer.JoinTypeInner}}
	op := &nestedLoopIndexJoinOp{
		plan:  nliPlan(optimizer.JoinTypeInner, nliProbePlan(), false),
		outer: hash,
	}
	var got []*joinOp
	collectShareableJoins(op, &got)
	if len(got) != 1 || got[0] != hash {
		t.Fatalf("approved NLI: collected %d joins, want the outer hash", len(got))
	}

	refused := &nestedLoopIndexJoinOp{
		plan:  nliPlan(optimizer.JoinTypeLeft, nliProbePlan(), false),
		outer: hash,
	}
	got = got[:0]
	collectShareableJoins(refused, &got)
	if len(got) != 0 {
		t.Fatalf("refused NLI: collected %d joins, want none", len(got))
	}
}

// TestCollectBitmapScansDescendsNLI pins the bitmap-prebuild mirror: a
// driving bitmap under the approved outer is found; under a refused
// shape the collection is empty (the subtree never reaches workers).
func TestCollectBitmapScansDescendsNLI(t *testing.T) {
	bm := &bitmapHeapScanOp{}
	op := &nestedLoopIndexJoinOp{
		plan:  nliPlan(optimizer.JoinTypeInner, nliProbePlan(), false),
		outer: bm,
	}
	var got []*bitmapHeapScanOp
	collectBitmapScans(op, &got)
	if len(got) != 1 || got[0] != bm {
		t.Fatalf("approved NLI: collected %d bitmaps, want the outer one", len(got))
	}

	refused := &nestedLoopIndexJoinOp{
		plan:  nliPlan(optimizer.JoinTypeSemi, nliProbePlan(), false),
		outer: bm,
	}
	got = got[:0]
	collectBitmapScans(refused, &got)
	if len(got) != 0 {
		t.Fatalf("refused NLI: collected %d bitmaps, want none", len(got))
	}
}
