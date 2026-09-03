package optimizer

// M0127-P5.4c-ii-b — `cost_index` (costindex.go).
//
// These tests are the falsifiable half of the cost model. Production DOES
// select on it since M0127-P5.9 (2026-08-06) — `GOOPG_PGSHAPED_DP` defaults ON
// — so the header's former "nothing selects on it yet" no longer holds; the
// cheapest way to be wrong about it and find out is still to pin the
// arithmetic against
// hand-computed PG values and to pin the STRUCTURAL properties that make the
// model usable: that the correlation interpolation runs in the direction PG's
// does, that the descent charge lands at startup, and that the one calibration
// knob the project has reaches every random-page term rather than half of them.

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/utils/misc"
)

func approxCost(a, b float64) bool {
	if math.Abs(a-b) < 1e-9 {
		return true
	}
	return math.Abs(a-b) <= 1e-9*math.Max(math.Abs(a), math.Abs(b))
}

// TestIndexPagesFetchedFitsInCache pins the `T <= b` branch of Mackert and
// Lohman: when the table fits in its pro-rated share of the cache, the answer
// is the plain 2TN/(2T+N) estimate, ceilinged, and capped at T.
//
// Hand-computed: T = 100 pages, N = 50 tuples.
//
//	2*100*50 / (2*100 + 50) = 10000/250 = 40 -> ceil = 40
func TestIndexPagesFetchedFitsInCache(t *testing.T) {
	got := indexPagesFetched(50, 100, 10, 100, 524288)
	if got != 40 {
		t.Fatalf("pages_fetched = %v; want 40", got)
	}
	// Fetching far more tuples than there are pages saturates at T.
	if got := indexPagesFetched(1_000_000, 100, 10, 100, 524288); got != 100 {
		t.Fatalf("saturated pages_fetched = %v; want the table's 100 pages", got)
	}
}

// TestIndexPagesFetchedExceedsCache pins the `T > b` branch — the regime that
// actually matters for a TPC-H-sized fact table, where the cache share is a
// small fraction of the relation.
//
// Hand-computed with effective_cache_size = 100 pages and a query whose total
// table pages are 10000, over a 10000-page table:
//
//	b   = ceil(100 * 10000/10010) = ceil(99.9) = 100
//	lim = 2*10000*100 / (2*10000 - 100) = 2000000/19900 = 100.5025...
//	N = 50 <= lim, so pages_fetched = 2*10000*50/(2*10000+50)
//	                                = 1000000/20050 = 49.875... -> ceil = 50
func TestIndexPagesFetchedExceedsCache(t *testing.T) {
	got := indexPagesFetched(50, 10000, 10, 10000, 100)
	if got != 50 {
		t.Fatalf("pages_fetched below lim = %v; want 50", got)
	}
	// Above `lim` the formula switches to the linear tail
	// b + (N - lim)*(T - b)/T, which must exceed `b` and stay under T.
	tail := indexPagesFetched(5000, 10000, 10, 10000, 100)
	if tail <= 100 || tail >= 10000 {
		t.Fatalf("linear-tail pages_fetched = %v; want strictly between b=100 and T=10000", tail)
	}
	// Hand-computed: 100 + (5000 - 100.50251...)*(9900/10000)
	//              = 100 + 4850.5025... = 4950.5025... -> ceil = 4951
	if tail != 4951 {
		t.Fatalf("linear-tail pages_fetched = %v; want 4951", tail)
	}
}

// TestCostIndexScanCorrelationInterpolation is the shape of `cost_index`'s
// final step: the I/O charge is interpolated between the uncorrelated
// (all-random) and perfectly-correlated (one random, rest sequential) cases by
// the SQUARE of the correlation. A perfectly correlated index must therefore
// be strictly cheaper, and the halfway point must not be the arithmetic mean —
// csquared = 0.25 at correlation 0.5 puts it a quarter of the way down.
func TestCostIndexScanCorrelationInterpolation(t *testing.T) {
	cp := defaultCostParams()
	in := indexScanInputs{
		relPages:        10000,
		relTuples:       1_000_000,
		indexPages:      2000,
		indexTuples:     1_000_000,
		treeHeight:      2,
		selectivity:     1.0,
		totalTablePages: 10000,
	}
	uncorrelated := costIndexScan(cp, in)
	in.correlation = 1.0
	correlated := costIndexScan(cp, in)
	in.correlation = 0.5
	half := costIndexScan(cp, in)

	if !(correlated.Total < uncorrelated.Total) {
		t.Fatalf("a perfectly correlated index costs %v, not less than the uncorrelated %v",
			correlated.Total, uncorrelated.Total)
	}
	// csquared = 0.25: exactly a quarter of the way from max to min.
	want := uncorrelated.Total + 0.25*(correlated.Total-uncorrelated.Total)
	if !approxCost(half.Total, want) {
		t.Fatalf("correlation 0.5 gave %v; want %v (csquared interpolation, not linear)", half.Total, want)
	}
}

