package optimizer

// M0075-0002: per-outer selectivity guard for chained-NLI
// rebind. Re-attempts the M0072-0002 design with the
// missing safeguard: before accepting a rebind that moves
// the inner IndexScan probe onto a different outer column,
// estimate the per-outer match-set size and reject the
// rebind when it would explode.
//
// Background: M0072-0002 landed the planner-side rebind
// for *NestedLoopIndexJoin outers using
// findColumnIndexByNameAndSource (M0071-0009) but was
// reverted because the resolved column was high-cardinality
// at runtime → match-set explosion → Q9 cancelled at 600 s
// with 0 rows. The guard here prevents that regression mode.
//
// Threshold rationale: nliRebindSelectivityThreshold = 100.
// The M0072-0002 failure produced match-sets in the
// thousands. 100 is conservative — well below the failure
// threshold but permissive enough to allow legitimate
// rebinds where per-outer selectivity is reasonable.
//
// See docs/design/0075-0002-q9-chained-nli-selectivity-guard.md.

const nliRebindSelectivityThreshold = 100

// rebindPassesSelectivityGuard returns true when rebinding
// a chained-NLI inner IndexScan key to outerSchema[newIdx]
// is safe. "Safe" means the per-outer match-set estimate
// stays within nliRebindSelectivityThreshold rows.
//
// The estimate uses the column's NDistinct from
// columnNDistinctForChild (cardinality.go) to derive
// per-outer probe selectivity:
//
//   matchSet ≈ tableRows / NDistinct(column)
//
// When NDistinct is unavailable (no ANALYZE run, or
// derived column with no stats), the guard returns false
// — conservative rejection means the rebind is skipped
// and the original ColumnRef.Index is preserved (which
// is the M0072-0001 / M0074-0002 behaviour the rest of
// the chained-NLI code relies on).
func rebindPassesSelectivityGuard(outerNode Node, outerSchema Schema, newIdx int) bool {
	if outerNode == nil || newIdx < 0 || newIdx >= len(outerSchema) {
		return false
	}
	nd := columnNDistinctForChild(newIdx, outerNode)
	if nd <= 0 {
		// No stats available — cannot judge selectivity.
		// Conservative: reject the rebind. Q9 stays at
		// mode-1 baseline (7 rows) rather than risking
		// the M0072-0002 hang.
		return false
	}
	// Estimate per-outer match-set size: rows-per-distinct
	// = total / NDistinct. We don't have a direct rowcount
	// for the rebind target's source table at this layer,
	// so estimate from the outer schema's underlying scan.
	tableRows := outerScanRowCount(outerNode)
	if tableRows <= 0 {
		return false
	}
	matchSet := tableRows / nd
	return matchSet <= nliRebindSelectivityThreshold
}

// outerScanRowCount returns a rowcount estimate for the
// chained-NLI outer's underlying source. For
// NestedLoopIndexJoin outers, this is the LEFT scan's
// rowcount (the driver of the chained loop). Returns 0
// when no estimate is available.
//
// Defensive: walks past wrapper nodes (Project, Filter)
// to find the underlying scan; falls back to EstimateRows
// for unrecognised shapes.
func outerScanRowCount(n Node) int64 {
	if n == nil {
		return 0
	}
	switch x := n.(type) {
	case *NestedLoopIndexJoin:
		// Chained-NLI: the outer is itself an NLI; recurse
		// on its outer to find the driving table.
		if x.Outer != nil {
			return outerScanRowCount(x.Outer)
		}
		return 0
	case *SeqScan:
		return tableRows(x.Table)
	case *IndexScan:
		return tableRows(x.Table)
	case *Filter:
		return outerScanRowCount(x.Child)
	case *Project:
		return outerScanRowCount(x.Child)
	}
	return EstimateRows(n)
}

// origTypeMatches reports whether the slot at `cr.Index` in
// `outerSchema` carries a type compatible with `cr.Type`. When
// it returns false, the runtime IndexScan key encoder will fail
// with 42804 (`not numeric at runtime`) before any selectivity
// concern matters — so M0077-0001's tryBuildNLI rebind treats a
// `false` return as an override that bypasses the M0075-0002
// selectivity guard.
//
// The compatibility test is intentionally conservative: only
// the catalog Type Name is compared. ColumnRef.Type and the
// outerSchema entry are both populated from the same catalog
// types when the schema is current, so a mismatch is a strong
// signal that the schema annotation drifted (Q8's chained-NLI
// shape places `n_name` (char) at the slot the probe-key
// `c_nationkey` (numeric) was originally bound to).
func origTypeMatches(cr *ColumnRef, outerSchema Schema) bool {
	if cr == nil {
		return false
	}
	if cr.Index < 0 || cr.Index >= len(outerSchema) {
		// Out-of-range index can't be "matching"; tryBuildNLI
		// will treat this as a forced rebind via origTypeMismatch.
		return false
	}
	return outerSchema[cr.Index].Type.Name == cr.Type.Name
}
