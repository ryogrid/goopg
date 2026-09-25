package optimizer

import (
	"os"
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
	// children are nested sublinks pulled out of THIS body's own quals —
	// PG's `pull_up_sublinks_qual_recurse` re-run on the pulled-up quals
	// (postgres/src/backend/optimizer/prep/prepjointree.c:682-693, :736-747,
	// NOT arm :836-845). Each child's conjunct has been REMOVED from
	// `quals`: its semantics now live in the child's own semi/anti link.
	children []*jtPulledBody
	// srcOffset is added to every SourceTableIdx the body's leaves and
	// rebased refs carry (M0145-0030). The body's scope numbers its tables
	// from 1 like the outer query does, so without it a self-correlated
	// EXISTS put two SourceTableIdx=1 tables in one plan and EXPLAIN
	// (explain_names.go, first-wins per SourceTableIdx) printed
	// `t1.a = t1.a`. The legacy unnest applies the same shift through
	// remapSourceTableIdx.
	srcOffset int16
	// parent is the body this one was pulled out of, nil for a top-level
	// body. It gives a nested body's Level-1 outer references a coordinate
	// path: they point at the PARENT BODY's columns, not at the emitting
	// rels. Set by `flattenPulledBodies`.
	parent *jtPulledBody
	// subtreeLeaves is this body's own leaf count PLUS every descendant's.
	// It is the body's `syn_righthand`, and the distinction matters: PG
	// splices a nested conversion into the parent's `j->rarg`, so the
	// parent's right-hand side contains the nested body's rels too
	// (`j->rarg = pull_up_sublinks_jointree_recurse(...)` returning
	// `child_rels`, prepjointree.c:682-693). Without that, the search may
	// complete the parent semijoin FIRST — discarding the parent body's
	// columns, since a semijoin projects only its left side — and then try
	// to evaluate the nested link qual above it, which has nowhere to read
	// the parent column from. `TestJointreePullupDeclineParity/nested-exists`
	// is the witness for exactly that plan.
	subtreeLeaves int
	// derived marks a body pulled as ONE opaque leaf — the whole body's
	// plan, PG's un-flattened subquery RTE (M0145-0008aa) — rather than as
	// its own base-relation leaves. The seam admits that leaf without a
	// catalog table and prices it from its plan (`seamLeafBinding`).
	derived bool
	// distinct records, for a derived body, PG's `query_is_distinct_for`
	// on its one output column (M0145-0008ab): the leaf's unique-ified path
	// is then the leaf itself (create_unique_path's UNIQUE_PATH_NOOP).
	distinct bool
	// uniqueOutput is PG's `isunique` for the body's one output column
	// (`subqueryOutputIsUnique`), which the join selectivity reads through
	// baseRelInfo.subqueryUniqueOutput (M0145-0008ab).
	uniqueOutput bool
}

// maxPulledSublinkDepth bounds the nested pull-up recursion. PG has no
// explicit limit, but goopg appends leaves to ONE problem whose relation count
// is capped (`maxSearchRels`), so an unbounded nest builds a problem the seam
// then declines wholesale.
const maxPulledSublinkDepth = 3

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
			body, reason, okBody := pullUpAnyBody(in, ctx, cat, ps, 0)
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
			// M0145-0015: report the sublink kind AND the clause position.
			// The remedy is decided by the position — PG recurses through
			// AND and NOT but stops at every other clause type, OR args
			// included — so `ExistsExpr@or` is a correct decline and
			// `ExistsExpr@top` would be a real miss.
			if site := sublinkConjunctSite(c); site != "" {
				notePullupDecline(site)
			}
			continue
		}
		body, reason, ok := pullUpExistsBody(ex, negated, ctx, cat, ps, 0)
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
	// M0145-0014: flatten the pull-up TREE into the flat body list the seam
	// walks. Depth-first, parent immediately before its children, because
	// every downstream consumer assumes body order IS leaf order.
	pu.bodies = flattenPulledBodies(pu.bodies, nil)
	if !assignPulledSourceOffsets(pu.bodies, ctx) {
		return nil
	}
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

