package planner

// M0127-P5.5-e-i — the coordinate carrier every join arm needs, and the first
// join arm: `create_hashjoin_plan` (createplan.c:4633).
//
// PG oracle: `create_hashjoin_plan` (createplan.c:4633) over `create_join_plan`
// (createplan.c:1073), and — the part that matters far more here —
// `set_join_references` (setrefs.c:2557), the pass that rewrites a join's quals
// from *query-wide* Var coordinates into the join's own OUTER_VAR/INNER_VAR
// coordinates. Design: leftdeep-joins 03 §5.1, §5.4, §10.
//
// # The problem this file exists to solve
//
// The scan and sort arms (P5.5-c/-d) could re-emit their nodes without ever
// asking where a column IS: a scan's schema is its leaf's, and a sort neither
// adds nor moves a column. A join is the first arm that MERGES two schemas, and
// with that the coordinate question becomes unavoidable:
//
//   - Every `restrictInfo.clause` the search reasons about is written in
//     PRE-SEARCH ("binding") coordinates — the concatenation of every FROM
//     item's schema in syntactic order. That is not incidental: `relidsOfExpr`
//     (joinrestrict.go:357) DECIDES a clause's relset by bucketing each
//     `ColumnRef.Index` against exactly those offsets, so the coordinate space
//     and the clause's own notion of which relations it touches are the same
//     fact.
//   - The tree `createPlan` emits is a cost-chosen reordering. A join's output
//     is `outer ++ inner`, and which subset is outer was decided by cost, not by
//     syntax.
//
// So a join arm that copied `ri.clause` across unchanged would key on whichever
// column happened to land at that index in the new layout — a wrong answer that
// still runs, which is this project's most expensive failure mode. The
// translation is therefore performed here, per node, and it is performed against
// a layout the recursion CARRIES rather than one re-derived per clause.
//
// # `outputLayout` — what the recursion carries
//
// `outputLayout[i]` is the binding coordinate of the emitted node's output
// column `i`. It is the inverse of the map 03 §10 asks for, and it is built the
// only way that cannot drift from the tree: a base rel's layout is the range
// `RelOptInfo.baseOffset` recorded (joinsearch.go), a sort passes its child's
// through unchanged, and a join concatenates its two children's — the same
// concatenation that produced the schema, in the same statement.
//
// This is deliberately NOT yet 03 §10's canonical relid-order layout. That
// choice ("every joinrel's output columns are in relid order of its relset")
// governs what the SEARCH ROOT publishes to the enclosing tree, and it is
// P5.5-f's to make: whichever way it goes — rewriting the enclosing expressions
// or emitting one reordering `Project` at the root — the input it needs is the
// root's `outputLayout`, which is what this recursion now produces. Nothing here
// pre-empts that decision; it supplies the map it will be composed from.
//
// # What this slice does NOT do
//
// The merge and nested-loop arms are P5.5-e-ii. They reuse every mechanism here
// (the recursion, the layout, `translateToLayout`) and add their own: the merge
// arm's sorted-child handling and ORDERED key list, and the nested-loop arms'
// parameter-binding contract with a parameterised inner index path (the
// P5.4b-ii-b-2 Memoize/binding contract). Splitting there keeps the coordinate
// machinery reviewable on its own, on the one arm that exercises it most
// simply.
//
// M0127-P5.5-e-ii-a added `createMergeJoinPlan` below. It reuses this file's
// prologue whole — the recursion, the concatenated layout, the key orientation —
// which is why that prologue was lifted into `joinInputsFor` / `keyPairs` in the
// same commit rather than copied: two join arms that build the merged row by two
// separate pieces of code drift, and the drift is a wrong-column join that still
// runs.
//
// Live since M0127-P5.9 (2026-08-06): `GOOPG_PGSHAPED_DP` defaults ON and
// `planSelect` calls the search, so this arm builds production join trees and
// a wrong-column join here returns wrong rows. `createplanjoin_test.go` is no
// longer its only observer.

import (
	"fmt"

	"github.com/goopg/goopg/internal/parser"
)

