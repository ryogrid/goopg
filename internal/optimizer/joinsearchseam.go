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
// the (now retired, take3 C-06) `GOOPG_PGSHAPED_COLLAPSE` on OR off, which is
// why the collapse flip was measured as a no-go about a flag that could not
// move a plan (09 §3.18).
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
// C-04a/b/c: an outer link is no longer peeled at all on the shapes those
// slices admit. LEFT (C-04a) and RIGHT (C-04b, recorded as the LEFT join it
// reduces to) enter the flattened chain as LINKS of the search problem, and
// C-04c removed the last positional restriction — a link below an INNER one,
// or on a non-first comma FROM item, is admitted by the same per-link
// machinery. `splitOuterSpine` still exists for what remains pinned — since
// C-06 retired `GOOPG_PGSHAPED_COLLAPSE`, that is FULL and nothing else.
//
// Four shapes are declined and each decline is a correctness statement, not a
// tuning knob:
//
//   - a chain carrying a join type the search has no jointype-aware producer
//     for: FULL. `extractSearchLeaves` stops at it and returns that node as a
//     leaf, so the leaf count disagrees with the prefix's relation count and
//     the statement falls back to the syntactic shape. `makeRelFromJoinlist`
//     declines it a second time, from the joinlist side (P5.9-s), so a shape
//     that slipped past the walk cannot be planned as an inner join by
//     accident;
//   - an outer link inside ANOTHER outer link's nullable side. C-04c admitted
//     this shape, measured it, and put the decline back: `(a LEFT JOIN b)
//     RIGHT JOIN c` returned rows PG does not, because
//     `buildJoinRelRestrictList` re-applies the LOWER link's own `ON` clause at
//     the upper join as an outer-join filter clause — its relids are a subset
//     of the upper SJI's nullable hand, and goopg re-scans one flat clause list
//     per pair where upstream removes an applied clause from the per-rel
//     `joininfo` lists. The clause then filters the rows the upper join exists
//     to null-extend. Ledger `c04c-nested-outer-refilters-lower-on-qual`;
//   - an INNER link's `ON` conjunct that reaches the NULLABLE side of an
//     admitted outer link BELOW it. An inner qual may be placed anywhere at or
//     above its OWN join, and until C-04c every admitted inner link sat below
//     every admitted outer one, so that licence implied "at or above every
//     outer link" for free. It does not once an outer link can sit below an
//     inner one: `partitionConjunctsForJoinPlanning` has no nullable-side
//     guard, so a single-relation conjunct becomes a leaf-local filter
//     evaluated below the join that produces the NULLs, and a spanning one can
//     be placed at a join inside the nullable side. Upstream's answer is
//     `check_outerjoin_delay`'s `required_relids` widening, which goopg's
//     `restrictInfo` has no field for; holding the conjunct above instead
//     would remove the only join clause between two halves of the problem, a
//     cross product where the statement wrote a join. So the shape declines —
//     which is exactly its pre-C-04c verdict — and the widening is ledgered;
//   - an `ON` qual the seam cannot re-base into the statement's coordinates.
//     `planFromItem` resolves a chain's quals in that ITEM's coordinates
//     (`mergedCtx` is built from the item's own `leftCtx`), while
//     `planFromClause` shifts only the BINDINGS when it crosses items, so a
//     non-first comma item's qual is written `base` columns low. C-04c
//     re-bases it with `rebaseChainQual`, and the reason that is safe when
//     the header's earlier judgement said it was not is the REWRITER: the
//     shift is built on `cloneExprRefs`, whose child-slot primitive a
//     build-time gate keeps exhaustive over all 32 Expr types and which
//     ABORTS on one it does not know, where `shiftColumnRefsBy` (13 arms of
//     32) answers `return e` and would leave a ColumnRef reading the wrong
//     column. A qual carrying an inner plan is declined outright — a
//     subquery's coordinate space is not this one;
//   - a LATERAL item, whose rows depend on an item to its left. The search
//     chooses an order, so admitting one would be a wrong answer, not a slow
//     one. The flattened leaf list cannot see the marker — it lives on the
//     chain node — so the chain is walked for it explicitly;
//   - bindings whose offsets do not agree with the concatenation of the leaf
//     widths. Every coordinate in the clause list is written in that one space
//     (03 §6.2), so a disagreement means the caller's map is not the map the
//     search would use.
//
// What the joinlist does with an admitted chain is the deconstruction's
// business, not this walk's: an INNER `JoinExpr` chain flattens into one
// problem and its order is searched (`joinPinned`, collapse.go). Until take3
// C-06 retired `GOOPG_PGSHAPED_COLLAPSE` the flag could instead pin each INNER
// link into its own two-member subproblem, so the written order survived and
// only the PATHS were chosen. Both regimes reached the search, which is what
// makes 03 §6's
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
	// E-21 Cut 1 (onerelsearch.go): the floor is `minSearchRels()`, which is 2
	// historically and 1 under `GOOPG_ONEREL_SEARCH`.
	//
	// The comment this replaces read "One relation is not a search
	// (`make_rel_from_joinlist` returns the item)". That is true of the join
	// ORDER and false of everything else: upstream runs
	// `set_base_rel_pathlists` (allpaths.c:221) BEFORE
	// `make_rel_from_joinlist` (allpaths.c:226), so the rel its
	// `levels_needed == 1` branch returns already carries its `pathlist` AND
	// its `partial_pathlist`. Declining here is what leaves a single-table
	// statement with no `RelOptInfo` at all — no access-method comparison and,
	// the reason E-21 exists, no partial path for `generateUsefulGatherPaths`
	// to read. See DESIGN §1.1-§1.2.
	//
	// Past `maxSearchRels` the RelSet cannot address the problem at all, and
	// the joinlist's own leaf indices would exceed the clause list's bit width.
	//
	// M0145-0004: a UNION ALL appendrel's member scope carries
	// ctx.appendrelMember — the member needs its searched rel's
	// PartialPathlist for the parent SETOP rel's partial arms, exactly
	// the hole E-21 names below. Lower the one-relation floor for it the
	// same way GOOPG_ONEREL_SEARCH does globally; the flag is set only
	// inside planSelectImpl for a scope that arrived as a marked union's
	// member, so this is the jointree arm alone.
	//
	// M0145-0005 slice 4: the jointree arm takes the PG-faithful value
	// unconditionally — `make_one_rel` calls `set_base_rel_pathlists`
	// (allpaths.c:221) for every base rel before `make_rel_from_joinlist`
	// ever counts items, so a one-item joinlist is not a reason to skip
	// base-rel path generation. GOOPG_ONEREL_SEARCH keeps governing the
	// legacy arm alone; on this arm the floor is 1 whether or not the
	// knob is exported.
	floor := minSearchRels()
	if (jointreePipeline || ctx.appendrelMember) && floor > 1 {
		floor = 1
	}
	if nrels < floor || nrels > maxSearchRels || len(ctx.joinlist) == 0 {
		traceSeamDecline("size-or-no-joinlist", nrels, len(ctx.joinlist))
		return node, pred, false
	}
	// M0127-P5.9-s: peel the pinned outer spine off the top and search what is
	// below it. With no outer link this is the identity — `chain == node`,
	// `spine` empty, `jl == ctx.joinlist` — so the shapes P5.9-r already searched
	// take exactly the path they took before.
	// M0145-0005 slice 1 (m0145-0005 design doc §"Slice 1"): the jointree
	// arm does not peel at all — the search consumes the whole node tree
	// and the unpeeled joinlist in one pass. Since C-04a/b relaxed LEFT and
	// RIGHT to collapse-dependent, `joinPinned` pins only FULL (plus a
	// LEFT/RIGHT stacked over a FULL pin), and `spineLinkSearchable` never
	// certifies FULL — so a certified non-empty spine is unreachable today:
	// every reachable statement either has no pinned items (the flat
	// LEFT/RIGHT case, already searched whole through `outerChainLink`s and
	// `joinIsLegal`) or carries a pin the search cannot build, which
	// declines at `extractSearchLeaves`' leaf-count check or at
	// `makeRelFromJoinlist`'s `pinnedUnsearchable` arm instead of here —
	// the same `used=false` fall-back to the syntactic tree, one gate
	// earlier in the machinery the arm is retiring. `splitOuterSpine`,
	// `spineLinkSearchable`, `prefixNullable` and the splice below stay for
	// the legacy arm until the M0145-0008 cutover.
	var chain Node
	var spine []*Join
	var jl joinlist
	var ok bool
	if jointreePipeline {
		chain, spine, jl, ok = node, nil, ctx.joinlist, true
	} else {
		chain, spine, jl, ok = splitOuterSpine(node, ctx.joinlist)
	}
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
	// M0145-0005 slice 2: when the WHERE-clause pull-up ran, `jl`'s
	// trailing leaf items are the pulled bodies' own relations —
	// numbered into the joinlist by pullUpSublinksIntoJointree in
	// binding order, not walk-derived. `nReal` counts the EMITTING
	// (ctx.bindings-backed) leaf items; the pulled leaves occupy the
	// problem's LAST band [nprefix-nPulled, nprefix) — real searched
	// items with no bindings entry — and chain-extracted synthetic
	// leaves still land at-or-above nprefix in the tail. `nPulled` is
	// that trailing count.
	//
	// M0145-0005 slice 6 (jointree arm): the joinlist may ALSO carry
	// deferred chain semi/anti leaf items — real searched items that
	// emit no columns — in the band between the emitting items and the
	// pulled tail. `ctx.bindings` counts emitting leaves only, so the
	// deferred count is `nprefix - nPulled - nrels` and the
	// bindings-backed ("real") count is then exactly nrels.
	nReal := nprefix
	if pu := ctx.jtPullup; pu != nil {
		nReal -= pu.nLeaves
	}
	nPulled := nprefix - nReal
	nChain := 0
	if jointreePipeline {
		nChain = nprefix - nPulled - nrels
		if nChain < 0 {
			traceSeamDecline("prefix-exceeds-bindings", nrels, nprefix)
			return node, pred, false
		}
		nReal = nrels
	}
	// R41/K75: fail closed when the joinlist claims MORE relations than the
	// statement has bindings. `ctx.bindings[:nReal]` below would be an
	// index-out-of-range PANIC, and until R41 the `leaf-count` check was the
	// only thing that happened to prevent it (measured on TPC-DS Q78:
	// len(bindings)=2, nprefix=3).
	//
	// The two numbers come from SEPARATE computations — `demotedForPlan`
	// decides the plan tree's join types while the in-place
	// `reduceOuterJoins(s.FromExprs, …)` decides the joinlist's — so a future
	// divergence about which links became SEMI/ANTI would desynchronise the
	// numbering again. This turns that into a decline instead of a crash.
	//
	// NOT an equality check: a peeled outer spine legitimately leaves the
	// prefix narrower than the full binding list, which is the normal
	// admitted case. Pulled leaf items likewise sit past the binding
	// count — they emit no columns — so the comparison runs on `nReal`,
	// the bindings-backed count, not `nprefix`.
	if nReal < 0 || nReal > nrels {
		traceSeamDecline("prefix-exceeds-bindings", nrels, nprefix)
		return node, pred, false
	}
	if nprefix < floor && len(spine) == 0 {
		// UNDER a spine a one-relation prefix is already planned
		// (M0134-0188): there is no order to choose, but there IS an access
		// method — base-rel path generation runs, `add_path` picks among
		// seq / index / index-only, and the boundary republishes binding
		// order exactly as for a wider prefix. `a LEFT JOIN b`'s left side is
		// the one place PG chooses a covering scan that no other goopg seam
		// could reach (TPC-H Q13).
		//
		// E-21 Cut 1 extends that to a one-relation prefix with NO spine —
		// i.e. a plain single-table statement — under `GOOPG_ONEREL_SEARCH`,
		// because the argument above never depended on the spine. Upstream
		// runs base-rel path generation for a one-relation query too
		// (`set_base_rel_pathlists`, allpaths.c:221, called unconditionally
		// from `make_one_rel` before the joinlist is looked at). With the
		// knob off this is the historical decline, unchanged.
		//
		// M0145-0004 lowers the same floor for an appendrel member scope
		// (`ctx.appendrelMember` set by planSelectImpl): the member's
		// searched rel is the PartialPathlist carrier the parent SETOP
		// rel's partial arms read, so a single-leaf member needs base-rel
		// path generation exactly as `GOOPG_ONEREL_SEARCH` argues.
		traceSeamDecline("prefix-size", nrels, nprefix)
		return node, pred, false
	}
	if lo, hi, okRange := jl.leafRange(); !okRange || lo != 0 || hi != nprefix {
		traceSeamDecline("prefix-not-a-prefix", nrels, nprefix)
		return node, pred, false
	}
	// Semi/Anti chain admission is unconditional (M0142-0008a-3i-plumbing-b2
	// step (iii), design doc §33.4/§34; M0145-0005 slice 2 retired the
	// `admitSemiAnti` flag — safe unconditionally at this ONE call site
	// (§32.1's finding) since the other 3 `tryJoinSearch` callers' chains
	// are always captured pre-unnest and can never contain a Semi/Anti
	// node — only Phase B's second call (`predp.go`, `chain ==
	// spineJoins[0]`, reached post-unnest) can ever hand this a tree that
	// actually has one. Phase A's own call (`chain == origChain`, captured
	// before `unnestSubqueriesInPlan` runs) stays structurally unable to
	// contain one, per §28.3's finding.
	//
	// M0145-0005 slice 3 (jointreescope.go): on the jointree arm the
	// leaf/link record planFromItem emitted at construction time is the
	// extraction source — `extractScopeLeaves` rebuilds the identical
	// tuple from it. The `root == chain` pin keeps the node walk for any
	// chain the table was not built beside (the S5a post-unnest Phase B
	// chain, or a pre-search rewrite that grafted a different root).
	var scans []Node
	var widths []int
	var onQuals []chainOnQual
	var outerLinks []outerChainLink
	var semiAnti []semiAntiChainLink
	if jointreePipeline && ctx.jtScope != nil && ctx.jtScope.root == chain {
		scans, widths, onQuals, outerLinks, semiAnti, ok = extractScopeLeaves(ctx.jtScope, ctx.joinInfoList)
	} else {
		scans, widths, onQuals, outerLinks, semiAnti, ok = extractSearchLeaves(chain)
	}
	if !ok {
		traceSeamDecline("chain-not-flattenable", nrels, len(scans))
		return node, pred, false
	}
	// walkWidths is the walk-order leaf-width table BEFORE the pulled
	// splice — the coordinate space the walk rebased the extracted links'
	// preds and body quals into. `widths` below gains the pulled leaves
	// spliced in at [nReal, nprefix), so the chain-qual remap further down
	// must read this pre-splice table and translate leaf indexes across
	// the insertion (`remapWalkOrderFlatToSpans`'s nReal/nPulled args).
	walkWidths := append([]int(nil), widths...)
	// M0145-0005 slice 2: the jointree pipeline's pulled sublink bodies
	// are REAL leaf items — numbered into `jl` by
	// pullUpSublinksIntoJointree, occupying [nReal, nprefix) — so their
	// scans splice into the same positions here, ahead of the
	// chain-extracted synthetic tail, and every extracted leaf index
	// shifts up by nPulled. No semiAntiChainLink is built for them: the
	// joinlist carries the items, classifyPulledQuals threads the quals
	// and SJInfos once `spans` exists. On the legacy pipeline
	// ctx.jtPullup is always nil and this is a no-op.
	if ctx.jtPullup != nil && ctx.jtPullup.nLeaves > 0 {
		if !splicePulledLeaves(ctx.jtPullup, nReal, nprefix, ctx, &scans, &widths, &semiAnti, &outerLinks, &onQuals) {
			traceSeamDecline("pullup-splice", nrels, len(scans))
			return node, pred, false
		}
	}
	// §31.3 item 1 (design doc §31): with synthetic (Semi/Anti RHS) leaves
	// mixed into `scans`, the leaf count is real-FROM-items-plus-synthetic,
	// not just real-FROM-items — `nprefix` itself stays unchanged, since a
	// synthetic leaf is never a real FROM item. `semiAnti` is empty for
	// every Phase A call (`chain == origChain`, never has a Semi/Anti node)
	// and populated only for a Phase B call that actually admits one, per
	// step (iii)'s flip above.
	// M0142-0008a-3i-route-a step 2: a FLATTENED link contributes one
	// synthetic leaf per decomposed body relation, so the expected count
	// is the popcount of every link's `rhs` — not `len(semiAnti)`, which
	// counted one per link back when every RHS was one opaque leaf.
	nSynthetic := 0
	realLeafBits := RelSet(0)
	for _, lk := range semiAnti {
		if lk.realLeaf {
			// A chain-extracted link whose RHS is a real joinlist leaf
			// item (jointree arm, M0145-0005 slice 6): `nprefix` already
			// counts it — it contributes no synthetic tail leaf.
			realLeafBits |= lk.rhs
			continue
		}
		for r := lk.rhs; r != 0; r >>= 1 {
			nSynthetic += int(r & 1)
		}
	}
	// The deferred chain leaves the joinlist numbered and the ones the
	// scope extraction produced must agree leaf for leaf — the two come
	// from separate passes over the same links (deconstructJointreeScopedSJI
	// vs extractScopeLeaves), so a mismatch is a desync, never a shape.
	if realLeafBits != 0 && relLevel(realLeafBits) != nChain {
		traceSeamDecline("chain-leaf-desync", nrels, len(scans))
		return node, pred, false
	}
	if len(scans) != nprefix+nSynthetic {
		traceSeamDecline("leaf-count", nrels, len(scans))
		return node, pred, false
	}
	// c2's construction contract: every synthetic leaf occupies a TAIL
	// slot — `scans[nprefix:]` IS the synthetic set — so `jl`'s leaf
	// items (which name exactly [0,nprefix)) always resolve to real
	// leaves, `ctx.bindings[:nprefix]` pairs with them, and each
	// synthetic slot gets its own span-offset binding below. A chain
	// where a REAL leaf follows a synthetic one in walk order —
	// `web_sales ANTI web_returns JOIN date_dim`, the demoted-ANTI
	// shape — violates that: the real leaf would land in the
	// synthetic tail and take an out-of-band span binding while a
	// `jl` leaf item resolves to the synthetic leaf, and a join
	// clause referencing the synthetic leaf's span coordinate then
	// reaches createPlan on a real-only layout (Q78's panic). The
	// on-qual gates used to decline such chains before construction;
	// admitting them is real future work (leaf reorder + relset
	// remap), not a guard to relax — filed as M0145-0016 (owner
	// directive 2026-09-21); check M0145-0005 slice (a) subsumption
	// first. Unnest-produced Semi/Anti links
	// always sit at the TOP of the FROM chain, so their synthetic
	// leaves are always the tail — this declines nothing the splice
	// emits.
	syntheticBits := RelSet(0)
	for _, lk := range semiAnti {
		if lk.realLeaf {
			// Deferred-band link: `rhs` is a real joinlist leaf index —
			// already in canonical (non-synthetic) position.
			continue
		}
		syntheticBits |= lk.rhs
	}
	// M0145-0016: a chain whose synthetic leaves are NOT already the tail is
	// repaired by a stable partition rather than declined.
	leafPerm := identityLeafPerm(len(scans))
	if syntheticBits != leafRangeRelSet(nprefix, len(scans)) {
		traceSeamNotTail(syntheticBits, nprefix, len(scans))
		if nPulled != 0 {
			// Scope bound: with pulled leaves ALSO spliced in, the
			// non-synthetic set is itself two populations (emitting FROM
			// items and non-emitting pulled bodies) whose relative order the
			// splice established at `nReal`. A single stable partition does
			// not preserve that three-way split, and no corpus shape
			// exercises the combination, so it stays declined rather than
			// guessed at.
			traceSeamDecline("semianti-not-tail-with-pulled", nrels, len(scans))
			return node, pred, false
		}
		leafPerm = stableSyntheticTailPerm(syntheticBits, len(scans))
		applyLeafPerm(leafPerm, &scans, &widths, semiAnti, outerLinks, onQuals)
		syntheticBits = permuteRelSet(syntheticBits, leafPerm)
		if syntheticBits != leafRangeRelSet(nprefix, len(scans)) {
			// The partition is total by construction; a mismatch here means
			// the synthetic set and `nprefix` disagree about how many leaves
			// are real, which is a numbering desync, not a shape to plan
			// around.
			traceSeamDecline("semianti-perm-desync", nrels, len(scans))
			return node, pred, false
		}
	}
	// A flattened body can push the problem past the relset bit width even
	// when the statement's own FROM list fits — `RelSet` bits are leaf
	// indexes, so a synthetic leaf at index ≥ maxSearchRels would be
	// unrepresentable in joinIsLegal's masks.
	if len(scans) > maxSearchRels {
		traceSeamDecline("leaf-count-overflow", nrels, len(scans))
		return node, pred, false
	}
	if chainCarriesLateral(chain) {
		traceSeamDecline("lateral", nrels, len(scans))
		return node, pred, false
	}
	for i := range scans {
		if scans[i] == nil {
			traceSeamDecline("nil-leaf", nrels, len(scans))
			return node, pred, false
		}
	}
	// §31.3 items 2-3: `pgShapedOffsetChecksOK`'s synthetic-aware arithmetic
	// reduces to the old plain per-index comparison and REAL-total-width
	// spine check whenever `semiAnti` is empty (every Phase A call), and
	// exercises the numSynthetic>0 case for a Phase B call that admits one.
	// See the function's own doc comment for details.
	spans := buildLeafSpans(widths, semiAnti)
	// M0142-0008a-3i-plumbing-c20 (design doc §56): each `semiAnti[i].pred`
	// left `extractSearchLeaves`'s walk already rebased into WALK-ORDER flat
	// column offsets (leaf i's columns start at the sum of `widths[0:i]`,
	// visit order) — correct as its own coordinate space, but not the one
	// `relidsOfExpr`/`searchConsumes` read: those consult `spans`, which
	// `buildLeafSpans` just built in a DIFFERENT space that relocates every
	// synthetic (Semi/Anti RHS) leaf's span out-of-band, after the total
	// width of every REAL leaf (that function's own doc comment, item 3).
	// The two coincide only when no real leaf follows a semiAnti link's
	// opaque RHS in walk order. Q78 (TPC-DS) breaks that: the demoted ANTI
	// join (`web_sales`/`web_returns`) is immediately followed by a plain
	// `JOIN date_dim`, so `date_dim` (real, leaf 2) lands in walk order
	// between the ANTI join's own two leaves and the tree's total-real-width
	// boundary — `buildLeafSpans` gives `date_dim` the walk-order-flat span
	// the ANTI join's synthetic RHS leaf (`web_returns`) actually occupies in
	// walk-order-flat terms, so a folded `LeftKey=RightKey` conjunct whose
	// RHS operand was rebased to that walk-order-flat position resolves, via
	// `spans`, to `date_dim`'s relid instead of `web_returns`'s —
	// `semiAntiOnQualsOK` then declines it as spanning a THIRD, unrelated
	// leaf. Caught live via `GOOPG_C20DEBUG=1` on TPC-DS SF0.25 Q78 (this
	// task's own trace: `rs` for the folded eq conjunct came back spanning
	// leaves {0,2} instead of {0,1}).
	for i := range semiAnti {
		if semiAnti[i].realLeaf {
			// Scope-extracted links are rebased into leaf-span space
			// directly — the canonical emission puts every deferred leaf
			// at its joinlist position, so there is no walk-order flat
			// space to translate out of.
			continue
		}
		remapped, okRemap := remapWalkOrderFlatToSpans(semiAnti[i].pred, walkWidths, spans, nReal, nPulled, leafPerm)
		if !okRemap {
			traceSeamDecline("semianti-pred-remap", nrels, len(scans))
			return node, pred, false
		}
		semiAnti[i].pred = remapped
		// Step 2: pooled body quals ride the same walk-order-flat → spans
		// remap — they were shifted `+rightBase` into walk order beside
		// the link's own RHS leaves, which are exactly the positions the
		// remap relocates out-of-band.
		for k, q := range semiAnti[i].bodyQuals {
			rq, okQ := remapWalkOrderFlatToSpans(q, walkWidths, spans, nReal, nPulled, leafPerm)
			if !okQ {
				traceSeamDecline("semianti-bodyqual-remap", nrels, len(scans))
				return node, pred, false
			}
			semiAnti[i].bodyQuals[k] = rq
		}
	}
	// The offset oracle covers the EMITTING leaves only — pulled leaf
	// items have no ctx.bindings entry, so `pgShapedOffsetChecksOK` skips
	// everything at-or-above nReal (its `nonEmitting` argument covers
	// both pulled leaves and the extracted synthetic tail).
	bindingOffsets := make([]int, nReal)
	for i := range nReal {
		bindingOffsets[i] = ctx.bindings[i].offset
	}
	// The spine's first relation must begin exactly where the prefix ends. That
	// is what makes "beyond the prefix window" and "on the spine" the same
	// statement, which every conjunct decision below relies on: a spine column
	// that landed INSIDE the window would be attributed to a prefix leaf and
	// pushed under the outer join. `nReal` is the bindings-backed edge —
	// pulled leaves emit nothing, so a spine begins only where the REAL
	// items run out.
	hasSpine := nReal < nrels
	spineOffset := 0
	if hasSpine {
		spineOffset = ctx.bindings[nReal].offset
	}
	if reason, checksOK := pgShapedOffsetChecksOK(spans, leafRangeRelSet(nReal, len(scans)), widths, bindingOffsets, hasSpine, spineOffset); !checksOK {
		if reason == "offset-disagreement" {
			traceSeamDecline(reason, nrels, len(scans))
		} else {
			traceSeamDecline(reason, nrels, nprefix)
		}
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
	// M0145-0003: pulled sublink conjuncts never reach this pool — their
	// semantics live in the semi/anti SJInfos and pooled quals
	// `classifyPulledQuals` emits below, so feeding them to the
	// partition would evaluate the sublink a second time beside the
	// join. On the legacy pipeline `predConjuncts` is exactly
	// `splitAnd(pred)`.
	predConjuncts := splitAndExcludingPulled(pred, ctx.jtPullup)
	var conjuncts, heldAbovePrefix []Expr
	switch {
	case prefixNullable(spine):
		heldAbovePrefix = predConjuncts
	case len(outerLinks) == 0:
		conjuncts = predConjuncts
	default:
		var nullable RelSet
		for _, lk := range outerLinks {
			nullable |= lk.nullable
		}
		for _, c := range predConjuncts {
			rs, attributable := relidsOfExpr(c, spans)
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
	// C-04c: an INNER link's `ON` conjunct that reaches the NULLABLE side of an
	// admitted outer link BELOW it declines the statement.
	//
	// The licence the file header states for an inner qual — "anywhere at or
	// above its join" — is about the qual's OWN join, and until C-04c every
	// admitted inner link sat below every admitted outer one, so that licence
	// implied "at or above every outer link" for free. It does not any more.
	// `partitionConjunctsForJoinPlanning` has no nullable-side guard, so a
	// single-relation conjunct on the nullable side becomes a leaf-local filter
	// evaluated BELOW the join that produces the NULLs (`… JOIN c ON b.y IS
	// NULL` over `a LEFT JOIN b` then selects `b` rows instead of unmatched
	// `a` rows), and a spanning one can be placed at a join inside the nullable
	// side when the search is free to build one.
	//
	// The correct answer is upstream's `required_relids` widening: the qual is
	// delayed to the lowest join covering the outer link's own hands. goopg's
	// `restrictInfo` has no such field, and holding the conjunct in the residual
	// `Filter` instead is correct but removes the only join clause between two
	// halves of the problem — a cross product where the statement wrote a join.
	// So the shape DECLINES, which is exactly the behaviour it had before C-04c
	// (the whole statement fell back to the syntactic tree), and the widening is
	// ledgered as the resume point.
	for _, q := range onQuals {
		for _, c := range splitAnd(q.pred) {
			if q.belowNullable != 0 {
				rs, attributable := relidsOfExpr(c, spans)
				if !attributable || relsOverlap(rs, q.belowNullable) {
					traceSeamDecline("inner-on-qual-above-outer", nrels, nprefix)
					return node, pred, false
				}
			}
			conjuncts = append(conjuncts, c)
		}
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
	// R51: the search takes the FULL closure, constants plus transitive
	// `a = c` (PG's `generate_join_implied_equalities`, equivclass.c) —
	// the derived clauses re-open join orders the DP otherwise cannot
	// reach (K26: `{part}|{partsupp}` on TPC-H Q9). The old deferral
	// ("reshapes plans broadly", via TestPreDPPinnedSemiKeysResolveAfterDP)
	// was re-measured in K26 §§7–9: the failure was a type-pinning test
	// helper (fixed via `nliProbeKeys`, kept), and the remaining blast
	// radius was 2 keep-assertions adjudicated against PG. Join-order may
	// still not fall — the DP must also CHOOSE PG's order (costing half,
	// K26 §9.2) — but the candidate half is now open.
	if synth := inferTransitiveEqualities(conjuncts); len(synth) > 0 {
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
		if !outerOnQualsOK(outerLinks, spans) {
			traceSeamDecline("outer-on-qual", nrels, nprefix)
			return node, pred, false
		}
		// C-04b: an INNER link's `ON` qual is normally free to land anywhere
		// at or above its join, and the residual `Filter` above the whole
		// tree is one of those places. Under a RIGHT link's nullable side it
		// is not — see `innerOnQualsBelowNullableOK`.
		if !innerOnQualsBelowNullableOK(onQuals, outerLinks, spans) {
			traceSeamDecline("inner-on-qual-under-nullable", nrels, nprefix)
			return node, pred, false
		}
		// Derived BEFORE the ON conjuncts join the list: the constants it reads
		// are then exactly the ones the closure above could see, none of which
		// sits on a nullable side (a WHERE conjunct reaching one was held
		// above, and the ON conjuncts are not in the list yet).
		derived := deriveOuterLinkConstants(outerLinks, conjuncts, spans)
		conjuncts = append(conjuncts, onOuter...)
		conjuncts = append(conjuncts, derived...)
	}
	// M0142-0008a-3i-plumbing-c5 (design doc §36, gap 5): mirrors the
	// `outerLinks` FAIL-CLOSED gate above, now that c2-c4 give the search a
	// real leaf and a real SpecialJoinInfo for every semiAnti link.
	// `semiAntiLinksHaveSJInfos` is the same safety argument as
	// `outerLinksHaveSJInfos`: `joinIsLegal` only refuses to reorder across
	// the synthetic leaf if `ctx.joinInfoList` actually carries its
	// SpecialJoinInfo (c4's contribution), so this checks the production
	// caller did populate it rather than assuming so. `semiAntiOnQualsOK`
	// is `outerOnQualsOK`'s narrower SEMI/ANTI analogue (no preserved/
	// nullable placement question, see its own doc comment) and declines
	// any link whose ON predicate the search cannot fully attribute and
	// place. Both run BEFORE the link's conjuncts join `conjuncts` below,
	// so a decline here never lets an un-placeable semiAnti clause reach
	// the search at all.
	if len(semiAnti) > 0 {
		// M0142-0008a-3i-plumbing-c6 (design doc §41, gap named by c5's §40.3/
		// §40.5): threads each semiAnti link's `*SpecialJoinInfo` into
		// `ctx.joinInfoList` itself, from the one point in the pipeline that
		// runs AFTER `unnestExistsExpr`/`existsUnnestSJInfo` builds it AND
		// after `extractSearchLeaves`'s walk (line ~1323 above) has already
		// renumbered it from the synthetic synL=1/synR=2 placeholder to real
		// leaf-index bits — `deconstructJointreeScopedSJI` (planner.go:3051)
		// cannot do this itself, since it runs on `s.FromExprs` before
		// unnesting exists (§40.3). This mirrors upstream PG's own ordering:
		// `deconstruct_jointree` (which builds `root->join_info_list`) always
		// runs AFTER `pull_up_sublinks` has already rewritten EXISTS/NOT
		// EXISTS into the jointree's semi/anti JoinExpr nodes, so PG's list
		// is populated from the SAME already-unnested tree its legality
		// checks consult — there is no second, independent pipeline to
		// desynchronise from. `joinInfoListHas` (relfromjoinlist.go:408, by
		// pointer identity) guards against a duplicate append if this seam
		// ever runs more than once against the same `ctx` for one statement.
		// A link whose source `*Join` never carried an SJInfo contributes
		// nothing here and so still correctly fails the gate below — the
		// EXISTS and both IN/NOT-IN unnesting paths all set `.SJInfo` now
		// (existsUnnestSJInfo; M0142-0008-producer's inUnnestSJInfo on
		// unnestInExpr/unnestNonCorrelatedInExpr), so the only joins left
		// without one are `JoinTypeInner` splices that never enter the
		// semiAnti arm anyway: this is real production population, not the
		// rejected "check against a list built from itself" pattern §40.3
		// already ruled out.
		for _, lk := range semiAnti {
			if lk.sjinfo != nil && !joinInfoListHas(ctx.joinInfoList, lk.sjinfo) {
				ctx.joinInfoList = append(ctx.joinInfoList, lk.sjinfo)
			}
		}
		if !semiAntiLinksHaveSJInfos(semiAnti, ctx.joinInfoList) {
			traceSeamDecline("semianti-link-no-sjinfo", nrels, nprefix)
			return node, pred, false
		}
		if !semiAntiOnQualsOK(semiAnti, spans) {
			traceSeamDecline("semianti-on-qual", nrels, nprefix)
			return node, pred, false
		}
		// Step 2: a flattened link's pooled body quals must be placeable —
		// every conjunct attributable to leaves strictly inside `rhs` and
		// consumable by the search (a body-local restriction or an
		// intra-RHS join clause). Anything else stays an opaque body, the
		// same posture `semiAntiOnQualsOK` takes for the link pred.
		for _, lk := range semiAnti {
			for _, c := range lk.bodyQuals {
				rs, attributable := relidsOfExpr(c, spans)
				if !attributable || rs == 0 || !relsSubset(rs, lk.rhs) || !searchConsumes(c, spans) {
					traceSeamDecline("semianti-body-qual", nrels, nprefix)
					return node, pred, false
				}
			}
		}
	}
	// M0142-0008a-3i-plumbing-c1 (design doc §36, gap 1): a semiAnti link's
	// correlation predicate joins `conjuncts` only HERE, mirroring `onOuter`
	// above. Every split conjunct's relids necessarily span both `lk.lhs`
	// (real leaves) and `lk.rhs` (the synthetic RHS leaf) — c2-c4 now give
	// the search a real leaf, a real SJInfo and coverage in `joinInfoList`
	// for the position, and c5's gate above has already confirmed every
	// conjunct is placeable, so this loop is no longer inert as of c5: the
	// search can now actually visit the synthetic leaf and satisfy relid
	// sets that include it.
	for _, lk := range semiAnti {
		conjuncts = append(conjuncts, splitAnd(lk.pred)...)
		// Step 2: the flattened body's own quals join the same stream —
		// relids ⊆ `rhs` (checked above), so they land either as
		// leaf-local filters or as intra-RHS join clauses, exactly what
		// `distribute_qual_to_rels` does inside PG's pulled-up RHS.
		conjuncts = append(conjuncts, lk.bodyQuals...)
	}
	// M0145-0005 slice 2: the pulled bodies' quals join the pool here —
	// the same point the extracted links' predicates did above.
	// classifyPulledQuals rebases each pulled body's bound quals into
	// problem space and classifies them exactly distribute_qual_to_rels
	// would: spanning correlation conjuncts and body-local conjuncts
	// into `conjuncts` itself, SEMI-only outer-local hoists into
	// `pu.outerQuals` (appended below — the partition lands them on the
	// emitting leaves they read, exactly like a WHERE conjunct of the
	// parent itself). It also appends each body's SpecialJoinInfo to
	// `ctx.joinInfoList` — the pulled leaves are real joinlist members
	// constrained by a real hand, so no link record survives. Already
	// in problem space, so no remap.
	if pu := ctx.jtPullup; pu != nil && pu.nLeaves > 0 {
		if !classifyPulledQuals(pu, nReal, spans, ctx, &conjuncts) {
			traceSeamDecline("pullup-classify", nrels, len(scans))
			return node, pred, false
		}
		conjuncts = append(conjuncts, pu.outerQuals...)
	}
	searchConjuncts, locals := partitionConjunctsForJoinPlanning(conjuncts, spans)
	// M0142-0008a-3i-plumbing-c2 (design doc §36, gaps 2-3): `leaves`/
	// `relInfos`/the bindings handed to the search all grow from `nprefix` to
	// `nprefix+len(semiAnti)` — one extra slot per synthetic Semi/Anti RHS
	// leaf, at the SAME walk position `extractSearchLeaves` gave it
	// (`scans[nprefix:]`, in `semiAnti` order — both are appended in the same
	// walk step, so position `nprefix+k` is `semiAnti[k]`'s own leaf).
	// M0142-0008a-3i-route-a step 2: a flattened link contributes one
	// synthetic leaf per decomposed body relation — `nSynthetic` above —
	// so the count is `nprefix+nSynthetic` (== `len(scans)`), not
	// `nprefix+len(semiAnti)`.
	nleaves := nprefix + nSynthetic
	leaves := make([]Node, nleaves)
	relInfos := make([]baseRelInfo, nleaves)
	bindings := make([]rangeBinding, nleaves)
	copy(bindings, ctx.bindings[:nReal])
	for i, b := range ctx.bindings[:nReal] {
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
	// M0145-0005 slice 2: the pulled leaves at [nReal, nprefix) are REAL
	// joinlist items — numbered in binding order by the pull-up — but
	// emit no columns, so they get span-offset bindings like a flattened
	// chain leaf and the same `estimateBaseRelInfo` +
	// `applyRelSizeFallback` catalog-stats path (flattenPulledBodyTree
	// guarantees each one is a bare `*SeqScan`, so this is a verify, not
	// a discovery — anything else means the body's own planning rewrote
	// it after the marker ran, which is a desync to decline on, not a
	// shape to price).
	//
	// M0145-0011 scope (c) measured the OTHER reading of "anything else":
	// with `GOOPG_PULLUP_CTE_LEAF=on` the producer admits `*CTEScan`
	// leaves and every one of them dies HERE instead, so the two sites
	// are one invariant and must be relaxed together. Whoever relaxes the
	// producer owns this loop too: a derived leaf has no `ss.Table` or
	// `ss.Alias` to build the `rangeBinding` from, and no catalog
	// statistics for `estimateBaseRelInfo`/`applyRelSizeFallback` below.
	// Filed as M0145-0013 (owner directive 2026-09-21).
	//
	// M0145-0005 slice 6: the non-emitting band is TWO populations —
	// deferred chain semi/anti leaves at [nReal, pulledBase) and pulled
	// sublink leaves at [pulledBase, nprefix). The deferred leaves take
	// the same SeqScan-vs-opaque dispatch the synthetic tail uses: a
	// base-relation RHS gets catalog stats, a derived RHS (subquery,
	// tablefunc) gets `EstimateRows` over its plan subtree.
	pulledBase := nprefix - nPulled
	for i := nReal; i < nprefix; i++ {
		scan := scans[i]
		if i < pulledBase {
			// Deferred chain semi/anti leaf — a real joinlist item,
			// non-emitting. Same dispatch as the synthetic tail arm
			// below.
			if _, isScan := scan.(*SeqScan); isScan {
				b, ok := seamLeafBinding(scan, spans[i].lo, nil, 0)
				if !ok {
					traceSeamDecline("chain-leaf-not-scan", nrels, len(scans))
					return node, pred, false
				}
				bindings[i] = b
				var local Expr
				if preds := locals.byBinding[i]; len(preds) > 0 {
					local = combineAnd(preds)
					localized := make([]Expr, 0, len(preds))
					for _, p := range preds {
						localized = append(localized, localizeExprToLeaf(p, b))
					}
					leaves[i] = &Filter{Child: scan, Predicate: combineAnd(localized), LeafLocal: true}
				} else {
					leaves[i] = scan
				}
				relInfos[i] = seamLeafRelInfo(i, b, scan, local, cat)
				relInfos[i].isSemiAntiSyntheticLeaf = true
				continue
			}
			b := rangeBinding{offset: spans[i].lo}
			bindings[i] = b
			var local Expr
			if preds := locals.byBinding[i]; len(preds) > 0 {
				local = combineAnd(preds)
				localized := make([]Expr, 0, len(preds))
				for _, p := range preds {
					localized = append(localized, localizeExprToLeaf(p, b))
				}
				leaves[i] = &Filter{Child: scan, Predicate: combineAnd(localized), LeafLocal: true}
			} else {
				leaves[i] = scan
			}
			baseRows := EstimateRows(scan)
			relInfos[i] = baseRelInfo{
				bindingIdx:              i,
				baseRows:                baseRows,
				filteredRows:            applyLocalFilterSelectivity(baseRows, b, scan, local),
				isSemiAntiSyntheticLeaf: true,
			}
			continue
		}
		b, ok := seamLeafBinding(scan, spans[i].lo, ctx, i-pulledBase)
		if !ok {
			traceSeamDecline("pulled-leaf-not-scan", nrels, len(scans))
			return node, pred, false
		}
		bindings[i] = b
		var local Expr
		if preds := locals.byBinding[i]; len(preds) > 0 {
			local = combineAnd(preds)
			localized := make([]Expr, 0, len(preds))
			for _, p := range preds {
				localized = append(localized, localizeExprToLeaf(p, b))
			}
			leaves[i] = &Filter{Child: scan, Predicate: combineAnd(localized), LeafLocal: true}
		} else {
			leaves[i] = scan
		}
		relInfos[i] = seamLeafRelInfo(i, b, scan, local, cat)
		// The leaf's own coordinates are non-emitting (a SEMI/ANTI join
		// never projects its RHS), so the boundary filler must mark them
		// fillable exactly like the extracted leaves' — while
		// `leafIsDerivedInput` still answers not-derived through the real
		// `table` above, which is the distinction c8 added the flag for.
		// A DERIVED pulled leaf has `table == nil` on purpose and must
		// answer derived there: that is what keeps the `outer-over-derived`
		// firewall in force over it until M0145-0018 lifts it.
		relInfos[i].isSemiAntiSyntheticLeaf = true
	}
	// A synthetic leaf has no `*catalog.Table` — it is an opaque, already-
	// planned subtree (the Semi/Anti join's RHS), not a base relation — so
	// `estimateBaseRelInfo`/`applyRelSizeFallback`'s catalog-stats path is
	// the wrong tool: both read `binding.table`, and with it nil,
	// `estimateTableRowsFallback` returns 0 outright (relsize.go), flooring
	// the leaf at a ZERO row estimate rather than a sane fallback. `scan`
	// itself is a real, already-sized plan subtree, so it is priced with
	// `EstimateRows` — the same general-purpose estimator every other
	// non-base-relation node (Join, Filter, Aggregate, …) already goes
	// through — instead.
	//
	// M0142-0008a-3i-route-a step 2: a FLATTENED link's synthetic leaves are
	// the body's own `*SeqScan`s — real base relations. They get the same
	// `estimateBaseRelInfo` + `applyRelSizeFallback` path as prefix leaves
	// (catalog stats, leaf-local-filter selectivity), which is the whole
	// point of the splice: the search prices them like PG's pulled-up
	// relations instead of an opaque subtree estimate.
	flatLeaf := func(i int) bool {
		bit := RelSet(1) << uint(i)
		for _, lk := range semiAnti {
			if lk.flattened && lk.rhs&bit != 0 {
				return true
			}
		}
		return false
	}
	for i := nprefix; i < nleaves; i++ {
		b := rangeBinding{offset: spans[i].lo}
		scan := scans[i]
		preds := locals.byBinding[i]
		var local Expr
		if len(preds) > 0 {
			local = combineAnd(preds)
		}
		if flatLeaf(i) {
			// The third site of the same invariant (M0145-0013): a
			// flattened link's synthetic leaves come from the SAME
			// `flattenPulledBodyTree` decomposition, so whatever leaf kinds
			// that function admits have to be priceable here too. `ctx` is
			// not consulted for the binding: these leaves are numbered in
			// walk order at [nprefix, nleaves), not in the pull-up's body
			// order, so there is no `bodyBindings` index to hand through.
			var ok bool
			if b, ok = seamLeafBinding(scan, spans[i].lo, nil, 0); !ok {
				traceSeamDecline("flat-leaf-not-scan", nrels, len(scans))
				return node, pred, false
			}
			bindings[i] = b
			leaves[i] = scan
			if local != nil {
				localized := make([]Expr, 0, len(preds))
				for _, p := range preds {
					localized = append(localized, localizeExprToLeaf(p, b))
				}
				leaves[i] = &Filter{Child: scan, Predicate: combineAnd(localized), LeafLocal: true}
			}
			relInfos[i] = seamLeafRelInfo(i, b, scan, local, cat)
			// The leaf's own coordinates are synthetic (a SEMI/ANTI join
			// never projects its RHS), so the boundary filler must mark
			// them fillable exactly like the opaque leaf's — while
			// `leafIsDerivedInput` still answers not-derived through the
			// real `table` above, which is the distinction c8 added the
			// flag for.
			relInfos[i].isSemiAntiSyntheticLeaf = true
			continue
		}
		bindings[i] = b
		leaves[i] = scan
		if local != nil {
			localized := make([]Expr, 0, len(preds))
			for _, p := range preds {
				localized = append(localized, localizeExprToLeaf(p, b))
			}
			leaves[i] = &Filter{Child: scan, Predicate: combineAnd(localized), LeafLocal: true}
		}
		baseRows := EstimateRows(scan)
		relInfos[i] = baseRelInfo{
			bindingIdx:   i,
			baseRows:     baseRows,
			filteredRows: applyLocalFilterSelectivity(baseRows, b, scan, local),
			// c8 (design doc §42.4): this leaf's `table == nil` reflects
			// "not a single base relation," not "no real stats" —
			// `baseRows` above already came from `EstimateRows` over a
			// real, already-cost-estimated join subtree. Mark it so
			// `leafIsDerivedInput` doesn't conflate the two.
			isSemiAntiSyntheticLeaf: true,
		}
	}

	// R21 slice 1 (plan-parity-fix-take2, K24): the search's own RelOptInfo
	// now comes back instead of being discarded. Slice 1 CARRIES it only —
	// `_` here is deliberate and temporary, and its replacement is what lets
	// partial aggregation see `PartialPathlist` (K23) and the grouping/window
	// stages see `Pathkeys` (K12 slice B).
	// M0142-0008a-3i-plumbing-c3 (design doc §36, gap 4): `jl` (== `prefix`
	// from `splitOuterSpine`, checked above at [0,nprefix)) has no leaf items
	// for the synthetic Semi/Anti RHS leaves c2 added to `bindings`/`leaves`/
	// `relInfos` at [nprefix,nleaves). `validateJoinlistProblem` hard-requires
	// `jl.leafRange() == (0,len(prob.bindings))`, so search with a copy that
	// appends one `leafItem(nprefix+k)` per `semiAnti[k]` — same walk-position
	// correspondence c2 already established — rather than mutating `jl` itself
	// (it may alias `ctx.joinlist` directly per the P5.9-r comment above).
	// (c3, cont.) M0142-0008a-3i-route-a step 2: a flattened link's `rhs`
	// spans MULTIPLE consecutive leaf slots, so the leaf items are appended
	// per synthetic LEAF (`nprefix` through `nleaves`), not per LINK.
	// M0145-0005 slice 2: pulled leaves need no append — they are real
	// `jl` members already (the pull-up numbered them into the joinlist
	// itself), so this workaround survives only for chain-extracted
	// synthetic leaves.
	searchJl := jl
	if len(semiAnti) > 0 {
		searchJl = make(joinlist, 0, len(jl)+(nleaves-nprefix))
		searchJl = append(searchJl, jl...)
		for i := nprefix; i < nleaves; i++ {
			searchJl = append(searchJl, leafItem(i))
		}
	}
	searched, _, err := planJoinlistSearch(searchJl, &joinlistProblem{
		bindings:  bindings,
		scans:     leaves,
		relInfos:  relInfos,
		conjuncts: searchConjuncts,
		// P0-H11: the problem carries the leaf spans THEMSELVES, not a
		// cumulative boundary array — a synthetic leaf's out-of-band span
		// survives the hand-off (a cumulative boundary array flattened it
		// into the following leaf's inferred window, and
		// validateJoinlistProblem's monotonicity check then declined the
		// whole problem anyway).
		leafSpans: spans,
		// take2 P2-01: the search prices with the STATEMENT's settings, not a
		// hard-wired constant list. tryJoinSearch is reached only from inside
		// planSelect (predp.go and two sites in planner.go), so this ctx is
		// always the one planSelect stamped — no parent walk is needed, and
		// none is safe (see resolveContext.settings).
		cp:  ctx.settings.costParams(),
		cat: cat,
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
		// M0145-0003: the correlation check reads the POOLED conjuncts,
		// not `pred` — on the jointree arm the pulled sublinks' rebased
		// quals (which may still carry outer refs above this scope)
		// live in `conjuncts`/`heldAbovePrefix`, while the consumed
		// sublink exprs themselves stay in `pred` and must not count.
		corrAbove: exprHasOuterRefList(append(append([]Expr{}, conjuncts...), heldAbovePrefix...)) ||
			exprHasOuterRefList(chainOnQualPreds(onQuals)),
		// joinInfoList is root->join_info_list from jointree deconstruction,
		// consumed by join_is_legal/joinOrderRestricted/hasJoinRestriction
		// inside the search (M0128-P1.2).
		// M0142-0008a-3i-plumbing-c4 (design doc §36, gap 4): without each
		// semiAnti link's SJInfo, `joinIsLegal` (joinsearchlevel.go:198) has
		// no SpecialJoinInfo constraint on the synthetic RHS leaf's relid and
		// treats it as an ordinary INNER-joinable rel — c3's Q21 unit-test
		// regression (AntiJoin silently disappearing) traced to exactly this
		// gap, so c3 and c4 must land together, not "independent" as
		// originally filed.
		// M0142-0008a-3i-plumbing-c16: ctx.joinInfoList already carries
		// every semiAnti link's SJInfo by this point (the DEDUPING append
		// above, joinInfoListHas guard, landed by c4). Re-running
		// semiAntiJoinInfoList here appended a SECOND, undeduped copy of
		// each entry on top; deleted as a confirmed duplicate-list bug.
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
		if !searchConsumes(c, spans) {
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
func deriveOuterLinkConstants(links []outerChainLink, conjuncts []Expr, spans []leafSpan) []Expr {
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
			ra, okA := relidsOfExpr(a, spans)
			rb, okB := relidsOfExpr(b, spans)
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
			if !conjunctIsLocalEligible(d) || tableForCol(d, spans) < 0 {
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
func outerOnQualsOK(links []outerChainLink, spans []leafSpan) bool {
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
			rs, ok := relidsOfExpr(c, spans)
			if !ok || rs == 0 || !relsSubset(rs, lk.preserved|lk.nullable) {
				return false
			}
			switch {
			case relsOverlap(rs, lk.preserved) && relsOverlap(rs, lk.nullable):
				if !searchConsumes(c, spans) {
					return false
				}
			case relsSubset(rs, lk.nullable):
				if !conjunctIsLocalEligible(c) || tableForCol(c, spans) < 0 {
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

// innerOnQualsBelowNullableOK proves, per conjunct, that every INNER link's
// `ON` qual lying under an admitted outer link's nullable side will be
// evaluated BELOW that link — C-04b.
//
// C-04a never needed this: a LEFT link's nullable side is its right input,
// which in goopg's left-deep chain is one leaf, so no inner link sits under
// it. A RIGHT link null-extends its whole left prefix, and the prefix IS an
// inner chain. Its `ON` quals join the search's conjunct list on the licence
// the file header states for inner links — "anywhere at or above the join" —
// and one of those places is the residual `Filter` above the entire searched
// tree, which `tryPGShapedJoinSearch` builds from every search conjunct
// `searchConsumes` reports unplaced. Above the tree is above the outer link,
// and there such a qual tests null-extended rows: `(a JOIN b ON a.x = b.x)
// RIGHT JOIN c` evaluated as `Filter(a.x = b.x) over (… RIGHT JOIN c)` drops
// every `c` row that matched nothing. Too few rows, and no row count on the
// preserved side alone would notice.
//
// A conjunct that the search DOES consume is safe: `clausesFor` applies it at
// the lowest join covering its relids, and legality (`joinIsLegal` against the
// reduced SpecialJoinInfo, whose MinRighthand is the whole prefix) forbids the
// preserved leaf from joining before the prefix is complete, so the lowest
// covering join is inside the prefix and below the link. A single-relation
// conjunct becomes a leaf local, which is below everything. Anything else —
// an OR-of-ANDs the producer only mines for equalities (joinrestrict.go), a
// non-equality the search declines, a conjunct the seam cannot attribute —
// declines the statement. That is the decline `searchConsumes` documents as
// the cost of an unplaced conjunct, made a wrong-answer guard rather than a
// pessimisation on exactly the side where it would be one.
//
// Only multi-leaf nullable sides are tested, so C-04a's shapes take exactly
// the path they took before: a single-leaf nullable side holds no inner link.
func innerOnQualsBelowNullableOK(onQuals []chainOnQual, links []outerChainLink, spans []leafSpan) bool {
	var nullable RelSet
	for _, lk := range links {
		if lk.nullable != 0 && lk.nullable&(lk.nullable-1) != 0 {
			nullable |= lk.nullable
		}
	}
	if nullable == 0 {
		return true
	}
	for _, q := range onQuals {
		for _, c := range splitAnd(q.pred) {
			rs, ok := relidsOfExpr(c, spans)
			if !ok {
				// Unattributable: the seam cannot say which side it reads,
				// so it cannot say the residual would be a safe place.
				return false
			}
			if !relsOverlap(rs, nullable) {
				continue
			}
			if conjunctIsLocalEligible(c) && tableForCol(c, spans) >= 0 {
				continue
			}
			if !searchConsumes(c, spans) {
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
func searchConsumes(c Expr, spans []leafSpan) bool {
	for _, ri := range buildRestrictInfos([]Expr{c}, 0, spans).all {
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
// The Semi/Anti-admission arm (M0142-0008a-3i-plumbing-b1's
// `admitSemiAnti`, design doc §22.4) is unconditional — the ONE
// production call site passed literal `true` since b2 step (iii)
// landed, and M0145-0005 slice 2 retired the parameter: the off arm
// was dead flexibility, not a tested path.
func extractSearchLeaves(node Node) (scans []Node, widths []int, onQuals []chainOnQual, outer []outerChainLink, semiAnti []semiAntiChainLink, ok bool) {
	width := 0
	// realWidth counts only NON-synthetic leaf widths — the running
	// "span space" origin `buildLeafSpans` later reproduces (real leaves
	// contiguous, synthetic leaves relocated out-of-band). An inner/outer
	// link's `ON`/`Predicate` is written in its subtree's `Output()`
	// coordinates, which omit synthetic columns exactly like the span
	// space does, so the correct rebase delta for those quals is
	// realWidth — not width, which counts synthetic widths the predicate
	// never saw. The two coincide on every chain without a semiAnti link
	// (the pre-flattening production shape), so this changes nothing
	// there and fixes the misattribution a synthetic leaf introduces for
	// any inner/outer link sitting after one in walk order.
	realWidth := 0
	// `preserved` marks a subtree NO admitted outer link null-extends, and it
	// is C-04a/b's `onSpine` flag WIDENED rather than deleted.
	//
	// C-04a cleared the flag for both inputs of an INNER link, so a LEFT link
	// below an inner one — `a LEFT JOIN b ON … JOIN c ON …` — was declined
	// together with the genuinely hazardous shapes. That was one restriction
	// doing two jobs. An inner join PRESERVES both its inputs, so descending
	// one carries the flag through unchanged (C-04c); what clears it is
	// descending into a side some link null-extends: a LEFT link's right input,
	// a RIGHT link's left input.
	//
	// An outer link met on a cleared path is still DECLINED, and after C-04c
	// that is a measured decision rather than an inherited one. The shape
	// `(a LEFT JOIN b) RIGHT JOIN c` — an outer link inside another's nullable
	// side — was admitted during C-04c and returned WRONG ROWS:
	// `buildJoinRelRestrictList` (joinrestrict.go) classifies the LOWER link's
	// own `ON` clause as an outer-join FILTER clause for the upper link (its
	// relids are a subset of the upper SJI's nullable hand), and re-applies it
	// at the upper join — where it filters exactly the rows that join was
	// supposed to null-extend. Upstream cannot reach that: a clause applied at
	// a lower join is removed from the per-rel `joininfo` lists, while goopg
	// re-scans one flat clause list per pair. Ledger row
	// `c04c-nested-outer-refilters-lower-on-qual`; the pin is
	// `TestSeamDeclinesAnOuterLinkUnderARightLinksNullableSide`
	// (joinsearch_rightlink_test.go), which C-04c therefore leaves standing.
	//
	// The walk's return value is the union of the NULLABLE sides of every
	// admitted outer link at or below `n`, which is what an INNER link ABOVE
	// such a link needs in order to state where its own `ON` qual may go
	// (`chainOnQual`). It is non-zero only for C-04c's shapes: before them an
	// admitted inner link always sat below every admitted outer one.
	var walk func(n Node, preserved bool) (RelSet, bool)
	walk = func(n Node, preserved bool) (RelSet, bool) {
		if n == nil {
			return 0, false
		}
		j, isJoin := n.(*Join)
		if isJoin && (j.Type == JoinTypeSemi || j.Type == JoinTypeAnti) {
			// M0142-0008a-3i-plumbing-b1 (design doc §22.2's settled
			// semantics; unconditional since b2 — M0145-0005 slice 2 retired
			// the `admitSemiAnti` flag that used to gate this branch):
			// SEMI/ANTI null-extends neither side, so it is declined exactly
			// like an outer link when it sits on a subtree an admitted outer
			// link above it already null-extends (mirrors the Left/Right
			// `!preserved` decline immediately below), but unlike an outer
			// link its RHS stays ONE opaque leaf rather than being split
			// into preserved/nullable leaf ranges — making the RHS itself a
			// real DP-reorderable participant is the separate S5b mechanism
			// (§11-§13, §22.2), in flight under the unfrozen M0142-0008 chain.
			if !preserved {
				return 0, false
			}
			base := width
			loLeft := len(scans)
			nullLeft, okLeft := walk(j.Left, preserved)
			if !okLeft {
				return 0, false
			}
			loRight := len(scans)
			// c7 (design doc §41.3): `rightBase` is the flat-space position
			// where j.Right's own opaque leaf starts, and `outerWidth` is
			// the SEMANTIC width unnestExistsExpr used to build LeftKey/
			// RightKey/Predicate (unnest.go:4694, len(j.Left.Output()) at
			// synthesis time). The two diverge whenever j.Left already has
			// an earlier chained semiAnti link's opaque RHS leaf spliced
			// into it: `width` (the walk's flat leaf count) then counts
			// that extra leaf too, so `rightBase > base + outerWidth`.
			// `rebaseSemiAntiChainQual` needs both to shift the outer and
			// inner operands by different deltas; a single `rebaseChainQual`
			// call (one additive `base`) is only correct when they coincide.
			outerWidth := len(j.Left.Output())
			rightBase := width
			// M0142-0008a-3i-route-a step 2: a `FlattenedRHS` marker (set
			// by unnestExistsExpr/unnestInExpr only after
			// `sublinkBodyIsSimple` + `decomposeFlatBodyTree` both pass)
			// splices the body's own FROM items in as REAL leaves — the
			// seam's version of PG's `pull_up_subqueries`. The link's `rhs`
			// then spans every decomposed leaf (`leafRangeRelSet` over the
			// appended range), `buildLeafSpans` already marks each one
			// synthetic, and `semiAntiChainLink.bodyQuals` carries the
			// pooled body conjuncts rebased into this walk's flat space.
			var bodyQuals []Expr
			flattened := false
			if j.FlattenedRHS {
				flatLeaves, flatQuals, _, _, okFlat := decomposeFlatBodyTree(j.Right, false)
				if !okFlat {
					return 0, false
				}
				for _, lf := range flatLeaves {
					scans = append(scans, lf)
					leafW := len(lf.Output())
					widths = append(widths, leafW)
					// width is the walk's `sum(widths)` accumulator, and the
					// body-concat coordinate space `bodyQuals`/the link pred
					// are written in is the LEAF-concatenation — a body
					// Project may narrow `j.Right.Output()` to a different
					// width, so the advance is the appended leaves' sum,
					// not the opaque subtree's output width.
					width += leafW
				}
				// Pooled conjuncts are in the body's own leaf-concat
				// coordinates — `+rightBase` lands them in this walk's
				// flat space (the same space `rebaseSemiAntiChainQual`
				// gives the link pred); the post-walk
				// `remapWalkOrderFlatToSpans` pass then relocates them
				// into `spans` space alongside it.
				for _, q := range flatQuals {
					shifted, okShift := rebaseChainQual(q, rightBase)
					if !okShift {
						return 0, false
					}
					bodyQuals = append(bodyQuals, shifted)
				}
				flattened = true
			} else {
				// j.Right as ONE opaque leaf — the same append path the
				// "not an admitted join type" branch below uses for any other
				// non-reorderable node.
				scans = append(scans, j.Right)
				widths = append(widths, len(j.Right.Output()))
				width += len(j.Right.Output())
			}
			hiRight := len(scans)
			pred := j.Predicate
			if j.LeftKey != nil && j.RightKey != nil {
				// Design doc §28.4: unnestExistsExpr (this walk's only
				// producer of a keyed Semi/Anti join) deliberately excludes
				// its own primary equijoin condition from Predicate — the
				// hash match enforces it directly via LeftKey/RightKey, the
				// same convention fillOneJoinHashKeys relies on rather than
				// deriving HashKeys[0] from Predicate
				// (join_hash_keys.go:198). A semiAntiChainLink is consumed
				// by future code as if `pred` were the join's WHOLE
				// condition, so fold the key equality in here too,
				// mirroring joinInputs.joinPredicate's idiom
				// (createplanjoin.go:492-504) — otherwise a plan built from
				// `pred` alone would silently become an unconditional
				// (Cartesian-like) Semi/Anti.
				eq := &BinaryOp{pos: j.LeftKey.Pos(), Op: parser.OpEq, Left: j.LeftKey, Right: j.RightKey}
				conjuncts := []Expr{eq}
				if pred != nil {
					conjuncts = append(conjuncts, pred)
				}
				pred = combineAnd(conjuncts)
			}
			if pred != nil {
				shifted, okShift := rebaseSemiAntiChainQual(pred, outerWidth, base, rightBase)
				if !okShift {
					return 0, false
				}
				pred = shifted
			}
			pjt := parser.JoinSemi
			if j.Type == JoinTypeAnti {
				pjt = parser.JoinAnti
			}
			lhs := leafRangeRelSet(loLeft, loRight)
			rhs := leafRangeRelSet(loRight, hiRight)
			semiAnti = append(semiAnti, semiAntiChainLink{jointype: pjt, lhs: lhs, rhs: rhs, pred: pred, sjinfo: j.SJInfo, flattened: flattened, bodyQuals: bodyQuals})
			// Item 4: rebuild the placeholder SJInfo `unnestExistsExpr`
			// attached at synthesis time (`existsUnnestSJInfo`'s
			// throwaway synL=1/synR=2 — unnest.go:4390-4397, the only
			// self-contained numbering available before this walk ever
			// runs) with the real leaf-index bits just derived.
			// `SynLefthand`/`SynRighthand` stay the WHOLE atomic lhs/rhs —
			// that breadth is `existsUnnestSJInfo`'s deliberate convention
			// for the syntactic sides (unnest.go:4405-4409) and is still
			// correct here, real numbering or not.
			//
			// M0142-0008a-3i-plumbing-c10 (design doc §44.3-44.4, filed by
			// c9): `MinLefthand`/`MinRighthand` do NOT inherit that same
			// breadth. `existsUnnestSJInfo` sets `MinLefthand==SynLefthand`
			// unconditionally (unnest.go:4419-4430) only because at
			// construction time it has no real leaf numbering to narrow
			// against — the synthetic synL=1 bit IS "the whole outer side,"
			// full stop. Now that this walk has real numbering, PG's own
			// `min_lefthand` computation (`pull_varnos(clause)` intersected
			// with the syntactic side — see `sjiClauseRelids`'s doc comment,
			// specialjoin.go) narrows to just the relations the correlation
			// clause actually touches. Skipping that narrowing pins every
			// real DP admission check to "wait for the entire outer
			// composite," which is fatal for ANTI (no unique-ify escape
			// valve unlike SEMI): an ANTI link could then never be admitted
			// below the full outer join order.
			//
			// `pred` was just rebased (above) into THIS walk's own running
			// cumulative column-index space — `base`/`rightBase` advance in
			// walk order and count every leaf's width, synthetic or not.
			// `buildLeafSpans(widths, nil)` with a nil semiAnti list forces
			// its "no semiAnti links" branch (its own doc comment: reduces
			// to a plain cumulative sum), reconstructing that EXACT same
			// space for the leaves appended so far — NOT the canonical
			// post-walk "real-then-synthetic-out-of-band" space the finished
			// `semiAnti` list would otherwise trigger. `relidsOfExpr` then
			// resolves each of `pred`'s ColumnRefs back to its real leaf.
			// An unresolvable clause, or a computed set that misses either
			// side entirely, falls back to the old un-narrowed bit — the
			// same safe default `existsUnnestSJInfo` uses when it cannot see
			// real numbering at all.
			minL, minR := lhs, rhs
			if pred != nil {
				if relids, ok := relidsOfExpr(pred, buildLeafSpans(widths, nil)); ok {
					if nl := relids & lhs; nl != 0 {
						minL = nl
					}
					if nr := relids & rhs; nr != 0 {
						minR = nr
					}
				}
			}
			if j.SJInfo != nil {
				j.SJInfo.SynLefthand, j.SJInfo.MinLefthand = lhs, minL
				j.SJInfo.SynRighthand, j.SJInfo.MinRighthand = rhs, minR
			}
			// Semi/Anti contributes nothing to the NULL-extended union an
			// INNER link above it needs (§22.2) — `below` is `nullLeft`
			// unchanged, exactly like the existing INNER-link arm.
			return nullLeft, true
		}
		if !isJoin || (j.Type != JoinTypeCross && j.Type != JoinTypeInner && j.Type != JoinTypeLeft && j.Type != JoinTypeRight) {
			scans = append(scans, n)
			widths = append(widths, len(n.Output()))
			width += len(n.Output())
			realWidth += len(n.Output())
			return 0, true
		}
		if (j.Type == JoinTypeLeft || j.Type == JoinTypeRight) && !preserved {
			return 0, false
		}
		// The link's own coordinate origin: the leaves to its left have already
		// been counted, and its qual was resolved against a schema that starts
		// at its leftmost leaf, so this is the delta between the two spaces.
		// realWidth (not width): the predicate's native space is the subtree's
		// Output(), which omits synthetic columns — matching realWidth and the
		// post-walk `spans` span space exactly (see the realWidth comment).
		base := realWidth
		loLeft := len(scans)
		nullLeft, ok := walk(j.Left, preserved && j.Type != JoinTypeRight)
		if !ok {
			return 0, false
		}
		loRight := len(scans)
		nullRight, ok := walk(j.Right, preserved && j.Type != JoinTypeLeft)
		if !ok {
			return 0, false
		}
		hiRight := len(scans)
		below := nullLeft | nullRight
		if j.Type == JoinTypeLeft || j.Type == JoinTypeRight {
			// C-04c: a non-zero base is no longer a decline. It is the
			// NON-FIRST COMMA FROM ITEM case of the file header, and the
			// re-basing it calls unsafe is unsafe only in the spelling it had
			// in mind (`shiftColumnRefsBy`, which answers `return e` for an
			// expression kind it does not know and would leave a ColumnRef
			// reading the wrong column). `rebaseChainQual` is built on
			// `cloneExprRefs`, whose child-slot primitive is exhaustive over
			// every Expr type by a build-time gate and which ABORTS on an
			// unknown one — so an unshiftable qual declines the statement
			// instead of being silently half-shifted.
			pred := j.Predicate
			if pred != nil && base != 0 {
				shifted, okShift := rebaseChainQual(pred, base)
				if !okShift {
					return 0, false
				}
				pred = shifted
			}
			lk := outerChainLink{
				jointype:  parser.JoinLeft,
				preserved: leafRangeRelSet(loLeft, loRight),
				nullable:  leafRangeRelSet(loRight, hiRight),
				pred:      pred,
			}
			if j.Type == JoinTypeRight {
				// C-04b: the link is recorded as the LEFT join it reduces to
				// — the same reduction `makeSpecialJoinInfoScoped` applied to
				// its SpecialJoinInfo, through the same function, so the two
				// descriptions match hand for hand in `outerLinksHaveSJInfos`.
				// The leaves keep their FROM order; only which side is
				// null-extended is restated.
				lk.jointype, lk.preserved, lk.nullable = reduceRightLink(lk.preserved, lk.nullable)
			}
			outer = append(outer, lk)
			return below | lk.nullable, true
		}
		if j.Predicate == nil {
			return below, true
		}
		if j.Type != JoinTypeInner {
			// A CROSS link carrying a qual is a shape `planFromClause` /
			// `planFromItem` never build, so it is not one this walk may
			// reinterpret.
			return 0, false
		}
		pred := j.Predicate
		if base != 0 {
			shifted, okShift := rebaseChainQual(pred, base)
			if !okShift {
				return 0, false
			}
			pred = shifted
		}
		onQuals = append(onQuals, chainOnQual{pred: pred, belowNullable: below})
		return below, true
	}
	if _, okWalk := walk(node, true); !okWalk {
		return nil, nil, nil, nil, nil, false
	}
	return scans, widths, onQuals, outer, semiAnti, true
}

// extractScopeLeaves builds the SAME (scans, widths, onQuals, outer,
// semiAnti) tuple extractSearchLeaves derives by walking the node chain,
// reading the leaf/link records planFromItem emitted at construction time
// (jtScopeTable, jointreescope.go — M0145-0005 slice 3). This is a
// transcription of the walk's arithmetic onto table reads, not a
// reimplementation of its semantics:
//
//   - the walk's `width`/`realWidth` accumulators are prefix sums over
//     the leaf table, `realWidth` skipping the synthetic (semi/anti RHS)
//     leaves the same way the walk's opaque-append arm does;
//   - `base`/`rightBase`/`belowNullable` come out of the recorded leaf
//     ranges: a link's subtree spans [loLeft, hiRight) and its left
//     subtree is [loLeft, loRight), so the `below` union a node's
//     recursive walk returns is the containment test over already-
//     processed outer links' subtree ranges;
//   - `preserved` is the same descendant test the walk's flag computes:
//     every link sits in its ancestors' LEFT subtree (a right subtree is
//     always one leaf), so a link is unpreserved exactly when a RIGHT-
//     typed link's range encloses it — an outer or semi/anti link then
//     declines, matching the walk's `!preserved` arms;
//   - the rebases (`rebaseChainQual`, `rebaseSemiAntiChainQual`) run
//     unchanged on jn.Predicate — the table changes WHERE the structure
//     comes from, not what the quals say. A Join's Predicate is written
//     in its own concat coordinates whatever path reads it.
//
// A FlattenedRHS semi/anti link expands its single recorded leaf into
// the body's decomposed leaves exactly like the walk's arm (that shape
// is only built by the unnest rewrite, whose grafted chain has a
// different root and never reaches this path — the arm is reproduced
// for parity, not reachability); later leaf indices shift by the
// expansion, tracked through `leafEff`/`len(scans)`.
func extractScopeLeaves(tab *jtScopeTable, sjis []*SpecialJoinInfo) (scans []Node, widths []int, onQuals []chainOnQual, outer []outerChainLink, semiAnti []semiAntiChainLink, ok bool) {
	if tab == nil || tab.poisoned || len(tab.leaves) == 0 {
		return nil, nil, nil, nil, nil, false
	}
	// preserved[i]: the walk's flag ANDs `jn.Type != JoinTypeRight` over
	// every enclosing join met on the left-descent path to link i. In
	// the table those enclosers are exactly the links whose leaf range
	// strictly contains link i's whole range.
	preserved := make([]bool, len(tab.links))
	for i := range tab.links {
		preserved[i] = true
		for j := range tab.links {
			if j != i && tab.links[j].loLeft <= tab.links[i].loLeft && tab.links[j].hiRight >= tab.links[i].hiRight && tab.links[j].jn != nil && tab.links[j].jn.Type == JoinTypeRight {
				preserved[i] = false
				break
			}
		}
	}
	// M0145-0005 slice 6 — canonical emission. The walk emits leaves in
	// chain DFS order; this extraction instead emits them in JOINLIST
	// order: every emitting (non-synthetic) leaf first in table order,
	// then every deferred (synthetic — a SEMI/ANTI link's right side)
	// leaf in table order. That is exactly the numbering
	// deconstructJointreeScopedSJI gives the joinlist's leaf items
	// (deferred band after the emitting band), so a deferred leaf's
	// emitted index IS its joinlist item index and `rhs` in the link
	// record below is a real item index, not a synthetic tail slot —
	// the `realLeaf` marker carries that distinction to the seam.
	//
	// Because the emitting band is contiguous, `emitBase` — the
	// canonical flat column offset — is also the leaf's problem-space
	// span.lo, so every rebase below lands directly in span space and
	// the seam's walk-order→span remap is skipped for these links.
	canonLo := make([]int, len(tab.leaves))
	canonHi := make([]int, len(tab.leaves))
	emitBase := make([]int, len(tab.leaves))
	emitW := 0
	for ti := range tab.leaves {
		lf := tab.leaves[ti]
		if lf.synthetic {
			continue
		}
		if lf.node == nil {
			return nil, nil, nil, nil, nil, false
		}
		if j, isJ := lf.node.(*Join); isJ && j.Type != JoinTypeFull {
			return nil, nil, nil, nil, nil, false
		}
		canonLo[ti], canonHi[ti] = len(scans), len(scans)+1
		emitBase[ti] = emitW
		w := len(lf.node.Output())
		scans = append(scans, lf.node)
		widths = append(widths, w)
		emitW += w
	}
	// leafLink maps a synthetic leaf to the semi/anti link that produced
	// it (the link's loRight) — needed to find FlattenedRHS expansions.
	leafLink := make([]int, len(tab.leaves))
	for i := range leafLink {
		leafLink[i] = -1
	}
	for li := range tab.links {
		lk := &tab.links[li]
		if lk.kind == jtLinkSemiAnti && lk.loRight >= 0 && lk.loRight < len(tab.leaves) {
			leafLink[lk.loRight] = li
		}
	}
	// flatBodyQuals carries a flattened link's rebased body quals, keyed
	// by the table leaf the link's right side occupied. The shape is
	// unreachable on this path today (only the unnest rewrite builds
	// FlattenedRHS, and its grafted chain has a different root) — kept
	// for parity with the walk arm; a real occurrence declines at the
	// seam's leaf-count gate, since no joinlist items exist for the
	// expansion's extra leaves.
	flatBodyQuals := make(map[int][]Expr)
	for ti := range tab.leaves {
		lf := tab.leaves[ti]
		if !lf.synthetic {
			continue
		}
		if lf.node == nil {
			return nil, nil, nil, nil, nil, false
		}
		canonLo[ti] = len(scans)
		emitBase[ti] = emitW
		li := leafLink[ti]
		if li >= 0 && tab.links[li].jn != nil && tab.links[li].jn.FlattenedRHS {
			flatLeaves, flatQuals, _, _, okFlat := decomposeFlatBodyTree(lf.node, false)
			if !okFlat {
				return nil, nil, nil, nil, nil, false
			}
			for _, fl := range flatLeaves {
				w := len(fl.Output())
				scans = append(scans, fl)
				widths = append(widths, w)
				emitW += w
			}
			for _, q := range flatQuals {
				shifted, okShift := rebaseChainQual(q, emitBase[ti])
				if !okShift {
					return nil, nil, nil, nil, nil, false
				}
				flatBodyQuals[ti] = append(flatBodyQuals[ti], shifted)
			}
		} else {
			w := len(lf.node.Output())
			scans = append(scans, lf.node)
			widths = append(widths, w)
			emitW += w
		}
		canonHi[ti] = len(scans)
	}
	// members maps a table-leaf range to the relset of canonical leaf
	// indices it covers — contiguous for ordinary ranges, with the
	// deferred bits mixed in when the subtree contains a semijoin right.
	members := func(lo, hi int) RelSet {
		var m RelSet
		for t := lo; t < hi; t++ {
			m |= leafRangeRelSet(canonLo[t], canonHi[t])
		}
		return m
	}
	// outerSubs records each processed outer link's subtree member set
	// and nullable set; a link's `below` is the union over entries whose
	// subtree is contained in this link's left subtree — the quantity
	// the walk's nullLeft|nullRight recursion returns.
	type outerSub struct {
		members, nullable RelSet
	}
	var outerSubs []outerSub
	below := func(leftMembers RelSet) RelSet {
		var b RelSet
		for _, os := range outerSubs {
			if os.members&^leftMembers == 0 {
				b |= os.nullable
			}
		}
		return b
	}
	for li := range tab.links {
		lk := &tab.links[li]
		if lk.jn == nil || lk.loLeft < 0 || lk.loLeft >= lk.loRight || lk.loRight >= lk.hiRight || lk.hiRight > len(tab.leaves) {
			return nil, nil, nil, nil, nil, false
		}
		// A link's left subtree always starts at a non-synthetic leaf
		// (the item's base), so emitBase[loLeft] is an emitting-space
		// offset — the base every qual rebase below reads.
		if tab.leaves[lk.loLeft].synthetic {
			return nil, nil, nil, nil, nil, false
		}
		switch lk.kind {
		case jtLinkSemiAnti:
			if !preserved[li] {
				return nil, nil, nil, nil, nil, false
			}
			base := emitBase[lk.loLeft]
			rightBase := emitBase[lk.loRight]
			outerWidth := len(lk.jn.Left.Output())
			pred := lk.jn.Predicate
			if lk.jn.LeftKey != nil && lk.jn.RightKey != nil {
				eq := &BinaryOp{pos: lk.jn.LeftKey.Pos(), Op: parser.OpEq, Left: lk.jn.LeftKey, Right: lk.jn.RightKey}
				conjuncts := []Expr{eq}
				if pred != nil {
					conjuncts = append(conjuncts, pred)
				}
				pred = combineAnd(conjuncts)
			}
			if pred != nil {
				shifted, okShift := rebaseSemiAntiChainQual(pred, outerWidth, base, rightBase)
				if !okShift {
					return nil, nil, nil, nil, nil, false
				}
				pred = shifted
			}
			pjt := parser.JoinSemi
			if lk.jn.Type == JoinTypeAnti {
				pjt = parser.JoinAnti
			}
			lhs := members(lk.loLeft, lk.loRight)
			rhs := members(lk.loRight, lk.hiRight)
			// The link's SpecialJoinInfo is the one deconstruction
			// published on ctx.joinInfoList — match it by jointype and
			// syntactic sides (the same match semiAntiLinksHaveSJInfos
			// performs), so the search constrains the leaf with the SJI
			// `lower`-scan narrowing deconstruction computed. Only when
			// no list entry matches — a numbering desync the leaf-count
			// gate will also catch — fall back to the plan node's
			// placeholder, renumbered in place exactly as the walk does.
			sjinfo := lk.jn.SJInfo
			matched := false
			for _, sj := range sjis {
				if sj != nil && sj.Jointype == pjt && sj.SynLefthand == lhs && sj.SynRighthand == rhs {
					sjinfo = sj
					matched = true
					break
				}
			}
			semiAnti = append(semiAnti, semiAntiChainLink{jointype: pjt, lhs: lhs, rhs: rhs, pred: pred, sjinfo: sjinfo, flattened: lk.jn.FlattenedRHS, bodyQuals: flatBodyQuals[lk.loRight], realLeaf: true})
			if !matched && sjinfo != nil {
				minL, minR := lhs, rhs
				if pred != nil {
					if relids, okRel := relidsOfExpr(pred, buildLeafSpans(widths, nil)); okRel {
						if nl := relids & lhs; nl != 0 {
							minL = nl
						}
						if nr := relids & rhs; nr != 0 {
							minR = nr
						}
					}
				}
				sjinfo.SynLefthand, sjinfo.MinLefthand = lhs, minL
				sjinfo.SynRighthand, sjinfo.MinRighthand = rhs, minR
			}
		case jtLinkOuter:
			if !preserved[li] || (lk.jn.Type != JoinTypeLeft && lk.jn.Type != JoinTypeRight) {
				return nil, nil, nil, nil, nil, false
			}
			base := emitBase[lk.loLeft]
			pred := lk.jn.Predicate
			if pred != nil && base != 0 {
				shifted, okShift := rebaseChainQual(pred, base)
				if !okShift {
					return nil, nil, nil, nil, nil, false
				}
				pred = shifted
			}
			lk2 := outerChainLink{
				jointype:  parser.JoinLeft,
				preserved: members(lk.loLeft, lk.loRight),
				nullable:  members(lk.loRight, lk.hiRight),
				pred:      pred,
			}
			if lk.jn.Type == JoinTypeRight {
				lk2.jointype, lk2.preserved, lk2.nullable = reduceRightLink(lk2.preserved, lk2.nullable)
			}
			outer = append(outer, lk2)
			outerSubs = append(outerSubs, outerSub{members: members(lk.loLeft, lk.hiRight), nullable: lk2.nullable})
		case jtLinkInner:
			if lk.jn.Type != JoinTypeInner || lk.jn.Predicate == nil {
				return nil, nil, nil, nil, nil, false
			}
			base := emitBase[lk.loLeft]
			pred := lk.jn.Predicate
			if base != 0 {
				shifted, okShift := rebaseChainQual(pred, base)
				if !okShift {
					return nil, nil, nil, nil, nil, false
				}
				pred = shifted
			}
			onQuals = append(onQuals, chainOnQual{pred: pred, belowNullable: below(members(lk.loLeft, lk.loRight))})
		default:
			return nil, nil, nil, nil, nil, false
		}
	}
	return scans, widths, onQuals, outer, semiAnti, true
}

// buildLeafSpans builds extractSearchLeaves's per-leaf (lo, hi) table
// (M0142-0008a-3i-plumbing-b2 design doc §25.3/§26): every REAL leaf keeps
// exactly the column-index range a plain walk-order cumulative sum would
// give it, computed while SKIPPING synthetic leaves' widths, so a later real
// leaf's range is never shifted by an earlier synthetic one — §25.1's traced
// `qualAC` misattribution, where a real leaf following an admitted Semi/Anti
// link inherited that link's RHS leaf's stolen column range. Every SYNTHETIC
// (Semi/Anti RHS) leaf's range is instead appended AFTER the total real
// width, in walk order among themselves. `semiAnti[*].rhs` marks which walk
// positions are synthetic; every other leaf is real. With no semiAnti links
// — still the only production shape today, since every corpus chain that
// could carry one declines earlier at the leaf-count gate — this reduces to
// the old plain cumulative sum.
func buildLeafSpans(widths []int, semiAnti []semiAntiChainLink) []leafSpan {
	var synthetic RelSet
	for _, lk := range semiAnti {
		synthetic |= lk.rhs
	}
	spans := make([]leafSpan, len(widths))
	realOffset := 0
	for i, w := range widths {
		if synthetic&leafRangeRelSet(i, i+1) != 0 {
			continue
		}
		spans[i] = leafSpan{lo: realOffset, hi: realOffset + w}
		realOffset += w
	}
	syntheticOffset := realOffset
	for i, w := range widths {
		if synthetic&leafRangeRelSet(i, i+1) == 0 {
			continue
		}
		spans[i] = leafSpan{lo: syntheticOffset, hi: syntheticOffset + w}
		syntheticOffset += w
	}
	return spans
}

// remapWalkOrderFlatToSpans re-expresses `e`'s `ColumnRef.Index` values from
// `extractSearchLeaves`'s own WALK-ORDER flat column space — leaf i's columns
// occupy `[sum(widths[:i]), sum(widths[:i])+widths[i])`, the order the walk
// visited leaves in — into `buildLeafSpans`'s output space, `spans`.
// M0142-0008a-3i-plumbing-c20 (design doc §56): the two spaces coincide only
// when no REAL leaf follows a semiAnti link's opaque RHS leaf in walk
// order — `buildLeafSpans` relocates every synthetic leaf's span out-of-band,
// after the total width of every real leaf, so a later real leaf inherits
// the walk-order-flat range a synthetic leaf actually occupies. A `semiAnti`
// link's `pred` — rebased into walk-order-flat terms by
// `rebaseSemiAntiChainQual` while the walk still had it in hand, before any
// LATER leaf's width was knowable — must therefore be re-targeted here, once
// `widths` is complete and `spans` exists, before `relidsOfExpr`
// (which reads `spans`, not walk order) ever sees it.
func remapWalkOrderFlatToSpans(e Expr, widths []int, spans []leafSpan, pulledBase, pulledLeaves int, leafPerm []int) (Expr, bool) {
	if e == nil {
		return nil, true
	}
	prefix := make([]int, len(widths)+1)
	for i, w := range widths {
		prefix[i+1] = prefix[i] + w
	}
	// `widths` is the PRE-SPLICE walk table; `spans` is the problem's own
	// (post-splice) table. The pulled leaves were inserted at walk
	// position `pulledBase`, so a walk leaf at-or-above it lands at
	// problem index `+ pulledLeaves`. With `pulledLeaves` == 0 this is
	// the identity — the pre-slice-2 shape.
	// M0145-0016 composes a SECOND translation on top: after the splice, a
	// chain whose synthetic leaves were not already the tail is stably
	// partitioned, and `leafPerm` maps the post-splice position to its final
	// one. The permutation is column-NEUTRAL — `buildLeafSpans` assigns real
	// spans in position order and synthetic spans out-of-band after them, and
	// a stable partition preserves both relative orders, so every span's `lo`
	// is unchanged and only the index holding it moves. That is precisely
	// what makes composing the two translations safe.
	problemIndex := func(walkLeaf int) int {
		spliced := walkLeaf
		if walkLeaf >= pulledBase {
			spliced = walkLeaf + pulledLeaves
		}
		if spliced >= 0 && spliced < len(leafPerm) {
			return leafPerm[spliced]
		}
		return spliced
	}
	leafOf := func(walkOrderFlat int) (int, bool) {
		for i := range widths {
			if walkOrderFlat >= prefix[i] && walkOrderFlat < prefix[i+1] {
				return i, true
			}
		}
		return 0, false
	}
	failed := false
	out, ok := cloneExprRefs(e, scopeVeto, exprRewriter{
		Rewrite: func(x Expr) Expr {
			cr, isCol := x.(*ColumnRef)
			if !isCol {
				return x
			}
			leaf, found := leafOf(cr.Index)
			if !found || problemIndex(leaf) >= len(spans) {
				failed = true
				return x
			}
			pi := problemIndex(leaf)
			cr.Index = spans[pi].lo + (cr.Index - prefix[leaf])
			return x
		},
	})
	if !ok || failed {
		return nil, false
	}
	return out, true
}

// pgShapedOffsetChecksOK implements design doc §31.3 items 2 and 3:
// `tryPGShapedJoinSearch`'s per-leaf offset-agreement check and its
// spine-offset-disagreement check, both widened to skip the
// NON-EMITTING leaves — M0145-0005 slice 2 widened the set a second
// time: where it used to cover only synthetic (Semi/Anti RHS) leaves,
// it now covers every leaf at-or-above `nReal`: the pulled bodies'
// real leaf items (no ctx.bindings entry — nothing to check against)
// and the extracted synthetic tail alike. The caller passes the set
// directly (`leafRangeRelSet(nReal, len(scans))` — the tail invariant
// makes that exactly pulled ∪ extracted) rather than a link list.
//
// Item 2: a plain index-for-index comparison of `spans[i]` against
// `bindingOffsets[i]` (real, FROM-clause-derived offsets, one per real
// leaf) breaks the moment a non-emitting leaf precedes a real one —
// every real leaf at-or-after it shifts by however many non-emitting
// leaves came first. This walks `spans` with `i` and `bindingOffsets`
// with a SEPARATE counter that only advances past emitting leaves
// (there is no real-FROM oracle to check a non-emitting leaf against,
// so it is simply skipped).
//
// Item 3: `buildLeafSpans` places every synthetic leaf's span
// OUT-OF-BAND, after the total emitting width — so whenever the LAST
// leaf in walk order is non-emitting, `spans`'s raw last entry
// overshoots the real total by that leaf's own width. The spine (if
// any) must begin at the EMITTING total width, computed here by
// summing `widths` while skipping non-emitting indices, not at
// `spans`'s raw last entry.
//
// With `nonEmitting` zero every branch below reduces exactly to the
// pre-existing plain checks. Direct unit-test calls exercise the
// non-empty arithmetic.
func pgShapedOffsetChecksOK(spans []leafSpan, nonEmitting RelSet, widths []int, bindingOffsets []int, hasSpine bool, spineOffset int) (declineReason string, ok bool) {
	j := 0
	for i := range spans {
		if nonEmitting&leafRangeRelSet(i, i+1) != 0 {
			continue
		}
		if j >= len(bindingOffsets) || bindingOffsets[j] != spans[i].lo {
			return "offset-disagreement", false
		}
		j++
	}
	realTotalWidth := 0
	for i, w := range widths {
		if nonEmitting&leafRangeRelSet(i, i+1) != 0 {
			continue
		}
		realTotalWidth += w
	}
	if hasSpine && spineOffset != realTotalWidth {
		return "spine-offset-disagreement", false
	}
	return "", true
}

// chainOnQual is one INNER link's `ON` qual as the walk flattened it, plus the
// union of the nullable sides of every admitted outer link STRICTLY BELOW that
// link — C-04c.
//
// The set is the whole reason the field exists. An inner link's qual may be
// placed anywhere at or above its own join (file header), and until C-04c every
// admitted inner link sat BELOW every admitted outer one, so "at or above its
// own join" was automatically "at or above every outer link" and the licence
// was unconditional. Admitting an outer link below an inner one breaks that:
// `a LEFT JOIN b ON a.x = b.x JOIN c ON b.y IS NULL` has an inner `ON` conjunct
// reading the LEFT link's NULLABLE side, and
// `partitionConjunctsForJoinPlanning` — which has no nullable-side guard —
// would make it a leaf-local filter on `b`, evaluated BELOW the join that
// produces the NULLs. `IS NULL` then selects `b` rows rather than unmatched `a`
// rows and the statement returns different rows, which is finding-1's shape
// one level up.
//
// `belowNullable` is in leaf-index space, like `outerChainLink`'s sides, so the
// consumer can test it against `relidsOfExpr(…, spans)` directly.
type chainOnQual struct {
	pred          Expr
	belowNullable RelSet
}

// rebaseChainQual re-expresses a chain qual written in ONE FROM item's own
// coordinates (`planFromItem` resolves a chain's quals against that item's
// `leftCtx`, which starts at column 0) in the STATEMENT's coordinates, which
// is what every leaf-index and `spans` computation below the seam speaks.
// `delta` is the column offset of the link's leftmost leaf — the item's own
// start, since a FROM item's chain is left-deep.
//
// It is `cloneExprRefs` and not `shiftColumnRefsBy`, and the difference is the
// one the file header calls a correctness statement. `shiftColumnRefsBy` is a
// hand-written type switch over a SUBSET of the Expr types (13 of 32, pinned by
// `exprwalk_inventory_test.go`) that answers `return e` for the rest, so a
// ColumnRef nested inside an unenumerated node would come back UNSHIFTED —
// reading a different relation's column, a wrong answer rather than a lost
// plan. `cloneExprRefs` is built on `exprChildSlots`, which a build-time gate
// (`exprwalk_exhaustive_test.go`) keeps exhaustive over all 32 types in both
// directions, and it ABORTS instead of no-opping when it meets one it does not
// know. So the unsafe half of the header's decline is answered by the driver,
// not by the caller's diligence.
//
// `scopeVeto` declines any qual containing a sublink or other inner plan: the
// inner plan's own coordinate space is not this one, its correlated references
// are `OuterColumnRef`s resolved against a scope this shift knows nothing
// about, and no driver in this package descends a subplan. Declining is the
// conservative answer — the statement falls back to the syntactic tree, which
// carries the qual on its own node in its own coordinates.
func rebaseChainQual(e Expr, delta int) (Expr, bool) {
	if e == nil {
		return nil, true
	}
	if delta == 0 {
		return e, true
	}
	out, ok := cloneExprRefs(e, scopeVeto, exprRewriter{
		// The node handed to Rewrite is already the fresh shallow clone
		// `cloneExprRefs` made, so mutating it cannot touch the tree the
		// pre-search pipeline still holds (the seam may yet DECLINE, and a
		// decline must return `node`/`pred` untouched).
		Rewrite: func(x Expr) Expr {
			if cr, isCol := x.(*ColumnRef); isCol {
				cr.Index += delta
			}
			return x
		},
	})
	if !ok {
		return nil, false
	}
	return out, true
}

// rebaseSemiAntiChainQual re-expresses a semi/anti chain link's qual (the
// folded LeftKey=RightKey equality plus any lifted Predicate residual) from
// unnestExistsExpr's own MIXED convention into the walk's flat leaf-index
// space. unnestExistsExpr writes the outer operand of every ColumnRef
// 0-based in the outer's OWN schema and the inner operand as
// `outerWidth + subcol` (unnest.go:4694, `outerWidth = len(j.Left.Output())`
// at synthesis time) — a single hash-joined padded row convention, not a
// flat-leaf one.
//
// `rebaseChainQual`'s single additive `delta` is only a correct translation
// of that convention when `j.Left` contributes exactly `outerWidth` flat
// leaves to the walk, i.e. an UNCHAINED link. For a chained link — a later
// semiAnti link whose `j.Left` already has an earlier link's opaque RHS leaf
// spliced into it (c7, design doc §41.3) — the walk counts MORE flat leaves
// under `j.Left` than `outerWidth` (the extra sibling leaf), so the outer
// operand must still shift by `base` (the true original outer's leaves are
// always the FIRST leaves the walk appends under `j.Left`, chained or not)
// while the inner operand must shift to `rightBase + (cr.Index - outerWidth)`
// instead — `rightBase` is the flat-space position where `j.Right`'s own
// opaque leaf starts, which already absorbs any extra sibling leaves via
// the walk's own leaf count.
func rebaseSemiAntiChainQual(e Expr, outerWidth, base, rightBase int) (Expr, bool) {
	if e == nil {
		return nil, true
	}
	if base == 0 && rightBase == outerWidth {
		return e, true
	}
	out, ok := cloneExprRefs(e, scopeVeto, exprRewriter{
		Rewrite: func(x Expr) Expr {
			if cr, isCol := x.(*ColumnRef); isCol {
				if cr.Index < outerWidth {
					cr.Index += base
				} else {
					cr.Index = rightBase + (cr.Index - outerWidth)
				}
			}
			return x
		},
	})
	if !ok {
		return nil, false
	}
	return out, true
}

// outerChainLink is one OUTER link `extractSearchLeaves` admitted into the
// flattened chain: which leaves it preserves, which it null-extends, and the
// `ON` qual it was written with. C-04a builds these for LEFT links; C-04b adds
// RIGHT links, recorded AS the LEFT join they reduce to (`reduceRightLink`),
// so `jointype` is always `parser.JoinLeft` and `nullable` may be a
// multi-leaf range (a RIGHT link's whole left prefix).
//
// The sides are LEAF-INDEX relsets — bit i is the i'th leaf the walk appended,
// which is the FROM-binding index (03 §6.1's leaf-numbering guarantee) and
// therefore the same space `relidsOfExpr(…, spans)` answers in. That is
// what lets the seam decide, per conjunct, whether a qual reaches a nullable
// side without re-deriving the chain.
type outerChainLink struct {
	jointype             parser.JoinType
	preserved, nullable  RelSet
	pred                 Expr
}

// semiAntiChainLink is the SEMI/ANTI analogue of outerChainLink, kept as its
// own type rather than a `jointype`-discriminated arm of outerChainLink
// (M0142-0008a-3i-plumbing, design doc §15 item 2): SEMI/ANTI never
// null-extends in either direction (`reresolveJoinByName`'s own doc comment —
// "emit Outer (=Left) only at runtime"), so `outerChainLink`'s
// preserved/nullable fields, and every consumer built on that NULL-extension
// contract (`outerOnQualsOK`, `deriveOuterLinkConstants`), do not apply and
// must not be reused by encoding one side as "nullable" purely to satisfy
// their arithmetic — §15 found that trap live. `lhs`/`rhs` are the two
// disjoint LEAF-INDEX relsets `extractSearchLeaves` would flatten a SEMI/ANTI
// join's syntactic sides into, mirroring `SpecialJoinInfo.SynLefthand`/
// `SynRighthand` for the same join (`existsUnnestSJInfo`, unnest.go).
//
// NOT YET PRODUCED by `extractSearchLeaves` — landed here, with its
// consumers below, as inert/unit-tested-only infrastructure ahead of the
// walk-extension step, which is coupled to a separate, larger change
// (planner.go's pre-unnest `origChain` snapshot and predp.go's pinned-spine
// descend loop both currently prevent any SEMI/ANTI node from ever reaching
// `extractSearchLeaves`, so extending the walk alone would be dead code —
// see design doc §21).
type semiAntiChainLink struct {
	jointype parser.JoinType
	lhs, rhs RelSet
	pred     Expr
	// M0142-0008a-3i-plumbing-c4 (design doc §36, gap 4's own dependency):
	// the same `*SpecialJoinInfo` the walk already renumbers in place at the
	// append site below (`j.SJInfo`, real leaf-index `SynLefthand`/
	// `SynRighthand`/`MinLefthand`/`MinRighthand`) — nil if the source
	// `*Join` never carried one. `semiAntiLinksHaveSJInfos` matches against
	// this pointer's fields, not a freshly built one, so the two must never
	// drift apart.
	sjinfo *SpecialJoinInfo
	// flattened is set when the source `*Join` carried `FlattenedRHS` —
	// the body's own FROM items were spliced in as real scan leaves rather
	// than one opaque subtree (M0142-0008a-3i-route-a step 2). `rhs` then
	// spans every decomposed leaf (multi-bit), and each leaf gets the real
	// base-relInfo path (catalog stats + leaf-local filters) instead of the
	// opaque `EstimateRows` treatment.
	flattened bool
	// bodyQuals are the flattened body's own conjuncts (non-correlation
	// WHERE / inner-join quals), rebased into the walk's flat column space
	// beside `pred` and remapped to leaf-span space with it after the
	// walk. They join `conjuncts` at the same point `pred` does, so a
	// body-local restriction becomes a leaf-local filter and a body
	// spanning qual becomes an intra-RHS join clause — exactly
	// `distribute_qual_to_rels`'s shape inside PG's pulled-up RHS. Empty
	// for opaque links.
	bodyQuals []Expr
	// realLeaf is set when `rhs` names REAL joinlist leaf items — the
	// deferred-band numbering `extractScopeLeaves` emits for a
	// chain-extracted SEMI/ANTI link under the jointree pipeline
	// (M0145-0005 slice 6). Such a link's `pred`/`bodyQuals` are already
	// in leaf-span space (the canonical emission makes the walk-order →
	// span remap unnecessary) and its `sjinfo` is the deconstruction's own
	// `*SpecialJoinInfo` from `ctx.joinInfoList`, not a renumbered
	// placeholder. When false, `rhs` names synthetic tail leaves and the
	// legacy remap/renumber machinery applies.
	realLeaf bool
}

// semiAntiLinksHaveSJInfos is `outerLinksHaveSJInfos`'s SEMI/ANTI analogue:
// every admitted link must be backed by a real `SpecialJoinInfo` the search's
// legality machinery already knows about, matched by jointype and by the two
// syntactic sides (no preserved/nullable distinction to match).
func semiAntiLinksHaveSJInfos(links []semiAntiChainLink, list []*SpecialJoinInfo) bool {
	for _, lk := range links {
		found := false
		for _, sj := range list {
			if sj != nil && sj.Jointype == lk.jointype &&
				sj.SynLefthand == lk.lhs && sj.SynRighthand == lk.rhs {
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

// semiAntiOnQualsOK is `outerOnQualsOK`'s SEMI/ANTI analogue, simplified per
// design doc §15's finding that SEMI/ANTI's contract is strictly narrower
// than an outer join's: "a valid equi-correlation between two disjoint
// RelSets," with no preserved-side-only or nullable-side-only placement
// question to answer (a RHS-only conjunct would already have been pushed
// into the EXISTS body's own opaque leaf by the unnest rewrite, before this
// link is ever built). Every conjunct of the link's predicate must therefore
// span BOTH `lhs` and `rhs` and be one the search itself will place
// (`searchConsumes`); anything else — a conjunct confined to one side, or one
// the search cannot attribute — declines, mirroring `outerOnQualsOK`'s own
// conservative default for a shape it does not recognize.
func semiAntiOnQualsOK(links []semiAntiChainLink, spans []leafSpan) bool {
	for _, lk := range links {
		if lk.pred == nil {
			return false
		}
		for _, c := range splitAnd(lk.pred) {
			rs, ok := relidsOfExpr(c, spans)
			if !ok || rs == 0 || !relsSubset(rs, lk.lhs|lk.rhs) {
				return false
			}
			if !relsOverlap(rs, lk.lhs) || !relsOverlap(rs, lk.rhs) {
				return false
			}
			if !searchConsumes(c, spans) {
				return false
			}
		}
	}
	return true
}

// chainOnQualPreds is the bare predicate list of a `chainOnQual` slice, for the
// two consumers that ask a question about the EXPRESSIONS alone (outer-reference
// detection) rather than about where they sit in the chain.
func chainOnQualPreds(qs []chainOnQual) []Expr {
	if len(qs) == 0 {
		return nil
	}
	out := make([]Expr, len(qs))
	for i, q := range qs {
		out[i] = q.pred
	}
	return out
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
	// C-04a/b: it descends exactly the links `extractSearchLeaves` flattens,
	// which now includes LEFT and RIGHT. An admitted LATERAL outer link must
	// not reorder across its dependency any more than an inner one may, and
	// the marker lives on the chain node the flattening discards.
	if j, ok := n.(*Join); ok && (j.Type == JoinTypeCross || j.Type == JoinTypeInner || j.Type == JoinTypeLeft || j.Type == JoinTypeRight) {
		return j.Lateral || chainCarriesLateral(j.Left) || chainCarriesLateral(j.Right)
	}
	// M0142-0008a-3i-plumbing-c14 (design doc §48/§49): `extractSearchLeaves`
	// (with `admitSemiAnti` now unconditionally true in production, b2 step
	// (iii)) ALSO descends into a Semi/Anti join's `Left` — its own semiAnti
	// arm's `walk(j.Left, preserved)` — while its `Right` stays one opaque,
	// never-decomposed leaf regardless of what it contains. Before this fix
	// this function had no matching arm: a Semi/Anti join fell straight to
	// the `nodeReferencesOuter` fallback below, which asks a DIFFERENT
	// question ("does an OuterColumnRef here escape unbound") rather than
	// this function's own coarse "is a Lateral join reachable here at all" —
	// a Lateral join nested inside a Semi/Anti's `Left` (the exact shape a
	// pre-DP-unnest join-order search, predp.go's Phase A, splices in when
	// its own winning tree contains a parameterized index probe) has its
	// `OuterColumnRef` correctly bound by its own immediate parent, so it
	// never reads as "escaping" — the decline never fired, and
	// `extractSearchLeaves` went on to flatten the Lateral join's `Left`/
	// `Right` into two independent leaves, corrupting the outer-parameterized
	// probe into a plain per-relation leaf (§48's live-traced crash). `Right`
	// is excluded from this recursion because it mirrors the walk exactly:
	// the walk never decomposes it, so a Lateral join buried inside it is
	// never at risk of being split.
	if j, ok := n.(*Join); ok && (j.Type == JoinTypeSemi || j.Type == JoinTypeAnti) {
		return chainCarriesLateral(j.Left)
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

// seamLeafBinding builds the `rangeBinding` for a seam leaf that the pull-up
// spliced in, and reports whether the leaf kind is one the search can price at
// all. It is the single admission point for what used to be two copies of
// `scan.(*SeqScan)` — the `pulled-leaf-not-scan` and `flat-leaf-not-scan`
// checks, which M0145-0011's scope (c) measured to be ONE invariant held at
// two sites (relaxing only `flattenPulledBodyTree`, the producer, merely moved
// 30 TPC-DS declines from the pull-up census to the seam census).
//
// Two leaf kinds are admissible, and the difference between them is the whole
// point:
//
//   - `*SeqScan` — a real base relation. `table`/`alias` come off the scan and
//     the leaf is priced from catalog statistics.
//   - `*CTEScan` — a DERIVED input. `table` stays nil deliberately. That is not
//     an oversight to paper over: `leafIsDerivedInput` reads exactly this to
//     keep the `outer-over-derived` firewall in force over the leaf, which is
//     the ordering M0145-0013 requires (admission machinery first, the
//     firewall relaxation last, in M0145-0018).
//
// `pullCtx`/`k` are the body-order hand-through the task asks for: the pull-up
// already recorded each body's own `rangeBinding` (`jtPulledBody.bodyBindings`,
// whose comment reserves them for exactly this), so a pulled leaf takes its
// alias from the body's binding rather than from a re-derived guess. Only the
// OFFSET is re-stamped, because the problem's span — not the body-local
// numbering — is the coordinate space the search works in. Pass `nil` when the
// caller has no body-order index (the flattened-link leaves are numbered in
// walk order, not body order).
func seamLeafBinding(scan Node, offset int, pullCtx *resolveContext, k int) (rangeBinding, bool) {
	b := rangeBinding{offset: offset}
	if pullCtx != nil {
		if pb, ok := pulledBodyBinding(pullCtx, k); ok {
			b = pb
			b.offset = offset
		}
	}
	switch x := scan.(type) {
	case *SeqScan:
		b.table = x.Table
		if b.alias == "" {
			b.alias = x.Alias
		}
	case *CTEScan:
		// No catalog table, by construction. `seamLeafRelInfo` routes on
		// exactly this nil to the statistics-free pricing path.
		b.table = nil
		if b.alias == "" {
			b.alias = x.Alias
		}
	default:
		return rangeBinding{}, false
	}
	return b, true
}

// pulledBodyBinding returns the k-th pulled leaf's body-side binding, counting
// across bodies in the order `splicePulledLeaves` laid them into the problem.
func pulledBodyBinding(ctx *resolveContext, k int) (rangeBinding, bool) {
	pu := ctx.jtPullup
	if pu == nil || k < 0 {
		return rangeBinding{}, false
	}
	for _, body := range pu.bodies {
		if k < len(body.bodyBindings) {
			return body.bodyBindings[k], true
		}
		k -= len(body.bodyBindings)
	}
	return rangeBinding{}, false
}

// seamLeafRelInfo prices a spliced seam leaf, routing on whether the binding
// names a real catalog relation.
//
// The derived arm is not new machinery: it is the same pricing the seam
// already applies to an opaque Semi/Anti RHS leaf a few lines below — read the
// subtree with `EstimateRows` and apply the local filter's selectivity on top.
// That is the right tool here for the reason the opaque arm states: both
// `estimateBaseRelInfo` and `applyRelSizeFallback` read `binding.table`, and
// with it nil `estimateTableRowsFallback` returns 0 outright, flooring the leaf
// at a ZERO row estimate instead of a sane one.
//
// `EstimateRows(*CTEScan)` recurses the body, which is goopg's equivalent of
// `set_cte_size_estimates` propagating the subplan's `plan_rows`
// (postgres/src/backend/optimizer/path/allpaths.c). No per-column statistics
// are synthesised for the derived leaf, deliberately: M0145-0009's census
// established that the remaining CTE-output column asks are columns PG itself
// leaves unknown, so inventing them would be precision the oracle does not have
// either.
func seamLeafRelInfo(i int, b rangeBinding, scan Node, local Expr, cat catalog.Catalog) baseRelInfo {
	if b.table != nil {
		info := estimateBaseRelInfo(b, scan, local)
		info.bindingIdx = i
		applyRelSizeFallback(&info, b, scan, local, cat)
		return info
	}
	baseRows := EstimateRows(scan)
	return baseRelInfo{
		bindingIdx:   i,
		baseRows:     baseRows,
		filteredRows: applyLocalFilterSelectivity(baseRows, b, scan, local),
	}
}

// identityLeafPerm is the no-op leaf permutation: position i stays at i. It is
// what every chain whose synthetic leaves already occupy the tail uses, so the
// permutation machinery costs those chains nothing and cannot change them.
func identityLeafPerm(n int) []int {
	perm := make([]int, n)
	for i := range perm {
		perm[i] = i
	}
	return perm
}

// stableSyntheticTailPerm builds the permutation that restores the seam's
// construction contract: every synthetic (Semi/Anti RHS) leaf in a tail slot.
//
// It is a STABLE partition, and the stability is load-bearing rather than
// cosmetic. Real leaves carry the emitting column space: `buildLeafSpans`
// assigns their spans in position order, so preserving their relative order
// preserves every column offset exactly. Synthetic leaves are assigned
// out-of-band after the real total, so preserving THEIR relative order does
// the same for them. The result is that only position masks move — no column
// coordinate is renumbered — which is the property `remapWalkOrderFlatToSpans`
// relies on when it composes this translation with the pulled-splice one.
//
// The shape this exists for is the demoted-ANTI mid-chain walk
// `[real, synthetic, real]` (`web_sales ANTI web_returns JOIN date_dim`,
// TPC-DS Q78), which maps to [0, 2, 1].
func stableSyntheticTailPerm(synthetic RelSet, n int) []int {
	perm := make([]int, n)
	next := 0
	for i := 0; i < n; i++ {
		if synthetic&leafRangeRelSet(i, i+1) == 0 {
			perm[i] = next
			next++
		}
	}
	for i := 0; i < n; i++ {
		if synthetic&leafRangeRelSet(i, i+1) != 0 {
			perm[i] = next
			next++
		}
	}
	return perm
}

// permuteRelSet re-expresses a leaf-index mask under `perm`.
func permuteRelSet(rs RelSet, perm []int) RelSet {
	out := RelSet(0)
	for i := 0; i < len(perm); i++ {
		if rs&(RelSet(1)<<uint(i)) != 0 {
			out |= RelSet(1) << uint(perm[i])
		}
	}
	return out
}

// applyLeafPerm moves every position-indexed piece of seam state onto the
// permuted numbering. The inventory is exhaustive on purpose — a mask left
// behind does not fail loudly, it silently names a different leaf, which is
// the class of fault that produced Q78's historical `translateToLayout` panic.
//
// `semiAnti[i].sjinfo` is a SHARED pointer whose own doc comment forbids it
// from drifting apart from the link's `lhs`/`rhs`, so it is mutated in place
// rather than rebuilt.
func applyLeafPerm(perm []int, scans *[]Node, widths *[]int, semiAnti []semiAntiChainLink, outer []outerChainLink, onQuals []chainOnQual) {
	n := len(perm)
	newScans := make([]Node, n)
	newWidths := make([]int, n)
	for i := 0; i < n && i < len(*scans); i++ {
		newScans[perm[i]] = (*scans)[i]
		newWidths[perm[i]] = (*widths)[i]
	}
	*scans = newScans
	*widths = newWidths
	for i := range semiAnti {
		semiAnti[i].lhs = permuteRelSet(semiAnti[i].lhs, perm)
		semiAnti[i].rhs = permuteRelSet(semiAnti[i].rhs, perm)
		if sj := semiAnti[i].sjinfo; sj != nil {
			sj.SynLefthand = permuteRelSet(sj.SynLefthand, perm)
			sj.SynRighthand = permuteRelSet(sj.SynRighthand, perm)
			sj.MinLefthand = permuteRelSet(sj.MinLefthand, perm)
			sj.MinRighthand = permuteRelSet(sj.MinRighthand, perm)
		}
	}
	for i := range outer {
		outer[i].preserved = permuteRelSet(outer[i].preserved, perm)
		outer[i].nullable = permuteRelSet(outer[i].nullable, perm)
	}
	for i := range onQuals {
		onQuals[i].belowNullable = permuteRelSet(onQuals[i].belowNullable, perm)
	}
}
