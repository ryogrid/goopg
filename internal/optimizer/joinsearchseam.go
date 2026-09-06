package optimizer

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
// M0127-P5.9-s: an outer link no longer declines the statement — it is PEELED.
// `splitOuterSpine` takes the pinned outer links off the top of the chain, the
// search plans the INNER PREFIX below them, and the links are spliced back above
// the searched subtree unchanged. That is the shape the corpus actually has:
// all 12 of TPC-DS's explicit-JOIN queries contain an outer join and none is
// INNER-only (P5.9-r's `TestNoCorpusQueryHasAnInnerOnlyJoinChain`), so before the
// peel the INNER walk had nothing to walk — Q72's nine-way inner prefix was
// declined for the two `left outer join`s stacked on top of it. The peel is the
// same division `runJoinSearchBelowPinned` (predp.go) makes for the semi/anti
// spine, and it is bounded the same way: only LEFT links may be peeled, because
// the prefix is the link's left side and the seam pushes conjuncts INTO it (see
// `splitOuterSpine`).
//
// Four shapes are declined and each decline is a correctness statement, not a
// tuning knob:
//
//   - a chain whose outer join is NOT part of the top spine — one below an inner
//     link, or on a non-first comma FROM item. `extractSearchLeaves` stops at it
//     and returns that node as a leaf, so the leaf count disagrees with the
//     prefix's relation count and the statement falls back to the syntactic
//     shape. `a LEFT JOIN b ON … JOIN c ON …` is therefore still declined whole,
//     rather than searched from a joinlist whose leaf indices would subscript
//     bindings the leaves do not correspond to. `makeRelFromJoinlist` declines
//     it a second time, from the joinlist side (P5.9-s), so a shape that slipped
//     past the walk cannot be planned as an inner join by accident;
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
// `GOOPG_PGSHAPED_DP` (joinsearch.go) is ON by default (flipped at
// M0127-P5.9) and this is the only production reader of it. With it off
// `tryPGShapedJoinSearch` returns `used == false` on its first line, no tree
// carries the searched tag, and the skips above are unreachable — the
// statement keeps its syntactic FROM order. (Until M0127-P6.3 the off arm
// ran the old subset-bitmask DP instead; that enumerator is deleted, 08 §4.)

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// tryJoinSearch is the pipeline's join-order entry point: `planSelect`
// (planner.go) and `runJoinSearchBelowPinned` (predp.go) call it with the
// pre-search CROSS/INNER chain and the `WHERE` predicate above it, and get back
// the tree to plan plus whatever predicate is left.
//
// It was `tryBushyDP` (bushy.go) until M0127-P6.3 deleted the old
// subset-bitmask DP (08 §4). Between M0127-P5.9 and P6.3 the function had two
// arms — the PG-shaped search first, that DP as the `GOOPG_PGSHAPED_DP=0`
// fallback — and the kill-switch's rollback story was "restores the current
// `tryBushyDP` enumerator, which is not deleted until S7". S7 is here, so the
// second arm is gone and the flag now only decides whether a join order is
// SEARCHED at all: with it off, or on a shape the seam declines, the statement
// keeps the syntactic order the FROM clause was written in (permuted at parse
// level by `reorderCommaFromByCardinality`, joinorder.go) and the downstream
// rewrites — `pushPredicatesIntoCrossJoins`, `rewriteJoinsToNLI` — do what they
// have always done to such a tree.
//
// Returning `(node, pred)` unchanged is therefore the whole fallback, and it is
// 03 §4.2's rule: a search that did not run falls back to the syntactic shape
// rather than failing the statement.
func tryJoinSearch(node Node, pred Expr, ctx *resolveContext, cat catalog.Catalog) (Node, Expr) {
	if searched, residual, used := tryPGShapedJoinSearch(node, pred, ctx, cat); used {
		return searched, residual
	}
	return node, pred
}

