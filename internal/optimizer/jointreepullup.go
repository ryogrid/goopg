package optimizer

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// jointreepullup.go — M0145-0003: sublink pull-up into the jointree
// problem, the jointree pipeline's replacement for the chain-splice
// route the legacy pipeline takes through unnest.go + the seam's
// semi/anti link extraction.
//
// This is `pull_up_sublinks` (postgres/src/backend/optimizer/plan/
// subselect.c), not `pull_up_subqueries`: a WHERE-clause sublink whose
// body is a flat inner/cross jointree of base relations has its OWN
// relations appended to the parent's join-search leaf table and its
// quals pooled into the parent's conjunct stream, with a
// `SpecialJoinInfo` recording the SEMI/ANTI ordering restriction. The
// join search then sees every pulled relation as an ordinary citizen
// of one problem — the leaf entries exist by CONSTRUCTION (bindings +
// scans appended by the same pass that numbers them), not by
// decomposing a finished plan tree whose leaf/spans relationship can
// disagree with the walk. That is the difference the task statement
// draws against the seam's synthetic-leaf splice: there is no
// walk/decompose mismatch to decline on.
//
// What this file produces is coordinate-agnostic bound material
// (`jtPullup`): body leaf scans, body-local bound quals, and the
// sublink's join type, plus the leaf items the pull-up itself numbers
// into ctx.joinlist (M0145-0005 slice 2 — the pulled leaves are real
// joinlist members, not synthetic appended slots). Inside
// tryPGShapedJoinSearch, `splicePulledLeaves` splices the scans into
// the leaf table at those positions and `classifyPulledQuals` rebases
// the quals into the problem's column space, classifies them into the
// conjunct pools, and appends each body's SpecialJoinInfo to
// ctx.joinInfoList — so the entire problem tail (qual
// pooling, nullable-side delay proofs, leaf-local filter attachment,
// relInfo estimation, `planJoinlistSearch`) runs unchanged.
//
// Decline posture: every check here fails CLOSED. A sublink that is
// not pulled keeps its already-planned body and is processed by the
// same machinery as today (post-search `unnestSubqueriesInPlan`, or
// the SubPlan eval path) — a decline can only cost an optimisation,
// never change a result.

// jtPullup records the WHERE-clause sublinks the jointree pipeline
// converted to semi/anti join members before join-order search began.
// It lives on the resolveContext so tryPGShapedJoinSearch — the one
// place the emitting prefix's real leaf count is known — can fold the
// pulled bodies into the problem it builds.
type jtPullup struct {
	// bodies are the pulled sublinks in conjunct order.
	bodies []*jtPulledBody
	// outerQuals are pulled-body conjuncts that read only emitting
	// rels — legal to hoist under a SEMI join (`EXISTS(... WHERE
	// outer.x = 5)` filters the outer rows the semijoin would keep
	// either way), so they join the parent's own conjunct stream at
	// the same point the link predicates do. Under ANTI they cannot
	// be hoisted — `NOT EXISTS(... WHERE outer.x = 5)` must keep the
	// outer.x != 5 rows — so classifyPulledQuals declines instead
	// of ever populating this for one.
	outerQuals []Expr
	// pulled marks the bound WHERE conjuncts the pull-up consumed —
	// the bound *ExistsExpr node itself. tryPGShapedJoinSearch
	// excludes marked conjuncts from the WHERE conjunct pool: their
	// semantics now live in the semi/anti links, so feeding them to
	// the residual Filter would evaluate the sublink a second time on
	// top of the join. On a declined search the marks are inert: the
	// conjuncts stay in the predicate and the legacy tail handles
	// them exactly as before.
	pulled map[Expr]bool
	// base is the leaf-item index of the first pulled leaf — the
	// joinlist's relation count at pull-up time, when the bodies'
	// leaf items were appended to ctx.joinlist (M0145-0005 slice 2).
	// The seam's own nReal must agree with it; a mismatch is a
	// numbering desync, not a shape to plan around.
	base int
	// nLeaves is the total number of pulled leaf items appended —
	// sum of len(bodies[i].leafScans). Pulled leaves occupy the
	// problem positions [base, base+nLeaves), in body order.
	nLeaves int
}

// jtPulledBody is one pulled sublink's bound material in BODY-LOCAL
// coordinates. The leaf scans are the nodes planFromItem produced for
// the body's own FROM items — real plan nodes with real catalog
// identity — and the quals are the body's bound conjuncts, in which a
// `*ColumnRef` indexes the body's own leaf-concat space and an
// `*OuterColumnRef{Level:1}` indexes the PARENT statement's emitting
// space. rebasePulledQual performs the coordinate rebase inside
// classifyPulledQuals; until then nothing in this record names a
// parent-space position.
type jtPulledBody struct {
	// expr is the bound *ExistsExpr the pull-up consumed — the key in
	// jtPullup.pulled.
	expr Expr
	// jointype is parser.JoinSemi for EXISTS, parser.JoinAnti for
	// NOT EXISTS.
	jointype parser.JoinType
	// leafScans are the body's leaf nodes in body-binding order —
	// bare *SeqScan leaves only (the flatLeaf arm of the problem tail
	// requires them).
	leafScans []Node
	// leafWidths is len(leaf.Output()) per leaf, in the same order.
	leafWidths []int
	// bodyBindings are the body's own bindings, body-local offsets,
	// parallel to leafScans. Carried so integration can hand the
	// search real rangeBinding records (table, alias, sourceIdx)
	// rather than synthesising opaque ones.
	bodyBindings []rangeBinding
	// quals are the body's bound conjuncts — its WHERE plus the ON
	// quals of its own inner joins, in body-local + OuterColumnRef
	// space. WHERE conjuncts may carry Level-1 outer refs (the
	// correlation); ON conjuncts may not (PG's
	// `contain_vars_of_level(subselect,1)` check on the rest of the
	// subselect).
	quals []Expr
}

