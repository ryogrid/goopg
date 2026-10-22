package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// M0122-0012 slice 35 guards: a Project below a consumer-complete parent (a
// plain or partial Aggregate, through any sorts) evaluates only the targets
// the parent's arms and the sort keys read, and only those targets' refs
// bound the scan below it. Anywhere else every target stays live.

// reexpandProject is the planner's re-expansion shape: every child column,
// in a different order, plus a constant fill.
func reexpandProject(child optimizer.Node) *optimizer.Project {
	tg := make([]optimizer.Expr, 0, 17)
	for i := 15; i >= 0; i-- {
		tg = append(tg, deformCol(i))
	}
	tg = append(tg, &optimizer.NullConst{})
	return &optimizer.Project{Child: child, Targets: tg}
}

func projectUnder(t *testing.T, op Operator) *projectOp {
	t.Helper()
	for {
		switch o := op.(type) {
		case *projectOp:
			return o
		case *aggregateOp:
			op = o.child
		case *sortOp:
			op = o.child
		case *incrementalSortOp:
			op = o.child
		default:
			t.Fatalf("no projectOp under %T", op)
		}
	}
}

func liveIndexes(live []bool) []int {
	var out []int
	for i, b := range live {
		if b {
			out = append(out, i)
		}
	}
	return out
}

func TestProjectLiveSetUnderAggregate(t *testing.T) {
	scan16 := func() *optimizer.SeqScan { return &optimizer.SeqScan{Table: deformTable(16)} }
	agg := func(mode optimizer.AggMode, child optimizer.Node) *optimizer.Aggregate {
		return &optimizer.Aggregate{
			Child: child,
			Mode:  mode,
			Aggs: []optimizer.AggregateCall{
				// Output 2 of the projection is child column 13.
				{Name: "sum", Arg: deformCol(2), Type: catalog.Type{Name: "int8"}},
			},
		}
	}
	sortOn := func(idx int, child optimizer.Node) *optimizer.Sort {
		return &optimizer.Sort{Child: child, Keys: []optimizer.SortKey{{Expr: deformCol(idx)}}}
	}

	t.Run("aggregate-sort-project", func(t *testing.T) {
		// Arms read output 2 (child 13), the sort key output 4 (child 11):
		// the projection evaluates those two and the scan deforms [0, 14).
		op := mustBuildDeform(t, agg(optimizer.AggModeSimple, sortOn(4, reexpandProject(scan16()))))
		if got := liveIndexes(projectUnder(t, op).live); len(got) != 2 || got[0] != 2 || got[1] != 4 {
			t.Fatalf("live targets %v, want [2 4]", got)
		}
		if b, n := seqLeafBound(t, op); b != 14 || n != 16 {
			t.Fatalf("bound=%d ncols=%d, want 14/16 (the fold of the live targets alone)", b, n)
		}
	})

	t.Run("partial-aggregate", func(t *testing.T) {
		op := mustBuildDeform(t, agg(optimizer.AggModePartial, reexpandProject(scan16())))
		if got := liveIndexes(projectUnder(t, op).live); len(got) != 1 || got[0] != 2 {
			t.Fatalf("live targets %v, want [2]", got)
		}
	})

	t.Run("final-aggregate-keeps-every-target", func(t *testing.T) {
		// A Finalize aggregate reads its child's state columns without an
		// arm: its arms are not every reader.
		op := mustBuildDeform(t, agg(optimizer.AggModeFinal, reexpandProject(scan16())))
		if live := projectUnder(t, op).live; live != nil {
			t.Fatalf("live = %v, want nil (every target)", liveIndexes(live))
		}
	})

	t.Run("root-sort-keeps-every-target", func(t *testing.T) {
		// A root Sort folds only its key while the client reads every
		// column: not a consumer-complete parent.
		op := mustBuildDeform(t, sortOn(4, reexpandProject(scan16())))
		if live := projectUnder(t, op).live; live != nil {
			t.Fatalf("live = %v, want nil (every target)", liveIndexes(live))
		}
		if b, n := seqLeafBound(t, op); b != 16 || n != 16 {
			t.Fatalf("bound=%d ncols=%d, want full 16/16", b, n)
		}
	})

	t.Run("unskippable-dead-target-stays-live", func(t *testing.T) {
		// A dead target that could fail when evaluated is still evaluated.
		p := reexpandProject(scan16())
		p.Targets[9] = deformLt(deformCol(6), deformInt(1))
		op := mustBuildDeform(t, agg(optimizer.AggModeSimple, p))
		if got := liveIndexes(projectUnder(t, op).live); len(got) != 2 || got[0] != 2 || got[1] != 9 {
			t.Fatalf("live targets %v, want [2 9]", got)
		}
	})
}

// TestNestedProjectLiveSet pins M0122-0012 slice 53: a Project whose child is
// another Project builds that child with a live set — the child's only reader
// is the parent, which evaluates just its own live targets. TPC-DS Q14 stacks
// a re-expansion Project on another that lists every joined column; with
// every inner target live the fold reached the last column and the probes
// under it deformed whole rows.
func TestNestedProjectLiveSet(t *testing.T) {
	scan16 := &optimizer.SeqScan{Table: deformTable(16)}
	inner := reexpandProject(scan16)
	outer := reexpandProject(inner)
	agg := &optimizer.Aggregate{
		Child: outer,
		Mode:  optimizer.AggModeSimple,
		Aggs: []optimizer.AggregateCall{
			// Output 2 of the outer projection is inner output 13, which
			// is scan column 2.
			{Name: "sum", Arg: deformCol(2), Type: catalog.Type{Name: "int8"}},
		},
	}
	op := mustBuildDeform(t, agg)
	outerOp := projectUnder(t, op)
	if got := liveIndexes(outerOp.live); len(got) != 1 || got[0] != 2 {
		t.Fatalf("outer live targets %v, want [2]", got)
	}
	innerOp := projectUnder(t, outerOp.child)
	if got := liveIndexes(innerOp.live); len(got) != 1 || got[0] != 13 {
		t.Fatalf("inner live targets %v, want [13] (the one the outer's live target reads)", got)
	}
	if b, n := seqLeafBound(t, op); b != 3 || n != 16 {
		t.Fatalf("bound=%d ncols=%d, want 3/16 (scan column 2 alone)", b, n)
	}

	// Under a root (not consumer-complete) parent, the outer evaluates every
	// target, so the inner's consumers are all of them: every inner target
	// the outer reads (0..15; the trailing constant fill is read by nobody)
	// stays live, and the scan deforms in full.
	op = mustBuildDeform(t, reexpandProject(reexpandProject(&optimizer.SeqScan{Table: deformTable(16)})))
	if got := liveIndexes(projectUnder(t, projectUnder(t, op).child).live); len(got) != 16 || got[15] != 15 {
		t.Fatalf("inner live = %v under a full outer, want [0..15]", got)
	}
	if b, n := seqLeafBound(t, op); b != 16 || n != 16 {
		t.Fatalf("bound=%d ncols=%d under a full outer, want 16/16", b, n)
	}
}
