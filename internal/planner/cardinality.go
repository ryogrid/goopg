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
func estimateJoin(j *Join) int64 {
	l := EstimateRows(j.Left)
	r := EstimateRows(j.Right)
	if l <= 0 || r <= 0 {
		return 0
	}
	if j.Type == JoinTypeCross {
		return l * r
	}
	if j.Algo == JoinAlgoHash || j.Algo == JoinAlgoMerge {
		nd := keyNDistinct(j.LeftKey, j.Left)
		if rnd := keyNDistinct(j.RightKey, j.Right); rnd > nd {
			nd = rnd
		}
		if nd > 0 {
			est := (l * r) / nd
			if est < 1 {
				return 1
			}
			return est
		}
	}
	est := scaleByFloat(l*r, defaultEqSelectivity)
	if est < 1 {
		return 1
	}
	return est
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

func columnNDistinctForChild(idx int, child Node) int64 {
	switch x := child.(type) {
	case *SeqScan:
		if x.Table != nil && x.Table.Stats != nil {
			if idx >= 0 && idx < len(x.Table.Stats.Columns) {
				return x.Table.Stats.Columns[idx].NDistinct
			}
		}
	case *IndexScan:
		// Heap-fetching probe: output schema is the table's column
		// order, same as *SeqScan.
		if x.Table != nil && x.Table.Stats != nil {
			if idx >= 0 && idx < len(x.Table.Stats.Columns) {
				return x.Table.Stats.Columns[idx].NDistinct
			}
		}
	case *Filter:
		return columnNDistinctForChild(idx, x.Child)
	case *Sort:
		return columnNDistinctForChild(idx, x.Child)

	// M0125-0038 (C5): a join input is routinely Project-wrapped, and
	// this lookup returning 0 through the wrapper is what made every
	// such equi-join fall back to defaultEqSelectivity — the
	// "equi-join key contributes no selectivity" symptom (Q10's
	// rows=131280740 is exactly l·r·0.005).
	case *Project:
		if idx >= 0 && idx < len(x.Targets) {
			if cr, ok := x.Targets[idx].(*ColumnRef); ok {
				return columnNDistinctForChild(cr.Index, x.Child)
			}
		}
	case *Limit:
		return columnNDistinctForChild(idx, x.Child)
	case *LockRows:
		return columnNDistinctForChild(idx, x.Child)
	case *Gather:
		return columnNDistinctForChild(idx, x.Child)
	case *GatherMerge:
		return columnNDistinctForChild(idx, x.Child)
	case *CTEScan:
		// The scan's schema is the body's output schema, position for
		// position.
		return columnNDistinctForChild(idx, x.Child)
	}
	return 0
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