// pullUpSublinksIntoJointree implements the EXISTS/NOT-EXISTS arm of
// pull_up_sublinks for the jointree pipeline. It walks the top-level
// conjuncts of the bound WHERE predicate — the same level PG's
// pull_up_sublinks_jointree_recurse processes — and for every
// *ExistsExpr carrying a retained `.Subquery` parse tree, tries to
// bind and classify the body for the splice.
//
// The predicate is NOT modified: pulled conjuncts are recorded in
// `pulled` and consumed later, inside the search, where a decline can
// still return the predicate untouched. A sublink that fails any gate
// keeps its already-planned body and flows to the same downstream
// machinery it always has.
func pullUpSublinksIntoJointree(pred Expr, ctx *resolveContext, cat catalog.Catalog, ps PlannerSettings) *jtPullup {
	if pred == nil || ctx == nil || cat == nil {
		return nil
	}
	var pu *jtPullup
	for _, c := range splitAnd(pred) {
		// M0145-0003 ANY arm: `x IN (SELECT y FROM …)` is the other half
		// of PG's pull-up scope (`pull_up_sublinks_qual_recurse` converts
		// ANY_SUBLINK at prepjointree.c:665 and EXISTS_SUBLINK at :731).
		// The decline census measured it as 34 of the 45 unpulled sublink
		// conjuncts on TPC-DS SF0.25 — the dominant miss.
		if in, okIn := anyPullupConjunct(c); okIn {
			body, reason, okBody := pullUpAnyBody(in, ctx, cat, ps)
			if !okBody {
				notePullupDecline(reason)
				continue
			}
			notePullupDecline("")
			if pu == nil {
				pu = &jtPullup{pulled: make(map[Expr]bool)}
			}
			pu.bodies = append(pu.bodies, body)
			pu.pulled[c] = true
			continue
		}
		ex, negated, ok := existsPullupConjunct(c)
		if !ok || ex.Subquery == nil {
			// M0145-0003 census: a conjunct this arm does not even
			// recognise. Only sublink-bearing conjuncts are reported —
			// an ordinary `a = 1` is not a missed pull-up.
			if kind := sublinkConjunctKind(c); kind != "" {
				notePullupDecline(kind)
			}
			continue
		}
		body, reason, ok := pullUpExistsBody(ex, negated, ctx, cat, ps)
		if !ok {
			notePullupDecline(reason)
			continue
		}
		notePullupDecline("")
		if pu == nil {
			pu = &jtPullup{pulled: make(map[Expr]bool)}
		}
		pu.bodies = append(pu.bodies, body)
		pu.pulled[c] = true
	}
	if pu == nil {
		return nil
	}
	// M0145-0005 slice 2: the pulled bodies' leaves are REAL joinlist
	// members — leaf items numbered in binding order, appended after
	// the emitting items exactly as pull_up_subqueries appends the
	// subquery's RTEs to the parent's range table
	// (initsplan.c: convert_EXISTS_sublink_to_join's list_concat).
	// The seam then sees one uniform leaf table — every pulled leaf a
	// searched item with a real binding — instead of real items plus
	// synthetic appended slots. Non-emitting, so they are never bound
	// for name resolution: ctx.bindings is untouched, and the problem
	// bindings are built from each body's own scope at the seam.
	pu.base = ctx.joinlist.nrels()
	pos := pu.base
	for _, pb := range pu.bodies {
		for range pb.leafScans {
			ctx.joinlist = append(ctx.joinlist, leafItem(pos))
			pos++
		}
	}
	pu.nLeaves = pos - pu.base
	return pu
}

// existsPullupConjunct recognises the two conjunct shapes pull_up_sublinks
// converts: a bare `*ExistsExpr` (EXISTS, or the Negated flag the parser
// can set directly on it) and a `UnaryOp(OpNot, *ExistsExpr)` — the
// parser's canonical spelling of `NOT EXISTS` (unnest.go:4516). The
// effective negation flips once across the NOT wrapper, mirroring the
// legacy unnest's `negated = !negated` at unnest.go:4778. Anything
// else — an EXISTS under an OR, a doubly-wrapped NOT — is not a
// top-level sublink conjunct and stays untouched, exactly as PG's
// jointree-level recursion leaves it.
func existsPullupConjunct(c Expr) (*ExistsExpr, bool, bool) {
	if ex, ok := c.(*ExistsExpr); ok {
		return ex, ex.Negated, true
	}
	if u, ok := c.(*UnaryOp); ok && u.Op == parser.OpNot {
		if ex, isEx := u.Operand.(*ExistsExpr); isEx {
			return ex, !ex.Negated, true
		}
	}
	return nil, false, false
}

