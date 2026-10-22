package executor

import (
	"reflect"
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
	// Dead cells are NULL with the deform poison off (poison when armed).
	old := seqScanDeformPoison
	seqScanDeformPoison = false
	t.Cleanup(func() { seqScanDeformPoison = old })
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

// TestGatherMergeUnderAggregateShipsLiveColumns pins M0122-0012 slice 63: a
// Gather Merge under a plain or partial aggregate ships only the columns the
// aggregate's arms read AND its own merge keys, which the leader evaluates on
// every transferred row (a dead key column would arrive NULL and mis-order
// the merge). TPC-DS Q78's GroupAggregate → Gather Merge branches deep-copied
// whole join rows. A Finalize aggregate, or a key the live walk cannot resolve
// (ctid), keeps every column.
func TestGatherMergeUnderAggregateShipsLiveColumns(t *testing.T) {
	agg := func(mode optimizer.AggMode, key optimizer.Expr) *optimizer.Aggregate {
		return &optimizer.Aggregate{
			Mode: mode,
			Child: &optimizer.GatherMerge{
				Keys:  []optimizer.SortKey{{Expr: key}},
				Child: &optimizer.Sort{Keys: []optimizer.SortKey{{Expr: key}}, Child: &optimizer.SeqScan{Table: deformTable(16)}},
			},
			GroupExprs: []optimizer.Expr{deformCol(3)},
			Aggs: []optimizer.AggregateCall{
				{Name: "sum", Arg: deformCol(11), Type: catalog.Type{Name: "int8"}},
			},
		}
	}
	gatherMergeUnder := func(t *testing.T, op Operator) *gatherMergeOp {
		t.Helper()
		a, ok := op.(*aggregateOp)
		if !ok {
			t.Fatalf("root is %T", op)
		}
		g, ok := a.child.(*gatherMergeOp)
		if !ok {
			t.Fatalf("aggregate child is %T", a.child)
		}
		return g
	}
	got := liveIndexes(gatherMergeUnder(t, mustBuildDeform(t, agg(optimizer.AggModeSimple, deformCol(5)))).liveCols)
	if len(got) != 3 || got[0] != 3 || got[1] != 5 || got[2] != 11 {
		t.Fatalf("Gather Merge live columns %v, want [3 5 11] (group column, merge key, aggregate argument)", got)
	}
	if live := gatherMergeUnder(t, mustBuildDeform(t, agg(optimizer.AggModeFinal, deformCol(5)))).liveCols; live != nil {
		t.Fatalf("Finalize aggregate's Gather Merge live columns %v, want nil", liveIndexes(live))
	}
	if live := gatherMergeUnder(t, mustBuildDeform(t, agg(optimizer.AggModeSimple, &optimizer.CTIDExpr{}))).liveCols; live != nil {
		t.Fatalf("ctid merge key: live columns %v, want nil (every column)", liveIndexes(live))
	}
}

// TestNullDatumIsTheZeroDatum pins the premise cloneRowOwnedLive and
// cloneRowOwnedPrefix rest on since slice 63: a fresh row's cells are already
// NullDatum, so a dead cell is left unwritten. Reordering DatumKind so that
// KindNull is not 0 would turn every dead cell into a non-NULL value.
func TestNullDatumIsTheZeroDatum(t *testing.T) {
	// Dead cells are NULL with the deform poison off (poison when armed).
	old := seqScanDeformPoison
	seqScanDeformPoison = false
	defer func() { seqScanDeformPoison = old }()
	if !reflect.DeepEqual(NullDatum, Datum{}) || !(Datum{}).IsNull() {
		t.Fatalf("NullDatum %+v is not the zero Datum", NullDatum)
	}
	row := cloneRowOwnedPrefix(Row{NewIntDatum(1), NewIntDatum(2), NewStringDatum("x")}, 1)
	if row[0].Int != 1 || !row[1].IsNull() || !row[2].IsNull() {
		t.Fatalf("cloneRowOwnedPrefix(.., 1) = %v, want [1 NULL NULL]", row)
	}
	live := cloneRowOwnedLive(Row{NewIntDatum(1), NewIntDatum(2), NewIntDatum(3)}, []bool{false, true})
	if !live[0].IsNull() || live[1].Int != 2 || !live[2].IsNull() {
		t.Fatalf("cloneRowOwnedLive(.., [f t]) = %v, want [NULL 2 NULL]", live)
	}
}
