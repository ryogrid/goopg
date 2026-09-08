package optimizer

// upper_narrow_gate.go — B-01c APPLYING half, slice (a): the
// KEY-PRESERVATION GATE for the upper narrowing sites.
//
// COMPUTE-ONLY. Nothing in this file mutates a plan, inserts a node,
// changes a schema or moves a cost. It has ZERO production callers by
// design (the E-15 precedent): it is the proof obligation that a later
// applying cut must discharge BEFORE it narrows anything.
//
// # Why this half is load-bearing, not paperwork
//
// The compute half (sort_input_target.go / group_input_target.go /
// window_input_target.go) derives WHICH input columns a Sort / Aggregate /
// WindowAgg needs. Applying that derivation means dropping the rest and
// re-basing every surviving `ColumnRef.Index` — goopg's Var is POSITIONAL
// (ledger `take3-C-20b-var-is-positional`), so a keep-list is a coordinate
// change, and a coordinate error misaligns that column AND EVERY COLUMN
// AFTER IT.
//
// At a Sort that misalignment is a SILENT WRONG ANSWER. The executor gate
// checks ordering EXPLICITLY, not membership (`internal/executor/
// operators.go`, `sortOp.sortKeyVals`: "a tail sorted by one comparator and
// merged against spill files by another emits out-of-order rows with no
// error"). A row-count gate cannot see it. Neither can a values gate that
// sorts before comparing. It is the same class C-07 just fixed elsewhere in
// the planner (the outer's pathkeys kept across a FULL/RIGHT join: correct
// row count, wrong order).
//
// So the question this file answers is not "does the keep contain the key
// columns by name" — the compute half's asserts already ask that, and a
// NAME check is unsound here because a join output legitimately carries two
// columns of the same name. The question is POSITIONAL and per-comparator:
//
//	after the cut, is key i of the narrowed node the SAME comparator,
//	in the SAME position, with the SAME DESC/NULLS flags, as key i of
//	the un-narrowed node?
//
// # How it is proved
//
// By ROUND TRIP, not by inspection. `keepPreservesExpr` remaps an
// expression forward through the keep (old position -> new position), then
// remaps the result BACK through the inverse, and requires the result to be
// identity-equal to the original under `exprIdentityKey(_, scopeVeto)` —
// the fail-closed content key from M0125-0024. A remap that loses, aliases
// or reorders a coordinate cannot survive that: only a bijection on the
// referenced positions round-trips.
//
// # Fail-closed, and where it declines
//
// Every traversal here is `cloneExprRefs` under `scopeVeto`, so it is
// exhaustive over all 32 `Expr` types by construction and an unenumerated
// 33rd type is a build-time failure of `exprwalk_exhaustive_test.go`, not a
// silent pass-through. This is deliberately the `cloneExprRefs` shape and
// deliberately NOT the `shiftColumnRefsBy` shape (13 of 32 arms, `return e`
// for the rest) — a walker that silently misses an arm is exactly how this
// codebase has been bitten before (`exprwalk_inventory_test.go`).
//
// The gate REFUSES, never guesses, on:
//
//   - a `ColumnRef` at a position the keep drops — the cut would delete a
//     column the node reads;
//   - a `ColumnRef` with an empty `Name` — unnameable, and the compute
//     half vetoes it too (`visitColumnRefsByName`), so accepting it here
//     would admit a shape the stamp can never produce;
//   - `*OuterColumnRef`, `*CTIDExpr`, `*MergeWholeRowRef` — same veto set
//     as `visitColumnRefsByName`. CTID and the MERGE whole-row composite
//     are read from the scanned tuple, which a narrowed row no longer
//     carries; an outer ref names a different scope's row;
//   - ANY inner plan (`slotInnerPlan` / `slotSubqRow` under `scopeVeto`) —
//     an `InExpr`/`ExistsExpr`/`SubqueryExpr` `Plan` may contain
//     `OuterColumnRef`s at a level that resolves to THE ROW BEING
//     NARROWED, and translating those is precisely the missing
//     "narrowing-aware OuterColumnRef/Args rewriter" that declined B-01b
//     (ledger `take3-B-01b-declined` item (ii)). Their same-scope `Args`
//     ARE remapped when no `Plan` hangs off the node; a node that has one
//     is refused whole.
//
// Nothing in the veto set is a wrong answer: a refusal leaves the site at
// today's full width, bit-identically.
//
// Companion invariants this file must never weaken: `createplanroot.go`'s
// boundary assertions and `rangetable.go`'s `assertBoundaryColumnIdentity`
// are the DETECTOR for this very class.