// pullUpExistsBody binds one EXISTS/NOT-EXISTS body in a provisional
// scope and decides whether it may be spliced into the parent problem.
// The admissibility list mirrors convert_EXISTS_sublink_to_join
// (subselect.c:1447-1515):
//
//   - the body must pass `sublinkBodyIsSimple` — no WITH, set-ops,
//     aggregates, GROUP BY/HAVING, DISTINCT, LIMIT/OFFSET, FOR UPDATE,
//     VALUES, and (goopg's own narrowing) a flat jointree of bare
//     table references;
//   - its bound WHERE must reference the parent query
//     (`contain_vars_of_level(whereClause, 1)` — an uncorrelated
//     EXISTS is not a join, and upstream leaves it a subplan);
//   - no part of the body OUTSIDE the WHERE may reference the parent —
//     here that means the body's own inner-join ON quals must carry no
//     Level-1 outer refs;
//   - no bound qual may contain a sublink plan (`exprHasSublinkPlan`)
//     — a nested sublink's Level-1 refs bind only while the body
//     evaluates as one unit;
//   - the bound WHERE must be non-volatile
//     (`contain_volatile_functions(whereClause)`, subselect.c:1508):
//     once spliced, a qual's evaluation count is the search's, not the
//     subplan's once-per-outer-row — a volatile qual would produce
//     different answers under the two shapes.
//
// The middle return value is the DECLINE REASON, for M0145-0003's pull-up
// census (nlicensus.go): every `return nil, …, false` names the gate it fell
// at, so a corpus run says which arm to build next instead of which arm looks
// biggest. It is "" on success.
func pullUpExistsBody(ex *ExistsExpr, negated bool, parent *resolveContext, cat catalog.Catalog, ps PlannerSettings) (*jtPulledBody, string, bool) {
	sub := ex.Subquery
	if !sublinkBodyIsSimple(sub) {
		return nil, "body-not-simple", false
	}
	bodyCtx, leafScans, leafWidths, onQuals, why, ok := bindPulledBodyScope(sub, parent, cat, ps)
	if !ok {
		return nil, "exists-" + why, false
	}
	for _, q := range onQuals {
		// The rest of the subselect must not refer to the parent
		// (subselect.c:1502). Level >= 2 is fine — it refers above the
		// parent and decrements into place at integration.
		if exprHasOuterRefAtLevel(q, 1) {
			return nil, "on-qual-parent-ref", false
		}
	}
	// A WHERE-less body (an uncorrelated `EXISTS (SELECT … FROM t)`)
	// contributes no conjuncts at all — resolveExpr must not be called on
	// the nil Where (M0145-0005 slice 4 exposed the crash: single-table
	// statements only reach this machinery once the isSimpleSingle bypass
	// lifts). The empty conjunct set then declines at the correlation
	// check below, exactly as an outer-local-only WHERE does.
	var where Expr
	if sub.Where != nil {
		var err error
		where, err = resolveExpr(sub.Where, bodyCtx)
		if err != nil {
			return nil, "where-not-resolvable", false
		}
	}
	if exprHasSublinkPlan(where) || exprListHasSublinkPlan(onQuals) {
		return nil, "nested-sublink", false
	}
	if exprListHasVolatileBuiltin(append(splitAnd(where), onQuals...), cat) {
		return nil, "volatile-qual", false
	}
	quals := append(splitAnd(where), onQuals...)
	// `contain_vars_of_level(whereClause, 1)` — the correlation must
	// live in the WHERE, and it must exist (subselect.c:1509).
	if !exprListHasOuterRefAtLevel(quals, 1) {
		return nil, "no-level1-correlation", false
	}
	// The WHERE must also carry a conjunct that can actually become
	// the link predicate — one reading a body-local column AND a
	// Level-1 parent reference together. Without it the seam's
	// semiAntiOnQualsOK gate declines on the nil pred anyway, but by
	// then `pulled` has already suppressed the pre-DP arm for every
	// other sublink in this WHERE; declining here instead leaves the
	// statement exactly where the legacy pipeline would find it.
	if !exprListHasLocalAndLevel1Ref(splitAnd(where)) {
		return nil, "no-spanning-conjunct", false
	}
	jointype := parser.JoinSemi
	if negated {
		jointype = parser.JoinAnti
	}
	return &jtPulledBody{
		expr:         ex,
		jointype:     jointype,
		leafScans:    leafScans,
		leafWidths:   leafWidths,
		bodyBindings: bodyCtx.bindings,
		quals:        quals,
	}, "", true
}

// bindPulledBodyScope builds the provisional resolve scope for a pulled
// body: the same planFromClause the body's own planSelect would run,
// chained to the parent context so correlation resolves through
// `ctx.parent` exactly as it did when the body's plan was built. The
// body's jointree must come back as a flat inner/cross chain of bare
// scans — one leaf per binding — or the body is not splicable.
// The trailing string is the sub-reason the body failed on, for the pull-up
// census: "which arm next" was answered by counting, and the answer landed
// here, so the next question ("why does a body fail to bind") has to be
// countable too rather than re-derived by reading the code.
func bindPulledBodyScope(sub *parser.SelectStmt, parent *resolveContext, cat catalog.Catalog, ps PlannerSettings) (*resolveContext, []Node, []int, []Expr, string, bool) {
	node, bodyCtx, err := planFromClause(sub, cat, ps, parent.rtScope)
	if err != nil || bodyCtx == nil {
		return nil, nil, nil, nil, "from-clause-not-plannable", false
	}
	bodyCtx.cat = cat
	bodyCtx.parent = parent
	bodyCtx.settings = ps
	leafScans, onQuals, why, ok := flattenPulledBodyTree(node, len(bodyCtx.bindings))
	if !ok {
		return nil, nil, nil, nil, why, false
	}
	widths := make([]int, len(leafScans))
	for i, l := range leafScans {
		widths[i] = len(l.Output())
	}
	return bodyCtx, leafScans, widths, onQuals, "", true
}

