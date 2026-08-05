package planner

// M0127-P5.9-b — the `planSelect` seam: the one place the PG-shaped join search
// is asked to plan a real statement, and the one place it is decided what the
// search did NOT consume.
//
// PG oracle: `query_planner` (planner.c:1276) assembles the base rels, hands
// `root->parse->jointree->fromlist` to `make_rel_from_joinlist`
// (allpaths.c:3352), and `root->tuple_fraction` — set by `preprocess_limit`
// before any rel exists — travels with it. Design: leftdeep-joins 03 §6.2 (the
// consumer), 03 §10 (the boundary the spliced subtree publishes), 08 §2 (the
// `GOOPG_PGSHAPED_DP` stage) and 08 §3 (the coexistence rules this file's skips
// implement).
//
// # What was missing
//
// P5.9-a gave the joinlist a consumer and P5.1-P5.8 gave that consumer
// everything it searches with, but no production caller existed: every arm of
// the new search was reachable only from a unit test. This file is that caller,
// and it answers the three questions the recursion deliberately does not.
//
// ## 1. Which statements enter the search
//
// The search wants ONE leaf per FROM binding, in binding order, with no
// execution-time dependency between them. `extractSearchLeaves` over the
// pre-search chain supplies exactly that — including the subquery / VALUES /
// function-scan leaves the old DP's whitelist refused (joinsearch.go:20), which
// is the leaf-whitelist gap closing at the seam rather than only in principle.
//
// M0127-P5.9-r: it walks the INNER links too, not only the CROSS ones a
// comma-FROM list produces, and that is what makes an explicit `JOIN … ON`
// reorderable at all. Before it, `extractScans` (bushy.go) descended
// `JoinTypeCross` and nothing else, so an explicit JOIN arrived as ONE node for
// N bindings, the leaf count disagreed with the binding count, and the seam
// declined the whole statement before `ctx.joinlist` was ever consulted — with
// `GOOPG_PGSHAPED_COLLAPSE` on OR off, which is why the collapse flip was
// measured as a no-go about a flag that could not move a plan (09 §3.18).
// Upstream has no such restriction: `deconstruct_recurse` (initsplan.c:1250)
// walks the `JoinExpr` chain and `distribute_qual_to_rels` puts each `ON` qual
// into the enclosing problem's clause list, which is exactly what the walk's
// third return value carries here.
//
// An `ON` qual may be routed that way only because the link is INNER: an inner
// join's qual is semantically a `WHERE` qual, so a conjunct the search does not
// place is still correct in the residual `Filter` above the searched tree. That
// equivalence is the whole licence for this, and it is why the walk descends
// INNER and CROSS and stops at every other join type.
//
// Four shapes are declined and each decline is a correctness statement, not a
// tuning knob:
//
//   - a FROM item whose chain contains an OUTER (or semi/anti) join. The walk
//     stops there and returns that node as a leaf, so the leaf count disagrees
//     with the binding count and the statement falls back to the syntactic
//     shape. `a LEFT JOIN b ON … JOIN c ON …` is therefore declined whole,
//     rather than searched from a joinlist whose leaf indices would subscript
//     bindings the leaves do not correspond to;
//   - an `ON` qual on an item that is not the FIRST comma-separated FROM item.
//     `planFromItem` resolves a chain's quals in that ITEM's coordinates
//     (planner.go:2178-2190 — `mergedCtx` is built from the item's own
//     `leftCtx`), while `planFromClause` shifts only the BINDINGS when it
//     crosses items (planner.go:1985-1999). Re-basing the qual is one call to
//     `shiftColumnRefsBy`, but that rewriter answers `return e` for an
//     expression kind it does not know, which would leave a ColumnRef reading
//     the wrong column instead of failing — a wrong answer, and the class this
//     milestone exists to remove. So the seam admits the shift-free case (base
//     0) and declines the rest; ledgered, not silently half-done;
//   - a LATERAL item, whose rows depend on an item to its left. The search
//     chooses an order, so admitting one would be a wrong answer, not a slow
//     one. The flattened leaf list cannot see the marker — it lives on the
//     chain node — so the chain is walked for it explicitly;
//   - bindings whose offsets do not agree with the concatenation of the leaf
//     widths. Every coordinate in the clause list is written in that one space
//     (03 §6.2), so a disagreement means the caller's map is not the map the
//     search would use.
//
// What the joinlist does with an admitted chain is the collapse flag's
// business, not this walk's: with `GOOPG_PGSHAPED_COLLAPSE` off every INNER
// `JoinExpr` is still pinned into its own two-member subproblem
// (`joinPinned`, collapse.go), so the written order survives and only the PATHS
// are chosen; with it on the chain flattens into one problem and the order is
// searched. Both regimes now reach the search, which is what makes 03 §6's
// collapse pass a decidable question instead of a dead one.
//
// ## 2. Which conjuncts the search consumed
//
// The residual `Filter` belongs to the pre-search pipeline, so the pipeline has
// to be told what is left. The answer is NOT re-derived here: `searchConsumes`
// asks `buildRestrictInfos` — the search's own producer — whether THIS conjunct
// becomes a clause, and treats anything else as residual. That matters for the
// one shape where re-derivation would silently drop a qual: an OR-of-ANDs
// contributes its common equalities to the clause list and NOT itself
// (joinrestrict.go:171-177), so the full OR must survive in the `Filter` even
// though "it reaches two relations" is true of it. Asking the producer makes
// that fall out instead of having to be remembered.
//
// The question is asked in the FULL per-FROM-item coordinate space, not the
// per-joinlist-item space one problem uses. A conjunct spanning two FROM items
// is placed by whichever problem first has them in different items — that
// problem exists, because two distinct leaves must separate somewhere on the
// way down — so spanning two items anywhere is the same predicate as "some
// problem placed it".
//
// ## 3. What the legacy passes may still do to a searched subtree
//
// 08 §3: the qual-placement and layout passes keep running for non-searched
// shapes and must not double-fire on a searched one. P5.5-f-ii-a made the three
// posmap/reconcile passes skip a tagged root; this task adds the four that
// REWRITE a searched tree rather than renumber it (`pushPredicatesIntoCrossJoins`,
// `rewriteJoinsToNLI`, `rewriteMultiWayChain`,
// `rewriteScanInputsWithSingleTablePredicates`). The reason they cannot be left
// alone is coordinates, not taste: those passes address a join tree in the
// statement's FROM-cumulative space, while a searched tree's INTERNAL joins
// carry the search's own per-joinrel layouts and only its ROOT is republished in
// binding order (03 §10). Pushing a global-coordinate conjunct onto an internal
// searched join therefore evaluates it against the wrong columns.
//
// # The flag
//
// `GOOPG_PGSHAPED_DP` (joinsearch.go) is OFF by default and this is the only
// production reader of it. With it off `tryPGShapedJoinSearch` returns
// `used == false` on its first line, `tryBushyDP` runs unchanged, no tree
// carries the searched tag, and all four skips above are unreachable — the
// default arm is byte-identical.

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// tryPGShapedJoinSearch plans `node`'s FROM items with the PG-shaped join
// search and returns the searched tree plus the conjuncts of `pred` the search
// did not consume (nil when it consumed all of them).
//
// `used` is false when the search did not run or did not finish, and then
// `node`/`pred` come back untouched — which is 03 §4.2's rule that a failed
// search falls back to the syntactic shape rather than failing the statement.
func tryPGShapedJoinSearch(node Node, pred Expr, ctx *resolveContext, cat catalog.Catalog) (out Node, residual Expr, used bool) {
	if !pgShapedDPEnabled() || node == nil || pred == nil || ctx == nil {
		return node, pred, false
	}
	nrels := len(ctx.bindings)
	// One relation is not a search (`make_rel_from_joinlist` returns the item);
	// past `maxSearchRels` the RelSet cannot address the problem at all, and
	// the joinlist's own leaf indices would exceed the clause list's bit width.
	if nrels < 2 || nrels > maxSearchRels || len(ctx.joinlist) == 0 {
		traceSeamDecline("size-or-no-joinlist", nrels, len(ctx.joinlist))
		return node, pred, false
	}
	scans, widths, onQuals, ok := extractSearchLeaves(node)
	if !ok {
		traceSeamDecline("chain-not-flattenable", nrels, len(scans))
		return node, pred, false
	}
	if len(scans) != nrels {
		traceSeamDecline("leaf-count", nrels, len(scans))
		return node, pred, false
	}
	if chainCarriesLateral(node) {
		traceSeamDecline("lateral", nrels, len(scans))
		return node, pred, false
	}
	cumOffsets := make([]int, nrels+1)
	for i := range scans {
		if scans[i] == nil {
			traceSeamDecline("nil-leaf", nrels, len(scans))
			return node, pred, false
		}
		cumOffsets[i+1] = cumOffsets[i] + widths[i]
		if ctx.bindings[i].offset != cumOffsets[i] {
			traceSeamDecline("offset-disagreement", nrels, len(scans))
			return node, pred, false
		}
	}

	// Same preparation the bushy DP does (bushy.go:162-196), for the same
	// reason: a relation's cardinality has to be its POST-local-filter one or
	// every join order above it is ranked on a fiction. The difference is where
	// the local quals end up — attached to the leaf BEFORE the search rather
	// than to the winning tree after it. The search's index producers already
	// expect that shape (`scanLeafFor`, createplanindex.go), and attaching
	// first removes the pointer-identity dependency `attachRelationLocalFilters`
	// has, which a searched tree cannot honour: the index arm REBUILDS a leaf
	// (P5.5-c), so a leaf matched by identity afterwards could be missed and its
	// qual lost.
	// M0127-P5.9-r: the `ON` quals of the INNER links the walk descended join
	// the `WHERE` conjuncts in ONE list, split with the same `splitAnd` and
	// partitioned by the same rule. That is upstream's shape, not a shortcut:
	// `distribute_qual_to_rels` places a qual by the relids it reads, and an
	// inner join's qual has no other property to distinguish it — which is why
	// a single-relation `ON` qual becomes a leaf-local filter here exactly as a
	// single-relation `WHERE` qual does.
	conjuncts := splitAnd(pred)
	for _, q := range onQuals {
		conjuncts = append(conjuncts, splitAnd(q)...)
	}
	searchConjuncts, locals := partitionConjunctsForJoinPlanning(conjuncts, cumOffsets)
	leaves := make([]Node, nrels)
	relInfos := make([]baseRelInfo, nrels)
	for i, b := range ctx.bindings {
		leaves[i] = scans[i]
		var local Expr
		if preds := locals.byBinding[i]; len(preds) > 0 {
			local = combineAnd(preds)
			localized := make([]Expr, 0, len(preds))
			for _, p := range preds {
				localized = append(localized, localizeExprToLeaf(p, b))
			}
			leaves[i] = &Filter{Child: scans[i], Predicate: combineAnd(localized), LeafLocal: true}
		}
		relInfos[i] = estimateBaseRelInfo(b, scans[i], local)
		relInfos[i].bindingIdx = i
		// Tier 3 of `bushySeedRowCounts`' ladder (bushy.go), which
		// `estimateBaseRelInfo` does not apply for itself: `tableRows` answers 0
		// for a relation with no `TableStats`, and `TableStats.RowCount` does not
		// survive a restart (ledger pq-P6). Without this a cold server seeds
		// every relation at the 1-row floor, and at one row per side the cost
		// model correctly prefers a NESTED LOOP — so the seam would have handed
		// the search a blind problem where the DP it replaces gets a live
		// block-count estimate. Deliberately PRE-filter, for the reason
		// `bushySeedRowCounts` states: a server with no row count has no column
		// statistics either, so scaling it by a selectivity invents precision.
		if relInfos[i].filteredRows <= 0 {
			if rows := relSizeFallbackRows(2, cat, b.table); rows > 0 {
				relInfos[i].baseRows = rows
				relInfos[i].filteredRows = rows
			}
		}
	}

	searched, err := planJoinlistSearch(ctx.joinlist, &joinlistProblem{
		bindings:   ctx.bindings,
		scans:      leaves,
		relInfos:   relInfos,
		conjuncts:  searchConjuncts,
		cumOffsets: cumOffsets,
		cp:         defaultCostParams(),
		cat:        cat,
		// `root->tuple_fraction`, carried on the context because the `*Limit`
		// node does not exist yet at this point in `planSelect` (see
		// `searchTupleFraction`).
		tupleFraction: ctx.tupleFraction,
	})
	if err != nil || searched == nil {
		return node, pred, false
	}

	var left []Expr
	for _, c := range searchConjuncts {
		if !searchConsumes(c, cumOffsets) {
			left = append(left, c)
		}
	}
	if len(left) == 0 {
		return searched, nil, true
	}
	return searched, combineAnd(left), true
}

