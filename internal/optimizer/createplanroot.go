package optimizer

// M0127-P5.5-f-i — the search boundary: 03 §10's coordinate map, and the one
// node that makes it invisible to everything above the search root.
//
// PG oracle: `set_plan_references` / `set_upper_references` (setrefs.c:2214) —
// the pass that re-points an upper node's expressions at its subplan's actual
// output. Design: leftdeep-joins 03 §10, 02 §3.
//
// # What was left open
//
// P5.5-c…-e-ii-b built every `createPlan` arm, and P5.5-e-i gave the recursion
// its `outputLayout`: for the node an arm emits, `layout[i]` is the PRE-SEARCH
// BINDING coordinate of output column `i`. That solved the INSIDE-the-tree half
// of the translation — a join re-bases its own quals onto its own merged row.
// It did nothing for the ABOVE-the-tree half. The enclosing tree — the top
// Project's targets, retained Filters, Sort keys, Aggregate arguments, the
// pinned unnest spine — is written in binding coordinates, because that is the
// space `planSelect` resolved it in. The search's chosen tree is a cost-chosen
// reordering. Hand one to the other unchanged and every reference above the
// search root reads whichever column happened to land at that index.
//
// # The choice 03 §10 left to this task, and why it is not a shortcut
//
// §10 offers two variants: rewrite every enclosing expression through the map,
// or emit ONE `Project` at the search root that reorders the final rel's output
// back into binding order. This task takes the second — and the reason is
// stronger than "simpler v1", because of a fact §10 could not state before the
// layout existed:
//
//	At the SEARCH ROOT, §10's canonical RELID order and the pre-search
//	BINDING order are the same sequence.
//
// `buildInitialRels` (joinsearch.go:210) assigns relid `1<<i` to FROM item `i`
// and records `baseOffset = bindings[i].offset`, which ascends with `i`. So
// ordering the final relset's base rels by relid, and concatenating each one's
// columns, reproduces exactly the pre-search concatenation. The root relset is
// the FULL set, so this is not a coincidence of some subsets: publishing
// relid order at the root IS publishing binding order.
//
// That makes the reordering Project the *materialisation* of §10's canonical
// layout rather than a way around it, and it collapses the boundary map to the
// identity for every consumer above: the enclosing tree needs no rewrite, the
// `reconcileNLILayout` / `buildBindingsPosMap` family has nothing left to
// correct (P5.5-f-ii asserts that, and tags the subtree so those passes skip),
// and the map survives in exactly one place — here, as the Project's target
// list.
//
// The cost is one narrow pass-through node, and only when the search actually
// reordered: `createPlanAtSearchRoot` elides the Project when the root's layout
// is already binding order (the leading case — a left-deep tree whose outer
// spine starts at FROM item 0), which is the projection elision §10 anticipated.
//
// # Live since M0127-P5.9 (2026-08-06)
//
// `GOOPG_PGSHAPED_DP` defaults ON and `planSelect` calls the search, so this
// file builds the executor tree production actually runs: plans and rows DO
// move here. `createplanroot_test.go` is no longer its only observer.

import (
	"fmt"
	"sort"
)

// createPlanAtSearchRoot builds the plan for the search's chosen top-level path
// and returns it in PRE-SEARCH BINDING ORDER — the row shape the enclosing tree
// was resolved against.
//
// This is the ONLY `createPlan` entry point the search's caller may use.
// `createPlanNode` returns a node in the search's own (cost-chosen) column
// order, which is correct for a child of another join arm and wrong for anything
// else; the difference between the two is precisely this function.
//
// `bindingWidth` is the width of that pre-search concatenation, and it is passed
// in rather than inferred from the root because the root cannot know it. A FROM
// item that never entered the search produces a root that is simply NARROWER —
// self-consistent, permutation-clean, and missing columns the enclosing tree
// still references. The caller holds the only copy of that number (the schema of
// the join subtree the search replaced), so it is the caller that must state it.
func createPlanAtSearchRoot(p *Path, bindingWidth int) Node {
	return createPlanAtSearchRootRange(p, 0, bindingWidth, nil)
}

