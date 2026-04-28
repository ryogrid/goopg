package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// TestAnalyzeRelationPopulatesStats pins that running ANALYZE
// against a populated table writes RowCount / AvgWidth /
// per-column NDistinct + NullFrac into the
// catalog.TableStats that analyzeRelation returns. The test
// seeds 7 rows across 3 distinct labels; the result should be
// RowCount=7, Columns[1].NDistinct=3.
func TestAnalyzeRelationPopulatesStats(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})

	// Seed 7 rows: 3 distinct labels (a×2, b×2, c×3).
	insertPlan := &planner.Insert{
		Table: tbl,
		Source: &planner.Values{
			Rows: [][]planner.Expr{
				{&planner.IntegerConst{Value: 1}, &planner.StringConst{Value: "a"}},
				{&planner.IntegerConst{Value: 2}, &planner.StringConst{Value: "a"}},
				{&planner.IntegerConst{Value: 3}, &planner.StringConst{Value: "b"}},
				{&planner.IntegerConst{Value: 4}, &planner.StringConst{Value: "b"}},
				{&planner.IntegerConst{Value: 5}, &planner.StringConst{Value: "c"}},
				{&planner.IntegerConst{Value: 6}, &planner.StringConst{Value: "c"}},
				{&planner.IntegerConst{Value: 7}, &planner.StringConst{Value: "c"}},
			},
		},
		ColumnIndex: []int{0, 1},
	}
	op, err := Build(insertPlan)
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := op.Next(); err != EOF {
		t.Fatalf("Insert.Next: %v", err)
	}
	_ = op.Close()

	// Commit the seeding transaction so analyzeRelation's
	// fresh snapshot can see the rows. Production ANALYZE
	// runs against committed data; using the fixture's
	// in-progress tx would shadow the rows under
	// ReadCommitted.
	if err := ctx.TxnMgr.Commit(ctx.Tx); err != nil {
		t.Fatal(err)
	}

	stats, err := analyzeRelation(ctx.Pool, ctx.TxnMgr, ctx.Catalog, tbl)
	if err != nil {
		t.Fatalf("analyzeRelation: %v", err)
	}
	if stats.RowCount != 7 {
		t.Errorf("RowCount=%d want 7", stats.RowCount)
	}
	if len(stats.Columns) != 2 {
		t.Fatalf("Columns len=%d want 2", len(stats.Columns))
	}
	if got := stats.Columns[0].NDistinct; got != 7 {
		t.Errorf("id NDistinct=%d want 7", got)
	}
	if got := stats.Columns[1].NDistinct; got != 3 {
		t.Errorf("label NDistinct=%d want 3", got)
	}
	if stats.AvgWidth <= 0 {
		t.Errorf("AvgWidth=%v want > 0", stats.AvgWidth)
	}
}
