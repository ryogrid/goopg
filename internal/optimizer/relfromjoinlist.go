package optimizer

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
// # Live since M0127-P5.9 (2026-08-06)
//
// The production seam this header was waiting on landed: P5.9-b wired
// `tryBushyDP` / `runJoinSearchBelowPinned` → `tryPGShapedJoinSearch` →
// `planJoinlistSearch`, and `GOOPG_PGSHAPED_DP` defaults ON. This recursion
// runs on real statements; `relfromjoinlist_test.go` is no longer its only
// observer.

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
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

	// joinInfoList is root->join_info_list: every SpecialJoinInfo for the
	// statement, in bottom-up order. Carried by the top-level problem so
	// `buildInitialRels` can store it on the search context. nil for
	// inner-join-only FROM clauses. M0128-P1.2.
	joinInfoList []*SpecialJoinInfo

	// tupleFraction is the query's `root->tuple_fraction`, applied to the
	// TOP-level problem only (see makeRelFromJoinlist).
	tupleFraction float64

	// neededCols / neededColsKnown carry the statement's needed-column set to
	// `searchCtx` (pathindexonlyneed.go) and to the boundary hole-filler.
	// Every sub-problem of one statement shares it: the set is a property of
	// the STATEMENT, not of a FROM subset.
	neededCols      map[string]bool
	neededColsKnown bool

	// outputCols / outputColsKnown carry the statement's above-tree
	// needed-column set (outputColumnNames) to `searchCtx`. Take2 P4-01
	// Slice 3: the union needed above the scan/join tree from which
	// per-joinrel keep-sets derive.
	outputCols      map[string]bool
	outputColsKnown bool

	// queryPathkeys carries `PlannerInfo.query_pathkeys` (C-07/P3-06,
	// querypathkeys.go) to `searchCtx`. It is a property of the STATEMENT,
	// so every sub-problem of one statement shares it — the same rule
	// neededCols follows.
	queryPathkeys []PathKey

	// spineAbove reports that a pinned outer spine sits above this
	// problem's tree: the spine's ON quals read the searched subtree's
	// output from above, so parent-aware narrowing is declined here (the
	// Slice-2 arms still run). Set by the seam, which owns the spine.
	spineAbove bool

	// pinAbove reports that a pinned semi/anti spine sits above this
	// problem's tree (runJoinSearchBelowPinned): same decline, same
	// reason. Carried on the resolve context the seam reads.
	pinAbove bool

	// corrAbove reports that this problem's statement reads an outer query
	// level (a current-scope OuterColumnRef in its WHERE or searched ON
	// quals): post-hoc machinery above the tree reads body-local columns no
	// statement-level collector can see — the unnest rewrite's decorrelated
	// group keys and probe keys. Parent-aware narrowing is declined here
	// (the Slice-2 arms still run). Set by the seam, which owns the
	// resolved predicate; subquery interiors are stepped over, so only the
	// body's own correlation declines it. Take2 P4-01 Slice 3.
	corrAbove bool
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
	// M0134-0188: a ONE-leaf problem short-circuits inside makeRelFromJoinlist
	// ("single joinlist node, so we're done") and comes back as the RAW leaf —
	// correct for a nested sub-list unwrap, where the ENCLOSING problem owns
	// the leaf's paths (including its parameterised ones), but wrong at the
	// problem's own entry: this is the whole search for the statement, no
	// enclosing problem exists, and returning the raw leaf silently skips
	// base-rel path generation — the seam then reports a searched prefix whose
	// access method nobody ever examined. TPC-H Q13's `customer LEFT JOIN
	// orders` peels to exactly this: a one-leaf prefix whose covering
	// `customer_pk` scan can only be chosen by `addBaseRelIndexPaths` +
	// `add_path`. Run the one-relation search protocol here, at the entry
	// only: `joinSearch` with nrels=1 enumerates nothing and `finalPath`
	// returns the base rel's cheapest — seq, index, or index-only, on cost.
	// Gated on `scanLeafFor` — the same consumer-side rebuildability test
	// every path producer applies — because the boundary must tag whatever
	// `createPlan` returns and `markSearchedTree` accepts only the kinds that
	// embed the tag. TPC-DS Q77's prefix leaf is a `*CTEScan`: rebuildable by
	// nobody, taggable by nothing, and running the one-relation protocol on it
	// panicked at plan time. Declining leaves it exactly as the short-circuit
	// always left it.
	if _, _, rebuildable := scanLeafFor(r.node); rebuildable && r.info.table != nil && jl.nrels() == 1 {
		r, err = prob.searchOneProblem([]joinlistRel{r}, prob.tupleFraction)
		if err != nil {
			return nil, err
		}
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
		case it.pinnedUnsearchable():
			// M0127-P5.9-s: the search builds INNER joins for the types it has
			// no jointype-aware producer for, so planning such a pinned outer
			// subproblem would emit an inner join where the statement wrote an
			// outer one and drop its unmatched rows. C-04a took LEFT out of
			// that set — see `pinnedUnsearchable`. Upstream cannot reach
			// this: its joinlist member IS the `JoinExpr`, and
			// `make_rel_from_joinlist` hands a pinned one to
			// `make_join_rel`/`join_is_legal`, which honour `jointype`.
			//
			// Refusing here is a decline of the whole statement, not of this
			// item — `planJoinlistSearch`'s error makes the seam fall back to
			// the syntactic tree (03 §4.2), which still carries the outer join
			// on its own node. C-04c admitted the last positional shapes the
			// peel could not lift out (below an inner link, non-first comma
			// item), so what reaches this arm is a join type the search has no
			// producer for — FULL — rather than a LEFT/RIGHT link in an
			// awkward place.
			return joinlistRel{}, fmt.Errorf(
				"join search: joinlist item %d is a pinned %s join, which the search cannot rebuild",
				i, joinTypeName(it.jointype))
		default:
			// C-04b, and it closes a hole C-04a opened: a pinned LEFT/RIGHT
			// item — the `GOOPG_PGSHAPED_COLLAPSE=0` regime, or a link over
			// a FULL pin — is now handed to the search as a two-item
			// problem, and the search knows it is an OUTER join only through
			// `root->join_info_list`. The seam checks that list for the
			// links it flattens (`outerLinksHaveSJInfos`); this is the same
			// check for the links it did not, and it is FAIL-CLOSED for the
			// same reason: with the SpecialJoinInfo missing, every pairing
			// looks inner and the plan drops the unmatched rows. The
			// production producer attaches the pointer it also accumulates
			// (`deconstructFromItemScoped`), so this can fire only on a
			// joinlist built by hand.
			if it.pinnedOuter() && !joinInfoListHas(prob.joinInfoList, it.sjinfo) {
				return joinlistRel{}, fmt.Errorf(
					"join search: joinlist item %d is a pinned %s join with no SpecialJoinInfo in root->join_info_list; searched, it would be planned as an inner join",
					i, joinTypeName(it.jointype))
			}
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

// joinInfoListHas reports whether `sj` is a member of `list` by identity — the
// accumulation and the pinned item carry the SAME pointer
// (`deconstructFromItemScoped`), which is what
// TestJoinInfoListProvenanceMatchesJoinlistWalk pins.
func joinInfoListHas(list []*SpecialJoinInfo, sj *SpecialJoinInfo) bool {
	if sj == nil {
		return false
	}
	for _, x := range list {
		if x == sj {
			return true
		}
	}
	return false
}

// sjInfosInItemSpace rewrites every SpecialJoinInfo hand from statement-leaf
// coordinates into the coordinates of ONE search problem — PG has no such step
// because its joinrels are always base-relid sets, so a sub-joinlist's rel
// simply carries the union of its leaves; goopg's search numbers a problem's
// ITEMS `1<<i` and a sub-problem is one opaque item, so a leaf-space hand has
// to be re-expressed before `joinIsLegal`, `joinOrderRestricted`,
// `hasJoinRestriction`, `sizeJoinRel` and `paramSourceRelsForProblem` read it.
//
// The rule, per hand: item i is a member iff the hand overlaps the leaf range
// `[items[i].lo, items[i].hi)`. For a single-leaf run starting at leaf 0 this
// is the identity, which is why every C-03/C-04a fixture passed without it.
// A hand that touches an item only partially (an outer join's `MinLefthand`
// narrowed by C-01 to one leaf of an 8-leaf sub-problem) maps to the whole
// item, which is a STRICTER constraint than the statement's — the item is
// planned as a unit, so "must contain leaf 0" and "must contain the item
// holding leaf 0" admit the same item-space joinrels.
//
// Leaves OUTSIDE this problem's window — the spine above a peeled prefix,
// or the enclosing problem's other items when this is a sub-problem — are
// kept, as one bit just above the window (`1<<len(items)`), rather than
// dropped. A joinrel of this problem never contains that bit, so every
// overlap/subset test answers exactly as it did when the outside leaves were
// bits at or above `nprefix` in the un-remapped list: an SJI whose nullable
// side lies wholly outside is skipped by `joinIsLegal`'s first test, and the
// FULL arm of `paramSourceRelsForProblem` still sees a non-empty RHS it does
// not overlap. Zeroing them instead would turn `relsSubset(0, x)` true in
// three consumers and change verdicts this fix has no business changing.
//
// Fail-closed: a hand cannot need the outside bit when the problem already
// uses all 32 bits (32 single-leaf items cover every statement leaf), so
// that combination is reported rather than silently truncated.
func sjInfosInItemSpace(list []*SpecialJoinInfo, items []joinlistRel) ([]*SpecialJoinInfo, error) {
	if len(list) == 0 {
		return nil, nil
	}
	var window RelSet
	ranges := make([]RelSet, len(items))
	for i, it := range items {
		ranges[i] = leafRangeRelSet(it.lo, it.hi)
		window |= ranges[i]
	}
	outside := RelSet(1) << uint(len(items))
	remap := func(hand RelSet) (RelSet, error) {
		var out RelSet
		for i := range items {
			if relsOverlap(hand, ranges[i]) {
				out |= RelSet(1) << uint(i)
			}
		}
		if hand&^window != 0 {
			if len(items) >= maxSearchRels {
				return 0, fmt.Errorf("join search: SpecialJoinInfo hand %#08x reaches outside a %d-item problem that has no spare bit",
					uint32(hand), len(items))
			}
			out |= outside
		}
		return out, nil
	}
	out := make([]*SpecialJoinInfo, 0, len(list))
	for _, sj := range list {
		if sj == nil {
			continue
		}
		c := *sj
		var err error
		if c.MinLefthand, err = remap(sj.MinLefthand); err != nil {
			return nil, err
		}
		if c.MinRighthand, err = remap(sj.MinRighthand); err != nil {
			return nil, err
		}
		if c.SynLefthand, err = remap(sj.SynLefthand); err != nil {
			return nil, err
		}
		if c.SynRighthand, err = remap(sj.SynRighthand); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, nil
}

// searchOneProblem runs the three-step search protocol over a list of items and
// returns the chosen tree as one rel.
//
// The protocol order is upstream's and is not interchangeable: the clause list
// must exist before `addBaseRelIndexPaths`, because that is the list an index
// path is parameterised BY (`create_index_paths` after `deconstruct_jointree`,
// allpaths.c:191), and every initial rel must already carry its cheapest slots
// leafIsDerivedInput reports whether a statement leaf is a derived
// (statistics-less) input: a CTE scan, a recursive CTE's worktable, a
// FROM-subquery, or a function scan. It tests the leaf's NODE TYPE, and it
// exists because the obvious test — `relInfos[leaf].table == nil` — is WRONG
// for the case that matters most. `with.go` hands every CTE binding a
// synthesised `&catalog.Table{Name: cte.Name, Columns: cols}` so that column
// resolution works, and that table has no statistics behind it. To the
// `table == nil` test a CTE scan therefore looks like a base relation, and
// TPC-DS Q78's three CTE leaves classified as `derived=[false false false]`
// (traced 2026-09-06): the firewall below was reached with both LEFT sjinfos
// in hand and declined nothing, and the search ran three rows=1 leaves into
// an epsilon Nested Loop victory (3.07 vs Hash 3.09) — 15 s became a 327 s
// timeout. The `table == nil` arm is kept as a fallback for a leaf that has
// no binding table at all.
func leafIsDerivedInput(scan Node, info baseRelInfo) bool {
	// A statement leaf reaches the search WRAPPED: a CTE output with a
	// pushed-down predicate is `*Filter{Child: *CTEScan}`, not a bare
	// `*CTEScan`, and a type switch on the top node sees only the Filter.
	// That is exactly how TPC-DS Q78's three leaves escaped the first
	// version of this classifier (traced 2026-09-06: `scan=*optimizer.Filter
	// table=true` for all three, `derived=[false false false]`). Descend
	// through single-child wrappers to the scan underneath, then classify.
	for {
		switch x := scan.(type) {
		case *Filter:
			scan = x.Child
			continue
		case *Project:
			scan = x.Child
			continue
		}
		break
	}
	switch scan.(type) {
	case *CTEScan, *WorkTableScan:
		return true
	}
	return info.table == nil
}

// problemPairsOuterWithDerived reports whether any OUTER hand in sjis
// (already remapped to this problem's item space by sjInfosInItemSpace)
// touches an item whose STATEMENT leaves include a derived (table-less)
// FROM item — a CTE scan, FROM-subquery, or function scan. Derived inputs
// carry no statistics, so an outer join over them is the catastrophic-choice
// shape C-04a §4 names and the problem must decline (see the firewall in
// searchOneProblem).
//
// The check reads through to statement leaves (prob.relInfos[leaf].table)
// rather than trusting items[i].info.table: a searched sub-problem's rel is
// table-less by construction, and declining on that would refuse every
// nested outer join, including base-only ones. Syn (not Min) sides are
// tested — Min can narrow to one leaf of a multi-leaf item, and the question
// is whether the join READS a derived input, not whether the ordering
// constraint names it.
func problemPairsOuterWithDerived(sjis []*SpecialJoinInfo, items []joinlistRel, prob *joinlistProblem) bool {
	if len(sjis) == 0 || prob == nil {
		return false
	}
	derived := make([]bool, len(items))
	anyDerived := false
	for i, it := range items {
		for leaf := it.lo; leaf < it.hi; leaf++ {
			if leaf < 0 || leaf >= len(prob.relInfos) {
				continue
			}
			if leafIsDerivedInput(prob.scans[leaf], prob.relInfos[leaf]) {
				derived[i] = true
				anyDerived = true
				break
			}
		}
	}
	if !anyDerived {
		return false
	}
	for _, sj := range sjis {
		if sj == nil {
			continue
		}
		switch sj.Jointype {
		case parser.JoinLeft, parser.JoinRight, parser.JoinFull:
		default:
			continue
		}
		touched := sj.SynLefthand | sj.SynRighthand
		for i := range items {
			if !derived[i] {
				continue
			}
			if touched&(1<<uint(i)) != 0 {
				return true
			}
		}
	}
	return false
}

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

	// C-04a fix (Q72): `root->join_info_list` is written in STATEMENT-LEAF
	// coordinates, and this problem searches in ITEM coordinates. They are the
	// same space only for the one shape the C-03/C-04a fixtures exercised — a
	// run of single-leaf items starting at leaf 0. The moment
	// `join_collapse_limit` splits a JOIN chain (Q72: nine inner links, so the
	// first eight leaves become ONE sub-problem item and the two LEFT links
	// are items 2 and 3 of a 4-item problem), the SJI's `MinRighthand` names a
	// leaf bit no item-space joinrel ever contains, `joinIsLegal` never finds
	// it relevant, and the LEFT link is searched as an INNER join — the
	// sjinfo==nil arm, 100 → 84 rows. The list is remapped here, once per
	// problem, before anything reads it.
	sjis, err := sjInfosInItemSpace(prob.joinInfoList, items)
	if err != nil {
		return joinlistRel{}, err
	}
	// C-04a firewall (Q78): a problem that pairs an OUTER hand with a
	// derived (table-less) input declines. Derived inputs — CTE scans,
	// FROM-subqueries, function scans — carry no statistics (B-06's
	// CTE-output synthesis is inert; a searched sub-problem's rel is
	// table-less by construction too, but the check below reads through
	// to STATEMENT leaves so those never trigger it), so their rows are
	// defaults and guards. The search's algorithm comparison on those
	// defaults is the catastrophic-choice shape C-04a §4 names: Q78's
	// outer problem costed Nested Loop 3.07 against Hash 3.09 with every
	// path at rows=1 (the surviving IS NULL priced from the base
	// 	column's stanullfrac=0 — rowest A3), and the epsilon victory ran
	// Nested Loop with a Join Filter over full multi-year CTE outputs
	// (15 s Hash shape → 327 s timeout). C-04a's §4 floor
	// (applyOuterJoinRowFloor) cannot see it: the floor is the
	// preserved side's rows, and the lie is IN the preserved side's
	// rows. Declining falls back to the syntactic tree (03 §4.2), whose
	// legacy rewrites hash outer joins without a cost comparison — the
	// pre-C-04a shape. Base-leaf outer problems (Q72) are unaffected;
	// inner-only problems over derived inputs are unaffected (their
	// rows=1 is A4-expected and values-passing). Resume: lift when B-06
	// wires CTE-output stats (TODO_ALL B-06 step 4).
	if problemPairsOuterWithDerived(sjis, items, prob) {
		traceSeamDecline("outer-over-derived", len(prob.bindings), len(items))
		return joinlistRel{}, fmt.Errorf("join search: problem pairs an outer join with a derived input, which carries no statistics")
	}
	s, err := buildInitialRels(bindings, scans, infos, prob.cp, tupleFraction, sjis)
	if err != nil {
		return joinlistRel{}, err
	}
	// C-08: publish the item run beside the clause list — the
	// param_source_rels derivation remaps statement-global SJI hands
	// through it (see paramSourceRelsForProblem's frame rule).
	s.problemItems = items
	// `addParameterizedIndexPaths` reads `s.clauses`, and `joinSearch` sets it
	// — so the list is published here, before the producers that consume it,
	// and handed to `joinSearch` as well rather than left implicit.
	s.clauses = buildRestrictInfos(prob.conjuncts, 0, cum)
	// C-07: `root->query_pathkeys`, published beside the clause list because
	// `hasUsefulPathkeys` reads both.
	s.queryPathkeys = prob.queryPathkeys
	s.neededCols, s.neededColsKnown = prob.neededCols, prob.neededColsKnown
	s.outputCols, s.outputColsKnown = prob.outputCols, prob.outputColsKnown
	// Take2 P4-01 Slice 3: parent-aware narrowing is eligible only for the
	// problem covering its statement's whole prefix (a strict sub-joinlist
	// publishes to an enclosing problem whose quals this derivation cannot
	// see) with no pinned spine above it (outer or semi/anti — both read
	// the searched output from above) and no outer-scope reads (a
	// correlated statement's unnest group/probe keys read body-local
	// columns above the tree that no collector sees). Anything else keeps
	// the zero value on its rels, and the derivation declines there by
	// construction.
	s.outputEligible = lo == 0 && hi == len(prob.bindings) && !prob.spineAbove && !prob.pinAbove && !prob.corrAbove
	// take2 P4-01 rev 10 step 1: publish the set onto the base rels, AFTER the
	// assignment above. buildInitialRels ran eight lines earlier and could not
	// have seen it — that ordering is what made P4-01b's first version silently
	// dormant, so the stamp is deliberately here and not there.
	s.stampNeededColsOnRels()
	s.stampOutputColsOnRels()
	// C-19a / C-19b: `set_rel_consider_parallel` for every base rel, then
	// `create_plain_partial_paths` — in PG's order, the flag inside
	// set_rel_size and the partial seq scan in set_plain_rel_pathlist right
	// after the serial one and before create_index_paths (allpaths.c:768-790).
	// Both steps are AFTER stampNeededColsOnRels because the partial path's
	// Target is computed from NeededCols, like the serial scans' (P4-01).
	s.setBaseRelConsiderParallel(prob.cat)
	s.addBaseRelPartialPaths()
	s.addBaseRelIndexPaths(prob.cat)
	// C-19d: `generate_useful_gather_paths` for every BASE rel, which
	// upstream runs at the end of `set_rel_pathlist` (allpaths.c) — after
	// every base path producer, before the rel's set_cheapest — and ONLY when
	// the statement has more than one base rel (`bms_membership(root->
	// all_baserels) != BMS_SINGLETON`), so that a single-relation statement's
	// parallelism is decided by the upper planner (partial aggregation)
	// instead. goopg gets that exclusion for free: a one-item joinlist returns
	// its rel directly (makeRelFromJoinlist) and never reaches this function,
	// which is what keeps TPC-H Q1's `Finalize Agg -> Gather -> Partial Agg`
	// (built by the post-pass) out of this slice's blast radius.
	//
	// It runs before the level-2 search reads `CheapestTotal`, and is a no-op
	// unless GOOPG_GATHER_PATHS is `all` — see DESIGN.md §5 for why `top` is
	// the arm to measure first.
	s.addBaseRelGatherPaths()
	// GEQO: when the query has >= geqo_threshold base relations and geqo is
	// enabled, use the genetic query optimizer instead of the DP search.
	builder := newJoinRelBuilder(s, prob.cat)
	p, err := func() (*Path, error) {
		// take2 P2-02c: the per-STATEMENT geqo / geqo_threshold, not the
		// process globals. DefaultPlannerSettings seeds both from the globals,
		// so the GOOPG env defaults and the test hooks still steer a planner
		// that has no session.
		if prob.cp.geqo && len(items) >= prob.cp.geqoThreshold {
			s.builder = builder
			// take2 P3-10: the session's geqo_effort, not a literal 5.
			effort := prob.cp.geqoEffort
			if effort < 1 {
				effort = 5
			}
			return geqoSearch(s, builder, effort)
		}
		if _, err := s.joinSearch(s.clauses, builder); err != nil {
			return nil, err
		}
		return s.finalPath()
	}()
	if err != nil {
		return joinlistRel{}, err
	}
	return joinlistRel{
		// The hole-filler licenses a PADDED boundary slot for exactly the
		// coordinates a narrowed leaf legitimately dropped. Take2 P4-01
		// Slice 3: besides the statement-unneeded columns (the Slice-2
		// holes), a below-only join key — read by the statement but by
		// nothing above this root — may be padded, so the license is the
		// above-tree set when it is known and the needed set otherwise.
		// Anything the filler does not license still panics, and the seam
		// falls the search back when the above-root residual references a
		// padded coordinate — the totality assertion stays loud for real
		// producer bugs (M0134-0187, DESIGN §21).
		node: createPlanAtSearchRootRange(p, base, width, func(coord int) (SchemaColumn, bool) {
			if !prob.neededColsKnown {
				return SchemaColumn{}, false
			}
			for i := range items {
				if coord >= cum[i] && coord < cum[i+1] {
					leafSchema := scans[i].Output()
					pos := coord - cum[i]
					if pos < 0 || pos >= len(leafSchema) {
						return SchemaColumn{}, false
					}
					col := leafSchema[pos]
					if col.Name == "" {
						return SchemaColumn{}, false
					}
					if prob.outputColsKnown && prob.outputCols != nil {
						if prob.outputCols[col.Name] {
							return SchemaColumn{}, false
						}
						return col, true
					}
					if prob.neededCols[col.Name] {
						return SchemaColumn{}, false
					}
					return col, true
				}
			}
			return SchemaColumn{}, false
		}),
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
