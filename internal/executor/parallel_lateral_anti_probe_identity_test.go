package executor

// The parallel ANTI lateral-probe nested-loop identity pin (M0146-0002j).
//
// This is the R95/R25 shape under JoinAnti: `Join{Algo: NestedLoop,
// Lateral}` over a bare parameterized index probe — PG's
// `try_partial_nestloop_path` output when `innerrel->cheapest_parameterized_paths`
// is an index probe — partial through its OUTER only while each worker
// re-opens the probe per outer row. The ANTI verdict is per-outer-row and
// worker-local: emit the outer row iff NO probe row qualifies, decided by
// each worker over its own partition (`finishOuter`, join_nl_stream.go),
// so a partitioned outer is transparent. TPC-H Q21 is the named consumer:
// PG runs `Nested Loop Anti Join` inside the Gather probing l3's index
// per worker.
//
// The pin is an IDENTITY test, not a predicate test, for the reason the
// SEMI twin's header records: a gate diverging between the planner's
// `lateralProbeJoinIsPartialCapable` / the classifier's
// `partialProbeNestLoopJointype` and this file's `lateralProbeJoinPartial`
// leaves the driving scan UNATTACHED, and every participant then runs the
// whole plan — the N-copy signature this test fails on.

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// planHasLateralAntiProbe reports whether the tree contains the decomposed
// probe shape under test: a lateral ANTI nested-loop `*optimizer.Join`
// whose right side is a bare index probe — the exact node
// `lateralProbeJoinPartial` gates. Asserting it keeps the identity
// comparison non-vacuous: an ordinary ANTI nested loop, a fused
// *NestedLoopIndexJoin (the memoized twin), or a hash anti join would all
// pass the row count while exercising a different family.
func planHasLateralAntiProbe(n optimizer.Node) bool {
	if n == nil {
		return false
	}
	switch x := n.(type) {
	case *optimizer.Join:
		if x.Algo == optimizer.JoinAlgoNestedLoop && x.Type == optimizer.JoinTypeAnti && x.Lateral {
			switch x.Right.(type) {
			case *optimizer.IndexScan, *optimizer.IndexOnlyScan:
				return true
			}
		}
		return planHasLateralAntiProbe(x.Left) || planHasLateralAntiProbe(x.Right)
	case *optimizer.Project:
		return planHasLateralAntiProbe(x.Child)
	case *optimizer.Filter:
		return planHasLateralAntiProbe(x.Child)
	case *optimizer.Sort:
		return planHasLateralAntiProbe(x.Child)
	case *optimizer.Limit:
		return planHasLateralAntiProbe(x.Child)
	case *optimizer.Aggregate:
		return planHasLateralAntiProbe(x.Child)
	case *optimizer.Gather:
		return planHasLateralAntiProbe(x.Child)
	}
	return false
}

func TestParallelLateralAntiProbeIdentity(t *testing.T) {
	// The fused family's fixture: an indexed equality correlation with a
	// large non-matching inner, which is what makes the planner elect the
	// parameterized probe rather than a hash anti join.
	ctx, cleanup := nliJointypeFixture(t)
	defer cleanup()

	// PG's index-vs-bitmap election prices the two within 0.01; the
	// calibrated 2x probe multiplier decides it toward the bitmap inner,
	// which is the fused family's sibling shape and not this pin's subject.
	// `SetIndexProbeCostMultiplier` exists for exactly this purpose (owner
	// decision 2026-09-24, M0145-0008 option (c)).
	defer optimizer.SetIndexProbeCostMultiplier("1")()

	const sql = "SELECT o.id FROM nlij_outer o WHERE NOT EXISTS (SELECT 1 FROM nlij_inner i WHERE i.k = o.k)"

	serialRows, err := runQueryWithErr(ctx, sql)
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	want := len(serialRows)
	if want != 50 {
		t.Fatalf("fixture: serial returned %d rows, want 50 — only the last 50 outer keys lack a match", want)
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
	// the jointree pipeline emits a decomposed Join{Lateral,Anti} over the
	// probe, and if the planner moves off it a human must re-shape the
	// fixture or retire the pin deliberately.
	if !planHasLateralAntiProbe(node) {
		t.Fatalf("planner no longer elects a lateral ANTI index probe for this fixture; "+
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
					"the N-copy signature of an unattached driving scan on the lateral ANTI probe",
					workers, n, want)
			}
		})
	}
}