// flattenPulledBodyTree decomposes the body's provisional jointree into
// its leaf scans plus the conjuncts of its inner-join ON clauses. Any
// non-inner link, any leaf that is not a bare *SeqScan (a demoted outer
// join, a derived item, an already-rewritten access path), or a leaf
// count that disagrees with the binding count fails the whole body —
// the flat splice has no leaf to stand in for any of those.
func flattenPulledBodyTree(node Node, wantLeaves int) ([]Node, []Expr, string, bool) {
	var leaves []Node
	var quals []Expr
	why := ""
	var walk func(n Node) bool
	walk = func(n Node) bool {
		j, isJoin := n.(*Join)
		if !isJoin {
			leaves = append(leaves, n)
			return true
		}
		if j.Type != JoinTypeInner && j.Type != JoinTypeCross {
			why = "body-outer-join"
			return false
		}
		if !walk(j.Left) || !walk(j.Right) {
			return false
		}
		if j.Predicate != nil {
			quals = append(quals, splitAnd(j.Predicate)...)
		}
		return true
	}
	if !walk(node) {
		return nil, nil, why, false
	}
	if len(leaves) != wantLeaves {
		return nil, nil, "body-leaf-count-mismatch", false
	}
	for _, l := range leaves {
		if _, isScan := l.(*SeqScan); !isScan {
			// The leaf kind is named because it decides the remedy: a
			// *Filter over a scan needs unwrapping, a derived item needs
			// the opaque-body arm, a rewritten access path needs neither.
			return nil, nil, "body-leaf-" + nliProbeIndexName(l), false
		}
	}
	return leaves, quals, "", true
}

// splicePulledLeaves inserts the pulled bodies' leaf scans at their
// leaf-item positions — [pu.base, pu.base+pu.nLeaves), the slots the
// pull-up already numbered into ctx.joinlist — directly after the
// real (emitting) leaves and BEFORE any synthetic leaves the chain
// walk extracted, and shifts every extracted leaf index at-or-above
// pu.base up by pu.nLeaves so the extracted set keeps the problem
// tail.
//
// It is called after `extractSearchLeaves`, before the synthetic-leaf
// accounting, so the spliced leaves participate in every check the
// walk-derived leaves do (tail ordering, offset agreement, leaf-count
// arithmetic) under exactly the same rules.
//
// It performs no qual work: the pulled leaves are real leaf items
// now, so there is no link record to build — classifyPulledQuals runs
// once the problem spans exist.
func splicePulledLeaves(pu *jtPullup, nReal int, ctx *resolveContext, scans *[]Node, widths *[]int, semiAnti *[]semiAntiChainLink, outer *[]outerChainLink, onQuals *[]chainOnQual) bool {
	if pu == nil || pu.nLeaves == 0 {
		return true
	}
	if pu.base != nReal {
		// The pull-up numbered the pulled leaf items against the
		// joinlist's relation count at pull-up time; the seam's own
		// real-leaf count must agree — a mismatch is a numbering
		// desync, not a shape to plan around.
		return false
	}
	// realTotal is the walk-order base of the first non-real leaf:
	// every position before it is a real prefix leaf. The pulled
	// arm's qual rebase assumes the emitting space is exactly
	// [0, len(ctx.schema)) — a real leaf that does not emit (a
	// demoted-ANTI right side mid-chain is the production shape)
	// would leave a hole the pulled refs could land in.
	realTotal := 0
	for i := 0; i < nReal && i < len(*widths); i++ {
		realTotal += (*widths)[i]
	}
	if realTotal != len(ctx.schema) {
		return false
	}
	// A pulled leaf at index >= maxSearchRels has no RelSet bit — the
	// seam's own overflow check catches it one step later anyway, but
	// declining here keeps every shift below well-formed.
	if len(*scans)+pu.nLeaves > maxSearchRels {
		return false
	}
	var pulledScans []Node
	var pulledWidths []int
	for _, pb := range pu.bodies {
		pulledScans = append(pulledScans, pb.leafScans...)
		pulledWidths = append(pulledWidths, pb.leafWidths...)
	}
	merged := make([]Node, 0, len(*scans)+pu.nLeaves)
	merged = append(merged, (*scans)[:nReal]...)
	merged = append(merged, pulledScans...)
	merged = append(merged, (*scans)[nReal:]...)
	*scans = merged
	mw := make([]int, 0, len(*widths)+pu.nLeaves)
	mw = append(mw, (*widths)[:nReal]...)
	mw = append(mw, pulledWidths...)
	mw = append(mw, (*widths)[nReal:]...)
	*widths = mw
	// Every chain-extracted leaf index at-or-above nReal moves up by
	// nLeaves: the extracted links' hands, their SpecialJoinInfo
	// fields (renumbered to real leaf bits during the walk), the
	// outer links' sides, and the inner ON quals' belowNullable.
	shift := func(rs RelSet) RelSet { return shiftRelSetAbove(rs, nReal, pu.nLeaves) }
	for i := range *semiAnti {
		lk := &(*semiAnti)[i]
		lk.lhs, lk.rhs = shift(lk.lhs), shift(lk.rhs)
		if lk.sjinfo != nil {
			lk.sjinfo.SynLefthand = shift(lk.sjinfo.SynLefthand)
			lk.sjinfo.SynRighthand = shift(lk.sjinfo.SynRighthand)
			lk.sjinfo.MinLefthand = shift(lk.sjinfo.MinLefthand)
			lk.sjinfo.MinRighthand = shift(lk.sjinfo.MinRighthand)
		}
	}
	for i := range *outer {
		(*outer)[i].preserved = shift((*outer)[i].preserved)
		(*outer)[i].nullable = shift((*outer)[i].nullable)
	}
	for i := range *onQuals {
		(*onQuals)[i].belowNullable = shift((*onQuals)[i].belowNullable)
	}
	return true
}