// TestCostIndexScanFullScanArithmetic pins the whole `loop_count == 1`,
// no-index-qual computation against a hand-worked PG oracle value, so a
// refactor that drops a term is caught rather than absorbed.
//
// Inputs: 100-page/1000-tuple table, 20-page/1000-tuple index, tree height 1,
// selectivity 1.0, correlation 0, effective_cache_size default, and the query
// touching only this table (total_table_pages = 100).
//
//	index side (btreeIndexAMCost):
//	  numIndexTuples = 1000, numIndexPages = ceil(1000*20/1000) = 20
//	  20 * random_page_cost(4)                       =  80
//	  1000 * cpu_index_tuple_cost(0.005)             =   5
//	  descent = (1+1) * 50 * cpu_operator_cost(.0025)=   0.25   (startup AND total)
//	  -> startup 0.25, total 85.25
//	heap side:
//	  tuples_fetched = 1000
//	  T=100, b=ceil(524288*100/120)=436907 -> T<=b branch:
//	    2*100*1000/(2*100+1000) = 200000/1200 = 166.67 >= T -> 100 pages
//	  max_IO = 100 * 4 = 400
//	  csquared = 0 -> run += 400
//	  cpu = 1000 * cpu_tuple_cost(0.01) = 10
//
//	startup = 0.25, total = 0.25 + (85.25-0.25) + 400 + 10 = 495.25
func TestCostIndexScanFullScanArithmetic(t *testing.T) {
	cp := defaultCostParams()
	got := costIndexScan(cp, indexScanInputs{
		relPages:        100,
		relTuples:       1000,
		indexPages:      20,
		indexTuples:     1000,
		treeHeight:      1,
		selectivity:     1.0,
		correlation:     0,
		totalTablePages: 100,
	})
	if !approxCost(got.Startup, 0.25) {
		t.Errorf("startup = %v; want 0.25 (the B-tree descent)", got.Startup)
	}
	if !approxCost(got.Total, 495.25) {
		t.Errorf("total = %v; want 495.25", got.Total)
	}
}

// TestCostIndexScanStartupIsDescentOnly: an index scan can emit its first row
// after descending the tree, so its startup cost is the descent and nothing
// else. This is what lets it beat a sort (whose startup is the whole sort) on
// a LIMIT query, and it is the reason the descent charge is not folded into
// the run cost for convenience.
func TestCostIndexScanStartupIsDescentOnly(t *testing.T) {
	cp := defaultCostParams()
	in := indexScanInputs{
		relPages: 500, relTuples: 50_000, indexPages: 100, indexTuples: 50_000,
		treeHeight: 3, selectivity: 1.0, totalTablePages: 500,
	}
	got := costIndexScan(cp, in)
	wantStartup := float64(3+1) * pageCPUMultiplier * cp.cpuOperatorCost
	if !approxCost(got.Startup, wantStartup) {
		t.Fatalf("startup = %v; want the descent %v", got.Startup, wantStartup)
	}
	// A taller tree costs strictly more to descend.
	in.treeHeight = 5
	if taller := costIndexScan(cp, in); !(taller.Startup > got.Startup) {
		t.Fatalf("a height-5 tree started up at %v, not above height-3's %v", taller.Startup, got.Startup)
	}
}

// TestCostIndexScanSharesTheProbeCalibration is the 04 §1 one-currency
// property, and the reason this file exists at all rather than the cost living
// inside the path constructor: goopg has ONE knob recalibrating index access
// (`indexProbeCostMultiplier`, measured because goopg materialises the whole
// TID list per probe), and both index cost models must hang off it. If
// `cost_index` ignored the knob, raising it would make a parameterised probe
// expensive while leaving a full index scan untouched — two currencies inside
// one `addPath` comparison.
func TestCostIndexScanSharesTheProbeCalibration(t *testing.T) {
	cp := defaultCostParams()
	in := indexScanInputs{
		relPages: 100, relTuples: 1000, indexPages: 20, indexTuples: 1000,
		treeHeight: 1, selectivity: 1.0, totalTablePages: 100,
	}
	base := costIndexScan(cp, in)

	saved := indexProbeCostMultiplier
	indexProbeCostMultiplier = 3.0
	scaled := costIndexScan(cp, in)
	indexProbeCostMultiplier = saved

	// The random-page terms (80 index + 400 heap = 480) triple; the CPU terms
	// (5 + 0.25 + 10) do not, because the multiplier was measured against page
	// access, not against per-tuple work.
	want := base.Total + 2.0*480.0
	if !approxCost(scaled.Total, want) {
		t.Fatalf("multiplier 3 gave %v; want %v (random-page terms scaled, CPU terms not)",
			scaled.Total, want)
	}
}