import "fmt"

// ---------------------------------------------------------------------
// keepMap — a keep-list read as a coordinate change
// ---------------------------------------------------------------------

// keepMap is an `InputTarget` keep-list (ascending, unique, in-range
// positions into a row of `inWidth` columns) turned into the two directions
// a coordinate change needs: `forward` (pre-cut position -> post-cut
// position) and `inverse` (post-cut -> pre-cut).
//
// The struct exists so the validity conditions are checked ONCE, at
// construction, rather than re-argued at each use. `newKeepMap` is the only
// constructor and it refuses anything that is not a strictly ascending,
// in-range list — the exact shape `assertSortInputTargetCoversKeys` and its
// two siblings already panic on, restated here as a value the applying cut
// can carry.
type keepMap struct {
	inWidth int
	keep    []int // ascending, unique, each in [0, inWidth)
	fwd     []int // len inWidth; -1 = dropped by the cut
}

// newKeepMap validates keep against a row of inWidth columns and builds both
// directions. ok == false means the keep-list is malformed (not ascending,
// duplicated, negative, out of range, or longer than the row) — never a
// reason to narrow anyway.
func newKeepMap(keep []int, inWidth int) (keepMap, bool) {
	if inWidth < 0 || len(keep) > inWidth {
		return keepMap{}, false
	}
	fwd := make([]int, inWidth)
	for i := range fwd {
		fwd[i] = -1
	}
	prev := -1
	for newIdx, old := range keep {
		if old <= prev || old < 0 || old >= inWidth {
			return keepMap{}, false
		}
		prev = old
		fwd[old] = newIdx
	}
	return keepMap{inWidth: inWidth, keep: append([]int(nil), keep...), fwd: fwd}, true
}

// narrowedWidth is the column count of the post-cut row.
func (km keepMap) narrowedWidth() int { return len(km.keep) }

// isIdentity reports whether the cut removes nothing. Ascending + unique +
// in-range makes `len(keep) == inWidth` equivalent to `keep == [0, inWidth)`.
// An applying cut MUST skip an identity keep: inserting a Project that
// reproduces its input verbatim is pure cost with zero memory saved.
func (km keepMap) isIdentity() bool { return len(km.keep) == km.inWidth }

// forward maps a pre-cut column position to its post-cut position.
// ok == false means the cut DROPS that column.
func (km keepMap) forward(old int) (int, bool) {
	if old < 0 || old >= km.inWidth {
		return 0, false
	}
	if n := km.fwd[old]; n >= 0 {
		return n, true
	}
	return 0, false
}

// inverse maps a post-cut column position back to its pre-cut position.
func (km keepMap) inverse(newIdx int) (int, bool) {
	if newIdx < 0 || newIdx >= len(km.keep) {
		return 0, false
	}
	return km.keep[newIdx], true
}

// ---------------------------------------------------------------------
// remapExprIndices — the exhaustive, fail-closed positional rewriter
// ---------------------------------------------------------------------