// outputLayout maps an emitted node's output columns back to the pre-search
// binding coordinates the search's clauses are written in: `layout[i]` is the
// binding index of output column `i`.
//
// A nil layout means "these coordinates are not known", which is a real and
// legitimate state, not an error: the C0 bridge (`createPlanFromDPChoice`)
// wraps an already-built DP subtree in a `PathPrebuilt` over an empty
// `RelOptInfo`, and that subtree's columns were placed by the integer DP's own
// layout machinery, which this map has no relationship to. It is only a fault
// when a JOIN arm meets it, because a join is the first thing that must
// translate a clause — hence the panic there rather than here.
type outputLayout []int

// bindingIndex inverts the layout: binding coordinate -> output column. Built
// per node rather than cached on it, because it is consumed once (by the arm
// that just built the layout) and a join problem's node count is small.
//
// A duplicated binding coordinate would make the inverse ambiguous and is a
// producer bug — it means the same base relation reached this join through both
// children, which the enumerator's disjointness rule (`joinsearchlevel.go`)
// exists to prevent.
func (l outputLayout) bindingIndex() map[int]int {
	m := make(map[int]int, len(l))
	for out, bind := range l {
		if prev, dup := m[bind]; dup {
			panic(fmt.Sprintf("createPlan: output columns %d and %d both claim binding coordinate %d; a base relation reached one join through both children",
				prev, out, bind))
		}
		m[bind] = out
	}
	return m
}

// baseRelLayout is a level-1 rel's layout: the contiguous binding range its leaf
// occupied, `[baseOffset, baseOffset+width)`.
//
// `width` is taken from the EMITTED node, not from the recorded leaf, because
// the two arms that rebuild a leaf may legitimately emit a different node class
// for it (an `*IndexScan` demoted to a `*SeqScan`, or the reverse) and the
// layout must describe what was actually built. They keep the leaf's schema, so
// in practice the widths agree — and when they do not, the disagreement means a
// synthesised schema slipped in and every clause over this rel is about to be
// mistranslated, which is why it is checked rather than assumed.
func baseRelLayout(rel *RelOptInfo, n Node) outputLayout {
	if rel == nil || rel.baseLeaf == nil {
		// Not a level-1 rel of a real search: no recorded offset, so no
		// coordinates. See outputLayout's doc for why this is not an error.
		return nil
	}
	width := len(n.Output())
	if leafWidth := len(rel.baseLeaf.Output()); leafWidth != width {
		panic(fmt.Sprintf("createPlan: rebuilt leaf for relset %#04x is %d columns wide but the recorded leaf is %d; the schema was synthesised, not carried",
			uint16(rel.Relids), width, leafWidth))
	}
	lay := make(outputLayout, width)
	for i := range lay {
		lay[i] = rel.baseOffset + i
	}
	return lay
}

