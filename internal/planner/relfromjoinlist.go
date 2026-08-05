package planner

// M0127-P5.9-a — `make_rel_from_joinlist`: the joinlist's CONSUMER, and the one
// place a whole search problem is run end to end.
//
// PG oracle: `make_rel_from_joinlist` (allpaths.c:3352-3422). Design:
// leftdeep-joins 03 §6.2 (what a joinlist item means to the search), 03 §1
// (the search protocol), 03 §10 (the boundary this file composes recursively).
// Producer side: `deconstructJointree` (collapse.go, M0127-P5.8).
//
// # What was missing
//
// P5.8 landed the joinlist and P5.1-P5.6 landed everything the search needs to
// run on ONE flat problem, but the two had never met: `resolveContext.joinlist`
// had no reader, and the three-step protocol (`buildInitialRels` ->
// `addBaseRelIndexPaths` -> `joinSearch`) existed only as a sequence each test
// re-assembled by hand. Upstream keeps both facts in one function, and so does
// this file: `makeRelFromJoinlist` walks the joinlist, `searchOneProblem` runs
// the protocol, and the recursion between them is the only thing that decides
// how many searches a statement gets.
//
// # A sub-joinlist is planned, then handed over as ONE relation
//
// PG's recursion returns a `RelOptInfo` whose whole PATHLIST stays available to
// the enclosing problem. goopg's returns a NODE: the sub-problem's cheapest
// tree, republished in binding order by the search boundary
// (`createPlanAtSearchRootRange`), which the enclosing problem then admits as
// one `PathPrebuilt` initial rel — the same door a FROM-subquery leaf comes
// through (03 §2). Two consequences, both deliberate:
//
//   - the enclosing search can no longer pick a DIFFERENTLY-SORTED path for the
//     sub-problem (upstream could take a merge-friendly one), and the
//     sub-problem is priced for "produce all rows" because it does not know
//     which side of the enclosing join it will land on. Ledgered, not papered
//     over: keeping the pathlist alive across the boundary needs the level
//     lists to be indexed by JOINLIST ITEM rather than by base-relid popcount
//     (`addRel`, joinsearch.go:166), which is a representation change, not a
//     wiring one;
//   - the coordinate story stays trivial. A sub-problem covers a CONTIGUOUS run
//     of FROM items (deconstruction never reorders), so its published row is
//     exactly the pre-search concatenation restricted to `[base, base+width)`,
//     and the enclosing problem treats it as a leaf at `baseOffset = base` with
//     nothing to translate. `makeRelFromJoinlist` CHECKS that contiguity per
//     item rather than trusting it, because everything above depends on it.
//
// # Which clauses each problem sees
//
// Each problem builds its own clause list from the statement's conjuncts with
// its OWN `cumOffsets` — one entry per joinlist ITEM, not per FROM item
// (`searchOneProblem`). That single substitution does all the placement work,
// because `relidsOfExpr` (joinrestrict.go:334) already answers "which items does
// this expression touch" and declines the expression outright when a column
// falls outside the window:
//
//   - a clause between two leaves INSIDE one item collapses to a single bit,
//     `relLevel < 2`, and is dropped — it was already placed by the sub-problem
//     that owns those leaves;
//   - a clause reaching OUT of a sub-problem's window is declined there and
//     placed by the enclosing problem, where both of its ends are items.
//
// So no clause is placed twice and none is lost, without a placement pass.
//
// # Still inert
//
// `GOOPG_PGSHAPED_DP` is OFF and nothing in `planSelect` calls this yet — the
// production seam (`tryBushyDP` / `runJoinSearchBelowPinned`) is P5.9-b, which
// must also decide which conjuncts the search consumed before the residual
// `Filter` above it is rebuilt. Validated in isolation by
// `relfromjoinlist_test.go`.

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
)

