package planner

import (
	"math"
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/goopg/goopg/internal/catalog"
)

// relsizeFallbackStage selects how many of the three planner consumers read the
// block-count relation-size fallback. 0 (the default) is off, and off means the
// planner behaves exactly as it did before M0125-0003 — no catalog read, no
// estimate, byte-identical plans.
//
// The staging exists because a single flag switching all three consumers at
// once produces one number and no attribution (design §D4):
//
//	1 — EstimateRows / the MultiHashJoin probe-side choice. Shape-neutral for
//	    TPC-H; the cheap first cut. IMPLEMENTED.
//	2 — + the bushy DP seed (rowCounts[i]). Where round 4's statistics
//	    regressions live. NOT YET WIRED — M0125-0003 stage 2.
//	3 — + estimateBaseRelInfo.baseRows. NOT YET WIRED — stage 3.
//
// A stage enables every consumer up to and including itself, so setting 2 or 3
// today yields stage-1 behavior: the later consumers simply do not read the
// flag yet. Values are cumulative by construction and later stages will need no
// re-spelling of the knob.
//
// Default OFF, mirroring costDrivenJoinOrder (bushy.go). Flipping the default
// is deliberately a separate task with its own measurement — M0125-0005 — for
// the reason recorded in design §D5.3: at S-cold this plausibly imports round
// 4's statistics regressions into every un-ANALYZEd server.
var relsizeFallbackStage atomic.Int64

func init() {
	relsizeFallbackStage.Store(int64(parseRelSizeFallbackStage(os.Getenv("GOOPG_RELSIZE_FALLBACK"))))
}

// parseRelSizeFallbackStage maps the GOOPG_RELSIZE_FALLBACK value to a stage.
// "1"/"2"/"3" select a stage; "on"/"true"/"yes" mean stage 1; everything else
// — including the empty string and any unparseable value — is off. An
// unrecognised value must not silently enable a plan-shape change, so the
// failure direction is "off".
func parseRelSizeFallbackStage(v string) int {
	switch s := strings.ToLower(strings.TrimSpace(v)); s {
	case "", "0", "off", "false", "no":
		return 0
	case "on", "true", "yes":
		return 1
	default:
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return 0
		}
		if n > 3 {
			n = 3
		}
		return n
	}
}

// SetRelSizeFallbackStage sets the fallback stage and returns the previous
// value, so a test can restore it and stay order-independent.
func SetRelSizeFallbackStage(stage int) int {
	if stage < 0 {
		stage = 0
	}
	return int(relsizeFallbackStage.Swap(int64(stage)))
}

// relSizeFallbackEnabled reports whether the consumer introduced at the given
// stage should read the fallback.
func relSizeFallbackEnabled(stage int) bool {
	return relsizeFallbackStage.Load() >= int64(stage)
}

// Relation size and width estimation — the inputs the cost model's per-node cost
// functions consume. See docs/design/cost-model/ chapter 05. These are pure
// helpers in phase C2; nothing in the live planner calls them yet (selection is
// still the integer DP), so they cannot change a plan. They are wired into path
// generation in C3/C4.
//
// The load-bearing piece is the estimate_rel_size ROW FALLBACK (§4): column
// statistics survive a restart but TableStats.RowCount does not (ledger pq-P6),
// so on a freshly started server the model would be blind. PostgreSQL faces the
// same problem for a never-analysed relation and solves it in estimate_rel_size
// (plancat.c:1075) by deriving a row count from the live block count and the
// tuple width. Reproducing that here makes the milestone persistence-independent.