// translateToLayout returns a NEW expression with every `ColumnRef.Index`
// rewritten from the binding coordinate it was written in into the position that
// column occupies in `lay`. The original is left untouched — the search still
// holds these clauses, and several passes match planner expressions by POINTER
// identity, so rewriting in place would corrupt state the caller still owns.
//
// This is `set_join_references` (setrefs.c:2557) at goopg's fidelity: PG
// rewrites a join qual's Vars into OUTER_VAR/INNER_VAR pairs because its
// executor addresses the two input slots separately; goopg's executor evaluates
// a join predicate against ONE merged `outer ++ inner` row, so the same job is a
// single renumbering into merged coordinates.
//
// Two boundaries are drawn deliberately:
//
//   - Inner plans are stepped over (`scopeIgnore`), NOT descended into. A
//     subquery's ColumnRefs live in that subquery's own coordinate space and
//     renumbering them here would be nonsense. This matches `relidsOfExpr`
//     exactly, and it has to: that function decided this clause's relset under
//     the same policy, so a translation with a different notion of "a reference
//     belonging to this scope" would be translating a different clause than the
//     one that was placed (rule #2 — sibling paths must agree).
//   - An `*OuterColumnRef` or a `*CTIDExpr` at this level is refused. Neither
//     is positional, so both would survive the rewrite unchanged and silently
//     mean something else: an OuterColumnRef is a correlation into a scope the
//     flat merged row cannot supply, and a CTIDExpr is injected by the SCAN into
//     its own row's slot (`MaterializedSlot.hasCTID`), so hoisting it onto the
//     join re-points it at whichever side the merged row starts with. The same
//     two are refused by `cloneExprShiftIdx` (nl_index_join.go:802-811) for the
//     same reasons.
//
// `what` names the thing being translated ("join clause", "sort key") purely so
// a panic says which producer to look at; every caller is a `createPlan` arm.
func translateToLayout(what string, e Expr, lay outputLayout, index map[int]int) Expr {
	if e == nil {
		return nil
	}
	var refused Expr
	out, ok := cloneExprRefs(e, scopeIgnore, exprRewriter{
		Rewrite: func(n Expr) Expr {
			switch x := n.(type) {
			case *ColumnRef:
				local, found := index[x.Index]
				if !found {
					// The clause references a column that is not in this
					// node's output. Either the qual was placed at a join
					// that cannot evaluate it, or the coordinates it was
					// written in are not the ones recorded — both produce a
					// plan that reads the wrong column rather than failing.
					if refused == nil {
						refused = x
					}
					return n
				}
				x.Index = local
			case *OuterColumnRef, *CTIDExpr:
				if refused == nil {
					refused = n
				}
			}
			return n
		},
	})
	if !ok {
		panic(fmt.Sprintf("createPlan: %s %T contains an expression the walker does not enumerate; teach exprChildSlots about it", what, e))
	}
	if refused != nil {
		if cr, isCol := refused.(*ColumnRef); isCol {
			panic(fmt.Sprintf("createPlan: %s references binding column %d (%s), which is not among the %d output columns it is being re-based onto",
				what, cr.Index, cr.Name, len(lay)))
		}
		panic(fmt.Sprintf("createPlan: %s carries a %T, which is not positional and cannot be re-based", what, refused))
	}
	return out
}

// joinInputs is the result of the prologue every join arm runs: both children
// emitted, their schemas and layouts concatenated, and the inverse index the
// clause translation needs.
//
// It exists as one function rather than as a paragraph repeated per arm because
// the merged row is built ONCE here, schema and layout in the same statement.
// Two arms each concatenating their own would be two chances to concatenate them
// in different orders, and the resulting plan runs — reading the wrong columns.
type joinInputs struct {
	outer, inner Node
	// outerRelids / innerRelids are the CHILD PATHS' relsets, used to orient
	// each key clause; they come from the paths as the arm received them (a
	// `PathSort` carries its subpath's rel, so absorbing one is relid-neutral).
	outerRelids, innerRelids RelSet
	merged                   Schema
	lay                      outputLayout
	index                    map[int]int
}

// joinInputsFor runs that prologue. `outerPath` / `innerPath` are passed
// explicitly rather than read from `p.Children` because the merge arm absorbs a
// child `PathSort` before recursing (see `absorbMergeSort`), and the arm that
// decides what its children ARE is the one that must hand them over.
//
// Every refusal is a producer bug, per createplan.go's contract, and each names
// the wrong answer it prevents: a child that built no node cannot be joined, and
// a child whose coordinates are unknown cannot have this join's quals re-based
// onto it — the case `outputLayout`'s doc calls legitimate everywhere except
// here.
func joinInputsFor(p *Path, kind string, outerPath, innerPath *Path) joinInputs {
	outerNode, outerLay := createPlanNode(outerPath)
	innerNode, innerLay := createPlanNode(innerPath)
	if outerNode == nil || innerNode == nil {
		panic(fmt.Sprintf("createPlan: %s over a child path that built no node", kind))
	}
	if outerLay == nil || innerLay == nil {
		panic(fmt.Sprintf("createPlan: %s over a child whose column coordinates are unknown; its quals cannot be re-based (outer known: %t, inner known: %t)",
			kind, outerLay != nil, innerLay != nil))
	}

	outerSchema, innerSchema := outerNode.Output(), innerNode.Output()
	if len(outerLay) != len(outerSchema) || len(innerLay) != len(innerSchema) {
		panic(fmt.Sprintf("createPlan: %s child layouts (%d, %d) disagree with child schemas (%d, %d)",
			kind, len(outerLay), len(innerLay), len(outerSchema), len(innerSchema)))
	}
	merged := make(Schema, len(outerSchema)+len(innerSchema))
	copy(merged, outerSchema)
	copy(merged[len(outerSchema):], innerSchema)
	// The layout is concatenated in the SAME statement as the schema, from the
	// same two children in the same order — the invariant "layout[i] describes
	// merged[i]" is structural here rather than asserted later.
	lay := make(outputLayout, 0, len(merged))
	lay = append(lay, outerLay...)
	lay = append(lay, innerLay...)

	outerRelids := childRelids(outerPath)
	innerRelids := childRelids(innerPath)
	if want := outerRelids | innerRelids; p.Rel != nil && p.Rel.Relids != want {
		panic(fmt.Sprintf("createPlan: %s over relset %#04x whose children span %#04x", kind, uint16(p.Rel.Relids), uint16(want)))
	}

	return joinInputs{
		outer:       outerNode,
		inner:       innerNode,
		outerRelids: outerRelids,
		innerRelids: innerRelids,
		merged:      merged,
		lay:         lay,
		index:       lay.bindingIndex(),
	}
}

