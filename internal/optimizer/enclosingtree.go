package optimizer

// M0127-P5.5-f-ii-b — the two remaining consumers of the search boundary: the
// pinned-spine re-resolution, and 03 §10's tripwire widened from one node to the
// tree it guards.
//
// Design: leftdeep-joins 03 §10, 02 §3. Companions: `createplanroot.go`
// (P5.5-f-i, which built the boundary) and `searchedtree.go` (P5.5-f-ii-a,
// which made the legacy layout-correction family skip past it).
//
// # What P5.5-f left open
//
// P5.5-f-i established the boundary and P5.5-f-ii-a made three legacy passes
// respect it. Two consumers were named in 03 §10 and neither had been closed:
//
//  1. "The pinned-spine re-resolution in `predp.go` survives and consumes this
//     map." It survives, but it had never been shown to *consume* anything: the
//     search is not wired into `tryBushyDP` yet, so the claim "the map is the
//     identity above the root, so the spine re-resolution is skipped" was true
//     by argument and unexercised by code.
//  2. "A build-mode assertion that every `ColumnRef` above the search root
//     resolves within its input schema." That assertion is real code
//     (`assertColumnRefsWithinSchema`) but P5.5-f-i applied it to exactly one
//     node — the boundary `Project`'s own targets. The class it exists to catch
//     (M0097-0058: an index-skew bug surfacing as an out-of-bounds slice access
//     during EXECUTION) lives ABOVE that node, in the expressions the enclosing
//     tree resolved in pre-search binding coordinates.
//
// This file closes both, and the shape of the second is decided by the lesson
// P5.5-f-ii-a paid for.
//
// # The vacuity guard, and why it is the load-bearing part
//
// P5.5-f-ii-a's second finding was that an assertion which *abstains* where it
// cannot decide will pass for the wrong reason: `reresolveJoinByName` returns
// immediately on an unnamed operand, so reusing the P5.5-e fixtures (whose
// clauses are built from bare `col(i)`) would have made every assertion about
// `assertSearchedTreeNeedsNoReconcile` pass vacuously without checking one
// index.
//
// A tree walk has exactly the same failure mode, one level up. goopg has 53
// node kinds; a walk that enumerates a subset and silently stops at the rest is
// *correct* in the sense that it never accuses an innocent tree — and if the
// node kind it stops at happens to sit between the enclosing root and the
// searched subtree, it checks NOTHING while reporting success. That is the
// same false green, and it is worse here because the walk looks exhaustive.
//
// So the walk is deliberately partial and the guard is on the partiality, not
// on the enumeration:
//
//   - a node kind `enclosingNodeScope` does not enumerate is a STOP, not a
//     panic — enumerating all 53 (DML, utility, set-op, every FROM-clause SRF)
//     to assert something about a SELECT's upper nodes would trade a real check
//     for a maintenance surface, and a crash on an unenumerated kind is a
//     coverage gap masquerading as a defect;
//   - but the walk must REACH a searched subtree, and
//     `assertEnclosingTreeColumnRefs` panics if it did not, naming every kind it
//     stopped at. A stop off the path costs nothing; a stop on the path is now
//     impossible to mistake for a pass.
//
// # What "its input schema" means per node kind
//
// One switch answers all three questions at once — which expressions a node
// evaluates, what row those expressions index into, and which children continue
// the same walk — because the three are one fact about a node and splitting
// them into three switches is the sibling-path divergence this project keeps
// paying for (Rule #2). Two entries are worth stating out loud:
//
//   - `*Join`: the predicate AND both keys index into the MERGED `Left ++ Right`
//     row, including for Semi/Anti (whose OUTPUT is Left only — `Output()`
//     publishes the outer schema, but the predicate evaluates against the padded
//     row; `reresolveJoinByName` rebinds the right side at `offset = leftWidth`
//     for exactly this reason). Checking join expressions against `Output()`
//     would therefore reject every legal right-side key on a semi join.
//   - `*NestedLoopIndexJoin`: the residual predicate indexes the merged row, but
//     the walk descends into `Outer` ONLY. The inner `*IndexScan`'s key
//     expressions address the OUTER row through the parameterised probe, not the
//     inner one — a different coordinate space, and checking them against the
//     inner's own width would be checking the wrong schema, the same boundary
//     `translateToLayout` and `assertColumnRefsWithinSchema`'s `scopeIgnore`
//     policy draw.
//
// # Live since M0127-P5.9 (2026-08-06)
//
// `GOOPG_PGSHAPED_DP` is ON by default and `planSelect` calls the search, so
// production trees DO carry the tag, `splicedSearchedRoot` finds them, and both
// assertions below run on real plans rather than only in
// `enclosingtree_test.go`.

