package optimizer

// M0148-0001c3 — a partial index scan drops the restrictions its predicate
// implies, as create_indexscan_plan (createplan.c) does when it builds qpqual:
//
//	if (best_path->indexinfo->indpred)
//	    if (baserelid != root->parse->resultRelation &&
//	        get_plan_rowmark(root->rowMarks, baserelid) == NULL)
//	        if (predicate_implied_by(list_make1(clause), indpred, false))
//	            continue;		/* implied by index predicate */
//
// Every row the index holds satisfies the predicate, so a qual the predicate
// implies can never reject one: `WHERE x IS NOT NULL AND y < 10` over
// `ON nt (y) WHERE x IS NOT NULL` is `Index Cond: (y < 10)` with no Filter.
// PG keeps the qual for a result relation or a row-marked one, whose
// EvalPlanQual recheck must re-test the updated row version; goopg's recheck
// reads the Filter above the scan chain (lockRowsOp.findFilterPred and the DML
// twins), so plans that lock or modify rows are left alone entirely, as
// pushAppendRelRestrictions does.

import "github.com/goopg/goopg/internal/catalog"

// dropPartialIndexImpliedQuals applies the rule throughout the plan, sublink
// bodies included.
func dropPartialIndexImpliedQuals(root Node) Node {
	if root == nil || planTakesRowMarksOrModifies(root) {
		return root
	}
	seen := map[Node]bool{}
	var walk func(Node) Node
	walk = func(n Node) Node {
		if n == nil || seen[n] {
			return n
		}
		seen[n] = true
		for _, sp := range NodeSubplans(n) {
			walk(sp)
		}
		setPlanChildrenInPlace(n, walk)
		if f, ok := n.(*Filter); ok {
			if kept, changed := partialIndexFilterResidue(f); changed {
				if kept == nil {
					return f.Child
				}
				f.Predicate = kept
			}
		}
		return n
	}
	return walk(root)
}

// partialIndexFilterResidue is f's predicate without the conjuncts the
// predicate of the partial index scanned directly below f implies. changed is
// false when f is not over such a scan or nothing is implied; kept is nil when
// every conjunct is.
func partialIndexFilterResidue(f *Filter) (kept Expr, changed bool) {
	if f.Predicate == nil {
		return nil, false
	}
	var idx *catalog.Index
	var tbl *catalog.Table
	switch s := f.Child.(type) {
	case *IndexScan:
		idx, tbl = s.Index, s.Table
	case *IndexOnlyScan:
		idx, tbl = s.Index, s.Table
	case *BitmapHeapScan:
		// create_bitmap_scan_plan's qpqual makes the same test against the
		// predicates of the partial indexes under the bitmap; only the
		// single-index bitmap is handled here.
		if bi, ok := s.Outer.(*BitmapIndexScan); ok {
			idx, tbl = bi.Index, s.Table
		}
	default:
		return nil, false
	}
	if idx == nil || tbl == nil || !idx.HasPredicate {
		return nil, false
	}
	pred, err := ResolveIndexPredicateOf(idx, tbl)
	if err != nil || pred == nil {
		return nil, false
	}
	var rest []Expr
	for _, c := range splitAnd(f.Predicate) {
		if provePartialIndexPredicate(c, pred) {
			changed = true
			continue
		}
		rest = append(rest, c)
	}
	if !changed {
		return nil, false
	}
	if len(rest) == 0 {
		return nil, true
	}
	return combineAnd(rest), true
}