// keyPairs orients each key clause outer-on-the-left and translates both
// operands onto the merged row, preserving the order it was given.
//
// The order is load-bearing for the MERGE arm and merely conventional for the
// hash arm: goopg's merge executor sorts both sides by the key TUPLE in
// `HashKeys` order (`mergeSideKeyExprs`, join_merge_key.go), so this list IS
// `outersortkeys`/`innersortkeys`. `sortInnerAndOuter` builds its clause list by
// concatenating the key groups in the chosen pathkey order for exactly that
// reason, and this function must not re-sort it.
//
// Orientation is recomputed here rather than read off the clause because a
// `restrictInfo` records `left`/`right` as the expression writer wrote them, and
// which side is the outer was decided by COST at this pair — the same reason
// `mergeKeyGroups` recomputes it (joinpathsmerge.go).
//
//   - a key clause that is not an equijoin has no two-sided operand split, so
//     there is nothing to key on and the "key" would be a whole predicate;
//   - a key whose operands do not land one per side is
//     `clause_sides_match_join`'s (joinpath.c:2205) refusal: keying on it would
//     compare two columns of the same input.
func (in joinInputs) keyPairs(kind string, keys []*restrictInfo) []JoinKeyPair {
	pairs := make([]JoinKeyPair, 0, len(keys))
	for i, ri := range keys {
		if ri == nil || !ri.isEquijoin {
			panic(fmt.Sprintf("createPlan: %s key %d is not an equijoin; it has no two-sided operand split to key on", kind, i))
		}
		outerKey, innerKey := ri.leftKey, ri.rightKey
		switch {
		case relsSubset(ri.leftRelids, in.outerRelids) && relsSubset(ri.rightRelids, in.innerRelids):
			// Already oriented outer-on-the-left.
		case relsSubset(ri.rightRelids, in.outerRelids) && relsSubset(ri.leftRelids, in.innerRelids):
			outerKey, innerKey = ri.rightKey, ri.leftKey
		default:
			panic(fmt.Sprintf("createPlan: %s key %d splits %#04x/%#04x, which does not match the join's %#04x/%#04x sides",
				kind, i, uint16(ri.leftRelids), uint16(ri.rightRelids), uint16(in.outerRelids), uint16(in.innerRelids)))
		}
		pairs = append(pairs, JoinKeyPair{
			Left:  translateToLayout("join clause", outerKey, in.lay, in.index),
			Right: translateToLayout("join clause", innerKey, in.lay, in.index),
		})
	}
	return pairs
}