// remapExprIndices returns a CLONE of e with every same-scope
// `ColumnRef.Index` translated through f, leaving e untouched.
//
// ok == false is a refusal, and the only safe answer for every case listed
// in the file header. It is never "e needed no change".
//
// The traversal is `cloneExprRefs` under `scopeVeto` — the exhaustive
// driver, gated by `exprwalk_exhaustive_test.go` in both directions — so
// this function inherits total coverage of the `Expr` type set instead of
// re-deriving a partial type switch (the `shiftColumnRefsBy` mistake).
// `Rewrite` runs BOTTOM-UP on the clone, so mutating `x.Index` below cannot
// reach the caller's tree.
func remapExprIndices(e Expr, f func(int) (int, bool)) (Expr, bool) {
	if e == nil {
		return nil, true
	}
	refused := false
	out, ok := cloneExprRefs(e, scopeVeto, exprRewriter{
		Rewrite: func(n Expr) Expr {
			switch x := n.(type) {
			case *ColumnRef:
				// Unnamed refs are vetoed by the compute half's
				// collector; accepting one here would admit a shape
				// no stamp can produce.
				if x.Name == "" {
					refused = true
					return n
				}
				ni, ok := f(x.Index)
				if !ok {
					refused = true
					return n
				}
				x.Index = ni
				return x
			case *OuterColumnRef, *CTIDExpr, *MergeWholeRowRef:
				refused = true
			}
			return n
		},
		OnUnknown: func(Expr) { refused = true },
	})
	if !ok || refused {
		return nil, false
	}
	return out, true
}

// ---------------------------------------------------------------------
// keepPreservesExpr — the round-trip proof for ONE expression
// ---------------------------------------------------------------------

// keepPreservesExpr proves that narrowing the row under km preserves e
// exactly, and returns e rewritten into post-cut coordinates.
//
// The proof is a round trip: forward through km, back through km's inverse,
// then `exprIdentityKey(_, scopeVeto)` equality against the original. A
// remap that drops, aliases or reorders a coordinate cannot round-trip;
// only a bijection on the positions e actually reads can. Comparing the
// FORWARD result to the original directly would prove nothing (the indices
// are supposed to differ), and comparing only column MEMBERSHIP is the
// unsound name-level check this gate exists to replace.
//
// `scopeVeto` is mandatory for the identity key: an inner plan has no
// identity function, so `scopeIgnore` would step over it and two different
// subqueries with identical `Args` would key EQUAL (exprwalk.go).
func keepPreservesExpr(e Expr, km keepMap) (Expr, bool) {
	if e == nil {
		return nil, true
	}
	fwd, ok := remapExprIndices(e, km.forward)
	if !ok {
		return nil, false
	}
	back, ok := remapExprIndices(fwd, km.inverse)
	if !ok {
		return nil, false
	}
	origKey, ok := exprIdentityKey(e, scopeVeto)
	if !ok {
		return nil, false
	}
	backKey, ok := exprIdentityKey(back, scopeVeto)
	if !ok {
		return nil, false
	}
	if origKey != backKey {
		return nil, false
	}
	return fwd, true
}

// keepPreservesSortKeyList is the ORDERING half of the gate, and the reason
// the gate exists at all.
//
// It rebuilds the key list under km position by position and requires:
//
//   - every key expression round-trips (`keepPreservesExpr`) — so no
//     comparator silently reads a different column after the cut;
//   - the list has the SAME LENGTH and the SAME ORDER — a lost or reordered
//     key changes the sort order with a correct row count;
//   - `Desc` and `NullsFirst` are carried through UNCHANGED — they are the
//     other half of the comparator and are not expressions, so no
//     expression-level check can see them.
//
// A nil key `Expr` is REFUSED rather than skipped. The compute half skips
// nils (they contribute no column name and a name-set is all it builds);
// here a nil is an undecidable comparator sitting in an ordering list, and
// "undecidable" must not read as "preserved".
func keepPreservesSortKeyList(keys []SortKey, km keepMap) ([]SortKey, bool) {
	out := make([]SortKey, 0, len(keys))
	for _, k := range keys {
		if k.Expr == nil {
			return nil, false
		}
		nk, ok := keepPreservesExpr(k.Expr, km)
		if !ok {
			return nil, false
		}
		out = append(out, SortKey{Expr: nk, Desc: k.Desc, NullsFirst: k.NullsFirst})
	}
	if len(out) != len(keys) {
		return nil, false
	}
	for i := range out {
		if out[i].Desc != keys[i].Desc || out[i].NullsFirst != keys[i].NullsFirst {
			return nil, false
		}
	}
	return out, true
}