// assignPulledSourceOffsets gives every pulled body a SourceTableIdx range of
// its own, past everything the emitting scope and the earlier bodies use, and
// shifts the body's leaf scans (schemas and embedded refs, via
// remapSourceTableIdx — the legacy unnest's own tool for this) and binding
// records into it. rebasePulledQual adds the same offset to the refs it
// rebases, so a body's scans and the quals naming them keep agreeing. Returns
// false when a leaf cannot be remapped, which declines the pull-up.
func assignPulledSourceOffsets(bodies []*jtPulledBody, ctx *resolveContext) bool {
	next := int16(0)
	for _, c := range ctx.schema {
		if c.SourceTableIdx > next {
			next = c.SourceTableIdx
		}
	}
	for _, b := range ctx.bindings {
		if b.sourceIdx > next {
			next = b.sourceIdx
		}
	}
	for _, pb := range bodies {
		bodyMax := int16(0)
		for _, b := range pb.bodyBindings {
			if b.sourceIdx > bodyMax {
				bodyMax = b.sourceIdx
			}
		}
		for _, leaf := range pb.leafScans {
			for _, c := range leaf.Output() {
				if c.SourceTableIdx > bodyMax {
					bodyMax = c.SourceTableIdx
				}
			}
		}
		pb.srcOffset = next
		if pb.derived {
			// M0145-0008aa: a derived leaf is a whole planned body, and
			// remapSourceTableIdx covers only the node kinds the legacy
			// unnest met (no Distinct, no set operations). The shift is
			// EXPLAIN-naming hygiene, not a value dependency — the leaf is its
			// own scope and the link qual reads it by position — so a body
			// the remap cannot walk keeps its own numbering (offset 0), which
			// is what the post-hoc route does with the same plan.
			if shifted, err := remapSourceTableIdx(pb.leafScans[0], next); err == nil && shifted != nil {
				pb.leafScans[0] = shifted
			} else {
				pb.srcOffset = 0
				continue
			}
		} else {
			for i, leaf := range pb.leafScans {
				shifted, err := remapSourceTableIdx(leaf, next)
				if err != nil || shifted == nil {
					return false
				}
				pb.leafScans[i] = shifted
			}
		}
		for i := range pb.bodyBindings {
			if pb.bodyBindings[i].sourceIdx != 0 {
				pb.bodyBindings[i].sourceIdx += next
			}
		}
		next += bodyMax
	}
	return true
}