// joinPredicate folds every key equality and every residual clause into the one
// `Join.Predicate` goopg's executor evaluates.
//
// Folding the KEYS in is not redundancy. goopg's hash executor hashes on one
// pair and re-checks `Predicate` per matched pair, so a key omitted from the
// predicate is a key enforced only by the hash — the multi-equality wrong-answer
// case (Q9) that `buildJoinFromDP` (bushy.go:1417) was fixed for. It is also
// what keeps the published key list stable: `fillJoinHashKeys` REBUILDS
// `Join.HashKeys` from `Predicate` at the tail of `Plan()`
// (join_hash_keys.go:193), in conjunct order, so a key that is not a conjunct
// would be dropped from the list this arm just published — and for a merge join
// that list is the sort order itself.
func (in joinInputs) joinPredicate(kind string, pairs []JoinKeyPair, residual []*restrictInfo) Expr {
	conjuncts := make([]Expr, 0, len(pairs)+len(residual))
	for _, kp := range pairs {
		conjuncts = append(conjuncts, &BinaryOp{pos: kp.Left.Pos(), Op: parser.OpEq, Left: kp.Left, Right: kp.Right})
	}
	for i, ri := range residual {
		if ri == nil || ri.clause == nil {
			panic(fmt.Sprintf("createPlan: %s residual %d has no clause", kind, i))
		}
		conjuncts = append(conjuncts, translateToLayout("join clause", ri.clause, in.lay, in.index))
	}
	return combineAnd(conjuncts)
}

// createHashJoinPlan is `create_hashjoin_plan` (createplan.c:4633): recurse into
// both children, merge their schemas, and translate this join's keys and
// residual quals onto the merged row.
//
// Child order is the path model's, not the executor's: `Children[0]` is the
// OUTER (probe) side and `Children[1]` the INNER (build) side, the one
// convention all three join arms share (`joinpathsmerge.go:374-378`). goopg's
// `*Join` builds on the RIGHT input by default, so `Left`/`Right` take outer/
// inner respectively and `BuildLeft` is never set — and that is not a
// simplification. `generateHashJoinPaths` (pathgen.go:130) adds BOTH
// orientations as two separate paths and lets `add_path` keep the cheaper, so
// the build-side decision was already made, by cost, in the child order this arm
// receives. Setting `BuildLeft` here would be a second, later, uncosted opinion
// about the same question — precisely the "name-tag" rule (`IsSmallDimensionSide`
// pinning, bushy.go:1396-1402) that 06 §2.1 retires.
//
// The predicate carries EVERY key equality as well as the residual, matching the
// existing DP constructor (`buildJoinFromDP`, bushy.go:1417 plus
// `attachExtraEdgesLocal`): goopg's executor hashes on one pair and evaluates
// `Predicate` on each matched pair, so a key omitted from the predicate is a
// key enforced only by the hash — which is exactly the multi-equality
// wrong-answer case (Q9) that constructor was fixed for.
//
// Preconditions panic, per createplan.go's contract, each naming the wrong
// answer it prevents:
//
//   - a hash join with no key hashes nothing: `chooseInnerJoinAlgo`'s caller
//     would have to fall back to a nested loop, and building this as a `*Join`
//     with `JoinAlgoHash` and a nil `LeftKey` produces a node the executor
//     cannot open;
//   - a parameterised hash path is undischargeable — a hash join propagates a
//     child's `RequiredOuter` rather than binding it
//     (`calcNonNestloopRequiredOuter`, pathparam.go:78) — and no producer builds
//     one today (both children come from `CheapestTotal`, which `setCheapest`
//     takes from the unparameterised paths); reaching it means a producer
//     learned to parameterise a hash join without teaching this arm the binding
//     contract.
//
// The two key-shape refusals are `keyPairs`'.
func createHashJoinPlan(p *Path) (Node, outputLayout) {
	if len(p.Children) != 2 {
		panic(fmt.Sprintf("createPlan: PathHashJoin with %d children, want exactly 2", len(p.Children)))
	}
	if len(p.HashKeys) == 0 {
		panic("createPlan: PathHashJoin with no hash keys; a hash join keys on nothing only as a nested loop")
	}
	if p.RequiredOuter != 0 {
		panic(fmt.Sprintf("createPlan: parameterised PathHashJoin over relset %#04x; a hash join propagates a parameter rather than binding it",
			uint16(p.Rel.Relids)))
	}

	in := joinInputsFor(p, "PathHashJoin", p.Children[0], p.Children[1])
	pairs := in.keyPairs("PathHashJoin", p.HashKeys)

	j := &Join{
		pos:  in.outer.Pos(),
		Type: JoinTypeInner,
		Algo: JoinAlgoHash,
		// Outer drives the probe, inner is hashed — see the doc comment for
		// why BuildLeft stays false rather than being re-decided here.
		Left:      in.outer,
		Right:     in.inner,
		Predicate: in.joinPredicate("PathHashJoin", pairs, p.Residual),
		// `HashKeys[0] IS (LeftKey, RightKey), by pointer` (plan.go:840) — the
		// single-pair view and the list view must not be able to disagree, so
		// the pair is shared rather than rebuilt.
		LeftKey:  pairs[0].Left,
		RightKey: pairs[0].Right,
		HashKeys: pairs,
		schema:   in.merged,
	}
	return j, in.lay
}