// TestEstimateIndexGeometryDerivesPagesAndHeight: goopg has no index-level
// pg_class row, so the geometry is derived from the heap's row count and the
// key width. What must hold is not a specific page count but the two
// properties the cost model reads: more rows means more pages, and a tree tall
// enough to hold them.
func TestEstimateIndexGeometryDerivesPagesAndHeight(t *testing.T) {
	cat, orders, _ := ppiCatalog(t)
	var idx *catalog.Index
	for _, cand := range cat.IndexesOnTable(orders) {
		if cand.Name == "orders_pkey" {
			idx = cand
		}
	}
	if idx == nil {
		t.Fatal("orders_pkey missing from the fixture")
	}

	smallPages, smallTuples, smallHeight := estimateIndexGeometry(idx, orders, 100)
	bigPages, bigTuples, bigHeight := estimateIndexGeometry(idx, orders, 1_500_000)

	if smallTuples != 100 || bigTuples != 1_500_000 {
		t.Fatalf("index tuples = %v/%v; a B-tree has one entry per heap tuple", smallTuples, bigTuples)
	}
	if !(bigPages > smallPages) {
		t.Fatalf("1.5M rows fit in %v pages but 100 rows need %v", bigPages, smallPages)
	}
	if smallHeight != 0 {
		t.Fatalf("a 100-row index has height %d; want 0 (a single leaf page)", smallHeight)
	}
	if bigHeight < 1 {
		t.Fatalf("a 1.5M-row index has height %d; want at least one internal level", bigHeight)
	}
	// An int4 key: 8 (IndexTupleData) + 4 (int4) + 4 (line pointer) = 16.
	if w := indexTupleWidth(idx, orders); w != 16 {
		t.Fatalf("index tuple width = %d; want 16", w)
	}
}

// TestEffectiveCacheSizeMatchesConfigDefault is the drift guard for the one
// GUC this slice added to `costParams`. It cannot join
// TestCostParamsMatchConfigDefaults' table because that test parses every
// BootVal as a bare float, and `effective_cache_size` carries a unit ("4GB"):
// the value in the struct is in PAGES, which is what PG's own variable holds.
func TestEffectiveCacheSizeMatchesConfigDefault(t *testing.T) {
	reg := misc.BuildDefaultRegistry()
	v, ok := reg.Get("effective_cache_size")
	if !ok {
		t.Fatal("GUC effective_cache_size not registered")
	}
	boot := strings.TrimSpace(v.BootVal)
	if !strings.HasSuffix(boot, "GB") {
		t.Fatalf("effective_cache_size BootVal is %q; the conversion below assumes GB", boot)
	}
	gb, err := strconv.ParseFloat(strings.TrimSuffix(boot, "GB"), 64)
	if err != nil {
		t.Fatalf("effective_cache_size BootVal %q: %v", boot, err)
	}
	want := gb * 1024 * 1024 * 1024 / blockSizeBytes
	if got := defaultCostParams().effectiveCacheSize; !approxCost(got, want) {
		t.Fatalf("costParams.effectiveCacheSize = %v pages but the GUC boot value %q is %v pages — drift",
			got, boot, want)
	}
}

