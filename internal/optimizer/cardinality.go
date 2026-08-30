// Cardinality estimation for the v0 planner.
//
// The estimates are deliberately rough — upstream's full cost
// model relies on histograms and MCV lists that v0 doesn't yet
// collect. The aim here is to give the cost-based planner work
// (still in flight) a starting point grounded in real
// per-relation `reltuples` and per-column NDistinct from
// ANALYZE. For now the estimates are surfaced through
// EXPLAIN so operators can verify the math.
//
// Selectivity defaults match upstream when no stats apply:
//
//   - eq predicate: 1 / NDistinct(col), or 0.005 (= 1/200) when
//     NDistinct unavailable. Matches `default_statistics_target`
//     fallback.
//   - inequality / range: 0.333 (1/3) — upstream's
//     DEFAULT_INEQ_SEL.
//   - generic Filter without key shape: 1/3.
//
// See docs/design/0003-0003-statistics-and-cardinality.md.
package optimizer

import (
	"math"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

const (
	defaultEqSelectivity      = 0.005
	defaultIneqSelectivity    = 1.0 / 3.0
	defaultGenericSelectivity = 1.0 / 3.0
)

// EstimateRows returns the planner's per-node row-count estimate.
// 0 means "no estimate available" — used by callers that want to
// skip cost-based decisions when stats haven't been collected.
//
// Estimates flow bottom-up: SeqScan reads the catalog's
// TableStats; Filter/Limit/Join/Aggregate/Project/Sort scale
// their children; Values is exact.
func EstimateRows(n Node) int64 {
	switch x := n.(type) {
	case *SeqScan:
		return seqScanRows(x)
	case *IndexScan:
		// Equality probe → 1 row per call site; a bound-less scan reads the
		// whole relation (M0127-P5.9-h, see indexScanRows).
		return indexScanRows(x.Table, x.Index, x.Key, x.Keys, x.LowKey, x.HighKey)
	case *IndexOnlyScan:
		// Same two shapes as *IndexScan, and the full-range one is REACHABLE
		// without the join search: planner.go's sort-avoidance rewrite builds
		// an ordered full-range IOS with nil Key/Keys/LowKey/HighKey.
		return indexScanRows(x.Table, x.Index, x.Key, x.Keys, x.LowKey, x.HighKey)
	case *Values:
		return int64(len(x.Rows))
	case *Filter:
		child := EstimateRows(x.Child)
		if child <= 0 {
			return 0
		}
		return scaleByFloat(child, filterSelectivity(x))
	case *Limit:
		child := EstimateRows(x.Child)
		if lim, ok := constInt(x.Limit); ok {
			if child <= 0 || lim < child {
				return lim
			}
		}
		return child
	case *Sort:
		return EstimateRows(x.Child)
	case *Project:
		return EstimateRows(x.Child)
	case *Distinct:
		return EstimateRows(x.Child)
	case *DistinctOn:
		// M0127-P5.6-g-ii: another of the pass-through wrappers whose absence
		// zeroes every estimate above it (the M0125-0038 class documented
		// below). Neither this nor `*Distinct` is SIZED — upstream runs
		// `estimate_num_groups` over the DISTINCT clause and goopg does not —
		// which is a ledgered gap, not this arm's business.
		return EstimateRows(x.Child)
	case *WindowAgg:
		return EstimateRows(x.Child)
	case *Join:
		return estimateJoin(x)
	case *OrdinalityWrap:
		// Pass-through wrapper: appends an ordinal column, row count
		// unchanged (S4a scalar residual rewrite reuses it).
		return EstimateRows(x.Child)
	case *Aggregate:
		return estimateAggregate(x)
	case *Insert:
		return EstimateRows(x.Source)
	case *Update, *Delete, *DDL, *Transaction, *Checkpoint, *Utility, *Copy:
		return 0
	case *Explain:
		return EstimateRows(x.Child)

	// M0125-0038 (C5): pass-through wrappers. Before these arms existed,
	// any one of them anywhere in a subtree zeroed every estimate above it
	// (child <= 0 propagates as "no estimate"), which is why all 18 plans
	// in the M0125-0026 capture rendered rows=1 on every non-leaf node.
	case *Gather:
		// Total-row estimate; the per-worker division PG shows under a
		// Gather is a cost-model concern (the 0077 line), not a
		// cardinality one.
		return EstimateRows(x.Child)
	case *GatherMerge:
		return EstimateRows(x.Child)
	case *LockRows:
		return EstimateRows(x.Child)
	case *Memoize:
		return EstimateRows(x.Child)
	case *CTEScan:
		// Stage-A inlining clones the body per consumer, so Child is this
		// reference's own subtree — its estimate IS this scan's output.
		// (A recursive CTE plans as *RecursiveUnion + *WorkTableScan,
		// neither of which recurses back here.)
		return EstimateRows(x.Child)
	case *CTEDMLPrefix:
		return EstimateRows(x.Body)
	case *SetOp:
		return estimateSetOp(x)
	case *NestedLoopIndexJoin:
		return estimateNLIndexJoin(x)
	}
	return 0
}

// filterSelectivity prices a `*Filter`'s Predicate, skipping the conjuncts
// a qual-placement pass duplicated onto a descendant (`Filter.PushedBelow`).
//
// Those conjuncts have ALREADY been charged: the copy hangs on the node it
// was pushed to, and `EstimateRows` of that node scales by it. Charging the
// original here too squares the restriction's selectivity, and it lands on
// the JOIN above — so every join sitting over a filtered scan was
// under-sized by exactly the factor its own scan had already applied. On
// TPC-DS SF0.5 the row-preserving `store_sales ⋈ store` (unique
// `s_store_sk`, 12 rows) came out at 367 128 instead of its left input's
// 726 987 with `d_year > 1999` present, and at 1 439 608 — correct — with
// no restriction at all; PG's same join is 2 583 → 2 465. That collapse is
// what made a nested loop look free over Q47's `v1` CTE (design doc
// leftdeep-joins/09 §5.17).
//
// Splitting the predicate and multiplying is not a different formula from
// the whole-predicate call it replaces: `clauseSelectivity`'s OpAnd arm is
// itself `left * right` under the independence assumption, so an empty
// PushedBelow reproduces the former number exactly.
//
// M0127-P5.6-f-vi.
func filterSelectivity(f *Filter) float64 {
	if f == nil || f.Predicate == nil {
		return 1.0
	}
	if len(f.PushedBelow) == 0 {
		return clauseSelectivity(f.Predicate, f.Child)
	}
	sel := 1.0
	for _, c := range splitAnd(f.Predicate) {
		if f.pricedBelow(c) {
			continue
		}
		sel *= clauseSelectivity(c, f.Child)
	}
	if sel > 1 {
		return 1
	}
	if sel < 0 {
		return 0
	}
	return sel
}

// estimateSetOp mirrors upstream's output-row rules
// (prepunion.c:1146-1151): EXCEPT keeps the left input's count,
// INTERSECT the smaller input's, UNION ALL the sum. For the
// non-ALL forms upstream runs estimate_num_groups on the input
// (prepunion.c `estimate_size`), and the dedup here is still
// approximated as /2.
//
// M0127-P5.6-f-vii built that estimator — `estimateNumGroups`
// below — but wiring it here needs the set-op's output columns
// expressed as grouping expressions over EACH input, which this
// node does not carry. Ledgered as `estimate-num-groups setop-dedup`
// rather than folded in: the sweep that measures the aggregate
// change must not also be measuring a set-op change.
func estimateSetOp(s *SetOp) int64 {
	l := EstimateRows(s.Left)
	r := EstimateRows(s.Right)
	if l <= 0 || r <= 0 {
		return 0
	}
	var out int64
	switch s.Op {
	case parser.SetOpIntersect:
		out = l
		if r < out {
			out = r
		}
	case parser.SetOpExcept:
		out = l
	default: // UNION
		out = l + r
	}
	if !s.All {
		out /= 2
	}
	if out < 1 {
		return 1
	}
	return out
}

// estimateNLIndexJoin: the inner side is an equality index probe,
// which this file already estimates at 1 row per call site
// (*IndexScan above), so the join carries the outer's cardinality.
// LEFT keeps the same count by null-extension.
func estimateNLIndexJoin(j *NestedLoopIndexJoin) int64 {
	return EstimateRows(j.Outer)
}

// `estimateMultiHashJoin` was deleted with the node by M0127-P6.2. It
// mirrored the `*Join` arm's method — start at the probe table's row count,
// walk the key chain, apply `l·r / max(nd_l, nd_r)` per step — and existed
// because before M0126-0002 every packed MHJ estimated 0 rows, which every
// ancestor's BuildLeft/algorithm decision above the chain was then taken on.

// seqScanRows is the base-relation row estimate for a scan leaf — the
// stage-1 consumer of the relation-size fallback (M0125-0003 §D4).
//
// A warm ANALYZE'd RowCount always wins: when it is positive this returns
// exactly what it returned before the fallback existed, which is design §D3's
// invariant ("flag-on and flag-off must produce byte-identical plans in any
// ANALYZEd state") and is asserted by
// TestRelSizeFallbackDoesNotFireWhenAnalyzed.
//
// EstRelRows is 0 whenever the fallback did not apply, so with the flag off
// this reduces to tableRows and no caller can observe a difference. The
// consumers that make this move plan SHAPE — the bushy DP seed and
// estimateBaseRelInfo.baseRows — are stages 2 and 3 and read the flag
// separately. (Its one remaining shape consumer, the MultiHashJoin probe-side
// choice, went away with the node in M0127-P6.2.)
func seqScanRows(x *SeqScan) int64 {
	if x == nil {
		return 0
	}
	if rows := tableRows(x.Table); rows > 0 {
		return rows
	}
	return x.EstRelRows
}

// indexScanRows is the cardinality of an `*IndexScan` / `*IndexOnlyScan` leaf.
//
// The historical convention — "an index scan is an equality probe, so 1 row
// per call site" — is right for every such node goopg emitted before
// M0127-P5.4c-ii-b: each one carries an Index Cond binding the index columns
// and the executor probes it once per outer row. It became wrong the moment
// the PG-shaped join search started emitting the OTHER shape PG has always
// had, the index path with an EMPTY indexclauses list, which "implies a full
// index scan" (pathnodes.h:1817) — the ordering-only path
// `addOneOrderedIndexPath` builds so a merge join above it can skip a sort.
// That node reads the WHOLE relation, and the same full-range shape is
// reachable with the flag off through planner.go's sort-avoidance rewrite to
// an ordered `*IndexOnlyScan`.
//
// Reporting 1 for it is not a display defect. `EstimateRows` is what EXPLAIN
// prints (operators_explain.go:1312), but it is also what sizes a hash table
// (operators_join_agg.go:629), picks a join algorithm (planner.go:2360) and
// decides a Memoize (memoize.go:114) — so a full scan claiming one row
// collapses every estimate ABOVE it too, the same propagation shape M0125-0038
// fixed for the pass-through wrappers. Measured at M0127-P5.9 run 3: all six
// of `GOOPG_PGSHAPED_DP`'s §4 parity violations were joins sitting over such a
// leaf, estimated at 1 against actuals of 5 869–1 999 080, and the reproducer
// was Q12's `Index Scan using orders_pk on orders` with no Index Cond and
// `rows=1` over 1 500 000 actual rows (fix_plan M0127-P5.9-h).
//
// The search's OWN sizing was never wrong — `addOneOrderedIndexPath` sets
// `Path.Rows = rel.Rows` and `makeJoinRel` sizes off that — which is why the
// bisect P5.9-h asked for ("built at 1, or a correct size mis-consumed?")
// answers neither: the 1 is minted here, after the search, when the finished
// plan tree is re-estimated.
//
// The no-statistics case returns 1 rather than 0 deliberately: 0 means "no
// estimate available" to every caller and would zero every node above, which
// is a worse answer than the one this function is replacing.
//
// KEYED SCANS ARE NO LONGER A FLAT 1 (pg-plan-parity item 0). The historical
// convention above — "any key or bound ⇒ 1 row" — is right for a UNIQUE index
// whose every column is bound by equality, and wrong for every other keyed
// scan. On a non-unique index it was wrong by orders of magnitude:
//
//	SELECT * FROM supplier WHERE s_nationkey = 5
//	   goopg  rows=1        PG  rows=378      (10 000 rows / 25 nations)
//
// and because EstimateRows also sizes hash tables, picks join algorithms and
// decides Memoize, a probe priced at one row makes an index-driven nested loop
// look free while simultaneously making the index scan itself look too cheap to
// beat with a bitmap. It is the constant underneath both the "bitmap never
// wins" and "nested loop 1 vs 25" gaps.
//
// The estimate now follows PG: selectivity × reltuples, with the per-column
// equality selectivity from `var_eq_non_const` (selfuncs.c) — the same helper
// the parameterised-path sizing uses, so the two cannot drift — and
// DEFAULT_INEQ_SEL per range bound. A unique index fully bound by equality
// still returns 1, which is both correct and what keeps the common PK probe
// unchanged.
func indexScanRows(tbl *catalog.Table, idx *catalog.Index, key Expr, keys []Expr, lowKey, highKey Expr) int64 {
	relRows := tableRows(tbl)
	keyed := key != nil || len(keys) > 0
	bounded := lowKey != nil || highKey != nil

	if !keyed && !bounded {
		// Full index scan: reads the whole relation.
		if relRows > 0 {
			return relRows
		}
		return 1
	}
	// Without reltuples there is nothing to scale, so keep the historical
	// answer rather than inventing one.
	if relRows <= 0 {
		return 1
	}

	nEq := len(keys)
	if key != nil && nEq == 0 {
		nEq = 1
	}
	// A unique index with every column pinned by equality yields at most one
	// row — PG's `btcostestimate` special-case, and the shape the old constant
	// was really written for.
	if idx != nil && idx.Unique && nEq > 0 && nEq >= len(idx.Columns) {
		return 1
	}

	sel := 1.0
	for i := 0; i < nEq; i++ {
		var cs *catalog.ColumnStats
		if idx != nil && i < len(idx.Columns) {
			cs = columnStatsByName(tbl, idx.Columns[i])
		}
		sel *= varEqNonConstSelectivity(cs)
	}
	// PG charges DEFAULT_INEQ_SEL per unmatched inequality bound
	// (selfuncs.h); a two-sided range therefore lands near its
	// DEFAULT_RANGE_INEQ_SEL neighbourhood without needing histograms here.
	if lowKey != nil {
		sel *= defaultIneqSel
	}
	if highKey != nil {
		sel *= defaultIneqSel
	}
	sel = clampSelectivity(sel)

	rows := int64(sel * float64(relRows))
	if rows < 1 {
		return 1
	}
	if rows > relRows {
		return relRows
	}
	return rows
}

// defaultIneqSel is PG's DEFAULT_INEQ_SEL (selfuncs.h): the selectivity charged
// for a scalar inequality whose statistics do not settle it.
const defaultIneqSel = 0.3333333333333333

// tableRows returns the catalog's reltuples-equivalent for a
// table, or 0 when ANALYZE hasn't run yet.
func tableRows(tbl *catalog.Table) int64 {
	if tbl == nil || tbl.Stats == nil {
		return 0
	}
	return tbl.Stats.RowCount
}

// baseRelInfo carries the per-binding row-count estimate and
// supporting metadata that the bushy DP needs to make join-cost
// decisions aware of post-filter cardinality. (M0077-0002 /
// Slice B per design 02 §2.) One entry is built per FROM
// binding before the DP runs; singleton subsets seed
// `dpEntry.rows` from `filteredRows`, and Slice C's 3-part
// hash-join cost reads the same field for build / probe inputs.
type baseRelInfo struct {
	bindingIdx int
	// sourceIdx mirrors `rangeBinding.sourceIdx` so
	// `inferAnchoredEqualities` can translate a
	// `ColumnRef.SourceTableIdx` (the column's binding-of-origin
	// identifier) back to the matching `baseRelInfo` entry.
	sourceIdx        int16
	table            *catalog.Table
	baseRows         int64
	filteredRows     int64
	localFilter      Expr
	hasLocalFilter   bool
	isSmallDimension bool
}

// estimateBaseRelInfo computes a `baseRelInfo` for one FROM
// binding. The local filter (if any) is rebased into leaf-local
// coordinates via `localizeExprToLeaf` before the selectivity
// stack reads it, so column-ref lookups via `ColumnRef.Index`
// land on the leaf scan's own schema. Per design 02 §2, the
// scaled value is only adopted when the selectivity estimate is
// `reliable`; fallback selectivities (defaultEq /
// defaultIneq / defaultGeneric) leave `filteredRows = baseRows`
// rather than over-trusting an arbitrary 0.005 / 0.333 constant.
//
// (M0077-0002 / Slice B.)
func estimateBaseRelInfo(binding rangeBinding, scan Node, local Expr) baseRelInfo {
	info := baseRelInfo{
		bindingIdx:     -1,
		sourceIdx:      binding.sourceIdx,
		table:          binding.table,
		localFilter:    local,
		hasLocalFilter: local != nil,
	}
	// M0125-0043: the small-dimension answer lives on the leaf scan now.
	// `smallDimensionSide` keeps the catalog-hint reading for the bindings
	// the bushy DP hands us with no scan of their own.
	info.isSmallDimension = smallDimensionSide(scan, binding.table)
	if binding.table != nil {
		info.baseRows = tableRows(binding.table)
	}
	info.filteredRows = applyLocalFilterSelectivity(info.baseRows, binding, scan, local)
	return info
}

// applyLocalFilterSelectivity is the second half of upstream's
// `set_baserel_size_estimates` (costsize.c:5378) — `rel->rows = clamp_row_est(
// rel->tuples * clauselist_selectivity(root, rel->baserestrictinfo, …))` — over
// a pre-filter row count somebody else supplied. It is factored out of
// `estimateBaseRelInfo` because the relation-size fallback needs the SAME rule
// applied to a block-derived `tuples` (`applyRelSizeFallback`, relsize.go), and
// a second open-coded copy of the reliability gate is exactly the sibling shape
// hard-won rule #2 forbids (M0127-P5.6, the M0125-0003 stage-3 re-evaluation).
//
// The one deliberate deviation from upstream is the `reliable` gate: PG always
// multiplies, falling back to DEFAULT_EQ_SEL / DEFAULT_INEQ_SEL when it has no
// statistic, whereas goopg keeps the pre-filter count (design 02 §2 rule (4)).
// Ledgered — see the 2026-08-06 row.
func applyLocalFilterSelectivity(baseRows int64, binding rangeBinding, scan Node, local Expr) int64 {
	if local == nil || scan == nil || baseRows <= 0 {
		return baseRows
	}
	localized := localizeExprToLeaf(local, binding)
	sel := clauseSelectivityWithSource(localized, scan)
	if !sel.reliable {
		return baseRows
	}
	rows := scaleByFloat(baseRows, sel.value)
	if rows < 1 {
		// Preserve the bushy DP's "no zero-row singletons"
		// invariant — without this guard the planner would
		// collapse a heavily-filtered relation's contribution
		// to 0 even when at least one row is plausible.
		return 1
	}
	return rows
}

// IsSmallDimensionSide (M0054-0010) returns true when the plan
// node `n` reads from a relation the planner tagged as a small
// dimension (or is trivially derived from one — Filter, Project,
// Sort over such a scan). Used by the join build-side selector to
// pin tiny dim-tables on the build side regardless of stats
// availability.
//
// M0125-0043 moved the tag off `catalog.Table.SmallDimension` —
// where it was a lookup of the literal names "region" / "nation" —
// onto the scan node, stamped at plan-build time by
// `smallDimensionTag` from the relation's SIZE. The read side is
// unchanged in meaning; it now answers for every tiny dimension
// table rather than for two TPC-H ones.
func IsSmallDimensionSide(n Node) bool {
	if n == nil {
		return false
	}
	switch x := n.(type) {
	case *SeqScan:
		return x.SmallDim
	case *IndexScan:
		return x.SmallDim
	case *Filter:
		return IsSmallDimensionSide(x.Child)
	case *Project:
		return IsSmallDimensionSide(x.Child)
	case *Sort:
		return IsSmallDimensionSide(x.Child)
	}
	return false
}

// estimateJoin uses the upstream-aligned formula
// `|L| * |R| / max(NDistinct(L.k), NDistinct(R.k))` for
// specialised equality joins (hash / merge) with disjoint-side
// keys. Falls back to the generic equality selectivity when
// either NDistinct is unavailable. CROSS join is the cartesian
// product.
//
// M0126-0010: when NDistinct is unavailable for the join key
// and we fall back to the generic 0.5% selectivity, cap the
// output at max(|outer|, |inner|). Without this, FK-PK join
// chains compound the multiplicative selectivity at every level,
// producing absurd row-count estimates (e.g. Q9's 5-level chain
// estimated 5.9e15 rows against an actual of 175). The cap
// codifies the invariant that a non-cross equi-join never
// produces more rows than the larger of its two inputs when
// the join key is a FK referencing a PK — the worst case
// without fan-out is the probe-side row count. The cap is
// conservative: it may under-estimate for genuine many-to-many
// joins, but the existing NDistinct-driven formula handles
// those correctly when stats are available; the cap only fires
// in the stats-unavailable fallback path.
// M0127-P5.6-e-ii adds the two arms `calc_joinrel_size_estimate`
// (costsize.c) has and this function did not — see 09 §5.2 for the
// measurement that isolated them:
//
//   - SEMI / ANTI were sized as if they were INNER (`l·r/nd`), which
//     can and did exceed the outer input: Q18's final SEMI carried
//     1 756 987 324 against 70 actual rows. Upstream scales the OUTER
//     rows by the semi-join match fraction and never multiplies by the
//     inner's size at all (`nrows = outer_rows * fkselec * jselec` for
//     JOIN_SEMI, `outer_rows * (1 - fkselec*jselec)` for JOIN_ANTI).
//   - the non-equi part of the ON clause contributed NOTHING. Only
//     `LeftKey`/`RightKey` were read, so a join carrying Q19's
//     three-branch OR as its whole restriction was estimated at
//     4.3 × 10⁷ against 131 actual rows. Upstream feeds the entire
//     restrictlist to `clauselist_selectivity`.
//
// M0127-P5.6-e-iii closes the THIRD cause that surfaced while fixing those
// two, together with the two defects that made it unlandable on its own.
// `LeftKey` and `RightKey` live in the MERGED left‖right coordinate space
// (`splitEqualityForHash` classifies operands by `Index < leftWidth`, and
// the executor evaluates them against a `mergedKeySlot`), so the old
// `keyNDistinct(j.RightKey, j.Right)` handed a merged index to the right
// child's OWN schema: it read out of range — 0, the "nd unavailable" path
// — or, on a right input wider than the left, silently read another
// column's ndistinct. The right side of an equi-join never contributed to
// `nd`.
//
// Correcting that alone was measured and REJECTED
// (`2026-08-04-p56eii-postfix.txt`): every joinrel it touched became more
// accurate — Q9's two deepest joins landed exactly on their actuals — and
// the queries above them got far worse, Q9's final joinrel going from
// 124.7× over to 176 424× and Q8's from 1.9× under to 2 171× over. Two
// pre-existing defects were being cancelled by the missing nd, and both are
// addressed here:
//
//   - ANALYZE reported the SAMPLE's distinct count with no Haas-Stokes
//     scale-up, so a 1 500 000-row unique key read as ~30 000 and every
//     join above it divided by a number 50× too small. Fixed in
//     `executor.ndistinctEstimate` (mirroring `compute_scalar_stats`,
//     analyze.c) — the prerequisite, landed in the same commit.
//   - the M0126-0010 cap that bounds a join at max(|l|, |r|) fires only on
//     the nd-unavailable path, so supplying nd also removed the bound that
//     was holding those estimates down (Q8 d4: 5 997 241 capped →
//     624 279 803 uncapped). Re-examined here and deliberately LEFT on the
//     fallback path only: it is a non-PG heuristic standing in for
//     upstream's FK-driven `fkselec` (`get_foreign_key_join_selectivity`,
//     costsize.c), and a real many-to-many join legitimately exceeds
//     max(|l|, |r|). What made it look load-bearing was the saturated nd
//     it was compensating for, not a missing bound. Extending it to the
//     nd-driven path would silently truncate genuine fan-out; the audit is
//     what certifies that judgement (09 §5.3).
func estimateJoin(j *Join) int64 {
	l := EstimateRows(j.Left)
	r := EstimateRows(j.Right)
	if l <= 0 || r <= 0 {
		return 0
	}
	if j.Type == JoinTypeCross {
		return l * r
	}
	// SEMI / ANTI: the output is a subset of the OUTER input, so the
	// inner's size enters only through the match fraction. Mirrors
	// costsize.c's JOIN_SEMI / JOIN_ANTI arms; the residual is folded
	// into `jselec` because a row only counts as matched when the whole
	// join condition holds.
	if j.Type == JoinTypeSemi || j.Type == JoinTypeAnti {
		sel := semiJoinMatchFraction(j, r) * joinResidualSelectivity(j)
		if sel > 1 {
			sel = 1
		}
		if j.Type == JoinTypeAnti {
			sel = 1 - sel
		}
		return scaleByFloat(l, sel)
	}
	if j.Algo == JoinAlgoHash || j.Algo == JoinAlgoMerge {
		// M0127-P5.6-f: EVERY equi-pair is priced, and the pairs a proven
		// key covers are priced by the key instead. See the function header
		// and joinkeyproof.go for why the two are one change.
		pairs := joinEquiPairs(j)
		sk := superkeyJoinEstimate(j, pairs)
		sel := sk.sel
		measured := sk.fired
		for i, p := range pairs {
			if i < len(sk.covered) && sk.covered[i] {
				continue
			}
			// M0127-P5.6-e-iii: the right key is resolved in the MERGED
			// left‖right space it is written in, the same shift the
			// SEMI/ANTI path has used since P5.6-e-ii. Before that the
			// right side of an equi-join never entered `max(nd)` at all,
			// so a PK-FK join divided by the FK side's ndistinct only.
			if nd := pairNDistinct(j, p); nd > 0 {
				sel /= float64(nd)
				measured = true
			} else {
				// `clauselist_selectivity` charges an unmeasurable
				// equijoin the selfuncs.h constant and multiplies it in
				// with the rest; it does not abandon the measured pairs.
				sel *= defaultEqSelectivity
			}
		}
		if measured {
			// Computed in float64 and saturated rather than as the former
			// `(l*r)/nd`: with the divisors now applied AFTER the product,
			// a deep chain's `l*r` would wrap int64 negative before the
			// divide and `clampRowEst` would pin the garbage to 1. Same
			// reason `satRowsMulDiv` exists on the DP side (bushy.go).
			rows := float64(l) * float64(r) * sel * joinResidualSelectivity(j)
			// The key-implied bound (04 §3.3). `rowsBound` is +Inf unless a
			// proven key makes one side's rows an upper bound on the
			// output, so this is a no-op on every join that proved nothing.
			// It is STRUCTURAL — a counting argument, not a heuristic —
			// which is why, unlike the max(l,r) cap below, it is allowed to
			// touch a measured estimate.
			if rows > sk.rowsBound {
				rows = sk.rowsBound
			}
			return saturateRowEst(outerJoinRowFloor(j, rows, l, r))
		}
	}
	est := scaleByFloat(l*r, defaultEqSelectivity)
	if est < 1 {
		return 1
	}
	// M0126-0010: cap fallback estimate at max input size.
	mx := l
	if r > mx {
		mx = r
	}
	if est > mx {
		est = mx
	}
	// Upstream applies the outer-join clamp to whatever `jselec` produced, not
	// only to a measured one, so the unmeasurable fallback gets it too.
	return saturateRowEst(outerJoinRowFloor(j, float64(est), l, r))
}

// outerJoinRowFloor is `calc_joinrel_size_estimate`'s outer-join clamp
// (costsize.c): "the joinqual selectivity has to be clamped using the knowledge
// that the output must be at least as large as the non-nullable input".
//
//	case JOIN_LEFT:  if (nrows < outer_rows) nrows = outer_rows;
//	case JOIN_FULL:  if (nrows < outer_rows) nrows = outer_rows;
//	                 if (nrows < inner_rows) nrows = inner_rows;
//
// goopg keeps JOIN_RIGHT as its own type where upstream has already commuted it
// into a JOIN_LEFT, so RIGHT's non-nullable input is the INNER one — the same
// rule read from the other side.
//
// M0127-P5.6-g-ii. `estimateJoin` has never had an outer-join arm: LEFT / RIGHT
// / FULL fall through to the INNER product, which is upstream's first line for
// all of them, and the clamp was the missing second line. It stayed invisible
// because a LEFT join's key column typically resolved to nothing, so the
// estimate came out of the `defaultEqSelectivity` fallback below, which caps at
// `max(l, r)` and could not go under the outer's own count by accident. The
// grouping-node arm above made those keys resolvable, and Q77's
// `store LEFT JOIN (… GROUP BY s_store_sk)` immediately estimated 885 rows for
// a join that emits at least its outer's 8 885 — an impossible number, and a
// pre-existing defect this exposed rather than caused.
func outerJoinRowFloor(j *Join, rows float64, l, r int64) float64 {
	switch j.Type {
	case JoinTypeLeft:
		if rows < float64(l) {
			return float64(l)
		}
	case JoinTypeRight:
		if rows < float64(r) {
			return float64(r)
		}
	case JoinTypeFull:
		if rows < float64(l) {
			rows = float64(l)
		}
		if rows < float64(r) {
			rows = float64(r)
		}
	}
	return rows
}

// semiJoinMatchFraction is the fraction of OUTER rows expected to have
// at least one join partner — upstream's `eqjoinsel_semi`
// (selfuncs.c) over the join's whole equi-key list.
//
// M0127-P5.6-f made it fold over EVERY equi-pair rather than only
// (LeftKey, RightKey). `clauselist_selectivity` multiplies the per-clause
// `eqjoinsel_semi` results, and the residual now excludes every pair
// (`joinEquiPairs`), so a second equated column that used to be priced by
// `clauseSelectivity` inside the residual is priced by its own match fraction
// here instead. Pricing it in neither place is the defect this closes.
func semiJoinMatchFraction(j *Join, innerRows int64) float64 {
	sel := 1.0
	for _, p := range joinEquiPairs(j) {
		sel *= semiPairMatchFraction(j, p, innerRows)
	}
	return sel
}

// semiPairMatchFraction is `eqjoinsel_semi` (selfuncs.c) for ONE pair.
//
// The nd2 clamp is load-bearing and asymmetric, for the reason upstream states:
// it is the ONLY pathway by which a restriction on the inner relation reaches a
// SEMI/ANTI size estimate, since the inner's row count is otherwise unused.
// Clamping nd1 as well would double-count the outer's own restrictions. A
// clamped nd2 also stops being "default": an inner relation smaller than
// `defaultNumDistinct` bounds its own distinct count exactly.
//
// M0127-P5.6-g added the two pieces the first cut left out, and they are one
// change because they fail in the same direction (09 §5.7):
//
//   - **The MCV arm.** Upstream takes it FIRST when both sides have an MCV
//     list, matches the two lists against each other, and estimates only the
//     UNCERTAIN remainder with the nd heuristic. The heuristic alone is a
//     rounding function on real data: with a truthful ndistinct (P5.6-e-iii)
//     the `nd1 <= nd2` test succeeds on every PK-FK-shaped semi-join, the
//     fraction is exactly 1.0, and JOIN_ANTI's `outer · (1 - jselec)` then
//     floors at 1 row — Q21's final ANTI read 4 003× under, Q19 131× under,
//     Q16 85× under. A matched-MCV mass is a MEASURED lower bound on the match
//     fraction, so it is also the only thing that can pull the estimate off
//     those two rails.
//   - **`(1 - nullfrac1)`.** A NULL outer row matches nothing in a semi-join;
//     upstream multiplies every branch's result by the outer's null fraction
//     complement, including the punt. goopg dropped the factor entirely, which
//     is a pure over-estimate whenever the outer key is nullable.
//
// Divergences from upstream, both recorded in the ledger: MCV equality is
// TEXT equality over the stored renderings rather than a call through the
// operator's `oprcode` (goopg stores MCVs as strings — same convention as
// `eqSelectivityForColumn`), and only the inner INPUT's rows clamp nd2, not
// also `vardata2->rel->rows`.
func semiPairMatchFraction(j *Join, p JoinKeyPair, innerRows int64) float64 {
	nd1 := float64(keyNDistinct(p.Left, j.Left))
	nd2 := float64(rightExprNDistinct(j, p.Right))
	st1 := keyColumnStats(p.Left, j.Left)
	st2 := rightExprStats(j, p.Right)

	nullfrac1 := 0.0
	if st1 != nil {
		nullfrac1 = st1.NullFrac
	}

	nd1Known := nd1 > 0
	nd2Known := nd2 > 0
	if !nd2Known {
		nd2 = defaultNumDistinct
	}
	if innerRows > 0 && nd2 >= float64(innerRows) {
		nd2 = float64(innerRows)
		nd2Known = true
	}

	if st1 != nil && st2 != nil && len(st1.MCV) > 0 && len(st2.MCV) > 0 {
		// "The clamping above could have resulted in nd2 being less than
		// sslot2->nvalues; in which case, we assume that precisely the nd2 most
		// common values in the relation will appear in the join input"
		// (selfuncs.c) — the MCV list is frequency-ordered, so the prefix is
		// the right truncation.
		clamped2 := len(st2.MCV)
		if float64(clamped2) > nd2 {
			clamped2 = int(nd2)
		}
		matched2 := make([]bool, clamped2)
		matchFreq1, nmatches := 0.0, 0
		for i := range st1.MCV {
			// "we assume that each MCV will match at most one member of the
			// other MCV list" — hence the used-up flags and the break.
			for k := 0; k < clamped2; k++ {
				if matched2[k] || st1.MCV[i].Value != st2.MCV[k].Value {
					continue
				}
				matched2[k] = true
				nmatches++
				matchFreq1 += st1.MCV[i].Frequency
				break
			}
		}
		matchFreq1 = clampProbability(matchFreq1)

		// The matched MCVs are known to have partners, so they are discounted
		// from BOTH distinct counts before the heuristic prices the rest.
		uncertainFrac := 0.5
		if nd1Known && nd2Known {
			rem1, rem2 := nd1-float64(nmatches), nd2-float64(nmatches)
			if rem1 <= rem2 || rem2 < 0 {
				uncertainFrac = 1.0
			} else {
				uncertainFrac = rem2 / rem1
			}
		}
		uncertain := clampProbability(1.0 - matchFreq1 - nullfrac1)
		return matchFreq1 + uncertainFrac*uncertain
	}

	// Without MCV lists on both sides, only the nd heuristic is available.
	if !nd1Known || !nd2Known {
		return 0.5 * (1.0 - nullfrac1)
	}
	if nd1 <= nd2 {
		return 1.0 - nullfrac1
	}
	return (nd2 / nd1) * (1.0 - nullfrac1)
}

// clampProbability is `CLAMP_PROBABILITY` (selfuncs.h): a probability that
// escaped [0, 1] through accumulated float error or through a stale statistic
// is pinned rather than propagated, because a negative one flips the sign of
// everything multiplied by it.
func clampProbability(p float64) float64 {
	if math.IsNaN(p) || p < 0 {
		return 0
	}
	if p > 1 {
		return 1
	}
	return p
}

// keyColumnStats is `keyNDistinct`'s whole-row twin: the ANALYZE statistics of
// the base column a LEFT-side key operand resolves to, nil when it resolves to
// no base column or the relation was never analysed.
//
// It goes through `resolveBaseColumn` — the canonical resolver — rather than
// `columnStatsForChild`, for the reason recorded on `baseColumnRef.stats`.
func keyColumnStats(key Expr, side Node) *catalog.ColumnStats {
	cr, ok := key.(*ColumnRef)
	if !ok {
		return nil
	}
	return columnStatsForChildBase(cr.Index, side)
}

// rightExprStats is `rightExprNDistinct`'s whole-row twin, and repeats its
// coordinate shift for the same reason: a RIGHT-side operand's `Index` counts
// from the start of the merged left‖right schema. The two must agree on which
// column they are describing — reading nd from one column and the MCV list of
// another is the P5.6-e-ii/-e-iii defect class in a new place.
func rightExprStats(j *Join, key Expr) *catalog.ColumnStats {
	cr, ok := key.(*ColumnRef)
	if !ok || j.Left == nil {
		return nil
	}
	idx := cr.Index
	if lw := len(j.Left.Output()); lw > 0 && idx >= lw {
		idx -= lw
	}
	return columnStatsForChildBase(idx, j.Right)
}

func columnStatsForChildBase(idx int, child Node) *catalog.ColumnStats {
	if ref, ok := resolveBaseColumn(idx, child); ok {
		return ref.stats
	}
	return nil
}

// rightExprNDistinct resolves a RIGHT-side operand's ndistinct in the
// coordinate space it is actually written in: the operand's `Index` counts
// from the start of the MERGED left‖right schema, so it has to be shifted down
// by the left input's width before it means anything to the right child.
//
// It arrived as `rightKeyNDistinct` on the SEMI/ANTI path only (P5.6-e-ii),
// while the equi-join formula deliberately kept its historical wrong lookup:
// correcting it there removed the M0126-0010 cap from joins whose nd was
// saturated by ANALYZE, and those compound upward. P5.6-e-iii de-saturated
// ANALYZE and moved the equi arm onto this lookup too; P5.6-f generalised it
// from `j.RightKey` to any pair's right operand, which is all the single-key
// wrapper had left to do.
func rightExprNDistinct(j *Join, key Expr) int64 {
	cr, ok := key.(*ColumnRef)
	if !ok || j.Left == nil {
		return 0
	}
	idx := cr.Index
	if lw := len(j.Left.Output()); lw > 0 && idx >= lw {
		idx -= lw
	}
	return columnNDistinctForChild(idx, j.Right)
}

// pairNDistinct is `eqjoinsel`'s divisor for ONE equi-pair: the larger of the
// two sides' ndistinct, which is upstream's `MIN(1/nd1, 1/nd2)` written the
// other way up (selfuncs.c). A key on either side bounds the join's size and
// the estimate is the tighter of the two bounds.
func pairNDistinct(j *Join, p JoinKeyPair) int64 {
	nd := keyNDistinct(p.Left, j.Left)
	if rnd := rightExprNDistinct(j, p.Right); rnd > nd {
		nd = rnd
	}
	return nd
}

// joinEquiPairs is the join's FULL equi-key list in the coordinate space
// `Predicate` is written in — the list `clauselist_selectivity` would price and
// `Join.Residual` excludes.
//
// `HashKeys` is authoritative when present, but it is filled by a single late
// pass at the tail of `Plan()` (join_hash_keys.go), so it is EMPTY for every
// estimate taken during join-order search — which is most of them, and the ones
// that decide plan shape. Deriving the same list from `Predicate` in that
// window is what keeps the search's estimate and the finished plan's estimate
// answering the same question; `splitAllEqualitiesForHash` is literally the
// function `fillOneJoinHashKeys` uses, so the two cannot disagree about what
// counts as a key. The single (LeftKey, RightKey) pair remains the last resort
// for a join whose Predicate was consumed elsewhere.
func joinEquiPairs(j *Join) []JoinKeyPair {
	if j == nil {
		return nil
	}
	if len(j.HashKeys) > 0 {
		return j.HashKeys
	}
	if j.Left != nil && j.Predicate != nil {
		if pairs := splitAllEqualitiesForHash(j.Predicate, len(j.Left.Output())); len(pairs) > 0 {
			return pairs
		}
	}
	if j.LeftKey != nil && j.RightKey != nil {
		return []JoinKeyPair{{Left: j.LeftKey, Right: j.RightKey}}
	}
	return nil
}

// joinResidualSelectivity prices the conjuncts of the join's ON clause
// that the equi-key formula does NOT already account for — upstream's
// `clauselist_selectivity` over the joinrel's restrictlist, minus the
// clauses `eqjoinsel` answered.
//
// Two restrictions keep this from double-counting, which is the trap
// costsize.c warns about in `calc_joinrel_size_estimate`'s opening
// comment ("we are not double-counting them because they were not
// considered in estimating the sizes of the component rels"):
//
//   - the published `HashKeys` pairs are excluded, because `l·r/nd` IS
//     their selectivity. `Join.Residual()` performs exactly that
//     subtraction for the executor already; the estimator reuses it so
//     the two cannot drift.
//   - only conjuncts referencing BOTH sides count. A single-sided
//     conjunct is a baserestrictinfo in upstream's model and never
//     reaches the joinrel; in goopg it is pushed down as a `*Filter`
//     over the scan and is already priced into `EstimateRows(j.Left)`
//     / `EstimateRows(j.Right)` — even though the planner also leaves
//     a copy in `Predicate` for the executor to re-apply (Q3's
//     three-conjunct `Filter:` is exactly this shape).
//
// The residual is resolved against `j` itself, whose ColumnRef
// coordinates are the merged left‖right space `Predicate` is written
// in — see the `*Join` arm of `columnNDistinctForChild`.
func joinResidualSelectivity(j *Join) float64 {
	if j == nil || j.Predicate == nil || j.Left == nil {
		return 1.0
	}
	// M0127-P5.6-f: the exclusion list is `joinEquiPairs`, the SAME list the
	// estimators above price. It used to be `HashKeys` with a single-pair
	// fallback while `estimateJoin` priced exactly one pair, so a two-pair
	// join like Q9's `l_suppkey = ps_suppkey AND l_partkey = ps_partkey` had
	// its second pair excluded here AND unpriced there — the clause vanished
	// from the estimate entirely (09 §5.4).
	res := j.residualExcluding(joinEquiPairs(j))
	if res == nil {
		return 1.0
	}
	leftWidth := len(j.Left.Output())
	sel := 1.0
	for _, c := range splitAnd(res) {
		if exprSide(c, leftWidth) != sideMixed {
			continue
		}
		sel *= clauseSelectivity(c, j)
	}
	if sel > 1 {
		return 1
	}
	if sel < 0 {
		return 0
	}
	return sel
}

// groupVarKey identifies one grouping variable for the duplicate check.
//
// The relation half is the LEAF SCAN NODE, not the `*catalog.Table`, for the
// reason `baseColumnRef.scan` documents: a self-join puts one table pointer
// behind two scans, and `GROUP BY n1.name, n2.name` is two variables of two
// relations, not one repeated variable.
type groupVarKey struct {
	rel Node
	col string
	// idx distinguishes variables that resolved to no base relation; their
	// only identity is the coordinate they occupy in the child's schema.
	idx int
}

// groupVarInfo is upstream's GroupVarInfo (selfuncs.c:3310) minus `isdefault`,
// which only feeds the SELFLAG_USED_DEFAULT bit no goopg caller reads yet.
type groupVarInfo struct {
	// rel is the leaf scan the variable resolved to, nil when it resolved to
	// no base relation. A nil-rel variable skips the per-relation clamp
	// (there is no `rel->tuples` to clamp against) and multiplies straight
	// into the total, bounded only by the closing input-rows clamp.
	rel       Node
	ndistinct float64
	// rawRows is the relation's unfiltered tuple count — upstream's
	// `rel->tuples`, the clamp denominator.
	rawRows float64
}

// estimateAggregate returns the group count via `estimateNumGroups`.
//
// M0127-P5.6-f-vii. What stood here was `child / 2` for anything but a
// single-ColumnRef GROUP BY, and the single-key arm returned the column's
// NDistinct with NO clamp to the input row count. Both halves were wrong in
// the same direction on the TPC-DS corpus: `/2` makes a two-key GROUP BY over
// a wide join look like half the join, and an unclamped NDistinct lets a
// grouped scan of 6 surviving rows claim its column's whole-table 18 000
// distinct values.
func estimateAggregate(a *Aggregate) int64 {
	return estimateNumGroups(a.GroupExprs, a.Child, EstimateRows(a.Child))
}

// estimateNumGroups is `estimate_num_groups` (selfuncs.c:3449): the number of
// distinct combinations of `groupExprs` among `inputRows` rows arriving from
// `child`.
//
// The method is upstream's five numbered steps, and the two that carry the
// weight are 4 and the closing clamp:
//
//   - Reduce the expressions to their unique variables (step 2): `f(x)` is
//     treated as `x`, because a function cannot increase the distinct count
//     and rarely reduces it much.
//   - Per SOURCE RELATION, multiply the variables' distinct counts, then clamp
//     to the relation's tuple count — divided by 10 when more than one
//     variable, since the worst-case product assumes independence the columns
//     usually do not have, but never below the largest single ndistinct
//     (step 4).
//   - Multiply across relations (step 5), then clamp the whole thing to
//     `inputRows`. That last clamp is what the old `/2` was a crude stand-in
//     for, and it is strictly better: a group count can never exceed the rows
//     being grouped, but it also has no reason to be exactly half of them.
//
// Three upstream refinements are deliberately absent and ledgered rather than
// faked: the equivalence-class de-duplication of step 3 (goopg's planner has
// no EC structure at estimate time), extended-statistics ndistinct
// (`estimate_multivariate_ndistinct` — goopg collects no multivariate stats),
// and the boolean short-circuit ("a boolean expression contributes 2 groups"),
// which needs an `exprType` this package does not have. A boolean COLUMN still
// answers 2 through its own ANALYZE ndistinct; only boolean-valued
// EXPRESSIONS fall through to the default.
func estimateNumGroups(groupExprs []Expr, child Node, inputRows int64) int64 {
	rows := float64(inputRows)
	if rows < 1 {
		// clamp_row_est: never estimate zero groups, it divides by zero
		// upstream in every consumer.
		rows = 1
	}
	if len(groupExprs) == 0 {
		return 1
	}

	var varinfos []groupVarInfo
	seen := make(map[groupVarKey]bool)
	for i, ge := range groupExprs {
		vars, enumerated := groupVarsOfExpr(ge)
		if len(vars) == 0 && !enumerated {
			// An expression this package cannot decompose is still a
			// grouping variable; it just has no statistics. Keyed by its
			// POSITION so two opaque expressions count as two variables.
			varinfos = append(varinfos, groupVarInfo{ndistinct: defaultNumDistinct})
			seen[groupVarKey{idx: -1 - i}] = true
			continue
		}
		for _, cr := range vars {
			key, info := examineGroupVar(cr, child)
			if seen[key] {
				// "Drop exact duplicates" (add_unique_group_var):
				// GROUP BY a, a + b is GROUP BY a, b.
				continue
			}
			seen[key] = true
			varinfos = append(varinfos, info)
		}
	}
	// An all-constant GROUP BY list is one group. (Upstream also returns
	// `input_rows` here when the expression contains a volatile function;
	// goopg has no volatility catalog in the planner, so that arm is the
	// ledgered gap `estimate-num-groups volatile-groupexpr`.)
	if len(varinfos) == 0 {
		return 1
	}

	numdistinct := 1.0
	relOrder := make([]Node, 0, len(varinfos))
	byRel := make(map[Node][]groupVarInfo, len(varinfos))
	for _, vi := range varinfos {
		if vi.rel == nil {
			numdistinct *= vi.ndistinct
			continue
		}
		if _, ok := byRel[vi.rel]; !ok {
			relOrder = append(relOrder, vi.rel)
		}
		byRel[vi.rel] = append(byRel[vi.rel], vi)
	}

	for _, rel := range relOrder {
		vis := byRel[rel]
		reldistinct := 1.0
		relmax := 1.0
		for _, vi := range vis {
			reldistinct *= vi.ndistinct
			if relmax < vi.ndistinct {
				relmax = vi.ndistinct
			}
		}
		tuples := vis[0].rawRows
		if tuples <= 0 {
			// "Sanity check --- don't divide by zero if empty relation":
			// upstream skips the relation's whole contribution.
			continue
		}
		clamp := tuples
		if len(vis) > 1 {
			clamp *= 0.1
			if clamp < relmax {
				clamp = relmax
				if clamp > tuples {
					clamp = tuples
				}
			}
		}
		if reldistinct > clamp {
			reldistinct = clamp
		}
		if filtered, ok := relFilteredRows(child, rel); ok && reldistinct > 0 && filtered < tuples {
			// Yao/Dell'Era: selecting p of N rows from n uniformly
			// distributed distinct values is expected to yield
			// n·(1 - ((N-p)/N)^(N/n)) of them. This is the only term that
			// knows the relation was FILTERED, and it is why grouping a
			// heavily restricted relation inside a fan-out join does not
			// claim the whole table's distinct count.
			reldistinct *= 1 - math.Pow((tuples-filtered)/tuples, tuples/reldistinct)
		}
		numdistinct *= clampRowEstF(reldistinct)
	}

	numdistinct = math.Ceil(numdistinct)
	if numdistinct > rows {
		numdistinct = rows
	}
	return saturateRowEst(numdistinct)
}

// groupVarsOfExpr is `pull_var_clause` for one GROUP BY item: the ColumnRefs
// the expression reads, in order, plus whether the walk was EXHAUSTIVE.
//
// The second return is what separates upstream's two variable-free cases. A
// fully enumerated expression that yielded no ColumnRef genuinely has no
// variables — a literal, or `date '2000-01-01'` — and upstream ignores it
// ("either it is a constant (and we can ignore it) or it contains a volatile
// function"; the volatile arm, which returns `input_rows`, is the ledgered gap
// `estimate-num-groups volatile-groupexpr`). An expression `exprChildSlots`
// does not enumerate yielded nothing only because the walk gave up, and
// ignoring THAT would silently drop a grouping key; the caller gives it
// DEFAULT_NUM_DISTINCT instead.
//
// `scopeIgnore` matches upstream's treatment of a sub-select inside a grouping
// expression: by the time `estimate_num_groups` runs it is a SubPlan, which
// `pull_var_clause` does not descend into.
func groupVarsOfExpr(e Expr) ([]*ColumnRef, bool) {
	var out []*ColumnRef
	enumerated := walkExprRefs(e, scopeIgnore, exprVisitor{
		Visit: func(n Expr) bool {
			if cr, ok := n.(*ColumnRef); ok {
				out = append(out, cr)
			}
			return true
		},
	})
	return out, enumerated
}

// examineGroupVar is `examine_variable` + `get_variable_numdistinct` for one
// grouping variable.
func examineGroupVar(cr *ColumnRef, child Node) (groupVarKey, groupVarInfo) {
	if cr.Index >= 0 {
		if ref, ok := resolveBaseColumn(cr.Index, child); ok {
			return groupVarKey{rel: ref.scan, col: ref.col},
				groupVarInfo{
					rel:       ref.scan,
					ndistinct: groupVarNDistinct(float64(ref.ndistinct), ref.rawRows),
					rawRows:   ref.rawRows,
				}
		}
		// `get_variable_numdistinct`'s isunique branch: a column that is the
		// sole grouping key of an intervening grouped node is unique in that
		// node's output, so its distinct count is that node's row count. No
		// base relation means no per-relation clamp — see groupVarInfo.rel.
		if nd, ok := groupUniqueNDistinct(cr.Index, child); ok && nd > 0 {
			return groupVarKey{idx: cr.Index}, groupVarInfo{ndistinct: float64(nd)}
		}
	}
	return groupVarKey{idx: cr.Index}, groupVarInfo{ndistinct: defaultNumDistinct}
}

// groupVarNDistinct is the tail of `get_variable_numdistinct` (selfuncs.c:6341)
// for goopg's stats shape, where `ColumnStats.NDistinct` is already the
// ABSOLUTE count (the negative-fraction form lives in `NDistinctFrac` and is
// scaled out before it reaches `baseColumnRef`).
func groupVarNDistinct(nd, rawRows float64) float64 {
	if nd > 0 {
		return nd
	}
	if rawRows <= 0 {
		return defaultNumDistinct
	}
	// "With no data, estimate ndistinct = ntuples if the table is small,
	// else use default. We use DEFAULT_NUM_DISTINCT as the cutoff for
	// 'small' so that the behavior isn't discontinuous."
	if rawRows < defaultNumDistinct {
		return rawRows
	}
	return defaultNumDistinct
}

// relFilteredRows answers upstream's `rel->rows` — the estimated row count of
// ONE source relation after its own restriction clauses — for the leaf scan
// `rel` somewhere under `root`.
//
// goopg has no RelOptInfo to read it off, so it is recovered from the plan
// tree: the topmost node whose subtree contains `rel` AND NOTHING ELSE still
// describes that one relation, and its row estimate is the filtered count.
// The moment the walk crosses a join the count stops being single-relation,
// so a join arm SEALS the answer found below it and passes it up unchanged.
//
// Returning false (an unrecognised node on the path, or no path at all) means
// "unknown", and the caller then treats the relation as unfiltered — the same
// conservative direction the pre-M0127-P5.6-f-vii code took by never
// considering restriction at all.
func relFilteredRows(root, rel Node) (float64, bool) {
	rows, found, _ := relFilteredRowsWalk(root, rel)
	return rows, found
}

func relFilteredRowsWalk(n, rel Node) (rows float64, found, sealed bool) {
	if n == nil {
		return 0, false, false
	}
	if n == rel {
		return float64(EstimateRows(n)), true, false
	}
	// passthrough: n still describes a single relation if its child does.
	passthrough := func(child Node) (float64, bool, bool) {
		r, f, s := relFilteredRowsWalk(child, rel)
		if !f {
			return 0, false, false
		}
		if s {
			return r, true, true
		}
		return float64(EstimateRows(n)), true, false
	}
	// join: whichever side holds `rel`, its answer is final.
	joinSide := func(children ...Node) (float64, bool, bool) {
		for _, c := range children {
			if r, f, _ := relFilteredRowsWalk(c, rel); f {
				return r, true, true
			}
		}
		return 0, false, false
	}

	switch x := n.(type) {
	case *Filter:
		return passthrough(x.Child)
	case *Project:
		return passthrough(x.Child)
	case *Sort:
		return passthrough(x.Child)
	case *Limit:
		return passthrough(x.Child)
	case *LockRows:
		return passthrough(x.Child)
	case *Gather:
		return passthrough(x.Child)
	case *GatherMerge:
		return passthrough(x.Child)
	case *CTEScan:
		return passthrough(x.Child)
	case *Join:
		return joinSide(x.Left, x.Right)
	case *NestedLoopIndexJoin:
		if x.Inner != nil {
			return joinSide(x.Outer, x.Inner)
		}
		return joinSide(x.Outer)
	}
	return 0, false, false
}

func keyNDistinct(key Expr, side Node) int64 {
	cr, ok := key.(*ColumnRef)
	if !ok {
		return 0
	}
	return columnNDistinctForChild(cr.Index, side)
}

// columnNDistinctForChild answers the ANALYZE distinct count for the column at
// logical index `idx` of a plan subtree.
//
// M0127-P5.6-f: the whole walk moved to `resolveBaseColumn` (joinkeyproof.go),
// which resolves the same coordinate to the same base relation and publishes
// the distinct count alongside the uniqueness evidence the superkey prover
// needs. Two lookups over one arm list cannot drift; two arm lists over one
// question demonstrably do (M0125-0038's `*Project` remap and P5.6-e-iii's
// `*Join` arm each went into ONE of the resolvers first, and both divergences
// were live defects). The arm-by-arm history is preserved on the arms
// themselves, where it now lives.
// M0127-P5.6-g-ii added the second answer `get_variable_numdistinct` knows:
// a column that resolves to no base relation because a grouping node stands in
// the way is still UNIQUE in that node's output when it is the sole grouping
// key, and its distinct count is then the node's own row count. The order is
// upstream's — `isunique` overrides `stadistinct` ("assume it is unique no
// matter what pg_statistic says", selfuncs.c:6332) — but the two arms cannot
// both fire here, because `resolveBaseColumn` has no arm that walks through a
// grouping node in the first place, deliberately (see `groupUniqueNDistinct`).
func columnNDistinctForChild(idx int, child Node) int64 {
	if ref, ok := resolveBaseColumn(idx, child); ok {
		return ref.ndistinct
	}
	if nd, ok := groupUniqueNDistinct(idx, child); ok {
		return nd
	}
	return 0
}

// clampRowEstF is `clamp_row_est` (costsize.c:213) in the float domain the
// group estimator works in: at least one row, otherwise rounded to an integer.
// (Upstream's `rint` breaks ties to even and Go's `math.Round` away from zero;
// that differs only on an exact .5 and never by more than one row.)
func clampRowEstF(rows float64) float64 {
	if math.IsNaN(rows) || rows <= 1 {
		return 1
	}
	return math.Round(rows)
}

// saturateRowEst converts a float row estimate to the int64 the planner
// carries, clamping to [1, MaxInt64]. `clamp_row_est` (costsize.c) does the
// same at the bottom; the top clamp is goopg's, because its estimates are
// int64 rather than double and a `float64 → int64` conversion out of range is
// implementation-defined in Go (in practice MinInt64 — a negative row count
// that then reads as "1" and poisons every cost above it).
func saturateRowEst(rows float64) int64 {
	if math.IsNaN(rows) || rows < 1 {
		return 1
	}
	if rows >= math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(rows)
}

func scaleByFloat(n int64, sel float64) int64 {
	if n <= 0 {
		return 0
	}
	out := int64(float64(n) * sel)
	if out < 1 {
		return 1
	}
	return out
}

func constInt(e Expr) (int64, bool) {
	if c, ok := e.(*IntegerConst); ok {
		return c.Value, true
	}
	return 0, false
}