// createMergeJoinPlan is `create_mergejoin_plan` (createplan.c:4444)
// (M0127-P5.5-e-ii-a): the second join arm, and the one place where goopg's
// executor and PG's disagree about what a merge join IS.
//
// # The ordered key list
//
// Everything about a merge join is the ORDER of `p.HashKeys`. `sortInnerAndOuter`
// (joinpathsmerge.go) builds that list by concatenating the merge key GROUPS in
// the pathkey order it chose for this candidate, and goopg's merge executor sorts
// each side by the key tuple in exactly that order (`mergeSideKeyExprs`,
// join_merge_key.go, feeding `mergeSortedSource.less`). So the list this arm
// publishes is `outersortkeys`/`innersortkeys`, not merely a set of clauses that
// happen to be equalities — which is why `keyPairs` preserves the order it is
// given and why the keys must also become `Predicate` conjuncts in that same
// order (`fillJoinHashKeys` rebuilds the published list from `Predicate`, so a
// re-ordering there re-orders the SORT).
//
// A group serving several clauses contributes several pairs with EC-equivalent
// outer operands (`a.x = c.x AND b.x = c.x` at outer `{a,b}` is one pathkey and
// two clauses), so the emitted tuple can be longer than `outersortkeys`. That is
// PG's shape too — `find_mergeclauses_for_outer_pathkeys` (pathkeys.c:1670) is
// explicit that one outer pathkey may carry clauses with different inner ECs —
// and sorting by `(a.x, b.x)` when `a.x = b.x` already holds inside the outer is
// the same order as sorting by `a.x`.
//
// # The sort children are ABSORBED, not emitted
//
// PG's `MergePath` carries `outersortkeys`/`innersortkeys` and this arm
// MATERIALISES a `Sort` node, because PG's `nodeMergejoin` requires sorted
// inputs and cannot produce them. goopg's `JoinAlgoMerge` operator sorts BOTH
// inputs itself, unconditionally, into work_mem-bounded runs (`openMergeJoin`,
// operators_join_agg.go:315) — it is a Sort⋈Sort in one node. goopg's path model
// nevertheless makes the sorts explicit `PathSort` children (`sortPathFor`), so
// that `addPath` can compare a candidate that needs a sort against one that does
// not.
//
// Emitting those children as `*Sort` nodes would therefore sort each side TWICE:
// once in the node the path names, once inside the join that ignores it. That is
// not a faithful translation, it is a doubled cost the path was never charged —
// `tryMergeJoinPath` prices exactly one sort per side. So the arm absorbs them:
// a child `PathSort` is stepped over and ITS child is emitted, which reproduces
// the costed plan exactly. `absorbMergeSort` states the one property that has to
// hold for the step-over to be ordering-neutral.
//
// Preconditions, each naming the wrong answer it prevents:
//
//   - a merge join with no merge clause has nothing to merge ON: the executor
//     would fall through `initMergeKeys`' empty-list guard to a nil single pair
//     and error at Open. PG refuses earlier (`sort_inner_and_outer` returns when
//     `mergeclause_list` is NIL, joinpath.c:1372);
//   - a parameterised merge path is undischargeable for the same reason the hash
//     one is — `calc_non_nestloop_required_outer` (:1071) propagates rather than
//     binds — and `tryMergeJoinPath` already refuses to build one;
//   - a result ordering that is not ascending/nulls-last is a LIE about the
//     emitted node. goopg's merge comparator is fixed ascending with NULL-keyed
//     rows last (`mergeSortedSource.less`, join_merge_stream.go:280), so a path
//     claiming a descending ordering — which only P5.4c-ii's ordered index paths
//     can produce — would have a merge one level up skip a sort it still needs.
//     Ledgered against that slice.
func createMergeJoinPlan(p *Path) (Node, outputLayout) {
	if len(p.Children) != 2 {
		panic(fmt.Sprintf("createPlan: PathMergeJoin with %d children, want exactly 2", len(p.Children)))
	}
	if len(p.HashKeys) == 0 {
		panic("createPlan: PathMergeJoin with no merge clauses; a merge join has no ordering to merge on")
	}
	if p.RequiredOuter != 0 {
		panic(fmt.Sprintf("createPlan: parameterised PathMergeJoin over relset %#04x; a merge join propagates a parameter rather than binding it",
			uint16(p.Rel.Relids)))
	}
	for i, pk := range p.Pathkeys {
		if !pk.SortAsc || pk.NullsFirst {
			panic(fmt.Sprintf("createPlan: PathMergeJoin result pathkey %d is %s; goopg's merge operator emits ascending, nulls last, so this ordering would not be delivered",
				i, describePathKeyOrder(pk)))
		}
	}

	in := joinInputsFor(p, "PathMergeJoin",
		absorbMergeSort(p.Children[0], "outer"),
		absorbMergeSort(p.Children[1], "inner"))
	pairs := in.keyPairs("PathMergeJoin", p.HashKeys)

	j := &Join{
		pos:  in.outer.Pos(),
		Type: JoinTypeInner,
		Algo: JoinAlgoMerge,
		// Same child convention as the hash arm: Children[0] is the outer
		// (streaming left) side. A merge join has no build side, so BuildLeft
		// is meaningless here and stays false.
		Left:      in.outer,
		Right:     in.inner,
		Predicate: in.joinPredicate("PathMergeJoin", pairs, p.Residual),
		LeftKey:   pairs[0].Left,
		RightKey:  pairs[0].Right,
		HashKeys:  pairs,
		schema:    in.merged,
	}
	return j, in.lay
}

