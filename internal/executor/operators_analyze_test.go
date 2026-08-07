package executor

import (
	"math"
	"math/rand"
	"strconv"
	"strings"
	"testing"
	"time"

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
	if _, err := op.Next(); err != EOF {
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
	stats, err := analyzeRelationWith(ctx.Pool, ctx.TxnMgr, ctx.Catalog, tbl, target, rand.New(rand.NewSource(42)), ctx.MultiXact, ctx)
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
	// With reservoir sampling only 300 of the 400 ids are SEEN, but every
	// one of them is seen exactly once, so upstream's `nmultiple == 0` arm
	// declares the column unique and scales to the relation: 400, not the
	// sample's 300. That scale-up is the whole point of M0127-P5.6-e-iii —
	// before it, a 1.5 M-row unique key reported the 30 000-row sample's
	// count and every join above it divided by a number 50× too small.
	_, smallStats := seedRowsAndAnalyze(t, 400, makeRow, 1)
	if smallStats.RowCount != 400 {
		t.Errorf("RowCount=%d want 400", smallStats.RowCount)
	}
	if got := smallStats.Columns[0].NDistinct; got != 400 {
		t.Errorf("with statsTarget=1, NDistinct(id)=%d want 400 (Haas-Stokes unique-column arm)", got)
	}

	// target=upstream-default (100*300=30000), N=400 → full
	// sample, NDistinct(id) is exact.
	_, fullStats := seedRowsAndAnalyze(t, 400, makeRow, 0)
	if fullStats.Columns[0].NDistinct != 400 {
		t.Errorf("with default statsTarget, NDistinct(id)=%d want 400", fullStats.Columns[0].NDistinct)
	}
}

// TestAnalyzeRespectsPerColumnStatTarget pins that
// `ALTER TABLE ... ALTER COLUMN ... SET STATISTICS n`
// (catalog.Column.StatTarget) overrides the table-wide target for that one
// column, mirroring upstream's examine_attribute/do_analyze_rel
// (postgres/src/backend/commands/analyze.c): the histogram bucket count for
// the overridden column tracks the override, not the ambient table-wide
// target, and a sibling column with no override still uses the table-wide
// target.
func TestAnalyzeRespectsPerColumnStatTarget(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})

	// SET STATISTICS 5 on column 0 ("id"); column 1 ("label") keeps the
	// table-wide target of 100.
	override := 5
	tbl.Columns[0].StatTarget = &override

	rows := make([][]planner.Expr, 1000)
	for i := 0; i < 1000; i++ {
		rows[i] = []planner.Expr{
			&planner.IntegerConst{Value: int64(i + 1)}, // 1..1000, unique
			&planner.StringConst{Value: "x"},
		}
	}
	insertPlan := &planner.Insert{Table: tbl, Source: &planner.Values{Rows: rows}, ColumnIndex: []int{0, 1}}
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
	if err := ctx.TxnMgr.Commit(ctx.Tx); err != nil {
		t.Fatal(err)
	}

	stats, err := analyzeRelationWith(ctx.Pool, ctx.TxnMgr, ctx.Catalog, tbl, upstreamDefaultStatsTarget, rand.New(rand.NewSource(42)), ctx.MultiXact, ctx)
	if err != nil {
		t.Fatalf("analyzeRelationWith: %v", err)
	}

	// Column 0's histogram is capped by the override (5 buckets ⇒ at most
	// 6 boundaries), not the table-wide target of 100.
	if hist := stats.Columns[0].Histogram; len(hist) > 6 {
		t.Errorf("id histogram len=%d want <=6 (SET STATISTICS 5 override)", len(hist))
	}

	// Column 1 (uniform single value "x", no override) is unaffected by
	// the override on column 0; NDistinct must still reflect the sample.
	if got := stats.Columns[1].NDistinct; got != 1 {
		t.Errorf("label NDistinct=%d want 1", got)
	}
}

