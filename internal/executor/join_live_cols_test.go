package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// M0122-0012 slice 66 guards: a consumer-complete parent's liveness crosses a
// Project and a Join's left side, so a Gather on the join's probe side ships
// only what the aggregate, the projection and the join itself read. TPC-DS
// Q39's HashAggregate → Project → Hash Join → Gather shipped every column of
// its inventory × date_dim × warehouse rows.

// liveJoinPlan is Aggregate → Project → Join(Gather(scan of 8), scan of 4).
// The join's equality reads left column 2 and right column 1 (merged 9), its
// residual left column 4 and right column 2 (merged 10); the projection's targets read merged 5 (left), 11 (right) and 0 (left), of
// which the aggregate reads the first two only.
func liveJoinPlan(typ optimizer.JoinType) *optimizer.Aggregate {
	join := (&optimizer.Join{
		Type:  typ,
		Algo:  optimizer.JoinAlgoHash,
		Left:  &optimizer.Gather{Child: &optimizer.SeqScan{Table: deformTable(8)}},
		Right: &optimizer.SeqScan{Table: deformTable(4)},
		Predicate: &optimizer.BinaryOp{Op: parser.OpAnd,
			Left:  &optimizer.BinaryOp{Op: parser.OpEq, Left: deformCol(2), Right: deformCol(9)},
			Right: deformLt(deformCol(4), deformCol(10))},
		LeftKey:  deformCol(2),
		RightKey: deformCol(1),
	}).WithSchemaForTest(make(optimizer.Schema, 12))
	project := &optimizer.Project{Child: join, Targets: []optimizer.Expr{deformCol(5), deformCol(11), deformCol(0)}}
	return &optimizer.Aggregate{
		Mode:       optimizer.AggModeSimple,
		Child:      project,
		GroupExprs: []optimizer.Expr{deformCol(0)},
		Aggs:       []optimizer.AggregateCall{{Name: "sum", Arg: deformCol(1), Type: catalog.Type{Name: "int8"}}},
	}
}

// gatherUnderJoin digs the Gather out of Aggregate → Project → Join.
func gatherUnderJoin(t *testing.T, op Operator) *gatherOp {
	t.Helper()
	a, ok := op.(*aggregateOp)
	if !ok {
		t.Fatalf("root is %T", op)
	}
	p, ok := a.child.(*projectOp)
	if !ok {
		t.Fatalf("aggregate child is %T", a.child)
	}
	j, ok := p.child.(*joinOp)
	if !ok {
		t.Fatalf("project child is %T", p.child)
	}
	g, ok := j.left.(*gatherOp)
	if !ok {
		t.Fatalf("join left is %T", j.left)
	}
	return g
}

func TestGatherUnderJoinShipsLiveColumns(t *testing.T) {
	for _, typ := range []optimizer.JoinType{optimizer.JoinTypeInner, optimizer.JoinTypeLeft} {
		got := liveIndexes(gatherUnderJoin(t, mustBuildDeform(t, liveJoinPlan(typ))).liveCols)
		// 2: the join key; 4: the residual; 5: the aggregate's group
		// column. 0 is a dead projection target, 11 a right-side column.
		if len(got) != 3 || got[0] != 2 || got[1] != 4 || got[2] != 5 {
			t.Fatalf("type %v: Gather live columns %v, want [2 4 5]", typ, got)
		}
	}
}

func TestGatherUnderJoinKeepsEveryColumnWhenUnsure(t *testing.T) {
	cases := map[string]func(*optimizer.Aggregate){
		// A semi join's output is its left row alone, not the merged layout.
		"semi": func(a *optimizer.Aggregate) { joinOf(a).Type = optimizer.JoinTypeSemi },
		// A lateral inner reads the outer row through its own references.
		"lateral": func(a *optimizer.Aggregate) { joinOf(a).Lateral = true },
		// A layout the side widths do not account for cannot be split.
		"layout": func(a *optimizer.Aggregate) { joinOf(a).WithSchemaForTest(make(optimizer.Schema, 13)) },
		// A predicate reading the row in a way the walk cannot enumerate.
		"sublink": func(a *optimizer.Aggregate) { joinOf(a).Predicate = &optimizer.SubqueryExpr{} },
	}
	for name, mutate := range cases {
		plan := liveJoinPlan(optimizer.JoinTypeInner)
		mutate(plan)
		op, err := Build(plan)
		if err != nil {
			t.Fatalf("%s: Build: %v", name, err)
		}
		if live := gatherUnderJoin(t, op).liveCols; live != nil {
			t.Fatalf("%s: Gather live columns %v, want nil (every column)", name, liveIndexes(live))
		}
	}
}

func joinOf(a *optimizer.Aggregate) *optimizer.Join {
	return a.Child.(*optimizer.Project).Child.(*optimizer.Join)
}

func TestRowChunkRowsAreIndependent(t *testing.T) {
	var c rowChunk
	rows := make([]Row, 0, rowChunkRows+3)
	for i := range rowChunkRows + 3 {
		r := c.take(3)
		if len(r) != 3 || cap(r) != 3 {
			t.Fatalf("row %d: len %d cap %d, want 3 and 3", i, len(r), cap(r))
		}
		for j := range r {
			if r[j].Kind != KindNull {
				t.Fatalf("row %d cell %d is %v, want NULL", i, j, r[j])
			}
		}
		r[0] = NewIntDatum(int64(i))
		rows = append(rows, r)
	}
	// An append on one row reallocates instead of overwriting its neighbour.
	_ = append(rows[0], NewIntDatum(-1))
	for i, r := range rows {
		if r[0].Int != int64(i) || r[1].Kind != KindNull {
			t.Fatalf("row %d = %v, a neighbour's write reached it", i, r)
		}
	}
	var none *rowChunk
	if r := none.take(2); len(r) != 2 {
		t.Fatalf("nil chunk row width %d, want 2", len(r))
	}
}

func TestTransferRowForQueueLiveReadsLiveCellsOnly(t *testing.T) {
	old := seqScanDeformPoison
	seqScanDeformPoison = false
	t.Cleanup(func() { seqScanDeformPoison = old })
	left := &MaterializedSlot{row: Row{NewIntDatum(1), NewStringDatum("keep")}}
	right := &MaterializedSlot{row: Row{NewStringDatum("drop"), NewIntDatum(4)}}
	vs := &VirtualSlot{
		sources: []TupleSlot{left, right},
		cols:    []virtualCol{{0, 0}, {0, 1}, {1, 0}, {1, 1}},
	}
	var c rowChunk
	for name, slot := range map[string]TupleSlot{"virtual": vs, "materialized": &MaterializedSlot{row: vs.Row()}} {
		got := transferRowForQueueLive(slot, []bool{false, true, false, true}, &c)
		if len(got) != 4 || got[1].StringValue() != "keep" || got[3].Int != 4 {
			t.Fatalf("%s: row %v, want the live source values in place", name, got)
		}
		if got[0].Kind != KindNull || got[2].Kind != KindNull {
			t.Fatalf("%s: row %v, want dead cells NULL", name, got)
		}
	}
}
