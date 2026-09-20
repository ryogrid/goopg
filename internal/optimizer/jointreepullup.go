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
// sublink's join type. `integratePulledSublinks` — called inside
// tryPGShapedJoinSearch once the emitting prefix's real leaf count is
// known — assigns positions, rebases the quals into the problem's
// column space, and emits `semiAntiChainLink` entries: the same
// record the chain walk produces, so the entire problem tail (qual
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
	// outer.x != 5 rows — so buildPulledSemiAntiLink declines instead
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
}

// jtPulledBody is one pulled sublink's bound material in BODY-LOCAL
// coordinates. The leaf scans are the nodes planFromItem produced for
// the body's own FROM items — real plan nodes with real catalog
// identity — and the quals are the body's bound conjuncts, in which a
// `*ColumnRef` indexes the body's own leaf-concat space and an
// `*OuterColumnRef{Level:1}` indexes the PARENT statement's emitting
// space. integratePulledSublinks performs the coordinate rebase;
// until then nothing in this record names a parent-space position.
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
		ex, negated, ok := existsPullupConjunct(c)
		if !ok || ex.Subquery == nil {
			continue
		}
		body, ok := pullUpExistsBody(ex, negated, ctx, cat, ps)
		if !ok {
			continue
		}
		if pu == nil {
			pu = &jtPullup{pulled: make(map[Expr]bool)}
		}
		pu.bodies = append(pu.bodies, body)
		pu.pulled[c] = true
	}
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
func pullUpExistsBody(ex *ExistsExpr, negated bool, parent *resolveContext, cat catalog.Catalog, ps PlannerSettings) (*jtPulledBody, bool) {
	sub := ex.Subquery
	if !sublinkBodyIsSimple(sub) {
		return nil, false
	}
	bodyCtx, leafScans, leafWidths, onQuals, ok := bindPulledBodyScope(sub, parent, cat, ps)
	if !ok {
		return nil, false
	}
	for _, q := range onQuals {
		// The rest of the subselect must not refer to the parent
		// (subselect.c:1502). Level >= 2 is fine — it refers above the
		// parent and decrements into place at integration.
		if exprHasOuterRefAtLevel(q, 1) {
			return nil, false
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
			return nil, false
		}
	}
	if exprHasSublinkPlan(where) || exprListHasSublinkPlan(onQuals) {
		return nil, false
	}
	if exprListHasVolatileBuiltin(append(splitAnd(where), onQuals...), cat) {
		return nil, false
	}
	quals := append(splitAnd(where), onQuals...)
	// `contain_vars_of_level(whereClause, 1)` — the correlation must
	// live in the WHERE, and it must exist (subselect.c:1509).
	if !exprListHasOuterRefAtLevel(quals, 1) {
		return nil, false
	}
	// The WHERE must also carry a conjunct that can actually become
	// the link predicate — one reading a body-local column AND a
	// Level-1 parent reference together. Without it the seam's
	// semiAntiOnQualsOK gate declines on the nil pred anyway, but by
	// then `pulled` has already suppressed the pre-DP arm for every
	// other sublink in this WHERE; declining here instead leaves the
	// statement exactly where the legacy pipeline would find it.
	if !exprListHasLocalAndLevel1Ref(splitAnd(where)) {
		return nil, false
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
	}, true
}

// bindPulledBodyScope builds the provisional resolve scope for a pulled
// body: the same planFromClause the body's own planSelect would run,
// chained to the parent context so correlation resolves through
// `ctx.parent` exactly as it did when the body's plan was built. The
// body's jointree must come back as a flat inner/cross chain of bare
// scans — one leaf per binding — or the body is not splicable.
func bindPulledBodyScope(sub *parser.SelectStmt, parent *resolveContext, cat catalog.Catalog, ps PlannerSettings) (*resolveContext, []Node, []int, []Expr, bool) {
	node, bodyCtx, err := planFromClause(sub, cat, ps, parent.rtScope)
	if err != nil || bodyCtx == nil {
		return nil, nil, nil, nil, false
	}
	bodyCtx.cat = cat
	bodyCtx.parent = parent
	bodyCtx.settings = ps
	leafScans, onQuals, ok := flattenPulledBodyTree(node, len(bodyCtx.bindings))
	if !ok {
		return nil, nil, nil, nil, false
	}
	widths := make([]int, len(leafScans))
	for i, l := range leafScans {
		widths[i] = len(l.Output())
	}
	return bodyCtx, leafScans, widths, onQuals, true
}