// shiftRelSetAbove moves every leaf-index bit at-or-above base up by
// delta, leaving lower bits in place — the pulled leaves splice in
// ahead of the extracted (synthetic) ones, so an extracted leaf index
// ≥ base lands at base+delta.
func shiftRelSetAbove(rs RelSet, base, delta int) RelSet {
	if rs == 0 || delta == 0 {
		return rs
	}
	below := rs & leafRangeRelSet(0, base)
	above := rs &^ leafRangeRelSet(0, base)
	return below | (above << uint(delta))
}

// classifyPulledQuals rebases each pulled body's bound quals into
// problem space and classifies them — the work that used to
// build a link record, now feeding the plain conjunct pools
// directly since the pulled leaves are real searched items:
// correlation (cross-side) conjuncts and body-local conjuncts both
// join the caller's `searchQuals` pool — the partition lands the
// former on the semijoin's join clause and the latter inside the RHS,
// exactly distribute_qual_to_rels's placement — and outer-local
// conjuncts (SEMI only) hoist to pu.outerQuals. An outer-local
// restriction under an ANTI join cannot be hoisted:
// `NOT EXISTS(... WHERE outer.x = 5)` must keep the rows where
// outer.x != 5, so it declines instead. Each body's SpecialJoinInfo
// is appended to ctx.joinInfoList — the leaf items it constrains are
// real joinlist members, so no link record survives.
//
// spans is the problem's own leaf-span table — pulled leaves sit at
// [pu.base, pu.base+nLeaves) in it, the positions splicePulledLeaves
// assigned and the joinlist items name.
func classifyPulledQuals(pu *jtPullup, nReal int, spans []leafSpan, ctx *resolveContext, searchQuals *[]Expr) bool {
	if pu == nil || pu.nLeaves == 0 {
		return true
	}
	emittingBits := leafRangeRelSet(0, nReal)
	emittingTotal := len(ctx.schema)
	pos := nReal
	for _, pb := range pu.bodies {
		n := len(pb.leafScans)
		rhs := leafRangeRelSet(pos, pos+n)
		pullSpans := spans[pos : pos+n]
		var spanning []Expr
		for _, q := range pb.quals {
			rebased, ok := rebasePulledQual(q, pb, pullSpans, emittingTotal, ctx)
			if !ok {
				notePullupClassify("rebase-failed")
				return false
			}
			rs, attributable := relidsOfExpr(rebased, spans)
			if !attributable || rs == 0 {
				notePullupClassify("unattributable")
				return false
			}
			switch {
			case relsOverlap(rs, emittingBits) && relsOverlap(rs, rhs):
				spanning = append(spanning, rebased)
			case relsSubset(rs, rhs):
				// A qual confined to the pulled body's own rels is placed by
				// one of TWO mechanisms downstream, and the choice is decided
				// by how many rels it touches:
				//
				//   - two or more: it is a JOIN clause, and
				//     `buildRestrictInfos` files it as a restrictInfo — which
				//     is exactly what `searchConsumes` tests for;
				//   - exactly one: it is a BASE restriction, and
				//     `partitionConjunctsForJoinPlanning` (which runs on this
				//     very conjunct pool, right after this function returns)
				//     routes it to `locals.byBinding[leaf]`, where the seam's
				//     pulled-leaf loop wraps the leaf in a LeafLocal `*Filter`
				//     and prices it through `estimateBaseRelInfo`.
				//
				// Requiring `searchConsumes` for BOTH was the M0145-0003
				// defect root-caused on 2026-09-21: `buildRestrictInfos`'
				// `add` drops every clause with `relLevel < 2` by design, so a
				// single-rel body qual could never satisfy it and the whole
				// body was refused. Since `pulled` has already suppressed the
				// legacy pre-DP route by then, the statement lost its semijoin
				// from both routes — TPC-H Q4's `l_commitdate < l_receiptdate`
				// cost it a 10x regression on the jointree arm.
				//
				// PG has no equivalent step: `distribute_qual_to_rels`
				// (initsplan.c) puts a single-rel qual on that rel's
				// `baserestrictinfo` and the pull-up proceeds.
				if relLevel(rs) >= 2 && !searchConsumes(rebased, spans) {
					notePullupClassify("body-join-qual-not-consumable")
					return false
				}
				*searchQuals = append(*searchQuals, rebased)
			case relsSubset(rs, emittingBits):
				if pb.jointype == parser.JoinAnti {
					notePullupClassify("anti-outer-local-qual")
					return false
				}
				pu.outerQuals = append(pu.outerQuals, rebased)
			default:
				notePullupClassify("qual-spans-neither")
				return false
			}
		}
		// A pulled body with no correlation clause left is the
		// link-pred-nil decline the semiAntiChainLink arm enforced —
		// uncorrelated bodies never reach here by the pull-up's own
		// Level-1 gate, so this is defensive, not a new class.
		if len(spanning) == 0 {
			notePullupClassify("no-spanning-qual")
			return false
		}
		for _, c := range spanning {
			if !searchConsumes(c, spans) {
				return false
			}
		}
		*searchQuals = append(*searchQuals, spanning...)
		if sj := pulledSemiJoinInfo(pb.jointype, rhs, emittingBits, spanning, spans); sj != nil && !joinInfoListHas(ctx.joinInfoList, sj) {
			ctx.joinInfoList = append(ctx.joinInfoList, sj)
		}
		pos += n
	}
	return true
}