// TestBtreeUniqueIndexClampsToOneTuple pins take2 P2-09's unique-index clamp
// (btcostestimate, selfuncs.c): a UNIQUE index with an equality qual on every
// key column matches at most one tuple, whatever the selectivity arithmetic
// produces.
//
// The selectivity route cannot reach 1.0 on its own — it multiplies per-column
// estimates that each carry their own floor — so before this a multi-column
// unique probe was priced for a range scan the index can never perform.
func TestBtreeUniqueIndexClampsToOneTuple(t *testing.T) {
	cp := defaultCostParams()
	in := indexScanInputs{
		relPages:        1000,
		relTuples:       100000,
		indexPages:      300,
		indexTuples:     100000,
		treeHeight:      2,
		selectivity:     0.01, // 1000 tuples by the arithmetic
		totalTablePages: 1000,
		loopCount:       1,
	}

	_, loose := btreeIndexAMCost(cp, in)
	in.uniqueEqualityOnAllKeys = true
	_, clamped := btreeIndexAMCost(cp, in)

	if !(clamped < loose) {
		t.Errorf("a unique index bound on every key column must cost LESS than "+
			"the same scan priced for %g tuples: clamped=%v loose=%v",
			in.selectivity*in.indexTuples, clamped, loose)
	}

	// It must land at the single-tuple price, not merely lower: one index page
	// plus one index tuple plus the descent.
	want := 1*cp.randomPageCost*indexProbeCostMultiplier +
		1*cp.cpuIndexTupleCost +
		float64(in.treeHeight+1)*pageCPUMultiplier*cp.cpuOperatorCost
	if math.Abs(clamped-want) > 1e-9 {
		t.Errorf("clamped index cost = %v, want %v (one page, one tuple, one descent)", clamped, want)
	}

	// The clamp must never RAISE a cost that was already below one tuple.
	in.selectivity = 1e-9
	_, tiny := btreeIndexAMCost(cp, in)
	in.uniqueEqualityOnAllKeys = false
	_, tinyUnclamped := btreeIndexAMCost(cp, in)
	if tiny != tinyUnclamped {
		t.Errorf("the clamp must not move a sub-one-tuple estimate: %v vs %v", tiny, tinyUnclamped)
	}
}

// P1-02: a partial index holds only its predicate's rows, so its tuple
// count is the heap count scaled by the predicate's selectivity — the
// quantity PG reads measured off the index's own pg_class row. Each guard
// below names the fabrication it prevents; without them an unknown or
// default-driven selectivity would zero the index out from under the
// pages math.
func TestEstimateIndexGeometryPartialScalesTuples(t *testing.T) {
	statsTable := func() *catalog.Table {
		return makeStatsTable(&catalog.TableStats{
			RowCount: 1000, Analyzed: true,
			Columns: []catalog.ColumnStats{
				{NDistinct: 500, NullFrac: 0,
					Histogram: []string{"1", "100", "200", "300", "400", "500"}},
			},
		}, []catalog.Column{{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0}})
	}
	mkPartial := func(t *testing.T, tbl *catalog.Table) *catalog.Index {
		t.Helper()
		pe, err := parser.ParseExpr("id < 200")
		if err != nil {
			t.Fatalf("ParseExpr: %v", err)
		}
		return &catalog.Index{Name: "t_id_prtl", Columns: []string{"id"}, HasPredicate: true, Predicate: pe}
	}

	// Histogram [1..500], id<200 -> 0.4: 1000 heap rows become 400 index rows.
	tbl := statsTable()
	_, tuples, _ := estimateIndexGeometry(mkPartial(t, tbl), tbl, 1000)
	if tuples != 400 {
		t.Errorf("partial index tuples = %v, want 400 (1000 x 0.4)", tuples)
	}

	// Non-partial index on the same shape keeps the heap count.
	plain := &catalog.Index{Name: "t_id", Columns: []string{"id"}}
	if _, tuples, _ := estimateIndexGeometry(plain, tbl, 1000); tuples != 1000 {
		t.Errorf("plain index tuples = %v, want 1000", tuples)
	}

	// No statistics: decline to the heap count rather than fabricate from
	// a default-driven selectivity.
	bare := makeStatsTable(nil, []catalog.Column{{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0}})
	bareIdx := &catalog.Index{Name: "t_id_prtl", Columns: []string{"id"}, HasPredicate: true}
	if pe, err := parser.ParseExpr("id < 200"); err == nil {
		bareIdx.Predicate = pe
	}
	if _, tuples, _ := estimateIndexGeometry(bareIdx, bare, 1000); tuples != 1000 {
		t.Errorf("unanalysed partial index tuples = %v, want 1000 (declined)", tuples)
	}

	// Unanalysed despite a stats struct (Analyzed false): same decline.
	unanalyzed := statsTable()
	unanalyzed.Stats.Analyzed = false
	if _, tuples, _ := estimateIndexGeometry(mkPartial(t, unanalyzed), unanalyzed, 1000); tuples != 1000 {
		t.Errorf("unanalysed partial index tuples = %v, want 1000 (declined)", tuples)
	}

	// Unresolvable predicate (nil): decline, never "keep nothing".
	nilPred := &catalog.Index{Name: "t_id_prtl", Columns: []string{"id"}, HasPredicate: true}
	if _, tuples, _ := estimateIndexGeometry(nilPred, tbl, 1000); tuples != 1000 {
		t.Errorf("nil-predicate partial index tuples = %v, want 1000 (declined)", tuples)
	}
}