// createPlanAtSearchRootRange is `createPlanAtSearchRoot` for a search problem
// that does not start at binding coordinate 0 — a SUB-JOINLIST (M0127-P5.9-a).
//
// The joinlist recursion (`makeRelFromJoinlist`, relfromjoinlist.go) plans a
// pinned sub-problem on its own and hands the result to the enclosing problem as
// ONE initial rel. That sub-problem's rels carry GLOBAL binding coordinates —
// they must, because the clause list it searches is written in the statement's
// one concatenated space — while the row it publishes is only that sub-problem's
// slice of it, `[base, base+width)`. Splitting the window out of `boundaryMap`
// is what lets the same permutation check ("no hole, no duplicate, nothing out
// of range") run on a slice as on the whole: a sub-problem that dropped or
// duplicated a column fails here rather than as a wrong row six nodes above.
//
// The published order is still ascending binding coordinate, so the sub-result's
// columns are exactly the pre-search concatenation restricted to its own range —
// which is what makes it usable as a leaf of the enclosing problem with
// `baseOffset = base` and nothing else to translate.
// `fill` (M0134-0187) licenses a PADDED slot for a binding coordinate a
// narrowed index-only leaf legitimately dropped: it answers with the pruned
// column's SchemaColumn when — and only when — that column is provably outside
// the statement's needed set. The boundary Project then publishes a typed NULL
// at that position, which nothing above can read (the needed set over-states
// by construction). A hole the filler does NOT license still panics: the
// totality assertion stays loud for real producer bugs. nil = no padding.
func createPlanAtSearchRootRange(p *Path, base, width int, fill func(int) (SchemaColumn, bool)) Node {
	if p == nil {
		panic("createPlan: search root has no path")
	}
	if width <= 0 {
		panic(fmt.Sprintf("createPlan: search root asked to reproduce a %d-column binding concatenation", width))
	}
	if base < 0 {
		panic(fmt.Sprintf("createPlan: search root asked to publish binding coordinates from %d", base))
	}
	// Take2 P4-01 Slice 3: derive per-joinrel keep-sets over the chosen tree
	// before the recursion builds nodes. A no-op wherever the sets are
	// unknown (ineligible problems, hand-built test trees): the Slice-2 arms
	// and the NeededCols fallback are untouched.
	deriveJoinKeeps(p)
	n, lay := createPlanNode(p)
	if n == nil {
		panic("createPlan: search root path built no node")
	}
	if lay == nil {
		// Only the C0 bridge's `PathPrebuilt` over an empty `RelOptInfo` can
		// produce this, and that path never reaches the search boundary — it IS
		// the pre-search tree. Reaching here means a searched root was assembled
		// from a leaf whose `baseOffset` was never recorded, and the enclosing
		// tree is about to be handed a row whose column order nobody knows.
		panic("createPlan: search root's column coordinates are unknown; the boundary map cannot be composed")
	}
	if len(lay) != len(n.Output()) {
		panic(fmt.Sprintf("createPlan: search root layout is %d columns but its output is %d", len(lay), len(n.Output())))
	}
	// M0127-P5.5-f-ii-a: the independent cross-check on the arms' coordinate
	// arithmetic, run on the join tree BEFORE the boundary Project goes on top
	// — on the Project itself the claim is false by design (searchedtree.go
	// explains why that is the sharpest argument for the tag).
	assertSearchedTreeNeedsNoReconcile(n)
	m, fills := boundaryMap(lay, base, width, fill)
	if len(fills) == 0 && boundaryMapIsIdentity(m) {
		// The search's order already IS binding order — the common left-deep
		// case. Emitting a Project here would be a pure copy of every row.
		//
		// The tag still goes on: the legacy posmap family must skip this
		// subtree whether or not a Project was needed. In the identity case
		// its map would come out the identity too and the skip would look
		// optional — but only for the shapes where it does, and
		// `reconcileNLILayout`'s name resolution can move a self-join's keys
		// regardless of layout.
		return markSearchedTree(n)
	}
	return markSearchedTree(projectToBindingOrder(n, m, fills))
}

