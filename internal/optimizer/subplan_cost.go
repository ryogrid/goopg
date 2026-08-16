package optimizer

import "github.com/goopg/goopg/internal/catalog"

// D6.3a (design bundle correlated-subquery-planning, ch.06 §4): a rough
// per-invocation cost for running a sublink's inner plan as a SubPlan —
// the `cost_subplan` analog. The ONLY job of this number is ordering
// safety: letting the planner rank "re-run the SubPlan per outer row"
// against a set-oriented alternative (hash build, nested-loop semi scan)
// without ever being wrong by enough to invert a 10× decision. Precision
// beyond that is explicitly a non-goal; no attempt is made to model CPU
// per-row constants, caching effects beyond shape class, or memory.
//
// Shape classes mirror the executor's re-Open classification
// (internal/executor/subplan.go classifySubPlan) CONSERVATIVELY, without
// importing the executor:
//
//   - IndexScan probe chains (IndexScan under any stack of
//     Filter/Project/Aggregate/Sort/Limit/Distinct wrappers) cost one
//     match-set: tableRows / NDistinct(first key column). This is the
//     rescan-friendly class — the executor re-Opens these per call
//     without a rebuild.
//   - SeqScan-rooted chains cost the full table row count per call.
//   - Multi-child nodes (Join, NLI) cost the sum of their
//     children — the executor rebuilds their runtime state per rescan.
//   - Anything unrecognised, or any scan without usable ANALYZE stats,
//     makes the whole estimate UNKNOWN (returned as 0). Callers must
//     treat 0 as "no information", never as "free".
func estimateSubplanCostPerCall(n Node) int64 {
	if n == nil {
		return 0
	}
	switch x := n.(type) {
	case *SeqScan:
		return tableRows(x.Table)
	case *IndexScan:
		return indexProbeMatchSet(x)
	case *IndexOnlyScan:
		if x.Index != nil && x.Table != nil {
			return matchSetByColumnName(x.Table, firstIndexColumn(x.Index))
		}
		return 0
	case *Filter:
		return estimateSubplanCostPerCall(x.Child)
	case *Project:
		return estimateSubplanCostPerCall(x.Child)
	case *Aggregate:
		return estimateSubplanCostPerCall(x.Child)
	case *Sort:
		return estimateSubplanCostPerCall(x.Child)
	case *Limit:
		return estimateSubplanCostPerCall(x.Child)
	case *Distinct:
		return estimateSubplanCostPerCall(x.Child)
	case *OrdinalityWrap:
		return estimateSubplanCostPerCall(x.Child)
	case *Join:
		l := estimateSubplanCostPerCall(x.Left)
		r := estimateSubplanCostPerCall(x.Right)
		if l <= 0 || r <= 0 {
			return 0
		}
		return l + r
	case *NestedLoopIndexJoin:
		l := estimateSubplanCostPerCall(x.Outer)
		var r int64
		if x.Inner != nil {
			r = indexProbeMatchSet(x.Inner)
		}
		if l <= 0 || r <= 0 {
			return 0
		}
		return l + l*r
	}
	return 0
}

// indexProbeMatchSet estimates the rows one equality probe of the index
// returns: tableRows / NDistinct(first key column). Unknown stats → 0.
func indexProbeMatchSet(is *IndexScan) int64 {
	if is == nil || is.Table == nil || is.Index == nil {
		return 0
	}
	return matchSetByColumnName(is.Table, firstIndexColumn(is.Index))
}

func firstIndexColumn(idx *catalog.Index) string {
	if idx == nil || len(idx.Columns) == 0 {
		return ""
	}
	return idx.Columns[0]
}

// matchSetByColumnName computes max(1, rows/ndistinct) for the named
// column of tbl, or 0 when stats are missing (RowCount or the column's
// NDistinct unavailable). Column order in TableStats.Columns follows the
// table's column order, the same convention columnNDistinctForChild
// relies on for SeqScan output schemas.
func matchSetByColumnName(tbl *catalog.Table, col string) int64 {
	if tbl == nil || tbl.Stats == nil || col == "" {
		return 0
	}
	rows := tbl.Stats.RowCount
	if rows <= 0 {
		return 0
	}
	for i, c := range tbl.Columns {
		if c.Name != col {
			continue
		}
		if i >= len(tbl.Stats.Columns) {
			return 0
		}
		nd := tbl.Stats.Columns[i].NDistinct
		if nd <= 0 {
			return 0
		}
		ms := rows / nd
		if ms < 1 {
			ms = 1
		}
		return ms
	}
	return 0
}
