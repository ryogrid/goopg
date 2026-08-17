package optimizer

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// M0125-0003 stage 1 — the GOOPG_RELSIZE_FALLBACK relation-size fallback.
//
// These tests pin two separate things, and the distinction matters when one of
// them fails:
//
//   - the ARITHMETIC is upstream's table_block_relation_estimate_size
//     (postgres/src/backend/access/table/tableam.c), rule for rule. A failure
//     here means goopg's estimate diverges from the number PostgreSQL would
//     compute for the same relation.
//   - the GATING keeps the landing commit inert: flag off changes nothing, and
//     an ANALYZEd relation is untouched even with the flag on (design §D3).
//     A failure here means the A/B measurement this milestone is built around
//     would be reading a confounded signal.
//
// Design: docs/design/0125-0003-relsize-fallback-and-tpch-stats-tradeoff.md

// widthForDensity returns the data width that makes tuple_width come out to w,
// so a test can state the density it wants directly.
func widthForDensity(tupleWidth int) int { return tupleWidth - perTupleOverhead }

func TestEstimateRelSize_DensityFromTupleWidth(t *testing.T) {
	// Statistics-less branch at default fillfactor:
	//	tuple_width = 100 + 28 = 128
	//	density     = (8168 * 100 / 100) / 128 = 63   (integer division)
	//	tuples      = rint(63 * 200) = 12600
	pages, tuples := estimateRelSize(relSizeInputs{
		curpages:  200,
		dataWidth: 100,
	})
	if pages != 200 {
		t.Fatalf("pages = %d, want the live block count 200", pages)
	}
	if tuples != 12600 {
		t.Fatalf("tuples = %v, want 12600", tuples)
	}
}

func TestEstimateRelSize_FillfactorScalesDensity(t *testing.T) {
	// Upstream consults fillfactor ONLY on the statistics-less branch; the
	// density branch accounts for it implicitly through real relpages.
	//
	//	tuple_width = 72 + 28 = 100
	//	ff=100 -> density = (8168 * 100 / 100) / 100 = 81
	//	ff=50  -> density = (8168 *  50 / 100) / 100 = 40   (not 40.5)
	full := relSizeInputs{curpages: 10, dataWidth: widthForDensity(100)}
	half := full
	half.fillfactor = 50

	if _, got := estimateRelSize(full); got != 810 {
		t.Fatalf("fillfactor unset (=100): tuples = %v, want 810", got)
	}
	if _, got := estimateRelSize(half); got != 400 {
		t.Fatalf("fillfactor 50: tuples = %v, want 400", got)
	}

	// Fillfactor 0 means "reloption never set", not "no space per page".
	zero := full
	zero.fillfactor = 0
	if _, got := estimateRelSize(zero); got != 810 {
		t.Fatalf("fillfactor 0 must mean HEAP_DEFAULT_FILLFACTOR, got %v", got)
	}
}

func TestEstimateRelSize_NeverAnalyzedTenPageFloor(t *testing.T) {
	// Upstream: `if (curpages < 10 && reltuples < 0 && !relhassubclass)
	// curpages = 10;`. goopg spells `reltuples < 0` as !Analyzed.
	//
	//	tuple_width = 8168 + 28 -> density = 8168/8196 = 0 -> clamped to 1
	// so tuples tracks curpages exactly and the floor is directly visible.
	oneRowPerPage := relSizeInputs{curpages: 3, dataWidth: usableBytesPerBlock}

	pages, tuples := estimateRelSize(oneRowPerPage)
	if pages != neverAnalyzedMinPages || tuples != 10 {
		t.Fatalf("never-analyzed 3-block relation: pages=%d tuples=%v, want 10/10", pages, tuples)
	}

	// Analyzed: believe the real size.
	analyzed := oneRowPerPage
	analyzed.analyzed = true
	if pages, tuples := estimateRelSize(analyzed); pages != 3 || tuples != 3 {
		t.Fatalf("analyzed 3-block relation: pages=%d tuples=%v, want 3/3", pages, tuples)
	}

	// relhassubclass: "Totally empty parent tables are quite common, so we
	// should be willing to believe that they are empty."
	parent := oneRowPerPage
	parent.hasSubclass = true
	if pages, _ := estimateRelSize(parent); pages != 3 {
		t.Fatalf("relhassubclass must suppress the 10-page floor, got pages=%d", pages)
	}

	// The floor does not apply at or above 10 blocks.
	big := oneRowPerPage
	big.curpages = 40
	if pages, _ := estimateRelSize(big); pages != 40 {
		t.Fatalf("floor must not shrink a relation, got pages=%d", pages)
	}
}

