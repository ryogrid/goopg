package executor

// The parallel LEFT lateral-probe nested-loop identity pin (M0146-0005ao).
//
// This is the R95/R25 shape under JoinLeft: `Join{Algo: NestedLoop,
// Lateral}` over a bare parameterized index probe, partial through its OUTER
// only while each worker re-opens the probe per outer row. The LEFT verdict
// is per-outer-row and worker-local: emit every qualifying probe row, or the
// outer row null-padded when none qualifies, decided by each worker over its
// own partition. TPC-DS Q40 is the named consumer: PG runs `Nested Loop Left
// Join` probing catalog_returns' index inside the Gather Merge.
//
// Like its SEMI/ANTI twins this is an IDENTITY test: a gate diverging
// between the planner's `lateralProbeJoinIsPartialCapable` / the
// classifier's `partialProbeNestLoopJointype` and the executor's
// `lateralProbeJoinPartial` leaves the driving scan unattached, and every
// participant then runs the whole plan — the N-copy signature this test
// fails on. It also counts the null-padded rows separately, so a worker that
// padded an outer row another worker matched would show up even when the
// totals agree.

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// planHasLateralLeftProbe reports whether the tree contains the decomposed
// probe shape under test: a lateral LEFT nested-loop `*optimizer.Join`
// whose right side is a bare index probe — the exact node
// `lateralProbeJoinPartial` gates. Asserting it keeps the identity
// comparison non-vacuous: an ordinary LEFT nested loop, a fused
// *NestedLoopIndexJoin (the memoized twin), or a hash left join would all
// pass the row count while exercising a different family.
func planHasLateralLeftProbe(n optimizer.Node) bool {
	if n == nil {
		return false
	}
	switch x := n.(type) {
	case *optimizer.Join:
		if x.Algo == optimizer.JoinAlgoNestedLoop && x.Type == optimizer.JoinTypeLeft && x.Lateral {
			switch x.Right.(type) {
			case *optimizer.IndexScan, *optimizer.IndexOnlyScan:
				return true
			}
		}
		return planHasLateralLeftProbe(x.Left) || planHasLateralLeftProbe(x.Right)
	case *optimizer.Project:
		return planHasLateralLeftProbe(x.Child)
	case *optimizer.Filter:
		return planHasLateralLeftProbe(x.Child)
	case *optimizer.Sort:
		return planHasLateralLeftProbe(x.Child)
	case *optimizer.Limit:
		return planHasLateralLeftProbe(x.Child)
	case *optimizer.Aggregate:
		return planHasLateralLeftProbe(x.Child)
	case *optimizer.Gather:
		return planHasLateralLeftProbe(x.Child)
	}
	return false
}

func TestParallelLateralLeftProbeIdentity(t *testing.T) {
	// The fused family's fixture: an indexed equality correlation with a
	// large non-matching inner, which is what makes the planner elect the
	// parameterized probe rather than a hash or merge left join.
	ctx, cleanup := nliJointypeFixture(t)
	defer cleanup()

	// PG's index-vs-bitmap election prices the two within 0.01; the
	// calibrated 2x probe multiplier decides it toward the bitmap inner,
	// which is the fused family's sibling shape and not this pin's subject.
	// `SetIndexProbeCostMultiplier` exists for exactly this purpose (owner
	// decision 2026-09-24, M0145-0008 option (c)).
	defer optimizer.SetIndexProbeCostMultiplier("1")()

	const sql = "SELECT o.id, i.v FROM nlij_outer o LEFT JOIN nlij_inner i ON i.k = o.k"

	serialRows, err := runQueryWithErr(ctx, sql)
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	want := len(serialRows)
	if want != 100 {
		t.Fatalf("fixture: serial returned %d rows, want 100 — 50 matched outer rows plus 50 null-padded", want)
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
	// the jointree pipeline emits a decomposed Join{Lateral,Left} over the
	// probe, and if the planner moves off it a human must re-shape the
	// fixture or retire the pin deliberately.
	if !planHasLateralLeftProbe(node) {
		t.Fatalf("planner no longer elects a lateral LEFT index probe for this fixture; "+
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
			n, padded := 0, 0
			for {
				row, err := op.Next()
				if err == EOF {
					break
				}
				if err != nil {
					op.Close()
					t.Fatalf("next: %v", err)
				}
				n++
				if r := slotRow(row); len(r) > 1 && r[1].IsNull() {
					padded++
				}
			}
			op.Close()
			if padded != 50 {
				t.Fatalf("workers=%d: %d null-padded rows, want 50 — a worker padded an outer row that has a match", workers, padded)
			}
			if n != want {
				t.Fatalf("workers=%d: parallel returned %d rows, want %d (serial) — "+
					"the N-copy signature of an unattached driving scan on the lateral LEFT probe",
					workers, n, want)
			}
		})
	}
}