// searchConsumes reports whether the join search placed `c` somewhere in the
// tree it built, i.e. whether the residual `Filter` may drop it.
//
// It is deliberately a QUESTION PUT TO THE PRODUCER rather than a second
// implementation of the admission rule: `buildRestrictInfos` is what the search
// runs, and a clause it emits is applied at exactly one join (the lowest that
// covers it and touches both sides — `clausesFor`, joinrestrict.go:276), so
// "the producer emitted THIS conjunct as a clause" is precisely "the search
// applies it". The identity test on `clause` is the load-bearing part: an
// OR-of-ANDs makes the producer emit the equalities COMMON to its branches, and
// those are implied by the OR rather than equal to it, so the OR itself stays
// residual and is evaluated above the join exactly as it is today.
func searchConsumes(c Expr, cumOffsets []int) bool {
	for _, ri := range buildRestrictInfos([]Expr{c}, 0, cumOffsets).all {
		if ri.clause == c {
			return true
		}
	}
	return false
}

// extractSearchLeaves flattens the pre-search join chain into the one-leaf-per-
// FROM-binding list the search takes, and returns the `ON` quals of the links it
// flattened, re-expressed in the statement's binding coordinates.
//
// It is the seam's own walk rather than `extractScans` (bushy.go) because the
// two answer different questions. `extractScans` feeds the legacy bushy DP,
// which reads its predicates from the `WHERE` `*Filter` alone and rebuilds a
// tree over the leaves it is given; flattening an INNER link for THAT consumer
// would drop the link's qual on the floor. This walk hands the qual back, so the
// caller can place it — see the file header for why an INNER link's qual may be
// placed anywhere at or above the join and an outer link's may not.
//
// `ok` is false when the chain cannot be flattened without moving a qual the
// walk cannot re-base (the non-first-item case of the file header) or when a
// CROSS link carries a qual — which `planFromClause`/`planFromItem` never build
// (a `CROSS JOIN` has no `ON` clause and `planJoinPredicate` answers nil), so it
// is a shape from some later rewrite and not one this walk may reinterpret.
// A false `ok` is a decline, never a partial answer: the caller falls back to
// the syntactic tree, which still carries every qual on its own nodes.
//
// Leaves are appended in chain order, which is binding order — `planFromItem`
// numbers a chain's bindings left to right and `planFromClause` appends items
// in FROM order (03 §6.1's leaf-numbering guarantee), and this walk visits Left
// before Right at every level.
func extractSearchLeaves(node Node) (scans []Node, widths []int, onQuals []Expr, ok bool) {
	width := 0
	var walk func(Node) bool
	walk = func(n Node) bool {
		if n == nil {
			return false
		}
		j, isJoin := n.(*Join)
		if !isJoin || (j.Type != JoinTypeCross && j.Type != JoinTypeInner) {
			scans = append(scans, n)
			widths = append(widths, len(n.Output()))
			width += len(n.Output())
			return true
		}
		// The link's own coordinate origin: the leaves to its left have already
		// been counted, and its qual was resolved against a schema that starts
		// at its leftmost leaf, so this is the delta between the two spaces.
		base := width
		if !walk(j.Left) || !walk(j.Right) {
			return false
		}
		if j.Predicate == nil {
			return true
		}
		if j.Type != JoinTypeInner || base != 0 {
			return false
		}
		onQuals = append(onQuals, j.Predicate)
		return true
	}
	if !walk(node) {
		return nil, nil, nil, false
	}
	return scans, widths, onQuals, true
}