// pulledSemiJoinInfo builds the SpecialJoinInfo for a pulled semi/anti
// link — make_outerjoininfo's SEMI arm (initsplan.c:1774-1806) plus
// compute_semijoin_info's unique-ification fields (initsplan.c:2040+):
//
//	syn_lefthand  = every emitting rel (the syntactic left scope)
//	syn_righthand = the body's leaves
//	min_lefthand  = clause relids ∩ left
//	min_righthand = the whole body — its own leaves are inner-joined,
//	                so PG's inner_join_rels union covers them all
//	lhs_strict    = a spanning clause exists (same approximation
//	                existsUnnestSJInfo makes for params)
//	semi_rhs_exprs / can_btree / can_hash = from the AND'ed equality
//	clauses, mirroring inUnnestSJInfo's population.
func pulledSemiJoinInfo(jointype parser.JoinType, rhs, emittingBits RelSet, spanning []Expr, allSpans []leafSpan) *SpecialJoinInfo {
	sj := &SpecialJoinInfo{
		Jointype:     jointype,
		SynLefthand:  emittingBits,
		SynRighthand: rhs,
		MinRighthand: rhs,
		LhsStrict:    len(spanning) > 0,
	}
	for _, q := range spanning {
		if rs, ok := relidsOfExpr(q, allSpans); ok {
			sj.MinLefthand |= rs & emittingBits
		}
	}
	if jointype != parser.JoinSemi {
		return sj
	}
	// compute_semijoin_info: AND'ed equality clauses with RHS vars on
	// exactly one side feed the unique-ification lists; anything else
	// leaves the semijoin un-unique-ifiable (the flags stay false —
	// PG's `return` arm, not a failure).
	rhsExprs := make([]Expr, 0, len(spanning))
	allEq := len(spanning) > 0
	for _, q := range spanning {
		b, isBin := q.(*BinaryOp)
		if !isBin || b.Op != parser.OpEq {
			allEq = false
			continue
		}
		leftRs, lok := relidsOfExpr(b.Left, allSpans)
		rightRs, rok := relidsOfExpr(b.Right, allSpans)
		switch {
		case rok && relsSubset(rightRs, rhs) && (!lok || !relsOverlap(leftRs, rhs)):
			rhsExprs = append(rhsExprs, b.Right)
		case lok && relsSubset(leftRs, rhs) && (!rok || !relsOverlap(rightRs, rhs)):
			rhsExprs = append(rhsExprs, b.Left)
		default:
			allEq = false
		}
	}
	if allEq {
		sj.SemiCanBtree = true
		sj.SemiCanHash = true
		sj.SemiRhsExprs = rhsExprs
	}
	return sj
}

// rebasePulledQual moves one bound body conjunct into problem space:
// `*ColumnRef` indexes shift from the body's own leaf-concat
// coordinates to the pulled leaves' positions (the body's local
// cumulative offsets, read off bodyBindings, map onto pullSpans), and
// `*OuterColumnRef{Level:1}` — a reference into the parent's emitting
// space — becomes a plain `*ColumnRef` at the same flat position,
// while deeper outer refs decrement one level (PG's
// `IncrementVarSublevelsUp(.., -1, 1)`).
//
// The Level-1 index must land inside the emitting space
// (`emittingTotal`) — the parent has no other columns at this stage —
// and, when both sides carry a source identity, the parent binding at
// that position must agree (the self-join disambiguation
// M0071-0009 introduced).
func rebasePulledQual(q Expr, pb *jtPulledBody, pullSpans []leafSpan, emittingTotal int, ctx *resolveContext) (Expr, bool) {
	failed := false
	out, ok := cloneExprRefs(q, scopeVeto, exprRewriter{
		Rewrite: func(x Expr) Expr {
			switch r := x.(type) {
			case *ColumnRef:
				leaf, local, found := bodyLeafOf(pb, r.Index)
				if !found {
					failed = true
					return x
				}
				r.Index = pullSpans[leaf].lo + local
				return x
			case *OuterColumnRef:
				if r.Level > 1 {
					r.Level--
					return x
				}
				if r.Index < 0 || r.Index >= emittingTotal {
					failed = true
					return x
				}
				if b, okB := emittingBindingAt(ctx, r.Index); !okB ||
					(b.sourceIdx != 0 && r.SourceTableIdx != 0 && b.sourceIdx != r.SourceTableIdx) {
					failed = true
					return x
				}
				return &ColumnRef{pos: r.pos, Index: r.Index, Name: r.Name, Type: r.Type, SourceTableIdx: r.SourceTableIdx}
			}
			return x
		},
	})
	if !ok || failed {
		return nil, false
	}
	return out, true
}

// bodyLeafOf maps a body-local flat column index to its leaf and the
// intra-leaf offset, reading the cumulative offsets off the body's own
// bindings.
func bodyLeafOf(pb *jtPulledBody, idx int) (leaf int, local int, found bool) {
	for i, b := range pb.bodyBindings {
		if idx >= b.offset && idx < b.offset+pb.leafWidths[i] {
			return i, idx - b.offset, true
		}
	}
	return 0, 0, false
}

// emittingBindingAt returns the parent binding covering flat emitting
// position idx, reading widths off the contiguous binding offsets
// (the pulled arm is only reachable when the emitting space is
// contiguous — splicePulledLeaves checks realTotal ==
// emittingTotal first).
func emittingBindingAt(ctx *resolveContext, idx int) (rangeBinding, bool) {
	for i, b := range ctx.bindings {
		hi := len(ctx.schema)
		if i+1 < len(ctx.bindings) {
			hi = ctx.bindings[i+1].offset
		}
		if idx >= b.offset && idx < hi {
			return b, true
		}
	}
	return rangeBinding{}, false
}

// exprHasOuterRefAtLevel reports whether e contains an OuterColumnRef
// at exactly `level` hops from the current scope.
func exprHasOuterRefAtLevel(e Expr, level int) bool {
	if e == nil {
		return false
	}
	found := false
	ok := walkExprRefs(e, scopeIgnore, exprVisitor{
		Visit: func(n Expr) bool {
			if o, isOuter := n.(*OuterColumnRef); isOuter && o.Level == level {
				found = true
				return false
			}
			return true
		},
	})
	return found || !ok
}