// keepPreservesExprList is the grouping/partitioning counterpart: the same
// per-expression round trip plus length-and-order preservation, for lists
// whose ORDER is semantically load-bearing even though they carry no
// DESC/NULLS flags (`Aggregate.GroupExprs` — `GroupKeyOrder` and
// `GroupingMasks` index INTO it positionally — and `WindowAgg.PartitionBy`).
func keepPreservesExprList(es []Expr, km keepMap) ([]Expr, bool) {
	out := make([]Expr, 0, len(es))
	for _, e := range es {
		if e == nil {
			return nil, false
		}
		ne, ok := keepPreservesExpr(e, km)
		if !ok {
			return nil, false
		}
		out = append(out, ne)
	}
	if len(out) != len(es) {
		return nil, false
	}
	return out, true
}

// ---------------------------------------------------------------------
// The node-level gate
// ---------------------------------------------------------------------

// keepPreservesUpperNodeKeys is THE gate: it reports whether narrowing n's
// INPUT row to keep is safe, and why not when it is not.
//
// `reason` is empty exactly when ok is true. It is not decoration: every
// refusal below is a distinct missing capability, and a future applying cut
// that wants to lift one needs to know which.
//
// Two checks, in this order:
//
//  1. MEMBERSHIP, over every expression the node evaluates against its
//     input row. That set comes from `enclosingNodeScopeOf`, deliberately
//     — it is the ONE switch in this package that answers "which
//     expressions does this node evaluate, and what row do they index
//     into", and it is WIDER than `walkPlanExprs` (it covers
//     `Aggregate.Passthrough`, `AggregateCall.Filter`, `WindowFunc.Args`,
//     `WindowFunc.Filter` and the frame offsets). Re-deriving the set here
//     would be the sibling-path divergence this project keeps paying for.
//     `sc.width` is also the authority on the input row's width, which is
//     why the keep is validated against it rather than against
//     `len(n.Child.Output())` read independently.
//
//  2. ORDERING, over the key lists whose ORDER is the answer. Check (1)
//     already proves each of these expressions round-trips; (2) additionally
//     proves the LIST is the same sequence of comparators with the same
//     flags. Membership without (2) is exactly the D-06 hazard.
//
// Only `*Sort`, `*Aggregate` and `*WindowAgg` are narrowing sites. Anything
// else is refused: `*Join` and `*NestedLoopIndexJoin` report a MERGED
// `Left ++ Right` width from `enclosingNodeScopeOf`, so a keep validated
// against it would be validated against the wrong row, and the scan-arm
// sites are B-01b's declined territory.
func keepPreservesUpperNodeKeys(n Node, keep []int) (ok bool, reason string) {
	switch n.(type) {
	case *Sort, *Aggregate, *WindowAgg:
	case nil:
		return false, "nil node"
	default:
		return false, fmt.Sprintf("%T is not an upper narrowing site (Sort/Aggregate/WindowAgg only)", n)
	}

	sc, known := enclosingNodeScopeOf(n)
	if !known {
		return false, fmt.Sprintf("enclosingNodeScopeOf does not enumerate %T", n)
	}
	km, valid := newKeepMap(keep, sc.width)
	if !valid {
		return false, fmt.Sprintf("keep %v is not an ascending in-range subset of a %d-column input row", keep, sc.width)
	}

	// (1) membership — every expression the node reads from the input row.
	for i, e := range sc.exprs {
		if _, ok := keepPreservesExpr(e, km); !ok {
			return false, fmt.Sprintf("input expression %d of %T does not survive the cut (dropped column, outer/CTID/whole-row read, inner plan, or unenumerable type)", i, n)
		}
	}

	// (2) ordering — the key lists whose ORDER and flags are the answer.
	switch x := n.(type) {
	case *Sort:
		if _, ok := keepPreservesSortKeyList(x.Keys, km); !ok {
			return false, "sort keys are not preserved by the cut"
		}
	case *Aggregate:
		if _, ok := keepPreservesExprList(x.GroupExprs, km); !ok {
			return false, "group keys are not preserved by the cut"
		}
		for i := range x.Aggs {
			a := &x.Aggs[i]
			if _, ok := keepPreservesSortKeyList(a.OrderBy, km); !ok {
				return false, fmt.Sprintf("aggregate %d ORDER BY keys are not preserved by the cut", i)
			}
			if _, ok := keepPreservesSortKeyList(a.WithinGroupOrderBy, km); !ok {
				return false, fmt.Sprintf("aggregate %d WITHIN GROUP ORDER BY keys are not preserved by the cut", i)
			}
		}
	case *WindowAgg:
		if _, ok := keepPreservesExprList(x.PartitionBy, km); !ok {
			return false, "window PARTITION BY keys are not preserved by the cut"
		}
		if _, ok := keepPreservesSortKeyList(x.OrderBy, km); !ok {
			return false, "window ORDER BY keys are not preserved by the cut"
		}
	}
	return true, ""
}