// Page geometry, PG-fixed. Mirrors storage.BlockSize (8192) and
// storage.SizeOfPageHeaderData (24); hardcoded with citation to avoid a planner
// -> storage import for two constants that never change.
const (
	blockSizeBytes      = 8192
	pageHeaderSizeBytes = 24
	usableBytesPerBlock = blockSizeBytes - pageHeaderSizeBytes // 8168

	// Per-row heap overhead: the heap-tuple header (23, MAXALIGN'd to 24) plus
	// one item pointer (4). This is upstream's HEAP_OVERHEAD_BYTES_PER_TUPLE,
	// `MAXALIGN(SizeofHeapTupleHeader) + sizeof(ItemIdData)`
	// (access/heap/heapam_handler.c:2100).
	perTupleOverhead = 24 + 4

	// heapDefaultFillfactor is upstream's HEAP_DEFAULT_FILLFACTOR
	// (utils/rel.h:360). catalog.Table.Fillfactor holds 0 when the reloption
	// was never set, which maps to this.
	heapDefaultFillfactor = 100

	// neverAnalyzedMinPages is upstream's "HACK: if the relation has never yet
	// been vacuumed, use a minimum size estimate of 10 pages"
	// (table_block_relation_estimate_size). The point is not accuracy but
	// refusing to believe a brand-new relation will stay tiny.
	neverAnalyzedMinPages = 10

	// maximumRowCount is clamp_row_est's MAXIMUM_ROWCOUNT (optimizer/cost.h),
	// the ceiling it imposes to keep infinite/NaN estimates out of the cost
	// model.
	maximumRowCount = 1e100

	// varlenaDefaultWidth is PG's get_typavgwidth fallback for a varlena column
	// with no usable typmod ("we don't have a clue" — selfuncs/lsyscache). 32.
	// It doubles as the threshold below which a bounded varlena is assumed to
	// fill its declared maximum.
	varlenaDefaultWidth = 32

	// typAvgWidthCap is get_typavgwidth's 1000-byte knee: at or beyond it the
	// declared maximum stops informing the estimate at all, on the theory that
	// a varchar(10000) rarely comes near its limit.
	typAvgWidthCap = 1000

	// varlenaHeaderSize is VARHDRSZ.
	varlenaHeaderSize = 4

	// maxEncodingCharLen is pg_encoding_max_length(GetDatabaseEncoding()) for
	// UTF8 — goopg's only server_encoding (internal/config/defaults.go).
	maxEncodingCharLen = 4

	// numeric_maximum_size's terms (utils/adt/numeric.c): DEC_DIGITS is the
	// decimal digits per NBASE digit, NUMERIC_HDRSZ is
	// `VARHDRSZ + sizeof(uint16) + sizeof(int16)`, and a NumericDigit is int16.
	decDigits         = 4
	numericHeaderSize = varlenaHeaderSize + 2 + 2
	numericDigitSize  = 2
)

// typeWidth estimates the average stored width of one value of type t, in bytes.
// It is get_typavgwidth (postgres/src/backend/utils/cache/lsyscache.c) — the
// function that supplies get_rel_data_width, and hence the tuple width the
// relation-size estimate divides a page by.
//
// Fixed-width types return typlen. Bounded varlena types go through
// type_maximum_size (utils/adt/format_type.c) and then upstream's sliding
// scale, which is the part an intuitive implementation gets wrong:
//
//	BPCHAR        -> the max width IS the only width (blank-padded)
//	<= 32 bytes   -> assume the value fills its declared maximum
//	<  1000 bytes -> 32 + (max-32)/2, "assume 50%"
//	>= 1000 bytes -> 32 + (1000-32)/2 = 516, a fixed estimate, because a
//	                 varchar(10000) rarely comes near its limit
//
// M0125-0003 corrected this. The pre-existing version returned `n + 4` for
// char(n)/varchar(n), which is wrong twice over: typmod counts CHARACTERS, not
// bytes, so the maximum is scaled by the encoding's maximum character length
// (4 under UTF8, goopg's only server_encoding); and the sliding scale was
// missing entirely. Both errors were verified against PG 18.3 — for
// `varchar(20)` upstream estimates 58 bytes where this returned 24, and for
// `char(10)` it estimates 44 where this returned 14. Since the width divides
// into the page size, a 2.4x width error is a 2.4x row-estimate error on the
// char/varchar-heavy schemas (TPC-H, TPC-DS) the fallback exists to serve.
func typeWidth(t catalog.Type) int {
	if t.IsArray {
		// Arrays are varlena with no usable typmod: type_maximum_size
		// returns -1 and get_typavgwidth lands on its "wild guess".
		return varlenaDefaultWidth
	}
	switch t.Name {
	case "bool", "boolean":
		return 1
	case "int2", "smallint":
		return 2
	case "int4", "integer", "int", "date", "float4", "real", "oid":
		return 4
	case "int8", "bigint", "float8", "double precision", "double",
		"money", "time", "timestamp", "timestamptz", "timestamp with time zone",
		"timestamp without time zone":
		return 8
	case "name":
		return 64 // NAMEDATALEN; typlen > 0, so no sliding scale
	case "numeric", "decimal":
		if len(t.Args) >= 1 && t.Args[0] > 0 {
			return typAvgWidthFromMax(numericMaximumSize(int(t.Args[0])), false)
		}
		// Unconstrained numeric has no typmod: type_maximum_size returns -1.
		return varlenaDefaultWidth
	case "char", "bpchar", "character":
		if n := declaredLength(t); n > 0 {
			return typAvgWidthFromMax(varlenaMaximumSize(n), true /* BPCHAR */)
		}
		return varlenaDefaultWidth
	case "varchar", "character varying":
		if n := declaredLength(t); n > 0 {
			return typAvgWidthFromMax(varlenaMaximumSize(n), false)
		}
		return varlenaDefaultWidth
	case "bit", "varbit", "bit varying":
		if n := declaredLength(t); n > 0 {
			// type_maximum_size: (typmod + 7)/8 + 2*sizeof(int32).
			return typAvgWidthFromMax((n+7)/8+8, false)
		}
		return varlenaDefaultWidth
	default:
		// text and every other unbounded/unknown type: "Oops, we have no
		// idea ... wild guess time."
		return varlenaDefaultWidth
	}
}

