package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/utils/mmgr"
)

// M0122-0012 slice 71 guards: a sort whose output only consumer-complete
// readers see stores just the columns they and its keys read, a Gather
// Merge's partial plan is built for its readers, and VirtualSlot.Materialize
// detaches every cell in one pass.

func TestVirtualSlotMaterializeOwnsEveryCell(t *testing.T) {
	prod := mmgr.Acquire(nil, mmgr.KindStmt)
	defer prod.Release()
	left := &MaterializedSlot{row: Row{NewIntDatum(1), cut1ArenaString(t, prod, "arena")}}
	right := &MaterializedSlot{row: Row{NullDatum, NewStringDatum("owned")}}
	vs := &VirtualSlot{sources: []TupleSlot{left, right}, cols: []virtualCol{{1, 1}, {0, 1}, {0, 0}, {1, 0}}}
	want := cloneRowOwned(vs.Row())
	got := vs.Materialize().Row()
	prod.Reset()
	if err := AssertTransferable(got); err != nil {
		t.Fatalf("Materialize left an arena reference: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("width %d, want %d", len(got), len(want))
	}
	for i := range want {
		if !sameResult(got[i], nil, want[i], nil) {
			t.Fatalf("cell %d = %q, want %q", i, got[i].Format(), want[i].Format())
		}
	}
	if got[1].StringValue() != "arena" {
		t.Fatalf("arena cell after the arena's reset = %q", got[1].StringValue())
	}
}

func TestSortForConsumerCompleteReadersStoresLiveColumns(t *testing.T) {
	plan := &optimizer.Aggregate{
		Mode: optimizer.AggModeSimple,
		Child: &optimizer.GatherMerge{
			Keys:  []optimizer.SortKey{{Expr: deformCol(5)}},
			Child: &optimizer.Sort{Keys: []optimizer.SortKey{{Expr: deformCol(5)}}, Child: &optimizer.SeqScan{Table: deformTable(16)}},
		},
		GroupExprs: []optimizer.Expr{deformCol(3)},
		Aggs:       []optimizer.AggregateCall{{Name: "sum", Arg: deformCol(11), Type: catalog.Type{Name: "int8"}}},
	}
	g := mustBuildDeform(t, plan).(*aggregateOp).child.(*gatherMergeOp)
	worker, err := g.buildChild(nil)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := worker.(*sortOp)
	if !ok {
		t.Fatalf("worker tree root is %T, want the Sort", worker)
	}
	if got := liveIndexes(s.live); len(got) != 3 || got[0] != 3 || got[1] != 5 || got[2] != 11 {
		t.Fatalf("worker sort live columns %v, want [3 5 11]", got)
	}
	// Its only reader is the Gather Merge, which copies every row it pulls:
	// the sort keeps compact rows (M0122-0012 slice 83).
	if !s.compactOut {
		t.Fatal("worker sort under a Gather Merge is not marked compactOut")
	}
	// A Gather Merge built outside a consumer-complete parent keeps the
	// partial plan as it was: the sort stores every column.
	plain := mustBuildDeform(t, plan.Child).(*gatherMergeOp)
	worker, err = plain.buildChild(nil)
	if err != nil {
		t.Fatal(err)
	}
	if live := worker.(*sortOp).live; live != nil {
		t.Fatalf("top-level Gather Merge's worker sort live columns %v, want nil", liveIndexes(live))
	}
}

func TestSortWithLiveIngestionSortsLiveCells(t *testing.T) {
	old := seqScanDeformPoison
	seqScanDeformPoison = false
	t.Cleanup(func() { seqScanDeformPoison = old })
	rows := []Row{
		{NewIntDatum(3), NewStringDatum("c"), NewIntDatum(30)},
		{NewIntDatum(1), NewStringDatum("a"), NewIntDatum(10)},
		{NewIntDatum(2), NewStringDatum("b"), NewIntDatum(20)},
	}
	s := newSortOp(&optimizer.Sort{Keys: []optimizer.SortKey{{Expr: deformCol(0)}}}, &rowsOp{rows: rows})
	s.live = []bool{true, false, true}
	if err := s.Open(&Context{}); err != nil {
		t.Fatal(err)
	}
	for want := int64(1); want <= 3; want++ {
		slot, err := s.Next()
		if err != nil {
			t.Fatal(err)
		}
		r := slot.Row()
		if len(r) != 3 || r[0].Int != want || r[2].Int != want*10 || r[1].Kind != KindNull {
			t.Fatalf("row %d = %v, want key %d, live third cell %d, dead middle cell NULL", want, r, want, want*10)
		}
	}
	if _, err := s.Next(); err != EOF {
		t.Fatalf("want EOF, got %v", err)
	}
}