// boundaryMap composes 03 §10's map from the search root's layout: entry `b` is
// the root output column holding binding coordinate `b`. It is the inverse of
// `outputLayout`, and it is a plain slice rather than `bindingIndex`'s map
// because at the root the domain is provably dense — which is the thing this
// function checks.
//
// The check is the point. The root's layout must be a PERMUTATION of
// `[0, bindingWidth)`: the search's final relset spans every FROM item, so the
// columns it publishes are exactly the columns the binding concatenation had.
// Three ways that can fail, all of which otherwise produce a plan that runs:
//
//   - a HOLE — some binding coordinate is absent because a FROM item never
//     entered the search, so a reference above the root indexes past the end of
//     the row (the M0097-0058 out-of-bounds class) or lands on a neighbour that
//     shifted into the gap;
//   - a coordinate OUT OF RANGE — a leaf's `baseOffset` disagrees with the
//     binding concatenation the enclosing tree was resolved against, e.g. a leaf
//     rebuilt at a different width than the one recorded;
//   - a DUPLICATE — one base relation reached the root through two children,
//     which the enumerator's disjointness rule exists to prevent.
//
// All three are producer bugs, and all three are cheaper to fail on here than to
// debug as a wrong row count six gates later.
// `base` is the first binding coordinate the problem covers — 0 for the
// statement's own search, and the sub-problem's first FROM item's offset for a
// sub-joinlist (M0127-P5.9-a). Entry `i` of the returned map is therefore the
// root output column holding binding coordinate `base+i`.
func boundaryMap(lay outputLayout, base, width int, fill func(int) (SchemaColumn, bool)) ([]int, map[int]SchemaColumn) {
	if len(lay) == 0 {
		panic("createPlan: search root publishes no columns")
	}
	m := make([]int, width)
	for i := range m {
		m[i] = -1
	}
	for out, bind := range lay {
		if bind < base || bind >= base+width {
			panic(fmt.Sprintf("createPlan: search root output column %d carries binding coordinate %d, outside the %d-column binding concatenation [%d,%d) it must reproduce",
				out, bind, width, base, base+width))
		}
		if prev := m[bind-base]; prev != -1 {
			panic(fmt.Sprintf("createPlan: search root output columns %d and %d both claim binding coordinate %d; a base relation reached the root through two children",
				prev, out, bind))
		}
		m[bind-base] = out
	}
	var fills map[int]SchemaColumn
	if missing := missingBindingCoords(m, base); len(missing) > 0 {
		// A hole is legal ONLY when the filler licenses it — a pruned
		// index-only column that the statement provably never reads. Any
		// other hole is still the producer bug this panic has always named.
		var unfillable []int
		for _, coord := range missing {
			var col SchemaColumn
			ok := false
			if fill != nil {
				col, ok = fill(coord)
			}
			if !ok {
				unfillable = append(unfillable, coord)
				continue
			}
			if fills == nil {
				fills = make(map[int]SchemaColumn, len(missing))
			}
			fills[coord-base] = col
		}
		if len(unfillable) > 0 {
			panic(fmt.Sprintf("createPlan: search root does not publish binding coordinate(s) %v; the enclosing tree references columns the searched subtree cannot supply",
				unfillable))
		}
	}
	return m, fills
}

// missingBindingCoords lists the holes in a boundary map, for the diagnostic
// above. Separate so the message names every gap rather than the first — a
// whole missing relation shows up as a run, which says "a FROM item never
// entered the search" far more legibly than a single index.
func missingBindingCoords(m []int, base int) []int {
	var missing []int
	for bind, out := range m {
		if out == -1 {
			missing = append(missing, base+bind)
		}
	}
	sort.Ints(missing)
	return missing
}

// boundaryMapIsIdentity reports whether the search left the columns where the
// bindings put them, in which case no reordering node is needed.
func boundaryMapIsIdentity(m []int) bool {
	for bind, out := range m {
		if bind != out {
			return false
		}
	}
	return true
}

// projectToBindingOrder emits the reordering node: one `*Project` whose target
// `b` is a bare `ColumnRef` at the root output column holding binding
// coordinate `b`.
//
// Every target is a pass-through `ColumnRef`, never a computed expression, so
// the node adds a slice permutation per row and nothing else. The schema is
// permuted from the child's by the SAME map in the same loop — the invariant
// "schema[b] describes Targets[b]" is structural here rather than asserted
// afterwards, the discipline `joinInputsFor` established for the merged row.
//
// `SourceTableIdx` is carried across because it is the identity self-join
// disambiguation rides on (`findColumnIndexByNameAndSource`, `predRebind`):
// dropping it here would make Q21's three `lineitem` aliases indistinguishable
// to every pass above the search root.
func projectToBindingOrder(child Node, m []int, fills map[int]SchemaColumn) Node {
	in := child.Output()
	targets := make([]Expr, len(m))
	out := make(Schema, len(m))
	for bind, col := range m {
		if col < 0 {
			// A licensed hole (M0134-0187): the coordinate belongs to a
			// column a narrowed index-only leaf pruned, and the filler has
			// proven the statement never reads it. The slot keeps the pruned
			// column's name and type — every positional consumer above stays
			// aligned — and its VALUE is a typed NULL nothing dereferences.
			c, ok := fills[bind]
			if !ok {
				panic(fmt.Sprintf("createPlan: boundary hole at output %d has no licensed fill", bind))
			}
			targets[bind] = &NullConst{pos: child.Pos()}
			out[bind] = c
			continue
		}
		c := in[col]
		targets[bind] = &ColumnRef{
			pos:            child.Pos(),
			Index:          col,
			Name:           c.Name,
			Type:           c.Type,
			SourceTableIdx: c.SourceTableIdx,
		}
		out[bind] = c
	}
	p := &Project{pos: child.Pos(), Child: child, Targets: targets, schema: out}
	// 03 §10's debug tripwire, applied to the node that is by construction the
	// last chance to catch the class: everything above the root indexes into
	// THIS schema.
	assertColumnRefsWithinSchema("search-boundary projection", targets, len(in))
	return p
}