// chainCarriesLateral reports whether the pre-search join chain contains a
// LATERAL dependency — an item whose rows are computed per row of an item to
// its left.
//
// The join search chooses an order, so a LATERAL item may not enter it: the
// dependency is not expressible as a clause and reordering across it produces
// wrong rows rather than slow ones. Both spellings are checked because
// `planFromClause`/`planFromItem` mark the join they build (`Join.Lateral`)
// while a FROM-clause SRF's outer references live on the leaf itself
// (`nodeReferencesOuter`), and `extractSearchLeaves` — which flattens the chain
// — discards the first of those.
//
// M0127-P5.9-r: it descends exactly the links that walk flattens. A LATERAL on
// the right of an explicit `JOIN` is marked on the INNER node
// (planner.go:2261-2270), so checking only CROSS links would have let the one
// shape the search must never reorder in through the new door.
func chainCarriesLateral(n Node) bool {
	if n == nil {
		return false
	}
	if j, ok := n.(*Join); ok && (j.Type == JoinTypeCross || j.Type == JoinTypeInner) {
		return j.Lateral || chainCarriesLateral(j.Left) || chainCarriesLateral(j.Right)
	}
	return nodeReferencesOuter(n)
}

// searchTupleFraction is `preprocess_limit`'s answer (planner.c:2577) for a
// statement whose LIMIT/OFFSET have not been resolved yet.
//
// The join search runs long before `planSelect` builds its `*Limit` node, and
// `root->tuple_fraction` must be fixed BEFORE the first rel exists
// (`build_simple_rel` reads it while constructing one, relnode.c:211 — see
// `buildInitialRels`). Resolving the clauses early instead is not a free
// reordering: `resolveExpr` on a `LIMIT (SELECT …)` plans a subquery, and doing
// that twice would plan it twice. So the clause is read at the PARSE level and
// only a literal is treated as known.
//
// That is upstream's own division, one const-fold short of it: PG runs
// `estimate_expression_value` first, so `LIMIT 5 + 5` and a bound `LIMIT $1`
// are constants to PG and are the 10 % punt here — the same gap
// `limitClauseEstimate` already carries (ledger row 2026-08-05), now reachable
// from production rather than only from a test.
func searchTupleFraction(limit, offset parser.Expr) float64 {
	l, o := limitParseConst(limit), limitParseConst(offset)
	if l == nil && o == nil {
		return 0
	}
	tf, _ := preprocessLimit(&Limit{Limit: l, Offset: o}, 0)
	return tf
}

// limitParseConst renders an unresolved LIMIT/OFFSET clause as the resolved
// node `limitClauseEstimate` reads: nil for an absent clause, the literal for a
// constant one, and a `*ParamRef` — which `constInt` declines — for anything
// else, so a present-but-unknown clause takes the 10 % punt rather than reading
// as absent.
func limitParseConst(p parser.Expr) Expr {
	if p == nil {
		return nil
	}
	if _, isNull := p.(*parser.NullConst); isNull {
		return &NullConst{}
	}
	if ic, isInt := p.(*parser.IntegerConst); isInt {
		return &IntegerConst{Value: ic.Value}
	}
	return &ParamRef{}
}
