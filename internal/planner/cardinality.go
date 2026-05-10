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

import "github.com/goopg/goopg/internal/catalog"

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
		return tableRows(x.Table)
	case *IndexScan:
		// Equality probe → 1 row per call site.
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
	case *WindowAgg:
		return EstimateRows(x.Child)
	case *Join:
		return estimateJoin(x)
	case *Aggregate:
		return estimateAggregate(x)
	case *Insert:
		return EstimateRows(x.Source)
	case *Update, *Delete, *DDL, *Transaction, *Checkpoint, *Utility, *Copy:
		return 0
	case *Explain:
		return EstimateRows(x.Child)
	}
	return 0
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
	if binding.table != nil {
		info.isSmallDimension = binding.table.SmallDimension
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
// node `n` reads from a `SmallDimension`-flagged catalog table
// (or is trivially derived from one — Filter, Project, Sort
// over such a scan). Used by the join build-side selector to
// pin tiny dim-tables (region, nation) on the build side
// regardless of stats availability.
func IsSmallDimensionSide(n Node) bool {
	if n == nil {
		return false
	}
	switch x := n.(type) {
	case *SeqScan:
		return x.Table != nil && x.Table.SmallDimension
	case *IndexScan:
		return x.Table != nil && x.Table.SmallDimension
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
	case *Filter:
		return columnNDistinctForChild(idx, x.Child)
	case *Sort:
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
