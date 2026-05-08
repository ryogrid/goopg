package executor

import (
	"math/rand"
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
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
	if _, err := NextRow(op); err != EOF {
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

// seedRowsAndAnalyze is a small helper: insert N rows shaped by
// makeRow, commit, then reservoir-sample with a deterministic seed
// + the given statsTarget. Returns the resulting TableStats.
func seedRowsAndAnalyze(t *testing.T, n int, makeRow func(i int) []planner.Expr, statsTarget int) (*Context, *catalog.TableStats) {
	t.Helper()
	ctx, cat, cleanup := newStorageFixture(t)
	t.Cleanup(cleanup)
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})

	rows := make([][]planner.Expr, n)
	for i := 0; i < n; i++ {
		rows[i] = makeRow(i)
	}
	insertPlan := &planner.Insert{
		Table:       tbl,
		Source:      &planner.Values{Rows: rows},
		ColumnIndex: []int{0, 1},
	}
	op, err := Build(insertPlan)
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := NextRow(op); err != EOF {
		t.Fatalf("Insert.Next: %v", err)
	}
	_ = op.Close()
	if err := ctx.TxnMgr.Commit(ctx.Tx); err != nil {
		t.Fatal(err)
	}

	target := statsTarget
	if target <= 0 {
		target = upstreamDefaultStatsTarget
	}
	stats, err := analyzeRelationWith(ctx.Pool, ctx.TxnMgr, ctx.Catalog, tbl, target, rand.New(rand.NewSource(42)))
	if err != nil {
		t.Fatalf("analyzeRelationWith: %v", err)
	}
	return ctx, stats
}

// TestAnalyzeBuildsMCVForSkewedColumn pins that a column whose
// distribution is dominated by a single value lands that value in
// the MCV slot at roughly the right frequency. 800/150/50 split
// across 'F'/'O'/'P' should produce 'F' as MCV[0] with frequency
// ~0.8 (sample is 100% of the table since N=1000 < targrows).
func TestAnalyzeBuildsMCVForSkewedColumn(t *testing.T) {
	makeRow := func(i int) []planner.Expr {
		var label string
		switch {
		case i < 800:
			label = "F"
		case i < 950:
			label = "O"
		default:
			label = "P"
		}
		return []planner.Expr{
			&planner.IntegerConst{Value: int64(i)},
			&planner.StringConst{Value: label},
		}
	}
	_, stats := seedRowsAndAnalyze(t, 1000, makeRow, 0)

	if stats.RowCount != 1000 {
		t.Errorf("RowCount=%d want 1000", stats.RowCount)
	}
	mcv := stats.Columns[1].MCV
	if len(mcv) == 0 {
		t.Fatalf("expected MCV list, got none")
	}
	if mcv[0].Value != "F" {
		t.Errorf("MCV[0].Value=%q want %q", mcv[0].Value, "F")
	}
	if mcv[0].Frequency < 0.78 || mcv[0].Frequency > 0.82 {
		t.Errorf("MCV[0].Frequency=%v want ~0.8", mcv[0].Frequency)
	}
}

// TestAnalyzeBuildsHistogramForOrderedColumn pins the equi-depth
// histogram contract on a uniformly-distributed numeric column:
// boundaries are strictly ascending and span the value range.
func TestAnalyzeBuildsHistogramForOrderedColumn(t *testing.T) {
	makeRow := func(i int) []planner.Expr {
		return []planner.Expr{
			&planner.IntegerConst{Value: int64(i + 1)}, // 1..1000
			&planner.StringConst{Value: "x"},
		}
	}
	_, stats := seedRowsAndAnalyze(t, 1000, makeRow, 10)

	hist := stats.Columns[0].Histogram
	if len(hist) < 2 {
		t.Fatalf("histogram=%v want >= 2 boundaries", hist)
	}
	parsed := make([]int, len(hist))
	for i, s := range hist {
		v, err := strconv.Atoi(s)
		if err != nil {
			t.Fatalf("histogram[%d]=%q not an integer: %v", i, s, err)
		}
		parsed[i] = v
	}
	if parsed[0] > 200 {
		// First boundary should land in the low end of 1..1000
		// for a uniform distribution with ~10 buckets.
		t.Errorf("histogram first=%d want <= 200", parsed[0])
	}
	if parsed[len(parsed)-1] < 800 {
		t.Errorf("histogram last=%d want >= 800", parsed[len(parsed)-1])
	}
	for i := 1; i < len(parsed); i++ {
		if parsed[i] <= parsed[i-1] {
			t.Errorf("histogram not strictly ascending at %d: %d <= %d", i, parsed[i], parsed[i-1])
		}
	}
}

// TestAnalyzeRespectsStatsTarget pins that the StatsTarget passed
// into analyzeRelationWith scales sample size as
// targrows = target * 300. With a target of 1, only the first 300
// rows enter the reservoir; with target 0 in Context, the upstream
// default of 100 → 30000 kicks in (which exceeds N=400, so the
// whole table is sampled).
func TestAnalyzeRespectsStatsTarget(t *testing.T) {
	makeRow := func(i int) []planner.Expr {
		return []planner.Expr{
			&planner.IntegerConst{Value: int64(i + 1)},
			&planner.StringConst{Value: "x"},
		}
	}

	// target=1 → reservoir cap 300; full table is 400 rows.
	// With reservoir sampling, the per-id sample count is ≤300,
	// so NDistinct(id) ≤ 300 even though every row's id is
	// unique.
	_, smallStats := seedRowsAndAnalyze(t, 400, makeRow, 1)
	if smallStats.RowCount != 400 {
		t.Errorf("RowCount=%d want 400", smallStats.RowCount)
	}
	if smallStats.Columns[0].NDistinct > 300 {
		t.Errorf("with statsTarget=1, NDistinct(id)=%d want <=300", smallStats.Columns[0].NDistinct)
	}

	// target=upstream-default (100*300=30000), N=400 → full
	// sample, NDistinct(id) is exact.
	_, fullStats := seedRowsAndAnalyze(t, 400, makeRow, 0)
	if fullStats.Columns[0].NDistinct != 400 {
		t.Errorf("with default statsTarget, NDistinct(id)=%d want 400", fullStats.Columns[0].NDistinct)
	}
}