// exprListHasOuterRefAtLevel is exprHasOuterRefAtLevel over a slice.
func exprListHasOuterRefAtLevel(es []Expr, level int) bool {
	for _, e := range es {
		if exprHasOuterRefAtLevel(e, level) {
			return true
		}
	}
	return false
}

// exprListHasSublinkPlan is exprHasSublinkPlan over a slice.
func exprListHasSublinkPlan(es []Expr) bool {
	for _, e := range es {
		if exprHasSublinkPlan(e) {
			return true
		}
	}
	return false
}

// exprListHasLocalAndLevel1Ref reports whether any expression in es
// carries both a same-scope `*ColumnRef` (a body-local column, in the
// bound body's own space) and an `*OuterColumnRef{Level:1}` (a parent
// reference) — the shape a pulled conjunct needs to become the
// semi/anti link predicate after rebase.
func exprListHasLocalAndLevel1Ref(es []Expr) bool {
	for _, e := range es {
		hasLocal, hasOuter := false, false
		ok := walkExprRefs(e, scopeIgnore, exprVisitor{
			Visit: func(n Expr) bool {
				switch r := n.(type) {
				case *ColumnRef:
					hasLocal = true
				case *OuterColumnRef:
					if r.Level == 1 {
						hasOuter = true
					}
				}
				return !(hasLocal && hasOuter)
			},
		})
		if ok && hasLocal && hasOuter {
			return true
		}
	}
	return false
}

// pullupVolatileBuiltins mirrors the executor's volatileBuiltins
// (internal/executor/subplan.go:87-93) — the builtin names whose result
// can change within one statement, PG's provolatile 'v'. User routines
// resolve through the catalog below; names in neither place are plain
// builtins and treated as non-volatile, matching subPlanExprVolatile's
// resolution order (refusing to decline on every unknown builtin would
// disable pull-up wholesale, the opposite of PG's explicit markings).
var pullupVolatileBuiltins = map[string]bool{
	"random": true, "setseed": true,
	"nextval": true, "currval": true, "lastval": true, "setval": true,
	"clock_timestamp": true, "timeofday": true,
	"gen_random_uuid": true, "gen_random_bytes": true, "uuid_generate_v4": true,
	"pg_sleep": true, "txid_current": true, "pg_notify": true,
}

// exprListHasVolatileBuiltin reports whether any expression in es calls
// a volatile builtin or a volatile registered routine — the planner-side
// half of `contain_volatile_functions` for the pull-up gate. Registry
// volatility uses the same fields subPlanExprVolatile reads
// (Volatile == "v", or "" which PG treats as VOLATILE); a catalog
// lookup miss is non-volatile, mirroring that predicate's miss arm.
func exprListHasVolatileBuiltin(es []Expr, cat catalog.Catalog) bool {
	for _, e := range es {
		volatile := false
		walkExprTree(e, func(x Expr) {
			f, isCall := x.(*FuncCall)
			if !isCall {
				return
			}
			name := strings.ToLower(f.Name)
			if pullupVolatileBuiltins[name] {
				volatile = true
				return
			}
			if cat == nil {
				return
			}
			if rs := cat.Routines(); rs != nil {
				for _, r := range rs.LookupByName(parser.ObjectName{Name: name}) {
					// Any overload volatile (or unmarked — the
					// registry default, which PG treats as
					// VOLATILE) taints the name, same as
					// subPlanExprVolatile.
					if r.Volatile == "v" || r.Volatile == "" {
						volatile = true
						return
					}
				}
			}
		})
		if volatile {
			return true
		}
	}
	return false
}

// splitAndExcludingPulled is splitAnd(pred) minus the conjuncts the
// pull-up consumed. tryPGShapedJoinSearch builds its WHERE conjunct
// pool from this list so a pulled sublink is never fed to the residual
// Filter beside the semi/anti join that replaced it.
func splitAndExcludingPulled(pred Expr, pu *jtPullup) []Expr {
	conjuncts := splitAnd(pred)
	if pu == nil || len(pu.pulled) == 0 {
		return conjuncts
	}
	out := conjuncts[:0]
	for _, c := range conjuncts {
		if pu.pulled[c] {
			continue
		}
		out = append(out, c)
	}
	return out
}

// anyPullupConjunct recognises the conjunct shape the ANY arm converts: a
// bare `*InExpr` over a retained subquery body, in its PLAIN EQUALITY form.
//
// Two refusals are the point:
//
//   - `NOT IN` (`Negated`) is NOT converted, and this is PG's rule, not a
//     goopg limitation. `NOT IN` is `<> ALL`, an ALL_SUBLink, and
//     `pull_up_sublinks_qual_recurse` converts ANY and EXISTS only — the
//     three-valued NULL semantics of `<> ALL` are not those of an anti-join
//     (a single NULL on the inner side makes the whole predicate NULL, where
//     an anti-join would emit the row). goopg's LEGACY unnest does convert
//     `NOT IN`; that divergence is pre-existing and ledgered, and this arm
//     does not extend it to the new pipeline.
//   - `= ANY`-with-an-operator (`AnyOp`), `ALL`, and `x <> ANY(...)`
//     (`NotEqualAny`) are refused by `inExprIsPlainEquality`, the same gate
//     the legacy unnest uses: the link predicate this arm synthesises is an
//     equality, so a non-equality ANY would be a different predicate.
func anyPullupConjunct(c Expr) (*InExpr, bool) {
	in, ok := c.(*InExpr)
	if !ok || in.Subquery == nil || in.Operand == nil {
		return nil, false
	}
	if in.Negated || !inExprIsPlainEquality(in) {
		return nil, false
	}
	return in, true
}