func TestEstimateRelSize_ZeroPagesMeansZeroTuples(t *testing.T) {
	// The early exit is evaluated AFTER the 10-page floor, so an unanalyzed
	// empty relation is estimated at 10 pages' worth, while an ANALYZEd empty
	// one is believed to be empty. That ordering is the whole point of the
	// sentinel and is the empty-analyzed-table boundary of design §D1.
	if pages, tuples := estimateRelSize(relSizeInputs{curpages: 0, analyzed: true, dataWidth: 100}); pages != 0 || tuples != 0 {
		t.Fatalf("analyzed empty relation: pages=%d tuples=%v, want 0/0", pages, tuples)
	}
	if pages, tuples := estimateRelSize(relSizeInputs{curpages: 0, dataWidth: 100}); pages != 10 || tuples == 0 {
		t.Fatalf("never-analyzed empty relation: pages=%d tuples=%v, want 10 pages and a non-zero estimate", pages, tuples)
	}
	// An unknown (negative) block count is not "empty" — it is "no estimate".
	if pages, tuples := estimateRelSize(relSizeInputs{curpages: -1, dataWidth: 100}); pages != 0 || tuples != 0 {
		t.Fatalf("unknown block count: pages=%d tuples=%v, want 0/0", pages, tuples)
	}
}

func TestEstimateRelSize_StatisticsBranchUsesStoredDensity(t *testing.T) {
	// `if (reltuples >= 0 && relpages > 0) density = reltuples / relpages;`
	// 5000 rows over 50 pages = 100 rows/page, applied to 60 live pages.
	_, tuples := estimateRelSize(relSizeInputs{
		curpages:  60,
		relpages:  50,
		reltuples: 5000,
		analyzed:  true,
		dataWidth: 100,
	})
	if tuples != 6000 {
		t.Fatalf("stored-density branch: tuples = %v, want 6000", tuples)
	}
	// relpages == 0 falls through to the width branch even when analyzed —
	// this is the analyze-then-restart state loadStatisticsFromHeap leaves
	// behind, where only the per-column slots survive (ledger pq-P6).
	_, tuples = estimateRelSize(relSizeInputs{
		curpages:  60,
		analyzed:  true,
		dataWidth: 100,
	})
	if tuples != 63*60 {
		t.Fatalf("relpages=0 must use the width branch: tuples = %v, want %d", tuples, 63*60)
	}
}

func TestClampRowEst(t *testing.T) {
	// clamp_row_est (optimizer/path/costsize.c): finite, >= 1, integral.
	if got := clampRowEst(0); got != 1 {
		t.Fatalf("clampRowEst(0) = %v, want 1", got)
	}
	if got := clampRowEst(0.4); got != 1 {
		t.Fatalf("clampRowEst(0.4) = %v, want 1", got)
	}
	if got := clampRowEst(1); got != 1 {
		t.Fatalf("clampRowEst(1) = %v, want 1", got)
	}
	// rint() rounds half to even, so 2.5 -> 2 and 3.5 -> 4. A naive
	// math.Round would give 3 and 4 and drift from PG on exact halves.
	if got := clampRowEst(2.5); got != 2 {
		t.Fatalf("clampRowEst(2.5) = %v, want 2 (round half to even)", got)
	}
	if got := clampRowEst(3.5); got != 4 {
		t.Fatalf("clampRowEst(3.5) = %v, want 4 (round half to even)", got)
	}
	if got := clampRowEst(math.NaN()); got != maximumRowCount {
		t.Fatalf("clampRowEst(NaN) = %v, want MAXIMUM_ROWCOUNT", got)
	}
	if got := clampRowEst(math.Inf(1)); got != maximumRowCount {
		t.Fatalf("clampRowEst(+Inf) = %v, want MAXIMUM_ROWCOUNT", got)
	}
}

