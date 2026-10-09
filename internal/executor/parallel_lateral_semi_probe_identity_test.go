package executor

// The parallel SEMI lateral-probe nested-loop identity pin (M0146-0002i).
//
// This is the R95/R25 shape: `Join{Algo: NestedLoop, Lateral}` over a bare
// parameterized index probe — PG's `try_partial_nestloop_path` output when
// `innerrel->cheapest_parameterized_paths` is an index probe — partial
// through its OUTER only while each worker re-opens the probe per outer
// row. The SEMI verdict is per-outer-row and worker-local: one qualifying
// probe row decides the outer row and the probe breaks (`finishOuter`,
// join_nl_stream.go), so a partitioned outer is transparent.
//
// The pin is an IDENTITY test, not a predicate test, for the reason the
// ordinary SEMI pin's header records (2026-09-21): a gate diverging between
// the planner's `lateralProbeJoinIsPartialCapable` / the classifier's
// `partialProbeNestLoopJointype` and this file's `lateralProbeJoinPartial`
// leaves the driving scan UNATTACHED, and every participant then runs the
// whole plan — the N-copy signature this test fails on.

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// planHasLateralSemiProbe reports whether the tree contains the decomposed
// probe shape under test: a lateral SEMI nested-loop `*optimizer.Join` whose
// right side is a bare index probe — the exact node
// `lateralProbeJoinPartial` gates. Asserting it keeps the identity
// comparison non-vacuous: an ordinary SEMI nested loop, a fused
// *NestedLoopIndexJoin (the memoized twin), or a hash semi join would all
// pass the row count while exercising a different family.
func planHasLateralSemiProbe(n optimizer.Node) bool {
	if n == nil {
		return false
	}
	switch x := n.(type) {
	case *optimizer.Join:
		if x.Algo == optimizer.JoinAlgoNestedLoop && x.Type == optimizer.JoinTypeSemi && x.Lateral {
			switch x.Right.(type) {
			case *optimizer.IndexScan, *optimizer.IndexOnlyScan:
				return true
			}
		}
		return planHasLateralSemiProbe(x.Left) || planHasLateralSemiProbe(x.Right)
	case *optimizer.Project:
		return planHasLateralSemiProbe(x.Child)
	case *optimizer.Filter:
		return planHasLateralSemiProbe(x.Child)
	case *optimizer.Sort:
		return planHasLateralSemiProbe(x.Child)
	case *optimizer.Limit:
		return planHasLateralSemiProbe(x.Child)
	case *optimizer.Aggregate:
		return planHasLateralSemiProbe(x.Child)
	case *optimizer.Gather:
		return planHasLateralSemiProbe(x.Child)
	}
	return false
}

func TestParallelLateralSemiProbeIdentity(t *testing.T) {
	// The fused family's fixture: an indexed equality correlation with a
	// large non-matching inner, which is what makes the planner elect the
	// parameterized probe rather than a hash semi join.
	ctx, cleanup := nliJointypeFixture(t)
	defer cleanup()

	// PG's index-vs-bitmap election prices the two within 0.01; the
	// calibrated 2x probe multiplier decides it toward the bitmap inner,
	// which is the fused family's sibling shape and not this pin's subject.
	// `SetIndexProbeCostMultiplier` exists for exactly this purpose (owner
	// decision 2026-09-24, M0145-0008 option (c)).
	defer optimizer.SetIndexProbeCostMultiplier("1")()

	const sql = "SELECT o.id FROM nlij_outer o WHERE EXISTS (SELECT 1 FROM nlij_inner i WHERE i.k = o.k)"

	serialRows, err := runQueryWithErr(ctx, sql)
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	want := len(serialRows)
	if want != 50 {
		t.Fatalf("fixture: serial returned %d rows, want 50 — only the first 50 outer keys match", want)
	}

	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	node, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	// Fail rather than skip: this shape is the whole point of the test —
	// the jointree pipeline emits a decomposed Join{Lateral,Semi} over the
	// probe, and if the planner moves off it a human must re-shape the
	// fixture or retire the pin deliberately.
	if !planHasLateralSemiProbe(node) {
		t.Fatalf("planner no longer elects a lateral SEMI index probe for this fixture; "+
			"the identity pin would be vacuous. Re-shape the fixture or retire the pin deliberately.\nplan: %#v", node)
	}

	for _, workers := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			stmts, err := parser.Parse(sql)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			gathered := optimizer.NewGather(0, plan, workers)
			ctx.MaxParallelWorkers = 8
			ctx.ParallelLeaderParticipation = true
			op, err := Build(gathered)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if err := op.Open(ctx); err != nil {
				t.Fatalf("open: %v", err)
			}
			n := 0
			for {
				_, err := op.Next()
				if err == EOF {
					break
				}
				if err != nil {
					op.Close()
					t.Fatalf("next: %v", err)
				}
				n++
			}
			op.Close()
			if n != want {
				t.Fatalf("workers=%d: parallel returned %d rows, want %d (serial) — "+
					"the N-copy signature of an unattached driving scan on the lateral SEMI probe",
					workers, n, want)
			}
		})
	}
}
