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
	"github.com/goopg/goopg/internal/optimizer"
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
	insertPlan := &optimizer.Insert{
		Table: tbl,
		Source: &optimizer.Values{
			Rows: [][]optimizer.Expr{
				{&optimizer.IntegerConst{Value: 1}, &optimizer.StringConst{Value: "a"}},
				{&optimizer.IntegerConst{Value: 2}, &optimizer.StringConst{Value: "a"}},
				{&optimizer.IntegerConst{Value: 3}, &optimizer.StringConst{Value: "b"}},
				{&optimizer.IntegerConst{Value: 4}, &optimizer.StringConst{Value: "b"}},
				{&optimizer.IntegerConst{Value: 5}, &optimizer.StringConst{Value: "c"}},
				{&optimizer.IntegerConst{Value: 6}, &optimizer.StringConst{Value: "c"}},
				{&optimizer.IntegerConst{Value: 7}, &optimizer.StringConst{Value: "c"}},
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
func seedRowsAndAnalyze(t *testing.T, n int, makeRow func(i int) []optimizer.Expr, statsTarget int) (*Context, *catalog.TableStats) {
	t.Helper()
	ctx, cat, cleanup := newStorageFixture(t)
	// M0129-S8.3: advance the command counter between statements.
	advanceStmtCounter(ctx)
	t.Cleanup(cleanup)
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})

	rows := make([][]optimizer.Expr, n)
	for i := 0; i < n; i++ {
		rows[i] = makeRow(i)
	}
	insertPlan := &optimizer.Insert{
		Table:       tbl,
		Source:      &optimizer.Values{Rows: rows},
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
	makeRow := func(i int) []optimizer.Expr {
		var label string
		switch {
		case i < 800:
			label = "F"
		case i < 950:
			label = "O"
		default:
			label = "P"
		}
		return []optimizer.Expr{
			&optimizer.IntegerConst{Value: int64(i)},
			&optimizer.StringConst{Value: label},
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
	makeRow := func(i int) []optimizer.Expr {
		return []optimizer.Expr{
			&optimizer.IntegerConst{Value: int64(i + 1)}, // 1..1000
			&optimizer.StringConst{Value: "x"},
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
	makeRow := func(i int) []optimizer.Expr {
		return []optimizer.Expr{
			&optimizer.IntegerConst{Value: int64(i + 1)},
			&optimizer.StringConst{Value: "x"},
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

	rows := make([][]optimizer.Expr, 1000)
	for i := 0; i < 1000; i++ {
		rows[i] = []optimizer.Expr{
			&optimizer.IntegerConst{Value: int64(i + 1)}, // 1..1000, unique
			&optimizer.StringConst{Value: "x"},
		}
	}
	insertPlan := &optimizer.Insert{Table: tbl, Source: &optimizer.Values{Rows: rows}, ColumnIndex: []int{0, 1}}
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

	rows := make([][]optimizer.Expr, 10)
	for i := 0; i < 10; i++ {
		rows[i] = []optimizer.Expr{
			&optimizer.IntegerConst{Value: int64(i + 1)},
			&optimizer.StringConst{Value: "a"},
		}
	}
	insertPlan := &optimizer.Insert{Table: tbl, Source: &optimizer.Values{Rows: rows}, ColumnIndex: []int{0, 1}}
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

	rows := make([][]optimizer.Expr, 1000)
	for i := 0; i < 1000; i++ {
		rows[i] = []optimizer.Expr{
			&optimizer.IntegerConst{Value: int64(i + 1)}, // 1..1000, unique
			&optimizer.StringConst{Value: "x"},
		}
	}
	insertPlan := &optimizer.Insert{Table: tbl, Source: &optimizer.Values{Rows: rows}, ColumnIndex: []int{0, 1}}
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
		// M0138-0007: the int64 fast-path measures PG's actual on-disk
		// NumericData width (short varlena header + short numeric header +
		// digits) instead of reporting 0 — see TestNumericFastPathOnDiskWidth
		// for the oracle-verified cases this value comes from.
		{"numeric fast", NewNumericInt64Datum(12345, 0), 7},
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

// TestNumericFastPathOnDiskWidth pins numericFastPathOnDiskWidth against
// values measured directly off a live PG 18.3 TPC-H `lineitem` (M0138-0007):
// `pg_column_size(l_quantity)` reads 5 for the value `18` (dscale 0), and
// `pg_stats.avg_width` for `l_extendedprice` (dscale 2) reads 8, which this
// formula reproduces as 7 or 9 depending on the row's magnitude — those two
// straddle the reported average. Zero must not regress to 0 ("unknown"): a
// width-0 NUMERIC column was M0138-0005's finding (HammerDB declares every
// TPC-H key column NUMERIC, so this hits 28/61 TPC-H and 17/120 TPC-DS
// columns corpus-wide).
func TestNumericFastPathOnDiskWidth(t *testing.T) {
	tests := []struct {
		name  string
		mant  int64
		scale int16
		want  int
	}{
		{"18 dscale0 (PG pg_column_size=5)", 18, 0, 5},
		{"27153.18 dscale2 (PG pg_column_size=9)", 2715318, 2, 9},
		{"5000.00 dscale2, trailing-zero digit stripped", 500000, 2, 5},
		{"zero", 0, 0, 3},
		{"negative", -12345, 2, 7},
		{"trailing-zero mantissa", 1500, 0, 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := numericFastPathOnDiskWidth(tc.mant, tc.scale)
			if got != tc.want {
				t.Errorf("numericFastPathOnDiskWidth(%d, %d) = %d, want %d", tc.mant, tc.scale, got, tc.want)
			}
			if got == 0 {
				t.Errorf("numericFastPathOnDiskWidth(%d, %d) = 0 (M0138-0005's regression)", tc.mant, tc.scale)
			}
		})
	}
}

// TestAnalyzePopulatesAvgWidth pins that computeColumnStats calculates
// per-column AvgWidth from sampled non-null Datum values (M0128-P3.1), and
// (M0138-0004) that a fixed-width (non-varlena) column reports its type's
// typlen rather than a measured value, mirroring PG's compute_scalar_stats
// `is_varwidth` branch (analyze.c:2565-2569) — including in the all-null
// case, where PG still reports typlen for a fixed-width type but 0
// ("unknown") for a variable-width one (analyze.c:2975-2979).
// It tests the computation directly rather than through the full
// insert→heap→decode pipeline, so it controls the Datum shapes precisely.
func TestAnalyzePopulatesAvgWidth(t *testing.T) {
	int4Type := catalog.Type{Name: "int4"}
	textType := catalog.Type{Name: "text"}

	// Build a sample of 20 rows: column 0 is fixed-width (int4, typlen 4),
	// column 1 is variable-width text with known byte lengths.
	sample := make([]Row, 20)
	for i := 0; i < 20; i++ {
		sample[i] = Row{
			NewIntDatum(int64(i + 1)),                     // fixed-width: typlen fallback, not measured
			NewStringDatum(strings.Repeat("x", (i+1)*10)), // 10, 20, …, 200 bytes
		}
	}
	// Add one null row to verify nulls don't affect the average.
	sample = append(sample, Row{NullDatum, NullDatum})

	stats := computeColumnStats(sample, 0, 100, 21, int4Type, nil)
	if stats.AvgWidth != 4 {
		t.Errorf("col 0 (int4, fixed-width): AvgWidth=%v, want 4 (typlen, not measured)", stats.AvgWidth)
	}

	stats1 := computeColumnStats(sample, 1, 100, 21, textType, nil)
	// 20 values: 10, 20, …, 200 bytes; avg = (10+200)*20/2/20 = 105.
	if stats1.AvgWidth < 90 || stats1.AvgWidth > 120 {
		t.Errorf("col 1 (text 10–200B): AvgWidth=%v, want ~105", stats1.AvgWidth)
	}

	// A sample with only nulls, fixed-width type: PG still reports typlen.
	nullSample := []Row{{NullDatum}, {NullDatum}, {NullDatum}}
	nullFixedStats := computeColumnStats(nullSample, 0, 100, 3, int4Type, nil)
	if nullFixedStats.AvgWidth != 4 {
		t.Errorf("all-null int4 column: AvgWidth=%v, want 4 (typlen)", nullFixedStats.AvgWidth)
	}

	// A sample with only nulls, variable-width type: AvgWidth = 0 ("unknown").
	nullVarStats := computeColumnStats(nullSample, 0, 100, 3, textType, nil)
	if nullVarStats.AvgWidth != 0 {
		t.Errorf("all-null text column: AvgWidth=%v, want 0", nullVarStats.AvgWidth)
	}

	// An empty sample: AvgWidth = 0 (no stats computed at all, matching PG
	// leaving stats_valid false for a zero-row sample).
	emptyStats := computeColumnStats(nil, 0, 100, 0, int4Type, nil)
	if emptyStats.AvgWidth != 0 {
		t.Errorf("empty sample: AvgWidth=%v, want 0", emptyStats.AvgWidth)
	}
}

// TestAnalyzeCorrelationTieBreakMatchesPGTupnoOrder pins M0138-0004: PG's
// compare_scalars breaks a tie between equal-valued sample items by original
// scan position ("for equal datums, sort by tupno", analyze.c) --- a
// deterministic total order, not an unspecified one. This hand-derives the
// expected correlation from that exact rule (stable sort: ties keep their
// original relative order) so the test fails if the production sort is ever
// swapped back to a plain (unstable) `sort.Slice`.
//
// Six rows with two duplicate groups: values [3,3,3,1,1,2] at positions
// [0,1,2,3,4,5]. PG's ascending-value order with ties broken by ascending
// tupno places them as: pos3(1), pos4(1), pos5(2), pos0(3), pos1(3), pos2(3)
// --- i.e. sortedPos i holds original position originalOf[i] below.
func TestAnalyzeCorrelationTieBreakMatchesPGTupnoOrder(t *testing.T) {
	sample := []Row{
		{NewIntDatum(3)},
		{NewIntDatum(3)},
		{NewIntDatum(3)},
		{NewIntDatum(1)},
		{NewIntDatum(1)},
		{NewIntDatum(2)},
	}
	originalOf := []int{3, 4, 5, 0, 1, 2}
	n := float64(len(sample))
	var corrXYSum float64
	for sortedPos, orig := range originalOf {
		corrXYSum += float64(orig) * float64(sortedPos)
	}
	corrXSum := (n - 1) * n / 2
	corrX2Sum := (n - 1) * n * (2*n - 1) / 6
	denom := n*corrX2Sum - corrXSum*corrXSum
	want := (n*corrXYSum - corrXSum*corrXSum) / denom

	stats := computeColumnStats(sample, 0, 100, 6, catalog.Type{Name: "int4"}, nil)
	if math.Abs(stats.Correlation-want) > 1e-9 {
		t.Errorf("Correlation=%v want %v (PG tupno-tie-break order)", stats.Correlation, want)
	}
}

// TestAnalyzeReservoirSeedCausesCorrelationVarianceOnPeriodicFK is M0138-0009's
// sampling-variance confirmation. `TestPort_M0138CorrelationSyntheticGoopgVsPG`
// (internal/testport) proved goopg and PG compute BYTE-IDENTICAL correlation
// (0.095866) for a fully-sampled (no reservoir subsampling) periodic
// low-cardinality column loaded in identical order --- ruling out both a
// computation/tie-break bug and a physical-append-order divergence as the
// TPC-DS census's [0.09,0.16]-vs-PG's-[~0] banding cause. This test checks
// the one mechanism that test could not reach: at real TPC-DS scale the
// table is far bigger than targrows, so the Vitter reservoir sampler DOES
// subsample, and goopg's and PG's independent RNGs necessarily draw
// different subsets. A periodic FK-like column is exactly the shape where a
// subsample's apparent correlation is sensitive to WHICH physical positions
// the sampler drew (aliasing against the period), even though the full
// population's correlation is near zero --- unlike a non-periodic column,
// where any subsample stays near zero regardless of which rows it drew. If
// goopg's own correlation reading swings by at least the census's own
// banding spread purely from changing the sampler's seed (nothing else
// touched), that is sufficient to explain the finding as ordinary --- and
// equally present in PG --- sampling variance, not a goopg-specific defect.
func TestAnalyzeReservoirSeedCausesCorrelationVarianceOnPeriodicFK(t *testing.T) {
	const n = 3000
	const cycle = 11
	const statsTarget = 1 // targrows = 1*upstreamSampleMultiplier(300) = 300, well under n=3000

	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	advanceStmtCounter(ctx)
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})

	rows := make([][]optimizer.Expr, n)
	for i := 0; i < n; i++ {
		rows[i] = []optimizer.Expr{
			&optimizer.IntegerConst{Value: int64(i % cycle)},
			&optimizer.StringConst{Value: "x"},
		}
	}
	insertPlan := &optimizer.Insert{
		Table:       tbl,
		Source:      &optimizer.Values{Rows: rows},
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

	var corrs []float64
	for seed := int64(1); seed <= 8; seed++ {
		stats, err := analyzeRelationWith(ctx.Pool, ctx.TxnMgr, ctx.Catalog, tbl, statsTarget, rand.New(rand.NewSource(seed)), ctx.MultiXact, ctx)
		if err != nil {
			t.Fatalf("seed %d: analyzeRelationWith: %v", seed, err)
		}
		corrs = append(corrs, stats.Columns[0].Correlation)
	}

	lo, hi := corrs[0], corrs[0]
	for _, c := range corrs {
		if c < lo {
			lo = c
		}
		if c > hi {
			hi = c
		}
	}
	t.Logf("periodic FK (cycle=%d) correlation across %d reservoir seeds: %v (range [%.4f, %.4f])", cycle, len(corrs), corrs, lo, hi)

	// The census's banding was [0.09, 0.16] --- a spread of 0.07. If eight
	// independent seeds alone produce at least that much spread on a
	// periodic column, seed variance is a sufficient explanation and no
	// further ANALYZE-mechanism fix is implicated.
	if hi-lo < 0.07 {
		t.Errorf("correlation range across seeds = %.4f, want >= 0.07 (the census's own banding spread) to confirm seed variance alone explains the TPC-DS finding", hi-lo)
	}
}

// TestAnalyzeMCVTieBreakIsDeterministicAndPGOrdered pins M0138-0004: PG's
// compute_scalar_stats walks values in ascending sorted order and only
// evicts the current MCV track-list tail on a STRICTLY greater count
// (analyze.c: `dups_cnt > track[track_cnt-1].count`), so a count TIE at the
// truncation boundary keeps whichever value was encountered first --- the
// smaller one. Before this fix, goopg grouped values through a Go map
// (`freq`, randomized iteration order) and an unstable sort, so which of two
// equal-count candidates survived truncation was undefined and could vary
// run to run for the identical input.
func TestAnalyzeMCVTieBreakIsDeterministicAndPGOrdered(t *testing.T) {
	const n = 1000
	sample := make([]Row, n)
	for i := 0; i < n; i++ {
		switch {
		case i < 400:
			sample[i] = Row{NewStringDatum("AAAA")}
		case i < 800:
			sample[i] = Row{NewStringDatum("BBBB")}
		default:
			sample[i] = Row{NewStringDatum("s" + strconv.Itoa(i))}
		}
	}
	textType := catalog.Type{Name: "text"}
	for iter := 0; iter < 25; iter++ {
		stats := computeColumnStats(sample, 0, 1, n, textType, nil)
		if len(stats.MCV) != 1 {
			t.Fatalf("iter %d: MCV=%v want exactly 1 entry", iter, stats.MCV)
		}
		if stats.MCV[0].Value != "AAAA" {
			t.Errorf("iter %d: MCV[0].Value=%q want %q (PG keeps the value first encountered in ascending sort order on a count tie)",
				iter, stats.MCV[0].Value, "AAAA")
		}
	}
}

// TestAnalyzeMCVExcludesSingletonsFromCompletenessAndCandidates pins M0138-0004:
// upstream's `track[]` (compute_scalar_stats) only ever gains an entry for a
// value that appeared more than once (`dups_cnt > 1`, analyze.c:2549-2552), so
// (a) the "complete list, keep it all" shortcut (`track_cnt == ndistinct`,
// analyze.c:2676-2678) can only fire when literally every distinct sample
// value repeated, and (b) even on the ordinary path, `analyze_mcv_list` is
// handed only the multiply-occurring candidates (`num_mcv = min(num_mcv,
// track_cnt)`, analyze.c:2688-2689) — a singly-occurring value is never a
// candidate at all.
//
// The sample here has 60 distinct values occurring exactly twice (a
// near-uniform distribution — none of them is "significantly" more common
// than the others) plus 40 distinct singletons, for 100 total distinct values
// under a stats target of 100. Before this fix, `len(buckets) <= statsTarget`
// (100 <= 100) alone declared the list "complete" and returned all 60
// repeated values verbatim, skipping `analyzeMCVList` entirely — the same
// near-uniform shape that `TestAnalyzeMCVListMatchesUpstream`'s
// "near-uniform column admits nothing" case proves PG rejects.
func TestAnalyzeMCVExcludesSingletonsFromCompletenessAndCandidates(t *testing.T) {
	const nRepeated = 60
	const nSingletons = 40
	sample := make([]Row, 0, nRepeated*2+nSingletons)
	for v := 0; v < nRepeated; v++ {
		sample = append(sample, Row{NewIntDatum(int64(v))}, Row{NewIntDatum(int64(v))})
	}
	for v := nRepeated; v < nRepeated+nSingletons; v++ {
		sample = append(sample, Row{NewIntDatum(int64(v))})
	}
	// totalRows large enough that the Duj1 estimate stays well under the 10%
	// row-scaling threshold, isolating the singleton-exclusion behavior from
	// the unrelated stadistinct-sign guard.
	stats := computeColumnStats(sample, 0, 100, 100000, catalog.Type{Name: "int4"}, nil)
	if len(stats.MCV) != 0 {
		t.Errorf("MCV=%v (len %d) want empty — PG's analyze_mcv_list rejects a near-uniform count-2 distribution as not significant, and singletons were never candidates",
			stats.MCV, len(stats.MCV))
	}
}

// TestAnalyzeHistogramKeepsAdjacentDuplicateBoundaries pins M0138-0004:
// upstream's compute_scalar_stats (analyze.c:2806-2836) copies exactly
// `num_hist` evenly-spaced values straight out of the sorted non-MCV array
// with no distinctness check, so a value that's common but never became an
// MCV candidate can legitimately occupy several adjacent histogram slots.
// goopg used to dedup those slots down (shrinking the stored histogram
// relative to what PG would have stored for the identical sample); the
// selectivity consumer already copes with equal-adjacent bounds via the
// same `binfrac = 0.5` fallback PG itself uses (selfuncs.c:1234-1237,
// mirrored in bucketFraction), so there is nothing left needing the dedup.
//
// Two values (2000000, 3000000) are overwhelmingly dominant and take both
// MCV slots under a stats target of 2. A third value (-1000000, chosen as
// the minimum so it sorts first) repeats 500 times — enough contiguous mass
// that, ranked 3rd, it never becomes an MCV *candidate* at all (the target-2
// candidate cap admits only the top 2 by count) yet still spans multiple of
// the histogram's evenly-spaced sample points.
func TestAnalyzeHistogramKeepsAdjacentDuplicateBoundaries(t *testing.T) {
	var sample []Row
	for i := 0; i < 1000; i++ {
		sample = append(sample, Row{NewIntDatum(2000000)})
	}
	for i := 0; i < 1000; i++ {
		sample = append(sample, Row{NewIntDatum(3000000)})
	}
	for i := 0; i < 500; i++ {
		sample = append(sample, Row{NewIntDatum(-1000000)})
	}
	for v := int64(0); v < 10; v++ {
		sample = append(sample, Row{NewIntDatum(v)})
	}

	stats := computeColumnStats(sample, 0, 2, 100000, catalog.Type{Name: "int4"}, nil)
	if len(stats.MCV) != 2 {
		t.Fatalf("MCV=%v want exactly the 2 dominant values", stats.MCV)
	}
	if len(stats.Histogram) < 2 || stats.Histogram[0] != "-1000000" {
		t.Fatalf("Histogram=%v want to start with the repeated value -1000000", stats.Histogram)
	}
	dup := false
	for i := 1; i < len(stats.Histogram); i++ {
		if stats.Histogram[i] == stats.Histogram[i-1] {
			dup = true
			break
		}
	}
	if !dup {
		t.Errorf("Histogram=%v want at least one adjacent duplicate boundary (PG does not dedup; a prior goopg version wrongly did)", stats.Histogram)
	}
}

// TestResolveAnalyzeColumns pins the ANALYZE/VACUUM ANALYZE per-relation
// column-list validator (analyze.c:372-400 + attnameAttNum,
// parse_relation.c:3589-3609): case-sensitive, dropped-skipping lookup, 42703
// on the first unresolved name, 42701 on a duplicate mention, nil when every
// listed column resolves.
func TestResolveAnalyzeColumns(t *testing.T) {
	_, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"}) // id, label

	// Two valid columns -> nil.
	if err := resolveAnalyzeColumns(tbl, []string{"id", "label"}, 0); err != nil {
		t.Errorf("resolveAnalyzeColumns(id, label) = %v, want nil", err)
	}

	// Duplicate mention of the same column -> 42701.
	dup := resolveAnalyzeColumns(tbl, []string{"id", "label", "id"}, 0)
	if dup == nil || dup.Code != "42701" {
		t.Errorf("duplicate column: got %v, want 42701", dup)
	}
	if dup != nil && dup.Message != `column "id" of relation "items" appears more than once` {
		t.Errorf("duplicate message = %q", dup.Message)
	}

	// Case-sensitive: "ID" does not match "id" -> 42703.
	if err := resolveAnalyzeColumns(tbl, []string{"ID"}, 0); err == nil || err.Code != "42703" {
		t.Errorf("case-sensitive lookup: got %v, want 42703", err)
	}

	// Dropped columns are invisible to the lookup -> 42703.
	tbl.Columns[0].Dropped = true
	drop := resolveAnalyzeColumns(tbl, []string{"id"}, 0)
	if drop == nil || drop.Code != "42703" {
		t.Errorf("dropped column: got %v, want 42703", drop)
	}
	if drop != nil && drop.Message != `column "id" of relation "items" does not exist` {
		t.Errorf("dropped message = %q", drop.Message)
	}
}
