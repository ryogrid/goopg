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
// Still inert: `GOOPG_PGSHAPED_DP` is OFF and nothing calls the search from
// `planSelect`, so no plan and no row can move. Validated in isolation by
// `createplanjoin_test.go`.

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
func translateToLayout(e Expr, lay outputLayout, index map[int]int) Expr {
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
		panic(fmt.Sprintf("createPlan: join clause %T contains an expression the walker does not enumerate; teach exprChildSlots about it", e))
	}
	if refused != nil {
		if cr, isCol := refused.(*ColumnRef); isCol {
			panic(fmt.Sprintf("createPlan: join clause references binding column %d (%s), which is not among this join's %d output columns",
				cr.Index, cr.Name, len(lay)))
		}
		panic(fmt.Sprintf("createPlan: join clause carries a %T, which is not positional and cannot be re-based onto a merged join row", refused))
	}
	return out
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
//     contract;
//   - a key clause that is not an equijoin has no two-sided operand split, so
//     there is nothing to hash on and the "key" would be a whole predicate;
//   - a key whose operands do not land one per side is
//     `clause_sides_match_join`'s (joinpath.c:2205) refusal: hashing it would
//     compare two columns of the same input.
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

	outerNode, outerLay := createPlanNode(p.Children[0])
	innerNode, innerLay := createPlanNode(p.Children[1])
	if outerNode == nil || innerNode == nil {
		panic("createPlan: PathHashJoin over a child path that built no node")
	}
	if outerLay == nil || innerLay == nil {
		panic(fmt.Sprintf("createPlan: PathHashJoin over a child whose column coordinates are unknown; its quals cannot be re-based (outer known: %t, inner known: %t)",
			outerLay != nil, innerLay != nil))
	}

	outerSchema, innerSchema := outerNode.Output(), innerNode.Output()
	if len(outerLay) != len(outerSchema) || len(innerLay) != len(innerSchema) {
		panic(fmt.Sprintf("createPlan: PathHashJoin child layouts (%d, %d) disagree with child schemas (%d, %d)",
			len(outerLay), len(innerLay), len(outerSchema), len(innerSchema)))
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
	index := lay.bindingIndex()

	outerRelids := childRelids(p.Children[0])
	innerRelids := childRelids(p.Children[1])
	if want := outerRelids | innerRelids; p.Rel != nil && p.Rel.Relids != want {
		panic(fmt.Sprintf("createPlan: PathHashJoin over relset %#04x whose children span %#04x", uint16(p.Rel.Relids), uint16(want)))
	}

	pairs := make([]JoinKeyPair, 0, len(p.HashKeys))
	conjuncts := make([]Expr, 0, len(p.HashKeys)+len(p.Residual))
	for i, ri := range p.HashKeys {
		if ri == nil || !ri.isEquijoin {
			panic(fmt.Sprintf("createPlan: PathHashJoin key %d is not an equijoin; it has no two-sided operand split to hash on", i))
		}
		outerKey, innerKey := ri.leftKey, ri.rightKey
		switch {
		case relsSubset(ri.leftRelids, outerRelids) && relsSubset(ri.rightRelids, innerRelids):
			// Already oriented outer-on-the-left.
		case relsSubset(ri.rightRelids, outerRelids) && relsSubset(ri.leftRelids, innerRelids):
			outerKey, innerKey = ri.rightKey, ri.leftKey
		default:
			panic(fmt.Sprintf("createPlan: PathHashJoin key %d splits %#04x/%#04x, which does not match the join's %#04x/%#04x sides",
				i, uint16(ri.leftRelids), uint16(ri.rightRelids), uint16(outerRelids), uint16(innerRelids)))
		}
		l := translateToLayout(outerKey, lay, index)
		r := translateToLayout(innerKey, lay, index)
		pairs = append(pairs, JoinKeyPair{Left: l, Right: r})
		conjuncts = append(conjuncts, &BinaryOp{pos: l.Pos(), Op: parser.OpEq, Left: l, Right: r})
	}
	for i, ri := range p.Residual {
		if ri == nil || ri.clause == nil {
			panic(fmt.Sprintf("createPlan: PathHashJoin residual %d has no clause", i))
		}
		conjuncts = append(conjuncts, translateToLayout(ri.clause, lay, index))
	}

	j := &Join{
		pos:  outerNode.Pos(),
		Type: JoinTypeInner,
		Algo: JoinAlgoHash,
		// Outer drives the probe, inner is hashed — see the doc comment for
		// why BuildLeft stays false rather than being re-decided here.
		Left:      outerNode,
		Right:     innerNode,
		Predicate: combineAnd(conjuncts),
		// `HashKeys[0] IS (LeftKey, RightKey), by pointer` (plan.go:840) — the
		// single-pair view and the list view must not be able to disagree, so
		// the pair is shared rather than rebuilt.
		LeftKey:  pairs[0].Left,
		RightKey: pairs[0].Right,
		HashKeys: pairs,
		schema:   merged,
	}
	return j, lay
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