// joinlistProblem is everything one statement's joinlist recursion reads. It is
// a value the caller assembles once and every level of the recursion shares,
// which is what keeps the FROM-item indices a joinlist leaf carries meaningful
// at any depth: they are subscripts into THESE slices, whatever sub-problem is
// being planned.
type joinlistProblem struct {
	// bindings / scans / relInfos are the three parallel per-FROM-item slices
	// the pre-search pipeline already assembles (bushy.go:184-196) — the same
	// trio `buildInitialRels` takes, in the same order a joinlist leaf's `rel`
	// indexes (03 §6.1's leaf-numbering guarantee).
	bindings []rangeBinding
	scans    []Node
	relInfos []baseRelInfo

	// conjuncts is the statement's join-predicate conjunct list, in binding
	// coordinates. Every problem filters it for itself; see the file header.
	conjuncts []Expr

	// cumOffsets[i] is FROM item i's first binding coordinate, with a
	// terminating entry for the total width — the coordinate space
	// `relidsOfExpr` and `baseOffset` are both written in.
	cumOffsets []int

	cp  costParams
	cat catalog.Catalog

	// tupleFraction is the query's `root->tuple_fraction`, applied to the
	// TOP-level problem only (see makeRelFromJoinlist).
	tupleFraction float64
}

// joinlistRel is what a joinlist item resolves to: goopg's stand-in for the
// `RelOptInfo *` upstream's recursion returns.
//
// It carries the `rangeBinding` and `baseRelInfo` the enclosing problem must
// hand `buildInitialRels` for this item, so that a leaf item and a searched
// sub-problem are interchangeable at the call site — the property that makes the
// recursion one code path instead of two.
type joinlistRel struct {
	// node is the plan tree for this item: the pre-search leaf for a leaf item,
	// the searched sub-tree (in binding order) for a sub-joinlist.
	node Node
	// binding is what the enclosing `buildInitialRels` reads — its `offset`,
	// which becomes the rel's `baseOffset`.
	binding rangeBinding
	// info is the item's `baseRelInfo`. A searched sub-problem's has a nil
	// `table`, which is what makes every base-relation-only producer
	// (`addParameterizedIndexPaths`, the sizer's uniqueness proofs) decline it.
	info baseRelInfo
	// lo, hi is the half-open range of FROM items this rel covers. Contiguity
	// is an invariant, checked where it is established.
	lo, hi int
}

// planJoinlistSearch is the entry point: plan `jl` — a whole statement's
// joinlist — and return the plan tree for it, in pre-search binding order.
//
// `jl` must cover every FROM item exactly once and in order, which is what
// `deconstructJointree` produces by construction; a joinlist that does not is a
// producer bug and is reported rather than planned around.
func planJoinlistSearch(jl joinlist, prob *joinlistProblem) (Node, error) {
	if err := validateJoinlistProblem(jl, prob); err != nil {
		return nil, err
	}
	r, err := makeRelFromJoinlist(jl, prob, prob.tupleFraction)
	if err != nil {
		return nil, err
	}
	return r.node, nil
}

// validateJoinlistProblem checks the caller's inputs against each other. Every
// condition here is a precondition of the coordinate arithmetic below, and each
// one produces, if violated, a plan that runs and returns the wrong columns —
// the class this milestone exists to remove from the planner.
func validateJoinlistProblem(jl joinlist, prob *joinlistProblem) error {
	if prob == nil {
		return fmt.Errorf("join search: no joinlist problem")
	}
	n := len(prob.bindings)
	if n == 0 {
		return fmt.Errorf("join search: empty FROM list")
	}
	if len(prob.scans) != n || len(prob.relInfos) != n {
		return fmt.Errorf("join search: %d bindings but %d scans and %d rel infos",
			n, len(prob.scans), len(prob.relInfos))
	}
	if len(prob.cumOffsets) != n+1 {
		return fmt.Errorf("join search: %d FROM items need %d cumulative offsets, got %d",
			n, n+1, len(prob.cumOffsets))
	}
	for i := 1; i < len(prob.cumOffsets); i++ {
		if prob.cumOffsets[i] <= prob.cumOffsets[i-1] {
			return fmt.Errorf("join search: cumulative offsets are not ascending at item %d (%d after %d)",
				i, prob.cumOffsets[i], prob.cumOffsets[i-1])
		}
	}
	// The joinlist must be a partition of the FROM items into contiguous runs,
	// in order. `leafRange` checks each item; this checks the whole.
	lo, hi, ok := jl.leafRange()
	if !ok || lo != 0 || hi != n {
		return fmt.Errorf("join search: joinlist leaves %v do not cover FROM items [0,%d)",
			jl.leaves(nil), n)
	}
	return nil
}