// flattenPulledBodies linearises the pull-up tree depth-first, stamping each
// body's `parent` on the way down and clearing `children` so there is exactly
// ONE representation downstream.
func flattenPulledBodies(bodies []*jtPulledBody, parent *jtPulledBody) []*jtPulledBody {
	out := make([]*jtPulledBody, 0, len(bodies))
	for _, pb := range bodies {
		pb.parent = parent
		kids := pb.children
		pb.children = nil
		out = append(out, pb)
		sub := flattenPulledBodies(kids, pb)
		pb.subtreeLeaves = len(pb.leafScans)
		for _, k := range sub {
			pb.subtreeLeaves += len(k.leafScans)
		}
		out = append(out, sub...)
	}
	return out
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
func pullUpExistsBody(ex *ExistsExpr, negated bool, parent *resolveContext, cat catalog.Catalog, ps PlannerSettings, depth int) (*jtPulledBody, string, bool) {
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
	quals := append(splitAnd(where), onQuals...)
	quals, children := extractNestedPullups(quals, bodyCtx, cat, ps, depth)
	if why, ok := bodyQualsAdmitSublinkList(quals); !ok {
		return nil, why, false
	}
	if exprListHasVolatileBuiltin(quals, cat) {
		return nil, "volatile-qual", false
	}
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
		children:     children,
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

// pullupCTELeafEnabled gates M0145-0011 scope (c) / E2: admitting a
// `*CTEScan` leaf into the flat splice `flattenPulledBodyTree` performs,
// instead of declining the whole body with `body-leaf-(*optimizer.CTEScan)`.
//
// Default OFF. It is knob-arm measurement apparatus, not a relaxation:
// M0145-0011 lands none, and the default arm must stay byte-identical.
//
// At landing it was useless on its own and had to be paired with
// `GOOPG_DERIVED_FIREWALL=off`: a pulled ANY/EXISTS body becomes a
// JoinSemi/JoinAnti SpecialJoinInfo, and the `outer-over-derived` firewall's
// jointype switch covered Semi/Anti, so a body admitted here declined one
// step later at the firewall. M0145-0018 removed that firewall, so the
// pairing requirement is gone and this flag stands alone.
//
// Why a CTE leaf is the interesting case: the baseline census over TPC-DS
// makes `any-body-leaf-(*optimizer.CTEScan)` the largest non-`(pulled)`
// decline class (30 fires), and every one of those is an ANY sublink whose
// body FROM is a WITH reference — a derived input with no catalog statistics,
// which is exactly the population M0145-0011 exists to re-measure rather than
// argue about.
//
// MEASURED RESULT, and it is the reason this flag must not be read as a
// relaxation that works (E2, loop 41, TPC-DS SF0.25, both flags on):
//
//	pull-up census   any-body-leaf-(*optimizer.CTEScan) 30 -> 0, (pulled) 42 -> 72
//	seam census      leaf-count -> pulled-leaf-not-scan, same statements
//
// Every body this gate admits is declined ONE STEP LATER by the seam's own
// leaf-kind check (`pulled-leaf-not-scan`, joinsearchseam.go), whose comment
// names THIS function as its guarantor. The bare-`*SeqScan` rule is a
// TWO-SITE invariant — a producer and a consumer — so relaxing it here alone
// cannot put a CTE leaf into the DP. What it does instead is let `pulled`
// suppress the legacy pre-DP arm while the seam still declines, which is the
// exact shape that cost TPC-H Q4 a 10x regression; Q14/Q23/Q95 moved plans
// for that reason and not because the search found anything (values
// byte-identical, runtimes flat-to-slightly-worse at 13.1->14.5 s,
// 15.0->15.5 s, 3.0->3.2 s, against ESTIMATED costs that fell 1.4x-2.4x).
//
// So the resume point for scope (c) is the seam, not this line: the pulled
// leaf binding at joinsearchseam.go needs a `rangeBinding` for a leaf with no
// `Table`/`Alias`, plus an `estimateBaseRelInfo`/`applyRelSizeFallback` arm
// for a statistics-less leaf. That is a real piece of work, not a gate flip —
// filed as M0145-0013 (owner directive 2026-09-21).
var pullupCTELeafEnabled = os.Getenv("GOOPG_PULLUP_CTE_LEAF") == "on"

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
		if _, isScan := l.(*SeqScan); isScan {
			continue
		}
		if pullupCTELeafEnabled {
			if _, isCTE := l.(*CTEScan); isCTE {
				continue
			}
		}
		// The leaf kind is named because it decides the remedy: a
		// *Filter over a scan needs unwrapping, a derived item needs
		// the opaque-body arm, a rewritten access path needs neither.
		return nil, nil, "body-leaf-" + nliProbeIndexName(l), false
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
func splicePulledLeaves(pu *jtPullup, nReal int, nprefix int, ctx *resolveContext, scans *[]Node, widths *[]int, semiAnti *[]semiAntiChainLink, outer *[]outerChainLink, onQuals *[]chainOnQual) bool {
	if pu == nil || pu.nLeaves == 0 {
		return true
	}
	if pu.base != nprefix-pu.nLeaves {
		// The pull-up numbered the pulled leaf items against the
		// joinlist's relation count at pull-up time; the pulled band is
		// always the problem's LAST leaves (deferred chain semi/anti
		// items sit below it, M0145-0005 slice 6) — a mismatch is a
		// numbering desync, not a shape to plan around.
		return false
	}
	pos := pu.base
	if pos < nReal || pos > len(*scans) {
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
	merged = append(merged, (*scans)[:pos]...)
	merged = append(merged, pulledScans...)
	merged = append(merged, (*scans)[pos:]...)
	*scans = merged
	mw := make([]int, 0, len(*widths)+pu.nLeaves)
	mw = append(mw, (*widths)[:pos]...)
	mw = append(mw, pulledWidths...)
	mw = append(mw, (*widths)[pos:]...)
	*widths = mw
	// Every chain-extracted leaf index at-or-above the splice point moves
	// up by nLeaves: the extracted links' hands, their SpecialJoinInfo
	// fields (renumbered to real leaf bits during the walk), the
	// outer links' sides, and the inner ON quals' belowNullable.
	// Deferred chain semi/anti items (realLeaf links) sit BELOW pos —
	// their indices are unaffected.
	shift := func(rs RelSet) RelSet { return shiftRelSetAbove(rs, pos, pu.nLeaves) }
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
	// M0145-0014: a NESTED body's left-hand side is the emitting rels PLUS
	// every ancestor body's leaves, because the link predicate the nested
	// pull-up synthesised reads a column of its PARENT body. Both maps fill
	// as the walk goes, which is sound because `flattenPulledBodies` emits a
	// parent before its children.
	bodyBase := map[*jtPulledBody]int{}
	bodyN := map[*jtPulledBody]int{}
	baseOf := func(b *jtPulledBody) (int, bool) {
		v, ok := bodyBase[b]
		return v, ok
	}
	// The pulled band starts at pu.base — after the deferred chain
	// semi/anti items, not at nReal (M0145-0005 slice 6). A pulled qual
	// whose relids touch that band falls through the switch's default arm
	// (`qual-spans-neither`) — the band's rels are non-emitting and cannot
	// be named from a sublink body, so the decline is defensive.
	pos := pu.base
	for _, pb := range pu.bodies {
		n := len(pb.leafScans)
		rhs := leafRangeRelSet(pos, pos+n)
		pullSpans := spans[pos : pos+n]
		bodyBase[pb] = pos
		bodyN[pb] = n
		leftBits := emittingBits
		for anc := pb.parent; anc != nil; anc = anc.parent {
			ab, okA := bodyBase[anc]
			an, okN := bodyN[anc]
			if !okA || !okN {
				notePullupClassify("ancestor-not-numbered")
				return false
			}
			leftBits |= leafRangeRelSet(ab, ab+an)
		}
		var spanning []Expr
		for _, q := range pb.quals {
			rebased, ok := rebasePulledQual(q, pb, pullSpans, emittingTotal, ctx, spans, baseOf)
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
			case relsOverlap(rs, leftBits) && relsOverlap(rs, rhs):
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
			case relsSubset(rs, leftBits):
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
		// syn_righthand is the body's whole SUBTREE, not just its own
		// leaves — see jtPulledBody.subtreeLeaves.
		sjRhs := leafRangeRelSet(pos, pos+pb.subtreeLeaves)
		sjLeft := leftBits
		if pb.parent != nil {
			// PG splices a nested conversion into the PARENT's `j->rarg` and
			// recurses the quals with `available_rels = child_rels`
			// (prepjointree.c:682-693), so the child semijoin is ordered
			// INSIDE the parent's right-hand side.
			sjLeft = leftBits &^ emittingBits
		}
		// M0145-0008ae: `reduce_unique_semijoins` — a semijoin to a single
		// rel that is unique for the join clauses gets no SpecialJoinInfo,
		// so the search plans it as the inner join it is.
		if pulledSemiRhsIsUnique(pb, sjRhs, spanning, spans, ctx.cat) {
			notePullupClassify("semijoin-reduced-to-inner")
			pos += n
			continue
		}
		if sj := pulledSemiJoinInfo(pb.jointype, sjRhs, sjLeft, spanning, spans); sj != nil && !joinInfoListHas(ctx.joinInfoList, sj) {
			// M0145-0008ab: a derived ANY_subquery proven distinct on its
			// output unique-ifies for free — see SpecialJoinInfo.SemiRhsDistinct.
			sj.SemiRhsDistinct = pb.derived && pb.distinct && pb.subtreeLeaves == 1
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
		// The spanning quals are rebased into problem space, so the RHS
		// operands are problem-space column refs, not positions in the RHS
		// leaf's own output.
		sj.SemiRhsProblemSpace = true
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
func rebasePulledQual(q Expr, pb *jtPulledBody, pullSpans []leafSpan, emittingTotal int, ctx *resolveContext, allSpans []leafSpan, bodyBase func(*jtPulledBody) (int, bool)) (Expr, bool) {
	failed := false
	// scopeSignal, not scopeVeto (M0145-0014): a body qual may legitimately
	// carry a SubPlan — PG's `pull_up_sublinks_qual_recurse` converts the
	// OUTER sublink first and leaves a non-convertible nested one as a
	// SubPlan inside the pulled-up qual. Under `scopeVeto` `cloneExprRefs`
	// ABORTS the moment it meets an inner-plan slot, which made every such
	// qual fail with `rebase-failed` one step after `bodyQualsAdmitSublinks`
	// let it through — a decline that merely moved.
	//
	// `scopeSignal` reports the crossing and does not descend, which is the
	// correct treatment: the subplan's own refs live in the subplan's scope
	// and are not the body-local coordinates this rebase re-stamps. The one
	// case that would be wrong is a subplan CORRELATED to the body, whose
	// outer refs do point at coordinates moving underneath it — OnScope
	// declines those rather than guessing.
	out, ok := cloneExprRefs(q, scopeSignal, exprRewriter{
		OnScope: func(n Node) {
			if planHasOuterRef(n) {
				noteRebaseFail("subplan-correlated")
				failed = true
			}
		},
		OnUnknown: func(x Expr) {
			// M0145-0014: name the node the rewrite aborted on. Without it
			// `rebase-failed` is one string for three different causes and
			// the next loop re-derives which one fired.
			noteRebaseFail("unknown-" + exprTypeName(x))
		},
		Rewrite: func(x Expr) Expr {
			switch r := x.(type) {
			case *ColumnRef:
				leaf, local, found := bodyLeafOf(pb, r.Index)
				if !found {
					noteRebaseFail("body-column-not-in-leaf")
					failed = true
					return x
				}
				r.Index = pullSpans[leaf].lo + local
				if r.SourceTableIdx != 0 {
					r.SourceTableIdx += pb.srcOffset
				}
				return x
			case *OuterColumnRef:
				// M0145-0014: walk `r.Level` steps up the pulled-body chain.
				// Landing on a body means the reference points at a PARENT
				// BODY's columns; running off the top means it points at the
				// emitting scope, which is what Level 1 always meant for a
				// top-level body.
				anc := pb
				hops := 0
				for hops < r.Level && anc != nil {
					anc = anc.parent
					hops++
				}
				if anc != nil {
					base, okBase := bodyBase(anc)
					leaf, local, found := bodyLeafOf(anc, r.Index)
					if !okBase || !found {
						noteRebaseFail("ancestor-column-not-in-leaf")
						failed = true
						return x
					}
					src := r.SourceTableIdx
					if src != 0 {
						src += anc.srcOffset
					}
					return &ColumnRef{
						pos: r.pos, Index: allSpans[base+leaf].lo + local,
						Name: r.Name, Type: r.Type, SourceTableIdx: src,
					}
				}
				// Off the top after `hops` steps: the emitting scope sits
				// one hop above the topmost body, i.e. `hops` hops from pb
				// (M0146-0015a). Whatever remains points above the
				// emitting statement and stays an outer reference, re-
				// levelled relative to it. Decrementing by ONE, as this
				// did, is right only for a top-level body (hops == 1): a
				// nested body's grandparent reference (Level 2 from a
				// depth-1 body) stayed `OuterColumnRef{Level:1}` instead of
				// becoming an emitting-scope column — invisible while the
				// qual stayed in the join residual (the statement fell back
				// to SubPlans), an "out of range (depth=0)" execution error
				// once it reached a leaf.
				if rem := r.Level - hops; rem > 0 {
					r.Level = rem
					return x
				}
				// A NESTED body (hops > 1) reading the emitting scope makes
				// the qual a join clause between an emitting rel and a rel
				// inside the parent body's semi/anti RHS. PG plans that (the
				// clause joins across the nested semi join), but goopg's
				// search cannot place it — createPlan panics re-basing it
				// ("join clause references binding column … not among the
				// output columns"). Decline, so the statement keeps the
				// SubPlans it always got for this shape (ledgered as
				// M0146-0015c).
				if hops > 1 {
					noteRebaseFail("nested-body-emitting-ref")
					failed = true
					return x
				}
				if r.Index < 0 || r.Index >= emittingTotal {
					noteRebaseFail("outer-index-out-of-range")
					failed = true
					return x
				}
				if b, okB := emittingBindingAt(ctx, r.Index); !okB ||
					(b.sourceIdx != 0 && r.SourceTableIdx != 0 && b.sourceIdx != r.SourceTableIdx) {
					noteRebaseFail("outer-binding-mismatch")
					failed = true
					return x
				}
				return &ColumnRef{pos: r.pos, Index: r.Index, Name: r.Name, Type: r.Type, SourceTableIdx: r.SourceTableIdx}
			}
			return x
		},
	})
	if !ok || failed {
		if !ok && !failed {
			noteRebaseFail("clone-aborted")
		}
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

// bodyQualsAdmitSublinks decides whether a pulled body's own quals may carry a
// sublink, and names the refusal when they may not.
//
// It replaces a blanket `exprHasSublinkPlan` refusal at BOTH pull-up arms —
// the EXISTS arm and the ANY arm, which held the identical gate. That refusal
// was over-broad against the oracle. PG converts the OUTER sublink first and
// only then looks at what the body's quals contain:
// `pull_up_sublinks_qual_recurse` calls `convert_ANY_sublink_to_join` /
// `convert_EXISTS_sublink_to_join`, whose gates are about correlation and
// volatility (`postgres/src/backend/optimizer/plan/subselect.c:1345-1386`) and
// say nothing about the body's quals, and THEN recurses on the pulled-up quals
// (`prepjointree.c:682-693`, `:736-747`, NOT arm `:836-845`). A nested sublink
// that is not itself convertible simply stays a SubPlan inside the pulled-up
// qual; it never blocks the outer conversion.
//
// So the population splits in two, and this function admits only the half that
// needs no new machinery:
//
//   - a NON-convertible sublink (a scalar/EXPR subquery) rides along as an
//     ordinary body-local qual, exactly as it does in PG. TPC-DS Q58's
//     `d_date IN (SELECT d_date … WHERE d_week_seq = (SELECT …))` is this
//     shape: a one-leaf body whose single qual holds an uncorrelated scalar
//     subplan.
//   - a CONVERTIBLE nested ANY/EXISTS is still declined, because converting it
//     is the recursion M0145-0014 is named for and that needs the nested
//     body's leaves spliced and its link predicate rebased against the OUTER
//     body's leaves rather than against the emitting rels. TPC-DS Q83's
//     `d_date IN (SELECT d_date … WHERE d_week_seq IN (SELECT …))` is this
//     shape.
//
// A body qual that carries BOTH a sublink and a Level-1 outer reference is
// declined too: the correlation rebase that `rebasePulledQual` performs on the
// way out is not defined over a subplan's own scope, and guessing there is how
// a pull-up reads the wrong column.
func bodyQualsAdmitSublinks(where Expr, onQuals []Expr) (string, bool) {
	return bodyQualsAdmitSublinkList(append(splitAnd(where), onQuals...))
}

// bodyQualsAdmitSublinkList is the split-conjunct form, which is what both
// arms hold once `extractNestedPullups` has rewritten the list.
func bodyQualsAdmitSublinkList(quals []Expr) (string, bool) {
	for _, q := range quals {
		if !exprHasSublinkPlan(q) {
			continue
		}
		if exprHasConvertibleSublink(q) {
			return "nested-sublink-convertible", false
		}
		if exprHasOuterRefAtLevel(q, 1) {
			return "nested-sublink-correlated", false
		}
	}
	return "", true
}

// extractNestedPullups is `pull_up_sublinks_qual_recurse` re-run on the
// pulled-up quals: for each of the body's own conjuncts that is itself a
// convertible ANY/EXISTS sublink, pull its body up too and REMOVE the conjunct
// from the parent's qual list, because its semantics now live in the child's
// own semi/anti link. PG does this at prepjointree.c:682-693 (ANY), :736-747
// (EXISTS) and :836-845 (the NOT arm).
func extractNestedPullups(quals []Expr, bodyCtx *resolveContext, cat catalog.Catalog, ps PlannerSettings, depth int) ([]Expr, []*jtPulledBody) {
	if depth+1 >= maxPulledSublinkDepth {
		return quals, nil
	}
	var kept []Expr
	var children []*jtPulledBody
	for _, q := range quals {
		if in, okIn := anyPullupConjunct(q); okIn {
			if child, _, ok := pullUpAnyBody(in, bodyCtx, cat, ps, depth+1); ok {
				children = append(children, child)
				continue
			}
			kept = append(kept, q)
			continue
		}
		if ex, negated, okEx := existsPullupConjunct(q); okEx && ex.Subquery != nil {
			if child, _, ok := pullUpExistsBody(ex, negated, bodyCtx, cat, ps, depth+1); ok {
				children = append(children, child)
				continue
			}
		}
		kept = append(kept, q)
	}
	return kept, children
}

// exprHasConvertibleSublink reports whether e carries a sublink of a kind
// `pull_up_sublinks_qual_recurse` would convert into a jointree entry — an ANY
// (`*InExpr`) or an EXISTS (`*ExistsExpr`). Every other sublink kind stays a
// SubPlan in PG as well, which is what makes it safe to carry.
func exprHasConvertibleSublink(e Expr) bool {
	found := false
	walkExprTree(e, func(x Expr) {
		if found || len(ExprSubplans(x)) == 0 {
			return
		}
		switch x.(type) {
		case *InExpr, *ExistsExpr:
			found = true
		}
	})
	return found
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
func pullUpAnyBody(in *InExpr, parent *resolveContext, cat catalog.Catalog, ps PlannerSettings, depth int) (*jtPulledBody, string, bool) {
	sub := in.Subquery
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
	// M0145-0008aa: PG does not ask whether the body is simple here —
	// convert_ANY_sublink_to_join makes ANY sub-select a subquery RTE, and
	// only pull_up_subqueries later decides whether to flatten it. A body
	// goopg cannot flatten therefore enters the search as that subquery RTE:
	// one derived leaf.
	if !sublinkBodyIsSimple(sub) {
		return pullUpAnyDerivedBody(in, parent, cat, ps)
	}
	if len(sub.Targets) != 1 || sub.Targets[0].Expr == nil {
		return nil, "any-target-not-single", false
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
	quals := append(splitAnd(where), onQuals...)
	quals, children := extractNestedPullups(quals, bodyCtx, cat, ps, depth)
	if why, ok := bodyQualsAdmitSublinkList(quals); !ok {
		return nil, "any-" + why, false
	}
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
		children:     children,
	}, "", true
}

// anyDerivedAlias is the range-table alias PG gives the subquery RTE it
// builds for a pulled-up ANY sublink (`makeAlias("ANY_subquery", NIL)`,
// postgres/src/backend/optimizer/plan/subselect.c:1395).
const anyDerivedAlias = "ANY_subquery"

// pullUpAnyDerivedBody is the half of `convert_ANY_sublink_to_join` that
// `pullUpAnyBody`'s flat splice cannot express: a body that is not simple
// (grouped, HAVING, DISTINCT, set operations, LIMIT, its own WITH) becomes
// ONE semi-side leaf carrying the body's whole plan — the subquery RTE PG
// appends to the parent's range table
// (postgres/src/backend/optimizer/plan/subselect.c:1390-1401), which
// `pull_up_subqueries` then leaves in place because `is_simple_subquery`
// refuses it (postgres/src/backend/optimizer/prep/prepjointree.c:1143).
//
// The body is planned exactly as a FROM-clause subquery is — goopg's
// subquery RTE — through `planFromClause` over a one-item FROM list
// `(<body>) AS ANY_subquery`. The testexpr becomes `operand =
// ANY_subquery.col0`, synthesised the same way the flat arm does, so the
// seam files it as the link predicate and the search may unique-ify the
// leaf into an inner join (`create_unique_path`).
//
// One of PG's cases is refused: a CORRELATED body. PG marks that RTE
// LATERAL (`use_lateral`, subselect.c:1357) and the search then needs a
// parameterised path for it, which goopg's search does not build for a
// derived leaf (the M0145-0010 family). The statement keeps the post-hoc
// route instead, exactly as before this arm existed.
func pullUpAnyDerivedBody(in *InExpr, parent *resolveContext, cat catalog.Catalog, ps PlannerSettings) (*jtPulledBody, string, bool) {
	wrap := &parser.SelectStmt{FromExprs: []parser.FromExpr{{
		Base: parser.RangeVar{Subquery: in.Subquery, Alias: anyDerivedAlias},
	}}}
	node, bodyCtx, err := planFromClause(wrap, cat, ps, parent.rtScope)
	if err != nil || bodyCtx == nil || len(bodyCtx.bindings) != 1 {
		return nil, "any-derived-not-plannable", false
	}
	if _, isJoin := node.(*Join); isJoin {
		return nil, "any-derived-not-one-leaf", false
	}
	out := node.Output()
	if len(out) != 1 {
		return nil, "any-target-not-single", false
	}
	if planHasEscapingOuterRef(node, 1) {
		return nil, "any-derived-body-correlated", false
	}
	bodyCtx.cat = cat
	bodyCtx.parent = parent
	bodyCtx.settings = ps
	outer, okOuter := outerOperandAsLevel1(in.Operand)
	if !okOuter {
		return nil, "any-operand-not-outer-columns", false
	}
	target := &ColumnRef{Index: 0, Name: out[0].Name, Type: out[0].Type, SourceTableIdx: out[0].SourceTableIdx}
	link := &BinaryOp{Op: parser.OpEq, Left: outer, Right: target}
	return &jtPulledBody{
		expr:         in,
		jointype:     parser.JoinSemi,
		leafScans:    []Node{node},
		leafWidths:   []int{len(out)},
		bodyBindings: bodyCtx.bindings,
		quals:        []Expr{link},
		derived:      true,
		distinct:     queryIsDistinctForFirstColumn(in.Subquery, cat),
		uniqueOutput: subqueryOutputIsUnique(in.Subquery),
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