// assertSearchedBoundariesIntact re-checks every searched subtree's boundary
// projection against the FINISHED plan — after `planSelect`'s rewriters, after
// the legacy remap family, after `Plan()`'s own tail passes.
//
// # Why the check had to move here (M0127-P5.9-c)
//
// `boundaryMap`'s three refusals (hole / out of range / duplicate) guard the
// map's PRODUCER, and P5.9 run 1 proved that is only half the exposure. The
// layout the arms published was correct and `boundaryMap` was right to accept
// it; the corruption happened AFTERWARDS, when `remapTopProjection` (bushy.go)
// walked past the boundary `*Project` — the one legacy descent that steps over
// a `*Project` unconditionally — derived a binding→plan-position map from the
// searched join INSIDE the subtree, and applied it to the boundary's own target
// list as if it were a reference into the map rather than the map itself. The
// result composed two permutations and returned every column's value one
// relation-block from its name (`select * from customer, orders where
// o_custkey = c_custkey and o_orderkey = 1`).
//
// No producer-side check can see that, and strengthening `boundaryMap` from a
// permutation test to a per-leaf identity test — the shape P5.9 run 1's
// write-up proposed — would not have either: it runs before the pass that did
// the damage. The invariant that DOES catch it is a consumer-side one, and it
// is available because `projectToBindingOrder` builds the node so that it holds
// by construction:
//
//	target[i] is a bare ColumnRef naming the very column it addresses —
//	`child.Output()[target[i].Index]` — and the node's own `schema[i]`
//	is that same column.
//
// A permutation applied to the indices breaks the correspondence with the names
// immediately, because the names do not move with them. That is the M0097-0058
// class stated as a property of the node instead of as a property of the map.
//
// # Scope and cost
//
// Gated on the flag: with `GOOPG_PGSHAPED_DP` off no tree carries the tag and
// this walk returns on its first line, so the default arm pays one boolean. It
// descends THROUGH a searched root rather than stopping at it, because a pinned
// sub-joinlist publishes its own boundary inside the enclosing one
// (`createPlanAtSearchRootRange`, relfromjoinlist.go) and each is a separate
// map with the same obligation.
//
// It abstains on an unnamed target, for `assertSearchedTreeNeedsNoReconcile`'s
// reason: name-based evidence says nothing where there is no name. Production
// targets come from resolved leaf schemas and carry one.
func assertSearchedBoundariesIntact(root Node) {
	if !pgShapedDPEnabled() || root == nil {
		return
	}
	var walk func(Node)
	walk = func(n Node) {
		if n == nil {
			return
		}
		if p, isProj := n.(*Project); isProj && isSearchedTree(p) {
			assertBoundaryProjectionIntact(p)
		}
		for _, c := range boundaryWalkChildren(n) {
			walk(c)
		}
	}
	walk(root)
}

// boundaryWalkChildren enumerates the children the walk above descends into.
//
// It is a `Node` switch rather than a reuse of `enclosingNodeScopeOf`
// (enclosingtree.go) because the two walks answer different questions: that one
// asks "is every reference above the search root in range", which is meaningless
// once the coordinate space is unknown, so it STOPS at a node kind it does not
// recognise. This one only needs to reach the boundary node, so an unrecognised
// kind costs nothing to pass over — but it cannot pass over what it cannot
// enumerate, so an unknown kind is still where the walk ends.
//
// The listed set is every kind that can sit between a statement's root and a
// spliced searched subtree: `planSelect`'s upper stack, the pinned semi/anti
// spine, the MHJ packer's node (searched trees are never packed, but a
// non-searched sibling can be), the set-operation arms, and the DML wrappers
// whose source is a SELECT plan.
func boundaryWalkChildren(n Node) []Node {
	switch x := n.(type) {
	case *Project:
		return []Node{x.Child}
	case *Filter:
		return []Node{x.Child}
	case *Sort:
		return []Node{x.Child}
	case *Limit:
		return []Node{x.Child}
	case *Distinct:
		return []Node{x.Child}
	case *DistinctOn:
		return []Node{x.Child}
	case *OrdinalityWrap:
		return []Node{x.Child}
	case *LockRows:
		return []Node{x.Child}
	case *Aggregate:
		return []Node{x.Child}
	case *WindowAgg:
		return []Node{x.Child}
	case *Memoize:
		return []Node{x.Child}
	case *Join:
		return []Node{x.Left, x.Right}
	case *SetOp:
		return []Node{x.Left, x.Right}
	case *NestedLoopIndexJoin:
		return []Node{x.Outer, x.Inner}
	case *Update:
		return []Node{x.Child}
	case *Delete:
		return []Node{x.Child}
	case *Insert:
		return []Node{x.Source}
	}
	return nil
}