// leafRange is the half-open FROM-item range a joinlist covers, and whether its
// leaves are exactly that range in ascending order.
//
// Contiguity is not decoration: a sub-problem publishes ONE row slice at ONE
// `baseOffset`, so an item whose leaves interleave with another's could not be
// described to the enclosing problem at all. `deconstructJointree` cannot
// produce such an item (it walks the FROM clause once, in order, and only ever
// wraps adjacent members), so this is a guard on the producer, not a case to
// handle.
func (jl joinlist) leafRange() (lo, hi int, ok bool) {
	leaves := jl.leaves(nil)
	if len(leaves) == 0 {
		return 0, 0, false
	}
	lo = leaves[0]
	for i, rel := range leaves {
		if rel != lo+i {
			return 0, 0, false
		}
	}
	return lo, lo + len(leaves), true
}

// makeRelFromJoinlist is `make_rel_from_joinlist` (allpaths.c:3352): resolve
// every item to a rel — recursing on sub-joinlists — and then, unless there is
// only one, search the orders in which they can be joined.
//
// `tupleFraction` is the query's fraction at the top level and 0 in every
// recursive call, and the asymmetry is forced by the collapse this boundary
// performs (file header): upstream can afford `consider_startup` on a
// sub-problem's rel because the enclosing search still gets to choose among its
// paths, while goopg must commit to one here. Committing to a startup-optimised
// path for a sub-problem whose rows the enclosing join then consumes in full is
// the expensive direction of that bet, so sub-problems are priced for all rows.
func makeRelFromJoinlist(jl joinlist, prob *joinlistProblem, tupleFraction float64) (joinlistRel, error) {
	if len(jl) == 0 {
		return joinlistRel{}, fmt.Errorf("join search: empty joinlist")
	}
	items := make([]joinlistRel, len(jl))
	for i, it := range jl {
		var (
			r   joinlistRel
			err error
		)
		switch {
		case it.isLeaf():
			r, err = prob.leafRel(it.rel)
		case it.pinnedOuter():
			// M0127-P5.9-s: the search builds INNER joins, so planning a pinned
			// outer subproblem would emit an inner join where the statement wrote
			// an outer one and drop its unmatched rows. Upstream cannot reach
			// this: its joinlist member IS the `JoinExpr`, and
			// `make_rel_from_joinlist` hands a pinned one to
			// `make_join_rel`/`join_is_legal`, which honour `jointype`.
			//
			// Refusing here is a decline of the whole statement, not of this
			// item — `planJoinlistSearch`'s error makes the seam fall back to
			// the syntactic tree (03 §4.2), which still carries the outer join
			// on its own node. The seam peels an outer SPINE off before it gets
			// here (`splitOuterSpine`, joinsearchseam.go), so what reaches this
			// arm is an outer join the peel could not lift out: one below an
			// inner link, or on a non-first comma FROM item.
			return joinlistRel{}, fmt.Errorf(
				"join search: joinlist item %d is a pinned %s join, which the search cannot rebuild",
				i, joinTypeName(it.jointype))
		default:
			r, err = makeRelFromJoinlist(it.sub, prob, 0)
		}
		if err != nil {
			return joinlistRel{}, err
		}
		if i > 0 && r.lo != items[i-1].hi {
			return joinlistRel{}, fmt.Errorf(
				"join search: joinlist item %d covers FROM items [%d,%d), which does not follow [%d,%d)",
				i, r.lo, r.hi, items[i-1].lo, items[i-1].hi)
		}
		items[i] = r
	}
	if len(items) == 1 {
		// "Single joinlist node, so we're done" (allpaths.c:3399-3404). This is
		// also why the pinned shape `[[l, r]]` costs nothing: the enclosing
		// FromExpr's one-item joinlist unwraps to its subproblem's rel.
		return items[0], nil
	}
	return prob.searchOneProblem(items, tupleFraction)
}

