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
package planner

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
		// Equality probe → 1 row per call site.
		return 1
	case *IndexOnlyScan:
		// Same equality-probe convention as *IndexScan.
		return 1
	case *Values:
		return int64(len(x.Rows))
	case *Filter:
		child := EstimateRows(x.Child)
		if child <= 0 {
			return 0
		}
		return scaleByFloat(child, clauseSelectivity(x.Predicate, x.Child))
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

	case *MultiHashJoin:
		return estimateMultiHashJoin(x)
	}
	return 0
}

// estimateSetOp mirrors upstream's output-row rules
// (prepunion.c:1146-1151): EXCEPT keeps the left input's count,
// INTERSECT the smaller input's, UNION ALL the sum. For the
// non-ALL forms upstream runs estimate_num_groups on the input;
// goopg has no group estimator yet (the 0077 line), so the
// dedup is approximated as /2 — the same convention
// estimateAggregate already uses for multi-column GROUP BY.
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

// estimateMultiHashJoin mirrors the *Join arm's method: start from the
// probe table's row count and walk the key chain, applying the same
// binary-join selectivity formula (l·r / max(nd_l, nd_r)) at each step.
//
// Before this arm existed every packed MHJ estimated 0 rows, and every
// ancestor's BuildLeft/algorithm decision above a packed chain was taken
// on that zero (bushy.go:1375 requires BOTH sides > 0). M0126-0002.
func estimateMultiHashJoin(mh *MultiHashJoin) int64 {
	if len(mh.Tables) == 0 {
		return 0
	}
	rows := EstimateRows(mh.Tables[mh.ProbeTable])
	if rows <= 0 {
		return 0
	}
	// Walk the key chain: each key joins a new table into the
	// accumulated row count. Keys form a chain (verified by
	// collectMultiHashTables's degree check); track which tables
	// have been visited so the direction doesn't matter.
	visited := make([]bool, len(mh.Tables))
	visited[mh.ProbeTable] = true
	for _, k := range mh.Keys {
		var newTable int
		if !visited[k.LeftTable] {
			newTable = k.LeftTable
		} else if !visited[k.RightTable] {
			newTable = k.RightTable
		} else {
			continue
		}
		r := EstimateRows(mh.Tables[newTable])
		if r <= 0 {
			visited[newTable] = true
			continue
		}
		nd := columnNDistinctForChild(k.LeftCol, mh.Tables[k.LeftTable])
		if rnd := columnNDistinctForChild(k.RightCol, mh.Tables[k.RightTable]); rnd > nd {
			nd = rnd
		}
		if nd > 0 {
			rows = (rows * r) / nd
		} else {
			rows = scaleByFloat(rows*r, defaultEqSelectivity)
		}
		if rows < 1 {
			rows = 1
		}
		visited[newTable] = true
	}
	return rows
}

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
// separately; the only shape this one reaches is the MultiHashJoin probe-side
// choice (bushy.go's EstimateRows comparison).
func seqScanRows(x *SeqScan) int64 {
	if x == nil {
		return 0
	}
	if rows := tableRows(x.Table); rows > 0 {
		return rows
	}
	return x.EstRelRows
}

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
	info.filteredRows = info.baseRows
	if local == nil || scan == nil || info.baseRows <= 0 {
		return info
	}
	localized := localizeExprToLeaf(local, binding)
	sel := clauseSelectivityWithSource(localized, scan)
	if !sel.reliable {
		return info
	}
	info.filteredRows = scaleByFloat(info.baseRows, sel.value)
	if info.filteredRows < 1 {
		// Preserve the bushy DP's "no zero-row singletons"
		// invariant — without this guard the planner would
		// collapse a heavily-filtered relation's contribution
		// to 0 even when at least one row is plausible.
		info.filteredRows = 1
	}
	return info
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
			return saturateRowEst(rows)
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
	return est
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

// semiPairMatchFraction is `eqjoinsel_semi` (selfuncs.c) for ONE pair, reduced
// to the branch goopg can actually evaluate.
//
// goopg keeps no MCV list for a join key here (`columnStatsForChild` answers
// per column, not per join), so this implements the `else` branch verbatim:
// with reliable ndistinct on both sides, `nd1 <= nd2` means every outer row is
// assumed to match and otherwise the match fraction is `nd2/nd1`; with either
// side unknown, upstream punts to 0.5 rather than guessing, and so does this.
//
// The nd2 clamp is load-bearing and asymmetric, for the reason upstream states:
// it is the ONLY pathway by which a restriction on the inner relation reaches a
// SEMI/ANTI size estimate, since the inner's row count is otherwise unused.
// Clamping nd1 as well would double-count the outer's own restrictions. A
// clamped nd2 also stops being "default": an inner relation smaller than
// `defaultNumDistinct` bounds its own distinct count exactly.
func semiPairMatchFraction(j *Join, p JoinKeyPair, innerRows int64) float64 {
	nd1 := float64(keyNDistinct(p.Left, j.Left))
	nd2 := float64(rightExprNDistinct(j, p.Right))
	nd2Known := nd2 > 0
	if !nd2Known {
		nd2 = defaultNumDistinct
	}
	if innerRows > 0 && nd2 >= float64(innerRows) {
		nd2 = float64(innerRows)
		nd2Known = true
	}
	if nd1 <= 0 || !nd2Known {
		return 0.5
	}
	if nd1 <= nd2 {
		return 1.0
	}
	return nd2 / nd1
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

// estimateAggregate returns the group count. With a single
// ColumnRef GROUP BY against a stats-bearing table, that's the
// column's NDistinct. Otherwise child / 2 — conservative.
func estimateAggregate(a *Aggregate) int64 {
	child := EstimateRows(a.Child)
	if len(a.GroupExprs) == 0 {
		return 1
	}
	if len(a.GroupExprs) == 1 {
		if cr, ok := a.GroupExprs[0].(*ColumnRef); ok {
			if nd := columnNDistinctForChild(cr.Index, a.Child); nd > 0 {
				return nd
			}
		}
	}
	if child <= 0 {
		return 0
	}
	if child < 2 {
		return child
	}
	return child / 2
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
func columnNDistinctForChild(idx int, child Node) int64 {
	if ref, ok := resolveBaseColumn(idx, child); ok {
		return ref.ndistinct
	}
	return 0
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
