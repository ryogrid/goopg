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
// execution-time dependency between them. `extractScans` over the pre-search
// CROSS chain supplies exactly that for a comma-FROM list — including the
// subquery / VALUES / function-scan leaves the old DP's whitelist refused
// (joinsearch.go:20), which is the leaf-whitelist gap closing at the seam
// rather than only in principle. Three shapes are declined and each decline is
// a correctness statement, not a tuning knob:
//
//   - a FROM item that is itself an explicit `JOIN` produces ONE node and
//     SEVERAL bindings, so the leaf count disagrees with the binding count and
//     the joinlist's leaf indices no longer subscript anything. Flattening
//     those into the search additionally needs the `ON` conjuncts — which live
//     on the `*Join` node, not in the `WHERE` `*Filter` this seam is handed —
//     to reach the clause list. That is the collapse-ON population of 08 §2 and
//     it is ledgered, not silently half-done: with `GOOPG_PGSHAPED_COLLAPSE` on
//     the joinlist flattens but this seam still declines, so the flag pair
//     cannot produce a plan that reorders around a qual nobody placed;
//   - a LATERAL item, whose rows depend on an item to its left. The search
//     chooses an order, so admitting one would be a wrong answer, not a slow
//     one. `extractScans` cannot see it — it flattens the CROSS chain that
//     carries the marker — so the chain is walked for it explicitly;
//   - bindings whose offsets do not agree with the concatenation of the leaf
//     widths. Every coordinate in the clause list is written in that one space
//     (03 §6.2), so a disagreement means the caller's map is not the map the
//     search would use.
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
		return node, pred, false
	}
	scans, widths := extractScans(node)
	if len(scans) != nrels {
		return node, pred, false
	}
	if chainCarriesLateral(node) {
		return node, pred, false
	}
	cumOffsets := make([]int, nrels+1)
	for i := range scans {
		if scans[i] == nil {
			return node, pred, false
		}
		cumOffsets[i+1] = cumOffsets[i] + widths[i]
		if ctx.bindings[i].offset != cumOffsets[i] {
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
	conjuncts := splitAnd(pred)
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

// chainCarriesLateral reports whether the pre-search CROSS chain contains a
// LATERAL dependency — an item whose rows are computed per row of an item to
// its left.
//
// The join search chooses an order, so a LATERAL item may not enter it: the
// dependency is not expressible as a clause and reordering across it produces
// wrong rows rather than slow ones. Both spellings are checked because
// `planFromClause` marks the CROSS join it builds (`Join.Lateral`) while a
// FROM-clause SRF's outer references live on the leaf itself
// (`nodeReferencesOuter`), and `extractScans` — which flattens the chain —
// discards the first of those.
func chainCarriesLateral(n Node) bool {
	if n == nil {
		return false
	}
	if j, ok := n.(*Join); ok && j.Type == JoinTypeCross {
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