import "fmt"

// enclosingNodeScope is what one node contributes to the tripwire walk: the
// expressions it evaluates, the width of the row those expressions index into,
// and the children whose expressions are governed by the same rule.
type enclosingNodeScope struct {
	exprs    []Expr
	width    int
	children []Node
}

// enclosingNodeScopeOf answers the three questions for the node kinds that can
// appear between a searched subtree and the top of a SELECT: the pinned spine
// (`*Filter`, `*Join`) and the upper nodes `planSelect` stacks above a join tree.
//
// ok == false means "this walk does not know this node's coordinate space" and
// is a stop, never an accusation — see the header for why the guard is on
// reaching the searched subtree rather than on this returning true.
//
// Two expression sets here are wider than `walkPlanExprs`'s corresponding cases:
// `Aggregate.Passthrough` + `AggregateCall.Filter`, and `WindowFunc.Args` +
// `WindowFunc.Filter` + the frame offsets. All of them are resolved against the
// child's row like every other expression on those nodes, so an out-of-range
// index in one is the same defect; the shallow walker's omission is recorded as
// a ledger row rather than fixed here, because that walker's callers are
// rewriters and widening a rewriter's reach is not a P5.5-f change.
func enclosingNodeScopeOf(n Node) (enclosingNodeScope, bool) {
	switch x := n.(type) {
	case *Project:
		return enclosingNodeScope{exprs: x.Targets, width: childWidth(x.Child), children: []Node{x.Child}}, true
	case *Filter:
		return enclosingNodeScope{exprs: nonNilExprs(x.Predicate), width: childWidth(x.Child), children: []Node{x.Child}}, true
	case *Sort:
		return enclosingNodeScope{exprs: sortKeyExprs(x.Keys), width: childWidth(x.Child), children: []Node{x.Child}}, true
	case *Limit:
		// Limit/Offset are constants or params in every production plan; they
		// are checked anyway because a resolved `LIMIT (SELECT ...)` lowering
		// could put a same-scope reference there, and a check that costs two
		// nil tests should not be narrowed by an argument about what usually
		// happens.
		return enclosingNodeScope{exprs: nonNilExprs(x.Limit, x.Offset), width: childWidth(x.Child), children: []Node{x.Child}}, true
	case *Distinct:
		return enclosingNodeScope{width: childWidth(x.Child), children: []Node{x.Child}}, true
	case *DistinctOn:
		// KeyCols are plain ints into the node's OWN output schema, not
		// ColumnRefs into the child's row, so they are outside this tripwire's
		// question. They are still bounds-checked, against the right width.
		if bad, ok := firstOutOfRange(x.KeyCols, len(x.Output())); !ok {
			panic(fmt.Sprintf("createPlan: DISTINCT ON key column %d of a %d-column row", bad, len(x.Output())))
		}
		return enclosingNodeScope{width: childWidth(x.Child), children: []Node{x.Child}}, true
	case *OrdinalityWrap:
		return enclosingNodeScope{width: childWidth(x.Child), children: []Node{x.Child}}, true
	case *Aggregate:
		exprs := append([]Expr(nil), x.GroupExprs...)
		exprs = append(exprs, x.Passthrough...)
		for i := range x.Aggs {
			a := &x.Aggs[i]
			exprs = append(exprs, nonNilExprs(a.Arg, a.Arg2, a.Filter)...)
			exprs = append(exprs, a.ExtraArgs...)
			exprs = append(exprs, sortKeyExprs(a.OrderBy)...)
			exprs = append(exprs, sortKeyExprs(a.WithinGroupOrderBy)...)
		}
		return enclosingNodeScope{exprs: exprs, width: childWidth(x.Child), children: []Node{x.Child}}, true
	case *WindowAgg:
		exprs := append([]Expr(nil), x.PartitionBy...)
		exprs = append(exprs, sortKeyExprs(x.OrderBy)...)
		for i := range x.Funcs {
			f := &x.Funcs[i]
			exprs = append(exprs, f.Args...)
			exprs = append(exprs, nonNilExprs(f.Filter)...)
		}
		if x.Frame != nil {
			exprs = append(exprs, nonNilExprs(x.Frame.StartOffset, x.Frame.EndOffset)...)
		}
		return enclosingNodeScope{exprs: exprs, width: childWidth(x.Child), children: []Node{x.Child}}, true
	case *Join:
		exprs := nonNilExprs(x.Predicate, x.LeftKey, x.RightKey)
		for _, kp := range x.HashKeys {
			exprs = append(exprs, nonNilExprs(kp.Left, kp.Right)...)
		}
		// The MERGED row — see the header. Semi/Anti publish Left only and
		// still evaluate the predicate against Left ++ Right.
		return enclosingNodeScope{
			exprs:    exprs,
			width:    childWidth(x.Left) + childWidth(x.Right),
			children: []Node{x.Left, x.Right},
		}, true
	case *NestedLoopIndexJoin:
		return enclosingNodeScope{
			exprs: nonNilExprs(x.Predicate),
			width: childWidth(x.Outer) + childWidth(x.Inner),
			// Outer only — the inner index probe's keys live in the outer's
			// coordinate space (header).
			children: []Node{x.Outer},
		}, true
	}
	return enclosingNodeScope{}, false
}