// TestEstimateRelSize_DensityClampedToAtLeastOneRow covers upstream's
// "There's at least one row on the page, even with low fillfactor": a tuple
// wider than a page truncates the integer division to 0, and the clamp is what
// stops the relation being estimated at zero rows.
func TestEstimateRelSize_DensityClampedToAtLeastOneRow(t *testing.T) {
	_, tuples := estimateRelSize(relSizeInputs{
		curpages:  25,
		analyzed:  true,
		dataWidth: 100000, // far wider than a block
	})
	if tuples != 25 {
		t.Fatalf("over-wide tuple: tuples = %v, want one row per page (25)", tuples)
	}
}

func TestTableDataWidth_SumsEveryColumn(t *testing.T) {
	tbl := &catalog.Table{Columns: []catalog.Column{
		{Type: catalog.Type{Name: "int4"}},                       // 4
		{Type: catalog.Type{Name: "bigint"}},                     // 8
		{Type: catalog.Type{Name: "varchar", Args: []int64{20}}}, // 58 (get_typavgwidth)
	}}
	if got := tableDataWidth(tbl); got != 70 {
		t.Fatalf("tableDataWidth = %d, want 70", got)
	}
	// get_rel_data_width is a relation property, so a column the query never
	// projects still counts. Nil and empty floor at 1.
	if got := tableDataWidth(nil); got != 1 {
		t.Fatalf("tableDataWidth(nil) = %d, want 1", got)
	}
	if got := tableDataWidth(&catalog.Table{}); got != 1 {
		t.Fatalf("tableDataWidth(no columns) = %d, want 1", got)
	}
}

// TestEstimateRelSize_MatchesPostgresOracle is the acceptance test for the
// arithmetic: it replays four relations measured on PostgreSQL 18.3 (the TPC-DS
// reference instance on port 65438, UTF8, temp tables so autoanalyze cannot
// interfere) and requires goopg to produce the SAME row estimate PG's planner
// printed.
//
// The numbers are observations, not predictions — each `want` is the `rows=`
// field of `EXPLAIN SELECT * FROM <t>` and each `blocks` is
// `pg_relation_size(t)/8192`. Every one of these relations reported
// `pg_class.reltuples = -1`, which is what makes them exercise the
// statistics-less branch and the never-analyzed floor.
//
// If this fails, goopg's cold-start row estimate has diverged from PostgreSQL's
// and the whole point of modelling table_block_relation_estimate_size is lost.
func TestEstimateRelSize_MatchesPostgresOracle(t *testing.T) {
	cols := func(names ...catalog.Type) []catalog.Column {
		out := make([]catalog.Column, len(names))
		for i, ty := range names {
			out[i] = catalog.Column{Type: ty}
		}
		return out
	}
	int4 := catalog.Type{Name: "int4"}
	int8 := catalog.Type{Name: "bigint"}
	vc20 := catalog.Type{Name: "varchar", Args: []int64{20}}
	vc100 := catalog.Type{Name: "varchar", Args: []int64{100}}

	cases := []struct {
		name       string
		columns    []catalog.Column
		blocks     int64
		fillfactor int
		wantWidth  int   // PG's EXPLAIN width column
		wantRows   int64 // PG's EXPLAIN rows column
	}{
		{
			// CREATE TEMP TABLE probe_a (a int4, b bigint, c varchar(20));
			// 20000 rows inserted, never analyzed.
			// Seq Scan on probe_a (rows=12284 width=70), 148 blocks.
			name: "three-column", columns: cols(int4, int8, vc20),
			blocks: 148, wantWidth: 70, wantRows: 12284,
		},
		{
			// CREATE TEMP TABLE probe_b (a int4, c varchar(100));
			// Seq Scan on probe_b (rows=6624 width=222), 207 blocks.
			// Exercises the >32-byte half-of-max rule at a larger bound.
			name: "wide-varchar", columns: cols(int4, vc100),
			blocks: 207, wantWidth: 222, wantRows: 6624,
		},
		{
			// Same shape as probe_a WITH (fillfactor=50): 299 blocks.
			// Seq Scan on probe_ff (rows=12259 width=70).
			// density = (8168*50/100)/98 = 41, and 41*299 = 12259 — the
			// integer truncation is visible here: 4084/98 is 41.67.
			name: "fillfactor-50", columns: cols(int4, int8, vc20),
			blocks: 299, fillfactor: 50, wantWidth: 70, wantRows: 12259,
		},
		{
			// CREATE TEMP TABLE probe_empty (a int4, b bigint, c varchar(20));
			// zero rows, zero blocks, never analyzed.
			// Seq Scan on probe_empty (rows=830 width=70) — 830 = 83 * the
			// 10-page floor. PG does NOT believe a never-analyzed empty
			// relation is empty; after ANALYZE the same query estimates 1.
			name: "empty-never-analyzed", columns: cols(int4, int8, vc20),
			blocks: 0, wantWidth: 70, wantRows: 830,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tbl := &catalog.Table{Name: c.name, Columns: c.columns, Fillfactor: c.fillfactor}
			if got := tableDataWidth(tbl); got != c.wantWidth {
				t.Fatalf("get_rel_data_width = %d, want PG's %d", got, c.wantWidth)
			}
			_, rows := estimateRelSize(relSizeInputs{
				curpages:   c.blocks,
				fillfactor: c.fillfactor,
				dataWidth:  tableDataWidth(tbl),
			})
			if int64(rows) != c.wantRows {
				t.Fatalf("rows = %v, want PG's %d", rows, c.wantRows)
			}
		})
	}
}

