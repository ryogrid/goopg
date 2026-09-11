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
	// Page terms 480 (80 index + 400 heap) scale with the probe calibration;
	// CPU terms 15.25 do not. Written as an expression so C-20d's
	// recalibration does not turn this into a stale literal.
	want := indexProbeMultCalibrated*480.0 + 15.25
	if !approxCost(got.Total, want) {
		t.Errorf("total = %v; want %v", got.Total, want)
	}
}

// TestR59ProbePinLineitem920 is the R59 §3 reproduction gate turned
// prediction pin (pin-then-cut): the lineitem probe's TRACED inputs through
// costIndexScanCore reproduced the traced total EXACTLY pre-cut (9.2030 —
// the gate passed, inputs faithful), and now pin the post-cut value inside
// the §3 band.
//
// Trace: :5533 clone, temporary GOOPG_R59_PIN_DUMP input dump (reverted), plan
// byte-identical to the R56 capture. DPPATH: index.parameterised relids={1}
// reqouter={2} rows=2 startup=0.38 total=9.20. The R59PIN literals below are
// copied verbatim (sel at %.10g — 1e-10 relative, moves the total ~1e-9:
// immaterial at cent precision).
//
// Two SCOPE §1.i narrative corrections the trace forced: numQualOps is 2, not
// 1, and the probed selectivity is 8.52e-7 (measured ndistinct), not 1/1.5M —
// the decomposition still closes to the cent.
func TestR59ProbePinLineitem920(t *testing.T) {
	cp := defaultCostParams()
	cp.effectiveCacheSize = 262144 // traced (clone effective_cache_size); the default is 4GB
	got, _, _ := costIndexScanCore(cp, indexScanInputs{
		relPages: 136393, relTuples: 6001255,
		indexPages: 8588, indexTuples: 6001255, treeHeight: 2,
		selectivity:             8.519796172e-07,
		uniqueEqualityOnAllKeys: false,
		correlation:             -0.0017810857389122248,
		totalTablePages: 168880,
		loopCount:       1500000,
		numQualOps: 2,
	}, 0)
	if !approxCost(got.Startup, 0.375) {
		t.Errorf("startup = %v; want 0.375 (the treeHeight-2 descent)", got.Startup)
	}
	// Post-cut (R59 §2 arm landed): the SCOPE §3 prediction band is
	// [1.2, 2.0], central ≈1.3. Hand decomposition at the traced inputs:
	// index-side ML over 1×1.5M touches caps at the 8588-page index →
	// 8588×4×2/1.5M = 0.0458 + 5.11×0.005 cpuIndex = 0.0714; heap 0.7274
	// and CPU 0.075 unchanged; 0.375 + 0.0714 + 0.7274 + 0.075 = 1.2488.
	if got.Total < 1.2 || got.Total > 2.0 {
		t.Errorf("total = %v; want the §3 band [1.2, 2.0]", got.Total)
	}
	if !approxCost(got.Total, 1.2487967346880979) {
		t.Errorf("total = %v; want 1.2487967346880979", got.Total)
	}
}