// flattenPulledBodyTree decomposes the body's provisional jointree into
// its leaf scans plus the conjuncts of its inner-join ON clauses. Any
// non-inner link, any leaf that is not a bare *SeqScan (a demoted outer
// join, a derived item, an already-rewritten access path), or a leaf
// count that disagrees with the binding count fails the whole body —
// the flat splice has no leaf to stand in for any of those.
func flattenPulledBodyTree(node Node, wantLeaves int) ([]Node, []Expr, bool) {
	var leaves []Node
	var quals []Expr
	var walk func(n Node) bool
	walk = func(n Node) bool {
		j, isJoin := n.(*Join)
		if !isJoin {
			leaves = append(leaves, n)
			return true
		}
		if j.Type != JoinTypeInner && j.Type != JoinTypeCross {
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
	if !walk(node) || len(leaves) != wantLeaves {
		return nil, nil, false
	}
	for _, l := range leaves {
		if _, isScan := l.(*SeqScan); !isScan {
			return nil, nil, false
		}
	}
	return leaves, quals, true
}

// integratePulledSublinks assigns the pulled bodies their leaf
// positions inside the search problem — directly after the real
// prefix leaves and any synthetic leaves the chain walk extracted —
// rebases their bound quals into the problem's column space, and
// appends the resulting semi/anti links and leaf entries to the
// tables tryPGShapedJoinSearch is about to consume.
//
// It is called after `extractSearchLeaves`, before the synthetic-leaf
// accounting, so the pulled leaves participate in every check the
// walk-derived leaves do (tail ordering, offset agreement, leaf-count
// arithmetic) under exactly the same rules.
//
// Coordinate recap: for a pulled leaf at problem position p, both its
// walk-order-flat base and its out-of-band span base are
// `sum(widths[0:p])` — the two spaces coincide at the tail because no
// real leaf follows a synthetic one. The quals are therefore produced
// directly in the final space; the seam's own walk-order→spans remap
// treats them as already-correct.
//
// emittingTotal is the width of the emitting (real-prefix) space —
// len(ctx.schema) — the boundary an OuterColumnRef{Level:1} must not
// cross.
func integratePulledSublinks(pu *jtPullup, nprefix int, ctx *resolveContext, scans *[]Node, widths *[]int, semiAnti *[]semiAntiChainLink) bool {
	if pu == nil || len(pu.bodies) == 0 {
		return true
	}
	emittingTotal := len(ctx.schema)
	nExtracted := 0
	for _, lk := range *semiAnti {
		for r := lk.rhs; r != 0; r >>= 1 {
			nExtracted += int(r & 1)
		}
	}
	// realTotal is the walk-order base of the first non-real leaf:
	// every position before it is a real prefix leaf. On the pulled
	// arm those bindings are cumulative over the emitting space, so
	// realTotal must equal emittingTotal — a real leaf that does not
	// emit (a demoted-ANTI right side is the production shape) would
	// leave a hole in the flat space the pulled refs could land in.
	realTotal := 0
	for i := 0; i < nprefix && i < len(*widths); i++ {
		realTotal += (*widths)[i]
	}
	if realTotal != emittingTotal {
		return false
	}
	// Build the spans array relidsOfExpr attributes against: emitting
	// leaves at their binding offsets, extracted-synthetic positions
	// left zero-width (pulled quals can never reference them — a ref
	// that somehow does fails attribution and declines), pulled
	// leaves at their problem positions.
	totalPulled := 0
	for _, pb := range pu.bodies {
		totalPulled += len(pb.leafScans)
	}
	// A pulled leaf at index >= maxSearchRels has no RelSet bit — the
	// seam's own overflow check would catch it one step later anyway,
	// but declining here keeps every link built below well-formed.
	if nprefix+nExtracted+totalPulled > maxSearchRels {
		return false
	}
	allSpans := make([]leafSpan, nprefix+nExtracted+totalPulled)
	// Emitting leaves take cumulative-width spans — the same
	// construction buildLeafSpans performs on the real positions — so
	// the relid attribution below reads exactly the space the problem
	// tail will use.
	lo := 0
	for i := 0; i < nprefix && i < len(*widths); i++ {
		allSpans[i] = leafSpan{lo: lo, hi: lo + (*widths)[i]}
		lo += (*widths)[i]
	}
	emittingBits := leafRangeRelSet(0, nprefix)
	pos := nprefix + nExtracted
	walkBase := realTotal
	for i := nprefix; i < nprefix+nExtracted && i < len(*widths); i++ {
		walkBase += (*widths)[i]
	}
	for _, pb := range pu.bodies {
		n := len(pb.leafScans)
		for i, w := range pb.leafWidths {
			allSpans[pos+i] = leafSpan{lo: walkBase, hi: walkBase + w}
			walkBase += w
		}
		rhs := leafRangeRelSet(pos, pos+n)
		link, outerQuals, ok := buildPulledSemiAntiLink(pb, rhs, emittingBits, pos, allSpans, ctx)
		if !ok {
			return false
		}
		pu.outerQuals = append(pu.outerQuals, outerQuals...)
		*semiAnti = append(*semiAnti, link)
		*scans = append(*scans, pb.leafScans...)
		*widths = append(*widths, pb.leafWidths...)
		pos += n
	}
	return true
}

// buildPulledSemiAntiLink rebases one pulled body's bound quals into
// problem space and emits its semiAntiChainLink: correlation
// (cross-side) conjuncts become the link predicate, body-local
// conjuncts the pooled bodyQuals, and outer-local conjuncts — for a
// SEMI join only — join the pooled list as well, where the conjunct
// partition lands them on the emitting leaves they read. An
// outer-local restriction under an ANTI join cannot be hoisted:
// `NOT EXISTS(... WHERE outer.x = 5)` must keep the rows where
// outer.x != 5, so it declines the pull-up instead.
func buildPulledSemiAntiLink(pb *jtPulledBody, rhs, emittingBits RelSet, pos int, allSpans []leafSpan, ctx *resolveContext) (semiAntiChainLink, []Expr, bool) {
	pullSpans := allSpans[pos : pos+len(pb.leafScans)]
	var spanning, pooled, outerLocal []Expr
	for _, q := range pb.quals {
		rebased, ok := rebasePulledQual(q, pb, pullSpans, len(ctx.schema), ctx)
		if !ok {
			return semiAntiChainLink{}, nil, false
		}
		rs, attributable := relidsOfExpr(rebased, allSpans)
		if !attributable || rs == 0 {
			return semiAntiChainLink{}, nil, false
		}
		switch {
		case relsOverlap(rs, emittingBits) && relsOverlap(rs, rhs):
			spanning = append(spanning, rebased)
		case relsSubset(rs, rhs):
			pooled = append(pooled, rebased)
		case relsSubset(rs, emittingBits):
			if pb.jointype == parser.JoinAnti {
				return semiAntiChainLink{}, nil, false
			}
			outerLocal = append(outerLocal, rebased)
		default:
			return semiAntiChainLink{}, nil, false
		}
	}
	link := semiAntiChainLink{
		jointype:  pb.jointype,
		lhs:       emittingBits,
		rhs:       rhs,
		pred:      combineAnd(spanning),
		flattened: true,
		bodyQuals: pooled,
	}
	link.sjinfo = pulledSemiJoinInfo(pb.jointype, rhs, emittingBits, spanning, allSpans)
	return link, outerLocal, true
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
// contiguous — integratePulledSublinks checks realTotal ==
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