// TestAnalyzeSetStatisticsZeroDisablesColumn pins that
// `SET STATISTICS 0` (catalog.Column.StatTarget == 0) excludes the column
// from ANALYZE entirely, mirroring upstream's examine_attribute returning
// NULL for attstattarget == 0: the column's ColumnStats stays the zero
// value.
func TestAnalyzeSetStatisticsZeroDisablesColumn(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})

	zero := 0
	tbl.Columns[1].StatTarget = &zero // disable stats on "label"

	rows := make([][]planner.Expr, 10)
	for i := 0; i < 10; i++ {
		rows[i] = []planner.Expr{
			&planner.IntegerConst{Value: int64(i + 1)},
			&planner.StringConst{Value: "a"},
		}
	}
	insertPlan := &planner.Insert{Table: tbl, Source: &planner.Values{Rows: rows}, ColumnIndex: []int{0, 1}}
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
	if err := ctx.TxnMgr.Commit(ctx.Tx); err != nil {
		t.Fatal(err)
	}

	stats, err := analyzeRelationWith(ctx.Pool, ctx.TxnMgr, ctx.Catalog, tbl, upstreamDefaultStatsTarget, rand.New(rand.NewSource(42)), ctx.MultiXact, ctx)
	if err != nil {
		t.Fatalf("analyzeRelationWith: %v", err)
	}
	if got := stats.Columns[1]; got.NDistinct != 0 || got.NullFrac != 0 || len(got.MCV) != 0 || len(got.Histogram) != 0 {
		t.Errorf("label ColumnStats=%+v want zero value (SET STATISTICS 0)", got)
	}
	// Column 0 (no override) still gets real stats.
	if got := stats.Columns[0].NDistinct; got != 10 {
		t.Errorf("id NDistinct=%d want 10", got)
	}
}

// TestColumnNDistinctOverride pins the value-parsing contract of the
// `n_distinct` attribute option, mirroring upstream's stadistinct convention
// (postgres/src/backend/utils/adt/selfuncs.c get_variable_numdistinct): a
// positive value is an absolute distinct count, a value in [-1, 0) is a
// fraction of the row count, 0/unset/other options are no-ops, and an
// out-of-range negative value is clamped to -1.
func TestColumnNDistinctOverride(t *testing.T) {
	const rows = 1000
	cases := []struct {
		name    string
		options []string
		wantND  int64
		wantOK  bool
	}{
		{"absolute", []string{"n_distinct=5"}, 5, true},
		{"absolute-rounds", []string{"n_distinct=7.6"}, 8, true},
		{"fraction-half", []string{"n_distinct=-0.5"}, 500, true},
		{"fraction-all-distinct", []string{"n_distinct=-1"}, 1000, true},
		{"fraction-tiny-floors-at-one", []string{"n_distinct=-0.0000001"}, 1, true},
		{"below-range-clamps-to-minus-one", []string{"n_distinct=-2"}, 1000, true},
		{"zero-is-no-op", []string{"n_distinct=0"}, 0, false},
		{"unset", nil, 0, false},
		{"other-option-ignored", []string{"foo=3"}, 0, false},
		{"case-insensitive-key", []string{"N_Distinct=5"}, 5, true},
		{"inherited-flavor-not-honored", []string{"n_distinct_inherited=5"}, 0, false},
		{"malformed-value-is-no-op", []string{"n_distinct=abc"}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			col := &catalog.Column{Options: tc.options}
			nd, ok := columnNDistinctOverride(col, rows)
			if ok != tc.wantOK || nd != tc.wantND {
				t.Errorf("columnNDistinctOverride(%v)=(%d,%v) want (%d,%v)", tc.options, nd, ok, tc.wantND, tc.wantOK)
			}
		})
	}
}

// TestAnalyzeRespectsNDistinctOption pins that a per-column `n_distinct`
// attribute option (set via `ALTER TABLE ... ALTER COLUMN ... SET (n_distinct
// = <v>)`, stored on catalog.Column.Options) overrides the ANALYZE-computed
// NDistinct that the planner later consults, mirroring upstream's override in
// do_analyze_rel (postgres/src/backend/commands/analyze.c:571-581). Column 0
// carries an absolute override, column 1 has none and keeps its real sampled
// value.
func TestAnalyzeRespectsNDistinctOption(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})

	// SET (n_distinct = 5) on column 0 ("id"), which is otherwise fully
	// unique (1000 distinct ids). Column 1 ("label") is a single value.
	tbl.Columns[0].Options = []string{"n_distinct=5"}

	rows := make([][]planner.Expr, 1000)
	for i := 0; i < 1000; i++ {
		rows[i] = []planner.Expr{
			&planner.IntegerConst{Value: int64(i + 1)}, // 1..1000, unique
			&planner.StringConst{Value: "x"},
		}
	}
	insertPlan := &planner.Insert{Table: tbl, Source: &planner.Values{Rows: rows}, ColumnIndex: []int{0, 1}}
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
	if err := ctx.TxnMgr.Commit(ctx.Tx); err != nil {
		t.Fatal(err)
	}

	stats, err := analyzeRelationWith(ctx.Pool, ctx.TxnMgr, ctx.Catalog, tbl, upstreamDefaultStatsTarget, rand.New(rand.NewSource(42)), ctx.MultiXact, ctx)
	if err != nil {
		t.Fatalf("analyzeRelationWith: %v", err)
	}

	// Column 0's NDistinct is the manual override (5), not the sampled ~1000.
	if got := stats.Columns[0].NDistinct; got != 5 {
		t.Errorf("id NDistinct=%d want 5 (n_distinct=5 override)", got)
	}
	// Column 1 (no override, single value) still reflects the sample.
	if got := stats.Columns[1].NDistinct; got != 1 {
		t.Errorf("label NDistinct=%d want 1", got)
	}
}

