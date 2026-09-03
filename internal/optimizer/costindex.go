package optimizer

// M0127-P5.4c-ii-b — `cost_index` (costsize.c:520): what it costs to read a
// base relation through a B-tree index, priced in the search's one currency.
//
// PG oracle: `cost_index` (costsize.c:520), the Mackert-Lohman page estimator
// `index_pages_fetched` (costsize.c:906), `genericcostestimate`
// (selfuncs.c:7080) and the B-tree descent charge `btcostestimate` adds on top
// (selfuncs.c:7780-7783), and `btcost_correlation` (selfuncs.c:7305) for the
// correlation the interpolation reads. Design: leftdeep-joins 04 §1.1.
//
// Why this is a slice of its own and not a term inside the path constructor:
// `paramIndexScanCost` (pathparamindex.go) prices ONE bound probe off the
// calibrated `indexProbeCost`, and it is deliberately not a scan model — 04 §1
// forbids two independently-calibrated index cost models competing inside one
// `addPath` comparison. This file is therefore the scan model, and it is tied
// to the SAME calibration knob (`indexProbeCostMultiplier`) rather than to a
// second one of its own: everywhere PG charges a RANDOM page fetch, goopg
// charges the multiplier times that, because the multiplier exists precisely
// because goopg's index access materialises eagerly and so runs slower than
// `random_page_cost` predicts. Sequential fetches and CPU terms are PG-native,
// since neither is what the multiplier was measured against.
//
// BOTH `loop_count` arms are reproduced. The `loop_count > 1` arm prices a
// parameterised path inside a nested loop and returns the cost of ONE
// execution — PG pro-rates by `loop_count` itself — so it keeps the convention
// the single-scan arm and the join arm already share ("cost one execution; the
// join above multiplies"). There is no second model: the flat-probe function
// that used to price parameterised paths separately is gone.

import (
	"math"

	"github.com/goopg/goopg/internal/catalog"
)

// pageCPUMultiplier is upstream's DEFAULT_PAGE_CPU_MULTIPLIER (selfuncs.c):
// the CPU charge per B-tree page descended through, in cpu_operator_cost
// units. PG's comment is the reason it must not be dropped: without it a
// bloated index would look exactly as cheap as an unbloated one whenever a
// single leaf page is visited.
const pageCPUMultiplier = 50.0

// btreeDefaultFillfactor is upstream's BTREE_DEFAULT_FILLFACTOR (nbtree.h):
// leaf pages are packed to 90%, so an index's page count is derived from 90%
// of the usable space rather than all of it.
const btreeDefaultFillfactor = 90

// indexScanInputs is everything `cost_index` needs that is not a GUC. The
// fields are named for the PG variables they stand in for, because the whole
// point of the struct is that the caller must supply PG's inputs — in
// particular `relTuples`, which is the relation's PRE-restriction tuple count
// (`baserel->tuples`) and NOT the post-filter row estimate a goopg path
// carries. An index scan without index clauses reads every tuple and filters
// afterwards; charging it over the filtered count would make a full index scan
// look cheaper the more selective the query's local quals are.
type indexScanInputs struct {
	relPages    int64   // baserel->pages
	relTuples   float64 // baserel->tuples, pre-restriction
	indexPages  float64 // index->pages
	indexTuples float64 // index->tuples
	treeHeight  int     // index->tree_height
	// selectivity is `indexSelectivity`: the fraction of the heap the index
	// quals admit. 1.0 for an ordering-only scan, which is the only kind this
	// slice generates (the search's clause list holds join clauses, and a
	// clause used as an index qual would make the path parameterised).
	selectivity float64

	// uniqueEqualityOnAllKeys is `btcostestimate`'s unique-index clamp
	// (selfuncs.c): a UNIQUE index with an equality qual on EVERY key column
	// matches at most one tuple, whatever the selectivity arithmetic says.
	// take2 P2-09.
	uniqueEqualityOnAllKeys bool
	// correlation is `indexCorrelation` — see indexCorrelationFor for why it
	// is 0 for every index goopg has today.
	correlation float64
	// totalTablePages is `root->total_table_pages`: the pages of every base
	// relation in the query, which is what the cache budget is pro-rated
	// between.
	totalTablePages float64
	// loopCount is `cost_index`'s parameter of the same name: how many times
	// this scan is expected to be RE-executed, which for a parameterised path
	// is the row count of the outer relations supplying its parameter
	// (`get_loop_count`, indxpath.c:3266). 0 or 1 means a single execution.
	//
	// It exists so the returned cost stays "one execution": PG pro-rates the
	// amortised total back down by `loopCount` for exactly that reason, which
	// is what lets the join arm keep multiplying by the outer row count without
	// double-counting.
	loopCount float64

	// indexOnly / allVisFrac are `cost_index`'s `indexonly` argument and
	// `baserel->allvisfrac`. See `heapPagesAfterVM`. Zero values give exactly
	// the pre-existing behaviour, so every caller that does not set them is
	// costing a plain index scan as before.
	indexOnly  bool
	allVisFrac float64
}