// keepPreservesStampedTarget runs the gate against the keep the compute half
// already stamped on n, which is the shape an applying cut actually faces.
//
// An UNKNOWN stamp is refused, not treated as "narrow nothing": unknown means
// the derivation could not enumerate the site, and `InputTarget` is then nil,
// which as a keep-list would mean "drop every column".
func keepPreservesStampedTarget(n Node) (bool, string) {
	switch x := n.(type) {
	case *Sort:
		if !x.InputTargetKnown {
			return false, "Sort input target is unknown"
		}
		return keepPreservesUpperNodeKeys(x, x.InputTarget)
	case *Aggregate:
		if !x.InputTargetKnown {
			return false, "Aggregate input target is unknown"
		}
		return keepPreservesUpperNodeKeys(x, x.InputTarget)
	case *WindowAgg:
		if !x.InputTargetKnown {
			return false, "WindowAgg input target is unknown"
		}
		return keepPreservesUpperNodeKeys(x, x.InputTarget)
	}
	return false, fmt.Sprintf("%T carries no upper input target", n)
}

// upperNarrowingChangesOutput reports whether narrowing n's INPUT row also
// narrows n's OUTPUT row — i.e. whether the cut needs the narrowing-aware
// UPPER REWRITER on top of this gate.
//
// This is the asymmetry an applying cut should be sequenced around, and it
// is a fact about the three nodes' `Output()`:
//
//   - `*Sort.Output()` is `Child.Output()` verbatim, so a narrowed input is
//     a narrowed output: EVERY expression above the Sort must be re-based.
//   - `*WindowAgg` publishes `[child output..., window func outputs...]`,
//     so the same holds, with the func columns shifting left as well.
//   - `*Aggregate.Output()` is group exprs ++ aggs ++ grouping masks ++
//     passthrough — derived from the node's OWN expression lists, not from
//     the child's width. Narrowing an Aggregate's input therefore does NOT
//     move a single column above it.
//
// So the Aggregate site is the one upper site whose applying cut needs this
// gate and NOTHING ELSE; the other two are gated on the upper rewriter that
// declined B-01b. ok == false for any other node kind.
func upperNarrowingChangesOutput(n Node) (changes bool, ok bool) {
	switch n.(type) {
	case *Sort:
		return true, true
	case *WindowAgg:
		return true, true
	case *Aggregate:
		return false, true
	}
	return false, false
}