// childWidth is the column count a node publishes, with a nil child reported as
// zero rather than crashing: a malformed tree is the caller's bug to report with
// its own message, not this walk's to panic on with a nil dereference.
func childWidth(n Node) int {
	if n == nil {
		return 0
	}
	return len(n.Output())
}

// nonNilExprs collects the non-nil members of an optional-expression field set.
// Every node above has several `Expr` fields that are nil in the common case,
// and `assertColumnRefsWithinSchema` reports positions by index — passing nils
// through would make its diagnostics count phantom expressions.
func nonNilExprs(es ...Expr) []Expr {
	out := make([]Expr, 0, len(es))
	for _, e := range es {
		if e != nil {
			out = append(out, e)
		}
	}
	return out
}

// sortKeyExprs projects a sort-key list onto its expressions.
func sortKeyExprs(keys []SortKey) []Expr {
	out := make([]Expr, 0, len(keys))
	for _, k := range keys {
		if k.Expr != nil {
			out = append(out, k.Expr)
		}
	}
	return out
}

// firstOutOfRange reports the first member of idx outside [0, width).
func firstOutOfRange(idx []int, width int) (int, bool) {
	for _, i := range idx {
		if i < 0 || i >= width {
			return i, false
		}
	}
	return 0, true
}

// enclosingWalk is what one tripwire run observed. It is returned rather than
// kept internal so the tests can prove the walk was not vacuous in the way the
// panic cannot: `checkedNodes` says the tripwire had something to check, and
// `stoppedAt` says where a future node kind would need teaching.
type enclosingWalk struct {
	checkedNodes  int
	searchedRoots int
	stoppedAt     []string
}

// walkEnclosingTree runs the tripwire over root and returns what it saw.
//
// A searched subtree is an opaque leaf: its internal coordinates are the
// SEARCH's, published through the boundary, and its own `createPlan` arms
// already checked them against the layout they built (`boundaryMap`'s hole /
// out-of-range / duplicate checks, plus `assertSearchedTreeNeedsNoReconcile`).
// Descending into it would check the right indices against the wrong question.
func walkEnclosingTree(what string, root Node) enclosingWalk {
	var w enclosingWalk
	var walk func(Node)
	walk = func(n Node) {
		if n == nil {
			return
		}
		if isSearchedTree(n) {
			w.searchedRoots++
			return
		}
		sc, ok := enclosingNodeScopeOf(n)
		if !ok {
			w.stoppedAt = append(w.stoppedAt, fmt.Sprintf("%T", n))
			return
		}
		if len(sc.exprs) > 0 {
			assertColumnRefsWithinSchema(fmt.Sprintf("%s %T", what, n), sc.exprs, sc.width)
		}
		w.checkedNodes++
		for _, c := range sc.children {
			walk(c)
		}
	}
	walk(root)
	return w
}