// declaredLength returns a type's first declared argument (the character or bit
// count), or 0 when unconstrained.
func declaredLength(t catalog.Type) int {
	if len(t.Args) >= 1 && t.Args[0] > 0 {
		return int(t.Args[0])
	}
	return 0
}

// varlenaMaximumSize is type_maximum_size's BPCHAR/VARCHAR arm. Upstream's
// typmod includes VARHDRSZ and counts characters, so the byte maximum is
// `(typmod - VARHDRSZ) * pg_encoding_max_length(encoding) + VARHDRSZ`. goopg
// stores the character count directly, which is that `typmod - VARHDRSZ`.
//
// The encoding factor is fixed at UTF8's 4: server_encoding's BootVal is UTF8
// (internal/config/defaults.go) and goopg supports no other, so reading a GUC
// here would add a dependency without adding a behavior.
func varlenaMaximumSize(chars int) int { return chars*maxEncodingCharLen + varlenaHeaderSize }

// numericMaximumSize is numeric_maximum_size (utils/adt/numeric.c) for a
// declared precision. The `+ 2*(DEC_DIGITS-1)` is upstream's worst case: the
// weight is stored in NumericDigits, so the leading digit group may hold a
// single decimal digit.
func numericMaximumSize(precision int) int {
	digits := (precision + 2*(decDigits-1)) / decDigits
	return numericHeaderSize + digits*numericDigitSize
}

// typAvgWidthFromMax applies get_typavgwidth's sliding scale to a maximum
// width. isBPChar selects the blank-padded case, where "the max width is also
// the only width".
func typAvgWidthFromMax(maxWidth int, isBPChar bool) int {
	if maxWidth <= 0 {
		return varlenaDefaultWidth
	}
	if isBPChar {
		return maxWidth
	}
	if maxWidth <= varlenaDefaultWidth {
		return maxWidth // assume full width
	}
	if maxWidth < typAvgWidthCap {
		return varlenaDefaultWidth + (maxWidth-varlenaDefaultWidth)/2 // assume 50%
	}
	return varlenaDefaultWidth + (typAvgWidthCap-varlenaDefaultWidth)/2
}

// tupleWidth estimates the average width of a row with the given output columns,
// the analogue of PG's set_rel_width (costsize.c). Floored at 1. Consumed by
// cost_sort, the EXPLAIN width column, and the row fallback below.
func tupleWidth(cols []SchemaColumn) int {
	w := 0
	for _, c := range cols {
		w += typeWidth(c.Type)
	}
	if w < 1 {
		w = 1
	}
	return w
}

// relSizeInputs is the argument set of PostgreSQL's
// table_block_relation_estimate_size, named for the upstream fields it stands
// in for so the translation can be checked line by line.
type relSizeInputs struct {
	// curpages is the LIVE block count — upstream's
	// `curpages = RelationGetNumberOfBlocks(rel)`, not pg_class.relpages.
	// Negative means "unknown"; callers get (0, 0) back.
	curpages int64
	// relpages / reltuples are the ANALYZE/VACUUM-maintained counters,
	// catalog.TableStats.Pages / .RowCount.
	relpages  int64
	reltuples int64
	// analyzed is the inverse of upstream's `reltuples < 0` sentinel. See
	// catalog.TableStats.Analyzed for why goopg carries it as a flag.
	analyzed bool
	// hasSubclass is pg_class.relhassubclass. Upstream skips the 10-page
	// floor for inheritance parents: "Totally empty parent tables are quite
	// common, so we should be willing to believe that they are empty."
	hasSubclass bool
	// fillfactor is the table's reloption; 0 (unset) means
	// heapDefaultFillfactor. Only the statistics-less branch consults it —
	// the density branch accounts for it implicitly via real relpages.
	fillfactor int
	// dataWidth is upstream's get_rel_data_width(rel, attr_widths): the sum
	// of the relation's per-attribute average widths, WITHOUT the per-tuple
	// overhead, which this function adds.
	dataWidth int
}