// costIndexScan is `cost_index` (costsize.c:520) for a single, non-parallel,
// non-index-only scan.
//
// The shape to keep in view is the interpolation at the end. PG computes TWO
// I/O costs and blends them by the square of the index-to-heap correlation:
//
//   - `max_IO_cost` — the uncorrelated case: Mackert-Lohman says how many
//     distinct heap pages a fetch of N tuples touches, and every one of them is
//     a random fetch;
//   - `min_IO_cost` — the perfectly correlated case (a freshly CLUSTERed
//     table): exactly `selectivity * pages` pages, of which only the first is
//     random and the rest are sequential.
//
// A goopg index whose leading column has no correlation statistic prices at
// `max_IO_cost` (indexCorrelationFor returns 0 when stats are missing), which
// matches PG's default for the uncorrelated case. When ANALYZE has collected a
// correlation statistic, the blend takes effect and the cost interpolates
// between the random and sequential extremes.
func costIndexScan(cp costParams, in indexScanInputs) Cost {
	idxStartup, idxTotal := btreeIndexAMCost(cp, in)
	startupCost := idxStartup
	runCost := idxTotal - idxStartup

	tuplesFetched := clampRowEst(in.selectivity * in.relTuples)

	var maxIOCost, minIOCost float64
	if in.loopCount > 1 {
		// The repeated-scan arm (costsize.c:598-640). Its whole content is one
		// idea: a scan run `loopCount` times does NOT cost `loopCount` times a
		// single scan, because the pages the second scan wants are largely the
		// ones the first already brought in. PG expresses that by running
		// Mackert-Lohman over the TOTAL tuples all the scans fetch and then
		// pro-rating back to one scan, so the total grows sublinearly in
		// `loopCount` and the quotient shrinks.
		//
		// "In this case we assume all the fetches are random accesses" — so
		// unlike the single-scan arm below there is no seq_page_cost term in
		// either bound.
		pagesFetched := indexPagesFetched(tuplesFetched*in.loopCount, in.relPages, in.indexPages, in.totalTablePages, cp.effectiveCacheSize)
		pagesFetched = in.heapPagesAfterVM(pagesFetched)
		maxIOCost = (pagesFetched * cp.randomPageCost * indexProbeCostMultiplier) / in.loopCount

		// The correlated bound applies the same formula one level up, at PAGE
		// rather than tuple grain: each scan touches `selectivity * relPages`
		// pages, and caching across scans is what Mackert-Lohman then estimates.
		pagesFetched = math.Ceil(in.selectivity * float64(in.relPages))
		pagesFetched = indexPagesFetched(pagesFetched*in.loopCount, in.relPages, in.indexPages, in.totalTablePages, cp.effectiveCacheSize)
		pagesFetched = in.heapPagesAfterVM(pagesFetched)
		minIOCost = (pagesFetched * cp.randomPageCost * indexProbeCostMultiplier) / in.loopCount
	} else {
		// max_IO_cost: the perfectly uncorrelated case (csquared = 0).
		pagesFetched := indexPagesFetched(tuplesFetched, in.relPages, in.indexPages, in.totalTablePages, cp.effectiveCacheSize)
		pagesFetched = in.heapPagesAfterVM(pagesFetched)
		maxIOCost = pagesFetched * cp.randomPageCost * indexProbeCostMultiplier

		// min_IO_cost: the perfectly correlated case (csquared = 1). One random
		// fetch, then sequential ones.
		if correlatedPages := in.heapPagesAfterVM(math.Ceil(in.selectivity * float64(in.relPages))); correlatedPages > 0 {
			minIOCost = cp.randomPageCost * indexProbeCostMultiplier
			if correlatedPages > 1 {
				minIOCost += (correlatedPages - 1) * cp.seqPageCost
			}
		}
	}

	csquared := in.correlation * in.correlation
	runCost += maxIOCost + csquared*(minIOCost-maxIOCost)

	// CPU: `cpu_tuple_cost` per tuple actually fetched from the heap. The
	// qpqual per-tuple term PG adds here is zero for goopg's search, for the
	// same reason `buildInitialRels` passes numQualOps = 0: a base relation's
	// local quals live inside the already-built leaf node and their cost was
	// spent when `estimateBaseRelInfo` priced it. Charging them again here
	// would double-count them against the seq-scan rival that does not.
	runCost += cp.cpuTupleCost * tuplesFetched

	return Cost{Startup: startupCost, Total: startupCost + runCost}
}