// TestDatumVariablePayloadWidth pins the per-Datum variable-width byte count
// used by computeColumnStats to derive AvgWidth (M0128-P3.1).
func TestDatumVariablePayloadWidth(t *testing.T) {
	tests := []struct {
		name string
		d    Datum
		want int
	}{
		{"int", NewIntDatum(42), 0},
		{"float", NewIntDatum(int64(math.Float64bits(3.14))), 0},
		{"bool", NewBoolDatum(true), 0},
		{"null", NullDatum, 0},
		{"string empty", NewStringDatum(""), 0},
		{"string short", NewStringDatum("hello"), 5},
		{"string long", NewStringDatum(strings.Repeat("x", 2000)), 2000},
		{"bytes empty", NewBytesDatum(nil), 0},
		{"bytes small", NewBytesDatum([]byte{1, 2, 3}), 3},
		{"numeric fast", NewNumericInt64Datum(12345, 0), 0}, // int64 fast-path, no Buf
		{"time", NewTimeDatum(time.Unix(1, 0)), 0},
		{"date", NewDateDatum(time.Date(2026, time.August, 7, 0, 0, 0, 0, time.UTC)), 0},
		{"interval", NewIntervalDatum(0, 0), 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := datumVariablePayloadWidth(tc.d)
			if got != tc.want {
				t.Errorf("datumVariablePayloadWidth(%v) = %d, want %d", tc.name, got, tc.want)
			}
		})
	}
}

// TestAnalyzePopulatesAvgWidth pins that computeColumnStats calculates
// per-column AvgWidth from sampled non-null Datum values (M0128-P3.1).
// It tests the computation directly rather than through the full
// insert→heap→decode pipeline, so it controls the Datum shapes precisely.
func TestAnalyzePopulatesAvgWidth(t *testing.T) {
	// Build a sample of 20 rows: column 0 is fixed-width (int), column 1 is
	// variable-width text with known byte lengths.
	sample := make([]Row, 20)
	for i := 0; i < 20; i++ {
		sample[i] = Row{
			NewIntDatum(int64(i + 1)),                        // fixed-width: contributes 0
			NewStringDatum(strings.Repeat("x", (i+1)*10)),    // 10, 20, …, 200 bytes
		}
	}
	// Add one null row to verify nulls don't affect the average.
	sample = append(sample, Row{NullDatum, NullDatum})

	stats := computeColumnStats(sample, 0, 100, 21, nil)
	if stats.AvgWidth != 0 {
		t.Errorf("col 0 (fixed-width int): AvgWidth=%v, want 0", stats.AvgWidth)
	}

	stats1 := computeColumnStats(sample, 1, 100, 21, nil)
	// 20 values: 10, 20, …, 200 bytes; avg = (10+200)*20/2/20 = 105.
	if stats1.AvgWidth < 90 || stats1.AvgWidth > 120 {
		t.Errorf("col 1 (text 10–200B): AvgWidth=%v, want ~105", stats1.AvgWidth)
	}

	// A sample with only nulls: AvgWidth = 0.
	nullSample := []Row{{NullDatum}, {NullDatum}, {NullDatum}}
	nullStats := computeColumnStats(nullSample, 0, 100, 3, nil)
	if nullStats.AvgWidth != 0 {
		t.Errorf("all-null column: AvgWidth=%v, want 0", nullStats.AvgWidth)
	}

	// An empty sample: AvgWidth = 0.
	emptyStats := computeColumnStats(nil, 0, 100, 0, nil)
	if emptyStats.AvgWidth != 0 {
		t.Errorf("empty sample: AvgWidth=%v, want 0", emptyStats.AvgWidth)
	}
}