// estimateRelSize reproduces table_block_relation_estimate_size
// (postgres/src/backend/access/table/tableam.c), the function PostgreSQL
// actually runs for a heap relation: estimate_rel_size (plancat.c) dispatches
// RELKIND_HAS_TABLE_AM to table_relation_estimate_size →
// heapam_estimate_rel_size → this. The formula written inline in plancat.c is
// the RELKIND_INDEX branch and is NOT what a table goes through.
//
// It returns the estimated page count and row count. Upstream's third output,
// allvisfrac, is omitted: goopg has no visibility map for the planner to read,
// so there is nothing to report and no consumer to report it to.
//
// The order of the rules is load-bearing and matches upstream exactly:
//
//  1. the never-analyzed 10-page floor, which is skipped for inheritance
//     parents;
//  2. the `curpages == 0 ⇒ tuples = 0` early exit, AFTER the floor, so a
//     never-analyzed empty relation still estimates 10 pages' worth;
//  3. density from real statistics when they exist;
//  4. otherwise density from the tuple width, in INTEGER arithmetic
//     (upstream: "note: integer division is intentional here"), scaled by
//     fillfactor and pushed through clamp_row_est;
//  5. `rint(density * curpages)`.
//
// M0125-0003; design docs/design/0125-0003-relsize-fallback-and-tpch-stats-tradeoff.md §D1.
func estimateRelSize(in relSizeInputs) (pages int64, tuples float64) {
	curpages := in.curpages
	if curpages < 0 {
		return 0, 0
	}

	// (1) "We test 'never vacuumed' by seeing whether reltuples < 0."
	if curpages < neverAnalyzedMinPages && !in.analyzed && !in.hasSubclass {
		curpages = neverAnalyzedMinPages
	}
	pages = curpages

	// (2) Quick exit if the relation is clearly empty.
	if curpages == 0 {
		return 0, 0
	}

	var density float64
	switch {
	case in.analyzed && in.relpages > 0:
		// (3) Previous tuple density. Upstream guards on `reltuples >= 0 &&
		// relpages > 0`; `analyzed` is goopg's spelling of the first half.
		density = float64(in.reltuples) / float64(in.relpages)
	default:
		// (4) No usable statistics: derive density from the declared column
		// types. Upstream disregards alignment here on purpose — "(a) that
		// would be gilding the lily considering how crude the estimate is,
		// (b) it creates platform dependencies in the default plans".
		fillfactor := in.fillfactor
		if fillfactor <= 0 {
			fillfactor = heapDefaultFillfactor
		}
		tupleWidth := in.dataWidth + perTupleOverhead
		if tupleWidth < 1 {
			tupleWidth = 1
		}
		// Integer division on both steps, as upstream: the fillfactor scaling
		// truncates, and so does the division by tuple width. Doing this in
		// float would drift from PG's plans on small relations, where the
		// truncation is proportionally largest.
		density = float64((usableBytesPerBlock * fillfactor / 100) / tupleWidth)
		// "There's at least one row on the page, even with low fillfactor."
		density = clampRowEst(density)
	}
	return pages, math.RoundToEven(density * float64(curpages))
}

// clampRowEst is clamp_row_est (postgres/src/backend/optimizer/path/costsize.c).
// It forces a row estimate to be finite, at least 1, and integral. rint() rounds
// half to even under the default IEEE rounding mode, which is math.RoundToEven.
func clampRowEst(nrows float64) float64 {
	switch {
	case math.IsNaN(nrows) || nrows > maximumRowCount:
		return maximumRowCount
	case nrows <= 1.0:
		return 1.0
	default:
		return math.RoundToEven(nrows)
	}
}

// estimateRelSizeRows is the row half of estimateRelSize for the statistics-less
// case, kept as the entry point the C2 cost helpers already call. Returns 0 for
// an unknown (non-positive) block count rather than applying the never-analyzed
// floor, because its callers pass a block count they already know to be live.
func estimateRelSizeRows(blocks int64, width int) float64 {
	if blocks <= 0 {
		return 0
	}
	_, rows := estimateRelSize(relSizeInputs{
		curpages:  blocks,
		analyzed:  true, // suppress the 10-page floor; blocks is already live
		dataWidth: width,
	})
	return rows
}