// btreeIndexAMCost is the `amcostestimate` callback pair
// (`genericcostestimate` + `btcostestimate`'s additions) reduced to the case
// this slice generates: a forward B-tree scan with NO index quals.
//
// With no quals the generic estimator's inputs collapse a long way —
// `indexSelectivity` is 1, `num_sa_scans` is 1, `qual_arg_cost` and
// `qual_op_cost` are both 0, and `num_scans == 1` takes the "just charge
// random_page_cost per index page touched" branch. What survives is what
// matters for an ordering-only path: every index page is read, every index
// tuple is inspected, and the descent is charged once.
//
// Returns (startup, total) for the index side alone; `costIndexScan` adds the
// heap side.
func btreeIndexAMCost(cp costParams, in indexScanInputs) (startup, total float64) {
	numIndexTuples := in.selectivity * in.indexTuples
	if numIndexTuples < 0 {
		numIndexTuples = 0
	}
	// The unique-index clamp (btcostestimate, selfuncs.c). PG:
	//
	//	if (index->unique && indexBoundQuals != NIL && eqQualHere &&
	//	    <equality on every key column>)
	//	        numIndexTuples = 1.0;
	//
	// The selectivity route cannot reach 1.0 on its own here: it multiplies
	// per-column estimates that each carry their own floor, so a multi-column
	// unique probe came out well above one tuple and the index path was priced
	// for a range scan it can never perform. take2 P2-09.
	if in.uniqueEqualityOnAllKeys && numIndexTuples > 1 {
		numIndexTuples = 1
	}
	numIndexPages := 1.0
	if in.indexPages > 1 && in.indexTuples > 1 {
		numIndexPages = math.Ceil(numIndexTuples * in.indexPages / in.indexTuples)
	}

	// Index pages are random fetches, so they carry goopg's probe calibration
	// (see the file header). With the default multiplier of 1.0 this is PG's
	// own arithmetic exactly.
	total = numIndexPages * cp.randomPageCost * indexProbeCostMultiplier
	total += numIndexTuples * cp.cpuIndexTupleCost

	// The B-tree descent (selfuncs.c:7780): tree height plus the leaf page,
	// at 50 cpu_operator_cost per page. Charged at startup as well as total —
	// it is paid before the first tuple emerges, which is what makes an
	// ordered index path's startup cost non-zero and therefore comparable
	// against a sort's.
	descent := float64(in.treeHeight+1) * pageCPUMultiplier * cp.cpuOperatorCost
	startup = descent
	total += descent
	return startup, total
}

// indexPagesFetched is `index_pages_fetched` (costsize.c:906) — the Mackert
// and Lohman "index scans with cache" formula, reproduced branch for branch.
//
// What it answers: fetching `tuplesFetched` tuples from a `pages`-page table
// touches how many DISTINCT pages, given that some of them are already
// cached? The two branches are the two regimes:
//
//   - `T <= b` — the whole table fits in this query's pro-rated share of the
//     cache, so the only re-reads that cost anything are the ones inside a
//     single scan, and the formula is the plain 2TN/(2T+N) birthday-problem
//     estimate;
//   - `T > b` — it does not fit, so beyond a limit `lim` every further tuple
//     costs a fresh page at the rate (T-b)/T.
//
// `effectiveCacheSize` is in pages, as PG's variable is.
func indexPagesFetched(tuplesFetched float64, pages int64, indexPages, totalTablePages, effectiveCacheSize float64) float64 {
	// T is # pages in table, but don't allow it to be zero.
	T := 1.0
	if pages > 1 {
		T = float64(pages)
	}

	totalPages := totalTablePages + indexPages
	if totalPages < 1 {
		totalPages = 1
	}
	// PG asserts T <= total_pages, which holds because `total_table_pages`
	// sums every base rel in the query INCLUDING this one. goopg's caller
	// computes the sum the same way, but the clamp is kept rather than
	// asserted: a caller that passed a partial sum would otherwise be handed a
	// cache share larger than the table, which reads as "fully cached" and
	// silently zeroes the I/O the formula exists to charge.
	if totalPages < T {
		totalPages = T
	}

	// b is the pro-rated share of effective_cache_size, forced positive and
	// integral.
	b := effectiveCacheSize * T / totalPages
	if b <= 1.0 {
		b = 1.0
	} else {
		b = math.Ceil(b)
	}

	var pagesFetched float64
	if T <= b {
		pagesFetched = (2.0 * T * tuplesFetched) / (2.0*T + tuplesFetched)
		if pagesFetched >= T {
			pagesFetched = T
		} else {
			pagesFetched = math.Ceil(pagesFetched)
		}
		return pagesFetched
	}

	lim := (2.0 * T * b) / (2.0*T - b)
	if tuplesFetched <= lim {
		pagesFetched = (2.0 * T * tuplesFetched) / (2.0*T + tuplesFetched)
	} else {
		pagesFetched = b + (tuplesFetched-lim)*(T-b)/T
	}
	return math.Ceil(pagesFetched)
}