// leafRel resolves a joinlist leaf to the rel the pre-search pipeline already
// built for that FROM item.
func (prob *joinlistProblem) leafRel(rel int) (joinlistRel, error) {
	if rel < 0 || rel >= len(prob.bindings) {
		return joinlistRel{}, fmt.Errorf("join search: joinlist leaf %d is not a FROM item of this statement", rel)
	}
	if prob.scans[rel] == nil {
		return joinlistRel{}, fmt.Errorf("join search: FROM item %d has no leaf node", rel)
	}
	return joinlistRel{
		node:    prob.scans[rel],
		binding: prob.bindings[rel],
		info:    prob.relInfos[rel],
		lo:      rel,
		hi:      rel + 1,
	}, nil
}

// searchOneProblem runs the three-step search protocol over a list of items and
// returns the chosen tree as one rel.
//
// The protocol order is upstream's and is not interchangeable: the clause list
// must exist before `addBaseRelIndexPaths`, because that is the list an index
// path is parameterised BY (`create_index_paths` after `deconstruct_jointree`,
// allpaths.c:191), and every initial rel must already carry its cheapest slots
// before `joinSearch` compares anything.
func (prob *joinlistProblem) searchOneProblem(items []joinlistRel, tupleFraction float64) (joinlistRel, error) {
	lo, hi := items[0].lo, items[len(items)-1].hi
	base := prob.cumOffsets[lo]
	width := prob.cumOffsets[hi] - base

	bindings := make([]rangeBinding, len(items))
	scans := make([]Node, len(items))
	infos := make([]baseRelInfo, len(items))
	// One entry per ITEM: the coordinate space this problem's clause list is
	// resolved in (file header). The terminating entry is the problem's own end,
	// so a column outside `[base, base+width)` resolves to no item and its
	// clause is declined here — which is how a clause reaching out of a
	// sub-problem ends up placed by the enclosing one.
	cum := make([]int, len(items)+1)
	for i, it := range items {
		bindings[i], scans[i], infos[i] = it.binding, it.node, it.info
		cum[i] = prob.cumOffsets[it.lo]
	}
	cum[len(items)] = prob.cumOffsets[hi]

	s, err := buildInitialRels(bindings, scans, infos, prob.cp, tupleFraction)
	if err != nil {
		return joinlistRel{}, err
	}
	// `addParameterizedIndexPaths` reads `s.clauses`, and `joinSearch` sets it
	// — so the list is published here, before the producers that consume it,
	// and handed to `joinSearch` as well rather than left implicit.
	s.clauses = buildRestrictInfos(prob.conjuncts, 0, cum)
	s.addBaseRelIndexPaths(prob.cat)
	if _, err := s.joinSearch(s.clauses, newJoinRelBuilder(s, prob.cat)); err != nil {
		return joinlistRel{}, err
	}
	p, err := s.finalPath()
	if err != nil {
		return joinlistRel{}, err
	}
	return joinlistRel{
		node: createPlanAtSearchRootRange(p, base, width),
		// The searched tree enters the enclosing problem as a leaf at the first
		// coordinate it publishes. Nothing else of the sub-problem crosses:
		// `info` is deliberately table-less so that every producer that assumes
		// a base relation declines it, and its cardinality is re-derived from
		// the built node by `initialRelRows` — the pathlist-and-rows collapse
		// this boundary performs (file header; ledgered).
		binding: rangeBinding{offset: base},
		info:    baseRelInfo{bindingIdx: -1, sourceIdx: -1},
		lo:      lo,
		hi:      hi,
	}, nil
}
