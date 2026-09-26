package optimizer

// groupclause.go — M0145-0008d: PG's processed_groupClause.
//
// PG decides the order and direction of sort-based grouping in two steps:
//
//  1. Parse analysis (transformGroupClauseExpr, parse_clause.c:2424-2438): a
//     GROUP BY item that ORDER BY also names takes a COPY of ORDER BY's
//     SortGroupClause, sort operator and nulls ordering included. So
//     `GROUP BY g ORDER BY g DESC` groups on g DESC. This holds whether or
//     not g leads the ORDER BY: `ORDER BY count(*) DESC, g DESC` still makes
//     the GROUP BY item g DESC.
//  2. preprocess_groupclause (planner.c:2828): the GROUP BY items that form a
//     prefix of ORDER BY move to the front, in ORDER BY's order, compared
//     with equal() (expression AND direction). The first ORDER BY item that
//     is not a GROUP BY item ends the prefix. The remaining GROUP BY items
//     follow in written order. With no match at all the written order stands.
//
// The result is both the grouping pathkeys the scan level is asked for
// (querypathkeys.go groupClauseItems) and the sort a GroupAggregate consumes
// (Aggregate.GroupClause). Both derive from processedGroupOrder so the two
// sibling paths cannot disagree.
//
// Matching uses parserSortExprEqual, the matcher the presearch pathkeys
// already use: the same node after alias/ordinal substitution, or the same
// column name. PG matches through the target entry (tleSortGroupRef), which
// also pairs equal non-column expressions; that wider match is ledgered.

import "github.com/goopg/goopg/internal/parser"

// processedGroupItem is one entry of the processed group clause: an index
// into the group item list handed to processedGroupOrder, and its direction.
type processedGroupItem struct {
	idx        int
	desc       bool
	nullsFirst bool
}

// processedGroupOrder applies both steps to groupItems (the GROUP BY
// expressions still in the clause, in written order) against s's ORDER BY.
// It returns nil when the result is the default: written order, every key
// ASC NULLS LAST. That is also the answer for grouping sets (PG forces the
// rollup order there) and for a query with no ORDER BY.
func processedGroupOrder(groupItems []parser.Expr, s *parser.SelectStmt) []processedGroupItem {
	if s == nil || s.GroupingSets != nil || len(s.OrderBy) == 0 || len(groupItems) == 0 {
		return nil
	}
	sorts := sortClauseItems(s)
	if len(sorts) == 0 {
		return nil
	}
	// Step 1: each GROUP BY item copies the direction of the first ORDER BY
	// item naming it.
	items := make([]processedGroupItem, len(groupItems))
	for i, g := range groupItems {
		items[i] = processedGroupItem{idx: i}
		if g == nil {
			continue
		}
		for _, sb := range sorts {
			if parserSortExprEqual(g, sb.expr, s) {
				items[i].desc, items[i].nullsFirst = sb.desc, sb.nullsFirst
				break
			}
		}
	}
	// Step 2: the ORDER BY prefix, compared on expression and direction
	// (equal(gc, sc) on the SortGroupClause).
	used := make([]bool, len(items))
	out := make([]processedGroupItem, 0, len(items))
	for _, sb := range sorts {
		hit := -1
		for i, g := range groupItems {
			if used[i] || g == nil {
				continue
			}
			if items[i].desc == sb.desc && items[i].nullsFirst == sb.nullsFirst &&
				parserSortExprEqual(g, sb.expr, s) {
				hit = i
				break
			}
		}
		if hit < 0 {
			break
		}
		used[hit] = true
		out = append(out, items[hit])
	}
	for i := range items {
		if !used[i] {
			out = append(out, items[i])
		}
	}
	for i, it := range out {
		if it.idx != i || it.desc || it.nullsFirst {
			return out
		}
	}
	return nil
}

// buildGroupClause is Aggregate.GroupClause for the final group list.
// origIdx[k] is the s.GroupBy index GroupExprs[k] came from; pruned GROUP BY
// items are absent, as remove_useless_groupby_columns removes them from the
// clause before preprocess_groupclause runs.
func buildGroupClause(s *parser.SelectStmt, origIdx []int) []GroupClauseKey {
	if s == nil {
		return nil
	}
	items := make([]parser.Expr, len(origIdx))
	for k, oi := range origIdx {
		if oi < 0 || oi >= len(s.GroupBy) {
			return nil
		}
		items[k] = s.GroupBy[oi]
	}
	order := processedGroupOrder(items, s)
	if order == nil {
		return nil
	}
	out := make([]GroupClauseKey, len(order))
	for i, it := range order {
		out[i] = GroupClauseKey{Pos: it.idx, Desc: it.desc, NullsFirst: it.nullsFirst}
	}
	return out
}

// groupClauseKeys is agg's grouping order: its GroupClause, or written order
// ASC NULLS LAST when it has none. Only an entry list that names every group
// expression exactly once is honoured; anything else falls back to the
// default rather than sort on a partial key list.
func groupClauseKeys(agg *Aggregate) []GroupClauseKey {
	n := len(agg.GroupExprs)
	if len(agg.GroupClause) == n {
		seen := make([]bool, n)
		ok := true
		for _, k := range agg.GroupClause {
			if k.Pos < 0 || k.Pos >= n || seen[k.Pos] {
				ok = false
				break
			}
			seen[k.Pos] = true
		}
		if ok {
			return agg.GroupClause
		}
	}
	out := make([]GroupClauseKey, n)
	for i := range out {
		out[i] = GroupClauseKey{Pos: i}
	}
	return out
}

// transportGroupSortKeys is the sort/merge key list for the sorted
// row-transport split (M0146-0003 S6): one SortKey per group-clause
// entry, in clause order, whose Expr is a POSITIONAL ColumnRef into the
// transport row — position k.Pos carries GroupExprs[k.Pos]'s value, so
// the same list sorts each worker's partial output, merges the streams
// in the GatherMerge, and grounds the finalize's emission-order claim
// (aggregateEmissionPathkeys) with no coordinate translation at all.
//
// The ref is a clone of the group expression's own ColumnRef with Index
// rebound to k.Pos: Name/Type/SourceTableIdx ride along, so `Sort Key:
// l_returnflag` still renders the name PG prints under Gather Merge
// rather than an anonymous position.
//
// Declines (ok=false) when a group expression is not a *ColumnRef: the
// positional ref would still evaluate correctly, but the clause could
// then render no honest `Sort Key:` name — the ledgered remainder, not
// a silently mislabeled plan (same posture partialGroupKeyRefs takes).
func transportGroupSortKeys(agg *Aggregate) ([]SortKey, bool) {
	keys := make([]SortKey, 0, len(agg.GroupExprs))
	for _, k := range groupClauseKeys(agg) {
		cr, ok := agg.GroupExprs[k.Pos].(*ColumnRef)
		if !ok {
			return nil, false
		}
		ref := *cr
		ref.Index = k.Pos
		keys = append(keys, SortKey{Expr: &ref, Desc: k.Desc, NullsFirst: k.NullsFirst})
	}
	return keys, true
}
