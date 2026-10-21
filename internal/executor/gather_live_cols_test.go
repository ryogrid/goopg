package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// M0122-0012 slice 39 guards: a Gather directly under a plain or partial
// aggregate ships only the columns the aggregate's arms read; every other
// cell crosses the worker queue as NULL instead of a deep copy. TPC-DS Q23's
// Gathers sent ~50-column join rows to HashAggregates reading three of them.

func TestConsumerLiveColumns(t *testing.T) {
	substr := &optimizer.FuncCall{Name: "substr", Args: []optimizer.Expr{deformCol(4), deformInt(1), deformInt(30)}}
	if got := liveIndexes(consumerLiveColumns([]optimizer.Expr{substr, deformCol(9), nil})); len(got) != 2 || got[0] != 4 || got[1] != 9 {
		t.Fatalf("live columns %v, want [4 9]: a function call's argument is read", got)
	}
	if live := consumerLiveColumns(nil); live == nil || len(live) != 0 {
		t.Fatalf("no consumer (count(*)) = %v, want an empty, non-nil mask: nothing is read", live)
	}
	for name, e := range map[string]optimizer.Expr{
		"sublink":   &optimizer.SubqueryExpr{},
		"whole-row": &optimizer.ColumnRef{Index: -1, Name: "t", Type: catalog.Type{Name: "record"}},
		"ctid":      &optimizer.CTIDExpr{},
	} {
		if live := consumerLiveColumns([]optimizer.Expr{deformCol(1), e}); live != nil {
			t.Fatalf("%s: live = %v, want nil (every column)", name, liveIndexes(live))
		}
	}
}

func TestGatherUnderAggregateShipsLiveColumns(t *testing.T) {
	agg := func(mode optimizer.AggMode) *optimizer.Aggregate {
		return &optimizer.Aggregate{
			Mode:       mode,
			Child:      &optimizer.Gather{Child: &optimizer.SeqScan{Table: deformTable(16)}},
			GroupExprs: []optimizer.Expr{&optimizer.FuncCall{Name: "substr", Args: []optimizer.Expr{deformCol(3), deformInt(1), deformInt(5)}}},
			Aggs: []optimizer.AggregateCall{
				{Name: "sum", Arg: deformCol(11), Type: catalog.Type{Name: "int8"}},
			},
		}
	}
	gatherUnder := func(t *testing.T, op Operator) *gatherOp {
		t.Helper()
		a, ok := op.(*aggregateOp)
		if !ok {
			t.Fatalf("root is %T", op)
		}
		g, ok := a.child.(*gatherOp)
		if !ok {
			t.Fatalf("aggregate child is %T", a.child)
		}
		return g
	}
	if got := liveIndexes(gatherUnder(t, mustBuildDeform(t, agg(optimizer.AggModeSimple))).liveCols); len(got) != 2 || got[0] != 3 || got[1] != 11 {
		t.Fatalf("Gather live columns %v, want [3 11]", got)
	}
	// A Finalize aggregate reads its child's partial-state columns without an
	// arm, so its Gather ships every column.
	if live := gatherUnder(t, mustBuildDeform(t, agg(optimizer.AggModeFinal))).liveCols; live != nil {
		t.Fatalf("Finalize aggregate's Gather live columns %v, want nil", liveIndexes(live))
	}
}

func TestCloneRowOwnedLive(t *testing.T) {
	src := Row{NewIntDatum(1), NewStringDatum("keep"), NewStringDatum("drop"), NewIntDatum(4)}
	got := cloneRowOwnedLive(src, []bool{false, true})
	if len(got) != len(src) {
		t.Fatalf("width %d, want %d", len(got), len(src))
	}
	if got[1].StringValue() != "keep" {
		t.Fatalf("live cell = %v, want the source value", got[1])
	}
	for _, i := range []int{0, 2, 3} {
		if !got[i].IsNull() {
			t.Fatalf("dead cell %d = %v, want NULL", i, got[i])
		}
	}
}