// indexCorrelationFor is `btcost_correlation` (selfuncs.c:7305): the Pearson
// correlation between the index's physical row order and the leading column's
// logical (sorted) order. It reads the STATISTIC_KIND_CORRELATION slot
// (stored as ColumnStats.Correlation), collected by ANALYZE
// (operators_analyze.go:746), negates for a DESC key, and dilutes by 0.75 for
// multi-column indexes (extra columns weaken the ordering, selfuncs.c:7330).
// When the leading column has no correlation statistic, it returns 0 (PG's
// default for the uncorrelated case — selfuncs.c:7237).
func indexCorrelationFor(idx *catalog.Index, stats *catalog.ColumnStats) float64 {
	if idx == nil || stats == nil {
		return 0
	}
	varCorrelation := stats.Correlation

	// DESC key: negate the correlation (PG's reverse_sort check).
	if len(idx.ColDescending) > 0 && idx.ColDescending[0] {
		varCorrelation = -varCorrelation
	}

	// Multi-column index: later keys dilute the leading column's ordering
	// (PG uses a flat 0.75 factor — btcost_correlation, selfuncs.c:7330).
	if len(idx.Columns) > 1 {
		return varCorrelation * 0.75
	}
	return varCorrelation
}

// estimateIndexGeometry derives the `index->pages`, `index->tuples` and
// `index->tree_height` that PG reads off the index relation's own pg_class row
// and root page.
//
// goopg has none of those: `catalog.Index` carries no relpages/reltuples, and
// ANALYZE does not visit indexes, so there is nothing to read. The estimate is
// therefore built from what IS known — the heap's row count and the declared
// width of the key columns — using the same page arithmetic `estScanPages`
// uses for the heap, at the B-tree default fillfactor. Ledgered: the resume
// point is index-level catalog statistics, not a better formula.
//
// `relTuples` is the heap's pre-restriction row count; a B-tree has one entry
// per live heap tuple, so it is also the index's tuple count.
func estimateIndexGeometry(idx *catalog.Index, tbl *catalog.Table, relTuples float64) (indexPages, indexTuples float64, treeHeight int) {
	indexTuples = relTuples
	if indexTuples < 1 {
		indexTuples = 1
	}
	// Partial-index tuple count (take2 P1-02): a partial index holds only
	// the rows its predicate admits, so sizing it at the heap count
	// overstates it by 1/selectivity — against PG, which reads the measured
	// count off the index's own pg_class row. goopg keeps no per-index
	// measurement (its pg_class row reports the heap count), so the count
	// is derived from the predicate's selectivity instead: the same
	// quantity PG measured, computed rather than read.
	//
	// Guards, each naming the fabrication it prevents:
	//   - non-partial indexes keep the heap count (a complete index has one
	//     entry per live heap tuple — the same rule pg_class reports by);
	//   - unresolvable predicates decline (an unknown predicate must not
	//     read as "keep nothing");
	//   - unanalysed relations decline (without column statistics the
	//     selectivity is default-driven, and a DEFAULT-sized index is a
	//     fabricated number, not an estimate);
	//   - selectivity outside (0,1] declines (an empty or meaningless
	//     result must not zero the index out from under the pages math).
	// The predicate is evaluated against a bare SeqScan over the table:
	// selectivity resolution reads only table statistics, never plan
	// coordinates, so no search context is needed here.
	if idx != nil && idx.HasPredicate && tbl != nil &&
		tbl.Stats != nil && tbl.Stats.Analyzed && len(tbl.Stats.Columns) > 0 {
		if rp, err := ResolveIndexPredicate(idx.Predicate, tbl); err == nil && rp != nil {
			if sel := clauseSelectivity(rp, &SeqScan{Table: tbl}); sel > 0 && sel <= 1 {
				indexTuples = relTuples * sel
				if indexTuples < 1 {
					indexTuples = 1
				}
			}
		}
	}
	width := indexTupleWidth(idx, tbl)
	fill := btreeDefaultFillfactor
	if idx != nil && idx.Fillfactor >= 10 && idx.Fillfactor <= 100 {
		fill = idx.Fillfactor
	}
	perPage := math.Floor(float64(usableBytesPerBlock) * float64(fill) / 100.0 / float64(width))
	if perPage < 1 {
		perPage = 1
	}
	indexPages = math.Ceil(indexTuples / perPage)
	if indexPages < 1 {
		indexPages = 1
	}
	// The REAL file size when storage can answer — `get_relation_info`
	// (plancat.c:466) reads `RelationGetNumberOfBlocks(indexRelation)`, the
	// actual block count, and estimates nothing. The width-derived guess above
	// stays as the fallback (planning against a catalog with no storage
	// attached, e.g. unit tests), and it is a GUESS with a known failure:
	// HammerDB declares TPC-H's integer keys NUMERIC, `typeWidth` prices an
	// unconstrained-precision key at 32 bytes, and every such index came out
	// 2x its real size — supplier_nation_fkidx derived 60 pages against 30
	// real, which alone held the bitmap-vs-index probe choice on the wrong
	// side of PG's (M0134-0183). `perPage` is re-derived from the real count
	// so the tree-height estimate below reads the real fanout.
	if real, ok := catalog.IndexRealPages(idx); ok && real > 0 {
		indexPages = float64(real)
		if perPage < indexTuples/indexPages {
			perPage = math.Ceil(indexTuples / indexPages)
		}
	}

	// Tree height: how many INTERNAL levels sit above the leaves. PG's
	// `_bt_getrootheight` reports 0 for a single-page index, which is what
	// log_fanout(tuples) - 1 gives once the leaf level is discounted.
	treeHeight = 0
	if perPage > 1 && indexTuples > perPage {
		levels := math.Ceil(math.Log(indexTuples) / math.Log(perPage))
		if levels > 1 {
			treeHeight = int(levels) - 1
		}
	}
	return indexPages, indexTuples, treeHeight
}