// tableDataWidth is get_rel_data_width(rel, attr_widths)
// (postgres/src/backend/optimizer/util/plancat.c): the sum of the relation's
// per-attribute average widths. It is a property of the RELATION, so every
// column counts — not just the ones a particular query projects — and it needs
// no statistics, which is what makes it usable on a cold-started server.
func tableDataWidth(tbl *catalog.Table) int {
	if tbl == nil {
		return 1
	}
	w := 0
	for i := range tbl.Columns {
		w += typeWidth(tbl.Columns[i].Type)
	}
	if w < 1 {
		w = 1
	}
	return w
}

// estimateTableRowsFallback is the plan-time relation-size estimate for one
// base table: goopg's get_relation_info → estimate_rel_size, returning the row
// count PostgreSQL would stamp into RelOptInfo.tuples.
//
// It returns 0 — "no estimate", never "zero rows" — when the fallback is off,
// when the catalog cannot report a live block count, or when the relation is
// analyzed and genuinely empty.
//
// # Why an analyzed relation short-circuits
//
// PostgreSQL runs this unconditionally and SCALES stored statistics by the
// live block count, so a relation that grew since its last ANALYZE gets a
// proportionally larger estimate. goopg deliberately does not: when
// TableStats.RowCount is positive the caller keeps using it and this function
// is not consulted at all (see the call site in EstimateRows).
//
// That divergence is the invariant that makes the flag's A/B honest (design
// §D3): flag-on and flag-off must produce byte-identical plans in any ANALYZEd
// state, so the measured difference is attributable to the cold-start case the
// flag exists for and to nothing else. Adopting upstream's scaling is a
// separate, later decision — recorded in the deferral ledger.
//
// M0125-0003 stage 1.
func estimateTableRowsFallback(cat catalog.Catalog, tbl *catalog.Table) int64 {
	if tbl == nil || tbl.Virtual {
		return 0
	}
	im := inMemoryCat(cat)
	if im == nil {
		return 0
	}
	blocks, ok := im.RelationBlocks(tbl)
	if !ok {
		return 0
	}
	var (
		rowCount  int64
		relpages  int64
		analyzed  bool
		fillfactr = tbl.Fillfactor
	)
	if tbl.Stats != nil {
		rowCount = tbl.Stats.RowCount
		relpages = int64(tbl.Stats.Pages)
		analyzed = tbl.Stats.Analyzed
	}
	_, rows := estimateRelSize(relSizeInputs{
		curpages:  blocks,
		relpages:  relpages,
		reltuples: rowCount,
		analyzed:  analyzed,
		// A partitioned parent is upstream's relhassubclass case: it holds no
		// rows of its own, so the 10-page floor must not be applied to it.
		// Plain (non-partition) inheritance parents are NOT detected here —
		// see the deferral ledger row for M0125-0003.
		hasSubclass: len(tbl.PartitionKey) > 0,
		fillfactor:  fillfactr,
		dataWidth:   tableDataWidth(tbl),
	})
	if rows <= 0 {
		return 0
	}
	if rows > float64(math.MaxInt64) {
		return math.MaxInt64
	}
	return int64(rows)
}

// nodeTupleWidth estimates the average output-row width of any plan node from
// its schema — the width the cost model charges for a scan leaf.
func nodeTupleWidth(n Node) int {
	if n == nil {
		return 1
	}
	return tupleWidth(n.Output())
}

// estScanPages estimates a relation's block count from its row count and width —
// the inverse of the estimate_rel_size density (§4). Used to feed cost_seqscan a
// page count inside the join-order DP, where the live smgr block count is not in
// scope; scan cost is order-independent, so the approximation does not perturb the
// order ranking, only its absolute scale.
func estScanPages(rows float64, width int) int64 {
	if rows < 1 {
		rows = 1
	}
	pages := int64(math.Ceil(rows * float64(width) / float64(usableBytesPerBlock)))
	if pages < 1 {
		pages = 1
	}
	return pages
}

// baseRelRows is the set_baserel_size_estimates analogue for a base relation's
// pre-filter row count (design ch. 05 §1, §4). A warm ANALYZE'd RowCount wins;
// otherwise the estimate_rel_size fallback derives rows from the live block count
// and width, so the estimate is a coarse number rather than 0 on a cold-started
// server. Local-filter selectivity is applied by the caller on top of this, only
// when reliable (design ch. 05 §1) — kept separate so the two concerns do not
// entangle.
func baseRelRows(rowCount, blocks int64, width int) float64 {
	if rowCount > 0 {
		return float64(rowCount) // warm: ANALYZE ran this session
	}
	return estimateRelSizeRows(blocks, width) // cold: block-derived, no persistence
}