// ── gating ───────────────────────────────────────────────────────────────────

// TestParseRelSizeFallbackStage pins the knob's contract AFTER M0125-0005
// flipped the default 0 -> 2. Two entries here are the flip itself and are the
// ones to read first if this fails: the empty string is now the default rather
// than off, and an unparseable value lands on the default rather than off —
// because once stage 2 is what goopg ships, "off" is a deviation, so a typo
// must not silently hand an operator a planner production does not run.
func TestParseRelSizeFallbackStage(t *testing.T) {
	cases := map[string]int{
		"":      defaultRelSizeFallbackStage, // unset = the shipped default
		"0":     0,                           // the explicit opt-out (design §7.3 RC-5's reopen path)
		"off":   0,
		"false": 0,
		"no":    0,
		" OFF ": 0,
		// The word forms meant stage 1 while stage 1 was the whole feature.
		// Post-flip they mean the default, so "true" cannot silently downgrade.
		"on":      defaultRelSizeFallbackStage,
		"true":    defaultRelSizeFallbackStage,
		"YES":     defaultRelSizeFallbackStage,
		"1":       1, // a numeral still selects its stage exactly
		"2":       2,
		"3":       3,
		"9":       3,                           // clamped to the highest defined stage
		"-1":      defaultRelSizeFallbackStage, // not a stage; not an opt-out either
		"garbage": defaultRelSizeFallbackStage,
	}
	for in, want := range cases {
		if got := parseRelSizeFallbackStage(in); got != want {
			t.Errorf("parseRelSizeFallbackStage(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestRelSizeFallbackStageGating(t *testing.T) {
	defer SetRelSizeFallbackStage(SetRelSizeFallbackStage(0))

	SetRelSizeFallbackStage(0)
	for stage := 1; stage <= 3; stage++ {
		if relSizeFallbackEnabled(stage) {
			t.Fatalf("stage %d must be disabled when the flag is off", stage)
		}
	}
	// A stage enables every consumer up to and including itself.
	SetRelSizeFallbackStage(2)
	if !relSizeFallbackEnabled(1) || !relSizeFallbackEnabled(2) {
		t.Fatal("stage 2 must enable consumers 1 and 2")
	}
	if relSizeFallbackEnabled(3) {
		t.Fatal("stage 2 must not enable consumer 3")
	}
}

// TestRelSizeFallbackDoesNotFireWhenAnalyzed is design §D3's invariant, and the
// property that makes the milestone's four-arm measurement interpretable: with
// a positive TableStats.RowCount the estimate must be exactly what it was
// before the fallback existed, in BOTH flag states. If this regresses, the W1
// and W2 arms stop being a control and "no difference" would no longer mean
// "not exercised".
func TestRelSizeFallbackDoesNotFireWhenAnalyzed(t *testing.T) {
	defer SetRelSizeFallbackStage(SetRelSizeFallbackStage(0))

	analyzed := &catalog.Table{
		Name:    "warm",
		Columns: []catalog.Column{{Type: catalog.Type{Name: "int4"}}},
		Stats:   &catalog.TableStats{RowCount: 4242, Pages: 7, Analyzed: true},
	}
	// EstRelRows deliberately holds a wildly different number: if the guard
	// were dropped, the assertion below would not merely be approximate, it
	// would be off by two orders of magnitude.
	scan := &SeqScan{Table: analyzed, EstRelRows: 999999}

	for _, stage := range []int{0, 1, 2, 3} {
		SetRelSizeFallbackStage(stage)
		if got := seqScanRows(scan); got != 4242 {
			t.Fatalf("stage %d: analyzed RowCount must win, got %d want 4242", stage, got)
		}
	}
}

// TestSeqScanRowsFallbackOnlyWhenRowCountAbsent pins the consumer side: the
// stamped estimate is read exactly when there is no ANALYZEd row count, and an
// unstamped scan (the flag-off case) is indistinguishable from the pre-M0125
// planner.
func TestSeqScanRowsFallbackOnlyWhenRowCountAbsent(t *testing.T) {
	cold := &catalog.Table{Name: "cold", Columns: []catalog.Column{{Type: catalog.Type{Name: "int4"}}}}

	if got := seqScanRows(&SeqScan{Table: cold}); got != 0 {
		t.Fatalf("unstamped cold scan must estimate 0 (pre-M0125 behavior), got %d", got)
	}
	if got := seqScanRows(&SeqScan{Table: cold, EstRelRows: 5000}); got != 5000 {
		t.Fatalf("stamped cold scan must use the fallback, got %d", got)
	}
	// Restored-but-RowCount-less state (loadStatisticsFromHeap): Analyzed is
	// true and Columns is non-nil, yet RowCount is 0. This is precisely the
	// case a `Columns == nil` gate would have suppressed — design §D1.
	restored := &catalog.Table{
		Name:    "restored",
		Columns: []catalog.Column{{Type: catalog.Type{Name: "int4"}}},
		Stats:   &catalog.TableStats{Analyzed: true, Columns: make([]catalog.ColumnStats, 1)},
	}
	if got := seqScanRows(&SeqScan{Table: restored, EstRelRows: 7000}); got != 7000 {
		t.Fatalf("analyze-then-restart state must still use the fallback, got %d", got)
	}
	if got := seqScanRows(nil); got != 0 {
		t.Fatalf("seqScanRows(nil) = %d, want 0", got)
	}
}

// ── stage 2: the join-search seed ──────────────────────────────────────────
//
// newSeedFixture and the three TestBushySeedRowCounts* pins lived here until
// M0127-P6.3 deleted `bushySeedRowCounts` with the old subset-bitmask DP
// (08 §4). The behaviour they pinned — the block-count fallback tier feeding
// a relation's search cardinality, the post-filter tier outranking it, the
// no-sizer 1-row floor — is now owned by `applyRelSizeFallback` (relsize.go),
// which the PG-shaped seam calls per leaf (joinsearchseam.go), and is pinned
// in relsize_baserel_placement_test.go.

// TestEstimateTableRowsFallbackNoCatalogSizer covers the plumbing's failure
// direction: with no live block count available the answer is "no estimate"
// (0), never "zero rows". A catalog with no storage behind it — every planner
// unit test, and any embedded caller — must therefore see no behavior change.
func TestEstimateTableRowsFallbackNoCatalogSizer(t *testing.T) {
	defer SetRelSizeFallbackStage(SetRelSizeFallbackStage(0))
	SetRelSizeFallbackStage(1)

	cat := catalog.NewInMemory()
	tbl := &catalog.Table{Name: "t", Columns: []catalog.Column{{Type: catalog.Type{Name: "int4"}}}}
	if got := estimateTableRowsFallback(cat, tbl); got != 0 {
		t.Fatalf("no sizer installed must yield 0, got %d", got)
	}
	if got := estimateTableRowsFallback(nil, tbl); got != 0 {
		t.Fatalf("nil catalog must yield 0, got %d", got)
	}
	if got := estimateTableRowsFallback(cat, nil); got != 0 {
		t.Fatalf("nil table must yield 0, got %d", got)
	}

	// With a sizer installed the estimate flows through, and the flag still
	// gates the stamping helper.
	cat.SetRelationSizer(func(storage.RelFileNode) (int64, bool) { return 100, true })
	if got := estimateTableRowsFallback(cat, tbl); got == 0 {
		t.Fatal("an installed sizer must produce a non-zero estimate")
	}
	SetRelSizeFallbackStage(0)
	if got := stage1RelSizeRows(cat, tbl); got != 0 {
		t.Fatalf("flag off must stamp 0, got %d", got)
	}
}
