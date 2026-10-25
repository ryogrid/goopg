package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestGatherMergeCompactTransferMatchesFullRows pins M0122-0012 slice 88: a
// Gather Merge whose parent is a borrowing aggregate (aggregateBorrowsGatherRows)
// ships compact rows — only the live columns — and the leader expands each
// into one of its source's two reused scratch rows. A sorted GroupAggregate
// over a Gather Merge → Sort → Seq Scan of a wide table must return exactly
// the rows the full-width transfer returns (borrow off), with workers and
// with the leader alone; and the compact path must actually be armed.
func TestGatherMergeCompactTransferMatchesFullRows(t *testing.T) {
	ctx := deformW8Fixture(t)
	runSQL(t, ctx, `CREATE TABLE gmc (k int, pad1 text, a int, pad2 text, b numeric, pad3 text, c int, s text)`)
	runSQL(t, ctx, `INSERT INTO gmc SELECT x % 97, repeat('p', 40), x, md5(x::text), x::numeric / 7, repeat('q', 30),
		CASE WHEN x % 11 = 0 THEN NULL ELSE x % 13 END, 'v' || (x % 31)
		FROM generate_series(1, 6000) x`)
	stmts, err := parser.Parse("SELECT * FROM gmc")
	if err != nil {
		t.Fatal(err)
	}
	col := func(i int, typ string) *optimizer.ColumnRef {
		return &optimizer.ColumnRef{Index: i, Name: "c", Type: catalog.Type{Name: typ}}
	}
	plan := func(workers int) *optimizer.Aggregate {
		scan, err := optimizer.Plan(stmts[0], ctx.Catalog)
		if err != nil {
			t.Fatal(err)
		}
		keys := []optimizer.SortKey{{Expr: col(0, "int4")}}
		return &optimizer.Aggregate{
			Strategy:   optimizer.AggStrategySorted,
			Child:      optimizer.NewGatherMerge(0, &optimizer.Sort{Keys: keys, Child: scan}, workers, keys),
			GroupExprs: []optimizer.Expr{col(0, "int4")},
			Aggs: []optimizer.AggregateCall{
				{Name: "count", Star: true, Type: catalog.Type{Name: "int8"}},
				{Name: "sum", Arg: col(2, "int4"), Type: catalog.Type{Name: "int8"}},
				{Name: "max", Arg: col(4, "numeric"), Type: catalog.Type{Name: "numeric"}},
				{Name: "avg", Arg: col(6, "int4"), Type: catalog.Type{Name: "numeric"}},
				{Name: "min", Arg: col(7, "text"), Type: catalog.Type{Name: "text"}},
			},
		}
	}
	run := func(workers int, wantCompact bool) string {
		advanceStmtCounter(ctx)
		op, err := Build(plan(workers))
		if err != nil {
			t.Fatal(err)
		}
		ctx.MaxParallelWorkers = 8
		ctx.ParallelLeaderParticipation = true
		if err := op.Open(ctx); err != nil {
			t.Fatal(err)
		}
		var gm *gatherMergeOp
		if a := unwrapAggregateOp(op); a != nil {
			child := a.child
			if io, ok := child.(*instrumentedOp); ok {
				child = io.inner
			}
			gm, _ = child.(*gatherMergeOp)
		}
		if gm == nil || (gm.liveIdx != nil) != wantCompact {
			t.Fatalf("workers=%d: compact transfer armed=%v, want %v", workers, gm != nil && gm.liveIdx != nil, wantCompact)
		}
		var rows []Row
		for {
			slot, err := op.Next()
			if err == EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			rows = append(rows, slot.Row())
		}
		_ = op.Close()
		return strings.Join(sortedCells(rows), "|")
	}
	for _, workers := range []int{0, 1, 3} {
		got := run(workers, true)
		scanBorrowDisabled = true
		want := run(workers, false)
		scanBorrowDisabled = false
		if got != want {
			t.Fatalf("workers=%d:\ncompact %.300s\nfull    %.300s", workers, got, want)
		}
		if strings.Count(got, "|") != 96 {
			t.Fatalf("workers=%d: %d groups, want 97", workers, strings.Count(got, "|")+1)
		}
	}
}

// TestAggregateBorrowsGatherRows pins which aggregates hand a Gather Merge
// the borrow: the scan rule's plain aggregates, now with the sorted strategy
// and passthrough columns (a Gather Merge row's values are already detached),
// and still not DISTINCT, ordered, user or Finalize aggregates.
func TestAggregateBorrowsGatherRows(t *testing.T) {
	arg := deformCol(1)
	for name, c := range map[string]struct {
		agg  optimizer.Aggregate
		want bool
	}{
		"sorted sum":  {optimizer.Aggregate{Strategy: optimizer.AggStrategySorted, Aggs: []optimizer.AggregateCall{{Name: "sum", Arg: arg}}}, true},
		"passthrough": {optimizer.Aggregate{Passthrough: []optimizer.Expr{arg}, Aggs: []optimizer.AggregateCall{{Name: "max", Arg: arg}}}, true},
		"distinct":    {optimizer.Aggregate{Aggs: []optimizer.AggregateCall{{Name: "count", Arg: arg, Distinct: true}}}, false},
		"ordered":     {optimizer.Aggregate{Aggs: []optimizer.AggregateCall{{Name: "sum", Arg: arg, OrderBy: []optimizer.SortKey{{Expr: arg}}}}}, false},
		"array_agg":   {optimizer.Aggregate{Aggs: []optimizer.AggregateCall{{Name: "array_agg", Arg: arg}}}, false},
		"finalize":    {optimizer.Aggregate{Mode: optimizer.AggModeFinal, Aggs: []optimizer.AggregateCall{{Name: "sum", Arg: arg}}}, false},
	} {
		if got := aggregateBorrowsGatherRows(&c.agg); got != c.want {
			t.Fatalf("%s: borrows=%v, want %v", name, got, c.want)
		}
	}
}