// pullUpAnyBody is `convert_ANY_sublink_to_join` (subselect.c:1333) at
// goopg's seam granularity: the body becomes semi/anti leaf entries exactly
// as an EXISTS body does, and the SubLink's testexpr becomes the join clause.
//
// # Why the join clause is synthesised rather than lifted
//
// An EXISTS body carries its correlation in its own WHERE, so the pull-up
// hands those conjuncts through unchanged and the seam finds the spanning
// one. An ANY body need not be correlated at all: `x IN (SELECT y FROM t)`
// has no cross-scope reference anywhere in the body — the correlation IS the
// testexpr, which lives in the OUTER qual. So this arm builds the missing
// conjunct, `outerOperand = bodyTarget`, in the same space the EXISTS body
// quals use: body-local columns as plain `*ColumnRef`, outer columns as
// `*OuterColumnRef{Level: 1}`. `rebasePulledQual` then rebases both halves,
// `classifyPulledQuals` sees a qual spanning the emitting rels and the body's
// rels, and files it as the link predicate — the same path the EXISTS arm's
// correlation conjunct takes. Nothing downstream learns that one of its
// inputs was synthesised.
//
// # The gates
//
// PG's own list (subselect.c:1345-1386): the sub-select may not reference
// parent rels outside `available_rels`, the testexpr must reference the
// parent (else the conversion is pointless), and the testexpr must be
// non-volatile. goopg's seam adds the body-shape gate every pulled body
// passes (`sublinkBodyIsSimple`) and one of its own: the body's target list
// must be a single non-star expression, because the synthesised equality
// needs exactly one body column to bind to.
func pullUpAnyBody(in *InExpr, parent *resolveContext, cat catalog.Catalog, ps PlannerSettings) (*jtPulledBody, string, bool) {
	sub := in.Subquery
	if !sublinkBodyIsSimple(sub) {
		return nil, "any-body-not-simple", false
	}
	if len(sub.Targets) != 1 || sub.Targets[0].Expr == nil {
		return nil, "any-target-not-single", false
	}
	// `contain_volatile_functions(sublink->testexpr)` (subselect.c:1385):
	// once the testexpr becomes a join clause its evaluation count is the
	// join's, not once per outer row.
	if exprListHasVolatileBuiltin([]Expr{in.Operand}, cat) {
		return nil, "any-volatile-testexpr", false
	}
	// A sublink inside the operand would have to be planned in a scope this
	// arm is about to move; refuse rather than reason about it.
	if exprHasSublinkPlan(in.Operand) {
		return nil, "any-nested-sublink-operand", false
	}
	bodyCtx, leafScans, leafWidths, onQuals, why, ok := bindPulledBodyScope(sub, parent, cat, ps)
	if !ok {
		return nil, "any-" + why, false
	}
	for _, q := range onQuals {
		if exprHasOuterRefAtLevel(q, 1) {
			return nil, "any-on-qual-parent-ref", false
		}
	}
	var where Expr
	if sub.Where != nil {
		var err error
		where, err = resolveExpr(sub.Where, bodyCtx)
		if err != nil {
			return nil, "any-where-not-resolvable", false
		}
	}
	if exprHasSublinkPlan(where) || exprListHasSublinkPlan(onQuals) {
		return nil, "any-nested-sublink", false
	}
	quals := append(splitAnd(where), onQuals...)
	if exprListHasVolatileBuiltin(quals, cat) {
		return nil, "any-volatile-qual", false
	}
	// The body target, in body-local coordinates.
	target, err := resolveExpr(sub.Targets[0].Expr, bodyCtx)
	if err != nil {
		return nil, "any-target-not-resolvable", false
	}
	outer, okOuter := outerOperandAsLevel1(in.Operand)
	if !okOuter {
		return nil, "any-operand-not-outer-columns", false
	}
	link := &BinaryOp{Op: parser.OpEq, Left: outer, Right: target}
	return &jtPulledBody{
		expr:         in,
		jointype:     parser.JoinSemi,
		leafScans:    leafScans,
		leafWidths:   leafWidths,
		bodyBindings: bodyCtx.bindings,
		quals:        append([]Expr{link}, quals...),
	}, "", true
}

// outerOperandAsLevel1 re-expresses an outer-scope operand in the body's
// reference space: every `*ColumnRef` becomes `*OuterColumnRef{Level: 1}`,
// which is what a correlated body WHERE would have contained if the user had
// written the equality inside the subquery. `rebasePulledQual` validates each
// one against the emitting bindings and converts it back, so a wrong index
// fails closed there rather than reading the wrong column here.
//
// It refuses an operand that already carries an `*OuterColumnRef`: that is a
// reference to a scope ABOVE this statement, and shifting its level is a
// different rebase than the one `rebasePulledQual` performs on the way out.
func outerOperandAsLevel1(operand Expr) (Expr, bool) {
	refused := false
	saw := false
	out, ok := cloneExprRefs(operand, scopeVeto, exprRewriter{
		Rewrite: func(x Expr) Expr {
			switch r := x.(type) {
			case *ColumnRef:
				saw = true
				return &OuterColumnRef{
					pos: r.pos, Level: 1, Index: r.Index,
					Name: r.Name, Type: r.Type, SourceTableIdx: r.SourceTableIdx,
				}
			case *OuterColumnRef:
				refused = true
				return x
			}
			return x
		},
	})
	if !ok || refused || !saw {
		return nil, false
	}
	return out, true
}