// indexTupleWidth is the on-page size of one B-tree entry: the key columns'
// declared widths plus upstream's per-entry overhead — `IndexTupleData` (8
// bytes, MAXALIGNed) and the leaf page's `ItemIdData` line pointer (4).
// INCLUDE columns are counted too: they are stored in the leaf entries even
// though they are not key columns.
func indexTupleWidth(idx *catalog.Index, tbl *catalog.Table) int {
	const indexTupleHeaderBytes = 8
	const linePointerBytes = 4
	w := 0
	if idx != nil && tbl != nil {
		for _, name := range append(append([]string{}, idx.Columns...), idx.IncludeColumns...) {
			for i := range tbl.Columns {
				if tbl.Columns[i].Name == name {
					w += typeWidth(tbl.Columns[i].Type)
					break
				}
			}
		}
	}
	if w < 1 {
		// An expression-only or unresolvable key: fall back to one word rather
		// than to zero, which would divide by zero in the page arithmetic.
		w = 8
	}
	return indexTupleHeaderBytes + w + linePointerBytes
}

// heapPagesAfterVM is `cost_index`'s index-only reduction (costsize.c:589-596):
//
//	if (indexonly)
//	    pages_fetched = ceil(pages_fetched * (1.0 - baserel->allvisfrac));
//
// An index-only scan still visits the heap for any page the visibility map
// does not mark all-visible, so the saving is the all-visible FRACTION and
// nothing more: on a never-vacuumed table `allVisFrac` is 0 and an index-only
// scan costs exactly what the equivalent index scan costs — the preference is
// earned from the visibility map, not granted. Applied at every one of
// `cost_index`'s four `pages_fetched` sites because PG applies it to the PAGE
// COUNT and the two IO bounds derive from that count with different per-page
// prices (M0134-0187, DESIGN §15).
func (in indexScanInputs) heapPagesAfterVM(pages float64) float64 {
	if !in.indexOnly {
		return pages
	}
	frac := in.allVisFrac
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	return math.Ceil(pages * (1 - frac))
}