// assertEnclosingTreeColumnRefs is 03 §10's tripwire at the width the section
// asked for: every `ColumnRef` above the search root must resolve within its
// input schema, checked at PLAN time.
//
// It panics in two cases, and the second is the one the header argues for:
//
//   - a reference outside its input row — the M0097-0058 class, reported by
//     `assertColumnRefsWithinSchema` with the offending node kind in `what`;
//   - the walk never reached a searched subtree, which means it checked the
//     wrong tree or stopped before the interesting one and its silence proves
//     nothing.
func assertEnclosingTreeColumnRefs(what string, root Node) {
	w := walkEnclosingTree(what, root)
	if w.searchedRoots == 0 {
		panic(fmt.Sprintf("createPlan: the %s tripwire never reached a searched subtree (checked %d node(s), stopped at %v); it proves nothing about the tree it was asked to guard — teach enclosingNodeScopeOf about the node kind on the path",
			what, w.checkedNodes, w.stoppedAt))
	}
}

// splicedSearchedRoot returns the searched subtree at or immediately below n,
// looking through the retained `*Filter`s the DP block can leave above a spliced
// join tree, and nil when n did not come from the search.
//
// It looks through Filters and nothing else on purpose. The question at the
// `predp.go` call site is "did the node I just spliced under the pinned spine
// come out of the PG-shaped search?", and the DP block's own two outcomes are
// exactly "the searched root" and "a Filter holding the conjuncts the search did
// not consume, over the searched root". Anything else is a shape this function
// has no opinion about, and answering nil there sends the caller down the legacy
// re-resolution it would have run anyway.
func splicedSearchedRoot(n Node) Node {
	for n != nil {
		if isSearchedTree(n) {
			return n
		}
		f, ok := n.(*Filter)
		if !ok {
			return nil
		}
		n = f.Child
	}
	return nil
}

// assertSpineConsumesIdentityBoundaryMap is the `predp.go` half: it PROVES the
// claim 03 §10 makes about the pinned spine instead of leaving it argued.
//
// The claim: at the search root, canonical relid order and pre-search binding
// order are the same sequence (P5.5-f-i), so the boundary republishes the join
// subtree's columns in exactly the order the spine was resolved against, and the
// spine's re-resolution has nothing to consume. In `predp.go` that shows up as
// `layoutPosMap(old, new) == nil`.
//
// Checking `pm == nil` directly would NOT prove it, and that is the reason this
// function compares the schemas itself: `layoutPosMap` returns nil for two
// different reasons — "identical, nothing to remap" and "widths differ, refuse
// to remap rather than corrupt". A boundary that lost or gained a column would
// take the second door and be indistinguishable from success, while the enclosing
// tree went on referencing columns that had moved. That is precisely the
// M0097-0058 shape `bindingWidth` is a parameter to prevent, arriving through a
// different door.
func assertSpineConsumesIdentityBoundaryMap(oldSchema, newSchema Schema) {
	if len(oldSchema) != len(newSchema) {
		panic(fmt.Sprintf("createPlan: the searched subtree publishes %d columns where the pinned spine was resolved against %d; the search boundary did not reproduce the binding concatenation",
			len(newSchema), len(oldSchema)))
	}
	for i := range oldSchema {
		if oldSchema[i].Name != newSchema[i].Name || oldSchema[i].SourceTableIdx != newSchema[i].SourceTableIdx {
			panic(fmt.Sprintf("createPlan: the searched subtree publishes %q (source %d) at column %d where the pinned spine expects %q (source %d); the boundary map is not the identity and the spine re-resolution would have had to consume it",
				newSchema[i].Name, newSchema[i].SourceTableIdx, i, oldSchema[i].Name, oldSchema[i].SourceTableIdx))
		}
	}
}