// tryPGShapedJoinSearch plans `node`'s FROM items with the PG-shaped join
// search and returns the searched tree plus the conjuncts of `pred` the search
// did not consume (nil when it consumed all of them).
//
// `used` is false when the search did not run or did not finish, and then
// `node`/`pred` come back untouched — which is 03 §4.2's rule that a failed
// search falls back to the syntactic shape rather than failing the statement.
func tryPGShapedJoinSearch(node Node, pred Expr, ctx *resolveContext, cat catalog.Catalog) (out Node, residual Expr, used bool) {
	// `pred == nil` is NOT a decline (M0134-0188): a FROM tree with no WHERE
	// still deserves the search — its access methods are chosen there, and
	// TPC-H Q13's `customer LEFT JOIN orders` subquery is precisely a
	// filterless statement whose customer scan must become PG's
	// `Index Only Scan using customer_pk`. `splitAnd(nil)` is an empty
	// conjunct list and every consumer below already handles it.
	if !pgShapedDPEnabled() || node == nil || ctx == nil {
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
	// M0127-P5.9-s: peel the pinned outer spine off the top and search what is
	// below it. With no outer link this is the identity — `chain == node`,
	// `spine` empty, `jl == ctx.joinlist` — so the shapes P5.9-r already searched
	// take exactly the path they took before.
	chain, spine, jl, ok := splitOuterSpine(node, ctx.joinlist)
	if !ok {
		traceSeamDecline("outer-spine", nrels, len(spine))
		return node, pred, false
	}
	// The prefix's own width, in FROM items. Everything below is written against
	// it rather than against `nrels`, because the spine's relations are outside
	// the problem: their columns lie beyond the prefix window, so every conjunct
	// touching one is declined by the clause producer and survives in the
	// residual `Filter` above the spine.
	nprefix := jl.nrels()
	if nprefix < 2 && len(spine) == 0 {
		// One relation and no spine is not a search — the single-table paths
		// own that statement. UNDER a spine a one-relation prefix is still
		// worth planning (M0134-0188): there is no order to choose, but there
		// IS an access method — base-rel path generation runs, `add_path`
		// picks among seq / index / index-only, and the boundary republishes
		// binding order exactly as for a wider prefix. `a LEFT JOIN b`'s left
		// side is the one place PG chooses a covering scan that no other
		// goopg seam could reach (TPC-H Q13).
		traceSeamDecline("prefix-size", nrels, nprefix)
		return node, pred, false
	}
	if lo, hi, okRange := jl.leafRange(); !okRange || lo != 0 || hi != nprefix {
		traceSeamDecline("prefix-not-a-prefix", nrels, nprefix)
		return node, pred, false
	}
	scans, widths, onQuals, outerLinks, ok := extractSearchLeaves(chain)
	if !ok {
		traceSeamDecline("chain-not-flattenable", nrels, len(scans))
		return node, pred, false
	}
	if len(scans) != nprefix {
		traceSeamDecline("leaf-count", nrels, len(scans))
		return node, pred, false
	}
	if chainCarriesLateral(chain) {
		traceSeamDecline("lateral", nrels, len(scans))
		return node, pred, false
	}
	cumOffsets := make([]int, nprefix+1)
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
	// The spine's first relation must begin exactly where the prefix ends. That
	// is what makes "beyond the prefix window" and "on the spine" the same
	// statement, which every conjunct decision below relies on: a spine column
	// that landed INSIDE the window would be attributed to a prefix leaf and
	// pushed under the outer join.
	if nprefix < nrels && ctx.bindings[nprefix].offset != cumOffsets[nprefix] {
		traceSeamDecline("spine-offset-disagreement", nrels, nprefix)
		return node, pred, false
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
	//
	// M0127-P5.9-t: unless the prefix is NULLABLE. A RIGHT link nullifies its
	// left input — the prefix — so a `WHERE` conjunct reading a prefix relation
	// is a test on null-extended rows and may not be evaluated below the join
	// that produces the NULLs. That is `check_outerjoin_delay` (initsplan.c):
	// a qual from ABOVE an outer join whose relids reach the nullable side is
	// delayed to the join itself, and here "delayed" is spelled as "held in the
	// residual `Filter` the caller puts above the spine". The prefix's OWN `ON`
	// quals are unaffected — they originate BELOW the outer join, so upstream
	// distributes them normally, and suppressing them would cost a cross
	// product rather than a wrong answer.
	//
	// C-04a §3.5: the whole-`WHERE` hold above is the SPINE's rule and only
	// ever fired on a nullable spine, which after LEFT admission means a
	// RIGHT/FULL one. The LEFT links are now INSIDE the flattened chain, so
	// their nullable sides are inside the search's coordinate window, and the
	// hold is replaced there by a PER-QUAL delay proof: a `WHERE` conjunct
	// whose relids reach the nullable side of any admitted link is delayed to
	// above the whole searched tree (upstream's `check_outerjoin_delay` rule,
	// as `delayedAboveOJ` states it — one delay verdict anywhere stops the
	// descent). Everything else distributes exactly as before.
	//
	// Without this, `partitionConjunctsForJoinPlanning` — which has no
	// nullable-side guard — would make `WHERE p.y > 5` a leaf-local filter on
	// `p` and evaluate it BELOW `t LEFT JOIN p`, keeping rows that must be
	// dropped. That is the finding-1 shape and it is load-bearing, not a
	// follow-up.
	var conjuncts, heldAbovePrefix []Expr
	switch {
	case prefixNullable(spine):
		heldAbovePrefix = splitAnd(pred)
	case len(outerLinks) == 0:
		conjuncts = splitAnd(pred)
	default:
		var nullable RelSet
		for _, lk := range outerLinks {
			nullable |= lk.nullable
		}
		for _, c := range splitAnd(pred) {
			rs, attributable := relidsOfExpr(c, cumOffsets)
			// Unattributable is DELAYED, not distributed: a conjunct whose
			// relids the seam cannot see exactly is one it cannot prove does
			// not reach a nullable side. Holding it above is always correct
			// (the residual `Filter` sits above every admitted link) and only
			// ever costs a pushdown.
			if !attributable || relsOverlap(rs, nullable) {
				heldAbovePrefix = append(heldAbovePrefix, c)
				continue
			}
			conjuncts = append(conjuncts, c)
		}
	}
	for _, q := range onQuals {
		conjuncts = append(conjuncts, splitAnd(q)...)
	}
	// take2 P1-20: give the SEARCH the equivalence class's CONSTANTS.
	//
	// The closure had one caller — `pushPredicatesIntoCrossJoins` (pushdown.go)
	// — on the legacy path, so with GOOPG_PGSHAPED_DP on by default the
	// searched plan never saw it. Applying it here, BEFORE the conjuncts are
	// partitioned into join clauses and per-relation locals, is what lets a
	// propagated `var = const` become a relation-local restriction the search
	// pushes into a leaf.
	//
	// CONSTANTS ONLY, deliberately. Propagating `a = 42` across a class only
	// adds restrictions and re-opens no join order. Adding the transitive
	// `a = c` would hand the search new JOIN clauses and reshape plans broadly
	// — measured: it broke the pinned-semi-join layout
	// TestPreDPPinnedSemiKeysResolveAfterDP asserts, on a query containing no
	// constants at all. That half stays on its legacy caller pending its own
	// evaluation.
	if synth := inferEquivClassConstants(conjuncts); len(synth) > 0 {
		conjuncts = append(conjuncts, synth...)
	}
	// C-04a: an admitted outer link's `ON` conjuncts join the list only HERE,
	// after the equivalence-class constant inference has run, so the closure
	// never merges a nullable-side column into a preserved-side class (that
	// would let a WHERE constant on the preserved side be stated as a
	// PRESERVED-side restriction derived through a nullable member, or a
	// transitive equality reorder across the link).
	//
	// What IS propagated across the link is PG's `reconsider_outer_join_clauses`
	// (equivclass.c): for an ON conjunct `pres = null` whose preserved-side
	// column is equated to a constant, the nullable side gains `null = const`
	// — `deriveOuterLinkConstants` below. C-04a first withheld this as "an
	// optimisation", and the SF0.5 gate showed it is the optimisation the
	// pre-admission tree already performed (`deriveConstAcrossJoinEquality`,
	// inner_join_qual_pushdown.go, on the syntactic LEFT node the seam used to
	// peel): TPC-DS Q78's `ss LEFT JOIN ws ON ws_sold_year = ss_sold_year …
	// WHERE ss_sold_year = 1998` lost `ws_sold_year = 1998` on the nullable
	// CTE reference and, through it, `d_year = 1998` inside the CTE body —
	// `date_dim` fed the channel unfiltered, 490x larger, 15 s → timeout.
	//
	// Each conjunct must then reach a place that is AT or BELOW its link's
	// join in a way that preserves outer-join semantics, and `outerOnQualsOK`
	// proves that per conjunct before any of them is admitted (see there for
	// the two admissible destinations and why a third would be a wrong
	// answer). A failed proof declines the statement.
	if len(outerLinks) > 0 {
		// FAIL-CLOSED, and this is the guard the whole slice rests on. A
		// flattened outer link is only safe because `join_is_legal` refuses
		// every pairing that would reorder across it, and `join_is_legal`
		// knows nothing except what `ctx.joinInfoList` tells it: with an empty
		// or mismatched list every pairing looks like a plain inner join and
		// the search would emit an INNER join where the statement wrote an
		// outer one — unmatched rows silently dropped. That the production
		// caller populates the list (planner.go, `deconstructJointreeScopedSJI`)
		// is not something this seam should have to assume, so it is checked.
		if !outerLinksHaveSJInfos(outerLinks, ctx.joinInfoList) {
			traceSeamDecline("outer-link-no-sjinfo", nrels, nprefix)
			return node, pred, false
		}
		var onOuter []Expr
		for _, lk := range outerLinks {
			onOuter = append(onOuter, splitAnd(lk.pred)...)
		}
		if !outerOnQualsOK(outerLinks, cumOffsets) {
			traceSeamDecline("outer-on-qual", nrels, nprefix)
			return node, pred, false
		}
		// Derived BEFORE the ON conjuncts join the list: the constants it reads
		// are then exactly the ones the closure above could see, none of which
		// sits on a nullable side (a WHERE conjunct reaching one was held
		// above, and the ON conjuncts are not in the list yet).
		derived := deriveOuterLinkConstants(outerLinks, conjuncts, cumOffsets)
		conjuncts = append(conjuncts, onOuter...)
		conjuncts = append(conjuncts, derived...)
	}
	searchConjuncts, locals := partitionConjunctsForJoinPlanning(conjuncts, cumOffsets)
	leaves := make([]Node, nprefix)
	relInfos := make([]baseRelInfo, nprefix)
	for i, b := range ctx.bindings[:nprefix] {
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
		// `estimateBaseRelInfo` does not apply for itself — and, since
		// M0127-P5.6's re-evaluation of M0125-0003 stage 3, in upstream's own
		// order: the block-derived count is the PRE-filter `tuples` and the
		// local-filter selectivity is re-applied on top of it, exactly as
		// `estimate_rel_size` feeds `set_baserel_size_estimates`. It reads the
		// same reliability gate `estimateBaseRelInfo` does, so the earlier
		// "scaling a fallback invents precision" concern is enforced by the
		// gate rather than by refusing to scale. See `applyRelSizeFallback`.
		applyRelSizeFallback(&relInfos[i], b, scans[i], local, cat)
	}

	searched, err := planJoinlistSearch(jl, &joinlistProblem{
		bindings:   ctx.bindings[:nprefix],
		scans:      leaves,
		relInfos:   relInfos,
		conjuncts:  searchConjuncts,
		cumOffsets: cumOffsets,
		// take2 P2-01: the search prices with the STATEMENT's settings, not a
		// hard-wired constant list. tryJoinSearch is reached only from inside
		// planSelect (predp.go and two sites in planner.go), so this ctx is
		// always the one planSelect stamped — no parent walk is needed, and
		// none is safe (see resolveContext.settings).
		cp:         ctx.settings.costParams(),
		cat:        cat,
		// `root->tuple_fraction`, carried on the context because the `*Limit`
		// node does not exist yet at this point in `planSelect` (see
		// `searchTupleFraction`).
		tupleFraction: ctx.tupleFraction,
		// C-07: `root->query_pathkeys`, derived by `standard_qp_callback`
		// (deriveQueryPathkeys) at the same point in `planSelect` the
		// fraction is, and in the SAME binding coordinates the conjuncts
		// above are written in.
		queryPathkeys: ctx.queryPathkeys,
		// The statement's needed-column set (pathindexonlyneed.go), computed
		// once in `planSelect` — the only frame holding the
		// *parser.SelectStmt; the search boundary sees resolved nodes only.
		neededCols:      ctx.neededCols,
		neededColsKnown: ctx.neededColsKnown,
		// Take2 P4-01 Slice 3: the above-tree set, plus the pinned-spine
		// gates and the correlated-statement gate. A non-empty outer spine
		// reads the prefix output from above (its ON quals); a pinned
		// semi/anti spine does the same (predp). A current-scope outer
		// reference in the searched predicate or ON quals marks a
		// correlated statement, whose unnest group/probe keys read
		// body-local columns above the tree that no collector sees
		// (corrAbove): parent-aware narrowing is declined there, while the
		// Slice-2 arms still run. Subquery interiors are stepped over, so
		// only the body's own correlation declines it.
		outputCols:      ctx.outputCols,
		outputColsKnown: ctx.outputColsKnown,
		spineAbove:      len(spine) > 0,
		pinAbove:        ctx.pinAbove,
		corrAbove:       exprHasOuterRef(pred) || exprHasOuterRefList(onQuals),
		// joinInfoList is root->join_info_list from jointree deconstruction,
		// consumed by join_is_legal/joinOrderRestricted/hasJoinRestriction
		// inside the search (M0128-P1.2).
		joinInfoList: ctx.joinInfoList,
	})
	if err != nil || searched == nil {
		return node, pred, false
	}

	// The held-back `WHERE` conjuncts lead, so a statement whose whole `WHERE`
	// is held comes back with `residual == pred` by identity (`combineAnd` of
	// one element is that element) rather than a re-associated copy.
	left := append([]Expr(nil), heldAbovePrefix...)
	for _, c := range searchConjuncts {
		if !searchConsumes(c, cumOffsets) {
			left = append(left, c)
		}
	}
	residual = nil
	if len(left) > 0 {
		residual = combineAnd(left)
	}
	// Take2 P4-01 Slice 3: the above-root residual is evaluated ABOVE the
	// searched subtree, positionally in binding coordinates, so every column
	// it names must have survived narrowing — a padded (dropped) column
	// would read back a NULL. When the residual references a padded
	// coordinate, fall back to the syntactic shape (03 §4.2) rather than
	// plan a query that runs: Slice-2 pads (statement-unneeded columns) can
	// never trip this, so the check only fires on the narrower Slice-3
	// keeps. Name-keyed, erring toward fallback.
	if residual != nil && searchedResidualHitsPad(residual, searched, ctx.neededCols) {
		traceSeamDecline("residual-hits-pad", nrels, nprefix)
		return node, pred, false
	}
	if len(spine) == 0 {
		return searched, residual, true
	}
	// Splice the searched prefix under the LOWEST spine link. Nothing above it
	// is rebuilt and nothing below it is rebound, and both halves of that are
	// claims about the boundary rather than conveniences:
	//
	//   - the searched root republishes the prefix's columns in pre-search
	//     binding order (`createPlanAtSearchRootRange`, 03 §10), so every spine
	//     `ON` qual — resolved in the statement's coordinates by `planFromItem` —
	//     still reads the columns it named. That is the identity boundary map
	//     `assertSpineConsumesIdentityBoundaryMap` (predp.go) proves for the
	//     semi/anti spine; the width check below is the part of it that can be
	//     checked from here, and the part whose failure would be silent;
	//   - a spine link keeps its own type, sides and qual, because it was never
	//     handed to the search. The joinlist's pin and this splice are the same
	//     decision spelled in the two representations, which is why
	//     `splitOuterSpine` refuses to proceed unless they agree.
	low := spine[len(spine)-1]
	if len(searched.Output()) != len(low.Left.Output()) {
		traceSeamDecline("spine-width", nrels, nprefix)
		return node, pred, false
	}
	low.Left = searched
	traceSeamSpine(len(spine), nrels, nprefix)
	return node, residual, true
}

// splitOuterSpine splits a statement into the INNER-PREFIX subproblem the search
// may plan and the pinned outer links stacked above it, and returns the prefix's
// own joinlist.
//
// It splits BOTH representations — the pre-search plan tree and the joinlist —
// and declines unless they agree link for link, because they are two spellings of
// the same chain: `deconstructFromItem` pins the same nodes `planFromItem` built,
// in the same order, so a disagreement means one of them is not describing this
// statement and the coordinate arithmetic below the seam has no ground truth.
// `ok == true` with an empty spine is the no-outer-link case, where `chain` is
// `node` and `prefix` is `jl` — the identity, so P5.9-r's shapes are unaffected.
//
// # LEFT and RIGHT, and the difference is what may be pushed below the link
//
// The prefix is always the link's LEFT side, whichever way the link points —
// goopg's FROM chain is left-deep and a `JoinExpr`'s right side is a single
// range var, so the multi-relation subproblem is on the left of a RIGHT JOIN
// exactly as it is on the left of a LEFT JOIN. What changes is NULLABILITY:
// a LEFT link preserves its left input, a RIGHT link null-extends it.
//
// That matters because the search does not merely reorder the prefix: the seam
// attaches single-relation conjuncts to prefix leaves and lets the search place
// spanning ones INSIDE the prefix, i.e. BELOW the outer join. Upstream's rule is
// `check_outerjoin_delay` (initsplan.c) — a qual coming from ABOVE an outer join
// is delayed when its relids reach the NULLABLE side — so under a RIGHT link the
// `WHERE` may not be pushed at all, or `WHERE a.x IS NULL` would turn from a test
// on null-extended rows into a test on `a`'s own rows. `prefixNullable` decides
// that, and `tryPGShapedJoinSearch` holds the whole `WHERE` in the residual when
// it answers true; the ORDER search is legal either way, because it is upstream's
// own sub-joinlist for the nullable side (`deconstruct_recurse` on a JoinExpr's
// nullable arm builds one, and `make_rel_from_joinlist` recurses into it).
//
// FULL stays out: both of its inputs are null-extended, and its `UsingLeftCols`
// / `UsingRightCols` coalescing names merged-var positions that a re-associated
// input would have to be checked against. Ledgered, not forgotten.
//
// This is deliberately NOT upstream's `reduce_outer_joins` RIGHT→LEFT flip
// (prepjointree.c:3360). That flip swaps a `JoinExpr`'s arms, which goopg's
// `parser.FromExpr` — a `Base` range var plus a FLAT `[]JoinExpr` — cannot
// represent: the flipped shape is `d LEFT JOIN (a ⋈ b ⋈ c)`, a nested join on
// the right side, and there is no node for it. Flipping inside the planner's own
// tree instead would renumber every binding offset and reorder `SELECT *`, which
// upstream avoids only because its Vars are varno-addressed. The flip is a
// representation change; what the seam actually needed was the delay rule.
//
// Semi/anti spines are declined too, and are not a gap: `runJoinSearchBelowPinned`
// (predp.go) already descends those before the seam is called, so the `node` the
// seam receives is the subtree below them.
func splitOuterSpine(node Node, jl joinlist) (chain Node, spine []*Join, prefix joinlist, ok bool) {
	prefix, types := jl.innerPrefixBelowOuterSpine()
	chain = node
	for _, t := range types {
		j, isJoin := chain.(*Join)
		if !isJoin || !spineLinkSearchable(j, t) {
			return nil, nil, nil, false
		}
		spine = append(spine, j)
		chain = j.Left
	}
	if chain == nil {
		return nil, nil, nil, false
	}
	return chain, spine, prefix, true
}

// prefixNullable reports whether the peeled spine null-extends the prefix below
// it, i.e. whether a `WHERE` conjunct reading a prefix relation would be a test
// on null-extended rows.
//
// ONE nullifying link anywhere on the spine is enough, and it is the LINK's own
// left input that is nullified: the spine is a stack, so a RIGHT link's NULLs
// flow up through every link above it whatever those links are. Written as
// "anything that is not LEFT" rather than "RIGHT" so a join type added to
// `spineLinkSearchable` later is nullable until someone says otherwise.
func prefixNullable(spine []*Join) bool {
	for _, j := range spine {
		if j.Type != JoinTypeLeft {
			return true
		}
	}
	return false
}

// spineLinkSearchable reports whether one peeled link may stay pinned above a
// searched prefix: the plan node and the joinlist must name the same join type,
// that type must be LEFT or RIGHT (see `splitOuterSpine`), and the link must
// carry no LATERAL dependency.
//
// LATERAL is checked on both spellings for the reason `chainCarriesLateral`
// states — `planFromClause` marks the join it builds while a FROM-clause SRF's
// outer references live on the leaf — and it is declined rather than reasoned
// about: the right side of a LATERAL link is evaluated per left row, so it is the
// one shape whose correctness depends on more than the left side's column
// layout, which is all this splice preserves.
func spineLinkSearchable(j *Join, t parser.JoinType) bool {
	// The two spellings must AGREE, not merely both be admissible: which member
	// of the pin is the left side is the whole question the splice answers, and
	// a plan node saying LEFT under a joinlist saying RIGHT means one of them is
	// not describing this statement.
	switch {
	case j.Type == JoinTypeLeft && t == parser.JoinLeft:
	case j.Type == JoinTypeRight && t == parser.JoinRight:
	default:
		return false
	}
	return !j.Lateral && !nodeReferencesOuter(j.Right)
}

// outerLinksHaveSJInfos reports whether every admitted outer link is described
// by a SpecialJoinInfo in the statement's `root->join_info_list`.
//
// The match is on the SYNTACTIC hands and the jointype, which is exactly what
// `deconstructJointreeScopedSJI` builds them from: the link's own two sides in
// leaf-index space. `MinLefthand`/`MinRighthand` are deliberately not compared —
// they are the SJI's own narrowing (C-01) and may legitimately be smaller.
//
// A caller that builds a joinlist without its SpecialJoinInfos — a hand-made
// fixture, or any future producer that forgets — declines the statement here
// rather than getting an inner join for its outer one.
func outerLinksHaveSJInfos(links []outerChainLink, list []*SpecialJoinInfo) bool {
	for _, lk := range links {
		found := false
		for _, sj := range list {
			if sj != nil && sj.Jointype == lk.jointype &&
				sj.SynLefthand == lk.preserved && sj.SynRighthand == lk.nullable {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// deriveOuterLinkConstants is `reconsider_outer_join_clauses` (equivclass.c)
// for the admitted LEFT links: for each ON conjunct `pres = null` — bare
// column references of one type, one on the link's preserved side and one on
// its nullable side — whose preserved column is equated to a constant by the
// searched conjunct list, it synthesises `null = const` for the nullable side.
//
// Soundness (the same argument `deriveConstAcrossJoinEquality` makes for the
// syntactic tree, restated for the seam): a nullable-side row can only MATCH a
// preserved row on which `pres = null` holds, and every preserved row that
// survives to the join satisfies `pres = const`, so a nullable row with
// `null <> const` was headed for no match at all. Filtering it out before the
// join removes only rows that produced nothing, and the preserved rows they
// would not have matched are null-extended exactly as before. That is why the
// derived conjunct may sit BELOW the outer join where the original constant may
// not.
//
// Two placements are fail-closed here, because the conjunct's only correct
// destination is the nullable LEAF:
//
//   - it is produced only when `partitionConjunctsForJoinPlanning` will make it
//     leaf-local (`conjunctIsLocalEligible` and a single attributable table —
//     the same two tests `outerOnQualsOK` applies to a nullable-side-only ON
//     conjunct). A conjunct the partition would hand to the join list could
//     end in the residual `Filter` above the tree, where it would drop the
//     null-extended rows — so it is simply not derived;
//   - the preserved column must not reach ANY admitted link's nullable side
//     (stacked LEFT links: the upper link's preserved side contains the lower
//     link's nullable one). The constants in `conjuncts` cannot name such a
//     column today (a WHERE conjunct reaching a nullable side is held above),
//     but the derivation states its own precondition rather than relying on
//     the caller's.
//
// Like the closure it extends it is deterministic in the link and conjunct
// order it was given, so the synthesised list is reproducible run to run.
func deriveOuterLinkConstants(links []outerChainLink, conjuncts []Expr, cumOffsets []int) []Expr {
	if len(links) == 0 {
		return nil
	}
	constByIdent := make(map[columnIdent]Expr)
	for _, c := range conjuncts {
		cr, konst, ok := isColumnRefConstEquality(c)
		if !ok {
			continue
		}
		if _, dup := constByIdent[identOf(cr)]; !dup {
			constByIdent[identOf(cr)] = konst
		}
	}
	if len(constByIdent) == 0 {
		return nil
	}
	var anyNullable RelSet
	for _, lk := range links {
		anyNullable |= lk.nullable
	}
	var out []Expr
	seen := make(map[columnIdent]bool)
	for _, lk := range links {
		for _, c := range splitAnd(lk.pred) {
			a, b, ok := isColumnRefEquality(c) // same-type bare refs only
			if !ok {
				continue
			}
			ra, okA := relidsOfExpr(a, cumOffsets)
			rb, okB := relidsOfExpr(b, cumOffsets)
			if !okA || !okB || ra == 0 || rb == 0 {
				continue
			}
			var pres, null *ColumnRef
			var rpres RelSet
			switch {
			case relsSubset(ra, lk.preserved) && relsSubset(rb, lk.nullable):
				pres, null, rpres = a, b, ra
			case relsSubset(rb, lk.preserved) && relsSubset(ra, lk.nullable):
				pres, null, rpres = b, a, rb
			default:
				continue
			}
			if relsOverlap(rpres, anyNullable) {
				continue
			}
			konst, ok := constByIdent[identOf(pres)]
			if !ok || seen[identOf(null)] {
				continue
			}
			d := &BinaryOp{Op: parser.OpEq, Left: null, Right: konst}
			if !conjunctIsLocalEligible(d) || tableForCol(d, cumOffsets) < 0 {
				continue
			}
			out = append(out, d)
			seen[identOf(null)] = true
		}
	}
	return out
}

// outerOnQualsOK proves, per conjunct, that every admitted outer link's `ON`
// qual will reach a destination that keeps the link's outer-join semantics.
//
// An INNER link's qual may be placed anywhere at or above its join, which is
// the whole licence the seam relies on for `onQuals` (file header). An OUTER
// link's may not: it decides which rows MATCH, so evaluating it above the join
// filters null-extended rows that the join exists to keep (too few rows), and
// evaluating a PRESERVED-side test below the join drops preserved rows that
// should have been null-extended instead (also too few). There are therefore
// exactly two admissible destinations, and each conjunct must land in one:
//
//   - SPANNING (it reaches both sides). It becomes a `restrictInfo` and
//     `clausesFor` applies it at the LOWEST join covering it and touching both
//     sides. That join is the outer join itself and cannot be anything else:
//     `join_is_legal` refuses to unite a nullable-side rel with anything
//     outside the SJ's RHS before the SJ's LHS is complete (joinrels.c:519-529
//     and the `must_be_leftjoin` post-scan at :542-546), so the first join that
//     covers a preserved AND a nullable relation IS the link. The proof that
//     the search will emit it as a clause at all is put to the producer
//     (`searchConsumes`) rather than re-derived — an OR-of-ANDs contributes its
//     common equalities and NOT itself (joinrestrict.go:171-177), and such a
//     conjunct would otherwise fall into the residual `Filter` above the tree,
//     which is precisely the too-few-rows failure above.
//   - NULLABLE-SIDE-ONLY. `t LEFT JOIN p ON p.y > 5` is `t LEFT JOIN (σ p.y>5)
//     p`, so pushing it into `p`'s scan is exact — and that is what
//     `partitionConjunctsForJoinPlanning` does with a single-relation conjunct.
//     It only does so for a LOCAL-ELIGIBLE conjunct that attributes to one
//     binding, so both halves are checked here rather than assumed.
//
// Anything else — a preserved-side-only test, a constant, a qual reaching a
// relation outside the link, an attribution the seam cannot make — declines the
// statement. Each of those is a shape whose correct placement is AT the link,
// and the searched tree has no way to say "at this link and nowhere else".
func outerOnQualsOK(links []outerChainLink, cumOffsets []int) bool {
	for _, lk := range links {
		if lk.pred == nil {
			// A qual-less outer link is a cartesian LEFT join; nothing to
			// place. `planJoinPredicate` does not build one for a parsed
			// `LEFT JOIN … ON`, so this is a shape from some later rewrite,
			// and it is admitted rather than declined only because there is
			// no qual whose placement could be wrong.
			continue
		}
		for _, c := range splitAnd(lk.pred) {
			rs, ok := relidsOfExpr(c, cumOffsets)
			if !ok || rs == 0 || !relsSubset(rs, lk.preserved|lk.nullable) {
				return false
			}
			switch {
			case relsOverlap(rs, lk.preserved) && relsOverlap(rs, lk.nullable):
				if !searchConsumes(c, cumOffsets) {
					return false
				}
			case relsSubset(rs, lk.nullable):
				if !conjunctIsLocalEligible(c) || tableForCol(c, cumOffsets) < 0 {
					return false
				}
			default:
				// Preserved-side-only: `t LEFT JOIN p ON t.x > 5` keeps every
				// `t` row and null-extends the ones failing the test. There is
				// no destination in a searched tree that says that.
				return false
			}
		}
	}
	return true
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
func extractSearchLeaves(node Node) (scans []Node, widths []int, onQuals []Expr, outer []outerChainLink, ok bool) {
	width := 0
	// onSpine marks the path from the chain's ROOT down through LEFT links'
	// left inputs — C-04a's "LEFT spine". A LEFT link reached any other way
	// (below an INNER link, or on a LEFT link's own nullable side) is C-04c's
	// scope and is declined here, so the slice admits exactly the shape it was
	// gated on. Descending into an INNER link therefore clears the flag for
	// BOTH of its inputs.
	var walk func(n Node, onSpine bool) bool
	walk = func(n Node, onSpine bool) bool {
		if n == nil {
			return false
		}
		j, isJoin := n.(*Join)
		if !isJoin || (j.Type != JoinTypeCross && j.Type != JoinTypeInner && j.Type != JoinTypeLeft) {
			scans = append(scans, n)
			widths = append(widths, len(n.Output()))
			width += len(n.Output())
			return true
		}
		if j.Type == JoinTypeLeft && !onSpine {
			return false
		}
		// The link's own coordinate origin: the leaves to its left have already
		// been counted, and its qual was resolved against a schema that starts
		// at its leftmost leaf, so this is the delta between the two spaces.
		base := width
		loLeft := len(scans)
		if !walk(j.Left, onSpine && j.Type == JoinTypeLeft) {
			return false
		}
		loRight := len(scans)
		if !walk(j.Right, false) {
			return false
		}
		hiRight := len(scans)
		if j.Type == JoinTypeLeft {
			// The non-zero-offset decline extends to admitted outer links for
			// the reason it exists on INNER ones (file header): a misattributed
			// coordinate is a wrong answer, not a lost plan. It is checked even
			// though the SIDES below are leaf-index ranges (which are global and
			// need no re-basing), because the link's own `ON` qual does need it.
			if j.Predicate != nil && base != 0 {
				return false
			}
			outer = append(outer, outerChainLink{
				jointype:  parser.JoinLeft,
				preserved: leafRangeRelSet(loLeft, loRight),
				nullable:  leafRangeRelSet(loRight, hiRight),
				pred:      j.Predicate,
			})
			return true
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
	if !walk(node, true) {
		return nil, nil, nil, nil, false
	}
	return scans, widths, onQuals, outer, true
}

// outerChainLink is one OUTER link `extractSearchLeaves` admitted into the
// flattened chain: which leaves it preserves, which it null-extends, and the
// `ON` qual it was written with. C-04a builds these for LEFT links only.
//
// The sides are LEAF-INDEX relsets — bit i is the i'th leaf the walk appended,
// which is the FROM-binding index (03 §6.1's leaf-numbering guarantee) and
// therefore the same space `relidsOfExpr(…, cumOffsets)` answers in. That is
// what lets the seam decide, per conjunct, whether a qual reaches a nullable
// side without re-deriving the chain.
type outerChainLink struct {
	jointype             parser.JoinType
	preserved, nullable  RelSet
	pred                 Expr
}

// leafRangeRelSet is the relset of the half-open leaf range [lo, hi).
func leafRangeRelSet(lo, hi int) RelSet {
	var rs RelSet
	for i := lo; i < hi; i++ {
		rs |= 1 << uint(i)
	}
	return rs
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
	// C-04a: it descends exactly the links `extractSearchLeaves` flattens,
	// which now includes LEFT. An admitted LATERAL outer link must not reorder
	// across its dependency any more than an inner one may, and the marker
	// lives on the chain node the flattening discards.
	if j, ok := n.(*Join); ok && (j.Type == JoinTypeCross || j.Type == JoinTypeInner || j.Type == JoinTypeLeft) {
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