// assertBoundaryProjectionIntact is the per-node half of the check above.
func assertBoundaryProjectionIntact(p *Project) {
	if p.Child == nil {
		panic("createPlan: search-boundary projection lost its child")
	}
	in := p.Child.Output()
	out := p.Output()
	for i, tg := range p.Targets {
		// A `*NullConst` target is a LICENSED boundary hole (M0134-0187): a
		// pruned index-only column the statement provably never reads,
		// published as a typed NULL to keep positions aligned. It is the ONE
		// non-ColumnRef shape `projectToBindingOrder` emits; anything else
		// still means a pass rebuilt the map as an expression.
		if _, isPad := tg.(*NullConst); isPad {
			continue
		}
		cr, isCol := tg.(*ColumnRef)
		if !isCol {
			panic(fmt.Sprintf("createPlan: search-boundary projection target %d is a %T; every target of the boundary map is a pass-through ColumnRef, so a pass rebuilt the map as an expression",
				i, tg))
		}
		if cr.Index < 0 || cr.Index >= len(in) {
			panic(fmt.Sprintf("createPlan: search-boundary projection target %d (%s) addresses column %d of a %d-column child",
				i, cr.Name, cr.Index, len(in)))
		}
		if cr.Name == "" {
			continue
		}
		if got := in[cr.Index]; got.Name != cr.Name || got.SourceTableIdx != cr.SourceTableIdx {
			panic(fmt.Sprintf("createPlan: search-boundary projection target %d says %q (source %d) but addresses child column %d, which is %q (source %d); a pass after the boundary permuted the coordinate map itself — see remapTopProjection's searched-subtree guard (bushy.go)",
				i, cr.Name, cr.SourceTableIdx, cr.Index, got.Name, got.SourceTableIdx))
		}
		if i < len(out) && out[i].Name != cr.Name {
			panic(fmt.Sprintf("createPlan: search-boundary projection publishes %q at output column %d but its target there is %q; the schema and the target list were permuted apart",
				out[i].Name, i, cr.Name))
		}
	}
}

// assertColumnRefsWithinSchema is 03 §10's plan-time tripwire: every
// `ColumnRef` in `exprs` must address a column that exists in a row `width`
// columns wide.
//
// §10 asks for it because the M0097-0058 class — an index-skew bug from the
// `buildBindingsPosMap` family this work replaces — surfaced as an
// out-of-bounds slice access DURING EXECUTION, i.e. as a query that had already
// been accepted, costed and started. Failing at plan time turns that into a
// loud, attributable planner bug.
//
// It deliberately does NOT descend into inner scopes (`scopeIgnore`): a
// subquery's ColumnRefs index into that subquery's own row, and checking them
// against this width would be checking the wrong schema — the same boundary
// `translateToLayout` and `relidsOfExpr` draw, for the same reason (rule #2).
//
// Exported to the package rather than inlined because P5.5-f-ii runs it over
// the whole enclosing tree once the search is spliced in; here it guards the one
// node this file builds.
func assertColumnRefsWithinSchema(what string, exprs []Expr, width int) {
	for i, e := range exprs {
		var bad *ColumnRef
		ok := walkExprRefs(e, scopeIgnore, exprVisitor{
			Visit: func(n Expr) bool {
				if cr, isCol := n.(*ColumnRef); isCol && bad == nil {
					if cr.Index < 0 || cr.Index >= width {
						bad = cr
					}
				}
				return true
			},
		})
		if !ok {
			panic(fmt.Sprintf("createPlan: %s expression %d (%T) contains a node the walker does not enumerate; teach exprChildSlots about it", what, i, e))
		}
		if bad != nil {
			panic(fmt.Sprintf("createPlan: %s expression %d references column %d (%s) of a %d-column row",
				what, i, bad.Index, bad.Name, width))
		}
	}
}