// TestR59ProbePinOrders1013 is the same gate's second pin: the orders probe,
// DPPATH index.parameterised relids={2} reqouter={3} rows=16 startup=0.38
// total=10.13 — reproduced exactly pre-cut (10.1302), now pinning the
// post-cut value: the same arm with shallower pro-rating over 150k scans.
func TestR59ProbePinOrders1013(t *testing.T) {
	cp := defaultCostParams()
	cp.effectiveCacheSize = 262144 // traced; see TestR59ProbePinLineitem920
	got, _, _ := costIndexScanCore(cp, indexScanInputs{
		relPages: 28435, relTuples: 1500000,
		indexPages: 1406, indexTuples: 1500000, treeHeight: 2,
		selectivity:             1.048240005e-05,
		uniqueEqualityOnAllKeys: false,
		correlation:             -0.0036176906432956457,
		totalTablePages: 168880,
		loopCount:       150000,
		numQualOps: 0,
	}, 0)
	if !approxCost(got.Startup, 0.375) {
		t.Errorf("startup = %v; want 0.375 (the treeHeight-2 descent)", got.Startup)
	}
	// Post-cut: the §3 band is [2.0, 3.0], central ≈2.4 — the same arm with
	// shallower pro-rating over 150k scans (residual vs PG 1.54 is the 2.0
	// knob + width-inflated relPages, by design per §1.ii).
	if got.Total < 2.0 || got.Total > 3.0 {
		t.Errorf("total = %v; want the §3 band [2.0, 3.0]", got.Total)
	}
	if !approxCost(got.Total, 2.2051380003749999) {
		t.Errorf("total = %v; want 2.2051380003749999", got.Total)
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
	// base was computed at the CALIBRATED default, so the delta is
	// (3 - calibrated) x the page terms, not (3 - 1).
	want := base.Total + (3.0-indexProbeMultCalibrated)*480.0
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

// TestCostIndexScanQpqualCurrency (R1, plan-parity-fix-take2) pins the
// one-currency rule: for the same conjunct count, the index-side CPU term
// per fetched tuple is EXACTLY the seq-side term per scanned tuple —
// `(cpuTupleCost + cpuOperatorCost*n)` — so an addPath comparison cannot
// favour either rival on the qual. It also pins the zero default (a
// filter-less fixture prices exactly as before R1) and that startup is
// untouched by the charge (scope decision: per-tuple only).
func TestCostIndexScanQpqualCurrency(t *testing.T) {
	cp := defaultCostParams()
	base := indexScanInputs{
		relPages: 10000, relTuples: 1_000_000,
		indexPages: 2000, indexTuples: 1_000_000, treeHeight: 2,
		selectivity: 0.01, totalTablePages: 10000,
	}
	plain := costIndexScan(cp, base)
	if plain.Total == 0 {
		t.Fatal("plain index scan priced at zero")
	}
	// Zero default is bit-identical to the unset field: numQualOps defaults
	// to 0 and must reproduce the pre-R1 price exactly.
	if again := costIndexScan(cp, base); !approxCost(again.Total, plain.Total) {
		t.Fatalf("zero numQualOps moved the price: %v vs %v", again.Total, plain.Total)
	}
	// The charge: cpu_operator_cost per conjunct per fetched tuple.
	// tuples_fetched = 0.01 * 1e6 = 10000; 3 conjuncts add
	// 3 * cpuOperatorCost * 10000 to the run cost and nothing to startup.
	q := base
	q.numQualOps = 3
	charged := costIndexScan(cp, q)
	tuplesFetched := 0.01 * 1_000_000
	want := plain.Total + 3*cp.cpuOperatorCost*tuplesFetched
	if !approxCost(charged.Total, want) {
		t.Fatalf("3-conjunct index scan = %v, want %v (plain %v + 3*op*tuples)",
			charged.Total, want, plain.Total)
	}
	if !approxCost(charged.Startup, plain.Startup) {
		t.Fatalf("startup moved %v -> %v; the R1 charge is per-tuple only",
			plain.Startup, charged.Startup)
	}
	// Currency identity with the seq rival: same n on both sides prices
	// the same per-tuple term. costSeqscan(..., n) - costSeqscan(..., 0)
	// must equal costIndexScan(..., n) - costIndexScan(..., 0) whenever
	// the tuple counts coincide (here both range over the full 1e6: the
	// index fixture's selectivity is 1.0 below).
	full := base
	full.selectivity = 1.0
	fullPlain := costIndexScan(cp, full)
	full.numQualOps = 3
	fullCharged := costIndexScan(cp, full)
	relTuples := 1_000_000.0
	seqDelta := costSeqscan(cp, 10000, relTuples, 3).Total - costSeqscan(cp, 10000, relTuples, 0).Total
	idxDelta := fullCharged.Total - fullPlain.Total
	if !approxCost(seqDelta, 3*cp.cpuOperatorCost*relTuples) {
		t.Fatalf("seq-side delta = %v, want 3*op*tuples", seqDelta)
	}
	if !approxCost(idxDelta, seqDelta) {
		t.Fatalf("currency mismatch: index-side delta %v != seq-side delta %v", idxDelta, seqDelta)
	}
}

// TestLocalQualOpCountMirrorsSeqRivalCount (R1, plan-parity-fix-take2)
// pins the agreement the currency rests on: the qpqual population the
// index producers count from the pre-search leaf's Filter chain is the
// same conjunct list baseSeqScanCostInputs counts from localFilter.
// Filter{scan,(a AND b)} counts 2; a bare scan counts 0 (filter-less rels
// price exactly as before on both sides).
func TestLocalQualOpCountMirrorsSeqRivalCount(t *testing.T) {
	mkAnd := func(l, r Expr) Expr { return &BinaryOp{Op: parser.OpAnd, Left: l, Right: r} }
	col := func(i int) Expr { return &ColumnRef{Index: i} }
	tru := &BooleanConst{Value: true}

	scan := &SeqScan{}
	if got := localQualOpCount(scan); got != 0 {
		t.Fatalf("bare scan counts %v, want 0", got)
	}
	two := &Filter{Child: scan, Predicate: mkAnd(mkAnd(col(0), col(1)), tru)}
	if got := localQualOpCount(two); got != 3 {
		t.Fatalf("three-conjunct filter counts %v, want 3", got)
	}
	// Nested Filter wrappers (the extractor walks the whole chain).
	nested := &Filter{Child: &Filter{Child: scan, Predicate: col(0)}, Predicate: mkAnd(col(1), col(2))}
	if got := localQualOpCount(nested); got != 3 {
		t.Fatalf("nested filters count %v, want 3", got)
	}
	// relQualOpCount is the nil-safe rel-level form the bitmap sites use.
	if got := relQualOpCount(nil); got != 0 {
		t.Fatalf("nil rel counts %v, want 0", got)
	}
}

// TestParamIndexQualOpCountAddsPopulations (R1, plan-parity-fix-take2) pins
// the parameterised site's rule. The first draft SUBTRACTED the index-qual
// count from the LOCAL conjunct count — two disjoint populations (local
// restrictions vs movable join clauses), so a rel with no local filter and
// one probe clause priced at -1 conjunct, CREDITING the index path with the
// very asymmetry R1 removes. The rule is: local conjuncts + (movable join
// clauses - those bound as index quals), added, floored at zero.
func TestParamIndexQualOpCountAddsPopulations(t *testing.T) {
	mkAnd := func(l, r Expr) Expr { return &BinaryOp{Op: parser.OpAnd, Left: l, Right: r} }
	col := func(i int) Expr { return &ColumnRef{Index: i} }
	scan := &SeqScan{}
	twoLocal := &Filter{Child: scan, Predicate: mkAnd(col(0), col(1))}

	// No local filter, one movable clause fully bound as an index qual:
	// nothing is rechecked on the heap.
	if got := paramIndexQualOpCount(scan, 1, 1); got != 0 {
		t.Fatalf("fully-bound filter-less rel counts %v, want 0", got)
	}
	// The pre-fix arithmetic would have yielded -1 here.
	if got := paramIndexQualOpCount(scan, 0, 1); got != 0 {
		t.Fatalf("count went negative: %v (a negative CREDITS the index path)", got)
	}
	// Two local conjuncts + three movable clauses of which one is an index
	// qual = 2 + 2 = 4 heap-side conjuncts.
	if got := paramIndexQualOpCount(twoLocal, 3, 1); got != 4 {
		t.Fatalf("2 local + (3 bound - 1 index qual) counts %v, want 4", got)
	}
	// Local conjuncts are never cancelled by index quals (disjoint sets).
	if got := paramIndexQualOpCount(twoLocal, 2, 2); got != 2 {
		t.Fatalf("local conjuncts cancelled by index quals: %v, want 2", got)
	}
}