// absorbMergeSort steps over a merge child's explicit `PathSort`, returning the
// path whose node should actually be emitted. See `createMergeJoinPlan`'s doc for
// why the sort is redundant.
//
// The step-over is ordering-neutral only because the join re-imposes an ordering
// on this side itself, so the one thing checked is that the absorbed sort is not
// asking for something the join will not deliver: goopg's merge comparator is
// ascending, NULL-keyed rows last. A descending `PathSort` under a merge join
// means the producer expected the sort to survive — it would not — and the
// resulting stream would be ordered the other way with nothing to notice.
//
// `sortPathFor` is the only `PathSort` producer today and it builds its keys from
// `mergeKeyGroups`, which are ascending/nulls-last by construction, so this
// refusal is unreachable now and is a guard for P5.4c-ii's ordered inputs.
func absorbMergeSort(child *Path, side string) *Path {
	if child == nil || child.Kind != PathSort {
		return child
	}
	if len(child.Children) != 1 || child.Children[0] == nil {
		panic(fmt.Sprintf("createPlan: PathMergeJoin %s child is a PathSort with %d children, want exactly 1", side, len(child.Children)))
	}
	for i, pk := range child.Pathkeys {
		if !pk.SortAsc || pk.NullsFirst {
			panic(fmt.Sprintf("createPlan: PathMergeJoin %s sort key %d is %s, but the merge operator re-sorts ascending, nulls last; the requested ordering would be discarded",
				side, i, describePathKeyOrder(pk)))
		}
	}
	return child.Children[0]
}

// describePathKeyOrder names a pathkey's direction for a panic message.
func describePathKeyOrder(pk PathKey) string {
	dir, nulls := "ascending", "nulls last"
	if !pk.SortAsc {
		dir = "descending"
	}
	if pk.NullsFirst {
		nulls = "nulls first"
	}
	return dir + ", " + nulls
}

// childRelids is a join child's relset. A child path always has a rel — every
// producer sets `Rel` — but the sort arm's child is a `PathSort` over the rel
// rather than the rel's own path, and both carry the same `Rel`, so no
// unwrapping is needed.
func childRelids(p *Path) RelSet {
	if p == nil || p.Rel == nil {
		return 0
	}
	return p.Rel.Relids
}
