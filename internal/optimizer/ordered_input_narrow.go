package optimizer

import "github.com/goopg/goopg/internal/parser"

// M0141-S2a-fix1-sweep-a — narrow the ORDERED upper rel's Sort cost inputs
// (NCols/AvgVarBytes) to the columns the statement actually needs, mirroring
// fix1's "compute once ahead of costing, consume at the existing read site"
// shape for the site fix1 itself did not cover: `createOrderedPaths`'s
// `sizeUpperRelFromNode(ordered, input)` call, which sizes the ORDERED rel
// from the finished input Node's FULL row (upperrel.go).
//
// PG's `create_sort_path` (postgres/src/backend/optimizer/util/pathnode.c:
// 3221-3250) prices the Sort from `subpath->pathtarget->width` — already
// narrow by the time `create_ordered_paths` runs, because PG's upper-planner
// projection machinery narrows the pathtarget at each stage before this
// point. goopg has no such staged narrowing above the seam; this file
// derives the PG-equivalent narrow keep-set BEFORE the `*Sort` node exists
// (there is nothing to stamp `sort.InputTarget` on yet at cost time — see
// sort_input_target.go's own "stamped too late" finding) from two name sets
// this package already knows how to enumerate:
//
//   - the sort-key columns (`sortKeyColumnNames`, shared with
//     sort_input_target.go's later above-aware re-stamp of the same Sort);
//   - the statement's own final SELECT-list output columns, resolved early
//     via the SAME resolveTargets*/ctx/agg/win objects `buildSelectPlan`
//     itself uses a little further down to build the real output Project —
//     this is a second, throwaway resolution for naming purposes only, not
//     a replacement of that later call, and any error here is swallowed
//     (declined) rather than surfaced: the real error still surfaces at the
//     later, authoritative call site.
//
// Scope: only the plain top-level ORDER BY arm (no pending ProjectSet of
// either kind — composite-star `(expr).*` or a SELECT-list SRF) is narrowed.
// A ProjectSet's expanded schema is not yet finalized at Sort-cost time for
// the pre-sort arm, and for the post-sort arm the Sort already runs directly
// over the ProjectSet's own output (nothing wider sits between them to
// narrow away) — declining in both leaves them at today's full-width
// behavior, the always-safe answer. The two other `createOrderedPaths`
// call sites outside the main SELECT builder (`wrapSetOpSortLimit`'s set-op
// ORDER BY, and the min/max-rewrite ORDER BY re-attach) both sort directly
// over a Node whose Output() already IS the minimal final schema, so there
// is nothing narrower to derive; they keep passing an empty keep the same
// way.
//
// Safety direction, same as R121/relNarrowedWidths: "unknown" declines to
// today's full-width sizing, and the returned keep-set only ever narrows —
// never invents a missing column, since it is exactly the union of two
// name-sets each already used elsewhere in this exact form.

// finalSelectOutputNames resolves the statement's own final SELECT-list
// output columns early (before the ORDER BY Sort is costed), using the same
// ctx/agg/win objects `buildSelectPlan`'s own later, authoritative target
// resolution reads. ok == false ("decline") whenever a ProjectSet (composite
// star or SELECT-list SRF) sits in play — its expanded schema is not the
// stable answer this function can safely name early — or the early
// resolution itself errors (the real error, if genuine, surfaces later at
// the authoritative call; this function only declines).
func finalSelectOutputNames(s *parser.SelectStmt, ctx *resolveContext, agg *aggregateSurface, win *windowSurface, starPS *ProjectSet, srfPending bool) (map[string]bool, bool) {
	if s == nil || starPS != nil || srfPending {
		return nil, false
	}
	var (
		schema Schema
		err    error
	)
	switch {
	case win != nil:
		_, schema, err = resolveTargetsAfterWindow(s.Targets, win)
	case agg == nil:
		_, schema, err = resolveTargets(s.Targets, ctx)
	default:
		_, schema, err = resolveTargetsAfterAggregate(s.Targets, agg)
	}
	if err != nil {
		return nil, false
	}
	names := make(map[string]bool, len(schema))
	for _, c := range schema {
		names[c.Name] = true
	}
	return names, true
}

// deriveOrderedSortInputKeep computes the ORDERED rel's Sort cost-input
// keep-set: ascending positions into input.Output() of the sort-key columns
// (from keys) union the statement's final output columns (aboveNames). ok ==
// false ("decline", keep today's full-width sizing) when aboveNames itself
// is unknown, the sort keys are not fully enumerable (an unenumerated key
// expression must keep its columns — sortKeyColumnNames' own contract), or
// the union matches no input column at all (an empty keep-set is
// unrepresentable the same way relNarrowedWidths declines one: a fresh
// RelOptInfo needs NCols > 0 for AvgVarBytes to mean anything).
func deriveOrderedSortInputKeep(keys []SortKey, aboveNames map[string]bool, aboveKnown bool, input Node) ([]int, bool) {
	if !aboveKnown || input == nil {
		return nil, false
	}
	keyNames, ok := sortKeyColumnNames(keys)
	if !ok {
		return nil, false
	}
	need := make(map[string]bool, len(keyNames)+len(aboveNames))
	for name := range keyNames {
		need[name] = true
	}
	for name := range aboveNames {
		need[name] = true
	}
	cols := input.Output()
	keep := make([]int, 0, len(need))
	for i, c := range cols {
		if need[c.Name] {
			keep = append(keep, i)
		}
	}
	if len(keep) == 0 {
		return nil, false
	}
	return keep, true
}

// narrowOrderedRelWidths refines the NCols/AvgVarBytes `sizeUpperRelFromNode`
// just published on rel to the columns named by keep — positions into
// child.Output(), the same coordinate space `sizeUpperRelFromNode` itself
// reads. Rows and Width are left exactly as sizeUpperRelFromNode set them:
// this is Site A's NCols/AvgVarBytes currency fix only (fix1's own
// precedent, and `pgSortRelationBytesCostEnabled()` — default off — is the
// only consumer of rel.Width here, per M0141-S2a-fix1-sweep's design doc).
func narrowOrderedRelWidths(rel *RelOptInfo, child Node, keep []int) {
	if rel == nil || child == nil || len(keep) == 0 {
		return
	}
	cols := child.Output()
	narrowed := make([]SchemaColumn, 0, len(keep))
	for _, i := range keep {
		if i >= 0 && i < len(cols) {
			narrowed = append(narrowed, cols[i])
		}
	}
	if len(narrowed) == 0 {
		return
	}
	rel.NCols = len(narrowed)
	rel.AvgVarBytes = nodeAvgVarBytes(narrowed)
}
